package workloadrec

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// ValidCompression reports whether c names a supported compression.
func ValidCompression(c string) bool {
	switch c {
	case "", CompressionNone, CompressionZstd, CompressionGzip:
		return true
	}
	return false
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// newCompressor wraps w for compression c, at the fastest level and on one
// goroutine, so recording does not compete with the workload for CPU.
func newCompressor(w io.Writer, c string) (io.WriteCloser, error) {
	switch c {
	case "", CompressionNone:
		return nopWriteCloser{w}, nil
	case CompressionZstd:
		return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	case CompressionGzip:
		return gzip.NewWriterLevel(w, gzip.BestSpeed)
	}
	return nil, fmt.Errorf("unknown recording compression %q (want none, zstd or gzip)", c)
}

// decompress returns the content of br, detecting the compression from its
// magic bytes, and a func that releases the decoder.
func decompress(br *bufio.Reader) (io.Reader, string, func(), error) {
	magic, _ := br.Peek(len(zstdMagic))
	switch {
	case bytes.HasPrefix(magic, zstdMagic):
		d, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, "", nil, fmt.Errorf("open zstd recording: %w", err)
		}
		return d, CompressionZstd, d.Close, nil
	case bytes.HasPrefix(magic, gzipMagic):
		g, err := gzip.NewReader(br)
		if err != nil {
			return nil, "", nil, fmt.Errorf("open gzip recording: %w", err)
		}
		return g, CompressionGzip, func() { g.Close() }, nil
	}
	return br, CompressionNone, func() {}, nil
}
