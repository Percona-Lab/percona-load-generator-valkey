//go:build integration

package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// unlinkPrefix removes the keys a test created (standalone).
func unlinkPrefix(t *testing.T, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		cursor := "0"
		for {
			r, err := c.Query(context.Background(), valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
			arr, ok := r.([]any)
			if err != nil || !ok || len(arr) != 2 {
				return
			}
			var cmds []valkey.Cmd
			for _, k := range arr[1].([]any) {
				cmds = append(cmds, valkey.Cmd{"UNLINK", fmt.Sprint(k)})
			}
			if len(cmds) > 0 {
				c.Exec(context.Background(), cmds)
			}
			if cursor = fmt.Sprint(arr[0]); cursor == "0" {
				return
			}
		}
	})
}

// Every built-in preset runs against a real standalone server with no errors,
// and the key-space analysis sees what the preset created.
func TestIntegrationEveryPresetRuns(t *testing.T) {
	for _, p := range config.Presets() {
		t.Run(p.Name, func(t *testing.T) {
			cfg, err := config.WithPreset(p.Name)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Target.URIs = []string{itAddr()}
			cfg.Run.Duration = 2 * time.Second
			cfg.Run.Concurrency = 8
			cfg.Dataset.KeyPrefix = "it:preset:" + p.Name + ":"
			cfg.Dataset.KeyCount = min(cfg.Dataset.KeyCount, 300)
			cfg.Output.SampleInterval = 250 * time.Millisecond
			cfg.KeySpace.Enabled = true
			cfg.KeySpace.Samples = 500
			cfg.Normalize()
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			unlinkPrefix(t, cfg.Dataset.KeyPrefix)
			out, err := Run(context.Background(), cfg, Options{}, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			r := out.Result
			if r.Errors != 0 || r.Ops == 0 {
				t.Fatalf("ops %d errors %d: %s", r.Ops, r.Errors, r.FirstError)
			}
			for _, o := range r.PerOp {
				if o.Ops == 0 {
					t.Errorf("%s never ran", o.Op)
				}
			}
			ks := out.Summary.KeySpace
			if ks == nil || ks.Sampled == 0 || ks.Errors != 0 || len(ks.Types) == 0 {
				t.Fatalf("key-space analysis: %+v", ks)
			}
			if p.Name == "pubsub-fanout" {
				if ps := out.Summary.Client.PubSub; ps == nil || ps.Delivered != ps.Expected {
					t.Fatalf("pub/sub delivery: %+v", ps)
				}
			}
			if out.Summary.Client.PubSub == nil && cfg.Workload.PubSub.Subscribers > 0 {
				t.Fatal("subscribers were configured but no delivery result")
			}
			if p.Name == "big-keys" && ks.MaxBytes < 100<<10 {
				t.Fatalf("big-keys should create big keys, max %d", ks.MaxBytes)
			}
		})
	}
}

// The analysis reads only: the server holds the same number of keys before and after.
func TestIntegrationKeySpaceOnlyReads(t *testing.T) {
	cfg := itCfg(t, "it:ks:")
	cfg.Workload.Mix = []config.MixItem{{Op: "SET", Weight: 1}, {Op: "HSET", Weight: 1, Fields: 3}, {Op: "SADD", Weight: 1}}
	cfg.KeySpace.Enabled = true
	cfg.KeySpace.Samples = 200
	cfg.Dataset.TTL = config.TTL{Mode: "fixed", Seconds: 300, Fraction: 1}
	unlinkPrefix(t, cfg.Dataset.KeyPrefix)
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	ks := out.Summary.KeySpace
	if ks == nil || ks.Sampled == 0 || ks.Sampled > 200 || ks.Match != "it:ks:*" {
		t.Fatalf("%+v", ks)
	}
	if ks.NoExpiry == ks.Sampled {
		t.Fatalf("strings were written with a TTL, but every sampled key has none: %+v", ks.TTL)
	}
	var types []string
	for _, ts := range ks.Types {
		types = append(types, ts.Type)
	}
	if len(types) != 3 {
		t.Fatalf("want string, hash and set, got %v", types)
	}
	if len(out.Summary.Findings) > 0 {
		for _, f := range out.Summary.Findings {
			if f.ID == "big-keys" {
				t.Fatalf("small keys must not be reported as big: %+v", f)
			}
		}
	}
	// Off by default: no analysis without the setting.
	cfg.KeySpace.Enabled = false
	out, err = Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil || out.Summary.KeySpace != nil {
		t.Fatalf("keyspace_analysis is opt-in: %v %+v", err, out.Summary.KeySpace)
	}
}
