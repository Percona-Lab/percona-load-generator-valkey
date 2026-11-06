// Package topology reads the shape of a cluster (CLUSTER SHARDS and CLUSTER
// INFO), turns it into a slot router for per-node accounting, and watches it
// during a run so failovers and reshards can be marked on the timeline.
package topology

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Node is one cluster member.
type Node struct {
	ID     string
	Addr   string // ip:port as announced
	Role   string // primary | replica
	Health string // online, failed, loading
	Offset int64  // replication offset
}

// Online reports whether the node is serving.
func (n Node) Online() bool { return n.Health == "online" }

// Shard is a set of slot ranges served by one primary and its replicas.
type Shard struct {
	ID    string
	Slots [][2]int // inclusive ranges
	Nodes []Node
}

// SlotCount is the number of slots this shard owns.
func (s Shard) SlotCount() int {
	n := 0
	for _, r := range s.Slots {
		n += r[1] - r[0] + 1
	}
	return n
}

// Primary returns the shard's primary, if it has one.
func (s Shard) Primary() (Node, bool) {
	for _, n := range s.Nodes {
		if n.Role == "primary" {
			return n, true
		}
	}
	return Node{}, false
}

// Snapshot is the cluster at one moment.
type Snapshot struct {
	At            time.Time
	Shards        []Shard
	StateOK       bool // CLUSTER INFO cluster_state:ok
	SlotsAssigned int
}

// Nodes returns every node of every shard, sorted by address.
func (s Snapshot) Nodes() []Node {
	var out []Node
	for _, sh := range s.Shards {
		out = append(out, sh.Nodes...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

// Primaries returns the primaries, sorted by address.
func (s Snapshot) Primaries() []Node {
	var out []Node
	for _, n := range s.Nodes() {
		if n.Role == "primary" {
			out = append(out, n)
		}
	}
	return out
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ParseShards parses the reply of CLUSTER SHARDS (RESP3 maps or RESP2 flat
// key/value arrays).
func ParseShards(reply any) ([]Shard, error) {
	rows, ok := reply.([]any)
	if !ok {
		return nil, fmt.Errorf("topology: CLUSTER SHARDS reply is %T, want an array", reply)
	}
	var out []Shard
	for _, r := range rows {
		m := asMap(r)
		if m == nil {
			continue
		}
		sh := Shard{ID: asStr(m["id"])}
		if sl, ok := m["slots"].([]any); ok {
			for i := 0; i+1 < len(sl); i += 2 {
				sh.Slots = append(sh.Slots, [2]int{int(asInt(sl[i])), int(asInt(sl[i+1]))})
			}
		}
		if ns, ok := m["nodes"].([]any); ok {
			for _, x := range ns {
				nm := asMap(x)
				if nm == nil {
					continue
				}
				host := asStr(nm["endpoint"])
				if host == "" || host == "?" {
					host = asStr(nm["ip"])
				}
				role := asStr(nm["role"])
				if role == "master" {
					role = "primary"
				}
				sh.Nodes = append(sh.Nodes, Node{
					ID: asStr(nm["id"]), Addr: host + ":" + strconv.FormatInt(asInt(nm["port"]), 10),
					Role: role, Health: asStr(nm["health"]), Offset: asInt(nm["replication-offset"]),
				})
			}
		}
		out = append(out, sh)
	}
	return out, nil
}

// asMap reads a RESP3 map, or a RESP2 flat [k, v, k, v ...] array, as a map.
func asMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case []any:
		m := make(map[string]any, len(x)/2)
		for i := 0; i+1 < len(x); i += 2 {
			if k, ok := x[i].(string); ok {
				m[k] = x[i+1]
			}
		}
		return m
	}
	return nil
}

// ParseClusterInfo reads cluster_state and cluster_slots_assigned.
func ParseClusterInfo(text string) (ok bool, slotsAssigned int) {
	for _, line := range strings.Split(text, "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		switch k {
		case "cluster_state":
			ok = v == "ok"
		case "cluster_slots_assigned":
			n, _ := strconv.Atoi(v)
			slotsAssigned = n
		}
	}
	return
}

// Event is something that changed in the cluster during a run.
type Event struct {
	Offset time.Duration // since the run started
	Kind   string        // failover | reshard | node-down | node-up | cluster-state
	Detail string
}

// Event kinds.
const (
	KindFailover = "failover"
	KindReshard  = "reshard"
	KindNodeDown = "node-down"
	KindNodeUp   = "node-up"
	KindState    = "cluster-state"
)

func slotsOf(sh Shard) string {
	parts := make([]string, len(sh.Slots))
	for i, r := range sh.Slots {
		parts[i] = fmt.Sprintf("%d-%d", r[0], r[1])
	}
	return strings.Join(parts, ",")
}

// Diff lists what changed between two snapshots. Offsets are left for the caller.
func Diff(prev, cur Snapshot) []Event {
	var ev []Event
	if prev.StateOK != cur.StateOK {
		d := "the cluster became healthy again (cluster_state ok)"
		if !cur.StateOK {
			d = "the cluster reported cluster_state fail"
		}
		ev = append(ev, Event{Kind: KindState, Detail: d})
	}
	// Which node serves each slot range, keyed by the slot string of the owning primary.
	prevPrim := map[string]string{} // slots -> primary addr
	for _, sh := range prev.Shards {
		if p, ok := sh.Primary(); ok {
			prevPrim[slotsOf(sh)] = p.Addr
		}
	}
	curPrim := map[string]string{}
	for _, sh := range cur.Shards {
		if p, ok := sh.Primary(); ok {
			curPrim[slotsOf(sh)] = p.Addr
		}
	}
	for slots, was := range prevPrim {
		if now, ok := curPrim[slots]; ok && now != was {
			ev = append(ev, Event{Kind: KindFailover, Detail: fmt.Sprintf("slots %s: primary changed from %s to %s", slots, was, now)})
		}
	}
	// Slot ownership changed without a matching primary change: a reshard.
	var moved []string
	for slots := range curPrim {
		if _, ok := prevPrim[slots]; !ok {
			moved = append(moved, slots)
		}
	}
	sort.Strings(moved)
	if len(moved) > 0 && len(prevPrim) > 0 {
		ev = append(ev, Event{Kind: KindReshard, Detail: "slot ownership changed: " + strings.Join(moved, " ")})
	}
	// Nodes going down or coming back.
	prevH := map[string]string{}
	for _, n := range prev.Nodes() {
		prevH[n.Addr] = n.Health
	}
	for _, n := range cur.Nodes() {
		was, known := prevH[n.Addr]
		switch {
		case known && was == "online" && !n.Online():
			ev = append(ev, Event{Kind: KindNodeDown, Detail: fmt.Sprintf("%s (%s) is %s", n.Addr, n.Role, n.Health)})
		case known && was != "online" && n.Online():
			ev = append(ev, Event{Kind: KindNodeUp, Detail: fmt.Sprintf("%s (%s) is online again", n.Addr, n.Role)})
		}
	}
	for addr := range prevH {
		found := false
		for _, n := range cur.Nodes() {
			if n.Addr == addr {
				found = true
			}
		}
		if !found {
			ev = append(ev, Event{Kind: KindNodeDown, Detail: addr + " left the cluster view"})
		}
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].Kind < ev[j].Kind })
	return ev
}
