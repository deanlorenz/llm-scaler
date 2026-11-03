#!/usr/bin/env bash
#
# Executes the installer's passive-mode keep against a stub kubectl and asserts
# that passive mode is rendered INTO the apply -- WVA_PASSIVE in the manager
# ConfigMap, `passive` on the scaling-policy default entry, and the operator's
# other policy entries -- so no applied state is ever active; that a false is
# kept as false (yq's `//` reads false as absent); and that a controller that
# read another WVA_PASSIVE at start is restarted.
#
# It exists because the first version restored all of it AFTER `kubectl apply
# -k`: a controller pod that started between the two ran active, and a
# `passive: false` read through `// ""` came back empty and was dropped. Both
# parse, and both install green.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
PASS=0

for tool in yq jq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FATAL: $tool is required by this check, and is missing. Nothing below would be about the code."
        exit 2
    }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh infra_wva.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
# shellcheck source=/dev/null
. "$WORK/common.sh"
# shellcheck source=/dev/null
. "$WORK/infra_wva.sh"

for fn in wva_live_passive wva_passive_want wva_render_manager_config_patch wva_render_policy_keep_patch \
          wva_passive_restart_if_stale wva_yaml_scalar; do
    declare -F "$fn" >/dev/null || { echo "FATAL: $fn is not defined after sourcing the library."; exit 2; }
done

ok() { echo "ok   $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL $1"; FAIL=$((FAIL + 1)); }

# The live cluster, as the stub kubectl serves it.
LIVE_MANAGER_CONFIG=''
LIVE_DEFAULT=''
LIVE_STATUS_PASSIVE=''
RESTARTED=0
RENDERED_POLICY='apiVersion: v1
kind: ConfigMap
metadata:
  name: wva-scaling-policy-config
data:
  default: |
    analyzers:
      - name: saturation
        score: 1.0
    scaleUpThreshold: 0.85'
kubectl() {
    case "$*" in
        "get configmap wva-manager-config"*) printf '%s' "$LIVE_MANAGER_CONFIG" ;;
        "get configmap wva-scaling-policy-config"*) printf '%s' "$LIVE_DEFAULT" ;;
        "get configmap wva-controller-status"*) printf '%s' "$LIVE_STATUS_PASSIVE" ;;
        "kustomize "*) printf '%s\n' "$RENDERED_POLICY" ;;
        "rollout restart"*) RESTARTED=$((RESTARTED + 1)) ;;
        *) echo "unexpected kubectl $*" >&2; return 1 ;;
    esac
}

# 1. A false is read as false, not as absent.
LIVE_MANAGER_CONFIG='GLOBAL_OPT_INTERVAL: "15s"
WVA_PASSIVE: "false"'
LIVE_DEFAULT='passive: false
scaleUpThreshold: 0.85'
wva_live_passive wva
[ "$WVA_LIVE_PASSIVE" = false ] && ok "WVA_PASSIVE: \"false\" reads as false" || fail "WVA_PASSIVE: \"false\" read as '$WVA_LIVE_PASSIVE'"
[ "$WVA_LIVE_DEFAULT_PASSIVE" = false ] && ok "passive: false reads as false" || fail "passive: false read as '$WVA_LIVE_DEFAULT_PASSIVE'"
LIVE_MANAGER_CONFIG='GLOBAL_OPT_INTERVAL: "15s"'
LIVE_DEFAULT='scaleUpThreshold: 0.85'
wva_live_passive wva
[ -z "$WVA_LIVE_PASSIVE" ] && [ -z "$WVA_LIVE_DEFAULT_PASSIVE" ] && ok "absent keys read as empty" \
    || fail "absent keys read as '$WVA_LIVE_PASSIVE' / '$WVA_LIVE_DEFAULT_PASSIVE'"

# 2. The install variable wins over the live value; anything else is refused.
WVA_LIVE_PASSIVE=true
unset WVA_PASSIVE
[ "$(wva_passive_want)" = true ] && ok "unset WVA_PASSIVE keeps the live true" || fail "live true not kept"
[ "$(WVA_PASSIVE=false wva_passive_want)" = false ] && ok "WVA_PASSIVE=false overrides the live true" || fail "WVA_PASSIVE=false ignored"
if (WVA_PASSIVE=yes wva_passive_want >/dev/null 2>&1); then fail "WVA_PASSIVE=yes accepted"; else ok "WVA_PASSIVE=yes refused"; fi

# 3. WVA_PASSIVE is rendered into the manager ConfigMap the apply carries,
#    with or without a Prometheus URL to render.
OVERLAY="$WORK/overlay"
mkdir -p "$OVERLAY"
WVA_PROJECT="$ROOT"
unset PROMETHEUS_URL
WVA_LIVE_PASSIVE=true
: > "$OVERLAY/kustomization.yaml"
wva_render_manager_config_patch "$OVERLAY"
got="$(yq '.data."config.yaml"' "$OVERLAY/manager-config-patch.yaml" 2>/dev/null | yq '.WVA_PASSIVE')"
[ "$got" = true ] && ok "the rendered manager ConfigMap carries WVA_PASSIVE true" || fail "rendered WVA_PASSIVE is '$got'"
grep -q 'manager-config-patch.yaml' "$OVERLAY/kustomization.yaml" && ok "the manager patch is in the kustomization" \
    || fail "the manager patch is not in the kustomization"

# 4. The policy: the default entry rendered with the live passive, and the
#    operator's entries, all in the patch the apply carries.
: > "$OVERLAY/kustomization.yaml"
rm -f "$OVERLAY/policy-keep-patch.yaml"
WVA_LIVE_DEFAULT_PASSIVE=true
wva_render_policy_keep_patch "$OVERLAY" '{"data":{"chat.prod":"model_id: chat\nnamespace: prod\npassive: false\n"}}'
def="$(yq '.data.default' "$OVERLAY/policy-keep-patch.yaml" 2>/dev/null)"
[ "$(printf '%s\n' "$def" | yq '.passive')" = true ] && ok "the default entry carries passive: true" || fail "default entry: $def"
[ "$(printf '%s\n' "$def" | yq '.scaleUpThreshold')" = 0.85 ] && ok "the default entry keeps the rendered thresholds" \
    || fail "default entry lost the rendered thresholds: $def"
entry="$(yq '.data."chat.prod"' "$OVERLAY/policy-keep-patch.yaml" 2>/dev/null)"
[ "$(printf '%s\n' "$entry" | yq '.passive')" = false ] && ok "the per-model entry with passive: false is in the apply" \
    || fail "per-model entry: $entry"
grep -q 'policy-keep-patch.yaml' "$OVERLAY/kustomization.yaml" && ok "the policy patch is in the kustomization" \
    || fail "the policy patch is not in the kustomization"
# Nothing to keep: no patch.
: > "$OVERLAY/kustomization.yaml"
rm -f "$OVERLAY/policy-keep-patch.yaml"
WVA_LIVE_DEFAULT_PASSIVE=''
wva_render_policy_keep_patch "$OVERLAY" ''
[ ! -e "$OVERLAY/policy-keep-patch.yaml" ] && ! grep -q policy-keep "$OVERLAY/kustomization.yaml" \
    && ok "nothing to keep writes no patch" || fail "a patch was written with nothing to keep"

# 5. A controller that read another WVA_PASSIVE at start is restarted; one
#    that read the same is not, nor one that published nothing.
WVA_LIVE_PASSIVE=true
LIVE_STATUS_PASSIVE=false; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
[ "$RESTARTED" = 1 ] && ok "a controller running with the other value is restarted" || fail "restarts: $RESTARTED, want 1"
LIVE_STATUS_PASSIVE=true; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
[ "$RESTARTED" = 0 ] && ok "a controller running with the same value is left alone" || fail "restarted a controller with the same value"
LIVE_STATUS_PASSIVE=''; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
[ "$RESTARTED" = 0 ] && ok "a controller that published nothing is left to the rollout" || fail "restarted with no published value"

EXPECT_CASES=16
echo
echo "passive keep: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
if [ "$PASS" -ne "$EXPECT_CASES" ]; then
    echo "FAIL ran $PASS cases, expected $EXPECT_CASES: the run ended early"
    exit 1
fi
