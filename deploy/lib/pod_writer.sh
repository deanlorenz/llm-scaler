#!/usr/bin/env bash
#
# The controller's one pod-writing grant, made per namespace.
#
# The controller patches pods in two places only: the warm pool, writing an
# InferencePool's selector labels and a deletion cost onto pool Pods it lends,
# and the utilization-share optimizer, writing a deletion cost and its transfer
# mark onto pods of models it manages. Both are namespaced and both are opt-in.
#
# The ClusterRole used to carry `patch` on pods for every namespace. A Pod's
# image is mutable on a live Pod, so that one verb let a stolen controller token
# run code as any ServiceAccount in the cluster, kube-system included. The
# ClusterRole now only reads pods; these functions grant `patch` in the
# namespaces that opted in, and nowhere else. Sourced by deploy/warmpool.sh and
# deploy/utilization-share.sh. Needs only kubectl.
#
# The Role is the same in every namespace; the RoleBinding is named for the
# controller namespace, so two controllers granted in one namespace do not
# overwrite each other's subject.
#
# Run as a script, it prints the grant for an admin to apply:
#
#   bash deploy/lib/pod_writer.sh <namespace> <controller-namespace> | kubectl apply -f -

WVA_POD_WRITER_ROLE="wva-pod-writer"
WVA_TRANSFER_MARK="llm-d.ai/utilization-share-transfer"

# wva_pod_writer_suffix echoes the 8-character suffix for controller namespace $1.
# With neither sha256sum nor shasum it refuses, rather than echoing nothing and
# naming every controller's binding `wva-pod-writer-`.
wva_pod_writer_suffix() {
    local sum
    if command -v sha256sum >/dev/null 2>&1; then
        sum="$(printf '%s' "$1" | sha256sum)" || return 1
    elif command -v shasum >/dev/null 2>&1; then
        sum="$(printf '%s' "$1" | shasum -a 256)" || return 1
    else
        echo "ERROR: neither sha256sum nor shasum is installed, so the RoleBinding for role/${WVA_POD_WRITER_ROLE} cannot be named. Install one (coreutils or perl) and re-run." >&2
        return 1
    fi
    sum="${sum:0:8}"
    [ "${#sum}" -eq 8 ] || { echo "ERROR: could not hash '$1' for the role/${WVA_POD_WRITER_ROLE} binding name" >&2; return 1; }
    printf '%s\n' "$sum"
}

# wva_pod_writer_binding echoes the RoleBinding name for controller namespace $1.
wva_pod_writer_binding() {
    local suffix
    suffix="$(wva_pod_writer_suffix "$1")" || return 1
    printf '%s-%s\n' "$WVA_POD_WRITER_ROLE" "$suffix"
}

# wva_pod_writer_manifest prints the Role and RoleBinding that let the
# controller in namespace $2 (ServiceAccount wva-controller-manager) patch pods
# in namespace $1. Prints nothing and fails when the binding cannot be named.
wva_pod_writer_manifest() {
    local ns="$1" controller_ns="$2" binding
    binding="$(wva_pod_writer_binding "$controller_ns")" || return 1
    cat <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${WVA_POD_WRITER_ROLE}
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: workload-variant-autoscaler
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${binding}
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: workload-variant-autoscaler
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${WVA_POD_WRITER_ROLE}
subjects:
- kind: ServiceAccount
  name: wva-controller-manager
  namespace: ${controller_ns}
YAML
}

# wva_pod_writer_admin_hint echoes the command an admin runs to make the grant
# in namespace $1 for the controller in $2.
wva_pod_writer_admin_hint() {
    printf 'bash deploy/lib/pod_writer.sh %s %s | kubectl apply -f -\n' "$1" "$2"
}

# wva_grant_pod_writes applies that grant. Returns kubectl's status, with its
# output on stderr, so the caller decides whether a refusal is fatal.
wva_grant_pod_writes() {
    local manifest
    manifest="$(wva_pod_writer_manifest "$1" "$2")" || return 1
    printf '%s\n' "$manifest" | kubectl apply -f - >/dev/null
}

# wva_pod_writes_allowed echoes yes when the controller in $2 may already patch
# pods in $1, no when the API server says it may not, and unknown when that
# cannot be asked (impersonating the controller is not allowed) and no
# role/wva-pod-writer binding for it can be seen either. Unknown is never no.
wva_pod_writes_allowed() {
    local ns="$1" controller_ns="$2" out binding
    out="$(kubectl auth can-i patch pods -n "$ns" \
        --as="system:serviceaccount:${controller_ns}:wva-controller-manager" 2>/dev/null)" || true
    case "$out" in
        yes) echo yes; return 0 ;;
        no|"no "*) echo no; return 0 ;;
    esac
    # Not allowed to ask. The binding this grant makes is the next best answer.
    binding="$(wva_pod_writer_binding "$controller_ns" 2>/dev/null)" || { echo unknown; return 0; }
    if kubectl get rolebinding "$binding" -n "$ns" -o name >/dev/null 2>&1; then
        echo yes
    else
        echo unknown
    fi
}

# wva_ensure_pod_writes makes sure the controller in $2 may patch pods in $1:
# nothing when it already may, the grant otherwise. Sets WVA_POD_WRITER_RESULT
# (already, granted, forbidden or failed) and WVA_POD_WRITER_ERR (what kubectl
# said). Returns 0 for already and granted. Call it plainly, never inside
# $( ): it reports through those two variables.
WVA_POD_WRITER_RESULT=""
WVA_POD_WRITER_ERR=""
wva_ensure_pod_writes() {
    local ns="$1" controller_ns="$2" manifest
    WVA_POD_WRITER_RESULT="" WVA_POD_WRITER_ERR=""
    if [ "$(wva_pod_writes_allowed "$ns" "$controller_ns")" = yes ]; then
        WVA_POD_WRITER_RESULT=already
        return 0
    fi
    if ! manifest="$(wva_pod_writer_manifest "$ns" "$controller_ns" 2>&1)"; then
        WVA_POD_WRITER_RESULT=failed WVA_POD_WRITER_ERR="$manifest"
        return 1
    fi
    if WVA_POD_WRITER_ERR="$(printf '%s\n' "$manifest" | kubectl apply -f - 2>&1 >/dev/null)"; then
        WVA_POD_WRITER_RESULT=granted WVA_POD_WRITER_ERR=""
        return 0
    fi
    case "$WVA_POD_WRITER_ERR" in
        *[Ff]orbidden*|*"cannot create"*|*"cannot patch"*|*"cannot get"*|*escalat*) WVA_POD_WRITER_RESULT=forbidden ;;
        *) WVA_POD_WRITER_RESULT=failed ;;
    esac
    return 1
}

# wva_pod_writer_namespaces echoes, one per line and sorted, the namespaces
# holding ScaledObjects that call the controller in $1: with $2 = pools, those
# declaring a warm pool (trigger metadata warmPoolName); with $2 = models, the
# others. Fails, with kubectl's message on stderr, when they cannot be listed
# cluster-wide -- an unreadable list is not an empty one.
wva_pod_writer_namespaces() {
    local controller_ns="$1" kind="$2" out
    local tmpl='{{range .items}}{{$ns := .metadata.namespace}}{{range .spec.triggers}}{{with .metadata}}{{$ns}} {{or .scalerAddress "-"}} {{or .warmPoolName "-"}}{{"\n"}}{{end}}{{end}}{{end}}'
    out="$(kubectl get scaledobject -A -o go-template="$tmpl" 2>&1)" || { printf '%s\n' "$out" >&2; return 1; }
    printf '%s\n' "$out" | awk -v addr="wva-external-scaler.${controller_ns}." -v kind="$kind" '
        NF == 3 && index($2, addr) == 1 {
            if (kind == "pools" && $3 != "-") print $1
            if (kind == "models" && $3 == "-") print $1
        }' | sort -u
}

# wva_pod_writer_marked_namespaces echoes the namespaces holding a pod with the
# utilization-share transfer mark, one per line and sorted. Fails, with
# kubectl's message on stderr, when pods cannot be listed cluster-wide.
wva_pod_writer_marked_namespaces() {
    local out
    local tmpl='{{range .items}}{{$ns := .metadata.namespace}}{{with .metadata.annotations}}{{if index . "'"$WVA_TRANSFER_MARK"'"}}{{$ns}}{{"\n"}}{{end}}{{end}}{{end}}'
    out="$(kubectl get pods -A -o go-template="$tmpl" 2>&1)" || { printf '%s\n' "$out" >&2; return 1; }
    printf '%s\n' "$out" | grep -v '^$' | sort -u || true
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    if [ $# -ne 2 ]; then
        echo "usage: $0 <namespace> <controller-namespace>   prints role/${WVA_POD_WRITER_ROLE} and its RoleBinding; pipe it to kubectl apply -f -" >&2
        exit 2
    fi
    wva_pod_writer_manifest "$1" "$2"
fi
