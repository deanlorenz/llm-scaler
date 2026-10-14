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
# deploy/utilization-share.sh. No dependencies.
#
# The Role is the same in every namespace; the RoleBinding is named for the
# controller namespace, so two controllers granted in one namespace do not
# overwrite each other's subject.

# wva_pod_writer_suffix echoes the 8-character suffix for controller namespace $1.
wva_pod_writer_suffix() {
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s' "$1" | sha256sum | cut -c1-8
    else
        printf '%s' "$1" | shasum -a 256 | cut -c1-8
    fi
}

# wva_pod_writer_manifest prints the Role and RoleBinding that let the
# controller in namespace $2 (ServiceAccount wva-controller-manager) patch pods
# in namespace $1.
wva_pod_writer_manifest() {
    local ns="$1" controller_ns="$2"
    cat <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: wva-pod-writer
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
  name: wva-pod-writer-$(wva_pod_writer_suffix "$controller_ns")
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: workload-variant-autoscaler
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: wva-pod-writer
subjects:
- kind: ServiceAccount
  name: wva-controller-manager
  namespace: ${controller_ns}
YAML
}

# wva_grant_pod_writes applies that grant. Returns kubectl's status, with its
# output on stderr, so the caller decides whether a refusal is fatal.
wva_grant_pod_writes() {
    wva_pod_writer_manifest "$1" "$2" | kubectl apply -f - >/dev/null
}
