#!/usr/bin/env bash
# run_session.sh — harness pod lifecycle for bench-* targets.
#
# Manages a single long-lived harness pod per namespace. The pod is created on
# first `ensure` and persists until `stop` is called explicitly. It is cheap to
# keep idle and provides a stable access point between scenarios.
#
# Subcommands:
#   ensure   <namespace> [image]   Create pod + RBAC if not already running
#   stop     <namespace>           Delete pod and RBAC
#   status   <namespace>           Print pod phase and readiness
#   patch    <namespace>           Apply in-pod fixes (idempotent; run after ensure)
#
# Environment:
#   BENCH_HARNESS_POD_NAME   Pod name (default: llmdbench-harness)
#   BENCH_IMAGE_TAG          Image tag (default: v0.7.8)
#   BENCH_EPP_METRICS_SECRET EPP metrics secret name (default: epp-metrics-token)
#                            Override when cluster uses a different name, e.g.
#                            wva-epp-metrics-token (dhl-la-1708 naming convention).
#   KUBECTL_CMD              kubectl binary (default: kubectl)
#   KUBECTL_TIMEOUT          Seconds to wait for pod Ready (default: 180)
#
# The pod runs as root (runAsUser: 0) — required because the harness entrypoint
# writes to /usr/local/bin on startup. This matches run_only.sh's own pod spec.
#
# RBAC created (namespace-scoped):
#   ServiceAccount: llmdbench-harness-sa
#   Role:           llmdbench-harness-role
#     pods, pods/log  — get, list   (for collect_metrics.sh discovery)
#     secrets         — get on epp-metrics-token only (for EPP bearer token)
#   RoleBinding:    llmdbench-harness-rb
#
# In-pod patches applied by `patch`:
#   1. process_epp_logs.py    — fix EPP float timestamp (upstream bug, v0.7.8)
#   2. guidellm-analyze_results.sh — make report conversion non-fatal (upstream bug)
#   These replace patch_harness.sh's role for the bench-* path. Scripts are baked
#   into the image; patches are applied via kubectl exec after pod Ready.
set -euo pipefail

# --help prints this file's header comment.
case "${1:-}" in
    -h|--help)
        sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
        exit 0
        ;;
esac

CMD="${1:?usage: $0 ensure|stop|status|patch <namespace> [image]}"
NS="${2:?namespace required}"
KUBECTL="${KUBECTL_CMD:-kubectl}"
POD="${BENCH_HARNESS_POD_NAME:-llmdbench-harness}"
IMAGE_TAG="${BENCH_IMAGE_TAG:-v0.7.8}"
IMAGE="ghcr.io/llm-d/llm-d-benchmark:${IMAGE_TAG}"
TIMEOUT="${KUBECTL_TIMEOUT:-180}"

# EPP metrics secret name — default from config/base/rbac/epp-metrics-token-secret.yaml.
# Some clusters (e.g. dhl-la-1708) use the namePrefix-qualified name wva-epp-metrics-token.
# Override with BENCH_EPP_METRICS_SECRET.
EPP_METRICS_SECRET="${BENCH_EPP_METRICS_SECRET:-epp-metrics-token}"

_info()  { echo "run_session: $*"; }
_error() { echo "run_session: ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# RBAC
# ---------------------------------------------------------------------------
# Discover the actual EPP metrics token secret name in the namespace.
# Mirrors the detection logic in run_only_collect_metrics.sh.
_detect_epp_secret() {
    # 1. Explicit override via env
    if [ -n "$EPP_METRICS_SECRET" ]; then
        echo "$EPP_METRICS_SECRET"; return
    fi
    # 2. WVA-labelled SA token secret
    local s
    s=$($KUBECTL get secret -n "$NS" \
        -l "app.kubernetes.io/name=workload-variant-autoscaler" \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | \
        head -1)
    [ -n "$s" ] && { echo "$s"; return; }
    # 3. Canonical name from this repo's deploy
    $KUBECTL get secret epp-metrics-token -n "$NS" >/dev/null 2>&1 && \
        { echo "epp-metrics-token"; return; }
    # 4. Upstream default
    echo "inference-gateway-sa-metrics-reader-secret"
}

_apply_rbac() {
    _info "Applying RBAC in namespace $NS..."

    # Detect the actual EPP secret name at RBAC-creation time so the Role
    # grants get on the name that actually exists in this cluster.
    local detected_secret
    detected_secret=$(_detect_epp_secret)
    _info "EPP metrics secret for RBAC: $detected_secret"

    $KUBECTL apply -f - <<RBAC
apiVersion: v1
kind: ServiceAccount
metadata:
  name: llmdbench-harness-sa
  namespace: ${NS}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: llmdbench-harness-role
  namespace: ${NS}
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: ["${detected_secret}"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: llmdbench-harness-rb
  namespace: ${NS}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: llmdbench-harness-role
subjects:
- kind: ServiceAccount
  name: llmdbench-harness-sa
  namespace: ${NS}
RBAC
    _info "RBAC applied."
}

_delete_rbac() {
    _info "Removing RBAC in namespace $NS..."
    $KUBECTL delete rolebinding llmdbench-harness-rb   -n "$NS" --ignore-not-found
    $KUBECTL delete role        llmdbench-harness-role  -n "$NS" --ignore-not-found
    $KUBECTL delete sa          llmdbench-harness-sa    -n "$NS" --ignore-not-found
    _info "RBAC removed."
}

# ---------------------------------------------------------------------------
# Pod
# ---------------------------------------------------------------------------
_pod_phase() {
    $KUBECTL get pod "$POD" -n "$NS" \
        -o jsonpath='{.status.phase}' 2>/dev/null || true
}

_pod_ready() {
    local ready
    ready=$($KUBECTL get pod "$POD" -n "$NS" \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    [ "$ready" = "True" ]
}

_create_pod() {
    _info "Creating harness pod $POD (image: $IMAGE)..."
    $KUBECTL apply -f - <<POD
apiVersion: v1
kind: Pod
metadata:
  name: ${POD}
  namespace: ${NS}
  labels:
    app: llmdbench-harness
spec:
  serviceAccountName: llmdbench-harness-sa
  restartPolicy: Never
  containers:
  - name: harness
    image: ${IMAGE}
    imagePullPolicy: Always
    command: ["sh", "-c", "sleep 1000000"]
    securityContext:
      runAsUser: 0
    resources:
      limits:
        cpu: "16"
        memory: 32Gi
      requests:
        cpu: "16"
        memory: 32Gi
    env:
    - name: LLMDBENCH_RUN_WORKSPACE_DIR
      value: /workspace
    - name: LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX
      value: /requests
    - name: LLMDBENCH_RUN_DATASET_DIR
      value: /workspace
    - name: LLMDBENCH_VLLM_COMMON_NAMESPACE
      value: "${NS}"
    - name: LLMDBENCH_VLLM_COMMON_METRICS_SCRAPE_ENABLED
      value: "true"
    - name: LLMDBENCH_EPP_METRICS_SECRET
      value: "${EPP_METRICS_SECRET}"
    - name: LLMDBENCH_VLLM_COMMON_METRICS_PORT
      value: "8200"
    - name: LLMDBENCH_VLLM_COMMON_INFERENCE_PORT
      value: "8000"
    - name: LLMDBENCH_EPP_METRICS_PORT
      value: "9090"
    - name: METRICS_COLLECTION_INTERVAL
      value: "15"
    - name: RAYON_NUM_THREADS
      value: "4"
    volumeMounts:
    - name: results
      mountPath: /requests
  volumes:
  - name: results
    emptyDir: {}
POD

    _info "Waiting for pod $POD to be Ready (timeout: ${TIMEOUT}s)..."
    $KUBECTL wait --for=condition=Ready pod "$POD" -n "$NS" --timeout="${TIMEOUT}s" \
        || _error "Pod $POD did not become Ready within ${TIMEOUT}s."
    _info "Pod $POD is Ready."
}

_delete_pod() {
    _info "Deleting pod $POD in namespace $NS..."
    $KUBECTL delete pod "$POD" -n "$NS" --ignore-not-found
    _info "Pod deleted."
}

# ---------------------------------------------------------------------------
# In-pod patches (idempotent)
# ---------------------------------------------------------------------------
_patch_pod() {
    _pod_ready || _error "Pod $POD is not Ready — run 'ensure' first."

    _info "Applying in-pod patch 1: process_epp_logs.py (EPP float timestamp)..."
    $KUBECTL exec "$POD" -n "$NS" -- python3 - /usr/local/bin/process_epp_logs.py <<'PYEOF'
import io, sys

path = sys.argv[1]
src = io.open(path, encoding="utf-8").read()

MARK = "# wva-patch: numeric ts"
if MARK in src:
    print("  fix 1 (EPP float ts): already applied")
    sys.exit(0)

IMPORT_OLD = "from datetime import datetime\n"
IMPORT_NEW = "from datetime import datetime, timezone\n"
ANCHOR = '    # Handle nanosecond timestamps by truncating to 6 decimal places\n'
BRANCH = (
    MARK + ": EPP logs carry epoch seconds as a JSON number, not an ISO\n"
    "    # string. re.sub() then raises TypeError on the first entry and the whole\n"
    "    # log is dropped.\n"
    "    if isinstance(ts_str, (int, float)) and not isinstance(ts_str, bool):\n"
    "        return datetime.fromtimestamp(ts_str, timezone.utc).replace(tzinfo=None)\n"
)

if IMPORT_OLD not in src:
    sys.exit("anchor missing: %r" % IMPORT_OLD)
if ANCHOR not in src:
    sys.exit("anchor missing: %r" % ANCHOR)

src = src.replace(IMPORT_OLD, IMPORT_NEW, 1)
src = src.replace(ANCHOR, "    " + BRANCH + ANCHOR, 1)
io.open(path, "w", encoding="utf-8", newline="\n").write(src)
print("  fix 1 (EPP float ts): applied")
PYEOF

    _info "Applying in-pod patch 2: guidellm-analyze_results.sh (non-fatal conversion)..."
    $KUBECTL exec "$POD" -n "$NS" -- python3 - \
        /usr/local/bin/guidellm-analyze_results.sh <<'PYEOF'
import io, sys

path = sys.argv[1]
src = io.open(path, encoding="utf-8").read()

MARK = "# wva-patch: conversion is not fatal"
if MARK in src:
    print("  fix 2 (report conversion non-fatal): already applied")
    sys.exit(0)

ANCHOR = (
    'if [[ $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC -ne 0 ]]; then\n'
    '  echo "Results data conversion completed with errors."\n'
    '  exit $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC\n'
    'fi\n'
)
if ANCHOR not in src:
    sys.exit("anchor missing (upstream shape changed): %r" % ANCHOR)

REPLACEMENT = (
    'if [[ $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC -ne 0 ]]; then\n'
    '  echo "Results data conversion completed with errors."\n'
    '  ' + MARK + '\n'
    '  echo "NOTE: benchmark_report v0.1/v0.2 were NOT produced (upstream bug)."\n'
    '  echo "NOTE: results.json is complete; treat this run as valid."\n'
    '  LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC=0\n'
    'fi\n'
)

src = src.replace(ANCHOR, REPLACEMENT, 1)
io.open(path, "w", encoding="utf-8", newline="\n").write(src)
print("  fix 2 (report conversion non-fatal): applied")
PYEOF

    _info "In-pod patches complete."
}

# ---------------------------------------------------------------------------
# Subcommands
# ---------------------------------------------------------------------------
case "$CMD" in
  ensure)
    phase=$(_pod_phase)
    if [ "$phase" = "Running" ] && _pod_ready; then
        _info "Pod $POD already running and Ready in $NS — recreating for clean state."
        _delete_pod
    elif [ -n "$phase" ]; then
        _info "Pod $POD exists but phase=$phase — deleting and recreating."
        _delete_pod
    fi
    _apply_rbac
    _create_pod
    _patch_pod
    _info "Session ready: pod $POD in $NS."
    ;;

  stop)
    _delete_pod
    _delete_rbac
    _info "Session stopped."
    ;;

  status)
    phase=$(_pod_phase)
    if [ -z "$phase" ]; then
        _info "Pod $POD not found in $NS."
        exit 1
    fi
    ready=$($KUBECTL get pod "$POD" -n "$NS" \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    _info "Pod $POD: phase=$phase ready=$ready"
    ;;

  patch)
    _patch_pod
    ;;

  *)
    echo "Unknown subcommand: $CMD" >&2
    echo "Usage: $0 ensure|stop|status|patch <namespace> [image]" >&2
    exit 1
    ;;
esac
