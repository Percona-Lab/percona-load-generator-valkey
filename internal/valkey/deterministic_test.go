package valkey

import (
	"context"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func detCfg() config.Config {
	cfg := testCfg()
	cfg.Run.Seed = 21
	cfg.Run.Concurrency = 4
	cfg.Run.OpBudget = 1003 // not a multiple of the workers or the pipeline depth
	cfg.Run.Deterministic = true
	cfg.Target.Pool.PipelineDepth = 3
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 3}, {Op: "SET", Weight: 2}, {Op: "HSET", Weight: 1}, {Op: "ZADD", Weight: 1},
		{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET", Weight: 1}, {Op: "INCR", Weight: 1}}}}
	return cfg
}

func TestOpBudgetSendsExactlyThatManyOperations(t *testing.T) {
	cfg := detCfg()
	res, err := Run(context.Background(), newFake(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops != 1003 {
		t.Fatalf("ops = %d, want exactly the budget 1003", res.Ops)
	}
	if got := splitBudget(10, 4); got[0] != 3 || got[1] != 3 || got[2] != 2 || got[3] != 2 {
		t.Fatalf("split = %v", got)
	}
	if splitBudget(0, 4) != nil {
		t.Fatal("no budget, no split")
	}
}

func TestSameSeedAndBudgetGiveTheSameTrafficFingerprint(t *testing.T) {
	a, err := Run(context.Background(), newFake(), detCfg())
	if err != nil {
		t.Fatal(err)
	}
	slow := detCfg()
	slow.Run.TargetOpsPerSec = 20000 // other timing, other interleaving, other server state
	b, err := Run(context.Background(), newFake(), slow)
	if err != nil {
		t.Fatal(err)
	}
	if a.TrafficHash == "" || a.TrafficHash != b.TrafficHash || a.TrafficOps != b.TrafficOps || a.TrafficOps < 1003 {
		t.Fatalf("hash a %s (%d), b %s (%d)", a.TrafficHash, a.TrafficOps, b.TrafficHash, b.TrafficOps)
	}
	for name, edit := range map[string]func(*config.Config){
		"seed":   func(c *config.Config) { c.Run.Seed = 22 },
		"budget": func(c *config.Config) { c.Run.OpBudget = 1004 },
		"mix":    func(c *config.Config) { c.Workload.Mix[0].Weight = 5 },
		"keys":   func(c *config.Config) { c.Dataset.KeyCount = 999 },
	} {
		c := detCfg()
		edit(&c)
		r, err := Run(context.Background(), newFake(), c)
		if err != nil {
			t.Fatal(err)
		}
		if r.TrafficHash == a.TrafficHash {
			t.Fatalf("changing the %s must change the fingerprint", name)
		}
	}
}

func TestFingerprintEqualsTheRecordingsCommandsHash(t *testing.T) {
	cfg := detCfg()
	res, info, _ := recordedRun(t, newFake(), cfg, workloadrecOptions(), workloadrecHeader())
	_ = res
	// recordedRun re-paces the run; the budget still decides what is sent.
	got, err := Run(context.Background(), newFake(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.CommandsHash != got.TrafficHash {
		t.Fatalf("recording %s, fingerprint %s", info.CommandsHash, got.TrafficHash)
	}
}

func workloadrecOptions() workloadrec.Options { return workloadrec.Options{} }
func workloadrecHeader() workloadrec.Header   { return workloadrec.Header{} }

// Within one pipelined batch plain commands are sent before composite ones, so
// the pipeline depth is part of the traffic when the mix has composite
// operations (documented in docs/deterministic-mode.md).
func TestPipelineDepthIsPartOfTheTrafficForCompositeMixes(t *testing.T) {
	a := detCfg()
	b := detCfg()
	b.Target.Pool.PipelineDepth = 1
	ra, _ := Run(context.Background(), newFake(), a)
	rb, _ := Run(context.Background(), newFake(), b)
	if ra.TrafficHash == rb.TrafficHash {
		t.Fatal("expected a different order of commands with another pipeline depth")
	}
	// A mix of plain operations only is independent of the depth.
	a.Workload.Mix, b.Workload.Mix = a.Workload.Mix[:3], b.Workload.Mix[:3]
	ra, _ = Run(context.Background(), newFake(), a)
	rb, _ = Run(context.Background(), newFake(), b)
	if ra.TrafficHash != rb.TrafficHash {
		t.Fatal("plain operations are sent in generation order whatever the depth")
	}
}
