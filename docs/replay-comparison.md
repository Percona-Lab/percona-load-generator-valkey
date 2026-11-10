# Replay comparison

`plgv compare` and the **Replay Comparison** tab compare replays of the same recording. They tell you what got
faster or slower, whether anything returned different results, and whether you can trust the comparison.

![Replay Comparison](../images/web_replay_comparison.png)

## Make the inputs

Each replay writes a summary when you pass `-summary`:

```sh
plgv replay -label v8 -addr 10.0.0.1:6379 -speed 0 -summary v8.json run.jsonl.zst
plgv replay -label v9 -addr 10.0.0.2:6379 -speed 0 -summary v9.json run.jsonl.zst
```

You can also get both at once from `-targets` (see `multi-target.md`). A summary holds totals, percentiles,
per-command latency, comparisons with the recording and the starting-data check. It doesn't contain keys,
values, commands or credentials.

## Compare from the command line

```sh
plgv compare v8.json v9.json            # the first file is the baseline
plgv compare both.json                  # a -targets file
plgv compare -tolerance 5 -json a.json b.json c.json
plgv compare -fail-on-regression v8.json v9.json   # exit 1 if slower, mixed or not comparable
```

| Flag | Meaning |
|---|---|
| `-tolerance P` | How many percent a metric can change before it counts as better or worse (default 10). |
| `-json` | Prints the comparisons as JSON. |
| `-fail-on-regression` | Exits with 1 when a target is `slower`, `mixed` or `not comparable` against the first. |

## Compare in the web UI

Open the **Replay Comparison** tab. It lists the replays you ran from the page
([record-and-replay-web-ui.md](record-and-replay-web-ui.md)). Tick two or more and press **Compare selected**.

You can also choose summary files written by `plgv replay -summary`. Your browser reads them and sends them to
the server of that page, only so they can be compared. Nothing is stored, and no file on the server is read.
Change *Unchanged within* to run the comparison again with a different tolerance.

A link with `#replay=<base64url of a JSON array of summaries>` opens the tab with that comparison. That's how we
made the screenshot above.

## How to read it

- **Verdict:** each target gets one, measured against the baseline. It's `faster`, `slower`, `mixed` (some of
  throughput, p50, p95 and p99 are better and some are worse), `equivalent`, or `not comparable`.
- A metric only counts as *better* or *worse* when it changed by more than the tolerance, **and** by more than
  0.1 ms if it's a latency. That way microsecond noise on a fast server isn't reported. Errors are compared
  exactly.
- A comparison is **not comparable** when the replays used different recordings (their command hashes differ) or
  one of them didn't run. If the replays used different `-speed` or `-reads-only` settings, the comparison still
  runs, but with a warning.
- **Findings** explain the verdict. They cover worse and better metrics, more errors (critical when the baseline
  had none), replies that differ from the recording more often than on the baseline, a starting-data mismatch
  on either side, a paced replay that fell behind the recording, different server versions, and the three
  commands whose p99 got worse the most.
- **By command** lists each command's p99 on both sides.

A comparison is only as good as the replays behind it. Make them from the same starting data, with the same
options, on servers that are otherwise idle. Repeat a replay (`-iterations`) before you trust a small
difference.
