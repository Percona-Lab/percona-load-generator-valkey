# Run history and trends

Every finished run (including one you stop early) is saved to `<data-dir>/runs/` as a JSON file,
and listed in **Run History**. The newest 100 are kept (`-keep-runs`).

## Run History

![Run History](../images/web_history.png)

Each row shows the run's name (its label, or its id), the operation mix, when it started, its
duration, throughput, p99, errors and finding chips (`clean`, `client only`, or counts of
warnings and critical findings; `stopped` for a run ended early).

| Action | What it does |
|---|---|
| **Open** | Loads the run into Live Metrics, Latency Analysis, Server, Slowlog and Insights. |
| **Report** | Opens the [HTML report](reports-and-exports.md) in a new tab. |
| **Delete** | Removes the run after a confirmation. |
| Checkboxes | Select two runs, then **Compare selected** ([compare-runs.md](compare-runs.md)). |

Give a run a **Run label** on Settings to find it later.

## Trends

![Trends](../images/web_trends.png)

Trends charts the same workload over time. Runs are grouped by **workload fingerprint**: the
operation mix, dataset, pipeline depth and client library. The server address, duration, seed
and server-metrics setting are not part of it, so you can compare the same workload across
servers and run lengths.

- Pick a workload from the list; the charts show throughput, p99 latency and hit ratio per run.
- A workload needs at least **3** runs to be charted.
- Runs stopped early are left out (their numbers are not comparable).

### Regression flags

The newest run is compared with the **median of up to five runs before it**. A flag appears when:

| Metric | Flagged when |
|---|---|
| p99 latency | 20% or more higher |
| Throughput | 10% or more lower |
| Hit ratio | 5 points or more lower (needs server metrics on those runs) |
| Error rate | 0.1 percentage points or more higher |

Otherwise the page says the newest run is in line with the previous runs. Flags point at a
change to look into; run-to-run noise on a busy machine can also cause them.
