#!/usr/bin/env bash
# Keep the engines' compile caches on every accelerator node's local disk, so
# a replica's start does not depend on a shared volume being writable.
#
# vLLM keeps three caches between runs -- the torch.compile artefacts, the
# FlashInfer autotune table and Triton's JIT cache -- and a replica that finds
# them warm skips the compile; one that does not pays it at every start.
# Pointing them at a shared RWX claim works until the claim does not: measured
# on one cluster, nine starts of the same pod spec split by node into 51-56 s
# and 75-81 s, and the 25 s was the shared claim being published read-only on
# some nodes by the CSI driver, the engine falling back to an empty cache
# under /tmp, and every start there compiling from nothing (14 s of
# torch.compile against 3 s from the cache, and 8 s more before the engine).
# This puts the caches on the node: one static hostPath PersistentVolume, one
# claim bound to it, and one DaemonSet that prepares the directory on every
# selected node and then reports Ready. The model servers mount the claim
# exactly as they mount any other; a hostPath is a bind mount with no
# storage driver in its path, and each node's cache is warm from the second
# start on that node -- or from the first, with --seed-claim, which copies a
# shared claim's caches onto each node as it is prepared. (The filesystem
# under it can still go read-only -- that is what the preparer's readiness
# and the engines' guard are for.)
#
#   enginecache.sh apply  -n NS --path DIR --image IMG [--seed-claim PVC[:SUBPATH]] [--dry-run]
#                                                                     prepare DIR/engine-cache on every accelerator node
#   enginecache.sh status -n NS [--node-selector KEY=VALUE]           which nodes are prepared, which are not, and why
#   enginecache.sh delete -n NS [--dry-run]                           drop the claim, volume and preparer (the caches stay)
#   enginecache.sh publish -n NS [--dry-run]                          fill the seed claim from every prepared node, then re-seed every node
#                                                                     (run once each engine configuration has started somewhere)
#
#   -n, --namespace NS          the namespace the claim and preparer live in
#
# Options:
#   --path DIR                  directory on the node, e.g. /mnt/local/weights/<ns>
#                               -- the same DIR weights.sh takes; the caches
#                               land under DIR/engine-cache (/var/mnt/weights/<ns>
#                               on RHCOS, under /var). What DIR must be:
#                                 * absolute, at least two components, no `..`,
#                                   no trailing slash; the whole of a top-level
#                                   directory is refused
#                                 * on a disk that is not the node's own, and
#                                   never a network mount: a hostPath is a
#                                   bind mount, and what is written there is
#                                   charged to no quota -- a fill is
#                                   DiskPressure for every pod on the node
#                                 * never a system path: /etc, /usr, /var/lib,
#                                   /var/log, /tmp, /home, /opt/bin and their
#                                   kind are refused, and so are /var/mnt and
#                                   /var/srv themselves (RHCOS's root disk;
#                                   mount the NVMe at a subdirectory first --
#                                   the script cannot tell a mountpoint from a
#                                   directory)
#                                 * one DIR per trust domain (see below: what
#                                   lands there is code every engine on the
#                                   node loads); on OpenShift one per project
#                                   is the only thing that works
#                                 * creatable by the preparer, which runs as
#                                   root on Kubernetes; on OpenShift prepare
#                                   DIR/engine-cache on each node first (see
#                                   the end of this text)
#                               The same DIR must exist on EVERY node the
#                               preparer selects (--node-selector; a node it
#                               cannot prepare shows in `status`, and an
#                               engine there costs a compile, not the
#                               replica, with the guard). Without a node
#                               directory at all, the engines can keep the
#                               caches on a shared ReadWriteMany claim
#                               instead -- `make workload-patch` with
#                               WVA_ENGINE_CACHE_CLAIM=<claim>[:<subPath>]
#                               writes that -- at the cost this script exists
#                               to remove: a CSI driver can publish that claim
#                               read-only on a node, and every start there
#                               then compiles. And what is on that claim is
#                               code every engine loads: whoever can write it
#                               runs code in every engine, on every node at
#                               once -- trust it as you would the engine image.
#   --image IMG                 image to prepare with: any image carrying /bin/sh
#                               -- the engine image itself is the natural
#                               choice, and it is then held on the node as a
#                               side effect
#   --capacity SIZE             the volume's declared capacity (default 200Gi;
#                               it is a declaration, hostPath has no quota)
#   --seed-claim PVC[:SUBPATH]  a claim in the namespace holding caches to
#                               copy onto each node before it is prepared --
#                               the shared claim the caches lived on before,
#                               or one filled once on purpose. Mounted
#                               read-only; copied without overwriting what
#                               the node already has; once per node (a
#                               marker). The caches are portable across
#                               nodes of one accelerator model, driver and
#                               engine version: without a seed the first
#                               start on EVERY node compiles (the scheduler
#                               spreads replicas, so that is one cold start
#                               per node), with one no node starts cold.
#                               What is copied is code the engines load (see
#                               below): the seed claim must be trusted as
#                               the cache itself is, and more -- one write
#                               to it reaches every node at the next
#                               prepare, not the one node a pod landed on.
#                               A changed seed is merged in too (the marker
#                               records the seed). A subPath that does not
#                               exist yet is nothing to seed, not an error.
#   --node-selector KEY=VALUE   which nodes count as accelerator nodes. The
#                               default is any node carrying a known GPU
#                               product label (deploy/lib/accelerator_nodes.sh);
#                               set this to what your model servers select
#                               on when they select on something
#   --toleration KEY            tolerate a taint with KEY (any value, any
#                               effect); nvidia.com/gpu is always tolerated
#   --dry-run                   apply: print the manifests instead of
#                               applying them; delete: print what would go
#
# The claim is named engine-cache, one per namespace (the caches inside are
# keyed by a hash of the engine config, so every model and flag set in the
# namespace has its own entry and none hits another's). A model server
# mounts it read-write at, say, /engine-cache and points VLLM_CACHE_ROOT,
# FLASHINFER_WORKSPACE_DIR and TRITON_CACHE_DIR under it -- and keeps a guard
# that unsets a variable whose directory is not writable, so a node that is
# not prepared costs a compile, not the replica. The volume is cluster-scoped:
# apply needs leave to create PersistentVolumes, which a namespace tenant does
# not have -- ask the cluster admin to run apply, or to create the volume.
# Nothing here uses a hostPath volume in a Pod (the preparer mounts the
# claim), so Pod Security "baseline" admits it and "restricted" does not. The
# node directory is cluster-shared, WORLD-writable state (1777 on the three
# cache directories, so any UID the engines run as can write; sticky, so one
# UID's files are not another's to remove), what is written there is charged
# to no quota, and what is written there is CODE: torch.compile artefacts,
# FlashInfer and Triton kernels that every engine on the node loads and
# runs. Whoever can write the directory runs code in every engine on that
# node -- on Kubernetes that is anyone with pods/create in a namespace that
# can mount the claim, which is no more than pods/create already grants,
# and across namespaces it is everyone pointed at the same DIR. So the
# rules are weights.sh's, harder: one DIR per trust domain, on a disk that
# is not the node's own, and wipe DIR/engine-cache on the nodes when a
# namespace or a directory changes hands -- delete keeps the caches, and a
# new tenant of the same name and DIR would load the old one's. 1777 is a
# trade-off, taken so the engines need no fsGroup; a namespace that runs
# its engines under one UID and wants the separation can chown the three
# directories to it and chmod them 1770 on each node. The preparer runs as
# its own ServiceAccount, engine-cache-preparer.
#
# On OpenShift -- NOT YET RUN THERE -- restricted-v2 runs the preparer and
# the engines as the project's range UID with GID 0, and the kubelet does not
# relabel a hostPath: prepare DIR/engine-cache on each node as weights.sh
# --help says (chgrp 0, chmod 2775, chcon -t container_file_t, or a
# MachineConfig mount with context=...:container_file_t:s0). The preparer
# then creates the three directories inside it as that UID, owns them, and
# its chmod 1777 takes (the setgid bit is not inherited by a mode set
# outright; the files carry the project's SELinux level either way). `status`
# says when the preparer died on a permission error.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'
# shellcheck source=lib/common.sh
source "$HERE/lib/common.sh"
# shellcheck source=lib/accelerator_nodes.sh
source "$HERE/lib/accelerator_nodes.sh"
# shellcheck source=lib/nodedir.sh
source "$HERE/lib/nodedir.sh"
# NOTE: log_error EXITS. Nothing may follow it that needs to run.

NAMESPACE=""
NODE_PATH=""
IMAGE=""
CAPACITY="200Gi"
NODE_SELECTOR=""          # empty: every node carrying a known GPU product label
NODE_SELECTOR_GIVEN=false
TOLERATIONS=("nvidia.com/gpu")
DRY_RUN=false
LABEL_COMPONENT="app.kubernetes.io/component=node-local-engine-cache,app.kubernetes.io/managed-by=wva-enginecache"
STORAGE_CLASS="node-local-engine-cache"
CLAIM="engine-cache"
SUBDIR="engine-cache"
MARKER=".prepared"
SEED_MARKER=".seeded"
SEED_CLAIM=""
SEED_SUBPATH=""
SA_NAME="engine-cache-preparer"

usage() {
    sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//; $d'
    exit 0
}

# pv_for is the volume's name. PersistentVolumes are cluster-scoped, so the
# namespace and the directory are in the hash: two namespaces preparing the
# same directory each get their own volume, bound to their own claim.
pv_for() {
    printf '%s-%s' "$CLAIM" "$(wva_ns_suffix "${NAMESPACE}/${NODE_PATH}")"
}

check_path() {
    local why
    why="$(nodedir_ok "$1")" || log_error "--path: ${why}"
}

check_image() {
    case "$1" in
        "") log_error "--image must not be empty" ;;
        *[!A-Za-z0-9._:/@-]*) log_error "not an image reference: '$1' (registry/path[:tag|@sha256:digest], no spaces or quotes)" ;;
    esac
}

# preparer_script is the preparer's command. A quoted heredoc: nothing in it
# is bash's to evaluate at render time -- it runs on the node, as written. A
# `$(...)` here would otherwise run on the operator's machine at apply and
# bake its output into every node's manifest. The markers' names and the
# seed's identity reach it as environment variables. No `$(` and no `$$` in
# it: Kubernetes rewrites both in a container's command. The seed is merged
# file by file, descending directories both sides have: an entry the node
# already has is kept, one it lacks is copied (so a second engine config's
# key lands beside the node's own under torch_compile_cache), a copy that
# fails is removed and the seed marker withheld, so the next start of the
# pod tries again; the marker records the seed it came from, so a changed
# --seed-claim is merged in too. A recursive function, with `local`, which
# dash has.
preparer_script() {
    cat <<'SCRIPT'
for d in vllm flashinfer triton; do
  mkdir -p "/engine-cache/$d" || exit 1
  chmod 1777 "/engine-cache/$d" 2>/dev/null || true
done
# merge SRC DST: an entry the node has is kept, one it lacks is copied, a
# directory both have is descended -- so a second engine config's key lands
# beside the node's own. A copy that fails is removed and the seed marked
# incomplete.
merge() {
  local e n t
  for e in "$1"/* "$1"/.[!.]*; do
    [ -e "$e" ] || continue
    n="${e##*/}"; t="$2/$n"
    if [ -d "$e" ] && [ ! -L "$e" ] && [ -d "$t" ] && [ ! -L "$t" ]; then
      merge "$e" "$t"
    elif [ ! -e "$t" ]; then
      if cp -a --no-preserve=ownership "$e" "$2/"; then added="$added."; else complete=0; rm -rf "$t"; fi
    fi
  done
}
seeded_from=""
[ ! -f "/engine-cache/$SEED_MARKER" ] || read -r seeded_from < "/engine-cache/$SEED_MARKER" || seeded_from=""
# The seed claim is mounted whole at /seed and SEED_SUBPATH is looked up here:
# a subPath on the mount would make the kubelet create it, which fails on a
# fresh claim and leaves the pod unable to start. An absent seed is nothing
# to seed -- the marker says so with a "none:" prefix, which the readiness
# probe accepts, and the next start merges the seed if it has appeared.
seed_dir="/seed${SEED_SUBPATH:+/$SEED_SUBPATH}"
# `enginecache.sh publish` stamps the seed each time it adds to it; the marker
# records the stamp, so a node merges a seed published after it was seeded.
want="$SEED_ID"
if [ -f "$seed_dir/.published" ]; then
  read -r stamp < "$seed_dir/.published" || stamp=""
  [ -z "$stamp" ] || want="$SEED_ID@$stamp"
fi
if [ -d /seed ] && [ ! -d "$seed_dir" ]; then
  if [ "$seeded_from" != "none:$SEED_ID" ] && [ "$seeded_from" != "$SEED_ID" ]; then
    echo "none:$SEED_ID" > "/engine-cache/$SEED_MARKER" || exit 1
  fi
  echo "engine cache: no seed on $HOSTNAME: $SEED_ID has no $seed_dir yet; nothing to seed"
elif [ -d /seed ] && [ "$seeded_from" != "$want" ]; then
  complete=1; added=""
  for d in vllm flashinfer triton; do
    [ -d "$seed_dir/$d" ] || continue
    merge "$seed_dir/$d" "/engine-cache/$d"
  done
  if [ "$complete" = 1 ]; then
    echo "$want" > "/engine-cache/$SEED_MARKER" || exit 1
    echo "engine cache: seeded on $HOSTNAME from $want: ${#added} entries added, what the node already had kept"
  else
    echo "engine cache: seeding on $HOSTNAME from $want did not complete (a copy failed and was removed); it is retried when this pod next starts"
  fi
fi
touch "/engine-cache/$MARKER" || exit 1
echo "engine cache: prepared on $HOSTNAME: /engine-cache/{vllm,flashinfer,triton}"
trap 'exit 0' TERM; while :; do sleep 3600 & wait; done
SCRIPT
}

# seed_volume_yaml and seed_mount_yaml print the seed claim's volume and its
# read-only mount at /seed when --seed-claim was given, nothing otherwise.
seed_volume_yaml() {
    [ -n "$SEED_CLAIM" ] || return 0
    printf '        - name: seed\n          persistentVolumeClaim:\n            claimName: %s\n            readOnly: true\n' "$SEED_CLAIM"
}
# seed_probe prints the readiness probe's extra clause with a seed: the node
# carries the marker of THIS seed. The probe is a single-quoted YAML scalar,
# so the double quotes in it need no escaping.
seed_probe() {
    [ -n "$SEED_CLAIM" ] || return 0
    local id="${SEED_CLAIM}${SEED_SUBPATH:+:$SEED_SUBPATH}"
    printf ' && grep -qx -e "%s" -e "%s@[0-9]*" -e "none:%s" /engine-cache/%s' "$id" "$id" "$id" "$SEED_MARKER"
}
# seed_mount_yaml mounts the whole claim, never a subPath: the kubelet creates
# a missing subPath, which fails on a fresh claim (the pod never starts). The
# preparer finds SEED_SUBPATH under /seed itself.
seed_mount_yaml() {
    [ -n "$SEED_CLAIM" ] || return 0
    printf '            - name: seed\n              mountPath: /seed\n              readOnly: true\n'
}

# check_seed refuses a seed that is not a claim name with an optional subPath
# under it: the claim is mounted as given, and a subPath with '..' would
# reach outside it.
check_seed() {
    local claim="${1%%:*}" sub=""
    [ "$claim" = "$1" ] || sub="${1#*:}"
    case "$claim" in
        ""|*[!a-z0-9.-]*) log_error "--seed-claim: not a claim name: '${claim}' (a DNS-1123 name, with an optional :subPath)" ;;
    esac
    [ "$claim" = "$1" ] || [ -n "$sub" ] || log_error "--seed-claim: '${1}' names an empty subPath; give PVC or PVC:SUBPATH"
    [ "$claim" != "$CLAIM" ] || log_error "--seed-claim: the seed cannot be the engine-cache claim itself"
    case "$sub" in
        *..*|/*|*[!A-Za-z0-9._/-]*) log_error "--seed-claim: not a subPath: '${sub}' (relative, path characters, no '..')" ;;
    esac
    SEED_CLAIM="$claim"
    SEED_SUBPATH="$sub"
}

# render prints the ServiceAccount, the volume, the claim and the preparer.
render() {
    local pv
    pv="$(pv_for)"
    cat <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${SA_NAME}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: node-local-engine-cache
    app.kubernetes.io/component: node-local-engine-cache
    app.kubernetes.io/managed-by: wva-enginecache
# The preparer's own identity, so what an admin grants for it (an SCC on
# OpenShift) does not land on the namespace's default ServiceAccount. It
# makes no API call, so no token.
automountServiceAccountToken: false
---
apiVersion: v1
kind: PersistentVolume
metadata:
  name: ${pv}
  labels:
    app.kubernetes.io/name: node-local-engine-cache
    app.kubernetes.io/component: node-local-engine-cache
    app.kubernetes.io/managed-by: wva-enginecache
  annotations:
    wva.llmd.ai/engine-cache-path: "${NODE_PATH}/${SUBDIR}"
spec:
  capacity:
    storage: ${CAPACITY}
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ${STORAGE_CLASS}
  # Bound to this claim and no other: a static volume without a claimRef is
  # taken by the first claim that asks for its class.
  claimRef:
    namespace: ${NAMESPACE}
    name: ${CLAIM}
  hostPath:
    path: ${NODE_PATH}/${SUBDIR}
    type: DirectoryOrCreate
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${CLAIM}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: node-local-engine-cache
    app.kubernetes.io/component: node-local-engine-cache
    app.kubernetes.io/managed-by: wva-enginecache
  annotations:
    wva.llmd.ai/engine-cache-path: "${NODE_PATH}/${SUBDIR}"
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: ${STORAGE_CLASS}
  volumeName: ${pv}
  resources:
    requests:
      storage: ${CAPACITY}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: ${CLAIM}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: node-local-engine-cache
    app.kubernetes.io/component: node-local-engine-cache
    app.kubernetes.io/managed-by: wva-enginecache
  annotations:
    wva.llmd.ai/engine-cache-path: "${NODE_PATH}/${SUBDIR}"
    wva.llmd.ai/engine-cache-node-selector: "${NODE_SELECTOR}"
    wva.llmd.ai/engine-cache-seed: "${SEED_CLAIM}${SEED_SUBPATH:+:$SEED_SUBPATH}"
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: node-local-engine-cache
      wva.llmd.ai/engine-cache: ${CLAIM}
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 100%
  template:
    metadata:
      labels:
        app.kubernetes.io/name: node-local-engine-cache
        app.kubernetes.io/component: node-local-engine-cache
        app.kubernetes.io/managed-by: wva-enginecache
        wva.llmd.ai/engine-cache: ${CLAIM}
    spec:
      serviceAccountName: ${SA_NAME}
      automountServiceAccountToken: false
      securityContext:
        seccompProfile:
          type: RuntimeDefault
$(accelerator_placement_yaml "$NODE_SELECTOR")
      tolerations:
$(for t in "${TOLERATIONS[@]}"; do printf '        - key: "%s"\n          operator: Exists\n' "$t"; done)
      terminationGracePeriodSeconds: 1
      volumes:
        - name: engine-cache
          persistentVolumeClaim:
            claimName: ${CLAIM}
$(seed_volume_yaml)
      containers:
        - name: prepare
          image: "${IMAGE}"
          imagePullPolicy: IfNotPresent
          # One directory per cache, writable by any UID the engines run as
          # (1777: sticky, so one UID's files are not another's to remove --
          # and world-writable, so whoever can write here writes what every
          # engine on the node will load; see the header), then a marker,
          # then the pod stays, Ready, so the DaemonSet's numberReady is the
          # count of prepared nodes. The preparer creates the three
          # directories itself, so it owns them and the chmod takes, on
          # Kubernetes as root and on OpenShift as the project UID inside
          # the directory the admin prepared.
          command: ["/bin/sh", "-c"]
          args:
            - |
$(preparer_script | sed 's/^/              /')
          env:
            - name: MARKER
              value: "${MARKER}"
            - name: SEED_MARKER
              value: "${SEED_MARKER}"
            - name: SEED_ID
              value: "${SEED_CLAIM}${SEED_SUBPATH:+:$SEED_SUBPATH}"
            - name: SEED_SUBPATH
              value: "${SEED_SUBPATH}"
            # Engine images bake in NVIDIA_VISIBLE_DEVICES=all, and the NVIDIA
            # runtime honours it from a container that requested no GPU.
            - name: NVIDIA_VISIBLE_DEVICES
              value: "void"
          volumeMounts:
            - name: engine-cache
              mountPath: /engine-cache
$(seed_mount_yaml)
          # Ready is "prepared, and writable by this UID": what the engines
          # will find, as the UID they run as -- and, with a seed, seeded from
          # it, so numberReady and status count a node whose seed did not
          # complete as not ready. The engines do not wait on the preparer.
          readinessProbe:
            exec:
              command: ["/bin/sh", "-c", 'test -f /engine-cache/${MARKER} && test -w /engine-cache/vllm$(seed_probe)']
            periodSeconds: 10
          resources:
            requests:
              cpu: 5m
              memory: 16Mi
            limits:
              memory: 64Mi
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
EOF
}

cmd_apply() {
    [ -n "$NAMESPACE" ] || log_error "apply needs -n NAMESPACE"
    [ -n "$NODE_PATH" ] || log_error "apply needs --path DIR (a directory on the node)"
    [ -n "$IMAGE" ] || log_error "apply needs --image IMG (an image with /bin/sh; the engine image)"
    check_path "$NODE_PATH"
    check_image "$IMAGE"
    if [ "$DRY_RUN" = true ]; then
        render
        return
    fi
    # Four documents in one apply: without leave for the cluster-scoped
    # volume, kubectl would still create the rest -- a Pending claim and
    # pods that never schedule -- and report only the volume's Forbidden.
    if ! kubectl auth can-i create persistentvolumes -A -q 2>/dev/null; then
        log_error "apply needs leave to create PersistentVolumes (cluster-scoped), which a namespace tenant does not have -- ask the cluster admin to run apply (storage-admin on OpenShift)"
    fi
    # A claim of this name on another class (or on none) is somebody else's:
    # the volume here would never bind to it, and the engines would go on
    # using it. And a claim's volume is immutable: a second apply with a new
    # --path would create a second volume, land the DaemonSet with the new
    # directory in its annotation, and leave the claim bound to the old one
    # -- status would then report a directory the engines do not mount.
    local existing
    existing="$(kubectl get pvc -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.metadata.name}/{.spec.storageClassName}' 2>/dev/null || true)"
    if [ -n "$existing" ] && [ "${existing#*/}" != "$STORAGE_CLASS" ]; then
        log_error "namespace ${NAMESPACE} already has a claim named ${CLAIM} on storage class '${existing#*/}', which this script did not make; delete it or use a namespace without one"
    fi
    # The directory the claim is really on is its volume's, not the
    # DaemonSet's annotation (the DaemonSet may be gone; the claim stays bound).
    local volume applied
    volume="$(kubectl get pvc -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)"
    applied=""
    [ -z "$volume" ] || applied="$(kubectl get pv "$volume" -o jsonpath='{.spec.hostPath.path}' 2>/dev/null || true)"
    if [ -n "$applied" ] && [ "$applied" != "${NODE_PATH}/${SUBDIR}" ]; then
        log_error "namespace ${NAMESPACE} already has its engine cache at ${applied} (volume ${volume}); the claim's volume cannot change. enginecache.sh delete -n ${NAMESPACE} first (the caches on the nodes stay), then apply with the new --path"
    fi
    if [ -n "$SEED_CLAIM" ]; then
        local seed_phase
        seed_phase="$(kubectl get pvc -n "$NAMESPACE" "$SEED_CLAIM" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
        [ "$seed_phase" = Bound ] || log_error "--seed-claim ${SEED_CLAIM}: no Bound claim of that name in ${NAMESPACE} (found: '${seed_phase:-none}'); the preparer would mount it and never start"
    fi
    render | kubectl apply -f - >/dev/null
    log_info "preparing ${NODE_PATH}/${SUBDIR} on $(accelerator_selector_text "$NODE_SELECTOR"); claim ${CLAIM}${SEED_CLAIM:+, seeded from ${SEED_CLAIM}${SEED_SUBPATH:+:$SEED_SUBPATH}} -- mount it read-write and point VLLM_CACHE_ROOT, FLASHINFER_WORKSPACE_DIR and TRITON_CACHE_DIR under it"
    if kubectl api-resources --api-group=security.openshift.io 2>/dev/null | grep -q securitycontextconstraints; then
        log_warning "OpenShift: the preparer and the engines run as the project UID (GID 0) under restricted-v2 and the kubelet does not relabel a hostPath; on each node, before this takes: mkdir -p ${NODE_PATH}/${SUBDIR} && chgrp 0 ${NODE_PATH}/${SUBDIR} && chmod 2775 ${NODE_PATH}/${SUBDIR} && chcon -t container_file_t ${NODE_PATH}/${SUBDIR} (oc debug node/<n> -- chroot /host ...), or a MachineConfig mount with context=system_u:object_r:container_file_t:s0. Not yet run on OpenShift"
    fi
    if ! command -v jq >/dev/null 2>&1; then
        log_warning "jq is not installed; skipping the per-node report (enginecache.sh status needs it)"
        return 0
    fi
    cmd_status report || true
}

# cmd_status prints, per node, whether the node is prepared. Ready is the
# marker present and the directory writable by the preparer's UID; a pod
# that keeps failing shows its reason, and no pod at all is a node the
# DaemonSet did not reach. Exits non-zero while any selected node is not
# prepared or the claim is not Bound.
cmd_status() {
    local mode="${1:-verdict}"
    command -v jq >/dev/null 2>&1 || log_error "jq is required for status (the node and pod lists are read with it)"
    [ -n "$NAMESPACE" ] || log_error "status needs -n NAMESPACE"
    local rc=0 ready=0 holders=0 selector="$NODE_SELECTOR" path
    path="$(kubectl get daemonset -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.metadata.annotations.wva\.llmd\.ai/engine-cache-path}' 2>/dev/null || true)"
    if [ -z "$path" ]; then
        if [ "$mode" = report ]; then
            log_warning "no engine-cache DaemonSet in ${NAMESPACE}"
            return 1
        fi
        log_error "no engine-cache DaemonSet in ${NAMESPACE} (enginecache.sh apply makes one)"
    fi
    if [ "$NODE_SELECTOR_GIVEN" = false ]; then
        # Off the live DaemonSet's pod template, held to the same rule as
        # the flag before it reaches kubectl (a tenant can edit the object).
        selector="$(kubectl get daemonset -n "$NAMESPACE" "$CLAIM" \
            -o jsonpath='{.spec.template.spec.nodeSelector}' 2>/dev/null \
            | jq -r 'to_entries | map(.key + "=" + .value) | first // ""' 2>/dev/null || true)"
        local why
        if ! why="$(accelerator_selector_ok "$selector")"; then
            [ "$mode" = report ] || log_error "DaemonSet ${CLAIM} carries a nodeSelector this script did not write (${why}); pass --node-selector to report on it"
            log_warning "DaemonSet ${CLAIM} carries a nodeSelector this script did not write (${why}); pass --node-selector to report on it"
            return 1
        fi
    fi
    local nodes_json err_file
    err_file="$(mktemp)"
    if ! nodes_json="$(accelerator_nodes_json "$selector" 2>"$err_file")"; then
        local why
        if grep -q Forbidden "$err_file"; then
            why="a namespace tenant may not list nodes; this needs cluster-scoped node read (cluster-reader) -- ask for it or check with the cluster admin"
        else
            why="$(tr '\n' ' ' < "$err_file")"
        fi
        rm -f "$err_file"
        [ "$mode" = report ] || log_error "cannot list nodes: ${why}"
        log_warning "cannot list nodes: ${why}"
        return 1
    fi
    rm -f "$err_file"
    local node_count excluded
    # Usable nodes only: a cordoned or foreign-tainted node can never hold a
    # copy an engine starts on, and counting it failed finished preparations.
    excluded="$(printf '%s' "$nodes_json" | accelerator_excluded_json "$(accelerator_ds_toleration_keys "$NAMESPACE" "$CLAIM" "${TOLERATIONS[@]}")")"
    nodes_json="$(printf '%s' "$nodes_json" | accelerator_usable_nodes_json "$excluded")"
    node_count="$(printf '%s' "$nodes_json" | jq '.items | length')"
    [ "$node_count" -gt 0 ] || accelerator_print_excluded "$excluded" >&2
    if [ "$node_count" -eq 0 ]; then
        [ "$mode" = report ] || log_error "no node matches $(accelerator_selector_text "$selector") (--node-selector picks the nodes)"
        log_warning "no node matches $(accelerator_selector_text "$selector"); the cache is prepared nowhere (--node-selector picks the nodes)"
        return 1
    fi
    local pods_file bound
    pods_file="$(mktemp)"
    kubectl get pods -n "$NAMESPACE" -l "$LABEL_COMPONENT" -o json > "$pods_file"
    bound="$(kubectl get pvc -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    local seed
    seed="$(kubectl get daemonset -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.metadata.annotations.wva\.llmd\.ai/engine-cache-seed}' 2>/dev/null || true)"
    echo "${path}  (claim ${CLAIM}: ${bound:-missing}; seed: ${seed:-none}; nodes: $(accelerator_selector_text "$selector"))"
    if [ "$bound" != Bound ]; then
        rc=1
        [ -z "$bound" ] || echo "  the claim is not bound: its volume is cluster-scoped and was not created -- apply needs leave to create PersistentVolumes (ask the cluster admin to run apply)"
    fi
    printf '  %-28s %-12s %s\n' NODE CACHE PREPARER
    accelerator_print_excluded "$excluded"
    local eacces=0
    # The unit separator keeps empty fields; a tab in IFS would swallow them.
    while IFS=$'\x1f' read -r node phase reason podname message; do
        local has=absent
        [ "$phase" = "no pod" ] || holders=$((holders + 1))
        if [ "$phase" = Ready ]; then
            has=prepared
            ready=$((ready + 1))
        else
            rc=1
        fi
        printf '  %-28s %-12s %s %s\n' "$node" "$has" "$phase" "$reason"
        # the reason carries the last exit after it ("CrashLoopBackOff (last exit 1 Error)")
        case "$reason" in
            CrashLoopBackOff*|Error*)
                if kubectl logs -n "$NAMESPACE" "$podname" --tail=20 2>/dev/null | grep -q 'Permission denied\|Read-only file system'; then
                    echo "    the preparer cannot write ${path}: $(kubectl logs -n "$NAMESPACE" "$podname" --tail=20 2>/dev/null | grep -o 'Permission denied\|Read-only file system' | head -1) in its log"
                    eacces=$((eacces + 1))
                fi ;;
            CreateContainerConfigError) [ -z "$message" ] || echo "    ${message}" ;;
        esac
    done < <(printf '%s' "$nodes_json" | jq -r --arg ds "$CLAIM" --slurpfile pods "$pods_file" \
        '.items[] as $node
         | ($pods[0].items | map(select(.metadata.labels["wva.llmd.ai/engine-cache"] == $ds
              and (.spec.nodeName == $node.metadata.name
                   or ((.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms // [])[0].matchFields // [])[0].values[0]? == $node.metadata.name))) | first) as $pod
         | [ $node.metadata.name,
             (if $pod == null then "no pod"
              elif $pod.status.phase == "Running" and (($pod.status.containerStatuses // []) | map(.ready) | all) then "Ready"
              elif $pod.status.phase == "Running" then "Running (not ready)"
              else $pod.status.phase + " (not ready)" end),
             (if $pod == null then ""
              else ($pod.status.reason // ((($pod.status.containerStatuses // [])[0].state // {}) | to_entries | (.[0].value.reason // "")))
                   + ((($pod.status.containerStatuses // [])[0].lastState.terminated) | if . then " (last exit " + (.exitCode | tostring) + (if .reason then " " + .reason else "" end) + ")" else "" end) end),
             ($pod.metadata.name // ""),
             (if $pod == null then "" else (((($pod.status.containerStatuses // [])[0].state // {}) | to_entries | (.[0].value.message // "")) | gsub("[\\t\\n]"; " ")) end)
           ] | join("")')
    rm -f "$pods_file"
    if [ "$eacces" -gt 0 ]; then
        echo "  ${eacces} node(s): the node directory is not writable by the preparer. On Kubernetes the preparer is root and the kubelet creates the directory root-owned, so this is a mount that is read-only or not there, or a directory that already existed with another owner (ls -ld on the node); on OpenShift it runs as the project UID (GID 0) under restricted-v2 and the directory is not relabelled -- on each node: mkdir -p DIR && chgrp 0 DIR && chmod 2775 DIR && chcon -t container_file_t DIR, or a MachineConfig mount with context=...:container_file_t:s0 (enginecache.sh --help)"
    fi
    echo "  ${ready}/${node_count} usable nodes prepared; $((node_count - ready)) not"
    [ -n "$selector" ] || printf '%s' "$nodes_json" | accelerator_vendor_warning
    if [ "$holders" -eq 0 ]; then
        local why
        why="$(kubectl get events -n "$NAMESPACE" \
            --field-selector "involvedObject.kind=DaemonSet,involvedObject.name=${CLAIM},reason=FailedCreate" \
            -o jsonpath='{.items[-1:].message}' 2>/dev/null || true)"
        [ -n "$why" ] && echo "  the DaemonSet cannot create its pods: ${why}"
    fi
    return $rc
}

run_delete() {
    if [ "$DRY_RUN" = true ]; then
        echo "would run: kubectl $*"
    else
        kubectl "$@"
    fi
}

# cmd_delete removes the preparer, the claim, the volume and the
# ServiceAccount. The caches on the nodes stay: the next apply finds them
# warm. The volume is found by this script's labels and its claimRef into
# this namespace, never by a name read off the claim (a tenant's object).
cmd_delete() {
    [ -n "$NAMESPACE" ] || log_error "delete needs -n NAMESPACE"
    command -v jq >/dev/null 2>&1 || log_error "jq is required for delete (the volume is matched on its claimRef with it)"
    local volumes=() line
    while IFS= read -r line; do
        [ -n "$line" ] && volumes+=("$line")
    done < <(kubectl get pv -l "$LABEL_COMPONENT" -o json 2>/dev/null \
        | jq -r --arg ns "$NAMESPACE" --arg claim "$CLAIM" \
            '.items[] | select(.spec.claimRef.namespace == $ns and .spec.claimRef.name == $claim) | .metadata.name' \
        2>/dev/null || true)
    run_delete delete daemonset -n "$NAMESPACE" "$CLAIM" --ignore-not-found
    run_delete delete pvc -n "$NAMESPACE" "$CLAIM" --ignore-not-found
    [ "${#volumes[@]}" -eq 0 ] || run_delete delete pv "${volumes[@]}" --ignore-not-found
    run_delete delete serviceaccount -n "$NAMESPACE" "$SA_NAME" --ignore-not-found
    return 0
}

# publish_script runs on one node: it merges that node's caches into the seed
# claim -- an entry the seed already has is kept -- and stamps the seed, so
# every preparer merges it again on its next start. A quoted heredoc, for the
# same reasons as preparer_script.
publish_script() {
    cat <<'SCRIPT'
dst="/seed${SEED_SUBPATH:+/$SEED_SUBPATH}"
mkdir -p "$dst" || exit 1
merge() {
  local e n t
  for e in "$1"/* "$1"/.[!.]*; do
    [ -e "$e" ] || continue
    n="${e##*/}"; t="$2/$n"
    if [ -d "$e" ] && [ ! -L "$e" ] && [ -d "$t" ] && [ ! -L "$t" ]; then
      merge "$e" "$t"
    elif [ ! -e "$t" ]; then
      if cp -a --no-preserve=ownership "$e" "$2/"; then added="$added."; else complete=0; rm -rf "$t"; fi
    fi
  done
}
complete=1; added=""
for d in vllm flashinfer triton; do
  [ -d "/engine-cache/$d" ] || continue
  mkdir -p "$dst/$d" || exit 1
  merge "/engine-cache/$d" "$dst/$d"
done
[ "$complete" = 1 ] || { echo "engine cache: publishing from $NODE_NAME did not complete (a copy failed and was removed)"; exit 1; }
echo "engine cache: published from $NODE_NAME: ${#added} entries added to the seed"
SCRIPT
}

# pod_succeeded POD TIMEOUT_S waits for a run-once pod to finish: 0 when it
# Succeeded, 1 as soon as it Failed or when TIMEOUT_S passes. `kubectl wait`
# on one phase would sit out the whole timeout on a pod that already failed.
pod_succeeded() {
    local pod="$1" deadline=$((SECONDS + $2)) phase
    while [ "$SECONDS" -lt "$deadline" ]; do
        phase="$(kubectl get pod -n "$NAMESPACE" "$pod" -o jsonpath='{.status.phase}' --request-timeout=30s 2>/dev/null)"
        case "$phase" in
            Succeeded) return 0 ;;
            Failed) return 1 ;;
        esac
        sleep 5
    done
    return 1
}

# cmd_publish fills the seed from the nodes: every node whose preparer is Ready
# merges its caches into the seed claim, one node at a time, then the seed is
# stamped and the preparers restart, so each node merges in what any node
# compiled. Run it once every engine configuration has started somewhere; after
# it no node starts any of them cold.
cmd_publish() {
    command -v jq >/dev/null 2>&1 || log_error "jq is required for publish"
    [ -n "$NAMESPACE" ] || log_error "publish needs -n NAMESPACE"
    local seed image claim sub
    seed="$(kubectl get daemonset -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.metadata.annotations.wva\.llmd\.ai/engine-cache-seed}' 2>/dev/null || true)"
    [ -n "$seed" ] || log_error "publish needs an engine cache applied with --seed-claim in ${NAMESPACE} (there is nothing to publish into)"
    claim="${seed%%:*}"; sub=""
    [ "$claim" = "$seed" ] || sub="${seed#*:}"
    image="$(kubectl get daemonset -n "$NAMESPACE" "$CLAIM" -o jsonpath='{.spec.template.spec.containers[0].image}')"
    local nodes
    nodes="$(kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/name=node-local-engine-cache,wva.llmd.ai/engine-cache=${CLAIM}" -o json \
        | jq -r '.items[] | select(any(.status.conditions[]?; .type=="Ready" and .status=="True")) | .spec.nodeName' | sort -u)"
    [ -n "$nodes" ] || log_error "no Ready engine-cache preparer in ${NAMESPACE}: nothing to publish from (enginecache.sh status says why)"
    local stamp i=0 node pod
    stamp="$(date -u +%Y%m%d%H%M%S)"
    for node in $nodes; do
        i=$((i + 1)); pod="${CLAIM}-publish-${i}"
        local manifest
        manifest="$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: node-local-engine-cache-publish
    app.kubernetes.io/managed-by: wva-enginecache
spec:
  nodeName: ${node}
  restartPolicy: Never
  serviceAccountName: ${SA_NAME}
  automountServiceAccountToken: false
  securityContext:
    seccompProfile:
      type: RuntimeDefault
  tolerations:
    - operator: Exists
  volumes:
    - name: engine-cache
      persistentVolumeClaim:
        claimName: ${CLAIM}
        readOnly: true
    - name: seed
      persistentVolumeClaim:
        claimName: ${claim}
  containers:
    - name: publish
      image: "${image}"
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh", "-c"]
      args:
        - |
$(publish_script | sed 's/^/          /')
      env:
        - name: SEED_SUBPATH
          value: "${sub}"
        - name: NODE_NAME
          value: "${node}"
        - name: NVIDIA_VISIBLE_DEVICES
          value: "void"
      volumeMounts:
        - name: engine-cache
          mountPath: /engine-cache
          readOnly: true
        - name: seed
          mountPath: /seed
      resources:
        requests:
          cpu: 50m
          memory: 32Mi
        limits:
          memory: 256Mi
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
EOF
)"
        if [ "$DRY_RUN" = true ]; then printf '%s\n---\n' "$manifest"; continue; fi
        kubectl delete pod -n "$NAMESPACE" "$pod" --ignore-not-found --wait=true --timeout=60s >/dev/null
        printf '%s\n' "$manifest" | kubectl apply -f - >/dev/null || log_error "publish: could not create ${pod} on ${node}"
        if ! pod_succeeded "$pod" 900; then
            kubectl logs -n "$NAMESPACE" "$pod" 2>&1 | tail -5 >&2
            kubectl delete pod -n "$NAMESPACE" "$pod" --ignore-not-found --wait=false >/dev/null 2>&1
            log_error "publish: ${pod} on ${node} did not succeed (its log is above); the seed is not stamped, so no node re-seeds from a partial copy"
        fi
        kubectl logs -n "$NAMESPACE" "$pod" 2>/dev/null | tail -1
        kubectl delete pod -n "$NAMESPACE" "$pod" --ignore-not-found --wait=false >/dev/null
    done
    [ "$DRY_RUN" = false ] || return 0
    # Stamp the seed only after every node published, from the last node's view.
    local stamp_pod="${CLAIM}-publish-stamp"
    kubectl delete pod -n "$NAMESPACE" "$stamp_pod" --ignore-not-found --wait=true --timeout=60s >/dev/null
    kubectl run -n "$NAMESPACE" "$stamp_pod" --image="$image" --restart=Never --quiet \
        --overrides="{\"spec\":{\"nodeName\":\"${node}\",\"tolerations\":[{\"operator\":\"Exists\"}],\"serviceAccountName\":\"${SA_NAME}\",\"automountServiceAccountToken\":false,\"volumes\":[{\"name\":\"seed\",\"persistentVolumeClaim\":{\"claimName\":\"${claim}\"}}],\"containers\":[{\"name\":\"stamp\",\"image\":\"${image}\",\"command\":[\"/bin/sh\",\"-c\",\"echo ${stamp} > /seed${sub:+/$sub}/.published\"],\"env\":[{\"name\":\"NVIDIA_VISIBLE_DEVICES\",\"value\":\"void\"}],\"volumeMounts\":[{\"name\":\"seed\",\"mountPath\":\"/seed\"}]}]}}" >/dev/null \
        || log_error "publish: could not create ${stamp_pod}"
    pod_succeeded "$stamp_pod" 300 \
        || { kubectl delete pod -n "$NAMESPACE" "$stamp_pod" --ignore-not-found --wait=false >/dev/null 2>&1; log_error "publish: the seed was filled but not stamped; rerun publish"; }
    kubectl delete pod -n "$NAMESPACE" "$stamp_pod" --ignore-not-found --wait=false >/dev/null
    kubectl rollout restart -n "$NAMESPACE" "daemonset/${CLAIM}" >/dev/null || log_error "publish: could not restart the preparers"
    # Wait on the usable nodes, as status counts them: `rollout status` also
    # waits for a cordoned node's pod, which never comes up.
    local deadline=$((SECONDS + 600))
    sleep 5
    until bash "$HERE/enginecache.sh" status -n "$NAMESPACE" >/dev/null 2>&1; do
        [ "$SECONDS" -lt "$deadline" ] || { log_warning "publish: not every usable node re-seeded within 600 s; enginecache.sh status says which"; break; }
        sleep 10
    done
    log_success "published from ${i} node(s) into ${seed} (stamp ${stamp}); every preparer merges it on restart"
}

CMD="${1:-}"
[ -n "$CMD" ] || usage
shift
case "$CMD" in
    -h|--help|help) usage ;;
    apply|status|delete|publish) ;;
    *) log_error "unknown command: ${CMD} (apply | status | delete | publish)" ;;
esac

while [ $# -gt 0 ]; do
    case "$1" in
        -n|--namespace|--path|--image|--capacity|--seed-claim|--node-selector|--toleration)
            [ $# -ge 2 ] || log_error "$1 needs a value" ;;
    esac
    case "$1" in
        -n|--namespace) NAMESPACE="$2"; shift 2 ;;
        --path) NODE_PATH="$2"; shift 2 ;;
        --image) IMAGE="$2"; shift 2 ;;
        --capacity) CAPACITY="$2"; shift 2 ;;
        --seed-claim) check_seed "$2"; shift 2 ;;
        --node-selector) NODE_SELECTOR="$2"; NODE_SELECTOR_GIVEN=true; shift 2 ;;
        --toleration) accelerator_check_toleration "$2"; TOLERATIONS+=("$2"); shift 2 ;;
        --dry-run) [ "$CMD" != status ] || log_error "--dry-run applies to apply and delete, not status"; DRY_RUN=true; shift ;;
        -h|--help) usage ;;
        *) log_error "unknown option: $1" ;;
    esac
done
accelerator_check_selector "$NODE_SELECTOR"
case "$CAPACITY" in
    ""|*[!0-9A-Za-z.]*) log_error "--capacity must be a Kubernetes quantity such as 200Gi, got '${CAPACITY}'" ;;
esac

"cmd_${CMD}"
