package valkey

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyspace"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
)

func advGen(t *testing.T, mut func(*config.Dataset)) (*Gen, config.Dataset) {
	t.Helper()
	d := config.Default().Dataset
	d.KeyPrefix = "p:"
	d.KeyCount = 1000
	if mut != nil {
		mut(&d)
	}
	kg, err := keyspace.New("uniform", d.KeyCount, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := values.NewSource(d.ValueSize, 1)
	g := NewGen(rand.New(rand.NewSource(2)), kg, vs, d)
	g.Worker, g.Channels = 3, 8
	return g, d
}

func TestAdvancedOpShapes(t *testing.T) {
	g, _ := advGen(t, nil)
	cases := []struct {
		item  config.MixItem
		name  string
		ns    string // namespace of the routing key
		nargs int
		has   []string // arguments that must be present, in order
	}{
		{config.MixItem{Op: "BLPOP", Timeout: "250ms"}, "BLPOP", "l", 3, []string{"0.25"}},
		{config.MixItem{Op: "BRPOP"}, "BRPOP", "l", 3, []string{"1"}},
		{config.MixItem{Op: "XADD", Fields: 3}, "XADD", "x", 6 + 2*3, []string{"MAXLEN", "~", "1000", "*"}},
		{config.MixItem{Op: "XLEN"}, "XLEN", "x", 2, nil},
		{config.MixItem{Op: "XRANGE", Count: 7}, "XRANGE", "x", 6, []string{"-", "+", "COUNT", "7"}},
		{config.MixItem{Op: "XREAD", Count: 4}, "XREAD", "x", 6, []string{"COUNT", "4", "STREAMS"}},
		{config.MixItem{Op: "XREAD_BLOCK", Timeout: "300ms"}, "XREAD", "x", 8, []string{"BLOCK", "300", "COUNT", "10", "STREAMS"}},
		{config.MixItem{Op: "XTRIM"}, "XTRIM", "x", 5, []string{"MAXLEN", "~", "20"}},
		{config.MixItem{Op: "XPENDING"}, "XPENDING", "x", 3, []string{StreamGroup}},
		{config.MixItem{Op: "XREADGROUP", Count: 2}, "XREADGROUP", "x", 9, []string{"GROUP", StreamGroup, "c3", "COUNT", "2", "STREAMS"}},
		{config.MixItem{Op: "GEOADD"}, "GEOADD", "g", 5, nil},
		{config.MixItem{Op: "GEOPOS"}, "GEOPOS", "g", 3, nil},
		{config.MixItem{Op: "GEODIST"}, "GEODIST", "g", 5, []string{"km"}},
		{config.MixItem{Op: "GEOSEARCH", Count: 3}, "GEOSEARCH", "g", 11, []string{"FROMLONLAT"}},
		{config.MixItem{Op: "PFADD"}, "PFADD", "p", 3, nil},
		{config.MixItem{Op: "PFCOUNT"}, "PFCOUNT", "p", 2, nil},
		{config.MixItem{Op: "SETBIT"}, "SETBIT", "b", 4, nil},
		{config.MixItem{Op: "GETBIT"}, "GETBIT", "b", 3, nil},
		{config.MixItem{Op: "BITCOUNT"}, "BITCOUNT", "b", 2, nil},
		{config.MixItem{Op: "EVAL", Script: "rate_limit"}, "EVAL", "", 6, []string{"1"}},
		{config.MixItem{Op: "EVALSHA", Script: "incr_cap"}, "EVALSHA", "", 5, []string{"1"}},
		{config.MixItem{Op: "FCALL", Script: "get_set"}, "FCALL", "", 5, []string{"get_set", "1"}},
		{config.MixItem{Op: "PUBLISH"}, "PUBLISH", "", 3, nil},
		{config.MixItem{Op: "SPUBLISH"}, "SPUBLISH", "", 3, nil},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.item.Op] = true
		cmd := builders[tc.item.Op](g, tc.item)
		if cmd[0] != tc.name {
			t.Errorf("%s: command %q", tc.item.Op, cmd[0])
		}
		if len(cmd) != tc.nargs {
			t.Errorf("%s: %d args %v, want %d", tc.item.Op, len(cmd), cmd, tc.nargs)
		}
		if tc.ns != "" {
			key := RouteKey(cmd)
			if !strings.HasPrefix(key, "p:"+tc.ns+":") {
				t.Errorf("%s: routed by %q, want namespace %q", tc.item.Op, key, tc.ns)
			}
		}
		at := 0
		for _, w := range tc.has { // arguments appear in the given order
			found := false
			for ; at < len(cmd); at++ {
				if cmd[at] == w {
					found = true
					at++
					break
				}
			}
			if !found {
				t.Errorf("%s: %v lacks %q in order", tc.item.Op, cmd, w)
			}
		}
	}
	for op, info := range config.Ops {
		advanced := info.Family == "stream" || info.Family == "geo" || info.Family == "hll" || info.Family == "bitmap" || info.Family == "script" || info.Family == "pubsub" ||
			(info.Family == "list" && info.Kind != "")
		if advanced && info.Kind != config.KindComposite && !covered[op] {
			t.Errorf("op %s has no shape test", op)
		}
	}
}

func TestPublishPayloadCarriesATimestampAndTheValueSize(t *testing.T) {
	g, d := advGen(t, func(d *config.Dataset) { d.ValueSize = 64 })
	cmd := builders["PUBLISH"](g, config.MixItem{})
	payload := cmd[2]
	if len(payload) != d.ValueSize {
		t.Fatalf("payload %d bytes, want %d", len(payload), d.ValueSize)
	}
	ts, _, ok := strings.Cut(payload, "|")
	n, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil || time.Since(time.Unix(0, n)) > time.Second || time.Since(time.Unix(0, n)) < 0 {
		t.Fatalf("payload %q must start with the send time in ns", payload)
	}
	if !strings.HasPrefix(cmd[1], "p:ch:") {
		t.Fatalf("channel %q", cmd[1])
	}
	if sp := builders["SPUBLISH"](g, config.MixItem{}); !strings.Contains(sp[1], "{") {
		t.Fatalf("a sharded channel carries a hash tag: %q", sp[1])
	}
	tiny, _ := advGen(t, func(d *config.Dataset) { d.ValueSize = 4 })
	if p := builders["PUBLISH"](tiny, config.MixItem{})[2]; !strings.Contains(p, "|") {
		t.Fatalf("a payload smaller than the timestamp must still carry it: %q", p)
	}
}

func TestScriptArgumentsDefaultsAndOverrides(t *testing.T) {
	g, _ := advGen(t, nil)
	rl := builders["EVAL"](g, config.MixItem{Op: "EVAL", Script: "rate_limit"})
	if rl[1] != scriptBodies["rate_limit"] || rl[3] != rl[3] || rl[4] != "60" || rl[5] != "100000" || !strings.HasPrefix(rl[3], "p:c:") {
		t.Fatalf("%v", rl)
	}
	over := builders["EVAL"](g, config.MixItem{Op: "EVAL", Script: "rate_limit", Args: []string{"5", "2"}})
	if over[4] != "5" || over[5] != "2" {
		t.Fatalf("args override the defaults: %v", over)
	}
	gs := builders["EVALSHA"](g, config.MixItem{Op: "EVALSHA", Script: "get_set"})
	if gs[1] != sha(scriptBodies["get_set"]) || !strings.HasPrefix(gs[3], "p:s:") || len(gs[4]) != 256 {
		t.Fatalf("get_set works on string keys with a dataset value: %v", gs[:4])
	}
	inline := builders["EVAL"](g, config.MixItem{Op: "EVAL", Source: "return KEYS[1]", Args: []string{"x"}})
	if inline[1] != "return KEYS[1]" || inline[2] != "1" || inline[4] != "x" || !strings.HasPrefix(inline[3], "p:s:") {
		t.Fatalf("%v", inline)
	}
	if sha("return 1") != "e0e1f9fabfc9d4800c877a703b823ac0578ff8db" {
		t.Fatalf("sha1 of the script body, as SCRIPT LOAD returns it: %s", sha("return 1"))
	}
}

func TestKeyIndexesFindTheRoutingKeys(t *testing.T) {
	cases := []struct {
		cmd  Cmd
		want []int
	}{
		{Cmd{"GET", "k"}, []int{1}}, {Cmd{"MGET", "a", "b", "c"}, []int{1, 2, 3}}, {Cmd{"MSET", "a", "1", "b", "2"}, []int{1, 3}},
		{Cmd{"EVAL", "return 1", "1", "k", "arg"}, []int{3}}, {Cmd{"EVALSHA", "sha", "1", "k"}, []int{3}}, {Cmd{"FCALL", "f", "1", "k", "x"}, []int{3}},
		{Cmd{"EVAL", "return 1", "0"}, nil}, {Cmd{"EVAL", "return 1", "2", "a", "b"}, []int{3, 4}}, {Cmd{"EVAL", "x"}, nil}, {Cmd{"EVAL", "x", "y", "z"}, nil},
		{Cmd{"XREAD", "COUNT", "5", "STREAMS", "s", "0"}, []int{4}}, {Cmd{"XREAD", "BLOCK", "100", "STREAMS", "s", "$"}, []int{4}},
		{Cmd{"XREADGROUP", "GROUP", "g", "c", "COUNT", "2", "streams", "s", ">"}, []int{7}},
		{Cmd{"XADD", "s", "*", "f", "v"}, []int{1}}, {Cmd{"BRPOP", "k", "1"}, []int{1}}, {Cmd{"GEOSEARCH", "k", "FROMLONLAT"}, []int{1}},
		{Cmd{"SPUBLISH", "ch{1}", "m"}, []int{1}}, {Cmd{"PUBLISH", "ch", "m"}, nil}, {Cmd{"SCAN", "0"}, nil}, {Cmd{"INFO", "everything"}, nil},
		{Cmd{"CLUSTER", "SHARDS"}, nil}, {Cmd{"PING"}, nil}, {Cmd{"SCRIPT", "LOAD", "x"}, nil},
	}
	for _, tc := range cases {
		got := KeyIndexes(tc.cmd)
		if len(got) != len(tc.want) {
			t.Errorf("%v: %v, want %v", tc.cmd, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%v: %v, want %v", tc.cmd, got, tc.want)
			}
		}
	}
	if RouteKey(Cmd{"EVAL", "x", "1", "mykey"}) != "mykey" || RouteKey(Cmd{"PUBLISH", "c", "m"}) != "" {
		t.Fatal("RouteKey follows KeyIndexes")
	}
}

func TestRawCommandKeyPositionsAreRegistered(t *testing.T) {
	cfgMix := []config.MixItem{{Op: "RAW", Weight: 1, Command: "OBJECT ENCODING {key}"}, {Op: "RAW", Weight: 1, Command: "TYPE {key}"},
		{Op: "RAW", Weight: 1, Command: "GET {key}"}, {Op: "RAW", Weight: 1, Command: "PING"}}
	wl := Compile(cfgMix)
	g, _ := advGen(t, nil)
	cmd := wl.Build(g, 0)
	if cmd[0] != "OBJECT" || cmd[1] != "ENCODING" || !strings.HasPrefix(cmd[2], "p:s:") {
		t.Fatalf("%v", cmd)
	}
	if got := KeyIndexes(cmd); len(got) != 1 || got[0] != 2 {
		t.Fatalf("a raw command's {key} argument is its routing key: %v", got)
	}
	if got := KeyIndexes(wl.Build(g, 1)); len(got) != 1 || got[0] != 1 {
		t.Fatalf("TYPE: %v", got)
	}
	if got := KeyIndexes(wl.Build(g, 3)); got != nil {
		t.Fatalf("PING has no key: %v", got)
	}
	if wl.Kind(0) != config.KindSimple {
		t.Fatal("raw commands are pipelined like any simple op")
	}
	// Known commands keep their built-in key position.
	if RouteKey(wl.Build(g, 2)) == "" {
		t.Fatal("GET is routed by its key")
	}
}

func TestIsBlocking(t *testing.T) {
	for cmd, want := range map[string]bool{"BLPOP k 1": true, "BRPOP k 1": true, "XREAD BLOCK 5 STREAMS s $": true, "XREADGROUP GROUP g c BLOCK 5 STREAMS s >": true,
		"XREAD COUNT 5 STREAMS s 0": false, "GET k": false, "LPOP k": false} {
		if got := isBlocking(Cmd(strings.Fields(cmd))); got != want {
			t.Errorf("%q: %v", cmd, got)
		}
	}
}

// --- composites, with a scripted client ---

type scripted struct {
	mu       sync.Mutex
	execs    [][]Cmd
	queries  []Cmd
	results  map[string][]Result // by first command name of the batch
	replies  map[string]any      // Query replies by command name
	queryErr map[string]error
	conn     []Cmd // commands seen on a dedicated connection
}

func newScripted() *scripted {
	return &scripted{results: map[string][]Result{}, replies: map[string]any{}, queryErr: map[string]error{}}
}

func (s *scripted) Exec(_ context.Context, cmds []Cmd) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, append([]Cmd(nil), cmds...))
	if r, ok := s.results[cmds[0][0]]; ok {
		return r
	}
	return make([]Result, len(cmds))
}
func (s *scripted) Query(_ context.Context, cmd Cmd) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, cmd)
	return s.replies[cmd[0]], s.queryErr[cmd[0]]
}
func (s *scripted) Close() {}
func (s *scripted) Dedicated(_ context.Context, fn func(Conn) error) error {
	return fn(&scriptedConn{s})
}
func (s *scripted) Subscribe(ctx context.Context, _ bool, _ []string, _ func(string, string), onReady func()) error {
	if onReady != nil {
		onReady()
	}
	<-ctx.Done()
	return nil
}

type scriptedConn struct{ s *scripted }

func (c *scriptedConn) Query(ctx context.Context, cmd Cmd) (any, error) {
	c.s.mu.Lock()
	c.s.conn = append(c.s.conn, cmd)
	c.s.mu.Unlock()
	return c.s.Query(ctx, cmd)
}
func (c *scriptedConn) Exec(ctx context.Context, cmds []Cmd) []Result {
	c.s.mu.Lock()
	c.s.conn = append(c.s.conn, cmds...)
	c.s.mu.Unlock()
	return c.s.Exec(ctx, cmds)
}

func TestTxnWrapsTheStepsInMultiExec(t *testing.T) {
	c := newScripted()
	g, _ := advGen(t, func(d *config.Dataset) { d.HashTagGroups = 8 })
	it := config.MixItem{Op: "TXN", Steps: []config.MixItem{{Op: "GET"}, {Op: "SET"}, {Op: "HSET", Fields: 2}}}
	res, route := composites["TXN"](context.Background(), c, g, it)
	if res.Err != nil || res.Miss {
		t.Fatalf("%+v", res)
	}
	if len(c.execs) != 1 {
		t.Fatalf("one round trip for the whole transaction, got %d", len(c.execs))
	}
	b := c.execs[0]
	if len(b) != 5 || b[0][0] != "MULTI" || b[4][0] != "EXEC" || b[1][0] != "GET" || b[2][0] != "SET" || b[3][0] != "HSET" {
		t.Fatalf("%v", b)
	}
	// In a cluster every key of the transaction shares a slot (one hash tag group).
	slot := Slot(b[1][1])
	for _, cmd := range b[1:4] {
		if Slot(cmd[1]) != slot {
			t.Fatalf("keys span slots: %v", b)
		}
	}
	if route != b[1][1] {
		t.Fatalf("routed by the first step's key: %q", route)
	}
	if g.pinned {
		t.Fatal("the generator must be unpinned afterwards")
	}
}

func TestTxnOutcomes(t *testing.T) {
	g, _ := advGen(t, nil)
	it := config.MixItem{Op: "TXN", Steps: []config.MixItem{{Op: "SET"}}}
	c := newScripted()
	c.results["MULTI"] = []Result{{}, {}, {Miss: true}} // EXEC returned nil: an aborted transaction
	if res, _ := composites["TXN"](context.Background(), c, g, it); !res.Miss || res.Err != nil {
		t.Fatalf("an aborted EXEC is a miss: %+v", res)
	}
	c.results["MULTI"] = []Result{{}, {Err: errors.New("WRONGTYPE nope")}, {}}
	if res, _ := composites["TXN"](context.Background(), c, g, it); res.Err == nil || res.Miss {
		t.Fatalf("an error in the batch is an error: %+v", res)
	}
}

func TestPipelineGroupSendsStepsTogetherWithoutMulti(t *testing.T) {
	c := newScripted()
	g, _ := advGen(t, nil)
	res, _ := composites["PIPELINE"](context.Background(), c, g, config.MixItem{Op: "PIPELINE", Steps: []config.MixItem{{Op: "GET"}, {Op: "GET"}, {Op: "INCR"}}})
	if res.Err != nil || len(c.execs) != 1 || len(c.execs[0]) != 3 {
		t.Fatalf("%+v %v", res, c.execs)
	}
	for _, cmd := range c.execs[0] {
		if cmd[0] == "MULTI" || cmd[0] == "EXEC" {
			t.Fatal("a pipeline group is not atomic")
		}
	}
	c.results["GET"] = []Result{{}, {Err: errors.New("boom")}, {}}
	if res, _ := composites["PIPELINE"](context.Background(), c, g, config.MixItem{Op: "PIPELINE", Steps: []config.MixItem{{Op: "GET"}, {Op: "GET"}, {Op: "INCR"}}}); res.Err == nil {
		t.Fatal("a failing step makes the group an error")
	}
}

func TestWatchIncrIsOptimisticAndCountsConflicts(t *testing.T) {
	g, _ := advGen(t, nil)
	c := newScripted()
	c.replies["GET"] = "41"
	res, key := composites["WATCH_INCR"](context.Background(), c, g, config.MixItem{Op: "WATCH_INCR"})
	if res.Err != nil || res.Miss || !strings.HasPrefix(key, "p:c:") {
		t.Fatalf("%+v %q", res, key)
	}
	names := []string{}
	for _, cmd := range c.conn {
		names = append(names, cmd[0])
	}
	if strings.Join(names, " ") != "WATCH GET MULTI SET EXEC" {
		t.Fatalf("everything ran on one dedicated connection, in this order: %v", names)
	}
	if set := c.conn[3]; set[1] != key || set[2] != "42" {
		t.Fatalf("SET %v must write the value read plus one", set)
	}
	// EXEC returning nil means another client touched the key: a conflict.
	c = newScripted()
	c.results["MULTI"] = []Result{{}, {}, {Miss: true}}
	if res, _ := composites["WATCH_INCR"](context.Background(), c, g, config.MixItem{Op: "WATCH_INCR"}); !res.Miss {
		t.Fatalf("conflict must be a miss: %+v", res)
	}
	// A missing counter starts from zero.
	c = newScripted()
	composites["WATCH_INCR"](context.Background(), c, g, config.MixItem{Op: "WATCH_INCR"})
	if c.conn[3][2] != "1" {
		t.Fatalf("%v", c.conn[3])
	}
	// Errors surface.
	c = newScripted()
	c.queryErr["WATCH"] = errors.New("READONLY nope")
	if res, _ := composites["WATCH_INCR"](context.Background(), c, g, config.MixItem{Op: "WATCH_INCR"}); res.Err == nil {
		t.Fatal("WATCH failed")
	}
}

func TestXConsumeReadsThenAcknowledgesWhatItGot(t *testing.T) {
	g, _ := advGen(t, nil)
	// RESP3: a map of stream -> entries
	c := newScripted()
	c.replies["XREADGROUP"] = map[string]any{"p:x:5": []any{[]any{"1-0", []any{"f0", "v"}}, []any{"2-0", []any{"f0", "v"}}}}
	res, key := composites["XCONSUME"](context.Background(), c, g, config.MixItem{Op: "XCONSUME", Count: 2})
	if res.Err != nil || res.Miss || !strings.HasPrefix(key, "p:x:") {
		t.Fatalf("%+v %q", res, key)
	}
	if len(c.execs) != 1 || strings.Join(c.execs[0][0], " ") != "XACK "+key+" plgv 1-0 2-0" {
		t.Fatalf("%v", c.execs)
	}
	// RESP2: an array of [stream, entries]
	c = newScripted()
	c.replies["XREADGROUP"] = []any{[]any{"p:x:5", []any{[]any{"9-1", []any{"f0", "v"}}}}}
	composites["XCONSUME"](context.Background(), c, g, config.MixItem{Op: "XCONSUME"})
	if len(c.execs) != 1 || c.execs[0][0][len(c.execs[0][0])-1] != "9-1" {
		t.Fatalf("%v", c.execs)
	}
	// Nothing new: a miss, and no XACK.
	c = newScripted()
	if res, _ := composites["XCONSUME"](context.Background(), c, g, config.MixItem{Op: "XCONSUME"}); !res.Miss || len(c.execs) != 0 {
		t.Fatalf("%+v %v", res, c.execs)
	}
	c.queryErr["XREADGROUP"] = errors.New("NOGROUP no such group")
	if res, _ := composites["XCONSUME"](context.Background(), c, g, config.MixItem{Op: "XCONSUME"}); res.Err == nil {
		t.Fatal("NOGROUP must be an error")
	}
	if got := StreamIDs("junk"); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestSoloAndCompositeOpsRunOnTheirOwnInABatch(t *testing.T) {
	f := newFake()
	cfg := testCfg()
	cfg.Target.Pool.PipelineDepth = 6
	cfg.Run.Duration = 200 * time.Millisecond
	cfg.Dataset.KeyCount = 100
	cfg.Workload.Mix = []config.MixItem{
		{Op: "GET", Weight: 4}, {Op: "BRPOP", Weight: 1, Timeout: "10ms"},
		{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET"}, {Op: "GET"}}}, {Op: "WATCH_INCR", Weight: 1},
	}
	res, err := Run(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 || res.Ops == 0 {
		t.Fatalf("%+v", res)
	}
	byName := map[string]uint64{}
	for _, o := range res.PerOp {
		byName[o.Op] = o.Ops
	}
	for _, name := range []string{"GET", "BRPOP", "TXN[SET,GET]", "WATCH_INCR"} {
		if byName[name] == 0 {
			t.Errorf("%s never ran: %v", name, byName)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var sawMulti, sawBRPOPAlone bool
	for _, n := range f.batches {
		_ = n
	}
	for name := range f.cmds {
		if name == "MULTI" {
			sawMulti = true
		}
		if name == "BRPOP" {
			sawBRPOPAlone = true
		}
	}
	if !sawMulti || !sawBRPOPAlone {
		t.Fatalf("commands seen: %v", f.cmds)
	}
	// Each operation is counted once, however many commands it takes.
	var sum uint64
	for _, v := range byName {
		sum += v
	}
	if sum != res.Ops {
		t.Fatalf("%d != %d", sum, res.Ops)
	}
}

func TestPreloadCommandsForTheNewTypes(t *testing.T) {
	d := config.Default().Dataset
	d.KeyPrefix = "p:"
	d.StreamLength = 3
	d.CollectionSize = 4
	d.BitmapBits = 64
	vs, _ := values.NewSource(16, 1)
	r := rand.New(rand.NewSource(1))

	st := preloadCmds(d, config.TypeStream, 5, vs, r)
	if len(st) != 4 || strings.Join(st[0], " ") != "XGROUP CREATE p:x:5 plgv 0 MKSTREAM" || st[1][0] != "XADD" || st[1][1] != "p:x:5" {
		t.Fatalf("a stream is its consumer group and its entries: %v", st)
	}
	if g := preloadCmds(d, config.TypeGeo, 1, vs, r)[0]; g[0] != "GEOADD" || len(g) != 2+3*4 {
		t.Fatalf("%v", g)
	}
	if h := preloadCmds(d, config.TypeHLL, 1, vs, r)[0]; h[0] != "PFADD" || len(h) != 2+4 {
		t.Fatalf("%v", h)
	}
	if b := preloadCmds(d, config.TypeBitmap, 1, vs, r)[0]; b[0] != "SET" || len(b[2]) != 8 {
		t.Fatalf("a bitmap of 64 bits is 8 bytes: %v", len(b[2]))
	}
	if s := preloadCmds(d, config.TypeString, 1, vs, r); len(s) != 1 || s[0][0] != "SET" {
		t.Fatalf("%v", s)
	}
}

func TestPreloadToleratesAnExistingConsumerGroup(t *testing.T) {
	cfg := testCfg()
	cfg.Dataset.KeyCount = 10
	cfg.Dataset.StreamLength = 2
	cfg.Workload.Mix = []config.MixItem{{Op: "XCONSUME", Weight: 1}}
	f := &busyGroupClient{fakeClient: newFake()}
	if err := Preload(context.Background(), f, cfg); err != nil {
		t.Fatalf("preloading a second time hits BUSYGROUP, which is fine: %v", err)
	}
	if f.cmds["XGROUP"] != 10 || f.cmds["XADD"] != 20 {
		t.Fatalf("%v", f.cmds)
	}
}

type busyGroupClient struct{ *fakeClient }

func (b *busyGroupClient) Exec(ctx context.Context, cmds []Cmd) []Result {
	res := b.fakeClient.Exec(ctx, cmds)
	for i, c := range cmds {
		if c[0] == "XGROUP" {
			res[i].Err = errors.New("BUSYGROUP Consumer Group name already exists")
		}
	}
	return res
}

// --- scripts and functions ---

type nodeClient struct {
	*fakeClient
	nodes []string
	mu    sync.Mutex
	seen  map[string][]string
	fail  map[string]error
}

func (n *nodeClient) Nodes() []string { return n.nodes }
func (n *nodeClient) QueryNode(_ context.Context, addr string, cmd Cmd) (any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen[addr] = append(n.seen[addr], cmd[0]+" "+cmd[1])
	if err := n.fail[addr+" "+cmd[0]]; err != nil {
		return nil, err
	}
	return "ok", nil
}

func TestPrepareLoadsScriptsAndFunctionsOnEveryNode(t *testing.T) {
	mix := []config.MixItem{{Op: "EVALSHA", Script: "rate_limit"}, {Op: "EVALSHA", Script: "rate_limit"}, {Op: "EVALSHA", Source: "return 1"}, {Op: "FCALL", Script: "cas"}, {Op: "GET"}}
	nc := &nodeClient{fakeClient: newFake(), nodes: []string{"a:1", "b:2", "c:3"}, seen: map[string][]string{}, fail: map[string]error{"c:3 FUNCTION": errors.New("READONLY You can't write against a read only replica.")}}
	if err := Prepare(context.Background(), nc, mix); err != nil {
		t.Fatalf("a replica refusing FUNCTION LOAD is fine: %v", err)
	}
	for _, a := range nc.nodes {
		got := strings.Join(nc.seen[a], "|")
		if strings.Count(got, "SCRIPT LOAD") != 2 || !strings.Contains(got, "FUNCTION LOAD") {
			t.Errorf("%s: %s (each distinct script once, plus the function library)", a, got)
		}
	}
	// A real error is reported.
	nc.fail["a:1 SCRIPT"] = errors.New("ERR Error compiling script")
	if err := Prepare(context.Background(), nc, mix); err == nil || !strings.Contains(err.Error(), "a:1") {
		t.Fatalf("%v", err)
	}
	// Nothing to do without EVALSHA or FCALL: no server contact at all.
	untouched := &nodeClient{fakeClient: newFake(), nodes: []string{"a:1", "b:2"}, seen: map[string][]string{}}
	if err := Prepare(context.Background(), untouched, []config.MixItem{{Op: "EVAL", Script: "cas"}, {Op: "GET"}}); err != nil || len(untouched.seen) != 0 {
		t.Fatalf("%v %v", err, untouched.seen)
	}
	if !strings.HasPrefix(FunctionLibrary, "#!lua name=plgv") {
		t.Fatal("a function library starts with its shebang")
	}
	for _, fn := range config.BuiltinScripts {
		if !strings.Contains(FunctionLibrary, "register_function('"+fn+"'") || scriptBodies[fn] == "" || scriptNS[fn] == "" {
			t.Errorf("built-in %s is incomplete", fn)
		}
	}
}

func TestDisplayNames(t *testing.T) {
	for it, want := range map[string]config.MixItem{
		"GET": {Op: "GET"}, "RAW GET {key}": {Op: "RAW", Command: "GET   {key}"}, "TXN[SET,HSET]": {Op: "TXN", Steps: []config.MixItem{{Op: "SET"}, {Op: "HSET"}}},
		"PIPELINE[GET]": {Op: "PIPELINE", Steps: []config.MixItem{{Op: "GET"}}},
	} {
		if got := DisplayName(want); got != it {
			t.Errorf("%q, want %q", got, it)
		}
	}
	long := DisplayName(config.MixItem{Op: "RAW", Command: "SET " + strings.Repeat("x", 100)})
	if len(long) > 52 || !strings.HasSuffix(long, "...") {
		t.Fatalf("a long raw command is shortened: %q", long)
	}
}

func TestSplitKeylessOnlyWhenKeysSpanSlots(t *testing.T) {
	a, b := "{x}1", "{y}2" // different slots
	if Slot(a) == Slot(b) {
		t.Fatal("test keys must be in different slots")
	}
	// Keyed commands of two slots plus a keyless one cannot share a cluster pipeline.
	keyed, keyless := splitKeyless([]Cmd{{"GET", a}, {"PUBLISH", "c", "m"}, {"GET", b}, {"SCAN", "0"}})
	if len(keyed) != 2 || keyed[0] != 0 || keyed[1] != 2 || len(keyless) != 2 || keyless[0] != 1 || keyless[1] != 3 {
		t.Fatalf("%v %v", keyed, keyless)
	}
	// One slot (a transaction: MULTI, steps, EXEC) is routed whole, so it must not be split.
	keyed, keyless = splitKeyless([]Cmd{{"MULTI"}, {"SET", "{x}1", "v"}, {"GET", "{x}2"}, {"EXEC"}})
	if len(keyless) != 0 || len(keyed) != 2 {
		t.Fatalf("a single-slot batch stays whole: %v %v", keyed, keyless)
	}
	// All keyless, or all keyed: nothing to split.
	if _, kl := splitKeyless([]Cmd{{"PUBLISH", "c", "m"}, {"PUBLISH", "c", "m"}}); len(kl) != 0 {
		t.Fatal("an all-keyless batch is fine")
	}
	if _, kl := splitKeyless([]Cmd{{"GET", a}, {"GET", b}}); len(kl) != 0 {
		t.Fatal("an all-keyed batch is fine")
	}
	got := pick([]Cmd{{"A"}, {"B"}, {"C"}}, []int{2, 0})
	if got[0][0] != "C" || got[1][0] != "A" {
		t.Fatalf("%v", got)
	}
}
