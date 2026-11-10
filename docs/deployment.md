# Deployment: binaries, Docker and Kubernetes

- [Binaries](#binaries)
- [Docker](#docker)
- [Kubernetes](#kubernetes)
- [Where to run the load generator](#where-to-run-the-load-generator)

## Binaries

We build release archives for Linux and macOS, on amd64 and arm64:

```
plgv-<version>-linux-amd64.tar.gz     plgv-<version>-darwin-amd64.tar.gz
plgv-<version>-linux-arm64.tar.gz     plgv-<version>-darwin-arm64.tar.gz
SHA256SUMS
```

Each archive holds the static binary (`CGO_ENABLED=0`, so no runtime dependencies), `LICENSE`, `THIRD_PARTY.md`
and `README.md`. Check the download first, then unpack it:

```sh
sha256sum -c --ignore-missing SHA256SUMS      # macOS: shasum -a 256 -c SHA256SUMS
tar -xzf plgv-v0.0.1-linux-amd64.tar.gz && ./plgv-v0.0.1-linux-amd64/plgv version
```

If you'd rather build them yourself, run `make dist` (you need Go 1.25). `make build-local` builds `bin/plgv` for
your own machine. The version comes from `git describe`, and `plgv version` prints it.

## Docker

```sh
make docker-build                                   # or: docker build -t plgv --build-arg VERSION=v0.0.1 .
docker run --rm plgv run -addr valkey.internal:6379 -preset cache -duration 60s
```

The image is about 36 MB. It's Alpine with the static binary and CA certificates, it runs as user `10001` (not
root), and it contains no secrets. `ENTRYPOINT` is `plgv`, so whatever you pass is the command and its flags.

Credentials and certificates come from the environment and from mounted files:

```sh
docker run --rm \
  -v "$PWD/secrets:/secrets:ro" -v "$PWD/tls:/tls:ro" \
  -e PLGV_USERNAME_FILE=/secrets/username -e PLGV_PASSWORD_FILE=/secrets/password \
  plgv run -addr valkeys://valkey.internal:6379 -tls-ca /tls/ca.crt -preset session -duration 5m
```

If your Valkey runs on the host and you're using Docker Desktop, connect with `-addr host.docker.internal:6379`.

### The web UI in a container

The UI has no login. Inside a container it has to listen on all interfaces, so you need to allow that explicitly.
When you do, **publish the port on the host's loopback only**:

```sh
docker run -d --name plgv-web -p 127.0.0.1:8080:8080 -v plgv-data:/data \
  plgv web -addr 0.0.0.0:8080 -allow-remote -allowed-host localhost,127.0.0.1
```

`-allowed-host` makes the UI answer only to those host names. That stops a malicious web page from driving it
through your published port (DNS rebinding), and a request with any other `Host` gets a 403. Stored runs and
saved settings live in the `plgv-data` volume.

Don't publish the port on `0.0.0.0`, and don't put the UI on a shared network unless an authenticating reverse
proxy sits in front of it. See `security-tls-acl.md`.

## Kubernetes

`deploy/k8s/` has a namespace (Pod Security `restricted`), a ConfigMap with the workload, and a Job.

```sh
kubectl create namespace plgv
kubectl -n plgv create secret generic plgv-credentials --from-literal=username=bench --from-literal=password='...'
kubectl -n plgv create secret generic plgv-tls --from-file=ca.crt --from-file=tls.crt --from-file=tls.key   # for TLS
# edit deploy/k8s/configmap.yaml (address, workload) and the image in kustomization.yaml, then:
kubectl apply -k deploy/k8s
kubectl -n plgv logs -f job/plgv-run
```

The Job runs `plgv run -config /config/plgv.yaml` once (`backoffLimit: 0`, and it's cleaned up after a day). It
runs as a non-root user with a read-only root file system, no capabilities, no service account token, and CPU
and memory requests and limits.

The password is a mounted Secret file that plgv reads through `PLGV_PASSWORD_FILE`. It never appears in the
ConfigMap, the environment or the Pod spec. The JSON summary is written to `/tmp/summary.json` in the Pod (an
`emptyDir`), so copy it out with `kubectl cp` or read the log instead.

`deploy/k8s/job-replay.yaml` replays a recording kept on a PersistentVolumeClaim, reads only.

You can test the manifests without a cluster with `kubectl apply --dry-run=client -k deploy/k8s`. That's how we
validated them. We haven't applied them to a live cluster yet.

## Where to run the load generator

- **Run it close to the server, but on its own machine.** The load generator uses CPU. plgv aims to stay under
  half a core per 100,000 ops/s, but a 64-worker run on a busy laptop ends up measuring the laptop. Don't share
  a host with the server you're testing.
- **Use the same network path as your application,** so the latency you see includes what the application sees.
- **Be generous with CPU limits in containers.** A CPU-limited Pod throttles the load generator, and that shows
  up as server latency. plgv reports its own CPU per 100k ops/s at the end of a run. If that's near the limit,
  add workers on another machine instead of pushing harder.
- **If you use several generators against one server,** run `plgv run` on each with a different `-seed` and
  `-prefix`, then compare the server's own `instantaneous_ops_per_sec` with the sum of what they report.
