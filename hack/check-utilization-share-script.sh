#!/usr/bin/env bash
#
# Executes deploy/utilization-share.sh: its policy transforms against fixture
# policies, and every verb against a STUBBED kubectl, asserting what it would
# have written and what it refused to write.
#
# The verbs edit the one policy every model's scaling reads, so the cases that
# matter most are refusals: a pod list that fails must not read as "no pod is
# marked" (off would remove the block and strand the marks), a policy that
# cannot be read must not read as "no policy", and a block this script does not
# own is never replaced.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
CASES=0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# CR-stripped, for the reason hack/check-limiter-declaration.sh gives.
tr -d '\r' < "$ROOT/deploy/utilization-share.sh" > "$WORK/us.sh"

# shellcheck disable=SC1090
. "$WORK/us.sh"
set +e
# AFTER the source: the script defines its own ok and fail, and with those a
# failing case printed [FAIL] and counted nothing, so the check passed.
ok()   { echo "  ok   $*"; }
fail() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
case_() { CASES=$((CASES + 1)); }

QUOTA_POLICY='# shipped thresholds
scaleUpThreshold: 0.85
limiters:
  - name: install-quota
    type: quota
    scope: namespace
    namespaceQuotas:
      team-a:
        NVIDIA-H200: 8'
INV_POLICY='scaleUpThreshold: 0.85
limiters:
  - name: physical
    type: gpu-inventory'
TUNED_POLICY="$QUOTA_POLICY
optimizer:
  type: utilizationShare
  utilizationShare:
    shadow: true
    maxHeadroomReplicas: 2"

expect() { # expect <name> <want> <got>
    case_
    if [ "$2" = "$3" ]; then ok "$1"; else fail "$1: want [$2], got [$3]"; fi
}

# --- transforms -------------------------------------------------------------
expect "kind: quota" quota "$(printf '%s\n' "$QUOTA_POLICY" | us_limiter_kind)"
expect "kind: gpu-inventory" inventory "$(printf '%s\n' "$INV_POLICY" | us_limiter_kind)"
expect "kind: the inventory alias" inventory "$(printf 'limiters:\n  - name: p\n    type: inventory\n' | us_limiter_kind)"
expect "kind: quota and inventory is quota" quota "$(printf '%s\n  - name: p\n    type: gpu-inventory\n' "$QUOTA_POLICY" | us_limiter_kind)"
expect "kind: no limiters" none "$(printf 'scaleUpThreshold: 0.85\n' | us_limiter_kind)"
expect "quota keys" "NVIDIA-H200" "$(printf '%s\n' "$QUOTA_POLICY" | us_quota_keys)"
expect "state: no block" "off plain" "$(printf '%s\n' "$QUOTA_POLICY" | us_block_state)"
expect "state: tuned shadow" "shadow plain" "$(printf '%s\n' "$TUNED_POLICY" | us_block_state)"
expect "state: another optimizer" "other:fancy plain" "$(printf 'optimizer:\n  type: fancy\n' | us_block_state)"

out="$(printf '%s\n' "$TUNED_POLICY" | us_policy_with_mode active)"
expect "active: shadow false" false "$(printf '%s\n' "$out" | yq '.optimizer.utilizationShare.shadow')"
expect "active: tuning kept" 2 "$(printf '%s\n' "$out" | yq '.optimizer.utilizationShare.maxHeadroomReplicas')"
expect "active: limiters kept" 8 "$(printf '%s\n' "$out" | yq '.limiters[0].namespaceQuotas.team-a.NVIDIA-H200')"
case_
if printf '%s\n' "$out" | grep -q '^# shipped thresholds'; then ok "active: comments kept"; else fail "active: the policy's comment was dropped"; fi
out="$(printf '%s\n' "$INV_POLICY" | us_policy_with_mode shadow physical)"
expect "shadow physical: physicalGroups" true "$(printf '%s\n' "$out" | yq '.optimizer.utilizationShare.physicalGroups')"
expect "shadow physical: shadow" true "$(printf '%s\n' "$out" | yq '.optimizer.utilizationShare.shadow')"
out="$(printf '%s\n' "$TUNED_POLICY" | us_policy_with_mode off)"
expect "off: block removed" null "$(printf '%s\n' "$out" | yq '.optimizer')"
expect "off: everything else as before" \
    "$(printf '%s\n' "$QUOTA_POLICY" | yq -o=json -I=0 '.')" "$(printf '%s\n' "$out" | yq -o=json -I=0 '.')"

# The keys written must be the ones the controller decodes.
case_
src="$(tr -d '\r' < "$ROOT/internal/config/utilization_share.go")"
missing=""
for tag in 'yaml:"shadow' 'yaml:"physicalGroups' 'yaml:"utilizationShare'; do
    printf '%s' "$src" | grep -qF "$tag" || missing="$missing $tag"
done
printf '%s' "$src" | grep -qE 'OptimizerTypeUtilizationShare[[:space:]]*=[[:space:]]*"utilizationShare"' || missing="$missing type-constant"
if [ -z "$missing" ]; then ok "the keys written are the ones internal/config decodes"; else fail "internal/config no longer decodes:$missing"; fi

# --- verbs, against a stubbed kubectl ---------------------------------------
STATE="$WORK/state"
mkdir -p "$WORK/bin"
cat > "$WORK/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
S="$STUB_STATE"
args=" $* "
case "$args" in
  *" get configmap "*)
    [ -e "$S/cm.forbidden" ] && { echo 'Error from server (Forbidden): configmaps is forbidden' >&2; exit 1; }
    [ -e "$S/policy" ] || { echo 'Error from server (NotFound): configmaps "wva-scaling-policy-config" not found' >&2; exit 1; }
    cat "$S/policy" ;;
  *" patch configmap "*)
    p=""; prev=""
    for a in "$@"; do [ "$prev" = "-p" ] && p="$a"; prev="$a"; done
    printf '%s' "$p" | jq -r '.data.default' > "$S/policy"
    echo x >> "$S/patches"
    [ -e "$S/noreload" ] || echo 'INFO Updated global scaling policy from ConfigMap' >> "$S/logs"
    grep -q 'bogus' "$S/policy" && echo 'ERROR Utilization share: invalid optimizer block; keeping the optimizer' >> "$S/logs"
    # shadow removes the active optimizer's marks
    grep -q 'shadow: true' "$S/policy" && [ ! -e "$S/marks.stick" ] && echo '{"items":[]}' > "$S/pods.json"
    exit 0 ;;
  *" logs "*)
    [ -e "$S/logs.forbidden" ] && { echo 'Error from server (Forbidden): pods/log is forbidden' >&2; exit 1; }
    cat "$S/logs" 2>/dev/null; exit 0 ;;
  *" get events "*)
    [ -e "$S/events.forbidden" ] && { echo 'Error from server (Forbidden): events is forbidden' >&2; exit 1; }
    case "$args" in
      *AcceleratorNotResolved*) cat "$S/events.unresolved" 2>/dev/null ;;
      *type=Warning*) cat "$S/events.warning" 2>/dev/null ;;
    esac
    exit 0 ;;
  *" get pods "*)
    [ -e "$S/pods.forbidden" ] && { echo 'Error from server (Forbidden): pods is forbidden' >&2; exit 1; }
    cat "$S/pods.json" 2>/dev/null || echo '{"items":[]}' ;;
  *" rollout "*) echo 'INFO Updated global scaling policy from ConfigMap' >> "$S/logs"; exit 0 ;;
  *) echo "stub kubectl: unhandled: $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$WORK/bin/kubectl"
MARKED_PODS='{"items":[{"metadata":{"name":"donor-0","annotations":{"llm-d.ai/utilization-share-transfer":"t1"}}}]}'

# run <verb> [args] -- a fresh process, as make runs it. STATE must be set up.
run() {
    PATH="$WORK/bin:$PATH" STUB_STATE="$STATE" RELOAD_WAIT=2 CYCLE_WAIT=0 MARK_WAIT="${MARK_WAIT_T:-2}" \
        bash "$WORK/us.sh" "$1" -n team-a "${@:2}" > "$WORK/out" 2>&1
    RC=$?
}
fresh() { rm -rf "$STATE"; mkdir -p "$STATE"; [ -n "${1:-}" ] && printf '%s\n' "$1" > "$STATE/policy"; :; }
patches() { [ -e "$STATE/patches" ] && grep -c . "$STATE/patches" || echo 0; }
pol() { yq "$1" "$STATE/policy"; }
says() { grep -qF -- "$1" "$WORK/out"; }

vcase() { # vcase <name> <condition...>
    local name="$1"; shift
    case_
    if "$@"; then ok "$name"; else fail "$name (rc=$RC): $(tr '\n' ' ' < "$WORK/out" | cut -c1-300)"; fi
}

fresh "$QUOTA_POLICY"; run shadow
vcase "shadow: writes shadow true and confirms the reload" \
    eval '[ $RC -eq 0 ] && [ "$(pol .optimizer.utilizationShare.shadow)" = true ] && says "did not refuse it"'

fresh "$INV_POLICY"; run shadow --physical-groups
vcase "shadow --physical-groups: plans the inventory" \
    eval '[ $RC -eq 0 ] && [ "$(pol .optimizer.utilizationShare.physicalGroups)" = true ]'

fresh "$INV_POLICY"; run shadow
vcase "shadow on inventory without the flag: warns it plans nothing" \
    eval '[ $RC -eq 0 ] && says "--physical-groups"'

fresh "$QUOTA_POLICY
optimizer:
  type: fancy"; run shadow
vcase "shadow: refuses another optimizer, writes nothing" eval '[ $RC -ne 0 ] && [ "$(patches)" = 0 ]'

fresh 'scaleUpThreshold: 0.85'; run shadow
vcase "shadow: refuses a policy with no limiters" eval '[ $RC -ne 0 ] && [ "$(patches)" = 0 ]'

fresh; run shadow
vcase "shadow: refuses when there is no policy" eval '[ $RC -ne 0 ] && [ "$(patches)" = 0 ]'

fresh "$QUOTA_POLICY"; touch "$STATE/cm.forbidden"; run shadow
vcase "shadow: an unreadable policy is not an absent one" \
    eval '[ $RC -ne 0 ] && says "Forbidden" && [ "$(patches)" = 0 ]'

fresh "$QUOTA_POLICY"; run activate
vcase "activate: refuses with no shadow block first" eval '[ $RC -ne 0 ] && [ "$(patches)" = 0 ]'

fresh "$TUNED_POLICY"; run activate
vcase "activate: from shadow sets shadow false, keeps tuning" \
    eval '[ $RC -eq 0 ] && [ "$(pol .optimizer.utilizationShare.shadow)" = false ] && [ "$(pol .optimizer.utilizationShare.maxHeadroomReplicas)" = 2 ]'

fresh "$TUNED_POLICY
    bogus: 1"; run activate
vcase "activate: reports the controller's refusal" eval '[ $RC -ne 0 ] && says "refused the policy"'

fresh "$TUNED_POLICY"; touch "$STATE/noreload"; run activate
vcase "activate: no reload is a failure that names --restart" eval '[ $RC -ne 0 ] && says "--restart"'

fresh "$TUNED_POLICY"; touch "$STATE/noreload"; run activate --restart
vcase "activate --restart: restarts and confirms" eval '[ $RC -eq 0 ] && says "restarting"'

fresh "$(printf '%s\n' "$TUNED_POLICY" | us_policy_with_mode active)"; printf '%s' "$MARKED_PODS" > "$STATE/pods.json"; run off
vcase "off: through shadow, marks cleared, block removed" \
    eval '[ $RC -eq 0 ] && [ "$(pol .optimizer)" = null ] && [ "$(pol .limiters[0].type)" = quota ] && [ "$(patches)" = 2 ]'

fresh "$(printf '%s\n' "$TUNED_POLICY" | us_policy_with_mode active)"; printf '%s' "$MARKED_PODS" > "$STATE/pods.json"; touch "$STATE/pods.forbidden"; run off
vcase "off: pods that cannot be listed keep the block" \
    eval '[ $RC -ne 0 ] && [ "$(pol .optimizer.type)" = utilizationShare ] && says "marks were not checked"'

fresh "$(printf '%s\n' "$TUNED_POLICY" | us_policy_with_mode active)"; printf '%s' "$MARKED_PODS" > "$STATE/pods.json"; touch "$STATE/marks.stick"; MARK_WAIT_T=1 run off
vcase "off: marks that do not clear keep the block in shadow" \
    eval '[ $RC -ne 0 ] && [ "$(pol .optimizer.utilizationShare.shadow)" = true ] && says "still marked"'

fresh "$QUOTA_POLICY"; run off
vcase "off: nothing to remove is not an error" eval '[ $RC -eq 0 ] && [ "$(patches)" = 0 ]'

fresh "$QUOTA_POLICY"; echo 'INFO GPU limiter (re)built from config {"type": "quota"} {"accelerator": "NVIDIA-H200"}' > "$STATE/logs"; run check
vcase "check: a resolving quota passes" eval '[ $RC -eq 0 ] && says "no AcceleratorNotResolved"'

# H200 is a substring of the quota's NVIDIA-H200 key; a substring match passed it.
fresh "$QUOTA_POLICY"; echo '{"accelerator": "H200"}' > "$STATE/logs"; run check
vcase "check: a quota key that is not the logged name warns" eval 'says "has no '"'"'H200'"'"' key"'

fresh "$QUOTA_POLICY"; echo 'event/x' > "$STATE/events.unresolved"; run check
vcase "check: an unresolved accelerator fails" eval '[ $RC -ne 0 ] && says "AcceleratorNotResolved event"'

fresh "$QUOTA_POLICY"; touch "$STATE/events.forbidden"; run check
vcase "check: unreadable events fail, not pass" eval '[ $RC -ne 0 ] && says "could not read events"'

fresh "$QUOTA_POLICY"; touch "$STATE/logs.forbidden"; run check
vcase "check: an unreadable log fails, not reads as empty" eval '[ $RC -ne 0 ] && says "could not read the log"'

fresh 'scaleUpThreshold: 0.85'; run check
vcase "check: no limiters fails" eval '[ $RC -ne 0 ] && says "no limiters: list"'

fresh "$INV_POLICY"; run check
vcase "check: inventory without physical groups says how to plan it" eval '[ $RC -eq 0 ] && says "--physical-groups"'

fresh "$TUNED_POLICY"; printf '%s' "$MARKED_PODS" > "$STATE/pods.json"; echo 'LAST SEEN TYPE REASON
1m Warning UtilizationShareCancelled deploy/a' > "$STATE/events.warning"; run status
vcase "status: reports the mode, warnings and marks" \
    eval '[ $RC -eq 0 ] && says "optimizer block: shadow" && says "UtilizationShareCancelled" && says "donor-0"'

CASES_EXPECTED=42
if [ "$CASES" -ne "$CASES_EXPECTED" ]; then
    fail "$CASES cases ran, not $CASES_EXPECTED. A case was added or removed; update CASES_EXPECTED deliberately."
fi
if [ "$FAIL" -ne 0 ]; then
    echo "utilization-share script check: $FAIL failure(s)"
    exit 1
fi
echo "utilization-share script check OK ($CASES cases)"
