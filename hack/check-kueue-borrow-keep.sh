#!/usr/bin/env bash
#
# Executes policy_with_limiters() in deploy/lib/limiter_policy.sh and asserts
# that rebuilding a Kueue quota entry KEEPS the borrowing settings written by
# hand into the live one: kueue.borrowNamespaces (the pilot list) and, unless
# WVA_QUOTA_KUEUE_BORROW is given, kueue.countBorrowable.
#
# It exists because `make deploy-wva WVA_QUOTA_KUEUE=true` with WVA_LIMITER set,
# and `make enable-physical-limiter WVA_LIMITER_TYPE=quota`, rebuilt the entry
# from install variables, and no variable carries borrowNamespaces: a re-run
# dropped the pilot list, and borrowing was counted for every namespace from
# then on. The document stayed valid, so nothing failed.
#
# The installer composes AFTER its apply reset the policy, so it hands the
# limiters it read before the apply in LIMITERS_KEEP_FROM; the cluster-policy
# path composes over the live policy itself. Both are exercised.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
PASS=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh limiter_policy.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
# shellcheck disable=SC1090
. "$WORK/common.sh"
unset WVA_QUOTAS WVA_QUOTA_SCOPE WVA_QUOTA_KUEUE WVA_QUOTA_KUEUE_BORROW WVA_SCOPE WVA_WATCH_NS WVA_LIMITER WVA_LIMITER_TYPE LIMITERS_KEEP_FROM
WVA_NS="wva-system"
# shellcheck disable=SC1090
. "$WORK/limiter_policy.sh"

for tool in yq jq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FATAL: $tool is required by this check, and is missing. Nothing below would be about the code."
        exit 2
    }
done

ok() { echo "ok   $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL $1"; FAIL=$((FAIL + 1)); }

# The live entry an operator piloted: borrowing for team-a only, and off.
OLD_ENTRY='- name: install-quota
  type: quota
  scope: namespace
  kueue:
    enabled: true
    countBorrowable: false
    borrowNamespaces: [team-a]
  namespaceQuotas:
    default:
      H100: -1'
LIVE_POLICY="scaleUpThreshold: 0.85
limiters:
$OLD_ENTRY"

# field <yaml> <path> prints a field of the first quota entry, or null.
field() {
    printf '%s\n' "$1" | yq "[.limiters[] | select(.type == \"quota\")] | .[0] | $2"
}

new_entry() {
    WVA_QUOTAS='H100=-1' WVA_QUOTA_KUEUE=true WVA_SCOPE=cluster limiter_set_yaml quota default
}

# 1. Cluster-policy path: composed over the live policy.
ENTRY="$(new_entry)" || { echo "FATAL: limiter_set_yaml failed"; exit 2; }
[ -n "$ENTRY" ] || { echo "FATAL: limiter_set_yaml emitted nothing; nothing below would be about the keep"; exit 2; }
OUT="$(policy_with_limiters "$LIVE_POLICY" "$ENTRY" compose)"
[ "$(field "$OUT" '.kueue.borrowNamespaces | join(",")')" = "team-a" ] \
    && ok "enable-physical-limiter keeps borrowNamespaces from the live entry" \
    || fail "enable-physical-limiter dropped borrowNamespaces: $(field "$OUT" '.kueue')"
[ "$(field "$OUT" '.kueue.countBorrowable')" = "false" ] \
    && ok "enable-physical-limiter keeps countBorrowable: false when WVA_QUOTA_KUEUE_BORROW is not given" \
    || fail "enable-physical-limiter dropped countBorrowable: $(field "$OUT" '.kueue')"
[ "$(printf '%s\n' "$OUT" | yq '.limiters | length')" = "1" ] \
    && ok "one quota entry after the rebuild, not two" \
    || fail "the rebuild left $(printf '%s\n' "$OUT" | yq '.limiters | length') entries"
[ "$(printf '%s\n' "$OUT" | yq '.scaleUpThreshold')" = "0.85" ] \
    && ok "the rest of the policy is kept" \
    || fail "the rest of the policy was lost: $OUT"

# 2. An explicit WVA_QUOTA_KUEUE_BORROW decides countBorrowable; the pilot list
#    is still kept.
OUT="$(WVA_QUOTA_KUEUE_BORROW=true policy_with_limiters "$LIVE_POLICY" "$ENTRY" compose)"
[ "$(field "$OUT" '.kueue.countBorrowable')" = "null" ] \
    && ok "WVA_QUOTA_KUEUE_BORROW=true is not overridden by the live countBorrowable: false" \
    || fail "the live countBorrowable overrode WVA_QUOTA_KUEUE_BORROW=true: $(field "$OUT" '.kueue')"
[ "$(field "$OUT" '.kueue.borrowNamespaces | join(",")')" = "team-a" ] \
    && ok "borrowNamespaces is kept beside an explicit WVA_QUOTA_KUEUE_BORROW" \
    || fail "borrowNamespaces dropped beside WVA_QUOTA_KUEUE_BORROW: $(field "$OUT" '.kueue')"

# 3. Install path: the apply reset the policy; the limiters read before it come
#    in LIMITERS_KEEP_FROM, as JSON (wva_live_limiters).
RESET_POLICY='scaleUpThreshold: 0.85'
KEEP="$(printf '%s\n' "$OLD_ENTRY" | yq -o=json -I=0 '.')"
OUT="$(LIMITERS_KEEP_FROM="$KEEP" policy_with_limiters "$RESET_POLICY" "$ENTRY")"
[ "$(field "$OUT" '.kueue.borrowNamespaces | join(",")')" = "team-a" ] \
    && ok "deploy-wva keeps borrowNamespaces read before its apply reset the policy" \
    || fail "deploy-wva dropped borrowNamespaces: $(field "$OUT" '.kueue')"
[ "$(field "$OUT" '.kueue.countBorrowable')" = "false" ] \
    && ok "deploy-wva keeps countBorrowable read before its apply" \
    || fail "deploy-wva dropped countBorrowable: $(field "$OUT" '.kueue')"

# 4. Another name, the only Kueue quota entry: still the one replaced.
RENAMED="$(printf '%s\n' "$LIVE_POLICY" | yq '.limiters[0].name = "tenant-quota"')"
OUT="$(policy_with_limiters "$RENAMED" "$ENTRY" compose)"
[ "$(field "$OUT" '.kueue.borrowNamespaces | join(",")')" = "team-a" ] \
    && ok "a renamed single Kueue entry is kept from" \
    || fail "a renamed single Kueue entry was not kept from: $(field "$OUT" '.kueue')"

# Controls: nothing is added where there is nothing to keep, or no kueue block.
OUT="$(policy_with_limiters "$RESET_POLICY" "$ENTRY")"
[ "$(field "$OUT" '.kueue.borrowNamespaces')" = "null" ] \
    && ok "control: no live entry, no borrowNamespaces invented" \
    || fail "borrowNamespaces invented from nothing: $(field "$OUT" '.kueue')"
PLAIN="$(WVA_QUOTAS='H100=8' WVA_SCOPE=cluster limiter_set_yaml quota default)"
OUT="$(policy_with_limiters "$LIVE_POLICY" "$PLAIN" compose)"
[ "$(field "$OUT" '.kueue')" = "null" ] \
    && ok "control: an entry without a kueue block gets none" \
    || fail "a kueue block was added to an entry without one: $(field "$OUT" '.kueue')"

echo "check-kueue-borrow-keep: $PASS passed, $FAIL failed"
[ "$PASS" -gt 0 ] || { echo "FAIL: no case ran"; exit 1; }
[ "$FAIL" -eq 0 ]
