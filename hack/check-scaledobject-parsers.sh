#!/usr/bin/env bash
#
# Executes the model-identification path in deploy/lib/scaledobject.sh against
# fixture command lines and pod specs.
#
# Sibling of check-workload-gaps.sh, and it exists for the same reason: these
# parsers decide which model every ScaledObject is written for, every defect
# they have shipped parses cleanly, and each was found on a cluster one install
# at a time -- `python` answered as the model of an SGLang server, a CRLF that
# matched no metric series, a --served-model-name read as the literal
# "$MODEL_NAME". None is a syntax error and all of them silently point WVA at a
# model no metric series reports.
#
# WRITING A CASE HERE: an expected-EMPTY case proves nothing on its own -- a
# function that does not exist returns empty too. The guard below refuses to run
# unless both functions are defined, and EXPECT_CASES refuses to pass a run that
# ended early; between them an empty answer means the function decided on it.
#
# The last group is not a string test. It lifts the discovery jq query out of
# the library and runs it against a pod fixture, because the record that query
# builds is pipe-delimited and newline-framed, and the two ways to break that
# framing -- a field holding the delimiter, a field holding a newline -- are
# invisible from the shell functions alone. Lifted rather than copied, so it
# cannot pass against a stale duplicate of the query.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# jq is not optional: the record group below cannot run without it, and a group
# that skips is how a green run comes to mean nothing.
command -v jq >/dev/null 2>&1 || {
    printf 'FATAL: jq is required to run these checks.\n' >&2
    exit 1
}

# Sourced through a CR-stripped copy, for the reason check-workload-gaps.sh
# gives: a Windows checkout with core.autocrlf makes bash stop at the first
# function definition, which looks exactly like every function being missing.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh scaledobject.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
# common.sh requires these; without them the first log_warning dies under set -u.
BLUE='' GREEN='' YELLOW='' RED='' NC=''
# shellcheck source=/dev/null
source "$WORK/common.sh"
# shellcheck source=/dev/null
source "$WORK/scaledobject.sh"

for fn in so_model_id so_resolve_env_ref; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=49
RAN=0
FAILED=0

# check <name> <want> <got>
check() {
    if [ "$2" = "$3" ]; then
        RAN=$((RAN + 1))
        printf 'ok    %s\n' "$1"
    else
        FAILED=$((FAILED + 1))
        printf 'FAIL  %s\n        want [%s]\n        got  [%s]\n' "$1" "$2" "$3" >&2
    fi
}

# ----------------------------------------------------------------------------
# so_model_id: which token on a command line is the model
# ----------------------------------------------------------------------------
check 'so_model_id reads --served-model-name <v>' \
    'Qwen/Qwen3-0.6B' \
    "$(so_model_id 'vllm serve /model-cache/x --served-model-name Qwen/Qwen3-0.6B --port 8000')"

check 'so_model_id reads --served-model-name=<v>' \
    'Qwen/Qwen3-0.6B' \
    "$(so_model_id 'vllm serve --served-model-name=Qwen/Qwen3-0.6B')"

check 'so_model_id prefers the served name over --model' \
    'Qwen/Qwen3-0.6B' \
    "$(so_model_id '--model /model-cache/models/Qwen/Qwen3-0.6B --served-model-name Qwen/Qwen3-0.6B')"

check 'so_model_id falls back to --model' \
    '/model-cache/models/Qwen' \
    "$(so_model_id 'vllm serve --model /model-cache/models/Qwen --port 8000')"

# The llm-d flagship-guide shape: command ["vllm","serve"], model first in args.
check 'so_model_id reads the positional model after serve' \
    'Qwen/Qwen3-32B' \
    "$(so_model_id 'vllm serve Qwen/Qwen3-32B --tensor-parallel-size=2')"

# No `serve` to anchor on. Empty is the answer; the bare "first non-flag token"
# fallback that used to live here returned `python`.
check 'so_model_id answers nothing for an sglang launcher' \
    '' \
    "$(so_model_id 'python -m sglang.launch_server --model-path /model-cache/x')"

check 'so_model_id answers nothing for an empty command line' \
    '' \
    "$(so_model_id '')"

# ----------------------------------------------------------------------------
# so_resolve_env_ref: a model name that is a variable reference
# ----------------------------------------------------------------------------
ENVS='MODEL_PATH=/model-cache/qwen BASE=Qwen/Qwen3 MODEL_NAME=Qwen/Qwen3-0.6B'

check 'so_resolve_env_ref passes a real model name through' \
    'Qwen/Qwen3-0.6B' \
    "$(so_resolve_env_ref 'Qwen/Qwen3-0.6B' "$ENVS")"

# The shell-script spelling: customCommand runs under bash inside the container.
check 'so_resolve_env_ref answers $VARNAME' \
    'Qwen/Qwen3-0.6B' \
    "$(so_resolve_env_ref '$MODEL_NAME' "$ENVS")"

check 'so_resolve_env_ref answers ${VARNAME}' \
    'Qwen/Qwen3-0.6B' \
    "$(so_resolve_env_ref '${MODEL_NAME}' "$ENVS")"

# The Kubernetes spelling: expanded by the kubelet, so the pod spec keeps the
# literal. config/scenarios/cicd/kind.yaml and examples/sim.yaml both use it.
check 'so_resolve_env_ref answers $(VARNAME)' \
    'Qwen/Qwen3-0.6B' \
    "$(so_resolve_env_ref '$(MODEL_NAME)' "$ENVS")"

check 'so_resolve_env_ref drops an unset reference rather than echoing it raw' \
    '' \
    "$(so_resolve_env_ref '$NOT_SET_HERE' "$ENVS")"

# Compound tokens resolve to nothing on purpose: half a name is a modelID that
# looks right and matches no metric series.
check 'so_resolve_env_ref drops ${VAR}suffix' \
    '' \
    "$(so_resolve_env_ref '${BASE}-instruct' "$ENVS")"

check 'so_resolve_env_ref drops $(VAR)suffix' \
    '' \
    "$(so_resolve_env_ref '$(MODEL_NAME)x' "$ENVS")"

check 'so_resolve_env_ref drops an unclosed brace' \
    '' \
    "$(so_resolve_env_ref '${MODEL_NAME' "$ENVS")"

check 'so_resolve_env_ref drops a bare $' \
    '' \
    "$(so_resolve_env_ref '$' "$ENVS")"

# printf, not echo: echo would swallow this as an option and answer empty.
check 'so_resolve_env_ref keeps a token that begins with a dash' \
    '-n' \
    "$(so_resolve_env_ref '-n' "$ENVS")"

check 'so_resolve_env_ref answers nothing with no env list at all' \
    '' \
    "$(so_resolve_env_ref '$MODEL_NAME' '')"

# ----------------------------------------------------------------------------
# The discovery record: one workload per LINE, fields split on a pipe
# ----------------------------------------------------------------------------
# The program is the last one closed by the `] | join("|"))` that ends the
# record. Extraction failing is a FAILURE, not a skip.
prog="$(awk '
    /jq -r --argjson p/            { buf = ""; collecting = 1; next }
    collecting && /join\("\|"\)\)/ { buf = buf $0; print buf; exit }
    collecting                     { buf = buf $0 "\n" }
' "$WORK/scaledobject.sh")"
case "$prog" in
    *"')") prog="${prog%??}" ;;   # the shell quote and paren closing the call
    *)     prog="" ;;
esac

if [ -z "$prog" ]; then
    FAILED=$((FAILED + 1))
    printf 'FAIL  could not lift the discovery jq program out of the library\n' >&2
else
    # BLOCK is what a YAML block scalar (`value: |`) writes: a value with a
    # newline in it. Left in the record it splits this one workload across two
    # lines -- the real entry losing the args field that trails it, and with it
    # the model it is being read for, plus a phantom entry named after the tail.
    # SPACED would split the env list itself, PIPED the record fields. The proxy
    # container is first on purpose: the engine is container 1.
    fixture="$WORK/pods.json"
    cat > "$fixture" <<'JSON'
{"items":[{"metadata":{"name":"decode","labels":{}},"spec":{"template":{"metadata":{"labels":{"app":"d"}},"spec":{"containers":[
  {"name":"proxy","image":"routing-proxy","args":[]},
  {"name":"vllm","image":"vllm/vllm-openai",
   "env":[{"name":"BLOCK","value":"/a\nb"},
          {"name":"SPACED","value":"a b"},
          {"name":"PIPED","value":"a|b"},
          {"name":"FROM_SECRET"},
          {"name":"MODEL_NAME","value":"Qwen/Qwen3-0.6B"}],
   "command":["vllm","serve"],
   "args":["--served-model-name","$(MODEL_NAME)"]}]}}}}]}
JSON
    out="$(jq -r --argjson p '["spec","template"]' "$prog" "$fixture" 2>&1)"

    check 'discovery emits one line per workload' \
        '1' \
        "$(printf '%s\n' "$out" | wc -l | tr -d ' ')"

    IFS='|' read -r r_name r_labels _r_objlabels r_envs r_args <<<"$out"
    check 'discovery names the workload' 'decode' "$r_name"
    check 'discovery reads the pod labels' 'app=d' "$r_labels"
    check 'discovery keeps only single-token env values' \
        'MODEL_NAME=Qwen/Qwen3-0.6B' "$r_envs"
    check 'discovery reads args off the ENGINE container' \
        'vllm serve --served-model-name $(MODEL_NAME)' "$r_args"

    # What the whole chain is for.
    check 'discovery resolves to a usable modelID' \
        'Qwen/Qwen3-0.6B' \
        "$(so_resolve_env_ref "$(so_model_id "$r_args")" "$r_envs")"
fi

# ----------------------------------------------------------------------------
# so_pause_target_gpus: GPUs one replica of a scale target holds
# ----------------------------------------------------------------------------
# The function reads the target with kubectl; a stub serves the fixture. The
# LWS cases pin the controller's arithmetic (GetTotalGPUsPerReplica): the leader
# runs the workerTemplate when there is no leaderTemplate.
declare -F so_pause_target_gpus >/dev/null || {
    printf 'FATAL: so_pause_target_gpus is not defined after sourcing the library.
' >&2
    exit 1
}
SO_FIXTURE=''
kubectl() { printf '%s' "$SO_FIXTURE"; }
gpus_of() { SO_FIXTURE="$1"; so_pause_target_gpus ns kind name; }
c() { printf '{"name":"c","resources":{"requests":{"%s":"%s"}}}' "$1" "$2"; }

check 'pause GPUs: a Deployment sums its containers' '3'     "$(gpus_of "{\"spec\":{\"template\":{\"spec\":{\"containers\":[$(c nvidia.com/gpu 2),$(c nvidia.com/gpu 1)]}}}}")"
check 'pause GPUs: an LWS without a leaderTemplate runs the worker as leader' '8'     "$(gpus_of "{\"spec\":{\"leaderWorkerTemplate\":{\"size\":2,\"workerTemplate\":{\"spec\":{\"containers\":[$(c amd.com/gpu 4)]}}}}}")"
check 'pause GPUs: an LWS with a leaderTemplate counts it once' '10'     "$(gpus_of "{\"spec\":{\"leaderWorkerTemplate\":{\"size\":3,\"leaderTemplate\":{\"spec\":{\"containers\":[$(c nvidia.com/gpu 2)]}},\"workerTemplate\":{\"spec\":{\"containers\":[$(c nvidia.com/gpu 4)]}}}}}")"
check 'pause GPUs: an LWS of size 1 without a leaderTemplate is one worker' '4'     "$(gpus_of "{\"spec\":{\"leaderWorkerTemplate\":{\"workerTemplate\":{\"spec\":{\"containers\":[$(c nvidia.com/gpu 4)]}}}}}")"
check 'pause GPUs: no request reads as one GPU, as the controller defaults' '1'     "$(gpus_of '{"spec":{"template":{"spec":{"containers":[{"name":"c"}]}}}}')"
unset -f kubectl

# ----------------------------------------------------------------------------

# ----------------------------------------------------------------------------
# so_apply_plan / so_repoint_stale: which scaler an address names, and what an
# adopt keeps
# ----------------------------------------------------------------------------
# Executed against a stubbed kubectl that records every patch and apply. Two
# failures this covers, both found by review and neither visible to `bash -n`:
#
#   - In the out-of-reach shape (controller in its own namespace, managing a
#     tenant one) WVA_NS is the TENANT namespace when NAMESPACE is set, and every
#     trigger was written as wva-external-scaler.<tenant>, where nothing runs.
#     KEDA falls back to CPU and the install looks healthy.
#   - `apply: adopt` merge-patches the triggers LIST, which replaces it, so every
#     re-run stripped the keys set by hand on the trigger (defaultOutputTokens,
#     warmPool, ...), and repointed objects already working against a live WVA.
#
# Cases are asserted on what reached the API server -- the recorded patch and
# apply -- never on the library source.
command -v yq >/dev/null 2>&1 || {
    printf 'FATAL: yq is required to run the plan checks (so_plan_rows reads the plan with it).\n' >&2
    exit 1
}

SOW="$WORK/so"
mkdir -p "$SOW"

# STUB_SVC          "ns=present ns2=forbidden" -- any other namespace is NotFound
# STUB_RB           the RoleBinding list served for every namespace
# STUB_SOS          the ScaledObject list served for every namespace and -A
# STUB_SO_FORBIDDEN 1: a GET of one named ScaledObject is Forbidden
so_stub() {
    printf '%s\n' "$*" >> "$SOW/kubectl.log"
    local a ns="" prev=""
    local empty='{"items":[]}'
    for a in "$@"; do
        [ "$prev" = "-n" ] && ns="$a"
        prev="$a"
    done
    case "$1 $2" in
        "get svc")
            case " ${STUB_SVC:-} " in
                *" $ns=present "*) echo "service/wva-external-scaler"; return 0 ;;
                *" $ns=forbidden "*)
                    echo "Error from server (Forbidden): services \"wva-external-scaler\" is forbidden: User \"tenant\" cannot get resource \"services\" in API group \"\" in the namespace \"$ns\"" >&2
                    return 1 ;;
            esac
            echo 'Error from server (NotFound): services "wva-external-scaler" not found' >&2
            return 1 ;;
        "get rolebinding")
            printf '%s\n' "${STUB_RB:-$empty}" ;;
        "get scaledobject")
            case "$3" in
                -A|-n) printf '%s\n' "${STUB_SOS:-$empty}" ;;
                *)
                    if [ -n "${STUB_SO_FORBIDDEN:-}" ]; then
                        echo 'Error from server (Forbidden): scaledobjects.keda.sh is forbidden' >&2
                        return 1
                    fi
                    printf '%s' "${STUB_SOS:-$empty}" \
                        | jq -ce --arg n "$3" '.items[] | select(.metadata.name == $n)' || {
                        echo "Error from server (NotFound): scaledobjects.keda.sh \"$3\" not found" >&2
                        return 1
                    } ;;
            esac ;;
        "patch scaledobject")
            local p="" next=0
            for a in "$@"; do
                [ "$next" = 1 ] && { p="$a"; next=0; }
                [ "$a" = "-p" ] && next=1
            done
            printf '%s\n' "$p" >> "$SOW/patched" ;;
        "apply -f")
            cat >> "$SOW/applied" ;;
        *) return 0 ;;
    esac
}

# run_so <function> [VAR=value...] runs it in a subshell against the stub and
# echoes its verdict: REFUSED when it refused, else OK. WARN is appended when it
# warned. What it wrote is left in $SOW/patched and $SOW/applied.
run_so() {
    local fn="$1"
    shift
    : > "$SOW/kubectl.log"; : > "$SOW/patched"; : > "$SOW/applied"; : > "$SOW/out"
    (
        log_info() { :; }
        log_success() { :; }
        log_warning() { printf '%s\n' "$1" >> "$SOW/warned"; }
        log_error() { printf 'REFUSED\n' >> "$SOW/out"; printf '%s\n' "$1" > "$SOW/refusal"; exit 1; }
        kubectl() { so_stub "$@"; }
        : > "$SOW/warned"
        WVA_NS=tenant WVA_NS_EXPLICIT='' WVA_DEFAULT_SO_PLAN='' WVA_DEFAULT_SO_NS=''
        STUB_SVC='' STUB_RB='' STUB_SOS='' STUB_SO_FORBIDDEN=''
        local kv
        for kv in "$@"; do eval "$kv"; done
        "$fn" "$SOW/plan.yaml" >/dev/null 2>&1
    )
    local v
    v="$(head -1 "$SOW/out")"
    [ -n "$v" ] || v=OK
    [ -s "$SOW/warned" ] && v="$v WARN"
    printf '%s' "$v"
}

# The address a run wrote: from the created object, else the patch.
written_addr() {
    { grep -h 'scalerAddress:' "$SOW/applied" 2>/dev/null | sed -E 's/.*scalerAddress:[[:space:]]*"?([^"]*)"?.*/\1/'
      jq -r '.spec.triggers[0].metadata.scalerAddress // empty' "$SOW/patched" 2>/dev/null
    } | head -1
}

plan_entry() {   # plan_entry <apply>
    cat > "$SOW/plan.yaml" <<YAML
plan:
  - apply: "$1"
    namespace: tenant
    kind: Deployment
    name: decode
    modelID: "Qwen/Qwen3-0.6B"
    minReplicas: 1
    maxReplicas: 4
    variantCost: "10.0"
YAML
}

RB_CTL='{"items":[{"metadata":{"name":"wva-manager-wva-ctl"},"subjects":[{"kind":"ServiceAccount","name":"wva-controller-manager","namespace":"wva-ctl"}]}]}'
# so_adopted <scaler namespace> -- an adopted object carrying two hand-set keys.
so_adopted() {
    printf '{"items":[{"metadata":{"name":"decode-so","namespace":"tenant"},"spec":{"scaleTargetRef":{"name":"decode"},"minReplicaCount":1,"maxReplicaCount":4,"triggers":[{"type":"external-push","name":"wva-external-scaler","metadata":{"scalerAddress":"wva-external-scaler.%s.svc.cluster.local:9090","modelID":"Qwen/Qwen3-0.6B","variantCost":"10.0","defaultOutputTokens":"512","warmPool":"pool-a"}}]}}]}' "$1"
}

# --- which scaler a created object names
plan_entry yes
check 'apply: out of reach, the scaler comes from the RoleBinding the installer wrote' \
    'OK|wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "$(run_so so_apply_plan STUB_SVC='wva-ctl=present' "STUB_RB='$RB_CTL'")|$(written_addr)"

check 'apply: no scaler in WVA_NS and nothing names one -- refused, nothing written' \
    'REFUSED|' \
    "$(run_so so_apply_plan)|$(written_addr)"

check 'apply: the refusal names the command that fixes it' \
    'yes' \
    "$(grep -q 'NAMESPACE=tenant make scaledobjects-apply WVA_NS=<controller-namespace> WVA_WATCH_NS=tenant' "$SOW/refusal" && echo yes || echo no)"

# Following that command must scan the TENANT namespace. With WVA_NS given and
# no WVA_WATCH_NS, so_target_namespaces scanned the controller's own, empty one.
tns() { ( unset WVA_WATCH_NS WVA_DEFAULT_SO_NS; for kv in "$@"; do eval "$kv"; done; so_target_namespaces 2>/dev/null ); }
check 'plan: NAMESPACE differing from an explicit WVA_NS is the namespace scanned'     'tenant' "$(tns WVA_SCOPE=namespace WVA_NS=wva-ctl NAMESPACE_EXPLICIT=tenant)"
check 'plan: an explicit WVA_WATCH_NS still wins'     'other' "$(tns WVA_SCOPE=namespace WVA_NS=wva-ctl NAMESPACE_EXPLICIT=tenant WVA_WATCH_NS=other)"
check 'plan: no NAMESPACE scans WVA_NS, as before'     'wva-ctl' "$(tns WVA_SCOPE=namespace WVA_NS=wva-ctl NAMESPACE_EXPLICIT=)"

check 'apply: an explicit WVA_NS the tenant may not read is used, with a warning' \
    'OK WARN|wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "$(run_so so_apply_plan WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl STUB_SVC='wva-ctl=forbidden')|$(written_addr)"

check 'apply: an explicit WVA_NS with no scaler Service is refused' \
    'REFUSED|' \
    "$(run_so so_apply_plan WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl)|$(written_addr)"

check 'apply: two live installs named in the namespace -- refused, not guessed' \
    'REFUSED|' \
    "$(run_so so_apply_plan "STUB_SVC='wva-ctl=present other=present'" "STUB_RB='$RB_CTL'" "STUB_SOS='$(so_adopted other | sed 's/decode/elsewhere/g')'")|$(written_addr)"

# --- what an adopt keeps
plan_entry adopt
check 'adopt: keeps the hand-set trigger keys (defaultOutputTokens, warmPool)' \
    'OK|512|pool-a|Qwen/Qwen3-0.6B' \
    "$(run_so so_apply_plan STUB_SVC='wva-ctl=present' "STUB_SOS='$(so_adopted wva-ctl)'")|$(jq -r '.spec.triggers[0].metadata | [.defaultOutputTokens, .warmPool, .modelID] | join("|")' "$SOW/patched" 2>/dev/null)"

check 'adopt: out of reach, re-applying keeps the address the object already works on' \
    'wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "$(run_so so_apply_plan STUB_SVC='wva-ctl=present' "STUB_SOS='$(so_adopted wva-ctl)'" >/dev/null; written_addr)"

check 'adopt: an object on another LIVE install is not moved without WVA_NS' \
    'wva-external-scaler.other.svc.cluster.local:9090' \
    "$(run_so so_apply_plan WVA_NS=wva-ctl "STUB_SVC='wva-ctl=present other=present'" "STUB_SOS='$(so_adopted other)'" >/dev/null; written_addr)"

check 'adopt: an explicit WVA_NS does move it' \
    'wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "$(run_so so_apply_plan WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl "STUB_SVC='wva-ctl=present other=present'" "STUB_SOS='$(so_adopted other)'" >/dev/null; written_addr)"

check 'adopt: an object that cannot be read is not patched over' \
    '0' \
    "$(run_so so_apply_plan STUB_SVC='wva-ctl=present' "STUB_SOS='$(so_adopted wva-ctl)'" STUB_SO_FORBIDDEN=1 >/dev/null; grep -c . "$SOW/patched")"

# --- what the plan says about an adopted object
STUB_SOS="$(so_adopted wva-ctl)"
kubectl() { so_stub "$@"; }
IFS=$'\037' read -r _n _min _max _cost _pol _wva e_model e_kept e_addr <<< "$(so_existing_info tenant decode)"
check 'plan: so_existing_info reads the modelID, kept keys and address back' \
    'Qwen/Qwen3-0.6B|defaultOutputTokens,warmPool|wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "${e_model:-}|${e_kept:-}|${e_addr:-}"
unset -f kubectl
STUB_SOS=''

kept_note() {
    local note='' existing_kept="$1" existing_name=decode-so
    so_kept_keys_note 2>/dev/null
    printf '%s' "$note"
}
check 'plan: the note names the keys an adopt keeps' \
    'yes' \
    "$(case "$(kept_note 'defaultOutputTokens,warmPool')" in *'defaultOutputTokens, warmPool'*) echo yes ;; *) echo no ;; esac)"

prefer() {   # prefer <discovered> <existing> [SO_PREFER_EXISTING_MODEL]
    local model="$1" existing_model="$2" existing_name=decode-so note=''
    SO_PREFER_EXISTING_MODEL="${3:-true}" so_prefer_existing_model tenant decode 2>/dev/null
    printf '%s' "$model"
}
check 'plan: a re-discovered modelID does not replace the one the object carries' \
    'Qwen/Qwen3-0.6B' "$(prefer /models/qwen Qwen/Qwen3-0.6B)"
check 'plan: verify-scaledobjects still sees what the container serves' \
    '/models/qwen' "$(prefer /models/qwen Qwen/Qwen3-0.6B false)"

# --- repoint: Forbidden is not dead
REPOINT_SOS='{"items":[{"metadata":{"name":"decode-so","namespace":"tenant"},"spec":{"triggers":[{"type":"external-push","metadata":{"scalerAddress":"wva-external-scaler.gone.svc.cluster.local:9090","modelID":"m"}}]}}]}'
check 'repoint: an address in a namespace that cannot be read is left alone' \
    '0' \
    "$(run_so so_repoint_stale WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl "STUB_SVC='wva-ctl=present gone=forbidden'" "STUB_SOS='$REPOINT_SOS'" >/dev/null; grep -c . "$SOW/patched")"
check 'repoint: an explicit WVA_NS the tenant may not read is accepted, and a dead address repointed' \
    'OK WARN|wva-external-scaler.wva-ctl.svc.cluster.local:9090' \
    "$(run_so so_repoint_stale WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl STUB_SVC='wva-ctl=forbidden' "STUB_SOS='$REPOINT_SOS'")|$(written_addr)"

if [ "$FAILED" -gt 0 ]; then
    printf '\n%d model-identification assertion(s) failed.\n' "$FAILED" >&2
    exit 1
fi
# Same guard as check-workload-gaps.sh: a run that ends early otherwise exits 0
# having checked less than it claims.
if [ "$RAN" -ne "$EXPECT_CASES" ]; then
    printf '\n%d case(s) reported ok, %d expected -- the run did not finish.\n' \
        "$RAN" "$EXPECT_CASES" >&2
    exit 1
fi
printf '\nModel identification checks passed.\n'
