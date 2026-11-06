package stats

import "time"

// OpSample is one operation's share of a Sample.
type OpSample struct {
	Ops, Errors, Misses uint64
	P99                 time.Duration
}

// NodeSample is one cluster node's share of a Sample (client side).
type NodeSample struct{ Ops, Errors uint64 }

// Sample is one time slice of the run (a chart point).
type Sample struct {
	Offset              time.Duration // end of the slice, since run start
	Span                time.Duration // length of the slice
	Ops, Errors, Misses uint64
	P50, P95, P99, Max  time.Duration
	Workers             int          // active workers at the end of the slice
	Dropped             uint64       // open loop: batches' operations that could not be sent in this slice
	PerOp               []OpSample   // indexed like the mix
	Nodes               []NodeSample // cluster only: indexed like the node registry
	// Pub/sub: messages subscribers received in this slice, and their delivery
	// latency (publish to receipt).
	Delivered                uint64
	DeliveryP50, DeliveryP99 time.Duration
}

// OpsPerSec is the slice's throughput.
func (s Sample) OpsPerSec() float64 {
	if s.Span <= 0 {
		return 0
	}
	return float64(s.Ops) / s.Span.Seconds()
}

// merge combines two adjacent slices. Counts and spans add. Percentiles cannot
// be merged exactly without the histograms, so p50 is the ops-weighted mean and
// p95, p99 and max are the larger of the two: a downsampled chart never hides a
// spike. The run-level percentiles stay exact (they come from the full histogram).
func merge(a, b Sample) Sample {
	out := Sample{
		Offset: b.Offset, Span: a.Span + b.Span,
		Ops: a.Ops + b.Ops, Errors: a.Errors + b.Errors, Misses: a.Misses + b.Misses,
		P95: maxDur(a.P95, b.P95), P99: maxDur(a.P99, b.P99), Max: maxDur(a.Max, b.Max),
		Workers: max(a.Workers, b.Workers), Dropped: a.Dropped + b.Dropped,
		Delivered: a.Delivered + b.Delivered, DeliveryP99: maxDur(a.DeliveryP99, b.DeliveryP99),
	}
	if t := a.Delivered + b.Delivered; t > 0 {
		out.DeliveryP50 = time.Duration((float64(a.DeliveryP50)*float64(a.Delivered) + float64(b.DeliveryP50)*float64(b.Delivered)) / float64(t))
	}
	if t := a.Ops + b.Ops; t > 0 {
		out.P50 = time.Duration((float64(a.P50)*float64(a.Ops) + float64(b.P50)*float64(b.Ops)) / float64(t))
	}
	out.Nodes = mergeNodes(a.Nodes, b.Nodes)
	n := max(len(a.PerOp), len(b.PerOp))
	out.PerOp = make([]OpSample, n)
	for i := 0; i < n; i++ {
		var x, y OpSample
		if i < len(a.PerOp) {
			x = a.PerOp[i]
		}
		if i < len(b.PerOp) {
			y = b.PerOp[i]
		}
		out.PerOp[i] = OpSample{Ops: x.Ops + y.Ops, Errors: x.Errors + y.Errors, Misses: x.Misses + y.Misses, P99: maxDur(x.P99, y.P99)}
	}
	return out
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// Series stores samples in bounded memory. When it is full, neighboring
// samples are merged pairwise and from then on each stored point covers twice
// as many raw samples, so a 24 hour run and a 1 minute run use the same space.
type Series struct {
	max     int
	factor  int // raw samples per stored point
	samples []Sample
	pending Sample
	nPend   int
}

// NewSeries creates a series holding at most max points (min 2).
func NewSeries(max int) *Series {
	if max%2 == 1 {
		max++
	}
	if max < 2 {
		max = 2
	}
	return &Series{max: max, factor: 1, samples: make([]Sample, 0, max)}
}

// Add appends one raw sample.
func (s *Series) Add(x Sample) {
	if s.nPend == 0 {
		s.pending = x
	} else {
		s.pending = merge(s.pending, x)
	}
	s.nPend++
	if s.nPend < s.factor {
		return
	}
	s.samples = append(s.samples, s.pending)
	s.nPend = 0
	if len(s.samples) >= s.max {
		s.halve()
	}
}

func (s *Series) halve() {
	n := len(s.samples) / 2
	for i := 0; i < n; i++ {
		s.samples[i] = merge(s.samples[2*i], s.samples[2*i+1])
	}
	if len(s.samples)%2 == 1 {
		s.samples[n] = s.samples[len(s.samples)-1]
		n++
	}
	for i := n; i < len(s.samples); i++ {
		s.samples[i] = Sample{} // release PerOp slices
	}
	s.samples = s.samples[:n]
	s.factor *= 2
}

// Samples returns the stored points, including a partial last point.
func (s *Series) Samples() []Sample {
	out := make([]Sample, len(s.samples), len(s.samples)+1)
	copy(out, s.samples)
	if s.nPend > 0 {
		out = append(out, s.pending)
	}
	return out
}

// Factor is how many raw samples each stored point covers.
func (s *Series) Factor() int { return s.factor }

// MergeLast folds x into the most recent point instead of adding a new one.
// With no points yet it adds x.
func (s *Series) MergeLast(x Sample) {
	switch {
	case s.nPend > 0:
		s.pending = merge(s.pending, x)
	case len(s.samples) > 0:
		last := len(s.samples) - 1
		s.samples[last] = merge(s.samples[last], x)
	default:
		s.Add(x)
	}
}

func mergeNodes(a, b []NodeSample) []NodeSample {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]NodeSample, max(len(a), len(b)))
	for i := range out {
		if i < len(a) {
			out[i].Ops, out[i].Errors = a[i].Ops, a[i].Errors
		}
		if i < len(b) {
			out[i].Ops += b[i].Ops
			out[i].Errors += b[i].Errors
		}
	}
	return out
}
