//go:build integration

package session

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/trafficimport"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func detConfig(prefix string) config.Config {
	cfg := recordCfg(prefix, valkey.LibValkeyGo)
	cfg.Run.TargetOpsPerSec = 0
	cfg.Run.Duration = time.Hour // ignored: the budget ends the run
	cfg.Run.OpBudget = 4000
	cfg.Run.Deterministic = true
	cfg.Workload.Mix = []config.MixItem{{Op: "GET", Weight: 4}, {Op: "SET", Weight: 3}, {Op: "HSET", Weight: 2}, {Op: "HGETALL", Weight: 1},
		{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET", Weight: 1}, {Op: "INCR", Weight: 1}}}}
	return cfg
}

// The same seed and op budget send the same commands on a real server, and
// the recording of such a run carries the same fingerprint as its trailer hash.
func TestIntegrationDeterministicRunFingerprint(t *testing.T) {
	prefix := fmt.Sprintf("plgv-det-%d:", time.Now().UnixNano())
	t.Cleanup(func() { deleteKeys(t, prefix) })
	cfg := detConfig(prefix)
	cfg.Run.Concurrency = 4
	path := filepath.Join(t.TempDir(), "det.jsonl")
	cfg.Record = config.Record{Path: path, Compression: "none", Backpressure: "block"}
	a, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Record = config.Record{Backpressure: "fail", Compression: "auto"}
	cfg.Run.TargetOpsPerSec = 50000 // other timing, same traffic
	b, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Result.Ops != 4000 || b.Result.Ops != 4000 {
		t.Fatalf("ops %d and %d, want exactly 4000", a.Result.Ops, b.Result.Ops)
	}
	if a.Summary.Traffic == nil || a.Summary.Traffic.Hash == "" || a.Summary.Traffic.Hash != b.Summary.Traffic.Hash {
		t.Fatalf("%+v vs %+v", a.Summary.Traffic, b.Summary.Traffic)
	}
	if a.Recording.CommandsHash != a.Summary.Traffic.Hash {
		t.Fatalf("recording hash %s, fingerprint %s", a.Recording.CommandsHash, a.Summary.Traffic.Hash)
	}
}

// Replay on several targets: one is down, the others still run and are compared.
func TestIntegrationMultiTargetReplayAndCompare(t *testing.T) {
	prefix := fmt.Sprintf("plgv-mt-%d:", time.Now().UnixNano())
	t.Cleanup(func() { deleteKeys(t, prefix) })
	cfg := detConfig(prefix)
	cfg.Run.Concurrency = 1
	path := filepath.Join(t.TempDir(), "mt.jsonl")
	cfg.Record = config.Record{Path: path, Compression: "none", Backpressure: "block", Outcomes: true, DataFingerprint: true}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatal(err)
	}
	resetData(t, cfg, prefix)

	base := config.Default()
	targets := []multitarget.Target{{Name: "first", Addr: itAddr()}, {Name: "down", Addr: "127.0.0.1:1"}, {Name: "second", Addr: itAddr()}}
	rs := ReplayMany(context.Background(), base, targets, path, replay.Options{Speed: 0}, false, false, "it")
	if len(rs) != 3 || rs[0].Err != nil || rs[1].Err == nil || rs[2].Summary == nil {
		t.Fatalf("%+v", rs)
	}
	if rs[1].Summary.Failure == "" || rs[1].Summary.Recording.CommandsHash == "" {
		t.Fatalf("a failed target still says what it was to replay: %+v", rs[1].Summary)
	}
	if rs[0].Summary.ServerVersion == "" || !rs[0].Summary.HashMatches || rs[0].Summary.Errors != 0 {
		t.Fatalf("%+v", rs[0].Summary)
	}
	// The second replay starts from the data the first one changed: reported.
	if rs[2].Summary.Data == nil || rs[2].Summary.Data.Match {
		t.Fatalf("data check on the second target: %+v", rs[2].Summary.Data)
	}
	sums := []*replay.Summary{rs[0].Summary, rs[1].Summary, rs[2].Summary}
	comps := replaycompare.CompareAll(sums, replaycompare.Options{})
	if len(comps) != 2 || comps[0].Comparable || !comps[1].Comparable {
		t.Fatalf("%+v", comps)
	}

	// Concurrent replay (reads-only, so the two cannot disturb each other).
	rs = ReplayMany(context.Background(), base, []multitarget.Target{{Name: "x", Addr: itAddr()}, {Name: "y", Addr: itAddr()}}, path,
		replay.Options{Speed: 0, ReadsOnly: true}, true, false, "it")
	for _, r := range rs {
		if r.Err != nil || r.Summary.Ops == 0 || r.Summary.Skipped == 0 {
			t.Fatalf("%+v %v", r.Summary, r.Err)
		}
	}
}

// Capture traffic with MONITOR, import it, and replay it reads-only.
func TestIntegrationImportLiveMonitorTraffic(t *testing.T) {
	prefix := fmt.Sprintf("plgv-imp-%d:", time.Now().UnixNano())
	t.Cleanup(func() { deleteKeys(t, prefix) })
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // traffic from two connections, with a credential-looking value
		for i := 0; ctx.Err() == nil; i++ {
			c.Exec(ctx, []valkey.Cmd{{"SET", fmt.Sprintf("%sk%d", prefix, i%50), fmt.Sprintf("v%d", i)}, {"GET", fmt.Sprintf("%sk%d", prefix, i%50)}})
			time.Sleep(2 * time.Millisecond)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	var buf bytes.Buffer
	n, err := trafficimport.Capture(context.Background(), itAddr(), trafficimport.CaptureOptions{MaxCommands: 200, MaxDuration: 5 * time.Second}, &buf)
	if err != nil || n != 200 {
		t.Fatalf("captured %d: %v", n, err)
	}
	cancel()
	entries, _, err := trafficimport.ParseMonitor(&buf)
	if err != nil || len(entries) != 200 {
		t.Fatalf("%d entries: %v", len(entries), err)
	}
	res := trafficimport.Convert(entries, []string{"monitor"}, nil, trafficimport.Options{})
	if res.Info.Converted == 0 {
		t.Fatalf("%+v", res.Info)
	}
	path := filepath.Join(t.TempDir(), "imp.jsonl.zst")
	if _, err := trafficimport.Write(path, res, "it", workloadrec.CompressionZstd); err != nil {
		t.Fatal(err)
	}
	out, err := Replay(context.Background(), config.Config{Target: recordCfg(prefix, valkey.LibValkeyGo).Target, Record: config.Default().Record}, path,
		replay.Options{Speed: 0, ReadsOnly: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if r.Ops == 0 || r.Errors != 0 || out.Data != nil || !r.Header.Outcomes && r.Outcomes != nil {
		t.Fatalf("%+v data %+v", r, out.Data)
	}
}
