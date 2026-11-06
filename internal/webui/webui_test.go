package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// fakeRun stands in for session.Run: it reports phases and two samples, waits
// for release (or cancellation), then returns a summary built from cfg.
type fakeRun struct {
	mu      sync.Mutex
	got     []config.Config
	release chan struct{}
	ops     uint64
	p99     time.Duration
	hit     float64
	fail    error
	started time.Time // fixed start time for fixtures; zero = now
	slow    bool
}

func newFake() *fakeRun {
	return &fakeRun{release: make(chan struct{}), ops: 1000, p99: 2 * time.Millisecond, hit: 0.9}
}

func (f *fakeRun) run(ctx context.Context, cfg config.Config, opt session.Options, h session.Hooks) (*session.Outcome, error) {
	f.mu.Lock()
	f.got = append(f.got, cfg)
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	h.OnPhase(session.PhaseConnecting)
	h.OnPhase(session.PhasePreloading)
	h.OnPhase(session.PhaseRunning)
	for i := 1; i <= 2; i++ {
		h.OnSample(stats.Sample{Offset: time.Duration(i) * time.Second, Span: time.Second, Ops: f.ops / 2, P50: time.Millisecond, P99: f.p99,
			PerOp: []stats.OpSample{{Ops: f.ops / 2, P99: f.p99}}})
	}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	h.OnPhase(session.PhaseFinishing)
	hist := &latency.Histogram{}
	for i := 0; i < 97; i++ {
		hist.Record(time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		hist.Record(f.p99)
	}
	res := &valkey.RunResult{Ops: f.ops, Elapsed: 2 * time.Second, CPU: time.Second, Hist: hist, Seed: 1, SeriesFactor: 1,
		Series: []stats.Sample{{Offset: time.Second, Span: time.Second, Ops: f.ops / 2, P50: time.Millisecond, P99: f.p99}},
		PerOp:  []valkey.OpStat{{Op: cfg.Workload.Mix[0].Op, Ops: f.ops}}}
	var srv *servermetrics.Summary
	if cfg.Server.Enabled {
		srv = &servermetrics.Summary{Version: "9.1.3", HitRatio: f.hit, ServerOps: int64(f.ops)}
	}
	started := time.Now()
	if !f.started.IsZero() {
		started = f.started
	}
	sum, found := runsummary.Build("test", cfg, res, srv, started)
	sum.Label = opt.Label
	sum.Stopped = ctx.Err() != nil
	return &session.Outcome{Result: res, Server: srv, Summary: sum, Findings: found, StartedAt: started}, nil
}

type env struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	f   *fakeRun
}

func setup(t *testing.T, creds map[string]string) *env {
	t.Helper()
	f := newFake()
	s, err := New(Options{DataDir: t.TempDir(), Version: "vtest", Run: f.run, Env: func(k string) string { return creds[k] }, KeepRuns: 0})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{t, s, ts, f}
}

func (e *env) do(method, path string, body any, headers map[string]string) (int, map[string]any, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(jsonBytes(body))
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

// defaultsBody fetches the default config and optionally edits it.
func (e *env) defaultsBody(edit func(m map[string]any)) map[string]any {
	_, m, _ := e.do("GET", "/api/config", nil, nil)
	cfg := m["defaults"].(map[string]any)
	if edit != nil {
		edit(cfg)
	}
	return map[string]any{"config": cfg}
}

func (e *env) waitState(want string) map[string]any {
	e.t.Helper()
	return e.waitStateFor(want, 2*time.Second)
}

func (e *env) waitStateFor(want string, limit time.Duration) map[string]any {
	e.t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); {
		_, m, _ := e.do("GET", "/api/live", nil, nil)
		if m["state"] == want {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("never reached state %q", want)
	return nil
}

func TestConfigEndpointServerMetricsOffByDefault(t *testing.T) {
	e := setup(t, nil)
	code, m, _ := e.do("GET", "/api/config", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	sm := m["defaults"].(map[string]any)["server_metrics"].(map[string]any)
	if sm["enabled"] != false {
		t.Fatalf("server metrics must be off by default: %v", sm)
	}
	if m["ops"].(map[string]any)["HSET"] == nil || len(m["op_names"].([]any)) < 30 {
		t.Fatal("op catalog missing")
	}
	if m["defaults"].(map[string]any)["run"].(map[string]any)["duration"] != "10s" {
		t.Fatal("durations must be strings")
	}
}

func TestValidateReportsFieldPaths(t *testing.T) {
	e := setup(t, nil)
	body := e.defaultsBody(func(c map[string]any) {
		c["run"].(map[string]any)["concurrency"] = 0
		c["workload"].(map[string]any)["mix"] = []any{map[string]any{"op": "GET", "weight": 1, "count": 3}}
	})
	_, m, _ := e.do("POST", "/api/validate", body, nil)
	if m["ok"] != false {
		t.Fatalf("%v", m)
	}
	paths := map[string]bool{}
	for _, x := range m["errors"].([]any) {
		paths[x.(map[string]any)["path"].(string)] = true
	}
	if !paths["run.concurrency"] || !paths["workload.mix[0].count"] {
		t.Fatalf("paths %v", paths)
	}
	_, ok, _ := e.do("POST", "/api/validate", e.defaultsBody(nil), nil)
	if ok["ok"] != true {
		t.Fatalf("defaults must validate: %v", ok)
	}
}

func TestRunLifecycleAndHistory(t *testing.T) {
	e := setup(t, map[string]string{"PLGV_USERNAME": "admin", "PLGV_PASSWORD": "pw"})
	body := e.defaultsBody(func(c map[string]any) { c["server_metrics"].(map[string]any)["enabled"] = true })
	body["label"] = "first run"
	code, _, raw := e.do("POST", "/api/start", body, nil)
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	live := e.waitState(StateRunning)
	if live["label"] != "first run" || live["server_metrics"] != true {
		t.Fatalf("%v", live)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(live["series"].([]any)) < 2 && time.Now().Before(deadline) {
		_, live, _ = e.do("GET", "/api/live", nil, nil)
		time.Sleep(10 * time.Millisecond)
	}
	if len(live["series"].([]any)) != 2 || live["ops"].(float64) != 1000 {
		t.Fatalf("live series/ops: %v", live)
	}
	if po := live["per_op"].([]any); po[0].(map[string]any)["ops"].(float64) != 1000 {
		t.Fatalf("per op %v", po)
	}
	// One run at a time.
	if code, _, _ = e.do("POST", "/api/start", body, nil); code != 409 {
		t.Fatalf("second start: %d", code)
	}
	close(e.f.release)
	done := e.waitState(StateDone)
	id := done["run_id"].(string)

	// Credentials came from the environment, not the form.
	e.f.mu.Lock()
	got := e.f.got[0]
	e.f.mu.Unlock()
	if got.Target.Username != "admin" || got.Target.Password != "pw" {
		t.Fatalf("credentials not applied: %+v", got.Target)
	}

	_, list, _ := e.do("GET", "/api/runs", nil, nil)
	runs := list["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["label"] != "first run" || runs[0].(map[string]any)["hit_ratio"].(float64) != 0.9 {
		t.Fatalf("%v", list)
	}
	code, sum, raw := e.do("GET", "/api/runs/"+id, nil, nil)
	if code != 200 || sum["label"] != "first run" || strings.Contains(raw, "pw") {
		t.Fatalf("%d %v", code, strings.Contains(raw, "pw"))
	}
	resp, _ := http.Get(e.ts.URL + "/api/runs/" + id + "/report?download=1")
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(rb), "Valkey Benchmark Report: first run") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "plgv-report-"+id) {
		t.Fatalf("report: %d %s", resp.StatusCode, resp.Header)
	}
	if code, _, _ := e.do("DELETE", "/api/runs/"+id, nil, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := e.do("GET", "/api/runs/"+id, nil, nil); code != 404 {
		t.Fatal(code)
	}
}

func TestStopSavesPartialRun(t *testing.T) {
	e := setup(t, nil)
	e.do("POST", "/api/start", e.defaultsBody(nil), nil)
	e.waitState(StateRunning)
	_, m, _ := e.do("POST", "/api/stop", map[string]any{}, nil)
	if m["stopping"] != true {
		t.Fatal(m)
	}
	done := e.waitState(StateDone)
	_, sum, _ := e.do("GET", "/api/runs/"+done["run_id"].(string), nil, nil)
	if sum["stopped"] != true {
		t.Fatalf("a stopped run must be marked: %v", sum["stopped"])
	}
	if _, m, _ = e.do("POST", "/api/stop", map[string]any{}, nil); m["stopping"] != false {
		t.Fatal("nothing to stop")
	}
}

func TestRunErrorIsShown(t *testing.T) {
	e := setup(t, nil)
	e.f.fail = io_err("cannot connect to 127.0.0.1:1: refused")
	e.do("POST", "/api/start", e.defaultsBody(nil), nil)
	m := e.waitState(StateError)
	if !strings.Contains(m["error"].(string), "cannot connect") {
		t.Fatal(m)
	}
	// A new run can start after an error.
	e.f.fail = nil
	if code, _, _ := e.do("POST", "/api/start", e.defaultsBody(nil), nil); code != 202 {
		t.Fatal(code)
	}
	close(e.f.release)
	e.waitState(StateDone)
}

type io_err string

func (e io_err) Error() string { return string(e) }

func TestStartRejectsInvalidConfig(t *testing.T) {
	e := setup(t, nil)
	body := e.defaultsBody(func(c map[string]any) { c["run"].(map[string]any)["duration"] = "0s" })
	code, m, _ := e.do("POST", "/api/start", body, nil)
	if code != 400 || len(m["errors"].([]any)) == 0 {
		t.Fatalf("%d %v", code, m)
	}
	if code, _, _ := e.do("POST", "/api/start", map[string]any{"nope": 1}, nil); code != 400 {
		t.Fatal("body without config must be 400")
	}
}

func TestCrossSiteAndContentTypeGuard(t *testing.T) {
	e := setup(t, nil)
	body := e.defaultsBody(nil)
	if code, _, _ := e.do("POST", "/api/start", body, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("cross-site: %d", code)
	}
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/stop", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Fatalf("content type: %d", resp.StatusCode)
	}
	// Same-origin is fine.
	if code, _, _ := e.do("POST", "/api/validate", body, map[string]string{"Origin": e.ts.URL}); code != 200 {
		t.Fatalf("same-origin: %d", code)
	}
}

func TestRunIDsCannotEscapeTheDirectory(t *testing.T) {
	e := setup(t, nil)
	secret := filepath.Join(filepath.Dir(e.srv.hist.dir), "secret.json")
	_ = os.WriteFile(secret, []byte(`{"label":"x"}`), 0o600)
	for _, id := range []string{"..%2Fsecret", "..%2F..%2Fetc%2Fpasswd", "x", "20261104-1"} {
		if code, _, _ := e.do("GET", "/api/runs/"+id, nil, nil); code != 404 {
			t.Errorf("%s: %d", id, code)
		}
		if code, _, _ := e.do("DELETE", "/api/runs/"+id, nil, nil); code != 404 {
			t.Errorf("delete %s: %d", id, code)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("a file outside the run directory was touched")
	}
}

func TestSavedSettingsCRUD(t *testing.T) {
	e := setup(t, map[string]string{"PLGV_PASSWORD": "pw"})
	body := e.defaultsBody(func(c map[string]any) { c["dataset"].(map[string]any)["key_count"] = 12345 })
	if code, _, raw := e.do("PUT", "/api/settings/My%20test", body, nil); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, l, _ := e.do("GET", "/api/settings", nil, nil)
	if names := l["settings"].([]any); len(names) != 1 || names[0] != "My test" {
		t.Fatalf("%v", l)
	}
	_, got, _ := e.do("GET", "/api/settings/My%20test", nil, nil)
	if got["config"].(map[string]any)["dataset"].(map[string]any)["key_count"].(float64) != 12345 {
		t.Fatalf("%v", got)
	}
	data, _ := os.ReadFile(filepath.Join(e.srv.settings.dir, "My test.yaml"))
	if strings.Contains(string(data), "pw") {
		t.Fatal("credentials must not be saved")
	}
	// Invalid settings and bad names are refused.
	bad := e.defaultsBody(func(c map[string]any) { c["run"].(map[string]any)["concurrency"] = -1 })
	if code, m, _ := e.do("PUT", "/api/settings/x", bad, nil); code != 400 || len(m["errors"].([]any)) == 0 {
		t.Fatal(code)
	}
	if code, _, _ := e.do("PUT", "/api/settings/..%2Fevil", body, nil); code != 400 {
		t.Fatalf("path traversal name: %d", code)
	}
	if code, _, _ := e.do("DELETE", "/api/settings/My%20test", nil, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := e.do("GET", "/api/settings/My%20test", nil, nil); code != 404 {
		t.Fatal(code)
	}
}

func TestCompareAndTrendsEndpoints(t *testing.T) {
	e := setup(t, nil)
	var ids []string
	for i, p99 := range []time.Duration{2 * time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond} {
		f := newFake()
		f.p99 = p99
		f.ops = uint64(1000 + i)
		close(f.release)
		e.srv.mgr.run = f.run
		e.do("POST", "/api/start", e.defaultsBody(nil), nil)
		ids = append(ids, e.waitState(StateDone)["run_id"].(string))
	}
	code, c, _ := e.do("GET", "/api/compare?a="+ids[0]+"&b="+ids[3], nil, nil)
	if code != 200 || c["comparable"] != true {
		t.Fatalf("%d %v", code, c)
	}
	var p99row map[string]any
	for _, r := range c["metrics"].([]any) {
		if r.(map[string]any)["name"] == "p99 latency" {
			p99row = r.(map[string]any)
		}
	}
	if p99row["better"] != "a" || p99row["change_pct"].(float64) < 0.9 {
		t.Fatalf("p99 row %v", p99row)
	}
	if code, _, _ := e.do("GET", "/api/compare?a="+ids[0], nil, nil); code != 400 {
		t.Fatal(code)
	}
	if code, _, _ := e.do("GET", "/api/compare?a="+ids[0]+"&b=20000101-000000", nil, nil); code != 404 {
		t.Fatal(code)
	}
	_, tr, _ := e.do("GET", "/api/trends", nil, nil)
	groups := tr["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("%v", tr)
	}
	flags := groups[0].(map[string]any)["flags"].([]any)
	if len(flags) != 1 || flags[0].(map[string]any)["metric"] != "p99_ms" {
		t.Fatalf("the doubled p99 must be flagged: %v", flags)
	}
}

func TestShutdownEndpoint(t *testing.T) {
	called := make(chan struct{})
	s, _ := New(Options{DataDir: t.TempDir(), Shutdown: func() { close(called) }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp, _ := http.Post(ts.URL+"/api/shutdown", "application/json", strings.NewReader("{}"))
	resp.Body.Close()
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hook not called")
	}
}

func TestStaticIndexServed(t *testing.T) {
	e := setup(t, nil)
	resp, err := http.Get(e.ts.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp)
	}
	resp.Body.Close()
}

func TestFieldErrors(t *testing.T) {
	got := fieldErrors(io_err("run.duration: must be > 0, got 0s\nworkload.mix[1].op: unknown \"X\" (supported: GET)\nplain message"))
	if len(got) != 3 || got[0].Path != "run.duration" || got[1].Path != "workload.mix[1].op" || got[2].Path != "" || got[2].Message != "plain message" {
		t.Fatalf("%+v", got)
	}
}
