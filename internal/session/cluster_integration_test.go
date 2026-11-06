//go:build integration_cluster

package session

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func clusterAddr() string {
	if a := os.Getenv("PLGV_IT_CLUSTER_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:7001"
}

func clusterClient(t *testing.T) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: []string{clusterAddr()}, Workers: 4})
	if err != nil {
		t.Fatalf("cannot reach the cluster at %s (make valkey-cluster-up): %v", clusterAddr(), err)
	}
	t.Cleanup(c.Close)
	return c
}

func clusterCfg(prefix string) config.Config {
	cfg := config.Default()
	cfg.Target.Mode = "cluster"
	cfg.Target.URIs = []string{clusterAddr()}
	cfg.Run.Duration = 3 * time.Second
	cfg.Run.Concurrency = 16
	cfg.Target.Pool.PipelineDepth = 4
	cfg.Dataset.KeyCount = 5000
	cfg.Dataset.KeyPrefix = prefix
	cfg.Dataset.HashTagGroups = 16
	cfg.Output.SampleInterval = 250 * time.Millisecond
	cfg.Server.Enabled = true
	cfg.Server.Interval = 250 * time.Millisecond
	return cfg
}

// primaries returns the primary address of each shard, ordered by first slot.
func primaries(t *testing.T, c valkey.Client) []string {
	t.Helper()
	snap, err := topology.Read(context.Background(), c.(topology.Source))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range snap.Primaries() {
		out = append(out, p.Addr)
	}
	return out
}

// restoreCluster puts the primaries back on 7001-7003 (a test may have failed over).
func restoreCluster(t *testing.T) {
	t.Helper()
	c := clusterClient(t)
	want := map[string]bool{"127.0.0.1:7001": true, "127.0.0.1:7002": true, "127.0.0.1:7003": true}
	for attempt := 0; attempt < 10; attempt++ {
		snap, err := topology.Read(context.Background(), c.(topology.Source))
		if err != nil {
			t.Fatal(err)
		}
		bad := false
		for _, n := range snap.Nodes() {
			// A node that should lead but is a replica now takes its role back.
			if want[n.Addr] && n.Role != "primary" {
				bad = true
				if _, err := c.(valkey.NodeQuerier).QueryNode(context.Background(), n.Addr, valkey.Cmd{"CLUSTER", "FAILOVER"}); err != nil {
					t.Logf("restore: %s: %v", n.Addr, err)
				}
			}
		}
		if !bad {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("could not restore the primaries: %v", primaries(t, c))
}

func cleanupCluster(t *testing.T, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: []string{clusterAddr()}, Workers: 2})
		if err != nil {
			return
		}
		defer c.Close()
		var cmds []valkey.Cmd
		for g := 0; g < 16; g++ {
			for i := g; i < 5000; i += 16 {
				cmds = append(cmds, valkey.Cmd{"UNLINK", fmt.Sprintf("%s{%d}:s:%d", prefix, g, i)})
				if len(cmds) == 500 {
					c.Exec(context.Background(), cmds)
					cmds = cmds[:0]
				}
			}
		}
		c.Exec(context.Background(), cmds)
	})
}

// The core cluster contract: the workload is routed by slot with no redirects,
// every primary gets its share, the client's per-node counts match what the
// nodes report, and the topology is read.
func TestClusterRunRoutesBySlotAndCountsPerNode(t *testing.T) {
	restoreCluster(t)
	cfg := clusterCfg("it:cl:")
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 5}, {Op: "SET", Weight: 2}, {Op: "MGET", Weight: 2, Count: 4}, {Op: "MSET", Weight: 1, Count: 3}}
	cleanupCluster(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	r, s := out.Result, out.Summary
	if r.Errors != 0 || r.Ops == 0 || r.Mode != "cluster" || s.Client.TargetMode != "cluster" {
		t.Fatalf("errors %d (%s) ops %d mode %q", r.Errors, r.FirstError, r.Ops, r.Mode)
	}
	if len(s.Client.Nodes) != 3 {
		t.Fatalf("client nodes %+v", s.Client.Nodes)
	}
	for _, n := range s.Client.Nodes {
		if n.SharePct < 20 || n.SharePct > 47 {
			t.Errorf("%s got %.1f%% of a uniform key space", n.Addr, n.SharePct)
		}
	}
	srv := s.Server
	if srv == nil || srv.Mode != "cluster" || len(srv.Nodes) != 6 || srv.Topology == nil {
		t.Fatalf("server section %+v", srv)
	}
	// plgv's slot routing and the nodes' own counts must agree, node by node.
	server := map[string]int64{}
	roles := map[string]string{}
	for _, n := range srv.Nodes {
		server[n.Addr], roles[n.Addr] = n.ServerOps, n.Role
	}
	for _, n := range s.Client.Nodes {
		got := server[n.Addr]
		if d := float64(got-int64(n.Ops)) / float64(n.Ops); d > 0.03 || d < -0.03 {
			t.Errorf("%s: plgv sent %d, the node counted %d", n.Addr, n.Ops, got)
		}
		if roles[n.Addr] != "primary" {
			t.Errorf("%s is attributed ops but is %s", n.Addr, roles[n.Addr])
		}
	}
	if srv.ErrorStats["MOVED"] != 0 || srv.ErrorStats["ASK"] != 0 {
		t.Fatalf("a correctly routed workload gets no redirects, got %v", srv.ErrorStats)
	}
	if d := float64(srv.ServerOps-int64(r.Ops)) / float64(r.Ops); d > 0.03 || d < -0.03 || !srv.ServerOpsExact {
		t.Fatalf("cluster total: plgv %d, servers %d", r.Ops, srv.ServerOps)
	}
	if len(srv.Topology.Shards) != 3 || len(srv.Topology.Events) != 0 || srv.Topology.ClusterDown {
		t.Fatalf("topology %+v", srv.Topology)
	}
	for _, f := range s.Findings {
		if f.ID == "redirects" || f.ID == "client-server-mismatch" || f.ID == "failover-during-run" {
			t.Errorf("unexpected finding %s: %s", f.ID, f.Evidence)
		}
	}
}

func TestClusterReplicaReadsSendReadsToReplicas(t *testing.T) {
	restoreCluster(t)
	cfg := clusterCfg("it:clr:")
	cfg.Target.ReadFromReplicas = true
	cleanupCluster(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Errors != 0 {
		t.Fatalf("%d errors: %s", out.Result.Errors, out.Result.FirstError)
	}
	var primaryOps, replicaOps int64
	for _, n := range out.Summary.Server.Nodes {
		if n.Role == "replica" {
			replicaOps += n.ServerOps
		} else {
			primaryOps += n.ServerOps
		}
	}
	// 80% reads go to replicas; the replicas also apply the 20% writes.
	if replicaOps < int64(out.Result.Ops)/2 {
		t.Fatalf("replicas served %d of %d operations; reads should go to them", replicaOps, out.Result.Ops)
	}
	if primaryOps > int64(out.Result.Ops)/2 {
		t.Fatalf("primaries still served %d of %d operations", primaryOps, out.Result.Ops)
	}
	if out.Summary.Server.ServerOpsExact {
		t.Fatal("with replica reads the server total is not exact (replicated writes)")
	}
	for _, f := range out.Summary.Findings {
		if f.ID == "client-server-mismatch" {
			t.Fatal("the mismatch finding must not fire when replicas are counted")
		}
	}
}

func TestClusterAutoModeDetectsAndValidatesTheMode(t *testing.T) {
	restoreCluster(t)
	cfg := clusterCfg("it:cla:")
	cfg.Target.Mode = "auto"
	cfg.Run.Duration = time.Second
	cleanupCluster(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil || out.Result.Mode != "cluster" || len(out.Summary.Client.Nodes) != 3 {
		t.Fatalf("auto must find the cluster: %v %+v", err, out)
	}
	// Explicit standalone against a cluster node is refused with advice.
	cfg.Target.Mode = "standalone"
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil || !strings.Contains(err.Error(), "cluster node") {
		t.Fatalf("%v", err)
	}
	// Multi-key commands without hash tags are refused before any traffic (auto mode).
	cfg.Target.Mode = "auto"
	cfg.Dataset.HashTagGroups = 0
	cfg.Workload.Mix = []config.MixItem{{Op: "MGET", Weight: 1}}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil || !strings.Contains(err.Error(), "CROSSSLOT") {
		t.Fatalf("%v", err)
	}
}

func TestClusterModeAgainstAStandaloneServerIsRefused(t *testing.T) {
	cfg := clusterCfg("it:clx:")
	cfg.Target.URIs = []string{"127.0.0.1:6379"}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil || !strings.Contains(err.Error(), "not running in cluster mode") {
		t.Fatalf("%v", err)
	}
}

// Phase 6 exit criterion: a forced failover during a run. The run completes,
// the failover is marked at about the right time, the nodes' roles swap, and the
// workload keeps going on the new primary.
func TestClusterForcedFailoverDuringARun(t *testing.T) {
	restoreCluster(t)
	t.Cleanup(func() { restoreCluster(t) })
	cfg := clusterCfg("it:clf:")
	cfg.Run.Duration = 9 * time.Second
	cleanupCluster(t, cfg.Dataset.KeyPrefix)

	c := clusterClient(t)
	var failedAt time.Time
	go func() {
		time.Sleep(3500 * time.Millisecond) // the run starts after connect and preload
		failedAt = time.Now()
		// 7004 is the replica of the shard led by 7001: promote it (a graceful manual failover).
		_, _ = c.(valkey.NodeQuerier).QueryNode(context.Background(), "127.0.0.1:7004", valkey.Cmd{"CLUSTER", "FAILOVER"})
	}()
	var runStart time.Time
	out, err := Run(context.Background(), cfg, Options{}, Hooks{OnPhase: func(p string) {
		if p == PhaseRunning {
			runStart = time.Now()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	topo := out.Summary.Server.Topology
	var fo *float64
	for _, e := range topo.Events {
		if e.Kind == "failover" {
			off := e.OffsetSeconds
			fo = &off
			if !strings.Contains(e.Detail, "127.0.0.1:7004") {
				t.Errorf("the new primary is 7004: %s", e.Detail)
			}
		}
	}
	if fo == nil {
		t.Fatalf("no failover event; events %+v", topo.Events)
	}
	expected := failedAt.Sub(runStart).Seconds()
	if *fo < expected-1 || *fo > expected+3 {
		t.Errorf("failover marked at %.1fs, but it was triggered at about %.1fs into the run", *fo, expected)
	}
	found := false
	for _, f := range out.Summary.Findings {
		if f.ID == "failover-during-run" {
			found = true
		}
	}
	if !found {
		t.Error("the failover finding is missing")
	}
	// The workload survived: it was still being served at the end of the run.
	pts := out.Summary.Series
	var late uint64
	for _, p := range pts[len(pts)-4:] {
		late += uint64(p.OpsPerSec)
	}
	if late == 0 {
		t.Fatal("no operations were served after the failover")
	}
	if frac := float64(out.Result.Errors) / float64(out.Result.Ops); frac > 0.05 {
		t.Errorf("%.1f%% of operations failed across a graceful failover (first: %s)", frac*100, out.Result.FirstError)
	}
	// The slot map followed: operations are attributed to the new primary after the change.
	final := topo.Shards
	foundNew := false
	for _, sh := range final {
		if sh.Primary == "127.0.0.1:7004" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Errorf("final topology should show 7004 as a primary: %+v", final)
	}
	var onNew uint64
	for _, n := range out.Summary.Client.Nodes {
		if n.Addr == "127.0.0.1:7004" {
			onNew = n.Ops
		}
	}
	if onNew == 0 {
		t.Error("no client operations were attributed to the new primary 7004")
	}
}
