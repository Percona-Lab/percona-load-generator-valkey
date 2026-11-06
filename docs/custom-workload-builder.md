# Custom workload builder

The **Workload** card (pick **Custom workload** on the front page, or any workload and edit it) builds the
operation mix: which commands to run and in what proportion. Every worker repeatedly picks one entry by weight and sends it.

![Settings: workload builder](../images/web_settings.png)

## Using it

- Each row is one operation: choose the **Operation** (grouped by Strings, Keys, Hashes, Lists,
  Sets, Sorted sets), set its **Weight**, and the **Share** column shows its percentage of the
  mix. Weights are relative, so 80 and 20 is the same as 4 and 1.
- **Add operation** appends a row; **Remove** deletes one. At least one row is needed.
- Some operations take parameters, and the builder shows only the ones that apply:

| Parameter | Shown for | Meaning |
|---|---|---|
| count | `MGET`, `MSET`, `LRANGE`, `ZRANGE`, `ZRANGEBYSCORE`, `SRANDMEMBER`, `SCAN` | Keys per call, elements returned, or scan page size. |
| fields | `HSET` | Fields written per call (up to the dataset's hash fields). |
| type | `DEL`, `UNLINK`, `EXISTS`, `TTL`, `EXPIRE`, `SCAN` | Which key type to act on: string, hash, list, set or sorted set. |

Changing an operation drops parameters it does not take, so a stale value is never sent.

## The dataset behind the mix

Keys live in one namespace per type, so a mixed workload never meets a key of the wrong type.
The **Dataset** card sets keys per type, the key distribution (uniform or zipf, a few hot keys)
and the value size; Advanced adds the collection size, hash fields, TTL model and preload. See
[workloads.md](workloads.md) for what each operation does and [configuration-reference.md](configuration-reference.md)
for every setting.

## Validation

A mix that cannot run (a parameter on an operation that does not take it, a weight below 1)
is reported next to the field when you start the run.

Save a mix you like with **Save current** (see [settings-levels.md](settings-levels.md)).
