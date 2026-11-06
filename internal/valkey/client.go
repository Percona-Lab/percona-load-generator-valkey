// Package valkey holds the client abstraction and the workload runner. All
// client-library use stays in this file so the library is swappable.
package valkey

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
	vk "github.com/valkey-io/valkey-go"
)

// Client library names accepted by NewClient.
const (
	LibValkeyGo = "valkey-go"
	LibGoRedis  = "go-redis"
)

// Cmd is one command: the command name followed by its arguments.
type Cmd []string

// Result is the outcome of one command in a batch. A nil reply (for example a
// GET on a missing key) is a Miss, not an error.
type Result struct {
	Err  error
	Miss bool
	// Len and Sum describe the reply (its size and a checksum). They are set
	// only when the client was created with Options.CaptureReplies.
	Len int
	Sum uint64
}

// Client executes batches of commands. A batch of more than one command is
// pipelined. Implementations are safe for concurrent use.
type Client interface {
	Exec(ctx context.Context, cmds []Cmd) []Result
	// Query runs one command and returns its reply as plain Go values: string,
	// int64, float64, bool, []any, map[string]any, or nil. It is for admin
	// commands such as INFO and SLOWLOG, not for load.
	Query(ctx context.Context, cmd Cmd) (any, error)
	// Dedicated runs fn with one connection that no other goroutine uses, for
	// commands whose effect depends on the connection (WATCH).
	Dedicated(ctx context.Context, fn func(Conn) error) error
	// Subscribe receives messages published to channels (sharded: shard
	// channels) and calls onMessage for each, until ctx ends. onReady is called
	// once, when the server has confirmed every channel: from then on a
	// published message reaches this subscriber. It must not block.
	Subscribe(ctx context.Context, sharded bool, channels []string, onMessage func(channel, payload string), onReady func()) error
	Close()
}

// Conn is a single dedicated connection.
type Conn interface {
	Query(ctx context.Context, cmd Cmd) (any, error)
	Exec(ctx context.Context, cmds []Cmd) []Result
}

// NodeQuerier is implemented by clients that can talk to one specific node of a
// cluster (needed to read INFO and topology from every node).
type NodeQuerier interface {
	// Nodes lists the addresses of the nodes the client currently knows.
	Nodes() []string
	// QueryNode runs one command on the node with the given address.
	QueryNode(ctx context.Context, addr string, cmd Cmd) (any, error)
}

// Options configure a client.
type Options struct {
	Addr string // single server (standalone)
	// Addrs are seed nodes (cluster) or sentinel addresses (sentinel). When set
	// it is used instead of Addr.
	Addrs    []string
	Username string
	Password string
	// SentinelMaster, when set, connects through sentinels (Addrs) and follows
	// the named master set.
	SentinelMaster string
	// ReadFromReplicas sends read-only commands to replicas (cluster, sentinel).
	ReadFromReplicas bool
	// Workers is the number of goroutines that will share the client. It sizes
	// the connection pool for libraries that need one.
	Workers int
	// CaptureReplies makes Exec fill Result.Len and Result.Sum, which a
	// recording with result checksums needs. It costs a pass over every reply,
	// so it is off unless asked for.
	CaptureReplies bool
	// TLS, when set, encrypts every connection (valkey-go also uses it for
	// sentinels and cluster nodes).
	TLS *tls.Config
}

// NewClient creates a standalone client using the named library.
func NewClient(lib string, o Options) (Client, error) {
	addrs := o.Addrs
	if len(addrs) == 0 && o.Addr != "" {
		addrs = []string{o.Addr}
	}
	if len(addrs) == 0 {
		return nil, errors.New("valkey: address is required")
	}
	if o.Addr == "" {
		o.Addr = addrs[0]
	}
	switch lib {
	case "", LibValkeyGo:
		opt := vk.ClientOption{
			InitAddress:  addrs,
			Username:     o.Username,
			Password:     o.Password,
			DisableCache: true,
			TLSConfig:    o.TLS,
		}
		if o.SentinelMaster != "" {
			opt.Sentinel = vk.SentinelOption{MasterSet: o.SentinelMaster, Username: o.Username, Password: o.Password}
		}
		if o.ReadFromReplicas {
			// Read-only commands may go to a replica; writes always go to the primary.
			opt.SendToReplicas = func(cmd vk.Completed) bool { return cmd.IsReadOnly() }
		}
		c, err := vk.NewClient(opt)
		if err != nil {
			return nil, err
		}
		return &valkeyGo{c: c, capture: o.CaptureReplies}, nil
	case LibGoRedis:
		if o.SentinelMaster != "" || o.ReadFromReplicas || len(addrs) > 1 {
			return nil, errors.New("valkey: go-redis supports standalone targets only in this version; use valkey-go for cluster and sentinel")
		}
		pool := o.Workers
		if pool < 1 {
			pool = 1
		}
		c := redis.NewClient(&redis.Options{
			Addr:      o.Addr,
			Username:  o.Username,
			Password:  o.Password,
			PoolSize:  pool,
			TLSConfig: o.TLS,
		})
		return &goRedis{c: c, capture: o.CaptureReplies}, nil
	default:
		return nil, fmt.Errorf("valkey: unknown client library %q (want %s or %s)", lib, LibValkeyGo, LibGoRedis)
	}
}

// --- valkey-go ---

type valkeyGo struct {
	capture     bool
	c           vk.Client
	clusterOnce sync.Once
	cluster     bool
}

func (v *valkeyGo) Close() { v.c.Close() }

func (v *valkeyGo) Exec(ctx context.Context, cmds []Cmd) []Result {
	out := make([]Result, len(cmds))
	if len(cmds) == 1 {
		out[0] = vkReply(v.capture, v.c.Do(ctx, vkBuild(v.c, cmds[0])))
		return out
	}
	// A cluster cannot take keyless commands (PUBLISH, SCAN, ...) in the same
	// pipeline as keyed commands of several slots; send the two groups apart.
	if keyed, keyless := splitKeyless(cmds); len(keyless) > 0 && v.inCluster(ctx) {
		for i, r := range v.execMulti(ctx, pick(cmds, keyed)) {
			out[keyed[i]] = r
		}
		for i, r := range v.execMulti(ctx, pick(cmds, keyless)) {
			out[keyless[i]] = r
		}
		return out
	}
	return v.execMulti(ctx, cmds)
}

func (v *valkeyGo) execMulti(ctx context.Context, cmds []Cmd) []Result {
	out := make([]Result, len(cmds))
	if len(cmds) == 0 {
		return out
	}
	built := make(vk.Commands, len(cmds))
	for i, cmd := range cmds {
		built[i] = vkBuild(v.c, cmd)
	}
	for i, r := range v.c.DoMulti(ctx, built...) {
		out[i] = vkReply(v.capture, r)
	}
	return out
}

func pick(cmds []Cmd, idx []int) []Cmd {
	out := make([]Cmd, len(idx))
	for i, j := range idx {
		out[i] = cmds[j]
	}
	return out
}

// splitKeyless returns the indexes of the keyed and keyless commands, or no
// keyless indexes when the batch needs no splitting (a single slot at most, which
// valkey-go routes whole, as for MULTI ... EXEC).
func splitKeyless(cmds []Cmd) (keyed, keyless []int) {
	slots := map[int]bool{}
	for i, c := range cmds {
		if k := RouteKey(c); k != "" {
			keyed = append(keyed, i)
			slots[Slot(k)] = true
		} else {
			keyless = append(keyless, i)
		}
	}
	if len(slots) <= 1 {
		return keyed, nil
	}
	return keyed, keyless
}

// inCluster reports (once) whether the server is a cluster node.
func (v *valkeyGo) inCluster(ctx context.Context) bool {
	v.clusterOnce.Do(func() {
		r, err := replyAny(v.c.Do(ctx, v.c.B().Arbitrary("INFO").Args("cluster").Build()))
		s, _ := r.(string)
		v.cluster = err == nil && strings.Contains(s, "cluster_enabled:1")
	})
	return v.cluster
}

func (v *valkeyGo) Query(ctx context.Context, cmd Cmd) (any, error) {
	return replyAny(v.c.Do(ctx, vkBuild(v.c, cmd)))
}

// replyAny converts a reply to plain Go values. A RESP3 "verbatim string"
// (INFO, CLUSTER INFO) arrives as "txt:<text>"; the format prefix is removed.
func replyAny(r vk.ValkeyResult) (any, error) {
	if err := r.Error(); err != nil {
		if vk.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, err
	}
	v, err := r.ToAny()
	if s, ok := v.(string); ok && len(s) >= 4 && s[3] == ':' && (s[:3] == "txt" || s[:3] == "mkd") {
		v = s[4:]
	}
	return v, err
}

// Nodes lists the nodes the client knows (all cluster nodes, or the one server).
func (v *valkeyGo) Nodes() []string {
	m := v.c.Nodes()
	out := make([]string, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// QueryNode runs a command on one node.
func (v *valkeyGo) QueryNode(ctx context.Context, addr string, cmd Cmd) (any, error) {
	nc, ok := v.c.Nodes()[addr]
	if !ok {
		return nil, fmt.Errorf("valkey: node %s is not known to the client", addr)
	}
	return replyAny(nc.Do(ctx, vkBuild(nc, cmd)))
}

// firstKeyCmds are the commands whose first argument is the key the cluster
// routes by. Admin commands (INFO, CLUSTER, SLOWLOG, ...) and SCAN have no key.
var firstKeyCmds = map[string]bool{
	"GET": true, "SET": true, "MGET": true, "MSET": true, "INCR": true, "APPEND": true, "GETEX": true, "SETEX": true,
	"DEL": true, "UNLINK": true, "EXISTS": true, "TTL": true, "EXPIRE": true,
	"HSET": true, "HGET": true, "HGETALL": true, "HDEL": true, "HINCRBY": true,
	"LPUSH": true, "RPUSH": true, "LPOP": true, "RPOP": true, "LRANGE": true, "LLEN": true, "BLPOP": true, "BRPOP": true,
	"SADD": true, "SREM": true, "SISMEMBER": true, "SCARD": true, "SRANDMEMBER": true, "SMEMBERS": true,
	"ZADD": true, "ZREM": true, "ZSCORE": true, "ZINCRBY": true, "ZRANK": true, "ZRANGE": true, "ZCARD": true,
	"XADD": true, "XLEN": true, "XRANGE": true, "XTRIM": true, "XPENDING": true, "XACK": true, "XGROUP": true,
	"GEOADD": true, "GEOPOS": true, "GEODIST": true, "GEOSEARCH": true,
	"PFADD": true, "PFCOUNT": true, "SETBIT": true, "GETBIT": true, "BITCOUNT": true,
	"SPUBLISH": true, "WATCH": true,
	// Read by the key-space analysis.
	"TYPE": true, "PTTL": true, "STRLEN": true, "HLEN": true,
	// MULTI/EXEC carry no key; they follow the keys queued between them on one connection.
}

// readOnlyCmds may be answered by a replica when read_from_replicas is on.
var readOnlyCmds = map[string]bool{
	"GET": true, "MGET": true, "EXISTS": true, "TTL": true, "HGET": true, "HGETALL": true, "LLEN": true, "LRANGE": true,
	"SISMEMBER": true, "SCARD": true, "SRANDMEMBER": true, "SMEMBERS": true, "ZSCORE": true, "ZRANK": true, "ZRANGE": true, "ZCARD": true,
	"XLEN": true, "XRANGE": true, "XPENDING": true, "GEOPOS": true, "GEODIST": true, "GEOSEARCH": true, "GETBIT": true, "BITCOUNT": true,
}

// rawKeyPos records, for commands that appear in RAW templates, which argument
// is the key, so a cluster client can route them. It is written at setup, before
// any worker starts, and only read afterwards.
var (
	rawKeyMu  sync.RWMutex
	rawKeyPos = map[string]int{}
)

// RegisterKeyPosition tells the client which argument of a command (counting
// the name as 0) is its routing key. Used by the raw injector.
func RegisterKeyPosition(command string, pos int) {
	rawKeyMu.Lock()
	rawKeyPos[strings.ToUpper(command)] = pos
	rawKeyMu.Unlock()
}

// KeyIndexes returns the argument indexes (the name is 0) that are keys.
func KeyIndexes(cmd Cmd) []int {
	if len(cmd) < 2 {
		return nil
	}
	name := cmd[0]
	switch name {
	case "MGET":
		idx := make([]int, 0, len(cmd)-1)
		for i := 1; i < len(cmd); i++ {
			idx = append(idx, i)
		}
		return idx
	case "MSET":
		var idx []int
		for i := 1; i < len(cmd); i += 2 {
			idx = append(idx, i)
		}
		return idx
	case "EVAL", "EVALSHA", "FCALL":
		if len(cmd) < 3 {
			return nil
		}
		n, err := strconv.Atoi(cmd[2])
		if err != nil {
			return nil
		}
		var idx []int
		for i := 0; i < n && 3+i < len(cmd); i++ {
			idx = append(idx, 3+i)
		}
		return idx
	case "XREAD", "XREADGROUP":
		for i, a := range cmd {
			if strings.EqualFold(a, "STREAMS") && i+1 < len(cmd) {
				return []int{i + 1}
			}
		}
		return nil
	case "SCAN":
		return nil
	case "MEMORY": // MEMORY USAGE key [SAMPLES n]
		if len(cmd) >= 3 && strings.EqualFold(cmd[1], "USAGE") {
			return []int{2}
		}
		return nil
	}
	if firstKeyCmds[name] {
		return []int{1}
	}
	rawKeyMu.RLock()
	pos, ok := rawKeyPos[name]
	rawKeyMu.RUnlock()
	if ok && pos < len(cmd) {
		return []int{pos}
	}
	return nil
}

func isBlocking(cmd Cmd) bool {
	switch cmd[0] {
	case "BLPOP", "BRPOP":
		return true
	case "XREAD", "XREADGROUP":
		for _, a := range cmd[1:] {
			if strings.EqualFold(a, "BLOCK") {
				return true
			}
		}
	}
	return false
}

// vkBuild turns a Cmd into a valkey-go command. Keys must be declared as keys
// (not plain arguments) or a cluster client cannot route the command and sends
// it to a random node, which answers with MOVED.
type vkBuilder interface{ B() vk.Builder }

func vkBuild(c vkBuilder, cmd Cmd) vk.Completed {
	a := c.B().Arbitrary(cmd[0])
	keys := map[int]bool{}
	for _, i := range KeyIndexes(cmd) {
		keys[i] = true
	}
	for i := 1; i < len(cmd); i++ {
		if keys[i] {
			a = a.Keys(cmd[i])
		} else {
			a = a.Args(cmd[i])
		}
	}
	switch {
	case isBlocking(cmd):
		return a.Blocking()
	case readOnlyCmds[cmd[0]]:
		return a.ReadOnly()
	}
	return a.Build()
}

// Dedicated runs fn on one connection used by nothing else.
func (v *valkeyGo) Dedicated(ctx context.Context, fn func(Conn) error) error {
	return v.c.Dedicated(func(dc vk.DedicatedClient) error { return fn(&vkConn{dc: dc, capture: v.capture}) })
}

type vkConn struct {
	dc      vk.DedicatedClient
	capture bool
}

func (c *vkConn) Query(ctx context.Context, cmd Cmd) (any, error) {
	return replyAny(c.dc.Do(ctx, vkBuild(c.dc, cmd)))
}

func (c *vkConn) Exec(ctx context.Context, cmds []Cmd) []Result {
	built := make(vk.Commands, len(cmds))
	for i, cmd := range cmds {
		built[i] = vkBuild(c.dc, cmd)
	}
	out := make([]Result, len(cmds))
	for i, r := range c.dc.DoMulti(ctx, built...) {
		out[i] = vkReply(c.capture, r)
	}
	return out
}

// Subscribe receives messages until ctx ends.
func (v *valkeyGo) Subscribe(ctx context.Context, sharded bool, channels []string, onMessage func(channel, payload string), onReady func()) error {
	if onReady != nil {
		var confirmed atomic.Int64
		want := int64(len(channels))
		ctx = vk.WithOnSubscriptionHook(ctx, func(s vk.PubSubSubscription) {
			if (s.Kind == "subscribe" || s.Kind == "ssubscribe") && confirmed.Add(1) == want {
				onReady()
			}
		})
	}
	var cmd vk.Completed
	if sharded {
		cmd = v.c.B().Ssubscribe().Channel(channels...).Build()
	} else {
		cmd = v.c.B().Subscribe().Channel(channels...).Build()
	}
	err := v.c.Receive(ctx, cmd, func(m vk.PubSubMessage) { onMessage(m.Channel, m.Message) })
	if ctx.Err() != nil {
		return nil // stopping is not an error
	}
	return err
}

// vkReply converts a reply; with capture it also digests the reply.
func vkReply(capture bool, r vk.ValkeyResult) Result {
	out := vkResult(r.Error())
	if capture && out.Err == nil && !out.Miss {
		if v, err := replyAny(r); err == nil {
			out.Len, out.Sum = Digest(v)
		}
	}
	return out
}

func vkResult(err error) Result {
	if err == nil {
		return Result{}
	}
	if vk.IsValkeyNil(err) {
		return Result{Miss: true}
	}
	return Result{Err: err}
}

// --- go-redis ---

type goRedis struct {
	c       *redis.Client
	capture bool
}

func (g *goRedis) Close() { _ = g.c.Close() }

func (g *goRedis) Exec(ctx context.Context, cmds []Cmd) []Result {
	out := make([]Result, len(cmds))
	if len(cmds) == 1 {
		out[0] = grReply(g.capture, g.c.Do(ctx, anyArgs(cmds[0])...))
		return out
	}
	p := g.c.Pipeline()
	rcmds := make([]*redis.Cmd, len(cmds))
	for i, cmd := range cmds {
		rcmds[i] = p.Do(ctx, anyArgs(cmd)...)
	}
	_, _ = p.Exec(ctx) // per-command errors are read from each command
	for i, rc := range rcmds {
		out[i] = grReply(g.capture, rc)
	}
	return out
}

func (g *goRedis) Query(ctx context.Context, cmd Cmd) (any, error) {
	v, err := g.c.Do(ctx, anyArgs(cmd)...).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return normalize(v), err
}

// normalize makes go-redis replies match valkey-go's shapes (string-keyed maps).
func normalize(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
		return x
	}
	return v
}

// Dedicated runs fn on one pooled connection used by nothing else.
func (g *goRedis) Dedicated(ctx context.Context, fn func(Conn) error) error {
	conn := g.c.Conn()
	defer conn.Close()
	return fn(&grConn{c: conn, capture: g.capture})
}

type grConn struct {
	c       *redis.Conn
	capture bool
}

func (c *grConn) Query(ctx context.Context, cmd Cmd) (any, error) {
	v, err := c.c.Do(ctx, anyArgs(cmd)...).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return normalize(v), err
}

func (c *grConn) Exec(ctx context.Context, cmds []Cmd) []Result {
	out := make([]Result, len(cmds))
	p := c.c.Pipeline()
	rcmds := make([]*redis.Cmd, len(cmds))
	for i, cmd := range cmds {
		rcmds[i] = p.Do(ctx, anyArgs(cmd)...)
	}
	_, _ = p.Exec(ctx)
	for i, rc := range rcmds {
		out[i] = grReply(c.capture, rc)
	}
	return out
}

// Subscribe receives messages until ctx ends. Sharded channels are not
// supported by this library path (use valkey-go).
func (g *goRedis) Subscribe(ctx context.Context, sharded bool, channels []string, onMessage func(channel, payload string), onReady func()) error {
	if sharded {
		return errors.New("valkey: sharded pub/sub needs valkey-go")
	}
	ps := g.c.Subscribe(ctx, channels...)
	defer ps.Close()
	// Receive returns the first confirmation; the others follow on the same connection.
	for range channels {
		if _, err := ps.Receive(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	if onReady != nil {
		onReady()
	}
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			onMessage(m.Channel, m.Payload)
		}
	}
}

func anyArgs(cmd Cmd) []any {
	a := make([]any, len(cmd))
	for i, s := range cmd {
		a[i] = s
	}
	return a
}

// grReply converts a reply; with capture it also digests the reply.
func grReply(capture bool, rc *redis.Cmd) Result {
	out := grResult(rc.Err())
	if capture && out.Err == nil && !out.Miss {
		if v, err := rc.Result(); err == nil {
			out.Len, out.Sum = Digest(normalize(v))
		}
	}
	return out
}

func grResult(err error) Result {
	if err == nil {
		return Result{}
	}
	if errors.Is(err, redis.Nil) {
		return Result{Miss: true}
	}
	return Result{Err: err}
}
