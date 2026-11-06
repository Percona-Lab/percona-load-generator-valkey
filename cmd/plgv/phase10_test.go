package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replay"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/replaycompare"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

func TestImportTrafficFromAFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "monitor.txt")
	_ = os.WriteFile(in, []byte("OK\n1700000000.000100 [0 10.0.0.1:1] \"SET\" \"k\" \"v\"\n1700000000.000200 [0 10.0.0.1:1] \"AUTH\" \"hunter2\"\n1700000000.000300 [0 10.0.0.2:2] \"GET\" \"k\"\n"), 0o600)
	out := filepath.Join(dir, "imp.jsonl.zst")
	var so, se bytes.Buffer
	if code := importCmd([]string{"-out", out, in}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	for _, want := range []string{"imported:   2 of 3 entries, 2 connections -> 2 workers", "carries credentials", "complete, 2 commands"} {
		if !strings.Contains(so.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, so.String())
		}
	}
	so.Reset()
	if code := replayCmd([]string{"-info", out}, &so, &se); code != 0 || !strings.Contains(so.String(), "imported:    from monitor") || strings.Contains(so.String(), "hunter2") {
		t.Fatalf("exit %d: %s", code, so.String())
	}
	if b, _ := os.ReadFile(out); bytes.Contains(b, []byte("hunter2")) {
		t.Fatal("credentials in the recording")
	}
	if info, err := workloadrec.Scan(out); err != nil || info.Ops != 2 {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestImportTrafficRejectsBadUse(t *testing.T) {
	var so, se bytes.Buffer
	for name, args := range map[string][]string{
		"no out":       {"x"},
		"no input":     {"-out", filepath.Join(t.TempDir(), "o")},
		"bad compress": {"-out", "o", "-compress", "lz4", "x"},
		"missing":      {"-out", filepath.Join(t.TempDir(), "o"), filepath.Join(t.TempDir(), "nope")},
	} {
		se.Reset()
		if code := importCmd(args, &so, &se); code == 0 {
			t.Errorf("%s: exit 0", name)
		}
	}
	// Everything skipped is an error, not an empty recording.
	in := filepath.Join(t.TempDir(), "m.txt")
	_ = os.WriteFile(in, []byte("1.1 [0 a:1] \"INFO\"\n"), 0o600)
	out := filepath.Join(t.TempDir(), "o")
	if code := importCmd([]string{"-out", out, in}, &so, &se); code != 1 || !strings.Contains(se.String(), "nothing to import") {
		t.Fatalf("exit %d %s", code, se.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("no file must be written")
	}
}

func writeSummary(t *testing.T, dir, name string, s *replay.Summary) string {
	p := filepath.Join(dir, name)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(p, b, 0o600)
	return p
}

func summ(label string, ops, p99 float64) *replay.Summary {
	return &replay.Summary{Format: replay.SummaryFormat, Label: label, Recording: replay.RecordingRef{Name: "r", CommandsHash: "h"}, Ops: 10, OpsPerSec: ops,
		Latency: replay.Latency{P50: 0.5, P95: 1, P99: p99, Max: 3}}
}

func TestCompareCommand(t *testing.T) {
	dir := t.TempDir()
	a := writeSummary(t, dir, "a.json", summ("old", 50000, 2))
	b := writeSummary(t, dir, "b.json", summ("new", 20000, 9))
	var so, se bytes.Buffer
	if code := compareCmd([]string{a, b}, &so, &se); code != 0 {
		t.Fatalf("exit %d %s", code, se.String())
	}
	for _, want := range []string{"new against baseline old: SLOWER", "WORSE", "Throughput (ops/s)"} {
		if !strings.Contains(so.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, so.String())
		}
	}
	if code := compareCmd([]string{"-fail-on-regression", a, b}, &so, &se); code != 1 {
		t.Fatalf("a regression must fail: exit %d", code)
	}
	so.Reset()
	if code := compareCmd([]string{"-json", a, a}, &so, &se); code != 0 {
		t.Fatal("same file twice")
	}
	var comps []replaycompare.Comparison
	if err := json.Unmarshal(so.Bytes(), &comps); err != nil || len(comps) != 1 || comps[0].Other != "old (2)" || comps[0].Verdict != "equivalent" {
		t.Fatalf("%v %+v", err, comps)
	}
	// A multi-target file works on its own.
	multi := filepath.Join(dir, "m.json")
	mb, _ := json.Marshal(replaycompare.File{Format: replaycompare.MultiFormat, Targets: []*replay.Summary{summ("x", 100, 1), summ("y", 100, 1)}})
	_ = os.WriteFile(multi, mb, 0o600)
	so.Reset()
	if code := compareCmd([]string{multi}, &so, &se); code != 0 || !strings.Contains(so.String(), "EQUIVALENT") {
		t.Fatalf("exit %d %s", code, so.String())
	}
	for name, args := range map[string][]string{"none": {}, "one": {a}, "missing": {a, filepath.Join(dir, "x")}} {
		if code := compareCmd(args, &so, &se); code != 2 {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"format":"nope"}`), 0o600)
	if code := compareCmd([]string{a, bad}, &so, &se); code != 2 {
		t.Fatal("not a summary")
	}
}

func TestReplayTargetsFlagsAreValidated(t *testing.T) {
	rec := makeRecording(t)
	var so, se bytes.Buffer
	for name, args := range map[string][]string{
		"addr and targets": {"-addr", "a:1", "-targets", "x=a:1,y=b:1", rec},
		"one target":       {"-targets", "a:1", rec},
		"duplicate names":  {"-targets", "x=a:1,x=b:1", rec},
		"concurrent alone": {"-concurrent", rec},
	} {
		se.Reset()
		if code := replayCmd(args, &so, &se); code != 2 {
			t.Errorf("%s: exit %d: %s", name, code, se.String())
		}
	}
}

func TestRunDeterministicFlagsAreValidated(t *testing.T) {
	var so, se bytes.Buffer
	if code := runCmd([]string{"-deterministic", "-preload=false"}, &so, &se); code != 2 || !strings.Contains(se.String(), "run.op_budget") {
		t.Fatalf("exit %d %s", code, se.String())
	}
	se.Reset()
	if code := runCmd([]string{"-deterministic", "-ops", "10", "-seed", "0", "-preload=false"}, &so, &se); code != 2 || !strings.Contains(se.String(), "run.seed") {
		t.Fatalf("exit %d %s", code, se.String())
	}
}
