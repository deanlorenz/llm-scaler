#!/usr/bin/env bash
# A directory on the node that a hostPath volume may be pointed at -- for
# deploy/weights.sh and hack/benchmark/model_hostpath.sh, which both hand a
# node directory to a hostPath PersistentVolume that a namespace's pods then
# mount read-write, as root.
#
# `type: DirectoryOrCreate` on an existing path bind-mounts it as it is, so a
# slip such as --path /var/lib/kubelet would hand every pod in the namespace
# root write on every other pod's secret volumes on every accelerator node.
# The caller holds PersistentVolume create rights, so this is a guard against
# a mistake, not an adversary -- but the mistake is one character away.

# nodedir_ok says whether $1 is an acceptable node directory: absolute, of
# path characters, at least two components (/mnt/local/models, not /mnt), and
# not under a prefix the node itself lives in. Prints why not, returns 1.
nodedir_ok() {
    local dir="$1"
    case "$dir" in
        "") echo "must not be empty"; return 1 ;;
        /) echo "must not be the root directory"; return 1 ;;
        *[!A-Za-z0-9._/-]*) echo "not a node path: '${dir}' (absolute, letters, digits, . _ - /)"; return 1 ;;
        /*) ;;
        *) echo "must be absolute: '${dir}'"; return 1 ;;
    esac
    case "$dir" in
        *..*) echo "must not contain '..': '${dir}'"; return 1 ;;
        */) echo "must not end with '/': '${dir}'"; return 1 ;;
    esac
    case "${dir#/}" in
        */*) ;;
        *) echo "needs at least two components (/mnt/local/models, not ${dir}): the whole of a top-level directory is not a weights directory"; return 1 ;;
    esac
    # /var/mnt and /var/srv themselves are RHCOS's root disk (the one the
    # image store and the eviction thresholds live on); a subdirectory with
    # a disk mounted at it is what the docs ask for
    case "$dir" in
        /var/mnt|/var/srv) echo "'${dir}' is the root disk on RHCOS; a subdirectory with a disk mounted at it (/var/mnt/weights)"; return 1 ;;
    esac
    local prefix
    # the second row is where RHCOS keeps /home, /root, /usr/local, /opt and
    # the ostree deployments (its /home, /root, /opt, /mnt are symlinks into /var)
    for prefix in /etc /usr /bin /sbin /lib /lib32 /lib64 /boot /dev /proc /sys /run /var/run /var/lib /var/log /var/spool /root /home /opt/cni /opt/bin /snap /tmp /var/tmp \
                  /var/home /var/roothome /var/usrlocal /var/opt/cni /var/opt/bin /sysroot /ostree; do
        case "$dir" in
            "$prefix"|"$prefix"/*) echo "'${dir}' is under ${prefix}, which the node itself lives in; a hostPath there is mounted read-write by every pod in the namespace"; return 1 ;;
        esac
    done
    return 0
}

# THE CLAIM'S SIZE ON A RE-APPLY. The claim over a node directory is bound
# to a static volume, and Kubernetes resizes only a dynamically provisioned
# claim: a re-apply that renders another size fails mid-apply with "only
# dynamically provisioned pvc can be resized" -- which every re-run did once
# the script's default size changed. So an existing claim keeps its size
# unless the caller asked for one, and a size that differs from it is
# refused before anything is applied.

# nodedir_quantity_bytes prints quantity $1 in bytes when it is an integer
# with a binary or decimal suffix (or none), and fails otherwise -- the
# forms a capacity is written in. 1024Gi and 1Ti are then the same size,
# which the API server, storing the canonical form, would report as 1Ti.
nodedir_quantity_bytes() {
    local re='^([0-9]{1,15})(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$' n mult=1
    [[ "$1" =~ $re ]] || return 1
    n="$((10#${BASH_REMATCH[1]}))"
    case "${BASH_REMATCH[2]}" in
        Ki) mult=$((1 << 10)) ;; Mi) mult=$((1 << 20)) ;; Gi) mult=$((1 << 30)) ;;
        Ti) mult=$((1 << 40)) ;; Pi) mult=$((1 << 50)) ;; Ei) mult=$((1 << 60)) ;;
        k) mult=1000 ;; M) mult=1000000 ;; G) mult=1000000000 ;;
        T) mult=1000000000000 ;; P) mult=1000000000000000 ;; E) mult=1000000000000000000 ;;
    esac
    # past 2^63 the product wraps; such a size is no claim's
    [ "$n" -eq 0 ] || [ "$mult" -le $(( 0x7fffffffffffffff / n )) ] || return 1
    printf '%s' "$((n * mult))"
}

# nodedir_same_quantity says whether quantities $1 and $2 are the same size:
# equal in bytes when both parse, equal as text otherwise.
nodedir_same_quantity() {
    local a b
    if a="$(nodedir_quantity_bytes "$1")" && b="$(nodedir_quantity_bytes "$2")"; then
        [ "$a" = "$b" ]
    else
        [ "$1" = "$2" ]
    fi
}

# nodedir_claim_capacity NAMESPACE CLAIM GIVEN RESET sets CAPACITY to the size
# a re-apply renders: an existing claim's own size when GIVEN is false (no
# --capacity), CAPACITY as it is when there is no claim or when --capacity
# equals the claim's size. A --capacity that differs from an existing
# claim's size is refused, RESET being the command that frees the claim for
# a new size. A claim that cannot be read (Forbidden, an unreachable API
# server) is an error, not an absent claim: the apply would otherwise
# render the default and fail mid-apply on the resize. Exits via
# log_error, so call it outside a command substitution.
nodedir_claim_capacity() {
    local ns="$1" claim="$2" given="$3" reset="$4" err current
    err="$(mktemp)"
    if ! current="$(kubectl get pvc -n "$ns" "$claim" --ignore-not-found -o jsonpath='{.spec.resources.requests.storage}' 2>"$err")"; then
        local why
        why="$(tr '\n' ' ' < "$err" | sed 's/ *$//')"
        rm -f "$err"
        log_error "cannot read the claim ${claim} in ${ns} (kubectl: ${why:-no error text}); a re-apply keeps an existing claim's size, so it must know whether there is one -- nothing was applied"
    fi
    rm -f "$err"
    [ -n "$current" ] || return 0
    if [ "$given" != true ]; then
        [ "$current" = "$CAPACITY" ] || log_info "claim ${claim} in ${ns} exists at ${current}; keeping that size (a node-local claim cannot be resized; --capacity to say otherwise)"
        CAPACITY="$current"
        return 0
    fi
    # the same size written another way renders as the claim has it
    if nodedir_same_quantity "$CAPACITY" "$current"; then
        CAPACITY="$current"
        return 0
    fi
    log_error "--capacity ${CAPACITY} differs from the existing claim ${claim} in ${ns}, which is ${current}: a claim bound to a static (node-local) volume cannot be resized -- Kubernetes resizes only dynamically provisioned claims. Leave --capacity off to keep ${current}, or ${reset} first (the files on the nodes stay) and apply again with the new size. Nothing was applied"
}
