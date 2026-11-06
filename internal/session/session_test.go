package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// fakeOutcome builds the outcome a real level would return.
func fakeOutcome(cfg config.Config, label string) *Outcome {
	h := &latency.Histogram{}
	h.Record(time.Millisecond)
	res := &valkey.RunResult{Ops: 10, Elapsed: time.Second, Hist: h, Seed: 1, Loop: cfg.Run.Loop,
		PerOp: []valkey.OpStat{{Op: cfg.Workload.Mix[0].Op, Ops: 10}}}
	sum, found := runsummary.Build("t", cfg, res, nil, time.Now())
	sum.Label = label
	return &Outcome{Result: res, Summary: sum, Findings: found}
}

func sweepCfg(kind string, vals ...float64) config.Config {
	c := config.Default()
	c.Run.Sweep = config.Sweep{Kind: kind, Values: vals}
	return c
}

func TestRunRejectsInvalidConfigAndUnreachableServer(t *testing.T) {
	bad := config.Default()
	bad.Run.Concurrency = 0
	if _, err := Run(context.Background(), bad, Options{}, Hooks{}); err == nil {
		t.Fatal("want validation error")
	}
	c := config.Default()
	c.Target.URIs = []string{"127.0.0.1:1"}
	_, err := Run(context.Background(), c, Options{}, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the error should name the address: %v", err)
	}
}

func TestSweepRunsEachLevelWithItsOwnSettings(t *testing.T) {
	var seen []config.Config
	var labels []string
	runOne = func(ctx context.Context, cfg config.Config, o Options, h Hooks) (*Outcome, error) {
		seen = append(seen, cfg)
		labels = append(labels, o.Label)
		return fakeOutcome(cfg, o.Label), nil
	}
	t.Cleanup(func() { runOne = Run })

	var levels []string
	var stored []*Outcome
	cfg := sweepCfg("workers", 4, 8, 16)
	outs, err := Sweep(context.Background(), cfg, Options{Label: "mine"}, Hooks{
		OnLevel: func(_ string, i, n int, kind string, v float64) {
			levels = append(levels, kind+":"+string(rune('0'+i))+"/"+string(rune('0'+n)))
		},
		OnOutcome: func(o *Outcome) { stored = append(stored, o) },
	})
	if err != nil || len(outs) != 3 || len(stored) != 3 {
		t.Fatalf("%v %d %d", err, len(outs), len(stored))
	}
	for i, want := range []int{4, 8, 16} {
		if seen[i].Run.Concurrency != want {
			t.Errorf("level %d ran with %d workers", i, seen[i].Run.Concurrency)
		}
		if seen[i].Run.Sweep.Kind != "none" {
			t.Errorf("a level must not sweep again: %+v", seen[i].Run.Sweep)
		}
	}
	if strings.Join(labels, "|") != "mine - 4 workers|mine - 8 workers|mine - 16 workers" {
		t.Fatalf("labels %v", labels)
	}
	if strings.Join(levels, ",") != "workers:1/3,workers:2/3,workers:3/3" {
		t.Fatalf("levels %v", levels)
	}
	id := outs[0].Summary.Sweep.ID
	for i, o := range outs {
		sw := o.Summary.Sweep
		if sw == nil || sw.ID != id || sw.Index != i+1 || sw.Total != 3 || sw.Kind != "workers" {
			t.Fatalf("sweep tag %d: %+v", i, sw)
		}
	}
	if outs[2].Summary.Sweep.Value != 16 || !strings.HasPrefix(id, "sw") {
		t.Fatalf("%+v", outs[2].Summary.Sweep)
	}
	if cfg.Run.Concurrency != 64 {
		t.Fatal("the caller's config must not be changed")
	}
}

func TestRateSweepSetsTheTargetRate(t *testing.T) {
	var rates []float64
	runOne = func(ctx context.Context, cfg config.Config, o Options, h Hooks) (*Outcome, error) {
		rates = append(rates, cfg.Run.TargetOpsPerSec)
		return fakeOutcome(cfg, o.Label), nil
	}
	t.Cleanup(func() { runOne = Run })
	outs, err := Sweep(context.Background(), sweepCfg("rate", 1000, 5000), Options{}, Hooks{})
	if err != nil || len(rates) != 2 || rates[0] != 1000 || rates[1] != 5000 {
		t.Fatalf("%v %v", err, rates)
	}
	if !strings.HasPrefix(outs[0].Summary.Label, "Sweep ") || !strings.HasSuffix(outs[1].Summary.Label, " - 5000 ops/s") {
		t.Fatalf("default labels: %q %q", outs[0].Summary.Label, outs[1].Summary.Label)
	}
}

func TestSweepStopsWhenCanceledAndKeepsFinishedLevels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runOne = func(c context.Context, cfg config.Config, o Options, h Hooks) (*Outcome, error) {
		out := fakeOutcome(cfg, o.Label)
		if cfg.Run.Concurrency == 8 {
			cancel() // the user presses Stop during level 2
			out.Summary.Stopped = true
		}
		return out, nil
	}
	t.Cleanup(func() { runOne = Run })
	outs, err := Sweep(ctx, sweepCfg("workers", 4, 8, 16, 32), Options{}, Hooks{})
	if err != nil || len(outs) != 2 {
		t.Fatalf("a stopped sweep returns the levels it ran (including the interrupted one): %v %d", err, len(outs))
	}
	if !outs[1].Summary.Stopped {
		t.Fatal("the interrupted level must be marked stopped")
	}
}

func TestSweepErrorKeepsEarlierLevels(t *testing.T) {
	n := 0
	runOne = func(ctx context.Context, cfg config.Config, o Options, h Hooks) (*Outcome, error) {
		n++
		if n == 2 {
			return nil, errors.New("cannot connect")
		}
		return fakeOutcome(cfg, o.Label), nil
	}
	t.Cleanup(func() { runOne = Run })
	outs, err := Sweep(context.Background(), sweepCfg("workers", 1, 2, 3), Options{}, Hooks{})
	if len(outs) != 1 || err == nil || !strings.Contains(err.Error(), "level 2 of 3") || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("%d %v", len(outs), err)
	}
}

func TestSweepPausesBetweenLevels(t *testing.T) {
	var at []time.Time
	runOne = func(ctx context.Context, cfg config.Config, o Options, h Hooks) (*Outcome, error) {
		at = append(at, time.Now())
		return fakeOutcome(cfg, o.Label), nil
	}
	t.Cleanup(func() { runOne = Run })
	cfg := sweepCfg("workers", 1, 2, 3)
	cfg.Run.Sweep.Pause = 120 * time.Millisecond
	if _, err := Sweep(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatal(err)
	}
	if d := at[1].Sub(at[0]); d < 100*time.Millisecond {
		t.Fatalf("level 2 started %v after level 1, want the pause", d)
	}
	if d := at[2].Sub(at[1]); d < 100*time.Millisecond {
		t.Fatalf("level 3 started %v after level 2", d)
	}
}

func TestSweepNeedsASweepKindAndValidConfig(t *testing.T) {
	if _, err := Sweep(context.Background(), config.Default(), Options{}, Hooks{}); err == nil {
		t.Fatal("a config with no sweep has nothing to sweep")
	}
	if _, err := Sweep(context.Background(), sweepCfg("workers", 4), Options{}, Hooks{}); err == nil {
		t.Fatal("one level is not a sweep")
	}
}

type modeClient struct {
	reply string
	err   error
}

func (m modeClient) Exec(context.Context, []valkey.Cmd) []valkey.Result       { return nil }
func (m modeClient) Query(context.Context, valkey.Cmd) (any, error)           { return m.reply, m.err }
func (m modeClient) Close()                                                   {}
func (m modeClient) Dedicated(context.Context, func(valkey.Conn) error) error { return nil }
func (m modeClient) Subscribe(context.Context, bool, []string, func(string, string), func()) error {
	return nil
}

func TestDetectMode(t *testing.T) {
	cluster := modeClient{reply: "# Cluster\r\ncluster_enabled:1\r\n"}
	standalone := modeClient{reply: "# Cluster\r\ncluster_enabled:0\r\n"}
	ok := []struct {
		c        modeClient
		want     string
		replicas bool
		got      string
	}{
		{cluster, "auto", false, "cluster"}, {cluster, "cluster", true, "cluster"}, {standalone, "auto", false, "standalone"}, {standalone, "standalone", false, "standalone"},
	}
	for _, tc := range ok {
		got, err := detectMode(context.Background(), tc.c, tc.want, tc.replicas)
		if err != nil || got != tc.got {
			t.Errorf("%s: %q %v", tc.want, got, err)
		}
	}
	bad := map[string]struct {
		c        modeClient
		want     string
		replicas bool
	}{
		"not running in cluster mode": {standalone, "cluster", false},
		"is a cluster node":           {cluster, "standalone", false},
		"read_from_replicas needs":    {standalone, "auto", true},
		"cannot read the server's":    {modeClient{err: errors.New("boom")}, "auto", false},
	}
	for frag, tc := range bad {
		if _, err := detectMode(context.Background(), tc.c, tc.want, tc.replicas); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s: %v", frag, err)
		}
	}
}

func TestCheckClusterMix(t *testing.T) {
	c := config.Default()
	c.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 1}, {Op: "MGET", Weight: 1}}
	if err := checkClusterMix(c); err == nil || !strings.Contains(err.Error(), "CROSSSLOT") {
		t.Fatalf("%v", err)
	}
	c.Dataset.HashTagGroups = 8
	if err := checkClusterMix(c); err != nil {
		t.Fatal(err)
	}
	c.Dataset.HashTagGroups = 0
	c.Workload.Mix = []config.MixItem{{Op: "MGET", Weight: 1, Count: 1}}
	if err := checkClusterMix(c); err != nil {
		t.Fatalf("a single-key MGET is fine: %v", err)
	}
}
