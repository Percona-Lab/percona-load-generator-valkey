package topology

import (
	"context"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// Source is what the watcher needs from a client: any-node queries plus
// per-node queries (to read topology from a node that is still up).
type Source interface {
	Query(ctx context.Context, cmd valkey.Cmd) (any, error)
	valkey.NodeQuerier
}

// Read takes one snapshot, trying the client's default node first and then
// every known node, so a dead node does not blind the watcher.
func Read(ctx context.Context, src Source) (Snapshot, error) {
	try := func(q func(valkey.Cmd) (any, error)) (Snapshot, error) {
		reply, err := q(valkey.Cmd{"CLUSTER", "SHARDS"})
		if err != nil {
			return Snapshot{}, err
		}
		shards, err := ParseShards(reply)
		if err != nil {
			return Snapshot{}, err
		}
		snap := Snapshot{At: time.Now(), Shards: shards}
		// A half-read snapshot (shards but no state) must not look like "state fail".
		info, err := q(valkey.Cmd{"CLUSTER", "INFO"})
		if err != nil {
			return Snapshot{}, err
		}
		if s, ok := info.(string); ok {
			snap.StateOK, snap.SlotsAssigned = ParseClusterInfo(s)
		}
		return snap, nil
	}
	snap, err := try(func(c valkey.Cmd) (any, error) { return src.Query(ctx, c) })
	if err == nil {
		return snap, nil
	}
	for _, addr := range src.Nodes() {
		addr := addr
		if s, e := try(func(c valkey.Cmd) (any, error) { return src.QueryNode(ctx, addr, c) }); e == nil {
			return s, nil
		}
	}
	return Snapshot{}, err
}

// Watcher polls the cluster topology during a run and records what changed.
type Watcher struct {
	src      Source
	interval time.Duration
	start    time.Time
	router   *SlotRouter
	reg      *Registry

	mu         sync.Mutex
	first      Snapshot
	last       Snapshot
	events     []Event
	pollErrors int64
	firstErr   string
	downPolls  int
	stateBad   bool
}

// NewWatcher takes the baseline snapshot (an error means the cluster cannot be
// read at all) and creates the slot router used for per-node accounting.
func NewWatcher(ctx context.Context, src Source, interval time.Duration, reg *Registry) (*Watcher, error) {
	snap, err := Read(ctx, src)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = time.Second
	}
	// Register primaries first, in address order, so node indexes are stable.
	for _, n := range snap.Primaries() {
		reg.Index(n.Addr)
	}
	for _, n := range snap.Nodes() {
		reg.Index(n.Addr)
	}
	w := &Watcher{src: src, interval: interval, reg: reg, first: snap, last: snap, start: time.Now()}
	w.router = NewSlotRouter(reg, snap)
	w.stateBad = !snap.StateOK
	return w, nil
}

// Router returns the slot router (kept current by Run).
func (w *Watcher) Router() *SlotRouter { return w.router }

// Run polls until ctx ends, then takes one last snapshot.
func (w *Watcher) Run(ctx context.Context, start time.Time) {
	w.mu.Lock()
	w.start = start
	w.mu.Unlock()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.poll(context.Background())
			return
		case <-t.C:
			w.poll(ctx)
		}
	}
}

func (w *Watcher) poll(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snap, err := Read(cctx, w.src)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.pollErrors++
		w.downPolls++
		if w.firstErr == "" {
			w.firstErr = err.Error()
		}
		return
	}
	off := time.Since(w.start)
	for _, e := range Diff(w.last, snap) {
		e.Offset = off
		w.events = append(w.events, e)
	}
	w.router.Update(snap)
	for _, n := range snap.Nodes() {
		w.reg.Index(n.Addr)
	}
	w.last = snap
}

// Result is what the watcher found.
type Result struct {
	Initial, Final Snapshot
	Events         []Event
	PollErrors     int64
	FirstError     string
	ClusterDown    bool // cluster_state was not ok at some poll (or at the start)
}

// Result returns the findings so far.
func (w *Watcher) Result() Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	down := !w.first.StateOK || !w.last.StateOK
	for _, e := range w.events {
		if e.Kind == KindState {
			down = true
		}
	}
	return Result{Initial: w.first, Final: w.last, Events: append([]Event(nil), w.events...),
		PollErrors: w.pollErrors, FirstError: w.firstErr, ClusterDown: down}
}

// Events returns the events recorded so far (for the live view).
func (w *Watcher) Events() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event(nil), w.events...)
}
