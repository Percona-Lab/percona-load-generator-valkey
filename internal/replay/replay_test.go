package replay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

type batch struct {
	cmds      [][]string
	dedicated bool
}

// sink is a Client that remembers what each worker sent. reply decides each
// command's result.
type sink struct {
	mu    sync.Mutex
	per   map[int][]batch
	delay time.Duration
	reply func(cmd []string) valkey.Result
}

func newSink() *sink {
	return &sink{per: map[int][]batch{}, reply: func(c []string) valkey.Result {
		if c[0] == "GET" {
			return valkey.Result{Miss: true}
		}
		return valkey.Result{}
	}}
}

func (s *sink) add(ctx context.Context, b batch) {
	w, _ := valkey.WorkerOf(ctx)
	s.mu.Lock()
	s.per[w] = append(s.per[w], b)
	s.mu.Unlock()
}

func (s *sink) Exec(ctx context.Context, cmds []valkey.Cmd) []valkey.Result {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	b := batch{}
	out := make([]valkey.Result, len(cmds))
	for i, c := range cmds {
		b.cmds = append(b.cmds, c)
		out[i] = s.reply(c)
	}
	s.add(ctx, b)
	return out
}

func (s *sink) Query(ctx context.Context, cmd valkey.Cmd) (any, error) {
	s.add(ctx, batch{cmds: [][]string{cmd}})
	return nil, nil
}

type sinkConn struct {
	s *sink
	b *batch
}

func (c sinkConn) Query(_ context.Context, cmd valkey.Cmd) (any, error) {
	c.b.cmds = append(c.b.cmds, cmd)
	return nil, nil
}

func (c sinkConn) Exec(_ context.Context, cmds []valkey.Cmd) []valkey.Result {
	out := make([]valkey.Result, len(cmds))
	for i, cmd := range cmds {
		c.b.cmds = append(c.b.cmds, cmd)
		out[i] = c.s.reply(cmd)
	}
	return out
}

func (s *sink) Dedicated(ctx context.Context, fn func(valkey.Conn) error) error {
	b := batch{dedicated: true}
	err := fn(sinkConn{s, &b})
	s.add(ctx, b)
	return err
}

func (s *sink) Subscribe(ctx context.Context, _ bool, _ []string, _ func(string, string), onReady func()) error {
	if onReady != nil {
		onReady()
	}
	<-ctx.Done()
	return nil
}
func (s *sink) Close() {}

// flat returns the commands a worker sent, with each dedicated batch (whatever
// its inner pipelining) as one entry and each plain batch as its size.
func (s *sink) shape(w int) (cmds []string, sizes []int) {
	for _, b := range s.per[w] {
		for _, c := range b.cmds {
			cmds = append(cmds, strings.Join(c, " "))
		}
		if b.dedicated {
			sizes = append(sizes, -len(b.cmds))
		} else {
			sizes = append(sizes, len(b.cmds))
		}
	}
	return
}

func cfgFor(mix []config.MixItem, workers, depth int) config.Config {
	c := config.Default()
	c.Run.Concurrency = workers
	c.Run.Duration = 300 * time.Millisecond
	c.Run.TargetOpsPerSec = 3000
	c.Run.Seed = 11
	c.Dataset.KeyCount = 500
	c.Dataset.ValueSize = 16
	c.Target.Pool.PipelineDepth = depth
	c.Workload.Mix = mix
	return c
}

// capture runs the workload against a sink with the recorder attached.
func capture(t *testing.T, cfg config.Config, h workloadrec.Header, opt workloadrec.Options, reply func([]string) valkey.Result) (string, *sink, *valkey.RunResult) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.rec")
	h.Workers, h.Seed = cfg.Run.Concurrency, cfg.Run.Seed
	rec, err := workloadrec.NewRecorder(p, opt, h)
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	if reply != nil {
		s.reply = reply
	}
	res, err := valkey.RunWith(context.Background(), s, cfg, valkey.RunOptions{Tap: valkey.RecordTap(rec), OnStart: rec.SetStart, CaptureReplies: h.ResultChecksums})
	if err != nil {
		t.Fatal(err)
	}
	if r := rec.Close(); r.Status != workloadrec.StatusComplete {
		t.Fatalf("recording: %+v", r)
	}
	return p, s, res
}

var mixed = []config.MixItem{
	{Op: "GET", Weight: 4}, {Op: "SET", Weight: 3}, {Op: "HSET", Weight: 1}, {Op: "INCR", Weight: 1},
	{Op: "WATCH_INCR", Weight: 1}, {Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET", Weight: 1}, {Op: "INCR", Weight: 1}}},
}

func TestRecordThenReplayReproducesTheOpStream(t *testing.T) {
	cfg := cfgFor(mixed, 4, 3)
	p, orig, run := capture(t, cfg, workloadrec.Header{}, workloadrec.Options{}, nil)

	target := newSink()
	res, err := Run(context.Background(), target, p, Options{Speed: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops == 0 || res.Errors != 0 {
		t.Fatalf("result = %+v", res)
	}
	for w := 0; w < 4; w++ {
		wantCmds, wantSizes := orig.shape(w)
		gotCmds, gotSizes := target.shape(w)
		if !reflect.DeepEqual(gotCmds, wantCmds) {
			t.Fatalf("worker %d sent a different command stream (%d vs %d commands)", w, len(gotCmds), len(wantCmds))
		}
		// Plain batches keep their pipelining; dedicated ones stay on one connection.
		if len(gotSizes) != len(wantSizes) {
			t.Fatalf("worker %d: %d groups, recorded %d", w, len(gotSizes), len(wantSizes))
		}
		for i := range gotSizes {
			if (gotSizes[i] < 0) != (wantSizes[i] < 0) {
				t.Fatalf("worker %d group %d: dedicated flag lost", w, i)
			}
			if gotSizes[i] > 0 && gotSizes[i] != wantSizes[i] {
				t.Fatalf("worker %d group %d: batch of %d, recorded %d", w, i, gotSizes[i], wantSizes[i])
			}
		}
	}
	info, _ := workloadrec.Scan(p)
	if !res.HashMatches || res.CommandsHash != info.CommandsHash {
		t.Fatalf("the replay must report the recording's hash: %s vs %s", res.CommandsHash, info.CommandsHash)
	}
	if res.Ops != info.Ops || run.Ops == 0 {
		t.Fatalf("replayed %d commands, recorded %d", res.Ops, info.Ops)
	}
}

func TestReadsOnlySkipsEverythingThatWrites(t *testing.T) {
	cfg := cfgFor(mixed, 2, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{}, workloadrec.Options{}, nil)
	target := newSink()
	res, err := Run(context.Background(), target, p, Options{Speed: 0, ReadsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops == 0 || res.Skipped == 0 || res.HashMatches {
		t.Fatalf("result = %+v", res)
	}
	for w, bs := range target.per {
		for _, b := range bs {
			for _, c := range b.cmds {
				if !IsReadOnly(c) {
					t.Fatalf("worker %d sent %v in a reads-only replay", w, c)
				}
			}
		}
	}
	info, _ := workloadrec.Scan(p)
	if res.Ops+res.Skipped != info.Ops {
		t.Fatalf("sent %d + skipped %d != recorded %d", res.Ops, res.Skipped, info.Ops)
	}
}

func TestReadsOnlyIsSafeForUnknownCommands(t *testing.T) {
	for _, c := range [][]string{{"GET", "k"}, {"hgetall", "h"}, {"ZRANGE", "z", "0", "-1"}} {
		if !IsReadOnly(c) {
			t.Fatalf("%v is a read", c)
		}
	}
	for _, c := range [][]string{{"SET", "k", "v"}, {"EVAL", "return 1", "0"}, {"XREADGROUP", "GROUP", "g", "c", "STREAMS", "s", ">"}, {"NEWCOMMAND"}, {"MULTI"}, {}} {
		if IsReadOnly(c) {
			t.Fatalf("%v must not count as a read", c)
		}
	}
}

func TestIterationsRepeatTheReplay(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "SET", Weight: 1}}, 2, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{}, workloadrec.Options{}, nil)
	info, _ := workloadrec.Scan(p)
	res, err := Run(context.Background(), newSink(), p, Options{Speed: 0, Iterations: 3, IterationDelay: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Iterations) != 3 || res.Ops != 3*info.Ops {
		t.Fatalf("iterations = %d, ops = %d, recorded %d", len(res.Iterations), res.Ops, info.Ops)
	}
	for i, it := range res.Iterations {
		if it.Index != i+1 || it.Ops != info.Ops {
			t.Fatalf("iteration %d = %+v", i, it)
		}
	}
	if !res.HashMatches {
		t.Fatal("the first iteration sends the recorded commands")
	}
}

func TestOutcomesAreComparedWithTheRecording(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "GET", Weight: 1}, {Op: "SET", Weight: 1}}, 2, 1)
	reply := func(c []string) valkey.Result { // GET returns a value of size 5
		if c[0] == "GET" {
			return valkey.Result{Len: 5, Sum: 0xaa}
		}
		return valkey.Result{}
	}
	p, _, _ := capture(t, cfg, workloadrec.Header{ResultChecksums: true}, workloadrec.Options{}, reply)
	info, _ := workloadrec.Scan(p)
	gets := info.Commands["GET"]

	// The same replies: nothing differs.
	same := newSink()
	same.reply = reply
	res, err := Run(context.Background(), same, p, Options{Speed: 0, Checksums: true})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcomes
	if o == nil || o.Compared != info.Ops || o.StatusDiffs != 0 || o.ChecksumDiffs != 0 || o.ChecksumCompared != info.Ops {
		t.Fatalf("outcomes = %+v (gets %d)", o, gets)
	}

	// Another server: GET misses, so the status differs for every GET.
	missing := newSink()
	res, _ = Run(context.Background(), missing, p, Options{Speed: 0, Checksums: true})
	if res.Outcomes.StatusDiffs != gets || len(res.Outcomes.Examples) == 0 || res.Outcomes.Examples[0].Kind != "status" {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}

	// Same status, other content: the checksum differs.
	other := newSink()
	other.reply = func(c []string) valkey.Result {
		if c[0] == "GET" {
			return valkey.Result{Len: 5, Sum: 0xbb}
		}
		return valkey.Result{}
	}
	res, _ = Run(context.Background(), other, p, Options{Speed: 0, Checksums: true})
	if res.Outcomes.ChecksumDiffs != gets || res.Outcomes.StatusDiffs != 0 || res.Outcomes.Examples[0].Kind != "checksum" {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	for _, d := range res.Outcomes.Examples {
		if len(d.Command) > 12 {
			t.Fatalf("examples hold command names, never arguments: %+v", d)
		}
	}

	// A client that does not digest replies cannot compare checksums; say so.
	res, _ = Run(context.Background(), other, p, Options{Speed: 0})
	if res.Outcomes.ChecksumCompared != 0 || res.Outcomes.Note == "" {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
}

func TestRecordedLatencyIsReported(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "SET", Weight: 1}}, 1, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{Outcomes: true}, workloadrec.Options{}, nil)
	res, _ := Run(context.Background(), newSink(), p, Options{Speed: 0})
	if res.Outcomes == nil || res.Outcomes.Compared == 0 {
		t.Fatalf("%+v", res.Outcomes)
	}
	plain, _, _ := capture(t, cfg, workloadrec.Header{}, workloadrec.Options{}, nil)
	res, _ = Run(context.Background(), newSink(), plain, Options{Speed: 0})
	if res.Outcomes != nil {
		t.Fatal("a recording without outcomes has nothing to compare")
	}
}

// handmade writes a recording of one worker whose commands are offset apart.
func handmade(t *testing.T, workers int, cmds ...workloadrec.Entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "h.rec")
	r, err := workloadrec.NewRecorder(p, workloadrec.Options{}, workloadrec.Header{Workers: workers})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		r.Record(c)
	}
	if res := r.Close(); res.Status != workloadrec.StatusComplete {
		t.Fatalf("%+v", res)
	}
	return p
}

func TestPacedReplayFollowsTheRecordedOffsets(t *testing.T) {
	var es []workloadrec.Entry
	for i := 0; i < 5; i++ {
		es = append(es, workloadrec.Entry{Worker: 0, Group: uint64(i + 1), Offset: time.Duration(i) * 60 * time.Millisecond, Cmd: []string{"SET", "k", "v"}})
	}
	p := handmade(t, 1, es...)

	t0 := time.Now()
	res, err := Run(context.Background(), newSink(), p, Options{Speed: 1})
	if err != nil || res.Ops != 5 {
		t.Fatalf("%+v, %v", res, err)
	}
	if d := time.Since(t0); d < 230*time.Millisecond {
		t.Fatalf("speed 1 replayed 240ms of traffic in %v", d)
	}
	t0 = time.Now()
	if _, err := Run(context.Background(), newSink(), p, Options{Speed: 4}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 50*time.Millisecond || d > 200*time.Millisecond {
		t.Fatalf("speed 4 took %v, want about 60ms", d)
	}
	t0 = time.Now()
	if _, err := Run(context.Background(), newSink(), p, Options{Speed: 0}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("speed 0 should not wait: %v", d)
	}
}

func TestPacedReplayReportsFallingBehind(t *testing.T) {
	var es []workloadrec.Entry
	for i := 0; i < 20; i++ {
		es = append(es, workloadrec.Entry{Worker: 0, Group: uint64(i + 1), Offset: time.Duration(i) * time.Millisecond, Cmd: []string{"SET", "k", "v"}})
	}
	slow := newSink()
	slow.delay = 25 * time.Millisecond // the target answers far slower than the recorded pace
	res, err := Run(context.Background(), slow, handmade(t, 1, es...), Options{Speed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxLag < 100*time.Millisecond {
		t.Fatalf("MaxLag = %v: a replay that cannot keep the pace must say so", res.MaxLag)
	}
}

func TestDangerousCommandsInAFileAreRefused(t *testing.T) {
	for _, c := range [][]string{{"FLUSHALL"}, {"CONFIG", "SET", "maxmemory", "1"}, {"DEBUG", "SLEEP", "1"}} {
		p := handmade(t, 1, workloadrec.Entry{Worker: 0, Group: 1, Cmd: []string{"SET", "k", "v"}}, workloadrec.Entry{Worker: 0, Group: 2, Cmd: c})
		target := newSink()
		if _, err := Run(context.Background(), target, p, Options{Speed: 0}); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("%v: err = %v", c, err)
		}
		if _, err := Run(context.Background(), newSink(), p, Options{Speed: 0, AllowDangerous: true}); err != nil {
			t.Fatalf("%v with -allow-dangerous: %v", c, err)
		}
	}
	// Connection-state commands are never replayed, and MULTI/EXEC/WATCH are fine.
	p := handmade(t, 1, workloadrec.Entry{Worker: 0, Group: 1, Cmd: []string{"SELECT", "3"}})
	if _, err := Run(context.Background(), newSink(), p, Options{Speed: 0, AllowDangerous: true}); err == nil {
		t.Fatal("SELECT must never be replayed")
	}
	p = handmade(t, 1,
		workloadrec.Entry{Worker: 0, Group: 1, Conn: workloadrec.ConnDedicated, Cmd: []string{"WATCH", "k"}},
		workloadrec.Entry{Worker: 0, Group: 1, Conn: workloadrec.ConnDedicated, Cmd: []string{"MULTI"}},
		workloadrec.Entry{Worker: 0, Group: 1, Conn: workloadrec.ConnDedicated, Cmd: []string{"EXEC"}})
	if _, err := Run(context.Background(), newSink(), p, Options{Speed: 0}); err != nil {
		t.Fatal(err)
	}
}

func TestUnfinishedOrEditedRecordingsAreRefused(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "SET", Weight: 1}}, 1, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{}, workloadrec.Options{}, nil)
	b := mustRead(t, p)
	lines := strings.Split(strings.TrimRight(b, "\n"), "\n")
	cut := filepath.Join(t.TempDir(), "cut.rec")
	mustWrite(t, cut, strings.Join(lines[:len(lines)-1], "\n")+"\n")
	if _, err := Run(context.Background(), newSink(), cut, Options{Speed: 0}); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("err = %v", err)
	}
	edited := filepath.Join(t.TempDir(), "edited.rec")
	mustWrite(t, edited, strings.Replace(b, `"SET"`, `"GET"`, 1))
	if _, err := Run(context.Background(), newSink(), edited, Options{Speed: 0}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkerOutsideTheHeaderIsAnError(t *testing.T) {
	p := handmade(t, 1, workloadrec.Entry{Worker: 3, Group: 1, Cmd: []string{"SET", "k", "v"}})
	if _, err := Run(context.Background(), newSink(), p, Options{Speed: 0}); err == nil || !strings.Contains(err.Error(), "worker 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestSlowTargetDoesNotLoseOrReorderCommands(t *testing.T) {
	// More groups than the per-worker queue holds, replayed by a slow worker:
	// the reader must wait for it instead of buffering the file.
	var es []workloadrec.Entry
	const n = 3 * queueDepth
	for i := 0; i < n; i++ {
		es = append(es, workloadrec.Entry{Worker: i % 2, Group: uint64(i/2 + 1), Cmd: []string{"SET", "k", "v"}})
	}
	slow := newSink()
	slow.delay = 200 * time.Microsecond
	res, err := Run(context.Background(), slow, handmade(t, 2, es...), Options{Speed: 0})
	if err != nil || res.Ops != n || !res.HashMatches {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestCancelStopsAReplayEarly(t *testing.T) {
	var es []workloadrec.Entry
	for i := 0; i < 2000; i++ {
		es = append(es, workloadrec.Entry{Worker: 0, Group: uint64(i + 1), Cmd: []string{"SET", "k", "v"}})
	}
	p := handmade(t, 1, es...)
	slow := newSink()
	slow.delay = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	res, err := Run(ctx, slow, p, Options{Speed: 0})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) > 2*time.Second || res.Ops == 0 || res.Ops >= 2000 || res.HashMatches {
		t.Fatalf("a canceled replay returns what it did: %d ops in %v", res.Ops, time.Since(t0))
	}
}

func TestErrorsFromTheTargetAreCounted(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "SET", Weight: 1}}, 1, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{Outcomes: true}, workloadrec.Options{}, nil)
	bad := newSink()
	bad.reply = func([]string) valkey.Result {
		return valkey.Result{Err: errors.New("READONLY You can't write against a read only replica")}
	}
	res, err := Run(context.Background(), bad, p, Options{Speed: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != res.Ops || res.FirstError == "" || len(res.ErrClasses) == 0 || res.Outcomes.StatusDiffs != res.Ops {
		t.Fatalf("%+v", res)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadsOnlyDoesNotCompareRepliesThatDependedOnSkippedWrites(t *testing.T) {
	cfg := cfgFor([]config.MixItem{{Op: "GET", Weight: 1}, {Op: "SET", Weight: 1}}, 1, 1)
	p, _, _ := capture(t, cfg, workloadrec.Header{Outcomes: true}, workloadrec.Options{}, nil)
	res, err := Run(context.Background(), newSink(), p, Options{Speed: 0, ReadsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := res.Outcomes; o == nil || o.Compared != 0 || o.Note == "" {
		t.Fatalf("outcomes = %+v", o)
	}
}
