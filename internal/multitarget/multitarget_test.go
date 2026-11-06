package multitarget

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	ts, err := Parse([]string{"a=10.0.0.1:6379", " 10.0.0.2:6379 ", "", "c=h1:1,h2:2"})
	if err != nil || len(ts) != 3 {
		t.Fatalf("%v %v", ts, err)
	}
	if ts[0] != (Target{"a", "10.0.0.1:6379"}) || ts[1] != (Target{"10.0.0.2:6379", "10.0.0.2:6379"}) || ts[2].Addr != "h1:1,h2:2" {
		t.Fatalf("%+v", ts)
	}
	if _, err := Parse([]string{"x=a:1", "x=b:1"}); err == nil {
		t.Fatal("duplicate names")
	}
	if _, err := Parse([]string{"x="}); err == nil {
		t.Fatal("no address")
	}
	many := make([]string, MaxTargets+1)
	for i := range many {
		many[i] = string(rune('a'+i)) + ":1"
	}
	if _, err := Parse(many); err == nil {
		t.Fatal("too many")
	}
}

func TestSequentialRunsInOrderAndKeepsGoingAfterAFailure(t *testing.T) {
	ts := []Target{{"a", "a"}, {"b", "b"}, {"c", "c"}}
	var order []string
	out := Run(context.Background(), ts, false, func(_ context.Context, t Target) (int, error) {
		order = append(order, t.Name)
		if t.Name == "b" {
			return 0, errors.New("down")
		}
		return len(order), nil
	})
	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Fatalf("order = %v", order)
	}
	if out[0].Value != 1 || out[1].Err == nil || out[2].Value != 3 || out[2].Err != nil {
		t.Fatalf("%+v", out)
	}
}

func TestConcurrentRunsTogether(t *testing.T) {
	ts := []Target{{"a", "a"}, {"b", "b"}, {"c", "c"}}
	var running, peak atomic.Int32
	out := Run(context.Background(), ts, true, func(_ context.Context, t Target) (string, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return t.Name, nil
	})
	if peak.Load() != 3 {
		t.Fatalf("peak concurrency = %d, want 3", peak.Load())
	}
	for i, o := range out {
		if o.Value != ts[i].Name || o.Target != ts[i] {
			t.Fatalf("outcomes must keep the target order: %+v", out)
		}
	}
}

func TestSequentialStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := Run(ctx, []Target{{"a", "a"}, {"b", "b"}}, false, func(context.Context, Target) (int, error) {
		cancel()
		return 1, nil
	})
	if out[0].Err != nil || !errors.Is(out[1].Err, context.Canceled) {
		t.Fatalf("%+v", out)
	}
}
