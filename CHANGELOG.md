# Changelog

This file is intended to mirror the release notes you publish on GitHub.
For each new release, add a new `## vX.Y.Z` section at the top. The release workflow will use the first versioned section as the GitHub Release body when you push a `v*` tag.

---

## v0.0.1 - First release: a Valkey load generator with a web UI

Percona Load Generator for Valkey (`plgv`) generates realistic load against Valkey, measures it, and shows the result in a web UI or a report. It works with standalone servers, Valkey Cluster and Sentinel, and uses RESP2 and RESP3 with core Valkey commands only (no modules).

**Highlights**

**New: Workloads**
- 39 operations across strings, keys, hashes, lists, sets and sorted sets, plus streams and consumer groups, blocking reads, pub/sub with delivery latency, geo, HyperLogLog, bitmaps, `MULTI`/`EXEC`, `WATCH`, pipeline groups, Lua scripts and functions, and a raw command injector with a blocklist.
- Uniform or zipf key choice, value sizes, TTL model, preload, pipelining, a target rate with coordinated-omission latency, and fixed seeds.
- Nine presets for typical applications: cache, session store, leaderboard, work queue, rate limiter, pub/sub fan-out, counters and analytics, big keys stress, and mixed.
- Load profiles (`ramp`, `step`, `spike`, `sine`) and an open loop that counts dropped operations.
- A **sweep** runs the same workload at several worker counts or target rates and shows where more load stops helping.

**New: Web UI (`plgv web`)**
- A front page that asks what you want to do, with a card per workload. Picking one opens its settings with the defaults already selected, and **Use defaults** returns to them.
- Basic, Advanced and Expert levels, and a category list on the left (Connection, Workload execution, Data and commands, Seed and deterministic, Metrics and reporting), so no screen is one long page.
- Live Metrics, Latency Analysis, Server, Slowlog, Key Space and Insights tabs, Completed Runs, Compare Runs, Replay Comparison and Trends with regression flags.
- Saved settings, a Time Machine range zoom, and a self-contained HTML report per run.
- Binds to loopback and has no login by design; see Security below.

**New: Record, replay and compare**
- `plgv run -record FILE` writes every command the workers send. `plgv replay FILE` sends it again at the recorded pace or as fast as possible, reads-only if you want, for several iterations, with a proof that the same commands were sent. Replay refuses harmful commands in an edited file.
- Deterministic mode (`-ops`, `-deterministic`) gives the same traffic every time, with a traffic fingerprint.
- `plgv import-traffic` turns `MONITOR` and `SLOWLOG` output into a recording, with credentials dropped.
- Multi-target replay (`-targets`, sequential or `-concurrent`) and `plgv compare` show differences between servers or versions.

**New: Cluster and Sentinel**
- Slot routing, hash tags for multi-key commands, replica reads, and per-node client and server results.
- Failover, reshard and node-down detection with chart markers, and seven cluster insights.

**New: Metrics and insights**
- Error classes, per-operation percentiles, a bounded time series, client CPU report, and opt-in server metrics (`INFO`, `SLOWLOG`, `LATENCY`).
- Opt-in key-space analysis: types, sizes, expiry and the biggest keys.
- Insight rules that give findings with evidence and advice, and a JSON run summary (`-summary`).

**New: Security and operations**
- TLS and mutual TLS (`valkeys://`, `-tls*` flags, `target.tls`, the UI's Encryption card) and ACL users, with a least-privilege rule that is documented and tested against a real ACL server.
- Credentials only from `PLGV_USERNAME` and `PLGV_PASSWORD`, or from mounted files (`PLGV_*_FILE`). They are never written to YAML, JSON, reports or recordings.
- Web UI hardening: Host check against DNS rebinding (`-allowed-host`), Content-Security-Policy, strict policy for served reports, and a refusal to listen beyond loopback without `-allow-remote`.
- `govulncheck` runs in CI. See `SECURITY.md`.

**New: Packaging**
- Release archives for Linux and macOS on amd64 and arm64, with `SHA256SUMS` (`make dist`).
- A non-root container image (`make docker-build`), published to ghcr.io on release, and Kubernetes Job manifests in `deploy/k8s/`.

**Testing & Docs**
- Unit, race, web UI (DOM) and integration tests against standalone, cluster, Sentinel, and TLS with ACL servers. Soak tests (`make test-soak`): 15 minutes, 34 million operations, memory flat, no goroutines left.
- A page per feature in `docs/`, with screenshots for the UI, a walkthrough, and a test that keeps every link, image and README entry valid.

**Limits**
- Tested on Valkey 9.1.3 (and 8.0.12 for replay). Redis OSS has not been tested.
- Recording and replay are available from the command line only; the web UI cannot record or start a replay.

---

## Release Template

Copy the structure below for the next release and replace the version/title:

## vX.Y.Z - Release Title

**Highlights**

**New: Feature Area**
- Bullet one.
- Bullet two.

**Fixes**
- Bullet one.

**Testing & Docs**
- Bullet one.
