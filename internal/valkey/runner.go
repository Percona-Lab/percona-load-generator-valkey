package valkey

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyspace"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// OpStat is one mix entry's totals.
type OpStat struct {
	Op                  string
	Ops, Errors, Misses uint64
	P50, P95, P99, Max  time.Duration
}

// RunResult summarizes a run. Latency is per command: in a pipelined batch
// every command is charged the batch round-trip time. With coordinated
// omission on, it is measured from the intended start of the batch.
type RunResult struct {
	Ops, Errors, Misses uint64
	ErrClasses          map[string]uint64
	FirstError          string
	PerOp               []OpStat // one entry per mix entry, in config order
	Seed                int64    // the seed actually used
	Elapsed             time.Duration
	CPU                 time.Duration // user+system CPU of this process during the run
	Hist                *latency.Histogram
	Series              []stats.Sample
	SeriesFactor        int  // raw samples per series point
	CoordinatedOmission bool // latency measured from intended start
	TargetOpsPerSec     float64
	Loop                string // closed or open
	LoadProfile         string // short description, for example "fixed 64" or "ramp 1..64 over 30s"
	PeakWorkers         int
	Mode                string             // standalone, cluster or sentinel; filled in by the session
	PubSub              *PubSubResult      // pub/sub delivery, when subscribers were used
	DeliveryHist        *latency.Histogram // delivery latency of the whole run
	NodeAddrs           []string           // node registry, filled in by the session
	PerNode             []stats.NodeSample // cluster: client ops and errors per node, indexed like the node registry
	Dropped             uint64             // open loop: operations never sent (queue full, or still queued at the end)
	// TrafficHash fingerprints the commands that were sent (deterministic mode):
	// the same seed, op budget and workload always give the same hash. It is
	// computed like a recording's commands hash. TrafficOps counts the commands.
	TrafficHash string
	TrafficOps  uint64
}

// OpsPerSec returns the achieved throughput.
func (r *RunResult) OpsPerSec() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Ops) / r.Elapsed.Seconds()
}

// CoresPer100k returns CPU cores this process used per 100k ops/s, the perf
// budget metric. Zero when there was no load.
func (r *RunResult) CoresPer100k() float64 {
	ops := r.OpsPerSec()
	if ops <= 0 || r.Elapsed <= 0 {
		return 0
	}
	return r.CPU.Seconds() / r.Elapsed.Seconds() / (ops / 100000)
}

// EffectiveSeed resolves run.seed: 0 means pick a random seed.
func EffectiveSeed(seed int64) int64 {
	if seed != 0 {
		return seed
	}
	return time.Now().UnixNano()&0x7fffffff | 1
}

// RunOptions are optional hooks for RunWith.
type RunOptions struct {
	// OnSample is called from the sampler goroutine with each time slice.
	OnSample func(stats.Sample)
	// Router attributes each command to the cluster node that serves its key,
	// for per-node client statistics. Nil outside a cluster.
	Router Router
	// Tap, when set, sees every command the workers send (the recorder uses
	// it). Nil leaves the hot path untouched.
	Tap Tap
	// CaptureReplies is true when the client digests replies and the Tap wants
	// them (recording with result checksums).
	CaptureReplies bool
	// OnStart is called with the time the measured part begins.
	OnStart func(time.Time)
}

// Run executes the workload until run.duration elapses or ctx is canceled.
func Run(ctx context.Context, c Client, cfg config.Config) (*RunResult, error) {
	return RunWith(ctx, c, cfg, RunOptions{})
}

// RunWith is Run with hooks. cfg must be validated and normalized. With the
// same non-zero seed every worker generates the same sequence of operations.
func RunWith(ctx context.Context, c Client, cfg config.Config, opt RunOptions) (*RunResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	seed := EffectiveSeed(cfg.Run.Seed)
	wl := Compile(cfg.Workload.Mix)
	src, err := values.NewSource(cfg.Dataset.ValueSize, seed)
	if err != nil {
		return nil, err
	}
	sched, err := loadprofile.Compile(cfg.Run.LoadProfile, cfg.Run.Concurrency)
	if err != nil {
		return nil, err
	}
	workers := sched.MaxWorkers() // provisioned; the profile decides how many are active
	gens := make([]*Gen, workers)
	rngs := make([]*rand.Rand, workers)
	for w := range gens {
		kg, err := keyspace.New(cfg.Dataset.KeyDistribution, cfg.Dataset.KeyCount, seed+int64(w)+1, cfg.Dataset.ZipfS)
		if err != nil {
			return nil, err
		}
		rngs[w] = rand.New(rand.NewSource(seed + int64(w)*7919))
		gens[w] = NewGen(rngs[w], kg, src, cfg.Dataset)
		gens[w].Worker, gens[w].Channels = w, cfg.Workload.PubSub.Channels
	}
	names := make([]string, wl.Len())
	for i := range names {
		names[i] = DisplayName(wl.Item(i))
	}

	depth := cfg.Target.Pool.PipelineDepth
	openLoop := cfg.Run.Loop == "open"
	// Closed loop with a rate: each worker sends a batch every `interval`, staggered.
	var interval time.Duration
	if !openLoop && cfg.Run.TargetOpsPerSec > 0 {
		interval = time.Duration(float64(time.Second) * float64(workers*depth) / cfg.Run.TargetOpsPerSec)
	}
	co := (cfg.Run.CoordinatedOmission && interval > 0) || openLoop

	// Deterministic mode fingerprints the commands as they are sent.
	var fp *trafficPrint
	tap := opt.Tap
	if cfg.Run.Deterministic {
		fp = newTrafficPrint(workers)
		if tap == nil {
			tap = fp.add
		} else {
			user := tap
			tap = func(ev Event) { fp.add(ev); user(ev) }
		}
	}
	if tap != nil {
		c = Tapped(c, workers, opt.CaptureReplies, tap)
	}
	// An op budget replaces the duration: worker w sends budgets[w] operations.
	budgets := splitBudget(cfg.Run.OpBudget, workers)
	cpuStart := cpuTime()
	start := time.Now()
	if opt.OnStart != nil {
		opt.OnStart(start)
	}
	deadline := start.Add(cfg.Run.Duration)
	if cfg.Run.OpBudget > 0 {
		deadline = start.Add(100 * 365 * 24 * time.Hour) // the budget ends the run, not the clock
	}
	col := stats.NewCollector(names, workers, start)
	col.SetActiveWorkers(func() int { return sched.TargetAt(time.Since(start)) })

	// Sampler: close a time slice every output.sample_interval.
	sampDone := make(chan struct{})
	stopSamp := make(chan struct{})
	go func() {
		defer close(sampDone)
		t := time.NewTicker(cfg.Output.SampleInterval)
		defer t.Stop()
		for {
			select {
			case <-stopSamp:
				return
			case now := <-t.C:
				s := col.Snapshot(now)
				if opt.OnSample != nil {
					opt.OnSample(s)
				}
			}
		}
	}()

	// Open loop: a dispatcher creates arrivals at the target rate no matter how
	// the server is doing; workers take them. A full queue drops arrivals.
	var arrivals chan time.Time
	if openLoop {
		batchesPerSec := cfg.Run.TargetOpsPerSec / float64(depth)
		qcap := int(math.Min(200000, math.Max(1000, batchesPerSec*10)))
		arrivals = make(chan time.Time, qcap)
		batchEvery := time.Duration(float64(time.Second) / batchesPerSec)
		go func() {
			defer close(arrivals)
			next := start
			for {
				if !sleepUntil(ctx, next, deadline) {
					return
				}
				select {
				case arrivals <- next:
				default:
					col.NoteDropped(uint64(depth))
				}
				next = next.Add(batchEvery)
			}
		}()
	}

	var psub *pubSub
	if cfg.Workload.PubSub.Subscribers > 0 {
		var perr error
		if psub, perr = startPubSub(ctx, c, cfg, col); perr != nil {
			close(stopSamp)
			<-sampDone
			return nil, perr
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ctx := WithWorker(ctx, w) // lets a Tap attribute this worker's commands
			r, g, rec := rngs[w], gens[w], col.Recorder(w)
			cmds := make([]Cmd, depth)
			which := make([]int, depth)
			obs := make([]stats.Obs, depth)
			var extras []int // solo and composite picks of the current batch
			one := make([]stats.Obs, 1)
			remaining := int64(-1) // operations this worker may still send; -1 = no budget
			if budgets != nil {
				remaining = budgets[w]
			}
			next := start
			if interval > 0 {
				next = start.Add(interval * time.Duration(w) / time.Duration(workers))
			}
			node := func(key string) int {
				if opt.Router == nil {
					return 0
				}
				return opt.Router.NodeIndex(key) + 1 // -1 (unknown) becomes 0 (not attributed)
			}
			send := func(intended time.Time) bool {
				n := 0
				extras = extras[:0]
				for i := 0; i < depth; i++ {
					if remaining == 0 {
						break
					}
					if remaining > 0 {
						remaining--
					}
					idx := wl.Pick(r)
					if wl.Kind(idx) == config.KindSimple {
						which[n], cmds[n] = idx, wl.Build(g, idx)
						n++
					} else {
						extras = append(extras, idx)
					}
				}
				// Simple operations travel together in one pipelined round trip.
				if n > 0 {
					t0 := time.Now()
					res := c.Exec(ctx, cmds[:n])
					done := time.Now()
					if ctx.Err() != nil {
						return false // canceled mid-batch: drop it rather than record a bogus latency
					}
					d := done.Sub(t0)
					if co {
						d = done.Sub(intended)
					}
					for i, rs := range res {
						o := stats.Obs{Op: which[i], Miss: rs.Miss, Node: node(RouteKey(cmds[i]))}
						if rs.Err != nil {
							o.Class = stats.Classify(rs.Err)
							col.NoteError(cmds[i][0] + ": " + rs.Err.Error())
						}
						obs[i] = o
					}
					rec.Record(obs[:len(res)], d)
				}
				// Blocking and composite operations each take their own round trips.
				for _, idx := range extras {
					t0 := time.Now()
					var rs Result
					var route, name string
					if wl.Kind(idx) == config.KindSolo {
						cmd := wl.Build(g, idx)
						rs, route, name = c.Exec(ctx, []Cmd{cmd})[0], RouteKey(cmd), cmd[0]
					} else {
						rs, route = wl.RunComposite(ctx, c, g, idx)
						name = wl.Item(idx).Op
					}
					done := time.Now()
					if ctx.Err() != nil {
						return false
					}
					d := done.Sub(t0)
					if co {
						d = done.Sub(intended)
					}
					o := stats.Obs{Op: idx, Miss: rs.Miss, Node: node(route)}
					if rs.Err != nil {
						o.Class = stats.Classify(rs.Err)
						col.NoteError(name + ": " + rs.Err.Error())
					}
					one[0] = o
					rec.Record(one, d)
				}
				return true
			}
			for ctx.Err() == nil {
				now := time.Now()
				if (!now.Before(deadline) && !openLoop) || remaining == 0 {
					return
				}
				// Load profile: workers above the current target sit out.
				if w >= sched.TargetAt(now.Sub(start)) {
					if openLoop && !now.Before(deadline) {
						// Drain what is still queued so it is counted as dropped.
						for range arrivals {
							col.NoteDropped(uint64(depth))
						}
						return
					}
					if !sleepUntil(ctx, now.Add(50*time.Millisecond), deadline.Add(time.Hour)) {
						return
					}
					continue
				}
				if openLoop {
					select {
					case <-ctx.Done():
						return
					case arrival, ok := <-arrivals:
						if !ok {
							return
						}
						if !time.Now().Before(deadline) {
							col.NoteDropped(uint64(depth)) // run over: unserved arrival
							continue
						}
						if !send(arrival) {
							return
						}
					}
					continue
				}
				intended := now
				if interval > 0 {
					intended = next
					next = next.Add(interval)
					if !sleepUntil(ctx, intended, deadline) {
						return
					}
				}
				if !send(intended) {
					return
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(stopSamp)
	<-sampDone
	s := col.Finish(time.Now(), cfg.Output.SampleInterval/2)
	if opt.OnSample != nil {
		opt.OnSample(s)
	}

	var psRes *PubSubResult
	if psub != nil {
		// Published counts come from the finished per-operation totals.
		var published uint64
		for i, o := range col.Totals().PerOp { // aligned with the mix
			if it := wl.Item(i); it.Op == "PUBLISH" || it.Op == "SPUBLISH" {
				published += o.Ops - o.Errors
			}
		}
		r := psub.finish(published * uint64(cfg.Workload.PubSub.Subscribers))
		r.Published = published
		r.Expected = published * uint64(cfg.Workload.PubSub.Subscribers)
		psRes = &r
	}

	t := col.Totals()
	res := &RunResult{
		Ops: t.Ops, Errors: t.Errors, Misses: t.Misses, ErrClasses: t.ErrClasses, FirstError: t.FirstError,
		Elapsed: elapsed, CPU: cpuTime() - cpuStart, Hist: t.Hist, Seed: seed,
		Series: t.Series, SeriesFactor: t.SeriesFactor,
		CoordinatedOmission: co, TargetOpsPerSec: cfg.Run.TargetOpsPerSec,
		Loop: cfg.Run.Loop, LoadProfile: sched.Summary(), PeakWorkers: workers, Dropped: t.Dropped, PerNode: t.PerNode,
		PubSub: psRes, DeliveryHist: t.DeliveryHist,
	}
	if fp != nil {
		res.TrafficHash, res.TrafficOps = fp.sum()
	}
	for _, o := range t.PerOp {
		res.PerOp = append(res.PerOp, OpStat{Op: o.Name, Ops: o.Ops, Errors: o.Errors, Misses: o.Misses,
			P50: o.P50, P95: o.P95, P99: o.P99, Max: o.Max})
	}
	return res, nil
}

// sleepUntil waits until t. It returns false if ctx ends or the deadline
// passes first, so the worker stops.
func sleepUntil(ctx context.Context, t, deadline time.Time) bool {
	if !t.Before(deadline) {
		return false
	}
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}

// splitBudget divides total operations between the workers: every worker gets
// total/workers and the first total%workers get one more. Nil when there is no budget.
func splitBudget(total int64, workers int) []int64 {
	if total <= 0 {
		return nil
	}
	out := make([]int64, workers)
	for w := range out {
		out[w] = total / int64(workers)
		if int64(w) < total%int64(workers) {
			out[w]++
		}
	}
	return out
}

// trafficPrint hashes the commands each worker sends, in order. A worker only
// ever calls add for its own id, so the per-worker state needs no lock.
type trafficPrint struct {
	hashes []*workloadrec.StreamHash
	ops    []uint64
}

func newTrafficPrint(workers int) *trafficPrint {
	t := &trafficPrint{hashes: make([]*workloadrec.StreamHash, workers), ops: make([]uint64, workers)}
	for i := range t.hashes {
		t.hashes[i] = workloadrec.NewStreamHash()
	}
	return t
}

func (t *trafficPrint) add(ev Event) {
	for _, c := range ev.Cmds {
		raw, _ := workloadrec.EncodeCmd(c)
		t.hashes[ev.Worker].Add(raw)
		t.ops[ev.Worker]++
	}
}

// sum combines the workers that sent something, like a recording's trailer.
func (t *trafficPrint) sum() (string, uint64) {
	sums := map[int]string{}
	var n uint64
	for w, h := range t.hashes {
		if t.ops[w] > 0 {
			sums[w] = h.Sum()
			n += t.ops[w]
		}
	}
	return workloadrec.CombineHashes(sums), n
}
