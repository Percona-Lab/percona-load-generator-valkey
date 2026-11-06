package valkey

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"math"
	"sort"
	"sync/atomic"
	"time"
)

// This file is the single execute path of the load generator. Workers reach
// the server only through a Client, and Tapped is the one place that observes
// what they send: the recorder (phase 9) is a Tap, so a recording holds exactly
// the commands that were sent. TestWorkersOnlyUseTheClientInterface keeps
// the other files from reaching a client library directly.

type workerKey struct{}

// WithWorker marks ctx as belonging to worker w, so a Tap can attribute the
// commands sent with it. Commands sent with another context (preload, server
// polling, the key-space scan) are not tapped.
func WithWorker(ctx context.Context, w int) context.Context {
	return context.WithValue(ctx, workerKey{}, w)
}

// WorkerOf returns the worker ctx was marked with.
func WorkerOf(ctx context.Context) (int, bool) {
	w, ok := ctx.Value(workerKey{}).(int)
	return w, ok
}

// Event is one batch of commands as a worker sent it.
type Event struct {
	Worker int
	// Group numbers the worker's batches, from 1. Every command sent in one
	// Exec call, or inside one Dedicated call, shares a group.
	Group     uint64
	Dedicated bool
	Cmds      []Cmd
	Results   []Result
	Start     time.Time
	Elapsed   time.Duration
}

// Tap receives every batch a worker sends, after it completed.
type Tap func(Event)

// Tapped wraps c so tap sees every command a worker sends. workers bounds the
// worker ids that are tapped. capture makes Query replies carry a checksum
// (Exec replies carry one when the client was created with CaptureReplies).
func Tapped(c Client, workers int, capture bool, tap Tap) Client {
	return &tapClient{Client: c, tap: tap, capture: capture, groups: make([]atomic.Uint64, workers)}
}

type tapClient struct {
	Client
	tap     Tap
	capture bool
	groups  []atomic.Uint64
}

func (t *tapClient) worker(ctx context.Context) (int, bool) {
	w, ok := WorkerOf(ctx)
	return w, ok && w >= 0 && w < len(t.groups)
}

func (t *tapClient) Exec(ctx context.Context, cmds []Cmd) []Result {
	w, ok := t.worker(ctx)
	if !ok {
		return t.Client.Exec(ctx, cmds)
	}
	start := time.Now()
	res := t.Client.Exec(ctx, cmds)
	if ctx.Err() == nil {
		t.tap(Event{Worker: w, Group: t.groups[w].Add(1), Cmds: cmds, Results: res, Start: start, Elapsed: time.Since(start)})
	}
	return res
}

func (t *tapClient) Query(ctx context.Context, cmd Cmd) (any, error) {
	w, ok := t.worker(ctx)
	if !ok {
		return t.Client.Query(ctx, cmd)
	}
	start := time.Now()
	v, err := t.Client.Query(ctx, cmd)
	if ctx.Err() == nil {
		t.tap(Event{Worker: w, Group: t.groups[w].Add(1), Cmds: []Cmd{cmd}, Results: []Result{t.queryResult(v, err)}, Start: start, Elapsed: time.Since(start)})
	}
	return v, err
}

func (t *tapClient) queryResult(v any, err error) Result {
	switch {
	case err != nil:
		return Result{Err: err}
	case v == nil:
		return Result{Miss: true}
	case t.capture:
		n, sum := Digest(v)
		return Result{Len: n, Sum: sum}
	}
	return Result{}
}

func (t *tapClient) Dedicated(ctx context.Context, fn func(Conn) error) error {
	w, ok := t.worker(ctx)
	if !ok {
		return t.Client.Dedicated(ctx, fn)
	}
	group := t.groups[w].Add(1)
	return t.Client.Dedicated(ctx, func(c Conn) error { return fn(&tapConn{Conn: c, t: t, w: w, group: group}) })
}

type tapConn struct {
	Conn
	t     *tapClient
	w     int
	group uint64
}

func (c *tapConn) Exec(ctx context.Context, cmds []Cmd) []Result {
	start := time.Now()
	res := c.Conn.Exec(ctx, cmds)
	if ctx.Err() == nil {
		c.t.tap(Event{Worker: c.w, Group: c.group, Dedicated: true, Cmds: cmds, Results: res, Start: start, Elapsed: time.Since(start)})
	}
	return res
}

func (c *tapConn) Query(ctx context.Context, cmd Cmd) (any, error) {
	start := time.Now()
	v, err := c.Conn.Query(ctx, cmd)
	if ctx.Err() == nil {
		c.t.tap(Event{Worker: c.w, Group: c.group, Dedicated: true, Cmds: []Cmd{cmd}, Results: []Result{c.t.queryResult(v, err)}, Start: start, Elapsed: time.Since(start)})
	}
	return v, err
}

// Digest summarizes a reply (as Query returns it) for checksums: its size (the
// bytes of a string, the elements of a collection, 1 otherwise) and an FNV-64a
// hash of its content. Map keys are hashed in sorted order, so the same reply
// always gives the same sum.
func Digest(v any) (int, uint64) {
	h := fnv.New64a()
	n := digestInto(h, v)
	return n, h.Sum64()
}

type byteWriter interface{ Write([]byte) (int, error) }

func digestInto(h byteWriter, v any) int {
	var tmp [9]byte
	switch x := v.(type) {
	case nil:
		h.Write([]byte{'n'})
		return 0
	case string:
		tmp[0] = 's'
		binary.LittleEndian.PutUint64(tmp[1:], uint64(len(x)))
		h.Write(tmp[:])
		h.Write([]byte(x))
		return len(x)
	case int64:
		tmp[0] = 'i'
		binary.LittleEndian.PutUint64(tmp[1:], uint64(x))
		h.Write(tmp[:])
		return 1
	case float64:
		tmp[0] = 'f'
		binary.LittleEndian.PutUint64(tmp[1:], math.Float64bits(x))
		h.Write(tmp[:])
		return 1
	case bool:
		if x {
			h.Write([]byte{'T'})
		} else {
			h.Write([]byte{'F'})
		}
		return 1
	case []any:
		tmp[0] = 'a'
		binary.LittleEndian.PutUint64(tmp[1:], uint64(len(x)))
		h.Write(tmp[:])
		for _, e := range x {
			digestInto(h, e)
		}
		return len(x)
	case map[string]any:
		tmp[0] = 'm'
		binary.LittleEndian.PutUint64(tmp[1:], uint64(len(x)))
		h.Write(tmp[:])
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			digestInto(h, k)
			digestInto(h, x[k])
		}
		return len(x)
	}
	h.Write([]byte{'?'})
	return 1
}
