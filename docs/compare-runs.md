# Compare Runs

**Compare Runs** puts the charts of several runs on top of each other, so you can see what changed between them:
a new Valkey version, a different setting, a different server, or the same test two days later. A workload that is
still running draws against the finished ones as it goes.

![Compare Runs with a sweep: its table and figures, and every run on the same charts](../images/web_compare.png)

## What it shows

The tab has a **Compare runs** card and three charts. Each line is one run.

| Chart | What it draws |
|---|---|
| **Throughput (ops/s)** | Operations per second, all operations combined |
| **Latency (ms)** | Median (p50) latency, all operations combined |
| **Tail latency (ms)** | p99 or p95 latency. Switch with the **p99 / p95** buttons |

Latency here is the median and not the average, because plgv stores percentiles for each second of a run.

## Open it

Any of these:

- Click the **Compare Runs** tab. The two newest runs are ticked.
- In **Run History**, tick two runs and click **Compare selected**.
- In **Run History**, click **Compare sweep** on a run of a sweep, or **Compare this sweep** on the live page.
- While a workload runs, open **Compare Runs**. The running workload is already ticked, with the run before it.

Runs are kept on disk, so you can compare after plgv restarts.

## Pick what to compare

Each stored run is a box with its name (your run label, or its start time), when it started, how long it ran, its
throughput and the server address. Tick the box to draw its line. The charts update at once.

- **Latest two runs** ticks the two newest runs again. **Clear** removes every line.
- You can draw up to **8 lines** at a time, to keep the charts readable. Untick one to add another.

## Read the charts

- **Every line has its own colour and line style** (solid, dashed, dotted), so two lines never look alike. The
  legend under the picker shows each one.
- **Each line is named at its right end** (for a sweep, `4w`, `8w`, ...). Point at a name or a legend entry to dim
  every other line.
- **The x-axis is minutes since each run started** (0.5 is 30 seconds). Every line starts at 0, whatever time of
  day it ran, so runs of the same length line up.
- **A running workload is a thicker line** called **Running now**. It grows as new samples arrive. When the run
  finishes, the line stays and becomes the stored run.
- **Settings that differ** appears under the legend when two or more stored runs are drawn, for example
  `run.concurrency: 8 vs 4`. If it says the runs used the same settings, any difference in the lines comes from the
  server or the environment.

## Details of two runs

When exactly two stored runs are drawn, the page also shows, below the charts, everything that used to be here:

- **Metrics table:** throughput, p50, p95, p99, p99.9 and max latency, errors, and client CPU per 100k ops/s. If
  both runs have server metrics, also hit ratio, peak server memory, evicted keys and new slowlog entries. Each row
  says which run is better, or "about the same" when the two are within 2%. The older run is A and the newer is B.
- **Setting differences** and **Server setting differences** (see [server-config.md](server-config.md)).
- **By operation:** operations and p99 for each operation in either run.
- **Findings that differ:** insight rules that fired in only one of the runs.

If the two runs have different workload fingerprints (a different mix, dataset, pipeline depth or client library),
you see a warning that the differences may come from the workload and not the server.

## Sweeps

If you ran a sweep, its results are on this tab, above the charts: a table, plain-language notes, and for a worker
sweep two figures: **Throughput by worker count**, and **Throughput and p95 latency** with the area where more
workers stop helping shaded. A rate sweep shows its table and charts. Click **Show on charts** to draw every run
of the sweep on the charts below. See [load-profiles-and-sweep.md](load-profiles-and-sweep.md).

## Reading the numbers

- In a pipelined run, every operation in a batch is charged the batch's round trip. Because of that, you can't
  compare latency between a pipelined run and an unpipelined one.
- When the server is kept busy, requests wait in line, so latency looks alike for every command
  ([performance-tuning.md](performance-tuning.md)).
- Short runs are noisy. Compare runs of similar length, and repeat a run before you trust a small difference.

## Limits

- At most 8 lines at a time.
- Each run is one line. For a cluster, the line is the whole run (what the client measured), not one per node.
- The settings of the running workload are not compared with the stored runs until it is stored.
- Latency is the median, not the average.

## API

- `GET /api/runs` lists stored runs (with the server address in `target`). `GET /api/runs/{id}` returns one run
  with its time `series`.
- `GET /api/compare?a=...&b=...` is the two-run comparison.
- `GET /api/compare-settings?ids=a,b,c` lists the settings that differ across two to eight runs.
