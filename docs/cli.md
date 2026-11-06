# Command line

```
plgv run [flags]     run a workload against a standalone Valkey
plgv web [flags]     start the web UI (see web-ui.md)
plgv config [flags]  validate and print the effective configuration
plgv version
plgv help
```

Start the local test server first (`make valkey-up`, see `VALKEY_TEST_ENV.md`) and build with
`make build-local`.

## `plgv run`

```bash
./bin/plgv run -config examples/mixed.yaml
./bin/plgv run -duration 10s -workers 64 -pipeline 16 -dist zipf
```

With no `-config` the built-in defaults run: an 80/20 GET/SET mix. A config file replaces the
defaults (see `configuration-reference.md`); flags you pass override the file.

| Flag | Overrides | Meaning |
|---|---|---|
| `-config` | | YAML config file. |
| `-addr` | `target.uris` | Server `host:port`, or several comma-separated seed nodes or sentinels. Env `PLGV_URI` also works. |
| `-mode` | `target.mode` | `auto` (default), `standalone`, `cluster` or `sentinel`. |
| `-replica-reads` | `target.read_from_replicas` | Send read-only commands to replicas (cluster or sentinel). |
| `-hash-tags` | `dataset.hash_tag_groups` | Put a `{group}` hash tag in every key (this many groups) so multi-key commands work in a cluster. |
| `-sentinel-master` | `target.sentinel.master_name` | Master set name (with `-mode sentinel`). |
| `-client` | `target.client` | `valkey-go` or `go-redis`. |
| `-workers` | `run.concurrency` | Concurrent workers (default 64). |
| `-duration` | `run.duration` | Run length (default 10s). Ctrl-C stops early and prints the results so far. |
| `-pipeline` | `target.pool.pipeline_depth` | Commands per pipelined batch (default 1). |
| `-raw-file` | `workload.mix` | Replace the mix with the commands in this file (one per line, optional weight). See `raw-injector.md`. |
| `-allow-dangerous` | `workload.allow_dangerous` | Let raw commands use blocked-by-default commands (`FLUSHALL`, `DEBUG`, ...). |
| `-subscribers`, `-channels` | `workload.pubsub` | Pub/sub subscribers and channels; delivery latency is reported. |
| `-preset` | `workload.preset` | Start from a built-in preset (`plgv presets` lists them). The config file and other flags override it. See `presets.md`. |
| `-keyspace`, `-keyspace-samples` | `keyspace_analysis` | After the run, sample the key space: types, sizes, expiry, biggest keys (read-only). See `key-space-analysis.md`. |
| `-ops N`, `-deterministic` | `run.op_budget`, `run.deterministic` | Send exactly N operations; with `-deterministic`, reproducible traffic and a fingerprint. See `deterministic-mode.md`. |
| `-preload-only` | | Write the dataset and exit (prepare a replay target with the recording's starting data). |
| `-tls`, `-tls-ca`, `-tls-cert`, `-tls-key`, `-tls-server-name`, `-tls-insecure` | `target.tls.*` | Connect over TLS or mutual TLS (also on `replay` and `import-traffic`). See `security-tls-acl.md`. |
| `-record FILE` | `record.path` | Write every command the workers send to this file so the run can be replayed. See `record-and-replay.md`. |
| `-record-outcomes`, `-record-checksums` | `record.outcomes`, `record.checksums` | Keep each command's status and latency, and a checksum of its reply. |
| `-record-compress`, `-record-max-mb`, `-record-backpressure`, `-record-fingerprint=false` | `record.compression`, `max_mb`, `backpressure`, `data_fingerprint` | Compression (`auto`, `none`, `zstd`, `gzip`), size limit, what to do when the writer lags (`fail` or `block`), and the starting-data fingerprint. |
| `-read-pct` | `workload.mix` | Replace the mix with GET/SET at this percent GET. |
| `-keys` | `dataset.key_count` | Keys per type. |
| `-dist`, `-zipf-s` | `dataset.key_distribution`, `zipf_s` | Key distribution and skew. |
| `-value-size` | `dataset.value_size` | Value bytes. |
| `-prefix` | `dataset.key_prefix` | Key prefix. |
| `-seed` | `run.seed` | Random seed; 0 picks one and prints it. |
| `-preload=false` | `dataset.preload` | Skip writing the dataset first. |
| `-server-metrics` | `server_metrics.enabled` | Poll `INFO`, `SLOWLOG` and `LATENCY` and report server stats and findings (read-only). |
| `-target-ops` | `run.target_ops_per_sec` | Pace the run to this total rate (0 = as fast as possible). |
| `-loop` | `run.loop` | `closed` (default) or `open`: an open loop sends at `-target-ops` whether or not the server keeps up. |
| `-sweep-workers` | `run.sweep` | Run once per worker count, for example `4,8,16,32` (2 to 8 levels). |
| `-sweep-rates` | `run.sweep` | Run once per target rate in ops/s, for example `10000,20000,40000`. |
| `-sweep-pause` | `run.sweep.pause` | Wait between the runs of a sweep. |
| `-co` | `run.coordinated_omission` | Measure latency from the intended start time (needs `-target-ops`). |
| `-sample-interval` | `output.sample_interval` | Time-series slice length (default 1s). |
| `-summary` | `output.summary_file` | Write a JSON run summary to this file. |

Credentials come from `PLGV_USERNAME` and `PLGV_PASSWORD`.

## Output

```
ops:        1423808
elapsed:    5.00s
throughput: 284745 ops/s
latency:    mean 892µs  p50 819µs  p95 1.802ms  p99 2.294ms  p99.9 4.719ms  max 6.006ms
misses:     0
errors:     0
client cpu: 1.08 cores, 0.38 cores per 100k ops/s

per operation:
  op                      ops    share     misses   errors        p50        p99
  GET                 1138933    80.0%          0        0      819µs    2.294ms
  SET                  284875    20.0%          0        0      819µs    2.294ms

server:                          (only with -server-metrics)
  version 9.1.3, master, standalone
  ops processed: 1423811 (plgv sent 1423808)
  ...

findings:
  none
```

- With pipelining, each command's latency is the round trip of its whole batch.
- `misses` are reads that found nothing (not errors).
- `error classes:` appears when there are errors (`OOM=12 timeout=3`).
- `client cpu` is the CPU used by plgv itself.
- The `server:` and `findings:` sections are explained in `metrics-and-server-stats.md` and
  `insights.md`.
- Exit code 1 if any command returned an error, 2 for a bad flag or configuration.

## `plgv presets`

Lists the built-in workload presets. Use one with `plgv run -preset <name>`.

## `plgv config`

```bash
./bin/plgv config                       # print the default configuration
./bin/plgv config -config my.yaml       # validate and print the effective configuration
./bin/plgv config -o starter.yaml       # write the defaults to a file to edit
```

Load profiles (`ramp`, `step`, `spike`, `sine`) are set in the YAML file (`examples/ramp.yaml`); see
[load-profiles-and-sweep.md](load-profiles-and-sweep.md), which also covers sweeps and the open loop.

Cluster and sentinel targets are covered in [cluster-and-sentinel.md](cluster-and-sentinel.md).

## `plgv replay`, `plgv compare`, `plgv import-traffic`

`plgv replay [flags] FILE` sends a recording to a server (`record-and-replay.md`); `-targets a=h:p,b=h:p`
replays on several servers and compares them (`multi-target.md`), `-label NAME` names a single replay.
`plgv compare [flags] SUMMARY...` compares replay summaries (`replay-comparison.md`).
`plgv import-traffic -out FILE [captured-file]` converts MONITOR or SLOWLOG traffic into a recording (`traffic-import.md`).
Run each with `-h` for its flags.
