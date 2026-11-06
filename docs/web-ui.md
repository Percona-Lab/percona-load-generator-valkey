# Web UI

The web UI runs workloads, shows them live, and keeps every finished run so you can reopen,
compare and chart them. It is one page served by the plgv binary: no install, no network access
needed (the chart library is bundled).

## Start it

```bash
make valkey-up              # a Valkey to test against (see VALKEY_TEST_ENV.md)
make build-local
./bin/plgv web              # then open http://127.0.0.1:8080
```

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8080` | Listen address. |
| `-data-dir` | `plgv-data` | Where finished runs (`runs/`) and saved settings (`settings/`) are kept. |
| `-keep-runs` | 100 | Newest finished runs to keep; older ones are deleted. 0 keeps all. |
| `-open` | off | Open the page in your browser. |
| `-allow-remote` | off | Allow a non-loopback address. See Security. |
| `-allowed-host` | empty | Host names the UI answers to (for example `localhost,127.0.0.1` behind a port published on loopback only). |

Credentials for the server under test are **not** typed into the page. Set `PLGV_USERNAME` and
`PLGV_PASSWORD` in the environment before starting `plgv web`.

## Your first run

The front page asks **What do you want to do?** and shows a card for each kind of workload.

![The front page: pick a workload](../images/web_home.png)

| Card | Use it to |
|---|---|
| **Standard workload** | Run 80% `GET` and 20% `SET` on string keys: the quickest way to see what a server can do. |
| **Custom workload** | Build your own operation mix from every command family ([custom-workload-builder.md](custom-workload-builder.md)). |
| **Your own commands** | Paste Valkey commands and run them as the workload ([raw-injector.md](raw-injector.md)). |
| **Find the limit** | Run the same workload at 4, 16, 64 and 128 workers ([load-profiles-and-sweep.md](load-profiles-and-sweep.md)). |
| **Cache, Session store, Leaderboard, Work queue, Rate limiter, Pub/sub fan-out, Counters and analytics, Big keys stress, Mixed** | Run a typical application's traffic ([presets.md](presets.md)). |
| **Open completed runs**, **Compare runs**, **Compare replays**, **Trends** | Go to the results sections. |

1. **Click a card.** Its settings open with the defaults for that workload already selected: the operations,
   the dataset, the worker count it needs. The server, workers and duration you already set carry over when you
   switch to another workload (use the **Workload** list at the top of the settings to switch).
2. Check the **Server address** and change what you want. Press **Use defaults** on a card, or
   **Reset to defaults** under the title, to start that workload from its defaults again; your server address is kept.
3. The bar at the bottom shows what will run, for example `Cache · 64 workers for 10s · 5 operations · valkey://127.0.0.1:6379 · server metrics off`.
   Click **Start workload**. The page moves to Live Metrics.
4. The status badge in the header shows `connecting`, `preloading dataset` (not measured), `running`, then
   `finished`, and the clock beside it shows elapsed and planned time. **Stop** ends the run early; the partial run is saved and marked "stopped early".
5. When the run finishes it opens automatically, and is saved to Run History (**Completed Runs** in the header).

![Settings of a chosen workload](../images/web_settings.png)

The settings are grouped by level and category: Basic settings are on one screen; **Advanced** and **Expert**
add a category list on the left, one category at a time ([settings-levels.md](settings-levels.md)). The
workload cards are locked while a run is going. **Exit** in the header stops the plgv server (it asks first).

Only one run can be active at a time. If a setting is not valid, the run does not start and each
problem is shown next to its field; the category that holds it is marked, and a hidden Advanced or
Expert level is turned on so you can reach it.

## The tabs

Once a workload is chosen, a row of tabs appears under the header. The brand (**PLGV**) and **All workloads** go back to the front page.

| Tab | What it shows | Page |
|---|---|---|
| **New run** | The settings of the chosen workload, at three levels and in categories, plus saved settings. | [settings-levels.md](settings-levels.md), [custom-workload-builder.md](custom-workload-builder.md), [server-metrics.md](server-metrics.md) |
| **Live Metrics** | Throughput, latency, errors and the per-operation table, updating while the run goes. | below |
| **Latency Analysis** | Percentiles, latency over time and per-operation latency for a finished run. | below |
| **Server** | Server-side charts (ops/s, memory, hit ratio, clients); for a cluster, the nodes table, topology changes and per-node charts. Needs server metrics. | [server-metrics.md](server-metrics.md), [cluster-and-sentinel.md](cluster-and-sentinel.md) |
| **Slowlog** | Commands the server found slow during the run. Needs server metrics. | [server-metrics.md](server-metrics.md) |
| **Key Space** | Types, sizes, expiry and the biggest keys after a run. | [key-space-analysis.md](key-space-analysis.md) |
| **Insights** | Findings with evidence and advice. | [insights.md](insights.md) |
| **Run History** | Every finished run; open, report, delete, pick two to compare. | [trends-and-history.md](trends-and-history.md) |
| **Replay Comparison** | Compare replays of one recording on different servers (files from `plgv replay -summary`). | [replay-comparison.md](replay-comparison.md) |
| **Compare Runs** | Two runs side by side, and sweep results. | [compare-runs.md](compare-runs.md), [load-profiles-and-sweep.md](load-profiles-and-sweep.md) |
| **Trends** | The same workload over time, with regression flags. | [trends-and-history.md](trends-and-history.md) |

## Live Metrics

![Live Metrics](../images/web_live.png)

- **Cards:** throughput, operations (with misses), p50, p95, p99 and errors. While running they
  show the latest one-second slice; for a finished run, the whole-run figures.
- **Charts:** throughput, latency (p50, p95, p99) and errors per slice.
- **Per operation:** operations, share, misses, errors and p99 for every entry of the mix.
- **Banner:** when a stored run is shown, it links to its [HTML report](reports-and-exports.md).

## Latency Analysis

![Latency Analysis](../images/web_latency.png)

Whole-run mean, p50, p95, p99, p99.9 and max, latency over time, and per-operation percentiles. A
note states how latency was measured (service time, or coordinated-omission aware when a target
rate and that option were set). With pipelining, every operation in a batch is charged the batch
round trip.

## Presets and key space

*Workload* offers **Start from a preset** ([presets.md](presets.md)). With *Key-space analysis* on (Advanced), the **Key Space** tab shows the types, sizes, expiry and biggest keys of a finished run ([key-space-analysis.md](key-space-analysis.md)).

## Advanced commands and raw commands

The mix builder offers streams, geo, HyperLogLog, bitmaps, scripts, pub/sub, transactions and raw commands; Advanced settings add pub/sub subscribers and a raw command box. Latency Analysis shows pub/sub delivery. See [advanced-commands.md](advanced-commands.md) and [raw-injector.md](raw-injector.md).

## Time Machine

On Live Metrics, Latency Analysis and Server, two sliders choose a time range; the charts zoom to it
and a line gives the statistics for just that range. See [load-profiles-and-sweep.md](load-profiles-and-sweep.md#time-machine).

## Links to a view

The address bar supports a few shortcuts, handy for sharing or for documentation:
`#tab=history`, `#run=<run id>&tab=server`, `#compare=<id>,<id>`, `#tab=settings&levels=advanced,expert`
`#srv=on`, `#sweep=<sweep id>`, `#run=<id>&tab=live&range=2-10`, and `#set=path:value,...` to pre-fill settings (lists use `;`, for example `#set=target.uris:127.0.0.1:7001;127.0.0.1:7002`).

## Security

The UI has **no login** and can run load against any server the machine can reach. `plgv web`
therefore refuses a non-loopback address unless you pass `-allow-remote`, answers only requests addressed
to `localhost`, `127.0.0.1` or `[::1]` (so a web page on another domain cannot drive it through DNS
rebinding), rejects requests that do not come from its own page (a different origin, or a body that is not
JSON), and sends a strict Content-Security-Policy. If you expose it beyond your machine, put it behind your
own access control and read `security-tls-acl.md`. Settings include an Advanced **Encryption (TLS)** card;
recording is not available from the UI.

Saved settings and stored runs never contain credentials.

## Design

The page follows a small token-based design system: [web-ui-design-system.md](web-ui-design-system.md).
