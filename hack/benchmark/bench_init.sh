#!/usr/bin/env bash
# bench_init.sh — discover a deployed llm-d stack and write bench-meta.json.
#
# Read-only: queries the cluster, writes one JSON file locally. No deploying,
# scaling, pausing, or modifying anything on the cluster.
#
# Usage:
#   bench_init.sh <namespace> [output-dir]
#
# Arguments:
#   <namespace>    Kubernetes namespace to inspect (also BENCH_NAMESPACE env)
#   [output-dir]   Directory to write bench-meta.json (default:
#                  hack/benchmark/bench-scratch/<namespace>/)
#
# Environment:
#   BENCH_NAMESPACE      Namespace (overridden by $1 when both are set)
#   BENCH_KUBECONFIG     Path to kubeconfig; exported as KUBECONFIG when set
#   BENCH_KUBE_CONTEXT   Expected kube context; script fails if mismatch
#   BENCH_IMAGE_TAG      Expected harness image tag; warns on drift (no fail)
#   KUBECTL_CMD          kubectl binary (default: kubectl)
#
# Output:
#   <output-dir>/bench-meta.json  — stack metadata consumed by bench-runtools
#
# The file is written atomically (temp → mv). Re-running overwrites the
# previous file safely. The output path is printed as the final stdout line.
#
# Prereqs: kubectl, bash, python3. No jq. No llmdbenchmark CLI. No repo clone.
# resolve_router_endpoint.sh must exist at hack/benchmark/ (owned by bench-runtools).
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
# Arguments and environment
# ---------------------------------------------------------------------------
NS="${1:-${BENCH_NAMESPACE:-}}"
if [ -z "$NS" ]; then
    echo "bench_init: namespace required as \$1 or BENCH_NAMESPACE" >&2
    echo "Usage: $0 <namespace> [output-dir]" >&2
    exit 1
fi

OUT_DIR="${2:-${_SCRIPT_DIR}/bench-scratch/${NS}}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

# ---------------------------------------------------------------------------
# Guards
# ---------------------------------------------------------------------------
if [ -n "${BENCH_KUBECONFIG:-}" ]; then
    export KUBECONFIG="$BENCH_KUBECONFIG"
fi

if [ -n "${BENCH_KUBE_CONTEXT:-}" ]; then
    live_ctx=$($KUBECTL config current-context 2>/dev/null || true)
    if [ "$live_ctx" != "$BENCH_KUBE_CONTEXT" ]; then
        echo "bench_init: context mismatch" >&2
        echo "  expected: $BENCH_KUBE_CONTEXT" >&2
        echo "  live:     ${live_ctx:-<none>}" >&2
        echo "  Set BENCH_KUBECONFIG or switch context before running bench-init." >&2
        exit 1
    fi
fi

if ! $KUBECTL get namespace "$NS" >/dev/null 2>&1; then
    echo "bench_init: namespace '$NS' not found" >&2
    exit 1
fi

echo "bench_init: discovering stack in namespace '$NS'..."

# ---------------------------------------------------------------------------
# Resolve endpoint URL (shared across all stacks — same EPP)
# ---------------------------------------------------------------------------
resolve_ep="$_SCRIPT_DIR/resolve_router_endpoint.sh"
if [ ! -x "$resolve_ep" ] && [ ! -f "$resolve_ep" ]; then
    echo "bench_init: resolve_router_endpoint.sh not found at $resolve_ep" >&2
    echo "  This script is owned by bench-runtools. Ensure both worktrees are merged." >&2
    exit 1
fi
endpoint_url=$(bash "$resolve_ep" "$NS" 2>/dev/null || true)
if [ -z "$endpoint_url" ]; then
    echo "bench_init: WARNING: no router/EPP service found in '$NS' — endpoint_url will be empty" >&2
fi

# ---------------------------------------------------------------------------
# Discover EPP metrics secret (shared across all stacks)
# ---------------------------------------------------------------------------
epp_metrics_secret=$($KUBECTL get secret -n "$NS" \
    -l app.kubernetes.io/name=workload-variant-autoscaler \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)

# ---------------------------------------------------------------------------
# Discover WVA controller deployment and metrics service
# ---------------------------------------------------------------------------
wva_deploy=$($KUBECTL get deploy -n "$NS" \
    -l app.kubernetes.io/name=workload-variant-autoscaler \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
wva_deploy="${wva_deploy:-wva-controller-manager}"

wva_metrics_svc=$($KUBECTL get svc -n "$NS" \
    -l app.kubernetes.io/name=workload-variant-autoscaler \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
wva_metrics_svc="${wva_metrics_svc:-wva-controller-manager-metrics-service}"

# Parse --metrics-bind-address and --metrics-secure from WVA deploy container args
wva_metrics_port=$($KUBECTL get deploy "$wva_deploy" -n "$NS" \
    -o json 2>/dev/null | python3 -c '
import json, sys, re
d = json.load(sys.stdin)
args = (d.get("spec",{}).get("template",{}).get("spec",{})
         .get("containers",[{}])[0].get("args") or [])
for a in args:
    m = re.search(r"--metrics-bind-address=:(\d+)", str(a))
    if m:
        print(m.group(1))
        sys.exit(0)
print("8443")
' 2>/dev/null || echo "8443")

wva_metrics_secure=$($KUBECTL get deploy "$wva_deploy" -n "$NS" \
    -o json 2>/dev/null | python3 -c '
import json, sys
d = json.load(sys.stdin)
args = (d.get("spec",{}).get("template",{}).get("spec",{})
         .get("containers",[{}])[0].get("args") or [])
print("true" if "--metrics-secure=true" in args else "false")
' 2>/dev/null || echo "false")

# ---------------------------------------------------------------------------
# Discover HF token secret
# ---------------------------------------------------------------------------
hf_token_secret=$($KUBECTL get secret -n "$NS" \
    -o jsonpath='{.items[*].metadata.name}' 2>/dev/null \
    | tr ' ' '\n' | grep -E 'hf.token|hf-token' | head -1 || true)

# ---------------------------------------------------------------------------
# Detect Prometheus URL
# ---------------------------------------------------------------------------
prom_type="unknown"
prom_url=""

if $KUBECTL get svc thanos-querier -n openshift-monitoring >/dev/null 2>&1; then
    prom_type="openshift-thanos"
    prom_url="https://thanos-querier.openshift-monitoring.svc.cluster.local:9091"
else
    for prom_ns in monitoring prometheus; do
        prom_svc=$($KUBECTL get svc -n "$prom_ns" \
            -l app.kubernetes.io/name=prometheus \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
        if [ -n "$prom_svc" ]; then
            prom_type="kube-prometheus-stack"
            prom_url="http://${prom_svc}.${prom_ns}.svc.cluster.local:9090"
            break
        fi
    done
    if [ "$prom_type" = "unknown" ]; then
        prom_svc=$($KUBECTL get svc -n "$NS" \
            -l app.kubernetes.io/name=prometheus \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
        if [ -n "$prom_svc" ]; then
            prom_type="in-namespace"
            prom_url="http://${prom_svc}.${NS}.svc.cluster.local:9090"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Drift check: running harness pod image tag vs BENCH_IMAGE_TAG
# ---------------------------------------------------------------------------
if [ -n "${BENCH_IMAGE_TAG:-}" ]; then
    harness_image=$($KUBECTL get pod -n "$NS" \
        -l app=llmdbench-harness \
        --field-selector=status.phase=Running \
        -o jsonpath='{.items[0].spec.containers[0].image}' 2>/dev/null || true)
    if [ -z "$harness_image" ]; then
        harness_image=$($KUBECTL get pod -n "$NS" \
            -l app.kubernetes.io/name=llmdbench \
            --field-selector=status.phase=Running \
            -o jsonpath='{.items[0].spec.containers[0].image}' 2>/dev/null || true)
    fi
    if [ -n "$harness_image" ]; then
        running_tag="${harness_image##*:}"
        if [ "$running_tag" != "$BENCH_IMAGE_TAG" ]; then
            echo "bench_init: WARNING: running harness image tag '$running_tag' differs from BENCH_IMAGE_TAG='$BENCH_IMAGE_TAG'" >&2
            echo "  Run 'make bench-teardown' to replace the pod, or update BENCH_IMAGE_TAG." >&2
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Discover ScaledObjects → build stacks JSON
# ---------------------------------------------------------------------------
# Gather all SO JSON in one call; parse with python3 to build the stacks array.
echo "bench_init: querying ScaledObjects..."

so_json=$($KUBECTL get scaledobject -n "$NS" -o json 2>/dev/null || echo '{"items":[]}')

stacks_json=$(echo "$so_json" | ENDPOINT_URL="$endpoint_url" \
    EPP_METRICS_SECRET="$epp_metrics_secret" \
    KUBECTL="$KUBECTL" NS="$NS" \
    python3 -c '
import json, os, subprocess, sys

so_data = json.load(sys.stdin)
items = so_data.get("items", [])

ns            = os.environ["NS"]
kubectl       = os.environ["KUBECTL"]
endpoint_url  = os.environ.get("ENDPOINT_URL", "")
epp_secret    = os.environ.get("EPP_METRICS_SECRET", "")

def kubectl_get(resource, name, jsonpath, default=""):
    try:
        r = subprocess.run(
            [kubectl, "get", resource, name, "-n", ns,
             "-o", f"jsonpath={jsonpath}"],
            capture_output=True, text=True)
        return r.stdout.strip() or default
    except Exception:
        return default

def kubectl_json(resource, name):
    try:
        r = subprocess.run(
            [kubectl, "get", resource, name, "-n", ns, "-o", "json"],
            capture_output=True, text=True)
        return json.loads(r.stdout) if r.returncode == 0 else {}
    except Exception:
        return {}

def probe_vllm_label(deploy_name):
    candidates = [
        "llm-d.ai/role=decode",
        "app.kubernetes.io/component=decode",
        f"app={deploy_name}",
    ]
    for label in candidates:
        r = subprocess.run(
            [kubectl, "get", "pod", "-n", ns, "-l", label,
             "--field-selector=status.phase=Running",
             "-o", "jsonpath={.items[0].metadata.name}"],
            capture_output=True, text=True)
        if r.stdout.strip():
            return label
    return f"app={deploy_name}"

def so_paused(so):
    # Check status condition type=Paused status=True
    for cond in (so.get("status") or {}).get("conditions") or []:
        if cond.get("type") == "Paused" and cond.get("status") == "True":
            return True
    # Check annotation
    anns = (so.get("metadata") or {}).get("annotations") or {}
    if "autoscaling.keda.sh/paused-replicas" in anns:
        return True
    return False

def model_id_from_args(deploy_name):
    d = kubectl_json("deploy", deploy_name)
    containers = (d.get("spec",{}).get("template",{}).get("spec",{})
                  .get("containers") or [])
    for c in containers:
        args = c.get("args") or []
        # vLLM: args like ["Qwen/Qwen3-0.6B", "--disable-access-log", ...]
        #       or ["serve", "Qwen/Qwen3-0.6B", ...]
        for i, a in enumerate(args):
            if a == "serve" and i + 1 < len(args):
                return args[i + 1]
        for a in args:
            if "/" in a and not a.startswith("-"):
                return a
    return ""

def strip_wva_suffix(name):
    """optimized-baseline-nvidia-gpu-vllm-decode-wva[-v2] -> ...-decode[-v2]"""
    import re
    # Strip -wva and optional variant suffix (-v2, -v3, etc.) before it
    m = re.match(r"^(.*)-wva(-v\d+.*)?$", name)
    if m:
        base = m.group(1)
        variant = m.group(2) or ""
        return base + variant
    return name

stacks = []
for so in items:
    name_raw = so["metadata"]["name"]
    stack_name = strip_wva_suffix(name_raw)
    deploy_name = (so.get("spec",{}).get("scaleTargetRef",{}).get("name") or "")

    # model_id: trigger metadata.modelID first, then deploy args fallback
    model_id = ""
    for t in (so.get("spec",{}).get("triggers") or []):
        mid = (t.get("metadata") or {}).get("modelID","")
        if mid:
            model_id = mid
            break
    if not model_id and deploy_name:
        model_id = model_id_from_args(deploy_name)

    min_rep = so.get("spec",{}).get("minReplicaCount", 1)
    max_rep = so.get("spec",{}).get("maxReplicaCount", 10)

    ready = 0
    if deploy_name:
        v = kubectl_get("deploy", deploy_name, "{.status.readyReplicas}", "0")
        try:
            ready = int(v)
        except (ValueError, TypeError):
            ready = 0

    vllm_label = probe_vllm_label(deploy_name) if deploy_name else "llm-d.ai/role=decode"

    stacks.append({
        "name":               stack_name,
        "deployment":         deploy_name,
        "scaledobject":       name_raw,
        "model_id":           model_id,
        "endpoint_url":       endpoint_url,
        "epp_metrics_secret": epp_secret,
        "vllm_pod_label":     vllm_label,
        "vllm_metrics_port":  8200,
        "epp_metrics_port":   9090,
        "min_replicas":       min_rep,
        "max_replicas":       max_rep,
        "so_paused":          so_paused(so),
        "ready_replicas":     ready,
    })

print(json.dumps(stacks))
')

# ---------------------------------------------------------------------------
# Assemble and write bench-meta.json
# ---------------------------------------------------------------------------
created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubeconfig_val="${BENCH_KUBECONFIG:-}"
kube_context=$($KUBECTL config current-context 2>/dev/null || echo "")

mkdir -p "$OUT_DIR"
tmp="${OUT_DIR}/bench-meta.json.tmp"

python3 - <<PYEOF
import json, sys

meta = {
    "schema_version": "1",
    "created_at":     "${created_at}",
    "identity": {
        "kubeconfig":   "${kubeconfig_val}",
        "kube_context": "${kube_context}",
        "namespace":    "${NS}",
    },
    "stacks": ${stacks_json},
    "wva": {
        "controller_deployment": "${wva_deploy}",
        "metrics_service":       "${wva_metrics_svc}",
        "metrics_port":          int("${wva_metrics_port}"),
        "metrics_secure":        "${wva_metrics_secure}" == "true",
    },
    "hf_token_secret": "${hf_token_secret}",
    "prometheus": {
        "type": "${prom_type}",
        "url":  "${prom_url}",
    },
}
with open("${tmp}", "w") as f:
    json.dump(meta, f, indent=2)
    f.write("\n")
PYEOF

mv "$tmp" "${OUT_DIR}/bench-meta.json"

n_stacks=$(echo "$stacks_json" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
echo "bench_init: wrote bench-meta.json  (stacks=${n_stacks}, prometheus=${prom_type})"
echo "${OUT_DIR}/bench-meta.json"
