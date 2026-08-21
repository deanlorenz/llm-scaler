#!/usr/bin/env python3
"""
analyze_wva_decisions.py — flag candidate cost-inefficiency in a multi-variant
WVA decision table.

Design sketched in docs/plans/benchmark/observability-gaps.md §3, validated
against the first two real multi-variant runs (§7) before being built: across
259 dual-variant cycles from a real burst run, zero cycles showed the pattern
this flags. Not a hard gate -- an analysis aid for a human reading a
multi-variant run, in the same PASS/WARN `Report` style as
preflight_shared_cluster.py.

What it checks
--------------
Per saturation-V2 design, the optimizer should prefer scaling up the variant
with the best serving-capacity-per-unit-cost first, not simply the cheapest,
and should recruit a second variant only once the first's headroom is
exhausted. A cycle is flagged when a LESS efficient variant is growing
(`decision_action` scale-up) while a MORE efficient variant in the same
cycle sits idle (`decision_action` no-change) with room to grow (its
current replica count is below its own `--max-replicas`, when known).

Efficiency is `analyzer_prc` (per-replica capacity, already in the decision
table) divided by the variant's cost. Cost is not in the decision table
(WVA logs it once, at ScaledObject-registration time, not per cycle) --
supply it with `--variant-cost NAME=VALUE`. Without costs supplied, this
falls back to comparing raw `analyzer_prc` (capacity only, not cost-adjusted)
and says so loudly: that degraded mode cannot tell "cheap and roomy" from
"expensive and roomy" apart, only "more room" from "less room".

Input
-----
  <run>/metrics/processed/wva_decision_table.json, from
  `make benchmark-decision-table RUN_DIR=<run>`.

Usage
-----
  python3 hack/benchmark/analyze_wva_decisions.py --run <run-dir> \
      --variant-cost <primary-scaledobject-name>=10.0 \
      --variant-cost <secondary-scaledobject-name>=5.0 \
      --max-replicas <primary-scaledobject-name>=10 \
      --max-replicas <secondary-scaledobject-name>=10

Exit status: always 0. This is an analysis aid, not a CI gate.
"""
import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path


class Report:
    def __init__(self):
        self.rows = []

    def add(self, status, check, detail):
        self.rows.append((status, check, detail))

    def render(self):
        width = max((len(c) for _, c, _ in self.rows), default=0)
        marker = {"PASS": "  ok  ", "WARN": " warn ", "NOTE": " note "}
        for status, check, detail in self.rows:
            print(f"[{marker[status]}] {check.ljust(width)}  {detail}")


def parse_kv_float(pairs):
    out = {}
    for p in pairs or []:
        if "=" not in p:
            print(f"ERROR: expected NAME=VALUE, got {p!r}", file=sys.stderr)
            sys.exit(1)
        name, value = p.split("=", 1)
        out[name] = float(value)
    return out


def load_rows(run_dir):
    # The original run directory keeps it under metrics/processed/; a
    # published bundle (publish_viz_result.sh) stages it flat at the top
    # level instead. Accept either.
    candidates = [
        Path(run_dir) / "metrics" / "processed" / "wva_decision_table.json",
        Path(run_dir) / "wva_decision_table.json",
    ]
    for path in candidates:
        if path.is_file():
            return json.loads(path.read_text())
    print(f"ERROR: no wva_decision_table.json under {run_dir} (checked "
          f"{', '.join(str(p) for p in candidates)}). Run "
          f"'make benchmark-decision-table RUN_DIR={run_dir}' first.",
          file=sys.stderr)
    sys.exit(1)


def group_by_cycle(rows):
    cycles = defaultdict(list)
    for r in rows:
        cycles[r["t"]].append(r)
    return cycles


def efficiency(row, costs, degraded):
    prc = row.get("analyzer_prc")
    if prc is None:
        return None
    if degraded:
        return prc
    cost = costs.get(row["variant"])
    if not cost:
        return None
    return prc / cost


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                  formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--run", required=True, help="run directory")
    ap.add_argument("--variant-cost", action="append",
                     help="NAME=VALUE, repeatable. NAME is the ScaledObject name "
                          "(the decision table's 'variant' field).")
    ap.add_argument("--max-replicas", action="append",
                     help="NAME=VALUE, repeatable. Lets the tool exclude a cycle "
                          "where the idle variant is already at its own ceiling.")
    args = ap.parse_args()

    costs = parse_kv_float(args.variant_cost)
    max_reps = parse_kv_float(args.max_replicas)
    rows = load_rows(args.run)
    cycles = group_by_cycle(rows)

    rep = Report()
    variants_seen = {r["variant"] for r in rows}
    degraded = not costs
    if degraded:
        rep.add("NOTE", "cost data",
                "no --variant-cost given; comparing raw per-replica capacity "
                "only (not cost-adjusted) -- cannot tell cheap-and-roomy from "
                "expensive-and-roomy apart")
    missing_cost = variants_seen - set(costs) if not degraded else set()
    if missing_cost:
        rep.add("NOTE", "cost data",
                f"no cost given for: {', '.join(sorted(missing_cost))} -- "
                f"cycles involving them are skipped")

    if len(variants_seen) < 2:
        rep.add("NOTE", "variant comparison",
                f"only one variant present ({', '.join(variants_seen) or 'none'}) "
                f"-- nothing to compare, this run cannot exercise this check")
        rep.render()
        return

    flagged = 0
    checked = 0
    for t in sorted(cycles):
        crows = [r for r in cycles[t] if r["variant"] in variants_seen]
        if len(crows) < 2:
            continue
        crows = [r for r in crows if r["variant"] not in missing_cost]
        if len(crows) < 2:
            continue
        checked += 1
        growing = [r for r in crows if (r.get("decision_action") or "").startswith("scale-up")]
        idle = [r for r in crows if r.get("decision_action") == "no-change"]
        for g in growing:
            eff_g = efficiency(g, costs, degraded)
            if eff_g is None:
                continue
            for i in idle:
                if i["variant"] == g["variant"]:
                    continue
                eff_i = efficiency(i, costs, degraded)
                if eff_i is None or eff_i <= eff_g:
                    continue
                cap = max_reps.get(i["variant"])
                curr = i.get("decision_curr")
                if cap is not None and curr is not None and curr >= cap:
                    continue  # idle variant is already at its own ceiling
                flagged += 1
                label = "capacity" if degraded else "efficiency"
                rep.add("WARN", f"t={t:.0f}",
                        f"{g['variant']} scaling up ({label}={eff_g:.3g}) while "
                        f"{i['variant']} sits idle at curr={curr} "
                        f"({label}={eff_i:.3g}, more {label})")

    if flagged == 0:
        rep.add("PASS", "cost-inefficiency scan",
                f"{checked} dual-(or-more)-variant cycle(s) checked, 0 "
                f"candidate(s) of a less-{('capable' if degraded else 'efficient')} "
                f"variant scaling while a more-{('capable' if degraded else 'efficient')} "
                f"one sits idle with room to grow")
    rep.render()


if __name__ == "__main__":
    main()
