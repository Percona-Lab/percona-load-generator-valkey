package values

import (
	"math/rand"
	"testing"
)

func TestSizeAndDeterminism(t *testing.T) {
	a, err := NewSource(256, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSource(256, 7)
	ra, rb := rand.New(rand.NewSource(1)), rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		x, y := a.Next(ra), b.Next(rb)
		if len(x) != 256 {
			t.Fatalf("len %d, want 256", len(x))
		}
		if x != y {
			t.Fatal("same seeds produced different values")
		}
	}
}

func TestBadSize(t *testing.T) {
	if _, err := NewSource(0, 1); err == nil {
		t.Fatal("want error for size 0")
	}
}
