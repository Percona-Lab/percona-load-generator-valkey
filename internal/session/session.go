// Package session runs one complete benchmark: connect, preload, poll the
// server, run the workload and build the run summary. The CLI and the web UI
// both use it, so a run behaves the same from either.
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/insights"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/loadprofile"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// Phase names reported through Hooks.OnPhase.
const (
	PhaseConnecting = "connecting"
	PhasePreloading = "preloading"
	PhaseRunning    = "running"
	PhaseFinishing  = "finishing"
)

// Hooks are optional callbacks. They are called from the goroutine that runs
// the session, except OnSample (the sampler goroutine).
type Hooks struct {
	OnPhase  func(phase string)
	OnSample func(stats.Sample)
	// OnPoller gives live access to the server poller (Summary is safe to call
	// while it runs; for a cluster it covers every node). Called only when
	// server metrics are enabled.
	OnPoller func(servermetrics.Source)
	// Sweep only: called before each level starts, and with each level's
	// finished outcome as soon as it is ready (so it can be stored at once).
	OnLevel   func(sweepID string, index, total int, kind string, value float64)
	OnOutcome func(*Outcome)
}

// Options are per-run extras.
type Options struct {
	Version string // tool version recorded in the summary
	Label   string // user label recorded in the summary
}

// Outcome is everything a finished run produced.
type Outcome struct {
	Result    *valkey.RunResult
	Server    *servermetrics.Summary // nil when server metrics were off
	Summary   *runsummary.Summary
	Findings  []insights.Finding
	StartedAt time.Time
	// Recording is set when the run was recorded (record.path).
	Recording *workloadrec.Result
}

// Run executes the benchmark described by cfg (validated and normalized).
// Canceling ctx stops the run early; the partial results are still returned.
func Run(ctx context.Context, cfg config.Config, opt Options, h Hooks) (*Outcome, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	phase := func(p string) {
		if h.OnPhase != nil {
			h.OnPhase(p)
		}
	}
	phase(PhaseConnecting)
	c, mode, addrs, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if mode == "cluster" {
		if err := checkClusterMix(cfg); err != nil {
			return nil, err
		}
	}

	// Resolve the seed once so preload and the run agree and can be repeated.
	cfg.Run.Seed = valkey.EffectiveSeed(cfg.Run.Seed)

	if cfg.Dataset.Preload {
		phase(PhasePreloading)
		if err := valkey.Preload(ctx, c, cfg); err != nil {
			return nil, err
		}
	}
	// Lua scripts and functions must exist on the server before they are called.
	if err := valkey.Prepare(ctx, c, cfg.Workload.Mix); err != nil {
		return nil, fmt.Errorf("cannot load the Lua scripts or functions: %w", err)
	}

	var (
		src      servermetrics.Source
		runSrv   func(context.Context)
		router   valkey.Router
		reg      = topology.NewRegistry()
		pollDone = make(chan struct{})
		stopPoll context.CancelFunc
	)
	if cfg.Server.Enabled {
		pcfg := servermetrics.Config{
			Interval: cfg.Server.Interval, Slowlog: cfg.Server.Slowlog,
			SlowlogEntries: cfg.Server.SlowlogEntries, Latency: cfg.Server.Latency,
		}
		switch mode {
		case "cluster":
			ts, ok := c.(topology.Source)
			if !ok {
				return nil, errors.New("the client library cannot read a cluster's topology")
			}
			w, err := topology.NewWatcher(ctx, ts, cfg.Server.Interval, reg)
			if err != nil {
				return nil, fmt.Errorf("cannot read the cluster topology: %w", err)
			}
			var nodeAddrs []string
			for _, n := range w.Result().Initial.Nodes() {
				nodeAddrs = append(nodeAddrs, n.Addr)
			}
			cl := servermetrics.NewCluster(c.(valkey.NodeQuerier), nodeAddrs, pcfg, w, cfg.Target.ReadFromReplicas)
			src, runSrv, router = cl, cl.Run, w.Router()
		case "sentinel":
			sw, err := topology.NewSentinelWatcher(ctx, addrs, cfg.Target.Sentinel.MasterName, cfg.Target.Username, cfg.Target.Password, cfg.Server.Interval)
			if err != nil {
				return nil, err
			}
			ss := servermetrics.NewSentinelSource(servermetrics.New(c, pcfg), sw)
			src, runSrv = ss, ss.Run
		default:
			p := servermetrics.New(c, pcfg)
			src, runSrv = p, p.Run
		}
		var pctx context.Context
		pctx, stopPoll = context.WithCancel(context.Background())
		defer stopPoll()
		if h.OnPoller != nil {
			h.OnPoller(src)
		}
		go func() { defer close(pollDone); runSrv(pctx) }()
	} else {
		close(pollDone)
	}

	var rec *workloadrec.Recorder
	ropt := valkey.RunOptions{OnSample: h.OnSample, Router: router}
	if cfg.Record.Path != "" {
		if rec, err = startRecording(ctx, c, cfg, mode, opt.Version); err != nil {
			if stopPoll != nil {
				stopPoll()
			}
			return nil, err
		}
		ropt.Tap, ropt.CaptureReplies, ropt.OnStart = valkey.RecordTap(rec), cfg.Record.Checksums, rec.SetStart
	}
	phase(PhaseRunning)
	startedAt := time.Now()
	res, err := valkey.RunWith(ctx, c, cfg, ropt)
	var recRes *workloadrec.Result
	if rec != nil {
		r := rec.Close()
		recRes = &r
	}
	if err == nil {
		res.NodeAddrs = reg.Addrs()
		res.Mode = mode
	}
	phase(PhaseFinishing)
	var srv *servermetrics.Summary
	if src != nil {
		stopPoll()
		<-pollDone
		sm := src.Summary()
		srv = &sm
	}
	if err != nil {
		return nil, err
	}
	var ks *keyscan.Result
	if cfg.KeySpace.Enabled && ctx.Err() == nil {
		ks = analyzeKeySpace(ctx, c, cfg, mode)
	}
	sum, found := runsummary.BuildWith(opt.Version, cfg, res, srv, ks, startedAt)
	sum.Label = opt.Label
	sum.Stopped = ctx.Err() != nil
	return &Outcome{Result: res, Server: srv, Summary: sum, Findings: found, StartedAt: startedAt, Recording: recRes}, nil
}

// connect opens the client, checks the server answers and resolves the mode
// (standalone, cluster or sentinel). The caller closes the client.
func connect(ctx context.Context, cfg config.Config) (valkey.Client, string, []string, error) {
	addrs, _ := cfg.Target.Addrs()
	mode := cfg.Target.Mode
	vopt := valkey.Options{
		Addrs: addrs, Workers: cfg.Run.Concurrency, Username: cfg.Target.Username, Password: cfg.Target.Password,
		ReadFromReplicas: cfg.Target.ReadFromReplicas, CaptureReplies: cfg.Record.Checksums,
	}
	tlsCfg, err := cfg.Target.TLSConfig()
	if err != nil {
		return nil, "", nil, err
	}
	vopt.TLS = tlsCfg
	if mode == "sentinel" {
		vopt.SentinelMaster = cfg.Target.Sentinel.MasterName
	}
	c, err := valkey.NewClient(cfg.Target.Client, vopt)
	if err != nil {
		return nil, "", nil, fmt.Errorf("cannot connect to %s: %w%s", strings.Join(addrs, ", "), err, tlsHint(cfg.Target.TLSEnabled(), err))
	}
	// Fail early with a clear message if the server cannot be reached.
	if _, err := c.Query(ctx, valkey.Cmd{"PING"}); err != nil {
		c.Close()
		return nil, "", nil, fmt.Errorf("cannot reach the server at %s: %w%s", strings.Join(addrs, ", "), err, tlsHint(cfg.Target.TLSEnabled(), err))
	}
	if mode != "sentinel" {
		if mode, err = detectMode(ctx, c, mode, cfg.Target.ReadFromReplicas); err != nil {
			c.Close()
			return nil, "", nil, err
		}
	}
	return c, mode, addrs, nil
}

// runOne runs a single level; tests replace it.
var runOne = Run

// Sweep runs the workload once per level of cfg.Run.Sweep (worker counts or
// target rates), one after another, and returns every finished level's outcome.
// A level that ends early (ctx canceled) is returned too and ends the sweep.
// Every level starts from the same dataset when preload is on.
func Sweep(ctx context.Context, cfg config.Config, opt Options, h Hooks) ([]*Outcome, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	sw := cfg.Run.Sweep
	if sw.Kind != "workers" && sw.Kind != "rate" {
		return nil, fmt.Errorf("run.sweep.kind is %q: nothing to sweep", sw.Kind)
	}
	base := opt.Label
	started := time.Now()
	if base == "" {
		base = "Sweep " + started.Format("Jan 2 15:04")
	}
	id := "sw" + started.Format("20060102150405")
	var outs []*Outcome
	for i, v := range sw.Values {
		if ctx.Err() != nil {
			break
		}
		if i > 0 && sw.Pause > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(sw.Pause):
			}
			if ctx.Err() != nil {
				break
			}
		}
		lvl := cfg
		lvl.Run.Sweep = config.Sweep{Kind: "none", Values: []float64{}}
		label := ""
		switch sw.Kind {
		case "workers":
			lvl.Run.Concurrency = int(v)
			label = fmt.Sprintf("%s - %d workers", base, int(v))
		case "rate":
			lvl.Run.TargetOpsPerSec = v
			label = fmt.Sprintf("%s - %.0f ops/s", base, v)
		}
		if h.OnLevel != nil {
			h.OnLevel(id, i+1, len(sw.Values), sw.Kind, v)
		}
		o := opt
		o.Label = label
		out, err := runOne(ctx, lvl, o, h)
		if err != nil {
			return outs, fmt.Errorf("sweep level %d of %d (%s): %w", i+1, len(sw.Values), label, err)
		}
		out.Summary.Sweep = &runsummary.SweepInfo{ID: id, Kind: sw.Kind, Index: i + 1, Total: len(sw.Values), Value: v}
		outs = append(outs, out)
		if h.OnOutcome != nil {
			h.OnOutcome(out)
		}
	}
	return outs, nil
}

// detectMode returns "standalone" or "cluster" by asking the server, and checks
// it against what the configuration asked for.
func detectMode(ctx context.Context, c valkey.Client, want string, readReplicas bool) (string, error) {
	reply, err := c.Query(ctx, valkey.Cmd{"INFO", "cluster"})
	if err != nil {
		return "", fmt.Errorf("cannot read the server's cluster setting: %w", err)
	}
	text, _ := reply.(string)
	cluster := strings.Contains(text, "cluster_enabled:1")
	switch {
	case want == "cluster" && !cluster:
		return "", errors.New("target.mode is cluster, but the server is not running in cluster mode")
	case want == "standalone" && cluster:
		return "", errors.New("the server is a cluster node; set target.mode to cluster (or auto) to load a cluster")
	case cluster:
		return "cluster", nil
	}
	if readReplicas {
		return "", errors.New("target.read_from_replicas needs a cluster or sentinel target; this server is standalone")
	}
	return "standalone", nil
}

// checkClusterMix rejects multi-key commands that would fail with CROSSSLOT
// (the same rule as configuration validation, for targets found by auto mode).
func checkClusterMix(cfg config.Config) error {
	if cfg.Dataset.HashTagGroups > 0 {
		return nil
	}
	for i, it := range cfg.Workload.Mix {
		if (it.Op == "MGET" || it.Op == "MSET") && it.Count != 1 {
			return fmt.Errorf("workload.mix[%d]: %s touches several keys, which fail with CROSSSLOT in a cluster unless they share a slot; set dataset.hash_tag_groups >= 1 (or count: 1)", i, it.Op)
		}
	}
	return nil
}

// scanSource is one server a read-only scan can query (a cluster has one per primary).
type scanSource struct {
	name  string
	query func(ctx context.Context, cmd []string) (any, error)
}

// scanSources lists the servers that hold data: the one server, or every
// primary of a cluster.
func scanSources(ctx context.Context, c valkey.Client, mode string) ([]scanSource, error) {
	q := func(ctx context.Context, cmd []string) (any, error) { return c.Query(ctx, valkey.Cmd(cmd)) }
	if mode != "cluster" {
		return []scanSource{{"server", q}}, nil
	}
	ts, ok := c.(topology.Source)
	if !ok {
		return nil, errors.New("the client library cannot read a cluster's topology")
	}
	snap, err := topology.Read(ctx, ts)
	if err != nil {
		return nil, err
	}
	var out []scanSource
	for _, n := range snap.Primaries() {
		addr := n.Addr
		out = append(out, scanSource{addr, func(ctx context.Context, cmd []string) (any, error) {
			return ts.QueryNode(ctx, addr, valkey.Cmd(cmd))
		}})
	}
	return out, nil
}

// startRecording opens the recording file with a header that describes the
// run, including the data the run starts with.
func startRecording(ctx context.Context, c valkey.Client, cfg config.Config, mode, version string) (*workloadrec.Recorder, error) {
	rc := cfg.Record
	h := workloadrec.Header{
		PLGVVersion: version, Mode: mode, Seed: cfg.Run.Seed, Workers: cfg.Run.Concurrency,
		Outcomes: rc.Outcomes || rc.Checksums, ResultChecksums: rc.Checksums,
		Config: workloadrec.ConfigSummary{Concurrency: cfg.Run.Concurrency, Duration: cfg.Run.Duration.String(), Mix: mixSummary(cfg),
			ValueSize: cfg.Dataset.ValueSize, KeyCount: cfg.Dataset.KeyCount},
	}
	if sched, err := loadprofile.Compile(cfg.Run.LoadProfile, cfg.Run.Concurrency); err == nil {
		h.Config.LoadProfile = sched.Summary()
		h.Workers = sched.MaxWorkers() // the runner provisions this many workers
	}
	if v, err := c.Query(ctx, valkey.Cmd{"INFO", "server"}); err == nil {
		h.ServerVersion = serverVersion(v)
	}
	if rc.DataFingerprint {
		srcs, err := scanSources(ctx, c, mode)
		if err != nil {
			return nil, fmt.Errorf("cannot fingerprint the starting data: %w", err)
		}
		fs := make([]datafp.Source, len(srcs))
		for i, s := range srcs {
			fs[i] = datafp.Source{Name: s.name, Query: s.query}
		}
		if h.DataFingerprint, err = datafp.Take(ctx, fs, datafp.Options{}); err != nil {
			return nil, fmt.Errorf("cannot fingerprint the starting data: %w", err)
		}
	}
	comp := rc.Compression
	if comp == "auto" || comp == "" {
		switch {
		case strings.HasSuffix(rc.Path, ".zst"):
			comp = workloadrec.CompressionZstd
		case strings.HasSuffix(rc.Path, ".gz"):
			comp = workloadrec.CompressionGzip
		default:
			comp = workloadrec.CompressionNone
		}
	}
	return workloadrec.NewRecorder(rc.Path, workloadrec.Options{MaxBytes: int64(rc.MaxMB) << 20, Backpressure: rc.Backpressure, Compression: comp}, h)
}

func mixSummary(cfg config.Config) string {
	var parts []string
	for _, it := range cfg.Workload.Mix {
		parts = append(parts, fmt.Sprintf("%s %d", it.Op, it.Weight))
	}
	return strings.Join(parts, ", ")
}

// serverVersion reads valkey_version (or redis_version) from INFO server text.
func serverVersion(reply any) string {
	text, _ := reply.(string)
	var redis string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "valkey_version:"); ok {
			return v
		}
		if v, ok := strings.CutPrefix(line, "redis_version:"); ok {
			redis = v
		}
	}
	return redis
}

// keyScanTimeout bounds the key-space analysis so a huge server cannot hold a run open.
const keyScanTimeout = 5 * time.Minute

// analyzeKeySpace samples the key space after the run. It never fails the run: a
// problem is recorded in the result instead (Sampled 0, Errors 1, FirstError set).
func analyzeKeySpace(ctx context.Context, c valkey.Client, cfg config.Config, mode string) *keyscan.Result {
	ctx, cancel := context.WithTimeout(ctx, keyScanTimeout)
	defer cancel()
	srcs, err := scanSources(ctx, c, mode)
	if err != nil {
		return keyScanFailed(cfg, err)
	}
	sources := make([]keyscan.Source, len(srcs))
	for i, sc := range srcs {
		sources[i] = keyscan.Source{Name: sc.name, Query: sc.query}
	}
	q := func(ctx context.Context, cmd []string) (any, error) { return c.Query(ctx, valkey.Cmd(cmd)) }
	r, err := keyscan.Analyze(ctx, q, sources, keyscan.Options{
		Samples: cfg.KeySpace.Samples, Top: cfg.KeySpace.Top, Match: cfg.KeySpace.MatchPattern(cfg.Dataset),
	})
	if err != nil {
		return keyScanFailed(cfg, err)
	}
	return r
}

func keyScanFailed(cfg config.Config, err error) *keyscan.Result {
	return &keyscan.Result{Requested: cfg.KeySpace.Samples, Match: cfg.KeySpace.MatchPattern(cfg.Dataset), Errors: 1, FirstError: err.Error(),
		Types: []keyscan.TypeStat{}, Big: []keyscan.BigKey{}}
}

// tlsHint adds a pointer when the error looks like a TLS mismatch.
func tlsHint(tlsOn bool, err error) string {
	msg := err.Error()
	switch {
	case !tlsOn && (strings.HasSuffix(msg, "EOF") || strings.Contains(msg, "connection reset")):
		return " (the server may require TLS: try -tls or a valkeys:// address)"
	case tlsOn && (strings.Contains(msg, "first record does not look like a TLS handshake") || strings.Contains(msg, "deadline exceeded")):
		return " (the server does not speak TLS on this port: remove -tls or use the server's TLS port)"
	}
	return ""
}
