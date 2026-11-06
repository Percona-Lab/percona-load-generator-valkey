package runsummary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func result() *valkey.RunResult {
	h := &latency.Histogram{}
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	return &valkey.RunResult{
		Ops: 1000, Errors: 2, Misses: 10, ErrClasses: map[string]uint64{"OOM": 2}, FirstError: "SET: OOM nope",
		Elapsed: 2 * time.Second, CPU: time.Second, Hist: h, Seed: 5, SeriesFactor: 1,
		Series: []stats.Sample{{Offset: time.Second, Span: time.Second, Ops: 500, Errors: 1, P50: time.Millisecond, P99: 2 * time.Millisecond}},
		PerOp:  []valkey.OpStat{{Op: "GET", Ops: 800, P50: time.Millisecond, P99: 3 * time.Millisecond}, {Op: "SET", Ops: 200, Errors: 2}},
	}
}

func TestBuildAndRoundTrip(t *testing.T) {
	cfg := config.Default()
	cfg.Target.Username, cfg.Target.Password = "admin", "hunter2"
	srv := &servermetrics.Summary{Version: "9.1.3", ServerOps: 1000, HitRatio: 0.99, EvictedKeys: 3, MaxMemory: 1 << 20,
		Samples: []servermetrics.Sample{{Offset: time.Second, OpsPerSec: 500, HitRatio: -1}}, SampleStride: 1}
	started := time.Unix(1700000000, 0)
	s, found := Build("v-test", cfg, result(), srv, started)

	if s.SchemaVersion != SchemaVersion || s.Tool != "plgv" || s.ToolVersion != "v-test" || s.Seed != 5 {
		t.Fatalf("header %+v", s)
	}
	if s.Client.Ops != 1000 || s.Client.OpsPerSec != 500 || s.Client.CPUCores != 0.5 || s.Client.P50Ms < 0.45 || s.Client.P50Ms > 0.55 {
		t.Fatalf("client %+v", s.Client)
	}
	if !s.FinishedAt.Equal(started.Add(2 * time.Second)) {
		t.Fatalf("finished %v", s.FinishedAt)
	}
	if len(s.Series) != 1 || s.Series[0].OpsPerSec != 500 || len(s.PerOp) != 2 || s.PerOp[0].P99Ms != 3 {
		t.Fatalf("series/perop %+v %+v", s.Series, s.PerOp)
	}
	if s.Server == nil || s.Server.EvictedKeys != 3 || len(s.Server.Samples) != 1 {
		t.Fatalf("server %+v", s.Server)
	}
	if len(found) == 0 || len(s.Findings) != len(found) {
		t.Fatalf("findings %v", s.Findings)
	}

	p := filepath.Join(t.TempDir(), "run.json")
	if err := s.Write(p); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	for _, secret := range []string{"hunter2", "admin"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("summary leaked the credential %q", secret)
		}
	}
	if !json.Valid(raw) {
		t.Fatal("invalid JSON")
	}
	back, err := Read(p)
	if err != nil || back.Client.Ops != 1000 || back.Server.Version != "9.1.3" || back.Findings[0].ID == "" {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	if !strings.Contains(back.ConfigYAML, "key_count") {
		t.Fatal("config not recorded")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestBuildWithoutServerOmitsIt(t *testing.T) {
	s, _ := Build("v", config.Default(), result(), nil, time.Now())
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), `"server"`) {
		t.Fatal("server section must be absent when metrics were off")
	}
	if s.Findings == nil {
		t.Fatal("findings must be an empty list, not null")
	}
}

func TestReadErrors(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want error")
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte("{"), 0o600)
	if _, err := Read(p); err == nil {
		t.Fatal("want parse error")
	}
}

func TestBuildClusterSections(t *testing.T) {
	res := result()
	res.Mode = "cluster"
	res.NodeAddrs = []string{"127.0.0.1:7001", "127.0.0.1:7002", "127.0.0.1:7003"}
	res.PerNode = []stats.NodeSample{{Ops: 600, Errors: 1}, {Ops: 300}, {Ops: 100}}
	res.Series[0].Nodes = []stats.NodeSample{{Ops: 50}, {Ops: 30}}
	srv := &servermetrics.Summary{Version: "9.1.3", Mode: "cluster", ServerOps: 1000, ServerOpsExact: true, HitRatio: 0.9,
		Nodes: []servermetrics.NodeSummary{
			{Addr: "127.0.0.1:7001", Role: "primary", Slots: 5461, Summary: servermetrics.Summary{ServerOps: 600, UsedMemoryMax: 1 << 20, HitRatio: 0.9, Samples: []servermetrics.Sample{{Offset: time.Second, OpsPerSec: 10, HitRatio: -1}}}},
			{Addr: "127.0.0.1:7004", Role: "replica"},
		},
		Topology: &servermetrics.Topology{Mode: "cluster", ClusterDown: false,
			Shards: []servermetrics.ShardInfo{{Slots: 5461, Primary: "127.0.0.1:7001", Replicas: []string{"127.0.0.1:7004"}}},
			Events: []servermetrics.Event{{Offset: 4 * time.Second, Kind: "failover", Detail: "slots 0-5460: primary changed"}}}}
	s, found := Build("v", config.Default(), res, srv, time.Unix(1700000000, 0))
	if s.Client.TargetMode != "cluster" || len(s.Client.Nodes) != 3 {
		t.Fatalf("%+v", s.Client)
	}
	n := s.Client.Nodes[0]
	if n.Addr != "127.0.0.1:7001" || n.Ops != 600 || n.SharePct != 60 || n.Errors != 1 {
		t.Fatalf("%+v", n)
	}
	if len(s.Series[0].NodeOps) != 3 || s.Series[0].NodeOps[0] != 50 || s.Series[0].NodeOps[2] != 0 {
		t.Fatalf("%+v", s.Series[0].NodeOps)
	}
	if len(s.Server.Nodes) != 2 || s.Server.Nodes[0].Slots != 5461 || len(s.Server.Nodes[0].Samples) != 1 || !s.Server.ServerOpsExact {
		t.Fatalf("%+v", s.Server.Nodes)
	}
	tp := s.Server.Topology
	if tp == nil || tp.Mode != "cluster" || len(tp.Events) != 1 || tp.Events[0].OffsetSeconds != 4 || tp.Shards[0].Replicas[0] != "127.0.0.1:7004" {
		t.Fatalf("%+v", tp)
	}
	ids := map[string]bool{}
	for _, f := range found {
		ids[f.ID] = true
	}
	if !ids["failover-during-run"] {
		t.Fatalf("the failover must become a finding: %v", ids)
	}
	if standalone, _ := Build("v", config.Default(), result(), nil, time.Now()); standalone.Client.TargetMode != "standalone" || len(standalone.Client.Nodes) != 0 {
		t.Fatalf("%+v", standalone.Client)
	}
}

func TestBuildPubSubAndDangerousSections(t *testing.T) {
	res := result()
	res.PubSub = &valkey.PubSubResult{Subscribers: 4, Channels: 8, Published: 1000, Expected: 4000, Delivered: 3000}
	res.DeliveryHist = &latency.Histogram{}
	for i := 1; i <= 100; i++ {
		res.DeliveryHist.Record(time.Duration(i) * time.Millisecond)
	}
	res.Series[0].Delivered, res.Series[0].DeliveryP50, res.Series[0].DeliveryP99 = 40, 3*time.Millisecond, 9*time.Millisecond
	res.PerOp = []valkey.OpStat{{Op: "WATCH_INCR", Ops: 1000, Misses: 300}, {Op: "RAW", Ops: 5}}
	cfg := config.Default()
	cfg.Workload.AllowDangerous = true
	cfg.Workload.Mix = []config.MixItem{{Op: "RAW", Weight: 1, Command: "FLUSHALL"}, {Op: "RAW", Weight: 1, Command: "GET {key}"}, {Op: "RAW", Weight: 1, Command: "CONFIG SET maxmemory 1"}}
	s, found := Build("v", cfg, res, nil, time.Now())
	ps := s.Client.PubSub
	if ps == nil || ps.Delivered != 3000 || ps.DeliveryRatio != 0.75 || ps.P99Ms < 98 || ps.P99Ms > 104 || ps.Sharded {
		t.Fatalf("%+v", ps)
	}
	if s.Series[0].Delivered != 40 || s.Series[0].DeliveryP99Ms != 9 {
		t.Fatalf("%+v", s.Series[0])
	}
	if len(s.Client.Dangerous) != 2 || s.Client.Dangerous[0] != "FLUSHALL" || s.Client.Dangerous[1] != "CONFIG SET" {
		t.Fatalf("%v", s.Client.Dangerous)
	}
	ids := map[string]bool{}
	for _, f := range found {
		ids[f.ID] = true
	}
	for _, want := range []string{"pubsub-loss", "watch-conflicts", "dangerous-commands-allowed"} {
		if !ids[want] {
			t.Errorf("finding %s missing: %v", want, ids)
		}
	}
	// Without the allow flag nothing is reported, even if a blocked command were present.
	cfg.Workload.AllowDangerous = false
	if s, _ := Build("v", cfg, result(), nil, time.Now()); len(s.Client.Dangerous) != 0 || s.Client.PubSub != nil {
		t.Fatalf("%+v", s.Client)
	}
}

func TestBuildWithKeySpace(t *testing.T) {
	res := result()
	cfg := config.Default()
	s, found := Build("v", cfg, res, nil, time.Now())
	if s.KeySpace != nil {
		t.Fatal("no analysis unless it ran")
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), `"keyspace":`) {
		t.Fatal("the JSON must not mention keyspace when it did not run")
	}
	ks := &keyscan.Result{Sampled: 100, Complete: true, MaxBytes: 5 << 20, OverMiB: 2,
		Big: []keyscan.BigKey{{Key: "plgv:l:3", Type: "list", Bytes: 5 << 20, Elements: 70000}}, Types: []keyscan.TypeStat{{Type: "list", Keys: 100}}}
	s, found = BuildWith("v", cfg, res, nil, ks, time.Now())
	if s.KeySpace != ks {
		t.Fatal("the analysis is part of the summary")
	}
	has := false
	for _, f := range found {
		if f.ID == "big-keys" && strings.Contains(f.Evidence, "plgv:l:3") {
			has = true
		}
	}
	if !has {
		t.Fatalf("a 5 MiB key must raise the big-keys finding: %+v", found)
	}
	b, _ = json.Marshal(s)
	var back Summary
	if err := json.Unmarshal(b, &back); err != nil || back.KeySpace == nil || back.KeySpace.Big[0].Key != "plgv:l:3" || back.KeySpace.OverMiB != 2 {
		t.Fatalf("round trip: %v %+v", err, back.KeySpace)
	}
	// A failed analysis is recorded, not hidden, and raises nothing.
	failed := &keyscan.Result{Errors: 1, FirstError: "NOPERM", Types: []keyscan.TypeStat{}, Big: []keyscan.BigKey{}}
	s, found = BuildWith("v", cfg, res, nil, failed, time.Now())
	if s.KeySpace.FirstError != "NOPERM" {
		t.Fatal("the error must be kept")
	}
	for _, f := range found {
		if f.ID == "big-keys" {
			t.Fatal("a failed analysis has nothing to report")
		}
	}
}
