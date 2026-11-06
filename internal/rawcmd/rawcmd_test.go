package rawcmd

import (
	"reflect"
	"strings"
	"testing"
)

type fakeSrc struct{ keys int }

func (f *fakeSrc) Key(typ string) string  { f.keys++; return "k:" + typ + ":7" }
func (f *fakeSrc) Value() string          { return "VAL" }
func (f *fakeSrc) Int(lo, hi int64) int64 { return lo + (hi-lo)/2 }
func (f *fakeSrc) N() int64               { return 42 }
func (f *fakeSrc) NowMillis() int64       { return 1700000000000 }

func expand(t *testing.T, text string) []string {
	t.Helper()
	tm, err := Parse(text)
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	return tm.Expand(&fakeSrc{})
}

func TestParseQuotingAndEscapes(t *testing.T) {
	cases := map[string][]string{
		`GET foo`:             {"GET", "foo"},
		"  SET   a   b  ":     {"SET", "a", "b"},
		`SET k "hello world"`: {"SET", "k", "hello world"},
		`SET k 'it''s'`:       {"SET", "k", "its"},
		`SET k "say \"hi\""`:  {"SET", "k", `say "hi"`},
		`SET k a\ b`:          {"SET", "k", "a b"},
		`SET k ''`:            {"SET", "k", ""},
		`EVAL "return 1" 0`:   {"EVAL", "return 1", "0"},
		`SET {user1}:a v`:     {"SET", "{user1}:a", "v"}, // a hash tag is text
		"GET\tk":              {"GET", "k"},
	}
	for in, want := range cases {
		if got := expand(t, in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "   ", `SET "unterminated`, `GET k\`, "{key} k"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	got := expand(t, "SET {key} {value} PX {int:100-300}")
	if !reflect.DeepEqual(got, []string{"SET", "k:string:7", "VAL", "PX", "200"}) {
		t.Fatalf("%q", got)
	}
	if got := expand(t, "HSET {key:hash} f {n}"); got[1] != "k:hash:7" || got[3] != "42" {
		t.Fatalf("%q", got)
	}
	if got := expand(t, "SET user:{n}:{now} v"); got[1] != "user:42:1700000000000" {
		t.Fatalf("placeholders inside a word: %q", got)
	}
	if got := expand(t, "GET {key:zset}"); got[1] != "k:zset:7" {
		t.Fatalf("%q", got)
	}
	for _, bad := range []string{"GET {key:nope}", "GET {int:5-1}", "GET {int:a-b}", "GET {int:5}"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	// Unknown braces are ordinary text, never an error.
	if got := expand(t, "SET {not-a-placeholder} v"); got[1] != "{not-a-placeholder}" {
		t.Fatalf("%q", got)
	}
}

func TestKeyArgFindsTheRoutingArgument(t *testing.T) {
	for text, want := range map[string]int{"GET {key}": 1, "SET x {key} v": 2, "EVAL s 1 {key}": 3, "PING": -1, "GET plain": -1, "MGET a {key:hash}": 2} {
		tm, err := Parse(text)
		if err != nil || tm.KeyArg != want {
			t.Errorf("%q: KeyArg %d (%v), want %d", text, tm.KeyArg, err, want)
		}
	}
}

func TestEachCallDrawsFreshValues(t *testing.T) {
	tm, _ := Parse("GET {key}")
	src := &fakeSrc{}
	tm.Expand(src)
	tm.Expand(src)
	if src.keys != 2 {
		t.Fatalf("a key placeholder is expanded once per call, got %d", src.keys)
	}
}

func check(t *testing.T, text string, allow bool) error {
	t.Helper()
	tm, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return Check(tm, allow)
}

func TestBlocklistRefusesDangerousCommands(t *testing.T) {
	for _, text := range []string{"FLUSHALL", "flushdb ASYNC", "SHUTDOWN NOSAVE", "DEBUG SLEEP 1", "CONFIG SET maxmemory 1", "config set save ''", "CONFIG RESETSTAT",
		"CLIENT KILL ID 3", "CLUSTER FAILOVER", "CLUSTER RESET HARD", "SCRIPT FLUSH", "FUNCTION FLUSH", "FUNCTION DELETE plgv", "ACL SETUSER x on", "REPLICAOF NO ONE",
		"MONITOR", "SAVE", "BGSAVE", "SLOWLOG RESET", "MODULE LOAD x.so", "SWAPDB 0 1", "FAILOVER", "MIGRATE h 1 k 0 1"} {
		err := check(t, text, false)
		if err == nil || !strings.Contains(err.Error(), "blocked") || !strings.Contains(err.Error(), "allow_dangerous") {
			t.Errorf("%q: %v", text, err)
		}
		if err := check(t, text, true); err != nil {
			t.Errorf("%q must be allowed when dangerous commands are allowed: %v", text, err)
		}
	}
}

func TestBlocklistAllowsHarmlessSubcommands(t *testing.T) {
	for _, text := range []string{"CONFIG GET maxmemory", "CLIENT LIST", "CLIENT ID", "CLUSTER INFO", "CLUSTER KEYSLOT {key}", "CLUSTER SHARDS", "SCRIPT LOAD x",
		"FUNCTION LIST", "ACL WHOAMI", "SLOWLOG GET 5", "LATENCY LATEST", "MEMORY USAGE {key}", "INFO", "GET {key}", "SET {key} {value}", "KEYS plgv:*", "EVAL 'return 1' 0", "XREAD COUNT 5 STREAMS {key:stream} 0"} {
		if err := check(t, text, false); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestSomeCommandsAreNeverAllowed(t *testing.T) {
	for _, text := range []string{"MULTI", "EXEC", "WATCH k", "SELECT 3", "AUTH pw", "HELLO 3", "SUBSCRIBE c", "SSUBSCRIBE c", "BLPOP k 1", "BRPOP k 1", "BZPOPMIN z 1",
		"XREAD BLOCK 100 STREAMS s $", "XREADGROUP GROUP g c BLOCK 5 STREAMS s >", "CLIENT REPLY OFF", "RESET", "SYNC"} {
		for _, allow := range []bool{false, true} {
			err := check(t, text, allow)
			if err == nil || strings.Contains(err.Error(), "allow_dangerous") {
				t.Errorf("%q (allow=%v) must be refused with no way around it: %v", text, allow, err)
			}
		}
	}
	// The messages point at the right feature.
	if err := check(t, "MULTI", true); !strings.Contains(err.Error(), "TXN") {
		t.Fatal(err)
	}
	if err := check(t, "BRPOP k 1", true); !strings.Contains(err.Error(), "BRPOP operation") {
		t.Fatal(err)
	}
}

func TestDangerousListsWhatWasAllowed(t *testing.T) {
	var ts []*Template
	for _, s := range []string{"GET {key}", "FLUSHALL", "CONFIG SET maxmemory 1", "CONFIG GET x", "FLUSHALL"} {
		tm, _ := Parse(s)
		ts = append(ts, tm)
	}
	got := Dangerous(ts)
	if !reflect.DeepEqual(got, []string{"FLUSHALL", "CONFIG SET"}) {
		t.Fatalf("%v", got)
	}
}

func TestParseLines(t *testing.T) {
	ls, err := ParseLines("# a comment\n\n5 GET {key}\n2x SET {key} {value}\nHSET {key:hash} f v\n  3   INCR counter  \n")
	if err != nil {
		t.Fatal(err)
	}
	want := []Line{{5, "GET {key}"}, {2, "SET {key} {value}"}, {1, "HSET {key:hash} f v"}, {3, "INCR counter"}}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("%+v", ls)
	}
	for _, bad := range []string{"", "# only a comment\n", "5", "3 SET \"x", "0 GET k"} {
		if _, err := ParseLines(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestCheckRecorded(t *testing.T) {
	ok := [][]string{{"GET", "k"}, {"multi"}, {"EXEC"}, {"WATCH", "k"}, {"BLPOP", "q", "1"}, {"CONFIG", "GET", "maxmemory"}, {"SCRIPT", "LOAD", "return 1"}}
	for _, c := range ok {
		if err := CheckRecorded(c, false); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	dangerous := [][]string{{"FLUSHALL"}, {"flushdb"}, {"CONFIG", "set", "maxmemory", "1"}, {"DEBUG", "SLEEP", "1"}, {"SCRIPT", "FLUSH"}, {"CLIENT", "KILL", "ID", "1"}}
	for _, c := range dangerous {
		if err := CheckRecorded(c, false); err == nil {
			t.Errorf("%v must be refused by default", c)
		}
		if err := CheckRecorded(c, true); err != nil {
			t.Errorf("%v must be allowed with allowDangerous: %v", c, err)
		}
	}
	never := [][]string{{"SELECT", "1"}, {"AUTH", "x"}, {"HELLO", "3"}, {"SUBSCRIBE", "c"}, {"CLIENT", "SETNAME", "x"}, {"PSYNC", "?", "-1"}}
	for _, c := range never {
		if err := CheckRecorded(c, true); err == nil {
			t.Errorf("%v must never be replayed", c)
		}
	}
	if CheckRecorded(nil, true) == nil {
		t.Error("an empty command is an error")
	}
}
