#!/usr/bin/env python3
"""staircase_analyze.py -- what a staircase run measured, against what was frozen.

  staircase_analyze.py steps  <run dir> <phase> [--discard 180]
  staircase_analyze.py report <run dir> <frozen.json> <spec.json> [--redo frozen-at-measured-ca2.json]

steps writes phases/<phase>/analysis.jsonl, one row a step. Client figures are
over requests DUE in the step's judged window: its hold less the discard, cut
at the moment the driver stopped. A failed or unfinished request counts as
infinitely slow. Router figures are the delta of the EPP's cumulative histogram
across the window: the raw scrape when there is one, else Prometheus rates
integrated over it. Every row carries stopped_by, the rule that ended the phase
on that step, "" on a step no rule ended.

report writes report.md into the run directory. The client is the witness: the
mark is read from it, at the operating point only. The router is a cross-check
and never decides a mark. The knee is reported apart, with no mark. A rate is
compared over the median of a bucket's repeats, and a verdict stands for the
bucket only if every repeat agrees.

Standard library only.
"""
import argparse
import collections
import csv
import gzip
import json
import math
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import staircase_lib as lib  # noqa: E402

TTFT = "llm_d_epp_request_ttft_seconds"
TPOT = "llm_d_epp_request_streaming_tpot_seconds"
PROM_STEP = 15
PHASE = re.compile(r"^(.+)-(on|off)(?:-r(\d+))?$")
INF = float("inf")


def read_lines(path):
    try:
        with open(path, encoding="utf8", errors="replace") as f:
            return f.readlines()
    except FileNotFoundError:
        return []


def read_requests(path):
    """The driver's rows, old format or new. The header repeats: every process writes one."""
    rows = []
    with gzip.open(path, "rt", encoding="utf8", errors="replace") as f:
        for r in csv.reader(f):
            if r and r[0] != "index" and len(r) >= 9:
                rows.append(r)
    return rows


def read_pod_map(path):
    """{request index: pod} from an optional index,pod CSV: a log join the driver cannot make."""
    out = {}
    for line in read_lines(path):
        parts = line.strip().split(",")
        if len(parts) == 2 and parts[0].isdigit():
            out[parts[0]] = parts[1]
    return out


def read_raw_epp(path):
    """The raw EPP scrape: {name: {t: {le or "": value}}}, summed over label sets."""
    raw = collections.defaultdict(lambda: collections.defaultdict(lambda: collections.defaultdict(float)))
    for line in read_lines(path):
        p = line.split()
        if len(p) < 4:
            continue
        name = p[2].split("{")[0]
        if not (name.startswith(TTFT) or name.startswith(TPOT)):
            continue
        m = re.search(r'le="([^"]*)"', p[2])
        raw[name][lib.ts(p[0])][m[1] if m else ""] += float(p[-1])
    return raw


def read_prom(path):
    out = {}
    for line in read_lines(path):
        o = json.loads(line)
        if o.get("data"):
            out[o["name"]] = o["data"]["result"]
    return out


def read_engine(path):
    """{role: {pod: {sum or count: [(t, value)]}}} of the engines' TTFT counters."""
    out = {"decode": {}, "prefill": {}}
    for line in read_lines(path):
        p = line.split()
        if len(p) < 4 or "time_to_first_token_seconds_" not in p[2]:
            continue
        role = "decode" if "-decode-" in p[1] else "prefill" if "-prefill-" in p[1] else None
        if role:
            kind = "sum" if "_sum" in p[2] else "count"
            out[role].setdefault(p[1], {"sum": [], "count": []})[kind].append((lib.ts(p[0]), float(p[-1])))
    return out


def counter_delta(series, a, b):
    """The delta between the last samples at or before a and b; None without both."""
    xa = [v for t, v in series if t <= a]
    xb = [v for t, v in series if t <= b]
    if not xa or not xb or xb[-1] < xa[-1]:
        return None
    return xb[-1] - xa[-1]


def engine_mean(pods, a, b):
    """A role's mean TTFT over the window, in seconds; None with no request counted."""
    total = count = 0.0
    for series in pods.values():
        s, c = counter_delta(series["sum"], a, b), counter_delta(series["count"], a, b)
        if s is not None and c is not None:
            total, count = total + s, count + c
    return total / count if count > 0 else None


def raw_delta(raw, base, a, b):
    """The delta of base's _bucket, _sum and _count across the window; None if unreadable."""
    bk = raw.get(base + "_bucket")
    if not bk:
        return None
    stamps = sorted(bk)
    ta = [t for t in stamps if t <= a]
    tb = [t for t in stamps if t <= b]
    if not ta or not tb or tb[-1] - ta[-1] < 0.8 * (b - a) or b - tb[-1] > 60:
        return None
    x, y = bk[ta[-1]], bk[tb[-1]]
    if any(y.get(k, 0) < v for k, v in x.items()):  # a counter went backwards: an EPP restart
        return None
    out = {"cum": sorted((INF if k == "+Inf" else float(k), y[k] - x.get(k, 0)) for k in y), "src": "raw"}
    for sfx in ("_sum", "_count"):
        s = raw.get(base + sfx)
        if s and ta[-1] in s and tb[-1] in s:
            out[sfx] = s[tb[-1]][""] - s[ta[-1]][""]
    return out


def prom_delta(prom, key, a, b):
    """A rate series integrated over the window, by bucket; None with no sample."""
    acc = collections.defaultdict(float)
    for s in prom.get(key, []):
        le = s["metric"].get("le")
        if le is None:
            continue
        for t, v in s["values"]:
            if a <= float(t) < b and v != "NaN":
                acc[INF if le == "+Inf" else float(le)] += float(v) * PROM_STEP
    return {"cum": sorted(acc.items()), "src": "prom"} if acc else None


def series_mean(prom, key, a, b):
    """{label values: (mean, peak)} over the window."""
    out = {}
    for s in prom.get(key, []):
        vals = [float(v) for t, v in s["values"] if a <= float(t) < b and v != "NaN"]
        if vals:
            out[" ".join(str(v) for v in s["metric"].values()) or "all"] = (sum(vals) / len(vals), max(vals))
    return out


def prom_total(prom, key, a, b):
    """A rate summed over its series, integrated over the window."""
    return sum(float(v) * PROM_STEP for s in prom.get(key, []) for t, v in s["values"]
               if a <= float(t) < b and v != "NaN")


def client_figures(win, span, pods=None):
    """The figures of one window's rows. A row is ok on 200, a first token and no error."""
    ok = [r for r in win if r[6] == "200" and float(r[4]) > 0 and (len(r) < 10 or r[9] == "")]
    missing = [INF] * (len(win) - len(ok))
    ttft = sorted(float(r[4]) - float(r[2]) for r in ok) + missing
    sent_ttft = sorted(float(r[4]) - float(r[3]) for r in ok) + missing
    tpot = sorted((float(r[5]) - float(r[4])) / (int(r[8]) - 1) for r in ok if int(r[8]) > 1)
    lag = sorted(float(r[3]) - float(r[2]) for r in win)
    prompts = [int(r[7]) for r in ok if int(r[7]) > 0]
    mean_p = sum(prompts) / len(prompts) if prompts else math.nan
    spread = math.sqrt(lib.scv(prompts)) if len(prompts) > 1 else math.nan
    pairs = [((len(r) > 10 and r[10]) or (pods or {}).get(r[0], ""), float(r[3])) for r in win]
    by_pod = lib.ca2_by_pod(pairs)
    return dict(due=len(win), failed=len(win) - len(ok), completed=len(ok), achieved=len(win) / span,
                ca2_sent=lib.arrival_ca2([float(r[3]) for r in win]), ca2_by_pod=by_pod or None,
                lag_p99=lib.pct(lag, 0.99), prompt_mean=mean_p, prompt_cv=spread,
                client_ttft_p50=lib.pct(ttft, .5), client_ttft_p90=lib.pct(ttft, .9),
                client_ttft_p95=lib.pct(ttft, .95), client_sent_ttft_p90=lib.pct(sent_ttft, .9),
                client_sent_ttft_p95=lib.pct(sent_ttft, .95),
                client_ttft_mean=sum(t for t in ttft if t != INF) / len(ok) if ok else math.nan,
                client_tpot_mean=sum(tpot) / len(tpot) if tpot else math.nan,
                client_tpot_p90=lib.pct(tpot, .9), client_tpot_p95=lib.pct(tpot, .95))


def router_figures(row, raw, prom, a, b):
    """The router's histogram quantiles into row: raw scrape first, Prometheus else."""
    for key, base, prom_key in (("router_ttft", TTFT, "ttft_bucket"), ("router_tpot", TPOT, "tpot_bucket")):
        h = raw_delta(raw, base, a, b) or prom_delta(prom, prom_key, a, b)
        if h:
            row[key + "_src"], row[key + "_n"] = h["src"], h["cum"][-1][1]
            for q in (.5, .9, .95):
                v, lo, hi = lib.hist_quantile(h["cum"], q)
                row["%s_p%d" % (key, q * 100)] = v
                row["%s_p%d_lo" % (key, q * 100)], row["%s_p%d_hi" % (key, q * 100)] = lo, hi
            if h.get("_count"):
                row[key + "_mean"] = h["_sum"] / h["_count"]
        count = prom_total(prom, prom_key.replace("_bucket", "_count"), a, b)
        if count and key + "_mean" not in row:
            row[key + "_mean"] = prom_total(prom, prom_key.replace("_bucket", "_sum"), a, b) / count


def analyze_steps(run, phase, discard):
    """One row a judged step, stop rule on each."""
    d = os.path.join(run, "phases", phase)
    levels, stop = lib.parse_log(read_lines(d + "/sweep.log"))
    rows = read_requests(d + "/requests.csv.gz")
    pods = read_pod_map(d + "/pod-map.csv")
    raw, prom = read_raw_epp(run + "/epp-metrics.log"), read_prom(d + "/prom.jsonl")
    engine = read_engine(run + "/vllm-metrics.log")
    out = []
    for level in levels:
        a, b = lib.judged_window(level, stop, discard)
        span = b - a
        win = [r for r in rows if a <= float(r[2]) < b]
        if not win or span <= 0:
            continue
        row = dict(phase=phase, level=level["n"], rate=level["rate"], ca2_asked=level["ca2"], window_s=span,
                   hold_s=level["hold"], truncated=span < level["hold"] - discard,
                   stopped_by=stop["rule"] if stop and stop["level"] == level["n"] else "")
        row.update(client_figures(win, span, pods))
        row["completion_rate"] = sum(1 for r in rows if r[6] == "200" and a <= float(r[5]) < b) / span
        router_figures(row, raw, prom, a, b)
        pre, dec = engine_mean(engine["prefill"], a, b), engine_mean(engine["decode"], a, b)
        if pre is not None and dec is not None and row["client_ttft_mean"] == row["client_ttft_mean"]:
            row["engine_prefill_mean"], row["engine_decode_mean"] = pre, dec
            row["hop_mean"] = row["client_ttft_mean"] - pre - dec
        row["fc_saturation"] = series_mean(prom, "fc_saturation", a, b)
        row["fc_queue"] = series_mean(prom, "fc_queue", a, b)
        row["waiting"] = series_mean(prom, "waiting", a, b)
        row["kv"] = series_mean(prom, "kv", a, b)
        half = (a + b) / 2
        row["drift_p90"] = drift(win, half)
        out.append(row)
    return out, stop


def drift(win, half):
    """Client p90 in the window's second half over its first: a step still moving reads far from 1."""
    def p90(part):
        return lib.pct(sorted(float(r[4]) - float(r[2]) if r[6] == "200" and float(r[4]) > 0 else INF for r in part), .9)
    first, second = p90([r for r in win if float(r[2]) < half]), p90([r for r in win if float(r[2]) >= half])
    if not first > 0 or first == INF or second == INF or second != second:
        return math.nan
    return second / first


def load_steps(run, phase):
    with open(os.path.join(run, "phases", phase, "analysis.jsonl"), encoding="utf8") as f:
        rows = [json.loads(line) for line in f]
    return sorted(rows, key=lambda r: r["level"])


def flag_noise(rows, bucket, prompt_cv):
    """Set noise_flag and need_s on each row. A flagged crossing is a bound."""
    for r in rows:
        need, rho_p, rho_d = lib.noise_floor(r["rate"], bucket, prompt_cv, r["ca2_sent"] if bucket["ca2"] != 1 else 1.0)
        r["need_s"], r["rho_prefill"], r["rho_decode"] = need, rho_p, rho_d
        r["noise_flag"] = r["window_s"] < need


def phases_of(run):
    """{(bucket, fc): {repeat: phase}} of the phases analyzed."""
    out = collections.defaultdict(dict)
    root = os.path.join(run, "phases")
    for name in sorted(os.listdir(root)):
        m = PHASE.match(name)
        if m and os.path.exists(os.path.join(root, name, "analysis.jsonl")):
            out[m[1], m[2]][int(m[3] or 1)] = name
    return out


def fail(verdict):
    return verdict.startswith("FAIL")


def judge(predicted, per_repeat):
    """(median measured text, ratio, verdict) over the repeats' measured() results.

    A bucket passes only if every repeat does. A repeat with no figure of its own
    leaves the bucket with no mark. The median is of the figures that exist.
    """
    verdicts = [lib.mark(predicted, k, v)[1] for k, v in per_repeat]
    values = [(v[0] + v[1]) / 2 if k == "bound" else v for k, v in per_repeat if v is not None]
    centre = lib.median(values) if values else None
    if any(fail(v) for v in verdicts):
        verdict = "FAIL"
    elif all(v.startswith("in the mark") for v in verdicts):
        verdict = "in the mark"
    else:
        verdict = "no mark"
    if len(per_repeat) == 1:
        kind, value = per_repeat[0]
        return lib.show(kind, value), lib.mark(predicted, kind, value)[0], lib.mark(predicted, kind, value)[1]
    ratio = "%.2f" % (predicted / centre) if centre else "n/a"
    return "median %.1f of %d" % (centre, len(values)) if centre else "n/a", ratio, verdict


def falsifiers(data, frozen, spec, out):
    """The falsifiers set before the run: each says whether it held."""
    bands = spec.get("falsifiers", {})
    print("\n## Falsifiers\n", file=out)
    print("| Falsifier | Reading | Band | Held |", file=out)
    print("| --- | --- | --- | --- |", file=out)
    results = []

    def line(name, value, band):
        ok = lib.in_band(value, band)
        results.append(ok)
        print("| %s | %.2f | %g to %g | %s |" % (name, value, band[0], band[1], "yes" if ok else "NO"), file=out)

    hops = {}
    for (bucket, fc), repeats in sorted(data.items()):
        rows = [r for phase_rows in repeats.values() for r in phase_rows
                if "engine_decode_mean" in r and not r["truncated"] and r["failed"] == 0]
        if len(rows) >= 3 and "itlSlope" in bands:
            line("ITL slope, %s %s" % (bucket, fc), lib.ols_slope([r["client_tpot_mean"] for r in rows],
                                                                  [r["engine_decode_mean"] for r in rows]), bands["itlSlope"])
        low = [r["hop_mean"] for r in rows if r["rate"] <= frozen[bucket]["operatingRate"]]
        if low:
            hops.setdefault(frozen[bucket]["prompt"], []).extend(low)
    if len(hops) >= 2 and "hopPerKiloToken" in bands:
        xs = sorted(hops)
        slope = lib.ols_slope([x / 1000 for x in xs], [1000 * lib.median(hops[x]) for x in xs])
        line("Hop, ms per 1000 prompt tokens, %d lengths" % len(xs), slope, bands["hopPerKiloToken"])
    if "burstRatio" in bands:
        for fc in ("off", "on"):
            b, c = data.get(("B", fc)), data.get(("C", fc))
            if not b or not c:
                continue
            for repeat in sorted(set(b) & set(c)):
                ratios = [rc["client_ttft_p90"] / rb["client_ttft_p90"] for rb, rc in zip(b[repeat], c[repeat])
                          if rb["level"] == rc["level"] and not rb["truncated"] and not rc["truncated"]
                          and rc["client_ttft_p90"] != INF and rb["client_ttft_p90"] != INF]
                if ratios:
                    line("C over B, client p90, flow control %s, repeat %d, median of %d steps" % (fc, repeat, len(ratios)),
                         lib.median(ratios), bands["burstRatio"])
    if not results:
        print("| none readable | | | |", file=out)
    return results


def report(run, frozen_file, spec, redo_file, out):
    """The report; returns the verdicts by (bucket, fc, reading), for the selftest."""
    frozen = {b["name"]: b for b in frozen_file["buckets"]}
    redo = {b["name"]: b for b in redo_file["buckets"]} if redo_file else {}
    ratio_q = {r["percentile"]: r.get("value") for r in frozen_file.get("tpotRatio", [])}
    data = {}
    for (bucket, fc), repeats in phases_of(run).items():
        if bucket not in frozen:
            continue
        data[bucket, fc] = {n: load_steps(run, name) for n, name in repeats.items()}
        for rows in data[bucket, fc].values():
            flag_noise(rows, frozen[bucket], spec["promptCV"])

    print("# Staircase report\n", file=out)
    print("The client is the witness and the router a cross-check. The mark is at the operating point; "
          "the knee has none.\n", file=out)
    print("## How each phase ended\n", file=out)
    print("| Phase | Steps | Stopped by | On step | Window judged, s | Truncated | Noise-flagged steps |", file=out)
    print("| --- | --- | --- | --- | --- | --- | --- |", file=out)
    for (bucket, fc), repeats in sorted(data.items()):
        for n, rows in sorted(repeats.items()):
            stopped = [r for r in rows if r["stopped_by"]]
            last = stopped[0] if stopped else None
            print("| %s %s r%d | %d | %s | %s | %s | %s | %s |" % (
                bucket, fc, n, len(rows), last["stopped_by"] if last else "no rule", last["level"] if last else "",
                "%d" % last["window_s"] if last else "", "yes" if last and last["truncated"] else "no",
                ",".join(str(r["level"]) for r in rows if r["noise_flag"]) or "none"), file=out)

    print("\n## TTFT at the operating point\n", file=out)
    print("| Bucket | Flow control | q | Target | Predicted r/s | Measured r/s, client | Ratio | Mark | Router, cross-check |", file=out)
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- |", file=out)
    verdicts = {}
    for (bucket, fc), repeats in sorted(data.items()):
        b = redo.get(bucket, frozen[bucket])
        for t in b["targets"]:
            q = int(t["percentile"])
            predicted = t["predictedOff"] if fc == "off" else t["predictedOn"]
            client = [lib.measured(rows, "client_ttft_p%d" % q, t["seconds"]) for _, rows in sorted(repeats.items())]
            router = [lib.measured(rows, "router_ttft_p%d" % q, t["seconds"]) for _, rows in sorted(repeats.items())]
            text, ratio, verdict = judge(predicted, client)
            rtext, rratio, _ = judge(predicted, router)
            verdicts[bucket, fc, "ttft", q] = verdict
            print("| %s | %s | p%d | %.1f ms | %.1f | %s | %s | %s | %s, %s |" % (
                bucket, fc, q, t["seconds"] * 1000, predicted, text, ratio, verdict, rtext, rratio), file=out)

    print("\n## TPOT at the operating point\n", file=out)
    print("| Bucket | Flow control | Reading | Target | Predicted r/s | Measured r/s, client | Ratio | Mark | Router, cross-check |", file=out)
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- |", file=out)
    for (bucket, fc), repeats in sorted(data.items()):
        b = frozen[bucket]
        readings = [("mean", "router_tpot_mean", "client_tpot_mean", b["meanTPOT"])]
        for q in (90, 95):
            if ratio_q.get(q):
                readings.append(("p%d" % q, "router_tpot_p%d" % q, "client_tpot_p%d" % q, b["meanTPOT"] * ratio_q[q]))
        for name, router_key, client_key, target in readings:
            if not target > 0:
                continue
            client = [lib.measured(rows, client_key, target) for _, rows in sorted(repeats.items())]
            router = [lib.measured(rows, router_key, target) for _, rows in sorted(repeats.items())]
            text, ratio, verdict = judge(b["operatingRate"], client)
            rtext, rratio, _ = judge(b["operatingRate"], router)
            verdicts[bucket, fc, "tpot", name] = verdict
            print("| %s | %s | %s | %.1f ms | %.1f | %s | %s | %s | %s, %s |" % (
                bucket, fc, name, target * 1000, b["operatingRate"], text, ratio, verdict, rtext, rratio), file=out)

    print("\n## The knee, reported apart\n", file=out)
    print("A step held if completions in its window came within 3% of what was sent and nothing failed. No mark applies.\n", file=out)
    print("| Bucket | Flow control | Repeat | Predicted knee | Last held | First not held |", file=out)
    print("| --- | --- | --- | --- | --- | --- |", file=out)
    for (bucket, fc), repeats in sorted(data.items()):
        for n, rows in sorted(repeats.items()):
            low, high = lib.knee_bracket(rows)
            print("| %s | %s | %d | %.1f | %s | %s |" % (bucket, fc, n, frozen[bucket]["knee"],
                                                          "none" if low is None else "%.1f" % low,
                                                          "none" if high is None else "%.1f" % high), file=out)

    print("\n## The onset, flow control on\n", file=out)
    print("| Bucket | Repeat | Predicted prefill onset | Lowest step above it | Saturation there | Router queue, peak | Window over 4T | Testable |", file=out)
    print("| --- | --- | --- | --- | --- | --- | --- | --- |", file=out)
    for (bucket, fc), repeats in sorted(data.items()):
        onset = frozen[bucket]["onsets"]["Prefill"]
        if fc != "on" or not onset:
            continue
        for n, rows in sorted(repeats.items()):
            above = [r for r in rows if r["rate"] > onset and not r["truncated"]]
            if not above:
                print("| %s | %d | %.1f | none | | | | no: no step above it |" % (bucket, n, onset), file=out)
                continue
            r = above[0]
            saturation = max((v[0] for v in r["fc_saturation"].values()), default=math.nan)
            queue = max((v[1] for v in r["fc_queue"].values()), default=math.nan)
            print("| %s | %d | %.1f | %.1f | %.2f | %.0f | %.2f | %s |" % (
                bucket, n, onset, r["rate"], saturation, queue, r["window_s"] / r["need_s"],
                "no: under the noise floor" if r["noise_flag"] else "yes"), file=out)

    print("\n## Model against client, per step\n", file=out)
    print("Report only: flow control off, the frozen curve at the step's rate.\n", file=out)
    print("| Bucket | Flow control | Repeat | Step, r/s | Client mean, ms | Model | Client p90, ms | Model | Error, p90 | Hop, ms |", file=out)
    print("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |", file=out)
    for (bucket, fc), repeats in sorted(data.items()):
        for n, rows in sorted(repeats.items()):
            for r in rows:
                point = next((p for p in frozen[bucket]["curve"] if p.get("p90") and abs(p["rate"] / r["rate"] - 1) < 0.005), None)
                if not point or r["client_ttft_p90"] == INF:
                    continue
                print("| %s | %s | %d | %.1f | %.1f | %.1f | %.1f | %.1f | %+.0f%% | %s |" % (
                    bucket, fc, n, r["rate"], 1000 * r["client_ttft_mean"], 1000 * point["mean"], 1000 * r["client_ttft_p90"],
                    1000 * point["p90"], 100 * (point["p90"] / r["client_ttft_p90"] - 1),
                    "%.1f" % (1000 * r["hop_mean"]) if "hop_mean" in r else ""), file=out)

    print("\n## Per-pod Ca2\n", file=out)
    print("| Bucket | Flow control | Repeat | Step | Sent Ca2 | By pod |", file=out)
    print("| --- | --- | --- | --- | --- | --- |", file=out)
    seen = False
    for (bucket, fc), repeats in sorted(data.items()):
        for n, rows in sorted(repeats.items()):
            for r in rows:
                if r.get("ca2_by_pod"):
                    seen = True
                    print("| %s | %s | %d | %d | %.2f | %s |" % (bucket, fc, n, r["level"], r["ca2_sent"], " ".join(
                        "%.2f" % v for v in r["ca2_by_pod"].values())), file=out)
    if not seen:
        print("| no pod identity in this run | | | | | |", file=out)
    falsifiers(data, frozen, spec, out)
    return verdicts


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="verb", required=True)
    steps = sub.add_parser("steps")
    steps.add_argument("run")
    steps.add_argument("phase")
    steps.add_argument("--discard", type=float, default=180)
    rep = sub.add_parser("report")
    rep.add_argument("run")
    rep.add_argument("frozen")
    rep.add_argument("spec")
    rep.add_argument("--redo", default="")
    a = ap.parse_args()
    if a.verb == "steps":
        rows, stop = analyze_steps(a.run, a.phase, a.discard)
        if not rows:
            sys.exit("%s: no judged step" % a.phase)
        path = os.path.join(a.run, "phases", a.phase, "analysis.jsonl")
        with open(path + ".tmp", "w", encoding="utf8") as f:
            for r in rows:
                f.write(json.dumps(r) + "\n")
        os.replace(path + ".tmp", path)
        print("%s: %d steps, stopped by %s" % (a.phase, len(rows), stop["rule"] if stop else "no rule"))
        return
    with open(a.frozen, encoding="utf8") as f:
        frozen = json.load(f)
    with open(a.spec, encoding="utf8") as f:
        spec = json.load(f)
    redo = None
    if a.redo:
        with open(a.redo, encoding="utf8") as f:
            redo = json.load(f)
    with open(os.path.join(a.run, "report.md"), "w", encoding="utf8") as f:
        verdicts = report(a.run, frozen, spec, redo, f)
    print("%d readings; %d fail" % (len(verdicts), sum(1 for v in verdicts.values() if fail(v))))


if __name__ == "__main__":
    main()
