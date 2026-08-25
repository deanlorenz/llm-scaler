#!/usr/bin/env bash
# hooks/snapshot_replicas.sh — inter-scenario hook: record the replica count
# at each inter-scenario boundary as a timestamped JSONL entry.
#
# Produces a running log of replica state between scenarios, useful for
# verifying that scale-down completed before the next scenario started, and
# for correlating replica counts with scenario boundaries in post-analysis.
#
# Called by bench-run-all with:
#   $1  workload name just completed  (e.g. prefill_heavy)
#   $2  namespace                     (e.g. dhl-la-1708)
#   $3  session directory             (e.g. hack/benchmark/bench-scratch/dhl-la-1708-...)
#
# Output (appended to $3/replica_snapshots.jsonl):
#   {"workload":"prefill_heavy","timestamp":1724589123,"deployment":"optimized-baseline-nvidia-gpu-vllm-decode","replicas":3,"ready_replicas":2}
#
# Environment:
#   BENCH_META_FILE   Path to bench-meta.json (default: auto-detect from session dir)
#   KUBECTL_CMD       kubectl binary (default: kubectl)
#
# Exit code is always 0 — bench-run-all ignores hook failures.
set -uo pipefail

WORKLOAD="${1:-<unknown>}"
NS="${2:?namespace required}"
SESSION_DIR="${3:-}"

KUBECTL="${KUBECTL_CMD:-kubectl}"

_info() { echo "snapshot_replicas[$WORKLOAD]: $*"; }
_warn() { echo "snapshot_replicas[$WORKLOAD]: WARNING: $*" >&2; }

# ---------------------------------------------------------------------------
# Resolve bench-meta.json
# ---------------------------------------------------------------------------
_META_FILE="${BENCH_META_FILE:-}"
if [ -z "$_META_FILE" ] && [ -n "$SESSION_DIR" ]; then
    _META_FILE="$(dirname "$SESSION_DIR")/bench-meta.json"
fi
if [ -z "$_META_FILE" ] || [ ! -f "$_META_FILE" ]; then
    _SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    _META_FILE="${_SCRIPT_DIR}/../../bench-scratch/${NS}/bench-meta.json"
fi

# ---------------------------------------------------------------------------
# Collect snapshot for every non-paused stack
# ---------------------------------------------------------------------------
_TS=$(date +%s)
_OUT_FILE=""
if [ -n "$SESSION_DIR" ]; then
    mkdir -p "$SESSION_DIR"
    _OUT_FILE="$SESSION_DIR/replica_snapshots.jsonl"
fi

if [ ! -f "$_META_FILE" ]; then
    _warn "bench-meta.json not found at '$_META_FILE'; falling back to label-based discovery."
    # Fallback: find decode deployments by label
    _DEPLOYS=$($KUBECTL get deploy -n "$NS" \
        -l "llm-d.ai/role=decode" \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
    for _DEPLOY in $_DEPLOYS; do
        _replicas=$($KUBECTL get deploy "$_DEPLOY" -n "$NS" \
            -o jsonpath='{.status.replicas}' 2>/dev/null || echo 0)
        _ready=$($KUBECTL get deploy "$_DEPLOY" -n "$NS" \
            -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
        _entry="{\"workload\":\"${WORKLOAD}\",\"timestamp\":${_TS},\"deployment\":\"${_DEPLOY}\",\"replicas\":${_replicas:-0},\"ready_replicas\":${_ready:-0}}"
        _info "  $_entry"
        if [ -n "$_OUT_FILE" ]; then
            echo "$_entry" >> "$_OUT_FILE"
        fi
    done
    exit 0
fi

# Use bench-meta.json for the deployment list
python3 - "$_META_FILE" "$NS" "$WORKLOAD" "$_TS" "$_OUT_FILE" "$KUBECTL" <<'PYEOF'
import json, subprocess, sys, os

meta_path   = sys.argv[1]
ns          = sys.argv[2]
workload    = sys.argv[3]
ts          = int(sys.argv[4])
out_file    = sys.argv[5]
kubectl     = sys.argv[6]

meta   = json.load(open(meta_path))
stacks = meta.get("stacks", [])

entries = []
for s in stacks:
    deploy = s.get("deployment", "")
    if not deploy:
        continue

    try:
        replicas = int(subprocess.check_output(
            [kubectl, "get", "deploy", deploy, "-n", ns,
             "-o", "jsonpath={.status.replicas}"],
            stderr=subprocess.DEVNULL
        ).decode().strip() or "0")
    except Exception:
        replicas = 0

    try:
        ready = int(subprocess.check_output(
            [kubectl, "get", "deploy", deploy, "-n", ns,
             "-o", "jsonpath={.status.readyReplicas}"],
            stderr=subprocess.DEVNULL
        ).decode().strip() or "0")
    except Exception:
        ready = 0

    entry = {
        "workload":      workload,
        "timestamp":     ts,
        "stack":         s.get("name", deploy),
        "deployment":    deploy,
        "replicas":      replicas,
        "ready_replicas": ready,
        "so_paused":     s.get("so_paused", False),
    }
    line = json.dumps(entry)
    print(f"snapshot_replicas[{workload}]:   {line}")
    entries.append(line)

if out_file:
    os.makedirs(os.path.dirname(out_file), exist_ok=True)
    with open(out_file, "a") as f:
        for line in entries:
            f.write(line + "\n")
    print(f"snapshot_replicas[{workload}]: wrote {len(entries)} entry/entries to {out_file}")
PYEOF

exit 0
