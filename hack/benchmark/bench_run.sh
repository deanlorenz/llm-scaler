#!/usr/bin/env bash
# bench_run.sh — session orchestrator for bench-* targets.
#
# Reads a bench-session.yaml file, calls bench_init.sh to discover/verify
# the stack, then drives one or more workloads against it.
#
# Usage:
#   bench_run.sh <session-file> [workload[,workload,...]]
#
# Arguments:
#   <session-file>    Path to bench-session.yaml (required)
#   [workload]        Comma-separated workload names to run. Default: all
#                     workloads listed in the session file's workloads: block.
#                     Each name must exist in hack/benchmark/bench-workloads/
#                     or be an inline workload entry in the session file.
#
# Environment:
#   BENCH_WORKLOAD         Comma-separated workload names (same as $2)
#   KUBECTL_CMD            kubectl binary (default: kubectl)
#   BENCH_HARNESS_POD_NAME Harness pod name (default: llmdbench-harness)
#   BENCH_SKIP_WVA_SCRAPE  Set to "true" to skip WVA metrics scraping
#   BENCH_SKIP_PROMETHEUS  Set to "true" to skip post-scenario Prometheus query
#   BENCH_SKIP_IGW_LOGS    Set to "true" to skip IGW log collection
#   BENCH_INTER_SCENARIO_HOOK  Script called between workloads:
#                              bash <hook> <workload> <namespace> <run-dir>
#
# Outputs (all under one run directory):
#   hack/benchmark/bench-scratch/<session-name>-<timestamp>/
#     bench-meta.json           stack discovery snapshot (from bench_init.sh)
#     bench-session.yaml        materialized session file (reproducibility record)
#     <workload>/               per-workload results (from run_scenario.sh)
#       results/
#       wva-metrics/
#       logs/
#       scenario_meta.json
#       prometheus_range.json
#
# Exit codes:
#   0  all workloads completed (individual harness failures do NOT abort the session)
#   1  session-level failure (bad session file, init failure, pod failure)
set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --help
case "${1:-}" in
    -h|--help)
        sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
        exit 0
        ;;
esac

SESSION_FILE="${1:?usage: $0 <session-file> [workload[,workload,...]]}"
WORKLOAD_ARG="${2:-${BENCH_WORKLOAD:-}}"

KUBECTL="${KUBECTL_CMD:-kubectl}"

_info()  { echo "bench_run: $*"; }
_warn()  { echo "bench_run: WARNING: $*" >&2; }
_error() { echo "bench_run: ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Validate session file exists
# ---------------------------------------------------------------------------
[ -f "$SESSION_FILE" ] || _error "session file not found: $SESSION_FILE"

# ---------------------------------------------------------------------------
# Parse session file with python3
#
# The session file has a nested target_id: block:
#
#   target_id:
#     cluster: ...
#     namespace: ...
#     kube_context: ...
#     kubeconfig: ...
#
# extract_scalar uses \s* so it matches indented keys — but only within the
# target_id block to avoid false matches on same-named keys elsewhere.
# ---------------------------------------------------------------------------
_session_info=$(python3 - "$SESSION_FILE" <<'PYEOF'
import sys, re

path = sys.argv[1]

with open(path) as f:
    raw = f.read()

def extract_scalar(text, key):
    m = re.search(r'^\s+' + re.escape(key) + r'\s*:\s*(.+)$', text, re.MULTILINE)
    if not m:
        # Also accept top-level (no leading whitespace) for flat session files.
        m = re.search(r'^' + re.escape(key) + r'\s*:\s*(.+)$', text, re.MULTILINE)
    if not m:
        return ''
    v = m.group(1).strip().strip('"').strip("'")
    return v

ns          = extract_scalar(raw, 'namespace')
kube_ctx    = extract_scalar(raw, 'kube_context')
kubeconfig  = extract_scalar(raw, 'kubeconfig')
cluster     = extract_scalar(raw, 'cluster')

# Parse workloads block: each list item is either a bare string or a map with name:
workloads = []
in_workloads = False
for line in raw.splitlines():
    stripped = line.rstrip()
    if re.match(r'^workloads\s*:', stripped):
        in_workloads = True
        continue
    if in_workloads:
        # Stop at next top-level key (no leading whitespace, ends with colon)
        if stripped and not stripped.startswith(' ') and not stripped.startswith('\t') and not stripped.startswith('-') and not stripped.startswith('#'):
            break
        m = re.match(r'^\s*-\s+(\S+)\s*$', stripped)
        if m:
            val = m.group(1).strip('"').strip("'")
            if not val.startswith('name:'):
                workloads.append(val)
        m2 = re.match(r'^\s*-\s+name:\s*(\S+)', stripped)
        if m2:
            workloads.append(m2.group(1).strip('"').strip("'"))

if not ns:
    print('ERROR:missing namespace'); sys.exit(1)
if not kube_ctx:
    print('ERROR:missing kube_context'); sys.exit(1)

print(f'NS={ns}')
print(f'KUBE_CTX={kube_ctx}')
print(f'KUBECONFIG={kubeconfig}')
print(f'CLUSTER={cluster}')
print(f'WORKLOADS={",".join(workloads)}')
PYEOF
)

# Check for parse errors
if echo "$_session_info" | grep -q '^ERROR:'; then
    _msg=$(echo "$_session_info" | grep '^ERROR:' | sed 's/^ERROR://')
    _error "session file parse failed: $_msg  (file: $SESSION_FILE)"
fi

# Export parsed values
eval "$(echo "$_session_info" | grep -E '^(NS|KUBE_CTX|KUBECONFIG|CLUSTER|WORKLOADS)=')"

# ---------------------------------------------------------------------------
# Apply kubeconfig if specified
# ---------------------------------------------------------------------------
if [ -n "${KUBECONFIG:-}" ]; then
    export KUBECONFIG
fi

# ---------------------------------------------------------------------------
# Context guard
# ---------------------------------------------------------------------------
live_ctx=$($KUBECTL config current-context 2>/dev/null || true)
if [ "$live_ctx" != "$KUBE_CTX" ]; then
    _error "context mismatch
  expected: $KUBE_CTX
  live:     ${live_ctx:-<none>}
  Set KUBECONFIG or switch context before running bench-run."
fi

_info "target: namespace=$NS cluster=${CLUSTER:-?} context=$KUBE_CTX"

# ---------------------------------------------------------------------------
# Create timestamped run directory — ALL artifacts go here
# ---------------------------------------------------------------------------
SESSION_NAME="$(basename "$SESSION_FILE" .yaml)"
RUN_TS="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="$_SCRIPT_DIR/bench-scratch/${SESSION_NAME}-${RUN_TS}"
mkdir -p "$RUN_DIR"
_info "Run dir: $RUN_DIR"

# ---------------------------------------------------------------------------
# Run bench_init — writes bench-meta.json directly into the run dir
# ---------------------------------------------------------------------------
_info "Running bench_init for namespace '$NS'..."
BENCH_NAMESPACE="$NS" \
BENCH_KUBECONFIG="${KUBECONFIG:-}" \
BENCH_KUBE_CONTEXT="$KUBE_CTX" \
bash "$_SCRIPT_DIR/bench_init.sh" "$NS" "$RUN_DIR"

META_FILE="$RUN_DIR/bench-meta.json"
[ -f "$META_FILE" ] || _error "bench_init did not produce $META_FILE"

# ---------------------------------------------------------------------------
# Read target_env from bench-meta.json
# ---------------------------------------------------------------------------
_meta=$(python3 - "$META_FILE" <<'PYEOF'
import json, sys
m = json.load(open(sys.argv[1]))
s = m.get('stacks', [{}])[0]
prom = m.get('prometheus', {}).get('url', '')
wva_svc = m.get('wva', {}).get('metrics_service', '')
epp_secret = s.get('epp_metrics_secret', '')
model_id = s.get('model_id', '')
endpoint_url = s.get('endpoint_url', '')
print(f'META_MODEL_ID={model_id}')
print(f'META_ENDPOINT_URL={endpoint_url}')
print(f'META_EPP_SECRET={epp_secret}')
print(f'META_PROM_URL={prom}')
print(f'META_WVA_SVC={wva_svc}')
PYEOF
)
eval "$_meta"

_info "stack: model=$META_MODEL_ID endpoint=$META_ENDPOINT_URL"

# ---------------------------------------------------------------------------
# Resolve workload list
# ---------------------------------------------------------------------------
if [ -n "$WORKLOAD_ARG" ]; then
    IFS=',' read -ra _requested <<< "$WORKLOAD_ARG"
else
    IFS=',' read -ra _requested <<< "$WORKLOADS"
fi

if [ "${#_requested[@]}" -eq 0 ]; then
    _error "no workloads to run. Add workloads: entries to $SESSION_FILE or pass BENCH_WORKLOAD=<name>"
fi

# Validate all requested workloads exist before starting anything
BENCH_WORKLOADS_DIR="$_SCRIPT_DIR/bench-workloads"
for _wl in "${_requested[@]}"; do
    _wf="$BENCH_WORKLOADS_DIR/${_wl}.yaml"
    [ -f "$_wf" ] || _error "workload '$_wl' not found: $_wf
  Available: $(ls "$BENCH_WORKLOADS_DIR"/*.yaml 2>/dev/null | xargs -n1 basename | sed 's/\.yaml//' | tr '\n' ' ')"
done

# ---------------------------------------------------------------------------
# Materialize session file into the run dir
# ---------------------------------------------------------------------------
_info "Materializing session file..."
python3 - "$SESSION_FILE" "$META_FILE" "$RUN_DIR/bench-session.yaml" <<'PYEOF'
import sys, re, json

session_path, meta_path, out_path = sys.argv[1:4]
meta = json.load(open(meta_path))
stack = meta.get('stacks', [{}])[0]
prom = meta.get('prometheus', {}).get('url', '')
wva_svc = meta.get('wva', {}).get('metrics_service', '')

auto_values = {
    'model_name':                      stack.get('model_id', ''),
    'model':                           stack.get('model_id', ''),
    'base_url':                        stack.get('endpoint_url', ''),
    'target':                          stack.get('endpoint_url', ''),
    'pretrained_model_name_or_path':   stack.get('model_id', ''),
    'endpoint_url':                    stack.get('endpoint_url', ''),
    'model_id':                        stack.get('model_id', ''),
}

with open(session_path) as f:
    lines = f.readlines()

out_lines = []
for line in lines:
    m = re.match(r'^(\s*)(\w+)(\s*):(\s*)#\s*auto\s*$', line.rstrip('\n'))
    if m:
        indent, key, colon_space, val_space = m.group(1), m.group(2), m.group(3), m.group(4)
        if key in auto_values and auto_values[key]:
            out_lines.append(f'{indent}{key}{colon_space}: {auto_values[key]}  # auto\n')
        else:
            out_lines.append(line)
    else:
        out_lines.append(line)

content = ''.join(out_lines)
if 'target_env:' not in content:
    import datetime
    now = datetime.datetime.utcnow().strftime('%Y-%m-%dT%H:%M:%SZ')
    target_env = f"""
target_env:  # written by bench_run.sh — do not edit by hand
  model_id: {stack.get('model_id', '')}
  endpoint_url: {stack.get('endpoint_url', '')}
  epp_metrics_secret: {stack.get('epp_metrics_secret', '')}
  wva_metrics_service: {wva_svc}
  prometheus_url: {prom}
  verified_at: {now}
"""
    content += target_env

with open(out_path, 'w') as f:
    f.write(content)
print(f'bench_run: materialized session → {out_path}')
PYEOF

# ---------------------------------------------------------------------------
# Ensure harness pod is running
# ---------------------------------------------------------------------------
_info "Ensuring harness pod..."
BENCH_EPP_METRICS_SECRET="$META_EPP_SECRET" \
WVA_METRICS_SERVICE="$META_WVA_SVC" \
bash "$_SCRIPT_DIR/run_session.sh" ensure "$NS"

# ---------------------------------------------------------------------------
# Run workloads
# ---------------------------------------------------------------------------
_HOOK="${BENCH_INTER_SCENARIO_HOOK:-}"
_FAILED=""

for _wl in "${_requested[@]}"; do
    _info "Running workload: $_wl"
    _wf="$BENCH_WORKLOADS_DIR/${_wl}.yaml"

    # Render harness profile from bench-workload YAML: fill # auto fields.
    _rendered_profile=$(mktemp --suffix=".yaml")
    python3 - "$_wf" "$META_FILE" "$_rendered_profile" <<'PYEOF'
import sys, re, json

wl_path, meta_path, out_path = sys.argv[1:4]
meta = json.load(open(meta_path))
stack = meta.get('stacks', [{}])[0]

auto_values = {
    'model_name':                      stack.get('model_id', ''),
    'model':                           stack.get('model_id', ''),
    'base_url':                        stack.get('endpoint_url', ''),
    'target':                          stack.get('endpoint_url', ''),
    'pretrained_model_name_or_path':   stack.get('model_id', ''),
    'endpoint_url':                    stack.get('endpoint_url', ''),
    'model_id':                        stack.get('model_id', ''),
}

with open(wl_path) as f:
    lines = f.readlines()

out_lines = []
harness = 'guidellm'
for line in lines:
    m_harness = re.match(r'^\s*harness\s*:\s*(\S+)', line)
    if m_harness:
        harness = m_harness.group(1)
    m = re.match(r'^(\s*)(\w+)(\s*):(\s*)#\s*auto\s*$', line.rstrip('\n'))
    if m:
        indent, key, colon_space, val_space = m.group(1), m.group(2), m.group(3), m.group(4)
        if key in auto_values and auto_values[key]:
            out_lines.append(f'{indent}{key}{colon_space}: {auto_values[key]}\n')
        else:
            out_lines.append(line)
    else:
        out_lines.append(line)

with open(out_path, 'w') as f:
    f.writelines(out_lines)
with open(out_path + '.harness', 'w') as f:
    f.write(harness)
PYEOF

    _harness=$(cat "${_rendered_profile}.harness" 2>/dev/null || echo "guidellm")
    rm -f "${_rendered_profile}.harness"

    MODEL_ID="$META_MODEL_ID" \
    BENCH_HARNESS="$_harness" \
    BENCH_WORKLOAD="$_wl" \
    BENCH_ENDPOINT_URL="$META_ENDPOINT_URL" \
    BENCHMARK_PROMETHEUS_URL="$META_PROM_URL" \
    WVA_METRICS_SERVICE="$META_WVA_SVC" \
    bash "$_SCRIPT_DIR/run_scenario.sh" \
        "$_rendered_profile" \
        "$NS" \
        "$RUN_DIR" \
        --workload="$_wl" \
        --harness="$_harness" \
        --model-id="$META_MODEL_ID" \
        --endpoint-url="$META_ENDPOINT_URL" \
    || { _warn "workload '$_wl' failed (rc=$?); continuing..."; _FAILED="${_FAILED} $_wl"; }

    rm -f "$_rendered_profile"

    if [ -n "$_HOOK" ]; then
        _info "Running inter-scenario hook: $_HOOK"
        bash "$_HOOK" "$_wl" "$NS" "$RUN_DIR" || true
    fi
done

# ---------------------------------------------------------------------------
# Final report
# ---------------------------------------------------------------------------
echo ""
_info "Session complete. Results: $RUN_DIR"
_info "Reproducibility record: $RUN_DIR/bench-session.yaml"
if [ -n "$_FAILED" ]; then
    _warn "Failed workloads:$_FAILED"
    exit 1
fi
exit 0
