#!/usr/bin/env python3
"""dump_wva_decision_table.py — join analyzer recommendations with the scaling
decisions they produced, into one table per (cycle time, variant).

Why this exists
----------------
extract_real_trace.py's bundle carries `derived.scaling_log`: two separate
per-line series pulled straight from the controller's own log --
`by_analyzer[analyzer_name]` (one row per variant per analyzer-result line) and
`decisions` (one row per variant per scaling-decision line). Reading either in
isolation answers a different question than the one that actually matters for
a benchmark: analyzer-result says what an analyzer *recommended*; scaling-
decision says what the optimizer *did*. This script answers "what did the
controller decide, and why" by joining them on (variant, nearest timestamp) --
the two lines are logged within the same reconcile cycle, so they land within
a couple of seconds of each other, never at the identical instant.

What "no such script exists yet" means here
--------------------------------------------
llm-d-workload-variant-autoscaler's benchmark branch proposed a script with
this exact name (planning/benchmark-observability-plan.md), but that plan's
Parts 1-4 were superseded before it was ever written -- there was nothing to
port. This is a fresh implementation against the log schema that actually
shipped (PR #1318: analyzer-result + scaling-decision), plus this scaler's own
additional fields (supply/demand/util/thresholds/role/atMax) that PR predates.

What it does NOT do
--------------------
It reads a bundle.json that already exists (run `benchmark-extract-trace`
first). It does not touch the cluster, and it does not change what the
controller logs -- see this repo's scaler-instrumentation scope boundary.

Usage
-----
  python3 dump_wva_decision_table.py --run <run-dir>
  python3 dump_wva_decision_table.py --bundle <run-dir>/bundle.json

Writes <run-dir>/metrics/processed/wva_decision_table.{txt,json} and prints
the text table to stdout.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

JOIN_TOLERANCE_S = 5.0


def load_bundle(run_dir: str | None, bundle_path: str | None) -> tuple[dict, str]:
    if bundle_path:
        path = bundle_path
    elif run_dir:
        path = os.path.join(run_dir, 'bundle.json')
    else:
        raise SystemExit('one of --run or --bundle is required')
    with open(path) as fh:
        return json.load(fh), path


def nearest(rows: list[dict], t: float, tol: float) -> dict | None:
    """The row in `rows` (sorted by 't') closest to `t`, within `tol` seconds."""
    best, best_dt = None, None
    for r in rows:
        dt = abs(r['t'] - t)
        if dt > tol:
            continue
        if best_dt is None or dt < best_dt:
            best, best_dt = r, dt
    return best


def build_table(bundle: dict) -> list[dict]:
    scaling_log = (bundle.get('derived') or {}).get('scaling_log') or {}
    by_analyzer = scaling_log.get('by_analyzer') or {}
    decisions = scaling_log.get('decisions') or []
    wva_metrics = (bundle.get('derived') or {}).get('wva_controller_metrics') or {}

    # decisions grouped per variant, so each analyzer row only searches its own
    # variant's decisions rather than the whole run's.
    decisions_by_variant: dict = {}
    for d in decisions:
        decisions_by_variant.setdefault(d.get('variant'), []).append(d)
    for lst in decisions_by_variant.values():
        lst.sort(key=lambda r: r['t'])

    metrics_by_variant = {v: sorted(rows, key=lambda r: r['t'])
                          for v, rows in wva_metrics.items()}

    rows_out = []
    for analyzer_name, lane in by_analyzer.items():
        for rec in lane:
            variant = rec.get('variant')
            decision = nearest(decisions_by_variant.get(variant, []), rec['t'],
                               JOIN_TOLERANCE_S)
            live = nearest(metrics_by_variant.get(variant, []), rec['t'],
                          JOIN_TOLERANCE_S)
            rows_out.append({
                't': rec['t'],
                'analyzer': analyzer_name,
                'variant': variant,
                'role': rec.get('role'),
                'reason': rec.get('reason'),
                'rc': rec.get('rc'),
                'sc': rec.get('sc'),
                'prc': rec.get('prc'),
                'supply': rec.get('supply'),
                'demand': rec.get('demand'),
                'util': rec.get('util'),
                'scaleUpThreshold': rec.get('scaleUpThreshold'),
                'scaleDownBoundary': rec.get('scaleDownBoundary'),
                'curr': decision.get('curr') if decision else None,
                'tgt': decision.get('tgt') if decision else None,
                'action': decision.get('action') if decision else None,
                'at_max': decision.get('at_max') if decision else None,
                'decision_dt_s': (round(decision['t'] - rec['t'], 1)
                                  if decision else None),
                'kv_tokens_used': live.get('kv_tokens_used') if live else None,
                'kv_tokens_capacity': live.get('kv_tokens_capacity') if live else None,
                'spare_capacity_live': live.get('spare_capacity') if live else None,
            })
    rows_out.sort(key=lambda r: (r['t'], r['variant'] or ''))
    return rows_out


COLUMNS = (
    ('t', 10, '.0f'), ('analyzer', 11, 's'), ('variant', 24, 's'),
    ('role', 6, 's'), ('rc', 7, '.2f'), ('sc', 7, '.2f'), ('prc', 7, '.2f'),
    ('supply', 7, '.2f'), ('demand', 7, '.2f'), ('util', 6, '.2f'),
    ('curr', 5, 'd'), ('tgt', 5, 'd'), ('action', 10, 's'), ('at_max', 6, 's'),
    ('reason', 30, 's'),
)


def render_text(rows: list[dict]) -> str:
    if not rows:
        return 'no analyzer-result/scaling-decision lines found in this bundle\n'
    t0 = rows[0]['t']
    lines = []
    header = '  '.join(name.ljust(width) for name, width, _ in COLUMNS)
    lines.append(header)
    lines.append('-' * len(header))
    for r in rows:
        cells = []
        for name, width, fmt in COLUMNS:
            v = r.get(name)
            if name == 't':
                v = v - t0 if v is not None else None
            if v is None:
                s = '-'
            elif fmt == 's':
                s = str(v)
            elif fmt == 'd':
                try:
                    s = str(int(v))
                except (TypeError, ValueError):
                    s = str(v)
            else:
                try:
                    s = format(float(v), fmt)
                except (TypeError, ValueError):
                    s = str(v)
            cells.append(s[:width].ljust(width))
        lines.append('  '.join(cells))
    lines.append('')
    lines.append(f"{len(rows)} row(s). t is seconds since the first row "
                 f"(t0 epoch={t0:.0f}). decision_dt_s (not printed above; see "
                 f"the JSON) is how many seconds after the analyzer-result "
                 f"line the matched scaling-decision line landed.")
    return '\n'.join(lines) + '\n'


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--run', help='run directory (reads <run>/bundle.json)')
    ap.add_argument('--bundle', help='explicit path to bundle.json')
    ap.add_argument('--out-dir', help='defaults to <run>/metrics/processed, '
                    'or alongside --bundle if --run is not given')
    args = ap.parse_args()

    bundle, bundle_path = load_bundle(args.run, args.bundle)
    rows = build_table(bundle)
    text = render_text(rows)
    print(text, end='')

    out_dir = args.out_dir or (
        os.path.join(args.run, 'metrics', 'processed') if args.run
        else os.path.join(os.path.dirname(os.path.abspath(bundle_path)),
                          'metrics', 'processed'))
    os.makedirs(out_dir, exist_ok=True)
    txt_path = os.path.join(out_dir, 'wva_decision_table.txt')
    json_path = os.path.join(out_dir, 'wva_decision_table.json')
    with open(txt_path, 'w') as fh:
        fh.write(text)
    with open(json_path, 'w') as fh:
        json.dump(rows, fh, indent=2)
    print(f'\nwrote {txt_path}')
    print(f'wrote {json_path}')
    return 0 if rows else 1


if __name__ == '__main__':
    sys.exit(main())
