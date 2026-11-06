# Record and replay

plgv can write down every command a run sends and send the same commands to a server again, in
the same order and, if you want, at the same pace. Use it to:

- re-run an exact workload on another server, another Valkey version or another configuration;
- send a **reads-only** copy of a workload to a replica or a staging server;
- check whether a change made a command return something different (reply checksums);
- repeat a workload several times to see cold and warm behavior.

Both features are **off by default** and are command-line features: the web UI refuses
`record` settings, because a web request must not choose a file for the server to write.
(Replay results are compared in the **Replay Comparison** tab or with `plgv compare`: see `replay-comparison.md`.)

## Record a run

```sh
plgv run -preset session -duration 30s -workers 8 -seed 7 \
  -record run.jsonl.zst -record-checksums
```

| Flag | YAML (`record:`) | Meaning |
|---|---|---|
| `-record FILE` | `path` | Write every command the workers send to this file. Empty = not recording. |
| `-record-outcomes` | `outcomes` | Also keep each command's status (`ok`, `miss`, `error` with its class) and latency. |
| `-record-checksums` | `checksums` | Also keep a size and a checksum of each reply (implies outcomes). |
| `-record-compress` | `compression` | `auto` (default: `.zst` is zstd, `.gz` is gzip, else none), `none`, `zstd`, `gzip`. |
| `-record-max-mb N` | `max_mb` | Stop recording after N uncompressed MB (0 = no limit). The file is marked `truncated`. |
| `-record-backpressure` | `backpressure` | `fail` (default) or `block`, see below. |
| `-record-fingerprint=false` | `data_fingerprint` | Skip the starting-data fingerprint (on by default when recording). |

`examples/record.yaml` shows the same settings in a config file.

What is recorded: every command that reaches the server through a worker, with its literal
arguments, the worker that sent it, its time offset from the start of the run, and the **group** it
was sent in (a pipelined batch, or one `MULTI`/`EXEC`/`WATCH` sequence on its own connection).
Not recorded: preload, server-metrics polling, the key-space analysis, and pub/sub subscribers
(the `PUBLISH` commands are recorded; nobody listens on a replay).

The file holds your data: values and keys in clear text. plgv creates it with mode `0600`, and
never writes the target address, user name or password into it. It refuses to overwrite a file
that is not a plgv recording.

Recording costs almost nothing: a 64-worker GET/SET run did 71,600 ops/s while recording
573,093 commands to a zstd file (109 MiB raw, 12 MiB on disk), against 70,900 ops/s without
recording (local Valkey 9.1.3).

### Backpressure: when the file writer falls behind

Commands go to one writer through a bounded queue (65,536 commands).

- **`fail`** (default): if the queue fills, the recording stops and is marked `failed`. Commands are
  never dropped silently, and the file keeps everything queued before the stop, which is a valid
  prefix of every worker's stream. The run itself is not affected.
- **`block`**: workers wait for the writer, so every command is recorded, and throughput may drop. The
  result and the trailer report how many times workers waited and for how long.

Use `block` for a complete recording on a slow disk, and `fail` when the benchmark's own numbers
matter more than the recording.

## Look at a recording

```sh
plgv replay -info run.jsonl.zst
```

prints the header (plgv version, server version, mode, seed, workers, mix), the status
(`complete`, `truncated`, `failed`, or `incomplete` when the file has no trailer), the command counts
and a **commands hash**. `-info` re-reads the whole file and recomputes the hash, so a file that was
edited after recording is reported as changed. It prints command names only, never arguments.

## Replay it

```sh
plgv replay -addr 127.0.0.1:6379 run.jsonl.zst
```

| Flag | Meaning |
|---|---|
| `-addr`, `-client`, `-mode`, `-sentinel-master`, `-replica-reads` | The target, as in `plgv run`. Credentials come from `PLGV_USERNAME` and `PLGV_PASSWORD`. |
| `-speed X` | `1` (default) replays at the recorded pace, `2` twice as fast, `0` sends as fast as the server answers. |
| `-reads-only` | Skip every group that holds a command not known to be read-only (see below). |
| `-iterations N`, `-iteration-delay D` | Replay N times, D apart, with a line per iteration. |
| `-require-data` | Stop before replaying if the target does not hold the recording's starting data. |
| `-allow-dangerous` | Replay `FLUSHALL`, `DEBUG`, `CONFIG SET` ... if the file holds them. |
| `-summary FILE` | Write the result as JSON (totals and comparisons, no arguments). |
| `-info` | Describe the recording and exit. |

The replay streams the file; it never loads it whole. A reader splits it into groups and hands each
to the worker that sent it through a bounded queue, so memory stays flat for a file of any size, and a
slow target slows the reader instead of growing a buffer. Every worker keeps its own order and every
group is sent the way it was recorded: pipelined together, or on one dedicated connection for
`WATCH`/`MULTI`/`EXEC`.

**Order between workers is not preserved.** Two workers that touched the same key may interleave
differently on a replay. With `-workers 1` (or workers that never share keys) the replay is exact.

### Same commands, proven

The result ends with `commands: the replay sent exactly the recorded commands (hash matches)`.
The hash is a sha256 over each worker's commands in order, computed the same way when recording and
replaying. It does not match for a `-reads-only` replay, for one that was stopped early, or for a
file that was changed.

### Same starting data

When recording, plgv stores a fingerprint of the data it started with: the key count and a digest of
a sample of keys (name, type and size; no values). The sample is chosen by key name, not scan order,
so two servers that hold the same keys agree. In a cluster every primary is scanned. When replaying,
plgv takes the same fingerprint of the target and prints whether it matches:

```
start data:  DIFFERENT from the recording:
  - the recording started with 2000 keys, this target has 1464
```

A mismatch is a warning (use `-require-data` to make it an error), because replaying onto the
data a run *left behind* is a common mistake: the data the run changed is no longer the data it
started with. To put the starting data back, clear the keys and preload again with the recording's
settings and seed (`dataset.preload`), or restore a snapshot.

Limits: only a sample is digested, so a change to an unsampled key goes unnoticed; on a server with
more than 2 million keys the digest is skipped and only key counts are compared (the output says so).
Scanning is a read, but on a very large server use `-record-fingerprint=false`.

### Outcomes and checksums

A recording made with `-record-outcomes` lets the replay compare each command with what it did when
recorded: `outcomes: 46749 compared, 0 with a different status`, and the recorded latency next to the
replay's. With `-record-checksums` it also compares a checksum of each reply:
`0 of 36517 reply checksums differ`. The first differences are listed with the command name and
number, never its arguments.

Expect differences for replies that are not a function of the data: `TTL`, `SPOP`,
`SRANDMEMBER`, `RANDOMKEY`, stream ids, replies built from the clock, and, with several workers,
anything that depends on the order of two workers' commands. Checksums are also not comparable
between the two client libraries for map replies; compare recordings and replays that used the same
library.

### Reads-only

`-reads-only` skips a whole group if any command in it is not on plgv's list of read-only commands
(`GET`, `HGETALL`, `LRANGE`, `ZRANGE`, `SMEMBERS`, `XRANGE`, `GEOSEARCH`, `BITCOUNT`, `EVAL_RO`, ...).
A command that is not on the list counts as a write, so a command plgv does not know is skipped,
not sent. `XREADGROUP`, `EVAL`, `PFCOUNT` and `WATCH`/`MULTI` groups are skipped. The result says how
many commands were skipped. Use it against a replica or a staging copy you do not want changed.

### Safety

A recording made by plgv only holds commands plgv's own operations sent, but a file can be edited. Replay
checks every command again: harmful commands (`FLUSHALL`, `FLUSHDB`, `DEBUG`, `CONFIG SET`, `SHUTDOWN`,
`REPLICAOF`, ...) stop the replay before it sends anything unless you pass `-allow-dangerous`, and
commands that would change the replay's own connections (`SELECT`, `AUTH`, `HELLO`, `SUBSCRIBE`) are
never replayed. A recording that is `incomplete` (no trailer) or whose commands do not match its hash
is refused.

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

A paced replay (`-speed` above 0) also prints `pace: the replay fell behind the recording by up to
...` when the target could not keep up with the recorded timing. The exit code is 1 when a command
failed (with outcomes in the recording: when a status differs from the recording), 0 otherwise.

## Format

One file: a header line, one JSON line per command, a trailer line, optionally compressed with zstd or
gzip (detected from the first bytes). A command line has `seq`, `worker`, `worker_seq`, `offset_us`,
`group`, `conn` (`dedicated` for connection-bound commands), `cmd` (the arguments; any that are not
valid UTF-8 are base64 and listed in `b64`) and, with outcomes, `outcome`. The trailer holds the
status, counts by command and worker, and the commands hash. The format is versioned
(`plgv-workload-record/1`); plgm's recordings are for MongoDB and cannot be replayed here.
Traffic captured from `MONITOR` or the slowlog is converted into this format by `plgv import-traffic` (`traffic-import.md`); a deterministic run's traffic fingerprint equals its recording's commands hash (`deterministic-mode.md`); several servers are replayed and compared with `-targets` (`multi-target.md`).
