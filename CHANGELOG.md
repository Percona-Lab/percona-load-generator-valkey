# Changelog

For each release, add a new `## vX.Y.Z` section at the top. The release workflow uses the first
versioned section as the GitHub Release body when a `v*` tag is pushed.

---

## v1.0.0 - Unreleased

- Web UI redesign, modeled on plgm: a front page that asks what you want to do, with a card per workload (standard, custom, your own commands,
  find the limit, and the nine presets) that opens its settings with the defaults selected and **Use defaults**; a header with status, run clock,
  Completed Runs and Exit; Basic, Advanced and Expert as a level control with a category list on the left (Connection, Workload execution, Data and
  commands, Seed and deterministic, Metrics and reporting) instead of one long page; problems mark their category; deterministic traffic settings in the UI.
  `scripts/screenshots.mjs` regenerates the screenshots.
- Security and operations: TLS and mutual TLS (`valkeys://`, `-tls*` flags, `target.tls`, the UI's Encryption card), ACL users
  (least-privilege rule documented and tested against a real ACL server), secrets from mounted files
  (`PLGV_PASSWORD_FILE`), web UI hardening (Host check against DNS rebinding, `-allowed-host`, Content-Security-Policy,
  strict report policy), `SECURITY.md`, `govulncheck` in CI, `klauspost/compress` 1.18.7.
- Packaging: release archives for Linux and macOS on amd64 and arm64 with `SHA256SUMS` (`make dist`), a non-root container image
  (`make docker-build`, published to ghcr.io on release), Kubernetes Job manifests (`deploy/k8s/`).
- Docs: security-tls-acl, deployment, performance-tuning, compatibility, troubleshooting and a walkthrough; a test keeps every link,
  image and README entry valid. Soak tests (`make test-soak`): 15 minutes, 34 million operations, memory flat and no goroutines left.
- Includes everything under v0.1.0 below.

## v0.1.0 - Unreleased

- Phase 10: `plgv import-traffic` (MONITOR and SLOWLOG, live capture with caps), deterministic mode (`-ops`, `-deterministic`, traffic fingerprint), multi-target replay (`plgv replay -targets`, sequential or `-concurrent`), `plgv compare` and a Replay Comparison tab, `-preload-only`, `-label`.
- Record and replay (opt-in, command line): `plgv run -record FILE` writes every command the workers send (with pipeline and `WATCH`/`MULTI` grouping, optional outcomes and reply checksums, zstd or gzip, `fail` or `block` backpressure, a starting-data fingerprint) and `plgv replay FILE` sends it again at the recorded pace or as fast as possible, reads-only, several iterations, with a proof that the same commands were sent. Replay refuses harmful commands in an edited file. `plgv replay -info` describes a recording.
- Presets (cache, session, leaderboard, queue, rate-limiter, pub/sub fan-out, counters, big keys, mixed) in the CLI, YAML and web UI, and an opt-in key-space analysis (types, sizes, expiry, biggest keys) with a Key Space tab, a report section and a big-keys insight. Fixed a rare hang when several pub/sub subscribers started together.
- Advanced commands: streams and consumer groups, blocking reads, pub/sub with delivery latency, geo, HyperLogLog, bitmaps, MULTI/EXEC, WATCH, pipeline groups, Lua scripts and functions, a raw command injector with a blocklist, five new insight rules, matching UI, `examples/` per family.
- Project bootstrap: `plgv version` / `plgv help`, Makefile gates, CI, local Valkey test
  environment. `plgv run`: GET/SET workload with uniform or zipf keys, pipelining, latency percentiles and
  client CPU report; `valkey-go` chosen as the client library.
- YAML configuration (`plgv run -config`, `plgv config`), 39 operations across strings, keys,
  hashes, lists, sets and sorted sets, TTL model, preload, per-operation results, seeds.
- Metrics: error classes, per-operation percentiles, bounded time series, target-rate pacing with
  coordinated-omission latency, opt-in server metrics (`INFO`, `SLOWLOG`, `LATENCY`), 16 insight
  rules, and a JSON run summary (`-summary`).
- Web UI (`plgv web`): settings at Basic/Advanced/Expert levels, saved settings, an operation mix
  builder, a Server metrics checkbox (off by default, with an explanatory note), Live Metrics,
  Latency Analysis, Server, Slowlog and Insights tabs, Run History, Compare Runs, Trends with
  regression flags, and a self-contained HTML report per run.
- Load profiles (`ramp`, `step`, `spike`, `sine`), an open loop with dropped-operation accounting,
  worker-count and target-rate sweeps (`-sweep-workers`, `-sweep-rates`, web UI) with a results table
  and plain-language reading in Compare Runs, an Active workers chart, and a Time Machine range zoom.
- Cluster and Sentinel targets (`-mode`, `-addr` with several nodes, `-replica-reads`, `-hash-tags`,
  `-sentinel-master`, web UI Deployment setting): slot routing, hash tag groups for multi-key commands,
  replica reads, per-node client and server results, failover/reshard/node-down detection with chart
  markers, seven cluster insights, and `make valkey-sentinel-up`.
