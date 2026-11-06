package webui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

func levelMeta(i int, id, kind string, value, ops, p99 float64, total int) Meta {
	return Meta{ID: fmt.Sprintf("20261104-1%d0000", i), StartedAt: time.Date(2026, 11, 4, 10, i, 0, 0, time.UTC), OpsPerSec: ops, P99Ms: p99, P95Ms: p99 / 2, P50Ms: p99 / 4,
		Label: fmt.Sprintf("nightly - %v workers", value), Fingerprint: "fp",
		Sweep: &runsummary.SweepInfo{ID: id, Kind: kind, Index: i, Total: total, Value: value}}
}

func TestWorkerSweepFindsThePlateauAndLatencyDoubling(t *testing.T) {
	metas := []Meta{
		levelMeta(1, "sw1", "workers", 4, 40000, 1, 4),
		levelMeta(2, "sw1", "workers", 8, 78000, 1.2, 4),
		levelMeta(3, "sw1", "workers", 16, 120000, 2.5, 4),
		levelMeta(4, "sw1", "workers", 32, 122000, 6, 4), // +1.7%: stopped helping after 16
	}
	r, ok := BuildSweep("sw1", metas)
	if !ok || !r.Complete || len(r.Rows) != 4 || r.Kind != "workers" || r.Label != "nightly" {
		t.Fatalf("%+v", r)
	}
	if r.PeakRunID != metas[3].ID {
		t.Fatalf("peak %s", r.PeakRunID)
	}
	// Efficiency: 1.0 is perfectly linear scaling from the first level.
	if e := r.Rows[1].Efficiency; e < 0.97 || e > 0.98 {
		t.Fatalf("8 workers efficiency %v", e)
	}
	if e := r.Rows[3].Efficiency; e < 0.38 || e > 0.39 {
		t.Fatalf("32 workers efficiency %v", e)
	}
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "stopped helping after 16 workers") {
		t.Fatalf("notes: %s", joined)
	}
	if !strings.Contains(joined, "p99 latency reached twice") || !strings.Contains(joined, "at 16 workers") {
		t.Fatalf("notes: %s", joined)
	}
}

func TestWorkerSweepStillScaling(t *testing.T) {
	metas := []Meta{levelMeta(1, "s", "workers", 1, 10000, 1, 3), levelMeta(2, "s", "workers", 2, 19000, 1, 3), levelMeta(3, "s", "workers", 4, 36000, 1.1, 3)}
	r, _ := BuildSweep("s", metas)
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "still growing at 4 workers") || strings.Contains(joined, "twice") {
		t.Fatalf("notes: %s", joined)
	}
}

func TestRateSweepFindsSaturation(t *testing.T) {
	metas := []Meta{
		levelMeta(1, "r", "rate", 10000, 10000, 1, 3),
		levelMeta(2, "r", "rate", 50000, 49900, 2, 3),
		levelMeta(3, "r", "rate", 200000, 120000, 80, 3),
	}
	metas[2].Dropped = 500000
	r, _ := BuildSweep("r", metas)
	if e := r.Rows[2].Efficiency; e < 0.59 || e > 0.61 {
		t.Fatalf("achieved/offered %v", e)
	}
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "could not serve 200000 ops/s") || !strings.Contains(joined, "reached 120000") {
		t.Fatalf("notes: %s", joined)
	}
	ok := []Meta{levelMeta(1, "r", "rate", 100, 100, 1, 2), levelMeta(2, "r", "rate", 200, 199, 1, 2)}
	r, _ = BuildSweep("r", ok)
	if !strings.Contains(strings.Join(r.Notes, " "), "Every level was served") {
		t.Fatalf("%v", r.Notes)
	}
}

func TestIncompleteAndStoppedSweepsSaySo(t *testing.T) {
	metas := []Meta{levelMeta(1, "s", "workers", 4, 40000, 1, 4), levelMeta(2, "s", "workers", 8, 50000, 1, 4)}
	metas[1].Stopped = true
	r, _ := BuildSweep("s", metas)
	if r.Complete {
		t.Fatal("2 of 4 levels is not complete")
	}
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "did not finish all of its levels") || !strings.Contains(joined, "stopped early") {
		t.Fatalf("notes: %s", joined)
	}
	if _, ok := BuildSweep("nope", metas); ok {
		t.Fatal("unknown sweep")
	}
}

func TestListSweepsGroupsAndOrders(t *testing.T) {
	a1, a2 := levelMeta(1, "a", "workers", 4, 1, 1, 2), levelMeta(2, "a", "workers", 8, 1, 1, 2)
	b1 := levelMeta(3, "b", "rate", 1000, 1, 1, 3)
	plain := Meta{ID: "20261104-090000", Label: "plain"}
	got := ListSweeps([]Meta{plain, b1, a2, a1})
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("newest first, plain runs excluded: %+v", got)
	}
	if got[1].Done != 2 || got[1].Total != 2 || got[1].Values[0] != 4 || got[1].Label != "nightly" {
		t.Fatalf("%+v", got[1])
	}
	if got[0].Done != 1 || got[0].Total != 3 {
		t.Fatalf("a sweep with missing levels shows how far it got: %+v", got[0])
	}
}

func TestSweepBase(t *testing.T) {
	for in, want := range map[string]string{"nightly - 8 workers": "nightly", "a - b - 3 ops/s": "a - b", "plain": "plain", "": ""} {
		if got := sweepBase(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// fakeSweep stands in for session.Sweep: it reports each level, stores outcomes
// through the hooks, and (when hold is set) waits at a chosen level.
type fakeSweep struct {
	holdAt  int // 1-based level to hold at until release/cancel; 0 = never
	release chan struct{}
	got     []config.Config
	ops     []uint64        // per-level throughput; default 1000*level
	p99     []time.Duration // per-level p99
}

func (f *fakeSweep) run(ctx context.Context, cfg config.Config, opt session.Options, h session.Hooks) ([]*session.Outcome, error) {
	f.got = append(f.got, cfg)
	sw := cfg.Run.Sweep
	var outs []*session.Outcome
	started := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for i, v := range sw.Values {
		h.OnLevel("swtest", i+1, len(sw.Values), sw.Kind, v)
		h.OnPhase(session.PhaseRunning)
		if f.holdAt == i+1 {
			select {
			case <-f.release:
			case <-ctx.Done():
			}
		}
		fr := newFake()
		fr.ops = uint64(1000 * (i + 1))
		if i < len(f.ops) {
			fr.ops = f.ops[i]
		}
		if i < len(f.p99) {
			fr.p99 = f.p99[i]
		}
		fr.started = started.Add(time.Duration(i) * time.Minute)
		close(fr.release)
		lvl := cfg
		if sw.Kind == "workers" {
			lvl.Run.Concurrency = int(v)
		}
		lvl.Run.Sweep = config.Sweep{Kind: "none", Values: []float64{}}
		o := lvlOptions(opt, sw.Kind, v)
		out, err := fr.run(ctx, lvl, o, session.Hooks{OnPhase: func(string) {}, OnSample: func(stats.Sample) {}})
		if err != nil {
			return outs, err
		}
		out.Summary.Sweep = &runsummary.SweepInfo{ID: "swtest", Kind: sw.Kind, Index: i + 1, Total: len(sw.Values), Value: v}
		outs = append(outs, out)
		h.OnOutcome(out)
		if ctx.Err() != nil {
			break
		}
	}
	return outs, nil
}

func lvlOptions(o session.Options, kind string, v float64) session.Options {
	if kind == "workers" {
		o.Label = fmt.Sprintf("%s - %d workers", o.Label, int(v))
	} else {
		o.Label = fmt.Sprintf("%s - %.0f ops/s", o.Label, v)
	}
	return o
}

func sweepBody(e *env, kind string, vals ...float64) map[string]any {
	body := e.defaultsBody(func(c map[string]any) {
		sw := c["run"].(map[string]any)["sweep"].(map[string]any)
		sw["kind"] = kind
		var anyVals []any
		for _, v := range vals {
			anyVals = append(anyVals, v)
		}
		sw["values"] = anyVals
	})
	body["label"] = "mysweep"
	return body
}

func TestManagerRunsASweepAndStoresEveryLevel(t *testing.T) {
	e := setup(t, nil)
	fs := &fakeSweep{holdAt: 2, release: make(chan struct{})}
	e.srv.mgr.sweep = fs.run
	if code, _, raw := e.do("POST", "/api/start", sweepBody(e, "workers", 2, 4, 8), nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	// While level 2 runs, the live view says where the sweep is.
	var live map[string]any
	for i := 0; i < 200; i++ {
		_, live, _ = e.do("GET", "/api/live", nil, nil)
		if sw, ok := live["sweep"].(map[string]any); ok && sw["index"] == float64(2) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sw := live["sweep"].(map[string]any)
	if sw["index"] != float64(2) || sw["total"] != float64(3) || sw["kind"] != "workers" || sw["value"] != float64(4) {
		t.Fatalf("%v", sw)
	}
	if ids := sw["run_ids"].([]any); len(ids) != 1 {
		t.Fatalf("level 1 should already be stored: %v", ids)
	}
	// A second start is refused while the sweep runs.
	if code, _, _ := e.do("POST", "/api/start", sweepBody(e, "workers", 2, 4), nil); code != 409 {
		t.Fatalf("%d", code)
	}
	close(fs.release)
	done := e.waitState(StateDone)
	if done["sweep"].(map[string]any)["index"] != float64(3) || done["run_id"] == "" {
		t.Fatalf("%v", done)
	}
	_, list, _ := e.do("GET", "/api/runs", nil, nil)
	runs := list["runs"].([]any)
	if len(runs) != 3 {
		t.Fatalf("%d runs stored", len(runs))
	}
	_, sweeps, _ := e.do("GET", "/api/sweeps", nil, nil)
	sl := sweeps["sweeps"].([]any)
	if len(sl) != 1 || sl[0].(map[string]any)["done"] != float64(3) || sl[0].(map[string]any)["label"] != "mysweep" {
		t.Fatalf("%v", sweeps)
	}
	code, res, _ := e.do("GET", "/api/sweeps/swtest", nil, nil)
	if code != 200 || res["complete"] != true || len(res["rows"].([]any)) != 3 {
		t.Fatalf("%d %v", code, res)
	}
	if code, _, _ := e.do("GET", "/api/sweeps/nope", nil, nil); code != 404 {
		t.Fatal(code)
	}
	if fs.got[0].Run.Sweep.Kind != "workers" || len(fs.got[0].Run.Sweep.Values) != 3 {
		t.Fatalf("the sweep config reached the sweep: %+v", fs.got[0].Run.Sweep)
	}
}

func TestStoppingASweepKeepsTheFinishedLevels(t *testing.T) {
	e := setup(t, nil)
	fs := &fakeSweep{holdAt: 2, release: make(chan struct{})}
	e.srv.mgr.sweep = fs.run
	e.do("POST", "/api/start", sweepBody(e, "workers", 2, 4, 8), nil)
	for i := 0; i < 200; i++ {
		_, live, _ := e.do("GET", "/api/live", nil, nil)
		if sw, ok := live["sweep"].(map[string]any); ok && sw["index"] == float64(2) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.do("POST", "/api/stop", map[string]any{}, nil)
	e.waitState(StateDone)
	_, list, _ := e.do("GET", "/api/runs", nil, nil)
	if n := len(list["runs"].([]any)); n < 1 || n > 2 {
		t.Fatalf("level 1 (and possibly the interrupted level 2) are stored, got %d", n)
	}
	_, res, _ := e.do("GET", "/api/sweeps/swtest", nil, nil)
	if res["complete"] != false {
		t.Fatal("a stopped sweep is not complete")
	}
}

func TestSweepErrorIsShownAndEarlierLevelsKept(t *testing.T) {
	e := setup(t, nil)
	e.srv.mgr.sweep = func(ctx context.Context, cfg config.Config, opt session.Options, h session.Hooks) ([]*session.Outcome, error) {
		h.OnLevel("x", 1, 2, "workers", 2)
		return nil, io_err("sweep level 1 of 2 (mysweep - 2 workers): cannot connect to 127.0.0.1:1")
	}
	e.do("POST", "/api/start", sweepBody(e, "workers", 2, 4), nil)
	m := e.waitState(StateError)
	if !strings.Contains(m["error"].(string), "level 1 of 2") {
		t.Fatalf("%v", m)
	}
}

func TestStartRejectsAnInvalidSweep(t *testing.T) {
	e := setup(t, nil)
	code, m, _ := e.do("POST", "/api/start", sweepBody(e, "workers", 4), nil)
	if code != 400 {
		t.Fatalf("%d %v", code, m)
	}
	found := false
	for _, x := range m["errors"].([]any) {
		if x.(map[string]any)["path"] == "run.sweep.values" {
			found = true
		}
	}
	if !found {
		t.Fatalf("%v", m["errors"])
	}
}
