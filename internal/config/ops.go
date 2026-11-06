package config

import (
	"sort"
	"strings"
)

// Key namespace type names accepted by the `type` mix parameter of key ops.
const (
	TypeString = "string"
	TypeHash   = "hash"
	TypeList   = "list"
	TypeSet    = "set"
	TypeZSet   = "zset"
	TypeStream = "stream"
	TypeGeo    = "geo"
	TypeHLL    = "hll"
	TypeBitmap = "bitmap"
)

// AllTypes lists every key type, in preload order.
var AllTypes = []string{TypeString, TypeHash, TypeList, TypeSet, TypeZSet, TypeStream, TypeGeo, TypeHLL, TypeBitmap}

// Kinds of operation: how the runner executes them.
const (
	KindSimple    = "simple"    // one command, pipelined with the rest of the batch
	KindSolo      = "solo"      // one command sent on its own (blocking commands)
	KindComposite = "composite" // several steps run as one unit (transactions, read+ack, ...)
)

// OpInfo describes a supported operation and the mix parameters it accepts.
type OpInfo struct {
	Family  string // string, key, hash, list, set, zset, stream, geo, hll, bitmap, script, pubsub, txn, raw
	Kind    string // KindSimple (default), KindSolo or KindComposite
	Count   bool   // accepts `count`
	Fields  bool   // accepts `fields`
	Type    bool   // accepts `type` (key ops)
	Timeout bool   // accepts `timeout` (blocking ops)
	Script  bool   // accepts `script`, `source`, `args` (Lua and functions)
	Steps   bool   // accepts `steps` (transactions, pipeline groups)
	Command bool   // accepts `command` (raw injector)
}

// Ops is the catalog of operations supported in this version. The valkey
// package must provide a builder for each entry (checked by a test).
var Ops = map[string]OpInfo{
	// strings
	"GET": {Family: "string"}, "SET": {Family: "string"},
	"MGET": {Family: "string", Count: true}, "MSET": {Family: "string", Count: true},
	"INCR": {Family: "string"}, "APPEND": {Family: "string"},
	"GETEX": {Family: "string"}, "SETEX": {Family: "string"},
	// keys
	"DEL": {Family: "key", Type: true}, "UNLINK": {Family: "key", Type: true},
	"EXISTS": {Family: "key", Type: true}, "TTL": {Family: "key", Type: true},
	"EXPIRE": {Family: "key", Type: true}, "SCAN": {Family: "key", Type: true, Count: true},
	// hashes
	"HSET": {Family: "hash", Fields: true}, "HGET": {Family: "hash"},
	"HGETALL": {Family: "hash"}, "HDEL": {Family: "hash"}, "HINCRBY": {Family: "hash"},
	// lists
	"LPUSH": {Family: "list"}, "RPUSH": {Family: "list"}, "LPOP": {Family: "list"},
	"RPOP": {Family: "list"}, "LRANGE": {Family: "list", Count: true}, "LLEN": {Family: "list"},
	// sets
	"SADD": {Family: "set"}, "SREM": {Family: "set"}, "SISMEMBER": {Family: "set"},
	"SCARD": {Family: "set"}, "SRANDMEMBER": {Family: "set", Count: true}, "SMEMBERS": {Family: "set"},
	// sorted sets
	"ZADD": {Family: "zset"}, "ZREM": {Family: "zset"}, "ZSCORE": {Family: "zset"},
	"ZINCRBY": {Family: "zset"}, "ZRANK": {Family: "zset"}, "ZRANGE": {Family: "zset", Count: true},
	"ZRANGEBYSCORE": {Family: "zset", Count: true}, "ZCARD": {Family: "zset"},
	// blocking lists (sent on their own; they wait up to `timeout` for an element)
	"BLPOP": {Family: "list", Kind: KindSolo, Timeout: true}, "BRPOP": {Family: "list", Kind: KindSolo, Timeout: true},
	// streams
	"XADD": {Family: "stream", Fields: true}, "XLEN": {Family: "stream"}, "XRANGE": {Family: "stream", Count: true},
	"XREAD": {Family: "stream", Count: true}, "XREAD_BLOCK": {Family: "stream", Kind: KindSolo, Count: true, Timeout: true},
	"XTRIM": {Family: "stream"}, "XPENDING": {Family: "stream"},
	"XREADGROUP": {Family: "stream", Count: true},
	"XCONSUME":   {Family: "stream", Kind: KindComposite, Count: true}, // XREADGROUP then XACK of what it returned
	// geo
	"GEOADD": {Family: "geo"}, "GEOPOS": {Family: "geo"}, "GEODIST": {Family: "geo"}, "GEOSEARCH": {Family: "geo", Count: true},
	// HyperLogLog
	"PFADD": {Family: "hll"}, "PFCOUNT": {Family: "hll"},
	// bitmaps
	"SETBIT": {Family: "bitmap"}, "GETBIT": {Family: "bitmap"}, "BITCOUNT": {Family: "bitmap"},
	// Lua and functions
	"EVAL": {Family: "script", Script: true}, "EVALSHA": {Family: "script", Script: true}, "FCALL": {Family: "script", Script: true},
	// pub/sub
	"PUBLISH": {Family: "pubsub"}, "SPUBLISH": {Family: "pubsub"},
	// transactions and pipeline groups (their steps are ordinary operations)
	"TXN":        {Family: "txn", Kind: KindComposite, Steps: true},
	"PIPELINE":   {Family: "txn", Kind: KindComposite, Steps: true},
	"WATCH_INCR": {Family: "txn", Kind: KindComposite},
	// raw injector: any command, written as text with placeholders
	"RAW": {Family: "raw", Command: true},
}

// Builtin Lua scripts and functions (see internal/valkey/scripts.go).
var BuiltinScripts = []string{"rate_limit", "incr_cap", "get_set"}

// OpNames returns the supported operation names, sorted.
func OpNames() []string {
	out := make([]string, 0, len(Ops))
	for n := range Ops {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// UsedTypes returns the key types a mix reads or writes, which is what
// preload must populate. Counter and append keys are created on demand.
func UsedTypes(mix []MixItem) map[string]bool {
	used := map[string]bool{}
	for _, it := range mix {
		info, ok := Ops[it.Op]
		if !ok {
			continue
		}
		switch info.Family {
		case "key":
			t := it.Type
			if t == "" {
				t = TypeString
			}
			used[t] = true
		case "string":
			if it.Op != "INCR" && it.Op != "APPEND" {
				used[TypeString] = true
			}
		case "script", "pubsub", "txn":
			// no data of their own; a transaction or pipeline group uses its steps' types
			for t := range UsedTypes(it.Steps) {
				used[t] = true
			}
		case "raw":
			if strings.Contains(it.Command, "{key") {
				used[TypeString] = true
			}
		default:
			used[info.Family] = true
		}
	}
	return used
}

// KindOf returns how an operation is executed.
func KindOf(op string) string {
	if k := Ops[op].Kind; k != "" {
		return k
	}
	return KindSimple
}
