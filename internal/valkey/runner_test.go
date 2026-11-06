package valkey

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

// fakeClient is an in-memory store that records commands.
type fakeClient struct {
	mu      sync.Mutex
	data    map[string]string
	batches []int
	cmds    map[string]int
	failAll bool
}

func newFake() *fakeClient { return &fakeClient{data: map[string]string{}, cmds: map[string]int{}} }

func (f *fakeClient) Close() {}

type fakeConn struct{ f *fakeClient }

func (c fakeConn) Query(ctx context.Context, cmd Cmd) (any, error) { return c.f.Query(ctx, cmd) }
func (c fakeConn) Exec(ctx context.Context, cmds []Cmd) []Result   { return c.f.Exec(ctx, cmds) }

func (f *fakeClient) Dedicated(_ context.Context, fn func(Conn) error) error { return fn(fakeConn{f}) }
func (f *fakeClient) Subscribe(ctx context.Context, _ bool, _ []string, _ func(string, string), onReady func()) error {
	if onReady != nil {
		onReady()
	}
	<-ctx.Done()
	return nil
}

func (f *fakeClient) Query(context.Context, Cmd) (any, error) { return nil, nil }

func (f *fakeClient) Exec(_ context.Context, cmds []Cmd) []Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, len(cmds))
	out := make([]Result, len(cmds))
	for i, c := range cmds {
		f.cmds[c[0]]++
		if f.failAll {
			out[i] = Result{Err: errors.New("boom")}
			continue
		}
		switch c[0] {
		case "GET":
			if _, ok := f.data[c[1]]; !ok {
				out[i].Miss = true
			}
		case "SET":
			f.data[c[1]] = c[2]
		}
	}
	return out
}

func testCfg() config.Config {
	c := config.Default()
	c.Run.Concurrency = 4
	c.Run.Duration = 100 * time.Millisecond
	c.Dataset.KeyCount = 1000
	c.Dataset.ValueSize = 32
	c.Dataset.KeyPrefix = "t:"
	return c
}

func TestRunMixAndPipeline(t *testing.T) {
	f := newFake()
	cfg := testCfg()
	cfg.Target.Pool.PipelineDepth = 8
	res, err := Run(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range f.batches {
		if b != 8 {
			t.Fatalf("batch of %d, want 8", b)
		}
	}
	if len(res.PerOp) != 2 || res.PerOp[0].Op != "GET" || res.PerOp[1].Op != "SET" {
		t.Fatalf("per-op: %+v", res.PerOp)
	}
	if res.Ops == 0 || res.Ops != res.PerOp[0].Ops+res.PerOp[1].Ops {
		t.Fatalf("ops=%d per-op=%+v", res.Ops, res.PerOp)
	}
	pct := float64(res.PerOp[0].Ops) / float64(res.Ops) * 100
	if pct < 70 || pct > 90 {
		t.Fatalf("GET share %.1f%%, want about 80%%", pct)
	}
	if res.Errors != 0 || res.Hist.Count() != res.Ops {
		t.Fatalf("errors=%d hist=%d ops=%d", res.Errors, res.Hist.Count(), res.Ops)
	}
}

func TestPreloadThenNoMisses(t *testing.T) {
	f := newFake()
	cfg := testCfg()
	if err := Preload(context.Background(), f, cfg); err != nil {
		t.Fatal(err)
	}
	if int64(len(f.data)) != cfg.Dataset.KeyCount {
		t.Fatalf("preloaded %d keys, want %d", len(f.data), cfg.Dataset.KeyCount)
	}
	for k := range f.data {
		if !strings.HasPrefix(k, "t:s:") {
			t.Fatalf("unexpected key %q", k)
		}
	}
	res, err := Run(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Misses != 0 {
		t.Fatalf("misses=%d after preload", res.Misses)
	}
}

func TestPreloadOnlyUsedTypes(t *testing.T) {
	f := newFake()
	cfg := testCfg()
	cfg.Dataset.KeyCount = 10
	cfg.Dataset.CollectionSize = 3
	cfg.Workload.Mix = []config.MixItem{{Op: "HGET", Weight: 1}, {Op: "ZADD", Weight: 1}, {Op: "INCR", Weight: 1}}
	if err := Preload(context.Background(), f, cfg); err != nil {
		t.Fatal(err)
	}
	if f.cmds["HSET"] != 10 || f.cmds["ZADD"] != 10 || len(f.cmds) != 2 {
		t.Fatalf("preload commands: %v", f.cmds)
	}
}

func TestErrorsAreCountedNotRecorded(t *testing.T) {
	f := newFake()
	f.failAll = true
	res, err := Run(context.Background(), f, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors == 0 || res.Errors != res.Ops || res.Hist.Count() != 0 {
		t.Fatalf("errors=%d ops=%d hist=%d", res.Errors, res.Ops, res.Hist.Count())
	}
	if !strings.HasSuffix(res.FirstError, ": boom") {
		t.Fatalf("first error %q", res.FirstError)
	}
	if res.PerOp[0].Errors+res.PerOp[1].Errors != res.Errors {
		t.Fatalf("per-op errors %+v", res.PerOp)
	}
}

func TestCancelStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := testCfg()
	cfg.Run.Duration = time.Minute
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := Run(ctx, newFake(), cfg); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("run did not stop after cancel")
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Concurrency = 0
	if _, err := Run(context.Background(), newFake(), cfg); err == nil {
		t.Fatal("want validation error")
	}
}

func TestSeedReportedAndRandomWhenZero(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Seed = 42
	res, _ := Run(context.Background(), newFake(), cfg)
	if res.Seed != 42 {
		t.Fatalf("seed %d", res.Seed)
	}
	cfg.Run.Seed = 0
	res, _ = Run(context.Background(), newFake(), cfg)
	if res.Seed == 0 {
		t.Fatal("seed 0 must resolve to a real seed")
	}
}

func TestCoresPer100k(t *testing.T) {
	r := &RunResult{Ops: 1_000_000, Elapsed: 10 * time.Second, CPU: 5 * time.Second}
	// 100k ops/s using 0.5 cores => 0.5 cores per 100k ops/s.
	if got := r.CoresPer100k(); got < 0.49 || got > 0.51 {
		t.Fatalf("got %v", got)
	}
	if (&RunResult{}).CoresPer100k() != 0 {
		t.Fatal("zero load must give 0")
	}
}

func TestNewClientErrors(t *testing.T) {
	if _, err := NewClient("nope", Options{Addr: "x:1"}); err == nil {
		t.Fatal("want unknown library error")
	}
	if _, err := NewClient(LibValkeyGo, Options{}); err == nil {
		t.Fatal("want address error")
	}
}

// slowClient takes a fixed time per batch so pacing and latency are predictable.
type slowClient struct {
	fakeClient
	delay time.Duration
}

func (s *slowClient) Exec(ctx context.Context, cmds []Cmd) []Result {
	time.Sleep(s.delay)
	return s.fakeClient.Exec(ctx, cmds)
}

func newSlow(d time.Duration) *slowClient {
	return &slowClient{fakeClient: *newFake(), delay: d}
}

func TestTargetRatePacesTheRun(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Concurrency = 4
	cfg.Run.Duration = time.Second
	cfg.Run.TargetOpsPerSec = 400
	res, err := Run(context.Background(), newSlow(time.Millisecond), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops < 340 || res.Ops > 440 {
		t.Fatalf("%d ops in 1s at a 400 ops/s target", res.Ops)
	}
	if res.TargetOpsPerSec != 400 || res.CoordinatedOmission {
		t.Fatalf("%+v", res)
	}
}

// The core of coordinated omission: when the server stalls, plain service-time
// latency hides the delay of the operations that were due during the stall; the
// intended-start measurement shows it.
func TestCoordinatedOmissionExposesQueueing(t *testing.T) {
	run := func(co bool) *RunResult {
		cfg := testCfg()
		cfg.Run.Concurrency = 1
		cfg.Run.Duration = 600 * time.Millisecond
		cfg.Run.TargetOpsPerSec = 100 // one op every 10ms
		cfg.Run.CoordinatedOmission = co
		// Every call takes 30ms: the single worker falls further behind each time.
		res, err := Run(context.Background(), newSlow(30*time.Millisecond), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	plain, corrected := run(false), run(true)
	if p := plain.Hist.Quantile(0.99); p > 60*time.Millisecond {
		t.Fatalf("service-time p99 %v should be about the 30ms call time", p)
	}
	if p := corrected.Hist.Quantile(0.99); p < 100*time.Millisecond {
		t.Fatalf("CO p99 %v should include the time spent waiting behind schedule", p)
	}
	if !corrected.CoordinatedOmission || plain.CoordinatedOmission {
		t.Fatal("result must say which latency mode was used")
	}
}

func TestCOIgnoredWithoutTarget(t *testing.T) {
	cfg := testCfg()
	cfg.Run.CoordinatedOmission = true
	if _, err := Run(context.Background(), newFake(), cfg); err == nil {
		t.Fatal("coordinated_omission without a target rate must be rejected")
	}
}

func TestRunProducesSeriesAndClasses(t *testing.T) {
	f := newFake()
	f.failAll = true
	cfg := testCfg()
	cfg.Run.Duration = 350 * time.Millisecond
	cfg.Output.SampleInterval = 100 * time.Millisecond
	var got []int
	var mu sync.Mutex
	res, err := RunWith(context.Background(), f, cfg, RunOptions{OnSample: func(s stats.Sample) { mu.Lock(); got = append(got, int(s.Ops)); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	// Every slice is reported to OnSample; a very short last slice is merged
	// into the previous chart point, so the series can have one point fewer.
	if len(res.Series) < 3 || len(got) < len(res.Series) || len(got) > len(res.Series)+1 {
		t.Fatalf("series %d, callbacks %d", len(res.Series), len(got))
	}
	var sum uint64
	for _, s := range res.Series {
		sum += s.Ops
	}
	if sum != res.Ops {
		t.Fatalf("series ops %d != total %d", sum, res.Ops)
	}
	if res.ErrClasses["other"] != res.Errors {
		t.Fatalf("classes %v errors %d", res.ErrClasses, res.Errors)
	}
}

func TestLoadProfileGatesTheActiveWorkers(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Concurrency = 1
	cfg.Run.Duration = 800 * time.Millisecond
	cfg.Output.SampleInterval = 100 * time.Millisecond
	cfg.Run.LoadProfile = loadprofile.Config{Kind: "step", Steps: []loadprofile.Stage{{Workers: 1, Duration: "400ms"}, {Workers: 4, Duration: "400ms"}}}
	res, err := Run(context.Background(), newSlow(5*time.Millisecond), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.PeakWorkers != 4 || !strings.HasPrefix(res.LoadProfile, "step") {
		t.Fatalf("peak %d profile %q", res.PeakWorkers, res.LoadProfile)
	}
	var early, late uint64
	seen := map[int]bool{}
	for _, s := range res.Series {
		seen[s.Workers] = true
		if s.Offset <= 350*time.Millisecond {
			early += s.Ops
		} else if s.Offset > 500*time.Millisecond {
			late += s.Ops
		}
	}
	if !seen[1] || !seen[4] {
		t.Fatalf("series should record both worker levels, saw %v", seen)
	}
	if early == 0 || float64(late) < 2.5*float64(early)*(300.0/350.0) {
		t.Fatalf("4 workers should do about 4x the work of 1 (early %d, late %d)", early, late)
	}
}

func TestRampProfileGrowsConcurrency(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Duration = 600 * time.Millisecond
	cfg.Output.SampleInterval = 100 * time.Millisecond
	cfg.Run.LoadProfile = loadprofile.Config{Kind: "ramp", StartWorkers: 1, EndWorkers: 6, RampOver: "500ms"}
	res, err := Run(context.Background(), newSlow(2*time.Millisecond), cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, last := res.Series[0].Workers, res.Series[len(res.Series)-1].Workers
	if first >= last || last < 5 {
		t.Fatalf("workers should grow from %d to near 6, ended at %d", first, last)
	}
}

func TestOpenLoopUnderloadHoldsRateWithServiceLatency(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Loop = "open"
	cfg.Run.Concurrency = 4
	cfg.Run.TargetOpsPerSec = 200
	cfg.Run.Duration = time.Second
	res, err := Run(context.Background(), newSlow(5*time.Millisecond), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops < 170 || res.Ops > 215 || res.Dropped != 0 {
		t.Fatalf("ops %d dropped %d at 200 ops/s for 1s", res.Ops, res.Dropped)
	}
	if p := res.Hist.Quantile(0.99); p > 30*time.Millisecond {
		t.Fatalf("p99 %v: an unloaded open loop sees about the service time", p)
	}
	if res.Loop != "open" || !res.CoordinatedOmission {
		t.Fatalf("an open loop always measures from the arrival: %+v", res)
	}
}

// The point of an open loop: when the server cannot keep up, work keeps
// arriving, waits in line, and that waiting shows up in the latency.
func TestOpenLoopOverloadShowsQueueingAndDrops(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Loop = "open"
	cfg.Run.Concurrency = 1
	cfg.Run.TargetOpsPerSec = 400 // the single 20ms worker can do about 50/s
	cfg.Run.Duration = time.Second
	res, err := Run(context.Background(), newSlow(20*time.Millisecond), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops > 80 {
		t.Fatalf("a 20ms worker cannot serve %d ops in 1s", res.Ops)
	}
	if p := res.Hist.Quantile(0.99); p < 300*time.Millisecond {
		t.Fatalf("p99 %v should include the time spent queued", p)
	}
	if res.Dropped < 200 {
		t.Fatalf("most arrivals never got served and must be counted as dropped, got %d", res.Dropped)
	}
	if total := res.Ops + res.Dropped; total < 360 || total > 440 {
		t.Fatalf("served + dropped %d should be about the 400 offered", total)
	}
}

func TestOpenLoopStopsPromptlyOnCancel(t *testing.T) {
	cfg := testCfg()
	cfg.Run.Loop = "open"
	cfg.Run.TargetOpsPerSec = 1000
	cfg.Run.Duration = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(80 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := Run(ctx, newSlow(time.Millisecond), cfg); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("open loop did not stop")
	}
}

func TestClosedLoopFixedProfileUnchanged(t *testing.T) {
	cfg := testCfg() // default: closed, fixed
	res, err := Run(context.Background(), newFake(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Loop != "closed" || res.PeakWorkers != cfg.Run.Concurrency || res.Dropped != 0 || res.CoordinatedOmission {
		t.Fatalf("%+v", res)
	}
	if !strings.HasPrefix(res.LoadProfile, "fixed") {
		t.Fatalf("profile %q", res.LoadProfile)
	}
}

type hashRouter struct{ nodes int }

func (h hashRouter) NodeIndex(key string) int {
	if key == "" {
		return -1
	}
	return Slot(key) % h.nodes
}

func TestRouterSplitsClientOpsPerNode(t *testing.T) {
	cfg := testCfg()
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 3}, {Op: "SET", Weight: 1}, {Op: "SCAN", Weight: 1}}
	cfg.Run.Duration = 300 * time.Millisecond
	res, err := RunWith(context.Background(), newFake(), cfg, RunOptions{Router: hashRouter{nodes: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PerNode) != 3 {
		t.Fatalf("per node %+v", res.PerNode)
	}
	var attributed, scans uint64
	for _, n := range res.PerNode {
		attributed += n.Ops
		if n.Ops == 0 {
			t.Fatalf("every node should get some of a uniform key space: %+v", res.PerNode)
		}
	}
	for _, o := range res.PerOp {
		if o.Op == "SCAN" {
			scans = o.Ops
		}
	}
	if attributed+scans != res.Ops {
		t.Fatalf("attributed %d + SCAN %d != %d ops (SCAN has no key and is not attributed)", attributed, scans, res.Ops)
	}
	// Without a router nothing is attributed.
	res, _ = Run(context.Background(), newFake(), cfg)
	if len(res.PerNode) != 0 {
		t.Fatalf("%+v", res.PerNode)
	}
}
