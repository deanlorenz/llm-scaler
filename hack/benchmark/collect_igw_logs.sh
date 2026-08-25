#!/usr/bin/env bash
# collect_igw_logs.sh — capture Istio/Envoy access logs from the inference
# gateway pod for a specific benchmark run window.
#
# Istio access logging must be enabled on the Gateway before running.
# For dhl-la-1708, apply hack/benchmark/igw-telemetry-dhl-la-1708.yaml once.
#
# The Istio/Envoy default access log format (one line per request):
#   [START_TIME] "METHOD PATH PROTOCOL" STATUS RESPONSE_FLAGS
#   BYTES_RECEIVED BYTES_SENT DURATION UPSTREAM_RESPONSE_TIME
#   "X-FORWARDED-FOR" "USER-AGENT" "X-REQUEST-ID" "AUTHORITY" UPSTREAM_HOST
#
# Example line:
#   [2026-08-21T02:51:04.123Z] "POST /v1/completions HTTP/1.1" 200 -
#   1234 5678 423 422 "-" "inference-perf/0.1" "abc-123" "optimized-baseline-epp:80"
#   "10.130.4.219:8081"
#
# Usage:
#   collect_igw_logs.sh <namespace> <start_epoch> <end_epoch> <output_file>
#
# The script collects ALL logs from the IGW pod's istio-proxy container since
# start_epoch - 60s (a small margin for clock skew), then filters client-side
# to the [start_epoch, end_epoch] window using the timestamp in each line.
# Works even if the pod has been running for days (no --since restart needed).
#
# Output: raw Envoy access log lines, one per request, written to output_file.
# Empty file if no requests were seen (e.g., Telemetry not yet enabled).
set -euo pipefail

NS="${1:?usage: $0 <namespace> <start_epoch> <end_epoch> <output_file>}"
START_EPOCH="${2:?start_epoch required}"
END_EPOCH="${3:?end_epoch required}"
OUT_FILE="${4:?output_file required}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

_info() { echo "collect_igw_logs: $*"; }
_warn() { echo "collect_igw_logs: WARNING: $*" >&2; }

# Find the IGW pod (the one managed by the Istio gateway controller).
IGW_POD=$($KUBECTL get pod -n "$NS" \
    -l "gateway.istio.io/managed=istio.io-gateway-controller" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)

if [ -z "$IGW_POD" ]; then
    _warn "No IGW pod found in $NS (label: gateway.istio.io/managed=istio.io-gateway-controller)"
    _warn "Is the inference gateway deployed? Is dhl-la-1708 the right namespace?"
    mkdir -p "$(dirname "$OUT_FILE")"
    touch "$OUT_FILE"
    exit 0
fi

_info "IGW pod: $IGW_POD"

# Verify istio-proxy container is present (access logs go there).
has_proxy=$($KUBECTL get pod "$IGW_POD" -n "$NS" \
    -o jsonpath='{.spec.containers[*].name}' 2>/dev/null | tr ' ' '\n' | \
    grep -c "^istio-proxy$" || true)
if [ "$has_proxy" -eq 0 ]; then
    _warn "Pod $IGW_POD has no istio-proxy container — Telemetry resource may not have taken effect yet."
    mkdir -p "$(dirname "$OUT_FILE")"
    touch "$OUT_FILE"
    exit 0
fi

# Collect logs since (start_epoch - 60s) to catch any slight clock skew.
SINCE_EPOCH=$(( START_EPOCH - 60 ))
SINCE_TS=$(date -u -d "@${SINCE_EPOCH}" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || \
           date -u -r "${SINCE_EPOCH}" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)

_info "Collecting logs from $IGW_POD (istio-proxy) since $SINCE_TS"
mkdir -p "$(dirname "$OUT_FILE")"

# Fetch all logs since the window start. Filter client-side to [start, end].
# Envoy access log lines begin with "[" followed by an ISO8601 timestamp.
# Lines that don't match that pattern (control-plane info/warn lines) are dropped.
$KUBECTL logs "$IGW_POD" -n "$NS" -c istio-proxy \
    --since-time="$SINCE_TS" 2>/dev/null | \
    python3 -c "
import sys, re
from datetime import datetime, timezone

start = $START_EPOCH
end   = $END_EPOCH

# Envoy access log: [\$START_TIME\$] \"METHOD PATH PROTO\" STATUS ...
ts_re = re.compile(r'^\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})\.')

kept = 0
for line in sys.stdin:
    m = ts_re.match(line)
    if not m:
        continue   # skip control-plane info/warn lines
    try:
        dt = datetime.strptime(m.group(1), '%Y-%m-%dT%H:%M:%S').replace(tzinfo=timezone.utc)
        epoch = int(dt.timestamp())
    except ValueError:
        continue
    if start <= epoch <= end:
        sys.stdout.write(line)
        kept += 1

print(f'collect_igw_logs: {kept} access log lines in window [{start}, {end}]', file=sys.stderr)
" > "$OUT_FILE" 2>&1

LINE_COUNT=$(wc -l < "$OUT_FILE" | tr -d ' ')
_info "Wrote $LINE_COUNT lines to $OUT_FILE"
if [ "$LINE_COUNT" -eq 0 ]; then
    _warn "Zero lines collected. Possible causes:"
    _warn "  1. Telemetry resource not yet applied (apply hack/benchmark/igw-telemetry-dhl-la-1708.yaml)"
    _warn "  2. No traffic in the window [$START_EPOCH, $END_EPOCH]"
    _warn "  3. istio-proxy was restarted and logs before restart are gone"
fi
