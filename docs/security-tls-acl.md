# Security: TLS, ACL users and secrets

plgv connects to your Valkey, writes data to it and (optionally) records what it sent. This page explains how to
do that safely. You'll learn how to encrypt the connection, give plgv a user that can only do what the
benchmark needs, keep passwords out of files and logs, and keep the web UI private.

- [TLS and mutual TLS](#tls-and-mutual-tls)
- [ACL users](#acl-users)
- [Secrets](#secrets)
- [What plgv writes to disk](#what-plgv-writes-to-disk)
- [The web UI](#the-web-ui)
- [Dependencies and review](#dependencies-and-review)

## TLS and mutual TLS

TLS is **off by default**. Turn it on with a `valkeys://` (or `rediss://`) address, or with the settings below. In
the web UI, switch to Advanced and open the *Connection* category.

![TLS settings](../images/web_tls_settings.png)

| YAML (`target.tls`) | CLI flag | Environment | Meaning |
|---|---|---|---|
| `enabled` | `-tls` | `PLGV_TLS=true` | Connect over TLS. A `valkeys://` address turns it on too. |
| `ca_file` | `-tls-ca FILE` | `PLGV_TLS_CA_FILE` | PEM file of the authorities that sign the server certificate. If it's empty, plgv uses the system's. |
| `cert_file`, `key_file` | `-tls-cert`, `-tls-key` | `PLGV_TLS_CERT_FILE`, `PLGV_TLS_KEY_FILE` | Client certificate and key for **mutual TLS**. Both or neither. |
| `server_name` | `-tls-server-name` | `PLGV_TLS_SERVER_NAME` | Check the certificate against this name instead of the address's host, for example when you connect by IP. |
| `insecure_skip_verify` | `-tls-insecure` | | Accept any server certificate. This removes the protection against a man in the middle, so only use it for throwaway tests. |

```sh
plgv run -addr valkey.internal:6379 -tls-ca /etc/plgv/ca.crt -tls-cert /etc/plgv/client.crt -tls-key /etc/plgv/client.key \
  -preset cache -duration 60s
```

Passing any TLS file or option on the command line turns TLS on by itself, so `-tls-ca ca.crt` is enough. The
minimum protocol version is TLS 1.2. The same settings apply to `plgv replay`, `plgv import-traffic` (`-capture`
and `-slowlog-from`), cluster nodes and sentinels, and to both client libraries.

Only **paths** are stored in configuration. The contents of certificates and keys never are. The TLS settings
control where the run connects, not what it does, so they aren't part of the workload fingerprint that Trends
groups by.

The error messages tell you what went wrong:

| You see | Meaning |
|---|---|
| `certificate required` | The server needs a client certificate (mutual TLS). Add `-tls-cert` and `-tls-key`. |
| `certificate signed by unknown authority` | Wrong or missing `-tls-ca`. |
| `unknown certificate authority` | The server doesn't trust your client certificate's authority. |
| `certificate is valid for X, not Y` | Use `-tls-server-name Y`, or connect using the name in the certificate. |
| `EOF` ... `the server may require TLS` | You connected without TLS to a TLS port. |
| `context deadline exceeded` ... `does not speak TLS` | You used TLS against a plain port. |

## ACL users

Run plgv as a user that can do what the benchmark needs and nothing more. Never run it as an administrator, and
don't allow `FLUSHALL`, `DEBUG`, `CONFIG` or `MONITOR` for it unless you really mean to.

Here's a rule that ran every preset, server metrics and key-space analysis in our test environment. It's the
`bench` user from `testdata/acl/users.acl`:

```
user bench on >CHANGE-ME ~plgv* -@all +@read +@write +@keyspace +@string +@hash +@list +@set +@sortedset +@stream
  +@geo +@hyperloglog +@bitmap +@pubsub +@transaction +@connection +eval +evalsha +eval_ro +evalsha_ro +fcall +fcall_ro
  +script|load +function|load +function|list +info +slowlog|get +latency|latest +memory|usage +dbsize +scan +type
  -flushall -flushdb
```

In the ACL file, that's all one line. Beyond the commands of the operations in your mix, each feature needs this:

| Feature | Commands |
|---|---|
| Any run | `+@connection` (`HELLO`, `CLIENT SETINFO`, `PING`) and `+info` to detect the server. |
| Server metrics | `+info +slowlog\|get +latency\|latest`, and for a cluster also `+cluster\|shards`. |
| Key-space analysis | `+scan +type +memory\|usage +pttl` and a length command per type (`+strlen +hlen +llen +scard +zcard +xlen`). |
| Record or replay with the starting-data check | `+dbsize +scan +type` and the length commands. The check scans **every** key, so give it read access to all keys with `%R~*`, next to your `~plgv*` write pattern. |
| Reads-only replay | `+@read` only. That's the `reader` user in `testdata/acl/users.acl`. |
| Scripts and functions | `+eval +evalsha +fcall` (and `+script\|load`, `+function\|load` to install them). |
| `import-traffic -capture` | `+monitor`. It's a dangerous command, so use an admin user for the capture, not the benchmark user. |
| `import-traffic -slowlog-from` | `+slowlog\|get`. |

If the user isn't allowed to run a command, that isn't fatal. plgv counts it as an error of class `NOPERM`,
shows the first message, and the run continues. Server metrics and the key-space analysis report what they
couldn't read. Restrict keys with `~patterns` that match `dataset.key_prefix` (default `plgv:`).

Credentials come from `PLGV_USERNAME` and `PLGV_PASSWORD`. If you use the default user of a server with
`requirepass`, you only need `PLGV_PASSWORD`.

## Secrets

Credentials come **only from the environment or a mounted file**. They never come from a YAML file, a flag or a
URI (`valkey://user:pass@host` is rejected).

| Variable | Meaning |
|---|---|
| `PLGV_USERNAME`, `PLGV_PASSWORD` | The credentials. |
| `PLGV_USERNAME_FILE`, `PLGV_PASSWORD_FILE` | Read the value from a file, such as a Kubernetes Secret or Docker secret mount. A file wins over the variable, plgv removes one trailing newline, and an unreadable file stops the run. |

The password is never written to the configuration, run summary, JSON export, HTML report, recording, replay
summary, log or error message. Our tests check this against a real ACL server. A wrong password is reported as
`WRONGPASS` without the password in the text, and none of the outputs of a successful run contains it. The web
UI has no password field. It uses the environment of the `plgv web` process.

## What plgv writes to disk

| File | Holds | Protection |
|---|---|---|
| Run summaries, saved settings (`plgv-data/`) | Results, configuration without credentials | Directory is `0700`. |
| Recordings (`-record`, `import-traffic -out`) | **Your keys and values in clear text** | File is `0600`. It never contains the target address or credentials, and plgv refuses to overwrite a file that isn't a recording. |
| Replay summaries | Totals and percentiles; no commands, keys or values | `0600`. |
| MONITOR captures | Every command, including `AUTH` | Captured in memory only. `AUTH`, `HELLO`, `MIGRATE` and `ACL` commands never reach a recording. |

Treat a recording like a database dump, and don't put one in a bug report. If you want to share a result, share a
replay or run summary instead.

## The web UI

`plgv web` has no login. It's meant for one person on one machine, so it does the following:

- It listens on `127.0.0.1` by default, and refuses another address unless you pass `-allow-remote`.
- It only answers requests addressed to `localhost`, `127.0.0.1` or `[::1]`. This stops DNS rebinding, where a
  web page on another domain resolves to your loopback. With `-allow-remote`, use `-allowed-host name1,name2` to
  say which host names it answers to.
- It refuses cross-site writes (an `Origin` that differs from the `Host`, or a body that isn't JSON).
- It sends a Content-Security-Policy that only allows its own scripts and connections and forbids framing. It
  serves reports with a policy that runs no script at all.
- It only lets a request name a file on the machine while it listens on loopback. That covers a typed recording
  path, or a capture to import. With `-allow-remote`, typed paths are off, and you choose recordings from a list
  or upload them.
  - A recording is written to a new name inside `<data-dir>/recordings` unless you type a path. A typed path may
    only create a file or replace an earlier plgv recording, and never overwrites another file.
  - A file that is read must be a regular file that starts like a plgv recording, and its content is never
    echoed.
  - Uploads are checked, size-limited and stored under a name the server chooses (see
    [record-and-replay-web-ui.md](record-and-replay-web-ui.md#where-files-live-and-typed-paths)).
- It only reads the TLS files you named, from the machine it runs on.
- It validates run ids and setting names, so no request can reach a file outside its data directory.

If you expose it (in a container or on a shared host), put it behind a reverse proxy that authenticates users,
and read `deployment.md`.

## Dependencies and review

`govulncheck ./...` reports no vulnerability that plgv's code reaches, and CI runs it on every push. The full
dependency list with licenses is in `THIRD_PARTY.md`.

We reviewed the attack surface (web server, file handling, credentials, outputs, container), and tests keep each
finding fixed (see `internal/webui/security_test.go` and `internal/config/tls_test.go`). To report a
vulnerability, see `SECURITY.md`.
