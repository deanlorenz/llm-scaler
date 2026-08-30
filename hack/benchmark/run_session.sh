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
#   BENCH_IMAGE_DIGEST       Image digest (default: pinned sha256 below).
#                            Override to use a different digest or clear to use tag only.
#                            Update when a new image version is available:
#                              docker inspect ghcr.io/llm-d/llm-d-benchmark:<tag> \
#                                --format '{{index .RepoDigests 0}}'
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
# Pinned digest for ghcr.io/llm-d/llm-d-benchmark:v0.7.8 — pinned 2026-08-30.
# Update when moving to a new image version. To get the digest of a new tag:
#   docker inspect ghcr.io/llm-d/llm-d-benchmark:<tag> --format '{{index .RepoDigests 0}}'
# or from a running pod:
#   kubectl get pod <pod> -o jsonpath='{.status.containerStatuses[0].imageID}'
_DEFAULT_IMAGE_DIGEST="sha256:6c8be427777df57fc6ef8da18ba4a7e6311a0c4ad26daee2fd306ae591fccc63"
IMAGE_DIGEST="${BENCH_IMAGE_DIGEST:-${_DEFAULT_IMAGE_DIGEST}}"
# Reference by digest when available, tag only otherwise.
if [ -n "$IMAGE_DIGEST" ]; then
    IMAGE="ghcr.io/llm-d/llm-d-benchmark@${IMAGE_DIGEST}"
else
    IMAGE="ghcr.io/llm-d/llm-d-benchmark:${IMAGE_TAG}"
fi
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
    imagePullPolicy: IfNotPresent
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
      value: "16"
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
# Each patch is a standalone Python file in hack/benchmark/patches/.
# _apply_patch copies it into the pod and runs it against the target file.
# The patch script receives the target path as sys.argv[1] and exits non-zero
# on failure (anchor missing, write error), zero on success or already-applied.
_apply_patch() {
    local label="$1"
    local local_script="$2"
    local remote_target="$3"
    local remote_script="/tmp/$(basename "$local_script")"

    $KUBECTL cp "$local_script" "${NS}/${POD}:${remote_script}" \
        || { _error "Patch '$label': failed to copy script to pod"; return 1; }

    $KUBECTL exec "$POD" -n "$NS" -- \
        python3 "$remote_script" "$remote_target" \
        || { _error "Patch '$label': script exited non-zero"; return 1; }

    $KUBECTL exec "$POD" -n "$NS" -- rm -f "$remote_script" || true
}

_patch_pod() {
    _pod_ready || _error "Pod $POD is not Ready — run 'ensure' first."

    local patches_dir
    patches_dir="$(dirname "${BASH_SOURCE[0]}")/patches"

    _info "Applying in-pod patch 1: process_epp_logs.py (EPP float timestamp)..."
    _apply_patch "fix1-epp-float-ts" \
        "${patches_dir}/fix1_epp_float_ts.py" \
        "/usr/local/bin/process_epp_logs.py"

    _info "Applying in-pod patch 2: guidellm-analyze_results.sh (non-fatal conversion)..."
    _apply_patch "fix2-conversion-non-fatal" \
        "${patches_dir}/fix2_conversion_non_fatal.py" \
        "/usr/local/bin/guidellm-analyze_results.sh"

    _info "In-pod patches complete."
}

# ---------------------------------------------------------------------------
# EPP metrics preflight (runs inside the harness pod after ensure)
# ---------------------------------------------------------------------------
# Verifies the EPP bearer token can be read by the pod's SA and that the EPP
# metrics endpoint responds with actual metrics (not Unauthorized or a timeout).
# Prints a clear WARNING but does not abort — EPP metrics are optional; a run
# with missing EPP data is still valid. The point is to surface the failure
# before the run starts, not after 25 minutes of silent Unauthorized scrapes.
_preflight_epp() {
    local secret="$EPP_METRICS_SECRET"
    _info "EPP metrics preflight: secret=$secret namespace=$NS"

    # Step 1: verify the SA can read the secret and it has a token field.
    #
    # The most common cause of an empty token is a kustomize namePrefix bug:
    # the overlay renames the SA (e.g. epp-metrics-reader → wva-epp-metrics-reader)
    # but the kubernetes.io/service-account.name annotation inside the Secret still
    # names the original SA. The API server finds no matching SA and never populates
    # .data.token. Fix: patch the annotation to the prefixed name and replace the
    # secret so the token controller re-evaluates it:
    #
    #   kubectl annotate secret <secret> -n <ns> \
    #     kubernetes.io/service-account.name=<prefixed-sa-name> --overwrite
    #   kubectl replace -f - <<EOF
    #   apiVersion: v1
    #   kind: Secret
    #   metadata:
    #     name: <secret>
    #     namespace: <ns>
    #     annotations:
    #       kubernetes.io/service-account.name: <prefixed-sa-name>
    #   type: kubernetes.io/service-account-token
    #   EOF
    #
    # Or redeploy WVA from a fixed kustomize base (the fix is in
    # config/base/rbac/kustomization.yaml replacements[]).
    local token
    token=$($KUBECTL exec "$POD" -n "$NS" -- \
        kubectl get secret "$secret" -n "$NS" \
        -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null) || true
    if [ -z "$token" ]; then
        # Probe what SA the secret's annotation currently names — that tells the
        # user whether this is the namePrefix mismatch or something else entirely.
        local annotated_sa
        annotated_sa=$($KUBECTL get secret "$secret" -n "$NS" \
            -o jsonpath='{.metadata.annotations.kubernetes\.io/service-account\.name}' \
            2>/dev/null) || true
        local actual_sa
        actual_sa=$($KUBECTL get sa -n "$NS" \
            -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
            tr ' ' '\n' | grep -i 'epp.*metrics\|metrics.*epp' | head -1) || true
        echo "run_session: WARNING: EPP preflight FAIL — secret '$secret' in $NS has no token." >&2
        if [ -n "$annotated_sa" ] && [ -n "$actual_sa" ] && [ "$annotated_sa" != "$actual_sa" ]; then
            echo "run_session:   Cause: the secret's annotation names SA '$annotated_sa'" \
                 "but the actual SA in this namespace is '$actual_sa'." \
                 "This is the kustomize namePrefix bug — the overlay renamed the SA" \
                 "but not the annotation inside the secret, so the API server never" \
                 "populated .data.token." >&2
            echo "run_session:   Fix (without redeploying WVA):" >&2
            echo "run_session:     kubectl annotate secret $secret -n $NS \\" >&2
            echo "run_session:       kubernetes.io/service-account.name=$actual_sa --overwrite" >&2
            echo "run_session:     kubectl get secret $secret -n $NS -o yaml \\" >&2
            echo "run_session:       | kubectl replace -f -" >&2
            echo "run_session:   Fix (permanent — redeploy from updated kustomize base):" >&2
            echo "run_session:     make deploy-wva-on-openshift NAMESPACE=$NS" >&2
        else
            echo "run_session:   The pod SA may lack 'get' on secret '$secret'," \
                 "or the secret does not exist. Check the Role in $NS:" >&2
            echo "run_session:     kubectl get role llmdbench-harness-role -n $NS -o yaml" >&2
        fi
        echo "run_session:   All EPP scrapes will return Unauthorized for this run." >&2
        return
    fi
    _info "EPP preflight: token obtained from secret $secret."

    # Step 2: find an EPP pod IP and verify the metrics endpoint accepts the token.
    local epp_ip
    epp_ip=$($KUBECTL get pods -n "$NS" \
        -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}' \
        2>/dev/null | grep -i epp | head -1 | awk '{print $2}') || true
    if [ -z "$epp_ip" ]; then
        _info "EPP preflight: no EPP pod found in $NS — skipping connectivity check."
        return
    fi

    local result
    result=$($KUBECTL exec "$POD" -n "$NS" -- \
        curl -sS --connect-timeout 5 --max-time 10 \
        -H "Authorization: Bearer $token" \
        "http://${epp_ip}:9090/metrics" 2>/dev/null | head -1) || true

    if [ "$result" = "Unauthorized" ]; then
        echo "run_session: WARNING: EPP preflight FAIL — token from secret '$secret'" \
             "is rejected by the EPP pod ($epp_ip:9090)." >&2
        echo "run_session:   The token was obtained but the EPP rejected it." \
             "Check that the ClusterRoleBinding 'epp-metrics-reader-role-binding'" \
             "includes SA '$(_detect_epp_secret_sa)' and that the EPP's" \
             "authentication webhook is configured correctly." >&2
        echo "run_session:   All EPP scrapes will return Unauthorized for this run." >&2
    elif [ -z "$result" ]; then
        echo "run_session: WARNING: EPP preflight FAIL — no response from EPP pod" \
             "($epp_ip:9090). Connection timed out — EPP metrics port may be wrong" \
             "or a NetworkPolicy is blocking in-pod access." >&2
        echo "run_session:   Set BENCH_EPP_METRICS_PORT=<port> if the EPP listens" \
             "on a non-standard port." >&2
    else
        _info "EPP preflight OK: EPP pod $epp_ip:9090 returned metrics."
    fi
}

# Return the EPP metrics SA name as it actually exists in $NS (post-namePrefix).
_detect_epp_secret_sa() {
    $KUBECTL get sa -n "$NS" \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null | \
        tr ' ' '\n' | grep -i 'epp.*metrics\|metrics.*epp' | head -1
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
    _preflight_epp
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
