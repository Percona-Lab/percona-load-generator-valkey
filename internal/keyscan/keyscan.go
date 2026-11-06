// Package keyscan samples the key space of a server after a run: key types, sizes,
// expiry and the biggest keys. It only reads (SCAN, DBSIZE, TYPE, MEMORY USAGE,
// PTTL and a length command per key) and never changes data.
package keyscan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Query runs one command and returns its reply as plain Go values.
type Query func(ctx context.Context, cmd []string) (any, error)

// Source is one place keys are scanned from: a standalone or sentinel master, or
// one primary of a cluster. Query must reach that node.
type Source struct {
	Name  string
	Query Query
}

// Options bound the work.
type Options struct {
	Samples int    // keys to sample in total, split evenly over the sources
	Top     int    // biggest keys to report
	Match   string // SCAN MATCH pattern, "*" for everything
	Workers int    // concurrent per-key lookups (default 16)
}

// Limits and defaults.
const (
	DefaultSamples = 10000
	MinSamples     = 100
	MaxSamples     = 1000000
	DefaultTop     = 10
	MaxTop         = 100
	scanCount      = 500
)

// Bucket is one bar of a histogram.
type Bucket struct {
	Label string `json:"label"`
	Keys  int    `json:"keys"`
}

// TypeStat summarizes the sampled keys of one type.
type TypeStat struct {
	Type        string  `json:"type"`
	Keys        int     `json:"keys"`
	Share       float64 `json:"share"` // of sampled keys
	Bytes       int64   `json:"bytes"` // sum of MEMORY USAGE over the sample
	AvgBytes    int64   `json:"avg_bytes"`
	MaxBytes    int64   `json:"max_bytes"`
	AvgElements int64   `json:"avg_elements"` // string: bytes; others: members, fields or entries
	MaxElements int64   `json:"max_elements"`
}

// BigKey is one of the largest sampled keys.
type BigKey struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	Bytes      int64  `json:"bytes"`
	Elements   int64  `json:"elements"`
	TTLSeconds int64  `json:"ttl_seconds"` // -1 = no expiry
}

// Result is what the scan found. Numbers describe the sample, not the whole data
// set, unless Complete is true.
type Result struct {
	Requested  int        `json:"requested"`
	Sampled    int        `json:"sampled"`
	Complete   bool       `json:"complete"` // every matching key was seen
	Sources    int        `json:"sources"`  // nodes scanned
	DBSize     int64      `json:"db_size"`  // keys on the scanned nodes (all keys, not only matching ones)
	Match      string     `json:"match"`
	TotalBytes int64      `json:"total_bytes"`
	P50Bytes   int64      `json:"p50_bytes"`
	P99Bytes   int64      `json:"p99_bytes"`
	MaxBytes   int64      `json:"max_bytes"`
	Types      []TypeStat `json:"types"`
	Sizes      []Bucket   `json:"sizes"`
	TTL        []Bucket   `json:"ttl"` // "no expiry" first
	NoExpiry   int        `json:"no_expiry"`
	OverMiB    int        `json:"over_1mib"` // sampled keys of 1 MiB or more
	Big        []BigKey   `json:"big_keys"`
	Gone       int        `json:"gone"`   // keys that expired or were deleted while scanning
	Errors     int        `json:"errors"` // lookups that failed
	FirstError string     `json:"first_error,omitempty"`
	DurationMs float64    `json:"duration_ms"`
}

var sizeEdges = []struct {
	limit int64
	label string
}{{128, "under 128 B"}, {1 << 10, "128 B to 1 KiB"}, {10 << 10, "1 to 10 KiB"}, {100 << 10, "10 to 100 KiB"}, {1 << 20, "100 KiB to 1 MiB"}, {10 << 20, "1 to 10 MiB"}, {1 << 62, "10 MiB or more"}}

var ttlEdges = []struct {
	limit int64 // milliseconds
	label string
}{{60_000, "under 1 min"}, {600_000, "1 to 10 min"}, {3_600_000, "10 min to 1 h"}, {86_400_000, "1 h to 1 day"}, {1 << 62, "1 day or more"}}

type keyInfo struct {
	key      string
	typ      string
	bytes    int64
	elements int64
	pttl     int64
	ok       bool
	gone     bool // expired or deleted between the scan and the lookup
}

// lengthCmd is the command that counts a key's elements, by type.
var lengthCmd = map[string]string{"string": "STRLEN", "hash": "HLEN", "list": "LLEN", "set": "SCARD", "zset": "ZCARD", "stream": "XLEN"}

// Analyze scans the sources and inspects the sampled keys with q (which must route
// a command to the node that owns its key).
func Analyze(ctx context.Context, q Query, sources []Source, opt Options) (*Result, error) {
	start := time.Now()
	if opt.Samples <= 0 {
		opt.Samples = DefaultSamples
	}
	if opt.Top <= 0 {
		opt.Top = DefaultTop
	}
	if opt.Match == "" {
		opt.Match = "*"
	}
	if opt.Workers <= 0 {
		opt.Workers = 16
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("keyscan: nothing to scan")
	}
	res := &Result{Requested: opt.Samples, Sources: len(sources), Match: opt.Match, Complete: true}
	quota := (opt.Samples + len(sources) - 1) / len(sources)

	var keys []string
	for _, src := range sources {
		if n, err := src.Query(ctx, []string{"DBSIZE"}); err == nil {
			if v, ok := n.(int64); ok {
				res.DBSize += v
			}
		}
		got, complete, err := scan(ctx, src, opt.Match, quota)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", src.Name, err)
		}
		if !complete {
			res.Complete = false
		}
		keys = append(keys, got...)
	}

	infos := make([]keyInfo, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	idx := make(chan int)
	for w := 0; w < opt.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				ki, err := inspect(ctx, q, keys[i])
				infos[i] = ki
				if err != nil {
					mu.Lock()
					res.Errors++
					if res.FirstError == "" {
						res.FirstError = err.Error()
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range keys {
		if ctx.Err() != nil {
			break
		}
		idx <- i
	}
	close(idx)
	wg.Wait()
	summarize(res, infos, opt.Top)
	res.DurationMs = float64(time.Since(start)) / float64(time.Millisecond)
	return res, ctx.Err()
}

// scan collects up to quota keys. complete is false when it stopped at the quota.
func scan(ctx context.Context, src Source, match string, quota int) ([]string, bool, error) {
	var out []string
	cursor := "0"
	for {
		if err := ctx.Err(); err != nil {
			return out, false, err
		}
		reply, err := src.Query(ctx, []string{"SCAN", cursor, "MATCH", match, "COUNT", strconv.Itoa(scanCount)})
		if err != nil {
			return out, false, err
		}
		arr, ok := reply.([]any)
		if !ok || len(arr) != 2 {
			return out, false, fmt.Errorf("unexpected SCAN reply %T", reply)
		}
		cursor = fmt.Sprint(arr[0])
		if ks, ok := arr[1].([]any); ok {
			for _, k := range ks {
				out = append(out, fmt.Sprint(k))
			}
		}
		if cursor == "0" {
			return capKeys(out, quota)
		}
		if len(out) >= quota {
			return out[:quota], false, nil
		}
	}
}

func capKeys(out []string, quota int) ([]string, bool, error) {
	if len(out) > quota {
		return out[:quota], false, nil
	}
	return out, true, nil
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func inspect(ctx context.Context, q Query, key string) (keyInfo, error) {
	ki := keyInfo{key: key}
	t, err := q(ctx, []string{"TYPE", key})
	if err != nil {
		return ki, err
	}
	ki.typ = strings.ToLower(fmt.Sprint(t))
	if ki.typ == "none" || ki.typ == "<nil>" {
		ki.gone = true
		return ki, nil
	}
	m, err := q(ctx, []string{"MEMORY", "USAGE", key, "SAMPLES", "5"})
	if err != nil {
		return ki, err
	}
	if m == nil {
		ki.gone = true
		return ki, nil
	}
	ki.bytes = asInt(m)
	if p, err := q(ctx, []string{"PTTL", key}); err == nil {
		ki.pttl = asInt(p)
	} else {
		return ki, err
	}
	if c, ok := lengthCmd[ki.typ]; ok {
		n, err := q(ctx, []string{c, key})
		if err != nil {
			return ki, err
		}
		ki.elements = asInt(n)
	}
	ki.ok = true
	return ki, nil
}

func summarize(res *Result, infos []keyInfo, top int) {
	byType := map[string]*TypeStat{}
	var sizes []int64
	sizeBuckets := make([]int, len(sizeEdges))
	ttlBuckets := make([]int, len(ttlEdges))
	var all []keyInfo
	for _, ki := range infos {
		if !ki.ok {
			if ki.gone {
				res.Gone++
			}
			continue
		}
		all = append(all, ki)
		ts := byType[ki.typ]
		if ts == nil {
			ts = &TypeStat{Type: ki.typ}
			byType[ki.typ] = ts
		}
		ts.Keys++
		ts.Bytes += ki.bytes
		ts.AvgElements += ki.elements // sum for now
		if ki.bytes > ts.MaxBytes {
			ts.MaxBytes = ki.bytes
		}
		if ki.elements > ts.MaxElements {
			ts.MaxElements = ki.elements
		}
		res.TotalBytes += ki.bytes
		sizes = append(sizes, ki.bytes)
		if ki.bytes >= 1<<20 {
			res.OverMiB++
		}
		for i, e := range sizeEdges {
			if ki.bytes < e.limit {
				sizeBuckets[i]++
				break
			}
		}
		if ki.pttl < 0 {
			res.NoExpiry++
		} else {
			for i, e := range ttlEdges {
				if ki.pttl < e.limit {
					ttlBuckets[i]++
					break
				}
			}
		}
	}
	res.Sampled = len(all)
	for _, ts := range byType {
		ts.Share = float64(ts.Keys) / float64(max(res.Sampled, 1))
		ts.AvgBytes = ts.Bytes / int64(ts.Keys)
		ts.AvgElements /= int64(ts.Keys)
		res.Types = append(res.Types, *ts)
	}
	sort.Slice(res.Types, func(i, j int) bool {
		if res.Types[i].Bytes != res.Types[j].Bytes {
			return res.Types[i].Bytes > res.Types[j].Bytes
		}
		return res.Types[i].Type < res.Types[j].Type
	})
	for i, e := range sizeEdges {
		res.Sizes = append(res.Sizes, Bucket{Label: e.label, Keys: sizeBuckets[i]})
	}
	res.TTL = append(res.TTL, Bucket{Label: "no expiry", Keys: res.NoExpiry})
	for i, e := range ttlEdges {
		res.TTL = append(res.TTL, Bucket{Label: e.label, Keys: ttlBuckets[i]})
	}
	if len(sizes) > 0 {
		sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
		res.P50Bytes = sizes[(len(sizes)-1)/2]
		res.P99Bytes = sizes[int(float64(len(sizes)-1)*0.99)]
		res.MaxBytes = sizes[len(sizes)-1]
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].bytes != all[j].bytes {
			return all[i].bytes > all[j].bytes
		}
		return all[i].key < all[j].key
	})
	for i := 0; i < len(all) && i < top; i++ {
		ttl := int64(-1)
		if all[i].pttl >= 0 {
			ttl = all[i].pttl / 1000
		}
		res.Big = append(res.Big, BigKey{Key: all[i].key, Type: all[i].typ, Bytes: all[i].bytes, Elements: all[i].elements, TTLSeconds: ttl})
	}
	if res.Types == nil {
		res.Types = []TypeStat{}
	}
	if res.Big == nil {
		res.Big = []BigKey{}
	}
}
