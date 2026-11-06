// Package values produces value payloads without per-call allocation.
package values

import (
	"fmt"
	"math/rand"
)

// Source hands out fixed-size values sliced from a pre-generated random buffer.
type Source struct {
	buf  string
	size int
}

// NewSource creates a source of values of the given size in bytes.
func NewSource(size int, seed int64) (*Source, error) {
	if size <= 0 {
		return nil, fmt.Errorf("values: size must be > 0, got %d", size)
	}
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, size+4096)
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return &Source{buf: string(b), size: size}, nil
}

// Next returns a value of the configured size, starting at a random offset.
func (s *Source) Next(r *rand.Rand) string {
	off := r.Intn(len(s.buf) - s.size + 1)
	return s.buf[off : off+s.size]
}
