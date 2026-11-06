package topology

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// Asker is what the sentinel watcher needs from a connection to a sentinel.
type Asker interface {
	Query(ctx context.Context, cmd valkey.Cmd) (any, error)
	Close()
}

// SentinelWatcher polls the sentinels for the address of the master and
// records a failover when it changes.
type SentinelWatcher struct {
	master   string
	interval time.Duration
	sentinel []Asker

	mu         sync.Mutex
	start      time.Time
	current    string
	events     []Event
	pollErrors int64
	firstErr   string
}

// NewSentinelWatcher connects to each sentinel address with its own standalone
// client and reads the current master address (an error if none answers).
func NewSentinelWatcher(ctx context.Context, addrs []string, master, user, pass string, interval time.Duration) (*SentinelWatcher, error) {
	var askers []Asker
	for _, a := range addrs {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: a, Username: user, Password: pass, Workers: 1})
		if err != nil {
			continue // an unreachable sentinel is skipped; others may answer
		}
		askers = append(askers, c)
	}
	return NewSentinelWatcherFrom(ctx, askers, master, interval)
}

// NewSentinelWatcherFrom is NewSentinelWatcher over ready connections (tests).
func NewSentinelWatcherFrom(ctx context.Context, askers []Asker, master string, interval time.Duration) (*SentinelWatcher, error) {
	if interval <= 0 {
		interval = time.Second
	}
	w := &SentinelWatcher{master: master, interval: interval, sentinel: askers, start: time.Now()}
	addr, err := w.ask(ctx)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("no sentinel could tell the address of master %q: %w", master, err)
	}
	w.current = addr
	return w, nil
}

// ask returns the master address from the first sentinel that knows it.
func (w *SentinelWatcher) ask(ctx context.Context) (string, error) {
	var last error = errors.New("no sentinels")
	for _, s := range w.sentinel {
		reply, err := s.Query(ctx, valkey.Cmd{"SENTINEL", "GET-MASTER-ADDR-BY-NAME", w.master})
		if err != nil {
			last = err
			continue
		}
		parts, ok := reply.([]any)
		if !ok || len(parts) < 2 {
			last = fmt.Errorf("unknown master %q", w.master)
			continue
		}
		return fmt.Sprintf("%v:%v", parts[0], parts[1]), nil
	}
	return "", last
}

// Master returns the master address seen at the last poll.
func (w *SentinelWatcher) Master() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current
}

// Run polls until ctx ends, then once more.
func (w *SentinelWatcher) Run(ctx context.Context, start time.Time) {
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

func (w *SentinelWatcher) poll(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addr, err := w.ask(cctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.pollErrors++
		if w.firstErr == "" {
			w.firstErr = err.Error()
		}
		return
	}
	if addr != w.current {
		w.events = append(w.events, Event{Offset: time.Since(w.start), Kind: KindFailover,
			Detail: fmt.Sprintf("master %q moved from %s to %s", w.master, w.current, addr)})
		w.current = addr
	}
}

// Events returns the failovers seen so far.
func (w *SentinelWatcher) Events() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event(nil), w.events...)
}

// Result returns what was found.
func (w *SentinelWatcher) Result() (master string, events []Event, pollErrors int64, firstErr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current, append([]Event(nil), w.events...), w.pollErrors, w.firstErr
}

// Close closes the sentinel connections.
func (w *SentinelWatcher) Close() {
	for _, s := range w.sentinel {
		s.Close()
	}
}
