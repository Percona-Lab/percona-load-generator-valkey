//go:build integration

package session

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func itAddr() string {
	if a := os.Getenv("PLGV_IT_VALKEY_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6379"
}

func itCfg(t *testing.T, prefix string) config.Config {
	cfg := config.Default()
	cfg.Target.URIs = []string{itAddr()}
	cfg.Run.Duration = 1500 * time.Millisecond
	cfg.Run.Concurrency = 8
	cfg.Dataset.KeyCount = 500
	cfg.Dataset.KeyPrefix = prefix
	cfg.Output.SampleInterval = 250 * time.Millisecond
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 600; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", prefix + "s:" + strconv.Itoa(i)}})
		}
	})
	return cfg
}

// A worker sweep against a real server: one stored outcome per level, tagged,
// and throughput that grows with the worker count at the low end.
func TestIntegrationWorkerSweep(t *testing.T) {
	cfg := itCfg(t, "it:sweep:")
	cfg.Run.Sweep = config.Sweep{Kind: "workers", Values: []float64{1, 4, 16}}
	var levels int
	outs, err := Sweep(context.Background(), cfg, Options{Label: "it"}, Hooks{OnLevel: func(string, int, int, string, float64) { levels++ }})
	if err != nil || len(outs) != 3 || levels != 3 {
		t.Fatalf("%v %d %d", err, len(outs), levels)
	}
	for i, o := range outs {
		if o.Result.Errors != 0 || o.Result.Ops == 0 {
			t.Fatalf("level %d: %+v", i, o.Result)
		}
		if o.Summary.Sweep.Index != i+1 || o.Summary.Sweep.Total != 3 {
			t.Fatalf("%+v", o.Summary.Sweep)
		}
		if o.Result.PeakWorkers != []int{1, 4, 16}[i] {
			t.Fatalf("level %d ran with %d workers", i, o.Result.PeakWorkers)
		}
	}
	if outs[1].Result.OpsPerSec() < 1.5*outs[0].Result.OpsPerSec() {
		t.Fatalf("4 workers (%.0f ops/s) should clearly beat 1 worker (%.0f ops/s)", outs[1].Result.OpsPerSec(), outs[0].Result.OpsPerSec())
	}
}

func TestIntegrationRateSweepServesLowRates(t *testing.T) {
	cfg := itCfg(t, "it:rate:")
	cfg.Run.Sweep = config.Sweep{Kind: "rate", Values: []float64{500, 2000}}
	outs, err := Sweep(context.Background(), cfg, Options{}, Hooks{})
	if err != nil || len(outs) != 2 {
		t.Fatalf("%v %d", err, len(outs))
	}
	for i, want := range []float64{500, 2000} {
		got := outs[i].Result.OpsPerSec()
		if got < want*0.9 || got > want*1.1 {
			t.Errorf("level %d: %.0f ops/s for a target of %.0f", i, got, want)
		}
	}
}

func TestIntegrationOpenLoopHoldsTheOfferedRate(t *testing.T) {
	cfg := itCfg(t, "it:open:")
	cfg.Run.Loop = "open"
	cfg.Run.TargetOpsPerSec = 3000
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if r.Loop != "open" || !r.CoordinatedOmission {
		t.Fatalf("%+v", r)
	}
	if got := r.OpsPerSec(); got < 2700 || got > 3300 {
		t.Fatalf("%.0f ops/s offered at 3000", got)
	}
	if r.Dropped > 50 || r.Errors != 0 {
		t.Fatalf("an unloaded server should serve everything: dropped %d errors %d", r.Dropped, r.Errors)
	}
	if out.Summary.Client.Loop != "open" {
		t.Fatal("the summary must record the loop")
	}
}

func TestIntegrationRampProfileGrowsConcurrency(t *testing.T) {
	cfg := itCfg(t, "it:ramp:")
	cfg.Run.Duration = 2 * time.Second
	cfg.Run.LoadProfile = loadprofile.Config{Kind: "ramp", StartWorkers: 1, EndWorkers: 12, RampOver: "1500ms"}
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	s := out.Result.Series
	if len(s) < 4 || s[0].Workers >= s[len(s)-1].Workers || s[len(s)-1].Workers < 10 {
		t.Fatalf("workers over time: first %d last %d", s[0].Workers, s[len(s)-1].Workers)
	}
	if out.Summary.Client.LoadProfile == "" || out.Summary.Series[len(out.Summary.Series)-1].Workers < 10 {
		t.Fatal("the summary must carry the profile and the active worker series")
	}
}
