package workloadrec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxLine bounds one line (a command with a very large value).
const maxLine = 64 << 20

// FileInfo describes a recording on disk.
type FileInfo struct {
	Path        string
	Header      Header
	Trailer     *Trailer // nil when the file has no trailer
	Status      string   // the trailer's status, or StatusIncomplete
	Reason      string
	Compression string
	Ops         uint64 // op lines found
	Groups      uint64
	FileBytes   int64
	// CommandsHash is recomputed from the op lines, so it is valid even when
	// the trailer is missing.
	CommandsHash string
	Commands     map[string]uint64
}

// Reader reads a recording line by line.
type Reader struct {
	f           *os.File
	r           *bufio.Reader
	release     func()
	line        int
	header      Header
	trailer     *Trailer
	compression string
}

// Open opens a recording and reads its header.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recording: %w", err)
	}
	content, comp, release, err := decompress(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		f.Close()
		return nil, err
	}
	rd := &Reader{f: f, r: bufio.NewReaderSize(content, 1<<20), release: release, compression: comp}
	b, err := rd.readLine()
	if err != nil {
		rd.Close()
		return nil, fmt.Errorf("%s is empty or not a recording", path)
	}
	var h Header
	if err := json.Unmarshal(b, &h); err != nil || h.Type != LineHeader {
		rd.Close()
		return nil, fmt.Errorf("%s is not a plgv recording (the first line is not a header)", path)
	}
	if h.Format != FormatVersion {
		rd.Close()
		return nil, fmt.Errorf("%s has format %q; this version reads %q", path, h.Format, FormatVersion)
	}
	rd.header = h
	return rd, nil
}

func (r *Reader) Close() {
	r.release()
	r.f.Close()
}

// Header is the recording's first line.
func (r *Reader) Header() Header { return r.header }

// Trailer is the last line, known once Next has returned io.EOF; nil when the
// file ended without one.
func (r *Reader) Trailer() *Trailer { return r.trailer }

// Compression is none, zstd or gzip.
func (r *Reader) Compression() string { return r.compression }

func (r *Reader) readLine() ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > maxLine {
			return nil, fmt.Errorf("line %d is longer than %d bytes", r.line+1, maxLine)
		}
		switch {
		case err == nil:
			r.line++
			return out, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(out) > 0:
			r.line++
			return out, nil
		default:
			return nil, err
		}
	}
}

// Next returns the next command, or io.EOF at the trailer or the end of file.
func (r *Reader) Next() (*OpLine, error) {
	for {
		b, err := r.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read recording: %w", err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(b, &probe); err != nil {
			return nil, fmt.Errorf("line %d: not JSON: %w", r.line, err)
		}
		switch probe.Type {
		case LineOp:
			var op OpLine
			if err := json.Unmarshal(b, &op); err != nil {
				return nil, fmt.Errorf("line %d: %w", r.line, err)
			}
			if len(op.Cmd) == 0 {
				return nil, fmt.Errorf("line %d: op without a command", r.line)
			}
			return &op, nil
		case LineTrailer:
			var t Trailer
			if err := json.Unmarshal(b, &t); err != nil {
				return nil, fmt.Errorf("line %d: %w", r.line, err)
			}
			r.trailer = &t
			return nil, io.EOF
		default:
			return nil, fmt.Errorf("line %d: unknown line type %q", r.line, probe.Type)
		}
	}
}

// Stream calls fn for every command in order.
func Stream(ctx context.Context, path string, fn func(*OpLine) error) error {
	r, err := Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		op, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(op); err != nil {
			return err
		}
	}
}

// Scan reads a whole recording and reports what it holds. It recomputes the
// command hash, so it also detects a file that was edited.
func Scan(path string) (*FileInfo, error) {
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	info := &FileInfo{Path: path, Header: r.Header(), Compression: r.Compression(), Commands: map[string]uint64{}}
	if st, err := os.Stat(path); err == nil {
		info.FileBytes = st.Size()
	}
	hashes := map[int]*StreamHash{}
	last := map[int]uint64{}
	for {
		op, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		info.Ops++
		if op.Group != last[op.Worker] {
			last[op.Worker] = op.Group
			info.Groups++
		}
		args, err := op.Args()
		if err != nil {
			return nil, err
		}
		raw, _ := EncodeCmd(args)
		h := hashes[op.Worker]
		if h == nil {
			h = NewStreamHash()
			hashes[op.Worker] = h
		}
		h.Add(raw)
		info.Commands[Name(args)]++
	}
	sums := map[int]string{}
	for id, h := range hashes {
		sums[id] = h.Sum()
	}
	info.CommandsHash = CombineHashes(sums)
	info.Trailer = r.Trailer()
	if info.Trailer == nil {
		info.Status = StatusIncomplete
		info.Reason = "the file has no trailer: the recording did not finish"
	} else {
		info.Status, info.Reason = info.Trailer.Status, info.Trailer.Reason
		if info.Trailer.Ops != info.Ops {
			info.Status, info.Reason = StatusFailed, fmt.Sprintf("the trailer counts %d commands but the file holds %d", info.Trailer.Ops, info.Ops)
		} else if info.Trailer.CommandsHash != "" && info.Trailer.CommandsHash != info.CommandsHash {
			info.Status, info.Reason = StatusFailed, "the commands do not match the trailer's hash: the file was changed"
		}
	}
	return info, nil
}
