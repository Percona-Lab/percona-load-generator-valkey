# Multi-target replay

You can replay one recording on several servers and compare the results. That's handy for two Valkey versions,
a primary and a replica, or before and after a configuration change.

```sh
plgv replay -targets v8=10.0.0.1:6379,v9=10.0.0.2:6379 -speed 0 -summary both.json run.jsonl.zst
```

`-targets` takes `name=host:port` entries. The name is optional but has to be unique, and you need at least two
targets (up to 8). Everything else works as it does for a single replay: `-speed`, `-reads-only`,
`-iterations`, `-require-data`, `-client`, and credentials from `PLGV_USERNAME` and `PLGV_PASSWORD`.

## Sequential or concurrent

| Mode | What it does | Use it when |
|---|---|---|
| sequential (default) | Replays on each target in turn, so every replay has the machine to itself. | You're comparing capacity or latency and don't want the servers to disturb each other. |
| `-concurrent` | Replays on all targets at the same moment. | You want identical conditions (the same minute of the day), or you want to save time. The load generator now shares the machine between replays, so keep `-speed` low enough that it doesn't become the bottleneck. |

If a target can't be reached or fails, the others keep going. It's reported as "did not run" with the reason,
and the comparison says so.

## Start every target from the same data

A replay changes data (unless you use `-reads-only`), so if you replay twice on one server, the second replay
starts from the first one's result. Give each target the same starting data first. If plgv made the recording,
you can preload from the same settings:

```sh
plgv run -preset session -keys 5000 -seed 7 -preload-only -addr 10.0.0.1:6379
plgv run -preset session -keys 5000 -seed 7 -preload-only -addr 10.0.0.2:6379
```

`-preload-only` writes the dataset and exits. Each replay then checks its target against the recording's
starting-data fingerprint. It prints `the target holds the data the recording started with`, or tells you what
differs. The fingerprint covers every key in the database, so replay on a server that holds only the benchmark
data.

## Result

Each target gets the usual replay output. After that comes a comparison of every target with the first one:

```
compare:    v8 against baseline v9: EQUIVALENT (Valkey 9.1.3 vs 8.0.12)
  metric                     v9             v8    change
  Throughput (ops/s)        5506.99        5457.42     -0.9%
  Latency p99 (ms)           0.34           0.39    +14.6%
```

`-summary FILE` writes one JSON file with every target's summary and the comparisons. See
`replay-comparison.md` for how to read it, and how to compare files from separate runs.
