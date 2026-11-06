//go:build integration_sentinel

package session

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func sentinelAddrs() []string {
	a := os.Getenv("PLGV_IT_SENTINEL_ADDRS")
	if a == "" {
		a = "127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381"
	}
	return strings.Split(a, ",")
}

func sentinelCfg(prefix string) config.Config {
	cfg := config.Default()
	cfg.Target.Mode = "sentinel"
	cfg.Target.URIs = sentinelAddrs()
	cfg.Target.Sentinel.MasterName = "mymaster"
	cfg.Run.Duration = 3 * time.Second
	cfg.Run.Concurrency = 8
	cfg.Dataset.KeyCount = 2000
	cfg.Dataset.KeyPrefix = prefix
	cfg.Output.SampleInterval = 250 * time.Millisecond
	cfg.Server.Enabled = true
	cfg.Server.Interval = 250 * time.Millisecond
	return cfg
}

func sentinelClient(t *testing.T) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: sentinelAddrs()[0], Workers: 1})
	if err != nil {
		t.Fatalf("cannot reach the sentinels (make valkey-sentinel-up): %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func masterAddr(t *testing.T, c valkey.Client) string {
	t.Helper()
	r, err := c.Query(context.Background(), valkey.Cmd{"SENTINEL", "GET-MASTER-ADDR-BY-NAME", "mymaster"})
	if err != nil {
		t.Fatal(err)
	}
	p := r.([]any)
	return p[0].(string) + ":" + p[1].(string)
}

// restoreSentinel makes 6380 the master again after a test failed over.
func restoreSentinel(t *testing.T) {
	t.Helper()
	c := sentinelClient(t)
	for i := 0; i < 8; i++ {
		if masterAddr(t, c) == "127.0.0.1:6380" {
			return
		}
		_, _ = c.Query(context.Background(), valkey.Cmd{"SENTINEL", "FAILOVER", "mymaster"})
		time.Sleep(4 * time.Second)
	}
	t.Fatalf("could not restore the master, it is %s", masterAddr(t, c))
}

func cleanupSentinelKeys(t *testing.T, prefix string) {
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: sentinelAddrs(), SentinelMaster: "mymaster", Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 2000; i++ {
			c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", prefix + "s:" + strconv.Itoa(i)}})
		}
	})
}

func TestSentinelRunFollowsTheMaster(t *testing.T) {
	restoreSentinel(t)
	cfg := sentinelCfg("it:sn:")
	cleanupSentinelKeys(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Errors != 0 || out.Result.Mode != "sentinel" || out.Summary.Client.TargetMode != "sentinel" {
		t.Fatalf("errors %d (%s) mode %q", out.Result.Errors, out.Result.FirstError, out.Result.Mode)
	}
	srv := out.Summary.Server
	if srv == nil || srv.Mode != "sentinel" || srv.Topology == nil || srv.Topology.Mode != "sentinel" {
		t.Fatalf("%+v", srv)
	}
	if len(srv.Topology.Shards) != 1 || srv.Topology.Shards[0].Primary != "127.0.0.1:6380" || len(srv.Topology.Events) != 0 {
		t.Fatalf("%+v", srv.Topology)
	}
	if d := float64(srv.ServerOps-int64(out.Result.Ops)) / float64(out.Result.Ops); d > 0.03 || d < -0.03 {
		t.Fatalf("plgv sent %d, the master counted %d", out.Result.Ops, srv.ServerOps)
	}
}

func TestSentinelReplicaReads(t *testing.T) {
	restoreSentinel(t)
	cfg := sentinelCfg("it:snr:")
	cfg.Target.ReadFromReplicas = true
	cleanupSentinelKeys(t, cfg.Dataset.KeyPrefix)
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil || out.Result.Errors != 0 {
		t.Fatalf("%v errors %d (%s)", err, out.Result.Errors, out.Result.FirstError)
	}
}

func TestSentinelForcedFailoverDuringARun(t *testing.T) {
	restoreSentinel(t)
	t.Cleanup(func() { restoreSentinel(t) })
	cfg := sentinelCfg("it:snf:")
	cfg.Run.Duration = 12 * time.Second
	cleanupSentinelKeys(t, cfg.Dataset.KeyPrefix)
	sc := sentinelClient(t)
	go func() {
		time.Sleep(3 * time.Second)
		_, _ = sc.Query(context.Background(), valkey.Cmd{"SENTINEL", "FAILOVER", "mymaster"})
	}()
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	for _, e := range out.Summary.Server.Topology.Events {
		if e.Kind == "failover" && strings.Contains(e.Detail, "6381") {
			got = true
		}
	}
	if !got {
		t.Fatalf("events %+v", out.Summary.Server.Topology.Events)
	}
	has := false
	for _, f := range out.Summary.Findings {
		if f.ID == "failover-during-run" {
			has = true
		}
	}
	if !has {
		t.Error("failover finding missing")
	}
	pts := out.Summary.Series
	var late float64
	for _, p := range pts[len(pts)-4:] {
		late += p.OpsPerSec
	}
	if late == 0 {
		t.Fatal("the workload must keep running on the new master")
	}
}

func TestSentinelAdvancedOperations(t *testing.T) {
	restoreSentinel(t)
	cfg := sentinelCfg("it:sna:")
	cfg.Dataset.StreamLength = 5
	cfg.Workload.Mix = []config.MixItem{
		{Op: "GET", Weight: 3}, {Op: "TXN", Weight: 2, Steps: []config.MixItem{{Op: "SET"}, {Op: "GET"}}}, {Op: "WATCH_INCR", Weight: 2},
		{Op: "XADD", Weight: 2}, {Op: "XCONSUME", Weight: 2}, {Op: "EVALSHA", Weight: 1, Script: "rate_limit"}, {Op: "FCALL", Weight: 1, Script: "incr_cap"},
		{Op: "PUBLISH", Weight: 2}, {Op: "BRPOP", Weight: 1, Timeout: "20ms"}, {Op: "LPUSH", Weight: 2},
	}
	cfg.Workload.PubSub = config.PubSub{Subscribers: 2, Channels: 3}
	cfg.Server.Enabled = false
	t.Cleanup(func() {
		c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: sentinelAddrs(), SentinelMaster: "mymaster", Workers: 1})
		if err != nil {
			return
		}
		defer c.Close()
		for _, ns := range []string{"s", "c", "l", "x"} {
			for i := 0; i < 2000; i++ {
				c.Exec(context.Background(), []valkey.Cmd{{"UNLINK", cfg.Dataset.KeyPrefix + ns + ":" + strconv.Itoa(i)}})
			}
		}
	})
	out, err := Run(context.Background(), cfg, Options{}, Hooks{})
	if err != nil || out.Result.Errors != 0 {
		t.Fatalf("%v errors=%d %q", err, out.Result.Errors, out.Result.FirstError)
	}
	if ps := out.Result.PubSub; ps == nil || ps.Delivered < ps.Expected*99/100 {
		t.Fatalf("%+v", ps)
	}
}
