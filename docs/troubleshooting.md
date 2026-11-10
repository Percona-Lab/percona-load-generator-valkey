# Troubleshooting

Find the message you're seeing, then read the cause next to it. The messages are quoted straight from plgv.

## Connecting

| Message | Cause and fix |
|---|---|
| `cannot connect to H: EOF (the server may require TLS ...)` | You're connecting to a TLS port without `-tls`. Use `-tls` or a `valkeys://` address. |
| `... context deadline exceeded (... does not speak TLS ...)` | You used `-tls` against a plain (non-TLS) port. |
| `certificate required` / `unknown certificate authority` / `signed by unknown authority` | This is a mutual TLS or CA problem. See `security-tls-acl.md`. |
| `WRONGPASS invalid username-password pair` | Wrong `PLGV_USERNAME` / `PLGV_PASSWORD`, or a disabled user. |
| `NOAUTH Authentication required` | The server needs a password: set `PLGV_PASSWORD`. |
| `NOPERM User X has no permissions to run the 'cmd' command` | The ACL user isn't allowed to run that command. Either add it (`security-tls-acl.md` lists what each feature needs) or remove the operation. |
| `target.uris: do not put credentials in the URI` | Credentials can only come from the environment. |
| `PLGV_PASSWORD_FILE: open ...: no such file` | The mounted secret file is missing. |
| `the server is a cluster node; set target.mode to cluster` | Use `-mode cluster`, or leave it on `auto`. |
| `CROSSSLOT` errors in a cluster | A multi-key command spans more than one slot. Set `dataset.hash_tag_groups` (see `cluster-and-sentinel.md`). |

## Results that look wrong

| Symptom | Cause |
|---|---|
| Throughput far below the server's | plgv itself is probably the bottleneck. Check the CPU line, use more workers or `-pipeline`, or run plgv on another machine (`performance-tuning.md`). |
| p99 jumps with a fixed rate | Try coordinated-omission mode (`-co`). See `metrics-and-server-stats.md`. |
| High miss rate on a first run | The dataset wasn't preloaded for the types you use, or `-preload=false` was set. |
| Client and server op counts differ by a few | Other clients, or the `INFO` and `SLOWLOG` polling commands, can cause this. |
| Replay shows many status or checksum differences | The target didn't start from the recording's data, several workers interleaved differently, or the recording included `TTL`, `SPOP` or other random replies (`record-and-replay.md`). |
| `start data: DIFFERENT` | Preload the target with the recording's settings and seed (`plgv run -preload-only`). The server should hold only that data. |
| Replay "fell behind the recording" | The target can't keep up with the recorded pace. Use `-speed 0` to compare capacity instead. |
| `deterministic` run rejected | Remove `PUBLISH`, `XCONSUME`, `WATCH_INCR` or `{now}` from the workload (`deterministic-mode.md`). |
| Fingerprint differs between two runs | The two runs differ in seed, budget, workers, mix, dataset or (with composite operations) pipeline depth. |

## Recording and import

| Message | Cause |
|---|---|
| `recording failed ... writer_queue_full` | The disk can't keep up with the workload. Use `-record-backpressure block` or `.zst` compression. |
| `recording truncated ... max_bytes_reached` | You hit the `-record-max-mb` limit. The file can still be replayed. |
| `... exists and is not a plgv recording; refusing to overwrite it` | The `-record` path points at some other file. |
| `cannot be replayed: the file has no trailer` | The recording never finished, because the process stopped. Record it again. |
| `the commands do not match the trailer's hash: the file was changed` | Someone edited the file after it was recorded. |
| `nothing to import: all N entries were skipped` | Every captured command was filtered out. The command printed the reasons. |

## The web UI

| Symptom | Cause |
|---|---|
| `403 this server answers requests addressed to localhost...` | You opened it using a different host name. Use `localhost` or `127.0.0.1`, or start it with `-allowed-host`. |
| `plgv web: ... is not a loopback address` | Pass `-allow-remote`, and read `security-tls-acl.md` first. |
| `cross-site request refused` | A request came from a different origin. |
| `record.path: tick Record this run to write a recording` | Use the *Record a workload* card, or turn on the Record switch. See `record-and-replay-web-ui.md`. |
| `typing a recording path is off because plgv web accepts remote connections` | Typed paths only work on a loopback address. Pick a recording from the list or upload one. |
| Settings page shows no TLS fields | TLS is an Advanced setting. Switch the Settings level to Advanced, then open the *Connection* category. |

## Still stuck

Try to reproduce the problem with the smallest case you can (`-duration 5s -workers 1`). When you ask for help,
include the exact command, the first error line, the output of `plgv version` and the server's `valkey_version`.
Please don't attach a recording, because it contains your data.
