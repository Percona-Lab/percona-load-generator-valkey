package servermetrics

import (
	"sort"
	"time"
)

// Source is anything that can report the server's view of a run: a single
// poller, or a whole cluster of them. Summary is safe to call while polling.
type Source interface {
	Summary() Summary
}

// Event is something that changed in the topology during the run.
type Event struct {
	Offset time.Duration
	Kind   string // failover | reshard | node-down | node-up | cluster-state
	Detail string
}

// ShardInfo is one shard at the end of the run.
type ShardInfo struct {
	Slots    int
	Primary  string
	Replicas []string
}

// Topology is what was learned about a cluster or sentinel deployment.
type Topology struct {
	Mode        string // cluster | sentinel
	Shards      []ShardInfo
	Events      []Event
	ClusterDown bool // cluster_state was not ok at some point
	PollErrors  int64
	FirstError  string
}

// NodeSummary is one node's own view (cluster: every primary and replica).
type NodeSummary struct {
	Addr    string
	Role    string // primary | replica
	Slots   int
	Summary Summary
}

// Summary is the server's view of a run. For a cluster it is the sum over the
// nodes, with the per-node detail in Nodes.
type Summary struct {
	Version        string
	Role           string
	Mode           string // standalone | cluster | sentinel
	IOThreads      int64
	MaxMemory      int64
	EvictionPolicy string

	// Deltas over the run (last INFO minus first INFO).
	CommandsDelta       int64 // total_commands_processed delta, raw
	ServerOps           int64 // CommandsDelta minus plgv's own admin commands
	ServerOpsExact      bool  // false when replicas are included (replicated writes are counted twice)
	KeyspaceHits        int64
	KeyspaceMisses      int64
	HitRatio            float64 // -1 when there were no lookups
	EvictedKeys         int64
	ExpiredKeys         int64
	RejectedConnections int64
	NetInputBytes       int64
	NetOutputBytes      int64
	ErrorStats          map[string]uint64 // errorstat delta by code

	// Peaks and end values.
	UsedMemoryEnd   int64
	UsedMemoryMax   int64
	MaxBlockedConns int64
	MaxFragRatio    float64
	MaxForkUsec     int64
	ReplicaLagBytes int64
	LinkDown        bool // this node is a replica with master_link_status != up
	BgsaveFailed    bool // rdb_last_bgsave_status is not ok at the end of the run

	// Slowlog and latency monitor.
	SlowlogNew    int64
	SlowlogWorst  time.Duration
	SlowlogSample []SlowEntry
	LatencyEvents []LatencyEvent

	Samples      []Sample
	Nodes        []NodeSummary // cluster: one entry per node, by address
	Topology     *Topology     // cluster or sentinel
	SampleStride int           // raw polls per sample (1 = every poll)
	PollErrors   int64
	FirstError   string
}

// Summary returns the result so far. It is valid after Run returns.
func (p *Poller) Summary() Summary {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Summary{HitRatio: -1, ServerOpsExact: true, PollErrors: p.pollErrors, FirstError: p.firstErr,
		SlowlogNew: p.slowNew, SlowlogWorst: p.slowWorst, SlowlogSample: append([]SlowEntry(nil), p.slow...),
		UsedMemoryMax: p.maxMem, MaxBlockedConns: p.maxBlocked, MaxFragRatio: p.maxFrag,
		MaxForkUsec: p.maxFork, ReplicaLagBytes: p.maxLagB, SampleStride: p.stride,
		ErrorStats: map[string]uint64{}}
	s.Samples = append(s.Samples, p.series...)
	if p.pendingN > 0 {
		s.Samples = append(s.Samples, p.pending)
	}
	for _, e := range p.latency {
		s.LatencyEvents = append(s.LatencyEvents, e)
	}
	sort.Slice(s.LatencyEvents, func(i, j int) bool { return s.LatencyEvents[i].MaxMs > s.LatencyEvents[j].MaxMs })
	if p.first == nil || p.last == nil {
		return s
	}
	f, l := p.first, p.last
	s.Version = l.Version()
	s.Role = l.Str("role")
	s.Mode = l.Str("server_mode")
	if s.Mode == "" && l.Int("cluster_enabled") == 1 {
		s.Mode = "cluster"
	}
	s.EvictionPolicy = l.Str("maxmemory_policy")
	s.MaxMemory = l.Int("maxmemory")
	s.IOThreads = l.Int("io_threads_active")
	s.UsedMemoryEnd = l.Int("used_memory")
	s.CommandsDelta = l.Int("total_commands_processed") - f.Int("total_commands_processed")
	s.ServerOps = s.CommandsDelta - (p.ownAtLast - p.ownAtFirst)
	if s.ServerOps < 0 {
		s.ServerOps = 0
	}
	s.KeyspaceHits = l.Int("keyspace_hits") - f.Int("keyspace_hits")
	s.KeyspaceMisses = l.Int("keyspace_misses") - f.Int("keyspace_misses")
	if t := s.KeyspaceHits + s.KeyspaceMisses; t > 0 {
		s.HitRatio = float64(s.KeyspaceHits) / float64(t)
	}
	s.EvictedKeys = l.Int("evicted_keys") - f.Int("evicted_keys")
	s.ExpiredKeys = l.Int("expired_keys") - f.Int("expired_keys")
	s.RejectedConnections = l.Int("rejected_connections") - f.Int("rejected_connections")
	s.NetInputBytes = l.Int("total_net_input_bytes") - f.Int("total_net_input_bytes")
	s.NetOutputBytes = l.Int("total_net_output_bytes") - f.Int("total_net_output_bytes")
	for code, n := range l.Errors {
		if d := n - f.Errors[code]; d > 0 {
			s.ErrorStats[code] = d
		}
	}
	s.BgsaveFailed = l.Has("rdb_last_bgsave_status") && l.Str("rdb_last_bgsave_status") != "ok"
	s.LinkDown = l.Str("role") == "slave" && l.Str("master_link_status") != "" && l.Str("master_link_status") != "up"
	return s
}
