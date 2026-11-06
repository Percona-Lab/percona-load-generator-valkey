package datafp

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"
)

type item struct {
	typ  string
	size int64
}

// fake is an in-memory server. SCAN pages through the keys in an order the
// seed chooses, like two real servers that hold the same keys in another layout.
type fake struct {
	items map[string]item
	order []string
	scans int
}

func newFake(n int, seed int64) *fake {
	f := &fake{items: map[string]item{}}
	for i := 0; i < n; i++ {
		typ := []string{"string", "hash", "list", "set", "zset"}[i%5]
		f.items[fmt.Sprintf("k:%d", i)] = item{typ, int64(i%17 + 1)}
	}
	for k := range f.items {
		f.order = append(f.order, k)
	}
	sort.Strings(f.order)
	rand.New(rand.NewSource(seed)).Shuffle(len(f.order), func(i, j int) { f.order[i], f.order[j] = f.order[j], f.order[i] })
	return f
}

func (f *fake) query(_ context.Context, cmd []string) (any, error) {
	switch cmd[0] {
	case "DBSIZE":
		return int64(len(f.items)), nil
	case "SCAN":
		f.scans++
		cur, _ := strconv.Atoi(cmd[1])
		end := cur + 100
		next := strconv.Itoa(end)
		if end >= len(f.order) {
			end, next = len(f.order), "0"
		}
		keys := make([]any, 0, end-cur)
		for _, k := range f.order[cur:end] {
			keys = append(keys, k)
		}
		return []any{next, keys}, nil
	case "TYPE":
		if it, ok := f.items[cmd[1]]; ok {
			return it.typ, nil
		}
		return "none", nil
	case "STRLEN", "HLEN", "LLEN", "SCARD", "ZCARD", "XLEN":
		return f.items[cmd[1]].size, nil
	}
	return nil, fmt.Errorf("unexpected command %v", cmd)
}

func take(t *testing.T, f *fake, opt Options) *Fingerprint {
	t.Helper()
	fp, err := Take(context.Background(), []Source{{Name: "s", Query: f.query}}, opt)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestSameKeysGiveTheSameFingerprintWhateverTheScanOrder(t *testing.T) {
	a := take(t, newFake(5000, 1), Options{Samples: 500})
	b := take(t, newFake(5000, 99), Options{Samples: 500})
	if a.Digest == "" || a.Digest != b.Digest || a.DBSize != 5000 || a.Sampled == 0 {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
	if a.Sampled < 200 || a.Sampled > 1000 {
		t.Fatalf("sampled %d keys, want about 500 (one in %d)", a.Sampled, a.Stride)
	}
	if c := Compare(a, b); !c.Match || len(c.Differences) != 0 {
		t.Fatalf("check = %+v", c)
	}
}

func TestSmallDataIsDigestedCompletely(t *testing.T) {
	fp := take(t, newFake(300, 3), Options{Samples: 1000})
	if fp.Stride != 1 || fp.Sampled != 300 {
		t.Fatalf("fp = %+v", fp)
	}
}

func TestChangedDataIsReported(t *testing.T) {
	base := newFake(2000, 1)
	want := take(t, base, Options{Samples: 1000})

	fewer := newFake(1999, 1)
	if c := Compare(want, take(t, fewer, Options{Samples: 1000})); c.Match || len(c.Differences) == 0 {
		t.Fatalf("a missing key must be reported: %+v", c)
	}
	// Only a sample is digested, so change keys the sample holds.
	var sampled []string
	for k := range base.items {
		if keyHash(k)%uint64(want.Stride) == 0 {
			sampled = append(sampled, k)
		}
	}
	sort.Strings(sampled)
	resized := newFake(2000, 1)
	resized.items[sampled[0]] = item{"string", 999}
	c := Compare(want, take(t, resized, Options{Samples: 1000}))
	if c.Match || len(c.Differences) != 1 {
		t.Fatalf("a changed size must be reported even when the key count matches: %+v", c)
	}
	retyped := newFake(2000, 1)
	retyped.items[sampled[1]] = item{"stream", 3}
	if c := Compare(want, take(t, retyped, Options{Samples: 1000})); c.Match {
		t.Fatalf("a changed type must be reported: %+v", c)
	}
}

func TestPartialScanComparesCountsOnly(t *testing.T) {
	f := newFake(3000, 1)
	partial := take(t, f, Options{Samples: 100, MaxScan: 500})
	if !partial.Partial || partial.Digest != "" {
		t.Fatalf("fp = %+v", partial)
	}
	full := take(t, newFake(3000, 2), Options{Samples: 100})
	c := Compare(full, partial)
	if !c.Match || len(c.Notes) == 0 {
		t.Fatalf("equal counts with a partial digest match with a note: %+v", c)
	}
}

func TestCompareWithoutRecordedFingerprint(t *testing.T) {
	if c := Compare(nil, &Fingerprint{}); !c.Match || len(c.Notes) == 0 {
		t.Fatalf("%+v", c)
	}
	if c := Compare(&Fingerprint{}, nil); c.Match {
		t.Fatalf("%+v", c)
	}
}

func TestClusterSumsEveryPrimary(t *testing.T) {
	a, b := newFake(600, 1), newFake(0, 1)
	for i := 0; i < 400; i++ {
		k := fmt.Sprintf("other:%d", i)
		b.items[k] = item{"string", 5}
		b.order = append(b.order, k)
	}
	fp, err := Take(context.Background(), []Source{{"a", a.query}, {"b", b.query}}, Options{Samples: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if fp.DBSize != 1000 || fp.Sampled != 1000 || fp.Sources != 2 {
		t.Fatalf("fp = %+v", fp)
	}
}

func TestKeyThatExpiresWhileScanningIsSkipped(t *testing.T) {
	f := newFake(50, 1)
	q := func(ctx context.Context, cmd []string) (any, error) {
		if cmd[0] == "TYPE" && cmd[1] == "k:3" {
			return "none", nil
		}
		return f.query(ctx, cmd)
	}
	fp, err := Take(context.Background(), []Source{{"s", q}}, Options{})
	if err != nil || fp.Sampled != 49 {
		t.Fatalf("fp = %+v, err = %v", fp, err)
	}
}

func TestErrorsAndCancel(t *testing.T) {
	bad := func(context.Context, []string) (any, error) { return nil, fmt.Errorf("down") }
	if _, err := Take(context.Background(), []Source{{"s", bad}}, Options{}); err == nil {
		t.Fatal("a failing server must fail the fingerprint")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Take(ctx, []Source{{"s", newFake(10, 1).query}}, Options{}); err == nil {
		t.Fatal("a canceled context must stop the scan")
	}
}
