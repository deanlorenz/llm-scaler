#!/usr/bin/env bash
# Utilization-share optimizer: check, shadow, activate, status, off.
#
# The optimizer is one `optimizer:` block in the scaling policy. Turning it on
# by hand meant reading the policy out, appending the block, patching it back
# and grepping the controller's log to learn whether it took. Each verb here
# does one of those steps, refuses what would be unsafe, and reports what the
# controller did with it.
#
#   utilization-share.sh check    -n NS   is there a budget to share; do the accelerators resolve
#   utilization-share.sh shadow   -n NS   compute and report, move nothing
#   utilization-share.sh activate -n NS   act on what shadow reported
#   utilization-share.sh status   -n NS   mode, recent decisions, transfers, warnings, marks
#   utilization-share.sh off      -n NS   back through shadow, wait for the marks to clear, remove the block
#
# Options:
#   -n, --namespace NS          the models' namespace (required)
#   --controller-namespace NS   where the controller runs (default: NS)
#   --policy-namespace NS       where its scaling policy lives (default: the controller's)
#   --physical-groups           (shadow) also plan GPUs bounded only by the
#                               physical inventory; needed when the limiter is
#                               gpu-inventory and there is no quota
#   --restart                   restart the controller when it does not reload
#                               the policy by itself (a policy in another
#                               namespace is read only at start)
set -euo pipefail

KUBECTL_TIMEOUT="${KUBECTL_TIMEOUT:-30s}"
CONTROLLER_DEPLOY="${CONTROLLER_DEPLOY:-wva-controller-manager}"
POLICY_CM="wva-scaling-policy-config"
MARK="llm-d.ai/utilization-share-transfer"
# The controller watches the ConfigMap, so a reload takes seconds; a refusal
# shows within one cycle (30 s by default).
RELOAD_WAIT="${RELOAD_WAIT:-90}"
CYCLE_WAIT="${CYCLE_WAIT:-45}"
# How long `off` waits in shadow for the transfer marks to clear.
MARK_WAIT="${MARK_WAIT:-180}"

info() { printf '  %s\n' "$*" >&2; }
ok()   { printf '[OK]   %s\n' "$*" >&2; }
warn() { printf '[WARN] %s\n' "$*" >&2; }
fail() { printf '[FAIL] %s\n' "$*" >&2; }
die()  { fail "$*"; exit 1; }

k() { kubectl --request-timeout="$KUBECTL_TIMEOUT" "$@"; }

# ---------------------------------------------------------------------------
# Policy transforms: a policy on stdin, a word or a policy on stdout.
# hack/check-utilization-share-script.sh runs them against fixtures.
# ---------------------------------------------------------------------------

# us_limiter_kind -- quota, inventory or none: the budget the optimizer would
# share. A list holding both is a quota: the controller then builds only the
# quota limiter.
us_limiter_kind() {
    local types
    types="$(yq -r '[.limiters[]?.type] | join(" ")')" || return 1
    case " $types " in
        *" quota "*) echo quota ;;
        *" gpu-inventory "*|*" inventory "*) echo inventory ;;
        *) echo none ;;
    esac
}

# us_quota_keys -- the accelerator keys of the quota entries, one per line:
# every key whose value is a count.
us_quota_keys() {
    yq -r '[.limiters[]? | select(.type == "quota") | .. | select(tag == "!!int") | path | .[-1]] | unique | .[]'
}

# us_block_state -- off, shadow or active, or other:<type> for an optimizer
# block this script does not own. Then a second word: physical when the block
# also plans inventory-only groups, plain otherwise.
us_block_state() {
    local t
    t="$(yq -r '(.optimizer.type // "") + " " + ((.optimizer.utilizationShare.shadow // false) | tostring) + " " + ((.optimizer.utilizationShare.physicalGroups // false) | tostring)')" || return 1
    set -- $t
    local type="${1:-}" shadow="${2:-false}" physical="${3:-false}" mode
    if [ -z "$type" ] || [ "$type" = false ]; then
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

# us_policy_with_mode <shadow|active|off> [physical] -- the policy with the
# block in that mode. Every other key, of the block and of the policy, is kept.
us_policy_with_mode() {
    local mode="$1" physical="${2:-}" out
    case "$mode" in
        shadow) out="$(yq '.optimizer.type = "utilizationShare" | .optimizer.utilizationShare.shadow = true')" ;;
        active) out="$(yq '.optimizer.type = "utilizationShare" | .optimizer.utilizationShare.shadow = false')" ;;
        off)    out="$(yq 'del(.optimizer)')" ;;
        *) return 2 ;;
    esac || return 1
    if [ "$physical" = physical ]; then
        out="$(printf '%s\n' "$out" | yq '.optimizer.utilizationShare.physicalGroups = true')" || return 1
    fi
    # Written back empty, this would blank the whole policy.
    [ -n "$(printf '%s' "$out" | tr -d '[:space:]')" ] || return 1
    printf '%s\n' "$out"
}

# ---------------------------------------------------------------------------
# Cluster reads and writes. A read that fails is never an empty answer.
# ---------------------------------------------------------------------------

POLICY=""
POLICY_FOUND=0
read_policy() {
    local out
    if out="$(k get configmap "$POLICY_CM" -n "$POLICY_NS" -o jsonpath='{.data.default}' 2>&1)"; then
        POLICY="$out"
        POLICY_FOUND=1
        return 0
    fi
    case "$out" in
        *NotFound*|*"not found"*) POLICY=""; POLICY_FOUND=0 ;;
        *) die "could not read $POLICY_NS/$POLICY_CM: $out" ;;
    esac
}

block_state() {
    local s
    s="$(printf '%s\n' "$POLICY" | us_block_state)" || die "could not parse the policy in $POLICY_NS/$POLICY_CM"
    printf '%s' "$s"
}

limiter_kind() {
    local s
    s="$(printf '%s\n' "$POLICY" | us_limiter_kind)" || die "could not parse the policy in $POLICY_NS/$POLICY_CM"
    printf '%s' "$s"
}

write_policy() {
    k patch configmap "$POLICY_CM" -n "$POLICY_NS" --type=merge \
        -p "$(jq -cn --arg d "$1" '{data:{"default":$d}}')" >/dev/null \
        || die "could not write $POLICY_NS/$POLICY_CM"
}

# LOGS -- the controller's log, last <since>. Fails loudly: an unreadable log
# is not one that says nothing.
LOGS=""
read_logs() {
    LOGS="$(k logs -n "$CONTROLLER_NS" "deploy/$CONTROLLER_DEPLOY" "$@" 2>&1)" \
        || die "could not read the log of $CONTROLLER_NS/$CONTROLLER_DEPLOY: $LOGS"
}

# confirm_reload <start epoch> -- the controller read the policy written at
# <start> and did not refuse it within a cycle. --since is measured on the
# node's clock, so a local clock that runs ahead cannot hide the reload; the
# 10 s margin only admits lines from just before the write.
confirm_reload() {
    local start="$1" deadline reloaded=0 bad
    deadline=$(( start + RELOAD_WAIT ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        read_logs --since="$(( $(date +%s) - start + 10 ))s"
        if printf '%s\n' "$LOGS" | grep -q 'Updated global scaling policy'; then reloaded=1; break; fi
        sleep 5
    done
    if [ "$reloaded" -eq 0 ]; then
        [ "$RESTART" -eq 1 ] || die "the controller logged no policy reload within ${RELOAD_WAIT}s. A policy in another namespace than the controller is read only at start: rerun with --restart (RESTART=1). The policy WAS written."
        info "the controller did not reload the policy by itself; restarting it"
        k rollout restart "deploy/$CONTROLLER_DEPLOY" -n "$CONTROLLER_NS" >/dev/null || die "could not restart the controller"
        kubectl rollout status "deploy/$CONTROLLER_DEPLOY" -n "$CONTROLLER_NS" --timeout=300s >/dev/null \
            || die "the controller did not come back within 300 s"
        start="$(date +%s)"
    fi
    sleep "$CYCLE_WAIT"
    read_logs --since="$(( $(date +%s) - start + 10 ))s"
    bad="$(printf '%s\n' "$LOGS" | grep -E 'invalid optimizer block|Invalid saturation scaling' | tail -1 || true)"
    [ -z "$bad" ] || die "the controller refused the policy: ${bad:0:400}"
    ok "the controller read the policy and did not refuse it"
}

# set_mode <shadow|active|off> [physical]
set_mode() {
    local updated start
    updated="$(printf '%s\n' "$POLICY" | us_policy_with_mode "$1" "${2:-}")" \
        || die "could not rewrite the policy; nothing was written"
    start="$(date +%s)"
    write_policy "$updated"
    POLICY="$updated"
    info "wrote the optimizer block ($1) into $POLICY_NS/$POLICY_CM"
    confirm_reload "$start"
}

# MARKED -- pods carrying a transfer mark.
MARKED=""
read_marks() {
    local pods
    pods="$(k get pods -n "$NS" -o json 2>&1)" || die "could not list pods in $NS, so the transfer marks were not checked: $pods"
    MARKED="$(printf '%s' "$pods" | jq -r --arg m "$MARK" '.items[] | select(.metadata.annotations[$m] != null) | .metadata.name')"
}

# ---------------------------------------------------------------------------
# Verbs.
# ---------------------------------------------------------------------------

verb_check() {
    local rc=0 kind state line accels events keys a
    read_policy
    [ "$POLICY_FOUND" -eq 1 ] || die "no scaling policy in $POLICY_NS: there is no limiter, so there is no budget to share"
    kind="$(limiter_kind)"
    state="$(block_state)"
    case "$kind" in
        quota) ok "limiter: quota; the optimizer shares each quota group" ;;
        inventory)
            if [ "${state#* }" = physical ]; then
                ok "limiter: gpu-inventory, and the block plans physical groups"
            else
                warn "limiter: gpu-inventory and no quota. The optimizer plans nothing here until the block sets physicalGroups: run shadow with --physical-groups (PHYSICAL_GROUPS=true)"
            fi ;;
        *) fail "the policy declares no limiters: list, so there is no budget to share (the mode would read invalid). Declare one: a quota at install, or make enable-physical-limiter"; rc=1 ;;
    esac

    read_logs
    line="$(printf '%s\n' "$LOGS" | grep -E 'GPU limiter (constructed|\(re\)built from config)|Invalid saturation scaling' | tail -1 || true)"
    case "$line" in
        "") warn "the controller's log names no limiter (yet)" ;;
        *"Invalid saturation scaling"*) fail "the controller refused the policy: ${line:0:300}"; rc=1 ;;
        *) info "controller: ${line:0:300}" ;;
    esac

    accels="$(printf '%s\n' "$LOGS" | grep -o '"accelerator": "[^"]*"' | sed 's/.*: "//; s/"$//' | sort -u || true)"
    if [ -z "$accels" ]; then
        warn "the controller has logged no accelerator yet; rerun once the models have been planned"
    else
        info "accelerators the controller resolves: $(printf '%s' "$accels" | tr '\n' ' ')"
    fi
    events="$(k get events -n "$NS" --field-selector reason=AcceleratorNotResolved -o name 2>&1)" \
        || die "could not read events in $NS: $events"
    if [ -n "$events" ]; then
        fail "$(printf '%s\n' "$events" | grep -c .) AcceleratorNotResolved event(s) in $NS: those variants get no budget"; rc=1
    else
        ok "no AcceleratorNotResolved events"
    fi
    if [ "$kind" = quota ] && [ -n "$accels" ]; then
        keys="$(printf '%s\n' "$POLICY" | us_quota_keys)" || die "could not parse the quota entries"
        for a in $accels; do
            printf '%s\n' "$keys" | grep -qxF -- "$a" \
                || warn "the quota has no '$a' key: its models are charged to no quota group (the key must be the name the controller logs)"
        done
    fi

    case "${state% *}" in
        off) info "optimizer block: none (today's optimizer). Next: shadow" ;;
        shadow) info "optimizer block: shadow. Next: status, then activate" ;;
        active) info "optimizer block: active" ;;
        other:*) warn "the policy has an optimizer block of type ${state%% *}; this script will not replace it" ;;
    esac
    return "$rc"
}

verb_shadow() {
    local state kind physical=""
    read_policy
    [ "$POLICY_FOUND" -eq 1 ] || die "no scaling policy in $POLICY_NS: there is no limiter, so there is no budget to share"
    state="$(block_state)"
    case "${state% *}" in
        other:*) die "the policy already has an optimizer block (${state% *}); change that block instead" ;;
        shadow) if [ "$PHYSICAL" -eq 0 ] || [ "${state#* }" = physical ]; then ok "already in shadow"; return 0; fi ;;
    esac
    kind="$(limiter_kind)"
    [ "$kind" != none ] || die "the policy declares no limiters: list, so there is no budget to share"
    if [ "$PHYSICAL" -eq 1 ]; then
        physical=physical
    elif [ "$kind" = inventory ] && [ "${state#* }" != physical ]; then
        warn "the limiter is gpu-inventory and there is no quota: without --physical-groups the optimizer plans nothing"
    fi
    set_mode shadow "$physical"
    info "shadow computes and reports and moves nothing. Read 'status' over a few bursts before 'activate'."
}

verb_activate() {
    local state
    read_policy
    state="$(block_state)"
    case "${state% *}" in
        active) ok "already active"; return 0 ;;
        shadow) ;;
        off) die "no optimizer block: run shadow first and read what it would move" ;;
        *) die "the policy has an optimizer block of another type (${state% *}); this script will not replace it" ;;
    esac
    set_mode active
    info "for two to three minutes nothing in the group scales (quiet-period); the same follows every controller restart"
}

verb_status() {
    local state decisions ev names warnings
    read_policy
    state="$(block_state)"
    info "optimizer block: ${state% *}$( [ "${state#* }" = physical ] && echo ', physical groups' || true)"
    read_logs --since=30m
    decisions="$(printf '%s\n' "$LOGS" | grep -E 'Utilization share: (would rebalance|invalid optimizer block|transfer (started|ended|cancelled))' | tail -5 || true)"
    if [ -n "$decisions" ]; then
        printf '%s\n' "$decisions" | cut -c1-300 | sed 's/^/  log: /' >&2
    else
        info "no utilization-share decision logged in the last 30 min (normal with no load: shadow logs only when its moves change)"
    fi
    for ev in UtilizationShareGiving UtilizationShareReceived; do
        names="$(k get events -n "$NS" --field-selector "reason=$ev" -o name 2>&1)" || die "could not read events in $NS: $names"
        info "$ev events: $(printf '%s' "$names" | grep -c . || true)"
    done
    warnings="$(k get events -n "$NS" --field-selector type=Warning 2>&1)" || die "could not read events in $NS: $warnings"
    if printf '%s\n' "$warnings" | grep -q UtilizationShare; then
        warn "UtilizationShare warnings:"
        printf '%s\n' "$warnings" | grep UtilizationShare | sed 's/^/    /' >&2
    else
        ok "no UtilizationShare warnings"
    fi
    read_marks
    info "pods marked for a transfer: ${MARKED:-none}"
}

verb_off() {
    local state deadline
    read_policy
    state="$(block_state)"
    case "${state% *}" in
        off) ok "no optimizer block: already off"; return 0 ;;
        other:*) die "the optimizer block is of another type (${state% *}); this script will not remove it" ;;
        active) set_mode shadow ;;
    esac
    # Shadow removes the marks the active optimizer wrote. Removing the block
    # while pods still carry marks would leave them for no one to clear.
    deadline=$(( $(date +%s) + MARK_WAIT ))
    read_marks
    while [ -n "$MARKED" ]; do
        [ "$(date +%s)" -lt "$deadline" ] \
            || die "pods still marked after ${MARK_WAIT}s, so the block stays in shadow: $(printf '%s' "$MARKED" | tr '\n' ' ')"
        sleep 10
        read_marks
    done
    ok "no pod is marked for a transfer"
    set_mode off
    info "today's optimizer runs again; the limiter is untouched"
}

usage() { sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

main() {
    local verb="${1:-}"
    [ $# -gt 0 ] && shift
    NS="" CONTROLLER_NS="" POLICY_NS="" PHYSICAL=0 RESTART=0
    while [ $# -gt 0 ]; do
        case "$1" in
            -n|--namespace) NS="${2:-}"; shift 2 ;;
            --controller-namespace) CONTROLLER_NS="${2:-}"; shift 2 ;;
            --policy-namespace) POLICY_NS="${2:-}"; shift 2 ;;
            --physical-groups) PHYSICAL=1; shift ;;
            --restart) RESTART=1; shift ;;
            -h|--help) usage ;;
            *) fail "unknown argument: $1"; usage ;;
        esac
    done
    [ -n "$NS" ] || { fail "set the models' namespace with -n"; usage; }
    CONTROLLER_NS="${CONTROLLER_NS:-$NS}"
    POLICY_NS="${POLICY_NS:-$CONTROLLER_NS}"
    case "$verb" in
        check) verb_check ;;
        shadow) verb_shadow ;;
        activate) verb_activate ;;
        status) verb_status ;;
        off) verb_off ;;
        *) usage ;;
    esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
