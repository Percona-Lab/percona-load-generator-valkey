//go:build integration_cluster

package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// unlinkClusterPrefix removes a test's keys from every primary.
func unlinkClusterPrefix(t *testing.T, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: []string{clusterAddr()}, Workers: 2})
		if err != nil {
			return
		}
		defer c.Close()
		snap, err := topology.Read(context.Background(), c.(topology.Source))
		if err != nil {
			return
		}
		nq := c.(valkey.NodeQuerier)
		for _, n := range snap.Primaries() {
			cursor := "0"
			for {
				r, err := nq.QueryNode(context.Background(), n.Addr, valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
				arr, ok := r.([]any)
				if err != nil || !ok || len(arr) != 2 {
					break
				}
				for _, k := range arr[1].([]any) {
					nq.QueryNode(context.Background(), n.Addr, valkey.Cmd{"UNLINK", fmt.Sprint(k)})
				}
				if cursor = fmt.Sprint(arr[0]); cursor == "0" {
					break
				}
			}
		}
	})
}

// Every preset also runs in a real cluster with no redirects or CROSSSLOT, and the
// key-space analysis scans every primary.
func TestClusterEveryPresetRuns(t *testing.T) {
	restoreCluster(t)
	for _, p := range config.Presets() {
		t.Run(p.Name, func(t *testing.T) {
			cfg, err := config.WithPreset(p.Name)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Target.Mode = "cluster"
			cfg.Target.URIs = []string{clusterAddr()}
			cfg.Run.Duration = 2 * time.Second
			cfg.Run.Concurrency = 8
			cfg.Dataset.KeyPrefix = "it:cpreset:" + p.Name + ":"
			cfg.Dataset.KeyCount = min(cfg.Dataset.KeyCount, 300)
			cfg.Output.SampleInterval = 250 * time.Millisecond
			cfg.KeySpace.Enabled = true
			cfg.KeySpace.Samples = 600
			cfg.Normalize()
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			unlinkClusterPrefix(t, cfg.Dataset.KeyPrefix)
			out, err := Run(context.Background(), cfg, Options{}, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			r := out.Result
			if r.Errors != 0 || r.Ops == 0 {
				t.Fatalf("ops %d errors %d: %s", r.Ops, r.Errors, r.FirstError)
			}
			ks := out.Summary.KeySpace
			if ks == nil || ks.Sampled == 0 || ks.Errors != 0 {
				t.Fatalf("key-space analysis: %+v", ks)
			}
			if ks.Sources != 3 {
				t.Fatalf("the analysis must scan the 3 primaries, scanned %d", ks.Sources)
			}
		})
	}
}
