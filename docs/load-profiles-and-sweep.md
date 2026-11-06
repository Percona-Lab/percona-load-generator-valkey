# Load profiles, open loop and sweeps

Three ways to shape a run beyond "N workers for T seconds". All are off by default and live under
**Advanced** settings in the web UI (`run.load_profile`, `run.loop`, `run.sweep` in YAML).

- [Load profiles](#load-profiles): change the number of active workers over time.
- [Open and closed loop](#open-and-closed-loop): who decides when the next request is sent.
- [Sweeps](#sweeps): run the same workload at several worker counts or rates and compare them.
- [Time Machine](#time-machine): look at one part of a run.

## Load profiles

A profile says how many workers are **active** at each moment. plgv starts as many workers as the
profile's peak and lets the rest sit idle until the profile calls for them.

| Profile | What it does | Settings |
|---|---|---|
| `fixed` (default) | The same number of workers all run long (**Workers** under Run). | none |
| `ramp` | Moves steadily from one worker count to another. | `start_workers`, `end_workers`, `ramp_over` |
| `step` | Holds a worker count for a time, then the next stage. | `steps`: a list of `workers` and `duration` |
| `spike` | A base number of workers, jumping to a peak for a window. | `base_workers`, `peak_workers`, `spike_at`, `spike_for` |
| `sine` | Smooth periodic load between a minimum and a maximum. It starts at the trough so the run warms up. | `min_workers`, `max_workers`, `period` |

```yaml
run:
  duration: 30s
  load_profile:
    kind: ramp
    start_workers: 1
    end_workers: 32
    ramp_over: 20s
```

(`examples/ramp.yaml`.) In the web UI tick **Advanced**, open **Workload execution** and choose **Load profile**; only the
fields for the chosen profile are shown, and `step` has a small table of stages.

![Load profile and open loop settings](../images/web_load_profile.png)

- The run ends at **Duration**, whatever the profile says; a profile shorter than the run holds
  its last value.
- **Live Metrics** has an **Active workers** chart, and every stored run records the active
  worker count for each time slice, so you can line throughput up with concurrency.

![A ramp run: active workers growing while throughput follows](../images/web_live_ramp.png)

- A target rate with a changing number of workers needs the open loop (below); the combination
  with the closed loop is rejected with a message.

## Open and closed loop

| Loop | How it works | Use it for |
|---|---|---|
| **closed** (default) | Each worker sends its next batch when the previous one finishes. A slow server slows the load down. | Finding the most the server can do with N clients. |
| **open** | Batches are *offered* at the target rate whether or not the server keeps up. Workers take them as they become free. | Asking "what would users see at this request rate?" |

In the open loop:

- It needs a **target rate** (`run.target_ops_per_sec`); the rate is for the whole run and is
  split into batches of **pipeline depth** operations.
- **Latency is measured from the moment a batch was due**, so time spent waiting for a free worker
  counts. This is the coordinated-omission-aware view (see [metrics-and-server-stats.md](metrics-and-server-stats.md)); the
  separate setting is implied.
- If the server or plgv cannot keep up, offered batches pile up in a queue (about ten seconds'
  worth) and then are dropped. Batches still queued when the run ends are dropped too. The result
  shows **dropped operations**, and the **Insights** raise `arrivals-dropped` once 0.5% or more of
  the offered operations were never sent (critical at 5%). A handful left over at the end is normal
  and not reported.

![Open loop overload finding](../images/web_open_loop_insights.png)

Example (`examples/open-loop.yaml`): offer 50,000 ops/s for 20 seconds.

```bash
./bin/plgv run -loop open -target-ops 50000 -workers 64 -duration 20s
```

## Sweeps

A sweep runs the same workload several times, once per **level**, and stores each as its own
run. Use it to see how throughput and latency change as concurrency grows, and where the server
stops keeping up.

| Sweep | Levels | Each run uses |
|---|---|---|
| **by worker count** | 2 to 8 whole numbers, 1 to 10000 | that many workers |
| **by target rate** | 2 to 8 rates in ops/s, above 0 | that target rate |

Settings: `run.sweep.kind` (`none`, `workers` or `rate`), `values`, and `pause` (wait between
runs, default none). **Duration is the length of each run**, not of the whole sweep. A sweep needs
the `fixed` load profile and, for a rate sweep, no target rate of its own.

### Run one

Web UI: the **Find the limit** card on the front page selects a worker sweep (4, 16, 64, 128) for you, or open **Workload execution** (Advanced), choose **Sweep**, a kind and list the levels (`4, 8, 16, 32`). The
launch bar says "sweep of 4 runs by worker count, 10s each". **Live Metrics** shows a banner
(`Sweep of 4 runs ... Level 2 of 4: 8 workers`) and moves through the levels by itself. Each
finished level is stored right away, so **Stop** keeps the levels already done (the one you
interrupted is saved and marked stopped).

![Sweep settings](../images/web_sweep_settings.png)

Command line:

```bash
./bin/plgv run -sweep-workers 4,8,16,32 -duration 10s            # by worker count
./bin/plgv run -sweep-rates 10000,20000,40000 -duration 10s      # by target rate
./bin/plgv run -config examples/sweep.yaml
```

The command prints each level and then a results table. With `-summary out.json`, each level is
written to its own file (`out-1.json`, `out-2.json`, ...).

```
sweep results:
  workers             ops/s        p50        p95        p99   errors   scaling
  2                   10863      176µs      205µs      377µs        0      100%
  8                   31331      193µs      786µs    1.507ms        0       72%
  32                  67010      303µs    1.475ms    1.737ms        0       39%
  Throughput was still growing at 32 workers (every step gained 5% or more); try higher counts.
  p99 latency reached twice its 2-worker value at 8 workers.
```

### Read the results

Open **Compare Runs**, **Sweep results**, pick the sweep (or click **Compare this sweep** after a
run, or **Compare sweep** on a row in Run History).

![Sweep results by worker count](../images/web_sweep_results.png)

- The **table** has one row per level with throughput, p50, p95, p99, errors and a last column:
  - **Scaling** (worker sweep): throughput relative to perfectly linear growth from the first
    level. 100% means doubling the workers doubled the throughput.
  - **Served** (rate sweep): achieved throughput divided by the target rate, plus a **Dropped**
    column for open-loop runs.
  - The row with the highest throughput is highlighted; **Open run** opens that level.
- The **charts** show throughput and latency against the level.
- The **notes** say in plain language what the numbers show:

| Note | When it appears |
|---|---|
| More workers stopped helping after N workers | The first step up that gained less than 5% throughput. |
| Throughput was still growing at N workers | No step gained less than 5%; try higher counts. |
| p99 latency reached twice its first-level value at N workers | p99 of a level is at least 2x the first level's. |
| The server (or plgv) could not serve N ops/s | A rate level reached under 95% of its target, or dropped operations. |
| This sweep did not finish all of its levels / A level was stopped early | The numbers are partial. |

![Sweep results by target rate](../images/web_sweep_rate.png)

### Make a sweep fair

- Keep **Preload** on: every level re-writes the dataset first, so each starts from the same data.
- A sweep on a server shared with other applications mixes their load into the numbers (the
  `client-server-mismatch` insight can tell).
- Compare sweeps from two servers by running one on each and comparing the tables side by side.
- For a worker sweep, the machine running plgv needs enough CPU for the highest level; the
  `load-generator-bound` insight warns when plgv itself is the limit.

### Sweep or load profile?

A **sweep** is separate runs with fixed worker counts, so each level has clean percentiles and its
own report. A **ramp** or **step** profile changes the worker count inside one run, which is
quicker but blends the levels together in one set of percentiles.

## Time Machine

Above the charts on **Live Metrics**, **Latency Analysis** and **Server** is the **Time Machine**:
two sliders, **From** and **To**, choose a range of the run. All charts of the view zoom to it and a
line shows the statistics for just that range: average throughput, worst p99, errors and the number
of one-second slices. **Show whole run** resets it.

- The range follows you between those three tabs, and starts fresh when you open another run.
- Use it to look at a ramp's top end, a spike, or the moment something went wrong, without
  being distracted by the rest of the run.
- A link can carry the range: `#run=<id>&tab=live&range=2-10`.
