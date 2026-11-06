package rawcmd

import (
	"fmt"
	"strings"
)

// rule describes a command (or a subcommand of one) that is refused.
type rule struct {
	reason string
	never  bool // refused even when dangerous commands are allowed
}

// whole commands that are refused whatever their arguments.
var blockedCommands = map[string]rule{
	"FLUSHALL":     {reason: "deletes every key"},
	"FLUSHDB":      {reason: "deletes every key of the database"},
	"SHUTDOWN":     {reason: "stops the server"},
	"DEBUG":        {reason: "can crash or stall the server"},
	"MONITOR":      {reason: "streams every command and slows the server"},
	"SYNC":         {reason: "starts a replication stream", never: true},
	"PSYNC":        {reason: "starts a replication stream", never: true},
	"REPLICAOF":    {reason: "changes replication"},
	"SLAVEOF":      {reason: "changes replication"},
	"FAILOVER":     {reason: "triggers a failover"},
	"SWAPDB":       {reason: "swaps databases"},
	"SAVE":         {reason: "blocks the server while it writes a snapshot"},
	"BGSAVE":       {reason: "forks the server"},
	"BGREWRITEAOF": {reason: "forks the server"},
	"MIGRATE":      {reason: "moves keys to another server"},
	"RESTORE":      {reason: "overwrites keys from a serialized value"},
	"MODULE":       {reason: "loads or unloads server code"},
	"BLPOP":        {reason: "blocks; use the BLPOP operation", never: true},
	"BRPOP":        {reason: "blocks; use the BRPOP operation", never: true},
	"BLMOVE":       {reason: "blocks the connection", never: true},
	"BRPOPLPUSH":   {reason: "blocks the connection", never: true},
	"BLMPOP":       {reason: "blocks the connection", never: true},
	"BZPOPMIN":     {reason: "blocks the connection", never: true},
	"BZPOPMAX":     {reason: "blocks the connection", never: true},
	"BZMPOP":       {reason: "blocks the connection", never: true},
	"SUBSCRIBE":    {reason: "turns the connection into a subscriber; use workload.pubsub", never: true},
	"PSUBSCRIBE":   {reason: "turns the connection into a subscriber; use workload.pubsub", never: true},
	"SSUBSCRIBE":   {reason: "turns the connection into a subscriber; use workload.pubsub", never: true},
	"UNSUBSCRIBE":  {reason: "belongs to a subscriber connection", never: true},
	"MULTI":        {reason: "changes the connection's state; use the TXN operation", never: true},
	"EXEC":         {reason: "changes the connection's state; use the TXN operation", never: true},
	"DISCARD":      {reason: "changes the connection's state; use the TXN operation", never: true},
	"WATCH":        {reason: "changes the connection's state; use the WATCH_INCR operation", never: true},
	"UNWATCH":      {reason: "changes the connection's state", never: true},
	"SELECT":       {reason: "changes the connection's database for every worker sharing it", never: true},
	"AUTH":         {reason: "changes the connection's user", never: true},
	"HELLO":        {reason: "changes the connection's protocol", never: true},
	"RESET":        {reason: "resets the connection", never: true},
	"QUIT":         {reason: "closes the connection", never: true},
}

// subcommands that are refused, by command.
var blockedSubs = map[string]map[string]rule{
	"CONFIG": {"SET": {reason: "changes the server's configuration"}, "REWRITE": {reason: "rewrites the server's configuration file"}, "RESETSTAT": {reason: "resets the server's statistics"}},
	"CLIENT": {"KILL": {reason: "disconnects clients"}, "PAUSE": {reason: "stops the server answering"}, "UNPAUSE": {reason: "changes the server's state"}, "REPLY": {reason: "stops replies", never: true},
		"NO-EVICT": {reason: "changes the connection"}, "SETNAME": {reason: "changes the connection", never: true}, "TRACKING": {reason: "changes the connection", never: true}},
	"CLUSTER": {"FAILOVER": {reason: "triggers a failover"}, "RESET": {reason: "resets the node"}, "FORGET": {reason: "removes a node"}, "MEET": {reason: "changes the cluster"},
		"ADDSLOTS": {reason: "changes slot ownership"}, "ADDSLOTSRANGE": {reason: "changes slot ownership"}, "DELSLOTS": {reason: "changes slot ownership"}, "DELSLOTSRANGE": {reason: "changes slot ownership"},
		"SETSLOT": {reason: "changes slot ownership"}, "REPLICATE": {reason: "changes replication"}, "SAVECONFIG": {reason: "writes the cluster configuration"}, "FLUSHSLOTS": {reason: "changes slot ownership"}},
	"SCRIPT":   {"FLUSH": {reason: "deletes every cached script"}, "KILL": {reason: "stops a running script"}},
	"FUNCTION": {"FLUSH": {reason: "deletes every function"}, "DELETE": {reason: "deletes a function library"}, "KILL": {reason: "stops a running function"}, "RESTORE": {reason: "overwrites function libraries"}},
	"ACL":      {"SETUSER": {reason: "changes users"}, "DELUSER": {reason: "deletes users"}, "LOAD": {reason: "replaces the users"}, "SAVE": {reason: "writes the ACL file"}},
	"SLOWLOG":  {"RESET": {reason: "clears the slowlog plgv reads"}},
	"LATENCY":  {"RESET": {reason: "clears latency events"}},
	"MEMORY":   {"PURGE": {reason: "forces the allocator to release memory"}},
	"OBJECT":   {},
}

// Check returns an error when the command must not run. With allowDangerous,
// commands that can harm the server are allowed, but commands that would break
// plgv's own connections (MULTI, SELECT, blocking and subscribe commands) never are.
func Check(t *Template, allowDangerous bool) error {
	name := t.Name()
	if r, ok := blockedCommands[name]; ok {
		return refuse(name, r, allowDangerous)
	}
	if subs, ok := blockedSubs[name]; ok {
		if r, ok := subs[t.Sub()]; ok {
			return refuse(name+" "+t.Sub(), r, allowDangerous)
		}
	}
	// A BLOCK option turns XREAD/XREADGROUP into a blocking command.
	if (name == "XREAD" || name == "XREADGROUP") && t.HasArg("BLOCK") {
		return fmt.Errorf("%s with BLOCK waits for data and would stall the pipeline; use the XREAD_BLOCK operation", name)
	}
	return nil
}

func refuse(what string, r rule, allowDangerous bool) error {
	if r.never {
		return fmt.Errorf("%s is not supported in raw commands: it %s", what, r.reason)
	}
	if allowDangerous {
		return nil
	}
	return fmt.Errorf("%s is blocked: it %s. Set workload.allow_dangerous: true to allow it", what, r.reason)
}

// Dangerous returns the names of the commands in the given templates that are
// only allowed because dangerous commands were allowed.
func Dangerous(ts []*Template) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if Check(t, false) != nil && Check(t, true) == nil {
			n := t.Name()
			if t.Sub() != "" {
				if _, ok := blockedSubs[n][t.Sub()]; ok {
					n += " " + t.Sub()
				}
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// ParseLines reads a list of commands, one per line. A line may start with a
// weight and an "x" or space ("5 GET {key}", "5x GET {key}"); blank lines and
// lines starting with # are skipped. It returns the weight and command text of
// each line.
func ParseLines(text string) ([]Line, error) {
	var out []Line
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		w, cmd := 1, line
		if first, rest, ok := strings.Cut(line, " "); ok {
			if n, err := parseWeight(first); err == nil {
				w, cmd = n, strings.TrimSpace(rest)
			}
		}
		if cmd == "" {
			return nil, fmt.Errorf("line %d: a weight with no command", i+1)
		}
		if _, err := Parse(cmd); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, Line{Weight: w, Command: cmd})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no commands found")
	}
	return out, nil
}

// Line is one command of a command list with its weight.
type Line struct {
	Weight  int
	Command string
}

func parseWeight(s string) (int, error) {
	s = strings.TrimSuffix(s, "x")
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 {
		return 0, fmt.Errorf("weight must be >= 1")
	}
	return n, nil
}

// recordedOK are commands a recording legitimately holds although raw commands
// may not use them: plgv's own operations send them (TXN, WATCH_INCR, the
// blocking operations).
var recordedOK = map[string]bool{
	"MULTI": true, "EXEC": true, "DISCARD": true, "WATCH": true, "UNWATCH": true,
	"BLPOP": true, "BRPOP": true, "BLMOVE": true, "BRPOPLPUSH": true, "BLMPOP": true, "BZPOPMIN": true, "BZPOPMAX": true, "BZMPOP": true,
}

// CheckRecorded returns an error when a command read from a recording must not
// be replayed. A recording made by plgv only holds commands plgv's operations
// sent, but a file can be edited or imported, so replay checks again: harmful
// commands need allowDangerous, and commands that would change the replay's
// own connections (SELECT, AUTH, subscribe) are never replayed.
func CheckRecorded(args []string, allowDangerous bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	name, sub := strings.ToUpper(args[0]), ""
	if len(args) > 1 {
		sub = strings.ToUpper(args[1])
	}
	if r, ok := blockedCommands[name]; ok && !recordedOK[name] {
		return refuse(name, r, allowDangerous)
	}
	if r, ok := blockedSubs[name][sub]; ok {
		return refuse(name+" "+sub, r, allowDangerous)
	}
	return nil
}
