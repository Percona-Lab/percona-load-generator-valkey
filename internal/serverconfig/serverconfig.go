// Package serverconfig reads the settings of the server under test (CONFIG GET *)
// before and after a run, so a stored run records which setup produced its
// numbers. It only reads. Secrets are masked before anything is stored.
package serverconfig

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// Status of one node's capture.
const (
	StatusOK           = "ok"
	StatusNotPermitted = "not_permitted"
	StatusError        = "error"
)

// Masked replaces the value of a secret setting.
const Masked = "***"

// MaxNameLen bounds the user's label for the server setup.
const MaxNameLen = 80

// Change is a setting whose value differed between the start and the end of the run.
type Change struct {
	Key    string `json:"key"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Node is the capture for one server.
type Node struct {
	Addr     string            `json:"addr"`
	Status   string            `json:"status"`
	Error    string            `json:"error,omitempty"`
	Settings map[string]string `json:"settings,omitempty"` // as read before the load started
	Changed  []Change          `json:"changed,omitempty"`  // settings that changed during the run
}

// Snapshot is the server configuration recorded with a run.
type Snapshot struct {
	Name   string   `json:"name,omitempty"`   // the user's label for this server setup
	Status string   `json:"status"`           // ok when at least one node was read; else the first node's status
	Nodes  []Node   `json:"nodes"`            // one per server (several in a cluster)
	Differ []string `json:"differ,omitempty"` // settings that differ between nodes
}

// Querier runs one command against a server.
type Querier interface {
	Query(ctx context.Context, cmd valkey.Cmd) (any, error)
}

// Reader reads the settings of one or more servers.
type Reader struct {
	name  string
	nodes []target
	snap  Snapshot
}

type target struct {
	addr string
	q    func(context.Context, valkey.Cmd) (any, error)
}

// ForClient returns a reader for the server c is connected to (one node), or
// every node it knows when c can address nodes (a cluster). addr names a single
// server in the report.
func ForClient(c valkey.Client, cluster bool, addr, name string) *Reader {
	r := &Reader{name: name}
	if nq, ok := c.(valkey.NodeQuerier); ok && cluster {
		for _, a := range nq.Nodes() {
			a := a
			r.nodes = append(r.nodes, target{addr: a, q: func(ctx context.Context, cmd valkey.Cmd) (any, error) { return nq.QueryNode(ctx, a, cmd) }})
		}
	}
	if len(r.nodes) == 0 {
		r.nodes = []target{{addr: addr, q: c.Query}}
	}
	return r
}

// Start reads every node's settings. It never fails: a node that cannot be
// read is recorded with its status.
func (r *Reader) Start(ctx context.Context) {
	r.snap = Snapshot{Name: r.name, Nodes: []Node{}}
	for _, t := range r.nodes {
		n := Node{Addr: t.addr}
		m, err := read(ctx, t)
		if err != nil {
			n.Status, n.Error = classify(err), shorten(err.Error())
		} else {
			n.Status, n.Settings = StatusOK, m
		}
		r.snap.Nodes = append(r.snap.Nodes, n)
	}
}

// Finish reads the settings again and returns the snapshot, with the settings
// that changed during the run and those that differ between nodes.
func (r *Reader) Finish(ctx context.Context) *Snapshot {
	for i, t := range r.nodes {
		n := &r.snap.Nodes[i]
		if n.Status != StatusOK {
			continue
		}
		after, err := read(ctx, t)
		if err != nil {
			continue // the start values stay; the end read is a bonus
		}
		n.Changed = changes(n.Settings, after)
	}
	r.snap.Status = overall(r.snap.Nodes)
	r.snap.Differ = differing(r.snap.Nodes)
	s := r.snap
	return &s
}

func overall(nodes []Node) string {
	for _, n := range nodes {
		if n.Status == StatusOK {
			return StatusOK
		}
	}
	if len(nodes) > 0 {
		return nodes[0].Status
	}
	return StatusError
}

func read(ctx context.Context, t target) (map[string]string, error) {
	reply, err := t.q(ctx, valkey.Cmd{"CONFIG", "GET", "*"})
	if err != nil {
		return nil, err
	}
	m := parse(reply)
	if len(m) == 0 {
		return nil, fmt.Errorf("CONFIG GET * returned no settings")
	}
	return Mask(m), nil
}

// parse accepts both reply shapes: a map (RESP3) or a flat name/value list (RESP2).
func parse(reply any) map[string]string {
	out := map[string]string{}
	switch x := reply.(type) {
	case map[string]any:
		for k, v := range x {
			out[k] = fmt.Sprint(v)
		}
	case map[any]any:
		for k, v := range x {
			out[fmt.Sprint(k)] = fmt.Sprint(v)
		}
	case []any:
		for i := 0; i+1 < len(x); i += 2 {
			out[fmt.Sprint(x[i])] = fmt.Sprint(x[i+1])
		}
	}
	return out
}

func classify(err error) string {
	s := strings.ToLower(err.Error())
	for _, w := range []string{"noperm", "no permissions", "unknown command", "unknown subcommand", "not allowed", "disabled", "denied"} {
		if strings.Contains(s, w) {
			return StatusNotPermitted
		}
	}
	return StatusError
}

func shorten(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// IsSecret reports whether a setting holds a password or other secret.
func IsSecret(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "pass") || strings.Contains(k, "secret") || strings.HasSuffix(k, "auth")
}

// Mask returns a copy of m with every non-empty secret value replaced.
func Mask(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if IsSecret(k) && v != "" {
			v = Masked
		}
		out[k] = v
	}
	return out
}

func changes(before, after map[string]string) []Change {
	var out []Change
	for k, b := range before {
		if a, ok := after[k]; ok && a != b {
			out = append(out, Change{Key: k, Before: b, After: a})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// perNode lists settings that are expected to differ between the nodes of one
// deployment (addresses, ports, file paths), so they are not reported.
var perNode = map[string]bool{
	"port": true, "tls-port": true, "bind": true, "bind-source-addr": true, "unixsocket": true,
	"dir": true, "logfile": true, "pidfile": true, "dbfilename": true, "appendfilename": true, "appenddirname": true,
	"replicaof": true, "slaveof": true, "cluster-port": true, "cluster-config-file": true,
	"cluster-announce-ip": true, "cluster-announce-port": true, "cluster-announce-bus-port": true,
	"cluster-announce-tls-port": true, "cluster-announce-hostname": true, "cluster-announce-human-nodename": true,
	"replica-announce-ip": true, "slave-announce-ip": true, "replica-announce-port": true, "slave-announce-port": true,
	"announce-ip": true, "announce-port": true, "announce-tls-port": true, "aclfile": true,
}

// differing lists the settings whose value is not the same on every node that
// was read (expected per-node settings left out).
func differing(nodes []Node) []string {
	var ok []Node
	for _, n := range nodes {
		if n.Status == StatusOK {
			ok = append(ok, n)
		}
	}
	if len(ok) < 2 {
		return nil
	}
	var out []string
	for k, v0 := range ok[0].Settings {
		if perNode[k] {
			continue
		}
		for _, n := range ok[1:] {
			if v, has := n.Settings[k]; has && v != v0 {
				out = append(out, k)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Diff is one setting that differs between two snapshots.
type Diff struct {
	Key string `json:"key"`
	A   string `json:"a"`
	B   string `json:"b"`
}

// Compare lists the settings that differ between the first readable node of a
// and of b. A setting present in only one run shows "-" for the other.
func Compare(a, b *Snapshot) []Diff {
	ma, mb := first(a), first(b)
	if ma == nil || mb == nil {
		return nil
	}
	keys := map[string]bool{}
	for k := range ma {
		keys[k] = true
	}
	for k := range mb {
		keys[k] = true
	}
	var out []Diff
	for k := range keys {
		if perNode[k] {
			continue
		}
		va, oka := ma[k]
		vb, okb := mb[k]
		if oka && okb && va == vb {
			continue
		}
		if !oka {
			va = "-"
		}
		if !okb {
			vb = "-"
		}
		out = append(out, Diff{Key: k, A: va, B: vb})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func first(s *Snapshot) map[string]string {
	if s == nil {
		return nil
	}
	for _, n := range s.Nodes {
		if n.Status == StatusOK {
			return n.Settings
		}
	}
	return nil
}
