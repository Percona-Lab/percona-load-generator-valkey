package trafficimport

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

const monitorSample = `OK
1700000000.000100 [0 10.0.0.1:5001] "SET" "user:1" "hello world"
1700000000.000200 [0 10.0.0.2:5002] "GET" "user:1"
1700000000.000300 [0 10.0.0.1:5001] "AUTH" "hunter2"
1700000000.000400 [0 10.0.0.1:5001] "MULTI"
1700000000.000450 [0 10.0.0.2:5002] "INCR" "n"
1700000000.000500 [0 10.0.0.1:5001] "INCR" "counter"
1700000000.000600 [0 10.0.0.1:5001] "EXEC"
1700000000.000700 [3 10.0.0.1:5001] "SET" "other" "db"
1700000000.000800 [0 lua] "SET" "from-script" "1"
1700000000.000900 [0 10.0.0.2:5002] "FLUSHALL"
1700000000.001000 [0 10.0.0.2:5002] "INFO"
1700000000.001100 [0 10.0.0.2:5002] "SET" "q" "a \"quoted\" \\ \x00\xff\n"
this is not a monitor line
`

func parse(t *testing.T, text string) []Entry {
	t.Helper()
	e, _, err := ParseMonitor(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestParseMonitorLine(t *testing.T) {
	es, junk, err := ParseMonitor(strings.NewReader(monitorSample))
	if err != nil || junk != 2 || len(es) != 12 {
		t.Fatalf("entries %d, junk %d, err %v", len(es), junk, err)
	}
	e := es[0]
	if e.TimeUS != 1700000000000100 || e.DB != 0 || e.Client != "10.0.0.1:5001" || strings.Join(e.Args, "|") != "SET|user:1|hello world" {
		t.Fatalf("%+v", e)
	}
	if !es[8].Script || es[7].DB != 3 {
		t.Fatal("db and script markers")
	}
	last := es[11].Args
	if last[2] != "a \"quoted\" \\ \x00\xff\n" {
		t.Fatalf("escapes: %q", last[2])
	}
}

func TestParseQuotedRejectsBroken(t *testing.T) {
	for _, bad := range []string{`"open`, `unquoted`, `"a" b`} {
		if _, ok := parseQuoted(bad); ok {
			t.Errorf("%q", bad)
		}
	}
}

func TestParseSlowlogJSON(t *testing.T) {
	es, err := ParseSlowlog(strings.NewReader(`[[7,1700000000,15000,["GET","k"],"10.0.0.1:1","n"],
	  [6,1700000001,9000,["SET","k","aaaa... (500 more bytes)"],"10.0.0.1:1",""],
	  [5,1700000002,100,["MSET","a","1","... (12 more arguments)"],"",""]]`))
	if err != nil || len(es) != 3 {
		t.Fatalf("%v %v", es, err)
	}
	if es[0].TimeUS != 1700000000*1_000_000 || es[0].DurationUS != 15000 || es[0].Client != "10.0.0.1:1" || es[0].Truncated {
		t.Fatalf("%+v", es[0])
	}
	if !es[1].Truncated || !es[2].Truncated {
		t.Fatal("shortened arguments must be flagged")
	}
	for _, bad := range []string{`{}`, `[[1,2]]`, `[[1,"x",3,["GET"]]]`, `[[1,2,3,[]]]`} {
		if _, err := ParseSlowlog(strings.NewReader(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
	if Detect([]byte("  \n [[1")) != "slowlog" || Detect([]byte("1700.1 [0 x]")) != "monitor" {
		t.Fatal("Detect")
	}
}

func TestConvertFiltersGroupsAndSpreadsConnections(t *testing.T) {
	r := Convert(parse(t, monitorSample), []string{"monitor"}, nil, Options{})
	want := map[string]uint64{SkipOtherDB: 1, SkipScript: 1, SkipCredentials: 1, SkipDangerous: 1, SkipAdmin: 1}
	for k, n := range want {
		if r.Info.SkippedByReason[k] != n {
			t.Errorf("skipped %q = %d, want %d (%v)", k, r.Info.SkippedByReason[k], n, r.Info.SkippedByReason)
		}
	}
	if r.Info.Entries != 12 || r.Info.Converted != 7 || r.Info.Skipped != 5 || r.Info.Connections != 2 || r.Workers != 2 {
		t.Fatalf("%+v workers %d", r.Info, r.Workers)
	}
	var names []string
	for _, c := range r.Commands {
		names = append(names, fmt.Sprintf("w%d/g%d/%s/%s", c.Worker, c.Group, c.Conn, c.Cmd[0]))
		if c.Cmd[0] == "AUTH" || strings.Contains(strings.Join(c.Cmd, " "), "hunter2") {
			t.Fatal("credentials must never reach a recording")
		}
	}
	// Worker 0 (10.0.0.1): SET, then MULTI/INCR/EXEC as one dedicated group. Worker 1: GET, INCR, SET.
	got := strings.Join(names, " ")
	for _, w := range []string{"w0/g1//SET", "w0/g2/dedicated/MULTI", "w0/g2/dedicated/INCR", "w0/g2/dedicated/EXEC", "w1/g1//GET", "w1/g2//INCR", "w1/g3//SET"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in %s", w, got)
		}
	}
	if r.Commands[0].Offset != 0 || r.Info.FirstTimestamp == "" {
		t.Fatalf("offsets start at the first command: %+v", r.Commands[0])
	}
}

func TestConvertAllowDangerousAndDB(t *testing.T) {
	es := parse(t, monitorSample)
	r := Convert(es, nil, nil, Options{AllowDangerous: true, DB: 3})
	if r.Info.Converted != 1 { // database 3 holds one SET
		t.Fatalf("%+v", r.Info)
	}
	r = Convert(es, nil, nil, Options{AllowDangerous: true})
	found := false
	for _, c := range r.Commands {
		found = found || c.Cmd[0] == "FLUSHALL"
	}
	if !found {
		t.Fatal("-allow-dangerous keeps FLUSHALL")
	}
	// Credentials are dropped even then.
	for _, c := range r.Commands {
		if c.Cmd[0] == "AUTH" {
			t.Fatal("AUTH kept")
		}
	}
}

func TestConnectionsBeyondMaxWorkersShareAWorkerWithoutBreakingGroups(t *testing.T) {
	text := `1.000001 [0 a:1] "MULTI"
1.000002 [0 b:1] "MULTI"
1.000003 [0 a:1] "SET" "x" "1"
1.000004 [0 b:1] "SET" "y" "2"
1.000005 [0 a:1] "EXEC"
1.000006 [0 b:1] "EXEC"
1.000007 [0 c:1] "GET" "x"
`
	r := Convert(parse(t, text), nil, nil, Options{MaxWorkers: 1})
	if r.Workers != 1 || len(r.Commands) != 7 {
		t.Fatalf("%d workers, %d commands", r.Workers, len(r.Commands))
	}
	// Consecutive commands with one group number form a group: each MULTI..EXEC is contiguous.
	var groups []string
	for _, c := range r.Commands {
		if n := len(groups); n > 0 && strings.HasPrefix(groups[n-1], fmt.Sprint(c.Group, ":")) {
			groups[n-1] += "," + c.Cmd[0]
		} else {
			groups = append(groups, fmt.Sprint(c.Group, ":", c.Cmd[0]))
		}
	}
	if strings.Join(groups, " ") != "1:MULTI,SET,EXEC 2:MULTI,SET,EXEC 3:GET" {
		t.Fatalf("%v", groups)
	}
}

func TestUnclosedTransactionIsStillWritten(t *testing.T) {
	r := Convert(parse(t, "1.1 [0 a:1] \"MULTI\"\n1.2 [0 a:1] \"SET\" \"k\" \"v\"\n"), nil, nil, Options{})
	if len(r.Commands) != 2 || r.Commands[0].Group != r.Commands[1].Group {
		t.Fatalf("%+v", r.Commands)
	}
	if r := Convert(nil, nil, nil, Options{}); len(r.Commands) != 0 {
		t.Fatal("empty input")
	}
}

func TestSlowlogTruncatedEntriesAreSkipped(t *testing.T) {
	es, _ := ParseSlowlog(strings.NewReader(`[[1,100,5,["GET","a"],"",""],[2,101,5,["SET","a","x... (9 more bytes)"],"",""]]`))
	r := Convert(es, []string{"slowlog"}, nil, Options{})
	if r.Info.Converted != 1 || r.Info.SkippedByReason[SkipTruncated] != 1 {
		t.Fatalf("%+v", r.Info)
	}
}

func TestWriteMakesAReplayableRecording(t *testing.T) {
	r := Convert(parse(t, monitorSample), []string{"monitor"}, []string{"traffic.txt"}, Options{})
	p := filepath.Join(t.TempDir(), "imp.jsonl.zst")
	res, err := Write(p, r, "test", workloadrec.CompressionZstd)
	if err != nil || res.Ops != 7 {
		t.Fatalf("%+v %v", res, err)
	}
	info, err := workloadrec.Scan(p)
	if err != nil || info.Status != workloadrec.StatusComplete || info.Header.Import == nil || info.Header.Import.Skipped != 5 ||
		info.Header.Workers != 2 || info.Header.DataFingerprint != nil || info.Header.Outcomes {
		t.Fatalf("%+v %v", info.Header, err)
	}
	if info.Header.Import.Files[0] != "traffic.txt" || !strings.Contains(info.Header.Import.Note, "MONITOR") {
		t.Fatalf("%+v", info.Header.Import)
	}
	if _, err := Write(filepath.Join(t.TempDir(), "x"), Convert(nil, nil, nil, Options{}), "t", ""); err == nil {
		t.Fatal("nothing to import must be an error")
	}
}

func TestReadFileDetectsTheFormat(t *testing.T) {
	dir := t.TempDir()
	m := filepath.Join(dir, "m.txt")
	s := filepath.Join(dir, "s.json")
	writeFile(t, m, monitorSample)
	writeFile(t, s, `[[1,100,5,["GET","a"],"",""]]`)
	if es, f, junk, err := ReadFile(m, ""); err != nil || f != "monitor" || len(es) != 12 || junk != 2 {
		t.Fatalf("%d %s %d %v", len(es), f, junk, err)
	}
	if es, f, _, err := ReadFile(s, "auto"); err != nil || f != "slowlog" || len(es) != 1 {
		t.Fatalf("%d %s %v", len(es), f, err)
	}
	if _, _, _, err := ReadFile(m, "csv"); err == nil {
		t.Fatal("unknown format")
	}
}

// fakeServer speaks just enough RESP: AUTH, MONITOR, then a stream of lines.
func fakeServer(t *testing.T, password string, lines int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		readCmd := func() []string {
			head, err := br.ReadString('\n')
			if err != nil {
				return nil
			}
			var n int
			fmt.Sscanf(head, "*%d", &n)
			var args []string
			for i := 0; i < n; i++ {
				l, _ := br.ReadString('\n')
				var size int
				fmt.Sscanf(l, "$%d", &size)
				buf := make([]byte, size+2)
				_, _ = br.Read(buf)
				args = append(args, string(buf[:size]))
			}
			return args
		}
		for {
			args := readCmd()
			if args == nil {
				return
			}
			switch args[0] {
			case "AUTH":
				if args[len(args)-1] != password {
					fmt.Fprint(c, "-WRONGPASS invalid\r\n")
					return
				}
				fmt.Fprint(c, "+OK\r\n")
			case "MONITOR":
				fmt.Fprint(c, "+OK\r\n")
				for i := 0; i < lines; i++ {
					fmt.Fprintf(c, "+1700000000.%06d [0 127.0.0.1:9] \"GET\" \"k%d\"\r\n", i, i)
				}
				time.Sleep(2 * time.Second) // keep the stream open: the client's caps end it
				return
			}
		}
	}()
	return ln.Addr().String()
}

func TestCaptureStopsAtTheCommandCap(t *testing.T) {
	addr := fakeServer(t, "", 50)
	var out strings.Builder
	n, err := Capture(context.Background(), addr, CaptureOptions{MaxCommands: 10, MaxDuration: 5 * time.Second}, &out)
	if err != nil || n != 10 {
		t.Fatalf("%d %v", n, err)
	}
	if es, _, _ := ParseMonitor(strings.NewReader(out.String())); len(es) != 10 || es[9].Args[1] != "k9" {
		t.Fatalf("%d entries from %q", len(es), out.String())
	}
}

func TestCaptureStopsAtTheTimeCapAndUsesCredentials(t *testing.T) {
	addr := fakeServer(t, "s3cret", 3)
	var out strings.Builder
	t0 := time.Now()
	n, err := Capture(context.Background(), addr, CaptureOptions{Password: "s3cret", MaxCommands: 1000, MaxDuration: 300 * time.Millisecond}, &out)
	if err != nil || n != 3 || time.Since(t0) > 1500*time.Millisecond {
		t.Fatalf("%d %v after %v", n, err, time.Since(t0))
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Fatal("the password must not appear in the capture")
	}
	if _, err := Capture(context.Background(), fakeServer(t, "right", 1), CaptureOptions{Password: "wrong"}, &out); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Capture(context.Background(), "127.0.0.1:1", CaptureOptions{}, &out); err == nil {
		t.Fatal("unreachable server")
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := osWriteFile(p, []byte(s)); err != nil {
		t.Fatal(err)
	}
}
