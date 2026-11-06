package workloadrec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultQueueSize bounds the entries waiting for the writer goroutine.
const DefaultQueueSize = 1 << 16

const (
	stateRecording int32 = iota
	stateTruncated
	stateFailed
)

const (
	reasonQueueFull = "writer_queue_full: the writer could not keep up with the workload"
	reasonMaxBytes  = "max_bytes_reached: the file reached the size limit"
)

var headerPrefix = []byte(`{"type":"header"`)

// Entry is one sent command. Record encodes Cmd on the calling goroutine, so
// the slice may change once Record returns.
type Entry struct {
	Worker  int
	Offset  time.Duration
	Group   uint64
	Conn    string
	Cmd     []string
	Outcome *Outcome

	raw []byte
	b64 []int
}

// Options configure a Recorder. The zero value is an unlimited, uncompressed
// recording that fails on backpressure.
type Options struct {
	MaxBytes     int64  // cap on the uncompressed bytes; <= 0 is unlimited
	QueueSize    int    // entries waiting for the writer; <= 0 uses DefaultQueueSize
	Backpressure string // BackpressureFail (default) or BackpressureBlock
	Compression  string // none (default), zstd or gzip
}

// Policy is the backpressure policy text stated in headers and logs.
func (o Options) Policy() string {
	if o.Backpressure == BackpressureBlock {
		return BlockPolicy
	}
	return FailurePolicy
}

// Result is the state of a recording. It never holds arguments.
type Result struct {
	Path         string
	Status       string
	Reason       string
	Ops          uint64
	Groups       uint64
	Bytes        int64 // uncompressed
	FileBytes    int64 // on disk once closed
	CommandsHash string
	Compression  string
	Backpressure string
	Waits        uint64
	WaitTotal    time.Duration
	WaitMax      time.Duration
	Outcomes     bool
	Checksums    bool
}

// Recorder appends commands to a recording file from one writer goroutine.
// When the bounded queue is full, Record either marks the recording failed and
// stops (BackpressureFail) or waits for the writer (BackpressureBlock).
type Recorder struct {
	path        string
	maxBytes    int64
	reserve     int64
	block       bool
	compression string
	outcomes    bool
	checksums   bool
	ch          chan Entry
	done        chan struct{}
	state       atomic.Int32
	ops         atomic.Uint64
	bytes       atomic.Int64
	waits       atomic.Uint64
	waitTotal   atomic.Int64
	waitMax     atomic.Int64
	start       time.Time
	startSet    atomic.Bool
	halted      atomic.Bool // the writer stopped (size limit, write error): later entries are discarded

	mu     sync.Mutex
	reason string

	f         *os.File
	file      *countingWriter
	comp      io.WriteCloser
	w         *bufio.Writer
	buf       bytes.Buffer
	seq       uint64
	workerSeq map[int]uint64
	lastGroup map[int]uint64
	groups    uint64
	hashes    map[int]*StreamHash
	commands  map[string]uint64

	closeOnce sync.Once
	result    Result

	// beforeWrite is a test hook run on the writer goroutine.
	beforeWrite func()
}

// NewRecorder creates (or truncates) path with mode 0600, writes the header
// and starts the writer.
func NewRecorder(path string, opts Options, h Header) (*Recorder, error) {
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	if opts.MaxBytes < 0 {
		opts.MaxBytes = 0
	}
	if opts.Backpressure == "" {
		opts.Backpressure = BackpressureFail
	}
	if opts.Backpressure != BackpressureFail && opts.Backpressure != BackpressureBlock {
		return nil, fmt.Errorf("unknown recording backpressure %q (want fail or block)", opts.Backpressure)
	}
	if opts.Compression == "" {
		opts.Compression = CompressionNone
	}
	if !ValidCompression(opts.Compression) {
		return nil, fmt.Errorf("unknown recording compression %q (want none, zstd or gzip)", opts.Compression)
	}
	if err := checkOverwrite(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open recording: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("set recording permissions: %w", err)
	}
	cw := &countingWriter{w: f}
	comp, err := newCompressor(cw, opts.Compression)
	if err != nil {
		f.Close()
		return nil, err
	}
	h.Type, h.Format, h.FailurePolicy, h.MaxBytes = LineHeader, FormatVersion, opts.Policy(), opts.MaxBytes
	h.Backpressure = ""
	if opts.Backpressure == BackpressureBlock {
		h.Backpressure = BackpressureBlock
	}
	h.Outcomes = h.Outcomes || h.ResultChecksums
	if h.CreatedAt == "" {
		h.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r := &Recorder{
		path: path, maxBytes: opts.MaxBytes, reserve: 2048 + 250*int64(h.Workers+1),
		block: opts.Backpressure == BackpressureBlock, compression: opts.Compression, outcomes: h.Outcomes, checksums: h.ResultChecksums,
		ch: make(chan Entry, opts.QueueSize), done: make(chan struct{}),
		f: f, file: cw, comp: comp, w: bufio.NewWriterSize(comp, 1<<20),
		workerSeq: map[int]uint64{}, lastGroup: map[int]uint64{}, hashes: map[int]*StreamHash{}, commands: map[string]uint64{},
		start: time.Now(),
	}
	if err := r.writeLine(h); err != nil {
		f.Close()
		return nil, fmt.Errorf("write recording header: %w", err)
	}
	if r.maxBytes > 0 && r.bytes.Load() > r.maxBytes {
		f.Close()
		return nil, fmt.Errorf("the recording size limit (%d bytes) is smaller than the header", r.maxBytes)
	}
	if err := r.w.Flush(); err != nil {
		f.Close()
		return nil, fmt.Errorf("write recording header: %w", err)
	}
	go r.run()
	return r, nil
}

// checkOverwrite allows replacing only an empty file or an earlier recording,
// so a mistyped path never truncates unrelated data.
func checkOverwrite(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open recording: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("recording path %s is not a regular file", path)
	}
	if fi.Size() == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open recording: %w", err)
	}
	defer f.Close()
	refuse := fmt.Errorf("%s exists and is not a plgv recording; refusing to overwrite it", path)
	content, _, release, err := decompress(bufio.NewReader(f))
	if err != nil {
		return refuse
	}
	defer release()
	buf := make([]byte, len(headerPrefix))
	if _, err := io.ReadFull(content, buf); err != nil || !bytes.Equal(buf, headerPrefix) {
		return refuse
	}
	return nil
}

// SetStart sets the time command offsets are measured from. Only the first
// call counts.
func (r *Recorder) SetStart(t time.Time) {
	if r != nil && r.startSet.CompareAndSwap(false, true) {
		r.start = t
	}
}

// Offset is t relative to the start of the workload.
func (r *Recorder) Offset(t time.Time) time.Duration { return t.Sub(r.start) }

// Active reports whether commands are still being recorded.
func (r *Recorder) Active() bool { return r != nil && r.state.Load() == stateRecording }

// WantsOutcomes and WantsChecksums say what the header promised.
func (r *Recorder) WantsOutcomes() bool  { return r != nil && r.outcomes }
func (r *Recorder) WantsChecksums() bool { return r != nil && r.checksums }

// Record queues one command. It must not be called after Close.
func (r *Recorder) Record(e Entry) {
	if r == nil || r.state.Load() != stateRecording {
		return
	}
	e.raw, e.b64 = EncodeCmd(e.Cmd)
	e.Cmd = nil
	select {
	case r.ch <- e:
		return
	default:
	}
	if !r.block {
		r.stop(stateFailed, reasonQueueFull)
		return
	}
	// The writer keeps draining after a stop, so this send always completes.
	t0 := time.Now()
	r.ch <- e
	r.addWait(time.Since(t0))
}

func (r *Recorder) addWait(d time.Duration) {
	r.waits.Add(1)
	r.waitTotal.Add(int64(d))
	for {
		cur := r.waitMax.Load()
		if int64(d) <= cur || r.waitMax.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

// Fail marks the recording failed; later commands are not recorded.
func (r *Recorder) Fail(reason string) {
	if r != nil {
		r.stop(stateFailed, reason)
	}
}

func (r *Recorder) stop(state int32, reason string) {
	if !r.state.CompareAndSwap(stateRecording, state) {
		return
	}
	r.mu.Lock()
	r.reason = reason
	r.mu.Unlock()
}

func (r *Recorder) run() {
	defer close(r.done)
	// After a queue-full failure the entries already queued are still written:
	// each worker queued them in order before the failure, so they are a prefix.
	for e := range r.ch {
		if r.halted.Load() {
			continue
		}
		r.write(e)
	}
}

// opWire is the line as written: Cmd is already encoded.
type opWire struct {
	Type      string          `json:"type"`
	Seq       uint64          `json:"seq"`
	Worker    int             `json:"worker"`
	WorkerSeq uint64          `json:"worker_seq"`
	OffsetUS  int64           `json:"offset_us"`
	Group     uint64          `json:"group"`
	Conn      string          `json:"conn,omitempty"`
	Cmd       json.RawMessage `json:"cmd"`
	B64       []int           `json:"b64,omitempty"`
	Outcome   *Outcome        `json:"outcome,omitempty"`
}

func (r *Recorder) write(e Entry) {
	if r.beforeWrite != nil {
		r.beforeWrite()
	}
	line, _ := json.Marshal(opWire{Type: LineOp, Seq: r.seq + 1, Worker: e.Worker, WorkerSeq: r.workerSeq[e.Worker] + 1,
		OffsetUS: e.Offset.Microseconds(), Group: e.Group, Conn: e.Conn, Cmd: e.raw, B64: e.b64, Outcome: e.Outcome})
	line = append(line, '\n')
	if r.maxBytes > 0 && r.bytes.Load()+int64(len(line))+r.reserve > r.maxBytes {
		r.halted.Store(true)
		r.stop(stateTruncated, reasonMaxBytes)
		return
	}
	if _, err := r.w.Write(line); err != nil {
		r.halted.Store(true)
		r.stop(stateFailed, "write_error: "+err.Error())
		return
	}
	r.seq++
	r.workerSeq[e.Worker]++
	if e.Group != r.lastGroup[e.Worker] {
		r.lastGroup[e.Worker] = e.Group
		r.groups++
	}
	r.bytes.Add(int64(len(line)))
	r.ops.Add(1)
	hs := r.hashes[e.Worker]
	if hs == nil {
		hs = NewStreamHash()
		r.hashes[e.Worker] = hs
	}
	hs.Add(e.raw)
	// The name is the first element of the encoded command.
	var args []string
	if json.Unmarshal(e.raw, &args) == nil {
		r.commands[Name(args)]++
	}
}

func (r *Recorder) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := r.w.Write(b); err != nil {
		return err
	}
	r.bytes.Add(int64(len(b)))
	return nil
}

func (r *Recorder) baseResult() Result {
	res := Result{Path: r.path, Ops: r.ops.Load(), Bytes: r.bytes.Load(), Compression: r.compression, Backpressure: BackpressureFail,
		Outcomes: r.outcomes, Checksums: r.checksums}
	if r.block {
		res.Backpressure = BackpressureBlock
		res.Waits, res.WaitTotal, res.WaitMax = r.waits.Load(), time.Duration(r.waitTotal.Load()), time.Duration(r.waitMax.Load())
	}
	return res
}

// Snapshot is the state while the recording runs.
func (r *Recorder) Snapshot() Result {
	if r == nil {
		return Result{}
	}
	res := r.baseResult()
	res.Status, res.Reason = r.status()
	if res.Status == "" {
		res.Status = "recording"
	}
	return res
}

func (r *Recorder) status() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state.Load() {
	case stateTruncated:
		return StatusTruncated, r.reason
	case stateFailed:
		return StatusFailed, r.reason
	}
	return "", ""
}

// Close drains the queue, writes the trailer and closes the file. Call it once
// every worker has stopped calling Record.
func (r *Recorder) Close() Result {
	if r == nil {
		return Result{}
	}
	r.closeOnce.Do(func() {
		close(r.ch)
		<-r.done
		status, reason := r.status()
		if status == "" {
			status = StatusComplete
		}
		t := Trailer{Type: LineTrailer, Status: status, Reason: reason, Ops: r.ops.Load(), Groups: r.groups,
			WorkerOps: map[string]uint64{}, WorkerHashes: map[string]string{}, Commands: r.commands}
		sums := make(map[int]string, len(r.hashes))
		for id, h := range r.hashes {
			sums[id] = h.Sum()
			t.WorkerHashes[strconv.Itoa(id)] = sums[id]
			t.WorkerOps[strconv.Itoa(id)] = r.workerSeq[id]
		}
		t.CommandsHash = CombineHashes(sums)
		res := r.baseResult()
		if r.block {
			t.Waits, t.WaitTotalUS, t.WaitMaxUS = res.Waits, res.WaitTotal.Microseconds(), res.WaitMax.Microseconds()
		}
		t.Bytes = r.bytes.Load()
		err := r.writeLine(t)
		if err == nil {
			err = r.w.Flush()
		}
		if cerr := r.comp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = r.f.Sync()
		}
		if cerr := r.f.Close(); err == nil {
			err = cerr
		}
		if err != nil && status == StatusComplete {
			status, reason = StatusFailed, "write_error: "+err.Error()
		}
		res.Status, res.Reason, res.Ops, res.Groups, res.Bytes, res.CommandsHash = status, reason, t.Ops, t.Groups, r.bytes.Load(), t.CommandsHash
		res.FileBytes = r.file.n
		r.result = res
	})
	return r.result
}
