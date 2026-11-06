//go:build integration_cluster

package session

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// deleteClusterKeys removes the prefix from every primary now (not at cleanup).
func deleteClusterKeys(t *testing.T, c valkey.Client, prefix string) {
	t.Helper()
	snap, err := topology.Read(context.Background(), c.(topology.Source))
	if err != nil {
		t.Fatal(err)
	}
	nq := c.(valkey.NodeQuerier)
	for _, n := range snap.Primaries() {
		cursor := "0"
		for {
			r, err := nq.QueryNode(context.Background(), n.Addr, valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
			arr, ok := r.([]any)
			if err != nil || !ok || len(arr) != 2 {
				t.Fatalf("scan %s: %v", n.Addr, err)
			}
			for _, k := range arr[1].([]any) {
				if _, err := nq.QueryNode(context.Background(), n.Addr, valkey.Cmd{"UNLINK", fmt.Sprint(k)}); err != nil {
					t.Fatal(err)
				}
			}
			if cursor = fmt.Sprint(arr[0]); cursor == "0" {
				break
			}
		}
	}
}

// Recording and replay work against a cluster: the fingerprint covers every
// primary, commands are routed by the client, and the replay reproduces the run.
func TestClusterRecordThenReplay(t *testing.T) {
	restoreCluster(t)
	prefix := fmt.Sprintf("plgv-rrc-%d:", time.Now().UnixNano())
	unlinkClusterPrefix(t, prefix)

	cfg := config.Default()
	cfg.Target.Mode = "cluster"
	cfg.Target.URIs = []string{clusterAddr()}
	cfg.Run.Concurrency = 1
	cfg.Run.Duration = time.Second
	cfg.Run.TargetOpsPerSec = 2000
	cfg.Run.Seed = 9
	cfg.Dataset.KeyCount = 400
	cfg.Dataset.KeyPrefix = prefix
	cfg.Dataset.ValueSize = 32
	cfg.Dataset.HashTagGroups = 8
	cfg.Workload.Mix = []config.MixItem{
		{Op: "GET", Weight: 4}, {Op: "SET", Weight: 3}, {Op: "INCR", Weight: 1}, {Op: "HSET", Weight: 2}, {Op: "HGETALL", Weight: 1},
		{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET", Weight: 1}, {Op: "INCR", Weight: 1}}},
	}
	cfg.Normalize()
	path := filepath.Join(t.TempDir(), "cluster.jsonl")
	cfg.Record = config.Record{Path: path, Compression: "none", Backpressure: "block", Outcomes: true, Checksums: true, DataFingerprint: true}

	out, err := Run(context.Background(), cfg, Options{Version: "it"}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Recording == nil || out.Recording.Status != workloadrec.StatusComplete || out.Recording.Ops == 0 || out.Result.Errors != 0 {
		t.Fatalf("recording = %+v, errors = %d", out.Recording, out.Result.Errors)
	}
	info, err := workloadrec.Scan(path)
	if err != nil || info.Header.Mode != "cluster" || info.Header.DataFingerprint == nil || info.Header.DataFingerprint.Sources < 3 {
		t.Fatalf("header = %+v, err = %v", info.Header, err)
	}

	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: []string{clusterAddr()}, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deleteClusterKeys(t, c, prefix)
	if err := valkey.Preload(context.Background(), c, cfg); err != nil {
		t.Fatal(err)
	}

	rcfg := config.Default()
	rcfg.Target.Mode = "cluster"
	rcfg.Target.URIs = []string{clusterAddr()}
	ro, err := Replay(context.Background(), rcfg, path, replay.Options{Speed: 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	r := ro.Result
	if !ro.Data.Match || r.Errors != 0 || !r.HashMatches || r.Ops != info.Ops {
		t.Fatalf("data %+v, errors %d (%s), hash %v, ops %d of %d", ro.Data, r.Errors, r.FirstError, r.HashMatches, r.Ops, info.Ops)
	}
	if o := r.Outcomes; o == nil || o.StatusDiffs != 0 || o.ChecksumDiffs != 0 || o.Compared != info.Ops {
		t.Fatalf("outcomes = %+v", o)
	}
}
