# Raw command injector

You can run your own Valkey commands as the workload. They're mixed by weight, rate-limited and measured like
any other operation, and they show up in the results under their own text.

![The Raw commands card in the settings](../images/web_raw_injector.png)

## Writing commands

Put one command on each line, and start it with a weight if you like. `#` starts a comment.

```text
6 GET {key}
2 SET {key} {value} EX 60
1 HSET {key:hash} visits {n}
ZADD {key:zset} {int:1-1000} member{n}
```

Quote any text that has spaces (`"hello world"`). You can use these placeholders:

| Placeholder | Becomes |
|---|---|
| `{key}`, `{key:TYPE}` | A key from the dataset (`string` by default; `hash`, `list`, `set`, `zset`, `stream`, `geo`, `hll`, `bitmap`), drawn with the key distribution. |
| `{value}` | A random value of `dataset.value_size`. |
| `{n}` | A key number. |
| `{int:LO-HI}` | A random integer in the range. |
| `{now}` | The current time in milliseconds. |

## Use it

- **CLI:** `plgv run -raw-file commands.txt` replaces the mix. In YAML, use `op: RAW` with `command:` (see
  `examples/raw.yaml`).
- **Web UI:** the **Your own commands** card opens *Data and commands* (Advanced), with example commands already
  set as the workload. Replace them, or paste or load a file, then choose *Use as the workload* or *Add to the
  mix*. If something's wrong, you'll see it on the field when you start the run.

In a cluster, plgv routes by the first key in the command. Keep to one key per command, or use hash tags.

## The blocklist

plgv checks this list before it sends anything, and the run won't start if a command is blocked.

| Tier | Commands | How to get them |
|---|---|---|
| Dangerous | `FLUSHALL`, `FLUSHDB`, `SHUTDOWN`, `DEBUG`, `MONITOR`, `FAILOVER`, `SWAPDB`, `SAVE`/`BGSAVE`/`BGREWRITEAOF`, `MIGRATE`, `RESTORE`, `MODULE`, `REPLICAOF`/`SLAVEOF`, `CONFIG SET`/`RESETSTAT`/`REWRITE`, `CLIENT KILL`/`PAUSE`, `CLUSTER FAILOVER`/`RESET`/`FORGET` and other slot or membership changes, `SCRIPT FLUSH`/`KILL`, `FUNCTION FLUSH`/`DELETE`/`RESTORE`, `ACL SETUSER`/`DELUSER`, `SLOWLOG RESET`, `LATENCY RESET` | Set `workload.allow_dangerous: true`, pass `-allow-dangerous`, or tick the checkbox in the UI. The run summary and an insight record that it was on. Only do this on a server you can afford to lose. |
| Never | `MULTI`/`EXEC`/`WATCH`, `SELECT`, `AUTH`, `HELLO`, `RESET`, `SUBSCRIBE` and friends, blocking commands (`BLPOP`, `BZPOPMIN`, ...), `XREAD`/`XREADGROUP` with `BLOCK`, `SYNC`/`PSYNC` | Not allowed, because they would break plgv's own connections. Use `TXN`, `WATCH_INCR`, `BRPOP`, `XREAD_BLOCK` and Pub/Sub instead. |

Credentials are never part of a command. Use `PLGV_USERNAME` and `PLGV_PASSWORD` instead.
