#!/usr/bin/env bash
# run_scenario.sh — drive a single benchmark scenario inside an already-running
# harness pod managed by run_session.sh.
#
# Usage:
#   run_scenario.sh <scenario.yaml.in> <namespace> <session-dir> [options]
#
# Required:
#   <scenario.yaml.in>  Path to a test/benchmark/scenarios/<name>.yaml.in file
#   <namespace>         Kubernetes namespace where the harness pod runs
#   <session-dir>       Local directory for this session's results (created if absent)
#
# Options (also accept environment variable equivalents):
#   --harness=NAME      guidellm|inference-perf (default: guidellm)
#   --model-id=VALUE    Model ID substituted into profile (default: $MODEL_ID)
#   --endpoint-url=URL  Inference endpoint URL (default: auto-detect via wait_serving.sh)
#   --request-rate=N    Requests per second substituted into profile (default: 10)
#   --max-duration=N    Max scenario duration in seconds (default: 600)
#   --workload=NAME     Workload name (default: basename of scenario without .yaml.in)
#
# Environment:
#   BENCH_HARNESS_POD_NAME   Pod name (default: llmdbench-harness)
#   BENCH_IMAGE_TAG          Image tag (default: v0.7.8)
#   KUBECTL_CMD              kubectl binary (default: kubectl)
#   BENCH_SKIP_WVA_SCRAPE    Set to "true" to skip client-side WVA metrics scraping
#   BENCH_SKIP_PROMETHEUS    Set to "true" to skip post-scenario Prometheus range query
#   BENCHMARK_PROMETHEUS_URL Prometheus/Thanos URL for range query
#   WVA_METRICS_SERVICE      WVA controller metrics service name (default: auto)
#
# Outputs written to <session-dir>/<workload_name>/:
#   results/           kubectl cp'd from pod's /requests/<harness>_<exp_id>_<stack>/
#   wva-metrics/       WVA controller metrics from scrape_wva_metrics.sh (client-side)
#   prometheus_range.json  Post-scenario Prometheus range query
#   scenario_meta.json     Start/end epoch timestamps, parameters
#
# The harness pod is NOT torn down by this script. Call run_session.sh stop to
# tear it down explicitly when the session is complete.
set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --help
case "${1:-}" in
    -h|--help)
        sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
        exit 0
        ;;
esac

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
SCENARIO_FILE="${1:?usage: $0 <scenario.yaml.in> <namespace> <session-dir> [opts]}"
NS="${2:?namespace required}"
SESSION_DIR="${3:?session-dir required}"
shift 3

HARNESS="${BENCH_HARNESS:-guidellm}"
MODEL_ID="${MODEL_ID:-}"
ENDPOINT_URL="${BENCH_ENDPOINT_URL:-}"
REQUEST_RATE="${BENCH_REQUEST_RATE:-10}"
MAX_DURATION="${BENCH_MAX_DURATION:-600}"
WORKLOAD="${BENCH_WORKLOAD:-}"

for arg in "$@"; do
    case "$arg" in
        --harness=*)    HARNESS="${arg#*=}" ;;
        --model-id=*)   MODEL_ID="${arg#*=}" ;;
        --endpoint-url=*) ENDPOINT_URL="${arg#*=}" ;;
        --request-rate=*) REQUEST_RATE="${arg#*=}" ;;
        --max-duration=*) MAX_DURATION="${arg#*=}" ;;
        --workload=*)   WORKLOAD="${arg#*=}" ;;
        *) echo "run_scenario: unknown option: $arg" >&2; exit 1 ;;
    esac
done

# Default workload name from the scenario file basename.
if [ -z "$WORKLOAD" ]; then
    WORKLOAD="$(basename "$SCENARIO_FILE" .yaml.in)"
fi

KUBECTL="${KUBECTL_CMD:-kubectl}"
POD="${BENCH_HARNESS_POD_NAME:-llmdbench-harness}"

_info()  { echo "run_scenario[$WORKLOAD]: $*"; }
_warn()  { echo "run_scenario[$WORKLOAD]: WARNING: $*" >&2; }
_error() { echo "run_scenario[$WORKLOAD]: ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Validate inputs
# ---------------------------------------------------------------------------
[ -f "$SCENARIO_FILE" ] || _error "scenario file not found: $SCENARIO_FILE"
[ -n "$MODEL_ID" ] || _error "MODEL_ID is required (--model-id= or MODEL_ID env)"

# Confirm pod is ready before starting work.
_pod_ready() {
    local ready
    ready=$($KUBECTL get pod "$POD" -n "$NS" \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    [ "$ready" = "True" ]
}
_pod_ready || _error "Pod $POD is not Ready in $NS — run 'run_session.sh ensure $NS' first."

# ---------------------------------------------------------------------------
# Endpoint URL (auto-detect if not provided)
# ---------------------------------------------------------------------------
if [ -z "$ENDPOINT_URL" ]; then
    _info "Auto-detecting endpoint URL via wait_serving.sh..."
    ENDPOINT_URL=$(bash "$_SCRIPT_DIR/wait_serving.sh" "$NS" 2>&1 | grep -oE 'http[s]?://[^ ]+' | head -1 || true)
    [ -n "$ENDPOINT_URL" ] || _error "Could not detect endpoint URL; set BENCH_ENDPOINT_URL."
    _info "Detected endpoint: $ENDPOINT_URL"
fi

# ---------------------------------------------------------------------------
# Session directory setup
# ---------------------------------------------------------------------------
SCENARIO_OUT_DIR="$SESSION_DIR/$WORKLOAD"
mkdir -p "$SCENARIO_OUT_DIR"

# Derive a stack name for results path matching (same convention as run_only.sh).
_STACK_NAME="bench-$(echo "$MODEL_ID" | tr '/[:upper:]' '-[:lower:]' | \
    tr -c 'a-z0-9-' '-' | sed -e 's/^-*//' -e 's/-*$//')"

# ---------------------------------------------------------------------------
# Token substitution: render .yaml.in → resolved profile
# ---------------------------------------------------------------------------
_TMP_PROFILE=$(mktemp)
trap 'rm -f "${_TMP_PROFILE}"' EXIT

_info "Rendering scenario profile: $SCENARIO_FILE"
sed \
    -e "s/__REQUEST_RATE__/${REQUEST_RATE}/g" \
    -e "s/__MAX_DURATION__/${MAX_DURATION}/g" \
    -e "s|REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL|${MODEL_ID}|g" \
    -e "s|REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL|${ENDPOINT_URL}|g" \
    "$SCENARIO_FILE" > "$_TMP_PROFILE"

if grep -qE '__REQUEST_RATE__|__MAX_DURATION__|REPLACE_ENV_LLMDBENCH_' "$_TMP_PROFILE"; then
    _error "Unresolved token(s) remain after substitution:
$(grep -nE '__REQUEST_RATE__|__MAX_DURATION__|REPLACE_ENV_LLMDBENCH_' "$_TMP_PROFILE")"
fi

# ---------------------------------------------------------------------------
# Upload rendered profile into pod
# ---------------------------------------------------------------------------
PROFILE_BASENAME="${WORKLOAD}.yaml"
IN_POD_PROFILE_DIR="/workspace/profiles/${HARNESS}"
IN_POD_PROFILE="${IN_POD_PROFILE_DIR}/${PROFILE_BASENAME}"

_info "Uploading rendered profile to pod: ${IN_POD_PROFILE}"
$KUBECTL exec "$POD" -n "$NS" -- mkdir -p "$IN_POD_PROFILE_DIR"
$KUBECTL cp "$_TMP_PROFILE" "${NS}/${POD}:${IN_POD_PROFILE}"

# ---------------------------------------------------------------------------
# Compute a unique experiment ID (epoch-based, same shape as run_only.sh)
# ---------------------------------------------------------------------------
_UID=$(date +%s)
EXPERIMENT_ID="${_UID}_${WORKLOAD}"

# ---------------------------------------------------------------------------
# Prepare logs/ directory
# ---------------------------------------------------------------------------
mkdir -p "$SCENARIO_OUT_DIR/logs"

# ---------------------------------------------------------------------------
# Start WVA controller log capture
# ---------------------------------------------------------------------------
_WVA_CTRL_LOG="$SCENARIO_OUT_DIR/logs/wva-controller.log"
_WVA_CTRL_DEPLOY="${WVA_CONTROLLER_DEPLOYMENT:-wva-controller-manager}"
_wva_ctrl_log_started=false
if [ "${BENCH_SKIP_WVA_CTRL_LOG:-false}" != "true" ]; then
    if bash "$_SCRIPT_DIR/capture_wva_controller_log.sh" start "$NS" "$_WVA_CTRL_LOG" "$_WVA_CTRL_DEPLOY"; then
        _wva_ctrl_log_started=true
    else
        _warn "WVA controller log capture failed to start; continuing without it."
    fi
fi

# ---------------------------------------------------------------------------
# Start replica sampler (client-side fix for upstream harness label-match bug)
# ---------------------------------------------------------------------------
_REPLICA_TS="$SCENARIO_OUT_DIR/metrics/processed/replica_status_timeseries.json"
mkdir -p "$(dirname "$_REPLICA_TS")"
_replica_sampler_started=false
if [ "${BENCH_SKIP_REPLICA_SAMPLE:-false}" != "true" ]; then
    if bash "$_SCRIPT_DIR/sample_replicas.sh" start "$NS" "$_REPLICA_TS"; then
        _replica_sampler_started=true
    else
        _warn "Replica sampler failed to start; replica_status_timeseries.json will be empty."
    fi
fi

# ---------------------------------------------------------------------------
# Start client-side WVA metrics scraper
# ---------------------------------------------------------------------------
WVA_OUT_DIR="$SCENARIO_OUT_DIR/wva-metrics"
mkdir -p "$WVA_OUT_DIR"

_wva_scrape_started=false
if [ "${BENCH_SKIP_WVA_SCRAPE:-false}" != "true" ]; then
    # Auto-detect WVA metrics service if not specified.
    _WVA_SVC="${WVA_METRICS_SERVICE:-}"
    if [ -z "$_WVA_SVC" ]; then
        _WVA_SVC=$($KUBECTL get svc -n "$NS" \
            -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
            tr ' ' '\n' | grep -i 'wva.*metrics\|metrics.*wva' | head -1 || true)
    fi
    if [ -n "$_WVA_SVC" ]; then
        _info "Starting WVA metrics scraper (svc/$_WVA_SVC)..."
        if WVA_METRICS_SERVICE="$_WVA_SVC" \
            bash "$_SCRIPT_DIR/scrape_wva_metrics.sh" start "$NS" "$WVA_OUT_DIR" "$_WVA_SVC"; then
            _wva_scrape_started=true
        else
            _warn "WVA metrics scraper failed to start; continuing without it."
        fi
    else
        _warn "No WVA metrics service found in $NS; skipping WVA scrape."
    fi
fi

# ---------------------------------------------------------------------------
# Clear previous scenario state inside pod
# ---------------------------------------------------------------------------
_info "Clearing previous scenario state in pod..."
$KUBECTL exec "$POD" -n "$NS" -- bash -c "rm -rf /requests 2>/dev/null; mkdir -p /requests"

# ---------------------------------------------------------------------------
# Record scenario start
# ---------------------------------------------------------------------------
SCENARIO_START_EPOCH=$(date +%s)
_info "Starting scenario at epoch $SCENARIO_START_EPOCH"

# ---------------------------------------------------------------------------
# Run the harness inside the pod
# ---------------------------------------------------------------------------
_info "Executing harness: $HARNESS, workload: $WORKLOAD"

# Start a progress ticker: prints elapsed time every 30s while the harness runs.
_ticker_start=$(date +%s)
(
    while true; do
        sleep 30
        _elapsed=$(( $(date +%s) - _ticker_start ))
        _pod_phase=$($KUBECTL get pod "$POD" -n "$NS" \
            -o jsonpath='{.status.phase}' 2>/dev/null || echo "?")
        echo "run_scenario[$WORKLOAD]: still running... ${_elapsed}s elapsed (pod=${_pod_phase})"
    done
) &
_ticker_pid=$!

$KUBECTL exec "$POD" -n "$NS" -- bash -c "
  export LLMDBENCH_HARNESS_EXPERIMENT_ID='${EXPERIMENT_ID}'
  export LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX=/requests
  export LLMDBENCH_VLLM_COMMON_NAMESPACE='${NS}'
  export LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED=true
  export LLMDBENCH_HARNESS_STACK_NAME='${_STACK_NAME}'
  export LLMDBENCH_HARNESS_STACK_ENDPOINT_URL='${ENDPOINT_URL}'
  export LLMDBENCH_DEPLOY_CURRENT_MODEL='${MODEL_ID}'
  exec llm-d-benchmark.sh --harness='${HARNESS}' --workload='${WORKLOAD}.yaml'
"
HARNESS_RC=$?

# Stop the ticker
kill "$_ticker_pid" 2>/dev/null || true
wait "$_ticker_pid" 2>/dev/null || true

# ---------------------------------------------------------------------------
# Record scenario end
# ---------------------------------------------------------------------------
SCENARIO_END_EPOCH=$(date +%s)
_info "Harness exited (rc=$HARNESS_RC) at epoch $SCENARIO_END_EPOCH"

# ---------------------------------------------------------------------------
# Stop WVA controller log capture
# ---------------------------------------------------------------------------
if [ "$_wva_ctrl_log_started" = "true" ]; then
    bash "$_SCRIPT_DIR/capture_wva_controller_log.sh" stop "$_WVA_CTRL_LOG" || true
fi

# ---------------------------------------------------------------------------
# Stop replica sampler
# ---------------------------------------------------------------------------
if [ "$_replica_sampler_started" = "true" ]; then
    bash "$_SCRIPT_DIR/sample_replicas.sh" stop "$_REPLICA_TS" || true
fi

# ---------------------------------------------------------------------------
# Stop WVA metrics scraper
# ---------------------------------------------------------------------------
if [ "$_wva_scrape_started" = "true" ]; then
    _info "Stopping WVA metrics scraper..."
    bash "$_SCRIPT_DIR/scrape_wva_metrics.sh" stop "$WVA_OUT_DIR" || true
fi

# ---------------------------------------------------------------------------
# Collect results from pod
# ---------------------------------------------------------------------------
RESULTS_OUT="$SCENARIO_OUT_DIR/results"
mkdir -p "$RESULTS_OUT"

_info "Collecting results from pod: /requests/"
# List everything under /requests/ and copy each entry individually.
# The harness directory name format varies by harness type and version, so we
# do not rely on a prefix guess — we copy whatever the harness actually wrote.
_pod_entries=$($KUBECTL exec "$POD" -n "$NS" -- bash -c \
    "ls /requests/ 2>/dev/null" || true)
if [ -n "$_pod_entries" ]; then
    while IFS= read -r _entry; do
        [ -z "$_entry" ] && continue
        _info "  copying /requests/$_entry → $RESULTS_OUT/"
        $KUBECTL cp "${NS}/${POD}:/requests/${_entry}" "$RESULTS_OUT/${_entry}" || \
            _warn "kubectl cp failed for /requests/$_entry; results may be incomplete."
    done <<< "$_pod_entries"
else
    _warn "No results found under /requests/ in pod $POD; listing for diagnostics:"
    $KUBECTL exec "$POD" -n "$NS" -- ls -la /requests/ 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Write scenario metadata
# ---------------------------------------------------------------------------
cat > "$SCENARIO_OUT_DIR/scenario_meta.json" <<META
{
  "workload": "${WORKLOAD}",
  "harness": "${HARNESS}",
  "namespace": "${NS}",
  "model_id": "${MODEL_ID}",
  "endpoint_url": "${ENDPOINT_URL}",
  "request_rate": ${REQUEST_RATE},
  "max_duration": ${MAX_DURATION},
  "experiment_id": "${EXPERIMENT_ID}",
  "stack_name": "${_STACK_NAME}",
  "scenario_start_epoch": ${SCENARIO_START_EPOCH},
  "scenario_end_epoch": ${SCENARIO_END_EPOCH},
  "harness_rc": ${HARNESS_RC}
}
META
_info "Wrote scenario_meta.json"

# ---------------------------------------------------------------------------
# Post-scenario Prometheus range query
# ---------------------------------------------------------------------------
if [ "${BENCH_SKIP_PROMETHEUS:-false}" != "true" ]; then
    _info "Running post-scenario Prometheus range query..."
    bash "$_SCRIPT_DIR/scrape_prometheus_range.sh" \
        "$SCENARIO_START_EPOCH" "$SCENARIO_END_EPOCH" \
        "$NS" \
        "${BENCHMARK_PROMETHEUS_URL:-}" \
        "$SCENARIO_OUT_DIR/prometheus_range.json" || \
        _warn "Prometheus range query failed or was skipped."
fi

# ---------------------------------------------------------------------------
# Post-scenario IGW access log collection
# ---------------------------------------------------------------------------
if [ "${BENCH_SKIP_IGW_LOGS:-false}" != "true" ]; then
    _info "Collecting IGW access logs for run window..."
    bash "$_SCRIPT_DIR/collect_igw_logs.sh" \
        "$NS" \
        "$SCENARIO_START_EPOCH" \
        "$SCENARIO_END_EPOCH" \
        "$SCENARIO_OUT_DIR/logs/igw_pods.log" || \
        _warn "IGW log collection failed or was skipped."
fi

# ---------------------------------------------------------------------------
# Post-scenario EPP pod log collection
# ---------------------------------------------------------------------------
if [ "${BENCH_SKIP_EPP_LOGS:-false}" != "true" ]; then
    _info "Collecting EPP pod logs for run window..."
    _EPP_LOG="$SCENARIO_OUT_DIR/logs/epp_pods.log"
    # Collect from all pods matching the epp label; since= covers the run window.
    _SINCE_S=$(( SCENARIO_END_EPOCH - SCENARIO_START_EPOCH + 30 ))
    _epp_pods=$($KUBECTL get pods -n "$NS" -l 'app=inference-gateway' \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    if [ -z "$_epp_pods" ]; then
        # Fallback: look for pods whose name contains 'epp'
        _epp_pods=$($KUBECTL get pods -n "$NS" \
            -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
            tr ' ' '\n' | grep -i 'epp' || true)
    fi
    if [ -n "$_epp_pods" ]; then
        {
            echo "# EPP pod logs collected at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
            echo "# run window: ${SCENARIO_START_EPOCH}–${SCENARIO_END_EPOCH}"
            for _p in $_epp_pods; do
                echo "# === pod: $_p ==="
                $KUBECTL logs "$_p" -n "$NS" --since="${_SINCE_S}s" 2>/dev/null || true
            done
        } > "$_EPP_LOG"
        _info "EPP pod logs written to: $_EPP_LOG"
    else
        _warn "No EPP pods found in $NS; skipping epp_pods.log."
    fi
fi

# ---------------------------------------------------------------------------
# Final status
# ---------------------------------------------------------------------------
_info "Scenario complete. Results in: $SCENARIO_OUT_DIR"
if [ $HARNESS_RC -ne 0 ]; then
    _warn "Harness exited with rc=$HARNESS_RC — results may be partial."
fi
exit $HARNESS_RC
