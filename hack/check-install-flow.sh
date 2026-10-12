#!/usr/bin/env bash
#
# Executes the installer's decision functions against a STUBBED kubectl/helm and
# asserts what they decide. Nothing here touches a cluster.
#
# Each section covers a review finding that `bash -n` cannot see, because every
# one of them parses and is wrong about a running cluster:
#
#   KEDA        the preflight never looked for KEDA, and the install applied the
#               controller before deploy_keda found it missing -- a half-install.
#
# Sourced through CR-stripped copies, for the reason hack/check-workload-gaps.sh
# gives: a Windows checkout writes these files as CRLF and bash then stops at the
# first function definition.

set -u

# CHECK_ROOT runs the check against another tree -- how its negative control is
# taken: point it at an extracted older deploy/ and the cases must fail.
ROOT="${CHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
FAIL=0
PASS=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh constants.sh prereqs.sh scaler_runtime.sh infra_scaler_backend.sh install_core.sh verify.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done

BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
unset WVA_SCOPE WVA_WATCH_NS WVA_LIMITER KEDA_HELM_INSTALL SCALER_BACKEND ENVIRONMENT
WVA_NS="wva-system"
KEDA_CHART_VERSION="2.19.0"
KEDA_NAMESPACE="keda-system"
REQUIRED_TOOLS=(kubectl)
# shellcheck disable=SC1090
for lib in common.sh constants.sh prereqs.sh scaler_runtime.sh infra_scaler_backend.sh install_core.sh verify.sh; do
    . "$WORK/$lib"
done

ok()   { PASS=$((PASS + 1)); echo "ok   $1"; }
fail() { FAIL=$((FAIL + 1)); echo "FAIL $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/       /'; }

# --- the stub -----------------------------------------------------------------
#
# Driven by variables, so each case states the cluster it describes:
#   CRD_MODE      absent | forbidden | old | new | error
#   OP_READY      True | False | none | forbidden    (keda-operator pods)
#   OP_IMAGE      the operator image
#   KUBECTL_LOG   every call is appended here, for cases that assert on calls
KUBECTL_LOG="$WORK/kubectl.log"
kubectl() {
    printf '%s\n' "$*" >> "$KUBECTL_LOG"
    case "$*" in
        "get crd scaledobjects.keda.sh"*)
            case "${CRD_MODE:-new}" in
                absent)    echo 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "scaledobjects.keda.sh" not found' >&2; return 1 ;;
                forbidden) echo 'Error from server (Forbidden): customresourcedefinitions.apiextensions.k8s.io "scaledobjects.keda.sh" is forbidden: User "u" cannot get resource' >&2; return 1 ;;
                error)     echo 'Unable to connect to the server: dial tcp: i/o timeout' >&2; return 1 ;;
                old)       echo ''; return 0 ;;
                new)       echo 'string'; return 0 ;;
            esac ;;
        "get pods -A -l app.kubernetes.io/name=keda-operator -o jsonpath={range"*)
            case "${OP_READY:-True}" in
                forbidden) return 1 ;;
                none)      echo ''; return 0 ;;
                *)         echo "${OP_READY}"; return 0 ;;
            esac ;;
        "get pods -A -l app.kubernetes.io/name=keda-operator -o name"*)
            case "${OP_READY:-True}" in
                forbidden) return 1 ;;
                none)      echo ''; return 0 ;;
                *)         echo 'pod/keda-operator-abc'; return 0 ;;
            esac ;;
        "get pods -A -l app.kubernetes.io/name=keda-operator -o jsonpath={.items[0].spec.containers[0].image}"*)
            echo "${OP_IMAGE:-ghcr.io/kedacore/keda:2.19.0}"; return 0 ;;
    esac
    return 0
}
helm() { printf 'helm %s\n' "$*" >> "$KUBECTL_LOG"; return 0; }

# keda_case runs check_keda_prerequisite in a subshell (log_error exits) and
# leaves rc / out / state for the assertions.
keda_case() {
    : > "$KUBECTL_LOG"
    out="$( (check_keda_prerequisite; echo "STATE=${WVA_KEDA_STATE:-}") 2>&1 )"
    rc=$?
    state="$(printf '%s\n' "$out" | sed -n 's/^STATE=//p')"
}

# --- KEDA ---------------------------------------------------------------------

if ! declare -F check_keda_prerequisite >/dev/null; then
    fail "KEDA: check_keda_prerequisite is not defined -- the preflight has no KEDA probe"
else
    ENVIRONMENT=kubernetes; SCALER_BACKEND=keda; KEDA_HELM_INSTALL=false
    CRD_MODE=absent; OP_READY=none; keda_case
    if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'KEDA_HELM_INSTALL=true'; then
        ok "KEDA absent on Kubernetes refuses, and says KEDA_HELM_INSTALL=true"
    else
        fail "KEDA absent on Kubernetes must refuse with KEDA_HELM_INSTALL advice (rc=$rc)" "$out"
    fi

    ENVIRONMENT=openshift; CRD_MODE=absent; keda_case
    if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'Custom Metrics Autoscaler'; then
        ok "KEDA absent on OpenShift refuses, and names the Custom Metrics Autoscaler operator"
    else
        fail "KEDA absent on OpenShift must name the operator to install (rc=$rc)" "$out"
    fi

    ENVIRONMENT=kubernetes; CRD_MODE=forbidden; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = unknown ] && printf '%s' "$out" | grep -qi 'cannot verify'; then
        ok "KEDA Forbidden is 'cannot verify', not absent"
    else
        fail "KEDA Forbidden must warn and continue as unknown (rc=$rc state=$state)" "$out"
    fi

    CRD_MODE=error; keda_case
    if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'could not check'; then
        ok "KEDA probe error (not NotFound, not Forbidden) refuses as 'could not check'"
    else
        fail "an unreadable CRD must refuse as unknown-error, not pass (rc=$rc)" "$out"
    fi

    KEDA_HELM_INSTALL=true; CRD_MODE=absent; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = absent-installing ]; then
        ok "KEDA absent with KEDA_HELM_INSTALL=true passes: this run installs it"
    else
        fail "KEDA_HELM_INSTALL=true must accept an absent KEDA (rc=$rc state=$state)" "$out"
    fi
    KEDA_HELM_INSTALL=false

    # Older than 2.17 is supported: the plan writes ScaledObjects with no
    # fallback (so_keda_fallback_probe). The preflight must say so and go on,
    # never refuse what the install itself accepts.
    CRD_MODE=old; OP_READY=True; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = present ] && printf '%s' "$out" | grep -q 'NO fallback'; then
        ok "KEDA CRD without fallback.behavior warns (no fallback) and continues"
    else
        fail "a CRD without fallback.behavior must warn and continue as present (rc=$rc state=$state)" "$out"
    fi

    WVA_SO_FALLBACK=false; keda_case
    if [ "$rc" -eq 0 ] && ! printf '%s' "$out" | grep -q 'NO fallback'; then
        ok "KEDA without fallback.behavior and WVA_SO_FALLBACK=false does not warn about a fallback nothing writes"
    else
        fail "WVA_SO_FALLBACK=false must not warn about the fallback (rc=$rc)" "$out"
    fi
    unset WVA_SO_FALLBACK

    CRD_MODE=new; OP_READY=True; OP_IMAGE=ghcr.io/kedacore/keda:2.16.1; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = present ] && printf '%s' "$out" | grep -q 'operator is 2.16.1.*NO fallback'; then
        ok "KEDA operator 2.16.1 warns (no fallback before ${KEDA_FALLBACK_MIN_VERSION:-2.17}) and continues"
    else
        fail "an operator older than ${KEDA_FALLBACK_MIN_VERSION:-2.17} must warn and continue (rc=$rc state=$state)" "$out"
    fi

    OP_IMAGE=ghcr.io/kedacore/keda:2.19.0; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = present ]; then
        ok "KEDA 2.19.0 with a Ready operator is present"
    else
        fail "KEDA 2.19.0 Ready must pass as present (rc=$rc state=$state)" "$out"
    fi

    OP_READY=False; keda_case
    if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'none is Ready'; then
        ok "KEDA operator pods present and none Ready refuses"
    else
        fail "operator pods with none Ready must refuse (rc=$rc)" "$out"
    fi

    OP_READY=forbidden; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = present ]; then
        ok "KEDA operator unlistable (Forbidden) does not refuse a CRD that is new enough"
    else
        fail "an unlistable operator must not refuse (rc=$rc state=$state)" "$out"
    fi
    OP_READY=True

    SCALER_BACKEND=none; keda_case
    if [ "$rc" -eq 0 ] && [ "$state" = skipped ] && [ ! -s "$KUBECTL_LOG" ]; then
        ok "SCALER_BACKEND=none skips the KEDA probe without calling the cluster"
    else
        fail "SCALER_BACKEND=none must skip the probe (rc=$rc state=$state)" "$out"
    fi
    SCALER_BACKEND=keda
fi

# The ORDER of the cluster-admin phase: the scaler backend before the controller.
if ! declare -F wva_run_prereqs_phase >/dev/null; then
    fail "KEDA: wva_run_prereqs_phase is not defined -- the prereqs order is not testable"
else
    order="$(
        ORDER=""
        create_namespaces()        { ORDER="$ORDER namespaces"; }
        deploy_monitoring_stack()  { ORDER="$ORDER monitoring"; }
        deploy_scaler_backend()    { ORDER="$ORDER scaler"; }
        deploy_wva_prerequisites() { ORDER="$ORDER prerequisites"; }
        deploy_wva_controller()    { ORDER="$ORDER controller"; }
        pl_grant_published_policy_read() { ORDER="$ORDER policy-read"; }
        wva_install_scope()        { echo namespace; }
        DEPLOY_WVA=true
        wva_run_prereqs_phase all
        echo "$ORDER"
    )"
    case "$order" in
        *scaler*controller*) ok "prereqs phase installs the scaler backend before the controller ($order)" ;;
        *) fail "the scaler backend must come before the controller, got:$order" ;;
    esac
    case "$order" in
        *monitoring*scaler*) ok "prereqs phase installs monitoring before the scaler backend (KEDA's chart creates ServiceMonitors)" ;;
        *) fail "monitoring must come before the scaler backend, got:$order" ;;
    esac
    # A namespace install grants the published policy read BEFORE the
    # controller is applied, or its first pod races the RoleBinding.
    case "$order" in
        *policy-read*controller*) ok "prereqs phase grants the published policy read before the controller" ;;
        *) fail "the published policy read must be granted before the controller, got:$order" ;;
    esac
fi

# The handover text must not promise a KEDA install that never happens.
summary="$(
    wva_rendered_prereq_objects() { echo "ClusterRole wva-manager-role-abc"; }
    wva_install_scope() { echo namespace; }
    ENVIRONMENT=openshift WVA_KEDA_STATE=present print_prereqs_summary 2>&1
)"
if printf '%s' "$summary" | grep -q 'if this cluster had none'; then
    fail "print_prereqs_summary still claims KEDA is installed 'if this cluster had none'"
elif printf '%s' "$summary" | grep -q 'KEDA: already on the cluster'; then
    ok "print_prereqs_summary reports the measured KEDA state"
else
    fail "print_prereqs_summary must report the measured KEDA state" "$summary"
fi

echo ""
echo "check-install-flow: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] && [ "$PASS" -gt 0 ]
