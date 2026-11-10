package webui

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

var updateUI = flag.Bool("update-ui-fixtures", false, "rewrite internal/webui/testdata/ui from the real API output")

// The DOM tests (internal/webui/uitest) run the page against these files. They
// are the real output of the handlers, so a change to the API shows up here
// first. Regenerate with:
//
//	go test ./internal/webui -run TestUIFixturesMatchAPI -update-ui-fixtures
func TestUIFixturesMatchAPI(t *testing.T) {
	dir := filepath.Join("testdata", "ui")
	got := map[string][]byte{}
	pretty := func(v any) []byte {
		b, _ := json.MarshalIndent(v, "", "  ")
		return append(b, '\n')
	}

	e := setup(t, nil)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	runs := []struct {
		label  string
		server bool
		p99    time.Duration
		ops    uint64
		edit   func(m map[string]any)
	}{
		{"cache baseline", true, 2 * time.Millisecond, 100000, nil},
		{"cache baseline", true, 2 * time.Millisecond, 101000, nil},
		{"cache baseline", true, 3 * time.Millisecond, 99000, nil},
		{"client only", false, 2 * time.Millisecond, 90000, func(m map[string]any) {
			m["workload"].(map[string]any)["mix"] = []any{map[string]any{"op": "HGET", "weight": 1}}
		}},
	}
	var ids []string
	for i, r := range runs {
		f := newFake()
		f.p99, f.ops, f.hit = r.p99, r.ops, 0.97
		f.started = base.Add(time.Duration(i) * time.Hour)
		close(f.release)
		e.srv.mgr.run = f.run
		body := e.defaultsBody(func(c map[string]any) {
			c["server_metrics"].(map[string]any)["enabled"] = r.server
			if r.edit != nil {
				r.edit(c)
			}
		})
		body["label"] = r.label
		e.do("POST", "/api/start", body, nil)
		ids = append(ids, e.waitState(StateDone)["run_id"].(string))
	}
	// Give the stored runs realistic server detail so the Server and Slowlog panels have data.
	for _, id := range ids[:3] {
		sum, _ := e.srv.hist.Get(id)
		sum.Findings = append(sum.Findings, runsummary.Finding{ID: "slow-commands", Severity: "warning", Title: "The server logged 2 slow commands",
			Evidence: "2 new SLOWLOG entries, slowest 12ms; for example: HGETALL big", Advice: "Look for O(N) commands on large collections."})
		sum.Server = &runsummary.Server{Version: "9.1.3", Role: "master", Mode: "standalone", ServerOps: int64(sum.Client.Ops), HitRatio: 0.97,
			UsedMemoryMax: 52 << 20, SlowlogNew: 2, SlowlogWorstMs: 12, SampleStride: 1,
			Slowlog:       []runsummary.SlowEntry{{ID: 4, UnixTime: base.Unix(), DurationMs: 12.4, Command: "HGETALL <b>big</b>"}},
			LatencyEvents: []runsummary.LatencyEvent{{Name: "command", LatestMs: 12, MaxMs: 12}},
			Samples: []runsummary.ServerPoint{{OffsetSeconds: 1, OpsPerSec: 50000, UsedMemory: 50 << 20, Connections: 17, HitRatio: 0.97},
				{OffsetSeconds: 2, OpsPerSec: 51000, UsedMemory: 52 << 20, Connections: 17, HitRatio: 0.97}}}
		if err := os.WriteFile(filepath.Join(e.srv.hist.dir, id+".json"), pretty(sum), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Invalidate the list cache after rewriting files.
	e.srv.hist.cache = map[string]cachedMeta{}

	_, _, cfgRaw := e.do("GET", "/api/config", nil, nil)
	got["config.json"] = []byte(cfgRaw)
	for i, id := range ids {
		_, _, raw := e.do("GET", "/api/runs/"+id, nil, nil)
		got["run_"+string(rune('a'+i))+".json"] = []byte(raw)
	}
	_, _, raw := e.do("GET", "/api/runs", nil, nil)
	got["runs.json"] = []byte(raw)
	_, _, raw = e.do("GET", "/api/compare?a="+ids[0]+"&b="+ids[2], nil, nil)
	got["compare_same.json"] = []byte(raw)
	_, _, raw = e.do("GET", "/api/compare?a="+ids[0]+"&b="+ids[3], nil, nil)
	got["compare_different.json"] = []byte(raw)
	_, _, raw = e.do("GET", "/api/compare-settings?ids="+ids[0]+","+ids[3], nil, nil)
	got["compare_settings.json"] = []byte(raw)
	_, _, raw = e.do("GET", "/api/trends", nil, nil)
	got["trends.json"] = []byte(raw)
	got["live_running.json"] = pretty(Live{State: StateRunning, Label: "cache baseline", StartedAt: base, ElapsedSec: 3, DurationSec: 10, ServerOn: true,
		Ops: 150000, Errors: 0, Misses: 12,
		Series: []runsummary.Point{{OffsetSeconds: 1, OpsPerSec: 50000, P50Ms: 0.3, P95Ms: 1.2, P99Ms: 1.8, MaxMs: 3}, {OffsetSeconds: 2, OpsPerSec: 51000, P50Ms: 0.3, P95Ms: 1.3, P99Ms: 1.9, MaxMs: 4}},
		PerOp:  []LiveOp{{Op: "GET", Ops: 120000, Misses: 12, P99Ms: 1.9}, {Op: "SET", Ops: 30000, P99Ms: 1.9}},
		Server: &LiveServer{Version: "9.1.3", Samples: []runsummary.ServerPoint{{OffsetSeconds: 1, OpsPerSec: 50000, UsedMemory: 50 << 20, Connections: 17, HitRatio: 0.97}},
			Slowlog: []runsummary.SlowEntry{}}})
	_, _, raw = setup(t, nil).do("GET", "/api/live", nil, nil)
	got["live_idle.json"] = []byte(raw)
	_, _, raw = setup(t, nil).do("POST", "/api/replay-compare", map[string]any{"files": []any{sampleReplaySummaries()[0], sampleReplaySummaries()[1]}, "tolerance": 10}, nil)
	got["replay_compare.json"] = []byte(raw)
	got["replay_summary_a.json"] = pretty(sampleReplaySummaries()[0])
	got["replay_summary_b.json"] = pretty(sampleReplaySummaries()[1])

	// A finished worker sweep and a rate sweep, stored by the real manager and API.
	se := setup(t, nil)
	fs := &fakeSweep{release: make(chan struct{}), ops: []uint64{40000, 78000, 120000, 122000},
		p99: []time.Duration{time.Millisecond, 1200 * time.Microsecond, 2500 * time.Microsecond, 6 * time.Millisecond}}
	se.srv.mgr.sweep = fs.run
	se.do("POST", "/api/start", sweepBody(se, "workers", 4, 8, 16, 32), nil)
	se.waitState(StateDone)
	_, _, raw = se.do("GET", "/api/sweeps", nil, nil)
	got["sweeps.json"] = []byte(raw)
	_, _, raw = se.do("GET", "/api/sweeps/swtest", nil, nil)
	got["sweep_workers.json"] = []byte(raw)
	_, _, raw = se.do("GET", "/api/runs", nil, nil)
	got["runs_sweep.json"] = []byte(raw)
	// A finished cluster run (with a failover) and a sentinel run, built the way a real run is.
	ce := setup(t, nil)
	for i, mode := range []string{"cluster", "sentinel"} {
		ccfg := config.Default()
		ccfg.Target.Mode = mode
		ccfg.Server.Enabled = true
		ccfg.Dataset.HashTagGroups = 16
		if mode == "sentinel" {
			ccfg.Target.Sentinel.MasterName = "mymaster"
			ccfg.Dataset.HashTagGroups = 0
		}
		sum, _ := runsummary.Build("test", ccfg, clusterResult(mode), clusterServer(mode), base.Add(time.Duration(10+i)*time.Hour))
		sum.Label = mode + " run"
		if _, err := ce.srv.hist.Save(sum); err != nil {
			t.Fatal(err)
		}
	}
	// A finished big-keys preset run with the key-space analysis.
	kcfg, err := config.WithPreset("big-keys")
	if err != nil {
		t.Fatal(err)
	}
	kcfg.KeySpace.Enabled = true
	ks := &keyscan.Result{Requested: 10000, Sampled: 200, Complete: true, Sources: 1, DBSize: 200, Match: "plgv:*",
		TotalBytes: 118 << 20, P50Bytes: 227 << 10, P99Bytes: 1400 << 10, MaxBytes: 1400 << 10, OverMiB: 50, NoExpiry: 200,
		Types: []keyscan.TypeStat{
			{Type: "list", Keys: 50, Share: 0.25, Bytes: 66 << 20, AvgBytes: 1350 << 10, MaxBytes: 1400 << 10, AvgElements: 10000, MaxElements: 10000},
			{Type: "zset", Keys: 50, Share: 0.25, Bytes: 29 << 20, AvgBytes: 598 << 10, MaxBytes: 690 << 10, AvgElements: 10000, MaxElements: 10000},
			{Type: "set", Keys: 50, Share: 0.25, Bytes: 11 << 20, AvgBytes: 227 << 10, MaxBytes: 227 << 10, AvgElements: 10000, MaxElements: 10000},
			{Type: "hash", Keys: 50, Share: 0.25, Bytes: 9 << 20, AvgBytes: 193 << 10, MaxBytes: 193 << 10, AvgElements: 1000, MaxElements: 1000}},
		Sizes: []keyscan.Bucket{{Label: "under 128 B"}, {Label: "128 B to 1 KiB"}, {Label: "1 to 10 KiB"}, {Label: "10 to 100 KiB"}, {Label: "100 KiB to 1 MiB", Keys: 150}, {Label: "1 to 10 MiB", Keys: 50}, {Label: "10 MiB or more"}},
		TTL:   []keyscan.Bucket{{Label: "no expiry", Keys: 200}, {Label: "under 1 min"}, {Label: "1 to 10 min"}, {Label: "10 min to 1 h"}, {Label: "1 h to 1 day"}, {Label: "1 day or more"}},
		Big: []keyscan.BigKey{{Key: "plgv:l:0", Type: "list", Bytes: 1400 << 10, Elements: 10000, TTLSeconds: -1}, {Key: "plgv:l:1", Type: "list", Bytes: 1400 << 10, Elements: 10000, TTLSeconds: -1},
			{Key: "plgv:z:4", Type: "zset", Bytes: 690 << 10, Elements: 10000, TTLSeconds: 3600}}}
	ksum, _ := runsummary.BuildWith("test", kcfg, clusterResult("cluster"), nil, ks, base.Add(12*time.Hour))
	ksum.Label = "big keys"
	ke := setup(t, nil) // its own store: the cluster run list must stay as it is
	if _, err := ke.srv.hist.Save(ksum); err != nil {
		t.Fatal(err)
	}
	_, _, raw = ke.do("GET", "/api/runs/20261104-220000", nil, nil)
	got["run_keyspace.json"] = []byte(raw)
	// A run that recorded the server's settings: two nodes that differ in one setting.
	scsum, _ := runsummary.BuildWith("test", kcfg, clusterResult("cluster"), nil, nil, base.Add(13*time.Hour))
	scsum.Label = "io-threads test"
	scsum.ServerConfig = &serverconfig.Snapshot{Name: "valkey-9.1-io4", Status: serverconfig.StatusOK, Differ: []string{"maxmemory"},
		Nodes: []serverconfig.Node{
			{Addr: "127.0.0.1:7001", Status: serverconfig.StatusOK, Settings: map[string]string{"io-threads": "4", "maxmemory": "0", "appendonly": "no", "requirepass": "***", "hz": "10"},
				Changed: []serverconfig.Change{{Key: "hz", Before: "10", After: "20"}}},
			{Addr: "127.0.0.1:7002", Status: serverconfig.StatusOK, Settings: map[string]string{"io-threads": "4", "maxmemory": "1073741824", "appendonly": "no", "requirepass": "***", "hz": "10"}}}}
	sce := setup(t, nil)
	if _, err := sce.srv.hist.Save(scsum); err != nil {
		t.Fatal(err)
	}
	_, _, raw = sce.do("GET", "/api/runs/20261104-230000", nil, nil)
	got["run_srvconfig.json"] = []byte(raw)
	_, _, raw = ce.do("GET", "/api/runs", nil, nil)
	got["runs_cluster.json"] = []byte(raw)
	_, _, raw = ce.do("GET", "/api/runs/20261104-200000", nil, nil)
	got["run_cluster.json"] = []byte(raw)
	_, _, raw = ce.do("GET", "/api/runs/20261104-210000", nil, nil)
	got["run_sentinel.json"] = []byte(raw)
	live := Live{State: StateRunning, Label: "cluster run", StartedAt: base, ElapsedSec: 4, DurationSec: 10, ServerOn: true, Ops: 100000, Workers: 16,
		Series: []runsummary.Point{{OffsetSeconds: 1, OpsPerSec: 50000, P50Ms: 0.3, P95Ms: 1, P99Ms: 1.5, MaxMs: 3}},
		PerOp:  []LiveOp{{Op: "GET", Ops: 80000, P99Ms: 1.5}, {Op: "SET", Ops: 20000, P99Ms: 1.5}}}
	lsrv := runsummary.BuildServer(clusterServer("cluster"))
	live.Server = &LiveServer{Version: "9.1.3", Mode: lsrv.Mode, Nodes: lsrv.Nodes, Topology: lsrv.Topology, Samples: lsrv.Samples, Slowlog: []runsummary.SlowEntry{}}
	got["live_cluster.json"] = pretty(live)

	got["live_sweep.json"] = pretty(Live{State: StateRunning, Label: "mysweep", StartedAt: base, ElapsedSec: 3, DurationSec: 10, ServerOn: false,
		Ops: 90000, Workers: 8,
		Series: []runsummary.Point{{OffsetSeconds: 1, OpsPerSec: 78000, P50Ms: 0.3, P95Ms: 0.9, P99Ms: 1.2, MaxMs: 3, Workers: 8}},
		PerOp:  []LiveOp{{Op: "GET", Ops: 72000, P99Ms: 1.2}, {Op: "SET", Ops: 18000, P99Ms: 1.2}},
		Sweep:  &LiveSweep{ID: "swtest", Kind: "workers", Index: 2, Total: 4, Value: 8, RunIDs: []string{"20261104-120000"}}})

	for k, v := range replayFixtures(t, base) {
		got[k] = v
	}
	for name, b := range got {
		p := filepath.Join(dir, name)
		if *updateUI {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("%s is not JSON: %v", name, err)
			}
			if err := os.WriteFile(p, pretty(v), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing fixture %s: run with -update-ui-fixtures", name)
		}
		var a, c any
		_ = json.Unmarshal(b, &a)
		_ = json.Unmarshal(want, &c)
		if !sameShape(a, c) {
			t.Errorf("fixture %s no longer matches the API output (same keys and types expected); regenerate with -update-ui-fixtures", name)
		}
	}
	for _, name := range []string{"config.json", "runs.json", "trends.json"} {
		if strings.Contains(string(got[name]), "password") {
			t.Errorf("%s must not mention a password", name)
		}
	}
}

// sameShape compares two decoded JSON values by structure: object keys and
// value types, ignoring the values themselves (times and measured numbers vary).
func sameShape(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameShape(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok {
			return false
		}
		if len(x) == 0 || len(y) == 0 {
			return true // an empty list has no element shape to compare
		}
		return sameShape(x[0], y[0])
	case nil:
		return b == nil
	default:
		switch b.(type) {
		case map[string]any, []any:
			return false
		}
		return typeName(a) == typeName(b)
	}
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	}
	return "other"
}

func clusterResult(mode string) *valkey.RunResult {
	h := &latency.Histogram{}
	for i := 0; i < 100; i++ {
		h.Record(time.Millisecond)
	}
	res := &valkey.RunResult{Ops: 90000, Elapsed: 10 * time.Second, CPU: time.Second, Hist: h, Seed: 1, SeriesFactor: 1, Mode: mode, Loop: "closed",
		Series: []stats.Sample{
			{Offset: time.Second, Span: time.Second, Ops: 9000, P50: time.Millisecond, P99: 2 * time.Millisecond},
			{Offset: 2 * time.Second, Span: time.Second, Ops: 9000, P50: time.Millisecond, P99: 2 * time.Millisecond},
		},
		PerOp: []valkey.OpStat{{Op: "GET", Ops: 72000}, {Op: "SET", Ops: 18000}}}
	if mode == "cluster" {
		res.NodeAddrs = []string{"127.0.0.1:7001", "127.0.0.1:7002", "127.0.0.1:7003", "127.0.0.1:7004"}
		res.PerNode = []stats.NodeSample{{Ops: 30000}, {Ops: 30000, Errors: 4}, {Ops: 30000}, {Ops: 0}}
		res.Series[0].Nodes = []stats.NodeSample{{Ops: 3000}, {Ops: 3000}, {Ops: 3000}, {Ops: 0}}
		res.Series[1].Nodes = []stats.NodeSample{{Ops: 0}, {Ops: 3000}, {Ops: 3000}, {Ops: 3000}}
	}
	return res
}

func clusterServer(mode string) *servermetrics.Summary {
	pt := []servermetrics.Sample{{Offset: time.Second, OpsPerSec: 3000, UsedMemory: 20 << 20, HitRatio: -1}, {Offset: 2 * time.Second, OpsPerSec: 3100, UsedMemory: 21 << 20, HitRatio: -1}}
	s := &servermetrics.Summary{Version: "9.1.3", Mode: mode, ServerOps: 90000, ServerOpsExact: true, HitRatio: 1, UsedMemoryMax: 60 << 20, Samples: pt}
	if mode == "cluster" {
		node := func(addr, role string, slots int, ops int64) servermetrics.NodeSummary {
			return servermetrics.NodeSummary{Addr: addr, Role: role, Slots: slots, Summary: servermetrics.Summary{ServerOps: ops, UsedMemoryMax: 20 << 20, HitRatio: 1, Samples: pt}}
		}
		s.Nodes = []servermetrics.NodeSummary{node("127.0.0.1:7001", "primary", 5461, 30000), node("127.0.0.1:7002", "primary", 5462, 30000), node("127.0.0.1:7003", "primary", 5461, 30000), node("127.0.0.1:7004", "replica", 0, 9000)}
		s.Topology = &servermetrics.Topology{Mode: "cluster",
			Shards: []servermetrics.ShardInfo{{Slots: 5461, Primary: "127.0.0.1:7004", Replicas: []string{"127.0.0.1:7001"}}, {Slots: 5462, Primary: "127.0.0.1:7002"}, {Slots: 5461, Primary: "127.0.0.1:7003"}},
			Events: []servermetrics.Event{{Offset: 4 * time.Second, Kind: "failover", Detail: "slots 0-5460: primary changed from 127.0.0.1:7001 to 127.0.0.1:7004"}}}
	} else {
		s.Topology = &servermetrics.Topology{Mode: "sentinel", Shards: []servermetrics.ShardInfo{{Primary: "127.0.0.1:6381"}},
			Events: []servermetrics.Event{{Offset: 3 * time.Second, Kind: "failover", Detail: `master "mymaster" moved from 127.0.0.1:6380 to 127.0.0.1:6381`}}}
	}
	return s
}
