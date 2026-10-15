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
# namespaces that opted in, and nowhere else. Sourced by deploy/warmpool.sh,
# deploy/utilization-share.sh and deploy/install.sh.
#
# The Role is the same in every namespace; the RoleBinding is named for the
# controller namespace, so two controllers granted in one namespace do not
# overwrite each other's subject.
#
# Every function up to wva_pod_writer_marked_namespaces needs only kubectl; the
# two that read the optimizer's mode also need yq. The installer's two
# (wva_regrant_pod_writers, wva_remove_pod_writers) also log through log_info
# and log_warning, which deploy/install.sh defines.
#
# Run as a script, it prints the grant for an admin to apply:
#
#   bash deploy/lib/pod_writer.sh <namespace> <controller-namespace> | kubectl apply -f -

WVA_POD_WRITER_ROLE="wva-pod-writer"
WVA_POD_WRITER_LABEL="app.kubernetes.io/name=workload-variant-autoscaler"
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
    printf 'bash deploy/lib/pod_writer.sh %s %s | kubectl apply -f -   # from the repository root\n' "$1" "$2"
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
# cannot be asked (impersonating the controller is not allowed) and the grant
# this file makes cannot be seen whole either. Unknown is never no.
wva_pod_writes_allowed() {
    local ns="$1" controller_ns="$2" out binding
    out="$(kubectl auth can-i patch pods -n "$ns" \
        --as="system:serviceaccount:${controller_ns}:wva-controller-manager" 2>/dev/null)" || true
    case "$out" in
        yes) echo yes; return 0 ;;
        no|"no "*) echo no; return 0 ;;
    esac
    # Not allowed to ask. The grant this file makes is the next best answer,
    # and it is both halves: a RoleBinding whose Role was deleted grants
    # nothing, and neither does a Role of that name that was edited, or made
    # by someone else, without patch on pods.
    binding="$(wva_pod_writer_binding "$controller_ns" 2>/dev/null)" || { echo unknown; return 0; }
    if kubectl get rolebinding "$binding" -n "$ns" -o name >/dev/null 2>&1 \
        && wva_pod_writer_role_grants_patch "$ns"; then
        echo yes
    else
        echo unknown
    fi
}

# wva_pod_writer_role_grants_patch reports whether role/wva-pod-writer in $1
# has a rule granting patch on pods (wildcards count). A rule limited by
# resourceNames grants patch on those pods only, so it does not count: the
# controller patches pods it has never seen by name.
wva_pod_writer_role_grants_patch() {
    local rules
    rules="$(kubectl get role "$WVA_POD_WRITER_ROLE" -n "$1" \
        -o jsonpath='{range .rules[*]}{.apiGroups}{" "}{.resources}{" "}{.verbs}{" "}{.resourceNames}{"\n"}{end}' 2>/dev/null)" || return 1
    printf '%s\n' "$rules" | grep -qE '^\[[^]]*(""|"\*")[^]]*\] \[[^]]*("pods"|"\*")[^]]*\] \[[^]]*("patch"|"\*")[^]]*\] ?(\[\])?$'
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
# others. A trigger calls it when its scalerAddress starts with
# wva-external-scaler.<$1>. or wva-external-scaler.<$1>: -- the long and the
# short Service name.
#
# With $2 = unattributed it echoes "<namespace> <address>" instead, for each
# trigger that carries warmPoolName or modelID but whose address names no
# wva-external-scaler.<namespace> at all (an IP, another host, the bare
# Service name): which controller it calls cannot be told from the address.
#
# A cluster without the ScaledObject kind (SCALER_BACKEND=none, no KEDA) has
# none, and echoes nothing. Fails, with kubectl's message on stderr, when they
# cannot be listed cluster-wide -- an unreadable list is not an empty one.
wva_pod_writer_namespaces() {
    local controller_ns="$1" kind="$2" out
    local tmpl='{{range .items}}{{$ns := .metadata.namespace}}{{range .spec.triggers}}{{with .metadata}}{{$ns}} {{or .scalerAddress "-"}} {{or .warmPoolName "-"}} {{or .modelID "-"}}{{"\n"}}{{end}}{{end}}{{end}}'
    if ! out="$(kubectl get scaledobject -A -o go-template="$tmpl" 2>&1)"; then
        case "$out" in
            *"doesn't have a resource type"*) return 0 ;;
        esac
        printf '%s\n' "$out" >&2
        return 1
    fi
    printf '%s\n' "$out" | awk -v dot="wva-external-scaler.${controller_ns}." \
        -v colon="wva-external-scaler.${controller_ns}:" -v kind="$kind" '
        NF >= 3 {
            model = (NF >= 4) ? $4 : "-"
            if (index($2, dot) == 1 || index($2, colon) == 1) {
                if (kind == "pools" && $3 != "-") print $1
                if (kind == "models" && $3 == "-") print $1
                next
            }
            if (kind == "unattributed" && ($3 != "-" || model != "-") && $2 !~ /^wva-external-scaler\.[^.:]+[.:]/)
                print $1 " " $2
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

# us_block_state -- off, shadow or active, or other:<type> for an optimizer
# block deploy/utilization-share.sh does not own (other:not-a-map when it is not
# a map), for the policy on stdin. Then a second word: physical when the block
# also plans inventory-only groups. Needs yq. Here rather than in
# deploy/utilization-share.sh so the installer reads a live policy the same way.
us_block_state() {
    local p tag type shadow physical mode
    p="$(cat)"
    tag="$(printf '%s\n' "$p" | yq -r '.optimizer | tag')" || return 1
    case "$tag" in
        '!!null') echo "off plain"; return 0 ;;
        '!!map') ;;
        *) echo "other:not-a-map plain"; return 0 ;;
    esac
    type="$(printf '%s\n' "$p" | yq -r '.optimizer.type // ""')" || return 1
    shadow="$(printf '%s\n' "$p" | yq -r '(.optimizer.utilizationShare.shadow // false) | tostring')" || return 1
    physical="$(printf '%s\n' "$p" | yq -r '(.optimizer.utilizationShare.physicalGroups // false) | tostring')" || return 1
    if [ -z "$type" ]; then
        mode=off
    elif [ "$type" != utilizationShare ]; then
        mode="other:$type"
    elif [ "$shadow" = true ]; then
        mode=shadow
    else
        mode=active
    fi
    if [ "$physical" = true ]; then echo "$mode physical"; else echo "$mode plain"; fi
}

# wva_live_optimizer_mode echoes the utilization-share mode the controller in
# $1 runs with now: off, invalid, shadow or active (or other:<type>).
#
# First from the status it publishes (ConfigMap wva-controller-status, key
# utilizationShare), which is what it applied. A controller too old to publish
# one is read from its policy instead: the default entry of
# wva-scaling-policy-config in its own namespace, which is where a
# cluster-scoped controller reads it. Fails, with kubectl's message on stderr,
# when neither can be read: unknown is not off.
wva_live_optimizer_mode() {
    local ns="$1" out state
    if out="$(kubectl get configmap wva-controller-status -n "$ns" -o jsonpath='{.data.utilizationShare}' 2>&1)"; then
        if [ -n "$out" ]; then
            printf '%s\n' "$out"
            return 0
        fi
    else
        case "$out" in
            *NotFound*|*"not found"*) ;;
            *) printf '%s\n' "$out" >&2; return 1 ;;
        esac
    fi
    if ! out="$(kubectl get configmap wva-scaling-policy-config -n "$ns" -o jsonpath='{.data.default}' 2>&1)"; then
        case "$out" in
            *NotFound*|*"not found"*) echo off; return 0 ;;
        esac
        printf '%s\n' "$out" >&2
        return 1
    fi
    state="$(printf '%s\n' "$out" | us_block_state 2>&1)" || { printf 'the policy in %s does not parse: %s\n' "$ns" "$state" >&2; return 1; }
    printf '%s\n' "${state% *}"
}

# wva_regrant_pod_writers carries the pod-write grant across an upgrade of the
# cluster-scoped controller in $1.
#
# An install from before role/wva-pod-writer had `patch` on pods in its
# ClusterRole, and this release's apply takes it away. A warm pool created then
# brought no grant of its own, so its controller disables it; an active
# utilization-share optimizer can no longer clear the marks it wrote, which stay
# on the pods, and an active one moves no pod at all. So the grant is made
# here, BEFORE the apply that removes the old one, in each namespace that holds
# a warm pool calling this controller, a marked pod of a model calling it, or --
# when the optimizer is active -- any model calling it. Where it cannot be
# made, each namespace is named with the command an admin runs. Where the
# namespaces or the optimizer's mode cannot be read, that is said too: unknown
# is not none.
wva_regrant_pod_writers() {
    local controller_ns="$1" pools models marked want ns err="" unread=0 hint mode odd
    hint="$(wva_pod_writer_admin_hint '<namespace>' "$controller_ns")"
    if ! pools="$(wva_pod_writer_namespaces "$controller_ns" pools 2>&1)"; then
        unread=1 err="$pools"
    elif ! models="$(wva_pod_writer_namespaces "$controller_ns" models 2>&1)"; then
        unread=1 err="$models"
    fi
    if [ "$unread" -eq 1 ]; then
        log_warning "Could not list ScaledObjects cluster-wide, so the namespaces whose warm pool or utilization-share optimizer needs role/${WVA_POD_WRITER_ROLE} are UNKNOWN: $err"
        log_warning "  This release removes patch on pods from the controller's ClusterRole. In each namespace with a warm pool or an active optimizer, run: $hint"
        return 0
    fi
    if odd="$(wva_pod_writer_namespaces "$controller_ns" unattributed 2>/dev/null)" && [ -n "$odd" ]; then
        log_warning "These ScaledObject triggers declare a warm pool or a model, but their scalerAddress does not say which controller they call, so no grant was made for them: $(printf '%s\n' "$odd" | awk '{printf "%s (%s) ", $1, $2}')"
        log_warning "  If one calls the controller in $controller_ns, run in its namespace: $hint"
    fi
    want="$pools"
    if mode="$(wva_live_optimizer_mode "$controller_ns" 2>&1)"; then
        if [ "$mode" = active ]; then
            # Active moves the deletion cost of any model's pods, marks or not.
            want="$(printf '%s\n%s\n' "$want" "$models")"
        fi
    else
        log_warning "Could not tell whether the utilization-share optimizer is active: $mode"
        if [ -n "$models" ]; then
            log_warning "  If it is, run this for each namespace of its models ($(printf '%s ' $models)): $hint"
        fi
    fi
    if ! marked="$(wva_pod_writer_marked_namespaces 2>&1)"; then
        log_warning "Could not list pods cluster-wide, so the namespaces holding utilization-share transfer marks are UNKNOWN: $marked"
        if [ -n "$models" ] && [ "$mode" != active ]; then
            log_warning "  If the optimizer is active, run this for each namespace of its models ($(printf '%s ' $models)): $hint"
        fi
    else
        # Marked pods of models this controller scales. A mark in a namespace
        # it does not scale belongs to another controller.
        want="$(printf '%s\n%s\n' "$want" "$(comm -12 <(printf '%s\n' "$marked" | sort -u) <(printf '%s\n' "$models" | sort -u))")"
    fi
    want="$(printf '%s\n' "$want" | grep -v '^$' | sort -u || true)"
    [ -n "$want" ] || return 0
    for ns in $want; do
        if err="$(wva_grant_pod_writes "$ns" "$controller_ns" 2>&1)"; then
            log_info "Kept the controller's patch on pods in $ns (role/${WVA_POD_WRITER_ROLE}): its warm pool or the utilization-share optimizer needs it"
        else
            log_warning "Could not grant the controller patch on pods in $ns (role/${WVA_POD_WRITER_ROLE}): $err"
            log_warning "  Its warm pool stops lending, or the optimizer cannot move or clear marks on its pods there, until an admin runs: $(wva_pod_writer_admin_hint "$ns" "$controller_ns")"
        fi
    done
}

# wva_remove_pod_writers removes, on uninstall, the grant made for the
# controller in $1 in every namespace: its RoleBinding, and the Role once no
# other controller's binding uses it. Found by label across the cluster;
# whatever cannot be read or deleted is listed, never passed over.
#
# Extra arguments are namespaces to search one by one when the cluster-wide
# list is refused -- for a namespace-scoped install, the namespaces it runs in
# and manages, which are the only ones its grant is ever made in. With none,
# or when one of them cannot be read either, the leftovers are UNKNOWN, and
# the warning says how to find them.
wva_remove_pod_writers() {
    local controller_ns="$1" binding list one ns others removed="" failed="" err unread=""
    shift
    if ! binding="$(wva_pod_writer_binding "$controller_ns" 2>&1)"; then
        log_warning "Left role/${WVA_POD_WRITER_ROLE} and its RoleBindings in place: $binding"
        return 0
    fi
    local tmpl='{{range .items}}{{.metadata.namespace}} {{.metadata.name}}{{"\n"}}{{end}}'
    if ! list="$(kubectl get rolebinding -A -l "$WVA_POD_WRITER_LABEL" -o go-template="$tmpl" 2>&1)"; then
        unread="cluster-wide ($list)" list=""
        if [ $# -gt 0 ]; then
            unread=""
            for ns in $(printf '%s\n' "$@" | grep -v '^$' | sort -u); do
                if one="$(kubectl get rolebinding -n "$ns" -l "$WVA_POD_WRITER_LABEL" -o go-template="$tmpl" 2>&1)"; then
                    list="$list
$one"
                else
                    unread="$unread $ns ($one)"
                fi
            done
        fi
    fi
    if [ -n "$unread" ]; then
        log_warning "Could not list RoleBindings in: ${unread# }. role/${WVA_POD_WRITER_ROLE} and rolebinding/${binding} may be left there."
        log_warning "  A cluster admin finds them with:"
        log_warning "    kubectl get rolebinding -A --field-selector metadata.name=${binding} -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name"
        log_warning "  and deletes each: kubectl delete rolebinding ${binding} -n <namespace>"
    fi
    for ns in $(printf '%s\n' "$list" | awk -v b="$binding" '$2 == b {print $1}' | sort -u); do
        if ! err="$(kubectl delete rolebinding "$binding" -n "$ns" --ignore-not-found 2>&1 >/dev/null)"; then
            failed="$failed
    rolebinding/$binding in $ns: $err
      kubectl delete rolebinding $binding -n $ns"
            continue
        fi
        others="$(printf '%s\n' "$list" | awk -v n="$ns" -v b="$binding" -v p="${WVA_POD_WRITER_ROLE}-" \
            '$1 == n && $2 != b && index($2, p) == 1 {print $2}')"
        if [ -n "$others" ]; then
            removed="$removed $ns(binding; role kept for $(printf '%s ' $others | sed 's/ $//'))"
            continue
        fi
        if err="$(kubectl delete role "$WVA_POD_WRITER_ROLE" -n "$ns" --ignore-not-found 2>&1 >/dev/null)"; then
            removed="$removed $ns"
        else
            removed="$removed $ns(binding)"
            failed="$failed
    role/${WVA_POD_WRITER_ROLE} in $ns: $err
      kubectl delete role ${WVA_POD_WRITER_ROLE} -n $ns"
        fi
    done
    [ -z "$removed" ] || log_info "Removed the controller's patch on pods (role/${WVA_POD_WRITER_ROLE}, rolebinding/${binding}) in:${removed}"
    if [ -n "$failed" ]; then
        log_warning "Could not remove the controller's pod-write grant here. A cluster admin runs the command under each:${failed}"
    fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    if [ $# -ne 2 ]; then
        echo "usage: $0 <namespace> <controller-namespace>   prints role/${WVA_POD_WRITER_ROLE} and its RoleBinding; pipe it to kubectl apply -f -" >&2
        exit 2
    fi
    wva_pod_writer_manifest "$1" "$2"
fi
