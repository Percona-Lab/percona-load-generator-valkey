// Package webui serves the plgv web interface and its JSON API.
package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
)

var idRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// ErrNotFound is returned for an unknown run or setting.
var ErrNotFound = errors.New("not found")

// Meta is the one-line description of a stored run.
type Meta struct {
	ID          string                `json:"id"`
	Label       string                `json:"label"`
	StartedAt   time.Time             `json:"started_at"`
	DurationSec float64               `json:"duration_seconds"`
	Ops         uint64                `json:"ops"`
	OpsPerSec   float64               `json:"ops_per_sec"`
	P50Ms       float64               `json:"p50_ms"`
	P99Ms       float64               `json:"p99_ms"`
	Errors      uint64                `json:"errors"`
	HitRatio    float64               `json:"hit_ratio"` // -1 = unknown (server metrics off or no lookups)
	ServerOn    bool                  `json:"server_metrics"`
	Critical    int                   `json:"critical"`
	Warnings    int                   `json:"warnings"`
	Infos       int                   `json:"infos"`
	Stopped     bool                  `json:"stopped"`
	Fingerprint string                `json:"fingerprint"`
	Mix         string                `json:"mix"` // short description of the workload
	ServerVer   string                `json:"server_version,omitempty"`
	P95Ms       float64               `json:"p95_ms"`
	Dropped     uint64                `json:"dropped_ops"`
	Loop        string                `json:"loop"`
	Mode        string                `json:"target_mode"`
	Workers     int                   `json:"peak_workers"`
	Sweep       *runsummary.SweepInfo `json:"sweep,omitempty"`
	// Target is the server address the run used (host and port, no credentials).
	Target string `json:"target,omitempty"`
	// ServerSetup is the user's name for the server setup (server_config.name); empty if none.
	ServerSetup string `json:"server_setup,omitempty"`
	// ServerConfigOn is true when the run recorded the server's settings.
	ServerConfigOn bool `json:"server_config,omitempty"`
}

func metaOf(id string, s *runsummary.Summary) Meta {
	m := Meta{
		ID: id, Label: s.Label, StartedAt: s.StartedAt, DurationSec: s.Client.ElapsedSeconds,
		Ops: s.Client.Ops, OpsPerSec: s.Client.OpsPerSec, P50Ms: s.Client.P50Ms, P99Ms: s.Client.P99Ms,
		Errors: s.Client.Errors, HitRatio: -1, ServerOn: s.Server != nil, Stopped: s.Stopped,
		Fingerprint: s.Fingerprint, Mix: mixSummary(s),
		P95Ms: s.Client.P95Ms, Dropped: s.Client.DroppedOps, Loop: s.Client.Loop, Mode: s.Client.TargetMode, Workers: s.Client.PeakWorkers, Sweep: s.Sweep,
	}
	if sc := s.ServerConfig; sc != nil {
		m.ServerSetup, m.ServerConfigOn = sc.Name, sc.Status == serverconfig.StatusOK
	}
	if s.Server != nil {
		m.HitRatio = s.Server.HitRatio
		m.ServerVer = s.Server.Version
	}
	m.Target = targetOf(s.ConfigYAML)
	for _, f := range s.Findings {
		switch f.Severity {
		case "critical":
			m.Critical++
		case "warning":
			m.Warnings++
		default:
			m.Infos++
		}
	}
	return m
}

// History stores run summaries as JSON files in a directory.
type History struct {
	dir string

	mu    sync.Mutex
	cache map[string]cachedMeta
}

type cachedMeta struct {
	mod  time.Time
	size int64
	meta Meta
}

// NewHistory opens (creating if needed) the run directory.
func NewHistory(dir string) (*History, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &History{dir: dir, cache: map[string]cachedMeta{}}, nil
}

func (h *History) path(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid run id %q", id)
	}
	return filepath.Join(h.dir, id+".json"), nil
}

// Save stores a summary and returns its id.
func (h *History) Save(s *runsummary.Summary) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	base := s.StartedAt.Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(h.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	if err := s.Write(filepath.Join(h.dir, id+".json")); err != nil {
		return "", err
	}
	return id, nil
}

// Get loads one run.
func (h *History) Get(id string) (*runsummary.Summary, error) {
	p, err := h.path(id)
	if err != nil {
		return nil, ErrNotFound
	}
	s, err := runsummary.Read(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return s, err
}

// Delete removes one run.
func (h *History) Delete(id string) error {
	p, err := h.path(id)
	if err != nil {
		return ErrNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.cache, id)
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}

// List returns every stored run, newest first. Unreadable files are skipped.
func (h *History) List() ([]Meta, error) {
	files, err := filepath.Glob(filepath.Join(h.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Meta
	for _, f := range files {
		id := filepath.Base(f)
		id = id[:len(id)-len(".json")]
		if !idRe.MatchString(id) {
			continue
		}
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		if c, ok := h.cache[id]; ok && c.mod.Equal(fi.ModTime()) && c.size == fi.Size() {
			out = append(out, c.meta)
			continue
		}
		s, err := runsummary.Read(f)
		if err != nil {
			continue
		}
		m := metaOf(id, s)
		h.cache[id] = cachedMeta{mod: fi.ModTime(), size: fi.Size(), meta: m}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// Prune keeps the newest `keep` runs and deletes the rest. keep <= 0 keeps all.
func (h *History) Prune(keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	metas, err := h.List()
	if err != nil || len(metas) <= keep {
		return 0, err
	}
	removed := 0
	for _, m := range metas[keep:] {
		if err := h.Delete(m.ID); err == nil {
			removed++
		}
	}
	return removed, nil
}

func mixSummary(s *runsummary.Summary) string {
	out := ""
	for i, o := range s.PerOp {
		if i == 4 {
			out += fmt.Sprintf(" +%d", len(s.PerOp)-4)
			break
		}
		if i > 0 {
			out += ", "
		}
		out += o.Op
	}
	return out
}

// jsonBytes marshals v for API replies.
func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// targetOf returns the first server address of a run's configuration as host
// and port: no scheme, user name, password or path.
func targetOf(configYAML string) string {
	u := flatten(configYAML)["target.uris[0]"]
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.LastIndex(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	return u
}
