# Workload presets

A preset is a named starting point for a typical use: the operation mix, plus the dataset and
pub/sub settings that make it realistic. Presets are opt-in; with none chosen you get the plain
80/20 GET/SET default.

```bash
./bin/plgv presets                       # list them
./bin/plgv run -preset cache -duration 30s
./bin/plgv config -preset session        # print the effective settings
```

In YAML (`examples/preset-cache.yaml`):

```yaml
workload:
  preset: cache
dataset:
  key_count: 50000     # overrides the preset's value; the rest of the preset stays
```

**Layering:** defaults, then the preset, then your file, then command-line flags. Anything you
write wins over the preset. `-preset` on the command line is combined with a config file the same way.

In the web UI every preset is a card under **Typical applications** on the front page. Clicking one opens its
settings with the defaults already selected: the operations, the dataset settings, and pub/sub subscribers or
the worker count where the preset sets them. You can edit anything afterwards; editing the operations turns the
preset off and the workload becomes a custom one. The **Workload** list at the top of the settings (and **Start
from a preset** in the Workload card) switch to another preset, keeping your server, workers and duration. A link
such as `#preset=queue` opens that workload.

![The preset cards on the front page](../images/web_presets.png)

Every preset runs on a standalone server and in a cluster (the ones that need multi-key commands
set `hash_tag_groups: 16`; see [cluster-and-sentinel.md](cluster-and-sentinel.md)).

## cache

Read-heavy key/value cache. Zipf keys (a few hot ones), 512 B values with random 60 to 600 s expiry.
Mix: `GET` 80, `SET` 10, `MGET` 4 (5 keys), `DEL` 3, `EXPIRE` 3. Look for: hit ratio and the
effect of hot keys.

## session

User sessions as hashes. Mix: `HGET` 45, `EXPIRE` 20 (on hashes), `HSET` 15 (4 fields), `HGETALL` 15,
`DEL` 5. Look for: hash read latency, expiry churn.

## leaderboard

Sorted-set rankings on 1,000 boards (zipf, so a few are hot), 1,000 members each. Mix: `ZRANGE` 25
(top 10), `ZINCRBY` 20, `ZRANK` 20, `ZADD` 15, `ZSCORE` 15, `ZCARD` 5.

## queue

Work queues on a list and on a stream with a consumer group. Mix: `LPUSH` 25, `RPOP` 20, `XADD` 20,
`XCONSUME` 20 (read, then acknowledge), `LLEN` 5, `XPENDING` 5, `XLEN` 5. Misses mean the queue
was empty. See [advanced-commands.md](advanced-commands.md).

## rate-limiter

Fixed-window limits. A Lua script (`INCR`, `EXPIRE` on the first hit, compare against the limit)
70, `INCR` 25, `TTL` 5, on 10,000 hot keys.

## pubsub-fanout

Publishers (`PUBLISH` 95, `GET` 5) send to 32 channels; 8 subscribers receive every message. The
results show delivery latency and any lost messages.

## counters

Analytics: `INCR` 30, HyperLogLog `PFADD` 25 / `PFCOUNT` 10, bitmaps `SETBIT` 20 / `GETBIT` 10 /
`BITCOUNT` 5 (100,000-bit bitmaps).

## big-keys

A few big collections: 50 keys of 10,000 elements (hashes of 1,000 fields), 16 workers. Mix:
`HGETALL` 20, `LRANGE` 20 (1,000), `ZRANGE` 20 (1,000), `SMEMBERS` 15, `SADD` 10, `ZADD` 10, `HSET` 5.
Whole-collection reads of big keys show up as tail latency. Combine with the
[key-space analysis](key-space-analysis.md) to see the key sizes:
`./bin/plgv run -preset big-keys -keyspace`.

## mixed

The kitchen sink, with every family: strings, hashes, lists, sets, sorted sets, streams,
HyperLogLog, geo, bitmaps, a script, a transaction, `WATCH_INCR` and pub/sub (2 subscribers).
Zipf keys on 20,000 keys.
