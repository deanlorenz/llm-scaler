#!/usr/bin/env bash
# Stage an extracted run as a publishable result, and optionally commit it to the
# `viz-results` orphan branch.
#
#   ./publish_result.sh -r real-trace/<label>              # stage + validate only
#   ./publish_result.sh -r real-trace/<label> --commit      # also commit locally
#
# WHY A BUNDLE IS THE UNIT OF SHARING
#   bundle.json is ~1-5 MB and is the *complete* input to every panel, versus GBs of
#   perishable cluster-bound source. Whoever ran the benchmark extracts once; everyone
#   else gets a file.
#
# TWO THINGS THIS SCRIPT WILL NOT DO
#   1. It never runs `git push`. Publishing to a remote is a separate human action.
#   2. It never checks out, resets, or otherwise touches a working tree or index --
#      not yours, not a sibling worktree's. `--commit` builds the commit with git
#      plumbing (hash-object / write-tree / commit-tree) against a throwaway index,
#      so HEAD and every worktree are left exactly as they were. This matters because
#      several worktrees share one bare repo here and may be mid-edit.
#
#   `--commit` DOES create/advance the local ref refs/heads/viz-results, which is
#   visible to every worktree sharing the repo. That is the one shared-state change
#   it makes, and why it is opt-in rather than the default.

set -euo pipefail

RUN=""; LABEL=""; DATE=""; CLUSTER=""; COMMIT=0; FORCE=0
BRANCH="viz-results"; MAX_MB=20
DEST_ROOT="results"

usage() { sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; cat <<'EOF'

Flags
  -r <dir>     extracted run directory (must hold bundle.json + coverage.json)  [required]
  -l <label>   result label (default: basename of -r)
  -d <date>    YYYYMMDD (default: bundle extraction date)
  -c <url>     cluster identifier to record in provenance.json
  -b <branch>  target orphan branch (default: viz-results)
  --commit     create the commit on <branch> via plumbing (never pushes)
  -F           allow overwriting an existing staged result dir (breaks append-only)
  -h           this help
EOF
exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    -r) RUN="$2"; shift 2 ;;
    -l) LABEL="$2"; shift 2 ;;
    -d) DATE="$2"; shift 2 ;;
    -c) CLUSTER="$2"; shift 2 ;;
    -b) BRANCH="$2"; shift 2 ;;
    --commit) COMMIT=1; shift ;;
    -F) FORCE=1; shift ;;
    -h|--help) usage 0 ;;
    *) echo "error: unknown argument '$1'" >&2; usage 2 ;;
  esac
done

[[ -n "$RUN" ]] || { echo "error: -r <run-dir> is required" >&2; usage 2; }
[[ -d "$RUN" ]] || { echo "error: $RUN is not a directory" >&2; exit 1; }

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE"

for f in bundle.json coverage.json; do
  [[ -f "$RUN/$f" ]] || {
    echo "error: $RUN/$f missing -- run extract_real_trace.py first" >&2; exit 1; }
done
[[ -f "$RUN/panels.png" ]] || \
  echo "warn: no panels.png -- the branch will not be browsable without re-rendering." >&2

# --- provenance + rule checks ------------------------------------------------ #
# Done in python because every check needs to read inside bundle.json, and because a
# silent jq/grep miss here is exactly the failure mode the rules exist to prevent.

EXTRACTOR_SHA="$(git log -1 --format=%H -- extract_real_trace.py 2>/dev/null || true)"
DIRTY=""
git diff --quiet -- extract_real_trace.py 2>/dev/null || DIRTY="+dirty"

PROV_JSON="$(RUN="$RUN" CLUSTER="$CLUSTER" DATE="$DATE" \
             EXTRACTOR_SHA="${EXTRACTOR_SHA}${DIRTY}" MAX_MB="$MAX_MB" python3 - <<'PY'
import hashlib, json, os, re, sys, time

run, max_mb = os.environ['RUN'], float(os.environ['MAX_MB'])
b = json.load(open(f'{run}/bundle.json'))
meta = b.get('meta') or {}
errs, warns = [], []

# Rule: bundles only, nothing oversized.
for f in ('bundle.json', 'coverage.json', 'panels.png'):
    p = f'{run}/{f}'
    if os.path.exists(p):
        mb = os.path.getsize(p) / 1e6
        if mb > max_mb:
            errs.append(f'{f} is {mb:.1f} MB, over the {max_mb:.0f} MB limit')

# Rule: no prompt or response text may reach a bundle. The extractor does not copy it,
# so this is a backstop against a future change quietly reintroducing it -- guidellm
# embeds full prompts in every record, which is bulk and possibly sensitive.
SUSPECT = re.compile(r'"(prompt|prompt_text|messages|content|response|completion|'
                     r'generated_text|choices)"\s*:')
with open(f'{run}/bundle.json') as fh:
    for i, chunk in enumerate(iter(lambda: fh.read(1 << 20), '')):
        m = SUSPECT.search(chunk)
        if m:
            errs.append(f'bundle.json contains a text-bearing key {m.group(1)!r} '
                        f'-- refusing to publish prompt/response content')
            break

# Rule: provenance is mandatory. An unknown extractor version makes a bundle
# unreusable -- the §8 parsing rules have already changed once in a way that would
# silently invalidate older bundles.
if not meta.get('extractor_version'):
    errs.append('bundle meta has no extractor_version')
sha = os.environ.get('EXTRACTOR_SHA') or ''
if not sha:
    warns.append('extractor git sha unknown (file not committed yet)')
elif sha.endswith('+dirty'):
    warns.append('extractor has uncommitted changes -- sha recorded as +dirty')

mtime = os.path.getmtime(f'{run}/bundle.json')
date = os.environ.get('DATE') or time.strftime('%Y%m%d', time.localtime(mtime))
cov = json.load(open(f'{run}/coverage.json'))

# Keep only the engine settings the capacity model depends on -- the raw
# cache_config_info block is ~25 mostly-irrelevant fields, and it is still in
# bundle.json for anyone who needs the rest.
eng = meta.get('engine') or {}
prov = {
    'run': meta.get('run'),
    'harness': meta.get('harness'),
    'harness_version': meta.get('harness_version'),
    'model': meta.get('model'),
    'engine': {k: eng.get(k) for k in
               ('num_gpu_blocks', 'block_size', 'gpu_memory_utilization',
                'enable_prefix_caching', 'cache_dtype', 'sliding_window')
               if eng.get(k) is not None},
    'namespace': meta.get('namespace'),
    'cluster': os.environ.get('CLUSTER') or None,
    'workload': meta.get('workload'),
    'shape': meta.get('shape'),
    'extractor_version': meta.get('extractor_version'),
    'extractor_git_sha': sha or None,
    'extracted_at': time.strftime('%Y-%m-%dT%H:%M:%S%z', time.localtime(mtime)),
    'published_at': time.strftime('%Y-%m-%dT%H:%M:%S%z'),
    'bundle_sha256': hashlib.sha256(open(f'{run}/bundle.json', 'rb').read()).hexdigest(),
    'bundle_bytes': os.path.getsize(f'{run}/bundle.json'),
    'coverage': {'pass': cov.get('n_pass'), 'fail': cov.get('n_fail'),
                 'not_exercised': [r['capability'] for r in cov.get('rows', [])
                                   if r['verdict'] == 'FAIL']},
    'time_anchor_trustworthy': (meta.get('time_anchor') or {}).get('trustworthy'),
    'source_dir': run,
}

for w in warns:
    print(f'warn: {w}', file=sys.stderr)
if errs:
    for e in errs:
        print(f'error: {e}', file=sys.stderr)
    sys.exit(1)

print(json.dumps({'_date': date, 'prov': prov}))
PY
)" || { echo "error: validation failed -- nothing staged." >&2; exit 1; }

DATE="$(printf '%s' "$PROV_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["_date"])')"
LABEL="${LABEL:-$(basename "$RUN")}"
DEST="$DEST_ROOT/$DATE-$LABEL"

# Rule: append-only. A re-extract lands as a new dated dir; it never rewrites one.
if [[ -e "$DEST" && "$FORCE" != 1 ]]; then
  echo "error: $DEST already exists (results are append-only)." >&2
  echo "       re-extract lands as a new date, or pass -F to overwrite deliberately." >&2
  exit 1
fi

mkdir -p "$DEST"
cp "$RUN/bundle.json" "$RUN/coverage.json" "$DEST/"
[[ -f "$RUN/panels.png" ]] && cp "$RUN/panels.png" "$DEST/"
printf '%s' "$PROV_JSON" | python3 -c \
  'import json,sys; json.dump(json.load(sys.stdin)["prov"], sys.stdout, indent=2); print()' \
  > "$DEST/provenance.json"

echo "# staged $DEST"
find "$DEST" -type f -printf '  %-20f %10s bytes\n' | sort

# --- commit (opt-in) --------------------------------------------------------- #

if [[ "$COMMIT" != 1 ]]; then
  cat <<EOF

# not committed. To commit locally onto '$BRANCH' (no push, no worktree touched):
    $0 -r $RUN -l $LABEL --commit
EOF
  exit 0
fi

git rev-parse --git-dir >/dev/null 2>&1 || {
  echo "error: not inside a git repository" >&2; exit 1; }

TMPIDX="$(mktemp -t viz-results-index.XXXXXX)"
trap 'rm -f "$TMPIDX"' EXIT
export GIT_INDEX_FILE="$TMPIDX"
rm -f "$TMPIDX"                      # git wants to create it itself

PARENT=""
if git rev-parse --verify -q "refs/heads/$BRANCH" >/dev/null; then
  PARENT="$(git rev-parse "refs/heads/$BRANCH")"
  git read-tree "refs/heads/$BRANCH"
  # Append-only also applies on the branch, not just on disk.
  # --full-tree is load-bearing: without it ls-tree scopes to the CWD, and since this
  # script runs from a subdirectory the check would silently match nothing and never
  # fire -- an append-only guard that always passes is worse than none.
  if git ls-tree -r --name-only --full-tree "refs/heads/$BRANCH" \
       | grep -q "^$DEST/" && [[ "$FORCE" != 1 ]]; then
    echo "error: $DEST already exists on $BRANCH (append-only). Use -F to override." >&2
    exit 1
  fi
else
  echo "# creating orphan branch $BRANCH (first result)"
fi

while IFS= read -r -d '' f; do
  blob="$(git hash-object -w "$f")"
  mode=100644
  git update-index --add --cacheinfo "$mode,$blob,${f#./}"
done < <(find "$DEST" -type f -print0)

TREE="$(git write-tree)"
MSG="results: $DATE-$LABEL

harness=$(printf '%s' "$PROV_JSON" | python3 -c 'import json,sys; p=json.load(sys.stdin)["prov"]; print(p.get("harness"))')
model=$(printf '%s' "$PROV_JSON" | python3 -c 'import json,sys; p=json.load(sys.stdin)["prov"]; print(p.get("model"))')
extractor=$(printf '%s' "$PROV_JSON" | python3 -c 'import json,sys; p=json.load(sys.stdin)["prov"]; print(p.get("extractor_version"))')

Bundle + coverage + provenance + rendered panels. Source data not included."

if [[ -n "$PARENT" ]]; then
  NEW="$(git commit-tree "$TREE" -p "$PARENT" -m "$MSG")"
else
  NEW="$(git commit-tree "$TREE" -m "$MSG")"
fi
git update-ref "refs/heads/$BRANCH" "$NEW" ${PARENT:+"$PARENT"}

cat <<EOF

# committed $NEW on refs/heads/$BRANCH
# HEAD, your index, and every other worktree are untouched (plumbing-only commit).
#
# inspect:   git show --stat $BRANCH
# NOT pushed. Pushing is a separate, explicit decision -- confirm the remote first.
EOF
