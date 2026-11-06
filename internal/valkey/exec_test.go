package valkey

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

type events struct {
	mu  sync.Mutex
	all []Event
}

func (e *events) tap(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ev.Cmds = append([]Cmd(nil), ev.Cmds...)
	e.all = append(e.all, ev)
}

func TestTapSeesOnlyWorkerCommands(t *testing.T) {
	var ev events
	c := Tapped(newFake(), 4, false, ev.tap)
	ctx := context.Background()

	c.Exec(ctx, []Cmd{{"SET", "a", "1"}})                // no worker: preload, polling, scans
	_, _ = c.Query(ctx, Cmd{"PING"})                     // likewise
	c.Exec(WithWorker(ctx, 9), []Cmd{{"SET", "a", "1"}}) // a worker id the tap was not sized for
	if len(ev.all) != 0 {
		t.Fatalf("only worker commands are tapped, got %d events", len(ev.all))
	}

	w1 := WithWorker(ctx, 1)
	c.Exec(w1, []Cmd{{"SET", "a", "1"}, {"GET", "a"}, {"GET", "b"}}) // one pipelined batch
	_, _ = c.Query(w1, Cmd{"XLEN", "s"})
	c.Exec(WithWorker(ctx, 2), []Cmd{{"GET", "a"}})
	_ = c.Dedicated(w1, func(cn Conn) error {
		_, _ = cn.Query(w1, Cmd{"WATCH", "k"})
		cn.Exec(w1, []Cmd{{"MULTI"}, {"EXEC"}})
		return nil
	})

	if len(ev.all) != 5 {
		t.Fatalf("events = %d, want 5", len(ev.all))
	}
	b := ev.all[0]
	if b.Worker != 1 || b.Group != 1 || len(b.Cmds) != 3 || len(b.Results) != 3 || !b.Results[2].Miss || b.Dedicated {
		t.Fatalf("batch event = %+v", b)
	}
	if ev.all[1].Group != 2 || ev.all[1].Cmds[0][0] != "XLEN" {
		t.Fatalf("each worker numbers its own groups: %+v", ev.all[1])
	}
	if ev.all[2].Worker != 2 || ev.all[2].Group != 1 {
		t.Fatalf("worker 2 starts at group 1: %+v", ev.all[2])
	}
	d1, d2 := ev.all[3], ev.all[4]
	if !d1.Dedicated || !d2.Dedicated || d1.Group != 3 || d2.Group != 3 {
		t.Fatalf("a Dedicated call is one group: %+v %+v", d1, d2)
	}
}

func TestTapIgnoresCanceledBatches(t *testing.T) {
	var ev events
	c := Tapped(newFake(), 1, false, ev.tap)
	ctx, cancel := context.WithCancel(WithWorker(context.Background(), 0))
	cancel()
	c.Exec(ctx, []Cmd{{"GET", "a"}})
	if len(ev.all) != 0 {
		t.Fatal("a batch cut short by cancellation must not be recorded: the runner drops it too")
	}
}

func TestTapKeepsOptionalInterfaces(t *testing.T) {
	// Anything embedded in the wrapped client stays reachable through Subscribe.
	c := Tapped(newFake(), 1, false, func(Event) {})
	ready := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = c.Subscribe(ctx, false, []string{"x"}, func(string, string) {}, func() { ready = true })
	if !ready {
		t.Fatal("Subscribe must pass through")
	}
}

func TestDigest(t *testing.T) {
	m1 := map[string]any{"a": int64(1), "b": []any{"x", "y"}, "c": nil}
	m2 := map[string]any{"c": nil, "b": []any{"x", "y"}, "a": int64(1)}
	n1, s1 := Digest(m1)
	_, s2 := Digest(m2)
	if s1 != s2 || n1 != 3 {
		t.Fatalf("map order must not matter: %d %x %x", n1, s1, s2)
	}
	_, s3 := Digest(map[string]any{"a": int64(2), "b": []any{"x", "y"}, "c": nil})
	if s3 == s1 {
		t.Fatal("different content must give a different sum")
	}
	if n, _ := Digest("hello"); n != 5 {
		t.Fatalf("string size = %d", n)
	}
	if _, a := Digest("1"); a == func() uint64 { _, x := Digest(int64(1)); return x }() {
		t.Fatal("the string 1 and the integer 1 are different replies")
	}
	if n, _ := Digest([]any{"a", "b", "c"}); n != 3 {
		t.Fatalf("array size = %d", n)
	}
}

func recordedRun(t *testing.T, f Client, cfg config.Config, ro workloadrec.Options, h workloadrec.Header) (*RunResult, *workloadrec.FileInfo, workloadrec.Result) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.rec")
	h.Workers = cfg.Run.Concurrency
	cfg.Run.TargetOpsPerSec = 4000 // a few hundred commands: enough to check, quick to scan
	cfg.Run.Duration = 200 * time.Millisecond
	rec, err := workloadrec.NewRecorder(p, ro, h)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunWith(context.Background(), f, cfg, RunOptions{Tap: RecordTap(rec), OnStart: rec.SetStart, CaptureReplies: h.ResultChecksums})
	if err != nil {
		t.Fatal(err)
	}
	r := rec.Close()
	info, err := workloadrec.Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	return res, info, r
}

func TestRunRecordsEveryCommandItSends(t *testing.T) {
	cfg := testCfg()
	cfg.Target.Pool.PipelineDepth = 4
	cfg.Run.Seed = 5
	res, info, rr := recordedRun(t, newFake(), cfg, workloadrec.Options{}, workloadrec.Header{})
	if rr.Status != workloadrec.StatusComplete || info.Status != workloadrec.StatusComplete {
		t.Fatalf("recording: %+v", rr)
	}
	if rr.Ops != res.Ops || info.Ops != res.Ops {
		t.Fatalf("the run sent %d ops, the recording holds %d", res.Ops, info.Ops)
	}
	if info.Commands["GET"]+info.Commands["SET"] != res.Ops {
		t.Fatalf("commands = %v", info.Commands)
	}
	if info.Groups < res.Ops/4-uint64(cfg.Run.Concurrency) || info.Groups > res.Ops/4+uint64(cfg.Run.Concurrency) {
		t.Fatalf("pipeline depth 4: %d groups for %d ops", info.Groups, res.Ops)
	}
}

func TestRunRecordsCompositeOperationsOnTheirConnection(t *testing.T) {
	cfg := testCfg()
	cfg.Workload.Mix = []config.MixItem{{Op: "WATCH_INCR", Weight: 1}, {Op: "SET", Weight: 1}}
	_, info, rr := recordedRun(t, newFake(), cfg, workloadrec.Options{}, workloadrec.Header{})
	if rr.Status != workloadrec.StatusComplete {
		t.Fatalf("%+v", rr)
	}
	if info.Commands["WATCH"] == 0 || info.Commands["MULTI"] != info.Commands["WATCH"] || info.Commands["EXEC"] != info.Commands["WATCH"] {
		t.Fatalf("WATCH_INCR sends WATCH, GET, MULTI, SET, EXEC: %v", info.Commands)
	}
	dedicated := 0
	_ = workloadrec.Stream(context.Background(), rr.Path, func(o *workloadrec.OpLine) error {
		if o.Conn == workloadrec.ConnDedicated {
			dedicated++
		}
		return nil
	})
	if uint64(dedicated) < info.Commands["WATCH"]*4 {
		t.Fatalf("dedicated commands = %d, want at least 4 per WATCH_INCR", dedicated)
	}
}

func TestRecordedOutcomes(t *testing.T) {
	cfg := testCfg()
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 1}}
	h := workloadrec.Header{Outcomes: true}
	_, _, rr := recordedRun(t, newFake(), cfg, workloadrec.Options{}, h)
	var miss, other int
	_ = workloadrec.Stream(context.Background(), rr.Path, func(o *workloadrec.OpLine) error {
		switch {
		case o.Outcome == nil:
			t.Fatal("outcomes were requested")
		case o.Outcome.Status == workloadrec.OutcomeMiss:
			miss++
		default:
			other++
		}
		return nil
	})
	if miss == 0 || other != 0 {
		t.Fatalf("an empty store only misses: miss=%d other=%d", miss, other)
	}

	f := newFake()
	f.failAll = true
	_, _, rr = recordedRun(t, f, cfg, workloadrec.Options{}, h)
	_ = workloadrec.Stream(context.Background(), rr.Path, func(o *workloadrec.OpLine) error {
		if o.Outcome.Status != workloadrec.OutcomeError || o.Outcome.Class == "" {
			t.Fatalf("failed commands carry their error class: %+v", o.Outcome)
		}
		return nil
	})
}

func TestOutcomeOf(t *testing.T) {
	if o := OutcomeOf(Result{Len: 3, Sum: 0xab}, 12, true); o.Status != "ok" || o.Len != 3 || o.Sum != "00000000000000ab" || o.US != 12 {
		t.Fatalf("%+v", o)
	}
	if o := OutcomeOf(Result{Len: 3, Sum: 0xab}, 12, false); o.Sum != "" || o.Len != 0 {
		t.Fatalf("no checksum unless asked: %+v", o)
	}
	if o := OutcomeOf(Result{Miss: true}, 1, true); o.Status != "miss" {
		t.Fatalf("%+v", o)
	}
	if o := OutcomeOf(Result{Err: errors.New("WRONGTYPE bad")}, 1, true); o.Status != "error" || o.Class == "" {
		t.Fatalf("%+v", o)
	}
}

// Every command a worker sends goes through Client.Exec/Query/Dedicated, the
// only place Tapped can see it, and only client.go talks to a client library.
func TestWorkersOnlyUseTheClientInterface(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"github.com/valkey-io/valkey-go", "github.com/redis/go-redis"}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "bin") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if strings.HasPrefix(p, b) && filepath.ToSlash(path) != "../../internal/valkey/client.go" {
					t.Errorf("%s imports %s: only internal/valkey/client.go may talk to a client library", path, p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
