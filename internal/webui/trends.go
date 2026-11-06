package webui

import (
	"sort"
)

// Regression thresholds for Trends (documented in docs/trends-and-history.md).
const (
	RegressP99Increase = 0.20 // p99 latency up by 20% or more
	RegressOpsDecrease = 0.10 // throughput down by 10% or more
	RegressHitDrop     = 0.05 // hit ratio down by 5 points or more
	RegressErrorRate   = 0.001
	BaselineRuns       = 5 // previous runs the latest run is compared with
	MinRunsForTrend    = 3
)

// Flag is one detected regression.
type Flag struct {
	Metric   string  `json:"metric"`
	Baseline float64 `json:"baseline"`
	Latest   float64 `json:"latest"`
	Change   float64 `json:"change"` // relative (0.25 = +25%); points for hit ratio
	Message  string  `json:"message"`
}

// TrendGroup is a set of comparable runs (same workload fingerprint).
type TrendGroup struct {
	Fingerprint string `json:"fingerprint"`
	Mix         string `json:"mix"`
	Runs        []Meta `json:"runs"` // oldest first
	Flags       []Flag `json:"flags"`
	Enough      bool   `json:"enough"` // at least MinRunsForTrend runs
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	if n := len(c); n%2 == 1 {
		return c[n/2]
	}
	n := len(c)
	return (c[n/2-1] + c[n/2]) / 2
}

// BuildTrends groups runs by fingerprint and flags regressions of the latest
// run in each group against the median of the runs before it. Runs stopped
// early are left out because their numbers are not comparable.
func BuildTrends(metas []Meta) []TrendGroup {
	by := map[string][]Meta{}
	var order []string
	for _, m := range metas {
		if m.Stopped || m.Fingerprint == "" {
			continue
		}
		if _, ok := by[m.Fingerprint]; !ok {
			order = append(order, m.Fingerprint)
		}
		by[m.Fingerprint] = append(by[m.Fingerprint], m)
	}
	var groups []TrendGroup
	for _, fp := range order {
		runs := by[fp]
		sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
		g := TrendGroup{Fingerprint: fp, Mix: runs[len(runs)-1].Mix, Runs: runs, Enough: len(runs) >= MinRunsForTrend, Flags: []Flag{}}
		if g.Enough {
			g.Flags = detect(runs)
		}
		groups = append(groups, g)
	}
	// Groups with the most recent activity first.
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Runs[len(groups[i].Runs)-1].StartedAt.After(groups[j].Runs[len(groups[j].Runs)-1].StartedAt)
	})
	return groups
}

func detect(runs []Meta) []Flag {
	latest := runs[len(runs)-1]
	prev := runs[:len(runs)-1]
	if len(prev) > BaselineRuns {
		prev = prev[len(prev)-BaselineRuns:]
	}
	col := func(f func(Meta) float64) float64 {
		var v []float64
		for _, m := range prev {
			v = append(v, f(m))
		}
		return median(v)
	}
	var flags []Flag
	if base := col(func(m Meta) float64 { return m.P99Ms }); base > 0 && latest.P99Ms >= base*(1+RegressP99Increase) {
		ch := latest.P99Ms/base - 1
		flags = append(flags, Flag{"p99_ms", base, latest.P99Ms, ch,
			"p99 latency is " + pct(ch) + " higher than the median of the previous runs"})
	}
	if base := col(func(m Meta) float64 { return m.OpsPerSec }); base > 0 && latest.OpsPerSec <= base*(1-RegressOpsDecrease) {
		ch := latest.OpsPerSec/base - 1
		flags = append(flags, Flag{"ops_per_sec", base, latest.OpsPerSec, ch,
			"throughput is " + pct(-ch) + " lower than the median of the previous runs"})
	}
	// Hit ratio only among runs that have it.
	var hv []float64
	for _, m := range prev {
		if m.HitRatio >= 0 {
			hv = append(hv, m.HitRatio)
		}
	}
	if len(hv) > 0 && latest.HitRatio >= 0 {
		base := median(hv)
		if latest.HitRatio <= base-RegressHitDrop {
			flags = append(flags, Flag{"hit_ratio", base, latest.HitRatio, latest.HitRatio - base,
				"hit ratio dropped by " + pct(base-latest.HitRatio) + " points"})
		}
	}
	rate := func(m Meta) float64 {
		if m.Ops == 0 {
			return 0
		}
		return float64(m.Errors) / float64(m.Ops)
	}
	if base := col(rate); rate(latest) > base+RegressErrorRate {
		flags = append(flags, Flag{"error_rate", base, rate(latest), rate(latest) - base,
			"error rate rose from " + pct(base) + " to " + pct(rate(latest))})
	}
	if flags == nil {
		flags = []Flag{}
	}
	return flags
}

func pct(f float64) string {
	return trimFloat(f*100) + "%"
}
