// Package runsummary builds the JSON record of a finished run: configuration,
// client and server results, time series and findings. Trends and run history
// (phase 4) read this file.
package runsummary

import (
	"encoding/json"
	"os"
	"runtime"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/insights"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyscan"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// SchemaVersion is bumped when the JSON layout changes incompatibly.
const SchemaVersion = 1

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Summary is the stored run record. Durations are milliseconds.
type Summary struct {
	SchemaVersion int        `json:"schema_version"`
	Tool          string     `json:"tool"`
	ToolVersion   string     `json:"tool_version"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    time.Time  `json:"finished_at"`
	Seed          int64      `json:"seed"`
	Label         string     `json:"label,omitempty"`
	Stopped       bool       `json:"stopped,omitempty"` // ended early by the user
	Fingerprint   string     `json:"fingerprint"`       // workload shape; equal = comparable
	Sweep         *SweepInfo `json:"sweep,omitempty"`   // set when this run is one level of a sweep
	ConfigYAML    string     `json:"config_yaml"`       // never contains credentials

	Client Client  `json:"client"`
	Series []Point `json:"series"`
	PerOp  []Op    `json:"per_op"`
	Server *Server `json:"server,omitempty"`
	// ServerConfig is the server's settings (CONFIG GET *); present only when server_config was on.
	ServerConfig *serverconfig.Snapshot `json:"server_config,omitempty"`
	// KeySpace is the key-space analysis, present when keyspace_analysis was on.
	KeySpace *keyscan.Result `json:"keyspace,omitempty"`
	// Traffic is the traffic fingerprint of a deterministic run.
	Traffic  *TrafficInfo `json:"traffic,omitempty"`
	Findings []Finding    `json:"findings"`
	// Recording describes the recording this run wrote (record.path). It never
	// holds a command.
	Recording *RecordingOut `json:"recording,omitempty"`
}

// RecordingOut is the result of recording a run.
type RecordingOut struct {
	Path         string  `json:"path"`
	Name         string  `json:"name"`
	Status       string  `json:"status"` // complete, truncated or failed
	Reason       string  `json:"reason,omitempty"`
	Ops          uint64  `json:"ops"`
	Bytes        int64   `json:"bytes"`      // uncompressed
	FileBytes    int64   `json:"file_bytes"` // on disk
	Compression  string  `json:"compression"`
	Backpressure string  `json:"backpressure"`
	Waits        uint64  `json:"waits,omitempty"` // times a worker waited for the writer (block)
	WaitTotalMs  float64 `json:"wait_total_ms,omitempty"`
	WaitMaxMs    float64 `json:"wait_max_ms,omitempty"`
	Outcomes     bool    `json:"outcomes,omitempty"`
	Checksums    bool    `json:"checksums,omitempty"`
}

// SweepInfo ties a run to the sweep it belongs to.
type SweepInfo struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`  // workers | rate
	Index int     `json:"index"` // 1-based
	Total int     `json:"total"`
	Value float64 `json:"value"` // worker count, or target ops/s
}

// Client is the load generator's view.
type Client struct {
	Ops                 uint64            `json:"ops"`
	Errors              uint64            `json:"errors"`
	Misses              uint64            `json:"misses"`
	ErrorClasses        map[string]uint64 `json:"error_classes,omitempty"`
	FirstError          string            `json:"first_error,omitempty"`
	ElapsedSeconds      float64           `json:"elapsed_seconds"`
	OpsPerSec           float64           `json:"ops_per_sec"`
	MeanMs              float64           `json:"mean_ms"`
	P50Ms               float64           `json:"p50_ms"`
	P95Ms               float64           `json:"p95_ms"`
	P99Ms               float64           `json:"p99_ms"`
	P999Ms              float64           `json:"p999_ms"`
	MaxMs               float64           `json:"max_ms"`
	CPUCores            float64           `json:"cpu_cores"`
	CoresPer100k        float64           `json:"cores_per_100k_ops"`
	TargetOpsPerSec     float64           `json:"target_ops_per_sec,omitempty"`
	CoordinatedOmission bool              `json:"coordinated_omission,omitempty"`
	SeriesFactor        int               `json:"series_factor"`
	Loop                string            `json:"loop"`
	LoadProfile         string            `json:"load_profile"`
	PeakWorkers         int               `json:"peak_workers"`
	DroppedOps          uint64            `json:"dropped_ops,omitempty"`        // open loop: offered but never sent
	TargetMode          string            `json:"target_mode"`                  // standalone | cluster | sentinel
	Nodes               []ClientNode      `json:"nodes,omitempty"`              // cluster: client operations per node
	PubSub              *PubSubOut        `json:"pubsub,omitempty"`             // runs with subscribers
	Dangerous           []string          `json:"dangerous_commands,omitempty"` // blocked-by-default commands the run was allowed
}

// PubSubOut is the delivery result of a run with subscribers.
type PubSubOut struct {
	Subscribers   int     `json:"subscribers"`
	Channels      int     `json:"channels"`
	Sharded       bool    `json:"sharded"`
	Published     uint64  `json:"published"`
	Expected      uint64  `json:"expected"`
	Delivered     uint64  `json:"delivered"`
	DeliveryRatio float64 `json:"delivery_ratio"`
	MeanMs        float64 `json:"mean_ms"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
	MaxMs         float64 `json:"max_ms"`
}

// ClientNode is the load one cluster node received, as plgv counted it.
type ClientNode struct {
	Addr     string  `json:"addr"`
	Ops      uint64  `json:"ops"`
	Errors   uint64  `json:"errors"`
	SharePct float64 `json:"share_pct"` // of the operations that could be attributed to a node
}

// Point is one chart point of the client series.
type Point struct {
	OffsetSeconds float64  `json:"offset_s"`
	OpsPerSec     float64  `json:"ops_per_sec"`
	Errors        uint64   `json:"errors"`
	P50Ms         float64  `json:"p50_ms"`
	P95Ms         float64  `json:"p95_ms"`
	P99Ms         float64  `json:"p99_ms"`
	MaxMs         float64  `json:"max_ms"`
	Workers       int      `json:"workers"`
	Dropped       uint64   `json:"dropped,omitempty"`
	NodeOps       []uint64 `json:"node_ops,omitempty"` // cluster: operations per node, indexed like client.nodes
	Delivered     uint64   `json:"delivered,omitempty"`
	DeliveryP50Ms float64  `json:"delivery_p50_ms,omitempty"`
	DeliveryP99Ms float64  `json:"delivery_p99_ms,omitempty"`
}

// Op is one mix entry's totals.
type Op struct {
	Op     string  `json:"op"`
	Ops    uint64  `json:"ops"`
	Errors uint64  `json:"errors"`
	Misses uint64  `json:"misses"`
	P50Ms  float64 `json:"p50_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
}

// Server is the server's view; present only when server metrics were on.
type Server struct {
	Version             string            `json:"version"`
	Role                string            `json:"role"`
	Mode                string            `json:"mode"`
	MaxMemory           int64             `json:"maxmemory"`
	EvictionPolicy      string            `json:"maxmemory_policy"`
	ServerOps           int64             `json:"server_ops"`
	HitRatio            float64           `json:"hit_ratio"` // -1 = no lookups
	EvictedKeys         int64             `json:"evicted_keys"`
	ExpiredKeys         int64             `json:"expired_keys"`
	RejectedConnections int64             `json:"rejected_connections"`
	UsedMemoryMax       int64             `json:"used_memory_max"`
	MaxBlockedClients   int64             `json:"max_blocked_clients"`
	MaxFragRatio        float64           `json:"max_fragmentation_ratio"`
	MaxForkUsec         int64             `json:"max_fork_usec"`
	ReplicaLagBytes     int64             `json:"replica_lag_bytes"`
	SlowlogNew          int64             `json:"slowlog_new"`
	SlowlogWorstMs      float64           `json:"slowlog_worst_ms"`
	Slowlog             []SlowEntry       `json:"slowlog,omitempty"`
	LatencyEvents       []LatencyEvent    `json:"latency_events,omitempty"`
	ErrorStats          map[string]uint64 `json:"error_stats,omitempty"`
	PollErrors          int64             `json:"poll_errors"`
	FirstPollError      string            `json:"first_poll_error,omitempty"`
	Samples             []ServerPoint     `json:"samples"`
	SampleStride        int               `json:"sample_stride"`
	ServerOpsExact      bool              `json:"server_ops_exact"`
	Nodes               []ServerNode      `json:"nodes,omitempty"`
	Topology            *TopologyOut      `json:"topology,omitempty"`
}

// ServerNode is one cluster node as the server reported it.
type ServerNode struct {
	Addr          string        `json:"addr"`
	Role          string        `json:"role"`
	Slots         int           `json:"slots"`
	ServerOps     int64         `json:"server_ops"`
	UsedMemoryMax int64         `json:"used_memory_max"`
	HitRatio      float64       `json:"hit_ratio"`
	EvictedKeys   int64         `json:"evicted_keys"`
	SlowlogNew    int64         `json:"slowlog_new"`
	Samples       []ServerPoint `json:"samples"`
}

// TopologyOut is what was learned about a cluster or sentinel deployment.
type TopologyOut struct {
	Mode        string     `json:"mode"`
	Shards      []ShardOut `json:"shards"`
	Events      []EventOut `json:"events"`
	ClusterDown bool       `json:"cluster_down"`
	PollErrors  int64      `json:"poll_errors"`
	FirstError  string     `json:"first_error,omitempty"`
}

// ShardOut is one shard at the end of the run.
type ShardOut struct {
	Slots    int      `json:"slots"`
	Primary  string   `json:"primary"`
	Replicas []string `json:"replicas"`
}

// EventOut is a failover, reshard or similar change during the run.
type EventOut struct {
	OffsetSeconds float64 `json:"offset_s"`
	Kind          string  `json:"kind"`
	Detail        string  `json:"detail"`
}

// SlowEntry is one slowlog entry.
type SlowEntry struct {
	ID         int64   `json:"id"`
	UnixTime   int64   `json:"unix_time"`
	DurationMs float64 `json:"duration_ms"`
	Command    string  `json:"command"`
}

// LatencyEvent is one LATENCY LATEST row.
type LatencyEvent struct {
	Name     string `json:"name"`
	LatestMs int64  `json:"latest_ms"`
	MaxMs    int64  `json:"max_ms"`
}

// ServerPoint is one polled gauge point.
type ServerPoint struct {
	OffsetSeconds float64 `json:"offset_s"`
	OpsPerSec     float64 `json:"ops_per_sec"`
	UsedMemory    int64   `json:"used_memory"`
	Connections   int64   `json:"connected_clients"`
	Blocked       int64   `json:"blocked_clients"`
	HitRatio      float64 `json:"hit_ratio"`
	NetInKBps     float64 `json:"net_in_kbps"`
	NetOutKBps    float64 `json:"net_out_kbps"`
	FragRatio     float64 `json:"fragmentation_ratio"`
}

// Finding is one insight.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Evidence string `json:"evidence"`
	Advice   string `json:"advice"`
}

// Build assembles the summary and evaluates the insights. server may be nil.
func Build(version string, cfg config.Config, res *valkey.RunResult, srv *servermetrics.Summary, started time.Time) (*Summary, []insights.Finding) {
	return BuildWith(version, cfg, res, srv, nil, started)
}

// BuildWith is Build with the key-space analysis of the run (nil when it did not run).
func BuildWith(version string, cfg config.Config, res *valkey.RunResult, srv *servermetrics.Summary, ks *keyscan.Result, started time.Time) (*Summary, []insights.Finding) {
	yml, _ := cfg.Marshal()
	h := res.Hist
	cores := 0.0
	if res.Elapsed > 0 {
		cores = res.CPU.Seconds() / res.Elapsed.Seconds()
	}
	s := &Summary{
		SchemaVersion: SchemaVersion, Tool: "plgv", ToolVersion: version,
		StartedAt: started, FinishedAt: started.Add(res.Elapsed), Seed: res.Seed, ConfigYAML: string(yml),
		Fingerprint: cfg.Fingerprint(),
		Client: Client{
			Ops: res.Ops, Errors: res.Errors, Misses: res.Misses, ErrorClasses: res.ErrClasses, FirstError: res.FirstError,
			ElapsedSeconds: res.Elapsed.Seconds(), OpsPerSec: res.OpsPerSec(),
			MeanMs: ms(h.Mean()), P50Ms: ms(h.Quantile(0.5)), P95Ms: ms(h.Quantile(0.95)),
			P99Ms: ms(h.Quantile(0.99)), P999Ms: ms(h.Quantile(0.999)), MaxMs: ms(h.Max()),
			CPUCores: cores, CoresPer100k: res.CoresPer100k(),
			TargetOpsPerSec: res.TargetOpsPerSec, CoordinatedOmission: res.CoordinatedOmission,
			SeriesFactor: res.SeriesFactor, TargetMode: modeOf(res.Mode), Loop: res.Loop, LoadProfile: res.LoadProfile, PeakWorkers: res.PeakWorkers, DroppedOps: res.Dropped,
		},
	}
	var attributed uint64
	for _, n := range res.PerNode {
		attributed += n.Ops
	}
	for i, n := range res.PerNode {
		if i >= len(res.NodeAddrs) {
			break
		}
		share := 0.0
		if attributed > 0 {
			share = float64(n.Ops) / float64(attributed) * 100
		}
		s.Client.Nodes = append(s.Client.Nodes, ClientNode{Addr: res.NodeAddrs[i], Ops: n.Ops, Errors: n.Errors, SharePct: share})
	}
	for _, p := range res.Series {
		pt := Point{
			OffsetSeconds: p.Offset.Seconds(), OpsPerSec: p.OpsPerSec(), Errors: p.Errors,
			P50Ms: ms(p.P50), P95Ms: ms(p.P95), P99Ms: ms(p.P99), MaxMs: ms(p.Max), Workers: p.Workers, Dropped: p.Dropped,
		}
		pt.Delivered, pt.DeliveryP50Ms, pt.DeliveryP99Ms = p.Delivered, ms(p.DeliveryP50), ms(p.DeliveryP99)
		for i := range s.Client.Nodes {
			var ops uint64
			if i < len(p.Nodes) {
				ops = p.Nodes[i].Ops
			}
			pt.NodeOps = append(pt.NodeOps, ops)
		}
		s.Series = append(s.Series, pt)
	}
	for _, o := range res.PerOp {
		s.PerOp = append(s.PerOp, Op{Op: o.Op, Ops: o.Ops, Errors: o.Errors, Misses: o.Misses,
			P50Ms: ms(o.P50), P99Ms: ms(o.P99), MaxMs: ms(o.Max)})
	}
	if ps := res.PubSub; ps != nil {
		out := &PubSubOut{Subscribers: ps.Subscribers, Channels: ps.Channels, Sharded: ps.Sharded, Published: ps.Published, Expected: ps.Expected, Delivered: ps.Delivered}
		if ps.Expected > 0 {
			out.DeliveryRatio = float64(ps.Delivered) / float64(ps.Expected)
		}
		if dh := res.DeliveryHist; dh != nil && dh.Count() > 0 {
			out.MeanMs, out.P50Ms, out.P95Ms, out.P99Ms, out.MaxMs = ms(dh.Mean()), ms(dh.Quantile(0.5)), ms(dh.Quantile(0.95)), ms(dh.Quantile(0.99)), ms(dh.Max())
		}
		s.Client.PubSub = out
	}
	danger := dangerousIn(cfg)
	s.Client.Dangerous = danger
	if srv != nil {
		s.Server = buildServer(srv)
	}
	s.KeySpace = ks
	if res.TrafficHash != "" {
		s.Traffic = &TrafficInfo{Hash: res.TrafficHash, Commands: res.TrafficOps, Seed: res.Seed, OpBudget: cfg.Run.OpBudget}
	}
	found := insights.Evaluate(insights.Input{
		Client: insights.Client{
			Ops: res.Ops, Errors: res.Errors, Misses: res.Misses, ErrClasses: res.ErrClasses,
			P50: h.Quantile(0.5), P99: h.Quantile(0.99), Elapsed: res.Elapsed, CPUCores: cores,
			GOMAXPROCS: runtime.GOMAXPROCS(0), TargetOpsPerSec: res.TargetOpsPerSec,
			CoordinatedOmission: res.CoordinatedOmission, Dropped: res.Dropped, Loop: res.Loop, NodeOps: nodeOps(res),
			NodeAddrs: res.NodeAddrs, Mode: res.Mode, PerOp: opCounts(res), PubSub: pubsubCount(res), DangerousAllowed: danger,
		},
		Server:   srv,
		KeySpace: keySpaceIn(ks),
	})
	for _, f := range found {
		s.Findings = append(s.Findings, Finding{ID: f.ID, Severity: f.Severity.String(), Title: f.Title, Evidence: f.Evidence, Advice: f.Advice})
	}
	if s.Findings == nil {
		s.Findings = []Finding{}
	}
	return s, found
}

// dangerousIn lists the blocked-by-default commands a configuration used because
// workload.allow_dangerous was on.
func dangerousIn(cfg config.Config) []string {
	if !cfg.Workload.AllowDangerous {
		return nil
	}
	var ts []*rawcmd.Template
	for _, it := range cfg.Workload.Mix {
		if it.Op == "RAW" {
			if t, err := rawcmd.Parse(it.Command); err == nil {
				ts = append(ts, t)
			}
		}
	}
	return rawcmd.Dangerous(ts)
}

func opCounts(res *valkey.RunResult) []insights.OpCount {
	out := make([]insights.OpCount, len(res.PerOp))
	for i, o := range res.PerOp {
		out[i] = insights.OpCount{Name: o.Op, Ops: o.Ops, Misses: o.Misses}
	}
	return out
}

func pubsubCount(res *valkey.RunResult) *insights.PubSubCount {
	if res.PubSub == nil {
		return nil
	}
	return &insights.PubSubCount{Subscribers: res.PubSub.Subscribers, Published: res.PubSub.Published, Expected: res.PubSub.Expected, Delivered: res.PubSub.Delivered}
}

func modeOf(m string) string {
	if m == "" {
		return "standalone"
	}
	return m
}

func nodeOps(res *valkey.RunResult) []uint64 {
	out := make([]uint64, len(res.PerNode))
	for i, n := range res.PerNode {
		out[i] = n.Ops
	}
	return out
}

// BuildServer converts the server's view of a run (a single server, a cluster or
// a sentinel deployment) into its stored form. Used by the live view as well.
func BuildServer(m *servermetrics.Summary) *Server { return buildServer(m) }

func buildServer(m *servermetrics.Summary) *Server {
	out := &Server{
		Version: m.Version, Role: m.Role, Mode: m.Mode, MaxMemory: m.MaxMemory, EvictionPolicy: m.EvictionPolicy,
		ServerOps: m.ServerOps, HitRatio: m.HitRatio, EvictedKeys: m.EvictedKeys, ExpiredKeys: m.ExpiredKeys,
		RejectedConnections: m.RejectedConnections, UsedMemoryMax: m.UsedMemoryMax, MaxBlockedClients: m.MaxBlockedConns,
		MaxFragRatio: m.MaxFragRatio, MaxForkUsec: m.MaxForkUsec, ReplicaLagBytes: m.ReplicaLagBytes,
		SlowlogNew: m.SlowlogNew, SlowlogWorstMs: ms(m.SlowlogWorst), ErrorStats: m.ErrorStats,
		PollErrors: m.PollErrors, FirstPollError: m.FirstError, SampleStride: m.SampleStride,
	}
	for _, e := range m.SlowlogSample {
		out.Slowlog = append(out.Slowlog, SlowEntry{ID: e.ID, UnixTime: e.Time.Unix(), DurationMs: ms(e.Duration), Command: e.Command})
	}
	for _, e := range m.LatencyEvents {
		out.LatencyEvents = append(out.LatencyEvents, LatencyEvent{Name: e.Name, LatestMs: e.LatestMs, MaxMs: e.MaxMs})
	}
	out.ServerOpsExact = m.ServerOpsExact
	for _, nd := range m.Nodes {
		sn := ServerNode{Addr: nd.Addr, Role: nd.Role, Slots: nd.Slots, ServerOps: nd.Summary.ServerOps, UsedMemoryMax: nd.Summary.UsedMemoryMax,
			HitRatio: nd.Summary.HitRatio, EvictedKeys: nd.Summary.EvictedKeys, SlowlogNew: nd.Summary.SlowlogNew}
		for _, p := range nd.Summary.Samples {
			sn.Samples = append(sn.Samples, serverPoint(p))
		}
		out.Nodes = append(out.Nodes, sn)
	}
	if t := m.Topology; t != nil {
		to := &TopologyOut{Mode: t.Mode, ClusterDown: t.ClusterDown, PollErrors: t.PollErrors, FirstError: t.FirstError, Shards: []ShardOut{}, Events: []EventOut{}}
		for _, sh := range t.Shards {
			reps := sh.Replicas
			if reps == nil {
				reps = []string{}
			}
			to.Shards = append(to.Shards, ShardOut{Slots: sh.Slots, Primary: sh.Primary, Replicas: reps})
		}
		for _, e := range t.Events {
			to.Events = append(to.Events, EventOut{OffsetSeconds: e.Offset.Seconds(), Kind: e.Kind, Detail: e.Detail})
		}
		out.Topology = to
	}
	for _, p := range m.Samples {
		out.Samples = append(out.Samples, ServerPoint{
			OffsetSeconds: p.Offset.Seconds(), OpsPerSec: p.OpsPerSec, UsedMemory: p.UsedMemory,
			Connections: p.ConnectedConns, Blocked: p.BlockedConns, HitRatio: p.HitRatio,
			NetInKBps: p.NetInKBps, NetOutKBps: p.NetOutKBps, FragRatio: p.FragRatio,
		})
	}
	return out
}

// Write saves the summary as indented JSON (mode 0600).
func (s *Summary) Write(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Read loads a summary file.
func Read(path string) (*Summary, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func serverPoint(p servermetrics.Sample) ServerPoint {
	return ServerPoint{
		OffsetSeconds: p.Offset.Seconds(), OpsPerSec: p.OpsPerSec, UsedMemory: p.UsedMemory,
		Connections: p.ConnectedConns, Blocked: p.BlockedConns, HitRatio: p.HitRatio,
		NetInKBps: p.NetInKBps, NetOutKBps: p.NetOutKBps, FragRatio: p.FragRatio,
	}
}

func keySpaceIn(ks *keyscan.Result) *insights.KeySpace {
	if ks == nil {
		return nil
	}
	out := &insights.KeySpace{Sampled: ks.Sampled, Complete: ks.Complete, MaxBytes: ks.MaxBytes}
	if len(ks.Big) > 0 {
		out.TopKey, out.TopType, out.TopElement = ks.Big[0].Key, ks.Big[0].Type, ks.Big[0].Elements
	}
	out.BigKeys = ks.OverMiB
	return out
}

// TrafficInfo identifies the commands a deterministic run sent: the same seed,
// op budget and workload always give the same hash, on any server.
type TrafficInfo struct {
	Hash     string `json:"hash"`
	Commands uint64 `json:"commands"`
	Seed     int64  `json:"seed"`
	OpBudget int64  `json:"op_budget"`
}
