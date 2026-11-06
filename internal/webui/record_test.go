package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recBody(e *env, edit func(rec map[string]any)) map[string]any {
	b := e.defaultsBody(func(m map[string]any) {
		if edit != nil {
			edit(m["record"].(map[string]any))
		}
	})
	b["record"] = true
	return b
}

// Recording writes a file, so it only happens when the page asks for it.
func TestRecordNeedsTheRecordSwitch(t *testing.T) {
	e := setup(t, nil)
	for _, edit := range []func(rec map[string]any){
		func(rec map[string]any) { rec["path"] = "/tmp/leak.plgvrec" },
		func(rec map[string]any) { rec["outcomes"] = true },
		func(rec map[string]any) { rec["checksums"] = true },
	} {
		body := e.defaultsBody(func(m map[string]any) { edit(m["record"].(map[string]any)) })
		for _, path := range []string{"/api/start", "/api/validate"} {
			code, _, raw := e.do("POST", path, body, nil)
			if !strings.Contains(raw, "tick Record this run") || (path == "/api/start" && code != 400) {
				t.Fatalf("%s: %d %s", path, code, raw)
			}
		}
	}
	if code, _, raw := e.do("POST", "/api/validate", e.defaultsBody(nil), nil); code != 200 || strings.Contains(raw, "tick Record") {
		t.Fatalf("%d %s", code, raw)
	}
}

// With no path the server picks a new file name inside its recordings folder.
func TestRecordWithoutAPathGetsAServerChosenFile(t *testing.T) {
	e := setup(t, nil)
	body := recBody(e, func(rec map[string]any) { rec["compression"] = "zstd"; rec["outcomes"] = true })
	if code, _, raw := e.do("POST", "/api/start", body, nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	e.waitState("running")
	close(e.f.release)
	live := e.waitState("done")
	if live["run_id"] == nil {
		t.Fatalf("live = %v", live)
	}
	got := e.f.got[0].Record.Path
	dir := filepath.Join(e.srv.opt.DataDir, "recordings")
	if filepath.Dir(got) != dir || !strings.HasSuffix(got, ".plgvrec.zst") || !strings.HasPrefix(filepath.Base(got), "rec-") {
		t.Fatalf("path = %s", got)
	}
	_, list, raw := e.do("GET", "/api/recordings", nil, nil)
	recs, _ := list["recordings"].([]any)
	if len(recs) != 1 || !strings.Contains(raw, filepath.Base(got)) {
		t.Fatalf("list = %s", raw)
	}
	// The stored run says what was recorded, by name, with its path on the server.
	_, run, _ := e.do("GET", "/api/runs/"+live["run_id"].(string), nil, nil)
	rec, _ := run["recording"].(map[string]any)
	if rec["name"] != filepath.Base(got) || rec["status"] != "complete" {
		t.Fatalf("run recording = %v", rec)
	}
	// A second recording never replaces the first.
	e.f.release = make(chan struct{})
	e.do("POST", "/api/start", body, nil)
	e.waitState("running")
	close(e.f.release)
	e.waitState("done")
	if e.f.got[1].Record.Path == got {
		t.Fatal("a new run reused the file name")
	}
}

// A typed path is allowed on loopback and refused when remote connections are accepted.
func TestRecordTypedPathRules(t *testing.T) {
	outside := t.TempDir()
	free := setupWith(t, nil, func(o *Options) { o.FreePaths = true })
	locked := setup(t, nil)

	target := filepath.Join(outside, "mine.plgvrec")
	if code, _, raw := free.do("POST", "/api/validate", recBody(free, func(r map[string]any) { r["path"] = target }), nil); code != 200 || !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("free: %d %s", code, raw)
	}
	if _, _, raw := locked.do("POST", "/api/validate", recBody(locked, func(r map[string]any) { r["path"] = target }), nil); !strings.Contains(raw, "record.path") || !strings.Contains(raw, "off because plgv web accepts remote") {
		t.Fatalf("locked: %s", raw)
	}
	// A path inside the recordings folder is fine either way.
	inside := filepath.Join(locked.srv.opt.DataDir, "recordings", "a.plgvrec")
	if _, _, raw := locked.do("POST", "/api/validate", recBody(locked, func(r map[string]any) { r["path"] = inside }), nil); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("inside: %s", raw)
	}
	// A missing folder, a folder, and a file that is not a recording are refused even with free paths.
	notes := filepath.Join(outside, "notes.txt")
	_ = os.WriteFile(notes, []byte("keep"), 0o600)
	for name, p := range map[string]string{"folder": filepath.Join(outside, "no", "such", "x.plgvrec"), "dir": outside, "notes": notes} {
		_, _, raw := free.do("POST", "/api/validate", recBody(free, func(r map[string]any) { r["path"] = p }), nil)
		if !strings.Contains(raw, `"ok":false`) || !strings.Contains(raw, "record.path") {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	if b, _ := os.ReadFile(notes); string(b) != "keep" {
		t.Fatal("validation changed a file")
	}
}
