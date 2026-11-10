# Insights

After each run, plgv checks the measurements against a set of rules and prints **findings**. A finding tells you
what looks wrong, shows the evidence, and suggests what to do about it. Findings are also saved in the run
summary. Rules that need server data only run with `-server-metrics`, while the client rules always run. A
healthy run prints `findings: none`.

In the web UI, findings are on the **Insights** tab, which has a count badge for warnings and critical findings.
They also appear in each run's report. The most serious come first: **critical**, then **warning**, then **info**.

| Rule id | Needs server metrics | Fires when | Severity |
|---|---|---|---|
| `client-errors` | no | Any operation failed. Critical at 1% or more of operations. | warning / critical |
| `eviction-pressure` | yes | `evicted_keys` rose during the run. | warning |
| `memory-near-limit` | yes | Peak `used_memory` is at least 90% of `maxmemory` (critical at 98%). Needs `maxmemory` set. | warning / critical |
| `low-hit-ratio` | yes | Hit ratio under 50% over at least 1000 lookups. | warning |
| `fork-stall` | yes | `latest_fork_usec` reached 100 ms (critical at 1 s). | warning / critical |
| `slow-commands` | yes | New SLOWLOG entries appeared during the run. | warning |
| `blocked-clients` | yes | `blocked_clients` was above 0. | info |
| `replication-lag` | yes | A replica was 1 MiB or more behind, or a replica's link to its primary is down (critical). | warning / critical |
| `rejected-connections` | yes | `rejected_connections` rose (the server hit `maxclients`). | critical |
| `memory-fragmentation` | yes | `mem_fragmentation_ratio` of 1.5 or more with at least 100 MiB in use. | warning |
| `bgsave-failed` | yes | `rdb_last_bgsave_status` is not `ok` at the end. | warning |
| `client-server-mismatch` | yes | plgv's operation count and the server's differ by 5% or more, on a run of at least 2 s. | warning |
| `server-metrics-unavailable` | yes | Some polls failed (usually an ACL denial). | info |
| `failover-during-run`, `reshard-during-run`, `node-down`, `cluster-not-ok`, `hot-node`, `uneven-slots`, `redirects` | yes | Cluster and Sentinel findings. See `cluster-and-sentinel.md`. | warning / critical / info |
| `load-generator-bound` | no | plgv used 85% or more of this machine's CPU cores. | warning |
| `arrivals-dropped` | no | An open-loop run could not send 0.5% or more of the offered operations (critical at 5%). | warning / critical |
| `target-rate-missed` | no | Achieved under 95% of `run.target_ops_per_sec` (not shown when `arrivals-dropped` already explains it). | warning |
| `watch-conflicts`, `blocking-timeouts`, `streams-ran-dry`, `pubsub-loss`, `dangerous-commands-allowed` | no | Advanced-command findings. See `advanced-commands.md`. | info / warning / critical |
| `big-keys` | no | The key-space analysis found a key of 1 MiB or more (warning at 10 MiB) or a collection of 100,000 or more elements. See `key-space-analysis.md`. | info / warning |
| `tail-latency` | no | p99 is more than 10 times p50 and at least 1 ms. | info |

## Reading the findings

- **client-errors:** the evidence lists the counts per error class, and the advice follows the most common class.
  For example, OOM means writes were refused at `maxmemory`.
- **low-hit-ratio:** most often the dataset wasn't preloaded (`dataset.preload: false`), the key prefix doesn't
  match existing data, or keys were evicted or expired. A workload that deletes keys also lowers the ratio.
- **client-server-mismatch:** usually another application is using the server. Benchmark on a dedicated server.
  (For the same reason, integration test packages have to run one at a time, and `make test-integration` takes
  care of that.)
- **load-generator-bound:** the numbers understate what the server can do. Use pipelining, fewer workers, or a
  bigger machine for plgv.
- **tail-latency:** this one is informational. Look at `fork-stall` and `slow-commands` for causes.

## Cluster and Sentinel findings

The cluster and Sentinel findings (failover, reshard, node down, cluster state, hot node, uneven slots,
redirects) are listed in [cluster-and-sentinel.md](cluster-and-sentinel.md#insights-for-clusters).

## Adding or tuning a rule

Rules live in `internal/insights/insights.go`, and the thresholds are named constants at the top. Each rule is a
function of its input only. Each one also has a fixture test in `insights_test.go` that checks it fires, checks
its severity and evidence, and checks that it stays quiet just below the threshold.
