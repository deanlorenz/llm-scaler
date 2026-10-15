#!/usr/bin/env bash
#
# Runs the release workflow's "Resolve image, version and tags" step against a
# scratch git repository and asserts when it moves `latest`.
#
# Only the highest final vMAJOR.MINOR.PATCH tag may move it. Before this, every
# final tag did, so a backport (v1.2.1, tagged after v1.3.0) moved `latest` back
# to the older line. Nothing else can see that: the step is valid shell, and a
# release run looks the same either way.
#
# The step's own script is read out of .github/workflows/ci-release.yaml, so
# this tests what the workflow runs, not a copy. Needs git and yq.

set -u

ROOT="${CHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
WORKFLOW="$ROOT/.github/workflows/ci-release.yaml"
# One ok or FAIL per case. A run that stops early otherwise passes having
# checked less than it says. Change this deliberately with the cases.
EXPECT_CASES=7
PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); echo "ok   $1"; }
fail() { FAIL=$((FAIL + 1)); echo "FAIL $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/       /'; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

tr -d '\r' < "$WORKFLOW" > "$WORK/release.yaml"
yq '.jobs.release.steps[] | select(.id == "meta") | .run' "$WORK/release.yaml" > "$WORK/meta.sh"
if ! grep -q 'GITHUB_OUTPUT' "$WORK/meta.sh"; then
    echo "FAIL could not read the meta step out of $WORKFLOW"
    exit 1
fi

# The checkout must bring the tags the step compares against.
depth="$(yq '.jobs.release.steps[] | select(.uses == "actions/checkout*") | .with."fetch-depth"' "$WORK/release.yaml")"
if [ "$depth" = 0 ]; then
    ok "the release checkout fetches full history and tags (fetch-depth: 0)"
else
    fail "the release checkout must use fetch-depth: 0, or the step sees no other tag (got '$depth')"
fi

# resolve <ref> <tag>... -- a repository holding those tags, the step run for
# <ref>; leaves its outputs in $out and its log in $log.
resolve() {
    local ref="$1" repo="$WORK/repo" t
    shift
    rm -rf "$repo"
    git init -q "$repo"
    git -C "$repo" -c user.name=check -c user.email=check@example.invalid commit -q --allow-empty -m base
    for t in "$@"; do
        git -C "$repo" tag "$t"
    done
    : > "$WORK/out"
    log="$(cd "$repo" && env REF_NAME="$ref" GITHUB_OUTPUT="$WORK/out" \
        GITHUB_REPOSITORY_OWNER=Owner GITHUB_REPOSITORY=Owner/repo bash "$WORK/meta.sh" 2>&1)"
    rc=$?
    out="$(cat "$WORK/out")"
}
tags() { printf '%s\n' "$out" | sed -n 's/^tags=//p'; }

resolve v1.3.0 v1.2.0 v1.3.0
if [ "$rc" -eq 0 ] && [ "$(tags)" = "ghcr.io/owner/repo:v1.3.0,ghcr.io/owner/repo:latest" ]; then
    ok "the highest final tag moves latest"
else
    fail "v1.3.0, the highest final tag, must move latest (rc=$rc)" "$log"
fi

resolve v1.2.1 v1.2.0 v1.3.0 v1.2.1
if [ "$rc" -eq 0 ] && [ "$(tags)" = "ghcr.io/owner/repo:v1.2.1" ]; then
    ok "a backport (v1.2.1 after v1.3.0) does not move latest"
else
    fail "a backport must not move latest back (rc=$rc, tags=$(tags))" "$log"
fi

resolve v1.4.0-rc1 v1.3.0 v1.4.0-rc1
if [ "$rc" -eq 0 ] && [ "$(tags)" = "ghcr.io/owner/repo:v1.4.0-rc1" ] \
    && printf '%s\n' "$out" | grep -qx 'prerelease=true'; then
    ok "a prerelease does not move latest"
else
    fail "a prerelease must not move latest (rc=$rc, tags=$(tags))" "$log"
fi

resolve v1.3.0 v1.3.0 v1.4.0-rc1
if [ "$rc" -eq 0 ] && [ "$(tags)" = "ghcr.io/owner/repo:v1.3.0,ghcr.io/owner/repo:latest" ]; then
    ok "a newer prerelease does not stop the highest final tag moving latest"
else
    fail "v1.3.0 is the highest FINAL tag beside v1.4.0-rc1, and must move latest (rc=$rc)" "$log"
fi

resolve v1.10.0 v1.9.0 v1.10.0
if [ "$rc" -eq 0 ] && [ "$(tags)" = "ghcr.io/owner/repo:v1.10.0,ghcr.io/owner/repo:latest" ]; then
    ok "versions compare as versions: v1.10.0 is above v1.9.0"
else
    fail "v1.10.0 must sort above v1.9.0 and move latest (rc=$rc)" "$log"
fi

resolve v1.3.0
if [ "$rc" -ne 0 ] && printf '%s\n' "$log" | grep -q 'tags were not fetched'; then
    ok "a checkout with no tags refuses, rather than guessing"
else
    fail "with no tags fetched the step must refuse (rc=$rc, tags=$(tags))" "$log"
fi

echo ""
echo "check-release-latest: $PASS passed, $FAIL failed"
if [ $((PASS + FAIL)) -ne "$EXPECT_CASES" ]; then
    echo "check-release-latest: $((PASS + FAIL)) case(s) ran, $EXPECT_CASES expected"
    exit 1
fi
[ "$FAIL" -eq 0 ]
