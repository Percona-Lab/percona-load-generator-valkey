//go:build integration

package webui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// cleanupKeys removes the string keys a test run created (its dataset is small).
func cleanupKeys(t *testing.T, addr, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: addr, Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 600; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", prefix + "s:" + strconv.Itoa(i)}})
		}
	})
}

// A real run through the web API against a real Valkey: start, live data,
// stored run with server metrics, report, and the checkbox default.
func TestIntegrationWebRunAgainstRealValkey(t *testing.T) {
	addr := os.Getenv("PLGV_IT_VALKEY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	s, err := New(Options{DataDir: t.TempDir(), Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	cleanupKeys(t, addr, "it:web:")

	// Default settings: server metrics off, so a default run has no server section.
	body := e.defaultsBody(func(c map[string]any) {
		c["target"].(map[string]any)["uris"] = []any{addr}
		c["run"].(map[string]any)["duration"] = "2s"
		c["run"].(map[string]any)["concurrency"] = 8
		c["dataset"].(map[string]any)["key_count"] = 500
		c["dataset"].(map[string]any)["key_prefix"] = "it:web:"
		c["output"].(map[string]any)["sample_interval"] = "500ms"
	})
	body["label"] = "web off"
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	done := e.waitStateFor(StateDone, 30*time.Second)
	_, plain, _ := e.do("GET", "/api/runs/"+done["run_id"].(string), nil, nil)
	if plain["server"] != nil {
		t.Fatal("server metrics were off: the stored run must have no server section")
	}
	if plain["client"].(map[string]any)["ops"].(float64) == 0 {
		t.Fatal("no operations ran")
	}

	// Same workload with the checkbox on.
	body = e.defaultsBody(func(c map[string]any) {
		c["target"].(map[string]any)["uris"] = []any{addr}
		c["run"].(map[string]any)["duration"] = "2s"
		c["run"].(map[string]any)["concurrency"] = 8
		c["dataset"].(map[string]any)["key_count"] = 500
		c["dataset"].(map[string]any)["key_prefix"] = "it:web:"
		c["output"].(map[string]any)["sample_interval"] = "500ms"
		c["server_metrics"].(map[string]any)["enabled"] = true
		c["server_metrics"].(map[string]any)["interval"] = "500ms"
	})
	body["label"] = "web on"
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	var sawServer bool
	for i := 0; i < 200; i++ {
		_, live, _ := e.do("GET", "/api/live", nil, nil)
		if srv, ok := live["server"].(map[string]any); ok && len(srv["samples"].([]any)) > 0 {
			sawServer = true
		}
		if live["state"] == StateDone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawServer {
		t.Error("the live view never showed server samples while running")
	}
	done = e.waitStateFor(StateDone, 30*time.Second)
	id := done["run_id"].(string)
	_, sum, _ := e.do("GET", "/api/runs/"+id, nil, nil)
	srv, ok := sum["server"].(map[string]any)
	if !ok || srv["version"] == "" || srv["server_ops"].(float64) <= 0 {
		t.Fatalf("server section missing or empty: %v", sum["server"])
	}
	resp, err := http.Get(ts.URL + "/api/runs/" + id + "/report")
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(rb), "Server ops/s") || !strings.Contains(string(rb), "web on") {
		t.Fatal("the report lacks the server section")
	}
	// Cleanup of this test's keys.
	_, trends, _ := e.do("GET", "/api/trends", nil, nil)
	if trends["groups"] == nil {
		t.Fatal("trends")
	}
}

func TestIntegrationWebRunUnreachableServerReportsError(t *testing.T) {
	s, _ := New(Options{DataDir: t.TempDir()})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	body := e.defaultsBody(func(c map[string]any) { c["target"].(map[string]any)["uris"] = []any{"127.0.0.1:1"} })
	e.do("POST", "/api/start", body, nil)
	m := e.waitStateFor(StateError, 10*time.Second)
	if !strings.Contains(m["error"].(string), "127.0.0.1:1") {
		t.Fatalf("%v", m)
	}
}

// A sweep started from the web API against a real server stores one tagged run
// per level and serves the results table.
func TestIntegrationWebSweep(t *testing.T) {
	addr := os.Getenv("PLGV_IT_VALKEY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	s, err := New(Options{DataDir: t.TempDir(), Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	cleanupKeys(t, addr, "it:wsw:")
	body := e.defaultsBody(func(c map[string]any) {
		c["target"].(map[string]any)["uris"] = []any{addr}
		run := c["run"].(map[string]any)
		run["duration"] = "1s"
		run["sweep"] = map[string]any{"kind": "workers", "values": []any{1, 8}, "pause": "0s"}
		c["dataset"].(map[string]any)["key_count"] = 500
		c["dataset"].(map[string]any)["key_prefix"] = "it:wsw:"
		c["output"].(map[string]any)["sample_interval"] = "250ms"
	})
	body["label"] = "web sweep"
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	done := e.waitStateFor(StateDone, 30*time.Second)
	if done["sweep"].(map[string]any)["total"] != float64(2) {
		t.Fatalf("%v", done["sweep"])
	}
	_, sweeps, _ := e.do("GET", "/api/sweeps", nil, nil)
	list := sweeps["sweeps"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["done"] != float64(2) {
		t.Fatalf("%v", sweeps)
	}
	id := list[0].(map[string]any)["id"].(string)
	_, res, _ := e.do("GET", "/api/sweeps/"+id, nil, nil)
	rows := res["rows"].([]any)
	if res["complete"] != true || len(rows) != 2 || rows[0].(map[string]any)["label"] != "web sweep - 1 workers" {
		t.Fatalf("%v", res)
	}
}

// scanUnlink removes every key under a prefix (standalone).
func scanUnlink(t *testing.T, addr, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: addr, Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		cursor := "0"
		for {
			r, err := c.Query(context.Background(), valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
			arr, ok := r.([]any)
			if err != nil || !ok || len(arr) != 2 {
				return
			}
			for _, k := range arr[1].([]any) {
				c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", k.(string)}})
			}
			if cursor = arr[0].(string); cursor == "0" {
				return
			}
		}
	})
}

// A preset chosen in the UI (its values come from /api/config) runs through the
// API with the key-space analysis on; the stored run, its JSON and the report all
// carry the analysis, and the big-keys finding is raised.
func TestIntegrationWebPresetAndKeySpace(t *testing.T) {
	addr := os.Getenv("PLGV_IT_VALKEY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	s, err := New(Options{DataDir: t.TempDir(), Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	scanUnlink(t, addr, "it:wpre:")

	_, cfg, _ := e.do("GET", "/api/config", nil, nil)
	var preset map[string]any
	for _, p := range cfg["presets"].([]any) {
		if p.(map[string]any)["name"] == "big-keys" {
			preset = p.(map[string]any)
		}
	}
	if preset == nil {
		t.Fatal("the big-keys preset is not offered")
	}
	body := e.defaultsBody(func(c map[string]any) {
		c["workload"] = preset["workload"]
		d := preset["dataset"].(map[string]any)
		d["key_count"] = 60
		d["key_prefix"] = "it:wpre:"
		c["dataset"] = d
		c["target"].(map[string]any)["uris"] = []any{addr}
		c["run"].(map[string]any)["duration"] = "2s"
		c["run"].(map[string]any)["concurrency"] = 8
		c["output"].(map[string]any)["sample_interval"] = "500ms"
		c["keyspace_analysis"] = map[string]any{"enabled": true, "samples": 200, "top": 3, "match": ""}
	})
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	done := e.waitStateFor(StateDone, 40*time.Second)
	id := done["run_id"].(string)
	_, sum, _ := e.do("GET", "/api/runs/"+id, nil, nil)
	if sum["client"].(map[string]any)["errors"].(float64) != 0 {
		t.Fatalf("errors: %v", sum["client"])
	}
	ks, ok := sum["keyspace"].(map[string]any)
	if !ok || ks["sampled"].(float64) == 0 || ks["match"] != "it:wpre:*" || len(ks["big_keys"].([]any)) != 3 || ks["over_1mib"].(float64) == 0 {
		t.Fatalf("keyspace: %v", sum["keyspace"])
	}
	if !strings.Contains(sum["config_yaml"].(string), "preset: big-keys") {
		t.Fatal("the stored config must record the preset")
	}
	var found bool
	for _, f := range sum["findings"].([]any) {
		if f.(map[string]any)["id"] == "big-keys" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings: %v", sum["findings"])
	}
	resp, err := http.Get(ts.URL + "/api/runs/" + id + "/report")
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(rb), "Key space") || !strings.Contains(string(rb), "Biggest keys") {
		t.Fatal("the report lacks the key-space section")
	}
}

// Record a run through the web API, replay it on the same server through the
// web API, and read the stored result: the whole feature against a real Valkey.
func TestIntegrationWebRecordThenReplay(t *testing.T) {
	addr := os.Getenv("PLGV_IT_VALKEY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	s, err := New(Options{DataDir: t.TempDir(), Version: "it", FreePaths: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, srv: s, ts: ts}
	cleanupKeys(t, addr, "it:webrec:")

	body := e.defaultsBody(func(c map[string]any) {
		c["target"].(map[string]any)["uris"] = []any{addr}
		c["run"].(map[string]any)["duration"] = "2s"
		c["run"].(map[string]any)["concurrency"] = 4
		c["dataset"].(map[string]any)["key_count"] = 200
		c["dataset"].(map[string]any)["key_prefix"] = "it:webrec:"
		rec := c["record"].(map[string]any)
		rec["compression"], rec["outcomes"], rec["backpressure"] = "zstd", true, "block"
	})
	body["record"] = true
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("record: %d %s", code, raw)
	}
	done := e.waitStateFor(StateDone, 30*time.Second)
	_, run, _ := e.do("GET", "/api/runs/"+done["run_id"].(string), nil, nil)
	rec, _ := run["recording"].(map[string]any)
	if rec == nil || rec["status"] != "complete" || rec["ops"].(float64) == 0 || !strings.HasSuffix(rec["name"].(string), ".plgvrec.zst") {
		t.Fatalf("run recording = %v", rec)
	}
	recorded := rec["ops"].(float64)

	rb := e.defaultsBody(func(c map[string]any) { c["target"].(map[string]any)["uris"] = []any{addr} })
	rb["replay"] = map[string]any{"path": rec["name"], "speed": 0, "reads_only": true}
	if code, _, raw := e.do("POST", "/api/replay", rb, nil); code != 202 {
		t.Fatalf("replay: %d %s", code, raw)
	}
	fin := e.waitStateFor(StateDone, 30*time.Second)
	_, got, raw := e.do("GET", "/api/replays/"+fin["replay_id"].(string), nil, nil)
	ts0 := got["targets"].([]any)[0].(map[string]any)
	if ts0["failure"] != nil || ts0["ops"].(float64) == 0 || ts0["ops"].(float64) > recorded {
		t.Fatalf("replay = %s", raw)
	}
}
