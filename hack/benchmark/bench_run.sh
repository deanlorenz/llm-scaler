#!/usr/bin/env bash
# bench_run.sh — session orchestrator for bench-* targets.
#
# Single entry point for running benchmark workloads against a live stack.
# Reads a bench-session.yaml, discovers the stack via bench_init.sh, then
# drives one or more workloads through the harness pod.
#
# Usage:
#   bench_run.sh <session-file> [workload[,workload,...]]
#
# Arguments:
#   <session-file>    Path to bench-session.yaml (required)
#   [workload]        Comma-separated workload names to run. Default: all
#                     workloads listed in the session file's workloads: block.
#
# Flags:
#   --foreground      Run in the foreground (default: background)
#   --run-dir=<path>  Resume into an existing run dir (internal use)
#
# Environment:
#   DRY_RUN                Set to "true" to validate + print without touching
#                          the cluster. Reads and checks proceed; no pod
#                          creation, no harness execution, no results copy.
#   NAMESPACE              Hint for namespace when session file has __default__.
#   BENCH_WORKLOAD         Comma-separated workload names (same as $2).
#   KUBECTL_CMD            kubectl binary (default: kubectl).
#   BENCH_HARNESS_POD_NAME Harness pod name (default: llmdbench-harness).
#   BENCH_SKIP_WVA_SCRAPE  Set to "true" to skip WVA metrics scraping.
#   BENCH_SKIP_PROMETHEUS  Set to "true" to skip post-scenario Prometheus query.
#   BENCH_SKIP_IGW_LOGS    Set to "true" to skip IGW log collection.
#   BENCH_INTER_SCENARIO_HOOK  Script called between workloads:
#                              bash <hook> <workload> <namespace> <run-dir>
#
# Background mode (default):
#   Creates the run dir, forks itself with --foreground, and returns
#   immediately. Prints the log path and pod log command for tracking.
#
# Outputs (all under one run directory):
#   hack/benchmark/bench-scratch/<session-name>-<timestamp>/
#     bench-run.log             full output of the background run
#     bench-run.pid             pid of the background process
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
#   0  all workloads completed successfully (foreground) or launched (background)
#   1  session-level failure or guided stop (missing/unresolvable fields)
set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_DRY_RUN="${DRY_RUN:-false}"
_FOREGROUND=false
_RUN_DIR_ARG=""

# Parse flags
_pos_args=()
for _arg in "$@"; do
    case "$_arg" in
        --foreground)       _FOREGROUND=true ;;
        --run-dir=*)        _RUN_DIR_ARG="${_arg#*=}" ;;
        -h|--help)
            sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
            exit 0
            ;;
        *)                  _pos_args+=("$_arg") ;;
    esac
done
set -- "${_pos_args[@]+"${_pos_args[@]}"}"

SESSION_FILE="${1:?usage: $0 <session-file> [workload[,workload,...]]}"
WORKLOAD_ARG="${2:-${BENCH_WORKLOAD:-}}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

_info()  { echo "bench_run: $*"; }
_warn()  { echo "bench_run: WARNING: $*" >&2; }
_error() { echo "bench_run: ERROR: $*" >&2; exit 1; }
_dry()   { echo "bench_run: [DRY_RUN] $*"; }

[ -f "$SESSION_FILE" ] || _error "session file not found: $SESSION_FILE"
SESSION_FILE="$(realpath "$SESSION_FILE")"

# ---------------------------------------------------------------------------
# Parse + resolve session file
# ---------------------------------------------------------------------------
_resolve_session=$(python3 - "$SESSION_FILE" "${NAMESPACE:-}" <<'PYEOF'
import sys, re, os

session_path = sys.argv[1]
ns_hint      = sys.argv[2]

SENTINEL = "__default__"

with open(session_path) as f:
    raw = f.read()

def extract_scalar(text, key):
    m = re.search(r'^\s+' + re.escape(key) + r'\s*:\s*(.*)$', text, re.MULTILINE)
    if not m:
        m = re.search(r'^' + re.escape(key) + r'\s*:\s*(.*)$', text, re.MULTILINE)
    if not m:
        return None
    v = m.group(1).strip().strip('"').strip("'")
    return v if v else SENTINEL

def write_back(path, key, value):
    with open(path) as f:
        content = f.read()
    new_content = re.sub(
        r'^(\s*)(' + re.escape(key) + r')(\s*:\s*).*$',
        lambda m: f'{m.group(1)}{m.group(2)}{m.group(3)}{value}',
        content, count=1, flags=re.MULTILINE
    )
    with open(path, 'w') as f:
        f.write(new_content)

kubeconfig = extract_scalar(raw, 'kubeconfig')
if kubeconfig is None:
    print('ERROR:missing kubeconfig field in session file'); sys.exit(1)
if kubeconfig == SENTINEL:
    kubeconfig = os.environ.get('KUBECONFIG', '') or os.path.expanduser('~/.kube/config')
    write_back(session_path, 'kubeconfig', kubeconfig)
    print(f'WROTE_BACK:kubeconfig={kubeconfig}', file=sys.stderr)

kube_ctx = extract_scalar(raw, 'kube_context')
if kube_ctx is None:
    print('ERROR:missing kube_context field in session file'); sys.exit(1)
if kube_ctx == SENTINEL:
    import subprocess
    r = subprocess.run(
        ['kubectl', '--kubeconfig', kubeconfig, 'config', 'current-context'],
        capture_output=True, text=True)
    if r.returncode != 0 or not r.stdout.strip():
        print('ERROR:kube_context is __default__ but no active context found in kubeconfig'); sys.exit(1)
    kube_ctx = r.stdout.strip()
    write_back(session_path, 'kube_context', kube_ctx)
    print(f'WROTE_BACK:kube_context={kube_ctx}', file=sys.stderr)

ns = extract_scalar(raw, 'namespace')
if ns is None:
    print('ERROR:missing namespace field in session file'); sys.exit(1)
if ns == SENTINEL:
    if ns_hint:
        ns = ns_hint
    else:
        m = re.match(r'^([^/]+)/[^/]+/[^/]+$', kube_ctx)
        ns = m.group(1) if m else ''
    if ns:
        write_back(session_path, 'namespace', ns)
        print(f'WROTE_BACK:namespace={ns}', file=sys.stderr)
    else:
        print('ERROR:namespace is __default__ and could not be resolved'); sys.exit(1)

workloads = []
in_workloads = False
for line in raw.splitlines():
    stripped = line.rstrip()
    if re.match(r'^workloads\s*:', stripped):
        in_workloads = True
        continue
    if in_workloads:
        if stripped and not stripped.startswith(' ') and not stripped.startswith('\t') \
                and not stripped.startswith('-') and not stripped.startswith('#'):
            break
        m = re.match(r'^\s*-\s+(\S+)\s*$', stripped)
        if m:
            val = m.group(1).strip('"').strip("'")
            if not val.startswith('name:'):
                workloads.append(val)
        m2 = re.match(r'^\s*-\s+name:\s*(\S+)', stripped)
        if m2:
            workloads.append(m2.group(1).strip('"').strip("'"))

cluster = extract_scalar(raw, 'cluster') or ''

print(f'NS={ns}')
print(f'KUBE_CTX={kube_ctx}')
print(f'KUBECONFIG={kubeconfig}')
print(f'CLUSTER={cluster}')
print(f'WORKLOADS={",".join(workloads)}')
PYEOF
)

if echo "$_resolve_session" | grep -q '^ERROR:'; then
    _msg=$(echo "$_resolve_session" | grep '^ERROR:' | sed 's/^ERROR://')
    _error "session file error: $_msg"
fi

eval "$(echo "$_resolve_session" | grep -E '^(NS|KUBE_CTX|KUBECONFIG|CLUSTER|WORKLOADS)=')"
[ -n "${KUBECONFIG:-}" ] && export KUBECONFIG

# ---------------------------------------------------------------------------
# Context guard
# ---------------------------------------------------------------------------
live_ctx=$($KUBECTL config current-context 2>/dev/null || true)
if [ "$live_ctx" != "$KUBE_CTX" ]; then
    _error "context mismatch
  session expects: $KUBE_CTX
  live context:    ${live_ctx:-<none>}
  Edit $SESSION_FILE or switch context."
fi

_info "target: namespace=$NS cluster=${CLUSTER:-?} context=$KUBE_CTX"
[ "$_DRY_RUN" = "true" ] && _dry "DRY_RUN=true — cluster writes will be skipped."

# ---------------------------------------------------------------------------
# Namespace exists guard
# ---------------------------------------------------------------------------
if ! $KUBECTL get namespace "$NS" >/dev/null 2>&1; then
    _error "namespace '$NS' not found on cluster. Check namespace in $SESSION_FILE."
fi

# ---------------------------------------------------------------------------
# Resolve workload list
# ---------------------------------------------------------------------------
if [ -n "$WORKLOAD_ARG" ]; then
    IFS=',' read -ra _requested <<< "$WORKLOAD_ARG"
else
    IFS=',' read -ra _requested <<< "$WORKLOADS"
fi

if [ "${#_requested[@]}" -eq 0 ]; then
    _error "no workloads to run. Add workloads: entries to $SESSION_FILE or set BENCH_WORKLOAD=<name>"
fi

BENCH_WORKLOADS_DIR="$_SCRIPT_DIR/bench-workloads"
for _wl in "${_requested[@]}"; do
    _wf="$BENCH_WORKLOADS_DIR/${_wl}.yaml"
    [ -f "$_wf" ] || _error "workload '$_wl' not found: $_wf
  Available: $(ls "$BENCH_WORKLOADS_DIR"/*.yaml 2>/dev/null | xargs -n1 basename | sed 's/\.yaml//' | tr '\n' ' ')"
done

# ---------------------------------------------------------------------------
# Create run directory (or reuse if --run-dir= passed by background fork)
# ---------------------------------------------------------------------------
if [ -n "$_RUN_DIR_ARG" ]; then
    RUN_DIR="$_RUN_DIR_ARG"
else
    SESSION_NAME="$(basename "$SESSION_FILE" .yaml)"
    RUN_TS="$(date +%Y%m%d-%H%M%S)"
    RUN_DIR="$_SCRIPT_DIR/bench-scratch/${SESSION_NAME}-${RUN_TS}"
    mkdir -p "$RUN_DIR"
fi

# ---------------------------------------------------------------------------
# Background mode: fork self with --foreground and return immediately
# ---------------------------------------------------------------------------
if [ "$_FOREGROUND" = "false" ] && [ "$_DRY_RUN" != "true" ]; then
    LOG="$RUN_DIR/bench-run.log"
    POD="${BENCH_HARNESS_POD_NAME:-llmdbench-harness}"

    # Re-assemble workload arg for the child
    _wl_arg="${WORKLOAD_ARG:-$(IFS=','; echo "${_requested[*]}")}"

    nohup bash "$0" --foreground --run-dir="$RUN_DIR" \
        "$SESSION_FILE" "$_wl_arg" \
        > "$LOG" 2>&1 &
    _bg_pid=$!
    echo "$_bg_pid" > "$RUN_DIR/bench-run.pid"

    echo ""
    _info "==========================================="
    _info "Run started in background  (pid=$_bg_pid)"
    _info "Run dir:  $RUN_DIR"
    _info "Log:      $LOG"
    _info ""
    _info "Track progress:"
    _info "  tail -f $LOG"
    _info ""
    _info "Pod logs (harness stdout):"
    _info "  $KUBECTL logs -f -n $NS $POD"
    _info ""
    _info "Pod status:"
    _info "  $KUBECTL get pod $POD -n $NS"
    _info "==========================================="
    echo ""
    exit 0
fi

# ---------------------------------------------------------------------------
# FOREGROUND execution from here
# ---------------------------------------------------------------------------
_info "Run dir: $RUN_DIR"

# ---------------------------------------------------------------------------
# Run bench_init
# ---------------------------------------------------------------------------
_info "Running bench_init for namespace '$NS'..."
BENCH_NAMESPACE="$NS" \
BENCH_KUBECONFIG="${KUBECONFIG:-}" \
BENCH_KUBE_CONTEXT="$KUBE_CTX" \
bash "$_SCRIPT_DIR/bench_init.sh" "$NS" "$RUN_DIR"

META_FILE="$RUN_DIR/bench-meta.json"
[ -f "$META_FILE" ] || _error "bench_init did not produce $META_FILE"

# ---------------------------------------------------------------------------
# Pre-run preflight
# ---------------------------------------------------------------------------
_info "Running preflight checks..."
KUBECTL_CMD="$KUBECTL" bash "$_SCRIPT_DIR/bench_preflight.sh" "$META_FILE" || true

# ---------------------------------------------------------------------------
# Guided stop: sentinels remaining after write-back
# ---------------------------------------------------------------------------
_needs_review=false
python3 -c "
import sys
raw = open('$SESSION_FILE').read()
sys.exit(1 if '__default__' in raw else 0)
" 2>/dev/null || _needs_review=true

if [ "$_needs_review" = "true" ]; then
    echo ""
    _warn "Session file still contains __default__ values. Review before re-running."
    echo ""
    echo "=== $SESSION_FILE ==="
    cat "$SESSION_FILE"
    echo ""
    echo "=== Cluster discovery (bench-meta.json) ==="
    python3 -c "import json,sys; print(json.dumps(json.load(open(sys.argv[1])), indent=2))" "$META_FILE"
    echo ""
    _info "Edit $SESSION_FILE, then run: make bench-run BENCH_SESSION=$SESSION_FILE"
    exit 1
fi

# ---------------------------------------------------------------------------
# Read stack info from bench-meta.json
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
# Materialize session file into the run dir
# ---------------------------------------------------------------------------
_info "Materializing session file..."
python3 - "$SESSION_FILE" "$META_FILE" "$RUN_DIR/bench-session.yaml" <<'PYEOF'
import sys, re, json, datetime

session_path, meta_path, out_path = sys.argv[1:4]
meta = json.load(open(meta_path))
stack = meta.get('stacks', [{}])[0]
prom = meta.get('prometheus', {}).get('url', '')
wva_svc = meta.get('wva', {}).get('metrics_service', '')

auto_values = {
    'model_name':                    stack.get('model_id', ''),
    'model':                         stack.get('model_id', ''),
    'base_url':                      stack.get('endpoint_url', ''),
    'target':                        stack.get('endpoint_url', ''),
    'pretrained_model_name_or_path': stack.get('model_id', ''),
    'endpoint_url':                  stack.get('endpoint_url', ''),
    'model_id':                      stack.get('model_id', ''),
}

with open(session_path) as f:
    lines = f.readlines()

out_lines = []
for line in lines:
    m = re.match(r'^(\s*)(\w+)(\s*):(\s*)#\s*auto\s*$', line.rstrip('\n'))
    if m:
        indent, key, colon_space, _ = m.group(1), m.group(2), m.group(3), m.group(4)
        if key in auto_values and auto_values[key]:
            out_lines.append(f'{indent}{key}{colon_space}: {auto_values[key]}  # auto\n')
        else:
            out_lines.append(line)
    else:
        out_lines.append(line)

content = ''.join(out_lines)
if 'target_env:' not in content:
    now = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    content += f"""
target_env:  # written by bench_run.sh — do not edit by hand
  model_id: {stack.get('model_id', '')}
  endpoint_url: {stack.get('endpoint_url', '')}
  epp_metrics_secret: {stack.get('epp_metrics_secret', '')}
  wva_metrics_service: {wva_svc}
  prometheus_url: {prom}
  verified_at: {now}
"""

with open(out_path, 'w') as f:
    f.write(content)
PYEOF

# ---------------------------------------------------------------------------
# DRY_RUN: print plan and stop
# ---------------------------------------------------------------------------
if [ "$_DRY_RUN" = "true" ]; then
    _dry "Would ensure harness pod in namespace $NS"
    for _wl in "${_requested[@]}"; do
        _dry "Would run workload: $_wl (harness: $(python3 -c "
import re, sys
raw = open('$BENCH_WORKLOADS_DIR/$_wl.yaml').read()
m = re.search(r'^\s*harness\s*:\s*(\S+)', raw, re.MULTILINE)
print(m.group(1) if m else 'guidellm')
" 2>/dev/null))"
    done
    _dry "Materialized session: $RUN_DIR/bench-session.yaml"
    _dry "Stack meta: $RUN_DIR/bench-meta.json"
    echo ""
    echo "=== Cluster discovery (bench-meta.json) ==="
    python3 -c "import json,sys; print(json.dumps(json.load(open(sys.argv[1])), indent=2))" "$META_FILE"
    exit 0
fi

# ---------------------------------------------------------------------------
# Ensure harness pod — and kill any stale processes from a previous run
# ---------------------------------------------------------------------------
_info "Ensuring harness pod..."
BENCH_EPP_METRICS_SECRET="$META_EPP_SECRET" \
WVA_METRICS_SERVICE="$META_WVA_SVC" \
bash "$_SCRIPT_DIR/run_session.sh" ensure "$NS"

_info "Clearing any stale harness processes from previous runs..."
POD="${BENCH_HARNESS_POD_NAME:-llmdbench-harness}"
$KUBECTL exec "$POD" -n "$NS" -- bash -c "
    nohup bash -c '
        pkill -9 -f llm-d-benchmark.sh 2>/dev/null || true
        pkill -9 -f inference-perf 2>/dev/null || true
        pkill -9 -f guidellm 2>/dev/null || true
    ' >/dev/null 2>&1 &
    sleep 2
    echo cleared
" 2>/dev/null || true

# ---------------------------------------------------------------------------
# Run workloads
# ---------------------------------------------------------------------------
_HOOK="${BENCH_INTER_SCENARIO_HOOK:-}"
_FAILED=""

for _wl in "${_requested[@]}"; do
    _info "-------------------------------------------"
    _info "Workload: $_wl  ($(date '+%H:%M:%S'))"
    _info "-------------------------------------------"
    _wf="$BENCH_WORKLOADS_DIR/${_wl}.yaml"

    _rendered_profile=$(mktemp --suffix=".yaml")
    python3 - "$_wf" "$META_FILE" "$_rendered_profile" <<'PYEOF'
import sys, re, json

wl_path, meta_path, out_path = sys.argv[1:4]
meta = json.load(open(meta_path))
stack = meta.get('stacks', [{}])[0]

auto_values = {
    'model_name':                    stack.get('model_id', ''),
    'model':                         stack.get('model_id', ''),
    'base_url':                      stack.get('endpoint_url', ''),
    'target':                        stack.get('endpoint_url', ''),
    'pretrained_model_name_or_path': stack.get('model_id', ''),
    'endpoint_url':                  stack.get('endpoint_url', ''),
    'model_id':                      stack.get('model_id', ''),
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
        indent, key, colon_space, _ = m.group(1), m.group(2), m.group(3), m.group(4)
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
_info "==========================================="
_info "Session complete  ($(date '+%H:%M:%S'))"
_info "Results: $RUN_DIR"
_info "Record:  $RUN_DIR/bench-session.yaml"
if [ -n "$_FAILED" ]; then
    _warn "Failed workloads:$_FAILED"
    _info "==========================================="
    exit 1
fi
_info "==========================================="
exit 0
