// Package replay sends the commands of a recording to a server again, in the
// order and, if asked, at the pace they were recorded.
//
// The file is streamed, never loaded whole: a reader goroutine cuts it into
// groups (the commands that were sent together) and hands each to the worker
// that sent it, through a bounded queue, so memory stays flat for any file
// size. Every command reaches the server through valkey.Client, the single
// execute path.
package replay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// queueDepth is how many groups the reader may run ahead of a worker.
const queueDepth = 256

// maxExamples bounds the differences kept in a Result.
const maxExamples = 10

// Options tune a replay.
type Options struct {
	// Speed scales the recorded pace: 1 replays at the recorded pace, 2 twice
	// as fast, 0 sends as fast as the server answers.
	Speed float64
	// ReadsOnly skips every group that holds a command that is not known to
	// be read-only, so a replay cannot change the data.
	ReadsOnly bool
	// Iterations repeats the whole replay (default 1), IterationDelay apart.
	Iterations     int
	IterationDelay time.Duration
	// AllowDangerous replays commands such as FLUSHALL if the file holds them.
	AllowDangerous bool
	// Checksums says the client digests replies (valkey.Options.CaptureReplies),
	// so recorded checksums can be compared.
	Checksums bool
	// OnSample, when set, is called about once a second with the slice just
	// finished, so a caller can show progress. It must not block.
	OnSample func(stats.Sample)
}

// Diff is one command whose outcome differed from the recording. It holds the
// command name, never its arguments.
type Diff struct {
	Seq      uint64 `json:"seq"`
	Command  string `json:"command"`
	Kind     string `json:"kind"` // status or checksum
	Recorded string `json:"recorded"`
	Replayed string `json:"replayed"`
}

// Iteration is one pass over the recording.
type Iteration struct {
	Index   int           `json:"index"`
	Ops     uint64        `json:"ops"`
	Errors  uint64        `json:"errors"`
	Misses  uint64        `json:"misses"`
	Skipped uint64        `json:"skipped"`
	Elapsed time.Duration `json:"elapsed_ns"`
	P50     time.Duration `json:"p50_ns"`
	P95     time.Duration `json:"p95_ns"`
	P99     time.Duration `json:"p99_ns"`
}

// OpStat is one command's totals.
type OpStat struct {
	Command             string
	Ops, Errors, Misses uint64
	P50, P95, P99, Max  time.Duration
}

// Outcomes compares the replay with the outcomes the recording kept.
type Outcomes struct {
	Compared         uint64 `json:"compared"`
	StatusDiffs      uint64 `json:"status_diffs"`
	ChecksumCompared uint64 `json:"checksum_compared"`
	ChecksumDiffs    uint64 `json:"checksum_diffs"`
	Examples         []Diff `json:"examples,omitempty"`
	// RecordedP50/P99 are the latencies the recording kept, for the commands that were replayed.
	RecordedP50 time.Duration `json:"recorded_p50_ns"`
	RecordedP99 time.Duration `json:"recorded_p99_ns"`
	Note        string        `json:"note,omitempty"`
}

// Result is what a replay did.
type Result struct {
	Path       string
	Header     workloadrec.Header
	Status     string // recording status: complete, truncated, failed or incomplete
	Iterations []Iteration
	Ops        uint64
	Errors     uint64
	Misses     uint64
	Skipped    uint64 // commands left out by ReadsOnly
	Elapsed    time.Duration
	Hist       *latency.Histogram
	Series     []stats.Sample
	PerOp      []OpStat
	ErrClasses map[string]uint64
	FirstError string
	// CommandsHash is the hash of the commands sent in the first iteration, per
	// the recorder's rules. HashMatches is true when it equals the recording's
	// (never when ReadsOnly left commands out).
	CommandsHash string
	HashMatches  bool
	// MaxLag is the longest a group started after its recorded time (paced replay).
	MaxLag    time.Duration
	Outcomes  *Outcomes
	Speed     float64
	ReadsOnly bool
	// RecordedHash and RecordedOps are the recording's own hash and command count.
	RecordedHash string
	RecordedOps  uint64
}

// OpsPerSec is the achieved throughput.
func (r *Result) OpsPerSec() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Ops) / r.Elapsed.Seconds()
}

type group struct {
	worker    int
	offsetUS  int64
	dedicated bool
	ops       []*workloadrec.OpLine
	args      [][]string
}

// Run replays the recording at path against c. Canceling ctx stops it early; the
// partial result is returned with a nil error.
func Run(ctx context.Context, c valkey.Client, path string, opt Options) (*Result, error) {
	info, err := workloadrec.Scan(path)
	if err != nil {
		return nil, err
	}
	if info.Status == workloadrec.StatusIncomplete || (info.Trailer != nil && info.Status == workloadrec.StatusFailed && info.Trailer.Status != workloadrec.StatusFailed) {
		return nil, fmt.Errorf("%s cannot be replayed: %s", path, info.Reason)
	}
	if opt.Iterations < 1 {
		opt.Iterations = 1
	}
	h := info.Header
	workers := h.Workers
	if workers < 1 {
		workers = 1
	}
	names := make([]string, 0, len(info.Commands))
	for n := range info.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	nameIdx := make(map[string]int, len(names))
	for i, n := range names {
		nameIdx[n] = i
	}

	start := time.Now()
	col := stats.NewCollector(names, workers, start)
	res := &Result{Path: path, Header: h, Status: info.Status, Speed: opt.Speed, ReadsOnly: opt.ReadsOnly, RecordedHash: info.CommandsHash, RecordedOps: info.Ops}
	var cmp *comparer
	if h.Outcomes && opt.ReadsOnly {
		// Without the writes the replies are not the recorded ones: comparing them would only report noise.
		res.Outcomes = &Outcomes{Note: "not compared: a reads-only replay skips the writes the recorded replies depended on"}
	} else if h.Outcomes {
		cmp = &comparer{sums: h.ResultChecksums && opt.Checksums}
		res.Outcomes = &Outcomes{}
		if h.ResultChecksums && !opt.Checksums {
			res.Outcomes.Note = "the recording has result checksums but this client does not digest replies, so only statuses were compared"
		}
	}

	sampDone, stopSamp := make(chan struct{}), make(chan struct{})
	go func() { // keep the time series going so a long replay has a chart
		defer close(sampDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopSamp:
				return
			case now := <-t.C:
				sm := col.Snapshot(now)
				if opt.OnSample != nil {
					opt.OnSample(sm)
				}
			}
		}
	}()

	var maxLag atomic.Int64
	for it := 1; it <= opt.Iterations && ctx.Err() == nil; it++ {
		if it > 1 && opt.IterationDelay > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(opt.IterationDelay):
			}
			if ctx.Err() != nil {
				break
			}
		}
		itr, hashes, err := runIteration(ctx, c, path, h, workers, nameIdx, col, cmp, opt, &maxLag)
		if err != nil {
			close(stopSamp)
			<-sampDone
			return nil, err
		}
		itr.Index = it
		res.Iterations = append(res.Iterations, itr)
		res.Skipped += itr.Skipped
		if it == 1 {
			res.CommandsHash = workloadrec.CombineHashes(hashes)
		}
	}
	res.Elapsed = time.Since(start)
	close(stopSamp)
	<-sampDone
	col.Finish(time.Now(), 0)
	t := col.Totals()
	res.Ops, res.Errors, res.Misses, res.Hist = t.Ops, t.Errors, t.Misses, t.Hist
	res.ErrClasses, res.FirstError, res.Series = t.ErrClasses, t.FirstError, t.Series
	for _, o := range t.PerOp {
		res.PerOp = append(res.PerOp, OpStat{Command: o.Name, Ops: o.Ops, Errors: o.Errors, Misses: o.Misses, P50: o.P50, P95: o.P95, P99: o.P99, Max: o.Max})
	}
	res.MaxLag = time.Duration(maxLag.Load())
	res.HashMatches = !opt.ReadsOnly && info.Trailer != nil && info.Trailer.CommandsHash == res.CommandsHash && ctx.Err() == nil
	if cmp != nil {
		res.Outcomes.Compared, res.Outcomes.StatusDiffs = cmp.compared.Load(), cmp.statusDiffs.Load()
		res.Outcomes.ChecksumCompared, res.Outcomes.ChecksumDiffs = cmp.sumCompared.Load(), cmp.sumDiffs.Load()
		res.Outcomes.Examples = cmp.examples
		recorded := cmp.recordedHist()
		res.Outcomes.RecordedP50, res.Outcomes.RecordedP99 = recorded.Quantile(0.5), recorded.Quantile(0.99)
	}
	return res, nil
}

// comparer checks replayed results against recorded outcomes.
type comparer struct {
	sums        bool
	compared    atomic.Uint64
	statusDiffs atomic.Uint64
	sumCompared atomic.Uint64
	sumDiffs    atomic.Uint64

	mu       sync.Mutex
	examples []Diff
	rec      latency.Histogram
}

func (m *comparer) note(d Diff) {
	m.mu.Lock()
	if len(m.examples) < maxExamples {
		m.examples = append(m.examples, d)
	}
	m.mu.Unlock()
}

func (m *comparer) recordedHist() *latency.Histogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := &latency.Histogram{}
	h.Merge(&m.rec)
	return h
}

func (m *comparer) check(op *workloadrec.OpLine, name string, r valkey.Result) {
	rc := op.Outcome
	if rc == nil {
		return
	}
	m.compared.Add(1)
	m.mu.Lock()
	m.rec.Record(time.Duration(rc.US) * time.Microsecond)
	m.mu.Unlock()
	got := workloadrec.OutcomeOK
	switch {
	case r.Err != nil:
		got = workloadrec.OutcomeError
	case r.Miss:
		got = workloadrec.OutcomeMiss
	}
	if got != rc.Status {
		m.statusDiffs.Add(1)
		m.note(Diff{Seq: op.Seq, Command: name, Kind: "status", Recorded: rc.Status, Replayed: got})
		return
	}
	if m.sums && got == workloadrec.OutcomeOK && rc.Sum != "" {
		m.sumCompared.Add(1)
		if now := valkey.OutcomeOf(r, 0, true).Sum; now != rc.Sum {
			m.sumDiffs.Add(1)
			m.note(Diff{Seq: op.Seq, Command: name, Kind: "checksum", Recorded: rc.Sum, Replayed: now})
		}
	}
}

// readOnly lists commands that never change data. Anything not listed counts as
// a write for ReadsOnly, so an unknown command is skipped, not sent.
var readOnly = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`GET GETRANGE STRLEN MGET EXISTS TTL PTTL EXPIRETIME PEXPIRETIME TYPE
		HGET HMGET HGETALL HKEYS HVALS HLEN HEXISTS HSTRLEN HRANDFIELD HSCAN
		LLEN LRANGE LINDEX LPOS
		SCARD SISMEMBER SMISMEMBER SMEMBERS SRANDMEMBER SINTER SUNION SDIFF SINTERCARD SSCAN
		ZSCORE ZMSCORE ZRANK ZREVRANK ZRANGE ZREVRANGE ZRANGEBYSCORE ZREVRANGEBYSCORE ZRANGEBYLEX ZREVRANGEBYLEX ZCOUNT ZLEXCOUNT ZCARD ZRANDMEMBER ZSCAN ZINTER ZUNION ZDIFF
		XLEN XRANGE XREVRANGE XREAD XPENDING XINFO
		GEOPOS GEODIST GEOHASH GEOSEARCH GEORADIUS_RO GEORADIUSBYMEMBER_RO
		GETBIT BITCOUNT BITPOS
		DBSIZE SCAN KEYS RANDOMKEY OBJECT PING ECHO EVAL_RO EVALSHA_RO FCALL_RO`) {
		readOnly[n] = true
	}
}

// IsReadOnly reports whether a command is known to leave the data unchanged.
func IsReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return readOnly[workloadrec.Name(args)]
}

func runIteration(ctx context.Context, c valkey.Client, path string, h workloadrec.Header, workers int, nameIdx map[string]int,
	col *stats.Collector, cmp *comparer, opt Options, maxLag *atomic.Int64) (Iteration, map[int]string, error) {

	rd, err := workloadrec.Open(path)
	if err != nil {
		return Iteration{}, nil, err
	}
	defer rd.Close()

	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	queues := make([]chan *group, workers)
	for i := range queues {
		queues[i] = make(chan *group, queueDepth)
	}
	hashes := make([]*workloadrec.StreamHash, workers)
	sent := make([]uint64, workers)
	hists := make([]latency.Histogram, workers)
	var ops, errs, misses, skipped atomic.Uint64
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		hashes[w] = workloadrec.NewStreamHash()
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			wctx := valkey.WithWorker(ictx, w)
			rec := col.Recorder(w)
			for g := range queues[w] {
				if ictx.Err() != nil {
					continue // drain so the reader never blocks
				}
				if opt.Speed > 0 {
					due := start.Add(time.Duration(float64(g.offsetUS) / opt.Speed * float64(time.Microsecond)))
					if d := time.Until(due); d > 0 {
						t := time.NewTimer(d)
						select {
						case <-ictx.Done():
							t.Stop()
							continue
						case <-t.C:
						}
					} else if lag := int64(-d); lag > maxLag.Load() {
						for {
							cur := maxLag.Load()
							if lag <= cur || maxLag.CompareAndSwap(cur, lag) {
								break
							}
						}
					}
				}
				cmds := make([]valkey.Cmd, len(g.args))
				for i, a := range g.args {
					cmds[i] = valkey.Cmd(a)
				}
				t0 := time.Now()
				var results []valkey.Result
				if g.dedicated {
					results = make([]valkey.Result, 0, len(cmds))
					derr := c.Dedicated(wctx, func(cn valkey.Conn) error {
						for _, cmd := range cmds {
							results = append(results, cn.Exec(wctx, []valkey.Cmd{cmd})[0])
						}
						return nil
					})
					if derr == nil {
						derr = errors.New("the dedicated connection stopped early")
					}
					for len(results) < len(cmds) {
						results = append(results, valkey.Result{Err: derr})
					}
				} else {
					results = c.Exec(wctx, cmds)
				}
				d := time.Since(t0)
				if ictx.Err() != nil {
					continue // stopped mid-group: do not count a partial batch
				}
				obs := make([]stats.Obs, len(results))
				for i, r := range results {
					name := workloadrec.Name(g.args[i])
					o := stats.Obs{Op: nameIdx[name], Miss: r.Miss}
					if r.Err != nil {
						o.Class = stats.Classify(r.Err)
						col.NoteError(name + ": " + r.Err.Error())
						errs.Add(1)
					}
					if r.Miss {
						misses.Add(1)
					}
					obs[i] = o
					raw, _ := workloadrec.EncodeCmd(g.args[i])
					hashes[w].Add(raw)
					sent[w]++
					if cmp != nil {
						cmp.check(g.ops[i], name, r)
					}
					hists[w].Record(d)
				}
				rec.Record(obs, d)
				ops.Add(uint64(len(results)))
			}
		}(w)
	}

	// Reader: cut the file into groups and route each to its worker.
	pending := make([]*group, workers)
	last := make([]uint64, workers)
	flush := func(w int) bool {
		g := pending[w]
		pending[w] = nil
		if g == nil {
			return true
		}
		if opt.ReadsOnly {
			for _, a := range g.args {
				if !IsReadOnly(a) {
					skipped.Add(uint64(len(g.args)))
					return true
				}
			}
		}
		select {
		case queues[w] <- g:
			return true
		case <-ictx.Done():
			return false
		}
	}
	readErr := func() error {
		for {
			op, err := rd.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if op.Worker < 0 || op.Worker >= workers {
				return fmt.Errorf("op %d belongs to worker %d, but the header lists %d workers", op.Seq, op.Worker, workers)
			}
			args, err := op.Args()
			if err != nil {
				return err
			}
			if err := rawcmd.CheckRecorded(args, opt.AllowDangerous); err != nil {
				return fmt.Errorf("op %d: %w (replay refuses it)", op.Seq, err)
			}
			w := op.Worker
			if pending[w] != nil && last[w] != op.Group {
				if !flush(w) {
					return nil
				}
			}
			if pending[w] == nil {
				pending[w] = &group{worker: w, offsetUS: op.OffsetUS, dedicated: op.Conn == workloadrec.ConnDedicated}
				last[w] = op.Group
			}
			pending[w].ops = append(pending[w].ops, op)
			pending[w].args = append(pending[w].args, args)
		}
	}()
	if readErr == nil {
		for w := range pending {
			if !flush(w) {
				break
			}
		}
	}
	for _, q := range queues {
		close(q)
	}
	if readErr == nil {
		wg.Wait()
	}
	if readErr != nil {
		cancel()
		wg.Wait()
		return Iteration{}, nil, readErr
	}
	var all latency.Histogram
	for i := range hists {
		all.Merge(&hists[i])
	}
	sums := map[int]string{}
	for w, hs := range hashes {
		if sent[w] > 0 { // a worker that sent nothing has no stream, as in the recorder
			sums[w] = hs.Sum()
		}
	}
	return Iteration{Ops: ops.Load(), Errors: errs.Load(), Misses: misses.Load(), Skipped: skipped.Load(), Elapsed: time.Since(start),
		P50: all.Quantile(0.5), P95: all.Quantile(0.95), P99: all.Quantile(0.99)}, sums, nil
}
