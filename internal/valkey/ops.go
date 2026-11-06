package valkey

import (
	"context"
	"math/rand"
	"strconv"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyspace"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
)

// Key namespaces. Each type lives under its own prefix so an operation never
// hits a key of the wrong type (no WRONGTYPE errors in a mixed workload).
const (
	nsString  = "s"
	nsCounter = "c" // INCR
	nsAppend  = "a" // APPEND
	nsHash    = "h"
	nsList    = "l"
	nsSet     = "t"
	nsZSet    = "z"
	nsStream  = "x"
	nsGeo     = "g"
	nsHLL     = "p"
	nsBitmap  = "b"
)

var typeNS = map[string]string{
	config.TypeString: nsString, config.TypeHash: nsHash, config.TypeList: nsList,
	config.TypeSet: nsSet, config.TypeZSet: nsZSet, config.TypeStream: nsStream, config.TypeGeo: nsGeo,
	config.TypeHLL: nsHLL, config.TypeBitmap: nsBitmap,
}

// Gen builds commands for one worker. It is not safe for concurrent use.
type Gen struct {
	r    *rand.Rand
	keys keyspace.Generator
	vals *values.Source
	d    config.Dataset

	// Worker is the number of the worker this generator belongs to (used to name
	// stream consumers). Channels is the number of pub/sub channels.
	Worker   int
	Channels int

	// While pinned, every key comes from one hash tag group, so the steps of a
	// transaction share a cluster slot.
	pinned   bool
	pinGroup int64
}

// NewGen creates a command generator. keys must be dedicated to this worker.
func NewGen(r *rand.Rand, keys keyspace.Generator, vals *values.Source, d config.Dataset) *Gen {
	return &Gen{r: r, keys: keys, vals: vals, d: d}
}

// KeyName builds the key for index idx in a namespace. With hash tag groups,
// the group is a {tag} so keys of one group share a cluster slot.
func KeyName(d config.Dataset, ns string, idx int64) string {
	if g := int64(d.HashTagGroups); g > 0 {
		return d.KeyPrefix + "{" + strconv.FormatInt(idx%g, 10) + "}:" + ns + ":" + strconv.FormatInt(idx, 10)
	}
	return d.KeyPrefix + ns + ":" + strconv.FormatInt(idx, 10)
}

func (g *Gen) key(ns string) string {
	if g.pinned {
		grp := int64(g.d.HashTagGroups)
		members := (g.d.KeyCount - g.pinGroup + grp - 1) / grp
		return KeyName(g.d, ns, g.pinGroup+g.r.Int63n(members)*grp)
	}
	return KeyName(g.d, ns, g.keys.Next())
}

// pin makes every key until unpin come from one hash tag group. It does nothing
// without hash tag groups.
func (g *Gen) pin() {
	if grp := int64(g.d.HashTagGroups); grp > 0 {
		g.pinned, g.pinGroup = true, g.keys.Next()%grp
	}
}

func (g *Gen) unpin() { g.pinned = false }

// keys returns n keys for a multi-key command. With hash tag groups they all
// belong to one group (so they share a slot); otherwise they are independent.
func (g *Gen) multiKeys(ns string, n int) []string {
	out := make([]string, 0, n)
	grp := int64(g.d.HashTagGroups)
	if grp <= 0 {
		for i := 0; i < n; i++ {
			out = append(out, g.key(ns))
		}
		return out
	}
	r := g.keys.Next() % grp
	members := (g.d.KeyCount - r + grp - 1) / grp // indexes r, r+grp, ... below key_count
	for i := 0; i < n; i++ {
		out = append(out, KeyName(g.d, ns, r+g.r.Int63n(members)*grp))
	}
	return out
}

func (g *Gen) member() string { return "m" + strconv.Itoa(g.r.Intn(g.d.CollectionSize)) }
func (g *Gen) field() string  { return "f" + strconv.Itoa(g.r.Intn(g.d.HashFields)) }
func (g *Gen) score() string  { return strconv.Itoa(g.r.Intn(1_000_000)) }

// ttl returns a TTL in seconds for a SET when the model selects one.
func (g *Gen) ttl() (int, bool) {
	t := g.d.TTL
	if t.Mode == "none" || (t.Fraction < 1 && g.r.Float64() >= t.Fraction) {
		return 0, false
	}
	return g.ttlSeconds(), true
}

// ttlSeconds is the TTL for explicit expiry ops; it falls back to the model's
// fixed seconds when the mode is none.
func (g *Gen) ttlSeconds() int {
	t := g.d.TTL
	if t.Mode == "random" {
		return t.MinSeconds + g.r.Intn(t.MaxSeconds-t.MinSeconds+1)
	}
	return t.Seconds
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func (g *Gen) typeKey(it config.MixItem) (string, string) {
	t := it.Type
	if t == "" {
		t = config.TypeString
	}
	return g.key(typeNS[t]), typeNS[t]
}

type builder func(g *Gen, it config.MixItem) Cmd

var builders = map[string]builder{
	// strings
	"GET": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"GET", g.key(nsString)} },
	"SET": func(g *Gen, _ config.MixItem) Cmd {
		if s, ok := g.ttl(); ok {
			return Cmd{"SET", g.key(nsString), g.vals.Next(g.r), "EX", strconv.Itoa(s)}
		}
		return Cmd{"SET", g.key(nsString), g.vals.Next(g.r)}
	},
	"MGET": func(g *Gen, it config.MixItem) Cmd {
		n := orDefault(it.Count, 4)
		c := make(Cmd, 0, n+1)
		c = append(c, "MGET")
		return append(c, g.multiKeys(nsString, n)...)
	},
	"MSET": func(g *Gen, it config.MixItem) Cmd {
		n := orDefault(it.Count, 4)
		c := make(Cmd, 0, 2*n+1)
		c = append(c, "MSET")
		for _, k := range g.multiKeys(nsString, n) {
			c = append(c, k, g.vals.Next(g.r))
		}
		return c
	},
	"INCR":   func(g *Gen, _ config.MixItem) Cmd { return Cmd{"INCR", g.key(nsCounter)} },
	"APPEND": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"APPEND", g.key(nsAppend), g.vals.Next(g.r)} },
	"GETEX": func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"GETEX", g.key(nsString), "EX", strconv.Itoa(g.ttlSeconds())}
	},
	"SETEX": func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"SETEX", g.key(nsString), strconv.Itoa(g.ttlSeconds()), g.vals.Next(g.r)}
	},
	// keys
	"DEL":    func(g *Gen, it config.MixItem) Cmd { k, _ := g.typeKey(it); return Cmd{"DEL", k} },
	"UNLINK": func(g *Gen, it config.MixItem) Cmd { k, _ := g.typeKey(it); return Cmd{"UNLINK", k} },
	"EXISTS": func(g *Gen, it config.MixItem) Cmd { k, _ := g.typeKey(it); return Cmd{"EXISTS", k} },
	"TTL":    func(g *Gen, it config.MixItem) Cmd { k, _ := g.typeKey(it); return Cmd{"TTL", k} },
	"EXPIRE": func(g *Gen, it config.MixItem) Cmd {
		k, _ := g.typeKey(it)
		return Cmd{"EXPIRE", k, strconv.Itoa(g.ttlSeconds())}
	},
	"SCAN": func(g *Gen, it config.MixItem) Cmd {
		_, ns := g.typeKey(it)
		match := g.d.KeyPrefix + ns + ":*"
		if g.d.HashTagGroups > 0 {
			match = g.d.KeyPrefix + "*:" + ns + ":*"
		}
		return Cmd{"SCAN", "0", "MATCH", match, "COUNT", strconv.Itoa(orDefault(it.Count, 10))}
	},
	// hashes
	"HSET": func(g *Gen, it config.MixItem) Cmd {
		n := orDefault(it.Fields, 1)
		c := make(Cmd, 0, 2*n+2)
		c = append(c, "HSET", g.key(nsHash))
		for i := 0; i < n; i++ {
			c = append(c, "f"+strconv.Itoa(i), g.vals.Next(g.r))
		}
		return c
	},
	"HGET":    func(g *Gen, _ config.MixItem) Cmd { return Cmd{"HGET", g.key(nsHash), g.field()} },
	"HGETALL": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"HGETALL", g.key(nsHash)} },
	"HDEL":    func(g *Gen, _ config.MixItem) Cmd { return Cmd{"HDEL", g.key(nsHash), g.field()} },
	"HINCRBY": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"HINCRBY", g.key(nsHash), "cnt", "1"} },
	// lists
	"LPUSH": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"LPUSH", g.key(nsList), g.vals.Next(g.r)} },
	"RPUSH": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"RPUSH", g.key(nsList), g.vals.Next(g.r)} },
	"LPOP":  func(g *Gen, _ config.MixItem) Cmd { return Cmd{"LPOP", g.key(nsList)} },
	"RPOP":  func(g *Gen, _ config.MixItem) Cmd { return Cmd{"RPOP", g.key(nsList)} },
	"LRANGE": func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"LRANGE", g.key(nsList), "0", strconv.Itoa(orDefault(it.Count, 10) - 1)}
	},
	"LLEN": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"LLEN", g.key(nsList)} },
	// sets
	"SADD":      func(g *Gen, _ config.MixItem) Cmd { return Cmd{"SADD", g.key(nsSet), g.member()} },
	"SREM":      func(g *Gen, _ config.MixItem) Cmd { return Cmd{"SREM", g.key(nsSet), g.member()} },
	"SISMEMBER": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"SISMEMBER", g.key(nsSet), g.member()} },
	"SCARD":     func(g *Gen, _ config.MixItem) Cmd { return Cmd{"SCARD", g.key(nsSet)} },
	"SRANDMEMBER": func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"SRANDMEMBER", g.key(nsSet), strconv.Itoa(orDefault(it.Count, 1))}
	},
	"SMEMBERS": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"SMEMBERS", g.key(nsSet)} },
	// sorted sets
	"ZADD":    func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZADD", g.key(nsZSet), g.score(), g.member()} },
	"ZREM":    func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZREM", g.key(nsZSet), g.member()} },
	"ZSCORE":  func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZSCORE", g.key(nsZSet), g.member()} },
	"ZINCRBY": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZINCRBY", g.key(nsZSet), "1", g.member()} },
	"ZRANK":   func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZRANK", g.key(nsZSet), g.member()} },
	"ZRANGE": func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"ZRANGE", g.key(nsZSet), "0", strconv.Itoa(orDefault(it.Count, 10) - 1)}
	},
	"ZRANGEBYSCORE": func(g *Gen, it config.MixItem) Cmd {
		lo := g.r.Intn(900_000)
		return Cmd{"ZRANGE", g.key(nsZSet), strconv.Itoa(lo), strconv.Itoa(lo + 100_000), "BYSCORE", "LIMIT", "0", strconv.Itoa(orDefault(it.Count, 10))}
	},
	"ZCARD": func(g *Gen, _ config.MixItem) Cmd { return Cmd{"ZCARD", g.key(nsZSet)} },
}

// Workload is the compiled weighted operation mix.
type Workload struct {
	items []config.MixItem
	cum   []int
	total int
	raw   map[int]*rawcmd.Template // RAW mix entries, parsed once
	bad   map[int]error            // entries that failed to compile (validation normally prevents this)
}

// Compile builds a Workload from a validated mix.
func Compile(mix []config.MixItem) *Workload {
	w := &Workload{items: mix, cum: make([]int, len(mix)), raw: map[int]*rawcmd.Template{}, bad: map[int]error{}}
	for i, it := range mix {
		w.total += it.Weight
		w.cum[i] = w.total
		if it.Op == "RAW" {
			t, err := rawcmd.Parse(it.Command)
			if err != nil {
				w.bad[i] = err
				continue
			}
			w.raw[i] = t
			if _, known := builders[t.Name()]; !known && t.KeyArg > 0 {
				RegisterKeyPosition(t.Name(), t.KeyArg)
			}
		}
	}
	return w
}

// Len is the number of mix entries.
func (w *Workload) Len() int { return len(w.items) }

// Item returns mix entry i.
func (w *Workload) Item(i int) config.MixItem { return w.items[i] }

// Kind says how mix entry i is executed: config.KindSimple (pipelined with the
// rest of the batch), KindSolo (its own call) or KindComposite (several steps).
func (w *Workload) Kind(i int) string { return config.KindOf(w.items[i].Op) }

// Pick selects a mix entry index by weight.
func (w *Workload) Pick(r *rand.Rand) int {
	x := r.Intn(w.total)
	for i, c := range w.cum {
		if x < c {
			return i
		}
	}
	return len(w.cum) - 1
}

// Build creates the command for a simple or solo mix entry i.
func (w *Workload) Build(g *Gen, i int) Cmd {
	it := w.items[i]
	if it.Op == "RAW" {
		if t := w.raw[i]; t != nil {
			return Cmd(t.Expand(g))
		}
		return Cmd{"PING"} // unreachable for a validated configuration
	}
	return builders[it.Op](g, it)
}

// RunComposite executes a composite entry and returns its outcome and the key
// it was routed by (for per-node accounting).
func (w *Workload) RunComposite(ctx context.Context, c Client, g *Gen, i int) (Result, string) {
	it := w.items[i]
	return composites[it.Op](ctx, c, g, it)
}

// DisplayName is how a mix entry appears in results. Most are their operation
// name; a raw command shows its text and a transaction or pipeline group shows
// its steps, so entries of the same kind can be told apart.
func DisplayName(it config.MixItem) string {
	switch it.Op {
	case "RAW":
		c := strings.Join(strings.Fields(it.Command), " ")
		if len(c) > 48 {
			c = c[:45] + "..."
		}
		return "RAW " + c
	case "TXN", "PIPELINE":
		steps := make([]string, len(it.Steps))
		for i, st := range it.Steps {
			steps[i] = st.Op
		}
		return it.Op + "[" + strings.Join(steps, ",") + "]"
	}
	return it.Op
}
