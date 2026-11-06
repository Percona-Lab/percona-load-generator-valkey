# Walkthrough

A guided tour of plgv, from a first run to comparing two servers. Every command here was run against the
local Docker environment (`VALKEY_TEST_ENV.md`); output is trimmed. About 30 minutes.

Written steps stand in for videos: each step shows the command, what to look at and where to read more.

1. [Start a server and build plgv](#1-start-a-server-and-build-plgv)
2. [A first run](#2-a-first-run)
3. [Understand the result](#3-understand-the-result)
4. [A real workload with a preset](#4-a-real-workload-with-a-preset)
5. [The web UI](#5-the-web-ui)
6. [Find the limit with a sweep](#6-find-the-limit-with-a-sweep)
7. [Record a run and replay it](#7-record-a-run-and-replay-it)
8. [Compare two servers](#8-compare-two-servers)
9. [Encrypt and restrict access](#9-encrypt-and-restrict-access)
10. [Where next](#10-where-next)

## 1. Start a server and build plgv

```sh
make valkey-up          # Valkey on 127.0.0.1:6379
make build-local        # bin/plgv
./bin/plgv version
```

## 2. A first run

```sh
./bin/plgv run -duration 10s -workers 32
```

plgv preloads the keys, sends an 80% `GET` / 20% `SET` mix with 32 workers for 10 seconds and prints
throughput, latency percentiles and a table per operation. It also prints the **seed**: run again with
`-seed N` for the same sequence of operations.

## 3. Understand the result

```sh
./bin/plgv run -duration 10s -workers 32 -server-metrics -summary /tmp/run.json
```

`-server-metrics` is **off by default** and adds what the server saw (`INFO`, `SLOWLOG`, `LATENCY`; read-only):
the server's own operation count, hit ratio and memory, and **findings**: plain-language observations with
evidence and what to do. Read `metrics-and-server-stats.md` (what the numbers mean) and `insights.md` (each finding).
Latency is per command; with a fixed rate use `-target-ops 20000 -co` to include queueing delay.

## 4. A real workload with a preset

```sh
./bin/plgv presets                                   # the nine presets
./bin/plgv run -preset leaderboard -duration 15s -keyspace
```

A preset fills in the operations and dataset of a typical use (cache, session store, leaderboard, queue,
rate limiter, pub/sub fan-out, counters, big keys, mixed). `-keyspace` inspects what the run left behind: key
types, sizes, expiry and the biggest keys. See `presets.md` and `key-space-analysis.md`. Your own commands
run as a workload too: `raw-injector.md`.

## 5. The web UI

```sh
./bin/plgv web          # http://127.0.0.1:8080
```

Settings at three levels (Basic, Advanced, Expert), live charts while a run is going, latency analysis, a
Server and Slowlog tab, run history, **Compare Runs** and Trends (a regression flag when the newest run is
worse than the last few). The UI binds to loopback and has no login: read `security-tls-acl.md` before
exposing it. Start with `web-ui.md`.

## 6. Find the limit with a sweep

```sh
./bin/plgv run -preset cache -duration 10s -sweep-workers 4,16,64,128
```

One run per level, each stored, with a table of throughput and p99 per level. The knee of the curve is
where more workers stop helping: either the server is saturated or the load generator is (check its CPU
line). See `load-profiles-and-sweep.md` and `performance-tuning.md`.

## 7. Record a run and replay it

```sh
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -ops 50000 -deterministic \
  -record /tmp/session.jsonl.zst -record-checksums
./bin/plgv replay -info /tmp/session.jsonl.zst
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -preload-only   # restore the starting data
./bin/plgv replay -speed 0 -require-data /tmp/session.jsonl.zst
```

`-deterministic` makes the traffic reproducible and prints its fingerprint; `-record` writes every command with
its outcome and a checksum of its reply. The replay checks the target holds the starting data, sends the same
commands (the output says `hash matches`) and compares every status and reply with the recording: with one
worker, nothing differs. See `deterministic-mode.md` and `record-and-replay.md`.

## 8. Compare two servers

```sh
docker run -d --rm --name plgv-v8 -p 127.0.0.1:6390:6379 valkey/valkey:8.0       # a second server, another version
docker exec plgv-valkey valkey-cli flushall                                       # the step-7 replay changed the data
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -preload-only -addr 127.0.0.1:6379
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -preload-only -addr 127.0.0.1:6390
./bin/plgv replay -targets v9=127.0.0.1:6379,v8=127.0.0.1:6390 -speed 0 -summary /tmp/both.json /tmp/session.jsonl.zst
./bin/plgv compare /tmp/both.json
docker stop plgv-v8
```

Each target starts from the same preloaded data (the replay checks it). The comparison gives a verdict per target, what is better or worse beyond a tolerance, and whether it can be
trusted (same recording, same starting data, same options). Open the files in the **Replay Comparison** tab
too. See `multi-target.md` and `replay-comparison.md`. Traffic from a production server can be imported:
`traffic-import.md`.

## 9. Encrypt and restrict access

```sh
make valkey-tls-up      # mutual TLS + ACL users on 127.0.0.1:6444 (throwaway certificates)
PLGV_USERNAME=bench PLGV_PASSWORD=benchpass ./bin/plgv run -addr 127.0.0.1:6444 \
  -tls-ca .test-certs/ca.crt -tls-cert .test-certs/client.crt -tls-key .test-certs/client.key -preset session -duration 5s
```

The `bench` user may run a workload but not `FLUSHALL` or `DEBUG`; try `reader` to see `NOPERM` errors counted
without stopping the run. See `security-tls-acl.md`.

## 10. Where next

- Run in a container or a Job: `deployment.md`.
- Cluster and Sentinel, with a failover during the run: `cluster-and-sentinel.md`.
- Tune the generator and the server: `performance-tuning.md`.
- Something failed: `troubleshooting.md`. Supported servers: `compatibility.md`.
