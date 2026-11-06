package stats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := map[ErrClass][]error{
		ErrNone:        {nil},
		ErrTimeout:     {context.DeadlineExceeded, timeoutErr{}, errors.New("read tcp: i/o timeout")},
		ErrConnection:  {io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, &net.OpError{Op: "dial"}, errors.New("read: connection reset by peer"), errors.New("write: broken pipe")},
		ErrMoved:       {errors.New("MOVED 3999 127.0.0.1:6381")},
		ErrAsk:         {errors.New("ASK 3999 127.0.0.1:6381")},
		ErrLoading:     {errors.New("LOADING Valkey is loading the dataset in memory")},
		ErrOOM:         {errors.New("OOM command not allowed when used memory > 'maxmemory'")},
		ErrReadOnly:    {errors.New("READONLY You can't write against a read only replica.")},
		ErrNoAuth:      {errors.New("NOAUTH Authentication required."), errors.New("WRONGPASS invalid username-password pair")},
		ErrNoPerm:      {errors.New("NOPERM this user has no permissions to run the 'get' command")},
		ErrBusy:        {errors.New("BUSY Valkey is busy running a script.")},
		ErrClusterDown: {errors.New("CLUSTERDOWN The cluster is down")},
		ErrTryAgain:    {errors.New("TRYAGAIN Multiple keys request during rehashing of slot")},
		ErrWrongType:   {errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")},
		ErrOther:       {errors.New("ERR unknown command 'x'"), errors.New("something odd")},
	}
	for want, errs := range cases {
		for _, e := range errs {
			if got := Classify(e); got != want {
				t.Errorf("%v: got %s want %s", e, got, want)
			}
		}
	}
	if ErrOOM.String() != "OOM" || ErrClass(200).String() != "other" {
		t.Error("String()")
	}
}

func TestSeriesBoundedAndExact(t *testing.T) {
	s := NewSeries(8)
	var total uint64
	for i := 1; i <= 1000; i++ {
		total += uint64(i)
		s.Add(Sample{Offset: time.Duration(i) * time.Second, Span: time.Second, Ops: uint64(i),
			P99: time.Duration(i) * time.Millisecond, PerOp: []OpSample{{Ops: uint64(i)}}})
	}
	pts := s.Samples()
	if len(pts) > 9 {
		t.Fatalf("%d points stored, want <= 9", len(pts))
	}
	var sum uint64
	var span time.Duration
	for _, p := range pts {
		sum += p.Ops
		span += p.Span
	}
	if sum != total || span != 1000*time.Second {
		t.Fatalf("downsampling lost data: ops %d/%d span %v", sum, total, span)
	}
	if got := pts[len(pts)-1].P99; got != time.Second {
		t.Fatalf("last p99 %v: spikes must survive merging", got)
	}
	if s.Factor() < 64 {
		t.Fatalf("factor %d", s.Factor())
	}
}

func TestSeriesKeepsSpikes(t *testing.T) {
	s := NewSeries(4)
	for i := 0; i < 100; i++ {
		p := 2 * time.Millisecond
		if i == 37 {
			p = 900 * time.Millisecond
		}
		s.Add(Sample{Span: time.Second, Ops: 10, P99: p, Max: p})
	}
	var worst time.Duration
	for _, x := range s.Samples() {
		worst = maxDur(worst, x.P99)
	}
	if worst != 900*time.Millisecond {
		t.Fatalf("spike hidden by downsampling: worst p99 %v", worst)
	}
}

// 24 hours of one-second slices through the real collector: memory must not
// grow with run length (time is simulated; no sleeping).
func TestCollectorMemoryFlatOver24Hours(t *testing.T) {
	names := []string{"GET", "SET", "HSET", "ZADD"}
	start := time.Unix(0, 0)
	c := NewCollector(names, 2, start)
	obs := []Obs{{Op: 0}, {Op: 1}, {Op: 2, Miss: true}, {Op: 3, Class: ErrTimeout}}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	var at12h uint64
	for sec := 1; sec <= 24*3600; sec++ {
		for w := 0; w < 2; w++ {
			c.Recorder(w).Record(obs, time.Duration(100+sec%50)*time.Microsecond)
		}
		c.Snapshot(start.Add(time.Duration(sec) * time.Second))
		if sec == 12*3600 {
			at12h = heap()
		}
	}
	end := heap()
	tot := c.Totals()
	if len(tot.Series) > MaxSeriesPoints+1 {
		t.Fatalf("series holds %d points", len(tot.Series))
	}
	if tot.Ops != 24*3600*2*4 {
		t.Fatalf("ops %d", tot.Ops)
	}
	if end > at12h+2<<20 {
		t.Fatalf("heap grew from %d to %d bytes between hour 12 and 24", at12h, end)
	}
	t.Logf("series points %d (factor %d), heap %d KiB at 12h, %d KiB at 24h", len(tot.Series), tot.SeriesFactor, at12h>>10, end>>10)
}

func TestCollectorCountsAndPercentiles(t *testing.T) {
	start := time.Unix(0, 0)
	c := NewCollector([]string{"GET", "SET"}, 4, start)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := c.Recorder(w)
			for i := 0; i < 1000; i++ {
				r.Record([]Obs{{Op: 0}, {Op: 1, Miss: i%10 == 0}, {Op: 1, Class: ErrOOM}}, time.Duration(i+1)*time.Microsecond)
			}
		}(w)
	}
	// Snapshots race with recording on purpose (run with -race).
	for i := 1; i <= 5; i++ {
		c.Snapshot(start.Add(time.Duration(i) * 10 * time.Millisecond))
	}
	wg.Wait()
	c.Snapshot(start.Add(time.Second))
	tot := c.Totals()
	if tot.Ops != 12000 || tot.Errors != 4000 || tot.Misses != 400 {
		t.Fatalf("totals %+v", tot)
	}
	if tot.ErrClasses["OOM"] != 4000 || len(tot.ErrClasses) != 1 {
		t.Fatalf("classes %v", tot.ErrClasses)
	}
	if tot.PerOp[0].Ops != 4000 || tot.PerOp[1].Ops != 8000 || tot.PerOp[1].Errors != 4000 {
		t.Fatalf("per op %+v", tot.PerOp)
	}
	if tot.Hist.Count() != 8000 { // errors are not latency samples
		t.Fatalf("hist count %d", tot.Hist.Count())
	}
	if p := tot.PerOp[0].P50; p < 450*time.Microsecond || p > 550*time.Microsecond {
		t.Fatalf("GET p50 %v", p)
	}
	var seriesOps uint64
	for _, s := range tot.Series {
		seriesOps += s.Ops
	}
	if seriesOps != tot.Ops {
		t.Fatalf("series ops %d != %d", seriesOps, tot.Ops)
	}
}

func TestFirstErrorKept(t *testing.T) {
	c := NewCollector([]string{"GET"}, 1, time.Now())
	c.NoteError("GET: first")
	c.NoteError("GET: second")
	if got := c.Totals().FirstError; got != "GET: first" {
		t.Fatal(got)
	}
}

func TestSampleRate(t *testing.T) {
	if (Sample{Ops: 500, Span: 2 * time.Second}).OpsPerSec() != 250 || (Sample{}).OpsPerSec() != 0 {
		t.Fatal("OpsPerSec")
	}
	_ = fmt.Sprint(ErrNone)
}

func TestFinishMergesShortLastSlice(t *testing.T) {
	start := time.Unix(0, 0)
	c := NewCollector([]string{"GET"}, 1, start)
	rec := c.Recorder(0)
	for i := 0; i < 100; i++ {
		rec.Record([]Obs{{Op: 0}}, time.Millisecond)
	}
	c.Snapshot(start.Add(time.Second)) // one full slice: 100 ops in 1 s
	for i := 0; i < 5; i++ {
		rec.Record([]Obs{{Op: 0}}, 9*time.Millisecond)
	}
	c.Finish(start.Add(time.Second+10*time.Millisecond), 500*time.Millisecond) // 5 ops in 10 ms: a 500 ops/s blip
	tot := c.Totals()
	if tot.Ops != 105 {
		t.Fatalf("totals must still count the last ops: %d", tot.Ops)
	}
	if len(tot.Series) != 1 {
		t.Fatalf("a %v slice must not become its own point: %d points", 10*time.Millisecond, len(tot.Series))
	}
	p := tot.Series[0]
	if p.Ops != 105 || p.Span != time.Second+10*time.Millisecond {
		t.Fatalf("merged point %+v", p)
	}
	if r := p.OpsPerSec(); r < 100 || r > 105 {
		t.Fatalf("rate %v should stay near 104 ops/s, not spike", r)
	}
	// A long enough last slice stays separate.
	c2 := NewCollector([]string{"GET"}, 1, start)
	c2.Recorder(0).Record([]Obs{{Op: 0}}, time.Millisecond)
	c2.Snapshot(start.Add(time.Second))
	c2.Recorder(0).Record([]Obs{{Op: 0}}, time.Millisecond)
	c2.Finish(start.Add(1800*time.Millisecond), 500*time.Millisecond)
	if n := len(c2.Totals().Series); n != 2 {
		t.Fatalf("%d points", n)
	}
	// Nothing to merge into: becomes the first point.
	c3 := NewCollector([]string{"GET"}, 1, start)
	c3.Finish(start.Add(10*time.Millisecond), time.Second)
	if n := len(c3.Totals().Series); n != 1 {
		t.Fatalf("%d points", n)
	}
}

func TestPerNodeCountsAndSeriesMerging(t *testing.T) {
	start := time.Unix(0, 0)
	c := NewCollector([]string{"GET"}, 2, start)
	r0, r1 := c.Recorder(0), c.Recorder(1)
	r0.Record([]Obs{{Op: 0, Node: 1}, {Op: 0, Node: 1}, {Op: 0, Node: 2, Class: ErrTimeout}, {Op: 0}}, time.Millisecond) // the last has no node
	r1.Record([]Obs{{Op: 0, Node: 3}, {Op: 0, Node: 2}}, time.Millisecond)
	s := c.Snapshot(start.Add(time.Second))
	if len(s.Nodes) != 3 || s.Nodes[0].Ops != 2 || s.Nodes[1].Ops != 2 || s.Nodes[1].Errors != 1 || s.Nodes[2].Ops != 1 {
		t.Fatalf("slice nodes %+v", s.Nodes)
	}
	r0.Record([]Obs{{Op: 0, Node: 1}}, time.Millisecond)
	c.Finish(start.Add(2*time.Second), 0)
	tot := c.Totals()
	if len(tot.PerNode) != 3 || tot.PerNode[0].Ops != 3 || tot.PerNode[1].Ops != 2 || tot.PerNode[1].Errors != 1 {
		t.Fatalf("totals %+v", tot.PerNode)
	}
	if tot.Ops != 7 {
		t.Fatalf("an unattributed operation still counts: %d", tot.Ops)
	}
	// Merging two slices adds the per-node counts.
	m := merge(Sample{Nodes: []NodeSample{{1, 0}}}, Sample{Nodes: []NodeSample{{2, 1}, {5, 0}}})
	if len(m.Nodes) != 2 || m.Nodes[0] != (NodeSample{3, 1}) || m.Nodes[1].Ops != 5 {
		t.Fatalf("%+v", m.Nodes)
	}
	if merge(Sample{}, Sample{}).Nodes != nil {
		t.Fatal("no nodes stays nil")
	}
}

func TestNodeNumberOutOfRangeIsIgnored(t *testing.T) {
	c := NewCollector([]string{"GET"}, 1, time.Unix(0, 0))
	c.Recorder(0).Record([]Obs{{Op: 0, Node: MaxNodes + 5}, {Op: 0, Node: -3}}, time.Millisecond)
	s := c.Snapshot(time.Unix(1, 0))
	if len(s.Nodes) != 0 || s.Ops != 2 {
		t.Fatalf("%+v", s)
	}
}
