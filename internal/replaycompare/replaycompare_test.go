package replaycompare

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
)

func sum(label, server string, ops, p50, p99 float64) *replay.Summary {
	return &replay.Summary{Format: replay.SummaryFormat, Label: label, ServerVersion: server, Speed: 0,
		Recording: replay.RecordingRef{Name: "r.jsonl", CommandsHash: "abc"}, Ops: 1000, OpsPerSec: ops,
		Latency:    replay.Latency{P50: p50, P95: p99 / 2, P99: p99, Max: p99 * 2},
		PerCommand: []replay.CommandStat{{Command: "GET", Ops: 800, P99Ms: p99}, {Command: "SET", Ops: 200, P99Ms: p99}}}
}

func find(c *Comparison, sub string) *Finding {
	for i := range c.Findings {
		if strings.Contains(c.Findings[i].Title, sub) {
			return &c.Findings[i]
		}
	}
	return nil
}

func TestEquivalentWithinTolerance(t *testing.T) {
	c := Compare(sum("a", "9.0", 50000, 0.5, 2.0), sum("b", "9.0", 52000, 0.52, 2.1), Options{})
	if c.Verdict != "equivalent" || !c.Comparable {
		t.Fatalf("%s: %+v", c.Verdict, c.Findings)
	}
	if len(c.Findings) != 0 {
		t.Fatalf("nothing to report: %+v", c.Findings)
	}
}

func TestSlowerAndWhere(t *testing.T) {
	c := Compare(sum("old", "9.0.1", 50000, 0.5, 2.0), sum("new", "9.1.3", 30000, 0.9, 5.0), Options{})
	if c.Verdict != "slower" {
		t.Fatalf("verdict = %s", c.Verdict)
	}
	if f := find(c, "Throughput is worse on new"); f == nil || f.Severity != "warning" || !strings.Contains(f.Detail, "-40%") {
		t.Fatalf("%+v", c.Findings)
	}
	if find(c, "Latency p99 is worse") == nil || find(c, "Different server versions") == nil || find(c, "GET p99 is higher") == nil {
		t.Fatalf("%+v", c.Findings)
	}
	if c.Commands[0].Command != "GET" || c.Commands[0].Verdict != Worse || c.Commands[0].P99DeltaPct != 150 {
		t.Fatalf("%+v", c.Commands)
	}
}

func TestFasterAndMixed(t *testing.T) {
	if c := Compare(sum("a", "", 30000, 0.9, 5.0), sum("b", "", 50000, 0.5, 2.0), Options{}); c.Verdict != "faster" {
		t.Fatal(c.Verdict)
	}
	// More throughput but a worse tail.
	if c := Compare(sum("a", "", 30000, 0.5, 2.0), sum("b", "", 50000, 0.5, 5.0), Options{}); c.Verdict != "mixed" {
		t.Fatal(c.Verdict)
	}
}

func TestToleranceOptionAndLatencyFloor(t *testing.T) {
	a, b := sum("a", "", 50000, 0.5, 2.0), sum("b", "", 54000, 0.5, 2.0) // +8%
	if Compare(a, b, Options{}).Verdict != "equivalent" {
		t.Fatal("8% is inside the default 10%")
	}
	if Compare(a, b, Options{Tolerance: 0.05}).Verdict != "faster" {
		t.Fatal("8% is outside 5%")
	}
	// 0.01 ms to 0.03 ms is +200% but only 20 microseconds: noise.
	if c := Compare(sum("a", "", 1, 0.01, 0.02), sum("b", "", 1, 0.03, 0.04), Options{}); c.Verdict != "equivalent" {
		t.Fatalf("%s", c.Verdict)
	}
}

func TestDifferentRecordingsAreNotComparable(t *testing.T) {
	b := sum("b", "", 1, 1, 1)
	b.Recording.CommandsHash = "other"
	c := Compare(sum("a", "", 1, 1, 1), b, Options{})
	if c.Comparable || c.Verdict != "not comparable" || find(c, "Different recordings") == nil || len(c.Rows) != 0 {
		t.Fatalf("%+v", c)
	}
	f := sum("f", "", 0, 0, 0)
	f.Failure = "cannot connect"
	if c := Compare(sum("a", "", 1, 1, 1), f, Options{}); c.Comparable || find(c, "f did not run") == nil {
		t.Fatalf("%+v", c)
	}
}

func TestErrorsRepliesAndData(t *testing.T) {
	a, b := sum("a", "", 50000, 0.5, 2), sum("b", "", 50000, 0.5, 2)
	a.Outcomes, b.Outcomes = &replay.Outcomes{Compared: 10}, &replay.Outcomes{Compared: 10, StatusDiffs: 3, ChecksumDiffs: 4}
	b.Errors, b.FirstError = 5, "ERR boom"
	b.Data = &datafp.Check{Match: false}
	b.MaxLagMs, b.Speed, a.Speed = 900, 1, 1
	c := Compare(a, b, Options{})
	if f := find(c, "More errors on b"); f == nil || f.Severity != "critical" || !strings.Contains(f.Detail, "ERR boom") {
		t.Fatalf("%+v", c.Findings)
	}
	for _, want := range []string{"answered differently", "returned different replies", "Starting data differs on b", "fell behind"} {
		if find(c, want) == nil {
			t.Errorf("missing %q: %+v", want, c.Findings)
		}
	}
}

func TestOptionsMismatchWarns(t *testing.T) {
	a, b := sum("a", "", 1, 1, 1), sum("b", "", 1, 1, 1)
	b.ReadsOnly = true
	if c := Compare(a, b, Options{}); find(c, "different options") == nil {
		t.Fatal("different options must be called out")
	}
}

func TestParseAndUniquify(t *testing.T) {
	s := sum("x", "", 1, 1, 1)
	one, _ := json.Marshal(s)
	got, err := Parse(one)
	if err != nil || len(got) != 1 || got[0].Label != "x" {
		t.Fatalf("%v %v", got, err)
	}
	multi, _ := json.Marshal(File{Format: MultiFormat, Mode: "sequential", Targets: []*replay.Summary{s, s}})
	got, err = Parse(multi)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	Uniquify(got)
	if got[0].Label != "x" || got[1].Label != "x (2)" {
		t.Fatalf("%s %s", got[0].Label, got[1].Label)
	}
	for _, bad := range []string{`not json`, `{"format":"other"}`, `{"format":"` + MultiFormat + `","targets":[]}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	all := []*replay.Summary{s, sum("y", "", 1, 1, 1), sum("z", "", 1, 1, 1)}
	if cs := CompareAll(all, Options{}); len(cs) != 2 || cs[1].Other != "z" {
		t.Fatalf("%+v", cs)
	}
}
