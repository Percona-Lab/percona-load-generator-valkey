//go:build integration

package servermetrics

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func itAddr() string {
	if a := os.Getenv("PLGV_IT_VALKEY_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6379"
}

func itClient(t *testing.T, lib string) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(lib, valkey.Options{Addr: itAddr(), Workers: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func itConfig(lib, prefix string) config.Config {
	cfg := config.Default()
	cfg.Target.Client = lib
	cfg.Run.Duration = 3 * time.Second
	cfg.Run.Concurrency = 16
	cfg.Run.Seed = 3
	cfg.Target.Pool.PipelineDepth = 8
	cfg.Dataset.KeyCount = 2000
	cfg.Dataset.KeyPrefix = prefix
	cfg.Output.SampleInterval = 500 * time.Millisecond
	return cfg
}

func cleanup(t *testing.T, c valkey.Client, prefix string, n int) {
	t.Cleanup(func() {
		for i := 0; i < n; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", prefix + "s:" + fmtInt(i)}})
		}
	})
}

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}

// Phase 3 exit criterion: client ops and server ops agree within tolerance.
func TestIntegrationClientAndServerOpsAgree(t *testing.T) {
	for _, lib := range []string{valkey.LibValkeyGo, valkey.LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			c := itClient(t, lib)
			cfg := itConfig(lib, "it:srv:"+lib+":")
			cleanup(t, c, cfg.Dataset.KeyPrefix, int(cfg.Dataset.KeyCount))
			ctx := context.Background()
			if err := valkey.Preload(ctx, c, cfg); err != nil {
				t.Fatal(err)
			}
			p := New(c, Config{Interval: 500 * time.Millisecond, Slowlog: true, Latency: true})
			pctx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { p.Run(pctx); close(done) }()
			res, err := valkey.Run(ctx, c, cfg)
			stop()
			<-done
			if err != nil {
				t.Fatal(err)
			}
			s := p.Summary()
			if s.Version == "" || s.PollErrors != 0 {
				t.Fatalf("poll problems: %+v", s)
			}
			diff := math.Abs(float64(s.ServerOps)-float64(res.Ops)) / float64(res.Ops)
			t.Logf("client %d ops, server %d ops, diff %.4f%%, hit ratio %.3f", res.Ops, s.ServerOps, diff*100, s.HitRatio)
			if diff > 0.02 {
				t.Fatalf("client sent %d ops but the server counted %d (%.2f%% apart)", res.Ops, s.ServerOps, diff*100)
			}
			if s.HitRatio < 0.99 {
				t.Fatalf("hit ratio %.3f after preload", s.HitRatio)
			}
			// Throughput series: server-side ops/s should match client throughput.
			var mid []float64
			for _, x := range s.Samples[2 : len(s.Samples)-2] {
				mid = append(mid, x.OpsPerSec)
			}
			var avg float64
			for _, v := range mid {
				avg += v
			}
			avg /= float64(len(mid))
			if math.Abs(avg-res.OpsPerSec())/res.OpsPerSec() > 0.10 {
				t.Fatalf("server ops/s samples average %.0f, client %.0f", avg, res.OpsPerSec())
			}
		})
	}
}

func TestIntegrationSlowlogDeltas(t *testing.T) {
	c := itClient(t, valkey.LibValkeyGo)
	ctx := context.Background()
	old, err := c.Query(ctx, valkey.Cmd{"CONFIG", "GET", "slowlog-log-slower-than"})
	if err != nil {
		t.Skipf("cannot read config: %v", err)
	}
	prev := old.(map[string]any)["slowlog-log-slower-than"].(string)
	t.Cleanup(func() { c.Exec(ctx, []valkey.Cmd{{"CONFIG", "SET", "slowlog-log-slower-than", prev}}) })

	// Entries that exist before polling starts must not be counted.
	c.Exec(ctx, []valkey.Cmd{{"CONFIG", "SET", "slowlog-log-slower-than", "0"}, {"SET", "it:slow:pre", "1"}})
	p := New(c, Config{Interval: 200 * time.Millisecond, Slowlog: true})
	p.start = time.Now()
	p.poll(ctx, time.Now()) // baseline
	for i := 0; i < 5; i++ {
		c.Exec(ctx, []valkey.Cmd{{"SET", "it:slow:k", "v"}})
	}
	p.poll(ctx, time.Now())
	c.Exec(ctx, []valkey.Cmd{{"UNLINK", "it:slow:k"}, {"UNLINK", "it:slow:pre"}})
	s := p.Summary()
	// With the threshold at 0 every command is logged, so other commands (ours
	// and other tests') appear too; what matters is new vs old.
	newSets := 0
	for _, e := range s.SlowlogSample {
		if strings.HasPrefix(e.Command, "SET it:slow:pre") {
			t.Fatalf("an entry from before polling started was counted as new: %+v", e)
		}
		if strings.HasPrefix(e.Command, "SET it:slow:k") {
			newSets++
		}
	}
	if newSets != 5 || s.SlowlogNew < 5 {
		t.Fatalf("want the 5 new SETs in the sample, got %d (SlowlogNew %d)", newSets, s.SlowlogNew)
	}
}

func TestIntegrationQueryShapesMatchAcrossClients(t *testing.T) {
	for _, lib := range []string{valkey.LibValkeyGo, valkey.LibGoRedis} {
		c := itClient(t, lib)
		ctx := context.Background()
		info, err := c.Query(ctx, valkey.Cmd{"INFO", "everything"})
		if err != nil {
			t.Fatal(err)
		}
		in := ParseInfo(info.(string))
		if in.Version() == "" || in.Int("total_commands_processed") <= 0 || in.Str("role") == "" {
			t.Fatalf("%s: INFO did not parse", lib)
		}
		cfgReply, err := c.Query(ctx, valkey.Cmd{"CONFIG", "GET", "maxmemory"})
		if _, ok := cfgReply.(map[string]any); err != nil || !ok {
			t.Fatalf("%s: CONFIG GET gave %T, want map[string]any", lib, cfgReply)
		}
		if _, err := c.Query(ctx, valkey.Cmd{"SLOWLOG", "GET", "5"}); err != nil {
			t.Fatalf("%s: %v", lib, err)
		}
		if _, err := c.Query(ctx, valkey.Cmd{"LATENCY", "LATEST"}); err != nil {
			t.Fatalf("%s: %v", lib, err)
		}
		if v, err := c.Query(ctx, valkey.Cmd{"GET", "it:definitely:missing"}); err != nil || v != nil {
			t.Fatalf("%s: missing key gave %v %v", lib, v, err)
		}
		if _, err := c.Query(ctx, valkey.Cmd{"NOSUCHCOMMAND"}); err == nil {
			t.Fatalf("%s: server error must be returned", lib)
		}
	}
}
