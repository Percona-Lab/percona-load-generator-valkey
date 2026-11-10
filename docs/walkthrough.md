# Walkthrough

This is a guided tour of plgv, from your first run to comparing two servers. It takes about 30 minutes. I ran
every command against the local Docker environment (`VALKEY_TEST_ENV.md`), and I trimmed the output to keep
things short.

Each step shows the command, what to look at, and where to read more.

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

plgv preloads the keys, then sends an 80% `GET` / 20% `SET` mix from 32 workers for 10 seconds. When it's done
it prints throughput, latency percentiles and a table per operation. It also prints the **seed**. Run again
with `-seed N` and you'll get the same sequence of operations.

## 3. Understand the result

```sh
./bin/plgv run -duration 10s -workers 32 -server-metrics -summary /tmp/run.json
```

`-server-metrics` is **off by default**. When you turn it on, the report also includes what the server saw
(it reads `INFO`, `SLOWLOG` and `LATENCY`, and nothing else): the server's own operation count, hit ratio and
memory. It also adds **findings**, which are plain-language notes with the evidence behind them and a
suggestion for what to do. `metrics-and-server-stats.md` explains what the numbers mean, and `insights.md`
covers each finding.

Latency is measured per command. If you run at a fixed rate, add `-target-ops 20000 -co` so queueing delay is
included too.

## 4. A real workload with a preset

```sh
./bin/plgv presets                                   # the nine presets
./bin/plgv run -preset leaderboard -duration 15s -keyspace
```

A preset sets up the operations and dataset for a typical use: cache, session store, leaderboard, queue,
rate limiter, pub/sub fan-out, counters, big keys, or a mix. `-keyspace` looks at what the run left behind,
including key types, sizes, expiry and the biggest keys. See `presets.md` and `key-space-analysis.md`. You can
also run your own commands as a workload, which `raw-injector.md` explains.

## 5. The web UI

```sh
./bin/plgv web          # http://127.0.0.1:8080
```

The UI has settings at three levels (Basic, Advanced, Expert), live charts while a run is going, latency
analysis, a Server and Slowlog tab, run history, **Compare Runs** and Trends. Trends flags a regression when
the newest run is worse than the last few.

The UI only listens on loopback and has no login, so read `security-tls-acl.md` before you expose it. `web-ui.md`
is the place to start.

## 6. Find the limit with a sweep

```sh
./bin/plgv run -preset cache -duration 10s -sweep-workers 4,16,64,128
```

This does one run per level, stores each one, and prints a table of throughput and p99 for every level. Look for
the knee of the curve, which is where more workers stop helping. Either the server is saturated or the load
generator is, so check its CPU line. See `load-profiles-and-sweep.md` and `performance-tuning.md`.

## 7. Record a run and replay it

```sh
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -ops 50000 -deterministic \
  -record /tmp/session.jsonl.zst -record-checksums
./bin/plgv replay -info /tmp/session.jsonl.zst
./bin/plgv run -preset session -keys 5000 -seed 7 -workers 1 -preload-only   # restore the starting data
./bin/plgv replay -speed 0 -require-data /tmp/session.jsonl.zst
```

`-deterministic` makes the traffic reproducible and prints its fingerprint. `-record` writes every command to
the file, along with its outcome and a checksum of its reply.

The replay first checks that the target holds the starting data. Then it sends the same commands (the output
says `hash matches`) and compares every status and reply with the recording. With one worker, nothing differs.
See `deterministic-mode.md` and `record-and-replay.md`.

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

Each target starts from the same preloaded data, and the replay checks that. The comparison gives you a verdict
per target, shows what's better or worse beyond a tolerance, and tells you whether you can trust the result
(same recording, same starting data, same options). You can open the same files in the **Replay Comparison**
tab too. See `multi-target.md` and `replay-comparison.md`. If you want to use traffic from a production
server, `traffic-import.md` shows how.

## 9. Encrypt and restrict access

```sh
make valkey-tls-up      # mutual TLS + ACL users on 127.0.0.1:6444 (throwaway certificates)
PLGV_USERNAME=bench PLGV_PASSWORD=benchpass ./bin/plgv run -addr 127.0.0.1:6444 \
  -tls-ca .test-certs/ca.crt -tls-cert .test-certs/client.crt -tls-key .test-certs/client.key -preset session -duration 5s
```

The `bench` user can run a workload but not `FLUSHALL` or `DEBUG`. Try the `reader` user to see `NOPERM` errors
get counted without stopping the run. See `security-tls-acl.md`.

## 10. Where next

- To run in a container or a Job, see `deployment.md`.
- For cluster and Sentinel, including a failover during the run, see `cluster-and-sentinel.md`.
- To tune the generator and the server, see `performance-tuning.md`.
- If something went wrong, try `troubleshooting.md`. For supported servers, see `compatibility.md`.
