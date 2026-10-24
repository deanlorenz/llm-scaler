#!/usr/bin/env bash
#
# Fails when a tracked file names one of the internal clusters measurements
# were taken on.
#
# Comments, docs and tests say "the shared GPU cluster" or describe the
# hardware ("a multi-node H200 cluster") instead, as PR bodies already do. The
# names themselves are not written here: the check compares the SHA-256 of
# every lowercased alphanumeric word against the digests below, so the list
# does not publish what it guards. To add a name, append the first 16 hex
# digits of `printf %s <name> | sha256sum`.
#
# Two files are exempt because a deployment depends on the name: the PR e2e
# workflow selects its runners by label, and verify-correct-cluster.sh refuses
# a run whose API server is the nightly-only cluster.
#
# The check runs once against a fixture holding a synthetic word whose digest
# is in the list, so a scan that matches nothing cannot pass silently.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PY="$(command -v python3 || command -v python)" || { echo "FAIL: python3 not found"; exit 1; }

DIGESTS="550139a3a4338ebe 556c7df75b76c6cc 94d399c353b47d53 7700a7306a4966d9 0fd6540147c8a2a2"
EXEMPT=".github/workflows/ci-e2e-openshift.yaml deploy/ci-e2e-openshift/verify-correct-cluster.sh"

# scan <git-dir> prints "<path>:<line>" for every hit and exits 1 if any.
scan() {
    git -C "$1" ls-files -z | DIGESTS="$DIGESTS" EXEMPT="$EXEMPT" "$PY" -c '
import hashlib, os, re, sys
root = sys.argv[1]
digests = set(os.environ["DIGESTS"].split())
exempt = set(os.environ["EXEMPT"].split())
hits = 0
for rel in sys.stdin.read().split("\0"):
    if not rel or rel in exempt:
        continue
    try:
        with open(os.path.join(root, rel), "rb") as fh:
            data = fh.read()
    except OSError:
        continue
    if b"\0" in data[:8192]:
        continue
    text = data.decode("utf-8", "replace").lower()
    for n, line in enumerate(text.splitlines(), 1):
        for word in set(re.findall(r"[a-z0-9]+", line)):
            if hashlib.sha256(word.encode()).hexdigest()[:16] in digests:
                print(f"{rel}:{n}")
                hits += 1
sys.exit(1 if hits else 0)
' "$1"
}

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

# Self-test: the fixture word is built at runtime so this file never holds it.
git -C "$T" init -q
printf 'measured on %s%s%s today\n' zz clusterfixture zz > "$T/fixture.md"
git -C "$T" add fixture.md
if out="$(scan "$T")" || [ "$out" != "fixture.md:1" ]; then
    echo "FAIL: the scan did not report the fixture (got: ${out:-nothing})"
    exit 1
fi

if ! out="$(scan "$ROOT")"; then
    echo "FAIL: tracked files name an internal cluster; say \"the shared GPU cluster\" or describe its hardware:"
    printf '  %s\n' $out
    exit 1
fi
echo "ok: no tracked file names an internal cluster"
