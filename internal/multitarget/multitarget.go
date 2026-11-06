// Package multitarget runs one job against several servers, one after another
// or all at once, and keeps going when one of them fails. It knows nothing
// about replays: the caller passes the job.
package multitarget

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MaxTargets bounds how many servers one run may use.
const MaxTargets = 8

// Target is one server. Addr may list several comma-separated seed nodes.
type Target struct {
	Name string
	Addr string
}

// Parse reads "name=host:port" or "host:port" entries. Names default to the
// address and must be unique.
func Parse(specs []string) ([]Target, error) {
	var out []Target
	seen := map[string]bool{}
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t := Target{Addr: s, Name: s}
		if i := strings.Index(s, "="); i > 0 {
			t.Name, t.Addr = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
		}
		if t.Addr == "" {
			return nil, fmt.Errorf("target %q has no address", s)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("target name %q is used twice; give each target a name with name=host:port", t.Name)
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	if len(out) > MaxTargets {
		return nil, fmt.Errorf("%d targets: at most %d are supported", len(out), MaxTargets)
	}
	return out, nil
}

// Outcome is what one target's job returned.
type Outcome[T any] struct {
	Target Target
	Value  T
	Err    error
}

// Run calls job for every target, in order when concurrent is false, together
// when it is true. A failing target does not stop the others; every target
// gets an Outcome, in the order of targets.
func Run[T any](ctx context.Context, targets []Target, concurrent bool, job func(context.Context, Target) (T, error)) []Outcome[T] {
	out := make([]Outcome[T], len(targets))
	if !concurrent {
		for i, t := range targets {
			out[i].Target = t
			if err := ctx.Err(); err != nil {
				out[i].Err = err
				continue
			}
			out[i].Value, out[i].Err = job(ctx, t)
		}
		return out
	}
	var wg sync.WaitGroup
	for i, t := range targets {
		out[i].Target = t
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			out[i].Value, out[i].Err = job(ctx, t)
		}(i, t)
	}
	wg.Wait()
	return out
}
