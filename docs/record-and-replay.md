# Record and replay

plgv can write down every command a run sends, then send the same commands to a server again, in the same order
and, if you want, at the same pace. You can use it to:

- re-run an exact workload on another server, another Valkey version or another configuration
- send a **reads-only** copy of a workload to a replica or a staging server
- check whether a change made a command return something different (reply checksums)
- repeat a workload several times to see cold and warm behavior

Both features are **off by default**. This page covers the command line. The web UI does the same thing with
**Record a workload** and **Replay a recorded workload** ([record-and-replay-web-ui.md](record-and-replay-web-ui.md)),
and it writes the same files. You can compare replay results in the **Replay Comparison** tab or with
`plgv compare` (see [replay-comparison.md](replay-comparison.md)).

## Record a run

```sh
plgv run -preset session -duration 30s -workers 8 -seed 7 \
  -record run.jsonl.zst -record-checksums
```

| Flag | YAML (`record:`) | Meaning |
|---|---|---|
| `-record FILE` | `path` | Write every command the workers send to this file. If it's empty, plgv doesn't record. |
| `-record-outcomes` | `outcomes` | Also keep each command's status (`ok`, `miss`, or `error` with its class) and its latency. |
| `-record-checksums` | `checksums` | Also keep a size and a checksum of each reply. This implies outcomes. |
| `-record-compress` | `compression` | `auto` (the default: `.zst` is zstd, `.gz` is gzip, anything else is none), `none`, `zstd` or `gzip`. |
| `-record-max-mb N` | `max_mb` | Stop recording after N uncompressed MB (0 means no limit). The file is marked `truncated`. |
| `-record-backpressure` | `backpressure` | `fail` (the default) or `block`. See below. |
| `-record-fingerprint=false` | `data_fingerprint` | Skip the starting-data fingerprint. It's on by default when recording. |

`examples/record.yaml` shows the same settings in a config file.

plgv records every command that reaches the server through a worker. For each one it keeps the literal
arguments, the worker that sent it, its time offset from the start of the run, and the **group** it was sent in
(a pipelined batch, or one `MULTI`/`EXEC`/`WATCH` sequence on its own connection).

It doesn't record preload, server-metrics polling, the key-space analysis, or pub/sub subscribers. The `PUBLISH`
commands are recorded, but nobody listens on a replay.

The file holds your data, with keys and values in clear text. plgv creates it with mode `0600` and never writes
the target address, user name or password into it. It also refuses to overwrite a file that isn't a plgv
recording.

Recording costs almost nothing. A 64-worker GET/SET run did 71,600 ops/s while recording 573,093 commands to a
zstd file (109 MiB raw, 12 MiB on disk), compared with 70,900 ops/s without recording (local Valkey 9.1.3).

### Backpressure: when the file writer falls behind

Commands go to one writer through a bounded queue (65,536 commands).

- **`fail`** (the default): if the queue fills, the recording stops and is marked `failed`. Commands are never
  dropped silently, and the file keeps everything that was queued before the stop, which is a valid prefix of
  every worker's stream. The run itself isn't affected.
- **`block`**: workers wait for the writer, so every command is recorded, but throughput may drop. The result
  and the trailer report how many times workers waited and for how long.

Use `block` when you want a complete recording on a slow disk, and `fail` when the benchmark's own numbers
matter more than the recording.

## Look at a recording

```sh
plgv replay -info run.jsonl.zst
```

This prints the header (plgv version, server version, mode, seed, workers, mix), the status (`complete`,
`truncated`, `failed`, or `incomplete` when the file has no trailer), the command counts and a **commands hash**.
`-info` re-reads the whole file and recomputes the hash, so if the file was edited after recording, it's
reported as changed. It prints command names only, never arguments.

## Replay it

```sh
plgv replay -addr 127.0.0.1:6379 run.jsonl.zst
```

| Flag | Meaning |
|---|---|
| `-addr`, `-client`, `-mode`, `-sentinel-master`, `-replica-reads` | The target, the same as in `plgv run`. Credentials come from `PLGV_USERNAME` and `PLGV_PASSWORD`. |
| `-speed X` | `1` (the default) replays at the recorded pace, `2` is twice as fast, and `0` sends as fast as the server answers. |
| `-reads-only` | Skip every group that holds a command that isn't known to be read-only (see below). |
| `-iterations N`, `-iteration-delay D` | Replay N times, D apart, and print a line per iteration. |
| `-require-data` | Stop before replaying if the target doesn't hold the recording's starting data. |
| `-allow-dangerous` | Replay `FLUSHALL`, `DEBUG`, `CONFIG SET` and similar commands, if the file holds them. |
| `-summary FILE` | Write the result as JSON (totals and comparisons, with no arguments). |
| `-info` | Describe the recording and exit. |

The replay streams the file and never loads it whole. A reader splits it into groups and hands each one to the
worker that sent it, through a bounded queue. That keeps memory flat for a file of any size, and a slow target
slows the reader down instead of growing a buffer. Every worker keeps its own order, and every group is sent the
way it was recorded: pipelined together, or on one dedicated connection for `WATCH`/`MULTI`/`EXEC`.

**Order between workers isn't preserved.** If two workers touched the same key, they may interleave differently
on a replay. With `-workers 1` (or workers that never share keys), the replay is exact.

### Same commands, proven

The result ends with `commands: the replay sent exactly the recorded commands (hash matches)`. The hash is a
sha256 over each worker's commands in order, and it's computed the same way when recording and replaying. It
won't match for a `-reads-only` replay, one that was stopped early, or a file that was changed.

### Same starting data

When you record, plgv stores a fingerprint of the data it started with. That's the key count and a digest of a
sample of keys (name, type and size, but no values). The sample is chosen by key name, not scan order, so two
servers that hold the same keys agree. In a cluster, every primary is scanned. When you replay, plgv takes the
same fingerprint of the target and prints whether it matches:

```
start data:  DIFFERENT from the recording:
  - the recording started with 2000 keys, this target has 1464
```

A mismatch is a warning. Use `-require-data` to make it an error. It's only a warning because replaying onto the
data a run *left behind* is a common mistake, since the data the run changed is no longer the data it started
with. To put the starting data back, clear the keys and preload again with the recording's settings and seed
(`dataset.preload`), or restore a snapshot.

There are limits. Only a sample is digested, so a change to an unsampled key goes unnoticed. On a server with
more than 2 million keys, the digest is skipped and only key counts are compared (the output tells you).
Scanning is a read, but on a very large server you should use `-record-fingerprint=false`.

### Outcomes and checksums

If you record with `-record-outcomes`, the replay can compare each command with what it did when it was
recorded. You'll see something like `outcomes: 46749 compared, 0 with a different status`, with the recorded
latency shown next to the replay's. With `-record-checksums`, it also compares a checksum of each reply, for
example `0 of 36517 reply checksums differ`. The first differences are listed with the command name and number,
never its arguments.

Expect differences for replies that aren't a function of the data: `TTL`, `SPOP`, `SRANDMEMBER`, `RANDOMKEY`,
stream ids, replies built from the clock, and, with several workers, anything that depends on the order of two
workers' commands. Checksums for map replies also can't be compared between the two client libraries, so
compare recordings and replays that used the same library.

### Reads-only

`-reads-only` skips a whole group if any command in it isn't on plgv's list of read-only commands (`GET`,
`HGETALL`, `LRANGE`, `ZRANGE`, `SMEMBERS`, `XRANGE`, `GEOSEARCH`, `BITCOUNT`, `EVAL_RO`, ...). A command that
isn't on the list counts as a write, so a command plgv doesn't know is skipped, not sent. `XREADGROUP`, `EVAL`,
`PFCOUNT` and `WATCH`/`MULTI` groups are skipped too. The result says how many commands were skipped. Use it
against a replica or a staging copy that you don't want changed.

### Safety

A recording made by plgv only holds commands that plgv's own operations sent, but a file can be edited, so
replay checks every command again. Harmful commands (`FLUSHALL`, `FLUSHDB`, `DEBUG`, `CONFIG SET`, `SHUTDOWN`,
`REPLICAOF`, ...) stop the replay before it sends anything, unless you pass `-allow-dangerous`. Commands that
would change the replay's own connections (`SELECT`, `AUTH`, `HELLO`, `SUBSCRIBE`) are never replayed. plgv
refuses a recording that is `incomplete` (no trailer) or whose commands don't match its hash.

## Reading the result

```
replayed:   run.jsonl.zst (recording complete)
mode:       as fast as possible, 1 iteration(s)
ops:        10918 (0 errors, 548 misses)
elapsed:    2.00s
throughput: 5471 ops/s
latency:    mean 182µs  p50 176µs  p95 201µs  p99 303µs  max 6.507ms
commands:   the replay sent exactly the recorded commands (hash matches)
outcomes:   10918 compared, 0 with a different status; 0 of 10370 reply checksums differ
recorded:   p50 176µs  p99 258µs (replay above)
```

A paced replay (`-speed` above 0) also prints `pace: the replay fell behind the recording by up to ...` when the
target couldn't keep up with the recorded timing. The exit code is 1 when a command failed (or, if the
recording has outcomes, when a status differs from the recording), and 0 otherwise.

## Format

A recording is one file with a header line, one JSON line per command, and a trailer line. It can be compressed
with zstd or gzip, which plgv detects from the first bytes.

A command line has `seq`, `worker`, `worker_seq`, `offset_us`, `group`, `conn` (`dedicated` for
connection-bound commands), `cmd` (the arguments, where any that aren't valid UTF-8 are base64 and listed in
`b64`) and, with outcomes, `outcome`. The trailer holds the status, counts by command and worker, and the
commands hash.

The format is versioned (`plgv-workload-record/1`). plgm's recordings are for MongoDB and can't be replayed
here.

A few related pieces:

- `plgv import-traffic` converts traffic captured from `MONITOR` or the slowlog into this format
  (`traffic-import.md`).
- A deterministic run's traffic fingerprint equals its recording's commands hash (`deterministic-mode.md`).
- You can replay on several servers and compare them with `-targets` (`multi-target.md`).
