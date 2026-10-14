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
for lib in common.sh infra_wva.sh; do
    tr -d '\r' < "deploy/lib/$lib" > "$WORK/$lib"
done
BLUE='' GREEN='' YELLOW='' RED='' NC=''
# shellcheck source=/dev/null
source "$WORK/common.sh"
# shellcheck source=/dev/null
source "$WORK/infra_wva.sh"
for fn in wva_append_crb_name_patches wva_append_metrics_reader_subject_patch \
          wva_append_kueue_reader_scope_patch wva_append_policy_namespace_reader_scope_patch \
          wva_rendered_prereq_objects; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=23
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
