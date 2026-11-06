package trafficimport

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// Write writes the converted commands as a recording. Imported recordings have
// no data fingerprint and no outcomes.
func Write(path string, res *Result, version, compression string) (workloadrec.Result, error) {
	if len(res.Commands) == 0 {
		return workloadrec.Result{}, fmt.Errorf("nothing to import: all %d entries were skipped (%s)", res.Info.Entries, summarizeSkips(res.Info.SkippedByReason))
	}
	h := workloadrec.Header{
		PLGVVersion: version, Workers: res.Workers, Import: &res.Info,
		Config: workloadrec.ConfigSummary{Concurrency: res.Workers, Mix: "imported traffic"},
	}
	rec, err := workloadrec.NewRecorder(path, workloadrec.Options{Backpressure: workloadrec.BackpressureBlock, Compression: compression}, h)
	if err != nil {
		return workloadrec.Result{}, err
	}
	for _, c := range res.Commands {
		rec.Record(workloadrec.Entry{Worker: c.Worker, Offset: c.Offset, Group: c.Group, Conn: c.Conn, Cmd: c.Cmd})
	}
	r := rec.Close()
	if r.Status != workloadrec.StatusComplete {
		return r, fmt.Errorf("the recording is %s: %s", r.Status, r.Reason)
	}
	return r, nil
}

func summarizeSkips(m map[string]uint64) string {
	var parts []string
	for k, n := range m {
		parts = append(parts, fmt.Sprintf("%d %s", n, k))
	}
	return strings.Join(parts, "; ")
}

// CaptureOptions cap a live MONITOR capture.
type CaptureOptions struct {
	Username, Password string
	TLS                *tls.Config   // nil = plain TCP
	MaxCommands        int           // stop after this many commands (default 100000)
	MaxDuration        time.Duration // stop after this long (default 10s)
}

// Capture runs MONITOR on the server for a short, capped time and writes what
// it prints to out, one line per command. MONITOR slows the server: the caller
// must have warned. It stops at the first cap or when ctx ends. It uses a plain
// TCP connection (no TLS).
func Capture(ctx context.Context, addr string, opt CaptureOptions, out io.Writer) (commands int, err error) {
	if opt.MaxCommands <= 0 {
		opt.MaxCommands = 100000
	}
	if opt.MaxDuration <= 0 {
		opt.MaxDuration = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var conn net.Conn
	if opt.TLS != nil {
		c := opt.TLS.Clone()
		if c.ServerName == "" {
			c.ServerName, _, _ = net.SplitHostPort(addr)
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, c)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return 0, fmt.Errorf("cannot connect to %s: %w", addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(opt.MaxDuration)
	go func() { // ctx ends the capture by closing the connection
		select {
		case <-ctx.Done():
			conn.Close()
		case <-time.After(opt.MaxDuration + time.Second):
		}
	}()
	br := bufio.NewReaderSize(conn, 1<<20)
	send := func(args ...string) error {
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		_, err := io.WriteString(conn, b.String())
		return err
	}
	expectOK := func(what string) error {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if !strings.HasPrefix(line, "+") {
			return fmt.Errorf("%s: %s", what, strings.TrimSpace(line))
		}
		return nil
	}
	if opt.Password != "" {
		args := []string{"AUTH", opt.Password}
		if opt.Username != "" {
			args = []string{"AUTH", opt.Username, opt.Password}
		}
		if err := send(args...); err != nil {
			return 0, err
		}
		if err := expectOK("AUTH"); err != nil {
			return 0, fmt.Errorf("the server refused the credentials: %w", err)
		}
	}
	if err := send("MONITOR"); err != nil {
		return 0, err
	}
	if err := expectOK("MONITOR"); err != nil {
		return 0, err
	}
	for commands < opt.MaxCommands && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				break
			}
			if ctx.Err() != nil {
				break
			}
			return commands, fmt.Errorf("MONITOR stream ended: %w", rerr)
		}
		// Each command arrives as a simple string: +<time> [db client] "cmd" ...
		if strings.HasPrefix(line, "+") {
			if _, werr := io.WriteString(out, line[1:]); werr != nil {
				return commands, werr
			}
			commands++
		}
	}
	return commands, nil
}

// ReadFile reads a captured file and parses it in the format Detect finds (or the one given).
func ReadFile(path, format string) ([]Entry, string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", 0, err
	}
	defer f.Close()
	if format == "" || format == "auto" {
		br := bufio.NewReader(f)
		head, _ := br.Peek(512)
		format = Detect(head)
		return parseAs(br, format)
	}
	return parseAs(f, format)
}

func parseAs(r io.Reader, format string) ([]Entry, string, int, error) {
	switch format {
	case "monitor":
		e, junk, err := ParseMonitor(r)
		return e, format, junk, err
	case "slowlog":
		e, err := ParseSlowlog(r)
		return e, format, 0, err
	}
	return nil, format, 0, fmt.Errorf("unknown format %q (want monitor or slowlog)", format)
}

// Parse reads captured traffic from r in the given format ("auto" detects it
// from the first bytes). It returns the entries, the format used and the
// number of lines that were not commands.
func Parse(r io.Reader, format string) ([]Entry, string, int, error) {
	if format == "" || format == "auto" {
		br := bufio.NewReader(r)
		head, _ := br.Peek(512)
		return parseAs(br, Detect(head))
	}
	return parseAs(r, format)
}
