# Performance tuning

Two questions: is **plgv** fast enough that it is not the bottleneck, and is the **server** configured
the way you want to test? The numbers below are from a laptop running Valkey 9.1.3 in Docker Desktop, GET/SET
80/20, 256-byte values, 20,000 keys. They show the shape, not what your servers will do.

## Is the load generator the bottleneck?

Look at the last lines of a run:

```
client cpu: 1.89 cores, 0.24 cores per 100k ops/s
```

plgv's budget is **under half a core per 100,000 ops/s**. If the CPU line is near the machine's core count, or the
throughput stops growing when you add workers while the server is idle, you are measuring the generator.
Fixes, in order: more workers or a deeper pipeline; run plgv on a bigger or separate machine; split the load over
several machines (different `-seed` and `-prefix` each) and add the server's own `instantaneous_ops_per_sec`.
In a container, remove CPU limits that throttle the process (`deployment.md`).

## Workers: a closed loop is latency-bound

Each worker sends its next batch when the previous one returns, so throughput is about `workers / latency`.
One worker at 180 µs round trips can do about 5,500 ops/s:

| Workers | Throughput |
|---|---|
| 1 | 5,600 ops/s |
| 4 | 11,400 ops/s |
| 16 | 22,300 ops/s |
| 64 | 71,800 ops/s |
| 256 | 245,000 ops/s |

Add workers until throughput stops growing (the server is saturated) or latency starts to rise sharply
(queueing on the server). `-sweep-workers 4,16,64,256` does this in one command and stores each level
(`load-profiles-and-sweep.md`). To measure latency without saturating, fix the rate (`-target-ops`) and use
`-co` so waiting counts.

## Pipelining: throughput for latency

`-pipeline N` sends N commands per round trip. Every command is charged the whole batch's time, so latency rises
as throughput does (64 workers):

| Pipeline | Throughput | p50 | p99 |
|---|---|---|---|
| 1 | 72,500 ops/s | 0.71 ms | 2.2 ms |
| 4 | 259,000 ops/s | 0.87 ms | 2.6 ms |
| 16 | 801,000 ops/s | 1.1 ms | 3.9 ms |
| 64 | 1,025,000 ops/s | 3.7 ms | 9.7 ms |

Match the depth to your application. A cache client that pipelines 8 commands should be tested at 8, not 1 or 64.
Operations that wait or need their own connection (blocking commands, transactions, `WATCH_INCR`) are sent on
their own and do not pipeline.

## Client library

`valkey-go` (default) beat `go-redis` here: 73,600 against 55,700 ops/s with 64 workers, and it is the only
one for cluster and Sentinel. Use `go-redis` only to reproduce what a `go-redis` application sees
(`client-library-decision.md` has the full comparison).

## Keys and values

- **Distribution.** `uniform` spreads load over all keys; `zipf` concentrates it on a few hot keys, like many
  real caches. A hot key is served by one server thread, so zipf with large collections stresses the server
  differently.
- **Key count and value size** decide the memory the server needs: check `maxmemory`, or the server will evict
  and your hit ratio changes mid-run (the findings tell you).
- **Preload** writes the keys of the types in the mix before the run; turn it off (`-preload=false`) only when the
  data is already there, or the first reads miss.

## The server side

Things worth recording next to every result, because they change it:

- `io-threads` (default 1): more I/O threads raise what one server can serve from many connections.
- `maxmemory` and `maxmemory-policy`: with `noeviction`, a full server refuses writes (class `OOM`).
- Persistence (`appendonly`, `save`): the test servers here run with both off; yours may not.
- `latency-monitor-threshold` and `slowlog-log-slower-than`: needed for server metrics to show anything.
- Replication and `replica-read-only`: with `-replica-reads` reads go to replicas.

`-server-metrics` records the server version and the key `INFO` values with the run.

## Recording and metrics overhead

- Recording a 64-worker run cost nothing measurable (71,600 against 70,900 ops/s) and used about one core of
  one writer goroutine; with a slow disk use `.zst` and `-record-backpressure block` (`record-and-replay.md`).
- Server metrics add a few commands per second. The key-space analysis runs after the measured part.
- Memory stays flat for hours: a 15-minute run with every feature that keeps state (server metrics, 200 ms
  slices, a mixed workload with pub/sub and transactions) held 64.6 MiB after GC at the start and 64.8 MiB at the
  end, with no goroutines left. `make test-soak` repeats this; `PLGV_SOAK_DURATION=24h` is the long soak.

## Checklist for a trustworthy number

1. The CPU line is well below the machine's cores; the server is on another machine.
2. Same seed, dataset and options when comparing two runs; repeat before trusting a difference under 10%.
3. Enough workers to saturate, or a fixed rate with `-co` to measure latency.
4. Warm: a run of 30 s or more; the first seconds include connection setup.
5. The server's version and settings written down (`INFO`, `CONFIG GET`).
