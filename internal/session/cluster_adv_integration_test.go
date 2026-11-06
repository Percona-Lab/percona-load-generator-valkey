//go:build integration_cluster

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

func advClusterCfg(prefix string) config.Config {
	cfg := clusterCfg(prefix)
	cfg.Run.Duration = 3 * time.Second
	cfg.Dataset.KeyCount = 800
	cfg.Dataset.CollectionSize = 8
	cfg.Dataset.StreamLength = 6
	cfg.Dataset.BitmapBits = 512
	cfg.Target.Pool.PipelineDepth = 4
	return cfg
}

// The advanced operations in a real cluster: transactions and WATCH on one slot,
// streams, scripts and functions loaded on every node, blocking reads, raw
// commands routed by their key, and pub/sub across the cluster.
func TestClusterAdvancedOperations(t *testing.T) {
	restoreCluster(t)
	cfg := advClusterCfg("it:cla2:")
	cfg.Workload.Mix = []config.MixItem{
		{Op: "GET", Weight: 3}, {Op: "MGET", Weight: 1, Count: 3}, {Op: "SCAN", Weight: 1, Count: 5}, // SCAN has no key: it must not share a cluster pipeline with keyed commands
		{Op: "TXN", Weight: 3, Steps: []config.MixItem{{Op: "SET"}, {Op: "GET"}, {Op: "HSET", Fields: 2}}},
		{Op: "PIPELINE", Weight: 2, Steps: []config.MixItem{{Op: "GET"}, {Op: "INCR"}}},
		{Op: "WATCH_INCR", Weight: 2},
		{Op: "XADD", Weight: 3}, {Op: "XCONSUME", Weight: 3, Count: 3}, {Op: "XREAD", Weight: 1},
		{Op: "EVALSHA", Weight: 2, Script: "rate_limit"}, {Op: "FCALL", Weight: 2, Script: "incr_cap"}, {Op: "EVAL", Weight: 1, Script: "get_set"},
		{Op: "GEOADD", Weight: 1}, {Op: "GEOSEARCH", Weight: 1}, {Op: "PFADD", Weight: 1}, {Op: "SETBIT", Weight: 1}, {Op: "BITCOUNT", Weight: 1},
		{Op: "BRPOP", Weight: 1, Timeout: "20ms"}, {Op: "LPUSH", Weight: 3},
		{Op: "RAW", Weight: 2, Command: "GET {key}"}, {Op: "RAW", Weight: 1, Command: "HSET {key:hash} f {n}"},
		{Op: "PUBLISH", Weight: 2},
	}
	cfg.Workload.PubSub = config.PubSub{Subscribers: 3, Channels: 4}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cleanupCluster(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if r.Errors != 0 {
		for _, o := range r.PerOp {
			if o.Errors > 0 {
				t.Errorf("%s: %d of %d failed", o.Op, o.Errors, o.Ops)
			}
		}
		t.Fatalf("errors %d: %s", r.Errors, r.FirstError)
	}
	var idle []string
	for _, o := range r.PerOp {
		if o.Ops == 0 {
			idle = append(idle, o.Op)
		}
	}
	if len(idle) > 0 {
		t.Errorf("never ran: %v", idle)
	}
	srv := out.Summary.Server
	if srv.ErrorStats["MOVED"] != 0 || srv.ErrorStats["ASK"] != 0 || srv.ErrorStats["CROSSSLOT"] != 0 {
		t.Fatalf("every operation, composites and raw commands included, must be routed by slot: %v", srv.ErrorStats)
	}
	if len(out.Summary.Client.Nodes) != 3 {
		t.Fatalf("composite operations are attributed to nodes too: %+v", out.Summary.Client.Nodes)
	}
	for _, n := range out.Summary.Client.Nodes {
		if n.SharePct < 15 || n.SharePct > 55 {
			t.Errorf("%s got %.1f%%", n.Addr, n.SharePct)
		}
	}
	ps := r.PubSub
	if ps == nil || ps.Published == 0 || ps.Delivered < ps.Expected*99/100 {
		t.Fatalf("pub/sub across the cluster: %+v", ps)
	}
}

func TestClusterShardedPubSub(t *testing.T) {
	restoreCluster(t)
	cfg := advClusterCfg("it:clsp:")
	cfg.Workload.Mix = []config.MixItem{{Op: "SPUBLISH", Weight: 1}}
	cfg.Workload.PubSub = config.PubSub{Subscribers: 2, Channels: 6, Sharded: true}
	cfg.Dataset.Preload = false
	cfg.Run.Duration = 2 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	ps := out.Result.PubSub
	if out.Result.Errors != 0 || ps == nil || !ps.Sharded || ps.Published == 0 || ps.Delivered < ps.Expected*99/100 {
		t.Fatalf("errors %d (%s) %+v", out.Result.Errors, out.Result.FirstError, ps)
	}
}

func TestClusterTransactionWithoutHashTagsIsRefusedUpFront(t *testing.T) {
	cfg := advClusterCfg("it:clt:")
	cfg.Dataset.HashTagGroups = 0
	cfg.Workload.Mix = []config.MixItem{{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "GET"}, {Op: "SET"}}}}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err == nil || !strings.Contains(err.Error(), "CROSSSLOT") {
		t.Fatalf("%v", err)
	}
}
