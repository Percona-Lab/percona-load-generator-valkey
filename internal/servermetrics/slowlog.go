package servermetrics

import (
	"strings"
	"time"
)

// SlowEntry is one SLOWLOG entry.
type SlowEntry struct {
	ID       int64
	Time     time.Time
	Duration time.Duration
	Command  string // arguments joined, truncated
	Client   string
	Node     string // cluster: address of the node that logged it
}

const maxCommandChars = 200

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ParseSlowlog parses the reply of SLOWLOG GET, newest first.
func ParseSlowlog(reply any) []SlowEntry {
	rows, _ := reply.([]any)
	var out []SlowEntry
	for _, r := range rows {
		f, ok := r.([]any)
		if !ok || len(f) < 4 {
			continue
		}
		e := SlowEntry{
			ID:       toInt(f[0]),
			Time:     time.Unix(toInt(f[1]), 0),
			Duration: time.Duration(toInt(f[2])) * time.Microsecond,
		}
		if args, ok := f[3].([]any); ok {
			parts := make([]string, 0, len(args))
			for _, a := range args {
				parts = append(parts, toStr(a))
			}
			e.Command = strings.Join(parts, " ")
			if len(e.Command) > maxCommandChars {
				e.Command = e.Command[:maxCommandChars] + "..."
			}
		}
		if len(f) > 4 {
			e.Client = toStr(f[4])
		}
		out = append(out, e)
	}
	return out
}

// LatencyEvent is one row of LATENCY LATEST.
type LatencyEvent struct {
	Name      string
	LatestMs  int64
	MaxMs     int64
	Timestamp time.Time
}

// ParseLatencyLatest parses LATENCY LATEST: [name, unix ts, latest ms, max ms].
func ParseLatencyLatest(reply any) []LatencyEvent {
	rows, _ := reply.([]any)
	var out []LatencyEvent
	for _, r := range rows {
		f, ok := r.([]any)
		if !ok || len(f) < 4 {
			continue
		}
		out = append(out, LatencyEvent{
			Name: toStr(f[0]), Timestamp: time.Unix(toInt(f[1]), 0),
			LatestMs: toInt(f[2]), MaxMs: toInt(f[3]),
		})
	}
	return out
}
