# Compatibility

## Servers

| Server | Status | How it was checked |
|---|---|---|
| **Valkey 9.1.3** | Supported, tested on every change | All integration suites: standalone, 6-node cluster with a forced failover, Sentinel with a forced failover, TLS + ACL. CI pins `valkey/valkey:9`. |
| **Valkey 8.0.12** | Supported | One recording replayed on it and on 9.1.3 and compared (see `multi-target.md`); no cluster or Sentinel suite. |
| Redis OSS | **Not tested** | plgv uses only core commands in RESP2 or RESP3 that Redis 7.x also has, so it is expected to work, but nothing here proves it. Report differences. |
| Valkey before 7.2 / Redis before 7.0 | Not supported | Several operations use commands from Redis 7.0 and later (below). |
| Percona's distribution of Valkey | Not yet tested | Distribution-specific behavior is kept behind capability detection; none is needed today. |

The engine and version are read from `INFO server` (`valkey_version`, which is not the `redis_version`
compatibility value). `plgv replay -info` and run summaries record the server version.

## Scope

Core Valkey commands only: strings, keys, hashes, lists, sets, sorted sets, streams and consumer
groups, geo, HyperLogLog, bitmaps, pub/sub (including sharded), `MULTI`/`EXEC`/`WATCH`, Lua scripts and
functions, plus raw commands you write. **No modules**: Search, JSON and Bloom commands are out of scope
(a server without them is the normal case; `FT.*`, `JSON.*` and `BF.*` are unknown commands there).

Commands that need a recent server: `EXPIRETIME` and `SINTERCARD` (7.0), `LMPOP`/`BLMPOP`, `FUNCTION`/`FCALL`
and `EVAL_RO` (7.0), `SPUBLISH`/`SSUBSCRIBE` (7.0), `CLUSTER SHARDS` (7.0), `GEOSEARCH` and `HRANDFIELD` (6.2).
An operation the server does not know fails with `unknown command`, which is counted as an error of class
`other` and shown with the first message.

## Protocols and clients

RESP2 and RESP3 (the client negotiates). Two client libraries: `valkey-go` (default; standalone, cluster
and Sentinel) and `go-redis` (standalone only). See `client-library-decision.md` for the measurements.

| Topology | valkey-go | go-redis |
|---|---|---|
| Standalone | yes | yes |
| Cluster | yes (slot routing, `MOVED`/`ASK`, replica reads) | no |
| Sentinel | yes (follows the master) | no |
| TLS, mutual TLS, ACL users | yes | yes |

## Platforms

Linux and macOS, amd64 and arm64, as static binaries; a container image for Linux. Windows is not built or
tested. Go 1.25 to build from source. The web UI needs a current browser; its tests run in jsdom, and
screenshots in the docs were made with Chrome.
