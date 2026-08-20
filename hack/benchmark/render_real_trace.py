#!/usr/bin/env python3
"""Render a real-run `bundle.json` into the autoscaling-viz panels.

    python3 render_real_trace.py --bundle real-trace/<label>/bundle.json

Colour vocabulary AND panel composition are taken from the synthetic PoC
(`plots.py`) so a real run and a simulated run can be read side by side without
relearning the figure. Where the synthetic figure shades a quantity, this one
shades the same quantity in the same colour.

What is deliberately different from the synthetic figure:

  * Rates use a trailing (Prometheus-style) window, not hard bins. Hard bins
    attribute a request's whole output to its completion instant, which turns a
    backlog drain into an impossible burst -- panel 1b showed a 0<->17000 tok/s
    sawtooth that was pure bin attribution. Panel 1a keeps bars for the quality
    composition, with the trailing total drawn over them; its window is pinned to
    the bar width (W_REQ = BIN) so the curve and the bars are the SAME estimator
    at the same resolution and the curve rides the bar tops. A wider window there
    reads as a contradiction: a 20 s trailing average over 10 s bars sits at the
    mean of each adjacent pair, which in this run's mid stage is ~12 req/s under
    a 24 req/s bar.
  * Work is measured in OUTPUT TOKENS only. That is the unit the measured
    saturation ceiling (`sat_band.gen_tok_s`) is expressed in; there is no
    calibrated prefill+decode ceiling to compare a combined figure against.
  * Panel 4 draws all three queue sources rather than picking one. Which queue is
    *the* queue is an open design question (see README); until it is settled,
    showing all three is the honest option -- they measure different things and
    the difference is itself the finding.
  * Panels degrade. A run with no per-request trace still renders 2, 3, 4c and 5;
    the missing panels say why they are empty instead of vanishing.
  * Decision markers come from `desired` changes in the replica timeseries, which
    is WVA's decision as actually observed, not a simulated one. Effect markers
    come from `ready` changes and from recorded drain events.

Ported unchanged from llm-d-workload-variant-autoscaler's autoscaling-viz
branch: this reads only bundle.json's schema, which extract_real_trace.py (the
half that changed for this repo's scaler) still produces compatibly.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import subprocess
import sys
from statistics import mean, median, pstdev

try:
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    from matplotlib.ticker import MaxNLocator, FuncFormatter, FixedLocator
    from matplotlib.colors import LinearSegmentedColormap, to_rgba
    import numpy as np
except ImportError:
    sys.exit('error: matplotlib is required to render.\n'
             '  uv run --with matplotlib render_real_trace.py --bundle ...\n'
             'The extractor itself needs nothing beyond the standard library.')

try:
    from plots import (C_ARR, C_DEP, C_DES, C_CEIL, C_ACT, C_CAP, C_Q, C_SYS,
                       C_WAIT, C_SERVED, C_UP, C_DOWN, C_EFF_UP, C_EFF_DN,
                       BAND_SHADES, GP_COLORS, SIZE_SHADES)
except ImportError:      # shareable standalone: fall back to the same hex values
    C_ARR, C_DEP, C_DES = '#2563eb', '#059669', '#dc2626'
    C_CEIL = C_ACT = C_CAP = C_SYS = '#7c3aed'
    C_Q, C_WAIT, C_SERVED = '#d97706', '#dc2626', '#16a34a'
    C_UP, C_DOWN = '#dc2626', '#2563eb'
    C_EFF_UP, C_EFF_DN = '#7c3aed', '#9ca3af'
    BAND_SHADES = ['#a7d8de', '#5fbcc7', '#2f9aa8', '#63c39a', '#9bd8b0']
    GP_COLORS = ['#15803d', '#65a30d', '#eab308', '#f59e0b', '#ea580c', '#b91c1c']
    SIZE_SHADES = ['#dbeafe', '#93c5fd', '#60a5fa']

INK = '#1f2937'                 # the sim's stack-top outline colour

# Panel 6: one fixed color PER ANALYZER (an identity, not a category) --
# deliberately not GP_COLORS/BAND_SHADES, which are picked fresh per distinct
# value seen (reason codes, pod index) and would reassign a different color
# to the same analyzer across two different runs. Two are enough for the
# analyzers seen in real controller.log data so far (saturation, throughput);
# extra names fall back to a rotation over the same small set.
ANALYZER_COLORS = ['#0891b2', '#c2410c', '#6d28d9', '#166534']

WAIT_EDGES = [2, 15, 30, 45, 60]        # absolute wait-before-service seconds
# Panel 1a bar width. Departures in this workload are not Poisson: output lengths
# are near-monodisperse (IQR 26 tok = 5% of the median), so a cohort admitted to
# the decode batch together finishes together and the freed slots admit the next
# cohort, which sustains a ~20 s wave. Measured on the mid stage: adjacent 10 s
# bins are uncorrelated (r=-0.03) while bins 20 s apart correlate +0.59. The
# resulting peak-to-trough spread is 64x at 5 s bins, 12x at 10 s, 2.7x at 20 s.
# 10 s keeps the wave legible without pretending it is noise.
BIN = 10.0
GRID = 2.0                               # resampling step for every smooth curve
W_REQ = BIN                              # trailing window for request rates
W_WORK = 30.0                            # trailing window for work rates
# Fallback only -- extract_real_trace.py's own SAT=0.85 always populates
# sat_band()['threshold'] in both its return branches, so this constant is
# never actually reached on any bundle this extractor produces. Kept as a
# local fallback (not an import of the extractor's own constant, since this
# file is designed to run standalone against just a bundle.json) rather than
# dropped, since a bundle from a different/future extractor version could
# still omit the key -- this is defense against that, not dead code by
# mistake.
SAT = 0.85


# --------------------------------------------------------------------------- #

def rel(t, t0):
    return (t - t0) if t is not None else None


def binned_rate(times, t0, t1, bin_s=BIN):
    """Event times -> (centres, per-second rate). Hard bins; panel 1a bars only."""
    if not times:
        return [], []
    n = max(1, int((t1 - t0) / bin_s) + 1)
    counts = [0] * n
    for t in times:
        i = int((t - t0) / bin_s)
        if 0 <= i < n:
            counts[i] += 1
    return [(i + 0.5) * bin_s for i in range(n)], [c / bin_s for c in counts]


def trailing(times, weights, grid, window, centred=False):
    """Prometheus-style rate: sum of weights in (t-window, t], divided by window.

    This is the estimator the synthetic figure uses, and the reason to prefer it
    over hard bins is not cosmetic: a request's entire output is booked at its
    completion instant, so a bin narrower than the service time reports bursts
    that never happened. A trailing window spreads the same total over the
    interval it was actually earned in.

    `centred=True` puts t in the MIDDLE of the window instead of at its end. Use it
    wherever the curve is drawn over bars of the same width: a trailing window is
    aligned to each bin's right edge, so it lags the bars by half a bin and reads
    as a horizontal offset that has no physical meaning. Centred, the curve passes
    through each bar top at that bar's centre.

    Either way the window SLIDES while bins are fixed, so the curve can rise above
    every bar: a burst straddling a bin edge is split between two bins, and no
    fixed bin ever sees it whole. That is the sub-bin structure the bars hide, not
    an inconsistency.
    """
    shift = window / 2.0 if centred else 0.0
    order = sorted(zip(times, weights))
    out, lo, hi, acc = [], 0, 0, 0.0
    for t in grid:
        end = t + shift                  # grid ascends, so lo/hi stay monotonic
        while hi < len(order) and order[hi][0] <= end:
            acc += order[hi][1]
            hi += 1
        while lo < hi and order[lo][0] <= end - window:
            acc -= order[lo][1]
            lo += 1
        out.append(acc / window)
    return out


def fill_one_tick(by_t, pgrid):
    """Panel 3's per-pod resolution (item AE/AF): a missing tick is NOT a real
    zero (`by_t.get(t) or 0.0` conflated the two -- fixed here explicitly).
    Forward-fills exactly one tick from the immediately preceding tick's real
    value and marks it stale; a second consecutive miss is NOT filled further
    (Dean's own calibration: "copying previous slot's numbers is fine.
    Copying over more is getting more and more suspect") -- returns
    `multi_tick_gap=True` if that case is ever hit, so callers can flag it
    rather than silently extend the fill.

    Only fills WITHIN this pod's own observed lifetime (its first to its
    last real sample in `by_t`) -- `pgrid` is the union of every pod's own
    timestamps, so it runs far past a pod's own last sample once other pods
    keep reporting after this one drains/is removed. Naively filling against
    the shared grid misread "this pod is simply gone now" as dozens of
    consecutive multi-tick gaps (found on a real run: a pod's last sample at
    t=1786573222 followed by 40+ more pgrid ticks from other pods still
    running, each one incorrectly flagged). Before the first sample and
    after the last, ticks are plain non-stale zeros, matching this
    function's pre-fix behavior for "pod not yet live" / "pod gone" -- this
    is not a gap to fill, the pod just isn't there.

    Returns (values, stale_flags, multi_tick_gap) -- values[i] is a real
    number for every tick with anything to show, stale_flags[i] is True only
    for a forward-filled tick.
    """
    if not by_t:
        return [0.0] * len(pgrid), [False] * len(pgrid), False
    first_t, last_t = min(by_t), max(by_t)
    values, stale, multi_tick_gap = [], [], False
    prev_real = None
    prev_was_fill = False
    for t in pgrid:
        if t < first_t or t > last_t:
            values.append(0.0)
            stale.append(False)
            prev_real = None
            prev_was_fill = False
            continue
        raw = by_t.get(t)
        if raw is not None:
            values.append(raw)
            stale.append(False)
            prev_real = raw
            prev_was_fill = False
        elif prev_real is not None and not prev_was_fill:
            values.append(prev_real)
            stale.append(True)
            prev_was_fill = True
        else:
            if prev_was_fill:
                multi_tick_gap = True
            values.append(0.0)
            stale.append(False)
            prev_real = None
            prev_was_fill = False
    return values, stale, multi_tick_gap


def hold(by_t, grid, default=0.0):
    """Step-hold a sampled gauge onto `grid` (last value wins; honest for gauges)."""
    ks = sorted(by_t)
    out, i, cur = [], 0, default
    for t in grid:
        while i < len(ks) and ks[i] <= t:
            v = by_t[ks[i]]
            if v is not None:
                cur = v
            i += 1
        out.append(cur)
    return out


def step_series(rows, key, t0):
    xs, ys = [], []
    for r in rows:
        v = r.get(key)
        if v is None:
            continue
        xs.append(rel(r['t'], t0))
        ys.append(v)
    return xs, ys


def pct(vals, q):
    """Nearest-rank percentile -- same convention as extract_real_trace.py's
    own helper of the same name, duplicated rather than imported since the
    two scripts are meant to be independently shareable (see either file's
    own module docstring)."""
    if not vals:
        return None
    v = sorted(vals)
    i = min(len(v) - 1, max(0, int(round(q * (len(v) - 1)))))
    return v[i]


def wait_band(r):
    """Which quality band a request falls in, by absolute wait before first token."""
    if r.get('outcome') == 'error':
        return len(WAIT_EDGES)
    w = r.get('ttft')
    if w is None:
        return 0
    for i, e in enumerate(WAIT_EDGES):
        if w < e:
            return i
    return len(WAIT_EDGES)


def empty(ax, msg):
    ax.text(0.5, 0.5, msg, transform=ax.transAxes, ha='center', va='center',
            fontsize=9, color='#6b7280', style='italic')
    ax.set_yticks([])


def mark_effects(axis, reps, t0, drains, label=False):
    """Vertical at each moment a scale decision TOOK EFFECT, same convention as
    the synthetic figure's `_mark_effects`: a `ready` increase is a boot finishing
    (purple dotted), a `ready` decrease or a recorded drain event is a drain
    completing (grey dash-dot). Drawn on every panel so the effect instant lines
    up with whatever the panel shows happening at it -- the decision lines alone
    cannot show that the capacity did not arrive for another 94 s."""
    seen_up = seen_dn = False
    events = []
    for p, q in zip(reps, reps[1:]):
        if q.get('ready') is None or p.get('ready') is None:
            continue
        if q['ready'] != p['ready']:
            events.append((rel(q['t'], t0), q['ready'] > p['ready']))
    for t in drains or []:
        # a recorded drain event that the replica series did not resolve into a
        # `ready` step (same-sample scale-down) still belongs on the figure
        if not any(abs(t - t0 - e[0]) < 1.0 for e in events):
            events.append((t - t0, False))
    for t, up in sorted(events):
        lbl = '_nolegend_'
        if label and up and not seen_up:
            lbl, seen_up = 'took effect (boot done)', True
        elif label and not up and not seen_dn:
            lbl, seen_dn = 'took effect (drain done)', True
        axis.axvline(t, color=(C_EFF_UP if up else C_EFF_DN), lw=1.0,
                     ls=((0, (1, 2)) if up else (0, (5, 2, 1, 2))),
                     alpha=0.85, zorder=3.2, label=lbl)


def terciles(values):
    """Lower/upper tercile boundaries of a sample, or None if degenerate."""
    v = sorted(x for x in values if x)
    if len(v) < 6 or v[0] == v[-1]:
        return None
    return v[len(v) // 3], v[2 * len(v) // 3]


# --------------------------------------------------------------------------- #

def git_sha():
    """Short SHA of the worktree this script itself lives in -- see the
    identical helper (and its rationale) in extract_real_trace.py. The two
    are kept separate on purpose: a bundle can be extracted on one commit and
    rendered on another later, and collapsing them into one stamp would hide
    exactly that staleness gap.
    """
    try:
        out = subprocess.run(
            ['git', 'rev-parse', '--short', 'HEAD'],
            cwd=os.path.dirname(os.path.abspath(__file__)),
            capture_output=True, text=True, timeout=5, check=True)
        return out.stdout.strip() or 'unknown'
    except (OSError, subprocess.SubprocessError):
        return 'unknown'


def render(bundle, path, title=None, coverage=None):
    meta = bundle['meta']
    reqs = bundle.get('requests') or []
    reps = bundle.get('replicas') or []
    system = bundle.get('system') or []
    pods = bundle.get('pods') or {}
    der = bundle.get('derived') or {}
    cap = der.get('capacity') or {}
    sat = der.get('sat_band') or {}
    lg = der.get('lags') or {}

    # A shared PNG has to carry its own caveats -- whoever opens it will not have the
    # extractor's stdout. `sampled` in particular changes how panels 1a/1b/5 must be
    # read: every rate is understated, not merely noisy. `estimated` (plan item AD)
    # means ttft/out_tok on every request are a stage-histogram estimate, not a
    # per-request measurement -- arrival/departure/outcome are still real.
    warns = list((coverage or {}).get('warnings') or [])
    sampled = any('SAMPLE' in w for w in warns)
    estimated = bool(meta.get('per_request_estimated'))

    # Common origin: earliest timestamp across every series we actually have.
    origins = [r['t'] for r in reps[:1]] + [s['t'] for s in system[:1]]
    origins += [min(r['t_arr'] for r in reqs)] if reqs else []
    for p in pods.values():
        if p.get('series'):
            origins.append(p['series'][0]['t'])
    if not origins:
        sys.exit('error: bundle has no time series at all')
    t0 = min(origins)
    ends = [r['t'] for r in reps[-1:]] + [s['t'] for s in system[-1:]]
    ends += [max(r['t_dep'] or r['t_arr'] for r in reqs)] if reqs else []
    for p in pods.values():
        if p.get('series'):
            ends.append(p['series'][-1]['t'])
    t1 = max(ends)
    span = t1 - t0
    # x=0 lands at warmup-end, not run-start, for *_warmup profiles (plan item X)
    # -- grid/span above are still computed from the TRUE origin (t0 unshifted),
    # so the full real-time range is covered; only the displayed zero-point moves.
    # Every rel(t, t0) call downstream reads this same shifted t0 local, so this
    # one reassignment cascades to all of them without touching each call site.
    warmup_offset_s = meta.get('warmup_offset_s') or 0.0
    grid = [i * GRID - warmup_offset_s for i in range(int(span / GRID) + 1)]
    t0 = t0 + warmup_offset_s

    # ready replicas on the smooth grid: every capacity ceiling below is per-POD,
    # so it must be multiplied by the replica count in force at that instant.
    ready_g = hold({rel(r['t'], t0): r.get('ready') for r in reps}, grid)

    fig, ax = plt.subplots(7, 1, figsize=(15, 19), sharex=True,
                           gridspec_kw={'height_ratios': [3, 3, 2, 3, 2.5, 2.5, 2.2]})
    anchor = meta.get('time_anchor') or {}
    weak = anchor.get('trustworthy') is False
    # workload/model/namespace can all be genuinely absent from run_metadata.yaml
    # for some harnesses -- the extractor already tries config/*.env and the
    # per-workload profile YAML as fallbacks, so degrade to the run id itself
    # (never present, always something) rather than a bare "?".
    run_id = meta.get('run') or '?'
    # --title is a workload-segment override, not a full-title replacement --
    # a caller who passes a workload name wants "workload · run_id · model ·
    # harness · ns", not just the workload name alone.  (panel-review-20260817
    # item 1: the batch pass passed --title <run_id> and got a title with no
    # model/harness/ns because title replaced the entire composite string.)
    workload = title if title is not None else meta.get('workload')
    head = (f"{workload + '  ·  ' if workload else ''}{run_id}  ·  "
            f"{meta.get('model') or '?'}  ·  {meta.get('harness')}  ·  "
            f"ns={meta.get('namespace') or '?'}")
    fig.suptitle(head, fontsize=12, y=0.997)

    # --- panel 1a: request rate, completions split by wait quality ---------- #
    a = ax[0]
    if reqs:
        arr_t = [rel(r['t_arr'], t0) for r in reqs]
        dep_t = [rel(r['t_dep'], t0) for r in reqs if r.get('t_dep') is not None]
        bands = {}
        for r in reqs:
            if r.get('t_dep') is not None:
                bands.setdefault(wait_band(r), []).append(r['t_dep'])
        bottom = None
        labels = [f'wait <{WAIT_EDGES[0]}s'] + \
                 [f'{WAIT_EDGES[i - 1]}-{WAIT_EDGES[i]}s'
                  for i in range(1, len(WAIT_EDGES))] + \
                 [f'>{WAIT_EDGES[-1]}s / failed']
        for i in sorted(bands):
            xs2, ys2 = binned_rate(bands[i], t0, t1)
            if not xs2:
                continue
            if bottom is None:
                bottom = [0.0] * len(ys2)
            a.bar(xs2, ys2, width=BIN * 0.95, bottom=bottom,
                  color=GP_COLORS[min(i, len(GP_COLORS) - 1)],
                  label=labels[min(i, len(labels) - 1)], zorder=1)
            bottom = [b + v for b, v in zip(bottom, ys2)]
        # Total departure rate as a curve THROUGH the bar tops: the bars carry the
        # composition, this carries the total. Same events (t_dep) and, because
        # W_REQ == BIN and the window is centred, the curve equals each bar exactly
        # at that bar's centre and weaves between them. Wait time sets a bar
        # segment's COLOUR, never its height or x. Same dark ink as the synthetic
        # figure's stack-top outline.
        #
        # The curve overshoots the bars roughly half the time, because a sliding
        # window sees bursts that a fixed partition splits across two bins. It is
        # unbiased, not inflated -- measured on this run, the peak sliding value
        # tops the peak bin by +1% / +9% / +16% on the three stages.
        a.plot(grid, trailing(dep_t, [1.0] * len(dep_t), grid, W_REQ, centred=True),
               color=INK, lw=2.2, alpha=0.85, zorder=2.6,
               label=f'departure rate, total ({W_REQ:.0f}s centred sliding)')
        a.plot(grid, trailing(arr_t, [1.0] * len(arr_t), grid, W_REQ, centred=True),
               color=C_ARR, lw=2.4, zorder=2.7,
               label=f'arrival rate ({W_REQ:.0f}s centred sliding)')
        n_tr = sum(1 for r in reqs if r.get('outcome') == 'truncated')
        # "Good" = wait_band 0-2, i.e. TTFT < WAIT_EDGES[2] (30s) and not an
        # error -- the same threshold wait_band() already buckets by for the
        # stacked bars, just re-aggregated into one number for the corner.
        n_good = sum(1 for r in reqs if wait_band(r) <= 2)
        pct_good = 100.0 * n_good / len(reqs) if reqs else 0.0
        a.set_title(f'requests: {len(reqs)} offered, {n_tr} cut off at run end, '
                    f'{pct_good:.0f}% good (<{WAIT_EDGES[2]}s)'
                    + ('   — SAMPLE ONLY, rates understated' if sampled else ''),
                    fontsize=8, loc='right',
                    color='#b45309' if sampled else '#6b7280')
        if estimated:
            # Appending to either title text overflowed the panel width once
            # combined with the left title (confirmed on a real render) --
            # placed as its own corner annotation instead, just inside the
            # plot area below both titles, so it never competes with them
            # for the same horizontal space.
            a.text(0.995, 0.94, 'ttft/tokens ESTIMATED, not measured',
                   transform=a.transAxes, fontsize=7.5, color='#b45309',
                   ha='right', va='top', style='italic')
    else:
        empty(a, 'no per-request trace in this bundle — '
                 'fetch results.json / per_request_lifecycle_metrics.json')
    a.set_ylabel('requests / s')
    # The curves are the same events as the bars at the same resolution, so say so
    # in the title: a reader who reads them as different quantities will try to
    # reconcile them and fail. Why a sliding window can still top a bar is in
    # `trailing`'s docstring; "sliding" in the legend is the hint.
    # Shortened left-side title: the (bars: … curves: …) parenthetical trimmed
    # to avoid collision with the right-side corner text on long runs.
    # (panel-review-20260817 item 2)
    a.set_title(f'1a · request throughput + goodput quality  '
                f'(bars: {BIN:.0f}s bins · curves: {BIN:.0f}s centred sliding)',
                loc='left', fontsize=10)

    # --- panel 1b: work throughput vs capacity ------------------------------ #
    # Work = OUTPUT TOKENS. Offered work is booked at arrival (the tokens that
    # request will demand), completed work at departure (the tokens delivered),
    # so the gap between the two curves is backlog in token units. The ceiling is
    # the measured saturated generation rate, which is PER POD -- hence
    # ready(t) x gen_tok_s, a step that rises when a boot completes.
    b = ax[1]
    drew_b = False
    offered_w = None
    total_w = None
    if reqs:
        arr_t = [rel(r['t_arr'], t0) for r in reqs]
        arr_w = [float(r.get('out_tok') or 0) for r in reqs]
        offered_w = trailing(arr_t, arr_w, grid, W_WORK)
        b.plot(grid, offered_w, color=C_ARR, lw=2.4,
               zorder=2.7, label=f'offered work ({W_WORK:.0f}s trailing)')
        done = [r for r in reqs if r.get('t_dep') is not None]
        tc = terciles([r.get('out_tok') for r in done])
        if tc:
            lo_e, hi_e = tc
            buckets = [[], [], []]
            for r in done:
                ot = r.get('out_tok') or 0
                k = 0 if ot <= lo_e else (1 if ot <= hi_e else 2)
                buckets[k].append(r)
            stacks = [trailing([rel(r['t_dep'], t0) for r in bk],
                               [float(r.get('out_tok') or 0) for r in bk],
                               grid, W_WORK) for bk in buckets]
            lbls = [f'small (≤{lo_e:.0f} tok)', f'medium ({lo_e:.0f}–{hi_e:.0f})',
                    f'large (>{hi_e:.0f})']
            b.stackplot(grid, *stacks, colors=SIZE_SHADES, labels=lbls,
                        alpha=0.6, edgecolor='none')
            total_w = [sum(v) for v in zip(*stacks)]
        else:
            total_w = trailing([rel(r['t_dep'], t0) for r in done],
                               [float(r.get('out_tok') or 0) for r in done],
                               grid, W_WORK)
            b.stackplot(grid, total_w, colors=[SIZE_SHADES[1]], alpha=0.6,
                        edgecolor='none', labels=['completed work'])
        b.plot(grid, total_w, color=INK, lw=2.2, alpha=0.85, zorder=2.6,
               label='completed work, total')
        drew_b = True
    # Two measured per-pod token rates, and they are NOT interchangeable. The knee
    # rate is the most a pod ever sustained. The sat-band rate is what it delivers
    # once kv >= threshold, where preemption starts destroying already-generated
    # work -- lower, by the cost of running past the watermark. Using the sat-band
    # rate as THE ceiling put delivered work 29% above its own ceiling on the
    # 2026-08-07 staircase (peak 10201 tok/s against 2x3941), which is why the
    # knee is the ceiling here and the sat-band rate is drawn as a second, dotted
    # reference: the gap between them is what the 0.85 watermark buys.
    knee = der.get('tput_knee') or {}
    knee_rate = knee.get('gen_tok_s') if knee.get('confident') else None
    sat_rate = sat.get('gen_tok_s')
    ceil_rate = knee_rate or sat_rate
    if ceil_rate and reps:
        ceil = [v * ceil_rate for v in ready_g]
        src = (f"throughput knee, n={knee.get('n')}" if knee_rate
               else f"saturated at kv≥{sat.get('threshold')}")
        b.plot(grid, ceil, color=C_CAP, ls='--', lw=1.6, zorder=2.5,
               label=f'capacity ceiling (ready × {ceil_rate:.0f} tok/s per pod; {src})')
        if knee_rate and sat_rate and abs(sat_rate - knee_rate) > 0.02 * knee_rate:
            b.plot(grid, [v * sat_rate for v in ready_g], color=C_CAP, ls=':',
                   lw=1.3, alpha=0.8, zorder=2.4,
                   label=f'delivered rate once kv≥{sat.get("threshold")} '
                         f'({sat_rate:.0f} tok/s per pod — preemption cost)')
        if drew_b:
            # capacity paid for but not used, only where the ceiling is above what
            # was delivered. Same purple, same alpha as the synthetic figure.
            b.fill_between(grid, total_w, ceil,
                           where=[c > d for c, d in zip(ceil, total_w)],
                           interpolate=True, color=C_CAP, alpha=0.15,
                           label='unused capacity')
        drew_b = True
        # The ceiling can run to several times the work stack's peak on runs
        # with many ready replicas -- left uncapped, that compresses the
        # offered/delivered curves this panel exists to show into a sliver.
        # Cap the axis to the work stack and annotate the ceiling's true
        # value (and the replica count driving it) at each step that runs
        # off-chart, so the reader can still read it off without cross-
        # referencing panel 2. Panel 5 remains the panel for the full
        # unused-capacity picture -- this cap is about legibility here only.
        # work_peak anchors the cap to the offered/delivered curves. When
        # there's no per-request trace at all (offered_w/total_w both None --
        # common: several campaign cells lack it), there's no work stack to
        # protect, but the ceiling line ITSELF still needs a bound or it's
        # back to auto-scaling on its own highest replica-count step, which
        # is the same "one big number squashes everything else" problem this
        # fix exists for -- just with the ceiling's low steps as the victim
        # instead of the work curves. Fall back to the ceiling's own median
        # step in that case, found reproducing exactly this on a real
        # no-per-request run (dean-20260810-092644-320): axis silently stayed
        # auto-scaled to the ceiling's ~50000 peak, the pre-fix look this
        # task was supposed to remove.
        work_peak = max((offered_w or [0]) + (total_w or [0]), default=0)
        if work_peak == 0 and ceil:
            work_peak = median(ceil)
        if work_peak > 0:
            y_max = 1.5 * work_peak
            b.set_ylim(0, y_max)
            # A boot ramp can step through several replica counts within
            # seconds of each other (observed: 8 steps in under 5 minutes on
            # one run) -- labelling every one crams unreadable text on top of
            # itself. Label only the LAST step of a run of off-chart values
            # before the ceiling either drops back on-chart or the ramp ends,
            # i.e. one label per plateau, not one per transient step.
            # A single boot ramp can step through several replica counts
            # within seconds (observed: 8 in under 5 min on one run) -- the
            # min_gap dedup below handles that. But a run can ALSO have
            # several separate off-chart plateaus scattered further apart in
            # time that still land close together on screen once compressed
            # into the figure's pixel width (observed on a 2000s+ run with
            # 5-6 distinct excursions) -- min_gap alone doesn't fix that, so
            # labels also alternate top/bottom-of-band vertically, same idea
            # as staggering overlapping tick labels.
            min_gap = max(1.0, span * 0.05)  # seconds; keeps labels apart
            off = [c > y_max for c in ceil]
            last_label_x = -min_gap
            n_labelled = 0
            for i, (x, c, rd) in enumerate(zip(grid, ceil, ready_g)):
                if not off[i]:
                    continue
                is_last_of_run = (i + 1 >= len(off)) or not off[i + 1] or ready_g[i + 1] != rd
                if not is_last_of_run or x - last_label_x < min_gap:
                    continue
                last_label_x = x
                y_frac = 0.97 if n_labelled % 2 == 0 else 0.88
                n_labelled += 1
                b.annotate(f'×{rd:.0f} ({c/1000:.1f}k)',
                           xy=(x, y_max), xytext=(x, y_max * y_frac),
                           fontsize=6.5, color=C_CAP, ha='left', va='top',
                           arrowprops=dict(arrowstyle='-|>', color=C_CAP,
                                            lw=0.8, shrinkA=0, shrinkB=0))
    if not drew_b:
        empty(b, 'no throughput view available')
    else:
        # Time per work unit -- the inverse framing of the tok/s curves this
        # panel already plots, not a duplicate: seconds-per-1000-tokens reads
        # more naturally next to "how long until X tokens are done" than
        # tok/s does. Mean delivered rate over the run's own wall-clock span,
        # from the same `done` requests already summed into total_w above.
        done_reqs = [r for r in reqs if r.get('t_dep') is not None]
        tok_sum = sum(float(r.get('out_tok') or 0) for r in done_reqs)
        if done_reqs and tok_sum > 0:
            span_s = max(r['t_dep'] for r in done_reqs) - min(r['t_arr'] for r in reqs)
            if span_s > 0:
                s_per_1000 = 1000.0 * span_s / tok_sum
                # std across the same delivered-work series (total_w, the
                # trailing tok/s curve plotted just above) converted to the
                # same s-per-1000-tokens units, so the corner text shows how
                # much the rate actually varies, not just its overall mean.
                per_1000_series = [1000.0 / v for v in total_w if v and v > 0]
                std_per_1000 = (pstdev(per_1000_series)
                                if len(per_1000_series) > 1 else 0.0)
                b.set_title(f'{s_per_1000:.2f}±{std_per_1000:.2f}s per 1000 tokens '
                            f'(mean±std, delivered)'
                            + ('   — tokens ESTIMATED, not measured' if estimated else ''),
                            fontsize=8, loc='right',
                            color='#b45309' if estimated else '#6b7280')
    b.set_ylabel('output tokens / s')
    b.set_title(f'1b · work throughput: output tokens offered vs delivered vs '
                f'capacity  ({W_WORK:.0f}s trailing, Prom-style)',
                loc='left', fontsize=10)

    # --- panel 2: replicas desired vs ready --------------------------------- #
    c = ax[2]
    if reps:
        xs = [rel(r['t'], t0) for r in reps]
        dz = [r.get('desired') for r in reps]
        rz = [r.get('ready') for r in reps]
        # tiny opposite y-offsets and equal weights, exactly as the synthetic
        # figure: once ready catches up the two coincide, and neither may hide.
        c.step(xs, [v + 0.05 if v is not None else None for v in dz], where='post',
               color=C_DES, lw=2.2, alpha=0.9, label='desired (WVA)')
        c.step(xs, [v - 0.05 if v is not None else None for v in rz], where='post',
               color=C_ACT, lw=2.2, alpha=0.9, label='ready (alive)')
        # A replica that is alive but no longer wanted is draining: still finishing
        # in-flight work, not accepting new work, so NOT usable capacity. Kept even
        # though this run has none -- if a future run drains, the band appears
        # without a code change, and its absence here is itself the finding.
        if all(v is not None for v in dz + rz):
            accepting = [min(d, r) for d, r in zip(dz, rz)]
            if any(r > acc for r, acc in zip(rz, accepting)):
                c.fill_between(xs, accepting, rz, step='post', facecolor='none',
                               hatch='////', edgecolor=C_ACT, linewidth=0.0,
                               alpha=0.6, label='draining (not usable capacity)')
        note = (f"boot {lg['boot_s_mean']:.0f}s mean/{len(lg.get('boot_s') or [])}"
                if lg.get('boot_s_mean') else 'boot: n/a')
        if not lg.get('scaledown_observed'):
            note += ' · no scale-down'
        elif not any(r > min(d, r) for d, r in zip(dz, rz) if None not in (d, r)):
            note += ' · scale-down: no drain window'
        else:
            durs = [t1 - t0 for p in pods.values()
                    for t0, t1 in (p.get('drain_windows') or [])]
            note += (f' · drain {mean(durs):.0f}s mean/{len(durs)}' if durs
                      else ' · drain: n/a')
        c.set_title(note, fontsize=8, loc='right', color='#6b7280')
    else:
        empty(c, 'no replica_status_timeseries.json')
    c.set_ylabel('replicas')
    # MaxNLocator(integer=True) does not guarantee integer TICK VALUES, only
    # integer STEP sizes -- on a near-flat series (desired==ready==1 the whole
    # run, offset by the +-0.05 trick above to keep both lines visible) the
    # view spans only [0.95, 1.05], no integer step fits, and it falls back to
    # evenly-spaced fractional ticks (0.945, 0.96, ... 1.05). Anchoring the
    # bottom at 0 gives it enough real span to find integers reliably at any
    # replica count -- confirmed 0..1 and 0..10 both come back clean.
    c.set_ylim(bottom=0)
    c.yaxis.set_major_locator(MaxNLocator(integer=True))
    c.set_title('2 · autoscaling: desired vs ready replicas', loc='left', fontsize=10)

    # --- panel 3: requests per pod -- running, draining, waiting, EPP queue -- #
    # Stack order is deliberate: all pods' RUNNING at the bottom (the work that
    # is actually progressing AND usable capacity), then DRAINING (a pod that's
    # ready but no longer part of the desired count -- still finishing
    # in-flight work, split out of "running" so that band means only currently-
    # usable capacity), then each pod's WAITING in the same colour but hatched
    # (admitted to that engine, not yet running), then whatever the router is
    # holding that no engine has yet.
    #
    # The top band is NOT `q_dispatch`: EPP's `inference_objective_running_requests`
    # already counts everything at the pods, waiting and running, so stacking it
    # would double-count the bands below it. It is the residual
    # max(0, in_system − Σrunning − Σdraining − Σwaiting), which makes the
    # stack total identically in_system -- so the overlay line rides on the
    # stack top, and any daylight between them is a decomposition error you
    # can see. (Draining is carved
    # OUT of running, not added on top, so this invariant is unchanged by it.)
    d = ax[3]
    if pods:
        pgrid = sorted({round(s['t']) for p in pods.values() for s in p['series']})
        xs = [t - t0 for t in pgrid]
        # One width for every bar (the run's average tick spacing) does not
        # match reality when the real scrape cadence varies tick to tick
        # (confirmed on a real run: local gaps ranging 16-19s, average 17.1s)
        # -- wherever the local gap is smaller than that average, neighbouring
        # bars overlap; wherever it's larger, they show a gap that isn't
        # real. Each bar's width is instead its own Voronoi cell on the
        # timeline -- half-way to its previous neighbour on the left,
        # half-way to its next neighbour on the right -- so consecutive bars
        # abut exactly, with no overlap and no gap, matching the actual
        # scrape they each represent. Edge bars mirror their one real
        # neighbour gap since they have no neighbour on the other side.
        if len(xs) > 1:
            width = []
            for i in range(len(xs)):
                left = (xs[i - 1] + xs[i]) / 2 if i > 0 else xs[i] - (xs[i + 1] - xs[i]) / 2
                right = (xs[i] + xs[i + 1]) / 2 if i < len(xs) - 1 else xs[i] + (xs[i] - xs[i - 1]) / 2
                width.append(max(1.0, right - left))
        else:
            width = [max(1.0, span)] if xs else []
        bottom = [0.0] * len(pgrid)
        run_tot = [0.0] * len(pgrid)
        drain_tot = [0.0] * len(pgrid)
        wait_tot = [0.0] * len(pgrid)
        # Numeric labels (pod 1, pod 2, ...) instead of full pod-name suffixes --
        # with 15+ pods the per-pod legend overflows the panel otherwise. Number
        # by the same sorted order used for stacking, so it's deterministic
        # across a run; the number -> pod-name mapping goes in a corner
        # annotation so the information a full name carries isn't just dropped.
        #
        # Scale-up order (first-appearance time), not alphabetical -- per
        # Dean's direct feedback ("pod sort order should be scale order (as
        # in p4). older always on bottom"), same key panel 4 already uses
        # (below, e_ordered). Since the stack's `bottom` accumulator is built
        # in iteration order, the earliest-appearing pod's band lands at the
        # bottom of the stack for free once `ordered` itself is reordered --
        # no change to the stacking loops themselves. pod_num is derived from
        # `ordered` directly, so it renumbers to match scale-up order too --
        # a deliberate consequence (this panel and panel 4 now share one
        # ordering scheme instead of two), not a side effect to avoid.
        # Primary sort: first-appearance time (scale-up order, "older on bottom").
        # Tie-breaker: among pods with the same first-appearance tick, the one
        # that scales down LATER sorts first (lower index → bottom of stack), so
        # co-booted pods still have a deterministic, meaningful order.
        # Scale-down time = last sample timestamp.  Consistent with panel 4's
        # e_ordered below.  (panel-review-20260817 item 5)
        ordered = sorted(pods.items(),
                         key=lambda kv: (
                             min((s['t'] for s in kv[1]['series']),
                                 default=float('inf')),
                             -max((s['t'] for s in kv[1]['series']),
                                  default=float('-inf')),
                         ))
        pod_num = {pod: i + 1 for i, (pod, _p) in enumerate(ordered)}
        any_draining = False
        # Per-pod "pod N running"/"pod N waiting" legend entries are fine up to
        # a handful of pods, but a real per-pod label for EVERY pod is exactly
        # what overflowed even after Task 1's numeric-label fix -- that fix
        # addressed label WIDTH (full names -> numbers), not DENSITY (still
        # one row per pod). Above this many pods, collapse to one representative
        # "pods running"/"pods waiting" legend entry each; per-pod color is
        # still readable directly off the bars via the number/color key below
        # the panel, same key Task 1 already introduced for this purpose.
        many_pods = len(ordered) > 6
        live_count = [0] * len(pgrid)
        peak_run = {}  # pod -> peak running-count (item AC), non-saturated samples only
        for i, (pod, p) in enumerate(ordered):
            by_t = {round(s['t']): s.get('run') for s in p['series']}
            kv_by_t = {round(s['t']): s.get('kv') for s in p['series']}
            windows = p.get('drain_windows') or []
            drain_ts = {round(t) for t0_, t1_ in windows
                        for t in range(int(t0_), int(t1_) + 1)}
            # Item AE/AF: a missing tick is forward-filled from this pod's own
            # immediately preceding tick (marked stale), not silently treated
            # as a real zero -- `by_t.get(t) or 0.0` conflated the two. Caps
            # at one tick; a second consecutive miss falls back to the
            # pre-fix behavior rather than guessing at a second tier.
            # Checked against both sample runs: neither actually has a
            # pgrid-relative gap within any pod's own lifetime (the spec's
            # own "18/15 real gaps" figure measured gaps in each pod's own
            # successive-sample deltas directly, a different, stricter
            # definition than "missing from the union grid" -- those 22-24s
            # jitter gaps have no OTHER pod's sample landing inside them
            # either, so pgrid has no tick there to be missing from). This
            # fill only fires when some other pod's scrape round produces a
            # pgrid tick this pod's own timeline lacks -- confirmed the
            # mechanism itself is correct (traced by hand against the raw
            # bundle), just not exercised by either named sample run.
            filled_run, run_stale, multi_gap = fill_one_tick(by_t, pgrid)
            if multi_gap:
                print(f'warning: panel 3: pod {pod_num[pod]} has a real '
                      'multi-tick scrape gap -- item AF only defines '
                      'single-tick forward-fill, this gap fell back to the '
                      'un-filled default instead of extending the fill',
                      file=sys.stderr)
            run_ys, drain_ys, run_ys_stale, drain_ys_stale = [], [], [], []
            for ti, t in enumerate(pgrid):
                if t in by_t:
                    live_count[ti] += 1
                v = filled_run[ti]
                st = run_stale[ti]
                if t in drain_ts:
                    run_ys.append(0.0)
                    run_ys_stale.append(False)
                    drain_ys.append(v)
                    drain_ys_stale.append(st)
                else:
                    run_ys.append(v)
                    run_ys_stale.append(st)
                    drain_ys.append(0.0)
                    drain_ys_stale.append(False)
            # item AC correction (2026-08-16): peak running-count for the
            # legend strip must exclude any sample taken while this pod's own
            # kv was at/above k_sat -- an over-saturated pod's running count
            # doesn't mean the same thing as a healthy pod's, and including
            # it let saturated pods dominate the strip with a number that
            # isn't comparable across pods. k_sat isn't computed yet at this
            # point in render() (panel 4 computes it below) -- use the same
            # `sat.get('threshold') or SAT` fallback panel 4 uses, so both
            # panels anchor on the identical value.
            k_sat_ac = sat.get('threshold') or SAT
            non_sat_run = [filled_run[ti] for ti, t in enumerate(pgrid)
                           if (kv_by_t.get(t) or 0.0) < k_sat_ac]
            peak_run[pod] = max(non_sat_run, default=0.0)
            run_label = (f'pod {pod_num[pod]} running' if not many_pods
                         else ('pods running (see color key below)' if i == 0
                               else '_nolegend_'))
            # 0.4 (Task 2's original value) still read heavier than intended
            # once viewed at the figure's own 120 DPI, per Dean's direct
            # feedback -- 0.25 across all three bands (here, draining,
            # waiting) for a consistently thin, uniform outline treatment.
            d.bar(xs, run_ys, width=width, bottom=bottom,
                  color=BAND_SHADES[i % len(BAND_SHADES)],
                  edgecolor=INK, linewidth=0.25,
                  label=run_label, zorder=1)
            # Item AF stale overlay: a forward-filled tick still shows its
            # carried-forward height (drawn above, same bar) but gets a
            # second, thin marker on top so it reads as "last known value,
            # not a fresh scrape" -- 'xx' hatch, distinct from draining's
            # dots, waiting's diagonals, and panel 4's solid gold outlier
            # outline, so nothing reads as the same signal as an unrelated
            # existing overlay. Drawn as a masked second bar call (only the
            # stale x-positions get a nonzero height here) rather than
            # per-artist hatch toggling, since matplotlib bars don't support
            # a per-bar hatch override cleanly in one call.
            stale_run_ys = [y if st else 0.0 for y, st in zip(run_ys, run_ys_stale)]
            if any(stale_run_ys):
                d.bar(xs, stale_run_ys, bottom=bottom,
                      width=width, color='none', hatch='xx',
                      edgecolor='#6b7280', linewidth=0.4,
                      label='stale (carried forward one tick)' if i == 0
                            else '_nolegend_', zorder=1.5)
            bottom = [bt + y for bt, y in zip(bottom, run_ys)]
            run_tot = [a_ + y for a_, y in zip(run_tot, run_ys)]
            if any(drain_ys):
                # NOT a verified per-pod drain/wind-down signal -- corrected
                # 2026-08-15/16 (panel-review-20260815.md Item W) after
                # investigation disproved the original "finishing in-flight
                # work" framing this band shipped with. pod_drain_windows()
                # infers this band from two proxies, neither a real per-pod
                # live/drain signal: (1) the nearest FLEET-level `desired`
                # drop, and (2) "this pod's own metrics stopped appearing
                # soon after." Checked against real data (m-satta-dwell):
                # every one of 6 windows was filled with normal, healthy
                # scrapes -- several climbing or spiking -- right up to the
                # pod's last sample, with no sub-interval that looks like a
                # wind-down. The band's honest purpose is narrower than its
                # old label claimed: "which pod was taken down at roughly
                # this scale-down event," not a claim about drain/grace-
                # period behavior. Same per-pod colour so it's traceable to
                # which pod on close inspection, dotted hatch vs. waiting's
                # diagonal so the two are never confused. ONE legend entry
                # total, not per-pod -- draining is rare enough (few pods,
                # short windows) that per-pod labels here would repeat the
                # same legend-overflow problem the numeric pod labels were
                # just introduced to fix.
                #
                # Outline INK (not C_ACT) for consistency with running/waiting
                # bars, per Dean's direct feedback that the shipped version's
                # hatch/outline weight read as too heavy at a glance -- both
                # the fill's own edge and the hatch pattern's stroke are
                # thinned here, they are drawn with different mechanisms
                # (edgecolor/linewidth is the bar's own border; hatch_linewidth
                # is the pattern stroke) and both needed adjusting to actually
                # look lighter once rendered.
                bars = d.bar(xs, drain_ys, width=width, bottom=bottom,
                      color=BAND_SHADES[i % len(BAND_SHADES)],
                      hatch='....', edgecolor=INK, linewidth=0.25,
                      label=('_nolegend_' if any_draining else
                             'pod removed near a scale-down event (not necessarily draining -- see docs)'),
                      zorder=1)
                for bar in bars:
                    bar.set_hatch_linewidth(0.3)
                    # Explicit light hatch color, matching waiting's own
                    # near-white choice -- without this the hatch defaults to
                    # a dark tone that has too little contrast against the
                    # darker end of BAND_SHADES to actually read as dots once
                    # rendered (caught by viewing a real render, not visible
                    # in an isolated hatch-comparison test on a single color).
                    bar.set_hatchcolor('#f5f5f5')
                any_draining = True
                stale_drain_ys = [y if st else 0.0
                                  for y, st in zip(drain_ys, drain_ys_stale)]
                if any(stale_drain_ys):
                    d.bar(xs, stale_drain_ys, bottom=bottom,
                          width=width, color='none', hatch='xx',
                          edgecolor='#6b7280', linewidth=0.4,
                          label='_nolegend_', zorder=1.5)
                bottom = [bt + y for bt, y in zip(bottom, drain_ys)]
                drain_tot = [a_ + y for a_, y in zip(drain_tot, drain_ys)]
        any_waiting_labelled = False
        for i, (pod, p) in enumerate(ordered):
            by_t = {round(s['t']): s.get('wait') for s in p['series']}
            # Item AE/AF, same treatment as the running band above.
            ys, ys_stale, multi_gap = fill_one_tick(by_t, pgrid)
            if multi_gap:
                print(f'warning: panel 3: pod {pod_num[pod]} has a real '
                      'multi-tick scrape gap (waiting band) -- fell back to '
                      'the un-filled default, see the running-band warning '
                      'above for detail', file=sys.stderr)
            if not any(ys):
                continue
            # Full-saturation colour (no alpha reduction) with a near-white
            # hatch/edge, so the hatch reads as texture on a solid colour
            # instead of adding a second muddying layer on top of an already-
            # faded fill -- this is what breaks down first with many pods.
            # Diagonal hatch, repeated character -- per Dean's direct request
            # (autoscaling-viz-panel-review-20260815-fixes-plan.md Item R),
            # which retracts an earlier horizontal-line choice; diagonal is
            # what he actually asked for, matching this file's existing
            # repeated-character convention (draining's '....', line/edge
            # color/weight unchanged, already confirmed correct).
            wait_label = (f'pod {pod_num[pod]} waiting' if not many_pods
                          else ('pods waiting (see color key below)'
                                if not any_waiting_labelled else '_nolegend_'))
            any_waiting_labelled = True
            bars = d.bar(xs, ys, width=width, bottom=bottom,
                  color=BAND_SHADES[i % len(BAND_SHADES)],
                  hatch='////', edgecolor=INK, linewidth=0.25,
                  label=wait_label, zorder=1)
            for bar in bars:
                bar.set_hatch_linewidth(0.3)
                bar.set_hatchcolor('#f5f5f5')
            stale_wait_ys = [y if st else 0.0 for y, st in zip(ys, ys_stale)]
            if any(stale_wait_ys):
                d.bar(xs, stale_wait_ys, bottom=bottom,
                      width=width, color='none', hatch='xx',
                      edgecolor='#6b7280', linewidth=0.4,
                      label='_nolegend_', zorder=1.5)
            bottom = [bt + y for bt, y in zip(bottom, ys)]
            wait_tot = [a_ + y for a_, y in zip(wait_tot, ys)]
        # Running-count average across live pods -- a separately-noted gap
        # folded into this spec (panel4-kv-heatmap-plan.md § Panel 3), thin
        # and clearly secondary to the stacked bars, own secondary axis since
        # its natural scale (requests per live pod) differs from the stack's
        # own (total requests across all pods).
        avg_run = [(rt / lc) if lc else None
                   for rt, lc in zip(run_tot, live_count)]
        avg_run_xs = [x for x, v in zip(xs, avg_run) if v is not None]
        avg_run_ys = [v for v in avg_run if v is not None]
        if avg_run_ys:
            d3 = d.twinx()
            # Yellow, not red -- per Dean's direct feedback: red read as the
            # same signal family as "total in system" (same units, different
            # scale, easy to conflate). Yellow reads clearly against this
            # panel's own teal/green BAND_SHADES stack and is visually
            # distinct from panel 4's own gold/amber outlier outline (a
            # different panel, an outline not a filled line -- checked side
            # by side on a real render, no clash).
            d3.plot(avg_run_xs, avg_run_ys, color='#eab308', lw=1.3, ls='-',
                     alpha=0.9, zorder=2.7, label='mean running per live pod')
            # Zero-align with the primary (stacked-bar) axis, which always
            # starts at 0 -- without this, matplotlib auto-scales the
            # secondary axis independently and the two axes' baselines float
            # at different heights, reading as disconnected rather than
            # sharing a common reference point. Top-down (Dean: "maybe reverse
            # the secondary Y-axis") tried and kept -- draws the line from
            # y=0 at the panel's TOP downward, which reads more clearly
            # against the stacked bars (which grow upward from the shared
            # baseline) than the previous bottom-up orientation, confirmed on
            # a real render.
            d3.set_ylim(bottom=0)
            d3.invert_yaxis()
            d3.set_ylabel('mean running/pod', fontsize=8)
            d3.tick_params(axis='y', labelsize=7)
            d3.legend(loc='upper right', fontsize=6.5, framealpha=0.85)
        # router-side residual, on the pod grid (nearest system sample)
        sys_by_t = {round(s['t']): s.get('in_system') for s in system
                    if s.get('in_system') is not None}
        insys_p = hold(sys_by_t, pgrid) if sys_by_t else None
        if insys_p:
            # residual over ALL bands below it (running + draining + waiting),
            # not just running + waiting -- draining is carved out of running,
            # not added on top, so it must still be subtracted here or this
            # residual double-counts and the stack stops summing to in_system.
            epp = [max(0.0, n - r - dr - w)
                   for n, r, dr, w in zip(insys_p, run_tot, drain_tot, wait_tot)]
            if any(epp):
                d.bar(xs, epp, width=width, bottom=bottom, color=C_Q, alpha=0.75,
                      label='EPP queue (in system − Σrunning − Σdraining − Σwaiting)',
                      zorder=1)
        yn = []
        if sys_by_t:
            # thick overlay, NOT part of the stack
            xn, yn = step_series(system, 'in_system', t0)
            d.plot(xn, yn, color=C_WAIT, lw=2.4, alpha=0.9, zorder=2.8,
                   label='total requests in system (overlay)')
        if cap.get('max_conc_pred') and reps:
            xr, yr = step_series(reps, 'ready', t0)
            ceil_y = [v * cap['max_conc_pred'] for v in yr]
            # A ceiling many times the in-system total compresses the request
            # stack the same way panel 1b's uncapped ceiling did -- except
            # here the fix is a secondary axis (not a cap), since the ceiling
            # is a step line, not a fill the stack needs to stay clear of.
            insys_max = max((yn or [0]), default=0)
            ceil_max = max(ceil_y, default=0)
            close = insys_max == 0 or abs(ceil_max - insys_max) <= 0.10 * insys_max
            if close:
                d.step(xr, ceil_y, where='post', color=C_CEIL, ls='--', lw=1.6,
                       zorder=2.5,
                       label=f"KV ceiling (ready × {cap['max_conc_pred']:.0f}/pod)")
            else:
                d2 = d.twinx()
                d2.step(xr, ceil_y, where='post', color=C_CEIL, ls='--', lw=1.6,
                        zorder=2.5,
                        label=f"KV ceiling (ready × {cap['max_conc_pred']:.0f}"
                              "/pod) [right axis]")
                d2.set_ylim(0, max(ceil_max * 1.05, 1))
                d2.tick_params(axis='y', colors=C_CEIL, labelsize=7)
                d2.spines['right'].set_color(C_CEIL)
                d2.legend(loc='upper right', fontsize=6.5, framealpha=0.85)
        # TTFT/wait-time percentiles -- router imbalance moved to panel 4 to make
        # room (this panel's corner was already dense post-Task-2). Reuses
        # the same per-request `reqs` this panel's own bars are unrelated to
        # (they're pod-scrape-derived) -- degrades the same way panel 1a does
        # when there's no per-request trace, rather than a new empty convention.
        ttfts = [r['ttft'] for r in reqs if r.get('ttft') is not None]
        if ttfts:
            ps = [pct(ttfts, q) for q in (0.5, 0.75, 0.9, 0.95)]
            d.set_title('TTFT p50/p75/p90/p95 (ms)'
                        + (' [ESTIMATED]' if estimated else '') + ': '
                        + '/'.join(f'{p*1000:.0f}' for p in ps),
                        fontsize=8, loc='right',
                        color='#b45309' if estimated else '#6b7280')
        else:
            d.set_title('TTFT percentiles: n/a (no per-request trace)',
                        fontsize=8, loc='right', color='#6b7280')
        # pod-number -> pod-name key, since the legend now uses "pod N" labels
        # to fit the panel with many pods (fix for legend overflow). Short
        # suffix only (matches the old per-label convention) -- full pod names
        # made this key wider than the panel and collided with panel 4's title.
        key = '  '.join(f"{n}={pod.split('-')[-1]}"
                         for pod, n in sorted(pod_num.items(), key=lambda kv: kv[1]))
        # -0.08 instead of -0.14: tighter vertical gap between panel 3's bottom
        # and the pod-number key; -0.14 produced a visibly wide blank strip above
        # panel 4's title.  (panel-review-20260817 item 4)
        d.text(0.5, -0.08, key, transform=d.transAxes, ha='center', va='top',
               fontsize=6, color='#6b7280', wrap=True)
        # Per-pod color legend strip on the right edge (Dean: "can we have a
        # color legend on the right (like we have for p4)") -- one 100%-
        # height bar, pods in the same scale-up order the main stack now
        # uses (item AB), each segment's height proportional to that pod's
        # own peak running-count (the stack's own primary content) rather
        # than uniform, colored with the same BAND_SHADES entry the main
        # stack already uses for that pod. Discrete per-pod segments, not a
        # continuous scale, so this is a plain stacked bar on its own thin
        # axes rather than a real colorbar/ScalarMappable -- inset_axes ties
        # its position to panel 3's own bounding box, same idea as panel 4's
        # fraction/pad-positioned colorbar just narrower since there's no
        # tick scale to show.
        # x=1.02 initially collided with d3's secondary-axis ticks/label at
        # x∈[1.0,1.08]; 1.12 cleared that but sat too far right visually.
        # Fix: use fig.colorbar-style fraction/pad positioning (same mechanism
        # panel 4 uses) which naturally tucks the strip closer than a raw
        # inset_axes offset.  fraction=0.015 keeps the strip narrow; pad=0.01
        # gives just enough clearance from d3's right edge without reopening
        # the collision confirmed on 15+-pod runs.  (panel-review-20260817 item 3)
        total_peak = sum(peak_run.values()) or 1.0
        strip_sm = plt.cm.ScalarMappable(
            cmap=plt.matplotlib.colors.ListedColormap(
                [BAND_SHADES[i % len(BAND_SHADES)] for i, _ in enumerate(ordered)]))
        strip_sm.set_array([])
        # Build a dummy colorbar axis so we can draw our stacked-bar strip inside it.
        cb_ax = fig.colorbar(strip_sm, ax=d, fraction=0.015, pad=0.01).ax
        cb_ax.cla()  # clear the gradient fill, draw our own segments below
        cb_ax.set_xticks([]); cb_ax.set_yticks([])
        for spine in cb_ax.spines.values():
            spine.set_visible(False)
        strip = cb_ax
        strip_bottom = 0.0
        for i, (pod, _p) in enumerate(ordered):
            h = peak_run.get(pod, 0.0) / total_peak
            if h <= 0:
                continue
            strip.bar([0], [h], bottom=strip_bottom, width=1.0,
                      color=BAND_SHADES[i % len(BAND_SHADES)],
                      edgecolor=INK, linewidth=0.25)
            if h > 0.03:  # skip labels on slivers too thin to read
                strip.text(0, strip_bottom + h / 2, str(pod_num[pod]),
                          ha='center', va='center', fontsize=5, color=INK)
            strip_bottom += h
    else:
        empty(d, 'no metrics/raw/ scrapes — per-pod view unavailable')
    d.set_ylabel('requests')
    d.set_title('3 · requests per pod: running, draining, waiting, EPP queue  '
                '(stack ≡ in system)', loc='left', fontsize=10)

    # --- panel 4: per-pod KV% heatmap ---------------------------------------- #
    # Retires the old three-queue INTERIM content (panel-4-kv-heatmap-plan.md
    # § Why panel 4 is being retired): checked against real data first, not on
    # feel -- (b) duplicated panel 5's L(t) exactly, (c) duplicated panel 3's
    # own waiting stack pre-summed, and (a) (the one series unique to panel 4)
    # carries no real signal (absent entirely on m-satta-dwell, present but
    # negligible -- max 11 against a ~2000 axis -- on m-ta-prefill-knee).
    # Nothing here survived that check, so this is a full retirement, not a
    # deprecation-in-place.
    e = ax[4]
    if pods:
        e_pgrid = sorted({round(s['t']) for p in pods.values() for s in p['series']})
        e_xs = [t - t0 for t in e_pgrid]
        # Row order is by SCALE-UP SEQUENCE (first-appearance time) -- panel 3
        # now uses this same key (plan item AB), so both panels share one
        # ordering scheme; this was originally panel-4-local (when panel 3
        # was still alphabetical) but that constraint no longer applies once
        # there's only one scheme to be consistent with. The row label still
        # shows each pod's pod_num value (not a fresh "row N" label), so a
        # reader can cross-reference the two panels' legends directly.
        # Primary: first-appearance time.  Tie-breaker: later scale-down time
        # sorts first (same rule as panel 3's `ordered` above).
        # (panel-review-20260817 item 5)
        e_ordered = sorted(pods.items(),
                           key=lambda kv: (
                               min((s['t'] for s in kv[1]['series']),
                                   default=float('inf')),
                               -max((s['t'] for s in kv[1]['series']),
                                    default=float('-inf')),
                           ))
        # kv_matrix[row][col]: row 0 = the earliest-appearing pod (top of the
        # heatmap). None marks "no data at this t" -- not yet live, or
        # already gone -- distinct from a real 0.0 (live, genuinely empty KV
        # cache), per the spec's own "don't silently render these as white"
        # requirement.
        kv_matrix = []
        for pod, p in e_ordered:
            by_t = {round(s['t']): s.get('kv') for s in p['series']}
            kv_matrix.append([by_t.get(t) for t in e_pgrid])
        # Two-segment colormap anchored at the real scaling threshold (k_sat),
        # not a plain linear [0,1] -- this run's KV% is heavily skewed low, so
        # a linear scale wastes most of its range on values below where
        # anything scaling-relevant happens. White->green covers "below
        # threshold" (the common case), green->red covers "at or above" (the
        # rare, scaling-relevant case) -- so crossing k_sat visibly changes
        # color character, not just shade.
        k_sat = sat.get('threshold') or SAT
        kv_cmap = LinearSegmentedColormap.from_list(
            'kv_heat', [(0.0, '#ffffff'), (k_sat, '#16a34a'),
                        (1.0, '#dc2626')])
        dead_color = to_rgba('#d1d5db')  # distinct light gray -- never claimed by a real 0.0
        rgba = np.array(
            [[dead_color if v is None else kv_cmap(v) for v in row]
             for row in kv_matrix])
        n_pods_e = len(e_ordered)
        im = e.imshow(rgba, aspect='auto', origin='upper',
                      extent=(e_xs[0] if e_xs else 0,
                              e_xs[-1] if e_xs else 1, n_pods_e, 0),
                      interpolation='nearest', zorder=1)
        e.set_yticks([i + 0.5 for i in range(n_pods_e)])
        # Labels still show each pod's pod_num (panel 3's own numbering), not
        # this panel's row index -- the row POSITION is now scale-up order,
        # but the LABEL stays cross-referenceable against panel 3/the legend
        # key, per the spec's own instruction not to introduce a second,
        # competing numbering scheme.
        e.set_yticklabels([str(pod_num[pod]) for pod, _p in e_ordered],
                          fontsize=6)
        # Row separators -- "the horizontal lines should be at the bottom of
        # each bar" -- a thin line at each row boundary so all rows are
        # crisply distinguished, since adjacent cells otherwise only have
        # antialiasing between them, no deliberate border.
        for i in range(n_pods_e + 1):
            e.axhline(i, color=INK, lw=0.5, alpha=0.3, zorder=2)
        # Outlier marking -- provisional rule (Dean asked to "somehow mark
        # outliers" without specifying one): a cell more than one live-pod
        # population stdev above the live-pod mean at that same t. Expect
        # this needs tuning once seen rendered; flagged here, not a
        # considered-final threshold.
        avg_kv, avg_xs = [], []
        for ti, t in enumerate(e_pgrid):
            live_vals = [row[ti] for row in kv_matrix if row[ti] is not None]
            if not live_vals:
                continue
            m = mean(live_vals)
            avg_kv.append(m)
            avg_xs.append(e_xs[ti])
            if len(live_vals) < 2:
                continue
            sd = pstdev(live_vals)
            if sd <= 0:
                continue
            for ri, row in enumerate(kv_matrix):
                v = row[ti]
                if v is not None and v > m + sd:
                    # A solid INK hatch border read as "more black vertical
                    # lines" -- indistinguishable at a glance from the shared
                    # decision-event axvlines every panel gets for free,
                    # confirmed by Dean's own confusion on the first render.
                    # A bright accent color outside the white/green/red KV
                    # scale (so it never blends into a cell's own fill) with
                    # no hatch -- just a colored outline -- reads as its own
                    # distinct signal instead.
                    e.add_patch(plt.Rectangle(
                        (e_xs[ti] - 0.5, ri), 1.0, 1.0, fill=False,
                        edgecolor='#eab308', linewidth=1.0, zorder=2.5))
        # Average line on a secondary axis -- the heatmap rows have no
        # numeric y-axis in the traditional sense (each row is just "pod N"),
        # so the average needs its own scale to plot against. twinx(), same
        # pattern already used for panel 3's KV-ceiling secondary axis
        # (Task 2) -- but unlike that case, this secondary axis's own [0,1]
        # scale IS the same KV% scale the colorbar already shows, so its own
        # ticks/label would just repeat the colorbar redundantly and the two
        # collided visually in an early pass (caught by viewing the render,
        # not obvious from the code). Keep the axis (needed to plot the line
        # at the right vertical position) but hide its ticks/label/spine.
        if avg_kv:
            e2 = e.twinx()
            e2.plot(avg_xs, avg_kv, color=INK, lw=1.2, zorder=3,
                    label='mean KV% across live pods (see colorbar scale)')
            e2.set_ylim(0, 1)
            e2.set_yticks([])
            e2.spines['right'].set_visible(False)
            # upper-right collided with the colorbar (fixed previously); a
            # fixed upper-left/upper-right legend box also collides with the
            # heatmap's own high-KV (red/green) cells on typical runs, which
            # tend to concentrate in the early pods/early time region --
            # confirmed on both real sample renders this session. Placed
            # below the panel instead, in the same margin panel 3's own
            # pod-number key already uses, so it never competes with heatmap
            # content regardless of where the color mass happens to fall.
            e2.legend(loc='upper center', bbox_to_anchor=(0.5, -0.02),
                      fontsize=6.5, framealpha=0.85, ncol=1)
        cb = fig.colorbar(plt.cm.ScalarMappable(cmap=kv_cmap), ax=e,
                          fraction=0.02, pad=0.04)
        cb.set_label(f'KV%  (white→green below k_sat={k_sat:.2f}, '
                     'green→red at/above)', fontsize=7)
        cb.ax.tick_params(labelsize=6)
    else:
        empty(e, 'no metrics/raw/ scrapes — per-pod KV% unavailable')
    e.set_ylabel('pod')
    e.set_title('4 · per-pod KV% heatmap', loc='left', fontsize=10)
    # Router imbalance was co-located on the old panel 4 for space reasons
    # unrelated to its queue content -- not part of what this spec retires,
    # so kept here rather than silently dropped; the title area has room
    # even though the heatmap body itself is now dense.
    r = der.get('router') or {}
    p95 = r.get('disp_p95')
    e.text(0.995, 1.14 if pods else 1.02,
           f"router imbalance p95={'?' if p95 is None else round(p95, 2)}, "
           f"{r.get('leader_flips', '?')} leader flips / {r.get('n', '?')} "
           f"samples (not an oscillation test)",
           transform=e.transAxes, fontsize=8, color='#6b7280',
           ha='right', va='bottom')

    # --- panel 5: concurrency L(t) vs slot capacity ------------------------- #
    # Same composition as the synthetic figure: the gap between served and
    # in-system IS the queue (shaded red), and the gap between served and the
    # ceiling is capacity paid for and not used (shaded purple).
    f = ax[5]
    nsys_g = None
    sys_by_t = {rel(s['t'], t0): s.get('in_system') for s in system
                if s.get('in_system') is not None}
    if sys_by_t:
        nsys_g = hold(sys_by_t, grid)
    # For the L(t) line on panel 5 we also keep the direct (un-resampled)
    # sample-point times so it can be plotted at the actual scrape instants --
    # hold() onto `grid` places the step at the first grid tick >= the sample
    # time (up to GRID=2s late), while panel 3's bars are centered on the
    # scrape time itself; using the direct times closes that half-step visual
    # offset.  (panel-review-20260817 item 6)
    nsys_direct = [(rel(s['t'], t0), s['in_system'])
                   for s in system if s.get('in_system') is not None]
    served_by_t = {}
    for p in pods.values():
        for s in p['series'] or []:
            if s.get('run') is not None:
                k = round(s['t'])
                served_by_t[k] = served_by_t.get(k, 0.0) + s['run']
    served_g = hold({k - t0: v for k, v in served_by_t.items()},
                    grid) if served_by_t else None
    slots_g = ([v * cap['max_conc_pred'] for v in ready_g]
               if cap.get('max_conc_pred') and reps else None)

    if served_g and slots_g:
        f.fill_between(grid, served_g, slots_g,
                       where=[c > s for c, s in zip(slots_g, served_g)],
                       interpolate=True, color=C_CAP, alpha=0.15,
                       label='unused capacity')
    if served_g and nsys_g:
        f.fill_between(grid, served_g, nsys_g, color=C_WAIT, alpha=0.16,
                       label='queued (L − served)')
    if nsys_direct:
        # Plot at direct sample times (drawstyle='steps-post' holds each value
        # forward until the next sample) so the L(t) line aligns with panel 3's
        # bar centers, which are also at actual scrape times.
        # (panel-review-20260817 item 6)
        nsys_xs = [x for x, _ in nsys_direct]
        nsys_ys = [y for _, y in nsys_direct]
        f.plot(nsys_xs, nsys_ys, color=C_WAIT, lw=1.6, alpha=0.9,
               drawstyle='steps-post',
               label='in system  L(t)' + (' — SAMPLE' if sampled else ''))
    if served_g:
        f.plot(grid, served_g, color=C_SERVED, lw=1.4, alpha=0.95,
               label='being served (Σ pod running)')
    if slots_g:
        f.plot(grid, slots_g, color=C_CEIL, ls='--', lw=1.6,
               label=f"usable slot capacity (ready × {cap['max_conc_pred']:.0f})")
    if nsys_direct or nsys_g or served_g:
        fit = der.get('itl_fit') or {}
        if fit.get('A_ms_per_req'):
            f.set_title(f"ITL = {fit['A_ms_per_req']:.3f}·k + {fit['B_ms']:.1f} ms "
                        f"on kv∈[{fit.get('y_lo')},{fit.get('y_hi')}] "
                        f"(r²={fit.get('r2', 0):.2f}, n={fit.get('n')})"
                        + ('  ρ=%.2f' % fit['rho'] if fit.get('rho') else ''),
                        fontsize=8, loc='right', color='#6b7280')
        # Cost/utilization -- new derived metric, not computed anywhere else.
        # Utilization: time-mean served/slots over the grid (both already
        # computed above for the fill_between just drawn). Cost: replica-
        # seconds, Σ ready(t)·Δt over the actual (unsampled) replica
        # timeseries, not the resampled grid -- reps already carries real
        # per-step durations, so this doesn't need GRID's own resolution.
        util_vals = [s / sl for s, sl in zip(served_g or [], slots_g or [])
                     if sl] if served_g and slots_g else []
        util = mean(util_vals) if util_vals else None
        repl_s = sum((b['t'] - a['t']) * (a.get('ready') or 0)
                     for a, b in zip(reps, reps[1:])) if reps else 0.0
        cost_txt = f'replica-seconds={repl_s:.0f}'
        if util is not None:
            cost_txt += f'  utilization={util:.0%}'
        f.text(0.995, 1.14, cost_txt, transform=f.transAxes, fontsize=8,
               color='#6b7280', ha='right', va='bottom')
    else:
        empty(f, 'no concurrency signal')
    f.set_ylabel('requests')
    f.set_title('5 · concurrency: requests in system vs slot capacity  (L = λ·W)',
                loc='left', fontsize=10)

    # --- panel 6: signed replica-delta per analyzer -------------------------- #
    # Redesign of the shipped reason-code marker strip (Dean: "good direction
    # but looks weird") -- same signal Dean has been hand-grepping
    # controller.log for per finding, now as a signed line in replica units:
    # positive when an analyzer's own capacity math implies scale-UP pressure
    # (RequiredCapacity > 0), negative when it implies scale-DOWN pressure
    # (SpareCapacity > 0), zero in between. Both are already max(0, ...) at
    # the source (saturation/engine_v2.go's applyUniversalThreshold) so they
    # are never simultaneously positive -- rc/prc - sc/prc is the one signed
    # quantity that captures both without a sign ambiguity. Hand-verified
    # against real controller.log ticks (dean-20260810-092644-320): a
    # throughput-analyzer tick immediately before a confirmed scale-up
    # (curr=1,tgt=2) read rc/prc=+0.110; a saturation tick at a confirmed
    # scale-down (curr=3,tgt=1) read sc/prc so the delta was negative.
    #
    # Mild signed log2 (Dean: "log 2? + negatives as -log(|x|)") compresses
    # large excursions (a +11 throughput spike would otherwise dwarf the -1..1
    # steady-state noise) without collapsing small values near zero. Tick
    # labels are re-inverted back to real replica-delta units below -- a
    # reader should never see the transformed numbers.
    def signed_log2(y):
        return math.log2(1 + y) if y >= 0 else -math.log2(1 + abs(y))

    def inv_signed_log2(v, _pos=None):
        real = (2 ** v - 1) if v >= 0 else -(2 ** abs(v) - 1)
        return f'{real:.0f}'

    g = ax[6]
    slog = der.get('scaling_log') or {}
    by_analyzer = slog.get('by_analyzer') or {}
    lanes = sorted(by_analyzer)
    if lanes:
        reason_markers = {}
        MARKER_SHAPES = ['o', 's', '^', 'D', 'v', 'P', 'X']
        absent_t = slog.get('saturation_absent_at')
        # First-occurrence label placements are collected, not drawn inline,
        # so overlapping ones (several reason codes appearing close together
        # in time across different analyzer lines -- confirmed visible on
        # m-satta-dwell around t~150-350) can be staggered apart after all
        # of them are known, rather than each one committing to a fixed
        # offset in isolation.
        pending_labels = []
        for i, name in enumerate(lanes):
            recs = [r for r in by_analyzer[name]
                    if r.get('rc') is not None and r.get('sc') is not None
                    and r.get('prc')]
            if not recs:
                continue
            xs = [rel(r['t'], t0) for r in recs]
            ys = [signed_log2((r['rc'] - r['sc']) / r['prc']) for r in recs]
            color = ANALYZER_COLORS[i % len(ANALYZER_COLORS)]
            # An analyzer that's absent from the configured list still
            # computes and logs a real rc/sc/prc every tick (confirmed,
            # 2026-08-13 all-cells sweep: absent AND still reporting are not
            # mutually exclusive) -- it just cannot act on the number. Dash
            # and fade its line so the shape doesn't read as "this analyzer's
            # vote mattered here", while still showing the real values.
            is_absent_lane = absent_t is not None and name == 'saturation'
            g.plot(xs, ys, color=color, lw=1.4,
                   ls=(':' if is_absent_lane else '-'),
                   alpha=(0.5 if is_absent_lane else 0.9),
                   label=f'{name} (absent, not voting)' if is_absent_lane
                         else name, zorder=2.4)
            # Item 7: detect "analyzer went silent" -- was voting earlier, then
            # all its records stopped appearing in by_analyzer (extractor drops
            # ticks with empty variants: [], so there are no trailing silent recs
            # to extend past; the gap is from recs[-1] to where other analyzers
            # still have data).
            # Distinct from absent-lane: here the analyzer IS configured and
            # voted earlier; it just stopped reporting a variant.
            # Trigger: this analyzer's last valid tick is >60s before the
            # latest tick across ALL other analyzers -- i.e. another analyzer
            # continued running long after this one went quiet, making the
            # gap unambiguously "went silent" vs "both stopped together."
            # Draw a faded dotted horizontal tail so the silent stretch reads
            # as "was active then went quiet" rather than "never had data here."
            # (panel-review-20260817 item 7)
            if recs:
                last_valid_t = rel(recs[-1]['t'], t0)
                # Latest tick across all OTHER analyzers' own valid records
                other_valid_last = max(
                    (rel(r['t'], t0)
                     for nm2, recs2 in by_analyzer.items() if nm2 != name
                     for r in recs2
                     if r.get('rc') is not None and r.get('sc') is not None
                     and r.get('prc')),
                    default=last_valid_t)
                silent_stretch = other_valid_last - last_valid_t
                if silent_stretch > 60.0:
                    last_y = ys[-1]
                    tail_end = other_valid_last
                    g.plot([last_valid_t, tail_end], [last_y, last_y],
                           color=color, lw=0.8, ls=(0, (2, 4)), alpha=0.35,
                           label='_nolegend_', zorder=2.3)
                    g.annotate(
                        f'no variant after {last_valid_t:.0f}s',
                        (last_valid_t + silent_stretch * 0.5, last_y),
                        xytext=(0, 5), textcoords='offset points',
                        fontsize=6, color=color, alpha=0.6,
                        ha='center', va='bottom')
            for r, x, y in zip(recs, xs, ys):
                reason = r.get('reason')
                if not reason:
                    continue
                first_occurrence = reason not in reason_markers
                if first_occurrence:
                    reason_markers[reason] = MARKER_SHAPES[
                        len(reason_markers) % len(MARKER_SHAPES)]
                # Never in the main legend -- the marker-shape-to-reason
                # mapping is documented once via the compact text key below
                # instead, since a full legend row per reason code across
                # every analyzer would repeat panel 3's legend-density
                # problem for what's meant to be a secondary indicator.
                g.scatter([x], [y], marker=reason_markers[reason], s=22,
                          color=color, alpha=(0.5 if is_absent_lane else 0.9),
                          edgecolor=INK, linewidth=0.3,
                          label='_nolegend_', zorder=2.6)
                # Label the reason code directly on the plot the first time
                # it's plotted anywhere in the panel (global across analyzer
                # lines, tracked by reason_markers' own insertion -- reuses
                # the same dedup the shape assignment already does above,
                # rather than a second tracking structure). The text key
                # still documents the full shape-to-reason mapping; this
                # makes the first occurrence of each one easy to find
                # without cross-referencing the key every time.
                if first_occurrence:
                    pending_labels.append((x, y, reason, color))
        # Stagger labels whose x-positions are close together (within 3% of
        # the panel's own span) onto alternating vertical offsets, so close-
        # together first-occurrences don't print on top of each other --
        # confirmed necessary on real data, not a hypothetical: several
        # reason codes across different analyzers land within seconds of
        # each other during the initial scale-up.
        pending_labels.sort(key=lambda p: p[0])
        min_label_gap = max(1.0, span * 0.03)
        # A label whose y sits in the top ~8% of the axes' own range renders
        # into the title's row once the (3, 4+slot*9) offset is added on top
        # of it -- confirmed on real data (m-satta-dwell): a very-early,
        # high-y "T2-default" label landed directly on the panel's title
        # text. get_ylim() already reflects the auto-scaled range from the
        # lines/scatter points plotted just above (no explicit draw needed).
        y_lo, y_hi = g.get_ylim()
        near_top = y_hi - 0.08 * (y_hi - y_lo)
        last_x_by_slot = {}
        for x, y, reason, color in pending_labels:
            # Sentinel must be smaller than any real x - min_label_gap can
            # ever be, not just "smaller than a typical one" -- using
            # -min_label_gap as the default here was an infinite loop for any
            # x more negative than that (real data: a record timestamped
            # before this bundle's own t0, x=-116 against a span where
            # min_label_gap was ~14 -- every empty slot's default compared as
            # "still colliding," so the loop never terminated). float('-inf')
            # is never exceeded by a real value minus a finite gap.
            slot = 0
            while last_x_by_slot.get(slot, float('-inf')) > x - min_label_gap:
                slot += 1
            last_x_by_slot[slot] = x
            # Labels near the top of the axes stagger DOWNWARD instead of
            # upward, so they land inside the plot area rather than in the
            # title's row above it.
            if y >= near_top:
                yoff = -10 - slot * 9
                va = 'top'
            else:
                yoff = 4 + slot * 9
                va = 'bottom'
            g.annotate(reason, (x, y), xytext=(3, yoff),
                       textcoords='offset points', fontsize=6,
                       color=color, ha='left', va=va)
        g.axhline(0, color=INK, lw=0.8, alpha=0.5, zorder=2.0)
        # Ticks must read as real replica-delta values ("±2, ±4, ±8..."), not
        # the log-space numbers the line/scatter data is actually plotted at.
        #
        # The default locator picks positions evenly spaced in LOG-SPACE
        # (where the axis actually lives), then the formatter below inverts
        # each one back to a real value -- but inv_signed_log2 rounds to the
        # nearest integer, and several nearby log-space positions invert to
        # the SAME rounded real value (e.g. signed_log2(0.5)=~0.41 rounds to
        # "0", same as signed_log2(0)=0 itself) or to "-0"/"0" pairs that read
        # as duplicates. A FixedLocator placed at the exact signed_log2(y)
        # position of each of these real values is losslessly invertible --
        # every tick maps to one distinct, real replica-delta, no rounding
        # collision possible. Matplotlib clips whichever of these fall
        # outside the current view, so listing all of them is safe on any
        # run regardless of how wide its actual range is.
        g.yaxis.set_major_locator(
            FixedLocator([signed_log2(y) for y in (-8, -4, -2, -1, 0, 1, 2, 4, 8)]))
        g.yaxis.set_major_formatter(FuncFormatter(inv_signed_log2))
        # The old shipped version placed this text differently depending on
        # whether saturation had its own horizontal lane -- panel 6 no longer
        # has per-analyzer lanes (every analyzer's line now spans the full
        # plot), so that distinction no longer applies; one placement covers
        # both cases.
        if absent_t is not None:
            g.text(0.01, 0.92,
                  'saturation analyzer absent from configured list — did not vote',
                  transform=g.transAxes, fontsize=7.5, color='#b45309',
                  ha='left', va='top', style='italic')
        # Reason-code marker key, since the marker shape is now a secondary
        # indicator layered on the line rather than the only content -- a
        # full second legend column for every marker would repeat the same
        # legend-density problem panel 3 hit; a compact text key does the
        # same job without a second legend box.
        if reason_markers:
            # Inside the axes, not below it -- panel 6 is the bottom-most
            # panel and already owns the figure's one x-axis label there;
            # a key placed below the axis would compete with it for the
            # same strip of space (caught by viewing the render).
            key = '  '.join(f'{shape}={reason}'
                             for reason, shape in sorted(reason_markers.items()))
            g.text(0.99, 0.02, f'markers: {key}', transform=g.transAxes,
                   ha='right', va='bottom', fontsize=6, color='#6b7280')
        # Decision markers: the shared axvline loop at the bottom of render()
        # already draws a vline at every `desired` change on EVERY panel
        # (confirmed by reading that loop before adding this block), so this
        # panel gets scale-up/scale-down markers for free -- no separate draw
        # call needed here.
    else:
        empty(g, 'no scaling-decision data in this bundle — '
                 'extract with --controller-log or none captured for this run')
    g.set_ylabel('replica-delta')
    g.set_xlabel('seconds since warmup end' if warmup_offset_s
                 else 'seconds since run start')
    g.set_title('6 · signed replica-delta per analyzer  '
                '(+ scale-up pressure / − scale-down pressure)',
                loc='left', fontsize=10)

    drains = lg.get('drain_events') or []
    for i, axis in enumerate(ax):
        axis.grid(alpha=.25, lw=.5)
        axis.set_xlim(-warmup_offset_s, span - warmup_offset_s)
        axis.margins(x=0)
        # WVA's observed decisions, drawn on every panel so the triggering signal
        # lines up with the decision instant...
        for p, q in zip(reps, reps[1:]):
            if q['desired'] != p['desired']:
                axis.axvline(rel(q['t'], t0), lw=1.0, ls=(0, (4, 3)),
                             color=C_UP if q['desired'] > p['desired'] else C_DOWN,
                             alpha=.55, zorder=3)
        # ...and the moments those decisions took effect, which is a different
        # instant entirely: one boot lag later for up, a drain later for down.
        mark_effects(axis, reps, t0, drains, label=(axis is ax[2]))
        if i in (0, 1, 3):
            axis.legend(loc='upper left', fontsize=6.5, ncol=1, labelspacing=0.3,
                        handlelength=1.4, borderpad=0.4, framealpha=0.85)
        else:
            axis.legend(loc='upper right', fontsize=7.5, ncol=2, framealpha=0.9)

    foot = ''
    # Placement provisional (Dean, panel-review-20260815-fixes-plan.md Item U):
    # a short marker first, ahead of the caveats/FAIL lines, so a reader sees
    # THAT something is weak before reading why -- the detailed explanation
    # (the "engine occupancy exceeds..." warning) still flows into `foot` via
    # the caveats join below, not duplicated here.
    if weak:
        foot = 'WEAK TIME ANCHOR — arrival-time panels unreliable. '
    if warns:
        foot += 'caveats: ' + '  |  '.join(
            w.split(' - ')[0].split(' -- ')[0] for w in warns)
    fails = [r['capability'] for r in (coverage or {}).get('rows', [])
             if r['verdict'] == 'FAIL']
    if fails:
        foot += ('\n' if foot else '') + 'not exercised by this run: ' + ', '.join(fails)
    render_sha = git_sha()
    extractor_sha = (coverage or {}).get('extractor_sha', '?')
    # Terse by design -- the footer is already dense with caveat text (per
    # Dean's own past feedback on panel density elsewhere on this branch),
    # so this adds one short line, not a second caveats block.
    foot += (('\n' if foot else '')
             + f'rendered @ {render_sha}, bundle extracted @ {extractor_sha}')
    fig.text(0.008, 0.004, foot, fontsize=7, color='#b45309', va='bottom')

    fig.tight_layout(rect=(0, 0.022 if foot else 0, 1, 0.985))
    # PNG-native text-chunk metadata (matplotlib passes this straight through
    # to Pillow's PNG writer -- no new dependency, no new write path). This
    # travels with the file even once it's copied out of its own directory
    # and separated from coverage.json, which this branch's own history
    # shows happens routinely (review-sample mirrors, backlog copies). The
    # footer above is terse by design for a human glancing at the figure;
    # this can be fuller since nobody reads PNG metadata by eye.
    png_meta = {
        'extractor_sha': extractor_sha,
        'render_sha': render_sha,
        'source_run': meta.get('run', '?'),
        'extracted_at': str((coverage or {}).get('extracted_at', '?')),
    }
    fig.savefig(path, dpi=120, metadata=png_meta)
    plt.close(fig)
    return path


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--bundle', required=True)
    ap.add_argument('--out', help='output PNG (default: panels.png beside the bundle)')
    ap.add_argument('--title',
                    help='workload-segment override (substitutes the workload '
                         'portion of the composite title, not the whole string; '
                         'run-id/model/harness/ns are still appended)')
    a = ap.parse_args(argv)

    with open(a.bundle) as fh:
        bundle = json.load(fh)
    here = os.path.dirname(os.path.abspath(a.bundle))

    cov = None
    cov_path = os.path.join(here, 'coverage.json')
    if os.path.exists(cov_path):
        with open(cov_path) as fh:
            cov = json.load(fh)

    out = a.out or os.path.join(here, 'panels.png')
    render(bundle, out, a.title, cov)
    print(f'wrote {out}')

    if cov:
        fails = [r['capability'] for r in cov['rows'] if r['verdict'] == 'FAIL']
        print(f"coverage: {cov['n_pass']} PASS / {cov['n_fail']} FAIL")
        if fails:
            print('  not supported by this run: ' + ', '.join(fails))
    else:
        print('note: no coverage.json beside the bundle — the figure will not carry '
              'its caveats. Re-run extract_real_trace.py to produce it.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
