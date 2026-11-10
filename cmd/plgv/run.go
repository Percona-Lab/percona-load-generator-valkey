package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/insights"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	sweepapi "github.com/Percona-Lab/percona-load-generator-valkey/internal/sweep"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// loadConfig builds the effective configuration: defaults, then the YAML file
// (-config), then environment, then any flags the user set explicitly.
func loadConfig(path, preset string, apply func(*config.Config)) (config.Config, error) {
	var b []byte
	if path != "" {
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return config.Config{}, err
		}
	}
	cfg, err := config.ParseWithPreset(b, preset)
	if err != nil {
		return cfg, err
	}
	if err := cfg.ApplyEnvStrict(os.Getenv); err != nil {
		return cfg, err
	}
	apply(&cfg)
	cfg.Normalize()
	return cfg, cfg.Validate()
}

// presetsCmd implements `plgv presets`: list the built-in presets.
func presetsCmd(stdout io.Writer) int {
	for _, p := range config.Presets() {
		fmt.Fprintf(stdout, "%-14s %s\n", p.Name, p.Description)
	}
	fmt.Fprintln(stdout, "\nUse one with: plgv run -preset <name>   (or workload.preset in a config file)")
	return 0
}

// runCmd implements `plgv run`. It returns the process exit code.
func runCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		path, addr, lib, dist string
		workers, pipeline     int
		readPct, valueSize    int
		keys, seed            int64
		zipfS                 float64
		duration              time.Duration
		prefix, summaryFile   string
		preload, srvMetrics   bool
		srvConfig             bool
		srvConfigName         string
		keyspace              bool
		keyspaceSamples       int
		targetOps             float64
		co                    bool
		sampleEvery           time.Duration
		loop, sweepWorkers    string
		mode, sentinelMaster  string
		rawFile, preset       string
		allowDangerous        bool
		subscribers, channels int
		replicaReads          bool
		hashTags              int
		sweepRates            string
		sweepPause            time.Duration
		recPath, recCompress  string
		recBackpressure       string
		recMaxMB              int
		recOutcomes, recSums  bool
		recFingerprint        bool
		opBudget              int64
		preloadOnly           bool
		tf                    tlsFlags
		deterministic         bool
	)
	fs.StringVar(&path, "config", "", "YAML config file (flags below override it)")
	fs.StringVar(&addr, "addr", "", "Valkey host:port, or several comma-separated seed nodes / sentinels (default 127.0.0.1:6379; env PLGV_URI)")
	fs.StringVar(&lib, "client", "", "client library: valkey-go or go-redis")
	fs.IntVar(&workers, "workers", 0, "concurrent workers (run.concurrency, default 64)")
	fs.DurationVar(&duration, "duration", 0, "run duration (default 10s)")
	fs.IntVar(&pipeline, "pipeline", 0, "commands per pipelined batch (default 1)")
	fs.IntVar(&readPct, "read-pct", 0, "replace the mix with GET/SET at this percent GET (default mix 80/20)")
	fs.Int64Var(&keys, "keys", 0, "keys per type (default 100000)")
	fs.StringVar(&dist, "dist", "", "key distribution: uniform or zipf")
	fs.Float64Var(&zipfS, "zipf-s", 0, "zipf skew, must be > 1")
	fs.IntVar(&valueSize, "value-size", 0, "value size in bytes (default 256)")
	fs.StringVar(&prefix, "prefix", "", "key prefix (default plgv:)")
	fs.Int64Var(&seed, "seed", 0, "random seed; 0 picks one and prints it (default config seed is 1)")
	fs.BoolVar(&keyspace, "keyspace", false, "after the run, sample the key space: types, sizes, expiry and the biggest keys (read-only)")
	fs.IntVar(&keyspaceSamples, "keyspace-samples", 0, "keys to inspect for -keyspace (default 10000)")
	fs.BoolVar(&srvMetrics, "server-metrics", false, "poll INFO, SLOWLOG and LATENCY from the server and report them (read-only)")
	fs.BoolVar(&srvConfig, "server-config", false, "record the server's settings (CONFIG GET *) at the start and end of the run (read-only)")
	fs.StringVar(&srvConfigName, "server-config-name", "", "a label for this server setup, stored with the run (implies -server-config)")
	fs.Float64Var(&targetOps, "target-ops", 0, "pace the run to this many ops/s in total (0 = as fast as possible)")
	fs.BoolVar(&co, "co", false, "measure latency from each batch's intended start (needs -target-ops)")
	fs.DurationVar(&sampleEvery, "sample-interval", 0, "time-series slice length (default 1s)")
	fs.StringVar(&preset, "preset", "", "start from a built-in preset ("+strings.Join(config.PresetNames(), ", ")+"); the config file and flags override it")
	fs.StringVar(&rawFile, "raw-file", "", "run the commands in this file instead of the mix: one per line, optional weight first (\"5 GET {key}\"); see docs/raw-injector.md")
	fs.BoolVar(&allowDangerous, "allow-dangerous", false, "let raw commands use commands blocked by default (FLUSHALL, DEBUG, CONFIG SET, ...)")
	fs.IntVar(&subscribers, "subscribers", 0, "pub/sub subscribers for PUBLISH operations (measures delivery latency)")
	fs.IntVar(&channels, "channels", 0, "pub/sub channels published and subscribed to (default 16)")
	fs.StringVar(&mode, "mode", "", "auto (default), standalone, cluster or sentinel")
	fs.BoolVar(&replicaReads, "replica-reads", false, "cluster or sentinel: send read-only commands to replicas")
	fs.IntVar(&hashTags, "hash-tags", 0, "put a {group} hash tag in every key (this many groups) so multi-key commands work in a cluster")
	fs.StringVar(&sentinelMaster, "sentinel-master", "", "sentinel master set name (with -mode sentinel; -addr lists the sentinels)")
	fs.StringVar(&loop, "loop", "", "closed (default) or open: an open loop sends at -target-ops whether or not the server keeps up")
	fs.StringVar(&sweepWorkers, "sweep-workers", "", "run once per worker count, for example 4,8,16,32 (2 to 8 levels)")
	fs.StringVar(&sweepRates, "sweep-rates", "", "run once per target rate in ops/s, for example 10000,20000,40000")
	fs.DurationVar(&sweepPause, "sweep-pause", 0, "wait between the runs of a sweep")
	tf.bind(fs)
	fs.BoolVar(&preloadOnly, "preload-only", false, "write the dataset and exit, without running a workload (prepare each server of a replay with the same starting data)")
	fs.Int64Var(&opBudget, "ops", 0, "send exactly this many operations (split between the workers) instead of running for -duration")
	fs.BoolVar(&deterministic, "deterministic", false, "reproducible traffic: needs -seed and -ops; the same seed, -ops and workload always send the same commands, and the run prints a traffic fingerprint (docs/deterministic-mode.md)")
	fs.StringVar(&recPath, "record", "", "write every command the workers send to this file, so the run can be replayed (plgv replay); see docs/record-and-replay.md")
	fs.BoolVar(&recOutcomes, "record-outcomes", false, "with -record: also keep each command's status and latency")
	fs.BoolVar(&recSums, "record-checksums", false, "with -record: also keep a checksum of each reply (implies -record-outcomes)")
	fs.StringVar(&recCompress, "record-compress", "", "with -record: auto (by .zst or .gz extension), none, zstd or gzip")
	fs.IntVar(&recMaxMB, "record-max-mb", 0, "with -record: stop recording after this many uncompressed MB (0 = no limit)")
	fs.StringVar(&recBackpressure, "record-backpressure", "", "with -record: fail (default; the recording stops if the writer lags) or block (workers wait for the writer)")
	fs.BoolVar(&recFingerprint, "record-fingerprint", true, "with -record: store the starting data's fingerprint; -record-fingerprint=false skips it")
	fs.StringVar(&summaryFile, "summary", "", "write a JSON run summary to this file")
	fs.BoolVar(&preload, "preload", true, "write the dataset before the run; -preload=false skips it")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var rawErr error
	cfg, err := loadConfig(path, preset, func(c *config.Config) {
		if set["addr"] {
			c.Target.URIs = splitAddrs(addr)
		}
		if set["raw-file"] {
			mix, err := rawMix(rawFile)
			if err != nil {
				rawErr = err
			} else {
				c.Workload.Mix = mix
			}
		}
		if set["allow-dangerous"] {
			c.Workload.AllowDangerous = allowDangerous
		}
		if set["subscribers"] {
			c.Workload.PubSub.Subscribers = subscribers
		}
		if set["channels"] {
			c.Workload.PubSub.Channels = channels
		}
		if set["mode"] {
			c.Target.Mode = mode
		}
		if set["replica-reads"] {
			c.Target.ReadFromReplicas = replicaReads
		}
		if set["hash-tags"] {
			c.Dataset.HashTagGroups = hashTags
		}
		if set["sentinel-master"] {
			c.Target.Sentinel.MasterName = sentinelMaster
		}
		if set["client"] {
			c.Target.Client = lib
		}
		if set["workers"] {
			c.Run.Concurrency = workers
		}
		if set["duration"] {
			c.Run.Duration = duration
		}
		if set["pipeline"] {
			c.Target.Pool.PipelineDepth = pipeline
		}
		if set["read-pct"] {
			c.Workload.Mix = []config.MixItem{{Op: "GET", Weight: readPct}, {Op: "SET", Weight: 100 - readPct}}
			c.Workload.Mix = dropZero(c.Workload.Mix)
		}
		if set["keys"] {
			c.Dataset.KeyCount = keys
		}
		if set["dist"] {
			c.Dataset.KeyDistribution = dist
		}
		if set["zipf-s"] {
			c.Dataset.ZipfS = zipfS
		}
		if set["value-size"] {
			c.Dataset.ValueSize = valueSize
		}
		if set["prefix"] {
			c.Dataset.KeyPrefix = prefix
		}
		if set["seed"] {
			c.Run.Seed = seed
		}
		if set["server-metrics"] {
			c.Server.Enabled = srvMetrics
		}
		if set["server-config"] {
			c.SrvConf.Enabled = srvConfig
		}
		if srvConfigName != "" {
			c.SrvConf.Name, c.SrvConf.Enabled = srvConfigName, true
		}
		if set["keyspace"] {
			c.KeySpace.Enabled = keyspace
		}
		if keyspaceSamples > 0 {
			c.KeySpace.Samples = keyspaceSamples
		}
		if set["target-ops"] {
			c.Run.TargetOpsPerSec = targetOps
		}
		if set["co"] {
			c.Run.CoordinatedOmission = co
		}
		if set["sample-interval"] {
			c.Output.SampleInterval = sampleEvery
		}
		if set["summary"] {
			c.Output.SummaryFile = summaryFile
		}
		if set["loop"] {
			c.Run.Loop = loop
		}
		if set["sweep-workers"] {
			c.Run.Sweep.Kind, c.Run.Sweep.Values = "workers", parseLevels(sweepWorkers)
		}
		if set["sweep-rates"] {
			c.Run.Sweep.Kind, c.Run.Sweep.Values = "rate", parseLevels(sweepRates)
		}
		if set["sweep-pause"] {
			c.Run.Sweep.Pause = sweepPause
		}
		if set["preload"] {
			c.Dataset.Preload = preload
		}
		tf.apply(fs, c)
		if set["ops"] {
			c.Run.OpBudget = opBudget
		}
		if set["deterministic"] {
			c.Run.Deterministic = deterministic
		}
		if set["record"] {
			c.Record.Path = recPath
		}
		if set["record-outcomes"] {
			c.Record.Outcomes = recOutcomes
		}
		if set["record-checksums"] {
			c.Record.Checksums = recSums
		}
		if set["record-compress"] {
			c.Record.Compression = recCompress
		}
		if set["record-max-mb"] {
			c.Record.MaxMB = recMaxMB
		}
		if set["record-backpressure"] {
			c.Record.Backpressure = recBackpressure
		}
		if set["record-fingerprint"] {
			c.Record.DataFingerprint = recFingerprint
		}
	})
	if rawErr != nil {
		fmt.Fprintln(stderr, "plgv run:", rawErr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "plgv run:", err)
		return 2
	}
	if preloadOnly {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		n, err := session.PreloadOnly(ctx, cfg)
		if err != nil {
			fmt.Fprintln(stderr, "plgv run:", err)
			return 1
		}
		fmt.Fprintf(stdout, "preloaded %d keys per type\n", n)
		return 0
	}
	if set["sweep-workers"] && set["sweep-rates"] {
		fmt.Fprintln(stderr, "plgv run: use either -sweep-workers or -sweep-rates, not both")
		return 2
	}
	if k := cfg.Run.Sweep.Kind; k == "workers" || k == "rate" {
		return executeSweep(cfg, stdout, stderr)
	}
	return execute(cfg, stdout, stderr)
}

// rawMix reads a command file into RAW mix entries.
func rawMix(path string) ([]config.MixItem, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines, err := rawcmd.ParseLines(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	mix := make([]config.MixItem, len(lines))
	for i, l := range lines {
		mix[i] = config.MixItem{Op: "RAW", Weight: l.Weight, Command: l.Command}
	}
	return mix, nil
}

// splitAddrs splits "a:1,b:2" into addresses.
func splitAddrs(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, f)
	}
	return out
}

// parseLevels reads "4,8,16" into numbers. A value that is not a number becomes
// 0, which validation then rejects with a message about that level.
func parseLevels(s string) []float64 {
	out := []float64{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			v = 0
		}
		out = append(out, v)
	}
	return out
}

func dropZero(m []config.MixItem) []config.MixItem {
	out := m[:0]
	for _, it := range m {
		if it.Weight > 0 {
			out = append(out, it)
		}
	}
	return out
}

func execute(cfg config.Config, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	out, err := session.Run(ctx, cfg, session.Options{Version: version}, session.Hooks{
		OnPhase: func(p string) {
			switch p {
			case session.PhasePreloading:
				fmt.Fprintf(stdout, "preloading %d keys per type...\n", cfg.Dataset.KeyCount)
			case session.PhaseRunning:
				fmt.Fprintf(stdout, "running %v: %d workers, pipeline %d, %d keys (%s), %dB values, client %s\n",
					cfg.Run.Duration, cfg.Run.Concurrency, cfg.Target.Pool.PipelineDepth, cfg.Dataset.KeyCount,
					cfg.Dataset.KeyDistribution, cfg.Dataset.ValueSize, cfg.Target.Client)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "plgv run:", err)
		return 1
	}
	printResult(stdout, out.Result)
	printServer(stdout, out.Server, out.Result)
	printServerConfig(stdout, out.Summary.ServerConfig)
	printKeySpace(stdout, out.Summary.KeySpace)
	printRecording(stdout, out.Recording)
	printFindings(stdout, out.Findings)
	if f := cfg.Output.SummaryFile; f != "" {
		if err := out.Summary.Write(f); err != nil {
			fmt.Fprintln(stderr, "plgv run:", err)
			return 1
		}
		fmt.Fprintln(stdout, "\nsummary written to", f)
	}
	if out.Result.Errors > 0 {
		return 1
	}
	return 0
}

// executeSweep runs a sweep and prints the results table.
func executeSweep(cfg config.Config, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sw := cfg.Run.Sweep
	unit := "workers"
	if sw.Kind == "rate" {
		unit = "ops/s"
	}
	fmt.Fprintf(stdout, "sweep of %d runs by %s, %v each\n", len(sw.Values), unit, cfg.Run.Duration)
	outs, err := session.Sweep(ctx, cfg, session.Options{Version: version, Label: ""}, session.Hooks{
		OnLevel: func(_ string, i, n int, kind string, v float64) {
			fmt.Fprintf(stdout, "\nlevel %d of %d: %s %s\n", i, n, sweepNum(v), unit)
		},
		OnPhase: func(p string) {
			if p == session.PhasePreloading {
				fmt.Fprintf(stdout, "  preloading %d keys per type...\n", cfg.Dataset.KeyCount)
			}
		},
		OnOutcome: func(o *session.Outcome) {
			r := o.Result
			fmt.Fprintf(stdout, "  %.0f ops/s, p50 %v, p99 %v, %d errors\n", r.OpsPerSec(),
				r.Hist.Quantile(0.5).Round(time.Microsecond), r.Hist.Quantile(0.99).Round(time.Microsecond), r.Errors)
		},
	})
	if len(outs) > 0 {
		printSweep(stdout, sw.Kind, outs)
		if f := cfg.Output.SummaryFile; f != "" {
			for _, o := range outs {
				p := levelFile(f, o.Summary.Sweep.Index)
				if werr := o.Summary.Write(p); werr != nil {
					fmt.Fprintln(stderr, "plgv run:", werr)
					return 1
				}
				fmt.Fprintln(stdout, "summary written to", p)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "plgv run:", err)
		return 1
	}
	for _, o := range outs {
		if o.Result.Errors > 0 {
			return 1
		}
	}
	return 0
}

func sweepNum(v float64) string { return sweepapi.Num(v) }

// levelFile turns "out.json" into "out-2.json" for level 2.
func levelFile(path string, level int) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(path, ext), level, ext)
}

func printSweep(w io.Writer, kind string, outs []*session.Outcome) {
	rows := make([]sweepapi.Row, len(outs))
	for i, o := range outs {
		r := o.Result
		rows[i] = sweepapi.Row{Value: o.Summary.Sweep.Value, OpsPerSec: r.OpsPerSec(),
			P99Ms: float64(r.Hist.Quantile(0.99)) / float64(time.Millisecond), Dropped: r.Dropped, Stopped: o.Summary.Stopped}
	}
	an := sweepapi.Analyze(kind, rows, outs[0].Summary.Sweep.Total)
	head := "workers"
	eff := "scaling"
	if kind == "rate" {
		head, eff = "target ops/s", "served"
	}
	fmt.Fprintf(w, "\nsweep results:\n  %-14s %10s %10s %10s %10s %8s %9s\n", head, "ops/s", "p50", "p95", "p99", "errors", eff)
	for i, o := range outs {
		r := o.Result
		fmt.Fprintf(w, "  %-14s %10.0f %10v %10v %10v %8d %8.0f%%\n", sweepapi.Num(rows[i].Value), r.OpsPerSec(),
			r.Hist.Quantile(0.5).Round(time.Microsecond), r.Hist.Quantile(0.95).Round(time.Microsecond),
			r.Hist.Quantile(0.99).Round(time.Microsecond), r.Errors, an.Efficiency[i]*100)
	}
	for _, n := range an.Notes {
		fmt.Fprintln(w, "  "+n)
	}
}

// configCmd implements `plgv config`: print the effective configuration, or
// the defaults when no file is given. A bad file is reported and exits 2.
func configCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var path, out, preset string
	fs.StringVar(&path, "config", "", "YAML config file to validate and print")
	fs.StringVar(&preset, "preset", "", "start from a built-in preset")
	fs.StringVar(&out, "o", "", "write the result to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	cfg, err := loadConfig(path, preset, func(*config.Config) {})
	if err != nil {
		fmt.Fprintln(stderr, "plgv config:", err)
		return 2
	}
	if out != "" {
		if err := cfg.Save(out); err != nil {
			fmt.Fprintln(stderr, "plgv config:", err)
			return 1
		}
		fmt.Fprintln(stdout, "wrote", out)
		return 0
	}
	b, err := cfg.Marshal()
	if err != nil {
		fmt.Fprintln(stderr, "plgv config:", err)
		return 1
	}
	_, _ = stdout.Write(b)
	return 0
}

func printResult(w io.Writer, r *valkey.RunResult) {
	h := r.Hist
	fmt.Fprintf(w, "\nops:        %d\n", r.Ops)
	fmt.Fprintf(w, "elapsed:    %.2fs (seed %d)\n", r.Elapsed.Seconds(), r.Seed)
	fmt.Fprintf(w, "throughput: %.0f ops/s\n", r.OpsPerSec())
	if r.TrafficHash != "" {
		fmt.Fprintf(w, "traffic:    %d commands, fingerprint %s\n", r.TrafficOps, r.TrafficHash)
	}
	fmt.Fprintf(w, "latency:    mean %v  p50 %v  p95 %v  p99 %v  p99.9 %v  max %v\n",
		h.Mean().Round(time.Microsecond), h.Quantile(0.5).Round(time.Microsecond),
		h.Quantile(0.95).Round(time.Microsecond), h.Quantile(0.99).Round(time.Microsecond),
		h.Quantile(0.999).Round(time.Microsecond), h.Max().Round(time.Microsecond))
	if r.LoadProfile != "" && !strings.HasPrefix(r.LoadProfile, "fixed") {
		fmt.Fprintf(w, "profile:    %s (peak %d workers)\n", r.LoadProfile, r.PeakWorkers)
	}
	if r.Loop == "open" {
		fmt.Fprintf(w, "open loop:  %.0f ops/s offered; latency includes time spent waiting to be sent\n", r.TargetOpsPerSec)
		if r.Dropped > 0 {
			fmt.Fprintf(w, "dropped:    %d operations were offered but never sent\n", r.Dropped)
		}
	} else if r.TargetOpsPerSec > 0 {
		mode := "service time (from send)"
		if r.CoordinatedOmission {
			mode = "coordinated-omission aware (from intended start)"
		}
		fmt.Fprintf(w, "target:     %.0f ops/s, latency measured as %s\n", r.TargetOpsPerSec, mode)
	}
	if ps := r.PubSub; ps != nil {
		ratio := 0.0
		if ps.Expected > 0 {
			ratio = float64(ps.Delivered) / float64(ps.Expected) * 100
		}
		kind := "channels"
		if ps.Sharded {
			kind = "shard channels"
		}
		fmt.Fprintf(w, "pub/sub:    %d subscribers on %d %s: %d published, %d of %d expected deliveries (%.1f%%)\n", ps.Subscribers, ps.Channels, kind, ps.Published, ps.Delivered, ps.Expected, ratio)
		if dh := r.DeliveryHist; dh != nil && dh.Count() > 0 {
			fmt.Fprintf(w, "delivery:   mean %v  p50 %v  p95 %v  p99 %v  max %v\n", dh.Mean().Round(time.Microsecond), dh.Quantile(0.5).Round(time.Microsecond),
				dh.Quantile(0.95).Round(time.Microsecond), dh.Quantile(0.99).Round(time.Microsecond), dh.Max().Round(time.Microsecond))
		}
	}
	fmt.Fprintf(w, "misses:     %d\n", r.Misses)
	fmt.Fprintf(w, "errors:     %d", r.Errors)
	if r.FirstError != "" {
		fmt.Fprintf(w, " (first: %s)", r.FirstError)
	}
	fmt.Fprintln(w)
	if len(r.ErrClasses) > 0 {
		keys := make([]string, 0, len(r.ErrClasses))
		for k := range r.ErrClasses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprint(w, "error classes:")
		for _, k := range keys {
			fmt.Fprintf(w, " %s=%d", k, r.ErrClasses[k])
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "client cpu: %.2f cores, %.2f cores per 100k ops/s\n",
		r.CPU.Seconds()/r.Elapsed.Seconds(), r.CoresPer100k())

	fmt.Fprintln(w, "\nper operation:")
	fmt.Fprintf(w, "  %-14s %12s %8s %10s %8s %10s %10s\n", "op", "ops", "share", "misses", "errors", "p50", "p99")
	for _, s := range r.PerOp {
		share := 0.0
		if r.Ops > 0 {
			share = float64(s.Ops) / float64(r.Ops) * 100
		}
		fmt.Fprintf(w, "  %-14s %12d %7.1f%% %10d %8d %10v %10v\n", strings.ToUpper(s.Op), s.Ops, share, s.Misses, s.Errors,
			s.P50.Round(time.Microsecond), s.P99.Round(time.Microsecond))
	}
	if note := sameLatencyNote(r.PerOp); note != "" {
		fmt.Fprintln(w, "\n"+note)
	}
}

// sameLatencyNote explains, in plain words, why every operation can show
// almost the same latency. It returns "" unless the p50 values are within 10%
// of each other across at least three operations.
func sameLatencyNote(ops []valkey.OpStat) string {
	var lo, hi time.Duration
	n := 0
	for _, o := range ops {
		if o.P50 <= 0 {
			continue
		}
		if n == 0 || o.P50 < lo {
			lo = o.P50
		}
		if o.P50 > hi {
			hi = o.P50
		}
		n++
	}
	if n < 3 || float64(hi-lo) > 0.10*float64(lo) {
		return ""
	}
	return "note: every operation shows almost the same latency. That is normal when the server is kept\n" +
		"busy: each request mostly waits in line behind the others, and the wait is about the same for all\n" +
		"commands. It does not mean one command is as cheap as another. To see what each command costs,\n" +
		"run with fewer workers (for example -workers 1), or set a fixed rate with -target-ops and -co."
}

func printServer(w io.Writer, s *servermetrics.Summary, r *valkey.RunResult) {
	if s == nil {
		return
	}
	fmt.Fprintln(w, "\nserver:")
	if s.Version == "" {
		fmt.Fprintf(w, "  no data (%d polls failed", s.PollErrors)
		if s.FirstError != "" {
			fmt.Fprintf(w, "; first: %s", s.FirstError)
		}
		fmt.Fprintln(w, ")")
		return
	}
	if s.Mode == "cluster" || s.Mode == "sentinel" {
		fmt.Fprintf(w, "  version %s, %s\n", s.Version, s.Mode)
	} else {
		fmt.Fprintf(w, "  version %s, %s, %s\n", s.Version, s.Role, s.Mode)
	}
	fmt.Fprintf(w, "  ops processed: %d (plgv sent %d)\n", s.ServerOps, r.Ops)
	if s.HitRatio >= 0 {
		fmt.Fprintf(w, "  hit ratio: %.1f%% (%d hits, %d misses)\n", s.HitRatio*100, s.KeyspaceHits, s.KeyspaceMisses)
	}
	fmt.Fprintf(w, "  memory: peak %.1f MiB, maxmemory %d, evicted keys %d, expired keys %d\n",
		float64(s.UsedMemoryMax)/(1<<20), s.MaxMemory, s.EvictedKeys, s.ExpiredKeys)
	fmt.Fprintf(w, "  slowlog: %d new entries", s.SlowlogNew)
	if s.SlowlogNew > 0 {
		fmt.Fprintf(w, ", slowest %v", s.SlowlogWorst.Round(time.Microsecond))
	}
	fmt.Fprintln(w)
	if s.PollErrors > 0 {
		fmt.Fprintf(w, "  poll errors: %d (first: %s)\n", s.PollErrors, s.FirstError)
	}
	if len(s.Nodes) > 0 {
		var attributed uint64
		for _, n := range r.PerNode {
			attributed += n.Ops
		}
		fmt.Fprintf(w, "\n  nodes:\n    %-18s %-8s %6s %12s %8s %12s %10s\n", "address", "role", "slots", "plgv ops", "share", "server ops", "memory")
		for _, n := range s.Nodes {
			var ops uint64
			for i, a := range r.NodeAddrs {
				if a == n.Addr && i < len(r.PerNode) {
					ops = r.PerNode[i].Ops
				}
			}
			share := 0.0
			if attributed > 0 {
				share = float64(ops) / float64(attributed) * 100
			}
			fmt.Fprintf(w, "    %-18s %-8s %6d %12d %7.1f%% %12d %9.1fM\n", n.Addr, n.Role, n.Slots, ops, share, n.Summary.ServerOps, float64(n.Summary.UsedMemoryMax)/(1<<20))
		}
	}
	if t := s.Topology; t != nil {
		if len(t.Events) == 0 {
			fmt.Fprintln(w, "  topology: no changes during the run")
		}
		for _, e := range t.Events {
			fmt.Fprintf(w, "  topology: at %.0fs %s: %s\n", e.Offset.Seconds(), e.Kind, e.Detail)
		}
	}
}

// keySettings are shown in the terminal; the full list is in the run summary.
var keySettings = []string{"maxmemory", "maxmemory-policy", "io-threads", "appendonly", "save", "hz", "maxclients"}

func printServerConfig(w io.Writer, sc *serverconfig.Snapshot) {
	if sc == nil {
		return
	}
	title := "server config"
	if sc.Name != "" {
		title += " (" + sc.Name + ")"
	}
	fmt.Fprintln(w, "\n"+title+":")
	if sc.Status != serverconfig.StatusOK {
		why := "the server could not be read"
		if sc.Status == serverconfig.StatusNotPermitted {
			why = "the server does not allow CONFIG GET (blocked or no permission)"
		}
		fmt.Fprintf(w, "  not recorded: %s. The run itself was not affected.\n", why)
		for _, n := range sc.Nodes {
			if n.Error != "" {
				fmt.Fprintf(w, "  %s: %s\n", n.Addr, n.Error)
				break
			}
		}
		return
	}
	for _, n := range sc.Nodes {
		if n.Status != serverconfig.StatusOK {
			fmt.Fprintf(w, "  %s: not recorded (%s)\n", n.Addr, n.Status)
			continue
		}
		var parts []string
		for _, k := range keySettings {
			if v, ok := n.Settings[k]; ok {
				parts = append(parts, fmt.Sprintf("%s=%q", k, v))
			}
		}
		fmt.Fprintf(w, "  %s: %d settings read; %s\n", n.Addr, len(n.Settings), strings.Join(parts, " "))
		for _, c := range n.Changed {
			fmt.Fprintf(w, "    changed during the run: %s %q -> %q\n", c.Key, c.Before, c.After)
		}
	}
	if len(sc.Differ) > 0 {
		fmt.Fprintf(w, "  differs between nodes: %s\n", strings.Join(sc.Differ, ", "))
	}
	fmt.Fprintln(w, "  (all settings are in the run summary; secrets are masked)")
}

func printFindings(w io.Writer, found []insights.Finding) {
	fmt.Fprintln(w, "\nfindings:")
	if len(found) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, f := range found {
		fmt.Fprintf(w, "  [%s] %s\n      %s\n      %s\n", f.Severity, f.Title, f.Evidence, f.Advice)
	}
}

func printKeySpace(w io.Writer, k *keyscan.Result) {
	if k == nil {
		return
	}
	fmt.Fprintln(w, "\nkey space:")
	if k.Sampled == 0 {
		fmt.Fprintf(w, "  no keys analyzed matching %s", k.Match)
		if k.FirstError != "" {
			fmt.Fprintf(w, " (%s)", k.FirstError)
		}
		fmt.Fprintln(w)
		return
	}
	scope := fmt.Sprintf("sample of %d keys (of %d on the server)", k.Sampled, k.DBSize)
	if k.Complete {
		scope = fmt.Sprintf("all %d matching keys", k.Sampled)
	}
	fmt.Fprintf(w, "  %s, pattern %s: %s total, p50 %s, p99 %s, max %s\n", scope, k.Match, humanBytes(k.TotalBytes), humanBytes(k.P50Bytes), humanBytes(k.P99Bytes), humanBytes(k.MaxBytes))
	fmt.Fprintf(w, "  %-8s %8s %7s %10s %10s %10s\n", "type", "keys", "share", "avg size", "max size", "max elems")
	for _, t := range k.Types {
		fmt.Fprintf(w, "  %-8s %8d %6.1f%% %10s %10s %10d\n", t.Type, t.Keys, t.Share*100, humanBytes(t.AvgBytes), humanBytes(t.MaxBytes), t.MaxElements)
	}
	fmt.Fprintf(w, "  %d of %d keys have no expiry\n", k.NoExpiry, k.Sampled)
	for i, b := range k.Big {
		fmt.Fprintf(w, "  big #%d: %s (%s, %s, %d elements)\n", i+1, b.Key, b.Type, humanBytes(b.Bytes), b.Elements)
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
