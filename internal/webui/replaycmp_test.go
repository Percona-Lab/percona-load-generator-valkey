package webui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
)

// sampleReplaySummaries are the replays of one recording on two Valkey versions.
func sampleReplaySummaries() []*replay.Summary {
	mk := func(label, server string, ops, p50, p95, p99 float64, errs uint64) *replay.Summary {
		return &replay.Summary{Format: replay.SummaryFormat, Label: label, Target: "10.0.0.5:6379", PLGVVersion: "0.1.0", ServerVersion: server, Mode: "standalone",
			FinishedAt: "2026-11-05T12:00:00Z", Recording: replay.RecordingRef{Name: "session.jsonl.zst", Status: "complete", Ops: 60000, Workers: 8, CommandsHash: "abc123", ServerVersion: "9.1.3"},
			Speed: 0, Ops: 60000, Errors: errs, Misses: 5476, ElapsedSeconds: 60000 / ops, OpsPerSec: ops, Latency: replay.Latency{P50: p50, P95: p95, P99: p99, Max: p99 * 2},
			CommandsHash: "abc123", HashMatches: true,
			PerCommand: []replay.CommandStat{{Command: "HGET", Ops: 26855, Misses: 5476, P50Ms: p50, P99Ms: p99}, {Command: "HSET", Ops: 9129, P50Ms: p50, P99Ms: p99 * 1.1}, {Command: "EXPIRE", Ops: 11963, P50Ms: p50, P99Ms: p99}},
			Outcomes:   &replay.Outcomes{Compared: 60000, StatusDiffs: 0, ChecksumCompared: 54524, ChecksumDiffs: 0, RecordedP50: 180 * time.Microsecond, RecordedP99: 310 * time.Microsecond},
			Data:       &datafp.Check{Match: true}}
	}
	return []*replay.Summary{mk("valkey 8.0", "8.0.12", 50000, 0.18, 0.2, 0.31, 0), mk("valkey 9.1", "9.1.3", 36000, 0.26, 0.44, 0.92, 3)}
}

func TestReplayCompareAPI(t *testing.T) {
	e := setup(t, nil)
	sums := sampleReplaySummaries()
	files := []any{sums[0], sums[1]}
	code, m, raw := e.do("POST", "/api/replay-compare", map[string]any{"files": files, "tolerance": 10}, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var out struct {
		Summaries   []replay.Summary
		Comparisons []replaycompare.Comparison
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Summaries) != 2 || len(out.Comparisons) != 1 {
		t.Fatalf("%v %s", err, raw)
	}
	c := out.Comparisons[0]
	if c.Verdict != "slower" || c.Other != "valkey 9.1" || !c.Comparable || len(c.Rows) == 0 || len(m) == 0 {
		t.Fatalf("%+v", c)
	}
	// A multi-target file, and the same label twice, both work.
	multi := replaycompare.File{Format: replaycompare.MultiFormat, Mode: "sequential", Targets: []*replay.Summary{sums[0], sums[0]}}
	if code, _, raw = e.do("POST", "/api/replay-compare", map[string]any{"files": []any{multi}}, nil); code != 200 || !strings.Contains(raw, "valkey 8.0 (2)") {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestReplayCompareAPIRejectsBadInput(t *testing.T) {
	e := setup(t, nil)
	one := sampleReplaySummaries()[0]
	cases := map[string]any{
		"one summary":   map[string]any{"files": []any{one}},
		"not a summary": map[string]any{"files": []any{map[string]any{"format": "x"}, one}},
		"no files":      map[string]any{"files": []any{}},
		"bad tolerance": map[string]any{"files": []any{one, one}, "tolerance": -5},
	}
	for name, body := range cases {
		if code, _, raw := e.do("POST", "/api/replay-compare", body, nil); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, _ := e.do("GET", "/api/replay-compare", nil, nil); code != 405 {
		t.Errorf("GET: %d", code)
	}
	many := make([]any, 9)
	for i := range many {
		many[i] = one
	}
	if code, _, raw := e.do("POST", "/api/replay-compare", map[string]any{"files": many}, nil); code != 400 || !strings.Contains(raw, "at most") {
		t.Errorf("too many: %d %s", code, raw)
	}
}
