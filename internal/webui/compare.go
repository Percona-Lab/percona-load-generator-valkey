package webui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
	"gopkg.in/yaml.v3"
)

// MetricRow compares one number between two runs.
type MetricRow struct {
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	A            float64 `json:"a"`
	B            float64 `json:"b"`
	ChangePct    float64 `json:"change_pct"` // (b-a)/a; 0 when a is 0
	Better       string  `json:"better"`     // "a", "b" or "same"
	HigherIsGood bool    `json:"higher_is_good"`
}

// OpRow compares one operation.
type OpRow struct {
	Op     string  `json:"op"`
	AOps   uint64  `json:"a_ops"`
	BOps   uint64  `json:"b_ops"`
	AP99Ms float64 `json:"a_p99_ms"`
	BP99Ms float64 `json:"b_p99_ms"`
}

// ConfigDiff is one setting that differs.
type ConfigDiff struct {
	Key string `json:"key"`
	A   string `json:"a"`
	B   string `json:"b"`
}

// Comparison is the result of comparing run A with run B.
type Comparison struct {
	A, B        Meta
	Comparable  bool         `json:"comparable"` // same workload fingerprint
	Metrics     []MetricRow  `json:"metrics"`
	PerOp       []OpRow      `json:"per_op"`
	ConfigDiffs []ConfigDiff `json:"config_diffs"`
	// ServerConfigDiffs lists the server settings that differ between the runs;
	// ServerConfigNote says why there is no list (a run did not record them).
	ServerConfigDiffs []serverconfig.Diff `json:"server_config_diffs"`
	ServerConfigNote  string              `json:"server_config_note,omitempty"`
	OnlyInA           []string            `json:"findings_only_in_a"`
	OnlyInB           []string            `json:"findings_only_in_b"`
	SeriesA           []runsummary.Point  `json:"series_a"`
	SeriesB           []runsummary.Point  `json:"series_b"`
}

const sameTolerance = 0.02 // under 2% apart counts as the same

func row(name, unit string, a, b float64, higherIsGood bool) MetricRow {
	r := MetricRow{Name: name, Unit: unit, A: a, B: b, HigherIsGood: higherIsGood, Better: "same"}
	if a != 0 {
		r.ChangePct = (b - a) / math.Abs(a)
	}
	if math.Abs(r.ChangePct) >= sameTolerance || (a == 0 && b != 0) {
		better := b > a
		if !higherIsGood {
			better = !better
		}
		if better {
			r.Better = "b"
		} else {
			r.Better = "a"
		}
	}
	return r
}

// Compare builds the comparison of two stored runs.
func Compare(am, bm Meta, a, b *runsummary.Summary) Comparison {
	c := Comparison{A: am, B: bm, Comparable: a.Fingerprint != "" && a.Fingerprint == b.Fingerprint,
		SeriesA: a.Series, SeriesB: b.Series, OnlyInA: []string{}, OnlyInB: []string{}}
	ca, cb := a.Client, b.Client
	c.Metrics = []MetricRow{
		row("Throughput", "ops/s", ca.OpsPerSec, cb.OpsPerSec, true),
		row("p50 latency", "ms", ca.P50Ms, cb.P50Ms, false),
		row("p95 latency", "ms", ca.P95Ms, cb.P95Ms, false),
		row("p99 latency", "ms", ca.P99Ms, cb.P99Ms, false),
		row("p99.9 latency", "ms", ca.P999Ms, cb.P999Ms, false),
		row("max latency", "ms", ca.MaxMs, cb.MaxMs, false),
		row("Errors", "", float64(ca.Errors), float64(cb.Errors), false),
		row("Client CPU per 100k ops/s", "cores", ca.CoresPer100k, cb.CoresPer100k, false),
	}
	if a.Server != nil && b.Server != nil {
		if a.Server.HitRatio >= 0 && b.Server.HitRatio >= 0 {
			c.Metrics = append(c.Metrics, row("Hit ratio", "%", a.Server.HitRatio*100, b.Server.HitRatio*100, true))
		}
		c.Metrics = append(c.Metrics,
			row("Server peak memory", "MiB", float64(a.Server.UsedMemoryMax)/(1<<20), float64(b.Server.UsedMemoryMax)/(1<<20), false),
			row("Evicted keys", "", float64(a.Server.EvictedKeys), float64(b.Server.EvictedKeys), false),
			row("New slowlog entries", "", float64(a.Server.SlowlogNew), float64(b.Server.SlowlogNew), false))
	}

	ops := map[string]*OpRow{}
	var order []string
	for _, o := range a.PerOp {
		ops[o.Op] = &OpRow{Op: o.Op, AOps: o.Ops, AP99Ms: o.P99Ms}
		order = append(order, o.Op)
	}
	for _, o := range b.PerOp {
		r, ok := ops[o.Op]
		if !ok {
			r = &OpRow{Op: o.Op}
			ops[o.Op] = r
			order = append(order, o.Op)
		}
		r.BOps, r.BP99Ms = o.Ops, o.P99Ms
	}
	for _, name := range order {
		c.PerOp = append(c.PerOp, *ops[name])
	}

	c.ConfigDiffs = diffConfig(a.ConfigYAML, b.ConfigYAML)
	c.ServerConfigDiffs = []serverconfig.Diff{}
	switch {
	case a.ServerConfig == nil && b.ServerConfig == nil:
	case a.ServerConfig == nil || b.ServerConfig == nil:
		c.ServerConfigNote = "Only one of the two runs recorded the server's settings, so they cannot be compared."
	case a.ServerConfig.Status != serverconfig.StatusOK || b.ServerConfig.Status != serverconfig.StatusOK:
		c.ServerConfigNote = "The server's settings could not be read for one of the runs."
	default:
		if d := serverconfig.Compare(a.ServerConfig, b.ServerConfig); d != nil {
			c.ServerConfigDiffs = d
		}
	}
	fa, fb := map[string]bool{}, map[string]bool{}
	for _, f := range a.Findings {
		fa[f.ID] = true
	}
	for _, f := range b.Findings {
		fb[f.ID] = true
	}
	for id := range fa {
		if !fb[id] {
			c.OnlyInA = append(c.OnlyInA, id)
		}
	}
	for id := range fb {
		if !fa[id] {
			c.OnlyInB = append(c.OnlyInB, id)
		}
	}
	sort.Strings(c.OnlyInA)
	sort.Strings(c.OnlyInB)
	return c
}

// SettingDiff is one setting and its value in each compared run (in the order asked).
type SettingDiff struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// diffConfigMany lists the settings whose value is not the same in every config.
func diffConfigMany(yamls []string) []SettingDiff {
	flat := make([]map[string]string, len(yamls))
	keys := map[string]bool{}
	for i, y := range yamls {
		flat[i] = flatten(y)
		for k := range flat[i] {
			keys[k] = true
		}
	}
	out := []SettingDiff{}
	for k := range keys {
		if strings.HasPrefix(k, "output.") {
			continue
		}
		vals := make([]string, len(flat))
		same := true
		for i, f := range flat {
			vals[i] = f[k]
			if vals[i] != vals[0] {
				same = false
			}
		}
		if !same {
			out = append(out, SettingDiff{Key: k, Values: vals})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// diffConfig lists the settings that differ between two YAML configs.
func diffConfig(a, b string) []ConfigDiff {
	fa, fb := flatten(a), flatten(b)
	keys := map[string]bool{}
	for k := range fa {
		keys[k] = true
	}
	for k := range fb {
		keys[k] = true
	}
	var out []ConfigDiff
	for k := range keys {
		if strings.HasPrefix(k, "output.") {
			continue
		}
		if fa[k] != fb[k] {
			out = append(out, ConfigDiff{Key: k, A: fa[k], B: fb[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if out == nil {
		out = []ConfigDiff{}
	}
	return out
}

func flatten(y string) map[string]string {
	var m any
	out := map[string]string{}
	if yaml.Unmarshal([]byte(y), &m) != nil {
		return out
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				walk(join(prefix, k), e)
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", prefix, i), e)
			}
		default:
			out[prefix] = fmt.Sprint(x)
		}
	}
	walk("", m)
	return out
}

func join(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}
