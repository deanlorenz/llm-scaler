#!/usr/bin/env python3
"""Generate a self-contained single-run HTML report from a bundle directory.

    python3 report.py --bundle-dir hack/benchmark/results/<label>
    python3 report.py --bundle-dir hack/benchmark/results/<label> --out my-report.html

The report embeds panels.png as a base64 data URI so the HTML file is fully
self-contained and shareable without the bundle directory alongside it.

Requires only the standard library — no matplotlib, no numpy.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import subprocess
from pathlib import Path
from typing import Any


# --------------------------------------------------------------------------- #
# Helpers
# --------------------------------------------------------------------------- #

def _esc(s: str) -> str:
    return (str(s)
            .replace("&", "&amp;")
            .replace("<", "&lt;")
            .replace(">", "&gt;")
            .replace('"', "&quot;"))


def _load_json(path: Path) -> Any:
    with path.open() as fh:
        return json.load(fh)


def _load_optional(path: Path) -> Any | None:
    return _load_json(path) if path.exists() else None


def _png_data_uri(path: Path) -> str | None:
    if not path.exists():
        return None
    data = base64.b64encode(path.read_bytes()).decode()
    return f"data:image/png;base64,{data}"


def _git_sha() -> str:
    try:
        out = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=os.path.dirname(os.path.abspath(__file__)),
            capture_output=True, text=True, timeout=5, check=True)
        return out.stdout.strip() or "unknown"
    except (OSError, subprocess.SubprocessError):
        return "unknown"


def _fmt(v: Any, unit: str = "") -> str:
    if v is None:
        return "—"
    if isinstance(v, float):
        if abs(v) >= 1000:
            return f"{v:,.0f}{unit}"
        return f"{v:.2f}{unit}"
    return f"{v}{unit}"


# --------------------------------------------------------------------------- #
# Metric extraction from bundle
# --------------------------------------------------------------------------- #

def _extract_metrics(
    meta: dict,
    endpoints: list,
    scaled_objects: list,
    requests: list | None,
    coverage: Any,
) -> list[tuple[str, str, str]]:
    """Return list of (section, label, value) rows for the metrics table."""
    rows: list[tuple[str, str, str]] = []

    # --- Run identity ---
    rows.append(("Run", "Run ID", _esc(meta.get("run_id") or "?")))
    rows.append(("Run", "Harness", _esc(meta.get("harness") or "?")))
    model = meta.get("wva_confirmed_model") or meta.get("model") or "?"
    rows.append(("Run", "Model", _esc(model)))
    rows.append(("Run", "Namespace", _esc(meta.get("namespace") or "?")))
    rows.append(("Run", "Extracted at", _esc(meta.get("extracted_at") or "?")))
    dur = meta.get("load_duration_s")
    rows.append(("Run", "Load duration (s)", _fmt(dur)))
    anchor = meta.get("time_anchor") or {}
    trustworthy = anchor.get("trustworthy")
    rows.append(("Run", "Time anchor trustworthy",
                 "✅ yes" if trustworthy is True else
                 "⚠️ no" if trustworthy is False else "—"))

    # --- Replicas ---
    all_reps: list[dict] = []
    for so in scaled_objects:
        all_reps.extend(so.get("replicas") or [])
    if all_reps:
        ready_vals = [r["ready"] for r in all_reps if r.get("ready") is not None]
        desired_vals = [r["desired"] for r in all_reps if r.get("desired") is not None]
        rows.append(("Replicas", "Max desired", _fmt(max(desired_vals, default=None))))
        rows.append(("Replicas", "Max ready", _fmt(max(ready_vals, default=None))))
        if ready_vals:
            rows.append(("Replicas", "Avg ready", _fmt(sum(ready_vals) / len(ready_vals))))
        # replica-seconds
        all_reps_sorted = sorted(all_reps, key=lambda r: r.get("t", 0))
        repl_s = sum(
            (b["t"] - a["t"]) * (a.get("ready") or 0)
            for a, b in zip(all_reps_sorted, all_reps_sorted[1:])
        )
        rows.append(("Replicas", "Replica-seconds", _fmt(repl_s)))

    # --- EPP / system ---
    epp: list[dict] = []
    if endpoints:
        epp = endpoints[0].get("epp") or []
    if epp:
        q_dispatch_vals = [s["q_dispatch"] for s in epp if s.get("q_dispatch") is not None]
        q_engine_vals = [s["q_engine_sum"] for s in epp if s.get("q_engine_sum") is not None]
        kv_vals = [s["kv_mean"] for s in epp if s.get("kv_mean") is not None]
        in_sys_vals = [s["in_system"] for s in epp if s.get("in_system") is not None]
        if q_dispatch_vals:
            rows.append(("EPP", "Avg queue depth (EPP dispatch)",
                         _fmt(sum(q_dispatch_vals) / len(q_dispatch_vals))))
        if q_engine_vals:
            rows.append(("EPP", "Avg queue depth (engine sum)",
                         _fmt(sum(q_engine_vals) / len(q_engine_vals))))
        if kv_vals:
            rows.append(("EPP", "Avg KV cache utilization",
                         f"{100 * sum(kv_vals) / len(kv_vals):.1f}%"))
            rows.append(("EPP", "Peak KV cache utilization",
                         f"{100 * max(kv_vals):.1f}%"))
        if in_sys_vals:
            rows.append(("EPP", "Peak requests in system (L(t))",
                         _fmt(max(in_sys_vals))))

    # --- Requests ---
    if requests:
        n = len(requests)
        rows.append(("Requests", "Total offered", _fmt(n)))
        n_ok = sum(1 for r in requests if r.get("outcome") in ("ok", "success"))
        n_err = sum(1 for r in requests if r.get("outcome") == "error")
        n_trunc = sum(1 for r in requests if r.get("outcome") == "truncated")
        rows.append(("Requests", "Successful", f"{n_ok} ({100*n_ok/n:.0f}%)"))
        if n_err:
            rows.append(("Requests", "Errors", f"{n_err} ({100*n_err/n:.0f}%)"))
        if n_trunc:
            rows.append(("Requests", "Truncated at run end", _fmt(n_trunc)))

        # TTFT — field is ttft_ms in new schema
        ttft_ms_vals = [r["ttft_ms"] for r in requests if r.get("ttft_ms") is not None]
        if ttft_ms_vals:
            s = sorted(ttft_ms_vals)
            rows.append(("Requests", "P50 TTFT (ms)", _fmt(_pct(s, 0.50))))
            rows.append(("Requests", "P90 TTFT (ms)", _fmt(_pct(s, 0.90))))
            rows.append(("Requests", "P99 TTFT (ms)", _fmt(_pct(s, 0.99))))

        # E2E latency
        e2e_vals = [r["e2e_ms"] for r in requests if r.get("e2e_ms") is not None]
        if e2e_vals:
            s = sorted(e2e_vals)
            rows.append(("Requests", "P50 E2E latency (ms)", _fmt(_pct(s, 0.50))))
            rows.append(("Requests", "P99 E2E latency (ms)", _fmt(_pct(s, 0.99))))

        # ITL
        itl_vals = [r["itl_ms"] for r in requests if r.get("itl_ms") is not None]
        if itl_vals:
            s = sorted(itl_vals)
            rows.append(("Requests", "P50 ITL (ms/token)", _fmt(_pct(s, 0.50))))
            rows.append(("Requests", "P99 ITL (ms/token)", _fmt(_pct(s, 0.99))))

        # output tokens
        out_tok_vals = [r["out_tok"] for r in requests if r.get("out_tok") is not None]
        if out_tok_vals:
            rows.append(("Requests", "Avg output tokens", _fmt(sum(out_tok_vals) / len(out_tok_vals))))

    return rows


def _pct(sorted_vals: list, q: float) -> float | None:
    if not sorted_vals:
        return None
    i = min(len(sorted_vals) - 1, max(0, int(round(q * (len(sorted_vals) - 1)))))
    return sorted_vals[i]


def _norm_coverage_rows(raw: Any) -> tuple[list[dict], list[str]]:
    """Returns (rows, warnings) in a normalised form."""
    if raw is None:
        return [], []
    if isinstance(raw, dict) and "rows" in raw:
        return raw.get("rows", []), raw.get("warnings", [])
    rows_in = raw if isinstance(raw, list) else []
    rows_out = []
    warns = []
    for r in rows_in:
        verdict = r.get("result") or r.get("verdict") or "WARN"
        rows_out.append({"verdict": verdict,
                          "capability": r.get("capability", ""),
                          "detail": r.get("detail", "")})
        if r.get("detail"):
            warns.append(f"{r.get('capability', '')} — {r.get('detail', '')}")
    return rows_out, warns


# --------------------------------------------------------------------------- #
# HTML rendering
# --------------------------------------------------------------------------- #

CSS = """
:root{--fg:#1f2328;--muted:#57606a;--line:#e5e7eb;--accent:#3b82d4;--bg:#fff;--surf:#f7f8fa;}
*{box-sizing:border-box;}
body{margin:0;font:14px/1.6 -apple-system,"Segoe UI",system-ui,sans-serif;color:var(--fg);background:var(--bg);}
header{padding:20px 28px 14px;border-bottom:2px solid var(--line);}
header h1{margin:0 0 4px;font-size:18px;font-weight:700;}
header p{margin:0;color:var(--muted);font-size:13px;}
main{padding:24px 28px 60px;max-width:1100px;}
h2{font-size:15px;font-weight:700;margin:28px 0 8px;color:var(--fg);border-bottom:1px solid var(--line);padding-bottom:4px;}
table{border-collapse:collapse;font-size:13px;width:100%;}
th,td{border:1px solid var(--line);padding:5px 12px;text-align:left;}
th{background:var(--surf);font-weight:600;white-space:nowrap;}
tr.section-hdr td{background:#eef2ff;color:#3730a3;font-weight:700;font-size:12px;text-transform:uppercase;letter-spacing:.04em;}
td.val{text-align:right;font-variant-numeric:tabular-nums;}
.pass{color:#15803d;font-weight:600;}
.fail{color:#b91c1c;font-weight:600;}
.warn{color:#b45309;font-weight:600;}
.caveats{background:#fff7ed;border:1px solid #fed7aa;border-radius:6px;padding:10px 14px;margin:0 0 20px;font-size:13px;color:#92400e;}
.caveats strong{display:block;margin-bottom:4px;}
.panels-wrap{margin:0 0 24px;}
.panels-wrap img{max-width:100%;height:auto;display:block;border:1px solid var(--line);border-radius:6px;}
.panels-missing{background:var(--surf);border:1px solid var(--line);border-radius:6px;padding:24px;text-align:center;color:var(--muted);font-size:13px;}
footer{margin-top:40px;padding-top:12px;border-top:1px solid var(--line);font-size:12px;color:var(--muted);text-align:center;}
"""

def _render_metrics_table(rows: list[tuple[str, str, str]]) -> str:
    if not rows:
        return "<p><em>No metrics available.</em></p>"
    html = ['<table>',
            '<thead><tr><th>Metric</th><th class="val">Value</th></tr></thead>',
            '<tbody>']
    cur_section = None
    for section, label, value in rows:
        if section != cur_section:
            cur_section = section
            html.append(f'<tr class="section-hdr"><td colspan="2">{_esc(section)}</td></tr>')
        html.append(f'<tr><td>{_esc(label)}</td><td class="val">{value}</td></tr>')
    html.append('</tbody></table>')
    return "\n".join(html)


def _verdict_cls(verdict: str) -> str:
    v = verdict.upper()
    if v == "PASS":
        return "pass"
    if v == "FAIL":
        return "fail"
    return "warn"


def _render_coverage_table(cov_rows: list[dict]) -> str:
    if not cov_rows:
        return "<p><em>No coverage data.</em></p>"
    html = ['<table>',
            '<thead><tr><th>Capability</th><th>Result</th><th>Detail</th></tr></thead>',
            '<tbody>']
    for r in cov_rows:
        verdict = r.get("verdict") or r.get("result") or "WARN"
        cls = _verdict_cls(verdict)
        cap = _esc(r.get("capability") or "")
        detail = _esc(r.get("detail") or "")
        html.append(f'<tr><td>{cap}</td>'
                    f'<td><span class="{cls}">{_esc(verdict)}</span></td>'
                    f'<td style="font-size:12px;color:#57606a">{detail}</td></tr>')
    html.append('</tbody></table>')
    return "\n".join(html)


def render_report(
    bundle_dir: Path,
    meta: dict,
    endpoints: list,
    scaled_objects: list,
    requests: list | None,
    coverage_raw: Any,
    provenance: dict | None,
    panels_uri: str | None,
    render_sha: str,
) -> str:
    run_id = meta.get("run_id") or meta.get("run") or "?"
    model = meta.get("wva_confirmed_model") or meta.get("model") or "?"
    harness = meta.get("harness") or "?"
    ns = meta.get("namespace") or "?"

    anchor = meta.get("time_anchor") or {}
    weak = anchor.get("trustworthy") is False

    cov_rows, cov_warns = _norm_coverage_rows(coverage_raw)
    metric_rows = _extract_metrics(meta, endpoints, scaled_objects, requests, coverage_raw)

    extractor_ver = (meta.get("extractor_version") or
                     (provenance or {}).get("extractor_version") or "?")

    # caveats block
    caveats_html = ""
    caveat_lines = []
    if weak:
        caveat_lines.append("⚠️ <strong>WEAK TIME ANCHOR</strong> — arrival-time panels are unreliable.")
    for w in cov_warns:
        caveat_lines.append(_esc(w))
    fails = [r.get("capability", "") for r in cov_rows if r.get("verdict") == "FAIL"]
    if fails:
        caveat_lines.append("Not exercised by this run: " + ", ".join(_esc(f) for f in fails))
    if caveat_lines:
        inner = "<br>".join(caveat_lines)
        caveats_html = f'<div class="caveats"><strong>Caveats</strong>{inner}</div>'

    # panels
    if panels_uri:
        panels_html = f'<div class="panels-wrap"><img src="{panels_uri}" alt="panels"></div>'
    else:
        panels_html = ('<div class="panels-missing">'
                       'panels.png not found — run render_real_trace.py first'
                       '</div>')

    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{_esc(run_id)} — benchmark report</title>
<style>{CSS}</style>
</head>
<body>
<header>
  <h1>{_esc(run_id)}</h1>
  <p>{_esc(model)} &nbsp;·&nbsp; {_esc(harness)} &nbsp;·&nbsp; ns={_esc(ns)}</p>
</header>
<main>
{caveats_html}
<h2>Panels</h2>
{panels_html}
<h2>Key metrics</h2>
{_render_metrics_table(metric_rows)}
<h2>Coverage</h2>
{_render_coverage_table(cov_rows)}
</main>
<footer>rendered @ {_esc(render_sha)} &nbsp;·&nbsp; bundle extracted @ {_esc(extractor_ver)} &nbsp;·&nbsp; Made with IBM Bob</footer>
</body>
</html>"""


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #

def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle-dir", required=True, help="Path to bundle directory")
    parser.add_argument("--out", help="Output HTML path (default: report.html inside bundle dir)")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    bundle_dir = Path(args.bundle_dir).resolve()

    meta = _load_json(bundle_dir / "meta.json")
    ep_raw = _load_json(bundle_dir / "endpoints.json")
    endpoints = ep_raw if isinstance(ep_raw, list) else ep_raw.get("endpoints", [])
    so_raw = _load_json(bundle_dir / "scaled_objects.json")
    scaled_objects = so_raw if isinstance(so_raw, list) else so_raw.get("scaled_objects", [])
    coverage_raw = _load_optional(bundle_dir / "coverage.json")
    provenance = _load_optional(bundle_dir / "provenance.json")
    req_raw = _load_optional(bundle_dir / "requests.json")
    requests: list | None = None
    if req_raw is not None:
        requests = req_raw if isinstance(req_raw, list) else req_raw.get("requests", [])

    panels_uri = _png_data_uri(bundle_dir / "panels.png")
    render_sha = _git_sha()

    html = render_report(
        bundle_dir=bundle_dir,
        meta=meta,
        endpoints=endpoints,
        scaled_objects=scaled_objects,
        requests=requests,
        coverage_raw=coverage_raw,
        provenance=provenance,
        panels_uri=panels_uri,
        render_sha=render_sha,
    )

    out_path = Path(args.out) if args.out else bundle_dir / "report.html"
    out_path.write_text(html, encoding="utf-8")
    print(f"wrote {out_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
