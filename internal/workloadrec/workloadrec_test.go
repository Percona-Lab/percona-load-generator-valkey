package workloadrec

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func hdr(workers int) Header {
	return Header{PLGVVersion: "test", Seed: 7, Workers: workers, Config: ConfigSummary{Concurrency: workers}}
}

func record(t *testing.T, path string, opt Options, h Header, entries []Entry) Result {
	t.Helper()
	r, err := NewRecorder(path, opt, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		r.Record(e)
	}
	return r.Close()
}

func sample() []Entry {
	return []Entry{
		{Worker: 0, Offset: 10 * time.Microsecond, Group: 1, Cmd: []string{"SET", "k1", "v1"}},
		{Worker: 1, Offset: 20 * time.Microsecond, Group: 1, Cmd: []string{"GET", "k1"}, Outcome: &Outcome{Status: OutcomeOK, US: 5, Len: 2, Sum: "00ff"}},
		{Worker: 0, Offset: 30 * time.Microsecond, Group: 2, Cmd: []string{"MULTI"}, Conn: ConnDedicated},
		{Worker: 0, Offset: 30 * time.Microsecond, Group: 2, Cmd: []string{"INCR", "c"}, Conn: ConnDedicated},
		{Worker: 0, Offset: 30 * time.Microsecond, Group: 2, Cmd: []string{"EXEC"}, Conn: ConnDedicated},
	}
}

func TestRoundTripAllCompressions(t *testing.T) {
	for _, comp := range []string{CompressionNone, CompressionZstd, CompressionGzip} {
		t.Run(comp, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "r.rec")
			h := hdr(2)
			h.Outcomes = true
			res := record(t, p, Options{Compression: comp}, h, sample())
			if res.Status != StatusComplete || res.Ops != 5 || res.Groups != 3 {
				t.Fatalf("result = %+v", res)
			}
			info, err := Scan(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Status != StatusComplete || info.Ops != 5 || info.Compression != comp {
				t.Fatalf("info = %+v", info)
			}
			if info.CommandsHash != res.CommandsHash || info.Trailer.CommandsHash != res.CommandsHash {
				t.Fatalf("hash differs: scan %s recorder %s", info.CommandsHash, res.CommandsHash)
			}
			if info.Commands["SET"] != 1 || info.Commands["EXEC"] != 1 {
				t.Fatalf("commands = %v", info.Commands)
			}
			var got []*OpLine
			if err := Stream(context.Background(), p, func(o *OpLine) error { got = append(got, o); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(got) != 5 || got[1].Outcome == nil || got[1].Outcome.Sum != "00ff" || got[2].Conn != ConnDedicated || got[3].Group != 2 {
				t.Fatalf("lines = %+v", got)
			}
			if got[0].Seq != 1 || got[4].Seq != 5 || got[3].WorkerSeq != 3 { // worker 0 sent ops 1, 3, 4, 5
				t.Fatalf("sequence numbers wrong: %+v %+v", got[3], got[4])
			}
		})
	}
}

func TestBinaryArgumentsSurvive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	bin := string([]byte{0xff, 0xfe, 0x00, 'a', 0x80})
	record(t, p, Options{}, hdr(1), []Entry{{Worker: 0, Group: 1, Cmd: []string{"SET", "k", bin}}})
	var args []string
	_ = Stream(context.Background(), p, func(o *OpLine) error {
		var err error
		args, err = o.Args()
		return err
	})
	if len(args) != 3 || args[2] != bin {
		t.Fatalf("args = %q", args)
	}
}

func TestEncodeCmdIsCanonical(t *testing.T) {
	a, _ := EncodeCmd([]string{"SET", "k", "v"})
	b, _ := EncodeCmd([]string{"SET", "k", "v"})
	c, _ := EncodeCmd([]string{"SET", "k", "w"})
	if string(a) != string(b) || string(a) == string(c) {
		t.Fatal("EncodeCmd must be deterministic and content-sensitive")
	}
	if Name([]string{"get", "k"}) != "GET" {
		t.Fatal("Name must upper-case")
	}
}

func TestRecordingCannotBeEditedUnnoticed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	record(t, p, Options{}, hdr(2), sample())
	b, _ := os.ReadFile(p)
	edited := strings.Replace(string(b), `"k1","v1"`, `"k1","XX"`, 1)
	if edited == string(b) {
		t.Fatal("test file did not contain the expected command")
	}
	_ = os.WriteFile(p, []byte(edited), 0o600)
	info, err := Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != StatusFailed || !strings.Contains(info.Reason, "changed") {
		t.Fatalf("an edited file must be reported: %+v", info)
	}
}

func TestMissingTrailerIsIncomplete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	record(t, p, Options{}, hdr(2), sample())
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	_ = os.WriteFile(p, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o600)
	info, err := Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != StatusIncomplete || info.Ops != 5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestRefusesToOverwriteOtherFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(p, []byte("important notes\n"), 0o600)
	if _, err := NewRecorder(p, Options{}, hdr(1)); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "important notes\n" {
		t.Fatal("the file was changed")
	}
	// An earlier recording, compressed or not, may be replaced.
	q := filepath.Join(t.TempDir(), "r.zst")
	record(t, q, Options{Compression: CompressionZstd}, hdr(1), sample())
	record(t, q, Options{Compression: CompressionZstd}, hdr(1), sample()[:1])
	if info, _ := Scan(q); info == nil || info.Ops != 1 {
		t.Fatalf("recording was not replaced: %+v", info)
	}
}

func TestRecordingFileIsPrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	record(t, p, Options{}, hdr(1), sample()[:1])
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestMaxBytesTruncatesAtACommandBoundary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	var es []Entry
	for i := 0; i < 1000; i++ {
		es = append(es, Entry{Worker: 0, Group: uint64(i + 1), Cmd: []string{"SET", "key", strings.Repeat("x", 100)}})
	}
	res := record(t, p, Options{MaxBytes: 20000}, hdr(1), es)
	if res.Status != StatusTruncated || res.Ops == 0 || res.Ops >= 1000 {
		t.Fatalf("result = %+v", res)
	}
	info, err := Scan(p)
	if err != nil || info.Status != StatusTruncated || info.Ops != res.Ops {
		t.Fatalf("scan = %+v, %v", info, err)
	}
	if res.Bytes > 20000 {
		t.Fatalf("wrote %d bytes, limit 20000", res.Bytes)
	}
}

func TestBackpressureFailMarksTheRecordingFailed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	r, err := NewRecorder(p, Options{QueueSize: 4, Backpressure: BackpressureFail}, hdr(1))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	r.beforeWrite = func() { <-release } // the writer stalls
	for i := 0; i < 50; i++ {
		r.Record(Entry{Worker: 0, Group: uint64(i + 1), Cmd: []string{"GET", "k"}})
	}
	if r.Active() {
		t.Fatal("a full queue must stop the recording")
	}
	close(release)
	res := r.Close()
	if res.Status != StatusFailed || !strings.Contains(res.Reason, "writer_queue_full") {
		t.Fatalf("result = %+v", res)
	}
	info, _ := Scan(p)
	if info == nil || info.Status != StatusFailed || info.Ops == 0 {
		t.Fatalf("a failed recording keeps a prefix: %+v", info)
	}
}

// The scale test: far more commands than the queue holds, a slow writer, and
// workers that must wait instead of dropping anything or growing memory.
func TestBackpressureBlockRecordsEverythingWithBoundedMemory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec.zst")
	const workers, perWorker = 8, 4000
	r, err := NewRecorder(p, Options{QueueSize: 64, Backpressure: BackpressureBlock, Compression: CompressionZstd}, hdr(workers))
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	var maxQueued int
	r.beforeWrite = func() { // runs on the one writer goroutine
		n++
		slow := n%1000 == 0
		if q := len(r.ch); q > maxQueued {
			maxQueued = q
		}
		if slow {
			time.Sleep(20 * time.Millisecond) // a stall the queue cannot absorb
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r.Record(Entry{Worker: w, Group: uint64(i + 1), Cmd: []string{"SET", "key:" + strings.Repeat("k", 20), strings.Repeat("v", 200)}})
			}
		}(w)
	}
	wg.Wait()
	res := r.Close()
	if res.Status != StatusComplete || res.Ops != workers*perWorker {
		t.Fatalf("every command must be recorded: %+v", res)
	}
	if res.Waits == 0 || res.WaitMax <= 0 {
		t.Fatalf("workers should have waited for the writer: %+v", res)
	}
	info, err := Scan(p)
	if err != nil || info.Status != StatusComplete || info.Ops != workers*perWorker {
		t.Fatalf("scan = %+v, %v", info, err)
	}
	for w := 0; w < workers; w++ {
		if info.Trailer.WorkerOps[string(rune('0'+w))] != perWorker {
			t.Fatalf("worker %d: %v", w, info.Trailer.WorkerOps)
		}
	}
	// 32,000 commands (about 8 MB) went through a queue of 64: nothing piled up.
	if maxQueued > 64 {
		t.Fatalf("%d commands were queued; the queue holds 64", maxQueued)
	}
	if res.FileBytes >= res.Bytes/2 {
		t.Fatalf("zstd should shrink repetitive commands: %d on disk, %d raw", res.FileBytes, res.Bytes)
	}
}

func TestOpenRejectsOtherFormats(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	_ = os.WriteFile(p, []byte("hello\n"), 0o600)
	if _, err := Open(p); err == nil {
		t.Fatal("a text file is not a recording")
	}
	_ = os.WriteFile(p, []byte(`{"type":"header","format":"plgm-workload-record/1"}`+"\n"), 0o600)
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Open(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file")
	}
}

func TestStreamStopsOnCancel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.rec")
	record(t, p, Options{}, hdr(2), sample())
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	err := Stream(ctx, p, func(*OpLine) error {
		n++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	var stopErr = errors.New("stop")
	if err := Stream(context.Background(), p, func(*OpLine) error { return stopErr }); !errors.Is(err, stopErr) {
		t.Fatalf("err = %v", err)
	}
	_ = io.EOF
}

func TestInvalidOptions(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRecorder(filepath.Join(dir, "a"), Options{Backpressure: "drop"}, hdr(1)); err == nil {
		t.Fatal("unknown backpressure")
	}
	if _, err := NewRecorder(filepath.Join(dir, "b"), Options{Compression: "lz4"}, hdr(1)); err == nil {
		t.Fatal("unknown compression")
	}
	if _, err := NewRecorder(filepath.Join(dir, "c"), Options{MaxBytes: 10}, hdr(1)); err == nil {
		t.Fatal("a limit smaller than the header")
	}
}
