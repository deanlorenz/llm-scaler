#!/usr/bin/env bash
#
# Fails when a tracked file names one of the internal clusters measurements
# were taken on.
#
# Comments, docs and tests say "the shared GPU cluster" or describe the
# hardware ("a multi-node H200 cluster") instead, as PR bodies already do. The
# names themselves are not written here: the check compares SHA-256 digests,
# so the list does not publish what it guards.
#
# What is compared: each line is lowercased and its non-alphanumerics dropped,
# and from every place a word starts (after a non-alphanumeric, or at a
# camelCase capital) a window as long as each listed name is hashed. So a name
# is found plain, in any case, hyphenated or split by - _ or spaces, joined to
# a word after it (NameCluster, namecluster) and with any suffix (name001).
# A name joined to a word BEFORE it (clustername) is not, nor is text in a
# file that is not UTF-8 (a UTF-16 file reads as binary and is skipped).
#
# To add a name, append "<length>:<first 16 hex digits of the digest>" to
# DIGESTS, from: printf %s <name> | wc -c; printf %s <name> | sha256sum
# Lowercase, alphanumerics only. A maintainer keeps the names themselves in an
# untracked local note; re-derive the list from it rather than from grep hits.
#
# Two files are exempt because a deployment depends on the name: the PR e2e
# workflow selects its runners by label, and verify-correct-cluster.sh refuses
# a run whose API server is the nightly-only cluster.
#
# The check first runs against fixtures holding a synthetic word, in each form
# above, whose digest is in the list, so a scan that matches nothing cannot
# pass silently.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PY="$(command -v python3 || command -v python)" || { echo "FAIL: python3 not found"; exit 1; }

DIGESTS="7:550139a3a4338ebe 7:556c7df75b76c6cc 6:a3d92ef00ea150eb 5:7700a7306a4966d9 5:523959063745fcda 18:0fd6540147c8a2a2"
EXEMPT=".github/workflows/ci-e2e-openshift.yaml deploy/ci-e2e-openshift/verify-correct-cluster.sh"

# scan <git-dir> prints "<path>:<line>" for every hit and exits 1 if any.
scan() {
    git -C "$1" ls-files -z | DIGESTS="$DIGESTS" EXEMPT="$EXEMPT" "$PY" -c '
import hashlib, os, re, sys
root = sys.argv[1]
pairs = [p.split(":") for p in os.environ["DIGESTS"].split()]
digests = {d for _, d in pairs}
lengths = sorted({int(n) for n, _ in pairs})
exempt = set(os.environ["EXEMPT"].split())
word = re.compile(r"[A-Za-z0-9]+")
camel = re.compile(r"(?<=[a-z])[A-Z]")
seen = {}
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
    for n, line in enumerate(data.decode("utf-8", "replace").splitlines(), 1):
        tokens = word.findall(line)
        if not tokens:
            continue
        norm = "".join(tokens).lower()
        starts, at = [], 0
        for tok in tokens:
            starts.append(at)
            starts.extend(at + m.start() for m in camel.finditer(tok))
            at += len(tok)
        found = False
        for s in starts:
            for size in lengths:
                window = norm[s:s + size]
                if len(window) < size:
                    break
                hit = seen.get(window)
                if hit is None:
                    hit = hashlib.sha256(window.encode()).hexdigest()[:16] in digests
                    seen[window] = hit
                if hit:
                    found = True
                    break
            if found:
                break
        if found:
            print(f"{rel}:{n}")
            hits += 1
sys.exit(1 if hits else 0)
' "$1"
}

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

# Self-test: the fixture word is built at runtime so this file never holds it,
# one line per form the scan claims to find.
git -C "$T" init -q
{
    printf 'measured on %s%s%s today\n' zz clusterfixture zz
    printf 'MEASURED ON %s%s%s\n' ZZ CLUSTERFIXTURE ZZ
    printf 'kind: %s-%s-%s\n' zz clusterfixture zz
    printf 'a %s %s %s run\n' zz clusterfixture zz
    printf 'measuredOn%s%s%sCluster\n' Zz clusterfixture zz
    printf 'host %s%s%s042.example\n' zz clusterfixture zz
    printf 'the %s%s%scluster\n' zz clusterfixture zz
    printf 'nothing to see here\n'
} > "$T/fixture.md"
git -C "$T" add fixture.md
want="$(printf 'fixture.md:%s\n' 1 2 3 4 5 6 7)"
if out="$(scan "$T")" || [ "$out" != "$want" ]; then
    echo "FAIL: the scan did not report exactly the fixture lines 1-7 (got: ${out:-nothing})"
    exit 1
fi

if ! out="$(scan "$ROOT")"; then
    echo "FAIL: tracked files name an internal cluster; say \"the shared GPU cluster\" or describe its hardware:"
    printf '  %s\n' $out
    exit 1
fi
echo "ok: no tracked file names an internal cluster"
