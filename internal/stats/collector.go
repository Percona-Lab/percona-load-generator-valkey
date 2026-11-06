// Package stats collects client-side run statistics: per-operation counts and
// latencies, error classes, a bounded time series, and exact run percentiles.
package stats

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
)

// Obs is one finished operation inside a batch.
type Obs struct {
	Op    int      // index into the mix
	Class ErrClass // ErrNone for success
	Miss  bool     // nil reply (not an error)
	Node  int      // cluster: 1-based node number from the registry; 0 = not attributed
}

// MaxNodes bounds the per-node series (matches topology.MaxNodes).
const MaxNodes = 64

type opWindow struct {
	ops, errs, misses uint64
	hist              *latency.Histogram // allocated on first success
}

// window holds what one worker recorded since the last snapshot.
type window struct {
	ops, errs, misses uint64
	hist              latency.Histogram
	per               []opWindow
	cls               [NumErrClasses]uint64
	nodeOps, nodeErrs [MaxNodes]uint64
	nodeMax           int // highest node number used + 1
}

func newWindow(nOps int) *window { return &window{per: make([]opWindow, nOps)} }

func (w *window) reset() {
	w.ops, w.errs, w.misses = 0, 0, 0
	w.hist.Reset()
	for i := range w.per {
		p := &w.per[i]
		p.ops, p.errs, p.misses = 0, 0, 0
		if p.hist != nil {
			p.hist.Reset()
		}
	}
	w.cls = [NumErrClasses]uint64{}
	w.nodeOps, w.nodeErrs, w.nodeMax = [MaxNodes]uint64{}, [MaxNodes]uint64{}, 0
}

// Recorder is one worker's handle. Use one per goroutine.
type Recorder struct {
	mu    sync.Mutex
	cur   *window
	spare *window
}

// Record adds the outcome of one batch. Each successful operation is charged
// the latency d (for a pipelined batch, the batch round trip).
func (r *Recorder) Record(obs []Obs, d time.Duration) {
	r.mu.Lock()
	w := r.cur
	for _, o := range obs {
		p := &w.per[o.Op]
		w.ops++
		p.ops++
		if n := o.Node - 1; n >= 0 && n < MaxNodes {
			w.nodeOps[n]++
			if o.Class != ErrNone {
				w.nodeErrs[n]++
			}
			if n+1 > w.nodeMax {
				w.nodeMax = n + 1
			}
		}
		if o.Class != ErrNone {
			w.errs++
			p.errs++
			w.cls[o.Class]++
			continue
		}
		if o.Miss {
			w.misses++
			p.misses++
		}
		w.hist.Record(d)
		if p.hist == nil {
			p.hist = &latency.Histogram{}
		}
		p.hist.Record(d)
	}
	r.mu.Unlock()
}

// OpTotals are exact run totals for one mix entry.
type OpTotals struct {
	Name                string
	Ops, Errors, Misses uint64
	P50, P95, P99, Max  time.Duration
}

// Collector aggregates all workers.
type Collector struct {
	names []string
	start time.Time
	recs  []*Recorder

	mu                      sync.Mutex // guards everything below
	last                    time.Time
	series                  *Series
	cum                     latency.Histogram
	cumOps                  uint64
	cumErrs                 uint64
	cumMiss                 uint64
	cumCls                  [NumErrClasses]uint64
	cumPer                  []opCum
	cumNodeOps, cumNodeErrs [MaxNodes]uint64
	cumNodeMax              int
	firstErr                string
	activeFn                func() int // reports the active worker count (load profiles)
	delMu                   sync.Mutex // pub/sub delivery latency, recorded by subscriber goroutines
	delWin                  latency.Histogram
	delCum                  latency.Histogram
	dropped                 atomic.Uint64
	lastDrop                uint64
}

type opCum struct {
	ops, errs, misses uint64
	hist              latency.Histogram
}

// MaxSeriesPoints bounds the stored time series.
const MaxSeriesPoints = 2048

// NewCollector creates a collector for the given operation names and number of
// workers.
func NewCollector(opNames []string, workers int, start time.Time) *Collector {
	c := &Collector{
		names: opNames, start: start, last: start,
		series: NewSeries(MaxSeriesPoints),
		cumPer: make([]opCum, len(opNames)),
	}
	c.recs = make([]*Recorder, workers)
	for i := range c.recs {
		c.recs[i] = &Recorder{cur: newWindow(len(opNames)), spare: newWindow(len(opNames))}
	}
	return c
}

// Recorder returns the handle for worker i.
func (c *Collector) Recorder(i int) *Recorder { return c.recs[i] }

// SetActiveWorkers tells the collector how to read the active worker count.
func (c *Collector) SetActiveWorkers(fn func() int) {
	c.mu.Lock()
	c.activeFn = fn
	c.mu.Unlock()
}

// RecordDelivery records the delivery latency of one pub/sub message. It is
// safe to call from any goroutine.
func (c *Collector) RecordDelivery(d time.Duration) {
	c.delMu.Lock()
	c.delWin.Record(d)
	c.delMu.Unlock()
}

// NoteDropped counts operations an open loop could not send (the queue was
// full, or the run ended with work still queued).
func (c *Collector) NoteDropped(n uint64) { c.dropped.Add(n) }

// NoteError remembers the first error text seen (for the run report).
func (c *Collector) NoteError(s string) {
	c.mu.Lock()
	if c.firstErr == "" {
		c.firstErr = s
	}
	c.mu.Unlock()
}

// Snapshot closes the current time slice at now: it swaps out every worker's
// window, folds it into the run totals, and appends a Sample to the series.
// Call it from a single goroutine (the sampler), and once more at the end.
func (c *Collector) Snapshot(now time.Time) Sample {
	return c.snapshot(now, 0)
}

// Finish closes the last slice at the end of the run. A slice shorter than
// minSpan (the run ended just after a tick) would give a noisy rate, so it is
// merged into the previous point instead of becoming a chart point of its own;
// the run totals include it either way.
func (c *Collector) Finish(now time.Time, minSpan time.Duration) Sample {
	return c.snapshot(now, minSpan)
}

func (c *Collector) snapshot(now time.Time, minSpan time.Duration) Sample {
	c.mu.Lock()
	defer c.mu.Unlock()

	slice := newWindow(len(c.names)) // merged view of this slice
	merged := make([]*latency.Histogram, len(c.names))
	for _, r := range c.recs {
		r.mu.Lock()
		w := r.cur
		r.cur, r.spare = r.spare, w
		r.mu.Unlock()

		slice.ops += w.ops
		slice.errs += w.errs
		slice.misses += w.misses
		slice.hist.Merge(&w.hist)
		for i := range w.cls {
			slice.cls[i] += w.cls[i]
		}
		for i := 0; i < w.nodeMax; i++ {
			slice.nodeOps[i] += w.nodeOps[i]
			slice.nodeErrs[i] += w.nodeErrs[i]
		}
		slice.nodeMax = max(slice.nodeMax, w.nodeMax)
		for i := range w.per {
			p := &w.per[i]
			sp := &slice.per[i]
			sp.ops += p.ops
			sp.errs += p.errs
			sp.misses += p.misses
			if p.hist != nil && p.hist.Count() > 0 {
				if merged[i] == nil {
					merged[i] = &latency.Histogram{}
				}
				merged[i].Merge(p.hist)
			}
		}
		w.reset() // the retired window becomes the next spare
	}

	smp := Sample{
		Offset: now.Sub(c.start), Span: now.Sub(c.last),
		Ops: slice.ops, Errors: slice.errs, Misses: slice.misses,
		P50: slice.hist.Quantile(0.5), P95: slice.hist.Quantile(0.95),
		P99: slice.hist.Quantile(0.99), Max: slice.hist.Max(),
		PerOp: make([]OpSample, len(c.names)),
	}
	if c.activeFn != nil {
		smp.Workers = c.activeFn()
	}
	c.delMu.Lock()
	if n := c.delWin.Count(); n > 0 {
		smp.Delivered = n
		smp.DeliveryP50, smp.DeliveryP99 = c.delWin.Quantile(0.5), c.delWin.Quantile(0.99)
		c.delCum.Merge(&c.delWin)
		c.delWin.Reset()
	}
	c.delMu.Unlock()
	if d := c.dropped.Load(); d > c.lastDrop {
		smp.Dropped = d - c.lastDrop
		c.lastDrop = d
	}
	if slice.nodeMax > 0 {
		smp.Nodes = make([]NodeSample, slice.nodeMax)
		for i := range smp.Nodes {
			smp.Nodes[i] = NodeSample{Ops: slice.nodeOps[i], Errors: slice.nodeErrs[i]}
			c.cumNodeOps[i] += slice.nodeOps[i]
			c.cumNodeErrs[i] += slice.nodeErrs[i]
		}
		c.cumNodeMax = max(c.cumNodeMax, slice.nodeMax)
	}
	c.last = now
	c.cum.Merge(&slice.hist)
	c.cumOps += slice.ops
	c.cumErrs += slice.errs
	c.cumMiss += slice.misses
	for i := range slice.cls {
		c.cumCls[i] += slice.cls[i]
	}
	for i := range c.names {
		sp := slice.per[i]
		cp := &c.cumPer[i]
		cp.ops += sp.ops
		cp.errs += sp.errs
		cp.misses += sp.misses
		smp.PerOp[i] = OpSample{Ops: sp.ops, Errors: sp.errs, Misses: sp.misses}
		if merged[i] != nil {
			cp.hist.Merge(merged[i])
			smp.PerOp[i].P99 = merged[i].Quantile(0.99)
		}
	}
	if minSpan > 0 && smp.Span < minSpan {
		c.series.MergeLast(smp)
	} else {
		c.series.Add(smp)
	}
	return smp
}

// Totals is the final, exact view of the run.
type Totals struct {
	Ops, Errors, Misses uint64
	ErrClasses          map[string]uint64
	FirstError          string
	PerOp               []OpTotals
	Hist                *latency.Histogram
	Series              []Sample
	SeriesFactor        int // raw samples per series point (1 = full resolution)
	Dropped             uint64
	PerNode             []NodeSample       // cluster: whole-run client ops and errors per node, by registry index
	DeliveryHist        *latency.Histogram // pub/sub delivery latency over the whole run
}

// Totals returns the run totals so far. Call Snapshot first to include the
// latest slice.
func (c *Collector) Totals() Totals {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := Totals{
		Ops: c.cumOps, Errors: c.cumErrs, Misses: c.cumMiss, FirstError: c.firstErr,
		ErrClasses: map[string]uint64{}, Hist: &latency.Histogram{},
		Series: c.series.Samples(), SeriesFactor: c.series.Factor(), Dropped: c.dropped.Load(),
	}
	t.Hist.Merge(&c.cum)
	c.delMu.Lock()
	t.DeliveryHist = &latency.Histogram{}
	t.DeliveryHist.Merge(&c.delCum)
	t.DeliveryHist.Merge(&c.delWin) // messages that arrived after the last slice closed
	c.delMu.Unlock()
	for i := 0; i < c.cumNodeMax; i++ {
		t.PerNode = append(t.PerNode, NodeSample{Ops: c.cumNodeOps[i], Errors: c.cumNodeErrs[i]})
	}
	for i, n := range c.cumCls {
		if ErrClass(i) != ErrNone && n > 0 {
			t.ErrClasses[ErrClass(i).String()] = n
		}
	}
	for i, name := range c.names {
		cp := &c.cumPer[i]
		t.PerOp = append(t.PerOp, OpTotals{
			Name: name, Ops: cp.ops, Errors: cp.errs, Misses: cp.misses,
			P50: cp.hist.Quantile(0.5), P95: cp.hist.Quantile(0.95), P99: cp.hist.Quantile(0.99), Max: cp.hist.Max(),
		})
	}
	return t
}
