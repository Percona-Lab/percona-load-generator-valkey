package serverconfig

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func TestMaskHidesSecrets(t *testing.T) {
	in := map[string]string{
		"requirepass": "hunter2", "masterauth": "x", "primaryauth": "y", "tls-key-file-pass": "z",
		"masterpass": "", "maxmemory": "0", "tls-auth-clients": "yes", "masteruser": "repl",
	}
	got := Mask(in)
	for _, k := range []string{"requirepass", "masterauth", "primaryauth", "tls-key-file-pass"} {
		if got[k] != Masked {
			t.Errorf("%s = %q, want masked", k, got[k])
		}
	}
	for _, k := range []string{"masterpass", "maxmemory", "tls-auth-clients", "masteruser"} {
		if got[k] != in[k] {
			t.Errorf("%s = %q, want %q unchanged", k, got[k], in[k])
		}
	}
	if in["requirepass"] != "hunter2" {
		t.Error("Mask changed its input")
	}
}

func TestParseBothShapes(t *testing.T) {
	want := map[string]string{"maxmemory": "0", "hz": "10"}
	if got := parse(map[string]any{"maxmemory": "0", "hz": "10"}); !reflect.DeepEqual(got, want) {
		t.Errorf("map: %v", got)
	}
	if got := parse([]any{"maxmemory", "0", "hz", "10"}); !reflect.DeepEqual(got, want) {
		t.Errorf("list: %v", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"NOPERM this user has no permissions to run the 'config' command": StatusNotPermitted,
		"ERR unknown command 'CONFIG'":                                    StatusNotPermitted,
		"dial tcp: connection refused":                                    StatusError,
	}
	for msg, want := range cases {
		if got := classify(errors.New(msg)); got != want {
			t.Errorf("%q: got %s, want %s", msg, got, want)
		}
	}
}

type fake struct {
	replies []any
	err     error
	n       int
}

func (f *fake) Query(context.Context, valkey.Cmd) (any, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := f.replies[min(f.n, len(f.replies)-1)]
	f.n++
	return r, nil
}

func reader(q func(context.Context, valkey.Cmd) (any, error), name string) *Reader {
	return &Reader{name: name, nodes: []target{{addr: "a:1", q: q}}}
}

func TestStartFinishRecordsChanges(t *testing.T) {
	f := &fake{replies: []any{
		map[string]any{"maxmemory": "0", "requirepass": "pw", "hz": "10"},
		map[string]any{"maxmemory": "1000", "requirepass": "pw", "hz": "10"},
	}}
	r := reader(f.Query, "setup-a")
	r.Start(context.Background())
	s := r.Finish(context.Background())
	if s.Status != StatusOK || s.Name != "setup-a" || len(s.Nodes) != 1 {
		t.Fatalf("snapshot: %+v", s)
	}
	n := s.Nodes[0]
	if n.Settings["requirepass"] != Masked {
		t.Error("secret was stored")
	}
	if len(n.Changed) != 1 || n.Changed[0] != (Change{Key: "maxmemory", Before: "0", After: "1000"}) {
		t.Errorf("changed: %+v", n.Changed)
	}
}

func TestBlockedConfigDoesNotFail(t *testing.T) {
	f := &fake{err: errors.New("NOPERM this user has no permissions to run the 'config' command")}
	r := reader(f.Query, "")
	r.Start(context.Background())
	s := r.Finish(context.Background())
	if s.Status != StatusNotPermitted || s.Nodes[0].Settings != nil || s.Nodes[0].Error == "" {
		t.Errorf("snapshot: %+v", s)
	}
}

func TestDifferingIgnoresPerNodeSettings(t *testing.T) {
	nodes := []Node{
		{Status: StatusOK, Settings: map[string]string{"port": "7001", "maxmemory": "0", "hz": "10"}},
		{Status: StatusOK, Settings: map[string]string{"port": "7002", "maxmemory": "1gb", "hz": "10"}},
		{Status: StatusError},
	}
	if got := differing(nodes); !reflect.DeepEqual(got, []string{"maxmemory"}) {
		t.Errorf("differing: %v", got)
	}
	if differing(nodes[:1]) != nil {
		t.Error("one node cannot differ")
	}
}

func TestCompare(t *testing.T) {
	mk := func(m map[string]string) *Snapshot {
		return &Snapshot{Nodes: []Node{{Status: StatusOK, Settings: m}}}
	}
	a := mk(map[string]string{"io-threads": "1", "hz": "10", "port": "1", "only-a": "x"})
	b := mk(map[string]string{"io-threads": "4", "hz": "10", "port": "2"})
	got := Compare(a, b)
	want := []Diff{{"io-threads", "1", "4"}, {"only-a", "x", "-"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if Compare(a, nil) != nil || Compare(&Snapshot{}, b) != nil {
		t.Error("an unreadable side gives no diff")
	}
}
