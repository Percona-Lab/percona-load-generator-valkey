package keyspace

import "testing"

func TestRangeAndDeterminism(t *testing.T) {
	for _, dist := range []string{Uniform, Zipf} {
		a, err := New(dist, 1000, 42, 0)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := New(dist, 1000, 42, 0)
		for i := 0; i < 5000; i++ {
			x, y := a.Next(), b.Next()
			if x != y {
				t.Fatalf("%s: same seed diverged at %d: %d vs %d", dist, i, x, y)
			}
			if x < 0 || x >= 1000 {
				t.Fatalf("%s: index %d out of range", dist, x)
			}
		}
	}
}

func TestZipfIsSkewed(t *testing.T) {
	g, _ := New(Zipf, 10000, 1, 1.2)
	hot := 0
	const draws = 100000
	for i := 0; i < draws; i++ {
		if g.Next() < 100 { // hottest 1% of keys
			hot++
		}
	}
	if hot < draws/2 {
		t.Fatalf("zipf: hottest 1%% of keys got only %d/%d draws", hot, draws)
	}
}

func TestUniformIsSpread(t *testing.T) {
	g, _ := New(Uniform, 100, 1, 0)
	seen := map[int64]bool{}
	for i := 0; i < 10000; i++ {
		seen[g.Next()] = true
	}
	if len(seen) < 95 {
		t.Fatalf("uniform covered only %d/100 keys", len(seen))
	}
}

func TestErrors(t *testing.T) {
	if _, err := New(Uniform, 0, 1, 0); err == nil {
		t.Error("want error for n=0")
	}
	if _, err := New("bogus", 10, 1, 0); err == nil {
		t.Error("want error for unknown distribution")
	}
	if _, err := New(Zipf, 10, 1, 0.9); err == nil {
		t.Error("want error for zipf s <= 1")
	}
}

func TestKey(t *testing.T) {
	if got := Key("plgv:", 42); got != "plgv:42" {
		t.Fatalf("got %q", got)
	}
}
