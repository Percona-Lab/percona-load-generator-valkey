# Advanced commands

Beyond the core data types, plgv drives streams, blocking reads, pub/sub, geo, HyperLogLog,
bitmaps, transactions, Lua scripts and functions. Every one is an operation in `workload.mix`,
used like any other (`examples/` has one file per family). Nothing here runs unless you add it.

![Mix builder with a transaction, a script, a blocking read and a raw command](../images/web_advanced_builder.png)

## How operations run

- **Simple** operations are pipelined in a batch with the rest of the mix.
- **Solo** operations (blocking reads, `XCONSUME`, scripts) get their own call, and their own
  latency, so a 200 ms wait never inflates its neighbours.
- **Composite** operations (`TXN`, `PIPELINE`, `WATCH_INCR`, `XCONSUME`) are several commands
  run as a unit on one connection and recorded as one operation.

## Operations

| Family | Operations | Notes |
|---|---|---|
| Streams | `XADD`, `XLEN`, `XRANGE`, `XREAD`, `XREAD_BLOCK`, `XTRIM`, `XPENDING`, `XREADGROUP`, `XCONSUME` | `XADD` caps streams with `MAXLEN ~`. `XCONSUME` = `XREADGROUP` then `XACK`; `XREADGROUP` alone leaves entries pending. Preload creates the consumer group. `dataset.stream_length` entries per stream (default 20). |
| Blocking | `BLPOP`, `BRPOP`, `XREAD_BLOCK` | `timeout` (default 1s). A timeout is a **miss**. |
| Geo | `GEOADD`, `GEOPOS`, `GEODIST`, `GEOSEARCH` | Members come from `collection_size`. |
| HyperLogLog | `PFADD`, `PFCOUNT` | |
| Bitmaps | `SETBIT`, `GETBIT`, `BITCOUNT` | `dataset.bitmap_bits` (default 10000). |
| Scripts | `EVAL`, `EVALSHA`, `FCALL` | `script`: `rate_limit`, `incr_cap` or `get_set` (built in), or inline Lua in `source` (`EVAL` only). `args` are passed as `ARGV`. Scripts and the `plgv` function library are loaded on every node before the run. |
| Pub/Sub | `PUBLISH`, `SPUBLISH` | Needs `workload.pubsub.subscribers` to measure delivery. |
| Transactions | `TXN`, `PIPELINE`, `WATCH_INCR` | `TXN` runs `steps` in `MULTI`/`EXEC`; `PIPELINE` sends them in one round trip without a transaction. `WATCH_INCR` is `WATCH`, `GET`, then `MULTI`/`SET`/`EXEC`; an aborted transaction is a **miss**. |

`steps` take simple operations only (no nesting, no blocking, no pub/sub, no raw). In a
**cluster**, `TXN` needs `dataset.hash_tag_groups` above 0 so its keys share a slot; plgv refuses
to start otherwise.

## Pub/Sub

```yaml
workload:
  pubsub: {subscribers: 4, channels: 16, sharded: false}
  mix: [{op: PUBLISH, weight: 10}]
```

Each subscriber listens to every channel, so expected deliveries are `published x subscribers`.
The publish time travels in the message, so delivery latency is measured end to end. After the
run, plgv waits for in-flight messages and reports delivered, expected, the ratio and p50/p95/p99.
`sharded: true` uses `SSUBSCRIBE`/`SPUBLISH` (cluster shard channels, hash-tagged names).
With 0 subscribers it only publishes.

![Pub/sub delivery in Latency Analysis](../images/web_pubsub_results.png)

## Findings

| Rule | Fires when |
|---|---|
| `watch-conflicts` | 5% or more of `WATCH_INCR` were aborted (warning at 25%). |
| `blocking-timeouts` | Half or more of blocking reads timed out. |
| `streams-ran-dry` | 80% or more of group reads found nothing: add `XADD` or raise `stream_length`. |
| `pubsub-loss` | Under 99% of expected messages arrived (critical under 90%). |
| `dangerous-commands-allowed` | `allow_dangerous` was on. |

## Web UI

Operations are grouped by family. The Parameters column shows what applies: count, fields, key
type, timeout, script (or inline Lua) with arguments, a steps editor for `TXN`/`PIPELINE`, or a
command for `RAW`. Pub/Sub subscribers and `stream_length`/`bitmap_bits` are Advanced settings.

## CLI

`-subscribers N`, `-channels N`, `-raw-file`, `-allow-dangerous`: see [cli.md](cli.md).
