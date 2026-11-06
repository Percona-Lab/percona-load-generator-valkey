// Package insights turns a run's client and server measurements into ranked,
// explained findings. Every rule is a pure function of its input, so each is
// unit tested on fixture data.
package insights

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/servermetrics"
)

// Severity ranks a finding.
type Severity int

// Severities, most serious first.
const (
	Critical Severity = iota
	Warning
	Info
)

func (s Severity) String() string {
	switch s {
	case Critical:
		return "critical"
	case Warning:
		return "warning"
	}
	return "info"
}

// Thresholds, documented in docs/insights.md.
const (
	MemoryWarnRatio     = 0.90 // used_memory / maxmemory
	MemoryCriticalRatio = 0.98
	LowHitRatio         = 0.50
	MinLookupsForHit    = 1000
	ForkWarnUsec        = 100_000   // 100 ms
	ForkCriticalUsec    = 1_000_000 // 1 s
	FragmentationRatio  = 1.5
	MinMemForFrag       = 100 << 20 // ignore the ratio on tiny datasets
	ReplicaLagWarnBytes = 1 << 20
	ClientCPUFraction   = 0.85 // of GOMAXPROCS
	TargetMissFraction  = 0.95
	TailLatencyFactor   = 10 // p99 > factor * p50
	TailLatencyMin      = time.Millisecond
	CountMismatch       = 0.05 // client vs server op count
	MinRunForMismatch   = 2 * time.Second
	HotNodeFactor       = 2.0 // a node getting this many times its fair share of operations
	MinNodesForHot      = 3
	MinOpsForHot        = 1000
	SlotSkew            = 0.20 // a shard owning this much more or less than the average share of slots
	RedirectWarnFrac    = 0.01 // redirects as a share of operations
	WatchConflictInfo   = 0.05 // share of WATCH_INCR operations aborted
	WatchConflictWarn   = 0.25
	TimeoutShare        = 0.5 // share of blocking reads that found nothing
	DryShare            = 0.8 // share of stream reads that found nothing
	MinOpsForShare      = 100
	BigKeyInfoBytes     = 1 << 20  // a sampled key this large is worth knowing about
	BigKeyWarnBytes     = 10 << 20 // and this large is a risk
	BigKeyElements      = 100000   // elements in one collection
	PubSubLossWarn      = 0.99     // delivered / expected below this is a warning
	PubSubLossCritical  = 0.90
	ErrorCriticalFrac   = 0.01
	DroppedWarnFrac     = 0.005 // share of offered operations dropped before it is worth a finding
	DroppedCriticalFrac = 0.05  // share of offered operations dropped
)

// Client is the client-side view of the run.
type Client struct {
	Ops, Errors, Misses uint64
	ErrClasses          map[string]uint64
	P50, P99            time.Duration
	Elapsed             time.Duration
	CPUCores            float64 // cores used by plgv
	GOMAXPROCS          int
	TargetOpsPerSec     float64
	CoordinatedOmission bool
	Loop                string   // closed or open
	Dropped             uint64   // open loop: operations offered but never sent
	Mode                string   // standalone, cluster or sentinel
	NodeOps             []uint64 // cluster: client operations per node
	NodeAddrs           []string
	PerOp               []OpCount // totals per mix entry
	PubSub              *PubSubCount
	DangerousAllowed    []string // blocked-by-default commands the run was allowed to use
}

// OpCount is one mix entry's totals.
type OpCount struct {
	Name        string
	Ops, Misses uint64
}

// PubSubCount is the delivery result of a run with subscribers.
type PubSubCount struct {
	Subscribers         int
	Published, Expected uint64
	Delivered           uint64
}

func (c Client) opsPerSec() float64 {
	if c.Elapsed <= 0 {
		return 0
	}
	return float64(c.Ops) / c.Elapsed.Seconds()
}

// Input is everything the rules look at. Server is nil when server metrics
// were not collected.
type Input struct {
	Client   Client
	Server   *servermetrics.Summary
	KeySpace *KeySpace // nil when the key-space analysis did not run
}

// KeySpace is what the key-space analysis found, as far as the rules need.
type KeySpace struct {
	Sampled    int
	Complete   bool
	MaxBytes   int64
	TopKey     string
	TopType    string
	TopElement int64
	BigKeys    int // sampled keys of 1 MiB or more
}

// Finding is one result.
type Finding struct {
	ID       string
	Severity Severity
	Title    string
	Evidence string
	Advice   string
}

type rule func(Input) *Finding

var rules = []rule{
	clientErrors, evictionPressure, memoryNearLimit, lowHitRatio, forkStall, slowCommands,
	blockedClients, replicationLag, rejectedConnections, fragmentation, loadGeneratorBound,
	watchConflicts, blockingTimeouts, streamsDry, pubsubLoss, dangerousAllowed, bigKeys, failover, reshard, nodeDown, clusterNotOK, hotNode, unevenSlots, redirects, arrivalsDropped, targetRateMissed, tailLatency, countMismatch, bgsaveFailed, serverMetricsUnavailable,
}

// Evaluate runs every rule and returns the findings, most serious first.
func Evaluate(in Input) []Finding {
	var out []Finding
	for _, r := range rules {
		if f := r(in); f != nil {
			out = append(out, *f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func human(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
		gb = 1 << 30
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GiB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MiB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KiB", float64(n)/kb)
	}
	return fmt.Sprintf("%d B", n)
}

var classAdvice = map[string]string{
	"OOM":         "The server refused writes because it reached maxmemory with a no-eviction policy. Raise maxmemory, set an eviction policy, or use TTLs.",
	"READONLY":    "Writes reached a replica or a read-only node. Point plgv at the primary.",
	"NOAUTH":      "The server requires authentication. Set PLGV_USERNAME and PLGV_PASSWORD.",
	"NOPERM":      "The user lacks permission for a command or key used by the workload. Check the ACL.",
	"LOADING":     "The server was loading its dataset. Wait until it finishes before starting the run.",
	"BUSY":        "The server was running a long script or function. Check for slow scripts.",
	"WRONGTYPE":   "An operation met a key of another type. Do not share key prefixes with other data on this server.",
	"timeout":     "Commands timed out. The server or network is overloaded, or the client timeout is too low.",
	"connection":  "Connections failed or were reset. Check the server, maxclients and the network.",
	"MOVED":       "The server is a cluster node and redirected a key. Cluster mode is not supported in this version; point plgv at a standalone server.",
	"ASK":         "The server is a cluster node and redirected a key. Cluster mode is not supported in this version.",
	"CLUSTERDOWN": "The cluster cannot serve requests. Check cluster state.",
}

func clientErrors(in Input) *Finding {
	c := in.Client
	if c.Errors == 0 {
		return nil
	}
	type kv struct {
		k string
		n uint64
	}
	var cls []kv
	for k, n := range c.ErrClasses {
		cls = append(cls, kv{k, n})
	}
	sort.Slice(cls, func(i, j int) bool { return cls[i].n > cls[j].n || (cls[i].n == cls[j].n && cls[i].k < cls[j].k) })
	var parts []string
	for _, e := range cls {
		parts = append(parts, fmt.Sprintf("%s: %d", e.k, e.n))
	}
	sev := Warning
	frac := float64(c.Errors) / float64(max(c.Ops, 1))
	if frac >= ErrorCriticalFrac {
		sev = Critical
	}
	advice := "Check the first error in the run output."
	if len(cls) > 0 {
		if a, ok := classAdvice[cls[0].k]; ok {
			advice = a
		}
	}
	return &Finding{
		ID: "client-errors", Severity: sev,
		Title:    fmt.Sprintf("%d of %d operations failed (%.2f%%)", c.Errors, c.Ops, frac*100),
		Evidence: strings.Join(parts, ", "), Advice: advice,
	}
}

func evictionPressure(in Input) *Finding {
	s := in.Server
	if s == nil || s.EvictedKeys <= 0 {
		return nil
	}
	return &Finding{
		ID: "eviction-pressure", Severity: Warning,
		Title:    fmt.Sprintf("The server evicted %d keys during the run", s.EvictedKeys),
		Evidence: fmt.Sprintf("evicted_keys rose by %d; maxmemory %s, policy %s", s.EvictedKeys, human(s.MaxMemory), s.EvictionPolicy),
		Advice:   "The dataset does not fit in maxmemory, so reads may miss. Raise maxmemory, shrink the dataset or values, or use TTLs.",
	}
}

func memoryNearLimit(in Input) *Finding {
	s := in.Server
	if s == nil || s.MaxMemory <= 0 {
		return nil
	}
	ratio := float64(s.UsedMemoryMax) / float64(s.MaxMemory)
	if ratio < MemoryWarnRatio {
		return nil
	}
	sev := Warning
	if ratio >= MemoryCriticalRatio {
		sev = Critical
	}
	return &Finding{
		ID: "memory-near-limit", Severity: sev,
		Title:    fmt.Sprintf("Memory reached %.0f%% of maxmemory", ratio*100),
		Evidence: fmt.Sprintf("peak used_memory %s of maxmemory %s", human(s.UsedMemoryMax), human(s.MaxMemory)),
		Advice:   "At the limit the server evicts keys or rejects writes (OOM). Raise maxmemory or reduce the dataset.",
	}
}

func lowHitRatio(in Input) *Finding {
	s := in.Server
	if s == nil || s.HitRatio < 0 || s.KeyspaceHits+s.KeyspaceMisses < MinLookupsForHit || s.HitRatio >= LowHitRatio {
		return nil
	}
	return &Finding{
		ID: "low-hit-ratio", Severity: Warning,
		Title:    fmt.Sprintf("Only %.0f%% of key lookups found data", s.HitRatio*100),
		Evidence: fmt.Sprintf("%d hits, %d misses", s.KeyspaceHits, s.KeyspaceMisses),
		Advice:   "Reads mostly missed. Enable dataset.preload, check the key prefix, or look for evictions and expired keys. A cache workload with a deliberately cold start can show this.",
	}
}

func forkStall(in Input) *Finding {
	s := in.Server
	if s == nil || s.MaxForkUsec < ForkWarnUsec {
		return nil
	}
	sev := Warning
	if s.MaxForkUsec >= ForkCriticalUsec {
		sev = Critical
	}
	return &Finding{
		ID: "fork-stall", Severity: sev,
		Title:    fmt.Sprintf("A background save fork blocked the server for %s", time.Duration(s.MaxForkUsec)*time.Microsecond),
		Evidence: fmt.Sprintf("latest_fork_usec = %d", s.MaxForkUsec),
		Advice:   "While the server forks for RDB or AOF rewrite it cannot serve requests. Large datasets and slow virtualization make forks slow. Check persistence settings and transparent huge pages.",
	}
}

func slowCommands(in Input) *Finding {
	s := in.Server
	if s == nil || s.SlowlogNew == 0 {
		return nil
	}
	ev := fmt.Sprintf("%d new SLOWLOG entries, slowest %s", s.SlowlogNew, s.SlowlogWorst)
	if len(s.SlowlogSample) > 0 {
		ev += "; for example: " + s.SlowlogSample[0].Command
	}
	return &Finding{
		ID: "slow-commands", Severity: Warning,
		Title:    fmt.Sprintf("The server logged %d slow commands", s.SlowlogNew),
		Evidence: ev,
		Advice:   "Commands over slowlog-log-slower-than ran in the run window. Look for O(N) commands on large collections (HGETALL, SMEMBERS, big LRANGE).",
	}
}

func blockedClients(in Input) *Finding {
	s := in.Server
	if s == nil || s.MaxBlockedConns <= 0 {
		return nil
	}
	return &Finding{
		ID: "blocked-clients", Severity: Info,
		Title:    fmt.Sprintf("Up to %d clients were blocked on the server", s.MaxBlockedConns),
		Evidence: fmt.Sprintf("blocked_clients peaked at %d", s.MaxBlockedConns),
		Advice:   "Clients waiting in blocking commands (BLPOP, XREAD BLOCK) are counted here. This workload does not use them, so another application is on this server.",
	}
}

func replicationLag(in Input) *Finding {
	s := in.Server
	if s == nil {
		return nil
	}
	if s.LinkDown {
		return &Finding{ID: "replication-lag", Severity: Critical, Title: "The replica's link to its primary is down",
			Evidence: "master_link_status is not up", Advice: "Reads from this node are stale. Check the primary and the network between them."}
	}
	if s.ReplicaLagBytes < ReplicaLagWarnBytes {
		return nil
	}
	return &Finding{
		ID: "replication-lag", Severity: Warning,
		Title:    fmt.Sprintf("A replica fell %s behind the primary", human(s.ReplicaLagBytes)),
		Evidence: fmt.Sprintf("master_repl_offset minus replica offset peaked at %d bytes", s.ReplicaLagBytes),
		Advice:   "Replicas cannot keep up with the write rate. Reads from them will be stale; check replica CPU and the link.",
	}
}

func rejectedConnections(in Input) *Finding {
	s := in.Server
	if s == nil || s.RejectedConnections <= 0 {
		return nil
	}
	return &Finding{
		ID: "rejected-connections", Severity: Critical,
		Title:    fmt.Sprintf("The server rejected %d connections", s.RejectedConnections),
		Evidence: fmt.Sprintf("rejected_connections rose by %d", s.RejectedConnections),
		Advice:   "The server hit maxclients. Raise maxclients or lower run.concurrency.",
	}
}

func fragmentation(in Input) *Finding {
	s := in.Server
	if s == nil || s.MaxFragRatio < FragmentationRatio || s.UsedMemoryMax < MinMemForFrag {
		return nil
	}
	return &Finding{
		ID: "memory-fragmentation", Severity: Warning,
		Title:    fmt.Sprintf("Memory fragmentation ratio reached %.2f", s.MaxFragRatio),
		Evidence: fmt.Sprintf("mem_fragmentation_ratio %.2f with %s in use", s.MaxFragRatio, human(s.UsedMemoryMax)),
		Advice:   "The server holds much more memory than the data needs. Consider active defragmentation or a restart after heavy churn.",
	}
}

func loadGeneratorBound(in Input) *Finding {
	c := in.Client
	if c.GOMAXPROCS <= 0 || c.CPUCores < ClientCPUFraction*float64(c.GOMAXPROCS) {
		return nil
	}
	return &Finding{
		ID: "load-generator-bound", Severity: Warning,
		Title:    "plgv used almost all of this machine's CPU",
		Evidence: fmt.Sprintf("%.1f of %d cores", c.CPUCores, c.GOMAXPROCS),
		Advice:   "The load generator may be the bottleneck, so results understate the server. Use pipelining (target.pool.pipeline_depth), fewer workers, or run plgv on a bigger machine.",
	}
}

func arrivalsDropped(in Input) *Finding {
	c := in.Client
	if c.Dropped == 0 {
		return nil
	}
	offered := c.Ops + c.Dropped
	frac := float64(c.Dropped) / float64(offered)
	if frac < DroppedWarnFrac {
		return nil // a handful still queued when the run ended is normal
	}
	sev := Warning
	if frac >= DroppedCriticalFrac {
		sev = Critical
	}
	return &Finding{
		ID: "arrivals-dropped", Severity: sev,
		Title:    fmt.Sprintf("%d of %d offered operations were never sent (%.1f%%)", c.Dropped, offered, frac*100),
		Evidence: fmt.Sprintf("open loop at %.0f ops/s: %d sent, %d dropped (queue full, or still waiting when the run ended)", c.TargetOpsPerSec, c.Ops, c.Dropped),
		Advice:   "The server, or plgv, could not keep up with the offered rate. Lower the rate, raise the number of workers or the pipeline depth, or check the server. Latency in an open loop includes the time operations waited, so it already shows the overload.",
	}
}

func targetRateMissed(in Input) *Finding {
	c := in.Client
	if c.TargetOpsPerSec <= 0 || c.Elapsed < time.Second || c.Dropped > 0 {
		return nil
	}
	got := c.opsPerSec()
	if got >= TargetMissFraction*c.TargetOpsPerSec {
		return nil
	}
	adv := "The server or plgv could not keep up with the target rate. Lower the target, add workers, or use pipelining."
	if c.CoordinatedOmission {
		adv += " Latency here includes the time operations waited to start, so it shows the real user-visible delay."
	}
	return &Finding{
		ID: "target-rate-missed", Severity: Warning,
		Title:    fmt.Sprintf("Reached %.0f ops/s of the %.0f ops/s target", got, c.TargetOpsPerSec),
		Evidence: fmt.Sprintf("%.0f%% of target", got/c.TargetOpsPerSec*100), Advice: adv,
	}
}

func tailLatency(in Input) *Finding {
	c := in.Client
	if c.P50 <= 0 || c.P99 < TailLatencyMin || c.P99 <= time.Duration(TailLatencyFactor)*c.P50 {
		return nil
	}
	return &Finding{
		ID: "tail-latency", Severity: Info,
		Title:    fmt.Sprintf("p99 latency is %.0fx the median", float64(c.P99)/float64(c.P50)),
		Evidence: fmt.Sprintf("p50 %v, p99 %v", c.P50.Round(time.Microsecond), c.P99.Round(time.Microsecond)),
		Advice:   "A long tail points to occasional stalls: background saves, slow commands, or a saturated server. Compare with the Slowlog and fork findings.",
	}
}

func countMismatch(in Input) *Finding {
	s, c := in.Server, in.Client
	if s == nil || !s.ServerOpsExact || c.Elapsed < MinRunForMismatch || c.Ops == 0 || s.ServerOps <= 0 {
		return nil
	}
	ratio := float64(s.ServerOps) / float64(c.Ops)
	if ratio > 1-CountMismatch && ratio < 1+CountMismatch {
		return nil
	}
	what := "other clients are using the server"
	if ratio < 1 {
		what = "some commands did not reach the server"
	}
	return &Finding{
		ID: "client-server-mismatch", Severity: Warning,
		Title:    "Client and server operation counts disagree",
		Evidence: fmt.Sprintf("plgv sent %d operations; the server processed %d (%.0f%%)", c.Ops, s.ServerOps, ratio*100),
		Advice:   "Probably " + what + ". Use a dedicated server for benchmarks.",
	}
}

func bgsaveFailed(in Input) *Finding {
	s := in.Server
	if s == nil || !s.BgsaveFailed {
		return nil
	}
	return &Finding{
		ID: "bgsave-failed", Severity: Warning,
		Title:    "The last background save failed",
		Evidence: "rdb_last_bgsave_status is not ok",
		Advice:   "Check the server log and disk space. A failed save can make the server refuse writes (stop-writes-on-bgsave-error).",
	}
}

func serverMetricsUnavailable(in Input) *Finding {
	s := in.Server
	if s == nil || s.PollErrors == 0 {
		return nil
	}
	return &Finding{
		ID: "server-metrics-unavailable", Severity: Info,
		Title:    "Some server metrics could not be read",
		Evidence: fmt.Sprintf("%d polls failed; first: %s", s.PollErrors, s.FirstError),
		Advice:   "Server findings may be incomplete. The ACL user needs INFO, SLOWLOG and LATENCY permissions.",
	}
}

func eventsOf(in Input, kind string) []servermetrics.Event {
	if in.Server == nil || in.Server.Topology == nil {
		return nil
	}
	var out []servermetrics.Event
	for _, e := range in.Server.Topology.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func eventList(ev []servermetrics.Event) string {
	var parts []string
	for _, e := range ev {
		parts = append(parts, fmt.Sprintf("at %.0fs: %s", e.Offset.Seconds(), e.Detail))
	}
	return strings.Join(parts, "; ")
}

func failover(in Input) *Finding {
	ev := eventsOf(in, "failover")
	if len(ev) == 0 {
		return nil
	}
	return &Finding{
		ID: "failover-during-run", Severity: Warning,
		Title:    fmt.Sprintf("%d failover(s) happened during the run", len(ev)),
		Evidence: eventList(ev),
		Advice:   "A primary was replaced while the workload ran. Expect a burst of errors (timeouts, connection resets) and a latency spike around the marked time, then recovery on the new primary. Compare the markers on the charts with the error series.",
	}
}

func reshard(in Input) *Finding {
	ev := eventsOf(in, "reshard")
	if len(ev) == 0 {
		return nil
	}
	return &Finding{
		ID: "reshard-during-run", Severity: Warning,
		Title:    "Slots moved between nodes during the run",
		Evidence: eventList(ev),
		Advice:   "Slot migration makes keys answer with MOVED or ASK redirects while it happens, which adds latency. Results from this run include that disturbance.",
	}
}

func nodeDown(in Input) *Finding {
	ev := eventsOf(in, "node-down")
	if len(ev) == 0 {
		return nil
	}
	return &Finding{
		ID: "node-down", Severity: Warning,
		Title:    fmt.Sprintf("%d node(s) went down during the run", len(ev)),
		Evidence: eventList(ev),
		Advice:   "A node stopped answering. If it was a primary, a failover follows; if a replica, reads sent to replicas lost capacity.",
	}
}

func clusterNotOK(in Input) *Finding {
	if in.Server == nil || in.Server.Topology == nil || in.Server.Topology.Mode != "cluster" || !in.Server.Topology.ClusterDown {
		return nil
	}
	return &Finding{
		ID: "cluster-not-ok", Severity: Critical,
		Title:    "The cluster reported cluster_state fail during the run",
		Evidence: "CLUSTER INFO cluster_state was not ok at least once",
		Advice:   "Some slots were not served, so commands for them failed (CLUSTERDOWN). Check the cluster with CLUSTER NODES and the server logs; a shard with no available primary or a failed majority causes this.",
	}
}

func hotNode(in Input) *Finding {
	c := in.Client
	if len(c.NodeOps) < MinNodesForHot {
		return nil
	}
	var total, top uint64
	topIdx := 0
	for i, n := range c.NodeOps {
		total += n
		if n > top {
			top, topIdx = n, i
		}
	}
	if total < MinOpsForHot {
		return nil
	}
	share := float64(top) / float64(total)
	fair := 1 / float64(len(c.NodeOps))
	if share < HotNodeFactor*fair {
		return nil
	}
	addr := fmt.Sprint(topIdx)
	if topIdx < len(c.NodeAddrs) {
		addr = c.NodeAddrs[topIdx]
	}
	return &Finding{
		ID: "hot-node", Severity: Info,
		Title:    fmt.Sprintf("%s received %.0f%% of the operations", addr, share*100),
		Evidence: fmt.Sprintf("a fair share across %d nodes would be %.0f%%", len(c.NodeOps), fair*100),
		Advice:   "A few hot keys (a zipf key distribution) or too few hash tag groups put most of the load on one node. In production this is the node that saturates first; spread the keys, or use more hash tag groups.",
	}
}

func unevenSlots(in Input) *Finding {
	if in.Server == nil || in.Server.Topology == nil || len(in.Server.Topology.Shards) < 2 {
		return nil
	}
	total := 0
	for _, sh := range in.Server.Topology.Shards {
		total += sh.Slots
	}
	avg := float64(total) / float64(len(in.Server.Topology.Shards))
	if avg == 0 {
		return nil
	}
	worst, worstD := in.Server.Topology.Shards[0], 0.0
	for _, sh := range in.Server.Topology.Shards {
		d := (float64(sh.Slots) - avg) / avg
		if d < 0 {
			d = -d
		}
		if d > worstD {
			worst, worstD = sh, d
		}
	}
	if worstD <= SlotSkew {
		return nil
	}
	return &Finding{
		ID: "uneven-slots", Severity: Info,
		Title:    "The shards do not own an equal share of the slots",
		Evidence: fmt.Sprintf("%s owns %d slots; the average is %.0f", worst.Primary, worst.Slots, avg),
		Advice:   "Unequal slot ownership gives one shard more keys and traffic than the others. Rebalance with the cluster tooling (for example valkey-cli --cluster rebalance).",
	}
}

func redirects(in Input) *Finding {
	s := in.Server
	if s == nil || s.Mode != "cluster" {
		return nil
	}
	n := s.ErrorStats["MOVED"] + s.ErrorStats["ASK"]
	if n == 0 {
		return nil
	}
	sev := Info
	if in.Client.Ops > 0 && float64(n)/float64(in.Client.Ops) >= RedirectWarnFrac {
		sev = Warning
	}
	return &Finding{
		ID: "redirects", Severity: sev,
		Title:    fmt.Sprintf("The servers answered %d commands with MOVED or ASK redirects", n),
		Evidence: fmt.Sprintf("errorstat MOVED %d, ASK %d (the client follows these automatically)", s.ErrorStats["MOVED"], s.ErrorStats["ASK"]),
		Advice:   "A few redirects around a failover or reshard are normal: the client's slot map was briefly out of date. A steady stream means the slot map is stale or slots are migrating; each redirect costs an extra round trip.",
	}
}

func opCount(c Client, name string) (OpCount, bool) {
	for _, o := range c.PerOp {
		if o.Name == name {
			return o, true
		}
	}
	return OpCount{}, false
}

func watchConflicts(in Input) *Finding {
	o, ok := opCount(in.Client, "WATCH_INCR")
	if !ok || o.Ops < MinOpsForShare {
		return nil
	}
	frac := float64(o.Misses) / float64(o.Ops)
	if frac < WatchConflictInfo {
		return nil
	}
	sev := Info
	if frac >= WatchConflictWarn {
		sev = Warning
	}
	return &Finding{
		ID: "watch-conflicts", Severity: sev,
		Title:    fmt.Sprintf("%.0f%% of optimistic (WATCH) increments were aborted", frac*100),
		Evidence: fmt.Sprintf("%d of %d WATCH_INCR operations found the key changed before EXEC", o.Misses, o.Ops),
		Advice:   "Several workers update the same keys, so EXEC is aborted and the work is wasted. A real application would retry. Spread the keys (a larger key count or a uniform distribution), or use an atomic command (INCR) or a Lua script instead of WATCH.",
	}
}

func blockingTimeouts(in Input) *Finding {
	var ops, misses uint64
	var names []string
	for _, n := range []string{"BLPOP", "BRPOP", "XREAD_BLOCK"} {
		if o, ok := opCount(in.Client, n); ok && o.Ops > 0 {
			ops += o.Ops
			misses += o.Misses
			names = append(names, n)
		}
	}
	if ops < MinOpsForShare || float64(misses)/float64(ops) < TimeoutShare {
		return nil
	}
	return &Finding{
		ID: "blocking-timeouts", Severity: Info,
		Title:    fmt.Sprintf("%.0f%% of blocking reads timed out with nothing to read", float64(misses)/float64(ops)*100),
		Evidence: fmt.Sprintf("%d of %d %s operations waited for their timeout", misses, ops, strings.Join(names, ", ")),
		Advice:   "Consumers outpaced producers, so most of their time (and latency) is waiting. Add producers (LPUSH, RPUSH, XADD) with a weight at least as large as the consumers', or shorten the timeout.",
	}
}

func streamsDry(in Input) *Finding {
	var ops, misses uint64
	for _, n := range []string{"XCONSUME", "XREADGROUP"} {
		if o, ok := opCount(in.Client, n); ok {
			ops += o.Ops
			misses += o.Misses
		}
	}
	if ops < MinOpsForShare || float64(misses)/float64(ops) < DryShare {
		return nil
	}
	return &Finding{
		ID: "streams-ran-dry", Severity: Info,
		Title:    fmt.Sprintf("%.0f%% of consumer-group reads found no new entries", float64(misses)/float64(ops)*100),
		Evidence: fmt.Sprintf("%d of %d XCONSUME/XREADGROUP operations returned nothing", misses, ops),
		Advice:   "The consumers read everything that was added. Add XADD operations to the mix (or raise dataset.stream_length) so there is something to consume.",
	}
}

func pubsubLoss(in Input) *Finding {
	p := in.Client.PubSub
	if p == nil || p.Expected == 0 {
		return nil
	}
	ratio := float64(p.Delivered) / float64(p.Expected)
	if ratio >= PubSubLossWarn {
		return nil
	}
	sev := Warning
	if ratio < PubSubLossCritical {
		sev = Critical
	}
	return &Finding{
		ID: "pubsub-loss", Severity: sev,
		Title:    fmt.Sprintf("Subscribers received %.1f%% of the published messages", ratio*100),
		Evidence: fmt.Sprintf("%d published to %d subscribers: %d expected, %d received", p.Published, p.Subscribers, p.Expected, p.Delivered),
		Advice:   "Pub/sub does not retry: a subscriber that cannot keep up, or whose output buffer overflows (client-output-buffer-limit pubsub), loses messages or is disconnected. Check subscriber count, message size and the server's output buffer limits.",
	}
}

func dangerousAllowed(in Input) *Finding {
	if len(in.Client.DangerousAllowed) == 0 {
		return nil
	}
	return &Finding{
		ID: "dangerous-commands-allowed", Severity: Info,
		Title:    "This run was allowed to use commands that are blocked by default",
		Evidence: strings.Join(in.Client.DangerousAllowed, ", "),
		Advice:   "workload.allow_dangerous was on. Make sure the server under test is disposable: these commands can delete data, change configuration or stop the server.",
	}
}

func bigKeys(in Input) *Finding {
	k := in.KeySpace
	if k == nil || k.Sampled == 0 {
		return nil
	}
	if k.MaxBytes < BigKeyInfoBytes && k.TopElement < BigKeyElements {
		return nil
	}
	sev := Info
	if k.MaxBytes >= BigKeyWarnBytes {
		sev = Warning
	}
	scope := fmt.Sprintf("a sample of %d keys", k.Sampled)
	if k.Complete {
		scope = fmt.Sprintf("all %d keys", k.Sampled)
	}
	return &Finding{
		ID: "big-keys", Severity: sev,
		Title:    fmt.Sprintf("Big keys found: the largest is %s", human(k.MaxBytes)),
		Evidence: fmt.Sprintf("In %s, %s (%s, %d elements) is the biggest; %d sampled keys are 1 MiB or more", scope, k.TopKey, k.TopType, k.TopElement, k.BigKeys),
		Advice:   "Commands that read or delete a whole big key (HGETALL, SMEMBERS, LRANGE 0 -1, DEL) block the server while they run and show up as tail latency. Split the key into smaller ones, page through it (HSCAN, SSCAN, ZRANGE with LIMIT), and delete with UNLINK.",
	}
}
