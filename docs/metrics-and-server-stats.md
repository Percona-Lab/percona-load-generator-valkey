# Metrics and server stats

A run measures two things: what the load generator sees (client side) and what the server
reports about itself (server side). The client side is always on. The server side is opt-in
because it sends extra commands to the server.

## Client-side metrics

Always collected, printed at the end of `plgv run`, and stored in the run summary.

| Metric | Meaning |
|---|---|
| ops, throughput | Operations sent and ops per second over the run. |
| latency | mean, p50, p95, p99, p99.9, max. Percentiles come from a histogram with about 3% relative error; max is exact. |
| misses | Reads that returned nothing (not errors). |
| errors and error classes | Failed operations, grouped by class (see below). The first error text is kept. |
| per operation | Ops, share, misses, errors, p50 and p99 for each entry of `workload.mix`. |
| client cpu | CPU cores plgv itself used, and cores per 100k ops/s. Watch it: if plgv uses nearly all CPU, it is the bottleneck. |
| time series | One point per `output.sample_interval` (default 1 s) with ops/s, errors and p50/p95/p99/max. |

With pipelining (`target.pool.pipeline_depth` above 1) every operation in a batch is charged the
batch round-trip time, so per-operation latencies inside one batch are equal.

### Error classes

`timeout`, `connection`, `MOVED`, `ASK`, `LOADING`, `OOM`, `READONLY`, `NOAUTH` (also wrong
password), `NOPERM`, `BUSY`, `CLUSTERDOWN`, `TRYAGAIN`, `WRONGTYPE`, `other`. Server errors are
recognized by the code at the start of the reply; network failures by the error type.

### Bounded memory (downsampling)

The time series holds at most 2048 points. When it is full, neighboring points are merged
pairwise and each stored point then covers twice as many slices, so a 24-hour run uses the
same memory as a 1-minute run. Merging keeps counts exact; for percentiles it keeps the larger
p95, p99 and max of the two (a spike never disappears from a chart) and averages p50. The
run-level percentiles are always exact because they come from the full histogram.

### Open loop

A run with `run.loop: open` always measures latency from when each batch was *due*, and counts
batches that were never sent ("dropped"). See [load-profiles-and-sweep.md](load-profiles-and-sweep.md).

### Coordinated omission (opt-in)

By default latency is the time from sending a command to receiving the reply. If the server
stalls, a closed-loop generator simply sends fewer commands during the stall and the stall
barely shows in the percentiles. That is coordinated omission.

With a target rate, each worker has a schedule: batch *n* is due at a fixed time. Set
`run.coordinated_omission: true` (or `-co`) and latency is measured from the **intended** start
of each batch, so time spent waiting behind schedule counts. Use it when you want the delay a
real user would see at a given request rate.

```bash
./bin/plgv run -target-ops 50000 -co -duration 30s
```

- It needs `run.target_ops_per_sec` greater than 0 (a configuration error otherwise).
- The run output says which latency mode was used.
- If the target rate is not reached, the Insights say so.

## Server-side metrics (opt-in)

Enable with `server_metrics.enabled: true`, `-server-metrics`, or the **Collect server metrics** checkbox in the web UI ([server-metrics.md](server-metrics.md)). plgv then polls the server
every `server_metrics.interval` (default 1 s) with read-only commands: `INFO everything`,
`SLOWLOG GET` and `LATENCY LATEST`. It never changes server configuration or resets the
slowlog.

| Source | What is taken |
|---|---|
| `INFO` | version (from `valkey_version`; `redis_version` is a fixed compatibility value), role, mode, memory and peak, fragmentation, hits and misses, evicted and expired keys, connections, blocked clients, network bytes, fork time, replication offsets, last background save status, per-code error counters |
| `SLOWLOG GET` | Entries added **during the run** (the log is read once at the start and only newer entries count), with command, duration and client |
| `LATENCY LATEST` | Events recorded by the latency monitor, if the server has `latency-monitor-threshold` set (it is off by default on the server) |

Printed at the end of the run:

```
server:
  version 9.1.3, master, standalone
  ops processed: 1423811 (plgv sent 1423808)
  hit ratio: 100.0% (1138933 hits, 0 misses)
  memory: peak 37.3 MiB, maxmemory 0, evicted keys 0, expired keys 0
  slowlog: 0 new entries
```

- **ops processed** is the server's `total_commands_processed` change over the run, minus the
  commands plgv itself sent to poll. It should match the number plgv sent; a gap means other
  clients are using the server (use a dedicated server for benchmarks) or commands were lost.
- If the ACL user may not run `INFO`, `SLOWLOG` or `LATENCY`, plgv skips what is denied, keeps
  going, and reports it (`poll errors`, and an Insight).
- Polling shares the workload connection and costs a few commands per second.
- The gauge series (ops/s, memory, clients, hit ratio, network) is bounded the same way as the
  client series (at most 2048 points; neighboring points are averaged).

## Run summary (JSON)

`-summary run.json` (or `output.summary_file`) writes everything above as one JSON file: the
configuration (YAML, never any credentials), client totals and percentiles, the time series,
per-operation results, the server section, and the findings. It is the record that run history
and Trends will read (Web UI, phase 4). Durations are milliseconds; `schema_version` is 1. The
file is created readable only by its owner.

## Settings

| Setting | Flag | Default |
|---|---|---|
| `run.target_ops_per_sec` | `-target-ops` | 0 (no pacing) |
| `run.coordinated_omission` | `-co` | false |
| `output.sample_interval` | `-sample-interval` | 1s (minimum 100ms) |
| `output.summary_file` | `-summary` | none |
| `server_metrics.enabled` | `-server-metrics` | false |
| `server_metrics.interval` | | 1s (minimum 100ms) |
| `server_metrics.slowlog` | | true (when enabled) |
| `server_metrics.slowlog_entries` | | 128 per poll |
| `server_metrics.latency` | | true (when enabled) |

If more than `slowlog_entries` slow commands occur between two polls, only the newest are seen.
