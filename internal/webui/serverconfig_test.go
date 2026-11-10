package webui

import (
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/runsummary"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/serverconfig"
)

func snap(name string, s map[string]string) *serverconfig.Snapshot {
	return &serverconfig.Snapshot{Name: name, Status: serverconfig.StatusOK, Nodes: []serverconfig.Node{{Addr: "x:1", Status: serverconfig.StatusOK, Settings: s}}}
}

func TestCompareListsServerSettingDifferences(t *testing.T) {
	a, b := summaryFor(1, 1, 1, ""), summaryFor(1, 1, 1, "")
	if c := Compare(Meta{}, Meta{}, a, b); len(c.ServerConfigDiffs) != 0 || c.ServerConfigNote != "" {
		t.Fatalf("neither recorded: %+v", c)
	}
	a.ServerConfig = snap("one", map[string]string{"io-threads": "1", "hz": "10"})
	if c := Compare(Meta{}, Meta{}, a, b); c.ServerConfigNote == "" || len(c.ServerConfigDiffs) != 0 {
		t.Fatalf("one side only must explain itself: %+v", c)
	}
	b.ServerConfig = snap("four", map[string]string{"io-threads": "4", "hz": "10"})
	c := Compare(Meta{}, Meta{}, a, b)
	if len(c.ServerConfigDiffs) != 1 || c.ServerConfigDiffs[0] != (serverconfig.Diff{Key: "io-threads", A: "1", B: "4"}) {
		t.Fatalf("diffs: %+v", c.ServerConfigDiffs)
	}
	b.ServerConfig = &serverconfig.Snapshot{Status: serverconfig.StatusNotPermitted}
	if c := Compare(Meta{}, Meta{}, a, b); c.ServerConfigNote == "" {
		t.Error("an unreadable side must explain itself")
	}
}

func TestMetaCarriesTheServerSetup(t *testing.T) {
	s := summaryFor(1, 1, 1, "")
	if m := metaOf("20260101-000000", s); m.ServerSetup != "" || m.ServerConfigOn {
		t.Fatalf("not recorded: %+v", m)
	}
	s.ServerConfig = snap("valkey-9.1-io4", map[string]string{"hz": "10"})
	m := metaOf("20260101-000000", s)
	if m.ServerSetup != "valkey-9.1-io4" || !m.ServerConfigOn {
		t.Fatalf("%+v", m)
	}
	_ = runsummary.SchemaVersion
}

func TestDiffConfigManyListsSettingsThatDifferAcrossAllRuns(t *testing.T) {
	a := "run:\n  concurrency: 8\n  duration: 30s\noutput:\n  summary_file: a\n"
	b := "run:\n  concurrency: 4\n  duration: 30s\noutput:\n  summary_file: b\n"
	c := "run:\n  concurrency: 4\n  duration: 60s\n"
	got := diffConfigMany([]string{a, b, c})
	if len(got) != 2 || got[0].Key != "run.concurrency" || got[1].Key != "run.duration" {
		t.Fatalf("%+v", got)
	}
	if v := got[0].Values; v[0] != "8" || v[1] != "4" || v[2] != "4" {
		t.Errorf("values: %v", v)
	}
	if n := len(diffConfigMany([]string{a, a})); n != 0 {
		t.Errorf("same configs differ: %d", n)
	}
}

func TestTargetOfStripsSchemeCredentialsAndPath(t *testing.T) {
	cases := map[string]string{
		"target:\n  uris:\n    - valkey://user:pw@10.0.0.5:6379/0\n": "10.0.0.5:6379",
		"target:\n  uris:\n    - 127.0.0.1:6379\n":                   "127.0.0.1:6379",
		"run:\n  duration: 1s\n":                                     "",
	}
	for y, want := range cases {
		if got := targetOf(y); got != want {
			t.Errorf("%q: got %q, want %q", y, got, want)
		}
	}
}
