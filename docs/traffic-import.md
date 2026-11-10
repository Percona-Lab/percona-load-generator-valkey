# Traffic import

`plgv import-traffic` turns traffic you captured from a real Valkey server into a recording. Then
`plgv replay` can send it to another server, a new version or a staging copy. It can read:

- **`MONITOR` output** from a file, or captured live by plgv for a short, capped time
- **`SLOWLOG` entries** from a JSON export, or read directly from a server

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

`MONITOR` makes the server send every command it executes to the monitoring connection, and it can cut the
server's throughput by half or more while it runs. Because of that, plgv caps a live capture (`-capture-duration`,
default 10 s, and `-capture-max`, default 100,000 commands) and prints a warning. Don't capture on a server you
can't afford to slow down. Capturing uses a plain TCP connection (no TLS), and credentials come from
`PLGV_USERNAME` and `PLGV_PASSWORD`.

The captured traffic is your data, in clear text, and so is the recording. plgv writes the recording with mode
`0600`. A MONITOR file you saved yourself may contain passwords (`AUTH`). Whatever flags you use, plgv never
copies `AUTH`, `HELLO`, `MIGRATE` or `ACL` commands into a recording.

## What the conversion does

| Captured | Result |
|---|---|
| Each connection | One worker (up to `-max-workers`, default 64). If there are more connections, they share workers in time order. |
| Time of each command | Its offset in the recording, so a paced replay reproduces the original timing. |
| `MULTI` ... `EXEC`, `WATCH` ... | One group, sent on a dedicated connection the way plgv sends its own transactions. |
| Everything else | One group per command (MONITOR can't show pipelining). |
| Commands issued by a Lua script or function | Skipped. The `EVAL` or `FCALL` that ran them is replayed instead. |
| Another database than `-db` (default 0) | Skipped, because replay uses the default database. |
| `MONITOR`, `SELECT`, `SUBSCRIBE`, `INFO`, `CONFIG`, `CLIENT`, `PING`, `COMMAND`, ... | Skipped, because they aren't workload. |
| `FLUSHALL`, `FLUSHDB`, `DEBUG`, `CONFIG SET`, `SHUTDOWN`, ... | Skipped unless `-allow-dangerous`. |
| Slowlog entries the server shortened (`... (N more bytes)`) | Skipped, because they can't be replayed faithfully. |

The command prints how many entries it converted and how many it skipped, grouped by reason. You can see the
same summary later with `plgv replay -info FILE`. If an import skips everything, that's an error and no file is
written.

## Limits

- An imported recording has **no replies, latencies or starting-data fingerprint**. There's nothing to compare
  outcomes with, and `plgv replay` tells you the starting data wasn't checked.
- The slowlog only holds *slow* commands, with times in whole seconds. It's a sample of the traffic, not the
  traffic itself.
- Values are literal. A command whose effect depends on the server's clock or on random choices (`SPOP`,
  `EXPIREAT` with a fixed time, ...) behaves differently on replay.
- If you replay writes onto a server that doesn't hold the data the traffic expected, you'll get misses and
  errors. Prepare the target first, or start with `plgv replay -reads-only`.

## Then replay it

```sh
plgv replay -addr staging:6379 -reads-only prod.jsonl.zst      # safe, because it skips writes
plgv replay -targets v8=a:6379,v9=b:6379 -speed 1 prod.jsonl.zst   # compare two versions at the recorded pace
```

## In the web UI

In **Replay a recorded workload**, turn on **Advanced** and open **Data and commands > Import captured traffic**.
Paste the capture, choose a file, or (if typed paths are on) name a file on the server, then click **Convert to a
recording**. The result is a recording in the recordings folder, already chosen for the replay.

Live capture is only available on the command line, because `MONITOR` can slow the server down. See
[record-and-replay-web-ui.md](record-and-replay-web-ui.md#import-captured-traffic).
