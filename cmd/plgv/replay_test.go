package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/datafp"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/latency"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func makeRecording(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.jsonl.zst")
	r, err := workloadrec.NewRecorder(p, workloadrec.Options{Compression: workloadrec.CompressionZstd}, workloadrec.Header{
		PLGVVersion: "test", Seed: 4, Workers: 2, ServerVersion: "9.1.3", Mode: "standalone",
		Config:          workloadrec.ConfigSummary{Duration: "10s", Mix: "GET 80, SET 20"},
		DataFingerprint: &datafp.Fingerprint{DBSize: 100, Stride: 1, Sampled: 100, Digest: "ab"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Record(workloadrec.Entry{Worker: 0, Group: 1, Cmd: []string{"SET", "k", "secret-value"}})
	r.Record(workloadrec.Entry{Worker: 1, Group: 1, Cmd: []string{"GET", "k"}})
	if res := r.Close(); res.Status != workloadrec.StatusComplete {
		t.Fatalf("%+v", res)
	}
	return p
}

func TestReplayInfoDescribesTheRecordingWithoutValues(t *testing.T) {
	p := makeRecording(t)
	var out, errb bytes.Buffer
	if code := replayCmd([]string{"-info", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"status:      complete", "9.1.3 (standalone)", "2 workers", "commands:    2 in 2 groups", "100 keys", "hash:", "SET", "GET", "zstd"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "secret-value") {
		t.Fatal("-info must not print command arguments")
	}
}

func TestReplayRejectsBadUse(t *testing.T) {
	var out, errb bytes.Buffer
	if code := replayCmd(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "recording-file") {
		t.Fatalf("no file: exit %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := replayCmd([]string{"-speed", "-1", makeRecording(t)}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "-speed") {
		t.Fatalf("negative speed: exit %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := replayCmd([]string{"-info", filepath.Join(t.TempDir(), "missing")}, &out, &errb); code != 1 {
		t.Fatalf("missing file: exit %d", code)
	}
	errb.Reset()
	if code := replayCmd([]string{"-client", "nope", makeRecording(t)}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "target.client") {
		t.Fatalf("bad client: exit %d, %q", code, errb.String())
	}
}

func TestRunRecordFlagsAreValidated(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCmd([]string{"-record-outcomes", "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "record.path") {
		t.Fatalf("outcomes without a file: exit %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := runCmd([]string{"-record", filepath.Join(t.TempDir(), "r"), "-record-backpressure", "drop", "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "record.backpressure") {
		t.Fatalf("exit %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := runCmd([]string{"-record", filepath.Join(t.TempDir(), "r"), "-sweep-workers", "1,2", "-preload=false"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "overwrite") {
		t.Fatalf("sweep with a recording: exit %d, %q", code, errb.String())
	}
}

func TestPrintRecordingAndReplay(t *testing.T) {
	var w bytes.Buffer
	printRecording(&w, nil)
	if w.Len() != 0 {
		t.Fatal("nothing to print without a recording")
	}
	printRecording(&w, &workloadrec.Result{Path: "r.zst", Status: workloadrec.StatusTruncated, Reason: "max_bytes_reached", Ops: 9, Bytes: 2 << 20, FileBytes: 1 << 20, Compression: "zstd",
		Waits: 3, WaitTotal: time.Second, WaitMax: 500 * time.Millisecond})
	for _, want := range []string{"r.zst: truncated, 9 commands", "reason: max_bytes_reached", "waited for the writer 3 times"} {
		if !strings.Contains(w.String(), want) {
			t.Fatalf("missing %q in %q", want, w.String())
		}
	}

	h := &latency.Histogram{}
	h.Record(time.Millisecond)
	w.Reset()
	printReplay(&w, &replay.Result{Path: "r", Status: "complete", Hist: h, Ops: 10, Errors: 1, Misses: 2, Skipped: 5, Speed: 2, ReadsOnly: true,
		Elapsed: time.Second, MaxLag: time.Second, Iterations: []replay.Iteration{{Index: 1, Ops: 5, Elapsed: time.Second}, {Index: 2, Ops: 5, Elapsed: time.Second}},
		Outcomes: &replay.Outcomes{Compared: 10, StatusDiffs: 2, ChecksumCompared: 8, ChecksumDiffs: 1, Examples: []replay.Diff{{Seq: 4, Command: "GET", Kind: "status", Recorded: "ok", Replayed: "miss"}}},
		PerOp:    []replay.OpStat{{Command: "GET", Ops: 10}}})
	for _, want := range []string{"recorded pace x2, reads only, 2 iteration(s)", "skipped:    5", "fell behind the recording", "not compared with the recording's hash",
		"2 with a different status; 1 of 8 reply checksums differ", "command #4 GET: status recorded ok, replayed miss", "iterations:"} {
		if !strings.Contains(w.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, w.String())
		}
	}
}
