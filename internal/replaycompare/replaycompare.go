// Package replaycompare compares replays of the same recording on different
// servers (or the same server before and after a change) and says what
// differs: speed, latency, errors, replies and whether the comparison can be
// trusted. It reads replay summaries; it never talks to a server.
package replaycompare

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
)

// DefaultTolerance is the change, as a fraction, below which a metric counts as unchanged.
const DefaultTolerance = 0.10

// latencyFloorMs: latency differences below this are noise, whatever the percentage.
const latencyFloorMs = 0.1

// Options tune a comparison.
type Options struct {
	// Tolerance is the fraction a metric may change before it is better or worse.
	Tolerance float64
}

func (o Options) tol() float64 {
	if o.Tolerance <= 0 {
		return DefaultTolerance
	}
	return o.Tolerance
}

// Verdicts of one metric.
const (
	Better = "better"
	Worse  = "worse"
	Same   = "same"
)

// Row compares one metric.
type Row struct {
	Metric   string  `json:"metric"`
	Unit     string  `json:"unit"`
	Base     float64 `json:"base"`
	Other    float64 `json:"other"`
	DeltaPct float64 `json:"delta_pct"`
	Verdict  string  `json:"verdict"`
}

// CommandRow compares one command's latency.
type CommandRow struct {
	Command     string  `json:"command"`
	BaseOps     uint64  `json:"base_ops"`
	OtherOps    uint64  `json:"other_ops"`
	BaseP99Ms   float64 `json:"base_p99_ms"`
	OtherP99Ms  float64 `json:"other_p99_ms"`
	P99DeltaPct float64 `json:"p99_delta_pct"`
	BaseErrors  uint64  `json:"base_errors"`
	OtherErrors uint64  `json:"other_errors"`
	Verdict     string  `json:"verdict"`
}

// Finding is something worth the reader's attention.
type Finding struct {
	Severity string `json:"severity"` // info, warning or critical
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// Comparison is one target (Other) against the baseline (Base).
type Comparison struct {
	Base         string       `json:"base"`
	Other        string       `json:"other"`
	BaseServer   string       `json:"base_server,omitempty"`
	OtherServer  string       `json:"other_server,omitempty"`
	Comparable   bool         `json:"comparable"`
	Verdict      string       `json:"verdict"` // equivalent, faster, slower, mixed or not comparable
	Rows         []Row        `json:"rows"`
	Commands     []CommandRow `json:"commands"`
	Findings     []Finding    `json:"findings"`
	TolerancePct float64      `json:"tolerance_pct"`
}

func pct(base, other float64) float64 {
	if base == 0 {
		if other == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return (other - base) / base * 100
}

func jsonSafe(f float64) float64 { // JSON cannot carry Inf
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0
	}
	return f
}

func verdict(base, other float64, higherBetter bool, tol, floor float64) string {
	if math.Abs(other-base) < floor {
		return Same
	}
	d := pct(base, other) / 100
	switch {
	case math.Abs(d) <= tol:
		return Same
	case (d > 0) == higherBetter:
		return Better
	}
	return Worse
}

func row(metric, unit string, base, other float64, higherBetter bool, tol, floor float64) Row {
	return Row{Metric: metric, Unit: unit, Base: base, Other: other, DeltaPct: jsonSafe(pct(base, other)), Verdict: verdict(base, other, higherBetter, tol, floor)}
}

// Compare compares other with base.
func Compare(base, other *replay.Summary, opt Options) *Comparison {
	tol := opt.tol()
	c := &Comparison{Base: base.Label, Other: other.Label, BaseServer: base.ServerVersion, OtherServer: other.ServerVersion, TolerancePct: tol * 100,
		Rows: []Row{}, Commands: []CommandRow{}, Findings: []Finding{}}
	add := func(sev, title, detail string) { c.Findings = append(c.Findings, Finding{sev, title, detail}) }

	// Can the two be compared at all?
	c.Comparable = true
	switch {
	case base.Failure != "" || other.Failure != "":
		c.Comparable = false
		for _, s := range []*replay.Summary{base, other} {
			if s.Failure != "" {
				add("critical", s.Label+" did not run", s.Failure)
			}
		}
	case base.Recording.CommandsHash != other.Recording.CommandsHash:
		c.Comparable = false
		add("critical", "Different recordings", fmt.Sprintf("%s replayed %s and %s replayed %s: they are not the same workload.", base.Label, base.Recording.Name, other.Label, other.Recording.Name))
	}
	if base.Speed != other.Speed || base.ReadsOnly != other.ReadsOnly {
		add("warning", "Replayed with different options", fmt.Sprintf("%s: speed %g, reads-only %v; %s: speed %g, reads-only %v. Compare replays made with the same options.", base.Label, base.Speed, base.ReadsOnly, other.Label, other.Speed, other.ReadsOnly))
	}
	if !c.Comparable {
		c.Verdict = "not comparable"
		return c
	}
	for _, s := range []*replay.Summary{base, other} {
		if s.Data != nil && !s.Data.Match {
			add("warning", "Starting data differs on "+s.Label, "It did not hold the data the recording started with, so differences may come from the data rather than the server.")
		}
		if s.Speed > 0 && s.MaxLagMs > 100 {
			add("warning", s.Label+" fell behind the recorded pace", fmt.Sprintf("By up to %.0f ms, so its latencies include waiting. Replay with -speed 0 to compare capacity.", s.MaxLagMs))
		}
		if s.Ops == 0 {
			add("warning", s.Label+" sent no commands", "")
		}
	}
	if base.ServerVersion != "" && other.ServerVersion != "" && base.ServerVersion != other.ServerVersion {
		add("info", "Different server versions", fmt.Sprintf("%s runs %s and %s runs %s.", base.Label, base.ServerVersion, other.Label, other.ServerVersion))
	}

	c.Rows = []Row{
		row("Throughput", "ops/s", base.OpsPerSec, other.OpsPerSec, true, tol, 0),
		row("Latency p50", "ms", base.Latency.P50, other.Latency.P50, false, tol, latencyFloorMs),
		row("Latency p95", "ms", base.Latency.P95, other.Latency.P95, false, tol, latencyFloorMs),
		row("Latency p99", "ms", base.Latency.P99, other.Latency.P99, false, tol, latencyFloorMs),
		row("Latency max", "ms", base.Latency.Max, other.Latency.Max, false, tol*3, latencyFloorMs*5),
		row("Errors", "count", float64(base.Errors), float64(other.Errors), false, 0, 0.5),
	}
	byName := map[string]replay.CommandStat{}
	for _, p := range base.PerCommand {
		byName[p.Command] = p
	}
	for _, o := range other.PerCommand {
		b, ok := byName[o.Command]
		if !ok || (b.Ops == 0 && o.Ops == 0) {
			continue
		}
		c.Commands = append(c.Commands, CommandRow{Command: o.Command, BaseOps: b.Ops, OtherOps: o.Ops, BaseP99Ms: b.P99Ms, OtherP99Ms: o.P99Ms,
			P99DeltaPct: jsonSafe(pct(b.P99Ms, o.P99Ms)), BaseErrors: b.Errors, OtherErrors: o.Errors, Verdict: verdict(b.P99Ms, o.P99Ms, false, tol, latencyFloorMs)})
	}
	sort.Slice(c.Commands, func(i, j int) bool { return c.Commands[i].OtherOps > c.Commands[j].OtherOps })

	var better, worse int
	for _, r := range c.Rows[:4] { // throughput, p50, p95, p99 decide the verdict
		switch r.Verdict {
		case Better:
			better++
		case Worse:
			worse++
		}
	}
	switch {
	case better > 0 && worse > 0:
		c.Verdict = "mixed"
	case worse > 0:
		c.Verdict = "slower"
	case better > 0:
		c.Verdict = "faster"
	default:
		c.Verdict = "equivalent"
	}

	for _, r := range c.Rows[:4] {
		switch r.Verdict {
		case Worse:
			add("warning", fmt.Sprintf("%s is worse on %s", r.Metric, other.Label), fmt.Sprintf("%.2f %s on %s, %.2f %s on %s (%+.0f%%).", r.Other, r.Unit, other.Label, r.Base, r.Unit, base.Label, r.DeltaPct))
		case Better:
			add("info", fmt.Sprintf("%s is better on %s", r.Metric, other.Label), fmt.Sprintf("%.2f %s on %s, %.2f %s on %s (%+.0f%%).", r.Other, r.Unit, other.Label, r.Base, r.Unit, base.Label, r.DeltaPct))
		}
	}
	if other.Errors > base.Errors {
		sev := "warning"
		if base.Errors == 0 {
			sev = "critical"
		}
		detail := fmt.Sprintf("%d errors on %s, %d on %s.", other.Errors, other.Label, base.Errors, base.Label)
		if other.FirstError != "" {
			detail += " First: " + other.FirstError
		}
		add(sev, "More errors on "+other.Label, detail)
	}
	if bo, oo := base.Outcomes, other.Outcomes; bo != nil && oo != nil {
		if oo.StatusDiffs > bo.StatusDiffs {
			add("warning", other.Label+" answered differently from the recording", fmt.Sprintf("%d commands had another status than recorded on %s, %d on %s.", oo.StatusDiffs, other.Label, bo.StatusDiffs, base.Label))
		}
		if oo.ChecksumDiffs > bo.ChecksumDiffs {
			add("warning", other.Label+" returned different replies", fmt.Sprintf("%d reply checksums differ from the recording on %s, %d on %s.", oo.ChecksumDiffs, other.Label, bo.ChecksumDiffs, base.Label))
		}
	}
	worseCmds := 0
	for _, r := range c.Commands {
		if r.Verdict == Worse && worseCmds < 3 {
			worseCmds++
			add("info", fmt.Sprintf("%s p99 is higher on %s", r.Command, other.Label), fmt.Sprintf("%.2f ms against %.2f ms (%+.0f%%).", r.OtherP99Ms, r.BaseP99Ms, r.P99DeltaPct))
		}
	}
	return c
}

// CompareAll compares every summary after the first with the first.
func CompareAll(all []*replay.Summary, opt Options) []*Comparison {
	var out []*Comparison
	for i := 1; i < len(all); i++ {
		out = append(out, Compare(all[0], all[i], opt))
	}
	return out
}

// MultiFormat identifies the JSON layout written for a multi-target replay.
const MultiFormat = "plgv-replay-multi/1"

// File is a multi-target replay: every target's summary and the comparisons.
type File struct {
	Format      string            `json:"format"`
	Mode        string            `json:"mode"` // sequential or concurrent
	Targets     []*replay.Summary `json:"targets"`
	Comparisons []*Comparison     `json:"comparisons"`
}

// Parse reads a replay summary file or a multi-target file and returns the
// summaries in it.
func Parse(b []byte) ([]*replay.Summary, error) {
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	switch probe.Format {
	case replay.SummaryFormat:
		var s replay.Summary
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return []*replay.Summary{&s}, nil
	case MultiFormat:
		var f File
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		if len(f.Targets) == 0 {
			return nil, fmt.Errorf("the file holds no targets")
		}
		return f.Targets, nil
	}
	return nil, fmt.Errorf("not a plgv replay summary (format %q; expected %s or %s)", probe.Format, replay.SummaryFormat, MultiFormat)
}

// Uniquify gives summaries with the same label a numbered suffix, so a
// comparison can tell them apart.
func Uniquify(all []*replay.Summary) {
	seen := map[string]int{}
	for _, s := range all {
		if s.Label == "" {
			s.Label = s.Target
		}
		seen[s.Label]++
		if n := seen[s.Label]; n > 1 {
			s.Label = fmt.Sprintf("%s (%d)", s.Label, n)
		}
	}
}
