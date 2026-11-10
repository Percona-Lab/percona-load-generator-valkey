# Performance tuning

There are two questions to ask. Is **plgv** fast enough that it isn't the bottleneck? And is the **server**
configured the way you want to test it? The numbers below come from a laptop running Valkey 9.1.3 in Docker
Desktop, with an 80/20 GET/SET mix, 256-byte values and 20,000 keys. They show the general shape, not what your
servers will do.

## Is the load generator the bottleneck?

Start with the last lines of a run:

```
client cpu: 1.89 cores, 0.24 cores per 100k ops/s
```

plgv aims to use **under half a core per 100,000 ops/s**. If the CPU line is close to the machine's core count,
or throughput stops growing when you add workers while the server sits idle, you're measuring the generator.

Try these fixes, in this order:

1. Add more workers or a deeper pipeline.
2. Run plgv on a bigger machine, or a separate one.
3. Split the load over several machines (use a different `-seed` and `-prefix` on each) and add up the server's
   own `instantaneous_ops_per_sec`.

In a container, remove any CPU limits that throttle the process (see `deployment.md`).

## Workers: a closed loop is latency-bound

Each worker sends its next batch when the previous one returns, so throughput is roughly `workers / latency`.
One worker with 180 µs round trips can do about 5,500 ops/s:

| Workers | Throughput |
|---|---|
| 1 | 5,600 ops/s |
| 4 | 11,400 ops/s |
| 16 | 22,300 ops/s |
| 64 | 71,800 ops/s |
| 256 | 245,000 ops/s |

Keep adding workers until throughput stops growing (the server is saturated) or latency starts to rise sharply
(queueing on the server). `-sweep-workers 4,16,64,256` does this in one command and stores each level (see
`load-profiles-and-sweep.md`). If you want to measure latency without saturating the server, fix the rate with
`-target-ops` and add `-co` so waiting counts.

## Why every command can show the same latency

If the per-operation table shows almost the same latency for every command (for example GET and SET both at
819µs), the tool is not broken. Here is what is happening:

- Latency is the time from sending a request to getting the answer. That includes **time spent waiting in line**.
- A Valkey server handles requests mostly one at a time. If many workers keep it busy, each request has to wait
  for the ones ahead of it.
- That wait is much longer than the work itself (a few microseconds), and it is about the same for every command.
  So all commands look alike.

It does **not** mean the commands cost the same. To see what each command really costs:

- Use fewer workers, for example `-workers 1`. Nothing waits in line, so the numbers show the real difference.
- Or set a fixed rate below what the server can handle, for example `-target-ops 20000 -co`.

plgv prints a short note under the table when it sees this, and the Web UI shows one above the per-operation
tables on Live Metrics and Latency Analysis.

## Pipelining: throughput for latency

`-pipeline N` sends N commands per round trip. Every command is charged the whole batch's time, so latency goes
up as throughput does. These numbers use 64 workers:

| Pipeline | Throughput | p50 | p99 |
|---|---|---|---|
| 1 | 72,500 ops/s | 0.71 ms | 2.2 ms |
| 4 | 259,000 ops/s | 0.87 ms | 2.6 ms |
| 16 | 801,000 ops/s | 1.1 ms | 3.9 ms |
| 64 | 1,025,000 ops/s | 3.7 ms | 9.7 ms |

Match the depth to your application. If your cache client pipelines 8 commands, test at 8, not 1 or 64.
Operations that wait or need their own connection (blocking commands, transactions, `WATCH_INCR`) are sent on
their own and don't pipeline.

## Client library

`valkey-go` (the default) beat `go-redis` here, with 73,600 against 55,700 ops/s at 64 workers. It's also the
only one that works with cluster and Sentinel. Only use `go-redis` if you want to reproduce what a `go-redis`
application sees. `client-library-decision.md` has the full comparison.

## Keys and values

- **Distribution.** `uniform` spreads the load over all keys. `zipf` concentrates it on a few hot keys, like
  many real caches do. One server thread serves a hot key, so Zipf with large collections stresses the server
  differently.
- **Key count and value size** decide how much memory the server needs. Check `maxmemory`, because otherwise the
  server will evict keys and your hit ratio will change mid-run (the findings tell you when that happens).
- **Preload** writes the keys of the types in the mix before the run. Only turn it off (`-preload=false`) when
  the data is already there, or the first reads will miss.

## The server side

These settings change your results, so write them down next to every result:

- `io-threads` (default 1): more I/O threads raise what one server can serve from many connections.
- `maxmemory` and `maxmemory-policy`: with `noeviction`, a full server refuses writes (class `OOM`).
- Persistence (`appendonly`, `save`): our test servers run with both off, but yours may not.
- `latency-monitor-threshold` and `slowlog-log-slower-than`: server metrics need these to show anything.
- Replication and `replica-read-only`: with `-replica-reads`, reads go to replicas.

`-server-metrics` records the server version and the key `INFO` values with the run.

## Recording and metrics overhead

- Recording a 64-worker run cost nothing we could measure (71,600 against 70,900 ops/s) and used about one core
  of one writer goroutine. If your disk is slow, use `.zst` and `-record-backpressure block` (see
  `record-and-replay.md`).
- Server metrics add a few commands per second. The key-space analysis runs after the measured part.
- Memory stays flat for hours. A 15-minute run with every feature that keeps state (server metrics, 200 ms
  slices, a mixed workload with pub/sub and transactions) held 64.6 MiB after GC at the start and 64.8 MiB at
  the end, with no goroutines left over. `make test-soak` repeats this, and `PLGV_SOAK_DURATION=24h` runs the
  long soak.

## Checklist for a trustworthy number

1. The CPU line is well below the machine's core count, and the server is on another machine.
2. You used the same seed, dataset and options when comparing two runs, and you repeated the run before trusting
   a difference under 10%.
3. You used enough workers to saturate the server, or a fixed rate with `-co` to measure latency.
4. The run was warm. Use 30 s or more, because the first seconds include connection setup.
5. You wrote down the server's version and settings (`INFO`, `CONFIG GET`).
