package webui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
)

func meta(i int, fp string, p99, ops, hit float64, errs uint64) Meta {
	return Meta{ID: fmt.Sprintf("2026110%d-000000", i), StartedAt: time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC), Fingerprint: fp,
		P99Ms: p99, OpsPerSec: ops, HitRatio: hit, Ops: 100000, Errors: errs, Mix: "GET, SET"}
}

func TestTrendsFlagRegressionsAgainstMedian(t *testing.T) {
	runs := []Meta{meta(1, "a", 2, 100000, 0.95, 0), meta(2, "a", 2.1, 101000, 0.95, 0), meta(3, "a", 1.9, 99000, 0.96, 0),
		meta(4, "a", 3.0, 80000, 0.80, 500)}
	g := BuildTrends(runs)
	if len(g) != 1 || !g[0].Enough || len(g[0].Runs) != 4 {
		t.Fatalf("%+v", g)
	}
	want := map[string]bool{"p99_ms": false, "ops_per_sec": false, "hit_ratio": false, "error_rate": false}
	for _, f := range g[0].Flags {
		want[f.Metric] = true
		if f.Message == "" {
			t.Errorf("flag %s has no message", f.Metric)
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing flag %s", k)
		}
	}
}

func TestTrendsQuietWhenStable(t *testing.T) {
	runs := []Meta{meta(1, "a", 2, 100000, 0.95, 0), meta(2, "a", 2.1, 99000, 0.95, 0), meta(3, "a", 2.2, 98000, 0.94, 0), meta(4, "a", 2.3, 95000, 0.93, 0)}
	if f := BuildTrends(runs)[0].Flags; len(f) != 0 {
		t.Fatalf("within thresholds, got %+v", f)
	}
}

func TestTrendsGroupingAndExclusions(t *testing.T) {
	stopped := meta(3, "a", 50, 1, 0.1, 0)
	stopped.Stopped = true
	runs := []Meta{meta(1, "a", 2, 100, 0.9, 0), meta(2, "b", 2, 100, 0.9, 0), stopped, meta(4, "", 2, 100, 0.9, 0)}
	g := BuildTrends(runs)
	if len(g) != 2 {
		t.Fatalf("%d groups", len(g))
	}
	for _, grp := range g {
		if grp.Enough || len(grp.Runs) != 1 || len(grp.Flags) != 0 {
			t.Errorf("group %s: stopped and fingerprint-less runs must be left out, and 1 run is not a trend: %+v", grp.Fingerprint, grp)
		}
	}
}

func TestTrendsNoHitRatioNoFlag(t *testing.T) {
	runs := []Meta{meta(1, "a", 2, 100, -1, 0), meta(2, "a", 2, 100, -1, 0), meta(3, "a", 2, 100, -1, 0), meta(4, "a", 2, 100, 0.1, 0)}
	for _, f := range BuildTrends(runs)[0].Flags {
		if f.Metric == "hit_ratio" {
			t.Fatal("no baseline hit ratio means no hit-ratio flag")
		}
	}
}

func TestMedian(t *testing.T) {
	if median(nil) != 0 || median([]float64{3, 1, 2}) != 2 || median([]float64{4, 1, 2, 3}) != 2.5 {
		t.Fatal("median")
	}
}

func summaryFor(ops, p99, p50 float64, yaml string) *runsummary.Summary {
	return &runsummary.Summary{Fingerprint: "fp", ConfigYAML: yaml,
		Client:   runsummary.Client{OpsPerSec: ops, P99Ms: p99, P50Ms: p50},
		PerOp:    []runsummary.Op{{Op: "GET", Ops: 10, P99Ms: p99}},
		Findings: []runsummary.Finding{{ID: "tail-latency"}}}
}

func TestCompareDirectionsAndDiffs(t *testing.T) {
	a := summaryFor(1000, 2, 1, "run:\n  concurrency: 8\ndataset:\n  value_size: 256\noutput:\n  summary_file: a.json\n")
	b := summaryFor(1500, 4, 1.01, "run:\n  concurrency: 16\ndataset:\n  value_size: 256\noutput:\n  summary_file: b.json\n")
	b.Findings = []runsummary.Finding{{ID: "eviction-pressure"}}
	b.PerOp = append(b.PerOp, runsummary.Op{Op: "SET", Ops: 5})
	c := Compare(Meta{ID: "a"}, Meta{ID: "b"}, a, b)
	by := map[string]MetricRow{}
	for _, m := range c.Metrics {
		by[m.Name] = m
	}
	if by["Throughput"].Better != "b" || by["p99 latency"].Better != "a" || by["p50 latency"].Better != "same" {
		t.Fatalf("%+v", by)
	}
	if got := by["Throughput"].ChangePct; got < 0.49 || got > 0.51 {
		t.Fatalf("change %v", got)
	}
	if len(c.ConfigDiffs) != 1 || c.ConfigDiffs[0].Key != "run.concurrency" || c.ConfigDiffs[0].A != "8" || c.ConfigDiffs[0].B != "16" {
		t.Fatalf("diffs (output.* ignored): %+v", c.ConfigDiffs)
	}
	if len(c.PerOp) != 2 || c.PerOp[1].Op != "SET" || c.PerOp[1].AOps != 0 || c.PerOp[1].BOps != 5 {
		t.Fatalf("%+v", c.PerOp)
	}
	if strings.Join(c.OnlyInA, ",") != "tail-latency" || strings.Join(c.OnlyInB, ",") != "eviction-pressure" || !c.Comparable {
		t.Fatalf("%+v", c)
	}
	a.Fingerprint = "other"
	if Compare(Meta{}, Meta{}, a, b).Comparable {
		t.Fatal("different fingerprints are not comparable")
	}
}

func TestCompareServerRowsOnlyWithBothServers(t *testing.T) {
	a, b := summaryFor(1, 1, 1, ""), summaryFor(1, 1, 1, "")
	a.Server = &runsummary.Server{HitRatio: 0.9}
	if n := len(Compare(Meta{}, Meta{}, a, b).Metrics); n != 8 {
		t.Fatalf("%d metrics without both servers", n)
	}
	b.Server = &runsummary.Server{HitRatio: 0.5}
	c := Compare(Meta{}, Meta{}, a, b)
	var hit MetricRow
	for _, m := range c.Metrics {
		if m.Name == "Hit ratio" {
			hit = m
		}
	}
	if hit.Better != "a" || hit.A != 90 {
		t.Fatalf("%+v", hit)
	}
}

func TestHistorySaveListPrune(t *testing.T) {
	h, err := NewHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 4; i++ {
		s := summaryFor(float64(i), 1, 1, "")
		s.StartedAt = t0.Add(time.Duration(i/2) * time.Hour) // two pairs share a start second
		id, err := h.Save(s)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Fatal("ids must be unique even within one second")
	}
	list, _ := h.List()
	if len(list) != 4 || !list[0].StartedAt.After(list[3].StartedAt) {
		t.Fatalf("newest first: %+v", list)
	}
	if n, _ := h.Prune(2); n != 2 {
		t.Fatalf("pruned %d", n)
	}
	list, _ = h.List()
	if len(list) != 2 || list[0].StartedAt != t0.Add(time.Hour) {
		t.Fatalf("prune must keep the newest: %+v", list)
	}
	if n, _ := h.Prune(0); n != 0 {
		t.Fatal("keep 0 means keep all")
	}
}
