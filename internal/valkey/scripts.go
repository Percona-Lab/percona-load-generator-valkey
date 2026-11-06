package valkey

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

// Built-in Lua scripts. KEYS[1] is a key; the arguments have defaults (see
// scriptArgs) and can be overridden with the mix item's `args`.
var scriptBodies = map[string]string{
	// Fixed-window rate limiter: allow up to ARGV[2] calls per ARGV[1] seconds.
	"rate_limit": `local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[1])) end
if n > tonumber(ARGV[2]) then return 0 end
return 1`,
	// Increment a counter, but never above a cap (ARGV[1]).
	"incr_cap": `local v = tonumber(redis.call('GET', KEYS[1]) or '0')
if v < tonumber(ARGV[1]) then return redis.call('INCR', KEYS[1]) end
return v`,
	// Set a value and return the old one, atomically.
	"get_set": `local old = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1])
return old`,
}

// scriptNS is the key namespace each built-in works on.
var scriptNS = map[string]string{"rate_limit": nsCounter, "incr_cap": nsCounter, "get_set": nsString}

// scriptArgs are the default arguments of the built-ins (get_set uses a value
// from the dataset when none is given).
var scriptArgs = map[string][]string{"rate_limit": {"60", "100000"}, "incr_cap": {"1000000"}}

// FunctionLibrary is loaded with FUNCTION LOAD and called with FCALL. Each
// function takes the same keys and arguments as the script of the same name.
const FunctionLibrary = `#!lua name=plgv
redis.register_function('rate_limit', function(keys, args)
` + "local n = redis.call('INCR', keys[1])\nif n == 1 then redis.call('EXPIRE', keys[1], tonumber(args[1])) end\nif n > tonumber(args[2]) then return 0 end\nreturn 1\n" + `end)
redis.register_function('incr_cap', function(keys, args)
` + "local v = tonumber(redis.call('GET', keys[1]) or '0')\nif v < tonumber(args[1]) then return redis.call('INCR', keys[1]) end\nreturn v\n" + `end)
redis.register_function('get_set', function(keys, args)
` + "local old = redis.call('GET', keys[1])\nredis.call('SET', keys[1], args[1])\nreturn old\n" + `end)
`

func sha(body string) string {
	h := sha1.Sum([]byte(body))
	return hex.EncodeToString(h[:])
}

// scriptSource returns the Lua text and key namespace of a script mix item.
func scriptSource(script, source string) (body, ns string) {
	if source != "" {
		return source, nsString
	}
	return scriptBodies[script], scriptNS[script]
}

// Prepare loads the Lua scripts and functions a workload uses on every node
// that may run them, so EVALSHA and FCALL find them. It does nothing for a
// workload without EVALSHA or FCALL.
func Prepare(ctx context.Context, c Client, mix []config.MixItem) error {
	var scripts []string
	needFunctions := false
	seen := map[string]bool{}
	for _, m := range mix {
		switch m.Op {
		case "EVALSHA":
			body, _ := scriptSource(m.Script, m.Source)
			if !seen[body] {
				seen[body] = true
				scripts = append(scripts, body)
			}
		case "FCALL":
			needFunctions = true
		}
	}
	if len(scripts) == 0 && !needFunctions {
		return nil
	}
	nodeQ, _ := c.(NodeQuerier)
	load := func(q func(Cmd) (any, error)) error {
		for _, body := range scripts {
			if _, err := q(Cmd{"SCRIPT", "LOAD", body}); err != nil {
				return fmt.Errorf("SCRIPT LOAD: %w", err)
			}
		}
		if needFunctions {
			if _, err := q(Cmd{"FUNCTION", "LOAD", "REPLACE", FunctionLibrary}); err != nil && !strings.Contains(strings.ToUpper(err.Error()), "READONLY") {
				return fmt.Errorf("FUNCTION LOAD: %w", err)
			}
		}
		return nil
	}
	if nodeQ != nil && len(nodeQ.Nodes()) > 1 {
		// A cluster: every node needs the scripts (a replica refuses FUNCTION LOAD, which is fine).
		for _, addr := range nodeQ.Nodes() {
			addr := addr
			if err := load(func(cmd Cmd) (any, error) { return nodeQ.QueryNode(ctx, addr, cmd) }); err != nil {
				return fmt.Errorf("%s: %w", addr, err)
			}
		}
		return nil
	}
	return load(func(cmd Cmd) (any, error) { return c.Query(ctx, cmd) })
}
