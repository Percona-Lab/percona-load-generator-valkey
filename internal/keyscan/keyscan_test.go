package keyscan

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
)

type entry struct {
	typ   string
	bytes int64
	n     int64
	pttl  int64
}

// fake is an in-memory server: SCAN pages through sorted keys.
type fake struct {
	mu   sync.Mutex
	data map[string]entry
	fail string // a command name that returns an error
	gone map[string]bool
	cmds []string
}

func (f *fake) query(_ context.Context, cmd []string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd[0])
	if cmd[0] == f.fail {
		return nil, errors.New("boom")
	}
	switch cmd[0] {
	case "DBSIZE":
		return int64(len(f.data)), nil
	case "SCAN":
		var keys []string
		for k := range f.data {
			if ok, _ := path.Match(cmd[3], k); ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var cur int
		fmt.Sscan(cmd[1], &cur)
		end := cur + 7 // small pages, to exercise the cursor
		next := fmt.Sprint(end)
		if end >= len(keys) {
			end, next = len(keys), "0"
		}
		out := []any{}
		for _, k := range keys[min(cur, len(keys)):end] {
			out = append(out, k)
		}
		return []any{next, out}, nil
	}
	key := cmd[1]
	if cmd[0] == "MEMORY" {
		key = cmd[2]
	}
	e, ok := f.data[key]
	if !ok || f.gone[key] {
		if cmd[0] == "TYPE" {
			return "none", nil
		}
		return nil, nil
	}
	switch cmd[0] {
	case "TYPE":
		return e.typ, nil
	case "MEMORY":
		return e.bytes, nil
	case "PTTL":
		return e.pttl, nil
	}
	return e.n, nil // STRLEN, HLEN, LLEN, SCARD, ZCARD, XLEN
}

func sample() *fake {
	f := &fake{data: map[string]entry{}, gone: map[string]bool{}}
	for i := 0; i < 40; i++ {
		f.data[fmt.Sprintf("p:s:%02d", i)] = entry{"string", 100, 50, -1}
	}
	for i := 0; i < 10; i++ {
		f.data[fmt.Sprintf("p:h:%02d", i)] = entry{"hash", 2000, 20, 30_000}
	}
	f.data["p:z:big"] = entry{"zset", 5 << 20, 90000, 7_200_000}
	f.data["other:1"] = entry{"string", 10, 1, -1}
	return f
}

func TestAnalyzeCompleteScanOfAPrefix(t *testing.T) {
	f := sample()
	r, err := Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{Samples: 1000, Top: 3, Match: "p:*"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || r.Sampled != 51 || r.DBSize != 52 {
		t.Fatalf("complete=%v sampled=%d dbsize=%d", r.Complete, r.Sampled, r.DBSize)
	}
	if r.Types[0].Type != "zset" || r.Types[0].Keys != 1 || r.Types[0].MaxElements != 90000 {
		t.Fatalf("types are ordered by bytes: %+v", r.Types)
	}
	byType := map[string]TypeStat{}
	for _, ts := range r.Types {
		byType[ts.Type] = ts
	}
	if s := byType["string"]; s.Keys != 40 || s.AvgBytes != 100 || s.AvgElements != 50 {
		t.Fatalf("string stats %+v", s)
	}
	if r.TotalBytes != 40*100+10*2000+5<<20 || r.MaxBytes != 5<<20 || r.P50Bytes != 100 {
		t.Fatalf("bytes: total %d max %d p50 %d", r.TotalBytes, r.MaxBytes, r.P50Bytes)
	}
	if len(r.Big) != 3 || r.Big[0].Key != "p:z:big" || r.Big[0].TTLSeconds != 7200 || r.Big[1].Type != "hash" {
		t.Fatalf("big keys %+v", r.Big)
	}
	if r.OverMiB != 1 {
		t.Fatalf("one key is over 1 MiB, got %d", r.OverMiB)
	}
	if r.NoExpiry != 40 || r.TTL[0].Label != "no expiry" || r.TTL[0].Keys != 40 {
		t.Fatalf("ttl %+v", r.TTL)
	}
	ttl := map[string]int{}
	for _, b := range r.TTL {
		ttl[b.Label] = b.Keys
	}
	if ttl["under 1 min"] != 10 || ttl["1 h to 1 day"] != 1 {
		t.Fatalf("ttl buckets %+v", r.TTL)
	}
	total := 0
	for _, b := range r.Sizes {
		total += b.Keys
	}
	if total != 51 {
		t.Fatalf("size buckets must add up to the sample, got %d", total)
	}
	for _, c := range f.cmds {
		switch c {
		case "DBSIZE", "SCAN", "TYPE", "MEMORY", "PTTL", "STRLEN", "HLEN", "ZCARD":
		default:
			t.Fatalf("the scan must only read; sent %s", c)
		}
	}
}

func TestAnalyzeStopsAtTheSampleSize(t *testing.T) {
	f := sample()
	r, err := Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{Samples: 100, Match: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete != true || r.Sampled != 52 {
		t.Fatalf("a small data set fits in the sample: %+v", r)
	}
	r, err = Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{Samples: 20, Match: "*"})
	if err != nil || r.Complete || r.Sampled != 20 {
		t.Fatalf("want 20 sampled and not complete: %v %+v", err, r)
	}
}

func TestAnalyzeSplitsTheSampleOverNodes(t *testing.T) {
	a, b := sample(), sample()
	r, err := Analyze(context.Background(), a.query, []Source{{Name: "a", Query: a.query}, {Name: "b", Query: b.query}}, Options{Samples: 20, Match: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Sources != 2 || r.Sampled > 20 || r.DBSize != 104 {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzeCountsVanishedKeysAndErrors(t *testing.T) {
	f := sample()
	f.gone["p:s:00"], f.gone["p:s:01"] = true, true
	r, err := Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{Match: "p:*"})
	if err != nil || r.Gone != 2 || r.Sampled != 49 {
		t.Fatalf("%v gone=%d sampled=%d", err, r.Gone, r.Sampled)
	}
	f = sample()
	f.fail = "MEMORY" // for example MEMORY is disabled by ACL
	r, err = Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{Match: "p:*"})
	if err != nil || r.Errors != 51 || !strings.Contains(r.FirstError, "boom") || r.Sampled != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	f = sample()
	f.fail = "SCAN"
	if _, err := Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{}); err == nil {
		t.Fatal("a failing SCAN must be reported")
	}
	if _, err := Analyze(context.Background(), f.query, nil, Options{}); err == nil {
		t.Fatal("no sources must fail")
	}
}

func TestAnalyzeEmptyAndCanceled(t *testing.T) {
	f := &fake{data: map[string]entry{}}
	r, err := Analyze(context.Background(), f.query, []Source{{Name: "a", Query: f.query}}, Options{})
	if err != nil || r.Sampled != 0 || len(r.Big) != 0 || r.Types == nil {
		t.Fatalf("%v %+v", err, r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, sample().query, []Source{{Name: "a", Query: sample().query}}, Options{}); err == nil {
		t.Fatal("a canceled scan must say so")
	}
}
