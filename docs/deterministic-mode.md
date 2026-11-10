# Deterministic mode

A normal run is bound by time. Two runs with the same seed send the same *sequence* of operations per worker, but
the *totals* differ. Deterministic mode fixes the total. With the same seed, operation budget and workload, plgv
sends exactly the same commands, and the run prints a **traffic fingerprint** that proves it.

Use it to compare servers or versions with identical traffic, to check that a change to plgv didn't change what
it sends, or to make a benchmark repeatable in CI. It's **off by default**.

## Turn it on

```sh
plgv run -preset session -seed 7 -ops 100000 -deterministic -workers 8
```

In the web UI, switch to Advanced and open the *Seed and deterministic* category.

```yaml
run:
  seed: 7              # must be non-zero
  op_budget: 100000    # exactly this many operations, split between the workers
  deterministic: true
```

`-ops` (or `run.op_budget`) also works without `deterministic`. It replaces `-duration`, so the run ends once
the operations are sent, however long that takes (Ctrl-C stops it early). The budget is split between the
workers: each gets `budget / workers`, and the first `budget % workers` workers send one more. `deterministic`
adds the fingerprint and the checks below.

## The fingerprint

```
ops:        100000
traffic:    100000 commands, fingerprint bbd80d13156305b29e57798012309ebc32f73e517044143e51e1da0c066bf858
```

The fingerprint is a sha256 over every worker's commands, in order. It's computed exactly like the commands hash
of a recording, so if you also record a deterministic run (`-record`), the recording has the same hash, and a
replay of that recording reports the same hash again. The fingerprint is also in the JSON summary, under
`traffic` (`hash`, `commands`, `seed`, `op_budget`).

It does **not** depend on the server, how long the run takes, `-target-ops`, or the load on the machine.

It **does** depend on everything that defines the traffic: the seed, the budget, the number of workers, the mix,
and the dataset settings (key count, distribution, value size, TTL, hash tags). If the mix has composite or
blocking operations (`TXN`, `WATCH_INCR`, `BLPOP`, ...), it also depends on the pipeline depth, because within
one batch plain commands are sent before composite ones.

## What is refused

Some operations build their commands from the clock or from what the server replied, so they can't be
reproduced. Deterministic mode rejects them with a message that names the operation:

- `PUBLISH` and `SPUBLISH`, because the message starts with the send time
- `XCONSUME`, because it acknowledges the stream ids the server returned
- `WATCH_INCR`, because it writes the value it just read
- `RAW` commands that use `{now}`

Deterministic mode also needs a fixed worker count and a closed loop (`run.load_profile: fixed`,
`run.loop: closed`), and you can't combine it with a sweep. The workload fingerprint that Trends uses ignores
`op_budget` and `deterministic`, just like it ignores the duration and the seed.

## Same traffic on two servers

You can combine it with a recording to replay the exact traffic on any server (see `record-and-replay.md` and
`multi-target.md`). Or run the same deterministic command against each server and check that the fingerprints
match:

```sh
plgv run -addr a:6379 -preset session -seed 7 -ops 200000 -deterministic | grep traffic
plgv run -addr b:6379 -preset session -seed 7 -ops 200000 -deterministic | grep traffic
```
