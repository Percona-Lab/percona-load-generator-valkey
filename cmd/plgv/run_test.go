package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunRejectsBadFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCmd([]string{"-workers", "-1", "-preload=false"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "run.concurrency") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestRunRejectsUnknownClient(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCmd([]string{"-client", "nope", "-preload=false"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "target.client") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestRunRejectsBadConfigFile(t *testing.T) {
	var out, errb bytes.Buffer
	p := write(t, "workload:\n  mix:\n    - {op: NOPE, weight: 1}\n")
	if code := runCmd([]string{"-config", p}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "mix[0].op") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestConfigPrintsEffective(t *testing.T) {
	var out, errb bytes.Buffer
	p := write(t, "run:\n  seed: 9\nworkload:\n  mix:\n    - {op: zadd, weight: 2}\n")
	if code := configCmd([]string{"-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"seed: 9", "op: ZADD", "key_count: 100000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestConfigSaveWritesFile(t *testing.T) {
	var out, errb bytes.Buffer
	dst := filepath.Join(t.TempDir(), "out.yaml")
	if code := configCmd([]string{"-o", dst}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(b), "key_prefix: 'plgv:'") && !strings.Contains(string(b), "key_prefix: plgv:") {
		t.Fatalf("%v\n%s", err, b)
	}
}

func TestCOWithoutTargetRejected(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCmd([]string{"-co", "-preload=false"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "run.coordinated_omission") || !strings.Contains(errb.String(), "target_ops_per_sec") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestMetricsFlagsOverrideConfig(t *testing.T) {
	p := write(t, "run:\n  target_ops_per_sec: 1000\n  coordinated_omission: true\nserver_metrics:\n  enabled: false\n")
	var out, errb bytes.Buffer
	// Bad sample interval proves the flag reached the config (validation fails on it).
	if code := runCmd([]string{"-config", p, "-sample-interval", "1ms", "-preload=false"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "output.sample_interval") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestPrintFindingsAndServer(t *testing.T) {
	var b bytes.Buffer
	printFindings(&b, nil)
	if !strings.Contains(b.String(), "none") {
		t.Fatal(b.String())
	}
	b.Reset()
	printServer(&b, nil, nil) // nil server: prints nothing
	if b.Len() != 0 {
		t.Fatal("no server section without server metrics")
	}
}

func TestWebRefusesNonLoopback(t *testing.T) {
	var out, errb bytes.Buffer
	for _, a := range []string{"0.0.0.0:8080", "192.168.1.5:8080", ":8080", "example.com:80"} {
		errb.Reset()
		if code := webCmd([]string{"-addr", a, "-data-dir", t.TempDir()}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "loopback") {
			t.Errorf("%s: exit %d %q", a, code, errb.String())
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for a, want := range map[string]bool{"127.0.0.1:1": true, "localhost:1": true, "[::1]:1": true, "0.0.0.0:1": false, "bad": false} {
		if isLoopback(a) != want {
			t.Errorf("%s", a)
		}
	}
}

func TestParseLevels(t *testing.T) {
	got := parseLevels("4, 8;16  32,x")
	want := []float64{4, 8, 16, 32, 0}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
	if len(parseLevels("")) != 0 {
		t.Fatal("empty means no levels")
	}
}

func TestSweepFlagsAreValidated(t *testing.T) {
	var out, errb bytes.Buffer
	cases := map[string][]string{
		"run.sweep.values":         {"-sweep-workers", "4"},
		"whole number":             {"-sweep-workers", "4,2.5"},
		"listed twice":             {"-sweep-workers", "4,4"},
		"target rate must be > 0":  {"-sweep-rates", "100,x"},
		"either -sweep-workers or": {"-sweep-workers", "1,2", "-sweep-rates", "100,200"},
		"run.loop: open needs":     {"-loop", "open"},
		"run.loop: unknown":        {"-loop", "sideways"},
	}
	for want, args := range cases {
		out.Reset()
		errb.Reset()
		if code := runCmd(append(args, "-preload=false"), &out, &errb); code != 2 || !strings.Contains(errb.String(), want) {
			t.Errorf("%v: exit %d, stderr %q (want %q)", args, code, errb.String(), want)
		}
	}
}

func TestLevelFile(t *testing.T) {
	if got := levelFile("out/run.json", 3); got != "out/run-3.json" {
		t.Fatal(got)
	}
	if got := levelFile("run", 2); got != "run-2" {
		t.Fatal(got)
	}
}

func TestConfigPrintsLoadProfileAndSweepDefaultsOff(t *testing.T) {
	var out, errb bytes.Buffer
	if code := configCmd(nil, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	for _, want := range []string{"loop: closed", "kind: fixed", "kind: none"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("default config lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRawFileBuildsTheMixAndRefusesDangerousCommands(t *testing.T) {
	p := write(t, "# commands\n5 GET {key}\n2 SET {key} {value} EX 60\nHSET {key:hash} f {n}\n")
	mix, err := rawMix(p)
	if err != nil || len(mix) != 3 || mix[0].Op != "RAW" || mix[0].Weight != 5 || mix[2].Weight != 1 || mix[1].Command != "SET {key} {value} EX 60" {
		t.Fatalf("%v %+v", err, mix)
	}
	var out, errb bytes.Buffer
	bad := write(t, "3 GET {key}\nFLUSHALL\n")
	if code := runCmd([]string{"-raw-file", bad, "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "FLUSHALL is blocked") || !strings.Contains(errb.String(), "allow_dangerous") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	errb.Reset()
	never := write(t, "MULTI\n")
	if code := runCmd([]string{"-raw-file", never, "-allow-dangerous", "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "not supported in raw commands") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	errb.Reset()
	if code := runCmd([]string{"-raw-file", filepath.Join(t.TempDir(), "missing.txt")}, &out, &errb); code != 2 || errb.Len() == 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	errb.Reset()
	if code := runCmd([]string{"-raw-file", write(t, "# nothing\n")}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "no commands") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestPubSubFlagsAreValidated(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCmd([]string{"-subscribers", "2", "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "the mix never publishes") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestPresetsCommandListsEveryPreset(t *testing.T) {
	var out bytes.Buffer
	if code := presetsCmd(&out); code != 0 {
		t.Fatal(code)
	}
	for _, n := range []string{"cache", "session", "leaderboard", "queue", "rate-limiter", "pubsub-fanout", "counters", "big-keys", "mixed"} {
		if !strings.Contains(out.String(), n) {
			t.Fatalf("missing %s in %q", n, out.String())
		}
	}
}

func TestPresetFlagAndFileAndOverrides(t *testing.T) {
	// The flag alone.
	var out, errb bytes.Buffer
	if code := configCmd([]string{"-preset", "leaderboard"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "ZINCRBY") || !strings.Contains(out.String(), "preset: leaderboard") {
		t.Fatalf("exit %d, %q %q", code, out.String(), errb.String())
	}
	// A file's own settings override the flag's preset.
	p := write(t, "workload:\n  mix: [{op: GET, weight: 1}]\n")
	out.Reset()
	if code := configCmd([]string{"-preset", "leaderboard", "-config", p}, &out, &errb); code != 0 || strings.Contains(out.String(), "ZINCRBY") || !strings.Contains(out.String(), "preset: leaderboard") {
		t.Fatalf("exit %d, %q", code, out.String())
	}
	// A preset named in the file.
	p = write(t, "workload:\n  preset: session\n")
	out.Reset()
	if code := configCmd([]string{"-config", p}, &out, &errb); code != 0 || !strings.Contains(out.String(), "HGETALL") {
		t.Fatalf("exit %d, %q", code, out.String())
	}
	// An unknown preset lists the real ones.
	errb.Reset()
	if code := configCmd([]string{"-preset", "nope"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "unknown preset") || !strings.Contains(errb.String(), "big-keys") {
		t.Fatalf("exit %d, %q", code, errb.String())
	}
}

func TestKeySpaceFlagsAndOutput(t *testing.T) {
	var out, errb bytes.Buffer
	if code := configCmd([]string{}, &out, &errb); code != 0 || !strings.Contains(out.String(), "keyspace_analysis:\n    enabled: false") && !strings.Contains(out.String(), "enabled: false") {
		t.Fatalf("keyspace analysis is off by default: %q", out.String())
	}
	var w bytes.Buffer
	printKeySpace(&w, nil)
	if w.Len() != 0 {
		t.Fatal("nothing to print without an analysis")
	}
	printKeySpace(&w, &keyscan.Result{Match: "x:*", FirstError: "NOPERM", Errors: 1})
	if !strings.Contains(w.String(), "no keys analyzed matching x:*") || !strings.Contains(w.String(), "NOPERM") {
		t.Fatalf("%q", w.String())
	}
	w.Reset()
	printKeySpace(&w, &keyscan.Result{Match: "x:*", Sampled: 3, DBSize: 9, TotalBytes: 3 << 20, MaxBytes: 2 << 20, NoExpiry: 1,
		Types: []keyscan.TypeStat{{Type: "hash", Keys: 3, Share: 1, AvgBytes: 1 << 20, MaxBytes: 2 << 20, MaxElements: 5000}},
		Big:   []keyscan.BigKey{{Key: "x:h:1", Type: "hash", Bytes: 2 << 20, Elements: 5000}}})
	for _, want := range []string{"sample of 3 keys (of 9 on the server)", "3.0 MiB total", "hash", "1 of 3 keys have no expiry", "big #1: x:h:1 (hash, 2.0 MiB, 5000 elements)"} {
		if !strings.Contains(w.String(), want) {
			t.Fatalf("missing %q in %q", want, w.String())
		}
	}
}

func TestSameLatencyNote(t *testing.T) {
	mk := func(p ...time.Duration) []valkey.OpStat {
		var o []valkey.OpStat
		for _, d := range p {
			o = append(o, valkey.OpStat{P50: d})
		}
		return o
	}
	us := time.Microsecond
	if sameLatencyNote(mk(180*us, 180*us, 182*us)) == "" {
		t.Error("expected a note when p50 values are within 10%")
	}
	if sameLatencyNote(mk(100*us, 180*us, 300*us)) != "" {
		t.Error("no note expected when latencies differ")
	}
	if sameLatencyNote(mk(180*us, 180*us)) != "" {
		t.Error("no note expected for fewer than three operations")
	}
}
