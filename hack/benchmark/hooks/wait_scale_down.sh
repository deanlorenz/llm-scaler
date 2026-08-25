#!/usr/bin/env bash
# hooks/wait_scale_down.sh — inter-scenario hook: wait for decode replicas to
# return to minReplicas before starting the next scenario.
#
# Use this between burst scenarios so each run starts from a clean baseline
# replica count rather than an already-scaled-up state.
#
# Called by bench-run-all with:
#   $1  workload name just completed  (e.g. burst_4k250)
#   $2  namespace                     (e.g. dhl-la-1708)
#   $3  session directory             (e.g. hack/benchmark/bench-scratch/dhl-la-1708-...)
#
# Environment:
#   SCALE_DOWN_TIMEOUT   Seconds to wait (default: 300)
#   BENCH_META_FILE      Path to bench-meta.json (default: auto-detect from session dir)
#   KUBECTL_CMD          kubectl binary (default: kubectl)
#
# Exit code is always 0 — bench-run-all ignores hook failures.
set -uo pipefail

WORKLOAD="${1:-<unknown>}"
NS="${2:?namespace required}"
SESSION_DIR="${3:-}"

KUBECTL="${KUBECTL_CMD:-kubectl}"
TIMEOUT="${SCALE_DOWN_TIMEOUT:-300}"

_info() { echo "wait_scale_down[$WORKLOAD]: $*"; }
_warn() { echo "wait_scale_down[$WORKLOAD]: WARNING: $*" >&2; }

# ---------------------------------------------------------------------------
# Resolve bench-meta.json to find the primary stack's deployment name
# ---------------------------------------------------------------------------
_META_FILE="${BENCH_META_FILE:-}"
if [ -z "$_META_FILE" ] && [ -n "$SESSION_DIR" ]; then
    # Session dir is <bench-scratch>/<ns>-<timestamp>/; meta lives one level up.
    _META_FILE="$(dirname "$SESSION_DIR")/bench-meta.json"
fi
if [ -z "$_META_FILE" ] || [ ! -f "$_META_FILE" ]; then
    # Fall back: look in the standard location.
    _SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    _META_FILE="${_SCRIPT_DIR}/../../bench-scratch/${NS}/bench-meta.json"
fi

_DEPLOY=""
_MIN_REPLICAS=1
if [ -f "$_META_FILE" ]; then
    _DEPLOY=$(python3 -c "
import json, sys
meta = json.load(open('$_META_FILE'))
stacks = meta.get('stacks', [])
# Skip paused stacks; use the first active one.
for s in stacks:
    if not s.get('so_paused', False):
        print(s.get('deployment', ''))
        break
" 2>/dev/null || true)
    _MIN_REPLICAS=$(python3 -c "
import json, sys
meta = json.load(open('$_META_FILE'))
for s in meta.get('stacks', []):
    if not s.get('so_paused', False):
        print(s.get('min_replicas', 1))
        break
" 2>/dev/null || echo 1)
fi

if [ -z "$_DEPLOY" ]; then
    _warn "Could not determine deployment from bench-meta.json; skipping scale-down wait."
    exit 0
fi

_info "Waiting for $NS/$_DEPLOY to reach ${_MIN_REPLICAS} ready replica(s) (timeout: ${TIMEOUT}s)..."

_deadline=$(( $(date +%s) + TIMEOUT ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    _ready=$($KUBECTL get deploy "$_DEPLOY" -n "$NS" \
        -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
    _current=$($KUBECTL get deploy "$_DEPLOY" -n "$NS" \
        -o jsonpath='{.status.replicas}' 2>/dev/null || echo "?")
    _info "  replicas: current=${_current} ready=${_ready:-0} target=${_MIN_REPLICAS}"

    if [ "${_ready:-0}" -le "$_MIN_REPLICAS" ] && [ "${_ready:-0}" -ge "$_MIN_REPLICAS" ]; then
        _info "Scale-down complete (ready=${_ready})."
        exit 0
    fi

    sleep 15
done

_warn "Timed out after ${TIMEOUT}s waiting for scale-down (last ready=${_ready:-?}). Continuing anyway."
exit 0
