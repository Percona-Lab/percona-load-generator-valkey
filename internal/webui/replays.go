package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
)

// StoredReplay is a finished replay: one summary per server and, with two or
// more, the comparisons. It holds totals, never a command or a credential.
type StoredReplay struct {
	ID          string                      `json:"id"`
	Label       string                      `json:"label,omitempty"`
	FinishedAt  time.Time                   `json:"finished_at"`
	Stopped     bool                        `json:"stopped,omitempty"`
	Mode        string                      `json:"mode"` // single, sequential or concurrent
	Recording   replay.RecordingRef         `json:"recording"`
	Speed       float64                     `json:"speed"`
	ReadsOnly   bool                        `json:"reads_only"`
	Iterations  int                         `json:"iterations"`
	Targets     []*replay.Summary           `json:"targets"`
	Comparisons []*replaycompare.Comparison `json:"comparisons"`
}

// ReplayMeta is the one-line description of a stored replay.
type ReplayMeta struct {
	ID         string            `json:"id"`
	Label      string            `json:"label,omitempty"`
	FinishedAt time.Time         `json:"finished_at"`
	Stopped    bool              `json:"stopped,omitempty"`
	Mode       string            `json:"mode"`
	Recording  string            `json:"recording"`
	ReadsOnly  bool              `json:"reads_only"`
	Speed      float64           `json:"speed"`
	Targets    []ReplayMetaTable `json:"targets"`
}

// ReplayMetaTable is one server's headline numbers.
type ReplayMetaTable struct {
	Name        string  `json:"name"`
	Target      string  `json:"target"`
	Ops         uint64  `json:"ops"`
	Errors      uint64  `json:"errors"`
	OpsPerSec   float64 `json:"ops_per_sec"`
	P99Ms       float64 `json:"p99_ms"`
	HashMatches bool    `json:"hash_matches"`
	Failure     string  `json:"failure,omitempty"`
}

func replayMeta(r *StoredReplay) ReplayMeta {
	m := ReplayMeta{ID: r.ID, Label: r.Label, FinishedAt: r.FinishedAt, Stopped: r.Stopped, Mode: r.Mode, Recording: r.Recording.Name, ReadsOnly: r.ReadsOnly, Speed: r.Speed}
	for _, t := range r.Targets {
		m.Targets = append(m.Targets, ReplayMetaTable{Name: t.Label, Target: t.Target, Ops: t.Ops, Errors: t.Errors, OpsPerSec: t.OpsPerSec,
			P99Ms: t.Latency.P99, HashMatches: t.HashMatches, Failure: t.Failure})
	}
	return m
}

// ReplayStore keeps finished replays as JSON files in a directory.
type ReplayStore struct {
	dir string
	mu  sync.Mutex
}

// NewReplayStore opens (creating if needed) the replay directory.
func NewReplayStore(dir string) (*ReplayStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &ReplayStore{dir: dir}, nil
}

func (rs *ReplayStore) path(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid replay id %q", id)
	}
	return filepath.Join(rs.dir, id+".json"), nil
}

// Save stores a replay and returns its id.
func (rs *ReplayStore) Save(r *StoredReplay) (string, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	base := r.FinishedAt.Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(rs.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	r.ID = id
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(rs.dir, id+".json"), append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// Get loads one replay.
func (rs *ReplayStore) Get(id string) (*StoredReplay, error) {
	p, err := rs.path(id)
	if err != nil {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r StoredReplay
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Delete removes one replay.
func (rs *ReplayStore) Delete(id string) error {
	p, err := rs.path(id)
	if err != nil {
		return ErrNotFound
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}

// List returns every stored replay, newest first.
func (rs *ReplayStore) List() []ReplayMeta {
	files, _ := filepath.Glob(filepath.Join(rs.dir, "*.json"))
	out := []ReplayMeta{}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if r, err := rs.Get(id); err == nil {
			out = append(out, replayMeta(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FinishedAt.Equal(out[j].FinishedAt) {
			return out[i].FinishedAt.After(out[j].FinishedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Prune keeps the newest keep replays. keep <= 0 keeps all.
func (rs *ReplayStore) Prune(keep int) {
	if keep <= 0 {
		return
	}
	l := rs.List()
	for i := keep; i < len(l); i++ {
		_ = rs.Delete(l[i].ID)
	}
}

// --- starting a replay ---

// ReplaySpec is a validated replay request.
type ReplaySpec struct {
	Path        string
	Targets     []multitarget.Target // one or more; the first is the form's server when none were listed
	Concurrent  bool
	Options     replay.Options
	RequireData bool
	Tolerance   float64 // percent
}

// readReplay parses and validates a replay request. The server and its TLS
// settings come from the same "config" object the run form sends; credentials
// come from the environment. problems is a list of field errors, empty when ok.
func (s *Server) readReplay(r *http.Request) (spec ReplaySpec, cfg config.Config, label string, problems []FieldError) {
	raw, err := readLimited(r, 1<<20)
	if err != nil {
		return spec, cfg, "", []FieldError{{Message: err.Error()}}
	}
	all, err := config.DecodeJSONMap(raw)
	if err != nil {
		return spec, cfg, "", []FieldError{{Message: "invalid JSON: " + err.Error()}}
	}
	label, _ = all["label"].(string)
	label = strings.TrimSpace(label)
	cm, _ := all["config"].(map[string]any)
	if cm == nil {
		return spec, cfg, label, []FieldError{{Message: `body must have a "config" object`}}
	}
	if cfg, err = config.ParseMap(cm); err != nil {
		return spec, cfg, label, fieldErrors(err)
	}
	cfg.ApplyCredentialsEnv(s.opt.Env)
	if err := cfg.ResolveSecrets(s.opt.Env); err != nil {
		return spec, cfg, label, fieldErrors(err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		problems = append(problems, fieldErrors(err)...)
	}
	add := func(path, msg string, a ...any) {
		problems = append(problems, FieldError{Path: path, Message: fmt.Sprintf(msg, a...)})
	}
	rp, _ := all["replay"].(map[string]any)
	if rp == nil {
		rp = map[string]any{}
	}
	str := func(k string) string { v, _ := rp[k].(string); return strings.TrimSpace(v) }
	boolean := func(k string) bool { v, _ := rp[k].(bool); return v }
	num := func(k string, def float64) float64 {
		switch v := rp[k].(type) {
		case float64:
			return v
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
		return def
	}

	if path, err := s.resolveRecording(str("path")); err != nil {
		add("replay.path", "%v", err)
	} else {
		spec.Path = path
	}
	speed := num("speed", 1)
	if speed < 0 || speed > 1000 {
		add("replay.speed", "must be between 0 (as fast as possible) and 1000, got %v", speed)
	}
	iters := int(num("iterations", 1))
	if iters < 1 || iters > 1000 {
		add("replay.iterations", "must be 1-1000, got %d", iters)
	}
	var delay time.Duration
	if d := str("iteration_delay"); d != "" {
		if delay, err = time.ParseDuration(d); err != nil || delay < 0 || delay > 24*time.Hour {
			add("replay.iteration_delay", "must be a duration such as 5s or 1m, up to 24h, got %q", d)
		}
	}
	spec.Tolerance = num("tolerance", 10)
	if spec.Tolerance < 0 || spec.Tolerance > 1000 {
		add("replay.tolerance", "is a percent between 0 and 1000")
	}
	spec.Options = replay.Options{Speed: speed, ReadsOnly: boolean("reads_only"), Iterations: iters, IterationDelay: delay, AllowDangerous: boolean("allow_dangerous")}
	spec.RequireData, spec.Concurrent = boolean("require_data"), boolean("concurrent")

	var lines []string
	if list, ok := rp["targets"].([]any); ok {
		for _, v := range list {
			if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
				lines = append(lines, t)
			}
		}
	}
	if len(lines) > 0 {
		ts, err := multitarget.Parse(lines)
		switch {
		case err != nil:
			add("replay.targets", "%v", err)
		case len(ts) < 2:
			add("replay.targets", "needs at least two servers (leave it empty to replay on the server in Target)")
		default:
			spec.Targets = ts
			for _, t := range ts { // credentials never travel in an address (they would be stored with the result)
				tt := cfg.Target
				tt.URIs = splitAddrList(t.Addr)
				if _, err := tt.Addrs(); err != nil {
					add("replay.targets", "%s: %v", t.Name, err)
				}
			}
		}
	} else {
		a, aerr := cfg.Target.Addrs()
		if aerr != nil || len(a) == 0 {
			add("target.uris", "give the server to replay on")
		} else {
			name := strings.Join(a, ",")
			if label != "" {
				name = label
			}
			spec.Targets = []multitarget.Target{{Name: name, Addr: strings.Join(a, ",")}}
		}
		if spec.Concurrent {
			add("replay.concurrent", "needs two or more targets")
		}
	}
	return spec, cfg, label, problems
}

func readLimited(r *http.Request, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("the request is larger than %d bytes", n)
	}
	return b, nil
}

func (s *Server) handleReplayValidate(w http.ResponseWriter, r *http.Request) {
	_, _, _, problems := s.readReplay(r)
	if len(problems) > 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "errors": problems})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "errors": []FieldError{}})
}

func (s *Server) handleReplayStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	spec, cfg, label, problems := s.readReplay(r)
	if len(problems) > 0 {
		writeJSON(w, 400, map[string]any{"error": "the replay is not valid", "errors": problems})
		return
	}
	if err := s.mgr.StartReplay(spec, cfg, label); err != nil {
		code := 500
		if errors.Is(err, ErrBusy) {
			code = 409
		}
		fail(w, code, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true})
}

func (s *Server) handleReplays(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET only")
		return
	}
	writeJSON(w, 200, map[string]any{"replays": s.replays.List()})
}

func (s *Server) handleReplayItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/replays/")
	switch r.Method {
	case http.MethodGet:
		rp, err := s.replays.Get(id)
		if errors.Is(err, ErrNotFound) {
			fail(w, 404, "no such replay")
			return
		}
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, rp)
	case http.MethodDelete:
		if err := s.replays.Delete(id); errors.Is(err, ErrNotFound) {
			fail(w, 404, "no such replay")
		} else if err != nil {
			fail(w, 500, err.Error())
		} else {
			writeJSON(w, 200, map[string]any{"ok": true})
		}
	default:
		fail(w, 405, "GET or DELETE")
	}
}

// splitAddrList splits comma or space separated addresses.
func splitAddrList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}
