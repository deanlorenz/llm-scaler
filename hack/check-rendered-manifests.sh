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
for fn in wva_append_crb_name_patches wva_append_metrics_reader_subject_patch; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=11
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
