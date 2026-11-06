package servermetrics

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/topology"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// nodeQuerier adapts a cluster client to the Querier of one node.
type nodeQuerier struct {
	nq   valkey.NodeQuerier
	addr string
}

func (n nodeQuerier) Query(ctx context.Context, cmd valkey.Cmd) (any, error) {
	return n.nq.QueryNode(ctx, n.addr, cmd)
}

// Cluster polls every node of a cluster with its own Poller, and (optionally)
// watches the topology for failovers and reshards.
type Cluster struct {
	order   []string
	pollers map[string]*Poller
	watcher *topology.Watcher
	// replicaReads: the workload read from replicas, so replicas are part of the
	// totals and the operation count cannot be compared with the client's exactly.
	replicaReads bool
}

// NewCluster creates one poller per node address. watcher may be nil.
func NewCluster(nq valkey.NodeQuerier, addrs []string, cfg Config, w *topology.Watcher, replicaReads bool) *Cluster {
	c := &Cluster{pollers: map[string]*Poller{}, watcher: w, replicaReads: replicaReads}
	for _, a := range addrs {
		c.order = append(c.order, a)
		c.pollers[a] = New(nodeQuerier{nq, a}, cfg)
	}
	sort.Strings(c.order)
	return c
}

// Run polls all nodes (and the topology) until ctx ends.
func (c *Cluster) Run(ctx context.Context) {
	start := time.Now()
	var wg sync.WaitGroup
	for _, a := range c.order {
		wg.Add(1)
		go func(p *Poller) { defer wg.Done(); p.Run(ctx) }(c.pollers[a])
	}
	if c.watcher != nil {
		wg.Add(1)
		go func() { defer wg.Done(); c.watcher.Run(ctx, start) }()
	}
	wg.Wait()
}

// Summary returns the merged view plus the per-node and topology detail.
func (c *Cluster) Summary() Summary {
	nodes := make([]NodeSummary, 0, len(c.order))
	subs := make([]Summary, 0, len(c.order))
	slots := map[string]int{}
	var topo *Topology
	if c.watcher != nil {
		res := c.watcher.Result()
		topo = &Topology{Mode: "cluster", ClusterDown: res.ClusterDown, PollErrors: res.PollErrors, FirstError: res.FirstError}
		for _, e := range res.Events {
			topo.Events = append(topo.Events, Event{Offset: e.Offset, Kind: e.Kind, Detail: e.Detail})
		}
		for _, sh := range res.Final.Shards {
			si := ShardInfo{Slots: sh.SlotCount()}
			for _, n := range sh.Nodes {
				if n.Role == "primary" {
					si.Primary = n.Addr
					slots[n.Addr] = sh.SlotCount()
				} else {
					si.Replicas = append(si.Replicas, n.Addr)
				}
			}
			topo.Shards = append(topo.Shards, si)
		}
		sort.Slice(topo.Shards, func(i, j int) bool { return topo.Shards[i].Primary < topo.Shards[j].Primary })
	}
	for _, a := range c.order {
		s := c.pollers[a].Summary()
		role := "primary"
		if s.Role == "slave" || s.Role == "replica" {
			role = "replica"
		}
		nodes = append(nodes, NodeSummary{Addr: a, Role: role, Slots: slots[a], Summary: s})
		// A replica also executes the writes it replicates, so counting it would
		// double the primaries' operations; it is only included when it served reads.
		if role == "primary" || c.replicaReads {
			subs = append(subs, s)
		}
	}
	m := MergeSummaries(subs)
	m.ServerOpsExact = !c.replicaReads
	// After a failover or reshard the roles at the end are not the roles the
	// operations were served under, so the per-role sums are no longer exact.
	if topo != nil && len(topo.Events) > 0 {
		m.ServerOpsExact = false
	}
	m.Mode = "cluster"
	m.Nodes = nodes
	m.Topology = topo
	return m
}

// MergeSummaries adds the per-node summaries into one cluster view: counters
// are summed, peaks use the maximum, the hit ratio is recomputed.
func MergeSummaries(subs []Summary) Summary {
	m := Summary{HitRatio: -1, ErrorStats: map[string]uint64{}}
	lat := map[string]LatencyEvent{}
	maxSamples := 0
	for _, s := range subs {
		if m.Version == "" {
			m.Version = s.Version
			m.EvictionPolicy = s.EvictionPolicy
		}
		m.CommandsDelta += s.CommandsDelta
		m.ServerOps += s.ServerOps
		m.KeyspaceHits += s.KeyspaceHits
		m.KeyspaceMisses += s.KeyspaceMisses
		m.EvictedKeys += s.EvictedKeys
		m.ExpiredKeys += s.ExpiredKeys
		m.RejectedConnections += s.RejectedConnections
		m.NetInputBytes += s.NetInputBytes
		m.NetOutputBytes += s.NetOutputBytes
		m.UsedMemoryEnd += s.UsedMemoryEnd
		m.UsedMemoryMax += s.UsedMemoryMax
		m.MaxMemory += s.MaxMemory
		m.MaxBlockedConns += s.MaxBlockedConns
		m.MaxFragRatio = maxF(m.MaxFragRatio, s.MaxFragRatio)
		m.MaxForkUsec = max(m.MaxForkUsec, s.MaxForkUsec)
		m.ReplicaLagBytes = max(m.ReplicaLagBytes, s.ReplicaLagBytes)
		m.LinkDown = m.LinkDown || s.LinkDown
		m.BgsaveFailed = m.BgsaveFailed || s.BgsaveFailed
		m.SlowlogNew += s.SlowlogNew
		if s.SlowlogWorst > m.SlowlogWorst {
			m.SlowlogWorst = s.SlowlogWorst
		}
		for _, e := range s.SlowlogSample {
			if len(m.SlowlogSample) < 1000 {
				m.SlowlogSample = append(m.SlowlogSample, e)
			}
		}
		for _, e := range s.LatencyEvents {
			if old, ok := lat[e.Name]; !ok || e.MaxMs > old.MaxMs {
				lat[e.Name] = e
			}
		}
		for code, n := range s.ErrorStats {
			m.ErrorStats[code] += n
		}
		m.PollErrors += s.PollErrors
		if m.FirstError == "" {
			m.FirstError = s.FirstError
		}
		m.IOThreads = max(m.IOThreads, s.IOThreads)
		m.SampleStride = max(m.SampleStride, s.SampleStride)
		maxSamples = max(maxSamples, len(s.Samples))
	}
	if t := m.KeyspaceHits + m.KeyspaceMisses; t > 0 {
		m.HitRatio = float64(m.KeyspaceHits) / float64(t)
	}
	for _, e := range lat {
		m.LatencyEvents = append(m.LatencyEvents, e)
	}
	sort.Slice(m.LatencyEvents, func(i, j int) bool { return m.LatencyEvents[i].MaxMs > m.LatencyEvents[j].MaxMs })
	sort.SliceStable(m.SlowlogSample, func(i, j int) bool { return m.SlowlogSample[i].Time.After(m.SlowlogSample[j].Time) })
	// Series: combine the nodes by sample index (they poll on the same interval).
	for i := 0; i < maxSamples; i++ {
		var agg Sample
		var hitSum float64
		var hitN int
		for _, s := range subs {
			if i >= len(s.Samples) {
				continue
			}
			x := s.Samples[i]
			agg.Offset = maxDur(agg.Offset, x.Offset)
			agg.OpsPerSec += x.OpsPerSec
			agg.UsedMemory += x.UsedMemory
			agg.ConnectedConns += x.ConnectedConns
			agg.BlockedConns += x.BlockedConns
			agg.NetInKBps += x.NetInKBps
			agg.NetOutKBps += x.NetOutKBps
			agg.FragRatio = maxF(agg.FragRatio, x.FragRatio)
			if x.HitRatio >= 0 {
				hitSum += x.HitRatio
				hitN++
			}
		}
		agg.HitRatio = -1
		if hitN > 0 {
			agg.HitRatio = hitSum / float64(hitN)
		}
		m.Samples = append(m.Samples, agg)
	}
	return m
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// SentinelSource polls the master through the client (which follows failovers)
// and watches the sentinels for the master moving.
type SentinelSource struct {
	p *Poller
	w *topology.SentinelWatcher
}

// NewSentinelSource combines a poller and a sentinel watcher.
func NewSentinelSource(p *Poller, w *topology.SentinelWatcher) *SentinelSource {
	return &SentinelSource{p: p, w: w}
}

// Run runs both until ctx ends.
func (s *SentinelSource) Run(ctx context.Context) {
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.p.Run(ctx) }()
	go func() { defer wg.Done(); s.w.Run(ctx, start) }()
	wg.Wait()
	s.w.Close()
}

// Summary returns the master's summary with the sentinel topology attached.
func (s *SentinelSource) Summary() Summary {
	sm := s.p.Summary()
	master, events, errs, first := s.w.Result()
	t := &Topology{Mode: "sentinel", PollErrors: errs, FirstError: first, Shards: []ShardInfo{{Primary: master}}}
	for _, e := range events {
		t.Events = append(t.Events, Event{Offset: e.Offset, Kind: e.Kind, Detail: e.Detail})
	}
	sm.Topology = t
	sm.Mode = "sentinel"
	return sm
}
