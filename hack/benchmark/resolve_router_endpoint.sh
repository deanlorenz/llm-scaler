#!/usr/bin/env bash
# Resolve the router/EPP endpoint's in-cluster DNS URL for a namespace, for
# config that will be read from INSIDE the cluster (e.g. run_only.sh's
# endpoint.base_url, resolved by the harness pod itself) -- as opposed to
# wait_serving.sh's ClusterIP probe, which tests reachability from a
# throwaway pod and has no reason to print a stable name.
#
# Reuses wait_serving.sh's own service-name/port detection (same
# router-epp|inference-gateway|epp$ match, same HTTP-port-by-name lookup) so
# this and that script cannot silently drift into detecting different
# services -- see docs/plans/benchmark/run-only-metrics-gap.md.
#
# Usage: resolve_router_endpoint.sh <namespace>
# Prints: http://<svc>.<namespace>.svc.cluster.local:<port>
set -u

NS="${1:?usage: resolve_router_endpoint.sh <namespace>}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

svc="${BENCHMARK_ENDPOINT_SVC:-}"
if [ -z "$svc" ]; then
    svc=$($KUBECTL get svc -n "$NS" -o name 2>/dev/null \
          | grep -E "router-epp|inference-gateway|epp$" | head -1 | cut -d/ -f2)
fi
if [ -z "$svc" ]; then
    echo "resolve_router_endpoint: no router/EPP service found in $NS" >&2
    exit 1
fi

port=$($KUBECTL get svc "$svc" -n "$NS" -o json 2>/dev/null | python3 -c '
import json, sys
ports = json.load(sys.stdin)["spec"].get("ports", [])
for want in ("http",):
    for p in ports:
        if p.get("name") == want:
            print(p["port"]); sys.exit(0)
for p in ports:
    if p.get("port") in (80, 8000, 8080):
        print(p["port"]); sys.exit(0)
if ports:
    print(ports[0]["port"])
')
if [ -z "$port" ]; then
    echo "resolve_router_endpoint: could not resolve a port for svc/$svc in $NS" >&2
    exit 1
fi

echo "http://${svc}.${NS}.svc.cluster.local:${port}"
