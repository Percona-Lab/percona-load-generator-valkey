package valkey

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyspace"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
)

func newTestGen(t *testing.T, d config.Dataset, seed int64) *Gen {
	t.Helper()
	kg, err := keyspace.New(d.KeyDistribution, d.KeyCount, seed, d.ZipfS)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := values.NewSource(d.ValueSize, seed)
	if err != nil {
		t.Fatal(err)
	}
	return NewGen(rand.New(rand.NewSource(seed)), kg, vs, d)
}

// Every op in the config catalog has a builder and the other way around.
func TestCatalogMatchesBuilders(t *testing.T) {
	for name, info := range config.Ops {
		switch {
		case info.Kind == config.KindComposite:
			if composites[name] == nil {
				t.Errorf("composite op %s has no implementation", name)
			}
		case name == "RAW":
			// built from the template at compile time
		case builders[name] == nil:
			t.Errorf("config op %s has no builder", name)
		}
	}
	for name := range composites {
		if config.Ops[name].Kind != config.KindComposite {
			t.Errorf("%s is implemented as composite but not marked composite in the catalog", name)
		}
	}
	for name := range builders {
		if _, ok := config.Ops[name]; !ok {
			t.Errorf("builder %s is not in the config catalog", name)
		}
	}
}

// Each op family: command name, key namespace and argument shape.
func TestOpShapes(t *testing.T) {
	d := config.Default().Dataset
	d.KeyPrefix = "p:"
	d.TTL = config.TTL{Mode: "fixed", Seconds: 99, Fraction: 1}
	cases := []struct {
		item   config.MixItem
		ns     string // namespace of the key, "" for none
		nargs  int    // total args including the command name; 0 = skip
		suffix []string
	}{
		{config.MixItem{Op: "GET"}, "s", 2, nil},
		{config.MixItem{Op: "SET"}, "s", 5, []string{"EX", "99"}}, // TTL model applied
		{config.MixItem{Op: "MGET", Count: 3}, "s", 4, nil},
		{config.MixItem{Op: "MSET", Count: 3}, "s", 7, nil},
		{config.MixItem{Op: "INCR"}, "c", 2, nil},
		{config.MixItem{Op: "APPEND"}, "a", 3, nil},
		{config.MixItem{Op: "GETEX"}, "s", 4, []string{"EX", "99"}},
		{config.MixItem{Op: "SETEX"}, "s", 4, nil},
		{config.MixItem{Op: "DEL"}, "s", 2, nil},
		{config.MixItem{Op: "UNLINK", Type: "hash"}, "h", 2, nil},
		{config.MixItem{Op: "EXISTS", Type: "list"}, "l", 2, nil},
		{config.MixItem{Op: "TTL"}, "s", 2, nil},
		{config.MixItem{Op: "EXPIRE", Type: "set"}, "t", 3, nil},
		{config.MixItem{Op: "SCAN", Type: "zset", Count: 5}, "", 6, []string{"COUNT", "5"}},
		{config.MixItem{Op: "HSET", Fields: 3}, "h", 8, nil},
		{config.MixItem{Op: "HGET"}, "h", 3, nil},
		{config.MixItem{Op: "HGETALL"}, "h", 2, nil},
		{config.MixItem{Op: "HDEL"}, "h", 3, nil},
		{config.MixItem{Op: "HINCRBY"}, "h", 4, nil},
		{config.MixItem{Op: "LPUSH"}, "l", 3, nil},
		{config.MixItem{Op: "RPUSH"}, "l", 3, nil},
		{config.MixItem{Op: "LPOP"}, "l", 2, nil},
		{config.MixItem{Op: "RPOP"}, "l", 2, nil},
		{config.MixItem{Op: "LRANGE", Count: 20}, "l", 4, []string{"19"}},
		{config.MixItem{Op: "LLEN"}, "l", 2, nil},
		{config.MixItem{Op: "SADD"}, "t", 3, nil},
		{config.MixItem{Op: "SREM"}, "t", 3, nil},
		{config.MixItem{Op: "SISMEMBER"}, "t", 3, nil},
		{config.MixItem{Op: "SCARD"}, "t", 2, nil},
		{config.MixItem{Op: "SRANDMEMBER", Count: 2}, "t", 3, []string{"2"}},
		{config.MixItem{Op: "SMEMBERS"}, "t", 2, nil},
		{config.MixItem{Op: "ZADD"}, "z", 4, nil},
		{config.MixItem{Op: "ZREM"}, "z", 3, nil},
		{config.MixItem{Op: "ZSCORE"}, "z", 3, nil},
		{config.MixItem{Op: "ZINCRBY"}, "z", 4, nil},
		{config.MixItem{Op: "ZRANK"}, "z", 3, nil},
		{config.MixItem{Op: "ZRANGE", Count: 10}, "z", 4, []string{"9"}},
		{config.MixItem{Op: "ZRANGEBYSCORE", Count: 7}, "z", 8, []string{"BYSCORE", "LIMIT", "0", "7"}},
		{config.MixItem{Op: "ZCARD"}, "z", 2, nil},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.item.Op] = true
		g := newTestGen(t, d, 1)
		cmd := builders[tc.item.Op](g, tc.item)
		name := tc.item.Op
		if name == "ZRANGEBYSCORE" {
			name = "ZRANGE" // sent as ZRANGE ... BYSCORE
		}
		if cmd[0] != name {
			t.Errorf("%s: command %q", tc.item.Op, cmd[0])
		}
		if tc.nargs != 0 && len(cmd) != tc.nargs {
			t.Errorf("%s: %d args %v, want %d", tc.item.Op, len(cmd), cmd, tc.nargs)
		}
		if tc.ns != "" && !strings.HasPrefix(cmd[1], "p:"+tc.ns+":") {
			t.Errorf("%s: key %q not in namespace %q", tc.item.Op, cmd[1], tc.ns)
		}
		if tc.item.Op == "SCAN" && cmd[3] != "p:z:*" {
			t.Errorf("SCAN match %q", cmd[3])
		}
		if len(tc.suffix) > 0 {
			tail := cmd[len(cmd)-len(tc.suffix):]
			if !reflect.DeepEqual([]string(tail), tc.suffix) {
				t.Errorf("%s: tail %v, want %v", tc.item.Op, tail, tc.suffix)
			}
		}
	}
	for op, info := range config.Ops {
		switch info.Family {
		case "string", "key", "hash", "set", "zset":
		case "list":
			if info.Kind != "" {
				continue // blocking lists are in ops_adv_test.go
			}
		default:
			continue // newer families are covered in ops_adv_test.go
		}
		if !covered[op] {
			t.Errorf("op %s has no shape test", op)
		}
	}
}

func TestKeysStayInRange(t *testing.T) {
	d := config.Default().Dataset
	d.KeyCount = 50
	g := newTestGen(t, d, 3)
	for i := 0; i < 2000; i++ {
		k := builders["GET"](g, config.MixItem{})[1]
		n, err := strconv.Atoi(k[strings.LastIndex(k, ":")+1:])
		if err != nil || n < 0 || n >= 50 {
			t.Fatalf("key %q out of range", k)
		}
	}
}

func TestTTLModel(t *testing.T) {
	d := config.Default().Dataset
	d.KeyPrefix = "p:"

	d.TTL = config.TTL{Mode: "none", Seconds: 300, Fraction: 1}
	if c := builders["SET"](newTestGen(t, d, 1), config.MixItem{}); len(c) != 3 {
		t.Fatalf("mode none must not set a TTL: %v", c)
	}
	// SETEX still works with mode none, using the fixed seconds.
	if c := builders["SETEX"](newTestGen(t, d, 1), config.MixItem{}); c[2] != "300" {
		t.Fatalf("SETEX with mode none: %v", c)
	}

	d.TTL = config.TTL{Mode: "random", MinSeconds: 10, MaxSeconds: 20, Fraction: 1}
	g := newTestGen(t, d, 1)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c := builders["SET"](g, config.MixItem{})
		n, _ := strconv.Atoi(c[4])
		if c[3] != "EX" || n < 10 || n > 20 {
			t.Fatalf("random TTL out of range: %v", c)
		}
		seen[c[4]] = true
	}
	if len(seen) < 8 {
		t.Fatalf("random TTL not varied: %d distinct", len(seen))
	}

	d.TTL = config.TTL{Mode: "fixed", Seconds: 60, Fraction: 0.5}
	g = newTestGen(t, d, 1)
	with := 0
	for i := 0; i < 2000; i++ {
		if len(builders["SET"](g, config.MixItem{})) == 5 {
			with++
		}
	}
	if with < 850 || with > 1150 {
		t.Fatalf("fraction 0.5 gave %d/2000 with TTL", with)
	}
}

func TestPickFollowsWeights(t *testing.T) {
	w := Compile([]config.MixItem{{Op: "GET", Weight: 90}, {Op: "SET", Weight: 10}})
	r := rand.New(rand.NewSource(1))
	counts := [2]int{}
	for i := 0; i < 20000; i++ {
		counts[w.Pick(r)]++
	}
	if counts[0] < 17400 || counts[0] > 18600 {
		t.Fatalf("GET picked %d/20000, want about 18000", counts[0])
	}
}

// Basic deterministic seed: same seed, same command sequence.
func TestSameSeedSameCommands(t *testing.T) {
	cfg := config.Default()
	cfg.Dataset.KeyDistribution = "zipf"
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 3}, {Op: "SET", Weight: 2}, {Op: "ZADD", Weight: 1}, {Op: "HSET", Weight: 1, Fields: 2}}
	seq := func(seed int64) []Cmd {
		w := Compile(cfg.Workload.Mix)
		g := newTestGen(t, cfg.Dataset, seed)
		var out []Cmd
		for i := 0; i < 500; i++ {
			out = append(out, w.Build(g, w.Pick(g.r)))
		}
		return out
	}
	a, b, c := seq(7), seq(7), seq(8)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different commands")
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced identical commands")
	}
}

func TestPreloadCmdShapes(t *testing.T) {
	d := config.Default().Dataset
	d.KeyPrefix = "p:"
	d.CollectionSize = 4
	d.HashFields = 3
	vs, _ := values.NewSource(16, 1)
	r := rand.New(rand.NewSource(1))
	want := map[string]struct {
		name string
		n    int
	}{
		config.TypeString: {"SET", 3}, config.TypeHash: {"HSET", 2 + 2*3}, config.TypeList: {"RPUSH", 2 + 4},
		config.TypeSet: {"SADD", 2 + 4}, config.TypeZSet: {"ZADD", 2 + 2*4},
	}
	for typ, w := range want {
		c := preloadCmd(d, typ, 5, vs, r)
		if c[0] != w.name || len(c) != w.n || !strings.HasSuffix(c[1], ":5") {
			t.Errorf("%s: %v", typ, c)
		}
	}
}
