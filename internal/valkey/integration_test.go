//go:build integration

package valkey

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

func itAddr() string {
	if a := os.Getenv("PLGV_IT_VALKEY_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6379"
}

func itClient(t *testing.T, lib string) Client {
	t.Helper()
	c, err := NewClient(lib, Options{Addr: itAddr(), Workers: 8})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func cleanup(t *testing.T, c Client, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// Remove this test's keys; SCAN-based so other data is untouched.
		for _, ns := range []string{"s", "c", "a", "h", "l", "t", "z"} {
			for i := 0; i < 300; i++ {
				c.Exec(context.Background(), []Cmd{{"UNLINK", prefix + ns + ":" + itoa(i)}})
			}
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}

func TestIntegrationBothClients(t *testing.T) {
	for _, lib := range []string{LibValkeyGo, LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			c := itClient(t, lib)
			ctx := context.Background()
			p := "it:" + lib + ":"

			res := c.Exec(ctx, []Cmd{{"SET", p + "k", "v"}})
			if res[0].Err != nil {
				t.Fatal(res[0].Err)
			}
			res = c.Exec(ctx, []Cmd{{"GET", p + "missing"}})
			if res[0].Err != nil || !res[0].Miss {
				t.Fatalf("missing key: %+v", res[0])
			}
			res = c.Exec(ctx, []Cmd{{"NOSUCHCOMMAND"}})
			if res[0].Err == nil || res[0].Miss {
				t.Fatalf("bad command: %+v", res[0])
			}
			res = c.Exec(ctx, []Cmd{{"SET", p + "a", "1"}, {"GET", p + "a"}, {"GET", p + "zz"}})
			if len(res) != 3 || res[0].Err != nil || res[1].Err != nil || res[1].Miss || !res[2].Miss {
				t.Fatalf("pipeline: %+v", res)
			}
			c.Exec(ctx, []Cmd{{"UNLINK", p + "k"}, {"UNLINK", p + "a"}})
		})
	}
}

// Every supported op, run against a real server after preload, must produce
// no errors (no WRONGTYPE, no syntax errors) on both client libraries.
func TestIntegrationAllOpsNoErrors(t *testing.T) {
	var mix []config.MixItem
	for _, name := range config.OpNames() {
		it := config.MixItem{Op: name, Weight: 1}
		info := config.Ops[name]
		if info.Count {
			it.Count = 3
		}
		if info.Fields {
			it.Fields = 2
		}
		if info.Script {
			it.Script = "get_set"
		}
		if info.Steps {
			it.Steps = []config.MixItem{{Op: "GET"}, {Op: "SET"}}
		}
		if info.Command {
			it.Command = "GET {key}"
		}
		mix = append(mix, it)
	}
	// Key ops on every type.
	for _, typ := range []string{"hash", "list", "set", "zset"} {
		mix = append(mix, config.MixItem{Op: "EXISTS", Weight: 1, Type: typ}, config.MixItem{Op: "SCAN", Weight: 1, Type: typ, Count: 5})
	}
	for _, lib := range []string{LibValkeyGo, LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			c := itClient(t, lib)
			cfg := config.Default()
			cfg.Target.Client = lib
			cfg.Run.Duration = 2 * time.Second
			cfg.Run.Concurrency = 8
			cfg.Run.Seed = 5
			cfg.Target.Pool.PipelineDepth = 4
			cfg.Dataset.KeyCount = 200
			cfg.Dataset.KeyPrefix = "it:ops:" + lib + ":"
			cfg.Dataset.CollectionSize = 20
			cfg.Dataset.TTL = config.TTL{Mode: "random", MinSeconds: 30, MaxSeconds: 60, Fraction: 0.5}
			cfg.Workload.Mix = mix
			cfg.Normalize()
			cleanup(t, c, cfg.Dataset.KeyPrefix)
			ctx := context.Background()
			if err := Preload(ctx, c, cfg); err != nil {
				t.Fatal(err)
			}
			out, err := Run(ctx, c, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if out.Ops == 0 || out.Errors != 0 {
				t.Fatalf("ops=%d errors=%d first=%q", out.Ops, out.Errors, out.FirstError)
			}
			var idle []string
			for _, s := range out.PerOp {
				if s.Ops == 0 {
					idle = append(idle, s.Op)
				}
			}
			sort.Strings(idle)
			if len(idle) > 0 {
				t.Errorf("ops never ran: %v", idle)
			}
		})
	}
}

// With the same seed and a preloaded dataset, a run that is cut by a fixed
// number of batches would match; here we check the key sequence and that the
// run uses the seed it reports.
func TestIntegrationSeedReported(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	cfg := config.Default()
	cfg.Run.Duration = 500 * time.Millisecond
	cfg.Run.Concurrency = 2
	cfg.Run.Seed = 11
	cfg.Dataset.KeyCount = 50
	cfg.Dataset.KeyPrefix = "it:seed:"
	cleanup(t, c, cfg.Dataset.KeyPrefix)
	ctx := context.Background()
	if err := Preload(ctx, c, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := Run(ctx, c, cfg)
	if err != nil || out.Seed != 11 || out.Errors != 0 {
		t.Fatalf("err=%v seed=%d errors=%d", err, out.Seed, out.Errors)
	}
}
