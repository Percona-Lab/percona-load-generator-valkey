// Package trafficimport turns captured Valkey traffic into a recording that
// `plgv replay` can send: the output of MONITOR (from a file or captured live
// for a short, capped time) and SLOWLOG entries (a JSON export or read from a
// server).
//
// MONITOR slows the server it watches and prints every command with its
// arguments, so the capture is capped and the tool warns. Credentials never
// reach a recording: AUTH, HELLO, MIGRATE and ACL commands are always dropped.
package trafficimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Entry is one captured command.
type Entry struct {
	// TimeUS is the time in microseconds since the Unix epoch.
	TimeUS int64
	DB     int
	// Client identifies the connection (the address MONITOR printed, or the
	// SLOWLOG client address; empty when unknown).
	Client string
	Args   []string
	// DurationUS is how long the command took (SLOWLOG only).
	DurationUS int64
	// Truncated is set when the server shortened an argument (SLOWLOG).
	Truncated bool
	// Script is set for commands a Lua script or function issued (MONITOR).
	Script bool
	Source string // monitor or slowlog
}

// maxLine bounds one MONITOR line (a big value is printed in full).
const maxLine = 64 << 20

// ParseMonitor reads MONITOR output. Lines that are not a MONITOR line (the
// "OK" the command answers with, blank lines) are skipped and counted.
func ParseMonitor(r io.Reader) (entries []Entry, junk int, err error) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, rerr := readLine(br)
		if len(line) > 0 {
			if e, ok := parseMonitorLine(line); ok {
				entries = append(entries, e)
			} else {
				junk++
			}
		}
		if rerr == io.EOF {
			return entries, junk, nil
		}
		if rerr != nil {
			return entries, junk, rerr
		}
	}
}

func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, err := br.ReadSlice('\n')
		sb.Write(chunk)
		if sb.Len() > maxLine {
			return "", fmt.Errorf("a MONITOR line is longer than %d bytes", maxLine)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return strings.TrimRight(sb.String(), "\r\n"), err
	}
}

// parseMonitorLine reads `1339518083.107412 [0 127.0.0.1:60866] "keys" "*"`.
func parseMonitorLine(line string) (Entry, bool) {
	sp := strings.IndexByte(line, ' ')
	if sp < 1 || !strings.HasPrefix(line[sp+1:], "[") {
		return Entry{}, false
	}
	ts, ok := parseTime(line[:sp])
	if !ok {
		return Entry{}, false
	}
	rest := line[sp+1:]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return Entry{}, false
	}
	meta := strings.SplitN(rest[1:end], " ", 2)
	db, err := strconv.Atoi(meta[0])
	if err != nil {
		return Entry{}, false
	}
	e := Entry{TimeUS: ts, DB: db, Source: "monitor"}
	if len(meta) == 2 {
		e.Client = meta[1]
		e.Script = meta[1] == "lua" || meta[1] == "script" || strings.HasPrefix(meta[1], "lua")
	}
	args, ok := parseQuoted(strings.TrimSpace(rest[end+1:]))
	if !ok || len(args) == 0 {
		return Entry{}, false
	}
	e.Args = args
	return e, true
}

func parseTime(s string) (int64, bool) {
	sec, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	us := int64(0)
	if frac != "" {
		for len(frac) < 6 {
			frac += "0"
		}
		if us, err = strconv.ParseInt(frac[:6], 10, 64); err != nil {
			return 0, false
		}
	}
	return n*1_000_000 + us, true
}

// parseQuoted reads the double-quoted, backslash-escaped words MONITOR prints:
// \" \\ \n \r \t \a \b and \xHH (the server's own escaping).
func parseQuoted(s string) ([]string, bool) {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] != '"' {
			return nil, false
		}
		i++
		var b strings.Builder
		closed := false
		for i < len(s) {
			c := s[i]
			switch {
			case c == '"':
				closed = true
				i++
			case c == '\\' && i+1 < len(s):
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case 'a':
					b.WriteByte(7)
				case 'b':
					b.WriteByte(8)
				case 'x':
					if i+2 < len(s) {
						if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
							b.WriteByte(byte(v))
							i += 2
							break
						}
					}
					b.WriteByte('x')
				default:
					b.WriteByte(s[i])
				}
				i++
			default:
				b.WriteByte(c)
				i++
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, false
		}
		out = append(out, b.String())
	}
	return out, true
}

// ParseSlowlog reads a JSON export of SLOWLOG GET: an array of
// [id, unix-seconds, microseconds, [args...], client, name] entries, as
// `valkey-cli --json slowlog get N` prints them.
func ParseSlowlog(r io.Reader) ([]Entry, error) {
	var raw []any
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("the slowlog export is not a JSON array: %w", err)
	}
	return SlowlogEntries(raw)
}

// SlowlogEntries converts a SLOWLOG GET reply (as the client returns it or as
// JSON decodes it) to entries.
func SlowlogEntries(reply []any) ([]Entry, error) {
	var out []Entry
	for i, it := range reply {
		row, ok := it.([]any)
		if !ok || len(row) < 4 {
			return nil, fmt.Errorf("slowlog entry %d is not [id, time, duration, args...]", i)
		}
		ts, ok1 := asInt(row[1])
		dur, ok2 := asInt(row[2])
		args, ok3 := row[3].([]any)
		if !ok1 || !ok2 || !ok3 {
			return nil, fmt.Errorf("slowlog entry %d has the wrong field types", i)
		}
		e := Entry{TimeUS: ts * 1_000_000, DurationUS: dur, Source: "slowlog"}
		for _, a := range args {
			s := fmt.Sprint(a)
			e.Args = append(e.Args, s)
			// The server cuts long arguments and long commands.
			if strings.Contains(s, "... (") && (strings.HasSuffix(s, " more bytes)") || strings.HasSuffix(s, " more arguments)")) {
				e.Truncated = true
			}
		}
		if len(row) > 4 {
			e.Client, _ = row[4].(string)
		}
		if len(e.Args) == 0 {
			return nil, fmt.Errorf("slowlog entry %d has no command", i)
		}
		out = append(out, e)
	}
	return out, nil
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// Detect guesses a file's format from its first bytes: "slowlog" for a JSON
// array, "monitor" otherwise.
func Detect(head []byte) string {
	for _, b := range head {
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			continue
		}
		if b == '[' {
			return "slowlog"
		}
		break
	}
	return "monitor"
}
