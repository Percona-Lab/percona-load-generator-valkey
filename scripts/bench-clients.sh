#!/bin/sh
# Compare valkey-go and go-redis against a running Valkey (make valkey-up).
# Usage: scripts/bench-clients.sh [addr] [duration]
ADDR=${1:-127.0.0.1:6379}
DUR=${2:-10s}
BIN=${PLGV_BIN:-bin/plgv}
printf '%-10s %-8s %-9s %12s %9s %9s %9s %14s\n' client workers pipeline ops/s p50 p99 errors cores/100k
for pipe in 1 16; do
  for lib in valkey-go go-redis; do
    out=$($BIN run -addr "$ADDR" -client $lib -workers 64 -pipeline $pipe -duration "$DUR" -keys 100000 -dist zipf -value-size 256 -seed 1 2>&1)
    ops=$(echo "$out" | awk '/^throughput:/{print $2}')
    p50=$(echo "$out" | awk '/^latency:/{print $5}')
    p99=$(echo "$out" | awk '/^latency:/{print $9}')
    err=$(echo "$out" | awk '/^errors:/{print $2}')
    cpu=$(echo "$out" | awk '/^client cpu:/{print $5}')
    printf '%-10s %-8s %-9s %12s %9s %9s %9s %14s\n' $lib 64 $pipe "$ops" "$p50" "$p99" "$err" "$cpu"
  done
done
