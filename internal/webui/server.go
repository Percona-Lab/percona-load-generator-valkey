package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/report"
)

//go:embed static
var staticFiles embed.FS

// Options configure the server.
type Options struct {
	DataDir  string // holds runs/ and settings/
	Version  string
	KeepRuns int                 // runs to keep; 0 = keep all
	Env      func(string) string // credentials come from here (PLGV_USERNAME, PLGV_PASSWORD, PLGV_URI)
	Run      RunFunc             // tests only
	Shutdown func()              // called by POST /api/shutdown
	// LoopbackOnly makes the server answer only requests addressed to localhost,
	// 127.0.0.1 or [::1], which stops DNS rebinding (a web page on another domain
	// that resolves to 127.0.0.1 and then drives this UI). Set it unless the
	// server was started with -allow-remote.
	LoopbackOnly bool
	// AllowedHosts, when set, replaces that rule: only requests addressed to one
	// of these host names (no port) are answered. Use it with -allow-remote, for
	// example behind a port published on 127.0.0.1 only.
	AllowedHosts []string
}

// Server is the web UI and API.
type Server struct {
	opt      Options
	hist     *History
	settings *SettingsStore
	mgr      *Manager
	mux      *http.ServeMux
}

// New creates the server and its data directories.
func New(opt Options) (*Server, error) {
	if opt.Env == nil {
		opt.Env = os.Getenv
	}
	hist, err := NewHistory(filepath.Join(opt.DataDir, "runs"))
	if err != nil {
		return nil, err
	}
	set, err := NewSettingsStore(filepath.Join(opt.DataDir, "settings"))
	if err != nil {
		return nil, err
	}
	s := &Server{opt: opt, hist: hist, settings: set, mgr: NewManager(hist, opt.Version, opt.KeepRuns, opt.Run), mux: http.NewServeMux()}
	sub, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/validate", s.handleValidate)
	s.mux.HandleFunc("/api/start", s.handleStart)
	s.mux.HandleFunc("/api/stop", s.handleStop)
	s.mux.HandleFunc("/api/live", s.handleLive)
	s.mux.HandleFunc("/api/runs", s.handleRuns)
	s.mux.HandleFunc("/api/runs/", s.handleRunItem)
	s.mux.HandleFunc("/api/compare", s.handleCompare)
	s.mux.HandleFunc("/api/replay-compare", s.handleReplayCompare)
	s.mux.HandleFunc("/api/sweeps", s.handleSweeps)
	s.mux.HandleFunc("/api/sweeps/", s.handleSweepItem)
	s.mux.HandleFunc("/api/trends", s.handleTrends)
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/settings/", s.handleSettingItem)
	s.mux.HandleFunc("/api/shutdown", s.handleShutdown)
	return s, nil
}

// Handler returns the HTTP handler (with the cross-site request guard).
func (s *Server) Handler() http.Handler { return guard(s.mux, s.opt.LoopbackOnly, s.opt.AllowedHosts) }

// Manager exposes the run manager (tests).
func (s *Server) Manager() *Manager { return s.mgr }

// guard rejects state-changing requests that did not come from this page:
// they must be JSON and, when the browser sends an Origin, it must match Host.
func guard(next http.Handler, loopbackOnly bool, allowed []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, loopbackOnly, allowed) {
			http.Error(w, "this server answers requests addressed to localhost, 127.0.0.1 or [::1] only", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", pageCSP)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					fail(w, http.StatusForbidden, "cross-site request refused")
					return
				}
			}
			if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, http.StatusUnsupportedMediaType, "send application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// FieldError ties a validation message to the configuration path it is about.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// fieldErrors turns a config validation error ("path: message" lines) into a list.
func fieldErrors(err error) []FieldError {
	out := []FieldError{}
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.Index(line, ": "); i > 0 && !strings.ContainsAny(line[:i], " ") {
			out = append(out, FieldError{Path: line[:i], Message: line[i+2:]})
		} else {
			out = append(out, FieldError{Message: line})
		}
	}
	return out
}

func (s *Server) readConfig(r *http.Request) (config.Config, string, error) {
	var body struct {
		Config map[string]any `json:"-"`
		Label  string         `json:"label"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return config.Config{}, "", err
	}
	all, err := config.DecodeJSONMap(raw)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("invalid JSON: %w", err)
	}
	if c, ok := all["config"].(map[string]any); ok {
		body.Config = c
	}
	if l, ok := all["label"].(string); ok {
		body.Label = l
	}
	if body.Config == nil {
		return config.Config{}, "", errors.New(`body must have a "config" object`)
	}
	cfg, err := config.ParseMap(body.Config)
	if err != nil {
		return cfg, body.Label, err
	}
	if cfg.Record.Path != "" || cfg.Record.Outcomes || cfg.Record.Checksums {
		return cfg, body.Label, errors.New("record: recording writes a file, so it is available from the command line only (plgv run -record)")
	}
	cfg.ApplyCredentialsEnv(s.opt.Env) // credentials come from the environment, never the form
	if err := cfg.ResolveSecrets(s.opt.Env); err != nil {
		return cfg, body.Label, err
	}
	cfg.Normalize()
	return cfg, body.Label, nil
}

// --- handlers ---

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	def := config.Default()
	def.ApplyEnv(s.opt.Env)
	m, err := def.ToMap()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ops := map[string]config.OpInfo{}
	for k, v := range config.Ops {
		ops[k] = v
	}
	type presetOut struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Workload    any    `json:"workload"`
		Dataset     any    `json:"dataset"`
		Concurrency int    `json:"concurrency"`
	}
	var presets []presetOut
	for _, p := range config.Presets() {
		pc := config.Default()
		p.Apply(&pc)
		pm, err := pc.ToMap()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		presets = append(presets, presetOut{Name: p.Name, Title: p.Title, Description: p.Description, Workload: pm["workload"], Dataset: pm["dataset"], Concurrency: pc.Run.Concurrency})
	}
	writeJSON(w, 200, map[string]any{
		"presets":  presets,
		"defaults": m, "ops": ops, "op_names": config.OpNames(), "version": s.opt.Version,
		"credentials_from_env": s.opt.Env("PLGV_USERNAME") != "" || s.opt.Env("PLGV_PASSWORD") != "",
	})
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := s.readConfig(r)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "errors": fieldErrors(err)})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "errors": []FieldError{}})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	cfg, label, err := s.readConfig(r)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "the configuration is not valid", "errors": fieldErrors(err)})
		return
	}
	if err := s.mgr.Start(cfg, strings.TrimSpace(label)); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"started": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	writeJSON(w, 200, map[string]any{"stopping": s.mgr.Stop()})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.mgr.Live())
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	metas, err := s.hist.List()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if metas == nil {
		metas = []Meta{}
	}
	writeJSON(w, 200, map[string]any{"runs": metas})
}

func (s *Server) handleRunItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	id, sub, _ := strings.Cut(rest, "/")
	switch {
	case r.Method == http.MethodDelete && sub == "":
		if err := s.hist.Delete(id); err != nil {
			notFoundOr500(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": id})
	case r.Method == http.MethodGet && sub == "":
		sum, err := s.hist.Get(id)
		if err != nil {
			notFoundOr500(w, err)
			return
		}
		writeJSON(w, 200, sum)
	case r.Method == http.MethodGet && sub == "report":
		sum, err := s.hist.Get(id)
		if err != nil {
			notFoundOr500(w, err)
			return
		}
		b, err := report.RenderBytes(sum)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", reportCSP)
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="plgv-report-%s.html"`, id))
		}
		_, _ = w.Write(b)
	default:
		fail(w, 404, "unknown run endpoint")
	}
}

func notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		fail(w, 404, "run not found")
		return
	}
	fail(w, 500, err.Error())
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	a, b := r.URL.Query().Get("a"), r.URL.Query().Get("b")
	if a == "" || b == "" {
		fail(w, 400, "give two run ids: ?a=...&b=...")
		return
	}
	sa, err := s.hist.Get(a)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	sb, err := s.hist.Get(b)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, 200, Compare(metaOf(a, sa), metaOf(b, sb), sa, sb))
}

func (s *Server) handleSweeps(w http.ResponseWriter, r *http.Request) {
	metas, err := s.hist.List()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	sweeps := ListSweeps(metas)
	writeJSON(w, 200, map[string]any{"sweeps": sweeps})
}

func (s *Server) handleSweepItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sweeps/")
	metas, err := s.hist.List()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	res, ok := BuildSweep(id, metas)
	if !ok {
		fail(w, 404, "sweep not found")
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleTrends(w http.ResponseWriter, r *http.Request) {
	metas, err := s.hist.List()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	groups := BuildTrends(metas)
	if groups == nil {
		groups = []TrendGroup{}
	}
	writeJSON(w, 200, map[string]any{"groups": groups, "min_runs": MinRunsForTrend, "baseline_runs": BaselineRuns})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	names, err := s.settings.List()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"settings": names})
}

func (s *Server) handleSettingItem(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/settings/"))
	switch r.Method {
	case http.MethodGet:
		c, err := s.settings.Get(name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				fail(w, 404, "saved settings not found")
			} else {
				fail(w, 500, err.Error())
			}
			return
		}
		m, _ := c.ToMap()
		writeJSON(w, 200, map[string]any{"name": name, "config": m})
	case http.MethodPut:
		cfg, _, err := s.readConfig(r)
		if err == nil {
			err = s.settings.Put(name, cfg)
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": "could not save", "errors": fieldErrors(err)})
			return
		}
		writeJSON(w, 200, map[string]any{"saved": name})
	case http.MethodDelete:
		if err := s.settings.Delete(name); err != nil {
			fail(w, 404, "saved settings not found")
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": name})
	default:
		fail(w, 405, "GET, PUT or DELETE")
	}
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	writeJSON(w, 200, map[string]any{"shutting_down": true})
	if s.opt.Shutdown != nil {
		go func() { time.Sleep(100 * time.Millisecond); s.opt.Shutdown() }()
	}
}

// ListenAndServe serves until ctx is canceled, then stops any run and shuts down.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		s.mgr.Stop()
		s.mgr.Wait()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// pageCSP: the page is one document with inline script and style and a bundled
// chart library; it talks only to its own server and cannot be framed.
const pageCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// reportCSP: a downloaded or viewed report is static HTML and SVG with inline
// style; it runs no script and loads nothing.
const reportCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox"

// loopbackHost reports whether a Host header names this machine's loopback.
func loopbackHost(host string) bool {
	switch strings.ToLower(hostName(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// hostAllowed applies the Host rule: an explicit list, else loopback names
// when loopbackOnly, else anything.
func hostAllowed(host string, loopbackOnly bool, allowed []string) bool {
	if len(allowed) > 0 {
		h := hostName(host)
		for _, a := range allowed {
			if strings.EqualFold(strings.TrimSpace(a), h) {
				return true
			}
		}
		return false
	}
	return !loopbackOnly || loopbackHost(host)
}

func hostName(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return strings.Trim(h, "[]")
}
