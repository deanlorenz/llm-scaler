#!/usr/bin/env bash
# Apply the standup's own settings to a benchmark scenario COPY.
#
#   scenario_overrides.sh <scenario.yaml>
#
# Everything here is a value a reader would otherwise have had to hand-edit into
# a tracked scenario file, and every one of them is read from the environment:
#
#   BENCHMARK_GATEWAY_SKIP_INSTALL=true
#       Reuse the gateway control plane the cluster already runs. Sets
#       skipInstall and providerNamespace (BENCHMARK_GATEWAY_PROVIDER_NAMESPACE,
#       default istio-system) on every gateway block whose className is istio.
#       Without it, a standup on a cluster that already runs Istio installs
#       istio-base over it and fails on ownership metadata, part-way through.
#   BENCHMARK_DECODE_CPU / BENCHMARK_DECODE_MEMORY
#       A decode Pod's CPU and memory request (and limit, where the scenario
#       sets one), for nodes whose spare CPU is smaller than the scenario asks.
#   BENCHMARK_DECODE_NODE=<node>
#       Every engine role with its own resources pinned to that node
#       (nodeSelector kubernetes.io/hostname).
#   BENCHMARK_RENDER_DRAIN=true (the default)
#       The drain fields -- a preStop sleep of WVA_DRAIN_SLEEP_SECONDS (45) and
#       terminationGracePeriodSeconds of WVA_DRAIN_GRACE_SECONDS (120) -- on every
#       engine role, so the engines start once already draining. The standup used
#       to deploy them without, then patch and re-roll every model server: each
#       model started twice, the second time cold. The values are the ones
#       `make workload-patch` writes, so the pods end identical; the patch then
#       finds nothing to do and rolls nothing.
#
# Edits with yq, as the other scenario edits of the standup do, so the file
# keeps its comments. Fails when a setting it was asked for matches nothing: a key that
# moved must not become a setting that silently did nothing.
set -euo pipefail
log_error() { printf 'scenario_overrides: %s\n' "$1" >&2; exit 1; }
SCENARIO="${1:?usage: scenario_overrides.sh <scenario.yaml>}"
[ -f "$SCENARIO" ] || log_error "no scenario at ${SCENARIO}"
command -v yq >/dev/null 2>&1 || log_error "yq is required (the standup edits the scenario with it)"

# The engine roles: every decode or prefill map that runs vLLM and is not
# switched off. In the shipped scenarios that is the shared block; the
# per-stack entries carry replicas and resources only, and inherit it.
ROLES='.. | select(type == "!!map" and (has("decode") or has("prefill"))) | (.decode, .prefill) | select(type == "!!map" and has("vllm") and .enabled != false)'

if [ "${BENCHMARK_GATEWAY_SKIP_INSTALL:-false}" = "true" ]; then
    export GW_NS="${BENCHMARK_GATEWAY_PROVIDER_NAMESPACE:-istio-system}"
    case "$GW_NS" in *[!a-z0-9-]*|"") log_error "not a namespace: '${GW_NS}'" ;; esac
    n="$(yq -r '[.. | select(type == "!!map" and has("gateway")) | .gateway | select(type == "!!map" and .className == "istio")] | length' "$SCENARIO")"
    [ "$n" -gt 0 ] || log_error "BENCHMARK_GATEWAY_SKIP_INSTALL=true, but ${SCENARIO} has no istio gateway block to reuse"
    yq -i '(.. | select(type == "!!map" and has("gateway")) | .gateway | select(type == "!!map" and .className == "istio")) |= (.skipInstall = true | .providerNamespace = strenv(GW_NS))' "$SCENARIO"
    echo "gateway: ${n} block(s) reuse the control plane in ${GW_NS} (skipInstall)"
fi

decode_resource() {  # decode_resource <cpu|memory> <value>
    export RES_KEY="$1" RES_VAL="$2"
    case "$RES_VAL" in *[!0-9A-Za-z.]*|"") log_error "not a quantity for ${RES_KEY}: '${RES_VAL}'" ;; esac
    local n
    n="$(yq -r '[.. | select(type == "!!map" and has("decode")) | .decode | select(type == "!!map" and has("resources")) | .resources.requests | select(type == "!!map" and has(strenv(RES_KEY)))] | length' "$SCENARIO")"
    [ "$n" -gt 0 ] || log_error "no decode resources.requests.${RES_KEY} in ${SCENARIO} to set"
    yq -i '(.. | select(type == "!!map" and has("decode")) | .decode | select(type == "!!map" and has("resources")) | .resources.requests | select(type == "!!map" and has(strenv(RES_KEY))) | .[strenv(RES_KEY)]) = strenv(RES_VAL)' "$SCENARIO"
    yq -i '(.. | select(type == "!!map" and has("decode")) | .decode | select(type == "!!map" and has("resources")) | .resources.limits | select(type == "!!map" and has(strenv(RES_KEY))) | .[strenv(RES_KEY)]) = strenv(RES_VAL)' "$SCENARIO"
    echo "decode ${RES_KEY}: ${RES_VAL} on ${n} decode role(s)"
}
[ -z "${BENCHMARK_DECODE_CPU:-}" ] || decode_resource cpu "$BENCHMARK_DECODE_CPU"
[ -z "${BENCHMARK_DECODE_MEMORY:-}" ] || decode_resource memory "$BENCHMARK_DECODE_MEMORY"

# BENCHMARK_DECODE_NODE=<node>: every engine role that sizes its own Pods (a
# decode or prefill map with resources -- the per-stack entries) is pinned to
# that node by kubernetes.io/hostname. A mixed-size run needs its small model's
# donors on the large model's node, and a shared cluster's scheduler spreads them.
if [ -n "${BENCHMARK_DECODE_NODE:-}" ]; then
    export DECODE_NODE="$BENCHMARK_DECODE_NODE"
    case "$DECODE_NODE" in *[!a-z0-9.-]*) log_error "not a node name: '${DECODE_NODE}'" ;; esac
    PINNED='.. | select(type == "!!map" and (has("decode") or has("prefill"))) | (.decode, .prefill) | select(type == "!!map" and has("resources"))'
    n="$(yq -r "[${PINNED}] | length" "$SCENARIO")"
    [ "$n" -gt 0 ] || log_error "BENCHMARK_DECODE_NODE=${DECODE_NODE}, but ${SCENARIO} has no engine role with resources to pin"
    yq -i "(${PINNED} | .nodeSelector.\"kubernetes.io/hostname\") = strenv(DECODE_NODE)" "$SCENARIO"
    echo "node: ${n} engine role(s) pinned to ${DECODE_NODE}"
fi

if [ "${BENCHMARK_RENDER_DRAIN:-true}" = "true" ]; then
    export DRAIN_GRACE="${WVA_DRAIN_GRACE_SECONDS:-120}" DRAIN_CMD="sleep ${WVA_DRAIN_SLEEP_SECONDS:-45}"
    case "$DRAIN_GRACE" in *[!0-9]*|"") log_error "WVA_DRAIN_GRACE_SECONDS is not a whole number of seconds: '${DRAIN_GRACE}'" ;; esac
    case "${WVA_DRAIN_SLEEP_SECONDS:-45}" in *[!0-9]*|"") log_error "WVA_DRAIN_SLEEP_SECONDS is not a whole number of seconds" ;; esac
    n="$(yq -r "[${ROLES}] | length" "$SCENARIO")"
    if [ "$n" -eq 0 ]; then
        echo "drain: no engine role in the scenario to render it on; the standup's workload patch adds it after the engines start"
    else
        # A hook the scenario already declares is kept, and a longer grace too.
        yq -i "(${ROLES} | .extraContainerConfig.lifecycle.preStop) |= (. // {\"exec\": {\"command\": [\"/bin/sh\", \"-c\", strenv(DRAIN_CMD)]}})" "$SCENARIO"
        yq -i "(${ROLES} | select((.terminationGracePeriodSeconds // 0) < (strenv(DRAIN_GRACE) | tonumber)) | .terminationGracePeriodSeconds) = (strenv(DRAIN_GRACE) | tonumber)" "$SCENARIO"
        echo "drain: preStop and a ${DRAIN_GRACE}s grace on ${n} engine role(s), before the engines first start"
    fi
fi
