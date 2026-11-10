# Workloads

A workload is a weighted mix of operations (`workload.mix`) running against a dataset (`dataset`). Each worker
picks an operation by weight, picks a key based on the key distribution, and sends it. It does this over and over,
in batches of `target.pool.pipeline_depth`.

For an example, see `examples/mixed.yaml`. All the parameters are in `configuration-reference.md`.

## Keys and types

Each data type has its own key namespace, `<key_prefix><type>:<index>`. That way an operation never runs into a
key of the wrong type, and a mixed workload doesn't give you `WRONGTYPE` errors:

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

plgv preloads `key_count` keys of each type the mix uses (`dataset.preload`). Every worker draws key indexes
from the same distribution, which is one of:

- `uniform`: every key is equally likely.
- `zipf`: a few hot keys get most of the traffic. `zipf_s` controls how skewed it is.

If you'd rather not build a mix yourself, start from a [preset](presets.md) (`workload.preset`).

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

A few notes on how these behave:

- Reading a missing key or an empty collection counts as a **miss**, not an error. If your mix removes data
  (`DEL`, `LPOP`, `HDEL`, `SREM`, `ZREM`), you'll see misses build up over time.
- `LPUSH` and `RPUSH` keep growing lists unless you also pop. `SADD` and `ZADD` stay within `collection_size`
  members, because members come from that pool.
- `SMEMBERS` and `HGETALL` return whole collections, so they get more expensive as `collection_size` and
  `hash_fields` grow.
- `SCAN` sends `SCAN 0 MATCH <prefix><type>:* COUNT n`. It's a fixed first-page scan, used as load.
- `ZRANGEBYSCORE` is sent as `ZRANGE key min max BYSCORE LIMIT 0 count`.
- Multi-key ops (`MGET`, `MSET`) pick plain, independent keys. In a cluster, set `dataset.hash_tag_groups` so
  their keys land in the same slot. See [cluster-and-sentinel.md](cluster-and-sentinel.md).

## TTL model

`dataset.ttl` controls expiry:

- `SET` (and preloaded strings) get `EX <seconds>` when the mode is `fixed` or `random`, for the `fraction` of
  writes that are selected. Other write operations don't set a TTL.
- `SETEX`, `GETEX` and `EXPIRE` always use a TTL. It's `seconds` for `fixed`, a random value in the range for
  `random`, and `seconds` when the mode is `none`.

## Output

`plgv run` prints totals, latency percentiles, misses, errors and client CPU. After that comes a per-operation
table with ops, share of the total, misses and errors. See `cli.md`.
