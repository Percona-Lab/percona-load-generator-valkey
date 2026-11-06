package topology

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

func node(id, ip string, port int64, role, health string) map[string]any {
	return map[string]any{"id": id, "endpoint": ip, "ip": ip, "port": port, "role": role, "health": health, "replication-offset": int64(100)}
}

// shardsReply has the shape CLUSTER SHARDS returned on Valkey 9.1.3 in RESP3
// (captured from the local test cluster).
func shardsReply(primary [3]string) []any {
	return []any{
		map[string]any{"id": "s3", "slots": []any{int64(10923), int64(16383)}, "nodes": []any{
			node("n3", "127.0.0.1", 7003, role(primary[2], "7003"), "online"), node("n6", "127.0.0.1", 7006, role(primary[2], "7006"), "online")}},
		map[string]any{"id": "s2", "slots": []any{int64(5461), int64(10922)}, "nodes": []any{
			node("n2", "127.0.0.1", 7002, role(primary[1], "7002"), "online"), node("n5", "127.0.0.1", 7005, role(primary[1], "7005"), "online")}},
		map[string]any{"id": "s1", "slots": []any{int64(0), int64(5460)}, "nodes": []any{
			node("n1", "127.0.0.1", 7001, role(primary[0], "7001"), "online"), node("n4", "127.0.0.1", 7004, role(primary[0], "7004"), "online")}},
	}
}

func role(primary, port string) string {
	if primary == port {
		return "master"
	}
	return "replica"
}

var base = [3]string{"7001", "7002", "7003"}

func snap(t *testing.T, primaries [3]string, ok bool) Snapshot {
	t.Helper()
	sh, err := ParseShards(shardsReply(primaries))
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Shards: sh, StateOK: ok, SlotsAssigned: 16384}
}

func TestParseShards(t *testing.T) {
	s := snap(t, base, true)
	if len(s.Shards) != 3 || len(s.Nodes()) != 6 || len(s.Primaries()) != 3 {
		t.Fatalf("%+v", s)
	}
	var total int
	for _, sh := range s.Shards {
		total += sh.SlotCount()
		p, ok := sh.Primary()
		if !ok || p.Role != "primary" || !p.Online() {
			t.Fatalf("primary %+v", p)
		}
	}
	if total != 16384 {
		t.Fatalf("slots %d", total)
	}
	if s.Primaries()[0].Addr != "127.0.0.1:7001" {
		t.Fatalf("%v", s.Primaries())
	}
	if _, err := ParseShards("nope"); err == nil {
		t.Fatal("a non-array reply must be an error")
	}
}

func TestParseShardsRESP2FlatArrays(t *testing.T) {
	flat := []any{[]any{"slots", []any{int64(0), int64(100)}, "nodes", []any{
		[]any{"id", "a", "port", int64(7001), "ip", "10.0.0.1", "endpoint", "10.0.0.1", "role", "master", "health", "online", "replication-offset", int64(5)}}}}
	sh, err := ParseShards(flat)
	if err != nil || len(sh) != 1 || sh[0].Nodes[0].Addr != "10.0.0.1:7001" || sh[0].Nodes[0].Role != "primary" {
		t.Fatalf("%v %+v", err, sh)
	}
	// "?" endpoint falls back to the ip.
	odd := []any{map[string]any{"slots": []any{}, "nodes": []any{map[string]any{"endpoint": "?", "ip": "1.2.3.4", "port": int64(1), "role": "replica", "health": "online"}}}}
	sh, _ = ParseShards(odd)
	if sh[0].Nodes[0].Addr != "1.2.3.4:1" {
		t.Fatalf("%+v", sh)
	}
}

func TestParseClusterInfo(t *testing.T) {
	ok, n := ParseClusterInfo("cluster_state:ok\r\ncluster_slots_assigned:16384\r\n")
	if !ok || n != 16384 {
		t.Fatal(ok, n)
	}
	if ok, _ := ParseClusterInfo("cluster_state:fail\n"); ok {
		t.Fatal("fail is not ok")
	}
}

func kinds(ev []Event) string {
	var k []string
	for _, e := range ev {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestDiffFailover(t *testing.T) {
	ev := Diff(snap(t, base, true), snap(t, [3]string{"7004", "7002", "7003"}, true))
	if kinds(ev) != "failover" || !strings.Contains(ev[0].Detail, "from 127.0.0.1:7001 to 127.0.0.1:7004") {
		t.Fatalf("%+v", ev)
	}
	if len(Diff(snap(t, base, true), snap(t, base, true))) != 0 {
		t.Fatal("no change, no events")
	}
}

func TestDiffReshard(t *testing.T) {
	prev := snap(t, base, true)
	cur := snap(t, base, true)
	cur.Shards[2].Slots = [][2]int{{0, 4999}} // shard 1 gave up slots 5000-5460
	ev := Diff(prev, cur)
	if kinds(ev) != "reshard" || !strings.Contains(ev[0].Detail, "0-4999") {
		t.Fatalf("%+v", ev)
	}
}

func TestDiffNodeDownUpAndState(t *testing.T) {
	prev := snap(t, base, true)
	cur := snap(t, base, false)
	cur.Shards[0].Nodes[1].Health = "failed"
	ev := Diff(prev, cur)
	if kinds(ev) != "cluster-state,node-down" {
		t.Fatalf("%+v", ev)
	}
	back := Diff(cur, snap(t, base, true))
	if kinds(back) != "cluster-state,node-up" || !strings.Contains(back[0].Detail, "healthy again") {
		t.Fatalf("%+v", back)
	}
	gone := snap(t, base, true)
	gone.Shards[1].Nodes = gone.Shards[1].Nodes[:1]
	if ev := Diff(prev, gone); kinds(ev) != "node-down" || !strings.Contains(ev[0].Detail, "left the cluster view") {
		t.Fatalf("%+v", ev)
	}
}

func TestRouterMapsKeysToTheOwningPrimary(t *testing.T) {
	reg := NewRegistry()
	s := snap(t, base, true)
	for _, n := range s.Primaries() {
		reg.Index(n.Addr)
	}
	r := NewSlotRouter(reg, s)
	addrs := reg.Addrs()
	// Slots from CLUSTER KEYSLOT: foo 12182 (shard 3), bar 5061 (shard 1), hello 866 (shard 1)... see ranges.
	for key, want := range map[string]string{"foo": "127.0.0.1:7003", "bar": "127.0.0.1:7001", "hello": "127.0.0.1:7001", "{x}1": addrFor(t, "{x}1")} {
		idx := r.NodeIndex(key)
		if idx < 0 || addrs[idx] != want {
			t.Errorf("%q -> %d (%v), want %s", key, idx, addrs, want)
		}
	}
	if r.NodeIndex("") != -1 {
		t.Fatal("no key, no node")
	}
	// After a failover the same key routes to the new primary.
	r.Update(snap(t, [3]string{"7004", "7002", "7003"}, true))
	idx := r.NodeIndex("bar")
	if a := reg.Addrs(); a[idx] != "127.0.0.1:7004" {
		t.Fatalf("after failover: %v", a[idx])
	}
}

// addrFor works out which base-shard primary owns a key from the slot ranges.
func addrFor(t *testing.T, key string) string {
	s := valkey.Slot(key)
	switch {
	case s <= 5460:
		return "127.0.0.1:7001"
	case s <= 10922:
		return "127.0.0.1:7002"
	}
	return "127.0.0.1:7003"
}

func TestRegistryIsStableAndBounded(t *testing.T) {
	r := NewRegistry()
	if r.Index("a") != 0 || r.Index("b") != 1 || r.Index("a") != 0 {
		t.Fatal("indexes must be stable")
	}
	for i := 0; i < MaxNodes+5; i++ {
		r.Index(string(rune('A'+i%26)) + string(rune('a'+i/26)))
	}
	if len(r.Addrs()) != MaxNodes {
		t.Fatalf("%d addresses", len(r.Addrs()))
	}
	if r.Index("brand-new") != -1 {
		t.Fatal("a full registry returns -1")
	}
}

// fakeSource serves scripted replies and can fail the default node.
type fakeSource struct {
	mu        sync.Mutex
	primaries [3]string
	ok        bool
	failMain  bool
	failAll   bool
	nodeCalls int
}

func (f *fakeSource) reply(c valkey.Cmd) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAll {
		return nil, errors.New("connection refused")
	}
	if c[1] == "SHARDS" {
		return shardsReply(f.primaries), nil
	}
	state := "ok"
	if !f.ok {
		state = "fail"
	}
	return "cluster_state:" + state + "\ncluster_slots_assigned:16384\n", nil
}

func (f *fakeSource) Query(_ context.Context, c valkey.Cmd) (any, error) {
	f.mu.Lock()
	fail := f.failMain
	f.mu.Unlock()
	if fail {
		return nil, errors.New("dead node")
	}
	return f.reply(c)
}
func (f *fakeSource) Nodes() []string { return []string{"127.0.0.1:7001", "127.0.0.1:7002"} }
func (f *fakeSource) QueryNode(_ context.Context, _ string, c valkey.Cmd) (any, error) {
	f.mu.Lock()
	f.nodeCalls++
	f.mu.Unlock()
	return f.reply(c)
}

func TestReadFallsBackToAnotherNode(t *testing.T) {
	f := &fakeSource{primaries: base, ok: true, failMain: true}
	s, err := Read(context.Background(), f)
	if err != nil || len(s.Shards) != 3 || f.nodeCalls == 0 {
		t.Fatalf("%v %+v calls=%d", err, s, f.nodeCalls)
	}
	f.failAll = true
	if _, err := Read(context.Background(), f); err == nil {
		t.Fatal("no node answered: error")
	}
}

func TestWatcherRecordsAFailoverWithItsOffset(t *testing.T) {
	f := &fakeSource{primaries: base, ok: true}
	reg := NewRegistry()
	w, err := NewWatcher(context.Background(), f, 20*time.Millisecond, reg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := time.Now()
	go func() { w.Run(ctx, started); close(done) }()
	time.Sleep(60 * time.Millisecond)
	f.mu.Lock()
	f.primaries = [3]string{"7004", "7002", "7003"} // CLUSTER FAILOVER on a replica
	f.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	f.mu.Lock()
	f.failAll = true // the cluster cannot be read for a while
	f.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	res := w.Result()
	if kinds(res.Events) != "failover" {
		t.Fatalf("events %+v", res.Events)
	}
	if off := res.Events[0].Offset; off < 50*time.Millisecond || off > 400*time.Millisecond {
		t.Fatalf("offset %v should be measured from the run start", off)
	}
	if res.PollErrors == 0 || res.FirstError == "" {
		t.Fatal("poll failures must be counted")
	}
	if res.ClusterDown {
		t.Fatal("cluster_state stayed ok")
	}
	addrs := reg.Addrs()
	if idx := w.Router().NodeIndex("bar"); addrs[idx] != "127.0.0.1:7004" {
		t.Fatalf("the router must follow the failover: %v", addrs[idx])
	}
	if len(w.Events()) != 1 {
		t.Fatal("live events")
	}
}

func TestWatcherNotesAnUnhealthyCluster(t *testing.T) {
	f := &fakeSource{primaries: base, ok: false}
	w, err := NewWatcher(context.Background(), f, 10*time.Millisecond, NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Result().ClusterDown {
		t.Fatal("a cluster that starts unhealthy must be flagged")
	}
	if _, err := NewWatcher(context.Background(), &fakeSource{failAll: true}, time.Second, NewRegistry()); err == nil {
		t.Fatal("an unreadable cluster is an error")
	}
}

type fakeSentinel struct {
	mu   sync.Mutex
	addr []any
	err  error
	n    int
}

func (f *fakeSentinel) Query(_ context.Context, c valkey.Cmd) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if c[0] != "SENTINEL" || c[1] != "GET-MASTER-ADDR-BY-NAME" || c[2] != "mymaster" {
		return nil, errors.New("unexpected " + strings.Join(c, " "))
	}
	return f.addr, f.err
}
func (f *fakeSentinel) Close() {}
func (f *fakeSentinel) set(ip string, port string, err error) {
	f.mu.Lock()
	f.addr, f.err = []any{ip, port}, err
	f.mu.Unlock()
}

func TestSentinelWatcherSeesTheMasterMove(t *testing.T) {
	dead := &fakeSentinel{err: errors.New("connection refused")}
	live := &fakeSentinel{}
	live.set("127.0.0.1", "6380", nil)
	w, err := NewSentinelWatcherFrom(context.Background(), []Asker{dead, live}, "mymaster", 15*time.Millisecond)
	if err != nil || w.Master() != "127.0.0.1:6380" {
		t.Fatalf("%v %q (the dead sentinel must be skipped)", err, w.Master())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, time.Now()); close(done) }()
	time.Sleep(40 * time.Millisecond)
	live.set("127.0.0.1", "6381", nil) // SENTINEL FAILOVER promoted the replica
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	master, ev, _, _ := w.Result()
	if master != "127.0.0.1:6381" || len(ev) != 1 || ev[0].Kind != KindFailover || !strings.Contains(ev[0].Detail, "from 127.0.0.1:6380 to 127.0.0.1:6381") {
		t.Fatalf("%q %+v", master, ev)
	}
	if ev[0].Offset < 30*time.Millisecond {
		t.Fatalf("offset %v", ev[0].Offset)
	}
	if len(w.Events()) != 1 {
		t.Fatal("events")
	}
}

func TestSentinelWatcherNeedsAnAnsweringSentinelAndKnownMaster(t *testing.T) {
	if _, err := NewSentinelWatcherFrom(context.Background(), []Asker{&fakeSentinel{err: errors.New("down")}}, "mymaster", time.Second); err == nil {
		t.Fatal("no sentinel answers: error")
	}
	unknown := &fakeSentinel{} // nil reply = unknown master
	if _, err := NewSentinelWatcherFrom(context.Background(), []Asker{unknown}, "mymaster", time.Second); err == nil || !strings.Contains(err.Error(), "mymaster") {
		t.Fatalf("%v", err)
	}
	if _, err := NewSentinelWatcher(context.Background(), []string{"127.0.0.1:1"}, "m", "", "", time.Second); err == nil {
		t.Fatal("an unreachable sentinel is an error")
	}
}

func TestSentinelWatcherCountsFailedPolls(t *testing.T) {
	s := &fakeSentinel{}
	s.set("h", "1", nil)
	w, _ := NewSentinelWatcherFrom(context.Background(), []Asker{s}, "mymaster", time.Hour)
	s.set("h", "1", errors.New("timeout"))
	w.poll(context.Background())
	_, _, errs, first := w.Result()
	if errs != 1 || first != "timeout" {
		t.Fatalf("%d %q", errs, first)
	}
}
