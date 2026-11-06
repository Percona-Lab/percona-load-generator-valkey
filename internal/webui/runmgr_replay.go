package webui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// maxLiveReplayPoints bounds the chart points kept per target while a replay runs.
const maxLiveReplayPoints = 3600

// Replay target states.
const (
	TargetWaiting = "waiting"
	TargetRunning = "running"
	TargetDone    = "done"
	TargetFailed  = "failed"
)

// LiveReplayTarget is one server of a running replay.
type LiveReplayTarget struct {
	Name       string             `json:"name"`
	Addr       string             `json:"addr"`
	State      string             `json:"state"`
	Ops        uint64             `json:"ops"`
	Errors     uint64             `json:"errors"`
	ElapsedSec float64            `json:"elapsed_seconds"`
	Series     []runsummary.Point `json:"series"`
	Error      string             `json:"error,omitempty"`

	started time.Time
}

// LiveReplay is the progress of a running replay.
type LiveReplay struct {
	Recording   string             `json:"recording"`
	RecordedOps uint64             `json:"recorded_ops"` // commands in one pass; 0 until known
	Iterations  int                `json:"iterations"`
	Concurrent  bool               `json:"concurrent"`
	ReadsOnly   bool               `json:"reads_only"`
	Speed       float64            `json:"speed"`
	Targets     []LiveReplayTarget `json:"targets"`
}

func (l *LiveReplay) clone() *LiveReplay {
	c := *l
	c.Targets = make([]LiveReplayTarget, len(l.Targets))
	for i, t := range l.Targets {
		c.Targets[i] = t
		c.Targets[i].Series = append([]runsummary.Point{}, t.Series...)
		if t.State == TargetRunning {
			c.Targets[i].ElapsedSec = time.Since(t.started).Seconds()
		}
	}
	return &c
}

// ReplayFunc replays a recording on every target; tests replace it.
type ReplayFunc func(ctx context.Context, cfg config.Config, targets []multitarget.Target, path string, opt replay.Options, concurrent, requireData bool, version string, h session.ReplayHooks) []session.TargetReplay

// StartReplay begins a replay in the background. It shares the one-run-at-a-time
// rule with workload runs.
func (m *Manager) StartReplay(spec ReplaySpec, cfg config.Config, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.live.State {
	case StateConnecting, StatePreloading, StateRunning, StateFinishing:
		return ErrBusy
	}
	if m.replays == nil {
		return fmt.Errorf("replays are not available")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.series, m.poller = nil, nil
	m.started = time.Now()
	lr := &LiveReplay{Recording: filepath.Base(spec.Path), Iterations: spec.Options.Iterations, Concurrent: spec.Concurrent,
		ReadsOnly: spec.Options.ReadsOnly, Speed: spec.Options.Speed}
	if p, err := workloadrec.Peek(spec.Path); err == nil && p.Trailer != nil {
		lr.RecordedOps = p.Trailer.Ops
	}
	for _, t := range spec.Targets {
		lr.Targets = append(lr.Targets, LiveReplayTarget{Name: t.Name, Addr: t.Addr, State: TargetWaiting, Series: []runsummary.Point{}})
	}
	m.live = Live{State: StateConnecting, Kind: "replay", Label: label, StartedAt: m.started, Replay: lr, Series: []runsummary.Point{}, PerOp: []LiveOp{}}
	m.done = make(chan struct{})
	go m.executeReplay(ctx, spec, cfg, label)
	return nil
}

func (m *Manager) target(name string) *LiveReplayTarget {
	for i := range m.live.Replay.Targets {
		if m.live.Replay.Targets[i].Name == name {
			return &m.live.Replay.Targets[i]
		}
	}
	return nil
}

func (m *Manager) executeReplay(ctx context.Context, spec ReplaySpec, cfg config.Config, label string) {
	defer close(m.done)
	m.mu.Lock()
	needCount := m.live.Replay != nil && m.live.Replay.RecordedOps == 0
	m.mu.Unlock()
	if needCount { // a compressed file's command count is only known after reading it
		if info, err := workloadrec.Scan(spec.Path); err == nil {
			m.mu.Lock()
			m.live.Replay.RecordedOps = info.Ops
			m.mu.Unlock()
		}
	}
	results := m.replayFn(ctx, cfg, spec.Targets, spec.Path, spec.Options, spec.Concurrent, spec.RequireData, m.version, session.ReplayHooks{
		OnTarget: func(_, _ int, name string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if t := m.target(name); t != nil {
				t.State, t.started = TargetRunning, time.Now()
			}
			m.live.State = StateRunning
		},
		OnSample: func(name string, s stats.Sample) {
			m.mu.Lock()
			defer m.mu.Unlock()
			t := m.target(name)
			if t == nil {
				return
			}
			t.Ops += s.Ops
			t.Errors += s.Errors
			t.Series = append(t.Series, runsummary.Point{OffsetSeconds: s.Offset.Seconds(), OpsPerSec: s.OpsPerSec(), Errors: s.Errors,
				P50Ms: ms(s.P50), P95Ms: ms(s.P95), P99Ms: ms(s.P99), MaxMs: ms(s.Max)})
			if n := len(t.Series); n > maxLiveReplayPoints {
				t.Series = append([]runsummary.Point(nil), t.Series[n-maxLiveReplayPoints:]...)
			}
		},
		OnDone: func(name string, tr session.TargetReplay) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if t := m.target(name); t != nil {
				t.ElapsedSec = time.Since(t.started).Seconds()
				if tr.Err != nil {
					t.State, t.Error = TargetFailed, tr.Err.Error()
				} else {
					t.State = TargetDone
					if tr.Summary != nil {
						t.Ops, t.Errors = tr.Summary.Ops, tr.Summary.Errors
					}
				}
			}
		},
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	var sums []*replay.Summary
	var firstErr error
	failed := 0
	for _, r := range results {
		sums = append(sums, r.Summary)
		if r.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.Target.Name, r.Err)
			}
		}
	}
	if failed == len(results) {
		m.live.State, m.live.Error = StateError, firstErr.Error()
		return
	}
	mode := "single"
	if len(sums) > 1 {
		mode = "sequential"
		if spec.Concurrent {
			mode = "concurrent"
		}
	}
	replaycompare.Uniquify(sums)
	sr := &StoredReplay{Label: label, FinishedAt: time.Now(), Stopped: ctx.Err() != nil, Mode: mode, Speed: spec.Options.Speed,
		ReadsOnly: spec.Options.ReadsOnly, Iterations: spec.Options.Iterations, Targets: sums, Comparisons: []*replaycompare.Comparison{}}
	for _, s := range sums {
		if s.Failure == "" {
			sr.Recording = s.Recording
			break
		}
	}
	if sr.Recording.Name == "" && len(sums) > 0 {
		sr.Recording = sums[0].Recording
	}
	if len(sums) > 1 {
		sr.Comparisons = replaycompare.CompareAll(sums, replaycompare.Options{Tolerance: spec.Tolerance / 100})
	}
	id, err := m.replays.Save(sr)
	if err != nil {
		m.live.State, m.live.Error = StateError, "the replay finished but could not be saved: "+err.Error()
		return
	}
	m.replays.Prune(m.keepReplays)
	m.live.State, m.live.ReplayID = StateDone, id
}
