package webui

import (
	"strings"
	"testing"
)

// A request must not choose a file for the server to write.
func TestWebRefusesToRecord(t *testing.T) {
	e := setup(t, nil)
	for _, edit := range []func(rec map[string]any){
		func(rec map[string]any) { rec["path"] = "/tmp/leak.jsonl" },
		func(rec map[string]any) { rec["outcomes"] = true },
		func(rec map[string]any) { rec["checksums"] = true },
	} {
		body := e.defaultsBody(func(m map[string]any) { edit(m["record"].(map[string]any)) })
		for _, path := range []string{"/api/start", "/api/validate"} {
			code, _, raw := e.do("POST", path, body, nil)
			if path == "/api/start" && (code != 400 || !strings.Contains(raw, "command line only")) {
				t.Fatalf("%s: %d %s", path, code, raw)
			}
			if path == "/api/validate" && !strings.Contains(raw, "command line only") {
				t.Fatalf("%s: %d %s", path, code, raw)
			}
		}
	}
	// The untouched defaults still start.
	if code, _, raw := e.do("POST", "/api/validate", e.defaultsBody(nil), nil); code != 200 || strings.Contains(raw, "command line only") {
		t.Fatalf("%d %s", code, raw)
	}
}
