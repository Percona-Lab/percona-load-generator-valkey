// Package latency provides a small log-linear latency histogram (about 3%
// relative error). It is a spike implementation; phase 3 replaces it with the
// full stats collector.
package latency

import (
	"math/bits"
	"time"
)

const (
	subBits  = 5
	subCount = 1 << subBits // 32 sub-buckets per power of two
	// Values up to about 2^37 ns (137 s) get their own buckets; larger values
	// land in the last bucket (Max still reports the true maximum).
	maxIdx = 33*subCount + subCount - 1
)

// Histogram records durations in nanoseconds. Not safe for concurrent use:
// keep one per worker and Merge afterwards.
type Histogram struct {
	counts [maxIdx + 1]uint64
	n      uint64
	sum    uint64
	max    uint64
}

func index(v uint64) int {
	if v < 2*subCount {
		return int(v)
	}
	s := bits.Len64(v) - (subBits + 1)
	i := s*subCount + int(v>>uint(s))
	if i > maxIdx {
		return maxIdx
	}
	return i
}

// upper returns the largest value mapped to bucket i.
func upper(i int) uint64 {
	if i < 2*subCount {
		return uint64(i)
	}
	s := uint(i/subCount - 1)
	m := uint64(i%subCount + subCount)
	return ((m + 1) << s) - 1
}

// Reset clears all observations.
func (h *Histogram) Reset() { *h = Histogram{} }

// Record adds one observation.
func (h *Histogram) Record(d time.Duration) {
	v := uint64(0)
	if d > 0 {
		v = uint64(d)
	}
	h.counts[index(v)]++
	h.n++
	h.sum += v
	if v > h.max {
		h.max = v
	}
}

// Merge adds the observations of o into h.
func (h *Histogram) Merge(o *Histogram) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	h.sum += o.sum
	if o.max > h.max {
		h.max = o.max
	}
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 { return h.n }

// Max returns the largest observation.
func (h *Histogram) Max() time.Duration { return time.Duration(h.max) }

// Mean returns the average observation.
func (h *Histogram) Mean() time.Duration {
	if h.n == 0 {
		return 0
	}
	return time.Duration(h.sum / h.n)
}

// Quantile returns the value at quantile q in [0,1]. It never exceeds Max.
func (h *Histogram) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	target := uint64(q * float64(h.n))
	if target >= h.n {
		target = h.n - 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen > target {
			v := upper(i)
			if v > h.max {
				v = h.max
			}
			return time.Duration(v)
		}
	}
	return time.Duration(h.max)
}
