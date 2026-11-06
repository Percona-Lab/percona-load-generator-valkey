package servermetrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func sub(role string, ops, hits, misses int64, mem int64, samples ...float64) Summary {
	s := Summary{Version: "9.1.3", Role: role, ServerOps: ops, CommandsDelta: ops, KeyspaceHits: hits, KeyspaceMisses: misses, HitRatio: -1,
		UsedMemoryMax: mem, UsedMemoryEnd: mem, MaxMemory: 1 << 20, ErrorStats: map[string]uint64{"MOVED": 2}, SlowlogNew: 1,
		SlowlogWorst: time.Duration(ops) * time.Microsecond, SampleStride: 1, ServerOpsExact: true}
	for i, v := range samples {
		s.Samples = append(s.Samples, Sample{Offset: time.Duration(i+1) * time.Second, OpsPerSec: v, UsedMemory: mem, HitRatio: -1, ConnectedConns: 3})
	}
	return s
}

func TestMergeSummariesSumsCountersAndRecomputesRatios(t *testing.T) {
	a := sub("master", 1000, 90, 10, 100, 500, 500)
	b := sub("master", 3000, 50, 50, 300, 1500, 1500, 1500)
	a.LatencyEvents = []LatencyEvent{{Name: "fork", MaxMs: 5}}
	b.LatencyEvents = []LatencyEvent{{Name: "fork", MaxMs: 9}, {Name: "command", MaxMs: 2}}
	b.SlowlogSample = []SlowEntry{{ID: 1, Time: time.Unix(10, 0), Node: "b"}}
	a.SlowlogSample = []SlowEntry{{ID: 1, Time: time.Unix(20, 0), Node: "a"}}
	m := MergeSummaries([]Summary{a, b})
	if m.ServerOps != 4000 || m.KeyspaceHits != 140 || m.KeyspaceMisses != 60 || m.UsedMemoryMax != 400 || m.MaxMemory != 2<<20 {
		t.Fatalf("%+v", m)
	}
	if m.HitRatio < 0.69 || m.HitRatio > 0.71 {
		t.Fatalf("hit ratio is recomputed from the sums, not averaged: %v", m.HitRatio)
	}
	if m.ErrorStats["MOVED"] != 4 || m.SlowlogNew != 2 || m.SlowlogWorst != 3*time.Millisecond {
		t.Fatalf("%+v", m)
	}
	if len(m.LatencyEvents) != 2 || m.LatencyEvents[0].Name != "fork" || m.LatencyEvents[0].MaxMs != 9 {
		t.Fatalf("%+v", m.LatencyEvents)
	}
	if len(m.SlowlogSample) != 2 || m.SlowlogSample[0].Node != "a" {
		t.Fatalf("slowlog merged newest first: %+v", m.SlowlogSample)
	}
	if len(m.Samples) != 3 || m.Samples[0].OpsPerSec != 2000 || m.Samples[2].OpsPerSec != 1500 || m.Samples[0].ConnectedConns != 6 {
		t.Fatalf("series are combined by sample index (a missing sample adds nothing): %+v", m.Samples)
	}
	if got := MergeSummaries(nil); got.HitRatio != -1 {
		t.Fatalf("%+v", got)
	}
}

// fakeNodes answers INFO per node with growing counters; role by address.
type fakeNodes struct {
	mu   sync.Mutex
	cmds map[string]int64
}

func (f *fakeNodes) Nodes() []string { return []string{"n1:1", "n2:1", "n3:1"} }
func (f *fakeNodes) QueryNode(_ context.Context, addr string, c valkey.Cmd) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch c[0] {
	case "INFO":
		role := "master"
		if addr == "n3:1" {
			role = "slave"
		}
		text := fmt.Sprintf("valkey_version:9.1.3\r\nrole:%s\r\ntotal_commands_processed:%d\r\nkeyspace_hits:%d\r\nkeyspace_misses:0\r\nused_memory:100\r\n", role, f.cmds[addr], f.cmds[addr]/2)
		f.cmds[addr]++
		return text, nil
	}
	f.cmds[addr]++
	return []any{}, nil
}

func TestClusterPollsEveryNodeAndMergesPrimariesOnly(t *testing.T) {
	f := &fakeNodes{cmds: map[string]int64{}}
	c := NewCluster(f, f.Nodes(), Config{Interval: time.Hour, Latency: false, Slowlog: false}, nil, false)
	ctx := context.Background()
	for _, a := range c.order {
		c.pollers[a].start = time.Now()
		c.pollers[a].poll(ctx, time.Now())
	}
	for _, a := range c.order { // 1000 workload commands land on every node, then a second poll
		f.mu.Lock()
		f.cmds[a] += 1000
		f.mu.Unlock()
		c.pollers[a].poll(ctx, time.Now().Add(time.Second))
	}
	s := c.Summary()
	if s.Mode != "cluster" || len(s.Nodes) != 3 || !s.ServerOpsExact {
		t.Fatalf("%+v", s)
	}
	roles := map[string]string{}
	for _, n := range s.Nodes {
		roles[n.Addr] = n.Role
		if n.Summary.ServerOps != 1000 {
			t.Fatalf("%s: %d ops (its own INFO polls must be subtracted)", n.Addr, n.Summary.ServerOps)
		}
	}
	if roles["n1:1"] != "primary" || roles["n3:1"] != "replica" {
		t.Fatalf("%v", roles)
	}
	if s.ServerOps != 2000 {
		t.Fatalf("a replica also applies the writes, so only primaries add up: %d", s.ServerOps)
	}
	// With replica reads the replica's own traffic counts, and the total is no longer exact.
	c2 := NewCluster(f, f.Nodes(), Config{Interval: time.Hour}, nil, true)
	for _, a := range c2.order {
		c2.pollers[a].start = time.Now()
		c2.pollers[a].poll(ctx, time.Now())
		f.mu.Lock()
		f.cmds[a] += 500
		f.mu.Unlock()
		c2.pollers[a].poll(ctx, time.Now().Add(time.Second))
	}
	s2 := c2.Summary()
	if s2.ServerOps != 1500 || s2.ServerOpsExact {
		t.Fatalf("%d exact=%v", s2.ServerOps, s2.ServerOpsExact)
	}
}

type fakeSentinelAsker struct{ addr []any }

func (f *fakeSentinelAsker) Query(context.Context, valkey.Cmd) (any, error) { return f.addr, nil }
func (f *fakeSentinelAsker) Close()                                         {}

func TestSentinelSourceAddsTopologyToThePollersSummary(t *testing.T) {
	asker := &fakeSentinelAsker{addr: []any{"127.0.0.1", "6380"}}
	w, err := topology.NewSentinelWatcherFrom(context.Background(), []topology.Asker{asker}, "m", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{}
	ss := NewSentinelSource(New(f, Config{Interval: time.Hour}), w)
	ss.p.start = time.Now()
	ss.p.poll(context.Background(), time.Now())
	asker.addr = []any{"127.0.0.1", "6381"}
	w.Run(canceled(), time.Now()) // one last poll sees the new master
	s := ss.Summary()
	if s.Mode != "sentinel" || s.Topology == nil || s.Topology.Mode != "sentinel" || s.Version != "9.1.3" {
		t.Fatalf("%+v", s)
	}
	if len(s.Topology.Events) != 1 || s.Topology.Events[0].Kind != "failover" || !strings.Contains(s.Topology.Events[0].Detail, "6381") {
		t.Fatalf("%+v", s.Topology)
	}
	if s.Topology.Shards[0].Primary != "127.0.0.1:6381" {
		t.Fatalf("%+v", s.Topology.Shards)
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// topoSource serves CLUSTER SHARDS / INFO for a 1-shard cluster whose primary can change.
type topoSource struct {
	mu      sync.Mutex
	primary string
}

func (f *topoSource) Nodes() []string { return []string{"n1:1", "n2:1"} }
func (f *topoSource) QueryNode(ctx context.Context, _ string, c valkey.Cmd) (any, error) {
	return f.Query(ctx, c)
}
func (f *topoSource) Query(_ context.Context, c valkey.Cmd) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c[1] == "INFO" {
		return "cluster_state:ok\ncluster_slots_assigned:16384\n", nil
	}
	role := func(addr string) string {
		if addr == f.primary {
			return "master"
		}
		return "replica"
	}
	node := func(host string, port int64) map[string]any {
		return map[string]any{"id": host, "endpoint": host, "ip": host, "port": port, "role": role(host + ":1"), "health": "online"}
	}
	return []any{map[string]any{"id": "s", "slots": []any{int64(0), int64(16383)}, "nodes": []any{node("n1", 1), node("n2", 1)}}}, nil
}

func TestServerOpsNotExactAfterAFailover(t *testing.T) {
	src := &topoSource{primary: "n1:1"}
	w, err := topology.NewWatcher(context.Background(), src, 10*time.Millisecond, topology.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNodes{cmds: map[string]int64{}}
	quiet := NewCluster(f, f.Nodes(), Config{Interval: time.Hour}, w, false)
	if s := quiet.Summary(); !s.ServerOpsExact || len(s.Topology.Events) != 0 {
		t.Fatalf("before any change the total is exact: %+v", s.Topology)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, time.Now()); close(done) }()
	time.Sleep(30 * time.Millisecond)
	src.mu.Lock()
	src.primary = "n2:1" // CLUSTER FAILOVER
	src.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done
	s := NewCluster(f, f.Nodes(), Config{Interval: time.Hour}, w, false).Summary()
	if len(s.Topology.Events) != 1 || s.Topology.Events[0].Kind != "failover" {
		t.Fatalf("%+v", s.Topology)
	}
	if s.ServerOpsExact {
		t.Fatal("after a failover the roles at the end differ from the roles the operations were served under, so the sum over primaries is not exact")
	}
}
