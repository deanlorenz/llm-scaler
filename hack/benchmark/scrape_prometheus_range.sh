#!/usr/bin/env bash
# scrape_prometheus_range.sh — post-scenario Prometheus/Thanos range query.
#
# Belt-and-suspenders complement to in-pod metrics scraping: pulls all
# wva_*, vllm_*, and llm_d_* metrics for the given time window immediately
# after a scenario completes. Runs even if the harness pod crashed mid-run
# (results from in-pod scraping may be incomplete; this is always complete).
#
# Usage:
#   scrape_prometheus_range.sh <start_epoch> <end_epoch> <namespace> \
#       [prometheus_url] [output_file]
#
# Arguments:
#   start_epoch     Unix timestamp (seconds) of scenario start
#   end_epoch       Unix timestamp (seconds) of scenario end
#   namespace       Kubernetes namespace label filter
#   prometheus_url  Prometheus/Thanos URL (optional; falls back to
#                   BENCHMARK_PROMETHEUS_URL, then auto-detection)
#   output_file     Output path (default: ./prometheus_range.json)
#
# Behaviour:
#   - Warns and writes an empty JSON object if Prometheus is unreachable.
#   - Never exits non-zero (degrades gracefully; run must not fail here).
#   - Queries step = max(15s, range / 120) to keep result size bounded.
#
# Output shape (for benchmark-extract):
#   {
#     "start_epoch": <int>,
#     "end_epoch": <int>,
#     "namespace": "<str>",
#     "prometheus_url": "<str>",
#     "step": "<str>",
#     "metrics": {
#       "<metric_name>": { "result": [...] }   ← raw Prometheus range_query result
#     }
#   }
set -uo pipefail
# Note: no -e; this script must never exit non-zero (graceful degradation).

START_EPOCH="${1:?usage: $0 <start_epoch> <end_epoch> <namespace> [prometheus_url] [output_file]}"
END_EPOCH="${2:?end_epoch required}"
NS="${3:?namespace required}"
PROM_URL="${4:-${BENCHMARK_PROMETHEUS_URL:-}}"
OUT_FILE="${5:-./prometheus_range.json}"

KUBECTL="${KUBECTL_CMD:-kubectl}"

_info() { echo "scrape_prometheus_range: $*"; }
_warn() { echo "scrape_prometheus_range: WARNING: $*" >&2; }

# ---------------------------------------------------------------------------
# Auto-detect Prometheus URL if not provided
# ---------------------------------------------------------------------------
_detect_prometheus_url() {
    local ns="$1"

    # 1. OpenShift Thanos querier (most common in this repo's target env).
    local thanos_svc
    thanos_svc=$($KUBECTL get svc -n openshift-monitoring \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
        tr ' ' '\n' | grep 'thanos-querier' | head -1 || true)
    if [ -n "$thanos_svc" ]; then
        echo "https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"
        return
    fi

    # 2. Prometheus service in the same namespace.
    local prom_svc
    prom_svc=$($KUBECTL get svc -n "$ns" \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
        tr ' ' '\n' | grep -i 'prometheus' | head -1 || true)
    if [ -n "$prom_svc" ]; then
        echo "http://${prom_svc}.${ns}.svc.cluster.local:9090"
        return
    fi

    # 3. kube-prometheus-stack in monitoring namespace.
    local kps_svc
    kps_svc=$($KUBECTL get svc -n monitoring \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
        tr ' ' '\n' | grep -i 'prometheus' | head -1 || true)
    if [ -n "$kps_svc" ]; then
        echo "http://${kps_svc}.monitoring.svc.cluster.local:9090"
        return
    fi

    echo ""
}

if [ -z "$PROM_URL" ]; then
    _info "BENCHMARK_PROMETHEUS_URL not set; attempting auto-detection..."
    PROM_URL=$(_detect_prometheus_url "$NS")
    if [ -n "$PROM_URL" ]; then
        _info "Auto-detected Prometheus URL: $PROM_URL"
    else
        _warn "Could not detect Prometheus URL; writing empty result to $OUT_FILE."
        mkdir -p "$(dirname "$OUT_FILE")"
        printf '{"start_epoch":%s,"end_epoch":%s,"namespace":"%s","prometheus_url":"","step":"","metrics":{}}' \
            "$START_EPOCH" "$END_EPOCH" "$NS" > "$OUT_FILE"
        exit 0
    fi
fi

# ---------------------------------------------------------------------------
# Compute step: at least 15s, but keep total samples ≤ 120 per series.
# ---------------------------------------------------------------------------
_RANGE=$(( END_EPOCH - START_EPOCH ))
if [ "$_RANGE" -le 0 ]; then
    _RANGE=60
fi
_STEP=$(( _RANGE / 120 ))
[ "$_STEP" -lt 15 ] && _STEP=15
STEP="${_STEP}s"

# ---------------------------------------------------------------------------
# Auth token (for OpenShift Thanos bearer token auth)
# ---------------------------------------------------------------------------
AUTH_TOKEN=""
if [ -f "/var/run/secrets/kubernetes.io/serviceaccount/token" ]; then
    AUTH_TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token 2>/dev/null || true)
fi
if [ -z "$AUTH_TOKEN" ]; then
    AUTH_TOKEN=$($KUBECTL config view --raw --minify \
        -o jsonpath='{.users[0].user.token}' 2>/dev/null || true)
fi
if [ -z "$AUTH_TOKEN" ]; then
    AUTH_TOKEN=$($KUBECTL whoami -t 2>/dev/null || true)
fi

_curl_args=(-sSf --connect-timeout 10 --max-time 120)
if [ -n "$AUTH_TOKEN" ]; then
    _curl_args+=(-H "Authorization: Bearer $AUTH_TOKEN")
fi
# Accept self-signed certs on internal cluster endpoints.
_curl_args+=(-k)

# ---------------------------------------------------------------------------
# Define metric selectors
# Extensible: add rows here; benchmark-extract reads the "metrics" key.
# ---------------------------------------------------------------------------
METRIC_QUERIES=(
    "wva_desired_replicas{namespace=\"${NS}\"}"
    "wva_current_replicas{namespace=\"${NS}\"}"
    "wva_saturation_utilization{namespace=\"${NS}\"}"
    "wva_kv_cache_tokens_used{namespace=\"${NS}\"}"
    "wva_kv_cache_tokens_capacity{namespace=\"${NS}\"}"
    "wva_spare_capacity{namespace=\"${NS}\"}"
    "wva_required_capacity{namespace=\"${NS}\"}"
    "wva_errors_total{namespace=\"${NS}\"}"
    "vllm:num_requests_running{namespace=\"${NS}\"}"
    "vllm:gpu_cache_usage_perc{namespace=\"${NS}\"}"
    "vllm:prompt_tokens_total{namespace=\"${NS}\"}"
    "vllm:generation_tokens_total{namespace=\"${NS}\"}"
    "vllm:request_success_total{namespace=\"${NS}\"}"
)

# ---------------------------------------------------------------------------
# Query each metric and build the JSON output
# ---------------------------------------------------------------------------
mkdir -p "$(dirname "$OUT_FILE")"

_info "Querying ${#METRIC_QUERIES[@]} metric selectors from $PROM_URL"
_info "Time window: $START_EPOCH → $END_EPOCH (step=$STEP)"

# Write to a temp file first so an interrupted run leaves no partial JSON.
_TMP_OUT=$(mktemp)
trap 'rm -f "$_TMP_OUT"' EXIT

printf '{' > "$_TMP_OUT"
printf '"start_epoch":%s,' "$START_EPOCH" >> "$_TMP_OUT"
printf '"end_epoch":%s,' "$END_EPOCH" >> "$_TMP_OUT"
printf '"namespace":"%s",' "$NS" >> "$_TMP_OUT"
printf '"prometheus_url":"%s",' "$PROM_URL" >> "$_TMP_OUT"
printf '"step":"%s",' "$STEP" >> "$_TMP_OUT"
printf '"metrics":{' >> "$_TMP_OUT"

_FIRST=true
_ERRORS=0

for query in "${METRIC_QUERIES[@]}"; do
    # Derive a stable key from the metric selector (strip label matchers).
    _KEY=$(echo "$query" | sed 's/{[^}]*}//')

    RANGE_URL="${PROM_URL}/api/v1/query_range"
    _RESULT=$(curl "${_curl_args[@]}" \
        --data-urlencode "query=${query}" \
        --data-urlencode "start=${START_EPOCH}" \
        --data-urlencode "end=${END_EPOCH}" \
        --data-urlencode "step=${STEP}" \
        "$RANGE_URL" 2>/dev/null || true)

    if [ -z "$_RESULT" ]; then
        _warn "Empty response for query: $query"
        _RESULT='{"status":"error","data":{"resultType":"matrix","result":[]}}'
        _ERRORS=$(( _ERRORS + 1 ))
    fi

    if [ "$_FIRST" = "true" ]; then
        _FIRST=false
    else
        printf ',' >> "$_TMP_OUT"
    fi
    printf '"%s":%s' "$_KEY" "$_RESULT" >> "$_TMP_OUT"
done

printf '}}' >> "$_TMP_OUT"
mv "$_TMP_OUT" "$OUT_FILE"
_info "Wrote $OUT_FILE ($_ERRORS query error(s))"
exit 0
