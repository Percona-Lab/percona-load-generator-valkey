# Client library decision (phase 1)

**Decision: `valkey-go` (v1.0.79) is the client library.** `go-redis` (v9.22.0) stays
supported behind `internal/valkey/client.go` (`plgv run -client go-redis`) as a comparison
and fallback. No other file imports either library.

## How it was measured

`scripts/bench-clients.sh` runs `plgv run` for each library: 64 workers, 80% GET / 20% SET,
100,000 keys, zipf distribution, 256-byte values, 8 s per run, against Valkey 9.1.3 in Docker
(`make valkey-up`) on a Mac (Docker Desktop VM). Run twice.

| Client | Pipeline | ops/s (run 1 / run 2) | p50 | p99 | Errors | Client cores per 100k ops/s |
|---|---|---|---|---|---|---|
| valkey-go | 1 | 100,619 / 100,580 | 418 µs | 1.64 ms | 0 | 0.85 / 0.91 |
| go-redis | 1 | 57,036 / 54,933 | 1.02 ms | 2.95 ms | 0 | 2.91 / 3.01 |
| valkey-go | 16 | 810,886 / 822,687 | 1.11 ms | 4.13 ms | 0 | 0.13 / 0.13 |
| go-redis | 16 | 586,977 / 566,989 | 1.61 ms | 4.72 ms | 0 | 0.26 / 0.26 |

Repeat runs agree within about 3%. Pipelined latency is the batch round trip charged to every
command in the batch.

## Perf budget (exit criterion)

Target: the load generator uses less than 0.5 of one core per 100k ops/s, or a documented
limit.

- **Pipelined (16 per batch): met.** valkey-go uses 0.13 cores per 100k ops/s.
- **Not pipelined: limit documented.** valkey-go uses about 0.9 cores per 100k ops/s. Every
  command is its own network round trip, so system-call cost dominates; valkey-go's
  auto-pipelining across workers already helps. go-redis uses about 3 cores per 100k ops/s.
  To save client CPU at high rates, use pipelining. This budget is re-checked at phase 3 (with
  the real stats collector) and phase 6 (cluster).

## Why valkey-go

- 1.8x the throughput of go-redis without pipelining and 1.4x with it, at lower latency.
- 3x (unpipelined) and 2x (pipelined) less client CPU, which matters because the load
  generator must not be the bottleneck.
- Valkey-native, RESP3, cluster and Sentinel support, as planned.

## Caveats

- Docker Desktop on a Mac adds a VM network hop and shares CPU with the client, so absolute
  numbers are not representative of a Linux server. The relative comparison and the
  CPU-per-op figures are what the decision rests on. Repeat on Linux before publishing numbers.
- The latency histogram is the phase 1 spike version (about 3% relative error); phase 3
  replaces it.
- `go-redis` v9.22.0 is pinned because v9.23 requires Go 1.26 while the project is on 1.25.4.
