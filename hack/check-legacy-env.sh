#!/usr/bin/env bash
#
# Fixtures for legacy_env_refuse in deploy/lib/common.sh and the Makefile guard
# next to it: an install variable with the old prefix stops the run after the
# rename, and nothing happens before it.
set -u

cd "$(dirname "$0")/.." || exit 1

fail=0
cases=0

check() {
    local name=$1 want_rc=$2 want_text=$3 rc out
    shift 3
    cases=$((cases + 1))
    rc=0
    out=$("$@" 2>&1) || rc=$?
    if [ "$rc" != "$want_rc" ] || { [ -n "$want_text" ] && ! printf '%s' "$out" | grep -qF -- "$want_text"; }; then
        echo "FAIL: $name: rc=$rc, want $want_rc; output: $out"
        fail=1
    fi
}

# The function alone: sourcing common.sh would run its own call first.
fn=$(sed -n '/^legacy_env_refuse() {$/,/^}$/p' deploy/lib/common.sh)
if [ -z "$fn" ]; then
    echo "FAIL: read no legacy_env_refuse from deploy/lib/common.sh"
    exit 1
fi

check "no old variable passes" 0 "" env -u WVA_NS -u WVA_SCOPE bash -c "$fn"$'\n''legacy_env_refuse LLMSM_'
check "an old variable stops the run" 1 "WVA_NS was renamed to LLMSM_NS" env WVA_NS=x bash -c "$fn"$'\n''legacy_env_refuse LLMSM_'
check "every old variable is named" 1 "WVA_SCOPE was renamed to LLMSM_SCOPE" env WVA_NS=x WVA_SCOPE=y bash -c "$fn"$'\n''legacy_env_refuse LLMSM_'
check "equal prefixes do nothing" 0 "" env WVA_NS=x bash -c "$fn"$'\n''legacy_env_refuse WVA_'

# common.sh has to call it, or none of the above protects anything.
if ! grep -qE '^legacy_env_refuse [A-Z]+_ \|\| exit 1' deploy/lib/common.sh; then
    echo "FAIL: deploy/lib/common.sh does not call legacy_env_refuse"
    fail=1
fi

# The Makefile guard, on the tree as it stands: it stops the run only once the
# prefix has moved.
current=$(sed -n 's/^ENV_PREFIX := //p' Makefile | tr -d '\r')
if [ -z "$current" ]; then
    echo "FAIL: read no ENV_PREFIX from the Makefile"
    fail=1
elif [ "$current" = WVA_ ]; then
    check "make before the rename" 0 "" env WVA_NS=x make -n --no-print-directory lint-config
else
    check "make after the rename" 2 "renamed, use the $current prefix" env WVA_NS=x make -n --no-print-directory lint-config
fi

if [ "$cases" != 5 ]; then
    echo "FAIL: ran $cases cases, want 5"
    fail=1
fi
[ "$fail" = 0 ] && echo "ok: legacy_env ($cases cases)"
exit "$fail"
