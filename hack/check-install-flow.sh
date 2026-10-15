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
# Every case below reports ok or FAIL exactly once. A run that ends early --
# a sourced lib that exits, a section that stops -- otherwise passes having
# checked less than it says. Change this deliberately with the cases.
EXPECT_CASES=45

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh constants.sh prereqs.sh scaler_runtime.sh infra_scaler_backend.sh install_core.sh verify.sh pod_writer.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done

BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
unset WVA_SCOPE WVA_WATCH_NS WVA_LIMITER KEDA_HELM_INSTALL SCALER_BACKEND ENVIRONMENT
WVA_NS="wva-system"
KEDA_CHART_VERSION="2.19.0"
KEDA_NAMESPACE="keda-system"
REQUIRED_TOOLS=(kubectl)
# shellcheck disable=SC1090
for lib in common.sh constants.sh prereqs.sh scaler_runtime.sh infra_scaler_backend.sh install_core.sh verify.sh pod_writer.sh; do
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

# --- POD WRITES ---------------------------------------------------------------
#
# This release moves `patch` on pods out of the controller's ClusterRole into
# role/wva-pod-writer, made per namespace (deploy/lib/pod_writer.sh). An upgrade
# took the old grant away and nothing made the new one: an existing warm pool
# stopped lending, and an active utilization-share optimizer could not clear
# the marks it had written. And an uninstall left every grant behind.
#
# The stub, driven by variables:
#   PW_SOS            ScaledObject triggers as the go-template renders them
#   PW_MARKED         namespaces holding a pod with the transfer mark
#   PW_RBS            "namespace name" of each labelled RoleBinding
#   PW_*_FORBIDDEN    that call is refused (a list of namespaces for apply,
#                     delete and the per-namespace RoleBinding list)
pw_kubectl() {
    printf '%s\n' "$*" >> "$KUBECTL_LOG"
    local body ns
    case "$*" in
        "get scaledobject -A"*)
            [ -z "${PW_SO_FORBIDDEN:-}" ] || { echo 'Error from server (Forbidden): scaledobjects.keda.sh is forbidden: User "u" cannot list resource "scaledobjects" at the cluster scope' >&2; return 1; }
            [ -z "${PW_SO_NOTYPE:-}" ] || { echo "error: the server doesn't have a resource type \"scaledobject\"" >&2; return 1; }
            printf '%s\n' "${PW_SOS:-}" ;;
        # The optimizer mode: PW_STATUS is what the controller publishes
        # (FORBIDDEN refuses the read; unset is no status ConfigMap), PW_POLICY
        # the default entry of its policy (unset is no policy ConfigMap).
        "get configmap wva-controller-status "*)
            case "${PW_STATUS:-}" in
                "") echo 'Error from server (NotFound): configmaps "wva-controller-status" not found' >&2; return 1 ;;
                FORBIDDEN) echo 'Error from server (Forbidden): configmaps "wva-controller-status" is forbidden: User "u" cannot get resource "configmaps"' >&2; return 1 ;;
                *) printf '%s' "$PW_STATUS" ;;
            esac ;;
        "get configmap wva-scaling-policy-config "*)
            [ -n "${PW_POLICY:-}" ] || { echo 'Error from server (NotFound): configmaps "wva-scaling-policy-config" not found' >&2; return 1; }
            printf '%s\n' "$PW_POLICY" ;;
        # May the controller patch pods? PW_CANI yes, no, or (unset) this user
        # may not impersonate it. PW_HAS_RB / PW_HAS_ROLE: the two halves of
        # the grant exist.
        "auth can-i "*)
            case "${PW_CANI:-}" in
                yes) echo yes ;;
                no) echo no; return 1 ;;
                *) echo 'Error from server (Forbidden): users "u" is forbidden: User "u" cannot impersonate resource "serviceaccounts"' >&2; return 1 ;;
            esac ;;
        "get rolebinding wva-pod-writer-"*)
            [ -n "${PW_HAS_RB:-}" ] || { echo 'Error from server (NotFound): rolebindings.rbac.authorization.k8s.io not found' >&2; return 1; } ;;
        "get role wva-pod-writer "*)
            [ -n "${PW_HAS_ROLE:-}" ] || { echo 'Error from server (NotFound): roles.rbac.authorization.k8s.io "wva-pod-writer" not found' >&2; return 1; } ;;
        "get pods -A"*)
            [ -z "${PW_PODS_FORBIDDEN:-}" ] || { echo 'Error from server (Forbidden): pods is forbidden: User "u" cannot list resource "pods" at the cluster scope' >&2; return 1; }
            printf '%s\n' "${PW_MARKED:-}" ;;
        "apply -f -")
            body="$(cat)"
            ns="$(printf '%s\n' "$body" | sed -n 's/^  namespace: //p' | sed -n 1p)"
            case " ${PW_APPLY_FORBIDDEN:-} " in
                *" $ns "*) echo "Error from server (Forbidden): roles.rbac.authorization.k8s.io \"wva-pod-writer\" is forbidden: User \"u\" cannot create resource \"roles\" in the namespace \"$ns\"" >&2; return 1 ;;
            esac
            echo "APPLIED $ns" >> "$KUBECTL_LOG" ;;
        "get rolebinding -A"*)
            [ -z "${PW_RB_FORBIDDEN:-}" ] || { echo 'Error from server (Forbidden): rolebindings.rbac.authorization.k8s.io is forbidden: User "u" cannot list resource "rolebindings" at the cluster scope' >&2; return 1; }
            printf '%s\n' "${PW_RBS:-}" ;;
        "get rolebinding -n "*)
            case " ${PW_RB_NS_FORBIDDEN:-} " in
                *" $4 "*) echo "Error from server (Forbidden): rolebindings is forbidden in the namespace \"$4\"" >&2; return 1 ;;
            esac
            printf '%s\n' "${PW_RBS:-}" | awk -v n="$4" '$1 == n' ;;
        "delete rolebinding "*|"delete role "*)
            case " ${PW_DELETE_FORBIDDEN:-} " in
                *" $5 "*) echo "Error from server (Forbidden): $2 is forbidden in the namespace \"$5\"" >&2; return 1 ;;
            esac ;;
    esac
    return 0
}

# pw_case <function> <args...> -- runs it against the stub, in a subshell;
# leaves out (its log) and the stub's call log in KUBECTL_LOG.
pw_case() {
    : > "$KUBECTL_LOG"
    out="$( (kubectl() { pw_kubectl "$@"; }; "$@") 2>&1 )"
}
applied() { grep '^APPLIED ' "$KUBECTL_LOG" | cut -d' ' -f2 | sort | tr '\n' ' ' | sed 's/ $//'; }

PW_ADDR="wva-external-scaler.wva-system.svc.cluster.local:9090"
PW_OTHER="wva-external-scaler.other.svc.cluster.local:9090"
PW_SUFFIX="$(printf '%s' wva-system | { sha256sum 2>/dev/null || shasum -a 256; } | cut -c1-8)"

if ! declare -F wva_regrant_pod_writers >/dev/null; then
    fail "POD WRITES: wva_regrant_pod_writers is not defined -- an upgrade takes patch on pods away and grants it nowhere"
else
    # A pool in ns-p and a marked model in ns-m call this controller. ns-q
    # holds a model with no mark; ns-x holds a mark of another controller's.
    PW_SOS="ns-p $PW_ADDR pool-a
ns-m $PW_ADDR -
ns-q $PW_ADDR -
ns-x $PW_OTHER -"
    PW_MARKED="ns-m
ns-x"
    pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-m ns-p" ]; then
        ok "POD WRITES: an upgrade grants in the warm-pool and marked namespaces of this controller, and no others"
    else
        fail "POD WRITES: an upgrade must grant in ns-m and ns-p only, got [$(applied)]" "$out"
    fi

    PW_APPLY_FORBIDDEN="ns-p" pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-m" ] && printf '%s' "$out" | grep -q 'pod_writer.sh ns-p wva-system | kubectl apply -f -'; then
        ok "POD WRITES: a namespace the installer may not grant in is named, with the command an admin runs"
    else
        fail "POD WRITES: a refused grant must name ns-p and the admin command" "$out"
    fi

    PW_SO_FORBIDDEN=1 pw_case wva_regrant_pod_writers wva-system
    if [ -z "$(applied)" ] && printf '%s' "$out" | grep -q 'UNKNOWN'; then
        ok "POD WRITES: unreadable ScaledObjects are UNKNOWN and warned, not taken for none"
    else
        fail "POD WRITES: an unreadable ScaledObject list must warn as unknown" "$out"
    fi

    PW_PODS_FORBIDDEN=1 pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-p" ] && printf '%s' "$out" | grep -q 'transfer marks are UNKNOWN' \
        && printf '%s' "$out" | grep -q 'ns-m ns-q'; then
        ok "POD WRITES: unreadable pods still grant the pools, and name the model namespaces to check"
    else
        fail "POD WRITES: unreadable pods must warn and list the model namespaces" "$out"
    fi

    # An ACTIVE optimizer moves the deletion cost of every model's pods, marked
    # or not. Before, a namespace with no mark yet lost patch on the upgrade,
    # and nothing was printed.
    PW_MARKED="" PW_STATUS=active pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-m ns-p ns-q" ]; then
        ok "POD WRITES: an active optimizer with no marks yet is granted in every namespace of its models"
    else
        fail "POD WRITES: an active optimizer must be granted in ns-m, ns-p and ns-q, got [$(applied)]" "$out"
    fi
    PW_MARKED="" PW_POLICY='optimizer:
  type: utilizationShare
  utilizationShare:
    shadow: false' pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-m ns-p ns-q" ]; then
        ok "POD WRITES: a controller with no status is read from its policy: active is granted everywhere"
    else
        fail "POD WRITES: an active block in the policy must grant in ns-m, ns-p and ns-q, got [$(applied)]" "$out"
    fi
    PW_STATUS=FORBIDDEN pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-m ns-p" ] && printf '%s' "$out" | grep -q 'Could not tell whether the utilization-share optimizer is active' \
        && printf '%s' "$out" | grep -q 'ns-m ns-q'; then
        ok "POD WRITES: an unreadable optimizer mode is warned with the model namespaces, never taken for off"
    else
        fail "POD WRITES: an unreadable mode must warn and list ns-m ns-q" "$out"
    fi

    # The short Service name (wva-external-scaler.<ns>:9090) calls this
    # controller too. An address naming no controller is warned, not guessed.
    PW_SOS="ns-c wva-external-scaler.wva-system:9090 pool-c -
ns-u 10.0.0.9:9090 - llama"
    PW_MARKED="" pw_case wva_regrant_pod_writers wva-system
    if [ "$(applied)" = "ns-c" ]; then
        ok "POD WRITES: a pool addressed by the short Service name (wva-external-scaler.<ns>:port) is granted"
    else
        fail "POD WRITES: wva-external-scaler.wva-system:9090 must count as this controller, got [$(applied)]" "$out"
    fi
    if printf '%s' "$out" | grep -q 'does not say which controller' && printf '%s' "$out" | grep -q 'ns-u (10.0.0.9:9090)'; then
        ok "POD WRITES: a pool or model whose address names no controller is warned about"
    else
        fail "POD WRITES: ns-u, addressed by IP, must be named as unattributable" "$out"
    fi

    # SCALER_BACKEND=none: no ScaledObject kind at all is none, not unknown.
    PW_SO_NOTYPE=1 pw_case wva_regrant_pod_writers wva-system
    if [ -z "$(applied)" ] && ! printf '%s' "$out" | grep -q 'UNKNOWN'; then
        ok "POD WRITES: a cluster without the ScaledObject kind has nothing to grant, and says nothing"
    else
        fail "POD WRITES: a missing ScaledObject kind must read as none, not UNKNOWN" "$out"
    fi
    unset PW_SOS PW_MARKED

    # Asked without impersonation, the grant counts only when both halves
    # exist: a RoleBinding whose Role was deleted grants nothing.
    if [ "$( (kubectl() { pw_kubectl "$@"; }; PW_HAS_RB=1 PW_HAS_ROLE=1 wva_pod_writes_allowed ns-a wva-system) )" = yes ] \
        && [ "$( (kubectl() { pw_kubectl "$@"; }; PW_CANI=yes wva_pod_writes_allowed ns-a wva-system) )" = yes ]; then
        ok "POD WRITES: the RoleBinding and the Role together read as already allowed"
    else
        fail "POD WRITES: a whole grant, or a can-i yes, must read as yes"
    fi
    if [ "$( (kubectl() { pw_kubectl "$@"; }; PW_HAS_RB=1 wva_pod_writes_allowed ns-a wva-system) )" = unknown ]; then
        ok "POD WRITES: a RoleBinding without its Role is not already allowed"
    else
        fail "POD WRITES: a RoleBinding whose Role is gone must read as unknown, so the grant is made again"
    fi

    # Before the apply that removes the old grant, and only where the
    # installer applies cluster RBAC.
    iw="$(tr -d '\r' < "$ROOT/deploy/lib/infra_wva.sh")"
    regrant_at="$(printf '%s\n' "$iw" | grep -nE '^[[:space:]]+wva_regrant_pod_writers ' | head -1 | cut -d: -f1)"
    apply_at="$(printf '%s\n' "$iw" | grep -nE 'kubectl apply -k "\$tmp_overlay"' | head -1 | cut -d: -f1)"
    if [ -n "$regrant_at" ] && [ -n "$apply_at" ] && [ "$regrant_at" -lt "$apply_at" ] \
        && tr -d '\r' < "$ROOT/deploy/install.sh" | grep -q 'source "$DEPLOY_LIB_DIR/pod_writer.sh"'; then
        ok "POD WRITES: deploy_wva_controller re-grants before its apply, and install.sh sources pod_writer.sh"
    else
        fail "POD WRITES: wva_regrant_pod_writers must run before the overlay apply (line ${regrant_at:-none} vs ${apply_at:-none}), from an install.sh that sources it"
    fi
    # And only where the installer applies cluster RBAC: the phases that carry
    # the bindings (all, prereqs), for a cluster-scoped controller. The three
    # lines above the call, whitespace aside.
    guard=""
    if [ -n "$regrant_at" ] && [ "$regrant_at" -gt 3 ]; then
        guard="$(printf '%s\n' "$iw" | sed -n "$((regrant_at - 3)),${regrant_at}p" | sed 's/^[[:space:]]*//' | tr '\n' '|')"
    fi
    want_guard='case "${WVA_APPLY_SCOPE:-all}" in|all|prereqs)|if [ "$(wva_install_scope)" = "cluster" ]; then|wva_regrant_pod_writers "$WVA_NS"|'
    if [ "$guard" = "$want_guard" ]; then
        ok "POD WRITES: the re-grant sits inside the all|prereqs and cluster-scope guards"
    else
        fail "POD WRITES: wva_regrant_pod_writers must sit directly inside the all|prereqs and cluster-scope guards" "got:  $guard
want: $want_guard"
    fi
fi

if ! declare -F wva_remove_pod_writers >/dev/null; then
    fail "POD WRITES: wva_remove_pod_writers is not defined -- an uninstall leaves role/wva-pod-writer in every workload namespace"
else
    # Ours in ns-a alone, ours beside another controller's in ns-b, only
    # another's in ns-c.
    PW_RBS="ns-a wva-pod-writer-$PW_SUFFIX
ns-b wva-pod-writer-$PW_SUFFIX
ns-b wva-pod-writer-0000abcd
ns-c wva-pod-writer-0000abcd"
    pw_case wva_remove_pod_writers wva-system
    dels="$(grep '^delete' "$KUBECTL_LOG" | sort | tr '\n' ';')"
    want="delete role wva-pod-writer -n ns-a --ignore-not-found;delete rolebinding wva-pod-writer-$PW_SUFFIX -n ns-a --ignore-not-found;delete rolebinding wva-pod-writer-$PW_SUFFIX -n ns-b --ignore-not-found;"
    if [ "$dels" = "$want" ]; then
        ok "POD WRITES: uninstall removes this controller's bindings, and the Role only where no other controller binds it"
    else
        fail "POD WRITES: uninstall deleted the wrong things" "got:  $dels
want: $want"
    fi

    PW_RB_FORBIDDEN=1 pw_case wva_remove_pod_writers wva-system
    if ! grep -q '^delete' "$KUBECTL_LOG" && printf '%s' "$out" | grep -q 'Could not list RoleBindings in: cluster-wide'; then
        ok "POD WRITES: an unreadable RoleBinding list is reported, with how to find the leftovers"
    else
        fail "POD WRITES: an unreadable list must be reported, not passed over" "$out"
    fi

    PW_RB_FORBIDDEN=1 pw_case wva_remove_pod_writers wva-system ns-a ns-z
    if grep -q "^delete rolebinding wva-pod-writer-$PW_SUFFIX -n ns-a" "$KUBECTL_LOG" \
        && ! printf '%s' "$out" | grep -q 'Could not list'; then
        ok "POD WRITES: a namespace-scoped uninstall searches the namespaces it was given"
    else
        fail "POD WRITES: refused cluster-wide, the given namespaces must be searched" "$out"
    fi

    PW_DELETE_FORBIDDEN="ns-a" pw_case wva_remove_pod_writers wva-system
    if printf '%s' "$out" | grep -q "rolebinding/wva-pod-writer-$PW_SUFFIX in ns-a"; then
        ok "POD WRITES: a grant the uninstall may not delete is listed"
    else
        fail "POD WRITES: a refused delete must be listed" "$out"
    fi
    unset PW_RBS

    if tr -d '\r' < "$ROOT/deploy/lib/cleanup.sh" | grep -qE '^[[:space:]]+wva_remove_pod_writers '; then
        ok "POD WRITES: undeploy_wva_controller removes the grants"
    else
        fail "POD WRITES: cleanup.sh never calls wva_remove_pod_writers"
    fi
    # After the delete, and gated on the Deployment being gone: removed first, a
    # delete that then failed left a running controller without patch on pods.
    cl="$(tr -d '\r' < "$ROOT/deploy/lib/cleanup.sh")"
    remove_at="$(printf '%s\n' "$cl" | grep -nE '^[[:space:]]+wva_remove_pod_writers ' | head -1 | cut -d: -f1)"
    delete_at="$(printf '%s\n' "$cl" | grep -nE 'kubectl delete -f - --ignore-not-found' | head -1 | cut -d: -f1)"
    gate_at="$(printf '%s\n' "$cl" | grep -nE 'get deployment wva-controller-manager -n "\$WVA_NS"' | head -1 | cut -d: -f1)"
    if [ -n "$remove_at" ] && [ -n "$delete_at" ] && [ -n "$gate_at" ] \
        && [ "$delete_at" -lt "$gate_at" ] && [ "$gate_at" -lt "$remove_at" ]; then
        ok "POD WRITES: uninstall removes the grants after the delete, once the Deployment is gone"
    else
        fail "POD WRITES: wva_remove_pod_writers must follow the delete and a Deployment-gone check (delete ${delete_at:-none}, check ${gate_at:-none}, remove ${remove_at:-none})"
    fi
fi

# --- GO -----------------------------------------------------------------------
#
# The install targets must apply the committed manifests. A prerequisite on
# `manifests` (controller-gen, a type-check of ./..., a rewrite of a tracked
# RBAC file) or `kustomize` makes Go an undeclared install prerequisite.
for target in setup-prereqs deploy-wva deploy-wva-on-k8s deploy-wva-on-openshift; do
    prereqs="$(tr -d '\r' < "$ROOT/Makefile" | awk -v t="$target" '
        index($0, t ":") == 1 { line = substr($0, length(t) + 2); sub(/##.*/, "", line); print line; exit }')"
    if printf '%s' "$prereqs" | grep -qwE 'manifests|generate|controller-gen|kustomize|build'; then
        fail "make $target depends on a code-generation/build step:$prereqs"
    else
        ok "make $target applies the committed manifests (prerequisites:${prereqs:- none})"
    fi
done

# --- TOKEN --------------------------------------------------------------------
#
# The OpenShift overlay used to mint a non-expiring token Secret for the
# controller's own ServiceAccount. It is no longer rendered, so nothing but a
# delete by name removes it from an existing install.
if ! declare -F wva_delete_legacy_controller_token >/dev/null; then
    fail "TOKEN: wva_delete_legacy_controller_token is not defined -- an upgraded install keeps the controller token Secret"
else
    token_case() {
        : > "$KUBECTL_LOG"
        out="$(
            kubectl() {
                printf '%s\n' "$*" >> "$KUBECTL_LOG"
                case "$1 $2" in
                    "get secret")
                        case "$TOKEN_SA" in
                            absent)    echo 'Error from server (NotFound): secrets "wva-controller-manager-token" not found'; return 1 ;;
                            forbidden) echo 'Error from server (Forbidden): secrets "wva-controller-manager-token" is forbidden'; return 1 ;;
                            *)         printf '%s' "$TOKEN_SA" ;;
                        esac ;;
                esac
                return 0
            }
            wva_delete_legacy_controller_token wns 2>&1
        )"
    }
    TOKEN_SA=wva-controller-manager token_case
    if grep -qx 'delete secret wva-controller-manager-token -n wns' "$KUBECTL_LOG"; then
        ok "TOKEN: the controller token Secret is deleted"
    else
        fail "TOKEN: the controller token Secret must be deleted" "$(cat "$KUBECTL_LOG")"
    fi
    TOKEN_SA=someone-else token_case
    if grep -q '^delete' "$KUBECTL_LOG"; then
        fail "TOKEN: a Secret of that name for another account is not ours to delete" "$(cat "$KUBECTL_LOG")"
    else
        ok "TOKEN: a Secret of that name for another account is left alone"
    fi
    TOKEN_SA=forbidden token_case
    if ! grep -q '^delete' "$KUBECTL_LOG" && printf '%s' "$out" | grep -q 'Could not check'; then
        ok "TOKEN: Forbidden is reported, not taken for absent"
    else
        fail "TOKEN: Forbidden must warn, not pass silently" "$out"
    fi
fi

# --- CHART --------------------------------------------------------------------
#
# kube-prometheus-stack is one release shared by every WVA on the cluster. With
# no --version, each install took whatever the repository served that day and a
# re-run upgraded everyone's Prometheus to it.
tr -d '\r' < "$ROOT/deploy/lib/deploy_prometheus_kube_stack.sh" > "$WORK/deploy_prometheus_kube_stack.sh"
kps_default="$(tr -d '\r' < "$ROOT/deploy/install.sh" | sed -n 's/^KUBE_PROMETHEUS_STACK_CHART_VERSION=\${KUBE_PROMETHEUS_STACK_CHART_VERSION:-\([^}]*\)}$/\1/p')"
kps_run() {
    : > "$KUBECTL_LOG"
    (
        openssl() { return 0; }
        jq() { echo; }
        install_operational_dashboard() { :; }
        MONITORING_NAMESPACE=mon PROMETHEUS_SECRET_NAME=s PROMETHEUS_PORT=9090 PROMETHEUS_URL=u \
            DEPLOY_OPERATIONAL_DASHBOARD=false SKIP_HELM_REPO_UPDATE=true
        # shellcheck disable=SC1091
        . "$WORK/deploy_prometheus_kube_stack.sh"
        deploy_prometheus_kube_stack
    ) > "$WORK/kps.out" 2>&1
}
KUBE_PROMETHEUS_STACK_CHART_VERSION="$kps_default" kps_run
kps_helm="$(grep '^helm upgrade --install kube-prometheus-stack' "$KUBECTL_LOG")"
if [ -n "$kps_default" ] && printf '%s' "$kps_helm" | grep -q -- "--version $kps_default"; then
    ok "kube-prometheus-stack is installed at the pinned chart version ($kps_default)"
else
    fail "kube-prometheus-stack must be installed with --version (install.sh default: '${kps_default}')" "$kps_helm"
fi
KUBE_PROMETHEUS_STACK_CHART_VERSION="" kps_run
if grep -q '^helm upgrade --install kube-prometheus-stack' "$KUBECTL_LOG"; then
    fail "an empty KUBE_PROMETHEUS_STACK_CHART_VERSION must refuse, not install unpinned" "$(cat "$WORK/kps.out")"
else
    ok "an empty KUBE_PROMETHEUS_STACK_CHART_VERSION refuses rather than installing unpinned"
fi

echo ""
echo "check-install-flow: $PASS passed, $FAIL failed"
if [ $((PASS + FAIL)) -ne "$EXPECT_CASES" ]; then
    echo "check-install-flow: $((PASS + FAIL)) case(s) ran, $EXPECT_CASES expected -- the run did not finish, or a case was added without updating EXPECT_CASES"
    exit 1
fi
[ "$FAIL" -eq 0 ]
