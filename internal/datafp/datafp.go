// Package datafp fingerprints the data a server holds: the number of keys and
// a digest of a sample of them (name, type and size). A recording stores the
// fingerprint of the data it started with, and a replay compares its target
// with it, so a replay on different data is reported instead of trusted.
//
// The sample is chosen by key name (a hash of the name, not the order SCAN
// returns them), so two servers that hold the same keys pick the same sample.
// The package does not import the Valkey client: it takes a Query function.
package datafp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

// Query runs one command and returns its reply as plain Go values.
type Query func(ctx context.Context, cmd []string) (any, error)

// Source is one server (a cluster has one per primary).
type Source struct {
	Name  string
	Query Query
}

// Options tune Take.
type Options struct {
	// Samples is the number of keys to digest, about (default 1000).
	Samples int
	// MaxScan caps the keys visited while scanning (default 2,000,000). A
	// larger server gives a partial fingerprint that compares key counts only.
	MaxScan int
}

// Fingerprint is the starting data of a run. It holds no key names or values.
type Fingerprint struct {
	DBSize  int64  `json:"dbsize"`
	Stride  int    `json:"stride"`           // one key in Stride is digested
	Sampled int    `json:"sampled"`          // keys digested
	Digest  string `json:"digest,omitempty"` // sha256 over the sampled keys; empty when partial
	Partial bool   `json:"partial,omitempty"`
	Sources int    `json:"sources"`
}

// Check is the result of comparing a target with a recorded fingerprint.
type Check struct {
	Match       bool     `json:"match"`
	Differences []string `json:"differences,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

var lenCmd = map[string]string{"string": "STRLEN", "hash": "HLEN", "list": "LLEN", "set": "SCARD", "zset": "ZCARD", "stream": "XLEN"}

func strideFor(dbsize int64, samples int) int {
	s := 1
	for int64(s)*int64(samples) < dbsize {
		s <<= 1
	}
	return s
}

func keyHash(k string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k))
	return h.Sum64()
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// Take fingerprints the data on the given sources. It only reads.
func Take(ctx context.Context, srcs []Source, opt Options) (*Fingerprint, error) {
	if opt.Samples <= 0 {
		opt.Samples = 1000
	}
	if opt.MaxScan <= 0 {
		opt.MaxScan = 2_000_000
	}
	fp := &Fingerprint{Sources: len(srcs)}
	for _, s := range srcs {
		v, err := s.Query(ctx, []string{"DBSIZE"})
		if err != nil {
			return nil, fmt.Errorf("DBSIZE on %s: %w", s.Name, err)
		}
		fp.DBSize += asInt(v)
	}
	fp.Stride = strideFor(fp.DBSize, opt.Samples)
	var lines []string
	visited := 0
	for _, s := range srcs {
		cursor := "0"
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			v, err := s.Query(ctx, []string{"SCAN", cursor, "COUNT", "1000"})
			if err != nil {
				return nil, fmt.Errorf("SCAN on %s: %w", s.Name, err)
			}
			arr, ok := v.([]any)
			if !ok || len(arr) != 2 {
				return nil, fmt.Errorf("SCAN on %s: unexpected reply", s.Name)
			}
			cursor, _ = arr[0].(string)
			keys, _ := arr[1].([]any)
			for _, k := range keys {
				key, _ := k.(string)
				visited++
				if keyHash(key)%uint64(fp.Stride) != 0 {
					continue
				}
				line, err := describe(ctx, s.Query, key)
				if err != nil {
					return nil, err
				}
				if line != "" {
					lines = append(lines, line)
				}
			}
			if cursor == "0" || cursor == "" {
				break
			}
			if visited >= opt.MaxScan {
				fp.Partial = true
				break
			}
		}
		if fp.Partial {
			break
		}
	}
	fp.Sampled = len(lines)
	if !fp.Partial {
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		fp.Digest = hex.EncodeToString(sum[:])
	}
	return fp, nil
}

// describe is "key|type|size" for one key, or "" when the key expired meanwhile.
func describe(ctx context.Context, q Query, key string) (string, error) {
	v, err := q(ctx, []string{"TYPE", key})
	if err != nil {
		return "", fmt.Errorf("TYPE: %w", err)
	}
	typ, _ := v.(string)
	if typ == "none" || typ == "" {
		return "", nil
	}
	size := int64(0)
	if c, ok := lenCmd[typ]; ok {
		n, err := q(ctx, []string{c, key})
		if err != nil {
			return "", fmt.Errorf("%s: %w", c, err)
		}
		size = asInt(n)
	}
	return key + "|" + typ + "|" + strconv.FormatInt(size, 10), nil
}

// Compare checks a target's fingerprint (got) against the recorded one (want).
func Compare(want, got *Fingerprint) Check {
	var out Check
	if want == nil {
		out.Notes = append(out.Notes, "the recording has no data fingerprint; the starting data was not checked")
		out.Match = true
		return out
	}
	if got == nil {
		out.Differences = append(out.Differences, "the target could not be fingerprinted")
		return out
	}
	if want.DBSize != got.DBSize {
		out.Differences = append(out.Differences, fmt.Sprintf("the recording started with %d keys, this target has %d", want.DBSize, got.DBSize))
	}
	switch {
	case want.Partial || got.Partial:
		out.Notes = append(out.Notes, "the key digest was not compared: the data was too large to scan completely, so only the key counts were checked")
	case want.Digest != got.Digest && want.DBSize == got.DBSize:
		out.Differences = append(out.Differences, fmt.Sprintf("the same number of keys, but the sampled key names, types or sizes differ (%d keys sampled)", want.Sampled))
	case want.Digest != got.Digest:
		out.Notes = append(out.Notes, "key digests differ")
	}
	out.Match = len(out.Differences) == 0
	return out
}
