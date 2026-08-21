#!/usr/bin/env bash
# In-pod vLLM/EPP Prometheus metrics collector for the run_only.sh path.
#
# Trimmed fork of llm-d-benchmark's workload/harnesses/collect_metrics.sh
# (v0.7.8), Collector A only: direct pod-IP curl of vLLM/EPP /metrics.
# Deliberately drops collect_replica_status/collect_pod_startup_times
# (Collector B, a different source entirely -- kubectl get
# deployments/statefulsets, not /metrics) -- see
# docs/plans/benchmark/run-only-metrics-gap.md's scope decision: replica
# counts already have two working client-side fallbacks in this port
# (sample_replicas.sh, scrape_wva_metrics.sh) and Collector B carries a
# known label-matching bug that's the reason those fallbacks exist.
#
# Runs INSIDE the harness pod, authenticating via the pod's own mounted
# ServiceAccount token (in-cluster kubectl config) -- no kubeconfig
# injection. Started/stopped once per workload (bracketing the harness
# executable call), matching how every upstream workload/harnesses/*.sh
# wrapper actually drives collect_metrics.sh: METRICS_DIR derives from
# $LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR, which changes per workload.
#
# Writes metrics/raw/<pod>_<epoch>_metrics.log -- the same filename
# convention extract_real_trace.py's scan_raw() already globs, so no
# changes needed on the analysis side.
#
# Usage:
#   run_only_collect_metrics.sh start [duration]   - background continuous collection
#   run_only_collect_metrics.sh stop               - stop it
#   run_only_collect_metrics.sh snapshot            - one-shot collection
set -euo pipefail

METRICS_DIR="${LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR:?LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR must be set}/metrics"
COLLECTION_INTERVAL="${METRICS_COLLECTION_INTERVAL:-15}"
METRICS_PORT="${LLMDBENCH_VLLM_COMMON_METRICS_PORT:-${LLMDBENCH_VLLM_COMMON_INFERENCE_PORT:-8000}}"
INFERENCE_PORT="${LLMDBENCH_VLLM_COMMON_INFERENCE_PORT:-8000}"
METRICS_PATH="${LLMDBENCH_VLLM_MONITORING_METRICS_PATH:-/metrics}"
METRICS_CURL_TIMEOUT="${METRICS_CURL_TIMEOUT:-30}"
EPP_METRICS_PORT="${LLMDBENCH_EPP_METRICS_PORT:-9090}"
EPP_METRICS_SECRET="${LLMDBENCH_EPP_METRICS_SECRET:-inference-gateway-sa-metrics-reader-secret}"  # pragma: allowlist secret
_EPP_AUTH_HEADER=""

init_metrics_dir() {
    mkdir -p "$METRICS_DIR/raw"
    echo "Metrics directory initialized: $METRICS_DIR"
}

# Get vLLM pod names and IPs. Output: lines of "pod_name pod_ip" pairs.
get_pod_info() {
    local namespace="$1"
    local kubectl_cmd="${KUBECTL_CMD:-kubectl}"

    local pod_info
    pod_info=$($kubectl_cmd --namespace "$namespace" get pods \
        -l llm-d.ai/inferenceServing=true \
        --field-selector=status.phase=Running \
        -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}' 2>/dev/null || true)

    if [[ -z "$pod_info" ]]; then
        pod_info=$($kubectl_cmd --namespace "$namespace" get pods \
            -l stood-up-via=standalone \
            --field-selector=status.phase=Running \
            -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}' 2>/dev/null || true)
    fi

    if [[ -z "$pod_info" ]]; then
        local pod_pattern="${LLMDBENCH_METRICS_POD_PATTERN:-decode}"
        local pod_names
        pod_names=$($kubectl_cmd get pods -n "$namespace" 2>/dev/null | grep "$pod_pattern" | grep "Running" | awk '{print $1}')
        if [[ -n "$pod_names" ]]; then
            for pod in $pod_names; do
                local ip
                ip=$($kubectl_cmd get pod -n "$namespace" "$pod" -o jsonpath='{.status.podIP}' 2>/dev/null || true)
                if [[ -n "$ip" ]]; then
                    pod_info="${pod_info:+${pod_info}$'\n'}${pod} ${ip}"
                fi
            done
        fi
    fi

    echo "$pod_info"
}

# Get EPP (inference scheduler) pod names and IPs. Output: lines of "pod_name pod_ip" pairs.
get_epp_pod_info() {
    local namespace="$1"
    local kubectl_cmd="${KUBECTL_CMD:-kubectl}"

    local pod_info
    pod_info=$($kubectl_cmd --namespace "$namespace" get pods \
        -l inferencepool \
        --field-selector=status.phase=Running \
        -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}' 2>/dev/null || true)

    if [[ -z "$pod_info" ]]; then
        local pod_names
        pod_names=$($kubectl_cmd get pods -n "$namespace" 2>/dev/null | grep -i "epp" | grep "Running" | awk '{print $1}')
        if [[ -n "$pod_names" ]]; then
            for pod in $pod_names; do
                local ip
                ip=$($kubectl_cmd get pod -n "$namespace" "$pod" -o jsonpath='{.status.podIP}' 2>/dev/null || true)
                if [[ -n "$ip" ]]; then
                    pod_info="${pod_info:+${pod_info}$'\n'}${pod} ${ip}"
                fi
            done
        fi
    fi

    echo "$pod_info"
}

# Bearer token for EPP's authenticated /metrics, cached after first call.
_get_epp_auth_header() {
    if [[ -n "$_EPP_AUTH_HEADER" ]]; then
        echo "$_EPP_AUTH_HEADER"
        return
    fi
    local namespace="${LLMDBENCH_VLLM_COMMON_NAMESPACE:-default}"
    local kubectl_cmd="${KUBECTL_CMD:-kubectl}"
    local token
    token=$($kubectl_cmd get secret "$EPP_METRICS_SECRET" \
        --namespace "$namespace" \
        -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null) || true
    if [[ -n "$token" ]]; then
        _EPP_AUTH_HEADER="Authorization: Bearer $token"
    fi
    echo "$_EPP_AUTH_HEADER"
}

# Scrape Prometheus /metrics from a single pod.
_scrape_pod() {
    local pod_name="$1" pod_ip="$2" timestamp="$3" output_file="$4" port="$5"
    local source_tag="$6" fallback_port="${7:-}" auth_header="${8:-}"
    local debug_log="$METRICS_DIR/raw/collection_debug.log"
    local tmp_file="${output_file}.tmp"

    local -a curl_auth=()
    if [[ -n "$auth_header" ]]; then
        curl_auth=(-H "$auth_header")  # pragma: allowlist secret
    fi

    {
        echo "# Timestamp: $timestamp"
        echo "# Pod: $pod_name"
        echo "# PodIP: $pod_ip"
        echo "# Source: $source_tag"
        echo ""
    } > "$output_file"

    local url="http://${pod_ip}:${port}${METRICS_PATH}"
    local rc=0
    curl -sS --connect-timeout 5 --max-time "$METRICS_CURL_TIMEOUT" \
        "${curl_auth[@]+"${curl_auth[@]}"}" \
        "$url" > "$tmp_file" 2>>"$debug_log" || rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "  [$(date -u +%H:%M:%S)] curl failed (rc=$rc) for ${pod_name} ${url}" >> "$debug_log"
    fi

    if [[ -s "$tmp_file" ]] && head -1 "$tmp_file" | grep -qiE '^(Unauthorized|Forbidden)$'; then
        echo "  [$(date -u +%H:%M:%S)] Auth rejected for ${pod_name}, retrying without auth" >> "$debug_log"
        rc=0
        curl -sS --connect-timeout 5 --max-time "$METRICS_CURL_TIMEOUT" \
            "$url" > "$tmp_file" 2>>"$debug_log" || rc=$?
    fi

    if [[ ! -s "$tmp_file" && -n "$fallback_port" && "$port" != "$fallback_port" ]]; then
        url="http://${pod_ip}:${fallback_port}${METRICS_PATH}"
        echo "  [$(date -u +%H:%M:%S)] Retrying ${pod_name} on fallback port: ${url}" >> "$debug_log"
        rc=0
        curl -sS --connect-timeout 5 --max-time "$METRICS_CURL_TIMEOUT" \
            "${curl_auth[@]+"${curl_auth[@]}"}" \
            "$url" > "$tmp_file" 2>>"$debug_log" || rc=$?
        if [[ $rc -ne 0 ]]; then
            echo "  [$(date -u +%H:%M:%S)] curl failed (rc=$rc) for ${pod_name} ${url}" >> "$debug_log"
        fi
    fi

    if [[ -s "$tmp_file" ]]; then
        cat "$tmp_file" >> "$output_file"
    else
        echo "# Warning: Failed to collect metrics from pod $pod_name ($pod_ip)" >> "$output_file"
    fi
    echo "" >> "$output_file"
    rm -f "$tmp_file"
    return 0
}

# Collect one metrics snapshot from all vLLM and EPP pods.
collect_metrics_snapshot() {
    local namespace="${LLMDBENCH_VLLM_COMMON_NAMESPACE:-default}"
    local timestamp=$(date +%s)
    local iso_timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S%z")

    echo "Collecting metrics at $iso_timestamp (namespace: $namespace)"

    local pod_info
    pod_info=$(get_pod_info "$namespace")
    if [[ -z "$pod_info" ]]; then
        echo "Warning: No running vLLM pods found in namespace $namespace" >&2
    else
        echo "$pod_info" | while read -r pod_name pod_ip; do
            [[ -z "$pod_ip" || -z "$pod_name" ]] && continue
            _scrape_pod "$pod_name" "$pod_ip" "$iso_timestamp" \
                "$METRICS_DIR/raw/${pod_name}_${timestamp}_metrics.log" \
                "$METRICS_PORT" "prometheus_metrics" "$INFERENCE_PORT" &
        done
    fi

    local epp_info
    epp_info=$(get_epp_pod_info "$namespace")
    if [[ -n "$epp_info" ]]; then
        local epp_auth
        epp_auth=$(_get_epp_auth_header)
        echo "$epp_info" | while read -r pod_name pod_ip; do
            [[ -z "$pod_ip" || -z "$pod_name" ]] && continue
            _scrape_pod "$pod_name" "$pod_ip" "$iso_timestamp" \
                "$METRICS_DIR/raw/${pod_name}_${timestamp}_metrics.log" \
                "$EPP_METRICS_PORT" "epp_prometheus_metrics" "" "$epp_auth" &
        done
    fi

    wait
}

start_continuous_collection() {
    local duration="${1:-0}"
    init_metrics_dir
    echo "Starting continuous metrics collection (interval: ${COLLECTION_INTERVAL}s)"
    echo $$ > "$METRICS_DIR/collector.pid"

    local start_time=$(date +%s) iterations=0
    while true; do
        collect_metrics_snapshot
        iterations=$((iterations + 1))
        if [[ $duration -gt 0 ]]; then
            local elapsed=$(( $(date +%s) - start_time ))
            [[ $elapsed -ge $duration ]] && break
        fi
        sleep "$COLLECTION_INTERVAL"
    done
    echo "Collected $iterations snapshots"
    rm -f "$METRICS_DIR/collector.pid"
}

stop_continuous_collection() {
    if [[ -f "$METRICS_DIR/collector.pid" ]]; then
        local pid=$(cat "$METRICS_DIR/collector.pid")
        if kill -0 "$pid" 2>/dev/null; then
            echo "Stopping metrics collector (PID: $pid)"
            kill "$pid"
            rm -f "$METRICS_DIR/collector.pid"
        fi
    fi
}

case "${1:-}" in
    start)   start_continuous_collection "${2:-0}" ;;
    stop)    stop_continuous_collection ;;
    snapshot) init_metrics_dir; collect_metrics_snapshot ;;
    *)
        echo "Usage: $0 {start [duration]|stop|snapshot}"
        exit 1
        ;;
esac
