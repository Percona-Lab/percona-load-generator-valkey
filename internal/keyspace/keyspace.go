// Package keyspace generates key indexes with a chosen access distribution.
package keyspace

import (
	"fmt"
	"math/rand"
	"strconv"
)

const (
	Uniform = "uniform"
	Zipf    = "zipf"
)

// DefaultZipfS is the default zipf skew. math/rand requires s > 1.
const DefaultZipfS = 1.1

// Generator returns key indexes in [0, n). It is not safe for concurrent use:
// create one per worker.
type Generator interface {
	Next() int64
}

// New returns a generator over n keys. zipfS is only used for the zipf
// distribution; 0 selects DefaultZipfS.
func New(dist string, n int64, seed int64, zipfS float64) (Generator, error) {
	if n <= 0 {
		return nil, fmt.Errorf("keyspace: key count must be > 0, got %d", n)
	}
	r := rand.New(rand.NewSource(seed))
	switch dist {
	case "", Uniform:
		return &uniform{r: r, n: n}, nil
	case Zipf:
		if zipfS == 0 {
			zipfS = DefaultZipfS
		}
		if zipfS <= 1 {
			return nil, fmt.Errorf("keyspace: zipf s must be > 1, got %v", zipfS)
		}
		if n < 2 {
			return &uniform{r: r, n: n}, nil
		}
		return &zipf{z: rand.NewZipf(r, zipfS, 1, uint64(n-1))}, nil
	default:
		return nil, fmt.Errorf("keyspace: unknown distribution %q (want uniform or zipf)", dist)
	}
}

type uniform struct {
	r *rand.Rand
	n int64
}

func (u *uniform) Next() int64 { return u.r.Int63n(u.n) }

type zipf struct{ z *rand.Zipf }

func (z *zipf) Next() int64 { return int64(z.z.Uint64()) }

// Key builds the key name for an index.
func Key(prefix string, idx int64) string {
	return prefix + strconv.FormatInt(idx, 10)
}
