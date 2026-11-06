package workloadrec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNotRecording says a file is not a plgv recording at all (as opposed to a
// recording that is damaged or from another version).
var ErrNotRecording = errors.New("not a plgv recording")

// Peeked is what a quick look at a recording finds: its header, its trailer
// when that can be read cheaply, and its size. Nothing in it holds a command.
type Peeked struct {
	Header      Header
	Trailer     *Trailer // only for an uncompressed file; nil otherwise
	Compression string
	FileBytes   int64
}

// peekTail is how much of an uncompressed file's end is searched for the trailer.
const peekTail = 256 << 10

// Peek reads the header of the recording at path, and the trailer when the file
// is not compressed (a compressed file would have to be read whole). It reads
// at most a few hundred KB, so a folder of recordings can be listed quickly.
// A file that is not a recording returns ErrNotRecording.
func Peek(path string) (*Peeked, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: %w", path, ErrNotRecording)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, comp, release, err := decompress(bufio.NewReaderSize(f, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRecording, err)
	}
	defer release()
	br := bufio.NewReaderSize(content, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return nil, ErrNotRecording
	}
	if !bytes.HasPrefix(line, headerPrefix) {
		return nil, ErrNotRecording
	}
	var h Header
	if err := json.Unmarshal(line, &h); err != nil || h.Type != LineHeader {
		return nil, ErrNotRecording
	}
	if h.Format != FormatVersion {
		return nil, fmt.Errorf("this recording has an unsupported format version (this version reads %q)", FormatVersion)
	}
	p := &Peeked{Header: h, Compression: comp, FileBytes: fi.Size()}
	if comp == CompressionNone {
		p.Trailer = peekTrailer(f, fi.Size())
	}
	return p, nil
}

// peekTrailer finds the trailer line in the last peekTail bytes, or nil.
func peekTrailer(f *os.File, size int64) *Trailer {
	off := size - peekTail
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	buf = bytes.TrimRight(buf, "\n")
	i := bytes.LastIndexByte(buf, '\n')
	last := buf[i+1:]
	if !bytes.Contains(last, []byte(`"type":"`+LineTrailer+`"`)) {
		return nil
	}
	var t Trailer
	if json.Unmarshal(last, &t) != nil || t.Type != LineTrailer {
		return nil
	}
	return &t
}

// CheckWritable says whether a recording could be written at path: the folder
// exists, the path is not a folder or a link, and any file already there is an
// earlier recording (the same rule the recorder applies when it opens it).
func CheckWritable(path string) error {
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("the folder %s does not exist or cannot be read", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	return checkOverwrite(path)
}
