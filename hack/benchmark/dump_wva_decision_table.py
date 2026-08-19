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

A separate, pre-existing tool (hack/benchmark/dump_k2_decisions.py, Ofer/
Evgeny) reads a materially different signal: the saturation_v2 analyzer's own
per-replica k1/k2 capacity-tier trail (which of 4 priority tiers produced k2,
which bound -- memory or compute -- won, whether a replica's capacity is
stale/estimated rather than live). Not redundant with analyzer-result/
scaling-decision: those are the model-level AGGREGATE result; the k1/k2 lines
are the per-replica reasoning that produces it. Preferred data source here is
now bundle.derived.k2_decision_table (extract_real_trace.py's
build_k2_decision_table), which cycle-clusters BOTH signals into one row per
(cycle, variant) -- a real superset of what this script originally did alone.
Falls back to the older analyzer-result/scaling-decision-only nearest-
timestamp join for a bundle.json produced before that field existed; the k1/k2
columns simply read "-" on that path, since that signal was never captured.

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


def _live_metrics_join(rows: list[dict], bundle: dict) -> None:
    """Cross-references each row against derived.wva_controller_metrics (the
    controller's own /metrics, scrape_wva_metrics.sh) for that variant at the
    nearest timestamp -- in place, additive, works on rows from either source
    below."""
    wva_metrics = (bundle.get('derived') or {}).get('wva_controller_metrics') or {}
    metrics_by_variant = {v: sorted(samples, key=lambda r: r['t'])
                          for v, samples in wva_metrics.items()}
    for r in rows:
        live = nearest(metrics_by_variant.get(r.get('variant'), []), r['t'], JOIN_TOLERANCE_S)
        r['kv_tokens_used'] = live.get('kv_tokens_used') if live else None
        r['kv_tokens_capacity'] = live.get('kv_tokens_capacity') if live else None
        r['spare_capacity_live'] = live.get('spare_capacity') if live else None


def build_table_from_k2_decision_table(bundle: dict) -> list[dict]:
    """Preferred path: bundle.derived.k2_decision_table already cycle-clustered
    the saturation_v2 k1/k2 trail with analyzer-result/scaling-decision (see
    extract_real_trace.py's build_k2_decision_table). Nothing left to join
    here -- just add the live-metrics cross-reference and copy through."""
    rows = [dict(r) for r in (bundle.get('derived') or {}).get('k2_decision_table') or []]
    _live_metrics_join(rows, bundle)
    return rows


def build_table_legacy(bundle: dict) -> list[dict]:
    """Fallback for a bundle.json produced before k2_decision_table existed:
    the original nearest-timestamp join of analyzer-result with
    scaling-decision only -- no k1/k2 signal, because that bundle never
    captured it. Field names match build_table_from_k2_decision_table's so
    render_text/COLUMNS work on either source unmodified.
    """
    scaling_log = (bundle.get('derived') or {}).get('scaling_log') or {}
    by_analyzer = scaling_log.get('by_analyzer') or {}
    decisions = scaling_log.get('decisions') or []

    decisions_by_variant: dict = {}
    for d in decisions:
        decisions_by_variant.setdefault(d.get('variant'), []).append(d)
    for lst in decisions_by_variant.values():
        lst.sort(key=lambda r: r['t'])

    rows_out = []
    for analyzer_name, lane in by_analyzer.items():
        for rec in lane:
            variant = rec.get('variant')
            decision = nearest(decisions_by_variant.get(variant, []), rec['t'],
                               JOIN_TOLERANCE_S)
            rows_out.append({
                't': rec['t'],
                'variant': variant,
                'n_replicas': None, 'k2_priority': None,
                'k1_memory_bound': None, 'k2_compute_bound': None, 'bound_by': None,
                'tokens_in_use': None, 'local_queue_demand': None,
                'analyzer_name': analyzer_name,
                'analyzer_role': rec.get('role'),
                'analyzer_reason': rec.get('reason'),
                'analyzer_rc': rec.get('rc'),
                'analyzer_sc': rec.get('sc'),
                'analyzer_prc': rec.get('prc'),
                'analyzer_supply': rec.get('supply'),
                'analyzer_demand': rec.get('demand'),
                'analyzer_util': rec.get('util'),
                'scale_up_threshold': rec.get('scaleUpThreshold'),
                'scale_down_boundary': rec.get('scaleDownBoundary'),
                'decision_curr': decision.get('curr') if decision else None,
                'decision_tgt': decision.get('tgt') if decision else None,
                'decision_action': decision.get('action') if decision else None,
                'decision_at_max': decision.get('at_max') if decision else None,
                'applied_target': None,
                'decision_dt_s': (round(decision['t'] - rec['t'], 1)
                                  if decision else None),
            })
    rows_out.sort(key=lambda r: (r['t'], r['variant'] or ''))
    _live_metrics_join(rows_out, bundle)
    return rows_out


def build_table(bundle: dict) -> list[dict]:
    if (bundle.get('derived') or {}).get('k2_decision_table'):
        return build_table_from_k2_decision_table(bundle)
    return build_table_legacy(bundle)


COLUMNS = (
    ('t', 9, '.0f'), ('variant', 22, 's'), ('n_replicas', 3, 'd'),
    ('bound_by', 3, 's'), ('k2_priority', 11, 's'),
    ('analyzer_rc', 7, '.2f'), ('analyzer_sc', 7, '.2f'), ('analyzer_prc', 7, '.2f'),
    ('analyzer_util', 6, '.2f'),
    ('decision_curr', 4, 'd'), ('decision_tgt', 4, 'd'), ('decision_action', 10, 's'),
    ('applied_target', 5, 'd'), ('analyzer_reason', 24, 's'),
)


def render_text(rows: list[dict]) -> str:
    if not rows:
        return ('no analyzer-result/scaling-decision/k1-k2 lines found in this '
                'bundle\n')
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
                 f"(t0 epoch={t0:.0f}). bound_by: k1=memory-bound won, "
                 f"k2=compute-bound won. decision_curr/tgt/action are the "
                 f"optimizer's PRE-enforcement decision; applied_target is "
                 f"what actually landed after scale-to-zero/min-replica "
                 f"enforcement (from \"Applied saturation decision via shared "
                 f"cache\") -- the two can legitimately differ. A row with "
                 f"n_replicas/bound_by/k2_priority all '-' has an "
                 f"analyzer-result/scaling-decision match but no saturation_v2 "
                 f"k1/k2 line for this cycle (older bundle, or this cycle's "
                 f"code path never emitted one -- not evidence it doesn't "
                 f"matter, just that this cycle didn't hit it).")
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
