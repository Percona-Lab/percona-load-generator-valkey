package servermetrics

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// Querier is what the poller needs from a client.
type Querier interface {
	Query(ctx context.Context, cmd valkey.Cmd) (any, error)
}

// Config controls polling.
type Config struct {
	Interval       time.Duration
	Slowlog        bool // poll SLOWLOG GET for new entries
	SlowlogEntries int  // entries fetched per poll
	Latency        bool // poll LATENCY LATEST
}

// Sample is one polled point of server gauges (the chart series).
type Sample struct {
	Offset         time.Duration
	OpsPerSec      float64 // from total_commands_processed deltas
	UsedMemory     int64
	ConnectedConns int64
	BlockedConns   int64
	HitRatio       float64 // over the interval; -1 when there were no lookups
	NetInKBps      float64
	NetOutKBps     float64
	FragRatio      float64
}

// MaxSamples bounds the stored gauge series.
const MaxSamples = 2048

// Poller samples the server on an interval.
type Poller struct {
	q     Querier
	cfg   Config
	start time.Time

	mu         sync.Mutex
	first      *Info
	last       *Info
	prev       *Info
	prevAt     time.Time
	ownAtFirst int64
	ownAtLast  int64
	prevOwnAt  int64
	own        int64 // poller commands issued so far
	series     []Sample
	stride     int
	pendingN   int
	pending    Sample
	slow       []SlowEntry
	slowNew    int64
	slowWorst  time.Duration
	lastSlowID int64
	haveSlowID bool
	latency    map[string]LatencyEvent
	pollErrors int64
	firstErr   string
	disabled   map[string]bool
	maxBlocked int64
	maxMem     int64
	maxFrag    float64
	maxFork    int64
	maxLagB    int64
}

// New creates a poller.
func New(q Querier, cfg Config) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.SlowlogEntries <= 0 {
		cfg.SlowlogEntries = 128
	}
	return &Poller{q: q, cfg: cfg, stride: 1, latency: map[string]LatencyEvent{}, disabled: map[string]bool{}}
}

func (p *Poller) query(ctx context.Context, cmd valkey.Cmd) (any, error) {
	p.own++
	return p.q.Query(ctx, cmd)
}

func (p *Poller) fail(what string, err error) {
	p.pollErrors++
	if p.firstErr == "" {
		p.firstErr = what + ": " + err.Error()
	}
}

// Run polls until ctx is canceled, then takes one last poll so the run's end
// is captured. It returns when done.
func (p *Poller) Run(ctx context.Context) {
	p.start = time.Now()
	p.poll(context.Background(), p.start) // baseline
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			p.poll(context.Background(), time.Now())
			return
		case now := <-t.C:
			p.poll(context.Background(), now)
		}
	}
}

func (p *Poller) poll(ctx context.Context, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// SLOWLOG and LATENCY first, INFO last, so the INFO reply is the latest word.
	if p.cfg.Slowlog && !p.disabled["slowlog"] {
		reply, err := p.query(ctx, valkey.Cmd{"SLOWLOG", "GET", itoa(p.cfg.SlowlogEntries)})
		if err != nil {
			p.fail("SLOWLOG", err)
			p.disabled["slowlog"] = true
		} else {
			p.absorbSlowlog(ParseSlowlog(reply))
		}
	}
	if p.cfg.Latency && !p.disabled["latency"] {
		reply, err := p.query(ctx, valkey.Cmd{"LATENCY", "LATEST"})
		if err != nil {
			p.fail("LATENCY", err)
			p.disabled["latency"] = true
		} else {
			for _, e := range ParseLatencyLatest(reply) {
				if old, ok := p.latency[e.Name]; !ok || e.MaxMs > old.MaxMs {
					p.latency[e.Name] = e
				}
			}
		}
	}
	ownBefore := p.own
	reply, err := p.query(ctx, valkey.Cmd{"INFO", "everything"})
	if err != nil {
		p.fail("INFO", err)
		return
	}
	text, _ := reply.(string)
	info := ParseInfo(text)
	if p.first == nil {
		p.first, p.ownAtFirst = info, ownBefore
	}
	p.last, p.ownAtLast = info, ownBefore
	p.addSample(info, now)
	p.prev, p.prevAt = info, now
}

func (p *Poller) absorbSlowlog(entries []SlowEntry) {
	if len(entries) == 0 {
		if !p.haveSlowID {
			p.haveSlowID = true // empty log at baseline
		}
		return
	}
	newest := entries[0].ID
	if !p.haveSlowID {
		// Baseline: remember the newest id; only later entries count as new.
		p.lastSlowID, p.haveSlowID = newest, true
		return
	}
	for _, e := range entries {
		if e.ID <= p.lastSlowID {
			continue
		}
		if isOwnCommand(e.Command, p.cfg.SlowlogEntries) {
			continue // plgv's own polling is not the workload's slowness
		}
		p.slowNew++
		if e.Duration > p.slowWorst {
			p.slowWorst = e.Duration
		}
		if len(p.slow) < 1000 {
			p.slow = append(p.slow, e)
		}
	}
	if newest > p.lastSlowID {
		p.lastSlowID = newest
	}
}

func (p *Poller) addSample(in *Info, now time.Time) {
	if used := in.Int("used_memory"); used > p.maxMem {
		p.maxMem = used
	}
	if b := in.Int("blocked_clients"); b > p.maxBlocked {
		p.maxBlocked = b
	}
	if f := in.Float("mem_fragmentation_ratio"); f > p.maxFrag {
		p.maxFrag = f
	}
	if f := in.Int("latest_fork_usec"); f > p.maxFork {
		p.maxFork = f
	}
	if l := in.ReplicaLagBytes(); l > p.maxLagB {
		p.maxLagB = l
	}

	s := Sample{
		Offset:     now.Sub(p.start),
		UsedMemory: in.Int("used_memory"), ConnectedConns: in.Int("connected_clients"),
		BlockedConns: in.Int("blocked_clients"), FragRatio: in.Float("mem_fragmentation_ratio"),
		HitRatio: -1,
	}
	if p.prev != nil {
		dt := now.Sub(p.prevAt).Seconds()
		if dt > 0 {
			// Subtract the poller's own commands executed in this interval.
			own := float64(p.ownAtLast - p.prevOwn())
			s.OpsPerSec = (float64(in.Int("total_commands_processed")-p.prev.Int("total_commands_processed")) - own) / dt
			if s.OpsPerSec < 0 {
				s.OpsPerSec = 0
			}
			s.NetInKBps = float64(in.Int("total_net_input_bytes")-p.prev.Int("total_net_input_bytes")) / 1024 / dt
			s.NetOutKBps = float64(in.Int("total_net_output_bytes")-p.prev.Int("total_net_output_bytes")) / 1024 / dt
		}
		h := in.Int("keyspace_hits") - p.prev.Int("keyspace_hits")
		m := in.Int("keyspace_misses") - p.prev.Int("keyspace_misses")
		if h+m > 0 {
			s.HitRatio = float64(h) / float64(h+m)
		}
	}
	p.appendSample(s)
}

// prevOwn is the poller command count at the previous INFO.
func (p *Poller) prevOwn() int64 { return p.prevOwnAt }

func (p *Poller) appendSample(s Sample) {
	p.prevOwnAt = p.ownAtLast
	if p.pendingN == 0 {
		p.pending = s
	} else {
		p.pending = avgSample(p.pending, s, p.pendingN)
	}
	p.pendingN++
	if p.pendingN < p.stride {
		return
	}
	p.series = append(p.series, p.pending)
	p.pendingN = 0
	if len(p.series) >= MaxSamples {
		n := len(p.series) / 2
		for i := 0; i < n; i++ {
			p.series[i] = avgSample(p.series[2*i], p.series[2*i+1], 1)
		}
		p.series = p.series[:n]
		p.stride *= 2
	}
}

// avgSample averages a (which already covers n samples) with one more sample b.
// For n = 1 it is the plain mean of the two. Offset follows the later sample.
func avgSample(a, b Sample, n int) Sample {
	w := float64(n)
	mix := func(x, y float64) float64 { return (x*w + y) / (w + 1) }
	out := Sample{
		Offset: b.Offset, OpsPerSec: mix(a.OpsPerSec, b.OpsPerSec),
		UsedMemory:     int64(mix(float64(a.UsedMemory), float64(b.UsedMemory))),
		ConnectedConns: int64(mix(float64(a.ConnectedConns), float64(b.ConnectedConns))),
		BlockedConns:   max(a.BlockedConns, b.BlockedConns),
		NetInKBps:      mix(a.NetInKBps, b.NetInKBps), NetOutKBps: mix(a.NetOutKBps, b.NetOutKBps),
		FragRatio: mix(a.FragRatio, b.FragRatio), HitRatio: -1,
	}
	switch {
	case a.HitRatio >= 0 && b.HitRatio >= 0:
		out.HitRatio = mix(a.HitRatio, b.HitRatio)
	case a.HitRatio >= 0:
		out.HitRatio = a.HitRatio
	default:
		out.HitRatio = b.HitRatio
	}
	return out
}

// isOwnCommand reports whether a slowlog entry is one of the commands the
// poller itself sends.
func isOwnCommand(cmd string, slowlogEntries int) bool {
	switch strings.ToUpper(cmd) {
	case "INFO EVERYTHING", "LATENCY LATEST", "SLOWLOG GET " + strconv.Itoa(slowlogEntries):
		return true
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }
