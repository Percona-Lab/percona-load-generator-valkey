# Troubleshooting

Find the message, then the cause. Messages are quoted from plgv.

## Connecting

| Message | Cause and fix |
|---|---|
| `cannot connect to H: EOF (the server may require TLS ...)` | A TLS port without `-tls`. Use `-tls` or a `valkeys://` address. |
| `... context deadline exceeded (... does not speak TLS ...)` | `-tls` against a plain port. |
| `certificate required` / `unknown certificate authority` / `signed by unknown authority` | Mutual TLS or CA problem: see `security-tls-acl.md`. |
| `WRONGPASS invalid username-password pair` | Wrong `PLGV_USERNAME` / `PLGV_PASSWORD`, or a disabled user. |
| `NOAUTH Authentication required` | The server needs a password: set `PLGV_PASSWORD`. |
| `NOPERM User X has no permissions to run the 'cmd' command` | The ACL user lacks that command: add it (`security-tls-acl.md` lists what each feature needs) or remove the operation. |
| `target.uris: do not put credentials in the URI` | Credentials come from the environment only. |
| `PLGV_PASSWORD_FILE: open ...: no such file` | The mounted secret is missing. |
| `the server is a cluster node; set target.mode to cluster` | Use `-mode cluster` (or leave it `auto`). |
| `CROSSSLOT` errors in a cluster | A multi-key command spans slots: set `dataset.hash_tag_groups` (see `cluster-and-sentinel.md`). |

## Results that look wrong

| Symptom | Cause |
|---|---|
| Throughput far below the server's | plgv is the bottleneck: check the CPU line, use more workers or `-pipeline`, run it on another machine (`performance-tuning.md`). |
| p99 jumps with a fixed rate | Use coordinated-omission mode (`-co`), see `metrics-and-server-stats.md`. |
| High miss rate on a first run | The dataset was not preloaded for the types used, or `-preload=false` was set. |
| Client and server op counts differ by a few | Other clients, or `INFO`/`SLOWLOG` polling commands. |
| Replay shows many status or checksum differences | The target did not start from the recording's data, several workers interleave differently, or the recording used `TTL`/`SPOP`/random replies (`record-and-replay.md`). |
| `start data: DIFFERENT` | Preload the target with the recording's settings and seed (`plgv run -preload-only`), on a server that holds only that data. |
| Replay "fell behind the recording" | The target is slower than the recorded pace: use `-speed 0` to compare capacity. |
| `deterministic` run rejected | Remove `PUBLISH`, `XCONSUME`, `WATCH_INCR` or `{now}` (`deterministic-mode.md`). |
| Fingerprint differs between two runs | Different seed, budget, workers, mix, dataset or (with composite operations) pipeline depth. |

## Recording and import

| Message | Cause |
|---|---|
| `recording failed ... writer_queue_full` | The disk is slower than the workload: use `-record-backpressure block` or `.zst` compression. |
| `recording truncated ... max_bytes_reached` | `-record-max-mb` reached; the file is still replayable. |
| `... exists and is not a plgv recording; refusing to overwrite it` | The `-record` path is another file. |
| `cannot be replayed: the file has no trailer` | The recording did not finish (the process stopped). Record again. |
| `the commands do not match the trailer's hash: the file was changed` | The file was edited after recording. |
| `nothing to import: all N entries were skipped` | Every captured command was filtered out: see the reasons the command printed. |

## The web UI

| Symptom | Cause |
|---|---|
| `403 this server answers requests addressed to localhost...` | You opened it by another host name. Use `localhost` or `127.0.0.1`, or start it with `-allowed-host`. |
| `plgv web: ... is not a loopback address` | Pass `-allow-remote` (and read `security-tls-acl.md`). |
| `cross-site request refused` | A request came from another origin. |
| `record: recording writes a file, so it is available from the command line only` | Use `plgv run -record`. |
| Settings page shows no TLS fields | They are an Advanced setting: tick Advanced in Settings level, then open the *Connection* category. |

## Still stuck

Run with the smallest case that shows it (`-duration 5s -workers 1`), keep the exact command and the first
error line, and include `plgv version` and the server's `valkey_version`. Do not attach a recording: it holds
your data.
