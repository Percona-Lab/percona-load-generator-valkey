# Configuration reference

`plgv run -config file.yaml` reads a YAML file on top of the built-in defaults, so a file only
needs the settings you want to change. Unknown fields are errors (typos do not pass silently).
Print the effective configuration with `plgv config -config file.yaml`; write the defaults to
a file to start from with `plgv config -o my.yaml`. A full example is `examples/mixed.yaml`.

Precedence, lowest to highest: defaults, YAML file, environment, command-line flags.

## Environment

| Variable | Effect |
|---|---|
| `PLGV_URI` | Server address; replaces `target.uris`. `PLGV_ADDR` is the older name and also works. |
| `PLGV_USERNAME`, `PLGV_PASSWORD` | Credentials. Never read from or written to the YAML file, and not allowed in the URI. |

## `target`

| Key | Default | Notes |
|---|---|---|
| `uris` | `["valkey://127.0.0.1:6379"]` | `valkey://host:port`, `redis://host:port` or `host:port`. One for a standalone server; one or more seed nodes for a cluster; the sentinels for sentinel. `valkeys://` or `rediss://` connects over TLS. Credentials in the URI are rejected. |
| `mode` | `auto` | `auto` (detect standalone or cluster), `standalone`, `cluster` or `sentinel`. See `cluster-and-sentinel.md`. |
| `sentinel.master_name` | empty | The master set to follow (mode `sentinel`, required there, not allowed otherwise). |
| `read_from_replicas` | false | Send read-only commands to replicas (cluster or sentinel). |
| `client` | `valkey-go` | `valkey-go` or `go-redis` (see `client-library-decision.md`). |
| `pool.pipeline_depth` | 1 | Operations per pipelined batch. 1 means no pipelining. |
| `tls.enabled` | false | Connect over TLS (also implied by a `valkeys://` address). Off by default. |
| `tls.ca_file` | empty | PEM authorities for the server certificate; empty = the system's. |
| `tls.cert_file`, `tls.key_file` | empty | Client certificate and key (mutual TLS); both or neither. |
| `tls.server_name` | empty | Name to check the certificate against; empty = the address's host. |
| `tls.insecure_skip_verify` | false | Accept any server certificate (throwaway tests only). |

Credentials are never in the file: `PLGV_USERNAME` / `PLGV_PASSWORD`, or `PLGV_USERNAME_FILE` / `PLGV_PASSWORD_FILE`
for mounted secrets. TLS can also come from `PLGV_TLS`, `PLGV_TLS_CA_FILE`, `PLGV_TLS_CERT_FILE`, `PLGV_TLS_KEY_FILE`
and `PLGV_TLS_SERVER_NAME`. See `security-tls-acl.md`.

## `run`

| Key | Default | Notes |
|---|---|---|
| `duration` | `10s` | Go duration (`30s`, `5m`). |
| `concurrency` | 64 | Worker goroutines. |
| `seed` | 1 | Random seed. 0 picks a random seed and prints it. See "Seeds" below. |
| `op_budget` | 0 | Send exactly this many operations instead of running for `duration` (split between the workers). Needs a closed loop and a fixed load profile. See `deterministic-mode.md`. |
| `deterministic` | false | Reproducible traffic and a traffic fingerprint. Needs `seed` and `op_budget`; refuses `PUBLISH`, `XCONSUME`, `WATCH_INCR` and `{now}`. See `deterministic-mode.md`. |
| `target_ops_per_sec` | 0 | Pace the whole run to this rate. 0 = as fast as possible. |
| `loop` | `closed` | `closed` or `open`. The open loop offers batches at `target_ops_per_sec` whether or not the server keeps up; it needs a target rate. See `load-profiles-and-sweep.md`. |
| `coordinated_omission` | false | Measure latency from each batch's intended start. Needs `target_ops_per_sec` > 0. See `metrics-and-server-stats.md`. |

### `run.load_profile`

How many workers are active over time. Default `fixed`. See `load-profiles-and-sweep.md`.

| Key | Used by | Notes |
|---|---|---|
| `kind` | all | `fixed`, `ramp`, `step`, `spike` or `sine`. |
| `start_workers`, `end_workers`, `ramp_over` | ramp | |
| `steps` | step | List of `{workers, duration}`. |
| `base_workers`, `peak_workers`, `spike_at`, `spike_for` | spike | |
| `min_workers`, `max_workers`, `period` | sine | |

A changing profile with a target rate needs `loop: open`.

### `run.sweep`

Run once per level. Default off.

| Key | Default | Notes |
|---|---|---|
| `kind` | `none` | `none`, `workers` or `rate`. |
| `values` | `[]` | 2 to 8 levels: whole worker counts (1 to 10000) or target ops/s (above 0), no repeats. |
| `pause` | 0 | Wait between runs, for example `2s`. |

A sweep needs the `fixed` profile; a rate sweep needs `target_ops_per_sec: 0`.

## `dataset`

| Key | Default | Notes |
|---|---|---|
| `key_count` | 100000 | Keys per type (strings, hashes, lists, sets and sorted sets each get this many). `1_000_000` style underscores are allowed. |
| `key_prefix` | `plgv:` | Keys are `<prefix><type>:<index>`, for example `plgv:s:42`. |
| `key_distribution` | `uniform` | `uniform` or `zipf`. |
| `zipf_s` | 0 (1.1) | Zipf skew, must be greater than 1. Higher means a smaller set of hot keys. |
| `value_size` | 256 | Bytes per value. |
| `hash_tag_groups` | 0 | With N above 0, every key carries a `{group}` hash tag (index modulo N) so keys of a group share a cluster slot and `MGET`/`MSET` work in a cluster. A cluster refuses multi-key operations without it. |
| `collection_size` | 100 | Elements preloaded into each list, set and sorted set. |
| `hash_fields` | 8 | Fields preloaded into each hash. |
| `preload` | true | Write the dataset before the run. Only the types the mix uses are loaded. |
| `ttl.mode` | `none` | `none`, `fixed` or `random`. |
| `ttl.seconds` | 300 | TTL for `fixed`; also used by explicit expiry ops when the mode is `none`. |
| `ttl.min_seconds`, `ttl.max_seconds` | 60, 3600 | Range for `random`. |
| `ttl.fraction` | 1 | Share of string `SET` writes (and preloaded strings) that get a TTL, 0 to 1. |

## `server_metrics`

Off by default; read-only polling of the server. See `metrics-and-server-stats.md`.

| Key | Default | Notes |
|---|---|---|
| `enabled` | false | Poll `INFO`, `SLOWLOG` and `LATENCY`. |
| `interval` | `1s` | Poll interval, at least `100ms`. |
| `slowlog` | true | Poll `SLOWLOG GET` for new entries (when enabled). |
| `slowlog_entries` | 128 | Entries fetched per poll (1-10000). |
| `latency` | true | Poll `LATENCY LATEST` (when enabled). |

## `keyspace_analysis`

| Key | Default | Notes |
|---|---|---|
| `enabled` | `false` | Sample the key space after the run (read-only). |
| `samples` | `10000` | Keys to inspect, 100 to 1,000,000. |
| `top` | `10` | Biggest keys to list, 1 to 100. |
| `match` | empty | `SCAN` pattern; empty = `dataset.key_prefix` + `*`. |

See [key-space-analysis.md](key-space-analysis.md).

## `record`

Off unless `path` is set. A command-line feature: the web server refuses it.

| Key | Default | Notes |
|---|---|---|
| `path` | empty | Recording file. Empty = not recording. Not allowed with a sweep. |
| `compression` | `auto` | `auto` (by `.zst` / `.gz`), `none`, `zstd`, `gzip`. |
| `max_mb` | `0` | Stop after this many uncompressed MB; the file is marked `truncated`. |
| `backpressure` | `fail` | `fail` stops the recording if the writer lags; `block` makes workers wait. |
| `outcomes` | `false` | Keep each command's status and latency (needs `path`). |
| `checksums` | `false` | Also keep a checksum of each reply (needs `path`; implies outcomes). |
| `data_fingerprint` | `true` | Store the key count and a key digest of the starting data (when recording). |

See [record-and-replay.md](record-and-replay.md).

## `output`

| Key | Default | Notes |
|---|---|---|
| `sample_interval` | `1s` | Length of each time-series slice, at least `100ms`. |
| `summary_file` | empty | Write a JSON run summary here. Empty = none. |

## `workload.mix`

`workload.preset` (default empty) names a built-in preset (see [presets.md](presets.md)). The preset is applied under the file, so any setting written here overrides it.

A list of weighted operations; a worker picks the next operation by weight.

| Key | Notes |
|---|---|
| `op` | Operation name, any case. See `workloads.md` for the list. |
| `weight` | Integer >= 1. Shares are `weight / sum of weights`. |
| `count` | Only for ops that take it: `MGET`/`MSET` (keys, default 4), `LRANGE`/`ZRANGE`/`ZRANGEBYSCORE` (elements, default 10), `SRANDMEMBER` (default 1), `SCAN` (page size, default 10). |
| `fields` | `HSET` only: fields written per call (default 1, at most `dataset.hash_fields`). |
| `type` | Key ops only (`DEL`, `UNLINK`, `EXISTS`, `TTL`, `EXPIRE`, `SCAN`): `string` (default), `hash`, `list`, `set`, `zset`, `stream`, `geo`, `hll` or `bitmap`. |
| `timeout` | Blocking ops (`BLPOP`, `BRPOP`, `XREAD_BLOCK`): wait limit, default `1s`. |
| `script`, `source`, `args` | `EVAL`/`EVALSHA`/`FCALL`: built-in `rate_limit`, `incr_cap`, `get_set`; inline Lua in `source` (`EVAL`); `ARGV` values in `args`. |
| `steps` | `TXN`/`PIPELINE`: a list of simple operations (`op`, optional `count`, `fields`, `type`). |
| `command` | `RAW`: the command text, with placeholders. See `raw-injector.md`. |

`workload.pubsub` (`subscribers` default 0, `channels` default 16, `sharded` default false) adds subscribers for `PUBLISH`/`SPUBLISH`.
`workload.allow_dangerous` (default false) lets `RAW` use commands blocked by default. `dataset.stream_length` (20) and `dataset.bitmap_bits` (10000) size the preloaded data. See [advanced-commands.md](advanced-commands.md).

Every problem in a file is reported at once, with the key path (for example
`workload.mix[2].count: not valid for GET`).

## Seeds

With a non-zero `run.seed`, each worker generates the same sequence of operations and keys on
every run. Totals still differ between runs because a run is bounded by time, not by a fixed
operation count (a fixed operation budget comes with deterministic mode in phase 10). The seed
used is printed at the start of every run, so a random seed can be reused.

## Not available yet

Value size distributions, TLS. It is planned for a later phase.
