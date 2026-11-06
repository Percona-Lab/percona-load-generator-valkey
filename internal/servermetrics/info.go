// Package servermetrics polls INFO, SLOWLOG and LATENCY from the server under
// test and summarizes them for a run.
package servermetrics

import (
	"strconv"
	"strings"
)

// Info is a parsed INFO reply.
type Info struct {
	kv     map[string]string
	Errors map[string]uint64 // errorstat_<CODE>:count=N
}

// ParseInfo parses the text returned by INFO. Section headers and blank lines
// are skipped; keys are unique across sections in practice.
func ParseInfo(text string) *Info {
	in := &Info{kv: map[string]string{}, Errors: map[string]uint64{}}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if code, ok := strings.CutPrefix(k, "errorstat_"); ok {
			// count=3,rejected_calls=0,failed_calls=0
			for _, f := range strings.Split(v, ",") {
				if n, ok := strings.CutPrefix(f, "count="); ok {
					in.Errors[code], _ = strconv.ParseUint(n, 10, 64)
				}
			}
			continue
		}
		in.kv[k] = v
	}
	return in
}

// Str returns a field or "".
func (in *Info) Str(key string) string { return in.kv[key] }

// Int returns an integer field or 0.
func (in *Info) Int(key string) int64 {
	n, _ := strconv.ParseInt(in.kv[key], 10, 64)
	return n
}

// Float returns a float field or 0.
func (in *Info) Float(key string) float64 {
	f, _ := strconv.ParseFloat(in.kv[key], 64)
	return f
}

// Has reports whether the field exists.
func (in *Info) Has(key string) bool { _, ok := in.kv[key]; return ok }

// Version is the real server version: valkey_version when present (Valkey
// reports redis_version 7.2.4 for compatibility), else redis_version.
func (in *Info) Version() string {
	if v := in.kv["valkey_version"]; v != "" {
		return v
	}
	return in.kv["redis_version"]
}

// Replica is one connected replica as seen from a primary.
type Replica struct {
	Offset int64
	Lag    int64 // seconds, as reported by the primary
	State  string
}

// Replicas returns the replicas listed by a primary (slave0, slave1, ...).
func (in *Info) Replicas() []Replica {
	var out []Replica
	for i := 0; ; i++ {
		line, ok := in.kv["slave"+strconv.Itoa(i)]
		if !ok {
			return out
		}
		var r Replica
		for _, f := range strings.Split(line, ",") {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "offset":
				r.Offset, _ = strconv.ParseInt(v, 10, 64)
			case "lag":
				r.Lag, _ = strconv.ParseInt(v, 10, 64)
			case "state":
				r.State = v
			}
		}
		out = append(out, r)
	}
}

// ReplicaLagBytes is the largest gap between the primary's replication offset
// and any replica's offset, or 0 without replicas.
func (in *Info) ReplicaLagBytes() int64 {
	master := in.Int("master_repl_offset")
	var worst int64
	for _, r := range in.Replicas() {
		if d := master - r.Offset; d > worst {
			worst = d
		}
	}
	return worst
}
