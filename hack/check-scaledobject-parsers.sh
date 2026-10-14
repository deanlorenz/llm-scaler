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

EXPECT_CASES=71
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
# The KEDA fallback on generated and adopted ScaledObjects
# ----------------------------------------------------------------------------
# Read back through yq, not grepped: the block is spliced into a heredoc, and
# the failure that matters is one that still prints the words but at the wrong
# indent -- a fallback under advanced, or an advanced block that lost its key.
command -v yq >/dev/null 2>&1 || {
    printf 'FATAL: yq is required for the fallback group.\n' >&2
    exit 1
}
for fn in render_default_scaledobject render_scaledobject so_adopt_patch so_adopt_needs_fallback \
          so_fallback_check so_fallback_failures so_keda_fallback_probe so_fallback_note so_show_plan; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done
# Every case below runs against a KEDA that has fallback.behavior unless it says
# otherwise. Set, not probed: the probe would call the real kubectl.
SO_KEDA_FALLBACK_BEHAVIOR=yes
render_with() {   # render_with <min> -- with whatever WVA_SO_FALLBACK* is set
    render_default_scaledobject ns Deployment decode Qwen/Qwen3-0.6B addr:9090 "$1" 8 '' ''
}
fb_of() { yq -o=json '.spec.fallback' | jq -r 'if . == null then "none" else [.failureThreshold, .replicas, .behavior] | map(tostring) | join(",") end'; }

check 'fallback: a generated object gets one, replicas at its minReplicaCount' \
    '3,2,currentReplicasIfHigher' "$(render_with 2 | fb_of)"
check 'fallback: the rest of the spec survives the splice' \
    '2,8,true,300' \
    "$(render_with 2 | yq -o=json '.spec' | jq -r '[.minReplicaCount, .maxReplicaCount, .advanced.restoreToOriginalReplicaCount, .advanced.horizontalPodAutoscalerConfig.behavior.scaleDown.stabilizationWindowSeconds] | map(tostring) | join(",")')"
# A floor of 0 gets NO fallback. KEDA sends a trigger error on an object with
# fallback.replicas 0 and minReplicaCount 0 to scaleToZeroOrIdle, so a zero
# fallback parks a serving model on any error window.
check 'fallback: a floor of zero gets none' \
    'none' "$(render_with 0 | fb_of)"
check 'fallback: a floor written 00 is still zero' \
    'none' "$(render_with 00 | fb_of)"
check 'fallback: a floor of zero still renders a valid object' \
    '0,8,true' \
    "$(render_with 0 | yq -o=json '.spec' | jq -r '[.minReplicaCount, .maxReplicaCount, .advanced.restoreToOriginalReplicaCount] | map(tostring) | join(",")')"
check 'fallback: WVA_SO_FALLBACK_FAILURES sets failureThreshold' \
    '5,1,currentReplicasIfHigher' "$(WVA_SO_FALLBACK_FAILURES=5 render_with 1 | fb_of)"
check 'fallback: WVA_SO_FALLBACK=false leaves it out' \
    'none' "$(WVA_SO_FALLBACK=false render_with 1 | fb_of)"
check 'fallback: WVA_SO_FALLBACK=false still renders the advanced block' \
    'true' "$(WVA_SO_FALLBACK=false render_with 1 | yq -r '.spec.advanced.restoreToOriginalReplicaCount')"
check 'fallback: WVA_SO_FALLBACK must be true or false' \
    'refused' "$( (WVA_SO_FALLBACK=yes so_fallback_check) 2>/dev/null && echo accepted || echo refused)"
check 'fallback: a failureThreshold of 0 is refused' \
    'refused' "$( (WVA_SO_FALLBACK_FAILURES=0 so_fallback_failures) > /dev/null 2>&1 && echo accepted || echo refused)"
check 'fallback: a failureThreshold with a leading zero is refused' \
    'refused' "$( (WVA_SO_FALLBACK_FAILURES=010 so_fallback_failures) > /dev/null 2>&1 && echo accepted || echo refused)"
check 'fallback: a failureThreshold of int32 max is accepted' \
    '2147483647' "$(WVA_SO_FALLBACK_FAILURES=2147483647 so_fallback_failures 2>/dev/null)"
check 'fallback: a failureThreshold above int32 is refused' \
    'refused' "$( (WVA_SO_FALLBACK_FAILURES=2147483648 so_fallback_failures) > /dev/null 2>&1 && echo accepted || echo refused)"
check 'fallback: a failureThreshold of eleven digits is refused' \
    'refused' "$( (WVA_SO_FALLBACK_FAILURES=99999999999 so_fallback_failures) > /dev/null 2>&1 && echo accepted || echo refused)"

# KEDA before 2.17 has no fallback.behavior: its CRD prunes the field and the
# fallback that is left is static at minReplicaCount. None at all instead.
check 'fallback: KEDA < 2.17 gets none' \
    'none' "$(SO_KEDA_FALLBACK_BEHAVIOR=no render_with 2 | fb_of)"
check 'fallback: an unreadable KEDA CRD gets none' \
    'none' "$(SO_KEDA_FALLBACK_BEHAVIOR=unknown render_with 2 | fb_of)"

# The probe, against a stub kubectl. Each in a subshell with the cache cleared,
# so the answer is the probe and not the value set above.
CRD_FIXTURE=''
CRD_FIXTURE_RC=0
kubectl() { printf '%s' "$CRD_FIXTURE"; return "$CRD_FIXTURE_RC"; }
probe() { ( unset SO_KEDA_FALLBACK_BEHAVIOR; so_keda_fallback_probe 2>/dev/null; printf '%s' "$SO_KEDA_FALLBACK_BEHAVIOR" ); }
CRD_FIXTURE='{"spec":{"versions":[{"name":"v1alpha1","schema":{"openAPIV3Schema":{"properties":{"spec":{"properties":{"fallback":{"properties":{"failureThreshold":{},"replicas":{},"behavior":{"enum":["static","currentReplicas"]}}}}}}}}}]}}'
check 'probe: a CRD with fallback.behavior is KEDA 2.17 or later' 'yes' "$(probe)"
CRD_FIXTURE='{"spec":{"versions":[{"name":"v1alpha1","schema":{"openAPIV3Schema":{"properties":{"spec":{"properties":{"fallback":{"properties":{"failureThreshold":{},"replicas":{}}}}}}}}}]}}'
check 'probe: a CRD without it is older' 'no' "$(probe)"
check 'probe: an older CRD says so in a warning' \
    'warned' "$( (unset SO_KEDA_FALLBACK_BEHAVIOR; so_keda_fallback_probe 2>&1 >/dev/null) | grep -q 'KEDA 2.17' && echo warned || echo silent)"
CRD_FIXTURE=''
CRD_FIXTURE_RC=1
check 'probe: a CRD that cannot be read is unknown, not new' 'unknown' "$(probe)"
CRD_FIXTURE_RC=0
CRD_FIXTURE='not json'
check 'probe: a CRD that does not parse is unknown' 'unknown' "$(probe)"
unset -f kubectl

# What the plan table and the apply lines say about each entry.
check 'note: a generated entry with a floor gets one' '(+fallback)' "$(so_fallback_note yes 2)"
check 'note: a floor of zero says why it gets none' '(no fallback: floor 0)' "$(so_fallback_note yes 0)"
check 'note: an old KEDA says why it gets none' '(no fallback: KEDA < 2.17)' "$(SO_KEDA_FALLBACK_BEHAVIOR=no so_fallback_note yes 2)"
check 'note: an adopted entry keeps a fallback it has' '(+fallback unless it has one)' "$(so_fallback_note adopt 2)"
check 'note: an entry not applied says nothing' '-' "$(so_fallback_note no 2)"
check 'note: a template decides its own' '(fallback: as the template says)' "$(WVA_DEFAULT_SO_TEMPLATE=/x so_fallback_note yes 2)"
PLAN_FIXTURE="$WORK/plan.yaml"
printf '%s\n' 'plan:' \
    '  - {apply: yes, namespace: ns, kind: Deployment, name: up, modelID: m, minReplicas: 2, maxReplicas: 4}' \
    '  - {apply: yes, namespace: ns, kind: Deployment, name: parked, modelID: m, minReplicas: 0, maxReplicas: 4}' \
    > "$PLAN_FIXTURE"
check 'plan table: a FALLBACK column per entry' \
    'up:(+fallback)|parked:(no fallback: floor 0)' \
    "$(so_show_plan "$PLAN_FIXTURE" 2>/dev/null | awk '$4 == "up" || $4 == "parked" { print $4 ":" substr($0, index($0, "(")) }' | sed 's/ *$//' | paste -sd'|' -)"

# Adoption: added only to an object with none, never merged into one it has.
check 'adopt: add_fallback=yes adds the default block' \
    '3,2,currentReplicasIfHigher' \
    "$(so_adopt_patch m addr 2 8 10.0 '' yes | jq -r '.spec.fallback | [.failureThreshold, .replicas, .behavior] | map(tostring) | join(",")')"
check 'adopt: without add_fallback the patch has no fallback key' \
    'false' "$(so_adopt_patch m addr 2 8 10.0 '' '' | jq -r '.spec | has("fallback")')"
SO_FIXTURE=''
SO_FIXTURE_RC=0
kubectl() { printf '%s' "$SO_FIXTURE"; return "$SO_FIXTURE_RC"; }
check 'adopt: an object with no fallback gets one' \
    'yes' "$(so_adopt_needs_fallback ns so 2)"
check 'adopt: an object with a floor of zero gets none' \
    '' "$(so_adopt_needs_fallback ns so 0)"
check 'adopt: an old KEDA adds none' \
    '' "$(SO_KEDA_FALLBACK_BEHAVIOR=no so_adopt_needs_fallback ns so 2)"
SO_FIXTURE='{"failureThreshold":9,"replicas":4}'
check 'adopt: an object with a fallback keeps it' \
    '' "$(so_adopt_needs_fallback ns so 2)"
SO_FIXTURE='{"failureThreshold":3,"replicas":0}'
check 'adopt: a kept zero fallback on a floor of zero is warned about' \
    'warned' "$(so_adopt_needs_fallback ns so 0 2>&1 >/dev/null | grep -q 'replicas is 0 with minReplicaCount 0' && echo warned || echo silent)"
check 'adopt: a kept zero fallback above a floor is not' \
    'silent' "$(so_adopt_needs_fallback ns so 2 2>&1 >/dev/null | grep -q 'replicas is 0' && echo warned || echo silent)"
SO_FIXTURE=''
SO_FIXTURE_RC=1
check 'adopt: an object that cannot be read is not one without a fallback' \
    '' "$(so_adopt_needs_fallback ns so 2 2>/dev/null)"
SO_FIXTURE_RC=0
check 'adopt: WVA_SO_FALLBACK=false adds none' \
    '' "$(WVA_SO_FALLBACK=false so_adopt_needs_fallback ns so 2)"
unset -f kubectl

# A template owns its fallback, except the one shape KEDA scales to zero on.
TMPL_FIXTURE="$WORK/zero-fallback-template.yaml"
printf '%s\n' 'apiVersion: keda.sh/v1alpha1' 'kind: ScaledObject' 'metadata:' '  name: {{NAME}}-wva' \
    'spec:' '  minReplicaCount: {{MIN}}' '  fallback:' '    failureThreshold: 3' '    replicas: {{MIN}}' > "$TMPL_FIXTURE"
check 'template: fallback.replicas 0 on a floor of 0 is refused' \
    'refused' "$( (WVA_DEFAULT_SO_TEMPLATE="$TMPL_FIXTURE" render_scaledobject ns Deployment d m addr 0 4) > /dev/null 2>&1 && echo accepted || echo refused)"
check 'template: and prints no manifest for kubectl to apply' \
    '' "$( (WVA_DEFAULT_SO_TEMPLATE="$TMPL_FIXTURE" render_scaledobject ns Deployment d m addr 0 4) 2>/dev/null)"
check 'template: the same template above a floor renders' \
    '2' "$(WVA_DEFAULT_SO_TEMPLATE="$TMPL_FIXTURE" render_scaledobject ns Deployment d m addr 2 4 2>/dev/null | yq -r '.spec.fallback.replicas')"
check 'template: the shipped template renders a floor of 0 with no fallback' \
    'none,0' "$(WVA_DEFAULT_SO_TEMPLATE="$ROOT/config/samples/keda/external-scaler/scaledobject-template.yaml" render_scaledobject ns Deployment d m addr 0 4 2>/dev/null | yq -o=json '.spec' | jq -r '[(.fallback // "none"), .minReplicaCount] | map(tostring) | join(",")')"

# ----------------------------------------------------------------------------

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
