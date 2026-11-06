# Valkey test environment

Run everything from the project folder:

```bash
cd /Users/daniel/gitRepos/percona-load-generator-valkey
```

Docker Desktop must be running.

## Standalone (127.0.0.1:6379)

```bash
make valkey-up      # start
make valkey-down    # stop and destroy (data is wiped)
```

Recreate from scratch:

```bash
make valkey-down && make valkey-up
```

## Cluster (127.0.0.1:7001-7006, 3 primaries + 3 replicas)

```bash
make valkey-cluster-up      # start and form the cluster
make valkey-cluster-down    # stop and destroy
```

Recreate from scratch:

```bash
make valkey-cluster-down && make valkey-cluster-up
```

## Sentinel (primary 6380, replica 6381, sentinels 26379-26381)

```bash
make valkey-sentinel-up       # master set "mymaster"
make valkey-sentinel-down     # stop and destroy
```

Check it: `docker exec plgv-sentinel-primary valkey-cli -p 26379 sentinel get-master-addr-by-name mymaster`.
To try a failover: `docker exec plgv-sentinel-primary valkey-cli -p 26379 sentinel failover mymaster`
(run it again to put 6380 back as the master).

## TLS and ACL (127.0.0.1:6444)

```bash
make valkey-tls-up      # makes throwaway certificates in .test-certs/ and starts a server with mutual TLS (no plain port)
make valkey-tls-down
make test-integration-tls
```

Users (`testdata/acl/users.acl`): `bench` / `benchpass` (a workload, server metrics, key-space analysis; nothing
dangerous), `reader` / `readerpass` (reads only), `admin` / `adminpass` (everything), and the default user is off.
Connect with `-tls-ca .test-certs/ca.crt -tls-cert .test-certs/client.crt -tls-key .test-certs/client.key`.
Port 6444 because Docker Desktop's Kubernetes uses 6443 on some machines.

## Pause without destroying

Stops the containers but keeps them, so you can start them again:

```bash
docker compose stop      # standalone
docker compose --profile cluster stop      # cluster
docker compose start     # standalone again
docker compose --profile cluster start     # cluster again
```

After a cluster restart, wait a few seconds for it to become healthy.

## Check it works

```bash
make valkey-cli                                   # opens valkey-cli on standalone (type exit to leave)
docker exec plgv-valkey valkey-cli ping           # expect PONG
docker exec plgv-valkey-c1 valkey-cli -p 7001 cluster info | head -2   # expect cluster_state:ok
```

## Logs

```bash
make valkey-logs
```

## Use a different Valkey version

Default is `valkey/valkey:9`. To change it:

```bash
VALKEY_VERSION=8 make valkey-down valkey-up
```

## Wipe everything, including images

```bash
make valkey-down valkey-cluster-down valkey-sentinel-down
docker image rm valkey/valkey:9
```
