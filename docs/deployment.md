# Deployment: binaries, Docker and Kubernetes

- [Binaries](#binaries)
- [Docker](#docker)
- [Kubernetes](#kubernetes)
- [Where to run the load generator](#where-to-run-the-load-generator)

## Binaries

Release archives are built for Linux and macOS, on amd64 and arm64:

```
plgv-<version>-linux-amd64.tar.gz     plgv-<version>-darwin-amd64.tar.gz
plgv-<version>-linux-arm64.tar.gz     plgv-<version>-darwin-arm64.tar.gz
SHA256SUMS
```

Each holds the static binary (`CGO_ENABLED=0`, no runtime dependencies), `LICENSE`, `THIRD_PARTY.md` and
`README.md`. Check the download, then unpack it:

```sh
sha256sum -c --ignore-missing SHA256SUMS      # macOS: shasum -a 256 -c SHA256SUMS
tar -xzf plgv-v0.0.1-linux-amd64.tar.gz && ./plgv-v0.0.1-linux-amd64/plgv version
```

Build them yourself with `make dist` (needs Go 1.25); `make build-local` builds `bin/plgv` for this machine.
The version comes from `git describe` and is printed by `plgv version`.

## Docker

```sh
make docker-build                                   # or: docker build -t plgv --build-arg VERSION=v0.0.1 .
docker run --rm plgv run -addr valkey.internal:6379 -preset cache -duration 60s
```

The image (about 36 MB) is Alpine with the static binary and CA certificates, runs as user `10001` (not root)
and holds no secret. `ENTRYPOINT` is `plgv`, so the arguments are the command and its flags.

Credentials and certificates come from the environment and mounted files:

```sh
docker run --rm \
  -v "$PWD/secrets:/secrets:ro" -v "$PWD/tls:/tls:ro" \
  -e PLGV_USERNAME_FILE=/secrets/username -e PLGV_PASSWORD_FILE=/secrets/password \
  plgv run -addr valkeys://valkey.internal:6379 -tls-ca /tls/ca.crt -preset session -duration 5m
```

Reach a Valkey on the host from Docker Desktop with `-addr host.docker.internal:6379`.

### The web UI in a container

The UI has no login; inside a container it must listen on all interfaces, so allow that explicitly and
**publish the port on the host's loopback only**:

```sh
docker run -d --name plgv-web -p 127.0.0.1:8080:8080 -v plgv-data:/data \
  plgv web -addr 0.0.0.0:8080 -allow-remote -allowed-host localhost,127.0.0.1
```

`-allowed-host` makes the UI answer only to those host names, so a malicious page cannot drive it
through your published port (DNS rebinding); a request with another `Host` gets 403. Stored runs and saved
settings live in the `plgv-data` volume. Do not publish the port on `0.0.0.0` or put the UI on a shared
network without an authenticating reverse proxy in front: see `security-tls-acl.md`.

## Kubernetes

`deploy/k8s/` holds a namespace (Pod Security `restricted`), a ConfigMap with the workload, and a Job.

```sh
kubectl create namespace plgv
kubectl -n plgv create secret generic plgv-credentials --from-literal=username=bench --from-literal=password='...'
kubectl -n plgv create secret generic plgv-tls --from-file=ca.crt --from-file=tls.crt --from-file=tls.key   # for TLS
# edit deploy/k8s/configmap.yaml (address, workload) and the image in kustomization.yaml, then:
kubectl apply -k deploy/k8s
kubectl -n plgv logs -f job/plgv-run
```

The Job runs `plgv run -config /config/plgv.yaml` once (`backoffLimit: 0`, cleaned up after a day), as a
non-root user with a read-only root file system, no capabilities, no service account token, and CPU and
memory requests and limits. The password is a mounted Secret file read through `PLGV_PASSWORD_FILE`; it is not
in the ConfigMap, the environment or the Pod spec. The JSON summary is `/tmp/summary.json` in the Pod (an
`emptyDir`); copy it with `kubectl cp` or read the log. `deploy/k8s/job-replay.yaml` replays a recording
kept on a PersistentVolumeClaim, reads-only.

Test the manifests without a cluster: `kubectl apply --dry-run=client -k deploy/k8s`. The manifests were
validated this way; they were not applied to a live cluster in this release's checks.

## Where to run the load generator

- **Close to the server, on its own machine.** The load generator uses CPU: plgv's budget is under half
  a core per 100,000 ops/s, but a 64-worker run on a loaded laptop measures the laptop. Do not share the
  host with the server under test.
- **Same network path as your application**, so latency includes what the application sees.
- **Set CPU limits generously in containers.** A CPU-limited Pod throttles the load generator and shows up
  as server latency. `plgv` reports its own CPU per 100k ops/s at the end of a run; if it is near the
  limit, add workers on another machine rather than raising the load.
- **Several generators, one server:** run `plgv run` on each with different `-seed` and `-prefix`, and compare
  the server's own `instantaneous_ops_per_sec` with the sum.
