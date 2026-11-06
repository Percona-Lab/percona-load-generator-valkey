# Deterministic mode

A normal run is time-bound, so two runs with the same seed send the same *sequence* of operations per
worker but different *totals*. Deterministic mode fixes the total: the same seed, operation budget and
workload send exactly the same commands, and the run prints a **traffic fingerprint** that proves it.

Use it to compare servers or versions with identical traffic, to check that a change to plgv did not
change what it sends, and to make a benchmark repeatable in CI. It is **off by default**.

## Turn it on

```sh
plgv run -preset session -seed 7 -ops 100000 -deterministic -workers 8
```

In the web UI tick Advanced and open the *Seed and deterministic* category.

```yaml
run:
  seed: 7              # must be non-zero
  op_budget: 100000    # exactly this many operations, split between the workers
  deterministic: true
```

`-ops` / `run.op_budget` works without `deterministic`: it replaces `-duration`, so the run ends
when the operations are sent, however long that takes (Ctrl-C stops it early). The budget is split
between the workers (`budget / workers`, the first `budget % workers` workers send one more).
`deterministic` adds the fingerprint and the checks below.

## The fingerprint

```
ops:        100000
traffic:    100000 commands, fingerprint bbd80d13156305b29e57798012309ebc32f73e517044143e51e1da0c066bf858
```

It is a sha256 over every worker's commands, in order, computed exactly like the commands hash of a
recording. So a deterministic run that is also recorded (`-record`) has the same hash in its recording,
and a replay of that recording reports the same hash again. The fingerprint is also in the JSON summary
(`traffic`: `hash`, `commands`, `seed`, `op_budget`).

It does **not** depend on the server, the time the run takes, `-target-ops`, or the load on the machine.
It **does** depend on everything that defines the traffic: the seed, the budget, the number of workers,
the mix, the dataset settings (key count, distribution, value size, TTL, hash tags) and, when the mix has
composite or blocking operations (`TXN`, `WATCH_INCR`, `BLPOP`, ...), the pipeline depth, because within one
batch plain commands are sent before composite ones.

## What is refused

Operations whose commands depend on the clock or on what the server replied cannot be reproduced, so
deterministic mode rejects them with a message naming the operation:

- `PUBLISH`, `SPUBLISH`: the message starts with the send time;
- `XCONSUME`: acknowledges the stream ids the server returned;
- `WATCH_INCR`: writes the value it just read;
- `RAW` commands with `{now}`.

It also needs a fixed worker count and a closed loop (`run.load_profile: fixed`, `run.loop: closed`) and
cannot be combined with a sweep. The workload fingerprint used for Trends ignores `op_budget` and
`deterministic`, like the duration and the seed.

## Same traffic on two servers

Combine it with a recording to replay the exact traffic on any server (see `record-and-replay.md`
and `multi-target.md`), or run the same deterministic command against each server and check the
fingerprints match:

```sh
plgv run -addr a:6379 -preset session -seed 7 -ops 200000 -deterministic | grep traffic
plgv run -addr b:6379 -preset session -seed 7 -ops 200000 -deterministic | grep traffic
```
