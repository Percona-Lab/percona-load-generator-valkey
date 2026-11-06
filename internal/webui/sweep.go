package webui

import (
	"sort"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/sweep"
)

// SweepMeta describes one sweep in the list.
type SweepMeta struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Kind      string    `json:"kind"`
	Total     int       `json:"total"`
	Done      int       `json:"done"`
	StartedAt time.Time `json:"started_at"`
	Values    []float64 `json:"values"`
}

// SweepRow is one level of a sweep.
type SweepRow struct {
	Index      int     `json:"index"`
	Value      float64 `json:"value"` // worker count or target ops/s
	RunID      string  `json:"run_id"`
	Label      string  `json:"label"`
	OpsPerSec  float64 `json:"ops_per_sec"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	P99Ms      float64 `json:"p99_ms"`
	Errors     uint64  `json:"errors"`
	Dropped    uint64  `json:"dropped_ops"`
	Efficiency float64 `json:"efficiency"` // workers: scaling vs linear; rate: achieved / offered
	Stopped    bool    `json:"stopped"`
}

// SweepResult is the results table of a sweep plus a plain-language reading.
type SweepResult struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Kind     string     `json:"kind"`
	Total    int        `json:"total"`
	Complete bool       `json:"complete"`
	Rows     []SweepRow `json:"rows"`
	Notes    []string   `json:"notes"`
	// PeakRunID is the level with the highest throughput.
	PeakRunID string `json:"peak_run_id"`
}

// ListSweeps groups stored runs by sweep id, newest sweep first.
func ListSweeps(metas []Meta) []SweepMeta {
	by := map[string]*SweepMeta{}
	var order []string
	for _, m := range metas {
		if m.Sweep == nil {
			continue
		}
		sm, ok := by[m.Sweep.ID]
		if !ok {
			sm = &SweepMeta{ID: m.Sweep.ID, Kind: m.Sweep.Kind, Total: m.Sweep.Total, StartedAt: m.StartedAt, Label: sweepBase(m.Label)}
			by[m.Sweep.ID] = sm
			order = append(order, m.Sweep.ID)
		}
		sm.Done++
		sm.Values = append(sm.Values, m.Sweep.Value)
		if m.StartedAt.Before(sm.StartedAt) {
			sm.StartedAt = m.StartedAt
		}
	}
	out := make([]SweepMeta, 0, len(order))
	for _, id := range order {
		sm := by[id]
		sort.Float64s(sm.Values)
		out = append(out, *sm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// sweepBase strips the " - N workers" or " - N ops/s" suffix from a level label.
func sweepBase(label string) string {
	for i := len(label) - 1; i > 0; i-- {
		if label[i] == '-' && i > 0 && label[i-1] == ' ' {
			return label[:i-1]
		}
	}
	return label
}

// BuildSweep makes the results table for the runs of one sweep.
func BuildSweep(id string, metas []Meta) (SweepResult, bool) {
	var runs []Meta
	for _, m := range metas {
		if m.Sweep != nil && m.Sweep.ID == id {
			runs = append(runs, m)
		}
	}
	if len(runs) == 0 {
		return SweepResult{}, false
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Sweep.Index < runs[j].Sweep.Index })
	first := runs[0].Sweep
	res := SweepResult{ID: id, Label: sweepBase(runs[0].Label), Kind: first.Kind, Total: first.Total, Notes: []string{}}
	res.Complete = len(runs) == first.Total
	var rows []sweep.Row
	for _, m := range runs {
		res.Rows = append(res.Rows, SweepRow{Index: m.Sweep.Index, Value: m.Sweep.Value, RunID: m.ID, Label: m.Label, OpsPerSec: m.OpsPerSec,
			P50Ms: m.P50Ms, P95Ms: m.P95Ms, P99Ms: m.P99Ms, Errors: m.Errors, Dropped: m.Dropped, Stopped: m.Stopped})
		rows = append(rows, sweep.Row{Value: m.Sweep.Value, OpsPerSec: m.OpsPerSec, P99Ms: m.P99Ms, Dropped: m.Dropped, Stopped: m.Stopped})
	}
	an := sweep.Analyze(res.Kind, rows, first.Total)
	for i := range res.Rows {
		res.Rows[i].Efficiency = an.Efficiency[i]
	}
	res.PeakRunID = res.Rows[an.PeakIndex].RunID
	res.Notes = an.Notes
	return res, true
}
