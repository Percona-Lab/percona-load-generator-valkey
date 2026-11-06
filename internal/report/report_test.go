package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func sample(withServer bool) *runsummary.Summary {
	h := &latency.Histogram{}
	for i := 1; i <= 100; i++ {
		h.Record(time.Duration(i) * time.Microsecond * 20)
	}
	res := &valkey.RunResult{
		Ops: 100, Errors: 3, ErrClasses: map[string]uint64{"OOM": 3}, FirstError: "SET: OOM <script>x</script>",
		Elapsed: 3 * time.Second, CPU: time.Second, Hist: h, Seed: 9, SeriesFactor: 1,
		Series: []stats.Sample{
			{Offset: time.Second, Span: time.Second, Ops: 30, P50: time.Millisecond, P95: 2 * time.Millisecond, P99: 3 * time.Millisecond},
			{Offset: 2 * time.Second, Span: time.Second, Ops: 40, P50: time.Millisecond, P95: 2 * time.Millisecond, P99: 4 * time.Millisecond},
		},
		PerOp: []valkey.OpStat{{Op: "GET", Ops: 80}, {Op: "SET", Ops: 20, Errors: 3}},
	}
	var srv *servermetrics.Summary
	if withServer {
		srv = &servermetrics.Summary{Version: "9.1.3", Role: "master", Mode: "standalone", ServerOps: 100, HitRatio: 0.5, EvictedKeys: 4,
			UsedMemoryMax: 5 << 20, SlowlogNew: 1,
			SlowlogSample: []servermetrics.SlowEntry{{ID: 1, Time: time.Unix(1700000000, 0), Duration: 12 * time.Millisecond, Command: "HGETALL <b>big</b>"}},
			Samples:       []servermetrics.Sample{{Offset: time.Second, OpsPerSec: 30, UsedMemory: 5 << 20, HitRatio: -1}}}
	}
	cfg := config.Default()
	cfg.Target.Password = "topsecret"
	s, _ := runsummary.Build("v9", cfg, res, srv, time.Unix(1700000000, 0))
	s.Label = "nightly <run>"
	return s
}

func TestRenderIsSelfContainedAndEscaped(t *testing.T) {
	b, err := RenderBytes(sample(true))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"<!DOCTYPE html>", "Valkey Benchmark Report: nightly &lt;run&gt;", "Findings", "Per operation",
		"<svg", "Latency over time", "Server ops/s", "Slowlog entries during the run", "HGETALL &lt;b&gt;big&lt;/b&gt;",
		"OOM", "SET: OOM &lt;script&gt;", "9.1.3", "Operation mix (weights)", "GET 80, SET 20",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	for _, bad := range []string{"<script", "http://", "https://", "topsecret", "<b>big</b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("report must not contain %q", bad)
		}
	}
}

func TestRenderWithoutServerSaysSo(t *testing.T) {
	b, err := RenderBytes(sample(false))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "Server metrics were off") || strings.Contains(out, "Slowlog entries") || strings.Contains(out, ">Server</h2>") {
		t.Fatal("server section must be absent and explained when metrics were off")
	}
}

func TestLineChart(t *testing.T) {
	if got := string(lineChart("t", "ms", nil)); !strings.Contains(got, "No data") {
		t.Fatalf("empty chart: %q", got)
	}
	got := string(lineChart("t", "ms", []Series{{"a", "s1", []Point{{0, 1}, {10, 5}}}, {"b", "s2", nil}}))
	if strings.Count(got, "<polyline") != 1 || !strings.Contains(got, `class="k s1"`) {
		t.Fatalf("chart: %s", got)
	}
	// All-zero data must not divide by zero.
	if got := string(lineChart("t", "ms", []Series{{"a", "s1", []Point{{0, 0}}}})); !strings.Contains(got, "<svg") {
		t.Fatal("zero chart")
	}
}

func TestReportKeySpaceSection(t *testing.T) {
	s := sample(false)
	if b, _ := RenderBytes(s); strings.Contains(string(b), "Key space") {
		t.Fatal("no key-space section unless the analysis ran")
	}
	s.KeySpace = &keyscan.Result{Sampled: 90, DBSize: 900, Match: "plgv:*", TotalBytes: 5 << 20, MaxBytes: 2 << 20, OverMiB: 1, NoExpiry: 80,
		Types: []keyscan.TypeStat{{Type: "hash", Keys: 90, AvgBytes: 1 << 10, MaxBytes: 2 << 20, MaxElements: 900}},
		Big:   []keyscan.BigKey{{Key: "k:<b>x</b>", Type: "hash", Bytes: 2 << 20, Elements: 900}}}
	b, err := RenderBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Key space", "A sample of 90 of the keys matching", "900 keys on the server", "5.0 MiB", "80 of 90", "2.0 MiB", "k:&lt;b&gt;x&lt;/b&gt;"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report lacks %q", want)
		}
	}
	s.KeySpace = &keyscan.Result{Match: "plgv:*", Errors: 1, FirstError: "NOPERM"}
	b, _ = RenderBytes(s)
	if !strings.Contains(string(b), "No keys were analyzed matching") || !strings.Contains(string(b), "NOPERM") {
		t.Error("a failed analysis must be stated")
	}
}
