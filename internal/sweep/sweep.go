// Package sweep reads the results of a sweep (the same workload run at several
// worker counts or target rates) and says in plain language what they show.
package sweep

import "strconv"

// Thresholds, documented in docs/load-profiles-and-sweep.md.
const (
	PlateauGain     = 0.05 // a step up gaining less than 5% throughput = more workers stopped helping
	LatencyDoubling = 2.0  // p99 at least this many times the first level's p99
	ServedFraction  = 0.95 // a rate level counts as served when achieved >= 95% of offered
)

// Kinds of sweep.
const (
	Workers = "workers"
	Rate    = "rate"
)

// Row is one level's result, in level order.
type Row struct {
	Value     float64 // worker count, or target ops/s
	OpsPerSec float64
	P99Ms     float64
	Dropped   uint64
	Stopped   bool
}

// Analysis is the reading of a sweep.
type Analysis struct {
	Efficiency []float64 // per row: workers = scaling versus linear; rate = achieved / offered
	PeakIndex  int       // row with the highest throughput
	Notes      []string
}

// Analyze computes the efficiency of each level and the plain-language notes.
// total is how many levels the sweep was meant to have.
func Analyze(kind string, rows []Row, total int) Analysis {
	a := Analysis{Efficiency: make([]float64, len(rows)), Notes: []string{}}
	var peak float64
	for i, r := range rows {
		if r.OpsPerSec > peak {
			peak, a.PeakIndex = r.OpsPerSec, i
		}
		switch kind {
		case Workers:
			if rows[0].OpsPerSec > 0 && rows[0].Value > 0 && r.Value > 0 {
				a.Efficiency[i] = (r.OpsPerSec / rows[0].OpsPerSec) / (r.Value / rows[0].Value)
			}
		case Rate:
			if r.Value > 0 {
				a.Efficiency[i] = r.OpsPerSec / r.Value
			}
		}
	}
	complete := len(rows) == total
	if !complete {
		a.Notes = append(a.Notes, "This sweep did not finish all of its levels, so the reading below covers only the levels that ran.")
	}
	for _, r := range rows {
		if r.Stopped {
			a.Notes = append(a.Notes, "A level was stopped early, so its numbers are partial and not comparable.")
			break
		}
	}
	if len(rows) < 2 {
		return a
	}
	switch kind {
	case Workers:
		plateau := -1
		for i := 1; i < len(rows); i++ {
			if rows[i-1].OpsPerSec > 0 && rows[i].OpsPerSec/rows[i-1].OpsPerSec-1 < PlateauGain {
				plateau = i - 1
				break
			}
		}
		last := rows[len(rows)-1]
		if plateau >= 0 {
			a.Notes = append(a.Notes, "More workers stopped helping after "+Num(rows[plateau].Value)+" workers: the next step gained less than 5% throughput.")
		} else {
			a.Notes = append(a.Notes, "Throughput was still growing at "+Num(last.Value)+" workers (every step gained 5% or more); try higher counts.")
		}
		for _, r := range rows[1:] {
			if rows[0].P99Ms > 0 && r.P99Ms >= LatencyDoubling*rows[0].P99Ms {
				a.Notes = append(a.Notes, "p99 latency reached twice its "+Num(rows[0].Value)+"-worker value at "+Num(r.Value)+" workers.")
				break
			}
		}
	case Rate:
		saturated := false
		for i, r := range rows {
			if r.Dropped > 0 || a.Efficiency[i] < ServedFraction {
				a.Notes = append(a.Notes, "The server (or plgv) could not serve "+Num(r.Value)+" ops/s: it reached "+Num(r.OpsPerSec)+" ops/s. The highest fully served level is shown in the table.")
				saturated = true
				break
			}
		}
		if !saturated {
			a.Notes = append(a.Notes, "Every level was served at its target rate; try higher rates to find the limit.")
		}
	}
	return a
}

// Num formats a level without a trailing ".0".
func Num(v float64) string {
	if v == float64(int64(v)) || v >= 100 {
		return strconv.FormatInt(int64(v+0.5), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
