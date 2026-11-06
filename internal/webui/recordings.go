package webui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/trafficimport"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

const (
	// Bounds on listing a folder: entries read, headers peeked, rows returned.
	maxRecordingDirEntries = 2000
	maxRecordingPeeks      = 300
	maxListedRecordings    = 100
	// maxRememberedRecordings bounds the recordings remembered from runs (they may live outside the folder).
	maxRememberedRecordings = 20
	// maxUploadBytes bounds an uploaded recording; maxImportBytes a pasted or uploaded capture.
	maxUploadBytes = 2 << 30
	maxImportBytes = 128 << 20
	// maxInfoBytes bounds a file the info call reads whole.
	maxInfoBytes = 2 << 30
)

// Recording sources.
const (
	srcFolder = "folder" // in <data-dir>/recordings
	srcRun    = "run"    // written by a run of this server, at a path of its own
)

// RecordingEntry is one recording the server can replay. It holds counts and
// names, never a command.
type RecordingEntry struct {
	Path          string  `json:"path"`
	Name          string  `json:"name"`
	Source        string  `json:"source"`
	SizeBytes     int64   `json:"size_bytes"`
	ModifiedAt    string  `json:"modified_at"`
	CreatedAt     string  `json:"created_at,omitempty"`
	PLGVVersion   string  `json:"plgv_version,omitempty"`
	ServerVersion string  `json:"server_version,omitempty"`
	Mode          string  `json:"mode,omitempty"`
	Workers       int     `json:"workers,omitempty"`
	Compression   string  `json:"compression,omitempty"`
	Imported      bool    `json:"imported,omitempty"`
	Outcomes      bool    `json:"outcomes,omitempty"`
	Fingerprint   bool    `json:"data_fingerprint,omitempty"`
	Ops           *uint64 `json:"ops,omitempty"`    // from the trailer of an uncompressed file
	Status        string  `json:"status,omitempty"` // complete, truncated, failed
	// Problem is set for a file that looks like a recording but cannot be replayed.
	Problem string `json:"problem,omitempty"`

	mod time.Time
}

func (s *Server) recordingsDir() string { return filepath.Join(s.opt.DataDir, "recordings") }

var recExts = []string{".plgvrec", ".plgvrec.zst", ".plgvrec.gz", ".rec", ".ndjson", ".jsonl", ".zst", ".gz"}

func isRecordingName(n string) bool {
	for _, e := range recExts {
		if strings.HasSuffix(n, e) {
			return true
		}
	}
	return false
}

// newRecordingPath returns a fresh file name in the recordings folder, so a new
// run never replaces an earlier recording.
func (s *Server) newRecordingPath(compression string) string {
	ext := ".plgvrec"
	switch compression {
	case "zstd":
		ext += ".zst"
	case "gzip":
		ext += ".gz"
	}
	var b [3]byte
	_, _ = rand.Read(b[:])
	return filepath.Join(s.recordingsDir(), fmt.Sprintf("rec-%s-%s%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]), ext))
}

// describeRecording peeks at a file. ok is false when it is not a recording.
func describeRecording(path, source string, fi os.FileInfo) (RecordingEntry, bool) {
	e := RecordingEntry{Path: path, Name: filepath.Base(path), Source: source, SizeBytes: fi.Size(),
		ModifiedAt: fi.ModTime().UTC().Format(time.RFC3339), mod: fi.ModTime()}
	p, err := workloadrec.Peek(path)
	if err != nil {
		if errors.Is(err, workloadrec.ErrNotRecording) {
			return e, false
		}
		e.Problem = err.Error()
		if errors.Is(err, os.ErrPermission) {
			e.Problem = "plgv web cannot read this file (permission denied)"
		}
		return e, true
	}
	h := p.Header
	e.CreatedAt, e.PLGVVersion, e.ServerVersion, e.Mode, e.Workers = h.CreatedAt, h.PLGVVersion, h.ServerVersion, h.Mode, h.Workers
	e.Compression, e.Imported, e.Outcomes, e.Fingerprint = p.Compression, h.Import != nil, h.Outcomes, h.DataFingerprint != nil
	if t := p.Trailer; t != nil {
		ops := t.Ops
		e.Ops, e.Status = &ops, t.Status
	}
	return e, true
}

// listRecordings returns the recordings in the folder and those remembered
// from runs, newest first.
func (s *Server) listRecordings() (out []RecordingEntry, truncated bool) {
	seen := map[string]bool{}
	peeks := 0
	dir := s.recordingsDir()
	if des, err := os.ReadDir(dir); err == nil {
		if len(des) > maxRecordingDirEntries {
			des, truncated = des[:maxRecordingDirEntries], true
		}
		for _, de := range des {
			if !de.Type().IsRegular() || !isRecordingName(de.Name()) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue
			}
			if peeks++; peeks > maxRecordingPeeks {
				truncated = true
				break
			}
			p := filepath.Join(dir, de.Name())
			if e, ok := describeRecording(p, srcFolder, fi); ok {
				out = append(out, e)
				seen[p] = true
			}
		}
	}
	for _, p := range s.rememberedRecordings() {
		if seen[p] {
			continue
		}
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			if e, ok := describeRecording(p, srcRun, fi); ok {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if len(out) > maxListedRecordings {
		out, truncated = out[:maxListedRecordings], true
	}
	return out, truncated
}

func (s *Server) rememberedRecordings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.remembered...)
}

// rememberRecording keeps the path a run wrote, so it is listed even when it
// is outside the recordings folder.
func (s *Server) rememberRecording(path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.remembered {
		if p == path {
			s.remembered = append(s.remembered[:i], s.remembered[i+1:]...)
			break
		}
	}
	s.remembered = append(s.remembered, path)
	if n := len(s.remembered); n > maxRememberedRecordings {
		s.remembered = append([]string(nil), s.remembered[n-maxRememberedRecordings:]...)
	}
}

// errFreePaths is returned when a typed path is not allowed.
func (s *Server) errFreePaths(what string) error {
	return fmt.Errorf("typing a %s path is off because plgv web accepts remote connections; use the list or upload a file (the folder is %s)", what, s.recordingsDir())
}

// resolveRecording turns what the user gave (a name from the list, or a path
// they typed) into a readable recording's path. A typed path is allowed only
// when the server listens on loopback (it has no login).
func (s *Server) resolveRecording(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errors.New("choose a recording, or type the path of one on the server running plgv web")
	}
	path := in
	if !strings.ContainsAny(in, `/\`) {
		path = filepath.Join(s.recordingsDir(), in) // a bare name means the recordings folder
	} else if !s.opt.FreePaths {
		// A path from the list is inside the folder: accept exactly that.
		if abs, err := filepath.Abs(in); err != nil || filepath.Dir(abs) != s.absRecordingsDir() {
			if !s.isRemembered(in) {
				return "", s.errFreePaths("recording")
			}
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s does not exist on the server running plgv web", path)
		}
		return "", fmt.Errorf("cannot read %s: %v", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file (a folder or a link cannot be replayed)", path)
	}
	if _, err := workloadrec.Peek(path); err != nil {
		if errors.Is(err, workloadrec.ErrNotRecording) {
			return "", fmt.Errorf("%s is not a plgv recording", path)
		}
		return "", fmt.Errorf("%s cannot be replayed: %v", path, err)
	}
	return path, nil
}

func (s *Server) absRecordingsDir() string {
	if abs, err := filepath.Abs(s.recordingsDir()); err == nil {
		return abs
	}
	return s.recordingsDir()
}

func (s *Server) isRemembered(p string) bool {
	abs, _ := filepath.Abs(p)
	for _, r := range s.rememberedRecordings() {
		if ra, _ := filepath.Abs(r); ra == abs {
			return true
		}
	}
	return false
}

// checkRecordPath validates a recording path a request gave for writing.
func (s *Server) checkRecordPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if filepath.Dir(abs) != s.absRecordingsDir() && !s.opt.FreePaths {
		return "", s.errFreePaths("recording")
	}
	if err := workloadrec.CheckWritable(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// --- handlers ---

func (s *Server) handleRecordings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET only")
		return
	}
	list, truncated := s.listRecordings()
	if list == nil {
		list = []RecordingEntry{}
	}
	writeJSON(w, 200, map[string]any{"dir": s.absRecordingsDir(), "free_paths": s.opt.FreePaths, "recordings": list, "truncated": truncated})
}

// handleRecordingInfo describes one recording completely (it reads the file).
func (s *Server) handleRecordingInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "GET only")
		return
	}
	path, err := s.resolveRecording(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if fi.Size() > maxInfoBytes {
		fail(w, 400, "this recording is too large to scan here; use plgv replay -info on the command line")
		return
	}
	info, err := workloadrec.Scan(path)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	type cmdCount struct {
		Command string `json:"command"`
		Ops     uint64 `json:"ops"`
	}
	var cmds []cmdCount
	for k, v := range info.Commands {
		cmds = append(cmds, cmdCount{k, v})
	}
	sort.Slice(cmds, func(i, j int) bool {
		if cmds[i].Ops != cmds[j].Ops {
			return cmds[i].Ops > cmds[j].Ops
		}
		return cmds[i].Command < cmds[j].Command
	})
	if len(cmds) > 40 {
		cmds = cmds[:40]
	}
	h := info.Header
	entry, _ := describeRecording(path, srcFolder, fi)
	writeJSON(w, 200, map[string]any{
		"entry": entry, "status": info.Status, "reason": info.Reason, "ops": info.Ops, "groups": info.Groups,
		"commands_hash": info.CommandsHash, "commands": cmds, "file_bytes": info.FileBytes, "compression": info.Compression,
		"workers": h.Workers, "seed": h.Seed, "outcomes": h.Outcomes, "checksums": h.ResultChecksums,
		"mix": h.Config.Mix, "duration": h.Config.Duration, "data_fingerprint": h.DataFingerprint != nil, "import": h.Import,
	})
}

// handleRecordingDelete removes a recording from the recordings folder. Files
// elsewhere are never deleted by the UI.
func (s *Server) handleRecordingDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		fail(w, 405, "DELETE only")
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || !isRecordingName(name) {
		fail(w, 400, "give the name of a recording in the recordings folder")
		return
	}
	p := filepath.Join(s.recordingsDir(), name)
	if _, err := workloadrec.Peek(p); err != nil {
		fail(w, 404, "not a recording in the recordings folder")
		return
	}
	if err := os.Remove(p); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// handleRecordingUpload stores a recording the browser sends as the request body.
// The name only suggests part of the stored name; the folder is fixed.
func (s *Server) handleRecordingUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		fail(w, 405, "PUT the file")
		return
	}
	base := strings.Trim(safeName.ReplaceAllString(filepath.Base(r.URL.Query().Get("name")), "_"), "._-")
	if len(base) > 40 {
		base = base[:40]
	}
	if base == "" {
		base = "upload"
	}
	if err := os.MkdirAll(s.recordingsDir(), 0o700); err != nil {
		fail(w, 500, err.Error())
		return
	}
	tmp, err := os.CreateTemp(s.recordingsDir(), ".upload-*")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, maxUploadBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fail(w, 400, fmt.Sprintf("the upload failed or is larger than %d GB: %v", maxUploadBytes>>30, err))
		return
	}
	pk, err := workloadrec.Peek(tmp.Name())
	if err != nil {
		fail(w, 400, "that file is not a plgv recording: "+err.Error())
		return
	}
	ext := ".plgvrec"
	switch pk.Compression {
	case workloadrec.CompressionZstd:
		ext += ".zst"
	case workloadrec.CompressionGzip:
		ext += ".gz"
	}
	var b [3]byte
	_, _ = rand.Read(b[:])
	base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".zst"), ".gz"), ".plgvrec")
	dst := filepath.Join(s.recordingsDir(), fmt.Sprintf("upload-%s-%s%s", base, hex.EncodeToString(b[:]), ext))
	if err := os.Rename(tmp.Name(), dst); err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = os.Chmod(dst, 0o600)
	fi, _ := os.Stat(dst)
	e, _ := describeRecording(dst, srcFolder, fi)
	writeJSON(w, 200, map[string]any{"ok": true, "bytes": n, "recording": e})
}

// handleImportTraffic converts captured traffic (MONITOR output or a SLOWLOG
// export) into a recording in the recordings folder. Credentials are dropped.
//
// Body: {"text": "...", "path": "...", "format": "auto|monitor|slowlog", "compression": "none|zstd|gzip",
// "max_workers": 64, "db": 0, "allow_dangerous": false}. Give text (pasted or read from a file by the
// browser) or path (a file on the server, loopback only).
func (s *Server) handleImportTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	var body struct {
		Text           string `json:"text"`
		Path           string `json:"path"`
		Format         string `json:"format"`
		Compression    string `json:"compression"`
		MaxWorkers     int    `json:"max_workers"`
		DB             int    `json:"db"`
		AllowDangerous bool   `json:"allow_dangerous"`
		Name           string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBytes)).Decode(&body); err != nil {
		fail(w, 400, fmt.Sprintf("the request is not valid JSON, or is larger than %d MB: %v", maxImportBytes>>20, err))
		return
	}
	switch body.Format {
	case "", "auto", "monitor", "slowlog":
	default:
		fail(w, 400, "format must be auto, monitor or slowlog")
		return
	}
	switch body.Compression {
	case "", "none", "zstd", "gzip":
	default:
		fail(w, 400, "compression must be none, zstd or gzip")
		return
	}
	if body.MaxWorkers < 0 || body.MaxWorkers > 1024 || body.DB < 0 || body.DB > 1<<16 {
		fail(w, 400, "max_workers must be 0-1024 and db a database number")
		return
	}
	var (
		entries []trafficimport.Entry
		got     string
		junk    int
		names   []string
		err     error
	)
	switch {
	case body.Text != "" && body.Path != "":
		fail(w, 400, "give the captured traffic as text or as a path, not both")
		return
	case body.Text != "":
		entries, got, junk, err = trafficimport.Parse(strings.NewReader(body.Text), body.Format)
		names = []string{"pasted or uploaded capture"}
	case body.Path != "":
		if !s.opt.FreePaths {
			fail(w, 400, s.errFreePaths("capture").Error())
			return
		}
		fi, serr := os.Stat(body.Path)
		if serr != nil || !fi.Mode().IsRegular() {
			fail(w, 400, body.Path+" is not a file on the server running plgv web")
			return
		}
		if fi.Size() > maxImportBytes {
			fail(w, 400, fmt.Sprintf("%s is larger than %d MB; use plgv import-traffic on the command line", body.Path, maxImportBytes>>20))
			return
		}
		entries, got, junk, err = trafficimport.ReadFile(body.Path, body.Format)
		names = []string{filepath.Base(body.Path)}
	default:
		fail(w, 400, "paste the captured traffic, upload a file, or give the path of one on the server")
		return
	}
	if err != nil {
		fail(w, 400, "cannot read the capture: "+err.Error())
		return
	}
	res := trafficimport.Convert(entries, []string{got}, names, trafficimport.Options{MaxWorkers: body.MaxWorkers, DB: body.DB, AllowDangerous: body.AllowDangerous})
	comp := body.Compression
	if comp == "" {
		comp = "zstd"
	}
	if err := os.MkdirAll(s.recordingsDir(), 0o700); err != nil {
		fail(w, 500, err.Error())
		return
	}
	out := s.newRecordingPath(map[string]string{"zstd": "zstd", "gzip": "gzip"}[comp])
	out = strings.Replace(out, "rec-", "import-", 1)
	rr, err := trafficimport.Write(out, res, s.opt.Version, comp)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	fi, _ := os.Stat(out)
	e, _ := describeRecording(out, srcFolder, fi)
	writeJSON(w, 200, map[string]any{
		"recording": e, "format": got, "lines_not_commands": junk, "commands": rr.Ops, "workers": res.Workers,
		"entries": res.Info.Entries, "skipped": res.Info.SkippedByReason,
	})
}
