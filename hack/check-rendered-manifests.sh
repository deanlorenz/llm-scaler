#!/usr/bin/env bash
#
# Renders the shipped overlays, and the overlay the installer builds on top of
# them, and asserts properties of what would be applied.
#
# The kustomize-build CI job proves each overlay BUILDS; nothing asserted what
# it builds. A grant to an account that does not exist, or a hardening field a
# component patch quietly drops, renders without complaint.
#
# Needs kustomize ($KUSTOMIZE, else bin/kustomize, else `kustomize`, else
# `kubectl kustomize`) and yq. Missing either is FATAL, never a skip.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

command -v yq >/dev/null 2>&1 || { printf 'FATAL: yq is required.\n' >&2; exit 1; }
if [ -n "${KUSTOMIZE:-}" ]; then
    KBUILD=("$KUSTOMIZE" build)
elif [ -x bin/kustomize ]; then
    KBUILD=(bin/kustomize build)
elif command -v kustomize >/dev/null 2>&1; then
    KBUILD=(kustomize build)
elif command -v kubectl >/dev/null 2>&1; then
    KBUILD=(kubectl kustomize)
else
    printf 'FATAL: kustomize is required (make kustomize).\n' >&2
    exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh infra_wva.sh install_core.sh; do
    tr -d '\r' < "deploy/lib/$lib" > "$WORK/$lib"
done
BLUE='' GREEN='' YELLOW='' RED='' NC=''
# shellcheck source=/dev/null
source "$WORK/common.sh"
# shellcheck source=/dev/null
source "$WORK/infra_wva.sh"
# shellcheck source=/dev/null
source "$WORK/install_core.sh"
for fn in wva_append_crb_name_patches wva_append_metrics_reader_subject_patch \
          wva_append_kueue_reader_scope_patch wva_append_policy_namespace_reader_scope_patch \
          wva_rendered_prereq_objects wva_prometheus_scraper_sa wva_missing_prereqs; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=41
RAN=0
FAILED=0

check() {
    if [ "$2" = "$3" ]; then
        RAN=$((RAN + 1))
        printf 'ok    %s\n' "$1"
    else
        FAILED=$((FAILED + 1))
        printf 'FAIL  %s\n        want [%s]\n        got  [%s]\n' "$1" "$2" "$3" >&2
    fi
}

# render_installer <scraper> renders the cluster-scoped Kubernetes overlay the
# way deploy_wva_controller wraps it, with the metrics-reader subject decided.
render_installer() {
    local dir
    dir="$(mktemp -d "$WORK/ovl.XXXXXX")"
    ln -s "$ROOT/config/overlays/cluster-scoped/kubernetes" "$dir/base"
    printf 'namespace: wns\nresources:\n- ./base\n' > "$dir/kustomization.yaml"
    wva_append_crb_name_patches "$dir/kustomization.yaml" wns
    wva_append_metrics_reader_subject_patch "$dir/kustomization.yaml" wns "$1"
    "${KBUILD[@]}" "$dir"
}

reader_subjects() {
    yq -N -r 'select(.kind == "ClusterRoleBinding" and (.metadata.name | test("^wva-metrics-reader-rolebinding"))) | .subjects[] | .namespace + "/" + .name'
}

# ----------------------------------------------------------------------------
# The controller /metrics grant names the Prometheus actually here
# ----------------------------------------------------------------------------
out="$(render_installer 'obs prom-sa')" || out=''
[ -n "$out" ] || { printf 'FATAL: the installer overlay did not render.\n' >&2; exit 1; }
check 'metrics-reader: the detected Prometheus is the only subject' \
    'obs/prom-sa' "$(printf '%s\n' "$out" | reader_subjects)"

out="$(render_installer '')" || out=''
[ -n "$out" ] || { printf 'FATAL: the installer overlay did not render.\n' >&2; exit 1; }
check 'metrics-reader: no Prometheus known -> no binding at all' \
    '' "$(printf '%s\n' "$out" | reader_subjects)"
# The delete must take only that binding: the cluster-scoped overlay renders
# three others (manager, metrics-auth, epp-metrics-reader).
check 'metrics-reader: the other bindings survive the delete' \
    '3' \
    "$(printf '%s\n' "$out" | yq -N -r 'select(.kind == "ClusterRoleBinding") | .metadata.name' | grep -c '^wva-')"

# ----------------------------------------------------------------------------
# The expected set of admin-owned objects makes the install's own decision
# ----------------------------------------------------------------------------
# wva_rendered_prereq_objects is what the controller-only phase requires to
# exist. It listed the metrics-reader binding even when the install had DROPPED
# it (no Prometheus account it trusts), so that phase reported it missing and
# refused, forever. Rendered here through the real function, with only kubectl
# (the kustomize build and the Prometheus CR listing) and the URL detection
# stubbed.
prereq_objects() {
    (
        WVA_PROJECT="$ROOT" WVA_NS=wns ENVIRONMENT=kubernetes WVA_SCOPE="$1" WVA_WATCH_NS=''
        PROMETHEUS_SA="$2" PROMETHEUS_URL='https://prom.obs.svc:9090'
        PROMETHEUS_URL_EXPLICIT=1 DEPLOY_PROMETHEUS=false
        kubectl() {
            case "$1" in
                # `command`: KBUILD may itself be `kubectl kustomize`.
                kustomize) command "${KBUILD[@]}" "$2" ;;
                *) return 0 ;;   # no Prometheus CR anywhere
            esac
        }
        wva_rendered_prereq_objects
    )
}
reader_crb="ClusterRoleBinding wva-metrics-reader-rolebinding-$(wva_ns_suffix wns)"
for scope in cluster namespace; do
    objs="$(prereq_objects "$scope" '')"
    [ -n "$objs" ] || { printf 'FATAL: wva_rendered_prereq_objects rendered nothing (%s scope).\n' "$scope" >&2; exit 1; }
    check "$scope scope: no trusted Prometheus account -> the binding is not expected" '0' \
        "$(printf '%s\n' "$objs" | grep -cxF "$reader_crb")"
    check "$scope scope: PROMETHEUS_SA -> the binding is expected" '1' \
        "$(prereq_objects "$scope" obs/prom-sa | grep -cxF "$reader_crb")"
done

# ----------------------------------------------------------------------------
# Who the metrics-reader binding names: only an account the caller CHOSE
# ----------------------------------------------------------------------------
# scraper <PROMETHEUS_SA> <PROMETHEUS_URL> <explicit: 1|''> <DEPLOY_PROMETHEUS>
# runs wva_prometheus_scraper_sa against a stub that knows two Prometheus CRs:
# one in `obs` running as prom-sa, one in `dflt` with no serviceAccountName.
# An AUTO-DETECTED URL is whichever Prometheus the API listed first -- on a
# shared cluster someone else's -- and must never decide the subject.
scraper() {
    (
        PROMETHEUS_SA="$1" PROMETHEUS_URL="$2" PROMETHEUS_URL_EXPLICIT="$3" DEPLOY_PROMETHEUS="$4"
        MONITORING_NAMESPACE=wva-mon
        log_warning() { :; }
        kubectl() {
            case "$*" in
                "get prometheuses.monitoring.coreos.com -n obs "*) echo "obs prom-sa" ;;
                "get prometheuses.monitoring.coreos.com -n dflt "*) echo "dflt " ;;
                *) return 0 ;;
            esac
        }
        wva_prometheus_scraper_sa
    )
}
check 'metrics-reader subject: an auto-detected Prometheus is never granted' '' \
    "$(scraper '' 'https://prometheus-operated.obs.svc.cluster.local:9090' '' false)"
check 'metrics-reader subject: auto-detected beside DEPLOY_PROMETHEUS, outside MONITORING_NAMESPACE: none' '' \
    "$(scraper '' 'https://prometheus-operated.obs.svc.cluster.local:9090' '' true)"
check 'metrics-reader subject: an explicit PROMETHEUS_URL reads its Prometheus CR' 'obs prom-sa' \
    "$(scraper '' 'https://prometheus-operated.obs.svc.cluster.local:9090' 1 false)"
check 'metrics-reader subject: explicit URL, CR on the default account: none' '' \
    "$(scraper '' 'https://prometheus-operated.dflt.svc:9090' 1 false)"
check 'metrics-reader subject: PROMETHEUS_SA is taken as given' 'ops scraper' \
    "$(scraper 'ops/scraper' 'https://prometheus-operated.obs.svc:9090' '' false)"
check 'metrics-reader subject: the stack this install deploys' 'wva-mon kube-prometheus-stack-prometheus' \
    "$(scraper '' 'https://kube-prometheus-stack-prometheus.wva-mon.svc.cluster.local:9090' '' true)"

# ----------------------------------------------------------------------------
# --check and INSTALL_PHASE=wva: objects an admin phase may leave out ON PURPOSE
# ----------------------------------------------------------------------------
# The external-scaler NetworkPolicy (written only where KEDA's namespace is
# known) and the metrics-reader binding (only for a Prometheus account the
# caller named) are absent on a correct install, and a tenant cannot see why.
# Reported MISSING, they made --check fail forever and INSTALL_PHASE=wva refuse.
missing_prereqs() {
    (
        WVA_PROJECT="$ROOT" WVA_NS=wns ENVIRONMENT=kubernetes WVA_SCOPE=namespace WVA_WATCH_NS=''
        PROMETHEUS_SA=obs/prom-sa PROMETHEUS_URL='https://prom.obs.svc:9090'
        PROMETHEUS_URL_EXPLICIT=1 DEPLOY_PROMETHEUS=false
        kubectl() {
            case "$1" in
                kustomize) command "${KBUILD[@]}" "$2" ;;
                get) echo "Error from server (NotFound): $2 \"$3\" not found" >&2; return 1 ;;
                *) return 0 ;;
            esac
        }
        wva_missing_prereqs
    )
}
mp="$(missing_prereqs)"
[ -n "$mp" ] || { printf 'FATAL: wva_missing_prereqs reported nothing on an empty cluster.\n' >&2; exit 1; }
check '--check: an absent external-scaler NetworkPolicy is OPTIONAL' '1' \
    "$(printf '%s\n' "$mp" | grep -c '^OPTIONAL NetworkPolicy/wva-external-scaler|')"
check '--check: an absent metrics-reader binding is OPTIONAL' '1' \
    "$(printf '%s\n' "$mp" | grep -c "^OPTIONAL ClusterRoleBinding/wva-metrics-reader-rolebinding-$(wva_ns_suffix wns)|")"
check '--check: neither is also reported missing' '0' \
    "$(printf '%s\n' "$mp" | grep -v '^OPTIONAL ' | grep -c 'wva-external-scaler\|wva-metrics-reader-rolebinding')"
check '--check: a required object is still MISSING, not optional' '1' \
    "$(printf '%s\n' "$mp" | grep -cx "ClusterRoleBinding/wva-metrics-auth-rolebinding-$(wva_ns_suffix wns)")"

# ----------------------------------------------------------------------------
# The namespace-scoped Kueue reader gets ONE Namespace: the managed one
# ----------------------------------------------------------------------------
# internal/kueue GETs a LocalQueue's Namespace by name, and a namespace-scoped
# controller lists LocalQueues in its managed namespace only.
render_kueue_role() {
    local dir overlay="$1" managed="$2"
    dir="$(mktemp -d "$WORK/ovl.XXXXXX")"
    ln -s "$ROOT/config/overlays/$overlay" "$dir/base"
    printf 'namespace: wns\nresources:\n- ./base\n' > "$dir/kustomization.yaml"
    wva_append_crb_name_patches "$dir/kustomization.yaml" wns
    wva_append_kueue_reader_scope_patch "$dir/kustomization.yaml" wns "$managed"
    "${KBUILD[@]}" "$dir" | yq -N "select(.kind == \"ClusterRole\" and .metadata.name == \"wva-kueue-reader-role-$(wva_ns_suffix wns)\")"
}
for overlay in namespace-scoped/kubernetes namespace-scoped/openshift; do
    role="$(render_kueue_role "$overlay" tenant-a)" || role=''
    [ -n "$role" ] || { printf 'FATAL: no kueue-reader ClusterRole rendered for %s.\n' "$overlay" >&2; exit 1; }
    check "$overlay: kueue-reader gets only the managed Namespace" 'tenant-a' \
        "$(printf '%s\n' "$role" | yq -r '.rules[] | select(.resources[] == "namespaces") | .resourceNames | join(",")')"
    check "$overlay: kueue-reader Kueue rules stay unnamed" 'true' \
        "$(printf '%s\n' "$role" | yq -r '[.rules[] | select(.apiGroups[] == "kueue.x-k8s.io") | has("resourceNames") | not] | all')"
done

# ----------------------------------------------------------------------------
# The namespace-scoped policy-namespace reader gets only the Namespaces it reads
# ----------------------------------------------------------------------------
# ResolvePolicyNamespace GETs wva-policy and the managed Namespace by name.
# enable-physical-limiter writes the SAME ClusterRole limited by resourceNames;
# an install that renders the shipped rule unnamed re-widened it to every
# Namespace in the cluster.
render_policy_ns_role() {
    local dir overlay="$1"
    shift
    dir="$(mktemp -d "$WORK/ovl.XXXXXX")"
    ln -s "$ROOT/config/overlays/$overlay" "$dir/base"
    printf 'namespace: wns\nresources:\n- ./base\n' > "$dir/kustomization.yaml"
    wva_append_crb_name_patches "$dir/kustomization.yaml" wns
    wva_append_policy_namespace_reader_scope_patch "$dir/kustomization.yaml" wns "$@"
    "${KBUILD[@]}" "$dir" | yq -N "select(.kind == \"ClusterRole\" and .metadata.name == \"wva-policy-namespace-reader-role-$(wva_ns_suffix wns)\")"
}
policy_ns_names() {
    yq -r '.rules[] | select(.resources[] == "namespaces") | .resourceNames // [] | sort | join(",")'
}
for overlay in namespace-scoped/kubernetes namespace-scoped/openshift; do
    # Self-managed, labelled to a policy namespace of its own.
    role="$(render_policy_ns_role "$overlay" wns '' platform-policy)" || role=''
    [ -n "$role" ] || { printf 'FATAL: no policy-namespace-reader ClusterRole rendered for %s.\n' "$overlay" >&2; exit 1; }
    check "$overlay: policy-namespace reader gets own, wva-policy and the labelled namespace" \
        'platform-policy,wns,wva-policy' "$(printf '%s\n' "$role" | policy_ns_names)"
    # Admin-owned, managing another namespace; WVA_POLICY_NS repeats wva-policy.
    role="$(render_policy_ns_role "$overlay" tenant-a wva-policy '')" || role=''
    [ -n "$role" ] || { printf 'FATAL: no policy-namespace-reader ClusterRole rendered for %s.\n' "$overlay" >&2; exit 1; }
    check "$overlay: policy-namespace reader names each Namespace once, nothing unread" \
        'tenant-a,wns,wva-policy' "$(printf '%s\n' "$role" | policy_ns_names)"
done

# ----------------------------------------------------------------------------
# The controller container runs on a read-only root filesystem, in every overlay
# ----------------------------------------------------------------------------
# Components patch this Deployment by index (args, env, volumes); a patch that
# replaces the securityContext or the volume list would drop either silently.
for overlay in cluster-scoped/kubernetes cluster-scoped/openshift \
               namespace-scoped/kubernetes namespace-scoped/openshift; do
    out="$("${KBUILD[@]}" "config/overlays/$overlay")" || out=''
    [ -n "$out" ] || { printf 'FATAL: %s did not render.\n' "$overlay" >&2; exit 1; }
    dep="$(printf '%s\n' "$out" | yq -N 'select(.kind == "Deployment" and .metadata.labels."control-plane" == "controller-manager")')"
    [ -n "$dep" ] || { printf 'FATAL: no controller Deployment in %s.\n' "$overlay" >&2; exit 1; }
    check "$overlay: manager has readOnlyRootFilesystem" 'true' \
        "$(printf '%s\n' "$dep" | yq -r '.spec.template.spec.containers[] | select(.name == "manager") | .securityContext.readOnlyRootFilesystem')"
    # /tmp writable, and from an emptyDir: a hostPath or a missing volume is not it.
    check "$overlay: /tmp is an emptyDir" 'emptyDir' \
        "$(printf '%s\n' "$dep" | yq -r '(.spec.template.spec.containers[] | select(.name == "manager") | .volumeMounts[] | select(.mountPath == "/tmp") | .name) as $v | .spec.template.spec.volumes[] | select(.name == $v) | keys | map(select(. != "name")) | .[0]')"
done

# ----------------------------------------------------------------------------
# No cluster-wide write on pods for the controller
# ----------------------------------------------------------------------------
# A Pod's image is mutable on a live Pod, so `patch` (or `update`) on pods
# granted cluster-wide lets a stolen controller token run code as any
# ServiceAccount, kube-system included. The controller writes pods only where a
# warm pool or the utilization-share optimizer runs, and gets that write per
# namespace (deploy/lib/pod_writer.sh).
for overlay in cluster-scoped/kubernetes cluster-scoped/openshift; do
    out="$("${KBUILD[@]}" "config/overlays/$overlay")" || out=''
    [ -n "$out" ] || { printf 'FATAL: %s did not render.\n' "$overlay" >&2; exit 1; }
    roles="$(printf '%s\n' "$out" | yq -N -r 'select(.kind == "ClusterRoleBinding" and ([.subjects[]? | select(.kind == "ServiceAccount" and .name == "wva-controller-manager")] | length > 0) and .roleRef.kind == "ClusterRole") | .roleRef.name' | sort -u)"
    [ -n "$roles" ] || { printf 'FATAL: no ClusterRole is bound to the controller in %s.\n' "$overlay" >&2; exit 1; }
    writes=0
    # One `contains` per (resource, verb) pair, chained selects, no `and`:
    # select(A and B) over two collected sub-lists passed EVERY rule under yq
    # 4.53, which reads as a grant that is not there. This form was checked
    # under 4.53 and the CI pin 4.44.3: it names pods for the old role and nothing
    # for this one.
    for r in $roles; do
        for res in pods '*'; do
            for verb in patch update '*'; do
                n="$(printf '%s\n' "$out" | R="$r" RES="$res" VERB="$verb" yq -N \
                    'select(.kind == "ClusterRole" and .metadata.name == strenv(R)) | .rules[] | select(.resources | contains([strenv(RES)])) | select(.verbs | contains([strenv(VERB)])) | .resources | join(",")' \
                    | grep -c .)"
                writes=$((writes + n))
            done
        done
    done
    check "$overlay: no ClusterRole bound to the controller writes pods cluster-wide" '0' "$writes"
done

# ----------------------------------------------------------------------------
# OpenShift: the ServiceMonitor scrapes as its own account, never the controller
# ----------------------------------------------------------------------------
# A legacy token Secret never expires. Minted for the controller's SA it was the
# controller's whole identity for anyone who could read Secrets in its
# namespace; the scrape needs only `get /metrics`.
for overlay in cluster-scoped/openshift namespace-scoped/openshift; do
    out="$("${KBUILD[@]}" "config/overlays/$overlay")" || out=''
    [ -n "$out" ] || { printf 'FATAL: %s did not render.\n' "$overlay" >&2; exit 1; }
    check "$overlay: no long-lived token Secret for the controller ServiceAccount" '0' \
        "$(printf '%s\n' "$out" | yq -N -r 'select(.kind == "Secret" and .type == "kubernetes.io/service-account-token") | .metadata.annotations."kubernetes.io/service-account.name"' | grep -cx 'wva-controller-manager')"
    sm_secret="$(printf '%s\n' "$out" | yq -N -r 'select(.kind == "ServiceMonitor" and .metadata.name == "wva-controller-manager-metrics-monitor") | .spec.endpoints[0].bearerTokenSecret.name')"
    check "$overlay: the ServiceMonitor token belongs to wva-metrics-scraper" 'wva-metrics-scraper' \
        "$(printf '%s\n' "$out" | yq -N -r "select(.kind == \"Secret\" and .metadata.name == \"${sm_secret}\") | .metadata.annotations.\"kubernetes.io/service-account.name\"")"
    check "$overlay: metrics-reader names wva-metrics-scraper alone" 'wva-system/wva-metrics-scraper' \
        "$(printf '%s\n' "$out" | reader_subjects)"
done

# ----------------------------------------------------------------------------

if [ "$FAILED" -gt 0 ]; then
    printf '\n%d rendered-manifest assertion(s) failed.\n' "$FAILED" >&2
    exit 1
fi
if [ "$RAN" -ne "$EXPECT_CASES" ]; then
    printf '\n%d case(s) reported ok, %d expected -- the run did not finish.\n' \
        "$RAN" "$EXPECT_CASES" >&2
    exit 1
fi
printf '\nRendered manifest checks passed (%d cases).\n' "$RAN"
