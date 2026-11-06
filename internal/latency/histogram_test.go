package latency

import (
	"testing"
	"time"
)

func TestQuantilesWithinError(t *testing.T) {
	var h Histogram
	for i := 1; i <= 100000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	for _, c := range []struct {
		q    float64
		want time.Duration
	}{{0.5, 50 * time.Millisecond}, {0.99, 99 * time.Millisecond}, {0.999, 99900 * time.Microsecond}} {
		got := h.Quantile(c.q)
		diff := float64(got-c.want) / float64(c.want)
		if diff < -0.04 || diff > 0.04 {
			t.Errorf("q=%v got %v want ~%v (%.1f%% off)", c.q, got, c.want, diff*100)
		}
	}
	if h.Count() != 100000 {
		t.Fatalf("count %d", h.Count())
	}
	if h.Max() != 100*time.Millisecond {
		t.Fatalf("max %v", h.Max())
	}
}

func TestSmallValuesExact(t *testing.T) {
	var h Histogram
	h.Record(10)
	h.Record(20)
	if got := h.Quantile(0); got != 10 {
		t.Fatalf("got %v want 10ns", got)
	}
}

func TestMerge(t *testing.T) {
	var a, b Histogram
	a.Record(time.Millisecond)
	b.Record(3 * time.Millisecond)
	a.Merge(&b)
	if a.Count() != 2 || a.Max() != 3*time.Millisecond {
		t.Fatalf("count=%d max=%v", a.Count(), a.Max())
	}
}

func TestEmptyAndHuge(t *testing.T) {
	var h Histogram
	if h.Quantile(0.99) != 0 || h.Mean() != 0 {
		t.Fatal("empty histogram must return zero")
	}
	h.Record(1 << 62) // must not panic or overflow the bucket array
	if h.Count() != 1 {
		t.Fatal("record lost")
	}
}
