package config

import (
	"strings"
	"testing"
)

func TestRecordingIsOffByDefault(t *testing.T) {
	c := Default()
	if c.Record.Path != "" || c.Record.Outcomes || c.Record.Checksums {
		t.Fatalf("recording must be opt-in: %+v", c.Record)
	}
	if c.Record.Backpressure != "fail" || c.Record.Compression != "auto" || !c.Record.DataFingerprint {
		t.Fatalf("defaults: %+v", c.Record)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"valid", func(c *Config) { c.Record.Path = "r.jsonl.zst" }, ""},
		{"outcomes need a path", func(c *Config) { c.Record.Outcomes = true }, "record.path"},
		{"checksums need a path", func(c *Config) { c.Record.Checksums = true }, "record.path"},
		{"bad compression", func(c *Config) { c.Record.Path = "r"; c.Record.Compression = "lz4" }, "record.compression"},
		{"bad backpressure", func(c *Config) { c.Record.Path = "r"; c.Record.Backpressure = "drop" }, "record.backpressure"},
		{"negative limit", func(c *Config) { c.Record.Path = "r"; c.Record.MaxMB = -1 }, "record.max_mb"},
		{"sweep", func(c *Config) {
			c.Record.Path = "r"
			c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{1, 2}}
		}, "overwrite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.edit(&c)
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRecordFromYAMLAndNormalize(t *testing.T) {
	c, err := Parse([]byte("record:\n  path: /tmp/r.jsonl\n  compression: ZSTD\n  backpressure: Block\n  checksums: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if c.Record.Compression != "zstd" || c.Record.Backpressure != "block" || !c.Record.Checksums || c.Record.Path != "/tmp/r.jsonl" {
		t.Fatalf("%+v", c.Record)
	}
	if _, err := Parse([]byte("record:\n  paht: x\n")); err == nil {
		t.Fatal("a misspelled key must be rejected")
	}
}

// Recording does not change the workload, so it must not change its fingerprint.
func TestRecordDoesNotChangeTheWorkloadFingerprint(t *testing.T) {
	a, b := Default(), Default()
	b.Record = Record{Path: "x", Outcomes: true, Compression: "zstd", Backpressure: "block"}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("record settings must not be part of the workload fingerprint")
	}
}

func TestDeterministicModeValidation(t *testing.T) {
	ok := func() Config {
		c := Default()
		c.Run.Seed, c.Run.OpBudget, c.Run.Deterministic = 5, 1000, true
		return c
	}
	c := ok()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if d := Default(); d.Run.Deterministic || d.Run.OpBudget != 0 {
		t.Fatal("deterministic mode and the op budget are off by default")
	}
	cases := map[string]func(*Config){
		"run.seed":      func(c *Config) { c.Run.Seed = 0 },
		"run.op_budget": func(c *Config) { c.Run.OpBudget = 0 },
		"PUBLISH":       func(c *Config) { c.Workload.Mix = []MixItem{{Op: "PUBLISH", Weight: 1}} },
		"XCONSUME":      func(c *Config) { c.Workload.Mix = []MixItem{{Op: "XCONSUME", Weight: 1}} },
		"WATCH_INCR":    func(c *Config) { c.Workload.Mix = []MixItem{{Op: "WATCH_INCR", Weight: 1}} },
		"{now}":         func(c *Config) { c.Workload.Mix = []MixItem{{Op: "RAW", Weight: 1, Command: "SET {key} {now}"}} },
		"sweep":         func(c *Config) { c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{1, 2}} },
	}
	for want, edit := range cases {
		c := ok()
		edit(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	// A budget alone needs a closed loop and a fixed profile.
	c = Default()
	c.Run.OpBudget = 10
	c.Run.Loop, c.Run.TargetOpsPerSec = "open", 100
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "run.op_budget") {
		t.Fatalf("err = %v", err)
	}
	a, b := Default(), ok()
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("budget and deterministic flag are not part of the workload fingerprint")
	}
}
