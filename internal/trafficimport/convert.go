package trafficimport

import (
	"sort"
	"strings"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/rawcmd"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/workloadrec"
)

// Options tune a conversion.
type Options struct {
	// MaxWorkers bounds the workers of the recording (default 64). Connections
	// beyond it share a worker, in time order.
	MaxWorkers int
	// DB selects the database to import (default 0, the one replay uses).
	DB int
	// AllowDangerous keeps harmful commands (FLUSHALL, CONFIG SET, DEBUG ...).
	AllowDangerous bool
}

// Skip reasons.
const (
	SkipEmpty       = "not a command"
	SkipOtherDB     = "another database"
	SkipScript      = "issued by a script (the EVAL that ran it is replayed instead)"
	SkipConnection  = "belongs to the capturing connection (MONITOR, SELECT, SUBSCRIBE, ...)"
	SkipCredentials = "carries credentials or changes access (AUTH, HELLO, MIGRATE, ACL)"
	SkipDangerous   = "harmful (use -allow-dangerous to keep it)"
	SkipTruncated   = "shortened by the server's slowlog, so it cannot be replayed faithfully"
	SkipAdmin       = "administration, not workload (INFO, COMMAND, SLOWLOG, ...)"
)

// Converted is a command ready to be written to a recording.
type Converted struct {
	Worker int
	Offset time.Duration
	Group  uint64
	Conn   string
	Cmd    []string
}

// Result is a conversion: the commands, in time order, and what was left out.
type Result struct {
	Commands    []Converted
	Info        workloadrec.ImportInfo
	Workers     int
	FirstTimeUS int64
	LastTimeUS  int64
}

var adminCommands = map[string]bool{
	"INFO": true, "COMMAND": true, "SLOWLOG": true, "LATENCY": true, "MEMORY": true, "DBSIZE": true, "LASTSAVE": true, "TIME": true,
	"PING": true, "ECHO": true, "REPLCONF": true, "WAIT": true, "WAITAOF": true, "ROLE": true, "MODULE": true,
	"CLUSTER": true, "SENTINEL": true, "CONFIG": true, "CLIENT": true, "DEBUG": true, "SCRIPT": true,
}

var connectionCommands = map[string]bool{
	"MONITOR": true, "SELECT": true, "QUIT": true, "RESET": true, "SUBSCRIBE": true, "PSUBSCRIBE": true, "SSUBSCRIBE": true,
	"UNSUBSCRIBE": true, "PUNSUBSCRIBE": true, "SUNSUBSCRIBE": true, "SYNC": true, "PSYNC": true, "READONLY": true, "READWRITE": true, "ASKING": true,
}

var credentialCommands = map[string]bool{"AUTH": true, "HELLO": true, "MIGRATE": true, "ACL": true}

// Convert filters, groups and spreads captured entries over workers. Entries
// from several sources are merged in time order.
func Convert(entries []Entry, sources []string, files []string, opt Options) *Result {
	if opt.MaxWorkers <= 0 {
		opt.MaxWorkers = 64
	}
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TimeUS < sorted[j].TimeUS })

	res := &Result{Info: workloadrec.ImportInfo{Sources: sources, Files: files, Entries: uint64(len(entries)), SkippedByReason: map[string]uint64{}}}
	skip := func(reason string) {
		res.Info.Skipped++
		res.Info.SkippedByReason[reason]++
	}
	workerOf := map[string]int{}
	worker := func(client string) int {
		w, ok := workerOf[client]
		if !ok {
			w = len(workerOf)
			workerOf[client] = w
		}
		return w % opt.MaxWorkers
	}
	group := map[int]uint64{} // last group number per worker
	// A MULTI or WATCH opens a group on its connection until EXEC, DISCARD or
	// UNWATCH. Its commands are held back and written together, so connections
	// that share a worker never interleave inside one group.
	pending := map[string][]Converted{}
	if len(sorted) == 0 {
		res.Info.Note = importNote
		return res
	}
	first := sorted[0].TimeUS
	emit := func(cs []Converted, w int) {
		group[w]++
		for _, c := range cs {
			c.Group = group[w]
			res.Commands = append(res.Commands, c)
		}
	}
	usedWorkers := 0
	for _, e := range sorted {
		name := workloadrec.Name(e.Args)
		reason := ""
		switch {
		case len(e.Args) == 0 || name == "":
			reason = SkipEmpty
		case e.DB != opt.DB && e.Source == "monitor":
			reason = SkipOtherDB
		case e.Script:
			reason = SkipScript
		case e.Truncated:
			reason = SkipTruncated
		case credentialCommands[name]:
			reason = SkipCredentials
		case connectionCommands[name]:
			reason = SkipConnection
		case adminCommands[name] && !(name == "SCRIPT" && len(e.Args) > 1 && strings.EqualFold(e.Args[1], "LOAD")):
			reason = SkipAdmin
		default:
			if err := rawcmd.CheckRecorded(e.Args, opt.AllowDangerous); err != nil {
				reason = SkipDangerous
			}
		}
		if reason != "" {
			skip(reason)
			continue
		}
		w := worker(e.Client)
		if w+1 > usedWorkers {
			usedWorkers = w + 1
		}
		c := Converted{Worker: w, Offset: time.Duration(e.TimeUS-first) * time.Microsecond, Cmd: e.Args}
		res.Info.Converted++
		if res.FirstTimeUS == 0 || e.TimeUS < res.FirstTimeUS {
			res.FirstTimeUS = e.TimeUS
		}
		if e.TimeUS > res.LastTimeUS {
			res.LastTimeUS = e.TimeUS
		}
		open, inGroup := pending[e.Client]
		switch {
		case name == "MULTI" || name == "WATCH":
			if inGroup && name == "WATCH" { // WATCH inside an open group: keep adding
				c.Conn = workloadrec.ConnDedicated
				pending[e.Client] = append(open, c)
				continue
			}
			c.Conn = workloadrec.ConnDedicated
			pending[e.Client] = []Converted{c}
		case inGroup:
			c.Conn = workloadrec.ConnDedicated
			open = append(open, c)
			if name == "EXEC" || name == "DISCARD" || name == "UNWATCH" {
				emit(open, w)
				delete(pending, e.Client)
			} else {
				pending[e.Client] = open
			}
		default:
			emit([]Converted{c}, w)
		}
	}
	// Groups that never closed (the capture ended inside a transaction).
	var clients []string
	for cl := range pending {
		clients = append(clients, cl)
	}
	sort.Strings(clients)
	for _, cl := range clients {
		emit(pending[cl], pending[cl][0].Worker)
	}
	res.Workers = usedWorkers
	res.Info.Connections = len(workerOf)
	if res.FirstTimeUS > 0 {
		res.Info.FirstTimestamp = time.UnixMicro(res.FirstTimeUS).UTC().Format(time.RFC3339)
		res.Info.LastTimestamp = time.UnixMicro(res.LastTimeUS).UTC().Format(time.RFC3339)
	}
	res.Info.Note = importNote
	return res
}

const importNote = "Imported from captured traffic: commands are replayed as captured, in the order and at the times they were seen. MONITOR cannot show pipelining, replies or latency, so every command is its own group and the recording has no outcomes; values are literal, so a command whose effect depends on the server's clock or random choices may behave differently."
