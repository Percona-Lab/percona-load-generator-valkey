// Package report renders a self-contained, shareable HTML report for a
// completed run. The output embeds its own CSS and inline SVG charts (no
// external assets or JavaScript), so it can be saved and shared as one file
// and printed to PDF from the browser.
package report

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
)

// KV is a labeled value.
type KV struct{ Key, Value string }

type view struct {
	S            *runsummary.Summary
	Title        string
	Generated    time.Time
	Config       []KV
	ServerInfo   []KV
	KeySpaceInfo []KV
	ErrClasses   []KV
	ThroughputC  template.HTML
	LatencyC     template.HTML
	ServerOpsC   template.HTML
	MemoryC      template.HTML
	HasServer    bool
}

// Render writes the HTML report for s to w.
func Render(w io.Writer, s *runsummary.Summary) error {
	v := view{S: s, Generated: time.Now(), HasServer: s.Server != nil}
	v.Title = "Valkey Benchmark Report"
	if s.Label != "" {
		v.Title += ": " + s.Label
	}
	v.Config = configItems(s)
	if k := s.KeySpace; k != nil && k.Sampled > 0 {
		v.KeySpaceInfo = []KV{{"Total size of the sample", humanBytes(k.TotalBytes)},
			{"Median / p99 / biggest key", humanBytes(k.P50Bytes) + " / " + humanBytes(k.P99Bytes) + " / " + humanBytes(k.MaxBytes)},
			{"Keys of 1 MiB or more", fmt.Sprint(k.OverMiB)}, {"Keys without expiry", fmt.Sprintf("%d of %d", k.NoExpiry, k.Sampled)}}
	}

	var thr, p50, p95, p99 []Point
	for _, p := range s.Series {
		thr = append(thr, Point{p.OffsetSeconds, p.OpsPerSec})
		p50 = append(p50, Point{p.OffsetSeconds, p.P50Ms})
		p95 = append(p95, Point{p.OffsetSeconds, p.P95Ms})
		p99 = append(p99, Point{p.OffsetSeconds, p.P99Ms})
	}
	v.ThroughputC = lineChart("Throughput over time", "ops/s", []Series{{"client ops/s", "s1", thr}})
	v.LatencyC = lineChart("Latency over time", "ms", []Series{{"p50", "s1", p50}, {"p95", "s2", p95}, {"p99", "s3", p99}})

	for k, n := range s.Client.ErrorClasses {
		v.ErrClasses = append(v.ErrClasses, KV{k, fmt.Sprint(n)})
	}
	if s.Server != nil {
		v.ServerInfo = serverItems(s.Server)
		var ops, mem []Point
		for _, p := range s.Server.Samples {
			ops = append(ops, Point{p.OffsetSeconds, p.OpsPerSec})
			mem = append(mem, Point{p.OffsetSeconds, float64(p.UsedMemory) / (1 << 20)})
		}
		v.ServerOpsC = lineChart("Server ops/s", "ops/s", []Series{{"server ops/s", "s2", ops}, {"client ops/s", "s1", thr}})
		v.MemoryC = lineChart("Server memory", "MiB", []Series{{"used memory", "s4", mem}})
	}

	tmpl, err := template.New("report").Funcs(template.FuncMap{
		"f2":    func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"f0":    func(f float64) string { return fmt.Sprintf("%.0f", f) },
		"ts":    func(t time.Time) string { return t.Format("2006-01-02 15:04:05 MST") },
		"pct":   func(f float64) string { return fmt.Sprintf("%.1f%%", f*100) },
		"bytes": humanBytes,
		"unix":  func(sec int64) string { return time.Unix(sec, 0).Format("15:04:05") },
	}).Parse(reportTemplate)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, v)
}

// RenderBytes renders the report to a byte slice.
func RenderBytes(s *runsummary.Summary) ([]byte, error) {
	var b bytes.Buffer
	if err := Render(&b, s); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func configItems(s *runsummary.Summary) []KV {
	c, err := config.Parse([]byte(s.ConfigYAML))
	if err != nil {
		return []KV{{"Configuration", "could not be read: " + err.Error()}}
	}
	items := []KV{
		{"Target", strings.Join(c.Target.URIs, ", ")},
		{"Client library", c.Target.Client},
		{"Workers", fmt.Sprint(c.Run.Concurrency)},
		{"Pipeline depth", fmt.Sprint(c.Target.Pool.PipelineDepth)},
		{"Duration", c.Run.Duration.String()},
		{"Seed", fmt.Sprint(s.Seed)},
		{"Keys per type", fmt.Sprint(c.Dataset.KeyCount)},
		{"Key distribution", c.Dataset.KeyDistribution},
		{"Value size", fmt.Sprintf("%d B", c.Dataset.ValueSize)},
		{"TTL mode", c.Dataset.TTL.Mode},
		{"Preload", fmt.Sprint(c.Dataset.Preload)},
		{"Server metrics", map[bool]string{true: "on", false: "off"}[c.Server.Enabled]},
	}
	if c.Run.TargetOpsPerSec > 0 {
		mode := "service time"
		if c.Run.CoordinatedOmission {
			mode = "coordinated-omission aware"
		}
		items = append(items, KV{"Target rate", fmt.Sprintf("%.0f ops/s (%s)", c.Run.TargetOpsPerSec, mode)})
	}
	var mix []string
	for _, it := range c.Workload.Mix {
		mix = append(mix, fmt.Sprintf("%s %d", it.Op, it.Weight))
	}
	return append(items, KV{"Operation mix (weights)", strings.Join(mix, ", ")})
}

func serverItems(s *runsummary.Server) []KV {
	items := []KV{
		{"Version", s.Version}, {"Role / mode", s.Role + " / " + s.Mode},
		{"Operations processed", fmt.Sprint(s.ServerOps)},
		{"Peak memory", fmt.Sprintf("%.1f MiB", float64(s.UsedMemoryMax)/(1<<20))},
		{"maxmemory / policy", fmt.Sprintf("%d / %s", s.MaxMemory, s.EvictionPolicy)},
		{"Evicted / expired keys", fmt.Sprintf("%d / %d", s.EvictedKeys, s.ExpiredKeys)},
		{"New slowlog entries", fmt.Sprint(s.SlowlogNew)},
	}
	if s.HitRatio >= 0 {
		items = append(items, KV{"Hit ratio", fmt.Sprintf("%.1f%%", s.HitRatio*100)})
	}
	return items
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
