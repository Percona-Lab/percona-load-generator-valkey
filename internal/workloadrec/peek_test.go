package workloadrec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeekReadsHeaderAndTrailerWithoutTheCommands(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.rec")
	record(t, plain, Options{}, hdr(2), sample())
	p, err := Peek(plain)
	if err != nil {
		t.Fatal(err)
	}
	if p.Header.Workers != 2 || p.Compression != CompressionNone || p.Trailer == nil || p.Trailer.Ops != 5 || p.Trailer.Status != StatusComplete {
		t.Fatalf("peek = %+v trailer=%+v", p, p.Trailer)
	}
	for _, comp := range []string{CompressionZstd, CompressionGzip} {
		cp := filepath.Join(dir, "b."+comp)
		record(t, cp, Options{Compression: comp}, hdr(3), sample())
		p, err := Peek(cp)
		if err != nil || p.Header.Workers != 3 || p.Compression != comp || p.Trailer != nil {
			t.Fatalf("%s peek = %+v, %v", comp, p, err)
		}
	}
}

func TestPeekRefusesFilesThatAreNotRecordings(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "text": "hello\n", "json": `{"a":1}` + "\n", "other": `{"type":"header","format":"x/9"}` + "\n"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Peek(p)
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if name != "other" && !errors.Is(err, ErrNotRecording) {
			t.Fatalf("%s: err = %v, want ErrNotRecording", name, err)
		}
	}
	if _, err := Peek(dir); err == nil {
		t.Fatal("a folder is not a recording")
	}
	link := filepath.Join(dir, "link")
	rec := filepath.Join(dir, "r.rec")
	record(t, rec, Options{}, hdr(1), sample())
	if err := os.Symlink(rec, link); err != nil {
		t.Skip("no symlinks")
	}
	if _, err := Peek(link); err == nil {
		t.Fatal("a symbolic link must not be followed")
	}
}

func TestPeekOfAnUnfinishedFileHasNoTrailer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.rec")
	record(t, p, Options{}, hdr(1), sample())
	b, _ := os.ReadFile(p)
	cut := strings.LastIndex(strings.TrimRight(string(b), "\n"), "\n")
	if err := os.WriteFile(p, b[:cut+1], 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Peek(p)
	if err != nil || got.Trailer != nil {
		t.Fatalf("peek = %+v, %v", got, err)
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := CheckWritable(filepath.Join(dir, "new.rec")); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(filepath.Join(dir, "no", "such", "x.rec")); err == nil {
		t.Fatal("a missing folder must be refused")
	}
	other := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(other, []byte("keep me"), 0o600)
	if err := CheckWritable(other); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("err = %v", err)
	}
	rec := filepath.Join(dir, "old.rec")
	record(t, rec, Options{}, hdr(1), sample())
	if err := CheckWritable(rec); err != nil {
		t.Fatalf("an earlier recording may be replaced: %v", err)
	}
	if err := CheckWritable(dir); err == nil {
		t.Fatal("a folder is not a recording path")
	}
}
