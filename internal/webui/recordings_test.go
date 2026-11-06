package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// writeRec writes a small real recording at path.
func writeRec(t *testing.T, path, compression string) {
	t.Helper()
	rec, err := workloadrec.NewRecorder(path, workloadrec.Options{Compression: compression}, workloadrec.Header{PLGVVersion: "test", Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rec.Record(workloadrec.Entry{Worker: i % 2, Group: uint64(i + 1), Cmd: []string{"SET", "k", "v"}})
	}
	if r := rec.Close(); r.Status != workloadrec.StatusComplete {
		t.Fatalf("recording: %+v", r)
	}
}

func (e *env) recDir() string { return filepath.Join(e.srv.opt.DataDir, "recordings") }

func (e *env) put(path, ctype string, body []byte, headers map[string]string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest("PUT", e.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func TestRecordingsListShowsRecordingsAndNothingElse(t *testing.T) {
	e := setup(t, nil)
	writeRec(t, filepath.Join(e.recDir(), "a.plgvrec"), "")
	writeRec(t, filepath.Join(e.recDir(), "b.plgvrec.zst"), "zstd")
	_ = os.WriteFile(filepath.Join(e.recDir(), "notes.plgvrec"), []byte("not a recording\n"), 0o600) // named like one, is not
	_ = os.WriteFile(filepath.Join(e.recDir(), "readme.txt"), []byte("x"), 0o600)
	_ = os.Mkdir(filepath.Join(e.recDir(), "sub.plgvrec"), 0o700)
	if err := os.Symlink(filepath.Join(e.recDir(), "a.plgvrec"), filepath.Join(e.recDir(), "link.plgvrec")); err != nil {
		t.Skip("no symlinks")
	}
	_, m, raw := e.do("GET", "/api/recordings", nil, nil)
	recs := m["recordings"].([]any)
	names := map[string]map[string]any{}
	for _, r := range recs {
		x := r.(map[string]any)
		names[x["name"].(string)] = x
	}
	if len(names) != 2 || names["a.plgvrec"] == nil || names["b.plgvrec.zst"] == nil {
		t.Fatalf("list = %s", raw)
	}
	if names["a.plgvrec"]["ops"].(float64) != 3 || names["a.plgvrec"]["status"] != "complete" || names["a.plgvrec"]["compression"] != "none" {
		t.Fatalf("a = %v", names["a.plgvrec"])
	}
	if names["b.plgvrec.zst"]["compression"] != "zstd" || names["b.plgvrec.zst"]["ops"] != nil { // a compressed file's count is not read cheaply
		t.Fatalf("b = %v", names["b.plgvrec.zst"])
	}
	if m["free_paths"] != false || !strings.HasSuffix(m["dir"].(string), "recordings") {
		t.Fatalf("m = %v", m)
	}
}

func TestRecordingInfoAndPathResolution(t *testing.T) {
	e := setup(t, nil) // typed paths off
	free := setupWith(t, nil, func(o *Options) { o.FreePaths = true })
	writeRec(t, filepath.Join(e.recDir(), "a.plgvrec"), "")
	outside := filepath.Join(t.TempDir(), "elsewhere.plgvrec")
	writeRec(t, outside, "")
	notes := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(notes, []byte("secret text"), 0o600)

	info := func(e *env, p string) (int, map[string]any, string) {
		return e.do("GET", "/api/recordings/info?path="+p, nil, nil)
	}
	// By name, or by the full path from the list.
	if code, m, raw := info(e, "a.plgvrec"); code != 200 || m["ops"].(float64) != 3 || m["status"] != "complete" || m["commands_hash"] == "" {
		t.Fatalf("by name: %d %s", code, raw)
	}
	if code, _, raw := info(e, filepath.Join(e.recDir(), "a.plgvrec")); code != 200 {
		t.Fatalf("by path: %d %s", code, raw)
	}
	// A typed path elsewhere: refused when locked, allowed when free.
	if code, _, raw := info(e, outside); code != 400 || !strings.Contains(raw, "off because plgv web accepts remote") {
		t.Fatalf("locked: %d %s", code, raw)
	}
	if code, _, raw := info(free, outside); code != 200 {
		t.Fatalf("free: %d %s", code, raw)
	}
	// Even when free, only a real recording is read; the message never echoes the file.
	for name, p := range map[string]string{"text": notes, "folder": filepath.Dir(notes), "missing": filepath.Join(t.TempDir(), "nope.plgvrec")} {
		code, _, raw := info(free, p)
		if code != 400 || strings.Contains(raw, "secret text") {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	if _, _, raw := info(free, notes); !strings.Contains(raw, "not a plgv recording") {
		t.Fatalf("text: %s", raw)
	}
	// A name cannot climb out of the folder when locked.
	if code, _, _ := info(e, "../"+filepath.Base(e.srv.opt.DataDir)+"/runs/x"); code != 400 {
		t.Fatalf("traversal: %d", code)
	}
}

func TestRecordingUploadAndDelete(t *testing.T) {
	e := setup(t, nil)
	src := filepath.Join(t.TempDir(), "mine.plgvrec.zst")
	writeRec(t, src, "zstd")
	data, _ := os.ReadFile(src)

	code, raw := e.put("/api/recordings/upload?name="+"../../etc/my%20rec.zst", "application/octet-stream", data, nil)
	if code != 200 {
		t.Fatalf("upload: %d %s", code, raw)
	}
	var out struct {
		Recording RecordingEntry `json:"recording"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	r := out.Recording
	if filepath.Dir(r.Path) != e.recDir() || !strings.HasPrefix(r.Name, "upload-") || !strings.HasSuffix(r.Name, ".plgvrec.zst") || strings.Contains(r.Name, "..") {
		t.Fatalf("stored as %+v", r)
	}
	if fi, _ := os.Stat(r.Path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	// Something that is not a recording is refused and leaves nothing behind.
	before, _ := os.ReadDir(e.recDir())
	if code, raw := e.put("/api/recordings/upload?name=x", "application/octet-stream", []byte("hello\n"), nil); code != 400 || !strings.Contains(raw, "not a plgv recording") {
		t.Fatalf("bad upload: %d %s", code, raw)
	}
	if after, _ := os.ReadDir(e.recDir()); len(after) != len(before) {
		t.Fatalf("left files behind: %d -> %d", len(before), len(after))
	}
	// The upload route is the only PUT that may skip JSON, and a foreign origin never may.
	if code, _ := e.put("/api/settings/x", "application/octet-stream", data, nil); code != 415 {
		t.Fatalf("other PUT: %d", code)
	}
	if code, _ := e.put("/api/recordings/upload?name=x", "application/octet-stream", data, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("cross-site upload: %d", code)
	}
	// Delete: only a recording in the folder, by plain name.
	for _, bad := range []string{"", "../x", "a/b", "readme.txt"} {
		if code, _, _ := e.do("DELETE", "/api/recordings/delete?name="+bad, nil, nil); code != 400 {
			t.Fatalf("delete %q: %d", bad, code)
		}
	}
	keep := filepath.Join(t.TempDir(), "keep.plgvrec")
	writeRec(t, keep, "")
	if code, _, _ := e.do("DELETE", "/api/recordings/delete?name="+keep, nil, nil); code != 400 {
		t.Fatalf("a path must not delete: %d", code)
	}
	if code, _, raw := e.do("DELETE", "/api/recordings/delete?name="+r.Name, nil, nil); code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("deleted a file outside the folder")
	}
}

const monitorSample = `1.000001 [0 10.0.0.1:5000] "SET" "k1" "v1"
1.000002 [0 10.0.0.1:5000] "GET" "k1"
1.000003 [0 10.0.0.2:5001] "AUTH" "default" "hunter2"
1.000004 [0 10.0.0.2:5001] "INCR" "c"
`

func TestImportTrafficFromPastedText(t *testing.T) {
	e := setup(t, nil)
	code, m, raw := e.do("POST", "/api/import-traffic", map[string]any{"text": monitorSample, "compression": "none"}, nil)
	if code != 200 || m["commands"].(float64) != 3 {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "hunter2") {
		t.Fatal("a credential reached the response")
	}
	rec := m["recording"].(map[string]any)
	p := rec["path"].(string)
	if filepath.Dir(p) != e.recDir() || !strings.HasPrefix(filepath.Base(p), "import-") || rec["imported"] != true {
		t.Fatalf("recording = %v", rec)
	}
	var cmds []string
	err := workloadrec.Stream(context.Background(), p, func(op *workloadrec.OpLine) error {
		args, _ := op.Args()
		cmds = append(cmds, strings.Join(args, " "))
		return nil
	})
	if err != nil || strings.Join(cmds, "|") != "SET k1 v1|GET k1|INCR c" {
		t.Fatalf("commands = %v, %v", cmds, err)
	}
	// Bad input is refused plainly.
	for name, body := range map[string]map[string]any{
		"nothing":      {},
		"both":         {"text": monitorSample, "path": "/x"},
		"bad format":   {"text": monitorSample, "format": "xml"},
		"bad workers":  {"text": monitorSample, "max_workers": 5000},
		"only skipped": {"text": "1.0 [0 a:1] \"AUTH\" \"x\"\n"},
	} {
		if code, _, raw := e.do("POST", "/api/import-traffic", body, nil); code != 400 {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
}

func TestImportTrafficFromAServerPathNeedsFreePaths(t *testing.T) {
	file := filepath.Join(t.TempDir(), "monitor.txt")
	_ = os.WriteFile(file, []byte(monitorSample), 0o600)
	locked, free := setup(t, nil), setupWith(t, nil, func(o *Options) { o.FreePaths = true })
	if code, _, raw := locked.do("POST", "/api/import-traffic", map[string]any{"path": file}, nil); code != 400 || !strings.Contains(raw, "off because plgv web accepts remote") {
		t.Fatalf("locked: %d %s", code, raw)
	}
	if code, m, raw := free.do("POST", "/api/import-traffic", map[string]any{"path": file}, nil); code != 200 || m["commands"].(float64) != 3 {
		t.Fatalf("free: %d %s", code, raw)
	}
	if code, _, _ := free.do("POST", "/api/import-traffic", map[string]any{"path": t.TempDir()}, nil); code != 400 {
		t.Fatalf("a folder: %d", code)
	}
}
