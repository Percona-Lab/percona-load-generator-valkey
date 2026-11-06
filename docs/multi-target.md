# Multi-target replay

Replay one recording on several servers and compare the results: two Valkey versions, a primary and
a replica, before and after a configuration change.

```sh
plgv replay -targets v8=10.0.0.1:6379,v9=10.0.0.2:6379 -speed 0 -summary both.json run.jsonl.zst
```

`-targets` takes `name=host:port` entries (the name is optional, and must be unique) and needs at
least two. Up to 8 are allowed. Everything else is as for a single replay (`-speed`, `-reads-only`,
`-iterations`, `-require-data`, `-client`, credentials from `PLGV_USERNAME` / `PLGV_PASSWORD`).

## Sequential or concurrent

| Mode | What it does | Use it when |
|---|---|---|
| sequential (default) | Replays on each target in turn. Every replay has the machine to itself. | You compare capacity or latency: the servers do not disturb each other. |
| `-concurrent` | Replays on all targets at the same moment. | You want identical conditions (the same minute of the day), or want to save time. The load generator now shares the machine between replays, so keep `-speed` low enough that it is not the bottleneck. |

A target that cannot be reached or fails does not stop the others: it is reported as "did not run"
with the reason, and the comparison says so.

## Start every target from the same data

A replay changes data (unless `-reads-only`), so replaying twice on one server starts the second
replay from the first one's result. Give each target the same starting data first. With a
recording made by plgv, preload from the same settings:

```sh
plgv run -preset session -keys 5000 -seed 7 -preload-only -addr 10.0.0.1:6379
plgv run -preset session -keys 5000 -seed 7 -preload-only -addr 10.0.0.2:6379
```

`-preload-only` writes the dataset and exits. Each replay then checks its target against the
recording's starting-data fingerprint and prints `the target holds the data the recording started
with`, or what differs. The fingerprint covers every key in the database, so replay on a server that
holds only the benchmark data.

## Result

Each target gets the usual replay output, then a comparison of every target with the first:

```
compare:    v8 against baseline v9: EQUIVALENT (Valkey 9.1.3 vs 8.0.12)
  metric                     v9             v8    change
  Throughput (ops/s)        5506.99        5457.42     -0.9%
  Latency p99 (ms)           0.34           0.39    +14.6%
```

`-summary FILE` writes one JSON file with every target's summary and the comparisons. See
`replay-comparison.md` to read it, and to compare files from separate runs.
