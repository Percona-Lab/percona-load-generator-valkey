# Security: TLS, ACL users and secrets

plgv connects to your Valkey, writes data to it and (optionally) records what it sent. This page says how
to do that safely: encrypt the connection, give plgv a user that can only do what the benchmark needs, keep
passwords out of files and logs, and keep the web UI private.

- [TLS and mutual TLS](#tls-and-mutual-tls)
- [ACL users](#acl-users)
- [Secrets](#secrets)
- [What plgv writes to disk](#what-plgv-writes-to-disk)
- [The web UI](#the-web-ui)
- [Dependencies and review](#dependencies-and-review)

## TLS and mutual TLS

TLS is **off by default**. Turn it on with a `valkeys://` (or `rediss://`) address, or with the settings below (in the web UI: tick Advanced, open the *Connection* category).

![TLS settings](../images/web_tls_settings.png)

| YAML (`target.tls`) | CLI flag | Environment | Meaning |
|---|---|---|---|
| `enabled` | `-tls` | `PLGV_TLS=true` | Connect over TLS. A `valkeys://` address turns it on too. |
| `ca_file` | `-tls-ca FILE` | `PLGV_TLS_CA_FILE` | PEM file of the authorities that sign the server certificate. Empty = the system's. |
| `cert_file`, `key_file` | `-tls-cert`, `-tls-key` | `PLGV_TLS_CERT_FILE`, `PLGV_TLS_KEY_FILE` | Client certificate and key for **mutual TLS**. Both or neither. |
| `server_name` | `-tls-server-name` | `PLGV_TLS_SERVER_NAME` | Check the certificate against this name instead of the address's host (for example when connecting by IP). |
| `insecure_skip_verify` | `-tls-insecure` | | Accept any server certificate. Removes the protection against a man in the middle: throwaway tests only. |

```sh
plgv run -addr valkey.internal:6379 -tls-ca /etc/plgv/ca.crt -tls-cert /etc/plgv/client.crt -tls-key /etc/plgv/client.key \
  -preset cache -duration 60s
```

A TLS file or option on the command line turns TLS on by itself (`-tls-ca ca.crt` is enough). The minimum
protocol version is TLS 1.2. The same settings apply to `plgv replay`, `plgv import-traffic` (`-capture`
and `-slowlog-from`), cluster nodes and sentinels, and to both client libraries. Only **paths** are stored
in configuration; certificate and key contents never are. The TLS settings are where the run connects, not
what it does, so they are not part of the workload fingerprint that Trends groups by.

Errors say what went wrong:

| You see | Meaning |
|---|---|
| `certificate required` | The server needs a client certificate (mutual TLS): add `-tls-cert` and `-tls-key`. |
| `certificate signed by unknown authority` | Wrong or missing `-tls-ca`. |
| `unknown certificate authority` | The server does not trust your client certificate's authority. |
| `certificate is valid for X, not Y` | Use `-tls-server-name Y` or connect by the name in the certificate. |
| `EOF` ... `the server may require TLS` | You connected without TLS to a TLS port. |
| `context deadline exceeded` ... `does not speak TLS` | You used TLS against a plain port. |

## ACL users

Run plgv as a user that can do what the benchmark needs and nothing more. Never run it as an
administrator, and never allow `FLUSHALL`, `DEBUG`, `CONFIG` or `MONITOR` for it unless you mean to.

A rule that ran every preset, server metrics and key-space analysis in the test environment (the `bench`
user of `testdata/acl/users.acl`):

```
user bench on >CHANGE-ME ~plgv* -@all +@read +@write +@keyspace +@string +@hash +@list +@set +@sortedset +@stream
  +@geo +@hyperloglog +@bitmap +@pubsub +@transaction +@connection +eval +evalsha +eval_ro +evalsha_ro +fcall +fcall_ro
  +script|load +function|load +function|list +info +slowlog|get +latency|latest +memory|usage +dbsize +scan +type
  -flushall -flushdb
```

(one line in the ACL file). What each feature needs, beyond the commands of the operations in your mix:

| Feature | Commands |
|---|---|
| Any run | `+@connection` (`HELLO`, `CLIENT SETINFO`, `PING`) and `+info` to detect the server. |
| Server metrics | `+info +slowlog\|get +latency\|latest`; for a cluster also `+cluster\|shards`. |
| Key-space analysis | `+scan +type +memory\|usage +pttl` and a length command per type (`+strlen +hlen +llen +scard +zcard +xlen`). |
| Record or replay with the starting-data check | `+dbsize +scan +type` and the length commands. The check scans **every** key, so give it read access to all keys: `%R~*` next to your `~plgv*` write pattern. |
| Reads-only replay | `+@read` only: the `reader` user in `testdata/acl/users.acl`. |
| Scripts and functions | `+eval +evalsha +fcall` (and `+script\|load`, `+function\|load` to install them). |
| `import-traffic -capture` | `+monitor` (a dangerous command: use an admin user for the capture, not the benchmark user). |
| `import-traffic -slowlog-from` | `+slowlog\|get`. |

A command the user may not run is not fatal: it is counted as an error of class `NOPERM`, the first message
is shown, and the run continues. Server metrics and the key-space analysis report what they could not read.
Restrict keys with `~patterns` that match `dataset.key_prefix` (default `plgv:`).

Credentials: `PLGV_USERNAME` and `PLGV_PASSWORD`. The default user of a server with `requirepass` needs only
`PLGV_PASSWORD`.

## Secrets

Credentials come **only from the environment or a mounted file**, never from a YAML file, a flag or a URI
(`valkey://user:pass@host` is rejected).

| Variable | Meaning |
|---|---|
| `PLGV_USERNAME`, `PLGV_PASSWORD` | The credentials. |
| `PLGV_USERNAME_FILE`, `PLGV_PASSWORD_FILE` | Read the value from a file (a Kubernetes Secret or Docker secret mount). A file wins over the variable; one trailing newline is removed; an unreadable file stops the run. |

The password is never written to the configuration, run summary, JSON export, HTML report, recording, replay
summary, log or error message. Tests check this with a real ACL server: a wrong password is reported as
`WRONGPASS` without the password in the text, and none of the outputs of a successful run holds it. The web
UI has no password field: it uses the environment of the `plgv web` process.

## What plgv writes to disk

| File | Holds | Protection |
|---|---|---|
| Run summaries, saved settings (`plgv-data/`) | Results, configuration without credentials | Directory `0700`. |
| Recordings (`-record`, `import-traffic -out`) | **Your keys and values in clear text** | File `0600`; never the target address or credentials; refuses to overwrite a file that is not a recording. |
| Replay summaries | Totals and percentiles; no commands, keys or values | `0600`. |
| MONITOR captures | Every command, including `AUTH` | Captured in memory only; `AUTH`, `HELLO`, `MIGRATE` and `ACL` commands never reach a recording. |

Treat a recording like a database dump. Do not put one in a bug report. To share a result, share a replay or
run summary instead.

## The web UI

`plgv web` has no login. It is meant for one person on one machine, so it:

- listens on `127.0.0.1` by default and refuses another address unless you pass `-allow-remote`;
- answers only requests addressed to `localhost`, `127.0.0.1` or `[::1]` (this stops DNS rebinding: a web
  page on another domain that resolves to your loopback). With `-allow-remote` use
  `-allowed-host name1,name2` to say which host names it answers to;
- refuses cross-site writes (an `Origin` that differs from the `Host`, or a body that is not JSON);
- sends a Content-Security-Policy that allows only its own scripts and connections, forbids framing, and
  serves reports with a policy that runs no script at all;
- lets a request name a file on the machine only while it listens on loopback: a typed recording path, or a
  capture to import. With `-allow-remote` typed paths are off, and recordings are chosen from a list or
  uploaded. A recording is written to a new name inside `<data-dir>/recordings` unless you type a path, and a
  typed path may only create a file or replace an earlier plgv recording, never overwrite another file. A file
  that is read must be a regular file that starts like a plgv recording, and its content is never echoed. Uploads
  are checked, size-limited and stored under a name the server chooses
  ([record-and-replay-web-ui.md](record-and-replay-web-ui.md#where-files-live-and-typed-paths));
- only reads the TLS files you named from the machine it runs on;
- validates run ids and setting names, so no request can reach a file outside its data directory.

If you expose it (a container, a shared host), put it behind a reverse proxy that authenticates users, and
read `deployment.md`.

## Dependencies and review

`govulncheck ./...` reports no vulnerability that plgv's code reaches (CI runs it on every push). The full
dependency list with licenses is `THIRD_PARTY.md`. The attack surface (web server, file
handling, credentials, outputs, container) was reviewed, and tests keep each finding fixed (see
`internal/webui/security_test.go` and `internal/config/tls_test.go`). To report a vulnerability, see `SECURITY.md`.
