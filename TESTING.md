# Testing

| Command | What it runs | Needs |
|---|---|---|
| `make check` | gofmt check, `go vet`, unit tests | nothing |
| `make test-race` | unit tests with the race detector | nothing |
| `make test-integration` | tests tagged `integration` against a real Valkey | `make valkey-up` |
| `make test-integration-cluster` | cluster tests (routing, per-node counts, replica reads, forced failover) | `make valkey-cluster-up` |
| `make test-integration-sentinel` | sentinel tests (follow the master, forced failover) | `make valkey-sentinel-up` |
| `make test-ui` | web UI DOM tests (jsdom) against API fixtures | Node.js 20+ and npm |

Integration packages run one at a time (`-p 1`) because they share one server and some tests
compare client and server operation counts; other traffic would break that comparison.

Integration target: `PLGV_IT_VALKEY_ADDR` (default `127.0.0.1:6379`). Cluster tests (phase 6)
will also need `make valkey-cluster-up`. Integration tests use their own key prefixes
(`it:...`) and remove their keys afterwards.

## Feature map

| Feature | Unit tests | Integration tests |
|---|---|---|
| Config schema, defaults, validation, env, save/load | `internal/config/config_test.go` | |
| Op catalog and command builders (every op, all families) | `internal/valkey/ops_test.go` | `TestIntegrationAllOpsNoErrors` (every op, both clients) |
| TTL model | `internal/valkey/ops_test.go` (`TestTTLModel`) | |
| Preload | `internal/valkey/runner_test.go`, `ops_test.go` | `TestIntegrationAllOpsNoErrors` |
| Seed handling | `internal/valkey/ops_test.go` (`TestSameSeedSameCommands`), `runner_test.go` | `TestIntegrationSeedReported` |
| Runner (mix, pipelining, errors, cancel) | `internal/valkey/runner_test.go` | `TestIntegrationBothClients` |
| Key distributions | `internal/keyspace/keyspace_test.go` | |
| Latency histogram | `internal/latency/histogram_test.go` | |
| CLI (`run`, `config`) | `cmd/plgv/run_test.go` | |
| Example configs | `internal/config/config_test.go` (`TestExampleConfigsAreValid`) | |
| Error classes | `internal/stats/stats_test.go` (`TestClassify`) | |
| Time series, downsampling, 24 h memory (simulated time) | `internal/stats/stats_test.go` | |
| Collector (counts, percentiles, concurrency) | `internal/stats/stats_test.go` | |
| Target rate and coordinated omission | `internal/valkey/runner_test.go` | |
| `INFO` / `SLOWLOG` / `LATENCY` parsing (real server fixture) | `internal/servermetrics/servermetrics_test.go`, `testdata/info_valkey_9.txt` | `TestIntegrationQueryShapesMatchAcrossClients` |
| Poller (ops accounting, slowlog deltas, denied sources, bounded series) | `internal/servermetrics/servermetrics_test.go` | `TestIntegrationClientAndServerOpsAgree`, `TestIntegrationSlowlogDeltas` |
| Insights (every rule fires, and is quiet below its threshold) | `internal/insights/insights_test.go` | |
| Run summary JSON (no credentials, round trip) | `internal/runsummary/runsummary_test.go` | `TestIntegrationRunWithServerMetricsAndSummary` |

| Web API (run lifecycle, history, compare, trends, saved settings, guards) | `internal/webui/webui_test.go`, `logic_test.go` | `internal/webui/integration_test.go` (real run, checkbox default, server section, report) |
| UI fixtures equal the real API output | `internal/webui/fixtures_test.go` | |
| Web UI page (settings levels, workload builder, server metrics checkbox and note, validation, saved settings, live view, tabs, history, compare, trends, escaping) | `internal/webui/uitest/*.test.mjs` | |
| HTML report | `internal/report/report_test.go` | |

| Load profile gating, ramp, open loop (rate, queueing, drops, cancel) | `internal/valkey/runner_test.go` | `internal/session/integration_test.go` (ramp, open loop) |
| Run shape validation (loop, profile, sweep) and fingerprint | `internal/config/config_test.go` | |
| Sweep orchestration (levels, labels, pause, stop, errors) | `internal/session/session_test.go` | `TestIntegrationWorkerSweep`, `TestIntegrationRateSweepServesLowRates` |
| Sweep analysis (plateau, latency doubling, saturation) and sweep API | `internal/webui/sweep_test.go` | `TestIntegrationWebSweep` |
| Dropped-arrivals insight | `internal/insights/insights_test.go` | |
| Load profile, loop and sweep settings; sweep banner; sweep results; Time Machine | `internal/webui/uitest/sweep.test.mjs` | |

| Slot function (CRC16, hash tags), hash tag key naming, multi-key on one slot | `internal/valkey/slot_test.go` | |
| Cluster settings validation | `internal/config/config_test.go` | |
| Topology parsing, diff (failover, reshard, node down, state), router, watcher, sentinel watcher | `internal/topology/topology_test.go` | |
| Per-node client stats | `internal/stats/stats_test.go`, `internal/valkey/runner_test.go` | |
| Cluster poller (per node, primaries-only totals, replica reads, failover => not exact) | `internal/servermetrics/cluster_test.go` | |
| Cluster insights, run summary cluster sections, mode detection | `internal/insights`, `internal/runsummary`, `internal/session` tests | |
| Real cluster: routing without redirects, per-node counts match the nodes, replica reads, auto mode, forced failover | | `internal/session/cluster_integration_test.go` (`make test-integration-cluster`) |
| Real sentinel: follow the master, replica reads, forced failover | | `internal/session/sentinel_integration_test.go` (`make test-integration-sentinel`) |
| Web API cluster run | | `internal/webui/cluster_integration_test.go` |
| Cluster settings, node table, topology events and chart markers in the UI | `internal/webui/uitest/cluster.test.mjs` | |
| Advanced ops: builders, key routing, composites, scripts, preload | `internal/valkey/ops_adv_test.go` | `internal/valkey/integration_adv_test.go` (both clients) |
| Pub/sub subscribers and delivery accounting | `internal/valkey/pubsub_test.go` | `integration_adv_test.go` |
| Raw injector: parser, placeholders, blocklist (both tiers) | `internal/rawcmd/rawcmd_test.go`, `internal/config/config_test.go`, `cmd/plgv/run_test.go` | |
| Advanced ops in a cluster (no redirects, TXN hash tags, sharded pub/sub) and sentinel | | `internal/session/cluster_adv_integration_test.go`, `TestSentinelAdvancedOperations` |
| Advanced-command insights, pub/sub summary | `internal/insights`, `internal/runsummary` tests | |
| Presets: validity (standalone and cluster), layering, flag, UI form | `internal/config/presets_test.go`, `cmd/plgv/run_test.go`, `internal/webui/uitest/presets_keyspace.test.mjs` | `TestIntegrationEveryPresetRuns`, `TestClusterEveryPresetRuns` |
| Key-space scan (types, sizes, TTL, big keys, cluster nodes, read-only) | `internal/keyscan/keyscan_test.go`, `internal/insights`, `internal/runsummary`, `internal/report` tests | `TestIntegrationKeySpaceOnlyReads`, `TestIntegrationWebPresetAndKeySpace`, cluster preset test |
| Server configuration per run (masking, label, blocked `CONFIG`, node diff, compare, history filter) | `internal/serverconfig/serverconfig_test.go`, `internal/config/serverconfig_test.go`, `internal/webui/serverconfig_test.go`, `internal/webui/uitest/serverconfig.test.mjs` | `TestIntegrationServerConfigRecorded`, `TestClusterServerConfigReadsEveryNode` |
| Compare Runs (overlay charts, picker, 8-line limit, live line, settings that differ, sweep figures) | `internal/webui/serverconfig_test.go` (`TestDiffConfigMany...`, `TestTargetOf...`) | `internal/webui/uitest/compare_runs.test.mjs`, `sweep.test.mjs` |
| Pub/sub subscribers are confirmed before publishing (no connection stall) | `internal/valkey/pubsub_test.go` | `TestIntegrationPubSubDeliveryOnBothLibraries` |
| Recorder and file format: round trip (none/zstd/gzip), binary arguments, edit and truncation detection, size limit, `fail` and `block` backpressure at scale (32,000 commands through a 64-slot queue), overwrite protection, 0600 | `internal/workloadrec/workloadrec_test.go` | |
| Starting-data fingerprint: scan-order independence, changed/missing keys, partial scans, cluster primaries | `internal/datafp/datafp_test.go` | |
| Tap (the single execute path), reply digests, composite operations recorded on their connection, outcomes; no file but `client.go` imports a client library | `internal/valkey/exec_test.go` | |
| Replay: record then replay reproduces each worker's command stream and batches, hash, reads-only, iterations, pacing, outcome and checksum comparison, dangerous commands, cancel, slow target | `internal/replay/replay_test.go` | |
| `record` config validation, off by default, CLI flags and `plgv replay` output | `internal/config/record_test.go`, `cmd/plgv/replay_test.go`, `internal/session/record_test.go` | |
| Record in the web UI (the Record switch, server-chosen file names, typed-path rules, run summary, recordings list/info/upload/delete, traffic import, replay validation and run, stored replays, live progress) | `internal/workloadrec/peek_test.go`, `internal/webui/record_test.go`, `recordings_test.go`, `replays_test.go`, `internal/webui/uitest/recording.test.mjs` | `TestIntegrationWebRecordThenReplay` |
| Record a real run and replay it: exact stream, statuses and reply checksums on both libraries; reads-only leaves data untouched; cluster | | `TestIntegrationRecordThenReplay`, `TestIntegrationReadsOnlyReplayLeavesDataUntouched`, `TestClusterRecordThenReplay` |
| Deterministic mode: exact budget split, same fingerprint whatever the timing, fingerprint equals the recording hash, depth caveat, validation, flags | `internal/valkey/deterministic_test.go`, `internal/config/record_test.go`, `cmd/plgv/phase10_test.go` | `TestIntegrationDeterministicRunFingerprint` |
| Traffic import: MONITOR and SLOWLOG parsing, filters, credentials never kept, groups, worker spread, live capture caps | `internal/trafficimport/trafficimport_test.go`, `cmd/plgv/phase10_test.go` | `TestIntegrationImportLiveMonitorTraffic` |
| Multi-target (sequential, concurrent, a target down) and replay comparison (verdicts, tolerance, not comparable, findings), `plgv compare` | `internal/multitarget`, `internal/replaycompare`, `cmd/plgv/phase10_test.go` | `TestIntegrationMultiTargetReplayAndCompare` |
| Replay Comparison API (uploads only, no file access) and tab | `internal/webui/replaycmp_test.go`, `internal/webui/uitest/replay.test.mjs` | |
| TLS config (off by default, `valkeys://`, validation, files, env, fingerprint), secrets from files, credentials never saved | `internal/config/tls_test.go` | |
| Mutual TLS + ACL against a real server: both libraries, refusals (no TLS, no cert, wrong CA, untrusted client, wrong password), restricted users, `NOPERM` counted, password in no output, record/replay and MONITOR over TLS | | `internal/session/tls_integration_test.go` (`make test-integration-tls`) |
| Web hardening: DNS rebinding, cross-site writes, CSP and headers, path traversal, report policy, allowed hosts; TLS card | `internal/webui/security_test.go`, `internal/webui/uitest/tls.test.mjs` | |
| Docs: every link and image resolves, every page is in the README, named pages exist | `internal/docscheck/docscheck_test.go` | |
| Soak: memory after GC and goroutines stay flat (long run and many short runs) | | `internal/session/soak_test.go` (`make test-soak`, `PLGV_SOAK_DURATION`) |
| Front page and workload cards (defaults selected, Use defaults, switching keeps the server), level control and categories, header clock and Exit, locked cards during a run, deterministic card | `internal/webui/uitest/tasks.test.mjs`, `settings.test.mjs` | |
| Mix builder families, steps, raw injector, pub/sub settings and results | `internal/webui/uitest/advanced.test.mjs` | |

## Notes

- The cluster and sentinel tests change the topology (a forced failover) and put it back afterwards.
- Regenerate the UI fixtures after an API change: `go test ./internal/webui -run TestUIFixturesMatchAPI -update-ui-fixtures`.

- The 24-hour memory test runs 86,400 one-second slices through the real collector with
  simulated time and asserts that the heap does not grow between hour 12 and hour 24. A real
  24-hour soak is part of phase 11.
- Refresh the INFO fixture with `docker exec plgv-valkey valkey-cli info everything`
  (strip carriage returns) when Valkey changes its fields.
