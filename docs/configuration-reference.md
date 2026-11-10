# Configuration reference

`plgv run -config file.yaml` reads a YAML file on top of the built-in defaults, so your file only needs the
settings you want to change. Unknown fields are errors, so a typo won't slip by unnoticed.

To see the effective configuration, run `plgv config -config file.yaml`. To write the defaults to a file you can
start from, run `plgv config -o my.yaml`. `examples/mixed.yaml` is a full example.

Settings are applied in this order, from lowest to highest priority: defaults, YAML file, environment,
command-line flags.

## Environment

| Variable | Effect |
|---|---|
| `PLGV_URI` | Server address. It replaces `target.uris`. `PLGV_ADDR` is the older name and still works. |
| `PLGV_USERNAME`, `PLGV_PASSWORD` | Credentials. plgv never reads them from or writes them to the YAML file, and they aren't allowed in the URI. |

## `target`

| Key | Default | Notes |
|---|---|---|
| `uris` | `["valkey://127.0.0.1:6379"]` | `valkey://host:port`, `redis://host:port` or `host:port`. Use one for a standalone server, one or more seed nodes for a cluster, or the sentinels for Sentinel. `valkeys://` or `rediss://` connects over TLS. Credentials in the URI are rejected. |
| `mode` | `auto` | `auto` (detect standalone or cluster), `standalone`, `cluster` or `sentinel`. See `cluster-and-sentinel.md`. |
| `sentinel.master_name` | empty | The master set to follow. Required in mode `sentinel`, and not allowed in any other mode. |
| `read_from_replicas` | false | Send read-only commands to replicas (cluster or Sentinel). |
| `client` | `valkey-go` | `valkey-go` or `go-redis` (see `client-library-decision.md`). |
| `pool.pipeline_depth` | 1 | Operations per pipelined batch. 1 means no pipelining. |
| `tls.enabled` | false | Connect over TLS. A `valkeys://` address implies it too. Off by default. |
| `tls.ca_file` | empty | PEM authorities for the server certificate. If it's empty, plgv uses the system's. |
| `tls.cert_file`, `tls.key_file` | empty | Client certificate and key for mutual TLS. Set both or neither. |
| `tls.server_name` | empty | Name to check the certificate against. If it's empty, plgv uses the address's host. |
| `tls.insecure_skip_verify` | false | Accept any server certificate. Only use this for throwaway tests. |

Credentials never go in the file. Use `PLGV_USERNAME` and `PLGV_PASSWORD`, or `PLGV_USERNAME_FILE` and
`PLGV_PASSWORD_FILE` for mounted secrets. TLS settings can also come from `PLGV_TLS`, `PLGV_TLS_CA_FILE`,
`PLGV_TLS_CERT_FILE`, `PLGV_TLS_KEY_FILE` and `PLGV_TLS_SERVER_NAME`. See `security-tls-acl.md`.

## `run`

| Key | Default | Notes |
|---|---|---|
| `duration` | `10s` | Go duration (`30s`, `5m`). |
| `concurrency` | 64 | Worker goroutines. |
| `seed` | 1 | Random seed. 0 picks a random seed and prints it. See "Seeds" below. |
| `op_budget` | 0 | Send exactly this many operations instead of running for `duration`, split between the workers. Needs a closed loop and a fixed load profile. See `deterministic-mode.md`. |
| `deterministic` | false | Reproducible traffic and a traffic fingerprint. Needs `seed` and `op_budget`, and refuses `PUBLISH`, `XCONSUME`, `WATCH_INCR` and `{now}`. See `deterministic-mode.md`. |
| `target_ops_per_sec` | 0 | Pace the whole run to this rate. 0 means as fast as possible. |
| `loop` | `closed` | `closed` or `open`. The open loop offers batches at `target_ops_per_sec` whether or not the server keeps up, so it needs a target rate. See `load-profiles-and-sweep.md`. |
| `coordinated_omission` | false | Measure latency from each batch's intended start. Needs `target_ops_per_sec` > 0. See `metrics-and-server-stats.md`. |

### `run.load_profile`

This controls how many workers are active over time. The default is `fixed`. See `load-profiles-and-sweep.md`.

| Key | Used by | Notes |
|---|---|---|
| `kind` | all | `fixed`, `ramp`, `step`, `spike` or `sine`. |
| `start_workers`, `end_workers`, `ramp_over` | ramp | |
| `steps` | step | List of `{workers, duration}`. |
| `base_workers`, `peak_workers`, `spike_at`, `spike_for` | spike | |
| `min_workers`, `max_workers`, `period` | sine | |

If a profile changes the worker count and you also set a target rate, you need `loop: open`.

### `run.sweep`

Runs once per level. It's off by default.

| Key | Default | Notes |
|---|---|---|
| `kind` | `none` | `none`, `workers` or `rate`. |
| `values` | `[]` | 2 to 8 levels: whole worker counts (1 to 10000) or target ops/s (above 0), no repeats. |
| `pause` | 0 | How long to wait between runs, for example `2s`. |

A sweep needs the `fixed` profile, and a rate sweep needs `target_ops_per_sec: 0`.

## `dataset`

| Key | Default | Notes |
|---|---|---|
| `key_count` | 100000 | Keys per type. Strings, hashes, lists, sets and sorted sets each get this many. Underscores like `1_000_000` are allowed. |
| `key_prefix` | `plgv:` | Keys are `<prefix><type>:<index>`, for example `plgv:s:42`. |
| `key_distribution` | `uniform` | `uniform` or `zipf`. |
| `zipf_s` | 0 (1.1) | Zipf skew, which must be greater than 1. A higher value means a smaller set of hot keys. |
| `value_size` | 256 | Bytes per value. |
| `hash_tag_groups` | 0 | With N above 0, every key carries a `{group}` hash tag (index modulo N), so keys of a group share a cluster slot and `MGET`/`MSET` work in a cluster. Without it, a cluster refuses multi-key operations. |
| `collection_size` | 100 | Elements preloaded into each list, set and sorted set. |
| `hash_fields` | 8 | Fields preloaded into each hash. |
| `preload` | true | Write the dataset before the run. plgv only loads the types the mix uses. |
| `ttl.mode` | `none` | `none`, `fixed` or `random`. |
| `ttl.seconds` | 300 | TTL for `fixed`. Explicit expiry operations also use it when the mode is `none`. |
| `ttl.min_seconds`, `ttl.max_seconds` | 60, 3600 | Range for `random`. |
| `ttl.fraction` | 1 | Share of string `SET` writes (and preloaded strings) that get a TTL, 0 to 1. |

## `server_metrics`

This is off by default, and it only polls the server with read-only commands. See `metrics-and-server-stats.md`.

| Key | Default | Notes |
|---|---|---|
| `enabled` | false | Poll `INFO`, `SLOWLOG` and `LATENCY`. |
| `interval` | `1s` | Poll interval, at least `100ms`. |
| `slowlog` | true | Poll `SLOWLOG GET` for new entries (when enabled). |
| `slowlog_entries` | 128 | Entries fetched per poll (1-10000). |
| `latency` | true | Poll `LATENCY LATEST` (when enabled). |

## `server_config`

Off by default. It only reads the server's settings with `CONFIG GET *`. See `server-config.md`.

| Key | Default | Notes |
|---|---|---|
| `enabled` | false | Record the server's settings at the start and end of the run. |
| `name` | empty | Your label for this server setup, at most 80 characters, no control characters. Stored with the run. |

## `keyspace_analysis`

| Key | Default | Notes |
|---|---|---|
| `enabled` | `false` | Sample the key space after the run (read-only). |
| `samples` | `10000` | Keys to inspect, 100 to 1,000,000. |
| `top` | `10` | Biggest keys to list, 1 to 100. |
| `match` | empty | `SCAN` pattern. If it's empty, plgv uses `dataset.key_prefix` + `*`. |

See [key-space-analysis.md](key-space-analysis.md).

## `record`

This is off unless `path` is set. It's a command-line feature, and the web server refuses it.

| Key | Default | Notes |
|---|---|---|
| `path` | empty | Recording file. If it's empty, plgv doesn't record. Not allowed with a sweep. |
| `compression` | `auto` | `auto` (by `.zst` / `.gz`), `none`, `zstd`, `gzip`. |
| `max_mb` | `0` | Stop after this many uncompressed MB. The file is marked `truncated`. |
| `backpressure` | `fail` | `fail` stops the recording if the writer lags; `block` makes workers wait. |
| `outcomes` | `false` | Keep each command's status and latency (needs `path`). |
| `checksums` | `false` | Also keep a checksum of each reply (needs `path`; implies outcomes). |
| `data_fingerprint` | `true` | Store the key count and a key digest of the starting data when recording. |

See [record-and-replay.md](record-and-replay.md).

## `output`

| Key | Default | Notes |
|---|---|---|
| `sample_interval` | `1s` | Length of each time-series slice, at least `100ms`. |
| `summary_file` | empty | Write a JSON run summary here. If it's empty, plgv doesn't write one. |

## `workload.mix`

`workload.preset` (empty by default) names a built-in preset (see [presets.md](presets.md)). The preset is applied
under your file, so any setting you write here overrides it.

The mix is a list of weighted operations, and a worker picks the next operation by weight.

| Key | Notes |
|---|---|
| `op` | Operation name, in any case. See `workloads.md` for the list. |
| `weight` | An integer, 1 or higher. An operation's share is `weight / sum of weights`. |
| `count` | Only for operations that take it: `MGET`/`MSET` (keys, default 4), `LRANGE`/`ZRANGE`/`ZRANGEBYSCORE` (elements, default 10), `SRANDMEMBER` (default 1), and `SCAN` (page size, default 10). |
| `fields` | `HSET` only: fields written per call (default 1, at most `dataset.hash_fields`). |
| `type` | Key operations only (`DEL`, `UNLINK`, `EXISTS`, `TTL`, `EXPIRE`, `SCAN`). One of `string` (the default), `hash`, `list`, `set`, `zset`, `stream`, `geo`, `hll` or `bitmap`. |
| `timeout` | Blocking operations (`BLPOP`, `BRPOP`, `XREAD_BLOCK`). It's the wait limit, `1s` by default. |
| `script`, `source`, `args` | For `EVAL`/`EVALSHA`/`FCALL`. Use the built-in `rate_limit`, `incr_cap` or `get_set`, or inline Lua in `source` (`EVAL`). Put `ARGV` values in `args`. |
| `steps` | `TXN`/`PIPELINE`: a list of simple operations (`op`, optional `count`, `fields`, `type`). |
| `command` | For `RAW`: the command text, with placeholders. See `raw-injector.md`. |

`workload.pubsub` adds subscribers for `PUBLISH`/`SPUBLISH`. Its settings are `subscribers` (default 0),
`channels` (default 16) and `sharded` (default false).

`workload.allow_dangerous` (default false) lets `RAW` use commands that are blocked by default.
`dataset.stream_length` (20) and `dataset.bitmap_bits` (10000) set the size of the preloaded data. See
[advanced-commands.md](advanced-commands.md).

plgv reports every problem in a file at once, with the key path, for example
`workload.mix[2].count: not valid for GET`.

## Seeds

With a non-zero `run.seed`, each worker generates the same sequence of operations and keys on every run. Totals
still differ between runs, because a run is bounded by time and not by a fixed operation count. If you need a
fixed count, use deterministic mode (see `deterministic-mode.md`). plgv prints the seed it used at the start of
every run, so you can reuse a random seed.

## Not available yet

Value size distributions aren't available yet.
