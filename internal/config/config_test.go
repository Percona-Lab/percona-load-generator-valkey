package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
)

func TestDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Target.Addr(); a != "127.0.0.1:6379" {
		t.Fatalf("addr %q", a)
	}
}

func TestParseOverlaysDefaultsAndNormalizes(t *testing.T) {
	c, err := Parse([]byte(`
run:
  duration: 30s
  seed: 7
dataset:
  key_count: 1_000
  key_distribution: zipf
  ttl: {mode: random, min_seconds: 10, max_seconds: 20, fraction: 0.5}
workload:
  mix:
    - {op: get, weight: 70}
    - {op: hset, weight: 30, fields: 4}
    - {op: del, weight: 1, type: Hash}
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Run.Duration != 30*time.Second || c.Run.Seed != 7 || c.Run.Concurrency != 64 {
		t.Fatalf("run: %+v", c.Run)
	}
	if c.Dataset.KeyCount != 1000 || c.Dataset.ValueSize != 256 {
		t.Fatalf("dataset: %+v", c.Dataset)
	}
	if c.Workload.Mix[0].Op != "GET" || c.Workload.Mix[2].Type != "hash" {
		t.Fatalf("mix not normalized: %+v", c.Workload.Mix)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyFileIsDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil || c.Run.Concurrency != 64 {
		t.Fatalf("%v %+v", err, c.Run)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	if _, err := Parse([]byte("run:\n  durration: 5s\n")); err == nil || !strings.Contains(err.Error(), "durration") {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse([]byte("workload:\n  presets: cache\n")); err == nil {
		t.Fatal("a misspelled key must be rejected")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	c := Default()
	c.Run.Duration = 90 * time.Second
	c.Workload.Mix = []MixItem{{Op: "LRANGE", Weight: 5, Count: 20}, {Op: "SET", Weight: 1}}
	c.Target.Password = "secret"
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "secret") {
		t.Fatal("password must never be written to the file")
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got.Target.Password = c.Target.Password
	if !reflect.DeepEqual(got.Run, c.Run) || got.Dataset != c.Dataset || len(got.Workload.Mix) != 2 || !reflect.DeepEqual(got.Workload.Mix[0], c.Workload.Mix[0]) {
		t.Fatalf("round trip differs:\n%+v\n%+v", got, c)
	}
}

func TestApplyEnv(t *testing.T) {
	c := Default()
	env := map[string]string{"PLGV_URI": "valkey://h:1234", "PLGV_USERNAME": "u", "PLGV_PASSWORD": "p"}
	c.ApplyEnv(func(k string) string { return env[k] })
	if a, _ := c.Target.Addr(); a != "h:1234" || c.Target.Username != "u" || c.Target.Password != "p" {
		t.Fatalf("%+v", c.Target)
	}
	c2 := Default()
	c2.ApplyEnv(func(k string) string { return map[string]string{"PLGV_ADDR": "x:1"}[k] })
	if a, _ := c2.Target.Addr(); a != "x:1" {
		t.Fatalf("legacy PLGV_ADDR: %q", a)
	}
}

func TestAddrForms(t *testing.T) {
	ok := map[string]string{"h:1": "h:1", "valkey://h:2": "h:2", "redis://h:3": "h:3", "valkeys://h:4": "h:4", "rediss://h:5": "h:5"}
	for in, want := range ok {
		got, err := Target{URIs: []string{in}}.Addr()
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	bad := []string{"http://h:1", "valkey://u:p@h:1", "h", "valkey://:1"}
	for _, in := range bad {
		if _, err := (Target{URIs: []string{in}}).Addr(); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
	two, err := Target{URIs: []string{"a:1", "valkey://b:2"}}.Addrs()
	if err != nil || len(two) != 2 || two[0] != "a:1" || two[1] != "b:2" {
		t.Errorf("several addresses: %v %v", two, err)
	}
	if _, err := (Target{URIs: []string{"a:1", "http://b:2"}}).Addrs(); err == nil || !strings.Contains(err.Error(), "uris[1]") {
		t.Errorf("the bad one must be named: %v", err)
	}
	if _, err := (Target{}).Addrs(); err == nil {
		t.Error("no address")
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := Default()
	c.Run.Concurrency = 0
	c.Dataset.KeyDistribution = "zipf"
	c.Dataset.ZipfS = 0.5
	c.Dataset.TTL.Mode = "random"
	c.Dataset.TTL.MinSeconds = 50
	c.Dataset.TTL.MaxSeconds = 10
	c.Target.Mode = "bogus"
	c.Workload.Mix = []MixItem{
		{Op: "NOPE", Weight: 1},
		{Op: "GET", Weight: 0},
		{Op: "GET", Weight: 1, Count: 3},
		{Op: "SET", Weight: 1, Fields: 2},
		{Op: "HSET", Weight: 1, Fields: 99},
		{Op: "DEL", Weight: 1, Type: "bogus"},
		{Op: "GET", Weight: 1, Type: "string"},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, frag := range []string{"run.concurrency", "zipf_s", "dataset.ttl", "target.mode", "mix[0].op", "mix[1].weight",
		"mix[2].count", "mix[3].fields", "mix[4].fields", "mix[5].type", "mix[6].type"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("missing %q in:\n%v", frag, err)
		}
	}
}

func TestEmptyMixRejected(t *testing.T) {
	c := Default()
	c.Workload.Mix = nil
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "workload.mix") {
		t.Fatalf("got %v", err)
	}
}

func TestUsedTypes(t *testing.T) {
	u := UsedTypes([]MixItem{{Op: "GET"}, {Op: "INCR"}, {Op: "HGET"}, {Op: "DEL", Type: "zset"}, {Op: "APPEND"}})
	if !u["string"] || !u["hash"] || !u["zset"] || u["list"] || len(u) != 3 {
		t.Fatalf("%v", u)
	}
	// INCR and APPEND alone need no preloaded strings.
	if len(UsedTypes([]MixItem{{Op: "INCR"}, {Op: "APPEND"}})) != 0 {
		t.Fatal("counter/append keys are created on demand")
	}
}

func TestExampleConfigsAreValid(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.yaml")
	if len(files) == 0 {
		t.Fatal("no example configs found")
	}
	for _, f := range files {
		c, err := Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestNewSettingsValidation(t *testing.T) {
	c := Default()
	c.Run.CoordinatedOmission = true // no target rate
	c.Run.TargetOpsPerSec = -1
	c.Output.SampleInterval = 10 * time.Millisecond
	c.Server.Interval = 0
	c.Server.SlowlogEntries = 0
	err := c.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, frag := range []string{"run.coordinated_omission", "run.target_ops_per_sec", "output.sample_interval", "server_metrics.interval", "server_metrics.slowlog_entries"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("missing %q in:\n%v", frag, err)
		}
	}
	c = Default()
	c.Run.TargetOpsPerSec = 5000
	c.Run.CoordinatedOmission = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestServerMetricsOffByDefault(t *testing.T) {
	c := Default()
	if c.Server.Enabled || c.Run.CoordinatedOmission || c.Run.TargetOpsPerSec != 0 || c.Output.SummaryFile != "" {
		t.Fatalf("opt-in features must default off: %+v %+v %+v", c.Server, c.Run, c.Output)
	}
}

func TestCredentialsNeverInJSONOrYAML(t *testing.T) {
	c := Default()
	c.Target.Username, c.Target.Password = "u-secret", "p-secret"
	y, _ := c.Marshal()
	j, _ := json.Marshal(c)
	for _, b := range [][]byte{y, j} {
		if strings.Contains(string(b), "-secret") {
			t.Fatalf("credential leaked: %s", b)
		}
	}
}

func TestMapFormRoundTripAndStrictness(t *testing.T) {
	c := Default()
	c.Run.Duration = 90 * time.Second
	c.Workload.Mix = []MixItem{{Op: "HSET", Weight: 3, Fields: 2}, {Op: "GET", Weight: 7}}
	m, err := c.ToMap()
	if err != nil {
		t.Fatal(err)
	}
	if m["run"].(map[string]any)["duration"] != "1m30s" {
		t.Fatalf("duration should be a string in the map form: %v", m["run"])
	}
	raw, _ := json.Marshal(m)
	back, err := DecodeJSONMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMap(back)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Run, c.Run) || got.Dataset != c.Dataset || !reflect.DeepEqual(got.Workload.Mix[0], c.Workload.Mix[0]) {
		t.Fatalf("round trip differs: %+v", got)
	}
	// Large integers must survive JSON (no 1e+06 float forms).
	back["dataset"].(map[string]any)["key_count"] = json.Number("1000000")
	got, err = ParseMap(back)
	if err != nil || got.Dataset.KeyCount != 1000000 {
		t.Fatalf("%v %d", err, got.Dataset.KeyCount)
	}
	back["run"].(map[string]any)["nope"] = 1
	if _, err := ParseMap(back); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestFingerprint(t *testing.T) {
	a := Default()
	b := Default()
	b.Run.Duration = time.Hour
	b.Run.Seed = 99
	b.Target.URIs = []string{"valkey://other:6379"}
	b.Server.Enabled = true
	b.Output.SummaryFile = "x.json"
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("duration, seed, server, output and metrics must not change the fingerprint")
	}
	b.Workload.Mix = []MixItem{{Op: "GET", Weight: 1}}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("a different mix must change the fingerprint")
	}
	c := Default()
	c.Dataset.ValueSize = 1024
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("dataset changes must change the fingerprint")
	}
}

func TestRunShapeValidation(t *testing.T) {
	bad := func(edit func(*Config), want ...string) {
		t.Helper()
		c := Default()
		edit(&c)
		err := c.Validate()
		if err == nil {
			t.Fatalf("want errors containing %v", want)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("missing %q in:\n%v", w, err)
			}
		}
	}
	bad(func(c *Config) { c.Run.Loop = "sideways" }, "run.loop: unknown")
	bad(func(c *Config) { c.Run.Loop = "open" }, "run.loop: open needs run.target_ops_per_sec")
	bad(func(c *Config) { c.Run.LoadProfile.Kind = "wobble" }, "run.load_profile")
	bad(func(c *Config) {
		c.Run.LoadProfile = loadprofile.Config{Kind: "ramp", StartWorkers: 1, EndWorkers: 8, RampOver: "10s"}
		c.Run.TargetOpsPerSec = 1000
	},
		"run.target_ops_per_sec: pacing a changing number of workers needs run.loop: open")
	bad(func(c *Config) { c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{4}} }, "run.sweep.values: give 2 to 8 levels")
	bad(func(c *Config) { c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{4, 4, 2.5, 0}} }, "listed twice", "whole number")
	bad(func(c *Config) { c.Run.Sweep = Sweep{Kind: "rate", Values: []float64{100, -5}} }, "target rate must be > 0")
	bad(func(c *Config) {
		c.Run.Sweep = Sweep{Kind: "rate", Values: []float64{100, 200}}
		c.Run.TargetOpsPerSec = 50
	}, "leave it at 0")
	bad(func(c *Config) {
		c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{1, 2}}
		c.Run.LoadProfile = loadprofile.Config{Kind: "sine", MinWorkers: 1, MaxWorkers: 4, Period: "10s"}
	}, "a sweep runs fixed levels")
	bad(func(c *Config) { c.Run.Sweep = Sweep{Kind: "nope"} }, "run.sweep.kind: unknown")

	ok := Default()
	ok.Run.LoadProfile = loadprofile.Config{Kind: "ramp", StartWorkers: 1, EndWorkers: 8, RampOver: "10s"}
	ok.Run.Loop = "open"
	ok.Run.TargetOpsPerSec = 5000
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	sw := Default()
	sw.Run.Sweep = Sweep{Kind: "workers", Values: []float64{4, 8, 16}, Pause: time.Second}
	if err := sw.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRunShapeDefaultsOffAndRoundTrip(t *testing.T) {
	c := Default()
	if c.Run.Loop != "closed" || c.Run.LoadProfile.Kind != "fixed" || c.Run.Sweep.Kind != "none" {
		t.Fatalf("opt-in features must default off: %+v", c.Run)
	}
	c.Run.Loop = "open"
	c.Run.TargetOpsPerSec = 1000
	c.Run.LoadProfile = loadprofile.Config{Kind: "step", Steps: []loadprofile.Stage{{Workers: 2, Duration: "5s"}, {Workers: 8, Duration: "5s"}}}
	c.Run.Sweep = Sweep{Kind: "workers", Values: []float64{2, 4}}
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !reflect.DeepEqual(got.Run, c.Run) {
		t.Fatalf("%v\n%+v\n%+v", err, got.Run, c.Run)
	}
	// Sweep levels are not part of a run's fingerprint.
	a, b := Default(), Default()
	b.Run.Sweep = Sweep{Kind: "workers", Values: []float64{1, 2}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("the sweep block must not change the fingerprint")
	}
	b.Run.LoadProfile = loadprofile.Config{Kind: "ramp", StartWorkers: 1, EndWorkers: 2, RampOver: "5s"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("a different load profile is a different workload")
	}
}

func TestClusterAndSentinelSettingsValidation(t *testing.T) {
	bad := func(edit func(*Config), want ...string) {
		t.Helper()
		c := Default()
		edit(&c)
		err := c.Validate()
		if err == nil {
			t.Fatalf("want errors containing %v", want)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("missing %q in:\n%v", w, err)
			}
		}
	}
	bad(func(c *Config) { c.Target.Mode = "standalone"; c.Target.URIs = []string{"a:1", "b:2"} }, "exactly one address")
	bad(func(c *Config) { c.Target.Mode = "sentinel"; c.Target.URIs = []string{"s:26379"} }, "target.sentinel.master_name: required")
	bad(func(c *Config) { c.Target.Sentinel.MasterName = "m" }, "only used with mode sentinel")
	bad(func(c *Config) { c.Target.Mode = "standalone"; c.Target.ReadFromReplicas = true }, "read_from_replicas: needs a cluster or sentinel")
	bad(func(c *Config) { c.Target.Mode = "cluster"; c.Target.Client = "go-redis" }, "go-redis supports standalone targets only")
	bad(func(c *Config) { c.Dataset.HashTagGroups = -1 }, "hash_tag_groups: must be >= 0")
	bad(func(c *Config) { c.Dataset.HashTagGroups = 500; c.Dataset.KeyCount = 100 }, "exceed dataset.key_count")
	bad(func(c *Config) {
		c.Target.Mode = "cluster"
		c.Workload.Mix = []MixItem{{Op: "MGET", Weight: 1}, {Op: "MSET", Weight: 1, Count: 3}, {Op: "GET", Weight: 1}}
	}, "workload.mix[0]: MGET touches several keys", "CROSSSLOT", "workload.mix[1]: MSET")

	ok := Default()
	ok.Target.Mode = "cluster"
	ok.Target.URIs = []string{"127.0.0.1:7001", "127.0.0.1:7002"}
	ok.Target.ReadFromReplicas = true
	ok.Dataset.HashTagGroups = 16
	ok.Workload.Mix = []MixItem{{Op: "MGET", Weight: 1}, {Op: "GET", Weight: 1}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	one := Default()
	one.Target.Mode = "cluster"
	one.Workload.Mix = []MixItem{{Op: "MGET", Weight: 1, Count: 1}}
	if err := one.Validate(); err != nil {
		t.Fatalf("a single-key MGET is fine in a cluster: %v", err)
	}
	sen := Default()
	sen.Target.Mode = "sentinel"
	sen.Target.URIs = []string{"127.0.0.1:26379", "127.0.0.1:26380"}
	sen.Target.Sentinel.MasterName = "mymaster"
	if err := sen.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClusterSettingsDefaultOff(t *testing.T) {
	c := Default()
	if c.Target.Mode != "auto" || c.Target.ReadFromReplicas || c.Dataset.HashTagGroups != 0 || c.Target.Sentinel.MasterName != "" {
		t.Fatalf("%+v %+v", c.Target, c.Dataset)
	}
}

func advCfg(mix ...MixItem) Config {
	c := Default()
	c.Workload.Mix = mix
	return c
}

func mustFail(t *testing.T, c Config, want ...string) {
	t.Helper()
	err := c.Validate()
	if err == nil {
		t.Fatalf("want errors containing %v", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in:\n%v", w, err)
		}
	}
}

func TestAdvancedOpsAreAccepted(t *testing.T) {
	c := advCfg(
		MixItem{Op: "XADD", Weight: 1, Fields: 2}, MixItem{Op: "XRANGE", Weight: 1, Count: 5}, MixItem{Op: "XCONSUME", Weight: 1, Count: 3},
		MixItem{Op: "XREAD_BLOCK", Weight: 1, Timeout: "500ms"}, MixItem{Op: "BRPOP", Weight: 1, Timeout: "1s"},
		MixItem{Op: "GEOADD", Weight: 1}, MixItem{Op: "GEOSEARCH", Weight: 1, Count: 5}, MixItem{Op: "PFADD", Weight: 1}, MixItem{Op: "SETBIT", Weight: 1}, MixItem{Op: "BITCOUNT", Weight: 1},
		MixItem{Op: "EVAL", Weight: 1, Script: "rate_limit"}, MixItem{Op: "EVALSHA", Weight: 1, Source: "return redis.call('GET', KEYS[1])"}, MixItem{Op: "FCALL", Weight: 1, Script: "get_set"},
		MixItem{Op: "TXN", Weight: 1, Steps: []MixItem{{Op: "GET"}, {Op: "SET"}, {Op: "HSET", Fields: 2}}},
		MixItem{Op: "PIPELINE", Weight: 1, Steps: []MixItem{{Op: "GET"}, {Op: "GET"}}}, MixItem{Op: "WATCH_INCR", Weight: 1},
		MixItem{Op: "RAW", Weight: 1, Command: "GET {key}"}, MixItem{Op: "DEL", Weight: 1, Type: "stream"})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := UsedTypes(c.Workload.Mix); !got["stream"] || !got["geo"] || !got["hll"] || !got["bitmap"] || !got["string"] || !got["hash"] {
		t.Fatalf("used types: %v", got)
	}
	if len(UsedTypes([]MixItem{{Op: "EVAL"}, {Op: "PUBLISH"}, {Op: "WATCH_INCR"}, {Op: "RAW", Command: "FLUSHALL"}})) != 0 {
		t.Fatal("scripts, publishing, WATCH_INCR and raw commands without {key} need no preloaded data")
	}
	if !UsedTypes([]MixItem{{Op: "RAW", Command: "HGET {key:hash} f"}})["string"] {
		t.Fatal("a raw {key} placeholder draws from the string keys")
	}
}

func TestAdvancedOpParameterValidation(t *testing.T) {
	mustFail(t, advCfg(MixItem{Op: "GET", Weight: 1, Timeout: "1s"}), "mix[0].timeout: not valid for GET")
	mustFail(t, advCfg(MixItem{Op: "BRPOP", Weight: 1, Timeout: "soon"}), "timeout: want a duration")
	mustFail(t, advCfg(MixItem{Op: "BRPOP", Weight: 1, Timeout: "5ms"}), "timeout: want a duration")
	mustFail(t, advCfg(MixItem{Op: "EVAL", Weight: 1}), "EVAL needs script")
	mustFail(t, advCfg(MixItem{Op: "EVAL", Weight: 1, Script: "nope"}), `unknown "nope"`)
	mustFail(t, advCfg(MixItem{Op: "EVAL", Weight: 1, Script: "get_set", Source: "return 1"}), "either script")
	mustFail(t, advCfg(MixItem{Op: "FCALL", Weight: 1, Source: "return 1"}), "FCALL calls a function by name")
	mustFail(t, advCfg(MixItem{Op: "GET", Weight: 1, Script: "get_set"}), "only valid for EVAL, EVALSHA and FCALL")
	mustFail(t, advCfg(MixItem{Op: "GET", Weight: 1, Steps: []MixItem{{Op: "GET"}}}), "steps: only valid for TXN and PIPELINE")
	mustFail(t, advCfg(MixItem{Op: "GET", Weight: 1, Command: "GET x"}), "command: only valid for RAW")
	mustFail(t, advCfg(MixItem{Op: "RAW", Weight: 1}), "RAW needs a command")
	mustFail(t, advCfg(MixItem{Op: "RAW", Weight: 1, Command: `GET "x`}), "unterminated")
	mustFail(t, advCfg(MixItem{Op: "RAW", Weight: 1, Command: "GET {key:nope}"}), "unknown key type")
	mustFail(t, advCfg(MixItem{Op: "TXN", Weight: 1}), "give 1 to 32 steps")
	mustFail(t, advCfg(MixItem{Op: "TXN", Weight: 1, Steps: []MixItem{{Op: "TXN", Steps: []MixItem{{Op: "GET"}}}}}), "cannot be a step")
	mustFail(t, advCfg(MixItem{Op: "TXN", Weight: 1, Steps: []MixItem{{Op: "BRPOP"}, {Op: "RAW", Command: "GET x"}, {Op: "PUBLISH"}, {Op: "XCONSUME"}}}), "BRPOP cannot be a step", "RAW cannot be a step", "PUBLISH cannot be a step", "XCONSUME cannot be a step")
	mustFail(t, advCfg(MixItem{Op: "PIPELINE", Weight: 1, Steps: []MixItem{{Op: "NOPE"}, {Op: "GET", Count: 3}}}), `steps[0].op: unknown "NOPE"`, "steps[1].count: not valid for GET")
	c := advCfg(MixItem{Op: "GET", Weight: 1})
	c.Dataset.BitmapBits = 4
	mustFail(t, c, "dataset.bitmap_bits")
}

func TestRawCommandsAndTheBlocklist(t *testing.T) {
	danger := advCfg(MixItem{Op: "RAW", Weight: 1, Command: "FLUSHALL"}, MixItem{Op: "RAW", Weight: 1, Command: "CONFIG SET maxmemory 1"})
	mustFail(t, danger, "mix[0].command: FLUSHALL is blocked", "mix[1].command: CONFIG SET is blocked", "workload.allow_dangerous")
	danger.Workload.AllowDangerous = true
	if err := danger.Validate(); err != nil {
		t.Fatalf("allowed explicitly: %v", err)
	}
	never := advCfg(MixItem{Op: "RAW", Weight: 1, Command: "MULTI"})
	never.Workload.AllowDangerous = true
	mustFail(t, never, "not supported in raw commands", "TXN")
	if err := advCfg(MixItem{Op: "RAW", Weight: 1, Command: "CONFIG GET maxmemory"}).Validate(); err != nil {
		t.Fatalf("harmless subcommands pass: %v", err)
	}
}

func TestPubSubValidation(t *testing.T) {
	ok := advCfg(MixItem{Op: "PUBLISH", Weight: 1})
	ok.Workload.PubSub = PubSub{Subscribers: 4, Channels: 8}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	c := ok
	c.Workload.PubSub.Sharded = true
	mustFail(t, c, "publish with SPUBLISH")
	c = advCfg(MixItem{Op: "SPUBLISH", Weight: 1})
	c.Workload.PubSub = PubSub{Subscribers: 2, Channels: 4}
	mustFail(t, c, "SPUBLISH only reaches sharded subscribers")
	c = advCfg(MixItem{Op: "GET", Weight: 1})
	c.Workload.PubSub = PubSub{Subscribers: 2, Channels: 4}
	mustFail(t, c, "the mix never publishes")
	c = ok
	c.Workload.PubSub = PubSub{Subscribers: -1, Channels: 0}
	mustFail(t, c, "subscribers: must be >= 0", "channels: must be >= 1")
	// Publishing without subscribers is allowed: a pure publish workload.
	if err := advCfg(MixItem{Op: "PUBLISH", Weight: 1}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionsInAClusterNeedHashTags(t *testing.T) {
	c := advCfg(MixItem{Op: "TXN", Weight: 1, Steps: []MixItem{{Op: "GET"}, {Op: "SET"}}})
	c.Target.Mode = "cluster"
	mustFail(t, c, "CROSSSLOT", "hash_tag_groups")
	c.Dataset.HashTagGroups = 8
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	one := advCfg(MixItem{Op: "TXN", Weight: 1, Steps: []MixItem{{Op: "GET"}}})
	one.Target.Mode = "cluster"
	if err := one.Validate(); err != nil {
		t.Fatalf("a one-step transaction touches one key: %v", err)
	}
}

func TestAdvancedItemsRoundTripThroughYAML(t *testing.T) {
	c := advCfg(
		MixItem{Op: "TXN", Weight: 2, Steps: []MixItem{{Op: "GET"}, {Op: "HSET", Fields: 2}}},
		MixItem{Op: "EVAL", Weight: 1, Script: "rate_limit", Args: []string{"60", "100"}},
		MixItem{Op: "RAW", Weight: 3, Command: `SET {key} "hello world" EX 60`},
		MixItem{Op: "BRPOP", Weight: 1, Timeout: "250ms"})
	c.Workload.PubSub = PubSub{Subscribers: 3, Channels: 5, Sharded: false}
	c.Workload.AllowDangerous = false
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !reflect.DeepEqual(got.Workload, c.Workload) {
		t.Fatalf("%v\n%+v\n%+v", err, got.Workload, c.Workload)
	}
	m, _ := c.ToMap()
	raw, _ := json.Marshal(m)
	back, _ := DecodeJSONMap(raw)
	viaJSON, err := ParseMap(back)
	if err != nil || !reflect.DeepEqual(viaJSON.Workload, c.Workload) {
		t.Fatalf("JSON form: %v\n%+v", err, viaJSON.Workload)
	}
	// Names are case-insensitive, steps included.
	n, _ := Parse([]byte("workload:\n  mix:\n    - {op: txn, weight: 1, steps: [{op: get}, {op: Set}]}\n    - {op: eval, weight: 1, script: GET_SET}\n"))
	if n.Workload.Mix[0].Op != "TXN" || n.Workload.Mix[0].Steps[1].Op != "SET" || n.Workload.Mix[1].Script != "get_set" {
		t.Fatalf("%+v", n.Workload.Mix)
	}
}

func TestAdvancedFeaturesDefaultOff(t *testing.T) {
	c := Default()
	if c.Workload.PubSub.Subscribers != 0 || c.Workload.AllowDangerous || c.Dataset.BitmapBits != 10000 {
		t.Fatalf("%+v %+v", c.Workload, c.Dataset)
	}
	if len(c.Workload.Mix) != 2 {
		t.Fatal("the default mix is still GET/SET")
	}
}
