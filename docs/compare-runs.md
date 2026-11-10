# Compare Runs

This tab compares two stored runs side by side. Pick **Run A** and **Run B** on the tab, or select two runs in
Run History and click **Compare selected**. Differences are shown as B relative to A.

![Compare Runs](../images/web_compare.png)

## What you see

- **Metrics table:** throughput, p50, p95, p99, p99.9 and max latency, errors, and client CPU per 100k ops/s.
  If both runs have server metrics, you also get hit ratio, peak server memory, evicted keys and new slowlog
  entries.
- **Verdict:** each row says which run is better, or "about the same" when the two are within 2%. Higher is
  better for throughput and hit ratio, and lower is better for everything else.
- **Charts:** throughput and p99 over time for both runs.
- **Setting differences:** every setting that differs between the two configurations. Output settings are
  ignored.
- **By operation:** operations and p99 for each operation in either run.
- **Findings that differ:** insight rules that fired in only one of the runs.

## Same workload or not

If the two runs have different workload fingerprints (a different mix, dataset, pipeline depth or client
library), you'll see a warning that the differences may come from the workload and not the server. The setting
differences table shows exactly what changed. The tab is meant for comparing the same workload against two
servers or two server versions.

## Reading the numbers

- In a pipelined run, every operation in a batch is charged the batch's round trip. Because of that, you can't
  compare latency between a pipelined run and an unpipelined one.
- Short runs are noisy. Compare runs of similar length, and repeat a run before you trust a small difference.

## Sweep results

Below the two-run comparison, **Sweep results** shows a whole sweep (one workload at several worker counts or
target rates) as a table with charts and plain-language notes. See
[load-profiles-and-sweep.md](load-profiles-and-sweep.md).
