#!/usr/bin/env bash
# Stage an extracted run as a small, committable, browsable result -- the
# convention documented in autoscaling-viz's real-trace-viz-plan.md §15.
#
#   ./publish_viz_result.sh -r dean-.../results/inference-perf-..._1
#
# WHY A BUNDLE IS THE UNIT OF SHARING
#   bundle.json is a few hundred KB to a few MB and is the *complete* input to
#   every panel, versus GBs of perishable cluster-bound source
#   (per_request_lifecycle_metrics.json, metrics/raw/*, controller.log). Extract
#   once; everyone else gets a file.
#
# What this deliberately drops from the original
#   The upstream script also had a --commit mode writing to a `viz-results`
#   orphan branch via git plumbing. That branch was retired in favor of a plain
#   tracked results/ directory (real-trace-viz-plan.md §15's own superseded
#   note says so, and flags --commit as a live defect: it would recreate the
#   retired branch). This keeps only the staging + validation half, which is
#   the part still current.
set -euo pipefail

RUN=""; LABEL=""; DATE=""; CLUSTER=""; FORCE=0
MAX_MB=20
DEST_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/results"

usage() {
  cat <<'EOF'
Usage: publish_viz_result.sh -r <run-dir> [-l <label>] [-d <YYYYMMDD>] [-c <cluster>] [-F]

  -r <dir>     extracted run directory (must hold bundle.json + coverage.json)  [required]
  -l <label>   result label (default: basename of -r)
  -d <date>    YYYYMMDD (default: bundle extraction date)
  -c <id>      cluster identifier to record in provenance.json
  -F           allow overwriting an existing staged result dir (breaks append-only)
  -h           this help
EOF
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -r) RUN="$2"; shift 2 ;;
    -l) LABEL="$2"; shift 2 ;;
    -d) DATE="$2"; shift 2 ;;
    -c) CLUSTER="$2"; shift 2 ;;
    -F) FORCE=1; shift ;;
    -h|--help) usage 0 ;;
    *) echo "error: unknown argument '$1'" >&2; usage 2 ;;
  esac
done

[[ -n "$RUN" ]] || { echo "error: -r <run-dir> is required" >&2; usage 2; }
[[ -d "$RUN" ]] || { echo "error: $RUN is not a directory" >&2; exit 1; }

for f in bundle.json coverage.json; do
  [[ -f "$RUN/$f" ]] || {
    echo "error: $RUN/$f missing -- run hack/benchmark/extract_real_trace.py first" >&2
    exit 1
  }
done
[[ -f "$RUN/panels.png" ]] || \
  echo "warn: no panels.png -- stage will not be browsable without re-rendering." >&2

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EXTRACTOR_SHA="$(cd "$REPO_ROOT" && git log -1 --format=%H -- hack/benchmark/extract_real_trace.py 2>/dev/null || true)"
DIRTY=""
(cd "$REPO_ROOT" && git diff --quiet -- hack/benchmark/extract_real_trace.py 2>/dev/null) || DIRTY="+dirty"

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

# Rule: no prompt or response text may reach a published bundle. The extractor
# does not copy it, so this is a backstop against a future change quietly
# reintroducing it -- guidellm/inference-perf embed full prompts per request,
# which is bulk and possibly sensitive.
SUSPECT = re.compile(r'"(prompt|prompt_text|messages|content|response|completion|'
                     r'generated_text|choices)"\s*:')
with open(f'{run}/bundle.json') as fh:
    for chunk in iter(lambda: fh.read(1 << 20), ''):
        m = SUSPECT.search(chunk)
        if m:
            errs.append(f'bundle.json contains a text-bearing key {m.group(1)!r} '
                        f'-- refusing to publish prompt/response content')
            break

# Rule: provenance is mandatory. An unknown extractor version makes a bundle
# unreusable if the parsing rules change later.
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
[[ -f "$RUN/metrics/processed/wva_decision_table.json" ]] && \
  cp "$RUN/metrics/processed/wva_decision_table.json" "$RUN/metrics/processed/wva_decision_table.txt" "$DEST/" 2>/dev/null || true
printf '%s' "$PROV_JSON" | python3 -c \
  'import json,sys; json.dump(json.load(sys.stdin)["prov"], sys.stdout, indent=2); print()' \
  > "$DEST/provenance.json"

echo "# staged $DEST"
find "$DEST" -type f -printf '  %-28f %10s bytes\n' | sort
echo
echo "# not committed to git -- add + commit $DEST yourself when ready."
