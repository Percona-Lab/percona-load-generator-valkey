package webui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

// fakeReplay stands in for session.ReplayManyWith.
type fakeReplay struct {
	mu      sync.Mutex
	calls   []fakeReplayCall
	release chan struct{}
	failOn  string // a target name that fails
}

type fakeReplayCall struct {
	path       string
	targets    []multitarget.Target
	opt        replay.Options
	concurrent bool
	require    bool
	addrs      []string
}

func (f *fakeReplay) run(ctx context.Context, cfg config.Config, targets []multitarget.Target, path string, opt replay.Options, concurrent, require bool, version string, h session.ReplayHooks) []session.TargetReplay {
	f.mu.Lock()
	f.calls = append(f.calls, fakeReplayCall{path, targets, opt, concurrent, require, cfg.Target.URIs})
	f.mu.Unlock()
	var out []session.TargetReplay
	for i, t := range targets {
		h.OnTarget(i+1, len(targets), t.Name)
		h.OnSample(t.Name, stats.Sample{Offset: time.Second, Span: time.Second, Ops: 500, P99: 2 * time.Millisecond})
		if f.release != nil {
			select {
			case <-f.release:
			case <-ctx.Done():
			}
		}
		tr := session.TargetReplay{Target: t}
		sum := &replay.Summary{Format: replay.SummaryFormat, Label: t.Name, Target: t.Addr, Ops: 1000 + uint64(i)*100, ElapsedSeconds: 2,
			OpsPerSec: 500 - float64(i)*20, Latency: replay.Latency{P50: 1, P95: 1.5, P99: 2 + float64(i)}, HashMatches: true, CommandsHash: "h",
			Recording: replay.RecordingRef{Name: filepath.Base(path), Status: "complete", Ops: 1000, CommandsHash: "h"}}
		if t.Name == f.failOn {
			tr.Err = context.DeadlineExceeded
			sum = &replay.Summary{Format: replay.SummaryFormat, Label: t.Name, Target: t.Addr, Failure: "connection refused", Recording: sum.Recording}
		}
		tr.Summary = sum
		h.OnDone(t.Name, tr)
		out = append(out, tr)
	}
	return out
}

func replaySetup(t *testing.T, f *fakeReplay) *env {
	t.Helper()
	e := setupWith(t, nil, func(o *Options) { o.Replay = f.run; o.FreePaths = true })
	writeRec(t, filepath.Join(e.recDir(), "a.plgvrec"), "")
	return e
}

func replayBody(e *env, edit func(rp map[string]any)) map[string]any {
	b := e.defaultsBody(nil)
	rp := map[string]any{"path": "a.plgvrec"}
	if edit != nil {
		edit(rp)
	}
	b["replay"] = rp
	return b
}

func TestReplayValidationNamesTheFieldAtFault(t *testing.T) {
	e := replaySetup(t, &fakeReplay{})
	for name, c := range map[string]struct {
		edit func(rp map[string]any)
		want string
	}{
		"no recording": {func(rp map[string]any) { rp["path"] = "" }, "replay.path"},
		"not a file":   {func(rp map[string]any) { rp["path"] = "nope.plgvrec" }, "replay.path"},
		"speed":        {func(rp map[string]any) { rp["speed"] = -1 }, "replay.speed"},
		"iterations":   {func(rp map[string]any) { rp["iterations"] = 0 }, "replay.iterations"},
		"delay":        {func(rp map[string]any) { rp["iteration_delay"] = "soon" }, "replay.iteration_delay"},
		"tolerance":    {func(rp map[string]any) { rp["tolerance"] = 5000 }, "replay.tolerance"},
		"one target":   {func(rp map[string]any) { rp["targets"] = []any{"a=127.0.0.1:1"} }, "replay.targets"},
		"duplicate":    {func(rp map[string]any) { rp["targets"] = []any{"a=127.0.0.1:1", "a=127.0.0.1:2"} }, "replay.targets"},
		"concurrent":   {func(rp map[string]any) { rp["concurrent"] = true }, "replay.concurrent"},
		"credentials":  {func(rp map[string]any) { rp["targets"] = []any{"a=valkey://u:secret@127.0.0.1:1", "b=127.0.0.1:2"} }, "do not put credentials"},
		"too many": {func(rp map[string]any) {
			rp["targets"] = []any{"1:1", "2:2", "3:3", "4:4", "5:5", "6:6", "7:7", "8:8", "9:9"}
		}, "replay.targets"},
	} {
		code, m, raw := e.do("POST", "/api/replay/validate", replayBody(e, c.edit), nil)
		if code != 200 || m["ok"] != false || !strings.Contains(raw, c.want) {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		if code, _, _ := e.do("POST", "/api/replay", replayBody(e, c.edit), nil); code != 400 {
			t.Fatalf("%s: start returned %d", name, code)
		}
	}
	if code, m, raw := e.do("POST", "/api/replay/validate", replayBody(e, nil), nil); code != 200 || m["ok"] != true {
		t.Fatalf("valid: %d %s", code, raw)
	}
}

func TestReplayRunsStoresAndComparesTargets(t *testing.T) {
	f := &fakeReplay{release: make(chan struct{})}
	e := replaySetup(t, f)
	body := replayBody(e, func(rp map[string]any) {
		rp["targets"] = []any{"old=127.0.0.1:6379", "new=127.0.0.1:6380"}
		rp["speed"], rp["reads_only"], rp["iterations"], rp["iteration_delay"] = 2, true, 3, "5s"
		rp["require_data"], rp["tolerance"] = true, 20
	})
	if code, _, raw := e.do("POST", "/api/replay", body, nil); code != 202 {
		t.Fatalf("start: %d %s", code, raw)
	}
	live := e.waitState("running")
	lr := live["replay"].(map[string]any)
	if live["kind"] != "replay" || lr["recording"] != "a.plgvrec" || len(lr["targets"].([]any)) != 2 {
		t.Fatalf("live = %v", live)
	}
	first := lr["targets"].([]any)[0].(map[string]any)
	if first["state"] != "running" || first["ops"].(float64) != 500 {
		t.Fatalf("progress = %v", first)
	}
	// Only one run at a time, workload or replay.
	if code, _, _ := e.do("POST", "/api/replay", body, nil); code != 409 {
		t.Fatalf("second start: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/start", e.defaultsBody(nil), nil); code != 409 {
		t.Fatalf("a run during a replay: %d", code)
	}
	close(f.release)
	done := e.waitState("done")
	id, _ := done["replay_id"].(string)
	if id == "" || done["run_id"] != nil {
		t.Fatalf("done = %v", done)
	}
	c := f.calls[0]
	if c.opt.Speed != 2 || !c.opt.ReadsOnly || c.opt.Iterations != 3 || c.opt.IterationDelay != 5*time.Second || !c.require || c.concurrent {
		t.Fatalf("options = %+v", c)
	}
	if filepath.Base(c.path) != "a.plgvrec" || len(c.targets) != 2 || c.targets[1].Name != "new" {
		t.Fatalf("call = %+v", c)
	}
	_, got, raw := e.do("GET", "/api/replays/"+id, nil, nil)
	if got["mode"] != "sequential" || len(got["targets"].([]any)) != 2 || len(got["comparisons"].([]any)) != 1 {
		t.Fatalf("stored = %s", raw)
	}
	_, list, _ := e.do("GET", "/api/replays", nil, nil)
	rs := list["replays"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["recording"] != "a.plgvrec" {
		t.Fatalf("list = %v", list)
	}
	if code, _, _ := e.do("DELETE", "/api/replays/"+id, nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/replays/"+id, nil, nil); code != 404 {
		t.Fatalf("after delete: %d", code)
	}
}

func TestReplayOnOneServerUsesTheTargetSection(t *testing.T) {
	f := &fakeReplay{}
	e := replaySetup(t, f)
	body := replayBody(e, nil)
	body["config"].(map[string]any)["target"].(map[string]any)["uris"] = []any{"10.1.2.3:6379"}
	body["label"] = "prod"
	if code, _, raw := e.do("POST", "/api/replay", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	id := e.waitState("done")["replay_id"].(string)
	if c := f.calls[0]; len(c.targets) != 1 || c.targets[0].Addr != "10.1.2.3:6379" || c.targets[0].Name != "prod" {
		t.Fatalf("call = %+v", c)
	}
	_, got, _ := e.do("GET", "/api/replays/"+id, nil, nil)
	if got["mode"] != "single" || len(got["comparisons"].([]any)) != 0 {
		t.Fatalf("stored = %v", got)
	}
}

func TestReplayWhereOneTargetFailsKeepsTheOthers(t *testing.T) {
	f := &fakeReplay{failOn: "b"}
	e := replaySetup(t, f)
	body := replayBody(e, func(rp map[string]any) {
		rp["targets"] = []any{"a=127.0.0.1:1", "b=127.0.0.1:2"}
		rp["concurrent"] = true
	})
	e.do("POST", "/api/replay", body, nil)
	id := e.waitState("done")["replay_id"].(string)
	_, got, raw := e.do("GET", "/api/replays/"+id, nil, nil)
	ts := got["targets"].([]any)
	if got["mode"] != "concurrent" || ts[1].(map[string]any)["failure"] != "connection refused" || ts[0].(map[string]any)["ops"].(float64) != 1000 {
		t.Fatalf("stored = %s", raw)
	}
	// When every target fails there is nothing to keep: the error is shown.
	f2 := &fakeReplay{failOn: "only"}
	e2 := replaySetup(t, f2)
	b2 := replayBody(e2, nil)
	b2["label"] = "only"
	e2.do("POST", "/api/replay", b2, nil)
	live := e2.waitState("error")
	if !strings.Contains(live["error"].(string), "only") {
		t.Fatalf("live = %v", live)
	}
	if _, l, _ := e2.do("GET", "/api/replays", nil, nil); len(l["replays"].([]any)) != 0 {
		t.Fatal("a failed replay was stored")
	}
}

func TestReplayCanBeStopped(t *testing.T) {
	f := &fakeReplay{release: make(chan struct{})}
	e := replaySetup(t, f)
	e.do("POST", "/api/replay", replayBody(e, nil), nil)
	e.waitState("running")
	if code, _, _ := e.do("POST", "/api/stop", map[string]any{}, nil); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	id := e.waitState("done")["replay_id"].(string)
	if _, got, _ := e.do("GET", "/api/replays/"+id, nil, nil); got["stopped"] != true {
		t.Fatalf("stored = %v", got)
	}
}

func TestReplayRefusesAPathOutsideTheFolderWhenLocked(t *testing.T) {
	f := &fakeReplay{}
	e := setupWith(t, nil, func(o *Options) { o.Replay = f.run }) // FreePaths off
	outside := filepath.Join(t.TempDir(), "x.plgvrec")
	writeRec(t, outside, "")
	_, _, raw := e.do("POST", "/api/replay/validate", replayBody(e, func(rp map[string]any) { rp["path"] = outside }), nil)
	if !strings.Contains(raw, "replay.path") || !strings.Contains(raw, "off because plgv web accepts remote") {
		t.Fatalf("%s", raw)
	}
	if len(f.calls) != 0 {
		t.Fatal("replayed a file outside the folder")
	}
}
