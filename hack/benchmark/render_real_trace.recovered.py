#!/usr/bin/env python3
"""Render a single-run benchmark bundle (new directory-based format) into panels.png.

    python3 render_real_trace.py --bundle-dir hack/benchmark/results/<label>

All rendering logic is ported verbatim from the validated reference renderer
(render_real_trace.ref.py). The only changes are the data-loading path: the old
renderer read a flat bundle.json; this one reads the new directory schema.

Time values in the bundle are already relative (seconds from load start, t=0).
"""

from __future__ import annotations

import argparse
import json
import math
import os
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from statistics import mean, median, pstdev
from typing import Any

try:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    from matplotlib.ticker import MaxNLocator, FuncFormatter, AutoMinorLocator, FixedLocator
    from matplotlib.colors import LinearSegmentedColormap, to_rgba
    import numpy as np
except ImportError:
    sys.exit("error: matplotlib is required to render.\n"
             "  uv pip install --python .venv/bin/python matplotlib numpy")

# --------------------------------------------------------------------------- #
# Colour vocabulary — identical to the reference renderer's fallback palette.
# --------------------------------------------------------------------------- #
C_ARR, C_DEP, C_DES = "#2563eb", "#059669", "#dc2626"
C_CEIL = C_ACT = C_CAP = C_SYS = "#7c3aed"
C_Q, C_WAIT, C_SERVED = "#d97706", "#dc2626", "#16a34a"
C_UP, C_DOWN = "#dc2626", "#2563eb"
C_EFF_UP, C_EFF_DN = "#7c3aed", "#9ca3af"
BAND_SHADES = ["#a7d8de", "#5fbcc7", "#2f9aa8", "#63c39a", "#9bd8b0"]
GP_COLORS = ["#15803d", "#65a30d", "#eab308", "#f59e0b", "#ea580c", "#b91c1c"]
SIZE_SHADES = ["#dbeafe", "#93c5fd", "#60a5fa"]
ANALYZER_COLORS = ["#0891b2", "#c2410c", "#6d28d9", "#166534"]
INK = "#1f2937"

WAIT_EDGES = [2, 15, 30, 45, 60]
BIN = 10.0
GRID = 2.0
W_REQ = BIN
W_WORK = 30.0
SAT = 0.85


# --------------------------------------------------------------------------- #
# Bundle loading
# --------------------------------------------------------------------------- #

@dataclass
class BundleData:
    bundle_dir: Path
    meta: dict[str, Any]
    provenance: dict[str, Any] | None
    coverage: dict[str, Any] | None
    endpoints: list[dict[str, Any]]
    requests: list[dict[str, Any]] | None
    scaled_objects: list[dict[str, Any]]
    pods_by_so: dict[str, list[dict[str, Any]]]


@dataclass
class InputCheckReport:
    bundle_dir: Path
    required_files_found: list[str] = field(default_factory=list)
    optional_files_missing: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    fallbacks_taken: list[str] = field(default_factory=list)


def _load_json(path: Path) -> Any:
    with path.open() as fh:
        return json.load(fh)


def _load_optional(bundle_dir: Path, filename: str, report: InputCheckReport) -> Any | None:
    path = bundle_dir / filename
    if not path.exists():
        report.optional_files_missing.append(filename)
        return None
    return _load_json(path)


def _require(bundle_dir: Path, filename: str, report: InputCheckReport) -> Any:
    path = bundle_dir / filename
    if not path.exists():
        raise FileNotFoundError(f"missing required bundle file: {path}")
    report.required_files_found.append(filename)
    return _load_json(path)


def _norm_list(data: Any, key: str) -> list[dict[str, Any]]:
    if isinstance(data, list):
        return data
    if isinstance(data, dict) and isinstance(data.get(key), list):
        return data[key]
    raise ValueError(f"expected list or dict with '{key}' key, got {type(data)}")


def _norm_pods(data: Any) -> dict[str, list[dict[str, Any]]]:
    sos = data if isinstance(data, list) else data.get("scaled_objects", [])
    by_so: dict[str, list[dict[str, Any]]] = {}
    for item in sos:
        so_id = item.get("so_id")
        if so_id:
            by_so[so_id] = item.get("pods") or []
    return by_so


def load_bundle(bundle_dir: Path) -> tuple[BundleData, InputCheckReport]:
    bundle_dir = bundle_dir.resolve()
    report = InputCheckReport(bundle_dir=bundle_dir)
    meta = _require(bundle_dir, "meta.json", report)
    endpoints = _norm_list(_require(bundle_dir, "endpoints.json", report), "endpoints")
    scaled_objects = _norm_list(_require(bundle_dir, "scaled_objects.json", report), "scaled_objects")
    pods_by_so = _norm_pods(_require(bundle_dir, "pods.json", report))
    provenance = _load_optional(bundle_dir, "provenance.json", report)
    coverage = _load_optional(bundle_dir, "coverage.json", report)
    requests_raw = _load_optional(bundle_dir, "requests.json", report)
    requests: list[dict[str, Any]] | None = None
    if requests_raw is not None:
        requests = requests_raw if isinstance(requests_raw, list) else requests_raw.get("requests", [])

    # surface warnings
    anchor = meta.get("time_anchor") or {}
    if anchor.get("trustworthy") is False:
        report.warnings.append("time_anchor.trustworthy is false; time-aligned panels are unreliable")
    if requests is None:
        report.warnings.append("requests.json absent; request-derived panels will degrade")

    return BundleData(
        bundle_dir=bundle_dir,
        meta=meta,
        provenance=provenance,
        coverage=coverage,
        endpoints=endpoints,
        requests=requests,
        scaled_objects=scaled_objects,
        pods_by_so=pods_by_so,
    ), report


# --------------------------------------------------------------------------- #
# Coverage normalisation
# New schema: list of {scope, capability, result, detail}
# Reference renderer expects: {rows:[{verdict,capability}], warnings:[], n_pass, n_fail}
# --------------------------------------------------------------------------- #

def _norm_coverage(raw: Any) -> dict[str, Any] | None:
    if raw is None:
        return None
    # already in old format (has n_pass key)
    if isinstance(raw, dict) and "n_pass" in raw:
        return raw
    # new format: list of row objects
    rows_in = raw if isinstance(raw, list) else raw.get("rows", [])
    rows_out = []
    warns = []
    n_pass = n_fail = 0
    for r in rows_in:
        verdict = r.get("result") or r.get("verdict") or "WARN"
        cap = r.get("capability", "")
        rows_out.append({"verdict": verdict, "capability": cap})
        if verdict == "PASS":
            n_pass += 1
        elif verdict == "FAIL":
            n_fail += 1
        detail = r.get("detail", "")
        if detail:
            warns.append(f"{cap} — {detail}")
    return {"rows": rows_out, "warnings": warns, "n_pass": n_pass, "n_fail": n_fail}


# --------------------------------------------------------------------------- #
# Data assembly: map new bundle schema to reference renderer's local variables
# --------------------------------------------------------------------------- #

def _assemble(bundle: BundleData) -> dict[str, Any]:
    """Return a dict with keys matching the reference renderer's locals."""
    meta = bundle.meta

    # requests — two schemas:
    #   per-request: has 't_arr' key — normalise ttft_ms→ttft
    #   bucketed (v0.3 extractor): has 'arr_rate' key — store separately as req_buckets
    raw_reqs = bundle.requests or []
    is_bucketed = bool(raw_reqs and "arr_rate" in raw_reqs[0])
    reqs: list[dict[str, Any]] = []
    req_buckets: list[dict[str, Any]] | None = None
    if is_bucketed:
        req_buckets = raw_reqs  # panels 1a/1b use pre-computed rates directly
    else:
        for r in raw_reqs:
            rec = dict(r)
            # normalise ttft to seconds
            if rec.get("ttft_ms") is not None and "ttft" not in rec:
                rec["ttft"] = rec["ttft_ms"] / 1000.0
            # normalise outcome: 'ok' → keep as-is (reference only tests == 'error' / == 'truncated')
            reqs.append(rec)

    # replicas — aggregate across all SOs (for single-SO runs, just the first)
    # reference renderer uses a single flat reps[] list
    reps_all = []
    for so in bundle.scaled_objects:
        reps_all.extend(so.get("replicas") or [])
    reps_all.sort(key=lambda r: r.get("t", 0))
    # dedup: keep only records where desired or ready actually changes, plus first and last
    reps: list[dict[str, Any]] = []
    for r in reps_all:
        if not reps:
            reps.append(r)
            continue
        prev = reps[-1]
        if r.get("desired") != prev.get("desired") or r.get("ready") != prev.get("ready"):
            reps.append(r)
    # always keep the last record (needed for step-plot tail)
    if reps_all and (not reps or reps[-1] is not reps_all[-1]):
        reps.append(reps_all[-1])

    # system — first endpoint's epp[] (reference calls this 'system')
    system = []
    if bundle.endpoints:
        system = bundle.endpoints[0].get("epp") or []

    # pods — reference expects dict: pod_name -> {series: [...], drain_windows: [...]}
    pods: dict[str, Any] = {}
    for so_id, pod_list in bundle.pods_by_so.items():
        for p in pod_list:
            pod_id = p.get("pod_id") or p.get("name") or so_id
            pods[pod_id] = {
                "series": p.get("series") or [],
                "drain_windows": p.get("drain_windows") or [],
            }

    # derived — reference reads der['scaling_log']['by_analyzer'] for panel 6
    # assemble from scaler[] analyzer_result records
    by_analyzer: dict[str, list[dict[str, Any]]] = {}
    saturation_absent_at = None
    for so in bundle.scaled_objects:
        for ev in (so.get("scaler") or []):
            et = ev.get("event_type")
            if et == "analyzer_result":
                name = ev.get("analyzer", "unknown")
                by_analyzer.setdefault(name, []).append(ev)
            elif et == "analyzer_absent":
                if ev.get("analyzer") == "saturation":
                    saturation_absent_at = ev.get("t")

    der: dict[str, Any] = {}
    if by_analyzer or saturation_absent_at is not None:
        der["scaling_log"] = {
            "by_analyzer": by_analyzer,
            "saturation_absent_at": saturation_absent_at,
        }

    # lags — compute boot lag from replicas timeseries if possible
    der["lags"] = _compute_lags(reps, pods)

    return {
        "meta": meta,
        "reqs": reqs,
        "req_buckets": req_buckets,
        "reps": reps,
        "system": system,
        "pods": pods,
        "der": der,
        "coverage": _norm_coverage(bundle.coverage),
    }


def _compute_lags(reps: list, pods: dict) -> dict[str, Any]:
    """Estimate boot lag from ready transitions in the replica timeseries."""
    boot_s: list[float] = []
    for a, b in zip(reps, reps[1:]):
        ra, rb = a.get("ready"), b.get("ready")
        da, db = a.get("desired"), b.get("desired")
        if None in (ra, rb, da, db):
            continue
        # desired jumped up and ready caught up later — find the gap
        if db > da and rb > ra:
            # approximate: time between desired-up and ready-up at same step is 0 here
            # a real boot lag needs the desired-change timestamp, not available simply
            pass
    # scan for desired changes followed by ready changes
    desire_up_t = None
    for r in reps:
        pass  # simplified: no boot lag computed without controller log
    return {"boot_s_mean": None, "boot_s": boot_s, "scaledown_observed": _has_scaledown(reps)}


def _has_scaledown(reps: list) -> bool:
    for a, b in zip(reps, reps[1:]):
        if (b.get("ready") or 0) < (a.get("ready") or 0):
            return True
    return False


# --------------------------------------------------------------------------- #
# Utility functions (verbatim from reference)
# --------------------------------------------------------------------------- #

def binned_rate(times, t0, t1, bin_s=BIN):
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
    shift = window / 2.0 if centred else 0.0
    order = sorted(zip(times, weights))
    out, lo, hi, acc = [], 0, 0, 0.0
    for t in grid:
        end = t + shift
        while hi < len(order) and order[hi][0] <= end:
            acc += order[hi][1]
            hi += 1
        while lo < hi and order[lo][0] <= end - window:
            acc -= order[lo][1]
            lo += 1
        out.append(acc / window)
    return out


def fill_one_tick(by_t, pgrid):
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
        xs.append(r["t"] - t0)
        ys.append(v)
    return xs, ys


def pct(vals, q):
    if not vals:
        return None
    v = sorted(vals)
    i = min(len(v) - 1, max(0, int(round(q * (len(v) - 1)))))
    return v[i]


def wait_band(r):
    if r.get("outcome") == "error":
        return len(WAIT_EDGES)
    w = r.get("ttft")
    if w is None:
        return 0
    for i, e in enumerate(WAIT_EDGES):
        if w < e:
            return i
    return len(WAIT_EDGES)


def empty(ax, msg):
    ax.text(0.5, 0.5, msg, transform=ax.transAxes, ha="center", va="center",
            fontsize=9, color="#6b7280", style="italic")
    ax.set_yticks([])


def mark_effects(axis, reps, t0, drains, label=False):
    seen_up = seen_dn = False
    events = []
    for p, q in zip(reps, reps[1:]):
        if q.get("ready") is None or p.get("ready") is None:
            continue
        if q["ready"] != p["ready"]:
            events.append((q["t"] - t0, q["ready"] > p["ready"]))
    for t in drains or []:
        if not any(abs(t - t0 - e[0]) < 1.0 for e in events):
            events.append((t - t0, False))
    for t, up in sorted(events):
        lbl = "_nolegend_"
        if label and up and not seen_up:
            lbl, seen_up = "took effect (boot done)", True
        elif label and not up and not seen_dn:
            lbl, seen_dn = "took effect (drain done)", True
        axis.axvline(t, color=(C_EFF_UP if up else C_EFF_DN), lw=1.0,
                     ls=((0, (1, 2)) if up else (0, (5, 2, 1, 2))),
                     alpha=0.85, zorder=3.2, label=lbl)


def terciles(values):
    v = sorted(x for x in values if x)
    if len(v) < 6 or v[0] == v[-1]:
        return None
    return v[len(v) // 3], v[2 * len(v) // 3]


def git_sha():
    try:
        out = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=os.path.dirname(os.path.abspath(__file__)),
            capture_output=True, text=True, timeout=5, check=True)
        return out.stdout.strip() or "unknown"
    except (OSError, subprocess.SubprocessError):
        return "unknown"


# --------------------------------------------------------------------------- #
# Main render function — panel logic verbatim from reference; data comes from
# the assembled dict returned by _assemble().
# --------------------------------------------------------------------------- #

def render(bundle: BundleData, out_path: Path, title: str | None = None) -> Path:
    d = _assemble(bundle)
    meta = d["meta"]
    reqs = d["reqs"]
    req_buckets: list[dict[str, Any]] | None = d.get("req_buckets")
    reps = d["reps"]
    system = d["system"]
    pods = d["pods"]
    der = d["der"]
    coverage = d["coverage"]

    cap = der.get("capacity") or {}
    sat = der.get("sat_band") or {}
    lg = der.get("lags") or {}

    warns = list((coverage or {}).get("warnings") or [])
    sampled = any("SAMPLE" in w for w in warns)
    estimated = bool(meta.get("per_request_estimated"))

    # times are already relative; t0=0, t1=max time seen
    origins = [r["t"] for r in reps[:1]] + [s["t"] for s in system[:1]]
    origins += [min(r["t_arr"] for r in reqs)] if reqs else []
    origins += [min(b["t"] for b in req_buckets)] if req_buckets else []
    for p in pods.values():
        if p.get("series"):
            origins.append(p["series"][0]["t"])
    if not origins:
        # nothing at all — write an empty-bundle notice
        fig, ax = plt.subplots(figsize=(15, 4))
        ax.axis("off")
        ax.text(0.5, 0.5, "bundle has no time series", transform=ax.transAxes,
                ha="center", va="center", fontsize=12, color="#6b7280")
        fig.savefig(out_path, dpi=120)
        plt.close(fig)
        return out_path

    t0 = min(origins)
    # x-axis end: last departure (best signal) with graceful fallbacks.
    # System/pod scrape tails run past load end and must NOT drive the axis.
    last_dep_meta = meta.get("last_departure_t")
    if last_dep_meta:
        # v0.3 bundles: extractor recorded exact last departure
        t1 = float(last_dep_meta)
    elif reqs:
        # per-request bundles: use actual last departure timestamp
        t1 = max(r.get("t_dep") or r["t_arr"] for r in reqs)
    elif req_buckets:
        # bucketed bundles: last bucket time is a good proxy
        t1 = max(b["t"] for b in req_buckets)
    elif system:
        # no request data at all: fall back to last epp scrape
        t1 = system[-1]["t"]
    else:
        # last resort: replica tail
        t1 = max(r["t"] for r in reps) if reps else t0
    span = t1 - t0

    warmup_offset_s = meta.get("warmup_offset_s") or meta.get("prewarm_s") or 0.0
    grid = [i * GRID - warmup_offset_s for i in range(int(span / GRID) + 2)]
    t0 = t0 + warmup_offset_s

    ready_g = hold({r["t"] - t0: r.get("ready") for r in reps}, grid)

    fig, ax = plt.subplots(7, 1, figsize=(15, 19), sharex=True,
                           gridspec_kw={"height_ratios": [3, 3, 2, 3, 2.5, 2.5, 2.2]})

    anchor = meta.get("time_anchor") or {}
    weak = anchor.get("trustworthy") is False
    run_id = meta.get("run_id") or meta.get("run") or "?"
    workload = title if title is not None else meta.get("workload")
    model = meta.get("wva_confirmed_model") or meta.get("model") or "?"
    head = (f"{workload + '  ·  ' if workload else ''}{run_id}  ·  "
            f"{model}  ·  {meta.get('harness') or '?'}  ·  "
            f"ns={meta.get('namespace') or '?'}")
    fig.suptitle(head, fontsize=12, y=0.997)

    # ------------------------------------------------------------------ #
    # Panel 1a: request rate + wait quality bands
    # ------------------------------------------------------------------ #
    a = ax[0]
    if reqs:
        arr_t = [r["t_arr"] - t0 for r in reqs]
        dep_t = [r["t_dep"] - t0 for r in reqs if r.get("t_dep") is not None]
        bands: dict[int, list] = {}
        for r in reqs:
            if r.get("t_dep") is not None:
                bands.setdefault(wait_band(r), []).append(r["t_dep"] - t0)
        bottom = None
        labels = ([f"wait <{WAIT_EDGES[0]}s"] +
                  [f"{WAIT_EDGES[i-1]}-{WAIT_EDGES[i]}s" for i in range(1, len(WAIT_EDGES))] +
                  [f">{WAIT_EDGES[-1]}s / failed"])
        for i in sorted(bands):
            xs2, ys2 = binned_rate(bands[i], -warmup_offset_s, span - warmup_offset_s)
            if not xs2:
                continue
            if bottom is None:
                bottom = [0.0] * len(ys2)
            a.bar(xs2, ys2, width=BIN * 0.95, bottom=bottom,
                  color=GP_COLORS[min(i, len(GP_COLORS) - 1)],
                  label=labels[min(i, len(labels) - 1)], zorder=1)
            bottom = [b + v for b, v in zip(bottom, ys2)]
        a.plot(grid, trailing(dep_t, [1.0] * len(dep_t), grid, W_REQ, centred=True),
               color=INK, lw=2.2, alpha=0.85, zorder=2.6,
               label=f"departure rate, total ({W_REQ:.0f}s centred sliding)")
        a.plot(grid, trailing(arr_t, [1.0] * len(arr_t), grid, W_REQ, centred=True),
               color=C_ARR, lw=2.4, zorder=2.7,
               label=f"arrival rate ({W_REQ:.0f}s centred sliding)")
        n_tr = sum(1 for r in reqs if r.get("outcome") == "truncated")
        n_good = sum(1 for r in reqs if wait_band(r) <= 2)
        pct_good = 100.0 * n_good / len(reqs) if reqs else 0.0
        a.set_title(f"requests: {len(reqs)} offered, {n_tr} cut off at run end, "
                    f"{pct_good:.0f}% good (<{WAIT_EDGES[2]}s)"
                    + ("   — SAMPLE ONLY, rates understated" if sampled else ""),
                    fontsize=8, loc="right",
                    color="#b45309" if sampled else "#6b7280")
        if estimated:
            a.text(0.995, 0.94, "ttft/tokens ESTIMATED, not measured",
                   transform=a.transAxes, fontsize=7.5, color="#b45309",
                   ha="right", va="top", style="italic")
    elif req_buckets:
        bkt_t = [b["t"] - t0 for b in req_buckets]
        bkt_arr = [b.get("arr_rate") or 0.0 for b in req_buckets]
        bkt_dep = [b.get("dep_rate") or 0.0 for b in req_buckets]
        bkt_ff  = [b.get("frac_fast") for b in req_buckets]
        # bars: fast vs slow fractions of dep_rate
        has_ff = any(v is not None for v in bkt_ff)
        if has_ff:
            fast_ys = [d * (f if f is not None else 0.0) for d, f in zip(bkt_dep, bkt_ff)]
            slow_ys = [d - f for d, f in zip(bkt_dep, fast_ys)]
            a.bar(bkt_t, fast_ys, width=BIN * 0.95,
                  color=GP_COLORS[0], label=f"fast (<{WAIT_EDGES[0]}s TTFT)", zorder=1)
            a.bar(bkt_t, slow_ys, width=BIN * 0.95, bottom=fast_ys,
                  color=GP_COLORS[2], label=f"slow (≥{WAIT_EDGES[0]}s TTFT)", zorder=1)
        else:
            a.bar(bkt_t, bkt_dep, width=BIN * 0.95,
                  color=GP_COLORS[0], label="departure rate (binned)", zorder=1)
        a.plot(bkt_t, bkt_dep, color=INK, lw=2.2, alpha=0.85, zorder=2.6,
               label="departure rate (pre-bucketed)")
        a.plot(bkt_t, bkt_arr, color=C_ARR, lw=2.4, zorder=2.7,
               label="arrival rate (pre-bucketed)")
        pct_fast = 100.0 * sum(f or 0.0 for f in bkt_ff) / max(len(bkt_ff), 1) if has_ff else None
        title_r = (f"{pct_fast:.0f}% fast (<{WAIT_EDGES[0]}s TTFT)" if pct_fast is not None
                   else "bucketed timeseries — no per-request TTFT")
        a.set_title(title_r, fontsize=8, loc="right", color="#6b7280")
    else:
        empty(a, "no per-request trace in this bundle")
    a.set_ylabel("requests / s")
    a.set_title(f"1a · request throughput + goodput quality  "
                f"(bars: {BIN:.0f}s bins · curves: {BIN:.0f}s centred sliding)",
                loc="left", fontsize=10)
    # p1a and p6 get their own x-axis labels; interior panels stay unlabelled
    a.tick_params(axis="x", labelbottom=True)
    a.set_xlabel("seconds since warmup end" if warmup_offset_s
                 else "seconds since run start", fontsize=8)

    # ------------------------------------------------------------------ #
    # Panel 1b: work throughput vs capacity
    # ------------------------------------------------------------------ #
    b = ax[1]
    drew_b = False
    offered_w = total_w = None
    if reqs:
        arr_t = [r["t_arr"] - t0 for r in reqs]
        arr_w = [float(r.get("out_tok") or 0) for r in reqs]
        offered_w = trailing(arr_t, arr_w, grid, W_WORK)
        b.plot(grid, offered_w, color=C_ARR, lw=2.4, zorder=2.7,
               label=f"offered work ({W_WORK:.0f}s trailing)")
        done = [r for r in reqs if r.get("t_dep") is not None]
        tc = terciles([r.get("out_tok") for r in done])
        if tc:
            lo_e, hi_e = tc
            buckets: list[list] = [[], [], []]
            for r in done:
                ot = r.get("out_tok") or 0
                k = 0 if ot <= lo_e else (1 if ot <= hi_e else 2)
                buckets[k].append(r)
            stacks = [trailing([r["t_dep"] - t0 for r in bk],
                               [float(r.get("out_tok") or 0) for r in bk],
                               grid, W_WORK) for bk in buckets]
            lbls = [f"small (≤{lo_e:.0f} tok)", f"medium ({lo_e:.0f}–{hi_e:.0f})",
                    f"large (>{hi_e:.0f})"]
            b.stackplot(grid, *stacks, colors=SIZE_SHADES, labels=lbls, alpha=0.6, edgecolor="none")
            total_w = [sum(v) for v in zip(*stacks)]
        else:
            total_w = trailing([r["t_dep"] - t0 for r in done],
                               [float(r.get("out_tok") or 0) for r in done],
                               grid, W_WORK)
            b.stackplot(grid, total_w, colors=[SIZE_SHADES[1]], alpha=0.6,
                        edgecolor="none", labels=["completed work"])
        b.plot(grid, total_w, color=INK, lw=2.2, alpha=0.85, zorder=2.6,
               label="completed work, total")
        drew_b = True
    knee = der.get("tput_knee") or {}
    knee_rate = knee.get("gen_tok_s") if knee.get("confident") else None
    sat_rate = sat.get("gen_tok_s")
    ceil_rate = knee_rate or sat_rate
    if ceil_rate and reps:
        ceil = [v * ceil_rate for v in ready_g]
        src = (f"throughput knee, n={knee.get('n')}" if knee_rate
               else f"saturated at kv≥{sat.get('threshold')}")
        b.plot(grid, ceil, color=C_CAP, ls="--", lw=1.6, zorder=2.5,
               label=f"capacity ceiling (ready × {ceil_rate:.0f} tok/s per pod; {src})")
        if drew_b and total_w:
            b.fill_between(grid, total_w, ceil,
                           where=[c > dv for c, dv in zip(ceil, total_w)],
                           interpolate=True, color=C_CAP, alpha=0.15,
                           label="unused capacity")
        drew_b = True
        work_peak = max((offered_w or [0]) + (total_w or [0]), default=0)
        if work_peak == 0 and ceil:
            work_peak = median(ceil)
        if work_peak > 0:
            y_max = 1.5 * work_peak
            b.set_ylim(0, y_max)
    if not drew_b:
        empty(b, "no throughput view available — out_tok not present in requests")
    else:
        done_reqs = [r for r in reqs if r.get("t_dep") is not None]
        tok_sum = sum(float(r.get("out_tok") or 0) for r in done_reqs)
        if done_reqs and tok_sum > 0 and total_w:
            span_s = max(r["t_dep"] for r in done_reqs) - min(r["t_arr"] for r in reqs)
            if span_s > 0:
                s_per_1000 = 1000.0 * span_s / tok_sum
                per_1000_series = [1000.0 / v for v in total_w if v and v > 0]
                std_per_1000 = pstdev(per_1000_series) if len(per_1000_series) > 1 else 0.0
                b.set_title(f"{s_per_1000:.2f}±{std_per_1000:.2f}s per 1000 tokens (mean±std, delivered)"
                            + ("   — tokens ESTIMATED, not measured" if estimated else ""),
                            fontsize=8, loc="right",
                            color="#b45309" if estimated else "#6b7280")
    b.set_ylabel("output tokens / s")
    b.set_title(f"1b · work throughput: output tokens offered vs delivered vs "
                f"capacity  ({W_WORK:.0f}s trailing, Prom-style)", loc="left", fontsize=10)

    # ------------------------------------------------------------------ #
    # Panel 2: desired vs ready replicas
    # ------------------------------------------------------------------ #
    c = ax[2]
    if reps:
        xs = [r["t"] - t0 for r in reps]
        dz = [r.get("desired") for r in reps]
        rz = [r.get("ready") for r in reps]
        c.step(xs, [v + 0.05 if v is not None else None for v in dz], where="post",
               color=C_DES, lw=2.2, alpha=0.9, label="desired (WVA)")
        c.step(xs, [v - 0.05 if v is not None else None for v in rz], where="post",
               color=C_ACT, lw=2.2, alpha=0.9, label="ready (alive)")
        if all(v is not None for v in dz + rz):
            accepting = [min(d, r) for d, r in zip(dz, rz)]
            if any(r > acc for r, acc in zip(rz, accepting)):
                c.fill_between(xs, accepting, rz, step="post", facecolor="none",
                               hatch="////", edgecolor=C_ACT, linewidth=0.0,
                               alpha=0.6, label="draining (not usable capacity)")
        note = (f"boot {lg['boot_s_mean']:.0f}s mean/{len(lg.get('boot_s') or [])}"
                if lg.get("boot_s_mean") else "boot: n/a")
        if not lg.get("scaledown_observed"):
            note += " · no scale-down"
        c.set_title(note, fontsize=8, loc="right", color="#6b7280")
        # Annotate each desired-change event with its signed delta on the p2 x-axis.
        # Events beyond the x-axis end (e.g. last scale-down after load ends) are
        # listed as a text summary in the bottom-right corner instead.
        xlim_end = span - warmup_offset_s
        off_axis_events: list[str] = []
        for p_r, q_r in zip(reps, reps[1:]):
            if q_r.get("desired") is None or p_r.get("desired") is None:
                continue
            delta = (q_r["desired"] or 0) - (p_r["desired"] or 0)
            if delta == 0:
                continue
            x_ev = q_r["t"] - t0
            label_str = f"{delta:+d}"
            if x_ev > xlim_end:
                off_axis_events.append(f"t={x_ev:.0f}s {label_str}")
            else:
                c.annotate(label_str, xy=(x_ev, 0), xycoords=("data", "axes fraction"),
                           xytext=(2, -9), textcoords="offset points",
                           fontsize=6.5, color=C_UP if delta > 0 else C_DOWN,
                           ha="left", va="top", zorder=4,
                           annotation_clip=False)
        if off_axis_events:
            c.text(0.99, 0.04, "off-axis: " + "  ".join(off_axis_events),
                   transform=c.transAxes, fontsize=6, color="#6b7280",
                   ha="right", va="bottom")
    else:
        empty(c, "no replica timeseries")
    c.set_ylabel("replicas")
    c.yaxis.set_major_locator(MaxNLocator(integer=True))
    c.set_title("2 · autoscaling: desired vs ready replicas", loc="left", fontsize=10)

    # ------------------------------------------------------------------ #
    # Panel 3: requests per pod — running, draining, waiting, EPP queue
    # ------------------------------------------------------------------ #
    d_ax = ax[3]
    if pods:
        pgrid = sorted({round(s["t"]) for p in pods.values() for s in p["series"]})
        xs = [t - t0 for t in pgrid]
        width = max(1.0, span / max(1, len(pgrid)) * 0.95)
        bottom = [0.0] * len(pgrid)
        run_tot = [0.0] * len(pgrid)
        drain_tot = [0.0] * len(pgrid)
        wait_tot = [0.0] * len(pgrid)
        ordered = sorted(pods.items(),
                         key=lambda kv: (
                             min((s["t"] for s in kv[1]["series"]), default=float("inf")),
                             -max((s["t"] for s in kv[1]["series"]), default=float("-inf")),
                         ))
        pod_num = {pod: i + 1 for i, (pod, _p) in enumerate(ordered)}
        many_pods = len(ordered) > 6
        live_count = [0] * len(pgrid)
        peak_run: dict[str, float] = {}
        any_draining = False
        k_sat_ac = sat.get("threshold") or SAT

        for i, (pod, p) in enumerate(ordered):
            by_t = {round(s["t"]): s.get("run") for s in p["series"]}
            kv_by_t = {round(s["t"]): s.get("kv") for s in p["series"]}
            windows = p.get("drain_windows") or []
            drain_ts = {round(t) for t0_, t1_ in windows
                        for t in range(int(t0_), int(t1_) + 1)}
            filled_run, run_stale, multi_gap = fill_one_tick(by_t, pgrid)
            if multi_gap:
                print(f"warning: panel 3: pod {pod_num[pod]} multi-tick gap", file=sys.stderr)
            run_ys, drain_ys, run_ys_stale, drain_ys_stale = [], [], [], []
            for ti, t in enumerate(pgrid):
                if t in by_t:
                    live_count[ti] += 1
                v = filled_run[ti]
                st = run_stale[ti]
                if t in drain_ts:
                    run_ys.append(0.0); run_ys_stale.append(False)
                    drain_ys.append(v); drain_ys_stale.append(st)
                else:
                    run_ys.append(v); run_ys_stale.append(st)
                    drain_ys.append(0.0); drain_ys_stale.append(False)
            non_sat_run = [filled_run[ti] for ti, t in enumerate(pgrid)
                           if (kv_by_t.get(t) or 0.0) < k_sat_ac]
            peak_run[pod] = max(non_sat_run, default=0.0)
            run_label = (f"pod {pod_num[pod]} running" if not many_pods
                         else ("pods running (see color key below)" if i == 0
                               else "_nolegend_"))
            d_ax.bar(xs, run_ys, width=width, bottom=bottom,
                     color=BAND_SHADES[i % len(BAND_SHADES)],
                     edgecolor=INK, linewidth=0.25, label=run_label, zorder=1)
            stale_run_ys = [y if st else 0.0 for y, st in zip(run_ys, run_ys_stale)]
            if any(stale_run_ys):
                d_ax.bar(xs, stale_run_ys, bottom=bottom, width=width, color="none",
                         hatch="xx", edgecolor="#6b7280", linewidth=0.4,
                         label="stale (carried forward one tick)" if i == 0 else "_nolegend_",
                         zorder=1.5)
            bottom = [bt + y for bt, y in zip(bottom, run_ys)]
            run_tot = [a_ + y for a_, y in zip(run_tot, run_ys)]
            if any(drain_ys):
                bars = d_ax.bar(xs, drain_ys, width=width, bottom=bottom,
                                color=BAND_SHADES[i % len(BAND_SHADES)],
                                hatch="....", edgecolor=INK, linewidth=0.25,
                                label=("_nolegend_" if any_draining
                                       else "pod removed near scale-down (not necessarily draining)"),
                                zorder=1)
                for bar in bars:
                    bar.set_hatch_linewidth(0.3)
                    bar.set_hatchcolor("#f5f5f5")
                any_draining = True
                bottom = [bt + y for bt, y in zip(bottom, drain_ys)]
                drain_tot = [a_ + y for a_, y in zip(drain_tot, drain_ys)]

        any_waiting_labelled = False
        for i, (pod, p) in enumerate(ordered):
            by_t = {round(s["t"]): s.get("wait") for s in p["series"]}
            ys, ys_stale, _ = fill_one_tick(by_t, pgrid)
            if not any(ys):
                continue
            wait_label = (f"pod {pod_num[pod]} waiting" if not many_pods
                          else ("pods waiting (see color key below)"
                                if not any_waiting_labelled else "_nolegend_"))
            any_waiting_labelled = True
            bars = d_ax.bar(xs, ys, width=width, bottom=bottom,
                            color=BAND_SHADES[i % len(BAND_SHADES)],
                            hatch="////", edgecolor=INK, linewidth=0.25,
                            label=wait_label, zorder=1)
            for bar in bars:
                bar.set_hatch_linewidth(0.3)
                bar.set_hatchcolor("#f5f5f5")
            bottom = [bt + y for bt, y in zip(bottom, ys)]
            wait_tot = [a_ + y for a_, y in zip(wait_tot, ys)]

        avg_run = [(rt / lc) if lc else None for rt, lc in zip(run_tot, live_count)]
        avg_run_xs = [x for x, v in zip(xs, avg_run) if v is not None]
        avg_run_ys = [v for v in avg_run if v is not None]
        if avg_run_ys:
            d3 = d_ax.twinx()
            d3.plot(avg_run_xs, avg_run_ys, color="#eab308", lw=1.3, ls="-",
                    alpha=0.9, zorder=2.7, label="mean running per live pod")
            d3.set_ylim(bottom=0)
            d3.invert_yaxis()
            d3.set_ylabel("mean running/pod", fontsize=8)
            d3.tick_params(axis="y", labelsize=7)
            d3.legend(loc="upper right", fontsize=6.5, framealpha=0.85)

        sys_by_t = {round(s["t"]): s.get("in_system") for s in system
                    if s.get("in_system") is not None}
        insys_p = hold(sys_by_t, pgrid) if sys_by_t else None
        if insys_p:
            epp = [max(0.0, n - r - dr - w)
                   for n, r, dr, w in zip(insys_p, run_tot, drain_tot, wait_tot)]
            if any(epp):
                d_ax.bar(xs, epp, width=width, bottom=bottom, color=C_Q, alpha=0.75,
                         label="EPP queue (in system − Σrunning − Σdraining − Σwaiting)",
                         zorder=1)
        yn = []
        if sys_by_t:
            xn, yn = step_series(system, "in_system", t0)
            d_ax.plot(xn, yn, color=C_WAIT, lw=2.4, alpha=0.9, zorder=2.8,
                      label="total requests in system (overlay)")

        ttfts = [r["ttft"] for r in reqs if r.get("ttft") is not None]
        if ttfts:
            ps = [pct(ttfts, q) for q in (0.5, 0.75, 0.9, 0.95)]
            d_ax.set_title("TTFT p50/p75/p90/p95 (s): "
                           + "/".join(f"{p:.2f}" for p in ps if p is not None),
                           fontsize=8, loc="right", color="#6b7280")
        else:
            d_ax.set_title("TTFT: n/a (no per-request trace or ttft absent)",
                           fontsize=8, loc="right", color="#6b7280")
        key = "  ".join(f"{n}={pod.split('-')[-1]}"
                        for pod, n in sorted(pod_num.items(), key=lambda kv: kv[1]))
        d_ax.text(0.5, -0.08, key, transform=d_ax.transAxes, ha="center", va="top",
                  fontsize=6, color="#6b7280", wrap=True)
        total_peak = sum(peak_run.values()) or 1.0
        strip_sm = plt.cm.ScalarMappable(
            cmap=plt.matplotlib.colors.ListedColormap(
                [BAND_SHADES[i % len(BAND_SHADES)] for i, _ in enumerate(ordered)]))
        strip_sm.set_array([])
        cb_ax = fig.colorbar(strip_sm, ax=d_ax, fraction=0.015, pad=0.01).ax
        cb_ax.cla()
        cb_ax.set_xticks([]); cb_ax.set_yticks([])
        for spine in cb_ax.spines.values():
            spine.set_visible(False)
        strip_bottom = 0.0
        for i, (pod, _p) in enumerate(ordered):
            h = peak_run.get(pod, 0.0) / total_peak
            if h <= 0:
                continue
            cb_ax.bar([0], [h], bottom=strip_bottom, width=1.0,
                      color=BAND_SHADES[i % len(BAND_SHADES)],
                      edgecolor=INK, linewidth=0.25)
            if h > 0.03:
                cb_ax.text(0, strip_bottom + h / 2, str(pod_num[pod]),
                           ha="center", va="center", fontsize=5, color=INK)
            strip_bottom += h
    else:
        empty(d_ax, "no pod metrics in this bundle — per-pod view unavailable")
    d_ax.set_ylabel("requests")
    d_ax.set_title("3 · requests per pod: running, draining, waiting, EPP queue  "
                   "(stack ≡ in system)", loc="left", fontsize=10)

    # ------------------------------------------------------------------ #
    # Panel 4: per-pod KV% heatmap
    # ------------------------------------------------------------------ #
    e = ax[4]
    if pods:
        e_pgrid = sorted({round(s["t"]) for p in pods.values() for s in p["series"]})
        e_xs = [t - t0 for t in e_pgrid]
        e_ordered = sorted(pods.items(),
                           key=lambda kv: (
                               min((s["t"] for s in kv[1]["series"]), default=float("inf")),
                               -max((s["t"] for s in kv[1]["series"]), default=float("-inf")),
                           ))
        kv_matrix = []
        for pod, p in e_ordered:
            by_t = {round(s["t"]): s.get("kv") for s in p["series"]}
            kv_matrix.append([by_t.get(t) for t in e_pgrid])
        k_sat = sat.get("threshold") or SAT
        kv_cmap = LinearSegmentedColormap.from_list(
            "kv_heat", [(0.0, "#ffffff"), (k_sat, "#16a34a"), (1.0, "#dc2626")])
        dead_color = to_rgba("#d1d5db")
        rgba = np.array([[dead_color if v is None else kv_cmap(v) for v in row]
                         for row in kv_matrix])
        n_pods_e = len(e_ordered)
        e.imshow(rgba, aspect="auto", origin="upper",
                 extent=(e_xs[0] if e_xs else 0, e_xs[-1] if e_xs else 1, n_pods_e, 0),
                 interpolation="nearest", zorder=1)
        e.set_yticks([i + 0.5 for i in range(n_pods_e)])
        e.set_yticklabels([str(pod_num.get(pod, i + 1)) for i, (pod, _) in enumerate(e_ordered)],
                          fontsize=6)
        for i in range(n_pods_e + 1):
            e.axhline(i, color=INK, lw=0.5, alpha=0.3, zorder=2)
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
                    e.add_patch(plt.Rectangle(
                        (e_xs[ti] - 0.5, ri), 1.0, 1.0, fill=False,
                        edgecolor="#eab308", linewidth=1.0, zorder=2.5))
        if avg_kv:
            e2 = e.twinx()
            e2.plot(avg_xs, avg_kv, color=INK, lw=1.2, zorder=3,
                    label="mean KV% across live pods (see colorbar scale)")
            e2.set_ylim(0, 1)
            e2.set_yticks([])
            e2.spines["right"].set_visible(False)
            e2.legend(loc="upper center", bbox_to_anchor=(0.5, -0.02),
                      fontsize=6.5, framealpha=0.85, ncol=1)
        cb = fig.colorbar(plt.cm.ScalarMappable(cmap=kv_cmap), ax=e,
                          fraction=0.02, pad=0.04)
        cb.set_label(f"KV%  (white→green below k_sat={k_sat:.2f}, green→red at/above)",
                     fontsize=7)
        cb.ax.tick_params(labelsize=6)
    else:
        empty(e, "no pod metrics — per-pod KV% unavailable")
    e.set_ylabel("pod")
    e.set_title("4 · per-pod KV% heatmap", loc="left", fontsize=10)
    r_info = der.get("router") or {}
    p95 = r_info.get("disp_p95")
    e.text(0.995, 1.14 if pods else 1.02,
           f"router imbalance p95={'?' if p95 is None else round(p95, 2)}, "
           f"{r_info.get('leader_flips', '?')} leader flips / {r_info.get('n', '?')} "
           "samples (not an oscillation test)",
           transform=e.transAxes, fontsize=8, color="#6b7280",
           ha="right", va="bottom")

    # ------------------------------------------------------------------ #
    # Panel 5: concurrency L(t) vs slot capacity
    # ------------------------------------------------------------------ #
    f = ax[5]
    nsys_g = None
    sys_by_t = {s["t"] - t0: s.get("in_system") for s in system
                if s.get("in_system") is not None}
    if sys_by_t:
        nsys_g = hold(sys_by_t, grid)
    nsys_direct = [(s["t"] - t0, s["in_system"])
                   for s in system if s.get("in_system") is not None]
    served_by_t: dict[float, float] = {}
    for p in pods.values():
        for s in p["series"] or []:
            if s.get("run") is not None:
                k = round(s["t"])
                served_by_t[k] = served_by_t.get(k, 0.0) + s["run"]
    served_g = hold({k - t0: v for k, v in served_by_t.items()}, grid) if served_by_t else None
    slots_g = ([v * cap["max_conc_pred"] for v in ready_g]
               if cap.get("max_conc_pred") and reps else None)

    if served_g and slots_g:
        f.fill_between(grid, served_g, slots_g,
                       where=[c > s for c, s in zip(slots_g, served_g)],
                       interpolate=True, color=C_CAP, alpha=0.15, label="unused capacity")
    if served_g and nsys_g:
        f.fill_between(grid, served_g, nsys_g, color=C_WAIT, alpha=0.16,
                       label="queued (L − served)")
    if nsys_direct or nsys_g:
        _nsys_xs = [x for x, _ in nsys_direct] if nsys_direct else grid
        _nsys_ys = [y for _, y in nsys_direct] if nsys_direct else (nsys_g or [])
        f.plot(_nsys_xs, _nsys_ys, color=C_WAIT, lw=1.6, alpha=0.9,
               drawstyle="steps-post",
               label="in system  L(t)" + (" — SAMPLE" if sampled else ""))
    if served_g:
        f.plot(grid, served_g, color=C_SERVED, lw=1.4, alpha=0.95,
               label="being served (Σ pod running)")
    if slots_g:
        f.plot(grid, slots_g, color=C_CEIL, ls="--", lw=1.6,
               label=f"usable slot capacity (ready × {cap['max_conc_pred']:.0f})")
    f.yaxis.set_minor_locator(AutoMinorLocator())
    f.grid(which="minor", axis="y", alpha=0.12, lw=0.4)
    if nsys_direct or nsys_g or served_g:
        util_vals = [s / sl for s, sl in zip(served_g or [], slots_g or []) if sl] \
            if served_g and slots_g else []
        util = mean(util_vals) if util_vals else None
        repl_s = sum((b_["t"] - a_["t"]) * (a_.get("ready") or 0)
                     for a_, b_ in zip(reps, reps[1:])) if reps else 0.0
        cost_txt = f"replica-seconds={repl_s:.0f}"
        if util is not None:
            cost_txt += f"  utilization={util:.0%}"
        f.text(0.995, 1.14, cost_txt, transform=f.transAxes, fontsize=8,
               color="#6b7280", ha="right", va="bottom")
    else:
        empty(f, "no concurrency signal")
    f.set_ylabel("requests")
    f.set_title("5 · concurrency: requests in system vs slot capacity  (L = λ·W)",
                loc="left", fontsize=10)

    # ------------------------------------------------------------------ #
    # Panel 6: signed replica-delta per analyzer
    # ------------------------------------------------------------------ #
    def signed_log2(y):
        return math.log2(1 + y) if y >= 0 else -math.log2(1 + abs(y))

    def inv_signed_log2(v, _pos=None):
        real = (2 ** v - 1) if v >= 0 else -(2 ** abs(v) - 1)
        return f"{real:.0f}"

    g = ax[6]
    slog = der.get("scaling_log") or {}
    by_analyzer = slog.get("by_analyzer") or {}
    lanes = sorted(by_analyzer)
    if lanes:
        reason_markers: dict[str, str] = {}
        labeled_reasons: set[str] = set()
        MARKER_SHAPES = ["o", "s", "^", "D", "v", "P", "X"]
        absent_t = slog.get("saturation_absent_at")
        pending_labels: list[tuple] = []
        for i, name in enumerate(lanes):
            recs = [r for r in by_analyzer[name]
                    if r.get("rc") is not None and r.get("sc") is not None and r.get("prc")]
            if not recs:
                continue
            xs = [r["t"] - t0 for r in recs]
            ys = [signed_log2((r["rc"] - r["sc"]) / r["prc"]) for r in recs]
            color = ANALYZER_COLORS[i % len(ANALYZER_COLORS)]
            is_absent_lane = absent_t is not None and name == "saturation"
            g.plot(xs, ys, color=color, lw=1.4,
                   ls=(":" if is_absent_lane else "-"),
                   alpha=(0.5 if is_absent_lane else 0.9),
                   label=f"{name} (absent, not voting)" if is_absent_lane else name,
                   zorder=2.4)
            if recs:
                last_valid_t = recs[-1]["t"] - t0
                other_valid_last = max(
                    (r["t"] - t0 for nm2, recs2 in by_analyzer.items() if nm2 != name
                     for r in recs2
                     if r.get("rc") is not None and r.get("sc") is not None and r.get("prc")),
                    default=last_valid_t)
                if other_valid_last - last_valid_t > 60.0:
                    g.plot([last_valid_t, other_valid_last], [ys[-1], ys[-1]],
                           color=color, lw=0.8, ls=(0, (2, 4)), alpha=0.35,
                           label="_nolegend_", zorder=2.3)
            for r, x, y in zip(recs, xs, ys):
                reason = r.get("reason")
                if not reason:
                    continue
                first_occurrence = reason not in reason_markers
                if first_occurrence:
                    reason_markers[reason] = MARKER_SHAPES[len(reason_markers) % len(MARKER_SHAPES)]
                g.scatter([x], [y], marker=reason_markers[reason], s=22,
                          color=color, alpha=(0.5 if is_absent_lane else 0.9),
                          edgecolor=INK, linewidth=0.3, label="_nolegend_", zorder=2.6)
                if reason not in labeled_reasons and x >= 0:
                    labeled_reasons.add(reason)
                    pending_labels.append((x, y, reason, color))
        pending_labels.sort(key=lambda p: p[0])
        min_label_gap = max(1.0, span * 0.03)
        y_lo, y_hi = g.get_ylim()
        near_top = y_hi - 0.08 * (y_hi - y_lo)
        last_x_by_slot: dict[int, float] = {}
        for x, y, reason, color in pending_labels:
            slot = 0
            while last_x_by_slot.get(slot, float("-inf")) > x - min_label_gap:
                slot += 1
            last_x_by_slot[slot] = x
            if y >= near_top:
                yoff = -10 - slot * 9
                va = "top"
            else:
                yoff = 4 + slot * 9
                va = "bottom"
            g.annotate(reason, (x, y), xytext=(3, yoff),
                       textcoords="offset points", fontsize=6,
                       color=color, ha="left", va=va)
        g.axhline(0, color=INK, lw=0.8, alpha=0.5, zorder=2.0)
        g.yaxis.set_major_locator(
            FixedLocator([signed_log2(y) for y in (-8, -4, -2, -1, 0, 1, 2, 4, 8)]))
        g.yaxis.set_major_formatter(FuncFormatter(inv_signed_log2))
        g.yaxis.set_minor_locator(AutoMinorLocator())
        g.grid(which="minor", axis="y", alpha=0.15, lw=0.5)
        g.grid(which="major", axis="y", alpha=0.3, lw=0.6)
        if absent_t is not None:
            g.text(0.01, 0.92,
                   "saturation analyzer absent from configured list — did not vote",
                   transform=g.transAxes, fontsize=7.5, color="#b45309",
                   ha="left", va="top", style="italic")
        if reason_markers:
            key = "  ".join(f"{shape}={reason}"
                            for reason, shape in sorted(reason_markers.items()))
            g.text(0.99, 0.02, f"markers: {key}", transform=g.transAxes,
                   ha="right", va="bottom", fontsize=6, color="#6b7280")
    else:
        empty(g, "no scaling-decision data — extract with --controller-log or none captured")
    g.set_ylabel("replica-delta")
    g.set_xlabel("seconds since warmup end" if warmup_offset_s else "seconds since run start")
    g.set_title("6 · signed replica-delta per analyzer  "
                "(+ scale-up pressure / − scale-down pressure)",
                loc="left", fontsize=10)

    # ------------------------------------------------------------------ #
    # Shared: grid, x-limits, decision + effect markers, legends
    # ------------------------------------------------------------------ #
    drains = lg.get("drain_events") or []
    for i, axis in enumerate(ax):
        axis.grid(alpha=0.25, lw=0.5)
        axis.set_xlim(-warmup_offset_s, span - warmup_offset_s)
        axis.margins(x=0)
        for p_r, q_r in zip(reps, reps[1:]):
            if q_r.get("desired") != p_r.get("desired"):
                axis.axvline(q_r["t"] - t0, lw=1.0, ls=(0, (4, 3)),
                             color=C_UP if (q_r.get("desired") or 0) > (p_r.get("desired") or 0)
                             else C_DOWN,
                             alpha=0.55, zorder=3)
        mark_effects(axis, reps, t0, drains, label=(axis is ax[2]))
        if i in (0, 1, 3):
            axis.legend(loc="upper left", fontsize=6.5, ncol=1, labelspacing=0.3,
                        handlelength=1.4, borderpad=0.4, framealpha=0.85)
        else:
            axis.legend(loc="upper right", fontsize=7.5, ncol=2, framealpha=0.9)

    # ------------------------------------------------------------------ #
    # Footer
    # ------------------------------------------------------------------ #
    foot = ""
    if weak:
        foot = "WEAK TIME ANCHOR — arrival-time panels unreliable. "
    if warns:
        foot += "caveats: " + "  |  ".join(
            w.split(" - ")[0].split(" -- ")[0] for w in warns)
    fails = [r["capability"] for r in (coverage or {}).get("rows", [])
             if r.get("verdict") == "FAIL"]
    if fails:
        foot += ("\n" if foot else "") + "not exercised by this run: " + ", ".join(fails)
    render_sha = git_sha()
    extractor_sha = (coverage or {}).get("extractor_sha") or \
                    (bundle.provenance or {}).get("extractor_version") or "?"
    foot += (("\n" if foot else "") +
             f"rendered @ {render_sha}, bundle extracted @ {extractor_sha}")
    fig.text(0.008, 0.004, foot, fontsize=7, color="#b45309", va="bottom")

    fig.tight_layout(rect=(0, 0.022 if foot else 0, 0.97, 0.985))

    png_meta = {
        "extractor_sha": extractor_sha,
        "render_sha": render_sha,
        "source_run": meta.get("run_id") or meta.get("run") or "?",
        "extracted_at": str((coverage or {}).get("extracted_at") or
                            meta.get("extracted_at") or "?"),
    }
    fig.savefig(out_path, dpi=120, metadata=png_meta)
    plt.close(fig)
    return out_path


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #

def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle-dir", required=True, help="Path to bundle directory")
    parser.add_argument("--out", help="Output PNG (default: panels.png inside bundle dir)")
    parser.add_argument("--title", help="Workload label override (prepended to run-id in title)")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    bundle_dir = Path(args.bundle_dir)
    bundle, report = load_bundle(bundle_dir)

    print("input-check:")
    print(f"  bundle_dir: {report.bundle_dir}")
    for f in report.required_files_found:
        print(f"    found: {f}")
    for f in report.optional_files_missing:
        print(f"    missing optional: {f}")
    for w in report.warnings:
        print(f"    warning: {w}")

    out_path = Path(args.out) if args.out else bundle_dir / "panels.png"
    render(bundle, out_path, title=args.title)
    print(f"wrote {out_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
