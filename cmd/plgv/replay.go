package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/multitarget"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/session"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// replayCmd implements `plgv replay`: send a recording to a server again.
func replayCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		addr, lib, mode, sentinel string
		speed                     float64
		readsOnly, info           bool
		iterations                int
		delay                     time.Duration
		allowDangerous, require   bool
		replicaReads              bool
		summaryFile               string
		targetsFlag, label        string
		tf                        tlsFlags
		concurrent                bool
		tolerance                 float64
	)
	fs.StringVar(&addr, "addr", "", "target host:port, or several comma-separated seed nodes / sentinels (default 127.0.0.1:6379; env PLGV_URI)")
	fs.StringVar(&targetsFlag, "targets", "", "replay on several servers: comma-separated name=host:port (or host:port); they are compared at the end (docs/multi-target.md)")
	tf.bind(fs)
	fs.StringVar(&label, "label", "", "name for this replay in a summary and in comparisons (default: the address)")
	fs.BoolVar(&concurrent, "concurrent", false, "with -targets: replay on all targets at the same time instead of one after another")
	fs.Float64Var(&tolerance, "tolerance", 10, "with -targets: percent a metric may change before it counts as better or worse")
	fs.StringVar(&lib, "client", "", "client library: valkey-go or go-redis")
	fs.StringVar(&mode, "mode", "", "auto (default), standalone, cluster or sentinel")
	fs.StringVar(&sentinel, "sentinel-master", "", "sentinel master set name (with -mode sentinel)")
	fs.BoolVar(&replicaReads, "replica-reads", false, "cluster or sentinel: send read-only commands to replicas")
	fs.Float64Var(&speed, "speed", 1, "1 replays at the recorded pace, 2 twice as fast, 0 as fast as the server answers")
	fs.BoolVar(&readsOnly, "reads-only", false, "skip every command group that is not known to be read-only, so the data is not changed")
	fs.IntVar(&iterations, "iterations", 1, "replay the recording this many times")
	fs.DurationVar(&delay, "iteration-delay", 0, "wait between iterations")
	fs.BoolVar(&require, "require-data", false, "stop before replaying if the target does not hold the data the recording started with")
	fs.BoolVar(&allowDangerous, "allow-dangerous", false, "replay commands blocked by default (FLUSHALL, DEBUG, CONFIG SET, ...) if the file holds them")
	fs.BoolVar(&info, "info", false, "only describe the recording: header, status, command counts and hash")
	fs.StringVar(&summaryFile, "summary", "", "write the replay result as JSON to this file")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: plgv replay [flags] <recording-file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)
	if info {
		return describeRecording(path, stdout, stderr)
	}
	if speed < 0 {
		fmt.Fprintln(stderr, "plgv replay: -speed must be 0 or more")
		return 2
	}
	cfg := config.Default()
	if err := cfg.ApplyEnvStrict(os.Getenv); err != nil {
		fmt.Fprintln(stderr, "plgv replay:", err)
		return 2
	}
	if addr != "" && targetsFlag != "" {
		fmt.Fprintln(stderr, "plgv replay: use either -addr or -targets, not both")
		return 2
	}
	if addr != "" {
		cfg.Target.URIs = splitAddrs(addr)
	}
	if lib != "" {
		cfg.Target.Client = lib
	}
	if mode != "" {
		cfg.Target.Mode = mode
	}
	if sentinel != "" {
		cfg.Target.Sentinel.MasterName = sentinel
	}
	cfg.Target.ReadFromReplicas = replicaReads
	tf.apply(fs, &cfg)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "plgv replay:", err)
		return 2
	}
	var targets []multitarget.Target
	if targetsFlag != "" {
		var err error
		if targets, err = multitarget.Parse(splitAddrs(targetsFlag)); err != nil || len(targets) < 2 {
			if err == nil {
				err = fmt.Errorf("-targets needs at least two servers (use -addr for one)")
			}
			fmt.Fprintln(stderr, "plgv replay:", err)
			return 2
		}
	} else {
		a, _ := cfg.Target.Addrs()
		name := strings.Join(a, ",")
		if label != "" {
			name = label
		}
		targets = []multitarget.Target{{Name: name, Addr: strings.Join(a, ",")}}
	}
	if concurrent && len(targets) < 2 {
		fmt.Fprintln(stderr, "plgv replay: -concurrent needs -targets")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	results := session.ReplayMany(ctx, cfg, targets, path, replay.Options{
		Speed: speed, ReadsOnly: readsOnly, Iterations: iterations, IterationDelay: delay, AllowDangerous: allowDangerous,
	}, concurrent, require, version)
	code := 0
	var sums []*replay.Summary
	for _, tr := range results {
		sums = append(sums, tr.Summary)
		if len(results) > 1 {
			fmt.Fprintf(stdout, "\n=== %s (%s)\n", tr.Target.Name, tr.Target.Addr)
		}
		if tr.Outcome != nil {
			printDataCheck(stdout, tr.Outcome)
		}
		if tr.Err != nil {
			fmt.Fprintf(stderr, "plgv replay: %s: %v\n", tr.Target.Name, tr.Err)
			code = 1
			continue
		}
		printReplay(stdout, tr.Outcome.Result)
		if r := tr.Outcome.Result; r.Errors > 0 && (r.Outcomes == nil || r.Outcomes.StatusDiffs > 0) {
			code = 1
		}
	}
	var comps []*replaycompare.Comparison
	if len(sums) > 1 {
		comps = replaycompare.CompareAll(sums, replaycompare.Options{Tolerance: tolerance / 100})
		for _, c := range comps {
			printComparison(stdout, c)
		}
	}
	if summaryFile != "" {
		var v any = sums[0]
		if len(sums) > 1 {
			m := "sequential"
			if concurrent {
				m = "concurrent"
			}
			v = replaycompare.File{Format: replaycompare.MultiFormat, Mode: m, Targets: sums, Comparisons: comps}
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(summaryFile, append(b, '\n'), 0o600); err != nil {
			fmt.Fprintln(stderr, "plgv replay:", err)
			return 1
		}
		fmt.Fprintln(stdout, "\nsummary written to", summaryFile)
	}
	return code
}

// compareCmd implements `plgv compare`: compare replay summaries.
func compareCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		tolerance float64
		asJSON    bool
		failOn    bool
	)
	fs.Float64Var(&tolerance, "tolerance", 10, "percent a metric may change before it counts as better or worse")
	fs.BoolVar(&asJSON, "json", false, "print the comparison as JSON")
	fs.BoolVar(&failOn, "fail-on-regression", false, "exit 1 when a target is slower, mixed or not comparable with the first")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: plgv compare [flags] <replay-summary.json>...")
		fmt.Fprintln(stderr, "Each file is a `plgv replay -summary` file (one target, or several from -targets). The first target is the baseline.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	var all []*replay.Summary
	for _, p := range fs.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(stderr, "plgv compare:", err)
			return 2
		}
		got, err := replaycompare.Parse(b)
		if err != nil {
			fmt.Fprintf(stderr, "plgv compare: %s: %v\n", p, err)
			return 2
		}
		all = append(all, got...)
	}
	if len(all) < 2 {
		fmt.Fprintln(stderr, "plgv compare: give two or more replays (files, or one file from -targets)")
		return 2
	}
	replaycompare.Uniquify(all)
	comps := replaycompare.CompareAll(all, replaycompare.Options{Tolerance: tolerance / 100})
	if asJSON {
		b, _ := json.MarshalIndent(comps, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, c := range comps {
			printComparison(stdout, c)
		}
	}
	if failOn {
		for _, c := range comps {
			if c.Verdict == "slower" || c.Verdict == "mixed" || !c.Comparable {
				return 1
			}
		}
	}
	return 0
}

func printComparison(w io.Writer, c *replaycompare.Comparison) {
	fmt.Fprintf(w, "\ncompare:    %s against baseline %s: %s", c.Other, c.Base, strings.ToUpper(c.Verdict))
	if c.BaseServer != "" || c.OtherServer != "" {
		fmt.Fprintf(w, " (Valkey %s vs %s)", orDash(c.BaseServer), orDash(c.OtherServer))
	}
	fmt.Fprintln(w)
	if len(c.Rows) > 0 {
		fmt.Fprintf(w, "  %-14s %14s %14s %9s\n", "metric", c.Base, c.Other, "change")
		for _, r := range c.Rows {
			mark := ""
			switch r.Verdict {
			case replaycompare.Better:
				mark = "  better"
			case replaycompare.Worse:
				mark = "  WORSE"
			}
			fmt.Fprintf(w, "  %-14s %14.2f %14.2f %+8.1f%%%s\n", r.Metric+" ("+r.Unit+")", r.Base, r.Other, r.DeltaPct, mark)
		}
	}
	for _, f := range c.Findings {
		fmt.Fprintf(w, "  [%s] %s", f.Severity, f.Title)
		if f.Detail != "" {
			fmt.Fprintf(w, ": %s", f.Detail)
		}
		fmt.Fprintln(w)
	}
}

func describeRecording(path string, stdout, stderr io.Writer) int {
	fi, err := workloadrec.Scan(path)
	if err != nil {
		fmt.Fprintln(stderr, "plgv replay:", err)
		return 1
	}
	h := fi.Header
	fmt.Fprintf(stdout, "recording:   %s (%s, %s on disk)\n", path, fi.Compression, humanBytes(fi.FileBytes))
	fmt.Fprintf(stdout, "status:      %s", fi.Status)
	if fi.Reason != "" {
		fmt.Fprintf(stdout, " (%s)", fi.Reason)
	}
	fmt.Fprintf(stdout, "\ncreated:     %s by plgv %s, seed %d\n", h.CreatedAt, h.PLGVVersion, h.Seed)
	fmt.Fprintf(stdout, "server:      %s (%s)\n", orDash(h.ServerVersion), orDash(h.Mode))
	fmt.Fprintf(stdout, "workload:    %d workers, %s, mix %s\n", h.Workers, h.Config.Duration, orDash(h.Config.Mix))
	fmt.Fprintf(stdout, "commands:    %d in %d groups\n", fi.Ops, fi.Groups)
	fmt.Fprintf(stdout, "outcomes:    %v, checksums %v\n", h.Outcomes, h.ResultChecksums)
	if im := h.Import; im != nil {
		fmt.Fprintf(stdout, "imported:    from %s: %d of %d entries converted, %d skipped, %d connections\n", strings.Join(im.Sources, "+"), im.Converted, im.Entries, im.Skipped, im.Connections)
		fmt.Fprintln(stdout, "  note:", im.Note)
	}
	if d := h.DataFingerprint; d != nil {
		fmt.Fprintf(stdout, "start data:  %d keys, %d sampled (1 in %d)%s\n", d.DBSize, d.Sampled, d.Stride, map[bool]string{true: ", partial", false: ""}[d.Partial])
	}
	fmt.Fprintf(stdout, "hash:        %s\n", fi.CommandsHash)
	var names []string
	for n := range fi.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "  %-12s %d\n", n, fi.Commands[n])
	}
	if fi.Status != workloadrec.StatusComplete {
		return 1
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printDataCheck(w io.Writer, o *session.ReplayOutcome) {
	if o.Data == nil {
		return
	}
	if o.Data.Match {
		fmt.Fprintln(w, "start data:  the target holds the data the recording started with")
	} else {
		fmt.Fprintln(w, "start data:  DIFFERENT from the recording:")
		for _, d := range o.Data.Differences {
			fmt.Fprintln(w, "  -", d)
		}
	}
	for _, n := range o.Data.Notes {
		fmt.Fprintln(w, "  note:", n)
	}
}

func printReplay(w io.Writer, r *replay.Result) {
	h := r.Hist
	fmt.Fprintf(w, "\nreplayed:   %s (recording %s)\n", r.Path, r.Status)
	pace := "as fast as possible"
	if r.Speed > 0 {
		pace = fmt.Sprintf("recorded pace x%g", r.Speed)
	}
	fmt.Fprintf(w, "mode:       %s%s, %d iteration(s)\n", pace, map[bool]string{true: ", reads only", false: ""}[r.ReadsOnly], len(r.Iterations))
	fmt.Fprintf(w, "ops:        %d (%d errors, %d misses)\n", r.Ops, r.Errors, r.Misses)
	if r.Skipped > 0 {
		fmt.Fprintf(w, "skipped:    %d commands that are not read-only\n", r.Skipped)
	}
	fmt.Fprintf(w, "elapsed:    %.2fs\n", r.Elapsed.Seconds())
	fmt.Fprintf(w, "throughput: %.0f ops/s\n", r.OpsPerSec())
	fmt.Fprintf(w, "latency:    mean %v  p50 %v  p95 %v  p99 %v  max %v\n",
		h.Mean().Round(time.Microsecond), h.Quantile(0.5).Round(time.Microsecond), h.Quantile(0.95).Round(time.Microsecond),
		h.Quantile(0.99).Round(time.Microsecond), h.Max().Round(time.Microsecond))
	if r.Speed > 0 && r.MaxLag > 100*time.Millisecond {
		fmt.Fprintf(w, "pace:       the replay fell behind the recording by up to %v\n", r.MaxLag.Round(time.Millisecond))
	}
	if len(r.Iterations) > 1 {
		fmt.Fprintln(w, "iterations:")
		for _, it := range r.Iterations {
			fmt.Fprintf(w, "  %d: %d ops, %.0f ops/s, p50 %v, p99 %v, %d errors\n", it.Index, it.Ops,
				float64(it.Ops)/it.Elapsed.Seconds(), it.P50.Round(time.Microsecond), it.P99.Round(time.Microsecond), it.Errors)
		}
	}
	switch {
	case r.ReadsOnly:
		fmt.Fprintln(w, "commands:   not compared with the recording's hash (reads-only leaves commands out)")
	case r.HashMatches:
		fmt.Fprintln(w, "commands:   the replay sent exactly the recorded commands (hash matches)")
	default:
		fmt.Fprintln(w, "commands:   the hash of the sent commands differs from the recording (stopped early, or the file changed)")
	}
	if o := r.Outcomes; o != nil {
		fmt.Fprintf(w, "outcomes:   %d compared, %d with a different status", o.Compared, o.StatusDiffs)
		if o.ChecksumCompared > 0 {
			fmt.Fprintf(w, "; %d of %d reply checksums differ", o.ChecksumDiffs, o.ChecksumCompared)
		}
		fmt.Fprintf(w, "\nrecorded:   p50 %v  p99 %v (replay above)\n", o.RecordedP50.Round(time.Microsecond), o.RecordedP99.Round(time.Microsecond))
		if o.Note != "" {
			fmt.Fprintln(w, "  note:", o.Note)
		}
		for _, d := range o.Examples {
			fmt.Fprintf(w, "  command #%d %s: %s recorded %s, replayed %s\n", d.Seq, d.Command, d.Kind, d.Recorded, d.Replayed)
		}
	}
	if len(r.PerOp) > 0 {
		fmt.Fprintf(w, "\n%-14s %10s %8s %8s %10s %10s\n", "command", "ops", "errors", "misses", "p50", "p99")
		for _, o := range r.PerOp {
			if o.Ops == 0 {
				continue
			}
			fmt.Fprintf(w, "%-14s %10d %8d %8d %10v %10v\n", o.Command, o.Ops, o.Errors, o.Misses, o.P50.Round(time.Microsecond), o.P99.Round(time.Microsecond))
		}
	}
	if r.FirstError != "" {
		fmt.Fprintln(w, "\nfirst error:", r.FirstError)
	}
}

// printRecording reports a finished recording after `plgv run -record`.
func printRecording(w io.Writer, r *workloadrec.Result) {
	if r == nil {
		return
	}
	fmt.Fprintf(w, "\nrecording:  %s: %s, %d commands, %s (%s on disk, %s)\n", r.Path, r.Status, r.Ops, humanBytes(r.Bytes), humanBytes(r.FileBytes), r.Compression)
	if r.Reason != "" {
		fmt.Fprintln(w, "  reason:", r.Reason)
	}
	if r.Waits > 0 {
		fmt.Fprintf(w, "  workers waited for the writer %d times (%v in total, %v at most)\n", r.Waits, r.WaitTotal.Round(time.Millisecond), r.WaitMax.Round(time.Millisecond))
	}
	if r.Status == workloadrec.StatusComplete {
		fmt.Fprintln(w, "  hash:", r.CommandsHash)
	}
}
