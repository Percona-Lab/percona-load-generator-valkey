package servermetrics

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func TestParseInfoRealFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/info_valkey_9.txt")
	if err != nil {
		t.Fatal(err)
	}
	in := ParseInfo(string(b))
	if v := in.Version(); !strings.HasPrefix(v, "9.") {
		t.Fatalf("version %q (must come from valkey_version, not redis_version %q)", v, in.Str("redis_version"))
	}
	if in.Str("redis_version") != "7.2.4" {
		t.Fatalf("fixture lost the compat redis_version: %q", in.Str("redis_version"))
	}
	if in.Str("role") != "master" || in.Int("used_memory") <= 0 || !in.Has("total_commands_processed") {
		t.Fatalf("basic fields missing")
	}
	if in.Int("nope") != 0 || in.Float("nope") != 0 || in.Has("nope") {
		t.Fatal("missing keys must be zero")
	}
}

func TestParseInfoErrorstatsAndReplicas(t *testing.T) {
	in := ParseInfo("# Errorstats\r\nerrorstat_OOM:count=3,rejected_calls=3,failed_calls=0\r\nerrorstat_ERR:count=1,rejected_calls=0,failed_calls=1\r\n" +
		"# Replication\r\nrole:master\r\nmaster_repl_offset:1000\r\n" +
		"slave0:ip=10.0.0.2,port=6379,state=online,offset=900,lag=0\r\nslave1:ip=10.0.0.3,port=6379,state=online,offset=400,lag=2\r\n")
	if in.Errors["OOM"] != 3 || in.Errors["ERR"] != 1 {
		t.Fatalf("errorstats %v", in.Errors)
	}
	r := in.Replicas()
	if len(r) != 2 || r[1].Offset != 400 || r[1].Lag != 2 || r[0].State != "online" {
		t.Fatalf("replicas %+v", r)
	}
	if got := in.ReplicaLagBytes(); got != 600 {
		t.Fatalf("lag %d", got)
	}
}

func TestParseSlowlogAndLatency(t *testing.T) {
	reply := []any{
		[]any{int64(7), int64(1700000000), int64(15000), []any{"HGETALL", "k"}, "1.2.3.4:5", ""},
		[]any{int64(6), int64(1699999999), int64(500), []any{"SET", strings.Repeat("x", 500)}, "1.2.3.4:5", "name"},
		"garbage", []any{int64(1)},
	}
	got := ParseSlowlog(reply)
	if len(got) != 2 || got[0].ID != 7 || got[0].Duration != 15*time.Millisecond || got[0].Command != "HGETALL k" || got[0].Client != "1.2.3.4:5" {
		t.Fatalf("%+v", got)
	}
	if len(got[1].Command) > maxCommandChars+3 {
		t.Fatalf("command not truncated: %d", len(got[1].Command))
	}
	ev := ParseLatencyLatest([]any{[]any{"fork", int64(1700000000), int64(120), int64(300)}})
	if len(ev) != 1 || ev[0].Name != "fork" || ev[0].MaxMs != 300 {
		t.Fatalf("%+v", ev)
	}
	if ParseSlowlog(nil) != nil || ParseLatencyLatest("x") != nil {
		t.Fatal("bad input must give nil")
	}
}

// fakeServer answers INFO, SLOWLOG and LATENCY with scripted, advancing values.
type fakeServer struct {
	mu       sync.Mutex
	cmds     int64 // total_commands_processed
	hits     int64
	misses   int64
	evicted  int64
	slowID   int64
	slow     []any
	failWhat string
	clients  int
}

func (f *fakeServer) Query(_ context.Context, cmd valkey.Cmd) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cmd[0] == f.failWhat {
		return nil, fmt.Errorf("NOPERM no permission for %s", cmd[0])
	}
	switch cmd[0] {
	case "INFO":
		// The server counts commands after executing them; the INFO reply does not include itself.
		text := fmt.Sprintf("valkey_version:9.1.3\r\nrole:master\r\nserver_mode:standalone\r\nused_memory:%d\r\nmaxmemory:1000\r\nmaxmemory_policy:noeviction\r\n"+
			"total_commands_processed:%d\r\nkeyspace_hits:%d\r\nkeyspace_misses:%d\r\nevicted_keys:%d\r\nconnected_clients:%d\r\nblocked_clients:2\r\n"+
			"mem_fragmentation_ratio:1.1\r\nlatest_fork_usec:0\r\nrdb_last_bgsave_status:ok\r\n", 500+f.cmds/100, f.cmds, f.hits, f.misses, f.evicted, f.clients)
		f.cmds++ // the INFO itself
		return text, nil
	case "SLOWLOG":
		f.cmds++
		return append([]any(nil), f.slow...), nil
	case "LATENCY":
		f.cmds++
		return []any{[]any{"fork", int64(1), int64(5), int64(50)}}, nil
	}
	return nil, nil
}

// load simulates workload commands hitting the server between polls.
func (f *fakeServer) load(n, hits, misses int64) {
	f.mu.Lock()
	f.cmds += n
	f.hits += hits
	f.misses += misses
	f.mu.Unlock()
}

func (f *fakeServer) addSlow(id int64, micros int64) {
	f.mu.Lock()
	f.slow = append([]any{[]any{id, int64(1700000000), micros, []any{"HGETALL", "big"}, "c:1", ""}}, f.slow...)
	f.mu.Unlock()
}

func TestPollerSummaryOpsAndDeltas(t *testing.T) {
	f := &fakeServer{clients: 4}
	f.addSlow(1, 99) // pre-existing slowlog entry: must not count
	p := New(f, Config{Interval: time.Hour, Slowlog: true, Latency: true})
	p.start = time.Now()
	t0 := p.start
	p.poll(context.Background(), t0)
	f.load(1000, 900, 100)
	f.evicted = 7
	f.addSlow(2, 20000)
	p.poll(context.Background(), t0.Add(time.Second))
	f.load(500, 500, 0)
	f.addSlow(3, 5000)
	p.poll(context.Background(), t0.Add(2*time.Second))

	s := p.Summary()
	if s.Version != "9.1.3" || s.Role != "master" || s.Mode != "standalone" || s.MaxMemory != 1000 {
		t.Fatalf("%+v", s)
	}
	if s.ServerOps != 1500 {
		t.Fatalf("ServerOps %d, want exactly the 1500 workload commands (admin commands subtracted)", s.ServerOps)
	}
	if s.KeyspaceHits != 1400 || s.KeyspaceMisses != 100 || s.HitRatio < 0.93 || s.HitRatio > 0.94 {
		t.Fatalf("hits %d misses %d ratio %v", s.KeyspaceHits, s.KeyspaceMisses, s.HitRatio)
	}
	if s.EvictedKeys != 7 || s.MaxBlockedConns != 2 {
		t.Fatalf("evicted %d blocked %d", s.EvictedKeys, s.MaxBlockedConns)
	}
	if s.SlowlogNew != 2 || s.SlowlogWorst != 20*time.Millisecond || len(s.SlowlogSample) != 2 {
		t.Fatalf("slowlog new=%d worst=%v sample=%d", s.SlowlogNew, s.SlowlogWorst, len(s.SlowlogSample))
	}
	if len(s.LatencyEvents) != 1 || s.LatencyEvents[0].Name != "fork" {
		t.Fatalf("latency %+v", s.LatencyEvents)
	}
	if len(s.Samples) != 3 {
		t.Fatalf("%d samples", len(s.Samples))
	}
	if ops := s.Samples[1].OpsPerSec; ops < 999 || ops > 1001 {
		t.Fatalf("interval ops/s %v, want 1000", ops)
	}
	if s.Samples[1].HitRatio < 0.89 || s.Samples[1].HitRatio > 0.91 || s.Samples[0].HitRatio != -1 {
		t.Fatalf("hit ratios %v %v", s.Samples[0].HitRatio, s.Samples[1].HitRatio)
	}
}

func TestSlowlogIgnoresThePollersOwnCommands(t *testing.T) {
	f := &fakeServer{}
	p := New(f, Config{Interval: time.Hour, Slowlog: true, SlowlogEntries: 128})
	p.start = time.Now()
	p.poll(context.Background(), p.start) // baseline
	mk := func(id int64, args ...any) {
		f.mu.Lock()
		f.slow = append([]any{[]any{id, int64(1700000000), int64(900), args, "c:1", ""}}, f.slow...)
		f.mu.Unlock()
	}
	mk(1, "INFO", "everything")
	mk(2, "SLOWLOG", "GET", "128")
	mk(3, "LATENCY", "LATEST")
	mk(4, "HGETALL", "big")
	mk(5, "INFO", "memory") // someone else's INFO is kept
	p.poll(context.Background(), p.start.Add(time.Second))
	s := p.Summary()
	if s.SlowlogNew != 2 {
		t.Fatalf("SlowlogNew %d, want only HGETALL and the foreign INFO", s.SlowlogNew)
	}
	seen := map[string]bool{s.SlowlogSample[0].Command: true, s.SlowlogSample[1].Command: true}
	if !seen["HGETALL big"] || !seen["INFO memory"] {
		t.Fatalf("%v", seen)
	}
}

func TestPollerDisablesDeniedSourcesAndKeepsGoing(t *testing.T) {
	f := &fakeServer{failWhat: "SLOWLOG"}
	p := New(f, Config{Interval: time.Hour, Slowlog: true, Latency: true})
	p.start = time.Now()
	for i := 0; i < 3; i++ {
		p.poll(context.Background(), p.start.Add(time.Duration(i)*time.Second))
	}
	s := p.Summary()
	if s.PollErrors != 1 || !strings.Contains(s.FirstError, "SLOWLOG") {
		t.Fatalf("poll errors %d %q (a denied source is tried once, then skipped)", s.PollErrors, s.FirstError)
	}
	if s.Version == "" || len(s.Samples) != 3 {
		t.Fatal("INFO must keep working")
	}
}

func TestPollerInfoDeniedGivesEmptySummary(t *testing.T) {
	f := &fakeServer{failWhat: "INFO"}
	p := New(f, Config{Interval: time.Hour})
	p.start = time.Now()
	p.poll(context.Background(), p.start)
	if s := p.Summary(); s.Version != "" || s.PollErrors != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestPollerRunStopsAndTakesFinalPoll(t *testing.T) {
	f := &fakeServer{}
	p := New(f, Config{Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(55 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if n := len(p.Summary().Samples); n < 4 {
		t.Fatalf("%d samples", n)
	}
}

func TestSampleSeriesBounded(t *testing.T) {
	f := &fakeServer{}
	p := New(f, Config{Interval: time.Hour})
	p.start = time.Now()
	for i := 0; i < 20000; i++ {
		f.load(100, 0, 0)
		p.poll(context.Background(), p.start.Add(time.Duration(i)*time.Second))
	}
	s := p.Summary()
	if len(s.Samples) > MaxSamples+1 {
		t.Fatalf("%d samples stored", len(s.Samples))
	}
	if s.SampleStride < 8 {
		t.Fatalf("stride %d", s.SampleStride)
	}
	for _, x := range s.Samples[1 : len(s.Samples)-1] {
		if x.OpsPerSec < 90 || x.OpsPerSec > 110 {
			t.Fatalf("averaged ops/s %v", x.OpsPerSec)
		}
	}
}
