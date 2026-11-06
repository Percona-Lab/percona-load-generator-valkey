package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/trafficimport"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// importCmd implements `plgv import-traffic`: turn captured traffic into a recording.
func importCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-traffic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		out, format, compress string
		capture, slowlogFrom  string
		capDur                time.Duration
		capMax, slowN         int
		maxWorkers, db        int
		allowDangerous        bool
		tf                    tlsFlags
	)
	fs.StringVar(&out, "out", "", "recording file to write (required); .zst and .gz compress")
	fs.StringVar(&format, "format", "auto", "input format: auto, monitor or slowlog")
	fs.StringVar(&capture, "capture", "", "capture live: run MONITOR on this host:port for a short, capped time (slows the server; see docs/traffic-import.md)")
	fs.DurationVar(&capDur, "capture-duration", 10*time.Second, "with -capture: stop after this long")
	fs.IntVar(&capMax, "capture-max", 100000, "with -capture: stop after this many commands")
	fs.StringVar(&slowlogFrom, "slowlog-from", "", "read the slowlog of this host:port (SLOWLOG GET) instead of a file")
	fs.IntVar(&slowN, "slowlog-count", 128, "with -slowlog-from: entries to read")
	fs.IntVar(&maxWorkers, "max-workers", 64, "at most this many workers in the recording; more connections share workers")
	fs.IntVar(&db, "db", 0, "import this database only (replay uses the default database, 0)")
	fs.BoolVar(&allowDangerous, "allow-dangerous", false, "keep harmful commands (FLUSHALL, CONFIG SET, ...); credentials are always dropped")
	tf.bind(fs)
	fs.StringVar(&compress, "compress", "auto", "auto (by extension), none, zstd or gzip")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: plgv import-traffic -out FILE [flags] [captured-file...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	files := fs.Args()
	if out == "" {
		fmt.Fprintln(stderr, "plgv import-traffic: -out is required")
		fs.Usage()
		return 2
	}
	if len(files) == 0 && capture == "" && slowlogFrom == "" {
		fmt.Fprintln(stderr, "plgv import-traffic: give a captured file, -capture host:port or -slowlog-from host:port")
		return 2
	}
	switch compress {
	case "auto", "":
		compress = workloadrec.CompressionNone
		if strings.HasSuffix(out, ".zst") {
			compress = workloadrec.CompressionZstd
		} else if strings.HasSuffix(out, ".gz") {
			compress = workloadrec.CompressionGzip
		}
	case "none", "zstd", "gzip":
	default:
		fmt.Fprintf(stderr, "plgv import-traffic: unknown -compress %q\n", compress)
		return 2
	}
	cfg := config.Default()
	if err := cfg.ApplyEnvStrict(os.Getenv); err != nil {
		fmt.Fprintln(stderr, "plgv import-traffic:", err)
		return 2
	}
	tf.apply(fs, &cfg)
	cfg.Target.URIs = splitAddrs(strings.Join([]string{capture, slowlogFrom}, ","))
	if err := cfg.Target.CheckTLSFiles(); err != nil {
		fmt.Fprintln(stderr, "plgv import-traffic:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var entries []trafficimport.Entry
	var sources []string
	addSource := func(s string) {
		for _, x := range sources {
			if x == s {
				return
			}
		}
		sources = append(sources, s)
	}
	var names []string
	for _, f := range files {
		es, got, junk, err := trafficimport.ReadFile(f, format)
		if err != nil {
			fmt.Fprintf(stderr, "plgv import-traffic: %s: %v\n", f, err)
			return 1
		}
		fmt.Fprintf(stdout, "read %s: %d %s entries (%d lines were not commands)\n", f, len(es), got, junk)
		entries = append(entries, es...)
		addSource(got)
		names = append(names, baseName(f))
	}
	if capture != "" {
		fmt.Fprintf(stderr, "WARNING: MONITOR makes %s print every command to this process and can cut its throughput by half or more.\n"+
			"         The capture stops after %v or %d commands. Do not run it on a production server you cannot afford to slow.\n", capture, capDur, capMax)
		var buf bytes.Buffer
		tlsCfg, _ := cfg.Target.TLSConfig()
		n, err := trafficimport.Capture(ctx, capture, trafficimport.CaptureOptions{Username: cfg.Target.Username, Password: cfg.Target.Password, TLS: tlsCfg, MaxCommands: capMax, MaxDuration: capDur}, &buf)
		if err != nil {
			fmt.Fprintln(stderr, "plgv import-traffic:", err)
			return 1
		}
		es, _, err := trafficimport.ParseMonitor(&buf)
		if err != nil {
			fmt.Fprintln(stderr, "plgv import-traffic:", err)
			return 1
		}
		fmt.Fprintf(stdout, "captured %d commands from %s\n", n, capture)
		entries = append(entries, es...)
		addSource("monitor")
		names = append(names, "live capture")
	}
	if slowlogFrom != "" {
		es, err := readSlowlog(ctx, cfg, slowlogFrom, slowN)
		if err != nil {
			fmt.Fprintln(stderr, "plgv import-traffic:", err)
			return 1
		}
		fmt.Fprintf(stdout, "read %d slowlog entries from %s\n", len(es), slowlogFrom)
		entries = append(entries, es...)
		addSource("slowlog")
		names = append(names, "slowlog of live server")
	}

	res := trafficimport.Convert(entries, sources, names, trafficimport.Options{MaxWorkers: maxWorkers, DB: db, AllowDangerous: allowDangerous})
	printImport(stdout, res)
	r, err := trafficimport.Write(out, res, version, compress)
	if err != nil {
		fmt.Fprintln(stderr, "plgv import-traffic:", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nrecording:  %s: %s, %d commands in %d groups, %s on disk\n", out, r.Status, r.Ops, r.Groups, humanBytes(r.FileBytes))
	fmt.Fprintf(stdout, "next:       plgv replay -addr HOST:PORT -reads-only %s   (see docs/traffic-import.md before replaying writes)\n", out)
	return 0
}

func readSlowlog(ctx context.Context, cfg config.Config, addr string, n int) ([]trafficimport.Entry, error) {
	cfg.Target.URIs = splitAddrs(addr)
	addrs, _ := cfg.Target.Addrs()
	tlsCfg, err := cfg.Target.TLSConfig()
	if err != nil {
		return nil, err
	}
	c, err := valkey.NewClient(valkey.LibValkeyGo, valkey.Options{Addrs: addrs, Username: cfg.Target.Username, Password: cfg.Target.Password, Workers: 1, TLS: tlsCfg})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	reply, err := c.Query(ctx, valkey.Cmd{"SLOWLOG", "GET", fmt.Sprint(n)})
	if err != nil {
		return nil, fmt.Errorf("SLOWLOG GET on %s: %w", addr, err)
	}
	rows, _ := reply.([]any)
	return trafficimport.SlowlogEntries(rows)
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func printImport(w io.Writer, r *trafficimport.Result) {
	i := r.Info
	fmt.Fprintf(w, "\nimported:   %d of %d entries, %d connections -> %d workers\n", i.Converted, i.Entries, i.Connections, r.Workers)
	if i.FirstTimestamp != "" {
		fmt.Fprintf(w, "span:       %s to %s\n", i.FirstTimestamp, i.LastTimestamp)
	}
	if i.Skipped > 0 {
		fmt.Fprintf(w, "skipped:    %d\n", i.Skipped)
		var reasons []string
		for k := range i.SkippedByReason {
			reasons = append(reasons, k)
		}
		sort.Strings(reasons)
		for _, k := range reasons {
			fmt.Fprintf(w, "  %6d  %s\n", i.SkippedByReason[k], k)
		}
	}
}
