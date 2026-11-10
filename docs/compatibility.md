# Compatibility

## Servers

| Server | Status | How it was checked |
|---|---|---|
| **Valkey 9.1.3** | Supported, tested on every change | All integration suites: standalone, 6-node cluster with a forced failover, Sentinel with a forced failover, and TLS + ACL. CI pins `valkey/valkey:9`. |
| **Valkey 8.0.12** | Supported | We replayed one recording on it and on 9.1.3 and compared the results (see `multi-target.md`). There's no cluster or Sentinel suite for it. |
| Redis OSS | **Not tested** | plgv only uses core commands in RESP2 or RESP3 that Redis 7.x also has, so it should work, but we haven't proven that. If you see differences, please tell us. |
| Valkey before 7.2 / Redis before 7.0 | Not supported | Several operations use commands from Redis 7.0 and later (see below). |
| Percona's distribution of Valkey | Not yet tested | We keep anything distribution-specific behind capability detection, and nothing needs it today. |

plgv reads the engine and version from `INFO server`. It uses `valkey_version`, not the `redis_version`
compatibility value. Both `plgv replay -info` and run summaries record the server version.

## Scope

plgv covers core Valkey commands only: strings, keys, hashes, lists, sets, sorted sets, streams and consumer
groups, geo, HyperLogLog, bitmaps, pub/sub (including sharded), `MULTI`/`EXEC`/`WATCH`, Lua scripts and
functions, plus raw commands you write yourself.

**Modules aren't supported.** Search, JSON and Bloom commands are out of scope. A server without them is the
normal case, and `FT.*`, `JSON.*` and `BF.*` are unknown commands there.

Some commands need a recent server: `EXPIRETIME` and `SINTERCARD` (7.0), `LMPOP`/`BLMPOP`, `FUNCTION`/`FCALL`
and `EVAL_RO` (7.0), `SPUBLISH`/`SSUBSCRIBE` (7.0), `CLUSTER SHARDS` (7.0), and `GEOSEARCH` and `HRANDFIELD`
(6.2). If the server doesn't know an operation, it fails with `unknown command`. plgv counts that as an error of
class `other` and shows the first message.

## Protocols and clients

plgv speaks RESP2 and RESP3, and the client negotiates which one to use. There are two client libraries:
`valkey-go` (the default, for standalone, cluster and Sentinel) and `go-redis` (standalone only). See
`client-library-decision.md` for the measurements.

| Topology | valkey-go | go-redis |
|---|---|---|
| Standalone | yes | yes |
| Cluster | yes (slot routing, `MOVED`/`ASK`, replica reads) | no |
| Sentinel | yes (follows the master) | no |
| TLS, mutual TLS, ACL users | yes | yes |

## Platforms

plgv runs on Linux and macOS, on amd64 and arm64, as static binaries, and there's a container image for Linux.
We don't build or test for Windows. To build from source you need Go 1.25. The web UI needs a current browser.
Its tests run in jsdom, and the screenshots in the docs were taken with Chrome.
