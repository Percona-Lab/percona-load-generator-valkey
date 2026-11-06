//go:build integration

package valkey

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

func advIntegrationCfg(prefix string) config.Config {
	cfg := config.Default()
	cfg.Run.Duration = 2 * time.Second
	cfg.Run.Concurrency = 16
	cfg.Run.Seed = 7
	cfg.Target.Pool.PipelineDepth = 4
	cfg.Dataset.KeyCount = 200
	cfg.Dataset.KeyPrefix = prefix
	cfg.Dataset.CollectionSize = 10
	cfg.Dataset.StreamLength = 8
	cfg.Dataset.BitmapBits = 1024
	cfg.Output.SampleInterval = 500 * time.Millisecond
	return cfg
}

// cleanupAdv removes the keys of every namespace under a prefix (small datasets).
func cleanupAdv(t *testing.T, c Client, prefix string, n int) {
	t.Cleanup(func() {
		var cmds []Cmd
		for _, ns := range []string{"s", "c", "a", "h", "l", "t", "z", "x", "g", "p", "b"} {
			for i := 0; i < n; i++ {
				cmds = append(cmds, Cmd{"UNLINK", prefix + ns + ":" + strconv.Itoa(i)})
				if len(cmds) == 500 {
					c.Exec(context.Background(), cmds)
					cmds = cmds[:0]
				}
			}
		}
		c.Exec(context.Background(), cmds)
	})
}

// Every new operation runs against a real server on both client libraries with
// no errors, and each is actually exercised.
func TestIntegrationAdvancedOpsNoErrors(t *testing.T) {
	mix := []config.MixItem{
		{Op: "GET", Weight: 4},
		{Op: "BLPOP", Weight: 1, Timeout: "20ms"}, {Op: "BRPOP", Weight: 1, Timeout: "20ms"}, {Op: "LPUSH", Weight: 3}, {Op: "RPUSH", Weight: 3},
		{Op: "XADD", Weight: 3, Fields: 3}, {Op: "XLEN", Weight: 1}, {Op: "XRANGE", Weight: 1, Count: 4}, {Op: "XREAD", Weight: 1, Count: 4},
		{Op: "XREAD_BLOCK", Weight: 1, Timeout: "20ms", Count: 2}, {Op: "XTRIM", Weight: 1}, {Op: "XPENDING", Weight: 1}, {Op: "XREADGROUP", Weight: 1, Count: 3}, {Op: "XCONSUME", Weight: 3, Count: 3},
		{Op: "GEOADD", Weight: 1}, {Op: "GEOPOS", Weight: 1}, {Op: "GEODIST", Weight: 1}, {Op: "GEOSEARCH", Weight: 1, Count: 3},
		{Op: "PFADD", Weight: 1}, {Op: "PFCOUNT", Weight: 1}, {Op: "SETBIT", Weight: 1}, {Op: "GETBIT", Weight: 1}, {Op: "BITCOUNT", Weight: 1},
		{Op: "EVAL", Weight: 1, Script: "rate_limit"}, {Op: "EVAL", Weight: 1, Script: "incr_cap"}, {Op: "EVAL", Weight: 1, Source: "return redis.call('GET', KEYS[1])"},
		{Op: "EVALSHA", Weight: 1, Script: "get_set"}, {Op: "EVALSHA", Weight: 1, Script: "rate_limit", Args: []string{"30", "1000"}},
		{Op: "FCALL", Weight: 1, Script: "rate_limit"}, {Op: "FCALL", Weight: 1, Script: "incr_cap"}, {Op: "FCALL", Weight: 1, Script: "get_set"},
		{Op: "PUBLISH", Weight: 2},
		{Op: "TXN", Weight: 2, Steps: []config.MixItem{{Op: "SET"}, {Op: "GET"}, {Op: "HSET", Fields: 2}, {Op: "ZADD"}}},
		{Op: "PIPELINE", Weight: 2, Steps: []config.MixItem{{Op: "GET"}, {Op: "HGET"}, {Op: "SISMEMBER"}}},
		{Op: "WATCH_INCR", Weight: 2},
		{Op: "RAW", Weight: 1, Command: "SET {key} {value} EX 120"}, {Op: "RAW", Weight: 1, Command: "OBJECT FREQ {key}"},
		{Op: "RAW", Weight: 1, Command: "INFO keyspace"}, {Op: "RAW", Weight: 1, Command: `ZADD {key:zset} {int:1-100} "raw member {n}"`},
		{Op: "RAW", Weight: 1, Command: "TYPE {key:stream}"}, {Op: "RAW", Weight: 1, Command: "MEMORY USAGE {key:hash}"},
	}
	for _, lib := range []string{LibValkeyGo, LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			c := itClient(t, lib)
			cfg := advIntegrationCfg("it:adv:" + lib + ":")
			cfg.Target.Client = lib
			cfg.Workload.Mix = mix
			cfg.Workload.PubSub = config.PubSub{Subscribers: 3, Channels: 4}
			cfg.Normalize()
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			cleanupAdv(t, c, cfg.Dataset.KeyPrefix, 200)
			ctx := context.Background()
			if err := Preload(ctx, c, cfg); err != nil {
				t.Fatal(err)
			}
			if err := Prepare(ctx, c, cfg.Workload.Mix); err != nil {
				t.Fatal(err)
			}
			out, err := Run(ctx, c, cfg)
			if err != nil {
				t.Fatal(err)
			}
			// OBJECT FREQ needs an LFU policy; on the default policy the server says so. That is the
			// server's answer to a valid command, not a plgv fault, so tolerate exactly that error.
			for _, o := range out.PerOp {
				if o.Errors > 0 && o.Op != "RAW OBJECT FREQ {key}" {
					t.Fatalf("%s failed %d of %d times: %q", o.Op, o.Errors, o.Ops, out.FirstError)
				}
			}
			if out.Errors != 0 && !strings.Contains(out.FirstError, "LFU maxmemory policy") {
				t.Fatalf("ops=%d errors=%d first=%q", out.Ops, out.Errors, out.FirstError)
			}
			var idle []string
			for _, s := range out.PerOp {
				if s.Ops == 0 {
					idle = append(idle, s.Op)
				}
			}
			if len(idle) > 0 {
				t.Errorf("ops never ran: %v", idle)
			}
			ps := out.PubSub
			if ps == nil || ps.Published == 0 || ps.Delivered < ps.Expected*99/100 {
				t.Fatalf("pub/sub: %+v", ps)
			}
			if out.DeliveryHist.Count() == 0 || out.DeliveryHist.Quantile(0.99) > 2*time.Second {
				t.Fatalf("delivery latency: %d samples, p99 %v", out.DeliveryHist.Count(), out.DeliveryHist.Quantile(0.99))
			}
		})
	}
}

// WATCH really is optimistic locking: under contention some increments are
// aborted, and the counters add up to exactly the ones that committed.
func TestIntegrationWatchIncrCountsOnlyCommittedIncrements(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	cfg := advIntegrationCfg("it:wch:")
	cfg.Dataset.KeyCount = 3 // three hot counters, 16 workers: plenty of conflicts
	cfg.Dataset.Preload = false
	cfg.Target.Pool.PipelineDepth = 1
	cfg.Run.Concurrency = 16
	cfg.Workload.Mix = []config.MixItem{{Op: "WATCH_INCR", Weight: 1}}
	cleanupAdv(t, c, cfg.Dataset.KeyPrefix, 5)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		c.Exec(ctx, []Cmd{{"UNLINK", fmt.Sprintf("%sc:%d", cfg.Dataset.KeyPrefix, i)}})
	}
	out, err := Run(ctx, c, cfg)
	if err != nil || out.Errors != 0 {
		t.Fatalf("%v errors=%d %q", err, out.Errors, out.FirstError)
	}
	var sum int64
	for i := 0; i < 3; i++ {
		v, _ := c.Query(ctx, Cmd{"GET", fmt.Sprintf("%sc:%d", cfg.Dataset.KeyPrefix, i)})
		if s, ok := v.(string); ok {
			n, _ := strconv.ParseInt(s, 10, 64)
			sum += n
		}
	}
	committed := int64(out.Ops - out.Misses)
	if sum != committed {
		t.Fatalf("counters add up to %d but %d increments committed (%d conflicts of %d ops)", sum, committed, out.Misses, out.Ops)
	}
	if out.Misses == 0 {
		t.Fatalf("16 workers on 3 keys must conflict at least once in %d operations", out.Ops)
	}
}

func TestIntegrationScriptsAndFunctionsBehave(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	ctx := context.Background()
	mix := []config.MixItem{{Op: "EVALSHA", Script: "rate_limit"}, {Op: "FCALL", Script: "rate_limit"}}
	if err := Prepare(ctx, c, mix); err != nil {
		t.Fatal(err)
	}
	key := "it:scr:k"
	t.Cleanup(func() {
		c.Exec(ctx, []Cmd{{"UNLINK", key}, {"UNLINK", key + "2"}, {"UNLINK", key + "3"}, {"UNLINK", key + "4"}})
	})
	call := func(cmd Cmd) int64 {
		t.Helper()
		v, err := c.Query(ctx, cmd)
		if err != nil {
			t.Fatalf("%v: %v", cmd, err)
		}
		n, _ := v.(int64)
		return n
	}
	body := scriptBodies["rate_limit"]
	// rate_limit with a limit of 3 per 60s: three calls allowed, then refused.
	var got []int64
	for i := 0; i < 5; i++ {
		got = append(got, call(Cmd{"EVALSHA", sha(body), "1", key, "60", "3"}))
	}
	if fmt.Sprint(got) != "[1 1 1 0 0]" {
		t.Fatalf("EVALSHA rate_limit: %v", got)
	}
	if ttl := call(Cmd{"TTL", key}); ttl < 1 || ttl > 60 {
		t.Fatalf("the window is the key's TTL: %d", ttl)
	}
	got = nil
	for i := 0; i < 5; i++ {
		got = append(got, call(Cmd{"FCALL", "rate_limit", "1", key + "2", "60", "3"}))
	}
	if fmt.Sprint(got) != "[1 1 1 0 0]" {
		t.Fatalf("FCALL rate_limit must behave like the script: %v", got)
	}
	// incr_cap never goes above the cap.
	for i := 0; i < 6; i++ {
		call(Cmd{"EVAL", scriptBodies["incr_cap"], "1", key + "3", "4"})
	}
	if v, _ := c.Query(ctx, Cmd{"GET", key + "3"}); v != "4" {
		t.Fatalf("incr_cap stopped at %v", v)
	}
	// get_set returns the old value.
	c.Exec(ctx, []Cmd{{"SET", key + "4", "old"}})
	if v, _ := c.Query(ctx, Cmd{"EVAL", scriptBodies["get_set"], "1", key + "4", "new"}); v != "old" {
		t.Fatalf("%v", v)
	}
	if v, _ := c.Query(ctx, Cmd{"GET", key + "4"}); v != "new" {
		t.Fatalf("%v", v)
	}
	// An unknown script hash is reported by the server, not hidden.
	if _, err := c.Query(ctx, Cmd{"EVALSHA", strings.Repeat("0", 40), "0"}); err == nil || !strings.Contains(err.Error(), "NOSCRIPT") {
		t.Fatalf("%v", err)
	}
}

func TestIntegrationStreamsConsumerGroupsAckWhatTheyRead(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	cfg := advIntegrationCfg("it:stm:")
	cfg.Dataset.KeyCount = 20
	cfg.Dataset.StreamLength = 30
	cfg.Run.Duration = time.Second
	cfg.Workload.Mix = []config.MixItem{{Op: "XCONSUME", Weight: 1, Count: 5}}
	cleanupAdv(t, c, cfg.Dataset.KeyPrefix, 20)
	ctx := context.Background()
	if err := Preload(ctx, c, cfg); err != nil {
		t.Fatal(err)
	}
	if err := Preload(ctx, c, cfg); err != nil {
		t.Fatalf("preloading again finds the consumer groups already there: %v", err)
	}
	out, err := Run(ctx, c, cfg)
	if err != nil || out.Errors != 0 {
		t.Fatalf("%v errors=%d %q", err, out.Errors, out.FirstError)
	}
	var pending int64
	for i := 0; i < 20; i++ {
		v, err := c.Query(ctx, Cmd{"XPENDING", fmt.Sprintf("%sx:%d", cfg.Dataset.KeyPrefix, i), StreamGroup})
		if err != nil {
			t.Fatal(err)
		}
		if a, ok := v.([]any); ok && len(a) > 0 {
			n, _ := a[0].(int64)
			pending += n
		}
	}
	if pending != 0 {
		t.Fatalf("XCONSUME acknowledges every entry it reads, but %d are pending", pending)
	}
	if out.Misses == 0 {
		t.Fatalf("20 streams x 30 entries read 5 at a time are exhausted within the run, so later reads find nothing (misses %d of %d)", out.Misses, out.Ops)
	}
	// XREADGROUP without an ack leaves entries pending.
	cfg.Dataset.Preload = true
	for i := 0; i < 20; i++ {
		c.Exec(ctx, []Cmd{{"UNLINK", fmt.Sprintf("%sx:%d", cfg.Dataset.KeyPrefix, i)}})
	}
	cfg.Workload.Mix = []config.MixItem{{Op: "XREADGROUP", Weight: 1, Count: 5}}
	if err := Preload(ctx, c, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, c, cfg); err != nil {
		t.Fatal(err)
	}
	var pend2 int64
	for i := 0; i < 20; i++ {
		v, _ := c.Query(ctx, Cmd{"XPENDING", fmt.Sprintf("%sx:%d", cfg.Dataset.KeyPrefix, i), StreamGroup})
		if a, ok := v.([]any); ok && len(a) > 0 {
			n, _ := a[0].(int64)
			pend2 += n
		}
	}
	if pend2 == 0 {
		t.Fatal("XREADGROUP does not acknowledge: entries must stay pending")
	}
}

func TestIntegrationBlockingListsWaitForTheirTimeout(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	cfg := advIntegrationCfg("it:blk:")
	cfg.Dataset.KeyCount = 5
	cfg.Dataset.Preload = false
	cfg.Run.Concurrency = 4
	cfg.Run.Duration = time.Second
	cfg.Target.Pool.PipelineDepth = 1
	cfg.Workload.Mix = []config.MixItem{{Op: "BRPOP", Weight: 1, Timeout: "150ms"}}
	cleanupAdv(t, c, cfg.Dataset.KeyPrefix, 5)
	for i := 0; i < 5; i++ {
		c.Exec(context.Background(), []Cmd{{"UNLINK", fmt.Sprintf("%sl:%d", cfg.Dataset.KeyPrefix, i)}})
	}
	out, err := Run(context.Background(), c, cfg)
	if err != nil || out.Errors != 0 {
		t.Fatalf("%v errors=%d %q", err, out.Errors, out.FirstError)
	}
	if out.Misses != out.Ops {
		t.Fatalf("an empty list: every BRPOP times out (%d of %d)", out.Misses, out.Ops)
	}
	if p := out.Hist.Quantile(0.5); p < 140*time.Millisecond || p > 400*time.Millisecond {
		t.Fatalf("a blocking read's latency is its wait: p50 %v for a 150ms timeout", p)
	}
	// With producers the readers get data and do not wait.
	cfg.Workload.Mix = []config.MixItem{{Op: "BRPOP", Weight: 1, Timeout: "150ms"}, {Op: "LPUSH", Weight: 3}}
	out, err = Run(context.Background(), c, cfg)
	if err != nil || out.Errors != 0 {
		t.Fatal(err)
	}
	var brpop OpStatLike
	for _, o := range out.PerOp {
		if o.Op == "BRPOP" {
			brpop = OpStatLike{o.Ops, o.Misses}
		}
	}
	if brpop.Ops == 0 || float64(brpop.Misses)/float64(brpop.Ops) > 0.2 {
		t.Fatalf("with 3x more producers most reads find data: %+v", brpop)
	}
}

type OpStatLike struct{ Ops, Misses uint64 }

func TestIntegrationPubSubDeliveryOnBothLibraries(t *testing.T) {
	for _, lib := range []string{LibValkeyGo, LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			c := itClient(t, lib)
			cfg := advIntegrationCfg("it:pub:" + lib + ":")
			cfg.Target.Client = lib
			cfg.Workload.PubSub = config.PubSub{Subscribers: 4, Channels: 6}
			cfg.Workload.Mix = []config.MixItem{{Op: "PUBLISH", Weight: 1}}
			cfg.Dataset.Preload = false
			out, err := Run(context.Background(), c, cfg)
			if err != nil || out.Errors != 0 {
				t.Fatalf("%v errors=%d %q", err, out.Errors, out.FirstError)
			}
			ps := out.PubSub
			if ps.Published != out.Ops || ps.Expected != ps.Published*4 || ps.Delivered != ps.Expected {
				t.Fatalf("every message reaches every subscriber: %+v", ps)
			}
			if out.DeliveryHist.Count() != ps.Delivered || out.DeliveryHist.Quantile(0.5) <= 0 {
				t.Fatalf("%d latencies, p50 %v", out.DeliveryHist.Count(), out.DeliveryHist.Quantile(0.5))
			}
		})
	}
}

func TestIntegrationRawCommandsReachTheServerAsWritten(t *testing.T) {
	c := itClient(t, LibValkeyGo)
	cfg := advIntegrationCfg("it:raw:")
	cfg.Dataset.KeyCount = 10
	cfg.Dataset.Preload = false
	cfg.Run.Concurrency = 2
	cfg.Run.Duration = 500 * time.Millisecond
	cfg.Workload.Mix = []config.MixItem{{Op: "RAW", Weight: 1, Command: `SET {key} "hello world {n}" EX 300`}}
	cleanupAdv(t, c, cfg.Dataset.KeyPrefix, 10)
	out, err := Run(context.Background(), c, cfg)
	if err != nil || out.Errors != 0 {
		t.Fatalf("%v errors=%d", err, out.Errors)
	}
	v, _ := c.Query(context.Background(), Cmd{"GET", cfg.Dataset.KeyPrefix + "s:0"})
	if s, _ := v.(string); !strings.HasPrefix(s, "hello world ") {
		// Key 0 may not have been drawn; look for any.
		found := false
		for i := 0; i < 10 && !found; i++ {
			v, _ = c.Query(context.Background(), Cmd{"GET", fmt.Sprintf("%ss:%d", cfg.Dataset.KeyPrefix, i)})
			s, _ = v.(string)
			found = strings.HasPrefix(s, "hello world ")
		}
		if !found {
			t.Fatal("no key holds the value the raw command wrote (quoted text must stay one argument)")
		}
	}
	if ttl, _ := c.Query(context.Background(), Cmd{"TTL", cfg.Dataset.KeyPrefix + "s:1"}); ttl != nil {
		if n, _ := ttl.(int64); n > 300 {
			t.Fatalf("ttl %d", n)
		}
	}
}
