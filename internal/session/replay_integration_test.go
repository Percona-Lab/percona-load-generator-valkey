//go:build integration

package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func recordCfg(prefix, lib string) config.Config {
	cfg := config.Default()
	cfg.Target.URIs = []string{itAddr()}
	cfg.Target.Client = lib
	cfg.Run.Concurrency = 1 // one worker: the order of commands is the order of the recording
	cfg.Run.Duration = time.Second
	cfg.Run.TargetOpsPerSec = 2000
	cfg.Run.Seed = 42
	cfg.Dataset.KeyCount = 300
	cfg.Dataset.KeyPrefix = prefix
	cfg.Dataset.ValueSize = 32
	cfg.Dataset.CollectionSize = 10
	cfg.Workload.Mix = []config.MixItem{
		{Op: "GET", Weight: 4}, {Op: "SET", Weight: 3}, {Op: "INCR", Weight: 1}, {Op: "HSET", Weight: 2}, {Op: "HGETALL", Weight: 1},
		{Op: "LPUSH", Weight: 1}, {Op: "LRANGE", Weight: 1}, {Op: "ZADD", Weight: 1}, {Op: "ZRANGE", Weight: 1},
		{Op: "TXN", Weight: 1, Steps: []config.MixItem{{Op: "SET", Weight: 1}, {Op: "INCR", Weight: 1}}},
		{Op: "WATCH_INCR", Weight: 1},
	}
	cfg.Normalize()
	return cfg
}

// resetData brings the prefix back to the state a fresh preload gives.
func resetData(t *testing.T, cfg config.Config, prefix string) {
	t.Helper()
	deleteKeys(t, prefix)
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := valkey.Preload(context.Background(), c, cfg); err != nil {
		t.Fatal(err)
	}
}

// contentDigest hashes every key under the prefix with its serialized value.
func contentDigest(t *testing.T, prefix string) string {
	t.Helper()
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var lines []string
	cursor := "0"
	for {
		r, err := c.Query(context.Background(), valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
		arr, ok := r.([]any)
		if err != nil || !ok || len(arr) != 2 {
			t.Fatalf("scan: %v %v", err, r)
		}
		for _, k := range arr[1].([]any) {
			v, err := c.Query(context.Background(), valkey.Cmd{"DUMP", fmt.Sprint(k)})
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprint(k)+"="+fmt.Sprint(v))
		}
		if cursor = fmt.Sprint(arr[0]); cursor == "0" {
			break
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return fmt.Sprintf("%d keys %x", len(lines), sum[:8])
}

func deleteKeys(t *testing.T, prefix string) {
	t.Helper()
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addr: itAddr(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cursor := "0"
	for {
		r, err := c.Query(context.Background(), valkey.Cmd{"SCAN", cursor, "MATCH", prefix + "*", "COUNT", "1000"})
		arr, ok := r.([]any)
		if err != nil || !ok || len(arr) != 2 {
			t.Fatalf("scan: %v %v", err, r)
		}
		var cmds []valkey.Cmd
		for _, k := range arr[1].([]any) {
			cmds = append(cmds, valkey.Cmd{"UNLINK", fmt.Sprint(k)})
		}
		if len(cmds) > 0 {
			c.Exec(context.Background(), cmds)
		}
		if cursor = fmt.Sprint(arr[0]); cursor == "0" {
			return
		}
	}
}

// Record a real run, restore the starting data, replay it, and get the same
// commands, the same statuses and the same replies (checksums), with both
// client libraries.
func TestIntegrationRecordThenReplay(t *testing.T) {
	for _, lib := range []string{valkey.LibValkeyGo, valkey.LibGoRedis} {
		t.Run(lib, func(t *testing.T) {
			prefix := fmt.Sprintf("plgv-rr-%s-%d:", lib, time.Now().UnixNano())
			t.Cleanup(func() { deleteKeys(t, prefix) })
			cfg := recordCfg(prefix, lib)
			path := filepath.Join(t.TempDir(), "run.jsonl.zst")
			cfg.Record = config.Record{Path: path, Compression: "auto", Backpressure: "block", Outcomes: true, Checksums: true, DataFingerprint: true}

			out, err := Run(context.Background(), cfg, Options{Version: "it"}, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			rec := out.Recording
			if rec == nil || rec.Status != workloadrec.StatusComplete || rec.Ops == 0 || out.Result.Errors != 0 {
				t.Fatalf("recording = %+v, errors = %d", rec, out.Result.Errors)
			}
			if rec.Compression != "zstd" {
				t.Fatalf("compression = %s, want zstd from the .zst extension", rec.Compression)
			}
			info, err := workloadrec.Scan(path)
			if err != nil || info.Header.DataFingerprint == nil || info.Header.DataFingerprint.DBSize == 0 || info.Header.ServerVersion == "" {
				t.Fatalf("header = %+v, err = %v", info.Header, err)
			}
			if info.Commands["WATCH"] == 0 || info.Commands["MULTI"] == 0 {
				t.Fatalf("composite operations must be recorded: %v", info.Commands)
			}

			// The recorded run changed the data: a replay on it is flagged.
			dirty, err := Replay(context.Background(), recordCfg(prefix, lib), path, replay.Options{Speed: 0, ReadsOnly: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if dirty.Data == nil || dirty.Data.Match {
				t.Fatalf("data changed by the run must be reported: %+v", dirty.Data)
			}

			resetData(t, cfg, prefix)
			ro, err := Replay(context.Background(), recordCfg(prefix, lib), path, replay.Options{Speed: 0}, true)
			if err != nil {
				t.Fatal(err)
			}
			r := ro.Result
			if !ro.Data.Match || r.Errors != 0 {
				t.Fatalf("data %+v, errors %d (%s)", ro.Data, r.Errors, r.FirstError)
			}
			if !r.HashMatches || r.Ops != info.Ops {
				t.Fatalf("replayed %d of %d commands, hash matches %v", r.Ops, info.Ops, r.HashMatches)
			}
			o := r.Outcomes
			if o == nil || o.Compared != info.Ops || o.StatusDiffs != 0 {
				t.Fatalf("outcomes = %+v", o)
			}
			if o.ChecksumDiffs != 0 || o.ChecksumCompared == 0 {
				t.Fatalf("reply checksums: %+v", o)
			}
			// TIME-based or random replies would differ; this mix has none, so the replay is exact.
		})
	}
}

// A reads-only replay must leave the data exactly as it was.
func TestIntegrationReadsOnlyReplayLeavesDataUntouched(t *testing.T) {
	prefix := fmt.Sprintf("plgv-rr-ro-%d:", time.Now().UnixNano())
	t.Cleanup(func() { deleteKeys(t, prefix) })
	cfg := recordCfg(prefix, valkey.LibValkeyGo)
	path := filepath.Join(t.TempDir(), "ro.jsonl")
	cfg.Record = config.Record{Path: path, Compression: "none", Backpressure: "fail", DataFingerprint: true}
	if _, err := Run(context.Background(), cfg, Options{}, Hooks{}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string { return contentDigest(t, prefix) }
	before := snapshot()
	out, err := Replay(context.Background(), recordCfg(prefix, valkey.LibValkeyGo), path, replay.Options{Speed: 0, ReadsOnly: true, Iterations: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Skipped == 0 || out.Result.Ops == 0 || len(out.Result.Iterations) != 2 {
		t.Fatalf("%+v", out.Result)
	}
	if after := snapshot(); after != before {
		t.Fatalf("a reads-only replay changed the data: %s -> %s", before, after)
	}
}
