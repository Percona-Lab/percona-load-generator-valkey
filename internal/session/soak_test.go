//go:build soak

package session

import (
	"context"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

func soakDuration() time.Duration {
	if v := os.Getenv("PLGV_SOAK_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 2 * time.Minute
}

func heapAfterGC() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// One long run with everything that keeps state turned on (server metrics, a
// big series, per-op histograms, a mixed workload with pub/sub and composite
// operations): memory after GC stays flat and no goroutine is left behind.
// PLGV_SOAK_DURATION sets the length (default 2m; the plan's soak is 24h).
func TestSoakMemoryAndGoroutinesStayFlat(t *testing.T) {
	cfg, err := config.WithPreset("mixed")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Target.URIs = []string{itAddr()}
	cfg.Run.Duration = soakDuration()
	cfg.Run.Concurrency = 32
	cfg.Run.Seed = 3
	cfg.Dataset.KeyPrefix = "plgv-soak:"
	cfg.Dataset.KeyCount = 20000
	cfg.Server.Enabled = true
	cfg.Output.SampleInterval = 200 * time.Millisecond // many slices: exercises series downsampling
	cfg.Normalize()
	t.Cleanup(func() { deleteKeys(t, "plgv-soak:") })

	startG := runtime.NumGoroutine()
	var heaps []uint64
	var peakG int
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(soakDuration() / 24)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				heaps = append(heaps, heapAfterGC())
				if g := runtime.NumGoroutine(); g > peakG {
					peakG = g
				}
			}
		}
	}()
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d ops in %v (%.0f ops/s), %d series points, %d heap samples, peak goroutines %d", out.Result.Ops, out.Result.Elapsed, out.Result.OpsPerSec(), len(out.Result.Series), len(heaps), peakG)
	if out.Result.Errors != 0 {
		t.Fatalf("errors: %d (%s)", out.Result.Errors, out.Result.FirstError)
	}
	if len(heaps) < 9 {
		t.Fatalf("only %d samples: lengthen PLGV_SOAK_DURATION", len(heaps))
	}
	third := len(heaps) / 3
	median := func(h []uint64) uint64 {
		c := append([]uint64(nil), h...)
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		return c[len(c)/2]
	}
	early, late := median(heaps[third:2*third]), median(heaps[2*third:])
	t.Logf("heap after GC: early median %d KiB, late median %d KiB", early>>10, late>>10)
	if late > early+early/2+8<<20 {
		t.Fatalf("memory grows: early %d KiB, late %d KiB", early>>10, late>>10)
	}
	time.Sleep(500 * time.Millisecond)
	if g := runtime.NumGoroutine(); g > startG+3 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines %d before, %d after the run:\n%s", startG, g, buf[:n])
	}
}

// Many short runs in one process (what the web UI does) leave nothing behind.
func TestSoakManyRunsLeakNothing(t *testing.T) {
	cfg, _ := config.WithPreset("pubsub-fanout")
	cfg.Target.URIs = []string{itAddr()}
	cfg.Run.Duration = 700 * time.Millisecond
	cfg.Run.Concurrency = 8
	cfg.Run.Seed = 4
	cfg.Dataset.KeyPrefix = "plgv-soak2:"
	cfg.Dataset.KeyCount = 500
	cfg.Server.Enabled = true
	cfg.KeySpace.Enabled = true
	cfg.KeySpace.Samples = 200
	cfg.Normalize()
	t.Cleanup(func() { deleteKeys(t, "plgv-soak2:") })
	// Warm up once so lazily created runtime goroutines are not counted.
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	g0, h0 := runtime.NumGoroutine(), heapAfterGC()
	for i := 0; i < 25; i++ {
		if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	time.Sleep(500 * time.Millisecond)
	g1, h1 := runtime.NumGoroutine(), heapAfterGC()
	t.Logf("goroutines %d -> %d, heap %d KiB -> %d KiB after 25 runs", g0, g1, h0>>10, h1>>10)
	if g1 > g0+3 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines %d -> %d:\n%s", g0, g1, buf[:n])
	}
	if h1 > h0+h0/2+8<<20 {
		t.Fatalf("heap grew from %d to %d KiB", h0>>10, h1>>10)
	}
}
