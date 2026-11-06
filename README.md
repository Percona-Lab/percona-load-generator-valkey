# Percona Load Generator for Valkey (plgv)

A workload generator and benchmark tool for [Valkey](https://valkey.io), modeled on the Percona Load
Generator for MongoDB (plgm). One static binary, a web UI, and a container image.

- **Realistic workloads:** strings, hashes, lists, sets, sorted sets, streams, geo, HyperLogLog, bitmaps,
  pub/sub, transactions, Lua scripts and functions, your own raw commands, and nine ready-made presets.
- **Honest measurement:** latency percentiles per command, coordinated-omission-aware pacing, client CPU
  cost, an open loop, load profiles and sweeps, and optional server-side metrics with plain-language findings.
- **Standalone, cluster and Sentinel**, with failover markers; TLS, mutual TLS and ACL users.
- **Repeatable:** a seed, an exact operation budget with a traffic fingerprint, record and replay (with outcomes and
  reply checksums), traffic import from `MONITOR` or the slowlog, multi-server replay and comparison.
- **Safe by default:** every feature is opt-in and off until you turn it on; the web UI binds to loopback;
  credentials come only from the environment or a mounted file.

## Quick start

```bash
make valkey-up          # local Valkey in Docker (see VALKEY_TEST_ENV.md)
make build-local        # builds bin/plgv
./bin/plgv run -duration 10s -workers 64 -pipeline 16 -dist zipf     # quick GET/SET run
./bin/plgv run -preset session -duration 15s -server-metrics         # a preset, with server stats and findings
./bin/plgv run -config examples/mixed.yaml                           # a workload from YAML
./bin/plgv web                                                       # web UI on http://127.0.0.1:8080
make valkey-down
```

New here? Read `docs/walkthrough.md`. Install options (binaries, Docker, Kubernetes): `docs/deployment.md`.

## Documentation

Every feature has its own page. Pages describe behavior that was run against a real Valkey.

### Start here

| Page | Topic |
|---|---|
| [`docs/walkthrough.md`](docs/walkthrough.md) | A guided tour, from a first run to comparing two servers |
| [`docs/cli.md`](docs/cli.md) | `plgv run` and `plgv config` flags and output |
| [`docs/configuration-reference.md`](docs/configuration-reference.md) | Every YAML setting, defaults, environment |
| [`docs/workloads.md`](docs/workloads.md) | Operations, key types, distributions, TTL model |
| [`docs/presets.md`](docs/presets.md) | Built-in workload presets (cache, session, leaderboard, queue, ...) |
| [`docs/deployment.md`](docs/deployment.md) | Binaries, Docker and Kubernetes |

### Measuring

| Page | Topic |
|---|---|
| [`docs/metrics-and-server-stats.md`](docs/metrics-and-server-stats.md) | Client and server metrics, coordinated omission, run summary |
| [`docs/server-metrics.md`](docs/server-metrics.md) | The Server metrics checkbox, Server and Slowlog tabs |
| [`docs/insights.md`](docs/insights.md) | Every finding rule: when it fires and what to do |
| [`docs/load-profiles-and-sweep.md`](docs/load-profiles-and-sweep.md) | Load profiles, open loop, sweeps, Time Machine |
| [`docs/performance-tuning.md`](docs/performance-tuning.md) | Getting the most from the generator and the server |
| [`docs/key-space-analysis.md`](docs/key-space-analysis.md) | Key types, sizes, expiry and big keys after a run |

### Workloads

| Page | Topic |
|---|---|
| [`docs/advanced-commands.md`](docs/advanced-commands.md) | Streams, blocking, pub/sub, geo/HLL/bitmap, transactions, scripts |
| [`docs/raw-injector.md`](docs/raw-injector.md) | Your own commands as the workload, and the blocklist |
| [`docs/cluster-and-sentinel.md`](docs/cluster-and-sentinel.md) | Cluster and Sentinel targets, hash tags, per-node results, failover markers |
| [`docs/deterministic-mode.md`](docs/deterministic-mode.md) | Exact operation budget, reproducible traffic and its fingerprint |

### Record, replay and compare

| Page | Topic |
|---|---|
| [`docs/record-and-replay.md`](docs/record-and-replay.md) | Record a run's commands and replay them (pace, reads-only, outcomes, checksums, starting data) |
| [`docs/traffic-import.md`](docs/traffic-import.md) | Turn MONITOR or SLOWLOG traffic into a recording |
| [`docs/multi-target.md`](docs/multi-target.md) | Replay one recording on several servers, sequentially or at once |
| [`docs/replay-comparison.md`](docs/replay-comparison.md) | `plgv compare` and the Replay Comparison tab |

### The web UI

| Page | Topic |
|---|---|
| [`docs/web-ui.md`](docs/web-ui.md) | The web UI: start it, run a workload, the tabs |
| [`docs/settings-levels.md`](docs/settings-levels.md) | Basic, Advanced and Expert settings; saved settings |
| [`docs/custom-workload-builder.md`](docs/custom-workload-builder.md) | Building an operation mix in the UI |
| [`docs/trends-and-history.md`](docs/trends-and-history.md) | Run history, trends and regression flags |
| [`docs/compare-runs.md`](docs/compare-runs.md) | Comparing two runs |
| [`docs/reports-and-exports.md`](docs/reports-and-exports.md) | HTML report and JSON summary |
| [`docs/web-ui-design-system.md`](docs/web-ui-design-system.md) | The UI's tokens and components |

### Security and operations

| Page | Topic |
|---|---|
| [`docs/security-tls-acl.md`](docs/security-tls-acl.md) | TLS, mutual TLS, ACL users, secrets, the web UI's defenses |
| [`docs/compatibility.md`](docs/compatibility.md) | Tested servers, scope, protocols, platforms |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | Messages and symptoms, with causes and fixes |

### Background

| Page | Topic |
|---|---|
| [`docs/client-library-decision.md`](docs/client-library-decision.md) | Why `valkey-go`, with benchmark numbers |
| [`docs/discovery-phase0.md`](docs/discovery-phase0.md) | Verified Valkey facts |

Also: `TESTING.md` (how to run the tests and what covers what), `VALKEY_TEST_ENV.md` (the local Docker servers),
`examples/` (workloads to start from), `CHANGELOG.md`, `THIRD_PARTY.md` (dependencies and licenses).

## Compatibility

Tested: Valkey 9.1.3 (every suite: standalone, cluster, Sentinel, TLS and ACL) and 8.0.12 (replay). Core commands over RESP2 or
RESP3; no modules (Search, JSON, Bloom). Redis OSS is not tested. Details: `docs/compatibility.md`.

## Security

TLS and mutual TLS, ACL users and secrets from files are supported; the web UI has no login and binds to
loopback. Read `docs/security-tls-acl.md` before exposing anything. Report a vulnerability privately to the
maintainers rather than in a public issue.

## License

See `LICENSE`. Third-party components: `THIRD_PARTY.md`.
