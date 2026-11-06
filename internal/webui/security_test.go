package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, loopbackOnly bool) *Server {
	t.Helper()
	s, err := New(Options{DataDir: t.TempDir(), Version: "t", LoopbackOnly: loopbackOnly})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func req(s *Server, method, host, path, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader("{}"))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A page on another domain that resolves to 127.0.0.1 (DNS rebinding) sends
// requests with its own Host and a matching Origin: the Host check stops it.
func TestDNSRebindingIsRefused(t *testing.T) {
	s := serve(t, true)
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "LOCALHOST:8080", "localhost"} {
		if w := req(s, "GET", host, "/api/live", ""); w.Code != 200 {
			t.Errorf("%s: %d", host, w.Code)
		}
	}
	for _, host := range []string{"evil.example:8080", "127.0.0.1.evil.example:8080", "10.0.0.5:8080", "localhost.evil.example"} {
		if w := req(s, "GET", host, "/api/live", ""); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", host, w.Code)
		}
		if w := req(s, "POST", host, "/api/shutdown", "http://"+host); w.Code != http.StatusForbidden {
			t.Errorf("POST %s: %d, want 403 even with a matching Origin", host, w.Code)
		}
	}
	// With -allow-remote the operator chose to expose it: any Host is served.
	if w := req(serve(t, false), "GET", "plgv.internal:8080", "/api/live", ""); w.Code != 200 {
		t.Errorf("remote mode: %d", w.Code)
	}
}

func TestCrossSiteWritesAreRefused(t *testing.T) {
	s := serve(t, true)
	if w := req(s, "POST", "127.0.0.1:8080", "/api/stop", "http://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/stop", strings.NewReader("x=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a form post (the cross-site kind) must be refused: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := serve(t, true)
	for _, p := range []string{"/", "/api/live"} {
		h := req(s, "GET", "127.0.0.1:1", p, "").Header()
		for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
			if h.Get(k) != want {
				t.Errorf("%s %s = %q", p, k, h.Get(k))
			}
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s CSP lacks %q: %s", p, want, csp)
			}
		}
		if strings.Contains(csp, "http:") || strings.Contains(csp, "https:") || strings.Contains(csp, " *") {
			t.Errorf("the page must not load anything from elsewhere: %s", csp)
		}
	}
}

func TestRunIDsAndSettingNamesCannotEscapeTheDataDirectory(t *testing.T) {
	s := serve(t, true)
	for _, p := range []string{"/api/runs/..%2f..%2fetc%2fpasswd", "/api/runs/../../etc/passwd", "/api/runs/%2e%2e%2fsettings", "/api/settings/..%2fruns%2fx", "/api/settings/..%2f..%2fx"} {
		w := req(s, "GET", "127.0.0.1:1", p, "")
		if w.Code == 200 || strings.Contains(w.Body.String(), "root:") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	w := req(s, "DELETE", "127.0.0.1:1", "/api/settings/..%2f..%2fvictim", "")
	if w.Code == 200 {
		t.Errorf("delete outside the settings directory: %d", w.Code)
	}
}

func TestReportIsServedWithAStrictPolicy(t *testing.T) {
	e := setup(t, nil)
	close(e.f.release)
	e.do("POST", "/api/start", e.defaultsBody(nil), nil)
	id := e.waitState(StateDone)["run_id"].(string)
	r, err := http.Get(e.ts.URL + "/api/runs/" + id + "/report")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	csp := r.Header.Get("Content-Security-Policy")
	if r.StatusCode != 200 || !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") || strings.Contains(csp, "script-src") {
		t.Fatalf("%d %q", r.StatusCode, csp)
	}
}

func TestAllowedHostsReplaceTheLoopbackRule(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "t", AllowedHosts: []string{"localhost", "plgv.internal"}})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{"localhost:8080": 200, "PLGV.internal:8080": 200, "evil.example": 403, "127.0.0.1:8080": 403} {
		if w := req(s, "GET", host, "/api/live", ""); w.Code != want {
			t.Errorf("%s: %d, want %d", host, w.Code, want)
		}
	}
}
