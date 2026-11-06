package valkey

import (
	"context"
	"strconv"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

// StreamGroup is the consumer group preload creates on every stream.
const StreamGroup = "plgv"

// compositeFn runs a multi-step operation and returns its outcome and the key
// it was routed by (for per-node accounting).
type compositeFn func(ctx context.Context, c Client, g *Gen, it config.MixItem) (Result, string)

var composites = map[string]compositeFn{
	"TXN": runTxn, "PIPELINE": runPipeline, "WATCH_INCR": runWatchIncr, "XCONSUME": runXConsume,
}

// --- Gen as a raw-command data source ---

// Key returns a random key of a type ("string", "hash", ...).
func (g *Gen) Key(typ string) string { return g.key(typeNS[typ]) }

// Value returns a random value of the dataset's value size.
func (g *Gen) Value() string { return g.vals.Next(g.r) }

// Int returns a random integer in [lo, hi].
func (g *Gen) Int(lo, hi int64) int64 { return lo + g.r.Int63n(hi-lo+1) }

// N returns a random key index.
func (g *Gen) N() int64 { return g.keys.Next() }

// NowMillis returns the current Unix time in milliseconds.
func (g *Gen) NowMillis() int64 { return time.Now().UnixMilli() }

// --- helpers ---

func timeoutOf(it config.MixItem) time.Duration {
	if d, err := time.ParseDuration(it.Timeout); err == nil && d > 0 {
		return d
	}
	return time.Second
}

func secondsArg(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

func (g *Gen) consumer() string { return "c" + strconv.Itoa(g.Worker) }

func (g *Gen) lon() string { return strconv.FormatFloat(-10+g.r.Float64()*20, 'f', 5, 64) }
func (g *Gen) lat() string { return strconv.FormatFloat(40+g.r.Float64()*10, 'f', 5, 64) }

// ChannelName names pub/sub channel i. Sharded channels carry a hash tag per
// channel so they spread over the cluster's shards.
func ChannelName(prefix string, i int, sharded bool) string {
	if sharded {
		return prefix + "ch{" + strconv.Itoa(i) + "}"
	}
	return prefix + "ch:" + strconv.Itoa(i)
}

// publishPayload starts with the send time in nanoseconds so a subscriber can
// measure delivery latency, then is padded to the value size.
func (g *Gen) publishPayload() string {
	ts := strconv.FormatInt(time.Now().UnixNano(), 10) + "|"
	if pad := g.d.ValueSize - len(ts); pad > 0 {
		return ts + g.vals.Next(g.r)[:min(pad, g.d.ValueSize)]
	}
	return ts
}

func (g *Gen) scriptKeyArgs(it config.MixItem) (key string, args []string) {
	_, ns := scriptSource(it.Script, it.Source)
	key = g.key(ns)
	switch {
	case len(it.Args) > 0:
		args = it.Args
	case it.Script == "get_set":
		args = []string{g.vals.Next(g.r)}
	default:
		args = scriptArgs[it.Script]
	}
	return key, args
}

func init() {
	add := func(name string, b builder) { builders[name] = b }

	// blocking lists
	add("BLPOP", func(g *Gen, it config.MixItem) Cmd { return Cmd{"BLPOP", g.key(nsList), secondsArg(timeoutOf(it))} })
	add("BRPOP", func(g *Gen, it config.MixItem) Cmd { return Cmd{"BRPOP", g.key(nsList), secondsArg(timeoutOf(it))} })

	// streams
	add("XADD", func(g *Gen, it config.MixItem) Cmd {
		n := orDefault(it.Fields, 2)
		c := make(Cmd, 0, 5+2*n)
		c = append(c, "XADD", g.key(nsStream), "MAXLEN", "~", strconv.Itoa(max(g.d.StreamLength*10, 1000)), "*")
		for i := 0; i < n; i++ {
			c = append(c, "f"+strconv.Itoa(i), g.vals.Next(g.r))
		}
		return c
	})
	add("XLEN", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"XLEN", g.key(nsStream)} })
	add("XRANGE", func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"XRANGE", g.key(nsStream), "-", "+", "COUNT", strconv.Itoa(orDefault(it.Count, 10))}
	})
	add("XREAD", func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"XREAD", "COUNT", strconv.Itoa(orDefault(it.Count, 10)), "STREAMS", g.key(nsStream), "0"}
	})
	add("XREAD_BLOCK", func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"XREAD", "BLOCK", strconv.FormatInt(timeoutOf(it).Milliseconds(), 10), "COUNT", strconv.Itoa(orDefault(it.Count, 10)), "STREAMS", g.key(nsStream), "$"}
	})
	add("XTRIM", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"XTRIM", g.key(nsStream), "MAXLEN", "~", strconv.Itoa(max(g.d.StreamLength, 1))}
	})
	add("XPENDING", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"XPENDING", g.key(nsStream), StreamGroup} })
	add("XREADGROUP", func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"XREADGROUP", "GROUP", StreamGroup, g.consumer(), "COUNT", strconv.Itoa(orDefault(it.Count, 10)), "STREAMS", g.key(nsStream), ">"}
	})

	// geo
	add("GEOADD", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"GEOADD", g.key(nsGeo), g.lon(), g.lat(), g.member()} })
	add("GEOPOS", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"GEOPOS", g.key(nsGeo), g.member()} })
	add("GEODIST", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"GEODIST", g.key(nsGeo), g.member(), g.member(), "km"} })
	add("GEOSEARCH", func(g *Gen, it config.MixItem) Cmd {
		return Cmd{"GEOSEARCH", g.key(nsGeo), "FROMLONLAT", g.lon(), g.lat(), "BYRADIUS", "200", "km", "ASC", "COUNT", strconv.Itoa(orDefault(it.Count, 10))}
	})

	// HyperLogLog
	add("PFADD", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"PFADD", g.key(nsHLL), "e" + strconv.Itoa(g.r.Intn(1_000_000))}
	})
	add("PFCOUNT", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"PFCOUNT", g.key(nsHLL)} })

	// bitmaps
	add("SETBIT", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"SETBIT", g.key(nsBitmap), strconv.Itoa(g.r.Intn(g.d.BitmapBits)), strconv.Itoa(g.r.Intn(2))}
	})
	add("GETBIT", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"GETBIT", g.key(nsBitmap), strconv.Itoa(g.r.Intn(g.d.BitmapBits))}
	})
	add("BITCOUNT", func(g *Gen, _ config.MixItem) Cmd { return Cmd{"BITCOUNT", g.key(nsBitmap)} })

	// Lua and functions
	add("EVAL", func(g *Gen, it config.MixItem) Cmd {
		body, _ := scriptSource(it.Script, it.Source)
		key, args := g.scriptKeyArgs(it)
		return append(Cmd{"EVAL", body, "1", key}, args...)
	})
	add("EVALSHA", func(g *Gen, it config.MixItem) Cmd {
		body, _ := scriptSource(it.Script, it.Source)
		key, args := g.scriptKeyArgs(it)
		return append(Cmd{"EVALSHA", sha(body), "1", key}, args...)
	})
	add("FCALL", func(g *Gen, it config.MixItem) Cmd {
		key, args := g.scriptKeyArgs(it)
		return append(Cmd{"FCALL", it.Script, "1", key}, args...)
	})

	// pub/sub
	add("PUBLISH", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"PUBLISH", ChannelName(g.d.KeyPrefix, g.r.Intn(max(g.Channels, 1)), false), g.publishPayload()}
	})
	add("SPUBLISH", func(g *Gen, _ config.MixItem) Cmd {
		return Cmd{"SPUBLISH", ChannelName(g.d.KeyPrefix, g.r.Intn(max(g.Channels, 1)), true), g.publishPayload()}
	})
}

// --- composite operations ---

func firstError(res []Result) error {
	for _, r := range res {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// stepCmds builds the commands of a TXN or PIPELINE.
func stepCmds(g *Gen, steps []config.MixItem) []Cmd {
	cmds := make([]Cmd, 0, len(steps)+2)
	for _, st := range steps {
		cmds = append(cmds, builders[st.Op](g, st))
	}
	return cmds
}

// runTxn queues the steps between MULTI and EXEC. In a cluster the steps' keys
// are drawn from one hash tag group so they share a slot.
func runTxn(ctx context.Context, c Client, g *Gen, it config.MixItem) (Result, string) {
	g.pin()
	defer g.unpin()
	steps := stepCmds(g, it.Steps)
	cmds := make([]Cmd, 0, len(steps)+2)
	cmds = append(cmds, Cmd{"MULTI"})
	cmds = append(cmds, steps...)
	cmds = append(cmds, Cmd{"EXEC"})
	res := c.Exec(ctx, cmds)
	route := ""
	if len(steps) > 0 {
		route = RouteKey(steps[0])
	}
	if err := firstError(res); err != nil {
		return Result{Err: err}, route
	}
	return Result{Miss: res[len(res)-1].Miss}, route // a nil EXEC means the transaction aborted
}

// runPipeline sends the steps together in one round trip (no atomicity).
func runPipeline(ctx context.Context, c Client, g *Gen, it config.MixItem) (Result, string) {
	g.pin()
	defer g.unpin()
	steps := stepCmds(g, it.Steps)
	res := c.Exec(ctx, steps)
	route := ""
	if len(steps) > 0 {
		route = RouteKey(steps[0])
	}
	return Result{Err: firstError(res)}, route
}

// runWatchIncr is an optimistic increment: WATCH a counter, read it, then
// MULTI/SET/EXEC. When another client changed the key in between, EXEC is
// aborted and the operation counts as a miss (a conflict).
func runWatchIncr(ctx context.Context, c Client, g *Gen, it config.MixItem) (Result, string) {
	key := g.key(nsCounter)
	var out Result
	err := c.Dedicated(ctx, func(cn Conn) error {
		if _, err := cn.Query(ctx, Cmd{"WATCH", key}); err != nil {
			return err
		}
		v, err := cn.Query(ctx, Cmd{"GET", key})
		if err != nil {
			return err
		}
		n := int64(0)
		if s, ok := v.(string); ok {
			n, _ = strconv.ParseInt(s, 10, 64)
		}
		res := cn.Exec(ctx, []Cmd{{"MULTI"}, {"SET", key, strconv.FormatInt(n+1, 10)}, {"EXEC"}})
		if e := firstError(res); e != nil {
			return e
		}
		out.Miss = res[len(res)-1].Miss
		return nil
	})
	if err != nil {
		return Result{Err: err}, key
	}
	return out, key
}

// runXConsume reads new entries for this worker's consumer and acknowledges
// the ones it received. Nothing new to read counts as a miss.
func runXConsume(ctx context.Context, c Client, g *Gen, it config.MixItem) (Result, string) {
	key := g.key(nsStream)
	reply, err := c.Query(ctx, Cmd{"XREADGROUP", "GROUP", StreamGroup, g.consumer(), "COUNT", strconv.Itoa(orDefault(it.Count, 10)), "STREAMS", key, ">"})
	if err != nil {
		return Result{Err: err}, key
	}
	ids := StreamIDs(reply)
	if len(ids) == 0 {
		return Result{Miss: true}, key
	}
	res := c.Exec(ctx, []Cmd{append(Cmd{"XACK", key, StreamGroup}, ids...)})
	return res[0], key
}

// StreamIDs extracts the entry ids from an XREAD/XREADGROUP reply, in RESP3
// (map of stream to entries) or RESP2 (array of [stream, entries]) form.
func StreamIDs(reply any) []string {
	var entries []any
	switch r := reply.(type) {
	case map[string]any:
		for _, v := range r {
			if es, ok := v.([]any); ok {
				entries = append(entries, es...)
			}
		}
	case []any:
		for _, s := range r {
			if pair, ok := s.([]any); ok && len(pair) == 2 {
				if es, ok := pair[1].([]any); ok {
					entries = append(entries, es...)
				}
			}
		}
	}
	var ids []string
	for _, e := range entries {
		if ent, ok := e.([]any); ok && len(ent) > 0 {
			if id, ok := ent[0].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
