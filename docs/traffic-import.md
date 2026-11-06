# Traffic import

`plgv import-traffic` turns traffic you captured from a real Valkey server into a recording, so
`plgv replay` can send it to another server, a new version or a staging copy. It reads:

- **`MONITOR` output** from a file, or captured live by plgv for a short, capped time;
- **`SLOWLOG` entries** from a JSON export, or read from a server.

```sh
# From a file (the format is detected)
plgv import-traffic -out prod.jsonl.zst monitor.txt

# Live: MONITOR on a server for at most 10 seconds or 100,000 commands
plgv import-traffic -capture 10.0.0.5:6379 -capture-duration 10s -out prod.jsonl.zst

# Slowlog from a file or a server
valkey-cli --json slowlog get 128 > slow.json
plgv import-traffic -out slow.jsonl slow.json
plgv import-traffic -slowlog-from 10.0.0.5:6379 -out slow.jsonl
```

## Read this before capturing

`MONITOR` makes the server send every command it executes to the monitoring connection, and can cut
the server's throughput by half or more while it runs. plgv therefore caps a live capture
(`-capture-duration`, default 10 s; `-capture-max`, default 100,000 commands) and prints a warning.
Do not capture on a server you cannot afford to slow down. Capturing uses a plain TCP connection
(no TLS); credentials come from `PLGV_USERNAME` and `PLGV_PASSWORD`.

The captured traffic is your data, in clear text, and so is the recording. plgv writes the recording
with mode `0600`. A MONITOR file you saved yourself may contain passwords (`AUTH`): plgv never copies
`AUTH`, `HELLO`, `MIGRATE` or `ACL` commands into a recording, whatever the flags.

## What the conversion does

| Captured | Result |
|---|---|
| Each connection | One worker (up to `-max-workers`, default 64; more connections share workers in time order). |
| Time of each command | Its offset in the recording: a paced replay reproduces the original timing. |
| `MULTI` ... `EXEC`, `WATCH` ... | One group sent on a dedicated connection, as plgv sends its own transactions. |
| Everything else | One group per command (MONITOR cannot show pipelining). |
| Commands issued by a Lua script or function | Skipped: the `EVAL` or `FCALL` that ran them is replayed. |
| Another database than `-db` (default 0) | Skipped: replay uses the default database. |
| `MONITOR`, `SELECT`, `SUBSCRIBE`, `INFO`, `CONFIG`, `CLIENT`, `PING`, `COMMAND`, ... | Skipped: not workload. |
| `FLUSHALL`, `FLUSHDB`, `DEBUG`, `CONFIG SET`, `SHUTDOWN`, ... | Skipped unless `-allow-dangerous`. |
| Slowlog entries the server shortened (`... (N more bytes)`) | Skipped: they cannot be replayed faithfully. |

The command prints how many entries were converted and how many were skipped, by reason, and
`plgv replay -info FILE` shows it again later. An import that skips everything is an error and writes no file.

## Limits you should know

- An imported recording has **no replies, latencies or starting-data fingerprint**: there is nothing to
  compare outcomes with, and `plgv replay` says the starting data was not checked.
- The slowlog only holds *slow* commands, with times in whole seconds: it is a sample of traffic, not
  the traffic.
- Values are literal. A command whose effect depends on the server's clock or random choices
  (`SPOP`, `EXPIREAT` with a fixed time, ...) behaves differently on replay.
- Replaying writes onto a server that does not hold the data the traffic expected produces misses and
  errors. Prepare the target first, or start with `plgv replay -reads-only`.

## Then replay it

```sh
plgv replay -addr staging:6379 -reads-only prod.jsonl.zst      # safe: skips writes
plgv replay -targets v8=a:6379,v9=b:6379 -speed 1 prod.jsonl.zst   # compare two versions at the recorded pace
```

## In the web UI

In **Replay a recorded workload**, turn on **Advanced** and open **Data and commands > Import captured traffic**: paste
the capture, choose a file, or (when typed paths are on) name a file on the server, then **Convert to a recording**.
The result is a recording in the recordings folder, chosen for the replay. Live capture is command-line only because
`MONITOR` can slow the server. See [record-and-replay-web-ui.md](record-and-replay-web-ui.md#import-captured-traffic).
