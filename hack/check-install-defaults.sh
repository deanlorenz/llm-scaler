#!/usr/bin/env bash
#
# Executes the installer's default-choosing helpers against fixtures.
#
# These decide what a plain `make deploy-wva` ships: which image is pulled and
# how, and whether the operator is told when the result is not reproducible.
# Every defect here parses cleanly and installs cleanly -- a registry port read
# as a tag, a digest treated as a moving tag, a floating tag deployed in
# silence -- so `bash -n` and a green install both miss it.
#
# WRITING A CASE HERE: an expected-EMPTY case proves nothing on its own -- a
# function that does not exist returns empty too. The guard below refuses to run
# unless every function is defined, and EXPECT_CASES refuses to pass a run that
# ended early.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

command -v yq >/dev/null 2>&1 || {
    printf 'FATAL: yq is required to run these checks.\n' >&2
    exit 1
}

# Sourced through CR-stripped copies: a Windows checkout with core.autocrlf
# makes bash stop at the first function definition, which looks exactly like
# every function being missing.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh cli.sh infra_wva.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE='' GREEN='' YELLOW='' RED='' NC=''
# shellcheck source=/dev/null
source "$WORK/common.sh"
# shellcheck source=/dev/null
source "$WORK/cli.sh"
# shellcheck source=/dev/null
source "$WORK/infra_wva.sh"

for fn in wva_split_image_ref wva_image_pull_policy wva_warn_floating_image; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=17
RAN=0
FAILED=0

# check <name> <want> <got>
check() {
    if [ "$2" = "$3" ]; then
        RAN=$((RAN + 1))
        printf 'ok    %s\n' "$1"
    else
        FAILED=$((FAILED + 1))
        printf 'FAIL  %s\n        want [%s]\n        got  [%s]\n' "$1" "$2" "$3" >&2
    fi
}

# ----------------------------------------------------------------------------
# Image reference: IMG -> repo, tag, digest
# ----------------------------------------------------------------------------
split() {
    WVA_IMAGE_REPO='' WVA_IMAGE_TAG='' WVA_IMAGE_DIGEST=''
    wva_split_image_ref "$1" || { printf 'rejected'; return 0; }
    printf '%s|%s|%s' "$WVA_IMAGE_REPO" "$WVA_IMAGE_TAG" "$WVA_IMAGE_DIGEST"
}

check 'image: repo:tag' \
    'ghcr.io/o/llm-scaling-manager|main|' \
    "$(split ghcr.io/o/llm-scaling-manager:main)"

# Splitting on the first colon read the port as the tag.
check 'image: a registry port is not a tag' \
    'localhost:5000/wva|dev|' \
    "$(split localhost:5000/wva:dev)"

check 'image: a digest is carried as a digest' \
    'ghcr.io/o/wva||sha256:abc123' \
    "$(split ghcr.io/o/wva@sha256:abc123)"

check 'image: repo:tag@digest keeps both' \
    'localhost:5000/wva|v1.2.3|sha256:abc123' \
    "$(split localhost:5000/wva:v1.2.3@sha256:abc123)"

check 'image: a bare repo is rejected' \
    'rejected' \
    "$(split localhost:5000/wva)"

# Through parse_args, the path IMG actually takes: a tagless IMG keeps the
# defaults rather than installing half of it.
via_img() {
    (
        WVA_IMAGE_REPO=default/repo WVA_IMAGE_TAG=deftag WVA_IMAGE_DIGEST=''
        IMG="$1"
        parse_args 2>/dev/null
        printf '%s|%s|%s' "$WVA_IMAGE_REPO" "$WVA_IMAGE_TAG" "$WVA_IMAGE_DIGEST"
    )
}

check 'IMG: registry port with tag' \
    'host:5000/team/wva|v1|' \
    "$(via_img host:5000/team/wva:v1)"

check 'IMG: digest pins by digest' \
    'ghcr.io/o/wva||sha256:0123abcd' \
    "$(via_img ghcr.io/o/wva@sha256:0123abcd)"

check 'IMG: no tag keeps the default image' \
    'default/repo|deftag|' \
    "$(via_img host:5000/team/wva)"

check 'IMG: plain repo:tag' \
    'ghcr.io/o/wva|main|' \
    "$(via_img ghcr.io/o/wva:main)"

# ----------------------------------------------------------------------------
# Pull policy: IfNotPresent only for what cannot move
# ----------------------------------------------------------------------------
check 'pull policy: main is Always'          'Always'       "$(wva_image_pull_policy main '')"
check 'pull policy: a digest is IfNotPresent' 'IfNotPresent' "$(wva_image_pull_policy main sha256:abc)"
check 'pull policy: a release tag is IfNotPresent' 'IfNotPresent' "$(wva_image_pull_policy v0.6.0 '')"
# ref-<sha> is re-pushed after a merge in the OpenShift e2e; unknown means Always.
check 'pull policy: an unclassified tag is Always' 'Always' "$(wva_image_pull_policy ref-0123abcd '')"

# ----------------------------------------------------------------------------
# Floating-tag warning
# ----------------------------------------------------------------------------
warned() { wva_warn_floating_image "$@" 2>&1 | grep -c 'FLOATING tag' || true; }

check 'warning: main is announced'      '1' "$(warned ghcr.io/o/wva main '')"
check 'warning: latest is announced'    '1' "$(warned ghcr.io/o/wva latest '')"
check 'warning: a release tag is quiet' '0' "$(warned ghcr.io/o/wva v0.6.0 '')"
check 'warning: a digest is quiet'      '0' "$(warned ghcr.io/o/wva main sha256:abc)"

# ----------------------------------------------------------------------------

if [ "$FAILED" -gt 0 ]; then
    printf '\n%d install-default assertion(s) failed.\n' "$FAILED" >&2
    exit 1
fi
if [ "$RAN" -ne "$EXPECT_CASES" ]; then
    printf '\n%d case(s) reported ok, %d expected -- the run did not finish.\n' \
        "$RAN" "$EXPECT_CASES" >&2
    exit 1
fi
printf '\nInstall default checks passed (%d cases).\n' "$RAN"
