# Workloads

A workload is a weighted mix of operations (`workload.mix`) running against a dataset
(`dataset`). Each worker repeatedly picks an operation by weight, picks a key by the key
distribution, and sends it, in batches of `target.pool.pipeline_depth`.

Example: `examples/mixed.yaml`. Parameters: `configuration-reference.md`.

## Keys and types

Each data type has its own key namespace, `<key_prefix><type>:<index>`, so an operation never
meets a key of the wrong type and a mixed workload gives no `WRONGTYPE` errors:

| Namespace | Used by |
|---|---|
| `s` strings | `GET SET MGET MSET GETEX SETEX`, and key ops by default |
| `c` counters | `INCR` (created on demand) |
| `a` appended strings | `APPEND` (created on demand; grows without bound) |
| `h` hashes | hash ops |
| `l` lists | list ops |
| `t` sets | set ops |
| `z` sorted sets | sorted set ops |
| `x` streams, `g` geo, `p` HyperLogLog, `b` bitmaps | advanced ops (`dataset.stream_length`, `bitmap_bits`) |

`key_count` keys of each type used by the mix are preloaded (`dataset.preload`). Every worker
draws key indexes from the same distribution:

- `uniform`: every key equally likely.
- `zipf`: a few hot keys take most traffic. `zipf_s` controls the skew.

Start from a [preset](presets.md) (`workload.preset`) for a ready-made mix.

## Operations

| Family | Operations |
|---|---|
| Strings | `GET`, `SET`, `MGET`, `MSET`, `INCR`, `APPEND`, `GETEX`, `SETEX` |
| Keys | `DEL`, `UNLINK`, `EXISTS`, `TTL`, `EXPIRE`, `SCAN` (take `type`) |
| Hashes | `HSET`, `HGET`, `HGETALL`, `HDEL`, `HINCRBY` |
| Lists | `LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LRANGE`, `LLEN` |
| Sets | `SADD`, `SREM`, `SISMEMBER`, `SCARD`, `SRANDMEMBER`, `SMEMBERS` |
| Sorted sets | `ZADD`, `ZREM`, `ZSCORE`, `ZINCRBY`, `ZRANK`, `ZRANGE`, `ZRANGEBYSCORE`, `ZCARD` |
| Streams, geo, HyperLogLog, bitmaps, blocking, scripts, pub/sub, transactions | see [advanced-commands.md](advanced-commands.md) |
| Raw commands | `RAW`: see [raw-injector.md](raw-injector.md) |

Notes on behavior:

- A read of a missing key or an empty collection is counted as a **miss**, not an error.
  Mixes that remove data (`DEL`, `LPOP`, `HDEL`, `SREM`, `ZREM`) produce misses over time.
- `LPUSH`/`RPUSH` grow lists unless you also pop; `SADD`/`ZADD` stay within
  `collection_size` members because members are drawn from that pool.
- `SMEMBERS` and `HGETALL` return whole collections: their cost grows with
  `collection_size` and `hash_fields`.
- `SCAN` sends `SCAN 0 MATCH <prefix><type>:* COUNT n`, a fixed first-page scan used as load.
- `ZRANGEBYSCORE` is sent as `ZRANGE key min max BYSCORE LIMIT 0 count`.
- Multi-key ops (`MGET`, `MSET`) draw plain independent keys. In a cluster set
  `dataset.hash_tag_groups` so their keys share a slot; see [cluster-and-sentinel.md](cluster-and-sentinel.md).

## TTL model

`dataset.ttl` controls expiry:

- `SET` (and preloaded strings) get `EX <seconds>` when the mode is `fixed` or `random`, for
  the `fraction` of writes selected. Other write ops do not set a TTL.
- `SETEX`, `GETEX` and `EXPIRE` always use a TTL: `seconds` for `fixed`, a random value in the
  range for `random`, and `seconds` when the mode is `none`.

## Output

`plgv run` prints totals, latency percentiles, misses, errors and client CPU, then a per-
operation table (ops, share of total, misses, errors). See `cli.md`.
