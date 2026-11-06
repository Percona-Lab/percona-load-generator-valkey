package valkey

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/keyspace"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/values"
)

// Reference values from the Valkey cluster specification and CLUSTER KEYSLOT.
func TestSlotMatchesTheClusterSpec(t *testing.T) {
	if got := crc16("123456789"); got != 0x31C3 {
		t.Fatalf("crc16 test vector: %#x", got)
	}
	for key, want := range map[string]int{"foo": 12182, "bar": 5061, "hello": 866, "user1000": 3443} {
		if got := Slot(key); got != want {
			t.Errorf("Slot(%q) = %d, want %d", key, got, want)
		}
	}
	if Slot("{user1000}.following") != Slot("user1000") || Slot("{user1000}.followers") != Slot("user1000") {
		t.Fatal("a hash tag must decide the slot")
	}
	// Edge cases from the spec: empty tag hashes the whole key; first tag wins.
	if Slot("foo{}{bar}") != Slot("foo{}{bar}") || Slot("foo{}{bar}") == Slot("bar") {
		t.Fatal("an empty {} tag must hash the whole key")
	}
	if Slot("foo{{bar}}zap") != Slot("{bar") {
		t.Fatal("the tag is what sits between the first { and the next }")
	}
	if Slot("foo{bar}{zap}") != Slot("bar") {
		t.Fatal("only the first tag counts")
	}
	for i := 0; i < 1000; i++ {
		if s := Slot(strings.Repeat("k", i%50) + string(rune('a'+i%26))); s < 0 || s >= SlotCount {
			t.Fatalf("slot %d out of range", s)
		}
	}
}

func TestRouteKey(t *testing.T) {
	if RouteKey(Cmd{"GET", "k"}) != "k" || RouteKey(Cmd{"MGET", "a", "b"}) != "a" || RouteKey(Cmd{"SCAN", "0"}) != "" || RouteKey(Cmd{"PING"}) != "" {
		t.Fatal("RouteKey")
	}
}

func tagGen(t *testing.T, groups int, keys int64) (*Gen, config.Dataset) {
	d := config.Default().Dataset
	d.KeyPrefix = "p:"
	d.HashTagGroups = groups
	d.KeyCount = keys
	kg, _ := keyspace.New("uniform", keys, 1, 0)
	vs, _ := values.NewSource(8, 1)
	return NewGen(rand.New(rand.NewSource(2)), kg, vs, d), d
}

func TestHashTagGroupsKeepMultiKeyCommandsOnOneSlot(t *testing.T) {
	g, _ := tagGen(t, 8, 1000)
	for i := 0; i < 500; i++ {
		for _, op := range []string{"MGET", "MSET"} {
			cmd := builders[op](g, config.MixItem{Op: op, Count: 6})
			var keys []string
			if op == "MGET" {
				keys = cmd[1:]
			} else {
				for j := 1; j < len(cmd); j += 2 {
					keys = append(keys, cmd[j])
				}
			}
			if len(keys) != 6 {
				t.Fatalf("%s: %d keys", op, len(keys))
			}
			slot := Slot(keys[0])
			for _, k := range keys {
				if Slot(k) != slot {
					t.Fatalf("%s keys %v span slots", op, keys)
				}
			}
		}
	}
}

func TestHashTagNamingIsConsistentAcrossGetSetAndPreload(t *testing.T) {
	g, d := tagGen(t, 4, 100)
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		seen[builders["GET"](g, config.MixItem{})[1]] = true
	}
	for k := range seen {
		if !strings.HasPrefix(k, "p:{") || !strings.Contains(k, "}:s:") {
			t.Fatalf("key %q lacks its tag", k)
		}
	}
	// A key's tag is idx % groups, and preload writes exactly the same names.
	vs, _ := values.NewSource(8, 1)
	pre := map[string]bool{}
	for i := int64(0); i < d.KeyCount; i++ {
		pre[preloadCmd(d, config.TypeString, i, vs, rand.New(rand.NewSource(1)))[1]] = true
	}
	for k := range seen {
		if !pre[k] {
			t.Fatalf("workload key %q is not among the preloaded keys", k)
		}
	}
	if KeyName(d, "s", 10) != "p:{2}:s:10" {
		t.Fatalf("%q", KeyName(d, "s", 10))
	}
	d.HashTagGroups = 0
	if KeyName(d, "s", 10) != "p:s:10" {
		t.Fatal("no tags when groups = 0")
	}
}

func TestMultiKeyDefaultUnchangedWithoutTags(t *testing.T) {
	g, _ := tagGen(t, 0, 1000)
	cmd := builders["MGET"](g, config.MixItem{Op: "MGET", Count: 4})
	if len(cmd) != 5 {
		t.Fatal(cmd)
	}
}

func TestScanPatternMatchesTaggedKeys(t *testing.T) {
	g, _ := tagGen(t, 4, 100)
	c := builders["SCAN"](g, config.MixItem{Op: "SCAN"})
	if c[3] != "p:*:s:*" {
		t.Fatalf("%v", c)
	}
}
