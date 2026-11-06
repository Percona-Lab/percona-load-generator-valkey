package replay

import (
	"path/filepath"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
)

// SummaryFormat identifies the JSON layout of a replay summary.
const SummaryFormat = "plgv-replay-summary/1"

// Summary is the JSON record of one replay on one target. It holds totals and
// comparisons, never command arguments, addresses with credentials or values.
type Summary struct {
	Format        string `json:"format"`
	Label         string `json:"label"`  // the target's name
	Target        string `json:"target"` // host:port as given (no credentials)
	PLGVVersion   string `json:"plgv_version"`
	ServerVersion string `json:"server_version,omitempty"`
	Mode          string `json:"mode,omitempty"`
	FinishedAt    string `json:"finished_at"`

	Recording RecordingRef `json:"recording"`
	Speed     float64      `json:"speed"`
	ReadsOnly bool         `json:"reads_only"`

	Ops            uint64  `json:"ops"`
	Errors         uint64  `json:"errors"`
	Misses         uint64  `json:"misses"`
	Skipped        uint64  `json:"skipped"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	OpsPerSec      float64 `json:"ops_per_sec"`
	Latency        Latency `json:"latency_ms"`
	MaxLagMs       float64 `json:"max_lag_ms"`
	// CommandsHash is the hash of the commands sent; HashMatches says it equals the recording's.
	CommandsHash string            `json:"commands_hash"`
	HashMatches  bool              `json:"hash_matches"`
	PerCommand   []CommandStat     `json:"per_command"`
	Iterations   []IterationStat   `json:"iterations,omitempty"`
	ErrorClasses map[string]uint64 `json:"error_classes,omitempty"`
	FirstError   string            `json:"first_error,omitempty"`
	Outcomes     *Outcomes         `json:"outcomes,omitempty"`
	Data         *datafp.Check     `json:"data_check,omitempty"`
	// Failure is set when the replay could not run on this target.
	Failure string `json:"failure,omitempty"`
}

// RecordingRef identifies the recording a replay used.
type RecordingRef struct {
	Name          string `json:"name"` // file name only
	Status        string `json:"status"`
	Ops           uint64 `json:"ops"`
	Workers       int    `json:"workers"`
	CommandsHash  string `json:"commands_hash"`
	ServerVersion string `json:"server_version,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	Imported      bool   `json:"imported,omitempty"`
}

// Latency is a set of percentiles in milliseconds.
type Latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// CommandStat is one command's totals.
type CommandStat struct {
	Command string  `json:"command"`
	Ops     uint64  `json:"ops"`
	Errors  uint64  `json:"errors"`
	Misses  uint64  `json:"misses"`
	P50Ms   float64 `json:"p50_ms"`
	P99Ms   float64 `json:"p99_ms"`
}

// IterationStat is one pass of an iterated replay.
type IterationStat struct {
	Index     int     `json:"index"`
	Ops       uint64  `json:"ops"`
	Errors    uint64  `json:"errors"`
	OpsPerSec float64 `json:"ops_per_sec"`
	P50Ms     float64 `json:"p50_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Summarize builds the summary of a finished replay. recordedHash is the hash
// in the recording's trailer.
func Summarize(r *Result, label, target, plgvVersion, serverVersion, mode string, data *datafp.Check) *Summary {
	s := &Summary{
		Format: SummaryFormat, Label: label, Target: target, PLGVVersion: plgvVersion, ServerVersion: serverVersion, Mode: mode,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		Recording: RecordingRef{Name: filepath.Base(r.Path), Status: r.Status, Workers: r.Header.Workers, CommandsHash: r.RecordedHash,
			Ops: r.RecordedOps, ServerVersion: r.Header.ServerVersion, CreatedAt: r.Header.CreatedAt, Imported: r.Header.Import != nil},
		Speed: r.Speed, ReadsOnly: r.ReadsOnly,
		Ops: r.Ops, Errors: r.Errors, Misses: r.Misses, Skipped: r.Skipped, ElapsedSeconds: r.Elapsed.Seconds(), OpsPerSec: r.OpsPerSec(),
		MaxLagMs: ms(r.MaxLag), CommandsHash: r.CommandsHash, HashMatches: r.HashMatches,
		ErrorClasses: r.ErrClasses, FirstError: r.FirstError, Outcomes: r.Outcomes, Data: data,
	}
	if r.Hist != nil {
		s.Latency = Latency{P50: ms(r.Hist.Quantile(0.5)), P95: ms(r.Hist.Quantile(0.95)), P99: ms(r.Hist.Quantile(0.99)), Max: ms(r.Hist.Max())}
	}
	for _, p := range r.PerOp {
		s.PerCommand = append(s.PerCommand, CommandStat{Command: p.Command, Ops: p.Ops, Errors: p.Errors, Misses: p.Misses, P50Ms: ms(p.P50), P99Ms: ms(p.P99)})
	}
	for _, it := range r.Iterations {
		ops := 0.0
		if it.Elapsed > 0 {
			ops = float64(it.Ops) / it.Elapsed.Seconds()
		}
		s.Iterations = append(s.Iterations, IterationStat{Index: it.Index, Ops: it.Ops, Errors: it.Errors, OpsPerSec: ops, P50Ms: ms(it.P50), P99Ms: ms(it.P99)})
	}
	return s
}
