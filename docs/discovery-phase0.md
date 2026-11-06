# Phase 0 discovery notes

Checked 2026-11-04 against `valkey/valkey:9` (resolved to 9.1.3) in Docker (`make valkey-up`,
`make valkey-cluster-up`).

## Server facts (verified)

- `INFO server` reports `redis_version:7.2.4` (compatibility value) and the real
  `valkey_version:9.1.3`. Detect the engine and version from `valkey_version`, with `HELLO 3`
  (`server valkey`, `version 9.1.3`) as a cross-check. Never trust `redis_version`.
- `INFO` sections present: Server, Clients, Memory, Persistence, Stats, Replication, CPU,
  Modules, Errorstats, Cluster, Keyspace. Fields used by the poller exist:
  `instantaneous_ops_per_sec`, `total_commands_processed`, `used_memory`, `keyspace_hits`,
  `evicted_keys`, `connected_clients`, `role`, `latest_fork_usec`.
- `MODULE LIST` returns one built-in module, `lua` (ver 1). It is not an optional module:
  capability detection must not treat any listed module as a feature. Search, JSON and Bloom
  commands (`FT._LIST`, `JSON.GET`, `BF.EXISTS`) return `unknown command`, confirming they are
  absent and out of scope.
- Core families all work: strings, hashes (including `HEXPIRE` and `HGETEX`, so hash-field TTL
  is available in 9.x), streams, geo, HyperLogLog, bitmaps, `EVAL`, `FUNCTION`.
- `SLOWLOG GET`, `LATENCY LATEST`, `CONFIG GET`, `HELLO 3` (RESP3) all work.
- `OBJECT FREQ` errors unless an LFU `maxmemory-policy` is set (default `noeviction`):
  features that depend on LFU data need a capability check.
- Defaults: `io-threads` = 1, `maxmemory-policy` = noeviction. Server I/O threading is a
  tuning knob worth recording in run metadata.
- Command count: 257.

## Cluster facts (verified)

- 6 nodes, 3 primaries and 3 replicas, `server_mode:cluster`, all slots covered, state ok.
- Nodes announce `127.0.0.1:700x` with bus ports `1700x`; the topology reader should use the
  announced addresses from `CLUSTER NODES` / `CLUSTER SHARDS`.

## Decisions

- Naming and trademark: no issue (user decision). Binary `plgv`.
- Client library: still the phase 1 spike (`valkey-go` first, `go-redis` fallback), behind
  `internal/valkey/client.go`. No client dependency is added in phase 0.
- Target version: latest stable upstream Valkey. CI pins the major tag `valkey/valkey:9`.
- Go: `go.mod` says 1.25.4 (same as plgm). Local toolchain was 1.23.4; Go downloaded 1.25.4
  automatically on first build.

## Packages seeded from plgm

Copied and renamed (no engine dependencies): `internal/logger`, `internal/loadprofile`,
`internal/report`.

Not copied yet, because they import the MongoDB driver or Mongo-only packages and would break
the build: `internal/stats` (phase 3), `internal/webui` (phase 4), `internal/workloadrec`,
`internal/replaycompare` (phase 9), `internal/multitarget` (phase 10), `internal/config`
(phase 2). Each is copied when its phase rewrites it.
