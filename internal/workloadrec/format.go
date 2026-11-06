// Package workloadrec reads and writes workload recordings: NDJSON files that
// hold every command a run sent, with its literal arguments, so the same
// commands can be replayed on another server.
//
// A file is a header line, one line per command and a trailer line. The file
// may be compressed with zstd or gzip; readers detect that from the first
// bytes. Commands that were sent together (a pipeline, a MULTI/EXEC block)
// share a group number. Arguments that are not valid UTF-8 are stored as
// base64 and listed in b64, so any byte string survives the round trip.
package workloadrec

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
)

const (
	// FormatVersion identifies the file layout.
	FormatVersion = "plgv-workload-record/1"

	LineHeader  = "header"
	LineOp      = "op"
	LineTrailer = "trailer"

	StatusComplete  = "complete"
	StatusTruncated = "truncated"
	StatusFailed    = "failed"
	// StatusIncomplete is reported for a file without a trailer (the writer
	// did not finish, for example because the process stopped).
	StatusIncomplete = "incomplete"

	// Backpressure policies: what a worker does when the writer queue is full.
	BackpressureFail  = "fail"
	BackpressureBlock = "block"

	FailurePolicy = "fail: when the writer queue is full the recording stops and is marked failed; commands are never dropped silently, and the file keeps a prefix of every worker's stream"
	BlockPolicy   = "block: when the writer queue is full workers wait for the writer, so every command is recorded and throughput may drop; the trailer reports how often and how long they waited"

	CompressionNone = "none"
	CompressionZstd = "zstd"
	CompressionGzip = "gzip"

	// Outcome statuses.
	OutcomeOK    = "ok"
	OutcomeMiss  = "miss"
	OutcomeError = "error"

	// ConnDedicated marks commands sent on one dedicated connection (WATCH).
	ConnDedicated = "dedicated"
)

// ConfigSummary is the workload a recording was made with. It never holds the
// target address or credentials.
type ConfigSummary struct {
	Concurrency int    `json:"concurrency"`
	Duration    string `json:"duration,omitempty"`
	Mix         string `json:"mix,omitempty"`
	LoadProfile string `json:"load_profile,omitempty"`
	ValueSize   int    `json:"value_size,omitempty"`
	KeyCount    int64  `json:"key_count,omitempty"`
}

// Header is the first line of a recording.
type Header struct {
	Type          string        `json:"type"`
	Format        string        `json:"format"`
	PLGVVersion   string        `json:"plgv_version"`
	CreatedAt     string        `json:"created_at"`
	ServerVersion string        `json:"server_version,omitempty"`
	Mode          string        `json:"mode,omitempty"` // standalone, cluster or sentinel
	Seed          int64         `json:"seed"`
	Workers       int           `json:"workers"`
	Config        ConfigSummary `json:"config"`
	FailurePolicy string        `json:"failure_policy"`
	// MaxBytes caps the uncompressed bytes of the file; 0 is unlimited.
	MaxBytes     int64  `json:"max_bytes"`
	Backpressure string `json:"backpressure,omitempty"` // "block" only; empty is fail
	// Outcomes is set when op lines carry their outcome; ResultChecksums when
	// each outcome also carries a checksum of the reply.
	Outcomes        bool `json:"outcomes,omitempty"`
	ResultChecksums bool `json:"result_checksums,omitempty"`
	// DataFingerprint describes the data the run started with.
	DataFingerprint *datafp.Fingerprint `json:"data_fingerprint,omitempty"`
	// Import is set when the recording was converted from captured traffic
	// (MONITOR, SLOWLOG) instead of recorded by a run.
	Import *ImportInfo `json:"import,omitempty"`
}

// ImportInfo describes the captured traffic an imported recording came from.
// It holds counts and file names, never arguments.
type ImportInfo struct {
	Sources         []string          `json:"sources"` // monitor, slowlog
	Files           []string          `json:"files,omitempty"`
	Entries         uint64            `json:"entries"`
	Converted       uint64            `json:"converted"`
	Skipped         uint64            `json:"skipped"`
	SkippedByReason map[string]uint64 `json:"skipped_by_reason,omitempty"`
	Connections     int               `json:"connections"`
	FirstTimestamp  string            `json:"first_timestamp,omitempty"`
	LastTimestamp   string            `json:"last_timestamp,omitempty"`
	Note            string            `json:"note"`
}

// Outcome is what one command returned when it was recorded.
type Outcome struct {
	Status string `json:"s"`
	Class  string `json:"c,omitempty"` // error class, when Status is error
	US     int64  `json:"us"`          // latency of the batch the command was in
	Len    int    `json:"n,omitempty"` // reply size: bytes of a string, elements of a collection
	Sum    string `json:"h,omitempty"` // checksum of the reply (result_checksums only)
}

// OpLine is one recorded command.
type OpLine struct {
	Type      string `json:"type"`
	Seq       uint64 `json:"seq"`
	Worker    int    `json:"worker"`
	WorkerSeq uint64 `json:"worker_seq"`
	OffsetUS  int64  `json:"offset_us"`
	// Group numbers the batch the command was sent in, per worker. Consecutive
	// commands of one worker with the same group were sent together.
	Group uint64   `json:"group"`
	Conn  string   `json:"conn,omitempty"`
	Cmd   []string `json:"cmd"`
	B64   []int    `json:"b64,omitempty"`
	// Outcome is set when the recording captured outcomes.
	Outcome *Outcome `json:"outcome,omitempty"`
}

// Args returns the command's arguments with base64 ones decoded.
func (o *OpLine) Args() ([]string, error) {
	if len(o.B64) == 0 {
		return o.Cmd, nil
	}
	out := append([]string(nil), o.Cmd...)
	for _, i := range o.B64 {
		if i < 0 || i >= len(out) {
			return nil, fmt.Errorf("op %d: b64 index %d is outside the command", o.Seq, i)
		}
		b, err := base64.StdEncoding.DecodeString(out[i])
		if err != nil {
			return nil, fmt.Errorf("op %d: argument %d is not valid base64", o.Seq, i)
		}
		out[i] = string(b)
	}
	return out, nil
}

// Trailer is the last line of a finished recording. CommandsHash is the sha256
// of the per-worker command hashes in worker order; a replay that sends the
// same commands per worker reports the same hash.
type Trailer struct {
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	Ops          uint64            `json:"ops"`
	Groups       uint64            `json:"groups"`
	Bytes        int64             `json:"bytes"`
	WorkerOps    map[string]uint64 `json:"worker_ops,omitempty"`
	WorkerHashes map[string]string `json:"worker_hashes,omitempty"`
	CommandsHash string            `json:"commands_hash,omitempty"`
	// Commands counts the recorded commands by name.
	Commands map[string]uint64 `json:"commands,omitempty"`
	// Waits, WaitTotalUS and WaitMaxUS report how often and how long workers
	// waited for the writer (backpressure block only).
	Waits       uint64 `json:"waits,omitempty"`
	WaitTotalUS int64  `json:"wait_total_us,omitempty"`
	WaitMaxUS   int64  `json:"wait_max_us,omitempty"`
}

// EncodeCmd returns the canonical JSON of a command's arguments and the
// indexes of the ones stored as base64. The recorder writes exactly these
// bytes and both the recorder and the replay hash them, so equal commands
// give equal hashes.
func EncodeCmd(cmd []string) (raw []byte, b64 []int) {
	args := make([]string, len(cmd))
	for i, a := range cmd {
		if utf8.ValidString(a) {
			args[i] = a
			continue
		}
		args[i] = base64.StdEncoding.EncodeToString([]byte(a))
		b64 = append(b64, i)
	}
	raw, _ = json.Marshal(args) // a []string always encodes
	return raw, b64
}

// Name is the upper-case command name ("GET", "XGROUP CREATE" is just "XGROUP").
func Name(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	b := []byte(cmd[0])
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// HashBytes is the hex sha256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// StreamHash hashes one worker's commands in order.
type StreamHash struct{ h hash.Hash }

func NewStreamHash() *StreamHash { return &StreamHash{h: sha256.New()} }

// Add adds one command (the bytes EncodeCmd returned).
func (s *StreamHash) Add(raw []byte) {
	_, _ = s.h.Write(raw)
	_, _ = s.h.Write([]byte{'\n'})
}

func (s *StreamHash) Sum() string { return hex.EncodeToString(s.h.Sum(nil)) }

// CombineHashes is the sha256 of the per-worker hashes in worker order.
func CombineHashes(workerHashes map[int]string) string {
	ids := make([]int, 0, len(workerHashes))
	for id := range workerHashes {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(strconv.Itoa(id) + ":" + workerHashes[id] + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
