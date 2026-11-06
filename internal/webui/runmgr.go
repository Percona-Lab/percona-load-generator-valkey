package webui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

// Run states reported to the UI.
const (
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StatePreloading = "preloading"
	StateRunning    = "running"
	StateFinishing  = "finishing"
	StateDone       = "done"
	StateError      = "error"
)

// ErrBusy is returned when a run is already in progress.
var ErrBusy = errors.New("a run is already in progress")

// RunFunc executes a session; tests replace it.
type RunFunc func(ctx context.Context, cfg config.Config, opt session.Options, h session.Hooks) (*session.Outcome, error)

// SweepFunc runs a sweep; tests replace it.
type SweepFunc func(ctx context.Context, cfg config.Config, opt session.Options, h session.Hooks) ([]*session.Outcome, error)

// LiveSweep is the progress of a running sweep.
type LiveSweep struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Index  int      `json:"index"` // level now running, 1-based
	Total  int      `json:"total"`
	Value  float64  `json:"value"`
	RunIDs []string `json:"run_ids"` // finished levels, stored
}

// LiveOp is the running total of one operation.
type LiveOp struct {
	Op     string  `json:"op"`
	Ops    uint64  `json:"ops"`
	Errors uint64  `json:"errors"`
	Misses uint64  `json:"misses"`
	P99Ms  float64 `json:"p99_ms"` // from the latest slice
}

// LiveServer is the server side of a running run.
type LiveServer struct {
	Samples    []runsummary.ServerPoint `json:"samples"`
	Version    string                   `json:"version"`
	PollErrors int64                    `json:"poll_errors"`
	FirstError string                   `json:"first_error,omitempty"`
	SlowlogNew int64                    `json:"slowlog_new"`
	Slowlog    []runsummary.SlowEntry   `json:"slowlog"`
	Mode       string                   `json:"mode"`
	Nodes      []runsummary.ServerNode  `json:"nodes,omitempty"`
	Topology   *runsummary.TopologyOut  `json:"topology,omitempty"`
}

// Live is the polled view of the current or last run.
type Live struct {
	State       string             `json:"state"`
	RunID       string             `json:"run_id,omitempty"` // stored run, once finished
	Label       string             `json:"label,omitempty"`
	StartedAt   time.Time          `json:"started_at,omitempty"`
	ElapsedSec  float64            `json:"elapsed_seconds"`
	DurationSec float64            `json:"duration_seconds"`
	ServerOn    bool               `json:"server_metrics"`
	Ops         uint64             `json:"ops"`
	Errors      uint64             `json:"errors"`
	Misses      uint64             `json:"misses"`
	Series      []runsummary.Point `json:"series"`
	PerOp       []LiveOp           `json:"per_op"`
	Server      *LiveServer        `json:"server,omitempty"`
	Sweep       *LiveSweep         `json:"sweep,omitempty"`
	Workers     int                `json:"active_workers"`
	Error       string             `json:"error,omitempty"`
	// Kind is "replay" while a replay (or its result) is shown; empty for a workload run.
	Kind     string      `json:"kind,omitempty"`
	Replay   *LiveReplay `json:"replay,omitempty"`
	ReplayID string      `json:"replay_id,omitempty"` // stored replay, once finished
}

// Manager owns the single active run.
type Manager struct {
	hist    *History
	version string
	run     RunFunc
	sweep   SweepFunc
	keep    int

	mu      sync.Mutex
	live    Live
	series  *stats.Series
	poller  servermetrics.Source
	cancel  context.CancelFunc
	started time.Time
	done    chan struct{}

	replays     *ReplayStore
	replayFn    ReplayFunc
	keepReplays int
	onRecording func(path string) // told the path of every recording a run wrote
}

// NewManager creates a manager that stores finished runs in hist.
func NewManager(hist *History, version string, keep int, run RunFunc) *Manager {
	if run == nil {
		run = session.Run
	}
	return &Manager{hist: hist, version: version, run: run, replayFn: session.ReplayManyWith, sweep: session.Sweep, keep: keep, live: Live{State: StateIdle}}
}

// Start begins a run in the background.
func (m *Manager) Start(cfg config.Config, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.live.State {
	case StateConnecting, StatePreloading, StateRunning, StateFinishing:
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.series = stats.NewSeries(stats.MaxSeriesPoints)
	m.poller = nil
	m.started = time.Now()
	m.live = Live{State: StateConnecting, Label: label, StartedAt: m.started, ServerOn: cfg.Server.Enabled,
		DurationSec: cfg.Run.Duration.Seconds()}
	for _, it := range cfg.Workload.Mix {
		m.live.PerOp = append(m.live.PerOp, LiveOp{Op: it.Op})
	}
	m.done = make(chan struct{})
	if k := cfg.Run.Sweep.Kind; k == "workers" || k == "rate" {
		go m.executeSweep(ctx, cfg, label)
	} else {
		go m.execute(ctx, cfg, label)
	}
	return nil
}

func (m *Manager) execute(ctx context.Context, cfg config.Config, label string) {
	defer close(m.done)
	out, err := m.run(ctx, cfg, session.Options{Version: m.version, Label: label}, session.Hooks{
		OnPhase: func(p string) {
			m.mu.Lock()
			m.live.State = p
			m.mu.Unlock()
		},
		OnSample: m.onSample,
		OnPoller: func(p servermetrics.Source) {
			m.mu.Lock()
			m.poller = p
			m.mu.Unlock()
		},
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	if err != nil {
		m.live.State, m.live.Error = StateError, err.Error()
		return
	}
	id, err := m.hist.Save(out.Summary)
	if err != nil {
		m.live.State, m.live.Error = StateError, "the run finished but could not be saved: "+err.Error()
		return
	}
	if m.keep > 0 {
		_, _ = m.hist.Prune(m.keep)
	}
	m.live.State, m.live.RunID = StateDone, id
	m.poller = nil
	if out.Recording != nil && m.onRecording != nil {
		m.onRecording(out.Recording.Path)
	}
}

// executeSweep runs every level of a sweep, storing each finished level as it ends.
func (m *Manager) executeSweep(ctx context.Context, cfg config.Config, label string) {
	defer close(m.done)
	var lastID string
	_, err := m.sweep(ctx, cfg, session.Options{Version: m.version, Label: label}, session.Hooks{
		OnPhase: func(p string) {
			m.mu.Lock()
			m.live.State = p
			m.mu.Unlock()
		},
		OnSample: m.onSample,
		OnPoller: func(p servermetrics.Source) {
			m.mu.Lock()
			m.poller = p
			m.mu.Unlock()
		},
		OnLevel: func(id string, index, total int, kind string, value float64) {
			m.mu.Lock()
			defer m.mu.Unlock()
			// Each level gets fresh live charts and counters.
			m.series = stats.NewSeries(stats.MaxSeriesPoints)
			m.poller = nil
			m.started = time.Now()
			m.live.Ops, m.live.Errors, m.live.Misses = 0, 0, 0
			for i := range m.live.PerOp {
				m.live.PerOp[i] = LiveOp{Op: m.live.PerOp[i].Op}
			}
			prev := []string{}
			if m.live.Sweep != nil {
				prev = m.live.Sweep.RunIDs
			}
			m.live.Sweep = &LiveSweep{ID: id, Kind: kind, Index: index, Total: total, Value: value, RunIDs: prev}
		},
		OnOutcome: func(o *session.Outcome) {
			id, err := m.hist.Save(o.Summary)
			m.mu.Lock()
			defer m.mu.Unlock()
			if err != nil {
				m.live.Error = "a level finished but could not be saved: " + err.Error()
				return
			}
			lastID = id
			m.live.Sweep.RunIDs = append(m.live.Sweep.RunIDs, id)
		},
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	m.poller = nil
	if m.keep > 0 {
		_, _ = m.hist.Prune(m.keep)
	}
	if err != nil {
		m.live.State, m.live.Error = StateError, err.Error()
		return
	}
	m.live.State, m.live.RunID = StateDone, lastID
}

func (m *Manager) onSample(s stats.Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.series.Add(s)
	m.live.Workers = s.Workers
	m.live.Ops += s.Ops
	m.live.Errors += s.Errors
	m.live.Misses += s.Misses
	for i, o := range s.PerOp {
		if i < len(m.live.PerOp) {
			p := &m.live.PerOp[i]
			p.Ops += o.Ops
			p.Errors += o.Errors
			p.Misses += o.Misses
			p.P99Ms = float64(o.P99) / float64(time.Millisecond)
		}
	}
}

// Stop asks the active run to end early. The partial run is saved.
func (m *Manager) Stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil {
		return false
	}
	m.cancel()
	return true
}

// Wait blocks until the active run finished (for tests and shutdown).
func (m *Manager) Wait() {
	m.mu.Lock()
	d := m.done
	m.mu.Unlock()
	if d != nil {
		<-d
	}
}

// Live returns the current view.
func (m *Manager) Live() Live {
	m.mu.Lock()
	l := m.live
	l.PerOp = append([]LiveOp{}, m.live.PerOp...) // copied under the lock: samples update it concurrently
	if l.Sweep != nil {                           // copy under the lock: levels finish concurrently
		sw := *l.Sweep
		sw.RunIDs = append([]string{}, sw.RunIDs...)
		l.Sweep = &sw
	}
	if m.live.Replay != nil {
		l.Replay = m.live.Replay.clone()
	}
	poller := m.poller
	started := m.started
	var samples []stats.Sample
	if m.series != nil {
		samples = m.series.Samples() // copy made under the lock: onSample adds concurrently
	}
	m.mu.Unlock()

	switch l.State {
	case StateConnecting, StatePreloading, StateRunning, StateFinishing:
		l.ElapsedSec = time.Since(started).Seconds()
	}
	for _, p := range samples {
		l.Series = append(l.Series, runsummary.Point{
			OffsetSeconds: p.Offset.Seconds(), OpsPerSec: p.OpsPerSec(), Errors: p.Errors,
			P50Ms: ms(p.P50), P95Ms: ms(p.P95), P99Ms: ms(p.P99), MaxMs: ms(p.Max),
		})
	}
	if l.Series == nil {
		l.Series = []runsummary.Point{}
	}
	if poller != nil {
		sm := poller.Summary()
		ls := &LiveServer{Version: sm.Version, PollErrors: sm.PollErrors, FirstError: sm.FirstError,
			SlowlogNew: sm.SlowlogNew, Samples: []runsummary.ServerPoint{}, Slowlog: []runsummary.SlowEntry{}}
		for _, p := range sm.Samples {
			ls.Samples = append(ls.Samples, runsummary.ServerPoint{
				OffsetSeconds: p.Offset.Seconds(), OpsPerSec: p.OpsPerSec, UsedMemory: p.UsedMemory,
				Connections: p.ConnectedConns, Blocked: p.BlockedConns, HitRatio: p.HitRatio,
				NetInKBps: p.NetInKBps, NetOutKBps: p.NetOutKBps, FragRatio: p.FragRatio,
			})
		}
		for _, e := range sm.SlowlogSample {
			ls.Slowlog = append(ls.Slowlog, runsummary.SlowEntry{ID: e.ID, UnixTime: e.Time.Unix(),
				DurationMs: ms(e.Duration), Command: e.Command})
		}
		full := runsummary.BuildServer(&sm)
		ls.Mode, ls.Nodes, ls.Topology = full.Mode, full.Nodes, full.Topology
		l.Server = ls
	}
	return l
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
