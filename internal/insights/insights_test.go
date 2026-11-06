package insights

import (
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
)

// healthy is a run with nothing wrong. Each case below changes one thing.
func healthy() Input {
	return Input{
		Client: Client{Ops: 1_000_000, P50: 800 * time.Microsecond, P99: 2 * time.Millisecond,
			Elapsed: 10 * time.Second, CPUCores: 1, GOMAXPROCS: 8},
		Server: &servermetrics.Summary{
			Version: "9.1.3", ServerOps: 1_000_000, ServerOpsExact: true, HitRatio: 0.99, KeyspaceHits: 990_000, KeyspaceMisses: 10_000,
			MaxMemory: 1 << 30, UsedMemoryMax: 100 << 20, MaxFragRatio: 1.1,
		},
	}
}

func ids(fs []Finding) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.ID)
	}
	return strings.Join(s, ",")
}

func TestHealthyRunHasNoFindings(t *testing.T) {
	if fs := Evaluate(healthy()); len(fs) != 0 {
		t.Fatalf("unexpected findings: %s", ids(fs))
	}
	// Without server metrics, only client rules can fire.
	in := healthy()
	in.Server = nil
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("unexpected findings: %s", ids(fs))
	}
}

func TestRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		want   string   // finding id that must appear
		sev    Severity // and its severity
		evid   string   // substring of Evidence
	}{
		{"errors warn", func(i *Input) { i.Client.Errors = 5; i.Client.ErrClasses = map[string]uint64{"OOM": 5} }, "client-errors", Warning, "OOM: 5"},
		{"errors critical at 1%", func(i *Input) { i.Client.Errors = 20_000; i.Client.ErrClasses = map[string]uint64{"timeout": 20_000} }, "client-errors", Critical, "timeout"},
		{"eviction", func(i *Input) { i.Server.EvictedKeys = 1234; i.Server.EvictionPolicy = "allkeys-lru" }, "eviction-pressure", Warning, "1234"},
		{"memory 91%", func(i *Input) { i.Server.UsedMemoryMax = int64(0.91 * float64(i.Server.MaxMemory)) }, "memory-near-limit", Warning, "maxmemory"},
		{"memory 99%", func(i *Input) { i.Server.UsedMemoryMax = int64(0.99 * float64(i.Server.MaxMemory)) }, "memory-near-limit", Critical, ""},
		{"low hit ratio", func(i *Input) { i.Server.HitRatio = 0.2; i.Server.KeyspaceHits = 2000; i.Server.KeyspaceMisses = 8000 }, "low-hit-ratio", Warning, "8000 misses"},
		{"fork 200ms", func(i *Input) { i.Server.MaxForkUsec = 200_000 }, "fork-stall", Warning, "200000"},
		{"fork 2s", func(i *Input) { i.Server.MaxForkUsec = 2_000_000 }, "fork-stall", Critical, ""},
		{"slow commands", func(i *Input) {
			i.Server.SlowlogNew = 3
			i.Server.SlowlogWorst = 40 * time.Millisecond
			i.Server.SlowlogSample = []servermetrics.SlowEntry{{Command: "HGETALL big"}}
		}, "slow-commands", Warning, "HGETALL big"},
		{"blocked", func(i *Input) { i.Server.MaxBlockedConns = 4 }, "blocked-clients", Info, "4"},
		{"replica lag", func(i *Input) { i.Server.ReplicaLagBytes = 5 << 20 }, "replication-lag", Warning, "5242880"},
		{"replica link down", func(i *Input) { i.Server.LinkDown = true }, "replication-lag", Critical, "master_link_status"},
		{"rejected", func(i *Input) { i.Server.RejectedConnections = 9 }, "rejected-connections", Critical, "9"},
		{"fragmentation", func(i *Input) { i.Server.MaxFragRatio = 1.9; i.Server.UsedMemoryMax = 500 << 20 }, "memory-fragmentation", Warning, "1.90"},
		{"client cpu bound", func(i *Input) { i.Client.CPUCores = 7.5 }, "load-generator-bound", Warning, "7.5 of 8"},
		{"failover", func(i *Input) {
			i.Server.Topology = topo(servermetrics.Event{Offset: 7 * time.Second, Kind: "failover", Detail: "slots 0-5460: primary changed from a to b"})
		}, "failover-during-run", Warning, "at 7s: slots 0-5460"},
		{"reshard", func(i *Input) {
			i.Server.Topology = topo(servermetrics.Event{Offset: 3 * time.Second, Kind: "reshard", Detail: "slot ownership changed: 0-4999"})
		}, "reshard-during-run", Warning, "0-4999"},
		{"node down", func(i *Input) {
			i.Server.Topology = topo(servermetrics.Event{Offset: time.Second, Kind: "node-down", Detail: "127.0.0.1:7004 (replica) is failed"})
		}, "node-down", Warning, "7004"},
		{"cluster not ok", func(i *Input) { i.Server.Topology = topo(); i.Server.Topology.ClusterDown = true }, "cluster-not-ok", Critical, "cluster_state"},
		{"hot node", func(i *Input) {
			i.Client.NodeOps = []uint64{700_000, 150_000, 150_000}
			i.Client.NodeAddrs = []string{"n1:1", "n2:1", "n3:1"}
		}, "hot-node", Info, "3 nodes"},
		{"uneven slots", func(i *Input) {
			i.Server.Topology = topo()
			i.Server.Topology.Shards = []servermetrics.ShardInfo{{Slots: 8000, Primary: "a:1"}, {Slots: 8384, Primary: "b:1"}, {Slots: 2000, Primary: "c:1"}}
		}, "uneven-slots", Info, "c:1 owns 2000"},
		{"redirects info", func(i *Input) {
			i.Server.Mode = "cluster"
			i.Server.ErrorStats = map[string]uint64{"MOVED": 40, "ASK": 2}
		}, "redirects", Info, "MOVED 40, ASK 2"},
		{"redirects warn", func(i *Input) { i.Server.Mode = "cluster"; i.Server.ErrorStats = map[string]uint64{"MOVED": 50_000} }, "redirects", Warning, "50000"},
		{"watch conflicts info", func(i *Input) { i.Client.PerOp = []OpCount{{"WATCH_INCR", 1000, 80}} }, "watch-conflicts", Info, "80 of 1000"},
		{"watch conflicts warn", func(i *Input) { i.Client.PerOp = []OpCount{{"WATCH_INCR", 1000, 400}} }, "watch-conflicts", Warning, "400 of 1000"},
		{"blocking timeouts", func(i *Input) { i.Client.PerOp = []OpCount{{"BRPOP", 600, 450}, {"XREAD_BLOCK", 200, 150}} }, "blocking-timeouts", Info, "600 of 800"},
		{"streams dry", func(i *Input) { i.Client.PerOp = []OpCount{{"XCONSUME", 900, 850}} }, "streams-ran-dry", Info, "850 of 900"},
		{"pubsub loss", func(i *Input) {
			i.Client.PubSub = &PubSubCount{Subscribers: 4, Published: 1000, Expected: 4000, Delivered: 3800}
		}, "pubsub-loss", Warning, "3800 received"},
		{"pubsub heavy loss", func(i *Input) {
			i.Client.PubSub = &PubSubCount{Subscribers: 4, Published: 1000, Expected: 4000, Delivered: 1000}
		}, "pubsub-loss", Critical, ""},
		{"dangerous allowed", func(i *Input) { i.Client.DangerousAllowed = []string{"FLUSHALL", "CONFIG SET"} }, "dangerous-commands-allowed", Info, "FLUSHALL, CONFIG SET"},
		{"dropped arrivals warn", func(i *Input) { i.Client.Loop = "open"; i.Client.TargetOpsPerSec = 100_000; i.Client.Dropped = 20_000 }, "arrivals-dropped", Warning, "20000 dropped"},
		{"dropped arrivals critical", func(i *Input) { i.Client.Loop = "open"; i.Client.TargetOpsPerSec = 100_000; i.Client.Dropped = 500_000 }, "arrivals-dropped", Critical, ""},
		{"target missed", func(i *Input) { i.Client.TargetOpsPerSec = 200_000 }, "target-rate-missed", Warning, "50%"},
		{"tail latency", func(i *Input) { i.Client.P99 = 30 * time.Millisecond }, "tail-latency", Info, "p99"},
		{"other clients on server", func(i *Input) { i.Server.ServerOps = 1_500_000 }, "client-server-mismatch", Warning, "1500000"},
		{"commands lost", func(i *Input) { i.Server.ServerOps = 800_000 }, "client-server-mismatch", Warning, "800000"},
		{"bgsave failed", func(i *Input) { i.Server.BgsaveFailed = true }, "bgsave-failed", Warning, ""},
		{"polls failed", func(i *Input) { i.Server.PollErrors = 2; i.Server.FirstError = "SLOWLOG: NOPERM" }, "server-metrics-unavailable", Info, "NOPERM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthy()
			tc.mutate(&in)
			var got *Finding
			for _, f := range Evaluate(in) {
				if f.ID == tc.want {
					f := f
					got = &f
				}
			}
			if got == nil {
				t.Fatalf("rule %s did not fire", tc.want)
			}
			if got.Severity != tc.sev {
				t.Errorf("severity %s, want %s", got.Severity, tc.sev)
			}
			if !strings.Contains(got.Evidence, tc.evid) {
				t.Errorf("evidence %q lacks %q", got.Evidence, tc.evid)
			}
			if got.Title == "" || got.Advice == "" {
				t.Errorf("finding must explain itself: %+v", got)
			}
		})
	}
}

// Each threshold must be silent just below it.
func TestRulesQuietBelowThresholds(t *testing.T) {
	cases := map[string]func(*Input){
		"memory 89%":       func(i *Input) { i.Server.UsedMemoryMax = int64(0.89 * float64(i.Server.MaxMemory)) },
		"no maxmemory":     func(i *Input) { i.Server.MaxMemory = 0; i.Server.UsedMemoryMax = 50 << 30 },
		"hit ratio 60%":    func(i *Input) { i.Server.HitRatio = 0.6 },
		"few lookups":      func(i *Input) { i.Server.HitRatio = 0.1; i.Server.KeyspaceHits = 10; i.Server.KeyspaceMisses = 90 },
		"no lookups":       func(i *Input) { i.Server.HitRatio = -1 },
		"fork 50ms":        func(i *Input) { i.Server.MaxForkUsec = 50_000 },
		"lag under 1MiB":   func(i *Input) { i.Server.ReplicaLagBytes = 100 << 10 },
		"small dataset":    func(i *Input) { i.Server.MaxFragRatio = 5; i.Server.UsedMemoryMax = 10 << 20 },
		"cpu 60%":          func(i *Input) { i.Client.CPUCores = 4.8 },
		"target reached":   func(i *Input) { i.Client.TargetOpsPerSec = 100_000 },
		"p99 9x":           func(i *Input) { i.Client.P99 = 7 * time.Millisecond },
		"p99 under 1ms":    func(i *Input) { i.Client.P50 = 20 * time.Microsecond; i.Client.P99 = 900 * time.Microsecond },
		"counts within 5%": func(i *Input) { i.Server.ServerOps = 1_040_000 },
		"short run":        func(i *Input) { i.Client.Elapsed = time.Second; i.Server.ServerOps = 3_000_000 },
	}
	for name, mutate := range cases {
		in := healthy()
		mutate(&in)
		if fs := Evaluate(in); len(fs) != 0 {
			t.Errorf("%s: unexpected findings %s", name, ids(fs))
		}
	}
}

func TestDroppedArrivalsReplaceTheTargetMissedFinding(t *testing.T) {
	in := healthy()
	in.Client.TargetOpsPerSec = 200_000 // would normally fire target-rate-missed
	in.Client.Loop = "open"
	in.Client.Dropped = 300_000
	for _, f := range Evaluate(in) {
		if f.ID == "target-rate-missed" {
			t.Fatal("when arrivals were dropped, that finding already explains the shortfall")
		}
	}
}

func TestAFewQueuedArrivalsAtTheEndAreNotAFinding(t *testing.T) {
	in := healthy()
	in.Client.Loop = "open"
	in.Client.TargetOpsPerSec = 100_000
	in.Client.Dropped = 200 // of 1,000,000: 0.02%
	for _, f := range Evaluate(in) {
		if f.ID == "arrivals-dropped" {
			t.Fatalf("0.02%% dropped is normal at the end of an open loop: %+v", f)
		}
	}
}

func topo(ev ...servermetrics.Event) *servermetrics.Topology {
	return &servermetrics.Topology{Mode: "cluster", Events: ev}
}

func TestClusterRulesQuietWhenAllIsWell(t *testing.T) {
	in := healthy()
	in.Server.Mode = "cluster"
	in.Server.Topology = topo()
	in.Server.Topology.Shards = []servermetrics.ShardInfo{{Slots: 5461}, {Slots: 5462}, {Slots: 5461}}
	in.Client.NodeOps = []uint64{340_000, 330_000, 330_000}
	in.Server.ErrorStats = map[string]uint64{"ERR": 3, "MOVED": 0}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("unexpected findings: %s", ids(fs))
	}
	// Too few nodes or too little traffic is not a hot node.
	in.Client.NodeOps = []uint64{900_000, 100_000}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("two nodes: %s", ids(fs))
	}
	in.Client.NodeOps = []uint64{450, 25, 25}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("a short run: %s", ids(fs))
	}
}

func TestMismatchRuleSkippedWhenReplicasAreCounted(t *testing.T) {
	in := healthy()
	in.Server.ServerOps = 1_500_000 // would normally say other clients are on the server
	in.Server.ServerOpsExact = false
	for _, f := range Evaluate(in) {
		if f.ID == "client-server-mismatch" {
			t.Fatal("replicas execute replicated writes, so the counts cannot be compared exactly")
		}
	}
}

func TestAdvancedRulesQuietBelowTheirThresholds(t *testing.T) {
	in := healthy()
	in.Client.PerOp = []OpCount{{"WATCH_INCR", 1000, 30}, {"BRPOP", 400, 100}, {"XCONSUME", 500, 300}, {"WATCH_INCR2", 10, 10}}
	in.Client.PubSub = &PubSubCount{Subscribers: 4, Published: 1000, Expected: 4000, Delivered: 3990}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("unexpected findings: %s", ids(fs))
	}
	// Too few operations to say anything about a share.
	in.Client.PerOp = []OpCount{{"WATCH_INCR", 50, 40}, {"BRPOP", 50, 50}, {"XREADGROUP", 50, 50}}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("a short run: %s", ids(fs))
	}
	// Publishing with no subscribers is not a loss.
	in.Client.PubSub = &PubSubCount{Published: 1000}
	if fs := Evaluate(in); len(fs) != 0 {
		t.Fatalf("%s", ids(fs))
	}
}

func TestOrderingMostSeriousFirst(t *testing.T) {
	in := healthy()
	in.Server.MaxBlockedConns = 1
	in.Server.EvictedKeys = 5
	in.Server.RejectedConnections = 1
	fs := Evaluate(in)
	if len(fs) != 3 || fs[0].ID != "rejected-connections" || fs[1].ID != "eviction-pressure" || fs[2].ID != "blocked-clients" {
		t.Fatalf("order: %s", ids(fs))
	}
}

func TestCOAdviceMentionsQueueing(t *testing.T) {
	in := healthy()
	in.Client.TargetOpsPerSec = 500_000
	in.Client.CoordinatedOmission = true
	for _, f := range Evaluate(in) {
		if f.ID == "target-rate-missed" && strings.Contains(f.Advice, "waited to start") {
			return
		}
	}
	t.Fatal("coordinated-omission runs must explain what the latency includes")
}

func TestSeverityString(t *testing.T) {
	if Critical.String() != "critical" || Warning.String() != "warning" || Info.String() != "info" {
		t.Fatal("Severity.String")
	}
}

func TestBigKeysRule(t *testing.T) {
	in := healthy()
	if f := bigKeys(in); f != nil {
		t.Fatalf("no analysis, no finding: %+v", f)
	}
	in.KeySpace = &KeySpace{Sampled: 500, MaxBytes: 600 << 10, TopKey: "k", TopType: "hash", TopElement: 800}
	if f := bigKeys(in); f != nil {
		t.Fatalf("keys under 1 MiB are fine: %+v", f)
	}
	in.KeySpace = &KeySpace{Sampled: 500, MaxBytes: 3 << 20, TopKey: "plgv:h:7", TopType: "hash", TopElement: 90000, BigKeys: 4}
	f := bigKeys(in)
	if f == nil || f.Severity != Info || !strings.Contains(f.Title, "3.0 MiB") || !strings.Contains(f.Evidence, "plgv:h:7") || !strings.Contains(f.Evidence, "sample of 500") || !strings.Contains(f.Advice, "UNLINK") {
		t.Fatalf("%+v", f)
	}
	in.KeySpace.MaxBytes, in.KeySpace.Complete = 40<<20, true
	if f := bigKeys(in); f.Severity != Warning || !strings.Contains(f.Evidence, "all 500 keys") {
		t.Fatalf("%+v", f)
	}
	// Many elements make a key big even when its bytes are small (for example tiny members).
	in.KeySpace = &KeySpace{Sampled: 10, MaxBytes: 500 << 10, TopKey: "z", TopType: "zset", TopElement: 150000}
	if bigKeys(in) == nil {
		t.Fatal("a collection with 150000 elements is a big key")
	}
	if !strings.Contains(ids(Evaluate(in)), "big-keys") {
		t.Fatal("the rule must be registered")
	}
}
