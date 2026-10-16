#!/usr/bin/env bash
#
# Executes hack/benchmark/scenario_overrides.sh against every shipped scenario.
#
# Each setting it applies replaces a step a reader used to take by hand -- an
# edit to a tracked scenario file to get past a cluster that already runs
# Istio, a decode request no knob could lower, a patch that re-rolled every
# model server right after it first started. Each one fails quietly when its
# key moves: the standup still runs, and the setting simply did not happen. So
# this runs the script on copies of the real scenarios and reads the result
# back, rather than trusting the expressions.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/hack/benchmark/scenario_overrides.sh"
SCEN="$ROOT/hack/benchmark/scenarios/guides"
FAIL=0
CASES=0

ok()   { CASES=$((CASES + 1)); echo "ok   $*"; }
fail() { CASES=$((CASES + 1)); echo "FAIL $*"; FAIL=$((FAIL + 1)); }

for tool in yq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FATAL: $tool is required by this check and is missing. Nothing below would be about the code."
        exit 2
    }
done
[ -f "$SCRIPT" ] || { echo "FAIL $SCRIPT is missing"; exit 1; }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

ROLES='.. | select(type == "!!map" and (has("decode") or has("prefill"))) | (.decode, .prefill) | select(type == "!!map" and has("vllm") and .enabled != false)'

# The two-model scenarios: the ones the two-model driver stands up.
for sc in "$SCEN"/two-model-*.yaml; do
    name="$(basename "$sc" .yaml)"

    # --- gateway reuse ------------------------------------------------------
    cp "$sc" "$T/g.yaml"
    if BENCHMARK_RENDER_DRAIN=false BENCHMARK_GATEWAY_SKIP_INSTALL=true BENCHMARK_GATEWAY_PROVIDER_NAMESPACE=mesh-system \
        bash "$SCRIPT" "$T/g.yaml" > "$T/g.out" 2>&1; then
        got="$(yq -r '[.. | select(type == "!!map" and has("gateway")) | .gateway | select(.className == "istio") | (.skipInstall | tostring) + "/" + .providerNamespace] | join(",")' "$T/g.yaml")"
        [ "$got" = "true/mesh-system" ] && ok "$name: gateway reuse sets skipInstall and the provider namespace" \
            || fail "$name: gateway reuse left the istio gateway as '$got', want true/mesh-system"
    else
        fail "$name: gateway reuse refused: $(cat "$T/g.out")"
    fi
    cp "$sc" "$T/g0.yaml"
    BENCHMARK_RENDER_DRAIN=false bash "$SCRIPT" "$T/g0.yaml" >/dev/null 2>&1
    if cmp -s "$sc" "$T/g0.yaml"; then ok "$name: with nothing asked, the scenario copy is untouched"
    else fail "$name: with every setting off, the script still changed the scenario"; fi

    # --- decode resources ---------------------------------------------------
    cp "$sc" "$T/r.yaml"
    if BENCHMARK_RENDER_DRAIN=false BENCHMARK_DECODE_CPU=4 BENCHMARK_DECODE_MEMORY=48Gi bash "$SCRIPT" "$T/r.yaml" > "$T/r.out" 2>&1; then
        bad="$(yq -r '[.. | select(type == "!!map" and has("decode")) | .decode | select(type == "!!map" and has("resources")) | .resources | (.requests.cpu, .limits.cpu, .requests.memory, .limits.memory) | select(. != null) | select(. != "4" and . != "48Gi")] | length' "$T/r.yaml")"
        n="$(yq -r '[.. | select(type == "!!map" and has("decode")) | .decode | select(type == "!!map" and has("resources"))] | length' "$T/r.yaml")"
        [ "$n" -gt 0 ] && [ "$bad" = 0 ] && ok "$name: DECODE_CPU/MEMORY reach the requests and limits of all $n decode role(s)" \
            || fail "$name: decode resources not all set ($n roles, $bad other values left)"
    else
        fail "$name: decode resources refused: $(cat "$T/r.out")"
    fi

    # --- drain fields -------------------------------------------------------
    cp "$sc" "$T/d.yaml"
    if bash "$SCRIPT" "$T/d.yaml" > "$T/d.out" 2>&1; then
        roles="$(yq -r "[${ROLES}] | length" "$T/d.yaml")"
        good="$(yq -r "[${ROLES} | select(.terminationGracePeriodSeconds == 120 and (.extraContainerConfig.lifecycle.preStop.exec.command | join(\" \")) == \"/bin/sh -c sleep 45\")] | length" "$T/d.yaml")"
        # Two empty extractions compare equal: the count must be a real one first.
        ports_before="$(yq -r "[${ROLES} | (.extraContainerConfig.ports // []) | length] | join(\",\")" "$sc")"
        ports_after="$(yq -r "[${ROLES} | (.extraContainerConfig.ports // []) | length] | join(\",\")" "$T/d.yaml")"
        case "$ports_before" in ""|*[!0-9,]*) ports_before="unreadable:$ports_before" ;; esac
        [ "$roles" -gt 0 ] && [ "$good" = "$roles" ] && [ "${ports_before#unreadable}" = "$ports_before" ] && [ "$ports_before" = "$ports_after" ] \
            && ok "$name: the drain hook and a 120s grace on all $roles engine role(s), container ports kept" \
            || fail "$name: drain on $good of $roles engine role(s); ports $ports_before -> $ports_after"
    else
        fail "$name: drain refused: $(cat "$T/d.out")"
    fi
done

# The engine roles a two-model P/D scenario runs: prefill as well as decode.
cp "$SCEN/two-model-shapes-pd.yaml" "$T/pd.yaml"
bash "$SCRIPT" "$T/pd.yaml" >/dev/null 2>&1
n="$(yq -r '[.shared.modelservice.prefill | select(.terminationGracePeriodSeconds == 120 and .extraContainerConfig.lifecycle.preStop != null)] | length' "$T/pd.yaml")"
[ "$n" = 1 ] && ok "P/D: the prefill role drains too" || fail "P/D: the prefill role got no drain fields"
cp "$SCEN/two-model-shapes.yaml" "$T/agg.yaml"
bash "$SCRIPT" "$T/agg.yaml" >/dev/null 2>&1
n="$(yq -r '[.shared.modelservice.prefill | select(has("terminationGracePeriodSeconds") or has("extraContainerConfig"))] | length' "$T/agg.yaml")"
[ "$n" = 0 ] && ok "aggregated: the disabled prefill block is left alone" || fail "aggregated: drain fields written into a prefill block that is not enabled"

# What a scenario already declares wins: a longer grace, and its own hook.
cat > "$T/own.yaml" <<'EOF'
shared:
  modelservice:
    decode:
      vllm: {customCommand: "x"}
      terminationGracePeriodSeconds: 300
      extraContainerConfig:
        lifecycle:
          preStop:
            exec:
              command: ["/bin/sh", "-c", "sleep 90"]
EOF
bash "$SCRIPT" "$T/own.yaml" >/dev/null 2>&1
got="$(yq -r '.shared.modelservice.decode | (.terminationGracePeriodSeconds | tostring) + " " + (.extraContainerConfig.lifecycle.preStop.exec.command | join(" "))' "$T/own.yaml")"
[ "$got" = "300 /bin/sh -c sleep 90" ] && ok "a scenario's own longer grace and its own hook are kept" || fail "a scenario's own drain settings were overwritten: $got"

# The values the standup patch would write, from the same variables.
cp "$SCEN/two-model-shapes.yaml" "$T/env.yaml"
WVA_DRAIN_GRACE_SECONDS=200 WVA_DRAIN_SLEEP_SECONDS=60 bash "$SCRIPT" "$T/env.yaml" >/dev/null 2>&1
got="$(yq -r '.shared.modelservice.decode | (.terminationGracePeriodSeconds | tostring) + " " + (.extraContainerConfig.lifecycle.preStop.exec.command | join(" "))' "$T/env.yaml")"
[ "$got" = "200 /bin/sh -c sleep 60" ] && ok "WVA_DRAIN_GRACE_SECONDS and WVA_DRAIN_SLEEP_SECONDS set the rendered values, as they set the patch" \
    || fail "the drain variables did not reach the scenario: $got"

cp "$SCEN/two-model-shapes.yaml" "$T/off.yaml"
BENCHMARK_RENDER_DRAIN=false bash "$SCRIPT" "$T/off.yaml" >/dev/null 2>&1
cmp -s "$SCEN/two-model-shapes.yaml" "$T/off.yaml" && ok "BENCHMARK_RENDER_DRAIN=false leaves the drain to the post-standup patch" \
    || fail "BENCHMARK_RENDER_DRAIN=false still edited the scenario"

# Refusals: a setting that matches nothing must say so, not pass.
refuse() {  # refuse "<why>" <scenario> env...
    local why="$1" sc="$2"; shift 2
    cp "$sc" "$T/x.yaml"
    if env "$@" BENCHMARK_RENDER_DRAIN=false bash "$SCRIPT" "$T/x.yaml" >/dev/null 2>&1; then fail "accepted: $why"
    elif cmp -s "$sc" "$T/x.yaml"; then ok "refused, scenario untouched: $why"
    else fail "refused but left a half-edited scenario: $why"; fi
}
printf 'scenario:\n  - name: x\n    modelservice:\n      decode: {replicas: 1}\n' > "$T/bare.yaml"
refuse "gateway reuse on a scenario with no istio gateway" "$T/bare.yaml" BENCHMARK_GATEWAY_SKIP_INSTALL=true
refuse "DECODE_CPU on a scenario with no decode requests" "$T/bare.yaml" BENCHMARK_DECODE_CPU=4
refuse "a CPU quantity that is not one" "$SCEN/two-model-shapes.yaml" "BENCHMARK_DECODE_CPU=4; rm"
refuse "a provider namespace that is not a name" "$SCEN/two-model-shapes.yaml" BENCHMARK_GATEWAY_SKIP_INSTALL=true BENCHMARK_GATEWAY_PROVIDER_NAMESPACE=Bad_NS

# BENCHMARK_DECODE_NODE pins every stack's engines to one node, and nothing
# else: the shared block sizes no Pod, and a pin there is one nobody asked for.
cp "$SCEN/two-model-mixed-sizes.yaml" "$T/node.yaml"
if BENCHMARK_RENDER_DRAIN=false BENCHMARK_DECODE_NODE=gpu-node-7 bash "$SCRIPT" "$T/node.yaml" > "$T/node.out" 2>&1; then
    got="$(yq -r '[.scenario[].modelservice.decode.nodeSelector."kubernetes.io/hostname"] | join(",")' "$T/node.yaml")"
    shared="$(yq -r '.shared.modelservice.decode.nodeSelector // "none"' "$T/node.yaml")"
    if [ "$got" = "gpu-node-7,gpu-node-7" ] && [ "$shared" = none ]; then
        ok "BENCHMARK_DECODE_NODE pins both stacks' engines to the node, and only them"
    else
        fail "BENCHMARK_DECODE_NODE pinned the stacks as '$got' and the shared block as '$shared'"
    fi
else
    fail "BENCHMARK_DECODE_NODE refused on the mixed-sizes scenario: $(cat "$T/node.out")"
fi
refuse "a node name that is not one" "$SCEN/two-model-mixed-sizes.yaml" "BENCHMARK_DECODE_NODE=n1;x"
refuse "a node pin on a scenario with no engine resources" "$T/bare.yaml" BENCHMARK_DECODE_NODE=n1

CASES_EXPECTED=28
if [ "$CASES" -ne "$CASES_EXPECTED" ]; then
    echo "FAIL $CASES cases ran, not $CASES_EXPECTED. Update CASES_EXPECTED deliberately rather than letting coverage drift out."
    FAIL=$((FAIL + 1))
fi
if [ "$FAIL" -ne 0 ]; then
    echo "scenario overrides check FAILED ($FAIL)"
    exit 1
fi
echo "scenario overrides check OK ($CASES cases)"
