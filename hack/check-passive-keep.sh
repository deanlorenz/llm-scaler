#!/usr/bin/env bash
#
# Executes the installer's passive-mode keep against a stub kubectl and asserts
# that passive mode is rendered INTO the apply -- WVA_PASSIVE in the manager
# ConfigMap, `passive` on the scaling-policy default entry, and the operator's
# other policy entries -- so no applied state is ever active; that a false is
# kept as false (yq's `//` reads false as absent); that a render or a read that
# fails stops the install rather than dropping passive mode; and that a
# controller that read another WVA_PASSIVE at start is restarted.
#
# It exists because the first version restored all of it AFTER `kubectl apply
# -k`: a controller pod that started in between ran active, and a
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

for fn in wva_live_passive wva_live_get wva_passive_want wva_render_manager_config_patch \
          wva_render_policy_keep_patch wva_passive_restart_if_stale wva_yaml_scalar; do
    declare -F "$fn" >/dev/null || { echo "FATAL: $fn is not defined after sourcing the library."; exit 2; }
done

ok() { echo "ok   $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL $1"; FAIL=$((FAIL + 1)); }
# check <description> <command...>: ok when the command succeeds.
check() {
    local what="$1"
    shift
    if "$@"; then
        ok "$what"
    else
        fail "$what"
    fi
}
is() { [ "$1" = "$2" ]; }

# The live cluster, as the stub kubectl serves it. LIVE_READ is how a read of
# a ConfigMap goes: ok, notfound or forbidden.
LIVE_MANAGER_CONFIG=''
LIVE_DEFAULT=''
LIVE_STATUS_PASSIVE=''
LIVE_READ=ok
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
        "get configmap wva-manager-config"* | "get configmap wva-scaling-policy-config"*)
            case "$LIVE_READ" in
                notfound) echo 'Error from server (NotFound): configmaps "x" not found' >&2; return 1 ;;
                forbidden) echo 'Error from server (Forbidden): configmaps "x" is forbidden' >&2; return 1 ;;
            esac
            case "$*" in
                *wva-manager-config*) printf '%s' "$LIVE_MANAGER_CONFIG" ;;
                *) printf '%s' "$LIVE_DEFAULT" ;;
            esac ;;
        "get configmap wva-controller-status"*) printf '%s' "$LIVE_STATUS_PASSIVE" ;;
        "kustomize "*) printf '%s\n' "$RENDERED_POLICY" ;;
        "rollout restart"*) RESTARTED=$((RESTARTED + 1)) ;;
        *) echo "unexpected kubectl $*" >&2; return 1 ;;
    esac
}

# 1. A false is read as false, not as absent; absent and NotFound read as
#    empty; Forbidden is a failure, not an absence.
LIVE_MANAGER_CONFIG='GLOBAL_OPT_INTERVAL: "15s"
WVA_PASSIVE: "false"'
LIVE_DEFAULT='passive: false
scaleUpThreshold: 0.85'
wva_live_passive wva
check "WVA_PASSIVE: \"false\" reads as false" is "$WVA_LIVE_PASSIVE" false
check "passive: false reads as false" is "$WVA_LIVE_DEFAULT_PASSIVE" false
LIVE_MANAGER_CONFIG='GLOBAL_OPT_INTERVAL: "15s"'
LIVE_DEFAULT='scaleUpThreshold: 0.85'
wva_live_passive wva
check "absent keys read as empty" is "$WVA_LIVE_PASSIVE$WVA_LIVE_DEFAULT_PASSIVE" ""
LIVE_READ=notfound
if wva_live_passive wva && is "$WVA_LIVE_PASSIVE" ""; then
    ok "a ConfigMap that does not exist reads as empty"
else
    fail "a NotFound read failed or read '$WVA_LIVE_PASSIVE'"
fi
LIVE_READ=forbidden
if wva_live_passive wva 2>/dev/null; then
    fail "a Forbidden read was taken as absent"
else
    ok "a Forbidden read is a failure"
fi
LIVE_READ=ok

# 2. The install variable wins over the live value; anything else is refused.
WVA_LIVE_PASSIVE=true
unset WVA_PASSIVE
check "unset WVA_PASSIVE keeps the live true" is "$(wva_passive_want)" true
check "WVA_PASSIVE=false overrides the live true" is "$(WVA_PASSIVE=false wva_passive_want)" false
if (WVA_PASSIVE=yes wva_passive_want >/dev/null 2>&1); then
    fail "WVA_PASSIVE=yes accepted"
else
    ok "WVA_PASSIVE=yes refused"
fi

# 3. WVA_PASSIVE is rendered into the manager ConfigMap the apply carries,
#    with or without a Prometheus URL to render; a render that fails with
#    passive mode to keep is an error.
OVERLAY="$WORK/overlay"
mkdir -p "$OVERLAY"
WVA_PROJECT="$ROOT"
unset PROMETHEUS_URL
WVA_LIVE_PASSIVE=true
: > "$OVERLAY/kustomization.yaml"
wva_render_manager_config_patch "$OVERLAY"
got="$(yq '.data."config.yaml"' "$OVERLAY/manager-config-patch.yaml" 2>/dev/null | yq '.WVA_PASSIVE')"
check "the rendered manager ConfigMap carries WVA_PASSIVE true" is "$got" true
check "the manager patch is in the kustomization" grep -q 'manager-config-patch.yaml' "$OVERLAY/kustomization.yaml"
BROKEN="$WORK/broken"
mkdir -p "$BROKEN/config/base/manager"
printf 'data: [unclosed\n' > "$BROKEN/config/base/manager/manager-configmap.yaml"
WVA_PROJECT="$BROKEN"
: > "$OVERLAY/kustomization.yaml"
if wva_render_manager_config_patch "$OVERLAY" 2>/dev/null; then
    fail "a render yq could not do succeeded with WVA_PASSIVE to keep"
else
    ok "a render yq cannot do fails with WVA_PASSIVE to keep"
fi
WVA_LIVE_PASSIVE=''
if wva_render_manager_config_patch "$OVERLAY" 2>/dev/null; then
    ok "with nothing to keep, an unrenderable manager ConfigMap is skipped as before"
else
    fail "with nothing to keep, the render failed the install"
fi
WVA_PROJECT="$ROOT"

# 4. The policy: the default entry rendered with the live passive, and the
#    operator's entries, all in the patch the apply carries.
: > "$OVERLAY/kustomization.yaml"
rm -f "$OVERLAY/policy-keep-patch.yaml"
WVA_LIVE_DEFAULT_PASSIVE=true
wva_render_policy_keep_patch "$OVERLAY" '{"data":{"chat.prod":"model_id: chat\nnamespace: prod\npassive: false\n"}}' >/dev/null
def="$(yq '.data.default' "$OVERLAY/policy-keep-patch.yaml" 2>/dev/null)"
check "the default entry carries passive: true" is "$(printf '%s\n' "$def" | yq '.passive')" true
check "the default entry keeps the rendered thresholds" is "$(printf '%s\n' "$def" | yq '.scaleUpThreshold')" 0.85
entry="$(yq '.data."chat.prod"' "$OVERLAY/policy-keep-patch.yaml" 2>/dev/null)"
check "the per-model entry with passive: false is in the apply" is "$(printf '%s\n' "$entry" | yq '.passive')" false
check "the policy patch is in the kustomization" grep -q 'policy-keep-patch.yaml' "$OVERLAY/kustomization.yaml"
: > "$OVERLAY/kustomization.yaml"
rm -f "$OVERLAY/policy-keep-patch.yaml"
WVA_LIVE_DEFAULT_PASSIVE=''
wva_render_policy_keep_patch "$OVERLAY" ''
if [ ! -e "$OVERLAY/policy-keep-patch.yaml" ] && ! grep -q policy-keep "$OVERLAY/kustomization.yaml"; then
    ok "nothing to keep writes no patch"
else
    fail "a patch was written with nothing to keep"
fi

# 5. A controller that read another WVA_PASSIVE at start is restarted; one
#    that read the same is not, nor one that published nothing.
WVA_LIVE_PASSIVE=true
LIVE_STATUS_PASSIVE=false; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
check "a controller running with the other value is restarted" is "$RESTARTED" 1
LIVE_STATUS_PASSIVE=true; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
check "a controller running with the same value is left alone" is "$RESTARTED" 0
LIVE_STATUS_PASSIVE=''; RESTARTED=0
wva_passive_restart_if_stale wva 2>/dev/null
check "a controller that published nothing is left to the rollout" is "$RESTARTED" 0

EXPECT_CASES=20
echo
echo "passive keep ($(yq --version 2>&1)): $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
if [ "$PASS" -ne "$EXPECT_CASES" ]; then
    echo "FAIL ran $PASS cases, expected $EXPECT_CASES: the run ended early"
    exit 1
fi
