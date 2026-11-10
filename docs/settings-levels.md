# Settings levels and categories

Once you've chosen a workload (see [web-ui.md](web-ui.md#your-first-run)), its settings are grouped by **level**,
so the common ones are easy to find, and by **category**, so you never face one very long page.

![Settings with Advanced and Expert on: categories on the left](../images/web_settings_levels.png)

## Levels

The **Settings level** control above the settings has three toggles. Basic is always on. Tick **Advanced**,
**Expert** or both to add more. The browser remembers your choice.

| Level | What is in it |
|---|---|
| **Basic** | Server address and deployment, duration, workers, run label, the workload and operation mix, keys, distribution, value size, server metrics and saved settings. |
| **Advanced** | Pipeline depth, target rate, coordinated-omission latency, loop, load profile, sweep, pub/sub subscribers, seed and deterministic traffic, raw commands, dataset details (prefix, zipf skew, collection size, hash fields, hash tags, TTL model, preload), TLS, key-space analysis, chart sample interval. |
| **Expert** | Client library (`valkey-go` or `go-redis`), slowlog entries per poll. |

If only **Basic** is on, its cards are shown one under another.

## Categories

When **Advanced** or **Expert** is on, a **Categories** list appears on the left. It shows one category at a
time, so no screen gets too long.

![Categories: Workload execution](../images/web_categories.png)

| Category | Holds |
|---|---|
| **Basic settings** | The Basic cards: server, run, workload, dataset, server metrics, save and load. |
| **Connection** | TLS and mutual TLS (Advanced), client library (Expert). See [security-tls-acl.md](security-tls-acl.md). |
| **Workload execution** | Pipelining and pacing, load profile, sweep, pub/sub subscribers (Advanced). See [load-profiles-and-sweep.md](load-profiles-and-sweep.md). |
| **Data and commands** | Dataset details, raw commands (Advanced). See [raw-injector.md](raw-injector.md). |
| **Seed and deterministic** | Random seed, operation budget and deterministic traffic (Advanced). See [deterministic-mode.md](deterministic-mode.md). |
| **Metrics and reporting** | Key-space analysis, chart sample interval (Advanced), slowlog polling (Expert). See [key-space-analysis.md](key-space-analysis.md). |

**Record a workload** adds a **Record** card to Basic settings.

**Replay a recorded workload** only shows what a replay uses. In Basic settings that's the Server card,
*Recording to replay* and *Replay options*. Advanced adds TLS (Connection), *Replay on several servers* and
*Replay checks* (Workload execution), and *Import captured traffic* (Data and commands). There's no Seed or
Metrics category. See [record-and-replay-web-ui.md](record-and-replay-web-ui.md).

A category only appears when it has something to show at the levels you ticked. If you turn a level off while
you're on one of its categories, the page goes back to **Basic settings**.

Some workloads open the category they need. **Find the limit** turns Advanced on and opens **Workload
execution**, where its worker counts are. **Your own commands** opens **Data and commands**.

## Hidden settings still apply

Hiding a level, or switching to another category, only hides cards. Settings you can't see are **not reset**.
They keep their current value and the run uses them. The note beside the level control tells you which levels
are hidden, and the bar at the bottom summarizes what will run.

If a setting has a problem, the run won't start. The message appears under the field, the category that holds it
gets a red dot in the list, and plgv opens the category of the first problem for you. If the problem is in a
level that was off, plgv turns that level on so you can get to it.

## Saved settings

The **Save and load run settings** card (in Basic settings) lets you save a whole configuration under a name and
load it again later.

- **Save current** stores the configuration as `<data-dir>/settings/<name>.yaml`. It's the same YAML that
  `plgv run -config` uses, so you can use a saved setting on the command line too.
- **Load** puts a saved configuration into the form, and **Delete** removes it.
- Names can be 1 to 64 characters long (letters, digits, space, dot, dash, underscore).
- Credentials are never saved.
- If a configuration fails validation, you can't save it, and the error is shown.
