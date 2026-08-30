#!/usr/bin/env python3
"""Extract one or more benchmark run directories into the multi-file viz bundle.

Input:  one or more run directories produced by the benchmark harness + runtools.
Output: bundle/ directory with meta.json, provenance.json, pods.json,
        scaled_objects.json, endpoints.json, coverage.json,
        extract_input_report.json, extract_output_report.json, and
        (optionally) requests.json.

Usage:
    python3 hack/benchmark/extract.py --run <results-dir>
    python3 hack/benchmark/extract.py --run <dir1> --run <dir2>
    python3 hack/benchmark/extract.py --run <results-dir> --out <bundle-dir>
    python3 hack/benchmark/extract.py --run <results-dir> --bucket-width 5

Design: stdlib only, single file, one function per data source.
"""

import argparse
import datetime
import json
import math
import os
import re
import sys
from collections import defaultdict

# ---------------------------------------------------------------------------
# Shared primitives
# ---------------------------------------------------------------------------

_warnings = []
_EXTRACT_VERSION = "0.4.0"


def warn(msg):
    """Print to stderr and record in warnings list for provenance."""
    print(f"WARN: {msg}", file=sys.stderr)
    _warnings.append(msg)


def read_json(path, default=None):
    """Read and parse a JSON file; return default on any error."""
    if not path or not os.path.isfile(path):
        return default
    try:
        with open(path) as f:
            return json.load(f)
    except Exception as e:
        warn(f"Could not read {path}: {e}")
        return default


def read_flat_yaml(path):
    """Parse a simple flat key: value YAML file without PyYAML.

    Handles:
    - key: value (plain string, int, float)
    - key: "quoted string"
    - # comments
    - blank lines
    Only the first level is parsed; nested YAML is ignored.
    """
    if not path or not os.path.isfile(path):
        return {}
    result = {}
    with open(path, errors="replace") as f:
        for line in f:
            line = line.rstrip()
            if not line or line.lstrip().startswith("#"):
                continue
            m = re.match(r'^(\w[\w\-]*)\s*:\s*(.*)', line)
            if not m:
                continue
            key, val = m.group(1), m.group(2).strip()
            # Strip inline comments
            val = re.sub(r'\s+#.*$', '', val)
            # Strip surrounding quotes
            if (val.startswith('"') and val.endswith('"')) or \
               (val.startswith("'") and val.endswith("'")):
                val = val[1:-1]
            result[key] = val
    return result


def iso_epoch(s):
    """Convert ISO 8601 string to epoch float. Returns None on failure.

    Handles: '2024-05-01T12:34:56Z', '2024-05-01T12:34:56+00:00', etc.
    """
    if not s:
        return None
    try:
        import datetime
        s = s.strip()
        # Normalise Z suffix
        s = re.sub(r'Z$', '+00:00', s)
        # Python 3.7+ fromisoformat does not accept +HH:MM without the colon
        # in some variants; ensure it is there.
        s = re.sub(r'([+-])(\d{2}):?(\d{2})$', r'\1\2:\3', s)
        dt = datetime.datetime.fromisoformat(s)
        return dt.timestamp()
    except Exception:
        return None


def iso_dur_seconds(s):
    """Convert ISO 8601 duration like 'PT217.8S' or 'P0DT5M' to float seconds."""
    if not s:
        return None
    s = str(s).strip().upper()
    # Shortcut: plain integer or float is treated as seconds already
    try:
        return float(s)
    except ValueError:
        pass
    total = 0.0
    m = re.match(r'^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$', s)
    if not m:
        return None
    days, hours, mins, secs = m.groups()
    if days:
        total += float(days) * 86400
    if hours:
        total += float(hours) * 3600
    if mins:
        total += float(mins) * 60
    if secs:
        total += float(secs)
    return total


def _mean(vals):
    """Mean of a list of non-None numbers."""
    vs = [v for v in vals if v is not None]
    return sum(vs) / len(vs) if vs else None


def _pct(vals, q):
    """q-th percentile (0–100) of a list of non-None numbers."""
    vs = sorted(v for v in vals if v is not None)
    if not vs:
        return None
    idx = (q / 100) * (len(vs) - 1)
    lo, hi = int(idx), min(int(idx) + 1, len(vs) - 1)
    frac = idx - lo
    return vs[lo] * (1 - frac) + vs[hi] * frac


def _hist_percentile(le_list, n_list, q):
    """Compute q-th percentile (0–100) from a delta histogram via linear interpolation.

    le_list: sorted list of finite upper-bound values (bucket boundaries)
    n_list:  per-bucket delta counts (same length as le_list)
    q:       percentile to compute (0–100)

    Returns the interpolated value, or None if the histogram is empty.
    The histogram represents observations in (prev_le, le] ranges where the
    first bucket represents (0, le_list[0]].
    """
    total = sum(n_list)
    if total <= 0:
        return None
    target = (q / 100.0) * total
    cumulative = 0.0
    prev_le = 0.0
    for le, n in zip(le_list, n_list):
        cumulative += n
        if cumulative >= target:
            # Linear interpolation within this bucket
            bucket_start = cumulative - n
            if n <= 0:
                return le
            frac = (target - bucket_start) / n
            return prev_le + frac * (le - prev_le)
        prev_le = le
    # Target exceeded all buckets (shouldn't happen, but return last le)
    return le_list[-1] if le_list else None


def prom_labels(line):
    """Extract all label key=value pairs from a Prometheus text line."""
    m = re.search(r'\{([^}]*)\}', line)
    if not m:
        return {}
    return dict(re.findall(r'(\w+)="([^"]*)"', m.group(1)))


def prom_label(line, key):
    """Extract a single label value from a Prometheus text line."""
    return prom_labels(line).get(key)


def prom_val(line):
    """Extract the numeric value from a Prometheus text line (after labels or name)."""
    # Strip trailing comment and whitespace
    line = re.sub(r'\s+#.*$', '', line).rstrip()
    parts = line.rsplit(None, 1)
    if len(parts) < 2:
        return None
    try:
        return float(parts[-1])
    except ValueError:
        return None


def write_bundle(out_dir, name, data):
    """Write data as indented JSON to <out_dir>/<name>."""
    os.makedirs(out_dir, exist_ok=True)
    path = os.path.join(out_dir, name)
    with open(path, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    return path


def write_provenance(out_dir, run_dir, harness, version, extra_warnings=None):
    """Write provenance.json. Called first so it exists even on partial runs."""
    all_warnings = list(_warnings)
    if extra_warnings:
        all_warnings.extend(extra_warnings)
    data = {
        "extractor_version": version,
        "run_dir": os.path.abspath(run_dir),
        "harness": harness,
        "warnings": all_warnings,
    }
    return write_bundle(out_dir, "provenance.json", data)


# ---------------------------------------------------------------------------
# Sub-task 2 — Run identity: meta.json
# ---------------------------------------------------------------------------

def read_run_metadata(run_dir):
    """Read run_metadata.yaml and return a normalised dict."""
    path = os.path.join(run_dir, "run_metadata.yaml")
    raw = read_flat_yaml(path)
    harness_start = iso_epoch(raw.get("harness_start"))
    harness_stop = iso_epoch(raw.get("harness_stop"))
    return {
        "harness": raw.get("harness_name", "unknown"),
        "model": raw.get("model", ""),
        "namespace": raw.get("namespace", ""),
        "endpoint_url": raw.get("endpoint_url", ""),
        "harness_start_epoch": harness_start,
        "harness_stop_epoch": harness_stop,
        "run_id": raw.get("experiment_id", raw.get("run_id", "")),
        "harness_workload": raw.get("harness_workload", ""),
        "_raw": raw,
    }


def find_workload_yaml(run_dir, metadata):
    """Locate the scenario YAML by name from metadata or by glob."""
    name = metadata.get("harness_workload", "")
    for candidate in [
        os.path.join(run_dir, name),
        os.path.join(run_dir, name + ".yaml"),
        os.path.join(run_dir, name + ".yml"),
    ]:
        if os.path.isfile(candidate):
            return candidate
    # Search for any .yaml in the run dir that looks like a scenario
    for fname in os.listdir(run_dir):
        if fname.endswith((".yaml", ".yml")) and fname != "run_metadata.yaml":
            path = os.path.join(run_dir, fname)
            with open(path, errors="replace") as f:
                text = f.read(512)
            if "load:" in text or "stages:" in text:
                return path
    return None


def read_load_stages(yaml_path):
    """Parse scenario YAML load.stages[] into a list of stage dicts.

    Returns [{duration_s, rate_rps, in_tok_mean, out_tok_mean}].
    Handles both constant and staged load types.
    """
    if not yaml_path or not os.path.isfile(yaml_path):
        return []
    with open(yaml_path, errors="replace") as f:
        text = f.read()

    stages = []
    # Simple block-level YAML parser for the stages list
    # Find the stages array under load:
    in_stage = False
    current = {}
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("- ") and ("rate" in stripped or "duration" in stripped):
            if current:
                stages.append(current)
            current = {}
            in_stage = True
        if in_stage:
            m = re.match(r'\s*-?\s*(rate|duration|mean_input_tokens|mean_output_tokens|input_mean|output_mean)\s*:\s*(.+)', line)
            if m:
                k, v = m.group(1), m.group(2).strip()
                current[k] = v

    if current:
        stages.append(current)

    result = []
    for s in stages:
        dur_raw = s.get("duration", "0")
        dur_s = iso_dur_seconds(dur_raw) or 0.0
        rate_raw = s.get("rate", "0")
        try:
            rate = float(rate_raw)
        except (ValueError, TypeError):
            rate = 0.0
        in_tok = None
        for k in ("mean_input_tokens", "input_mean"):
            if k in s:
                try:
                    in_tok = float(s[k])
                except (ValueError, TypeError):
                    pass
                break
        out_tok = None
        for k in ("mean_output_tokens", "output_mean"):
            if k in s:
                try:
                    out_tok = float(s[k])
                except (ValueError, TypeError):
                    pass
                break
        result.append({
            "duration_s": dur_s,
            "rate_rps": rate,
            "in_tok_mean": in_tok,
            "out_tok_mean": out_tok,
        })
    return result


def compute_load_start(harness_start_epoch, stages):
    """Compute load_start_epoch as the start of the first non-zero-rate stage.

    Returns (load_start_epoch, prewarm_s).
    """
    if not stages or harness_start_epoch is None:
        return harness_start_epoch, 0.0
    prewarm_s = 0.0
    for stage in stages:
        if stage.get("rate_rps", 0) > 0:
            break
        prewarm_s += stage.get("duration_s", 0.0)
    return harness_start_epoch + prewarm_s, prewarm_s


def confirm_model_from_wva(run_dir, expected_model):
    """Read the first WVA controller scrape and look for model_name label."""
    raw_dir = os.path.join(run_dir, "metrics", "raw")
    if not os.path.isdir(raw_dir):
        return None
    wva_files = sorted(
        f for f in os.listdir(raw_dir)
        if f.endswith("_metrics.log") and re.match(r"(workload-variant-autoscaler|wva)-controller", f)
    )
    if not wva_files:
        return None
    path = os.path.join(raw_dir, wva_files[0])
    try:
        with open(path, errors="replace") as f:
            for line in f:
                if "model_name" in line and ("wva_analyzer_demand" in line or "wva_kv_cache_tokens" in line):
                    found = prom_label(line, "model_name")
                    if found and found != expected_model:
                        warn(f"model_name in WVA scrape ({found!r}) differs from run_metadata ({expected_model!r})")
                    return found
    except OSError:
        pass
    return None


def _now_iso():
    """Current UTC time as ISO 8601 string."""
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def build_meta(metadata, stages, load_start_epoch, prewarm_s, time_anchor, wva_model,
               sentinels=None):
    """Assemble the meta.json structure.

    sentinels is an optional dict with keys:
      load_end_t            — planned end: load_duration_s (seconds from load start)
      last_departure_t      — t of last request departure observed
      last_scale_event_t    — t of last scaler[] event observed
      replica_scrape_end_t  — t of last replica record across all SOs/sources

    These let the renderer know the extent of each data stream and clip the
    x-axis sensibly (x_max = max(last_departure_t, last_scale_event_t) with
    load_end_t as a minimum floor).  No data is truncated by the extractor.
    """
    load_duration_s = sum(s["duration_s"] for s in stages if s.get("rate_rps", 0) > 0)
    meta = {
        "run_id": metadata["run_id"],
        "harness": metadata["harness"],
        "namespace": metadata["namespace"],
        "extracted_at": _now_iso(),
        "extractor_version": _EXTRACT_VERSION,
        "harness_start_epoch": metadata["harness_start_epoch"],
        "load_start_epoch": load_start_epoch,
        "prewarm_s": prewarm_s,
        "load_duration_s": load_duration_s,
        "wva_confirmed_model": wva_model,
        "time_anchor": time_anchor,
    }
    if sentinels:
        meta.update(sentinels)
    return meta


# ---------------------------------------------------------------------------
# Sub-task 3 — Time anchor
# ---------------------------------------------------------------------------

def compute_time_anchor(harness, requests, pod_scrapes_by_pod):
    """Compute the offset to align inference-perf monotonic timestamps to epoch.

    Returns an anchor dict:
      {method, offset_s, corr, trustworthy, n_scrapes, shift_from_guess_s}
    """
    _not_needed = {
        "method": "not-needed",
        "offset_s": 0.0,
        "corr": None,
        "trustworthy": True,
        "n_scrapes": 0,
        "shift_from_guess_s": 0.0,
    }
    if harness != "inference-perf":
        return _not_needed
    if not requests:
        return _not_needed

    # Collect pod scrape (epoch, run+wait) pairs across all pods
    scrape_pts = []
    for samples in pod_scrapes_by_pod.values():
        for s in samples:
            run = s.get("g", {}).get("run")
            wait = s.get("g", {}).get("wait")
            t = s.get("t_epoch")
            if t is not None and run is not None and wait is not None:
                scrape_pts.append((t, run + wait))
    scrape_pts.sort(key=lambda x: x[0])

    n_scrapes = len(scrape_pts)
    if n_scrapes < 5:
        return {"method": "refused-short-trace", "offset_s": 0.0, "corr": None,
                "trustworthy": False, "n_scrapes": n_scrapes, "shift_from_guess_s": 0.0}

    # Build monotonic request concurrency signal
    # requests must have fields: t_arr_mono, t_dep_mono (monotonic seconds)
    events = []
    for r in requests:
        t_arr = r.get("t_arr_mono")
        t_dep = r.get("t_dep_mono")
        if t_arr is not None:
            events.append((t_arr, +1))
        if t_dep is not None:
            events.append((t_dep, -1))
    if not events:
        return _not_needed
    events.sort()

    req_t0 = events[0][0]
    req_t1 = events[-1][0]
    req_span = req_t1 - req_t0

    scrape_t0 = scrape_pts[0][0]
    scrape_t1 = scrape_pts[-1][0]
    scrape_span = scrape_t1 - scrape_t0

    if req_span < 0.2 * scrape_span:
        return {"method": "refused-short-trace", "offset_s": 0.0, "corr": None,
                "trustworthy": False, "n_scrapes": n_scrapes, "shift_from_guess_s": 0.0}

    # Initial guess: align request stream start to first scrape
    guess = scrape_t0 - req_t0

    def _corr(offset):
        """Pearson correlation between pod scrape occupancy and request in-system count at scrape times."""
        ys_scrape = [occ for _, occ in scrape_pts]
        ys_req = []
        for ep_t, _ in scrape_pts:
            mono_t = ep_t - offset
            # Compute concurrency at mono_t
            conc = 0
            for ev_t, delta in events:
                if ev_t <= mono_t:
                    conc += delta
                else:
                    break
            ys_req.append(max(0, conc))
        # Pearson
        n = len(ys_scrape)
        if n < 2:
            return 0.0
        mean_s = sum(ys_scrape) / n
        mean_r = sum(ys_req) / n
        num = sum((a - mean_s) * (b - mean_r) for a, b in zip(ys_scrape, ys_req))
        den_s = math.sqrt(sum((a - mean_s) ** 2 for a in ys_scrape))
        den_r = math.sqrt(sum((b - mean_r) ** 2 for b in ys_req))
        if den_s < 1e-9 or den_r < 1e-9:
            return 0.0
        return num / (den_s * den_r)

    span = min(120.0, scrape_span * 0.5)
    step = max(1.0, scrape_span / 100)
    best_offset = guess
    best_corr = _corr(guess)

    t = guess - span
    while t <= guess + span:
        c = _corr(t)
        if c > best_corr:
            best_corr = c
            best_offset = t
        t += step

    return {
        "method": "cross-correlation",
        "offset_s": best_offset,
        "corr": round(best_corr, 4),
        "trustworthy": best_corr >= 0.95,
        "n_scrapes": n_scrapes,
        "shift_from_guess_s": round(best_offset - guess, 2),
    }


# ---------------------------------------------------------------------------
# Sub-task 4 — Pod scrapes → pods.json
# ---------------------------------------------------------------------------

# Metric families
_GAUGE_NAMES = {
    "run": "vllm:num_requests_running",
    "wait": "vllm:num_requests_waiting",
    "kv": "vllm:kv_cache_usage_perc",
}
_CTR_NAMES = {
    "preempt": "vllm:num_preemptions_total",
    "pfx_hit": "vllm:cache_query_token_hit",
    "pfx_miss": "vllm:cache_query_token_miss",
    # generation token counter: differenced → output token rate (tokens/s)
    "gen": "vllm:generation_tokens_total",
    # request departure counter: differenced → completed request rate (req/s)
    # NOTE: has finished_reason label — parser sums across all label values
    "ok": "vllm:request_success_total",
}
_CFG_LINE = "vllm:cache_config_info"
_HIST_NAMES = {
    "itl": ("vllm:time_per_output_token_seconds",),
    # ttft: time to first token (vLLM-measured, scrape-interval mean)
    "ttft": ("vllm:time_to_first_token_seconds",),
    # prefill: vLLM-internal prefill batch time (distinct from TTFT)
    "prefill": ("vllm:request_prefill_time_seconds",),
    "qwait": ("vllm:request_queue_time_seconds",),
    "e2e": ("vllm:e2e_request_latency_seconds",),
    "gen_tokens": ("vllm:request_generation_tokens",),
    "prompt_tokens": ("vllm:request_prompt_tokens",),
}

# Histogram bucket metrics: (internal_key, vllm_metric_prefix)
# Only finite le boundaries are stored ("+Inf" is excluded).
_BUCKET_METRICS = {
    "ttft_buckets":     "vllm:time_to_first_token_seconds_bucket",
    "out_tok_buckets":  "vllm:request_generation_tokens_bucket",
    "in_tok_buckets":   "vllm:request_prompt_tokens_bucket",
    "qwait_buckets":    "vllm:request_queue_time_seconds_bucket",
}

# Files whose names match these patterns are NOT vLLM pod scrapes
_SKIP_PATTERNS = re.compile(r"(epp|wva-controller|workload-variant-autoscaler|collection_debug)")

# Pod name: strip trailing ReplicaSet hash (-<5-10 alphanum>-<5 alphanum>)
_POD_HASH_RE = re.compile(r'-[a-z0-9]{5,10}-[a-z0-9]{5}$')

MAX_DT = 120  # seconds; gap longer than this means pod restart or collection gap
BUCKET_WIDTH_S = 1.0  # default bucket width for requests.json timeseries

# Filename pattern: <pod-name>_<10-digit-epoch>_metrics.log
_FILE_RE = re.compile(r'^(?P<pod>.+?)_(?P<ts>\d{10})_metrics\.log$')


def so_id_from_pod(pod_name):
    """Strip the trailing ReplicaSet hash to get the Deployment name (SO id)."""
    return _POD_HASH_RE.sub('', pod_name)


def parse_vllm_scrape(path):
    """Parse one vLLM pod metrics.log file.

    Returns {g: {run, wait, kv},
             c: {preempt, pfx_hit, pfx_miss, gen, ok},
             h: {itl_sum, itl_count, ttft_sum, ttft_count, prefill_sum,
                 prefill_count, qwait_sum, qwait_count, e2e_sum, e2e_count,
                 gen_tokens_sum, gen_tokens_count, ...},
             cfg: {num_gpu_blocks, block_size, gpu_memory_utilization,
                   enable_prefix_caching},
             bkt: {ttft_buckets: {le_float: count, ...},
                   out_tok_buckets: ..., in_tok_buckets: ...,
                   qwait_buckets: ...}}

    Notes:
    - "ok" sums vllm:request_success_total across all finished_reason label
      values (stop/length/abort/error/repetition) so the counter represents
      total departures and can be differenced for req/s.
    - "gen" is vllm:generation_tokens_total; differenced → output tokens/s.
    - "ttft" is vllm:time_to_first_token_seconds (histogram); differenced
      sum/count → mean TTFT ms per interval.
    - "prefill" is vllm:request_prefill_time_seconds, the vLLM-internal
      prefill batch time — distinct from TTFT.
    - "e2e" is vllm:e2e_request_latency_seconds; differenced → mean e2e ms.
    - "bkt" stores raw cumulative bucket vectors for histogram percentile
      computation; "+Inf" le values are excluded.
    """
    result = {"g": {}, "c": {}, "h": {}, "cfg": {}, "bkt": {}}
    # ok counter accumulates across multiple finished_reason label values
    _ok_acc = 0.0
    _ok_seen = False

    try:
        with open(path, errors="replace") as f:
            text = f.read()
    except OSError:
        return result

    ok_metric_name = _CTR_NAMES["ok"]

    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue

        # Cache config (info metric with all config in labels)
        if line.startswith(_CFG_LINE) and len(line) > len(_CFG_LINE) and line[len(_CFG_LINE)] in "{ ":
            labels = prom_labels(line)
            for k in ("num_gpu_blocks", "block_size"):
                if k in labels:
                    try:
                        result["cfg"][k] = int(labels[k])
                    except ValueError:
                        pass
            for k in ("gpu_memory_utilization", "enable_prefix_caching"):
                if k in labels:
                    result["cfg"][k] = labels[k]
            continue

        # ok counter: sum across all finished_reason label values
        if line.startswith(ok_metric_name) and len(line) > len(ok_metric_name) and line[len(ok_metric_name)] in "{ ":
            v = prom_val(line)
            if v is not None:
                _ok_acc += v
                _ok_seen = True
            continue

        # Gauges
        for field, name in _GAUGE_NAMES.items():
            if line.startswith(name) and len(line) > len(name) and line[len(name)] in "{ ":
                v = prom_val(line)
                if v is not None and field not in result["g"]:
                    result["g"][field] = v
                break
        else:
            # Counters (excluding ok, handled above)
            for field, name in _CTR_NAMES.items():
                if field == "ok":
                    continue
                if line.startswith(name) and len(line) > len(name) and line[len(name)] in "{ ":
                    v = prom_val(line)
                    if v is not None and field not in result["c"]:
                        result["c"][field] = v
                    break
            else:
                # Histograms: _sum and _count suffixes
                for field, names in _HIST_NAMES.items():
                    for name in names:
                        _sum_name = name + "_sum"
                        if line.startswith(_sum_name) and len(line) > len(_sum_name) and line[len(_sum_name)] in "{ ":
                            v = prom_val(line)
                            if v is not None:
                                result["h"][field + "_sum"] = v
                            break
                        _cnt_name = name + "_count"
                        if line.startswith(_cnt_name) and len(line) > len(_cnt_name) and line[len(_cnt_name)] in "{ ":
                            v = prom_val(line)
                            if v is not None:
                                result["h"][field + "_count"] = v
                            break
                else:
                    # Histogram buckets: cumulative _bucket lines (exclude +Inf)
                    for bkt_key, bkt_metric in _BUCKET_METRICS.items():
                        if line.startswith(bkt_metric) and len(line) > len(bkt_metric) and line[len(bkt_metric)] in "{":
                            le_str = prom_label(line, "le")
                            if le_str and le_str != "+Inf":
                                v = prom_val(line)
                                if v is not None:
                                    try:
                                        le_f = float(le_str)
                                    except ValueError:
                                        break
                                    bkt_dict = result["bkt"].setdefault(bkt_key, {})
                                    bkt_dict[le_f] = v
                            break

    if _ok_seen:
        result["c"]["ok"] = _ok_acc
    return result


def scan_pod_scrapes(run_dir):
    """Scan metrics/raw/ for vLLM pod scrape files.

    Returns {pod_name: [sorted dicts with t_epoch + parsed data]}.
    """
    raw_dir = os.path.join(run_dir, "metrics", "raw")
    if not os.path.isdir(raw_dir):
        return {}
    by_pod = defaultdict(list)
    for fname in os.listdir(raw_dir):
        if _SKIP_PATTERNS.search(fname):
            continue
        m = _FILE_RE.match(fname)
        if not m:
            continue
        pod = m.group("pod")
        ts = int(m.group("ts"))
        path = os.path.join(raw_dir, fname)
        parsed = parse_vllm_scrape(path)
        parsed["t_epoch"] = ts
        by_pod[pod].append(parsed)
    for pod in by_pod:
        by_pod[pod].sort(key=lambda x: x["t_epoch"])
    return dict(by_pod)


def read_pod_timings(run_dir):
    """Read pod created/ready timestamps from the best available source.

    Primary: metrics/processed/wva_pod_timings.json (explicit per-pod records).
    Fallback: metrics/processed/replica_status_timeseries.json — derives
      created_t as the first snapshot where a pod name appears in any
      controller's pods[] list, and ready_t as the first snapshot where
      that pod appears in readyPods[] (or similar ready field).

    Returns {pod_name: {created_t, ready_t}}.
    """
    path = os.path.join(run_dir, "metrics", "processed", "wva_pod_timings.json")
    data = read_json(path)
    if data:
        result = {}
        for p in data.get("pods", []):
            name = p.get("name") or p.get("pod_name")
            if not name:
                continue
            result[name] = {
                "created_t": iso_epoch(p.get("created")),
                "ready_t": iso_epoch(p.get("ready_at") or p.get("ready")),
            }
        if result:
            return result

    # Fallback: derive from replica_status_timeseries.json snapshots.
    # Each snapshot.controllers[].pods[] (or readyPods[]) may list pod names.
    ts_path = os.path.join(run_dir, "metrics", "processed", "replica_status_timeseries.json")
    ts_data = read_json(ts_path)
    if not ts_data:
        return {}
    result = {}
    for snap in ts_data.get("snapshots", []):
        ts = iso_epoch(snap.get("timestamp")) if isinstance(snap.get("timestamp"), str) else snap.get("timestamp")
        if ts is None:
            continue
        for ctrl in snap.get("controllers", []):
            # pods[] or allPods[] → creation observed here
            for pod_name in (ctrl.get("pods") or ctrl.get("allPods") or []):
                if pod_name and pod_name not in result:
                    result[pod_name] = {"created_t": ts, "ready_t": None}
            # readyPods[] → ready observed here
            for pod_name in (ctrl.get("readyPods") or ctrl.get("ready_pods") or []):
                if pod_name:
                    if pod_name not in result:
                        result[pod_name] = {"created_t": None, "ready_t": ts}
                    elif result[pod_name].get("ready_t") is None:
                        result[pod_name]["ready_t"] = ts
    return result


def pod_series(samples, load_start_epoch):
    """Convert sorted list of scrape samples into contract series[] entries."""
    series = []
    for i in range(1, len(samples)):
        a, b = samples[i - 1], samples[i]
        ta, tb = a["t_epoch"], b["t_epoch"]
        dt = tb - ta
        if dt <= 0:
            continue
        t = tb - load_start_epoch

        g = b.get("g", {})
        run_val = g.get("run")
        wait_val = g.get("wait")
        kv_val = g.get("kv")

        # Counter differencing (rates per second)
        def _rate(field):
            va = a.get("c", {}).get(field)
            vb = b.get("c", {}).get(field)
            if va is None or vb is None:
                return None
            diff = vb - va
            if diff < 0 or dt > MAX_DT:
                return None
            return diff / dt

        # Histogram mean over interval
        def _hist_mean_ms(field):
            ha, hb = a.get("h", {}), b.get("h", {})
            s_key, c_key = field + "_sum", field + "_count"
            vs_a, vc_a = ha.get(s_key), ha.get(c_key)
            vs_b, vc_b = hb.get(s_key), hb.get(c_key)
            if None in (vs_a, vc_a, vs_b, vc_b):
                return None
            ds = vs_b - vs_a
            dc = vc_b - vc_a
            if dc <= 0 or ds < 0 or dt > MAX_DT:
                return None
            return (ds / dc) * 1000  # seconds → ms

        def _hist_rate(field):
            ha, hb = a.get("h", {}), b.get("h", {})
            c_key = field + "_count"
            vc_a, vc_b = ha.get(c_key), hb.get(c_key)
            if vc_a is None or vc_b is None:
                return None
            dc = vc_b - vc_a
            if dc < 0 or dt > MAX_DT:
                return None
            return dc / dt

        stable = (kv_val is not None and kv_val >= 0.05 and dt <= MAX_DT)

        ok_rate = _rate("ok")
        gen_rate = _rate("gen")
        ttft_ms = _hist_mean_ms("ttft")
        e2e_ms = _hist_mean_ms("e2e")

        # Diff cumulative bucket vectors for histogram fields.
        # Returns {"le": [...], "n": [...]} or None.
        def _diff_buckets(bkt_key):
            ba = a.get("bkt", {}).get(bkt_key)
            bb = b.get("bkt", {}).get(bkt_key)
            if not ba or not bb:
                return None
            le_vals = sorted(set(ba) & set(bb))
            if not le_vals:
                return None
            deltas = []
            for le in le_vals:
                d = bb[le] - ba[le]
                if d < 0:
                    # Pod restarted — return null for this histogram
                    return None
                deltas.append(d)
            delta_count = sum(deltas)
            if delta_count == 0:
                return None
            return {"le": le_vals, "n": deltas}

        entry = {
            "t": t,
            "run": run_val,
            "wait": wait_val,
            "kv": kv_val,
            # departure rate (completed req/s) from vllm:request_success_total
            "ok_rate": ok_rate,
            # output token rate (tokens/s) from vllm:generation_tokens_total
            "gen_rate": gen_rate,
            # prompt token throughput (tokens/s) from histogram count
            "prompt_rate": _hist_rate("prompt_tokens"),
            # mean TTFT ms over interval from vllm:time_to_first_token_seconds
            "ttft_ms": ttft_ms,
            # mean vLLM-internal prefill batch time ms
            "prefill_ms": _hist_mean_ms("prefill"),
            # mean e2e request latency ms from vllm:e2e_request_latency_seconds
            "e2e_ms": e2e_ms,
            "itl_ms": _hist_mean_ms("itl"),
            "qwait_s": (_qwait_ms / 1000) if (_qwait_ms := _hist_mean_ms("qwait")) is not None else None,
            "preempt_rate": _rate("preempt"),
            "pfx_hit": _rate("pfx_hit"),
            "stable": stable,
            # Full histogram distributions (delta counts per bucket boundary)
            "ttft_hist":    _diff_buckets("ttft_buckets"),
            "out_tok_hist": _diff_buckets("out_tok_buckets"),
            "in_tok_hist":  _diff_buckets("in_tok_buckets"),
            "qwait_hist":   _diff_buckets("qwait_buckets"),
        }
        series.append(entry)
    return series


def _first_metric_t(samples, load_start_epoch):
    """Return t of first sample where run or kv is non-null."""
    for s in samples:
        g = s.get("g", {})
        if g.get("run") is not None or g.get("kv") is not None:
            return s["t_epoch"] - load_start_epoch
    return None


def engine_config_for_so(so_id, pod_scrapes_by_pod):
    """Find first non-empty cache_config_info for any pod belonging to this SO."""
    for pod, samples in pod_scrapes_by_pod.items():
        if so_id_from_pod(pod) != so_id:
            continue
        for s in samples:
            cfg = s.get("cfg", {})
            if cfg.get("num_gpu_blocks"):
                kv_tokens = cfg["num_gpu_blocks"] * cfg.get("block_size", 16)
                return {
                    "num_gpu_blocks": cfg["num_gpu_blocks"],
                    "block_size": cfg.get("block_size"),
                    "gpu_memory_utilization": cfg.get("gpu_memory_utilization"),
                    "enable_prefix_caching": cfg.get("enable_prefix_caching"),
                    "kv_tokens": kv_tokens,
                }
    return {}


def build_pods(pod_scrapes_by_pod, pod_timings, load_start_epoch, identity_map, coverage):
    """Assemble pods.json list."""
    if not pod_scrapes_by_pod:
        coverage.add("global", "Pod metrics present", "FAIL", "No pod scrape files found")
        return []

    # Reverse identity map: deploy_name -> so_id
    deploy_to_so = {v["deploy_name"]: k for k, v in identity_map.items()
                    if v.get("deploy_name")}

    lse = load_start_epoch or 0.0
    pods = []
    for pod_name, samples in sorted(pod_scrapes_by_pod.items()):
        deploy_name = so_id_from_pod(pod_name)
        # Resolve deploy_name to canonical so_id via identity map
        so_id = deploy_to_so.get(deploy_name, deploy_name)
        timings = pod_timings.get(pod_name, {})
        created_t = timings.get("created_t")
        ready_t = timings.get("ready_t")
        series = pod_series(samples, lse)
        fmt = _first_metric_t(samples, lse)
        pods.append({
            "pod_id": pod_name,
            "so_id": so_id,
            "created_t": (created_t - lse) if created_t else None,
            "ready_t": (ready_t - lse) if ready_t else None,
            "first_metric_t": fmt,
            "setup_s": (ready_t - created_t) if (ready_t and created_t) else None,
            "series": series,
        })

    if pods:
        coverage.add("global", "Pod metrics present", "PASS", f"{len(pods)} pods")
    else:
        coverage.add("global", "Pod metrics present", "FAIL", "Parsed 0 pods")
    return pods


# ---------------------------------------------------------------------------
# Sub-task 5 — WVA controller data → scaled_objects.json
# ---------------------------------------------------------------------------

_WVA_POD_RE = re.compile(r"(workload-variant-autoscaler|wva)-controller")
_WVA_FILE_RE = re.compile(r'^(?P<pod>.+?)_(?P<ts>\d{10})_metrics\.log$')


def read_replica_timeseries(run_dir):
    """Read replica snapshots from the best available processed file.

    Returns list of {t_epoch, desired, ready, available, so_id}.
    """
    processed = os.path.join(run_dir, "metrics", "processed")
    for fname in ("replica_status_timeseries.json", "wva_replica_samples.json"):
        path = os.path.join(processed, fname)
        data = read_json(path)
        if not data:
            continue
        snapshots = data.get("snapshots", [])
        if not snapshots:
            continue
        result = []
        for snap in snapshots:
            ts_raw = snap.get("timestamp") or snap.get("t_epoch")
            if isinstance(ts_raw, str):
                ts = iso_epoch(ts_raw)
            elif isinstance(ts_raw, (int, float)):
                ts = float(ts_raw)
            else:
                ts = iso_epoch(snap.get("timestamp_str"))
            for ctrl in snap.get("controllers", []):
                name = ctrl.get("name", "")
                result.append({
                    "t_epoch": ts,
                    "so_id": name,
                    "desired": ctrl.get("desired_replicas"),
                    "ready": ctrl.get("ready_replicas"),
                    "available": ctrl.get("available_replicas"),
                })
        if result:
            return result
    return []


def read_so_config(run_dir):
    """Read scaledobject-config.json → {so_name: config_dict}. Returns {} when absent.

    Falls back to wva/bench-meta.json when scaledobject-config.json is missing.
    The bench-meta stacks[] schema provides: scaledobject (SO name), deployment
    (deploy name), min_replicas, max_replicas, so_paused, so_keda_active.
    gpu_count, role, and cost are never present in bench-meta and stay null.

    Expected fields per entry: so_name, deploy_name, gpu_count, role, cost,
    min_replicas, max_replicas.
    """
    path = os.path.join(run_dir, "scaledobject-config.json")
    data = read_json(path)
    if data:
        if isinstance(data, list):
            result = {}
            for item in data:
                key = item.get("so_name") or item.get("so_id")
                if key:
                    result[key] = item
            return result
        return data

    # Fallback: wva/bench-meta.json stacks[]
    meta_path = os.path.join(run_dir, "wva", "bench-meta.json")
    meta = read_json(meta_path)
    if not meta:
        return {}
    stacks = meta.get("stacks", [])
    if not stacks:
        return {}
    result = {}
    for stack in stacks:
        so_name = stack.get("scaledobject") or stack.get("name")
        deploy_name = stack.get("deployment")
        if not so_name:
            continue
        result[so_name] = {
            "so_name": so_name,
            "deploy_name": deploy_name,
            "gpu_count": None,
            "role": None,
            "cost": None,
            "min_replicas": stack.get("min_replicas"),
            "max_replicas": stack.get("max_replicas"),
            "so_paused": stack.get("so_paused"),
            "so_keda_active": stack.get("so_keda_active"),
        }
    return result


# ---------------------------------------------------------------------------
# SO identity resolution
# ---------------------------------------------------------------------------

def _parse_scale_target_from_log(ctrl_log_path):
    """Parse SO→Deployment mapping from controller log error event lines.

    The VariantAutoscaling object JSON emitted by event.go contains both
    metadata.name (SO name) and spec.scaleTargetRef.name (Deployment name).

    Returns {so_name: deploy_name}.
    """
    if not ctrl_log_path or not os.path.isfile(ctrl_log_path):
        return {}
    result = {}
    with open(ctrl_log_path, errors="replace") as f:
        for line in f:
            if "scaleTargetRef" not in line or "metadata" not in line:
                continue
            # Try to extract both names from the same JSON blob on this line
            # The object JSON is: {"metadata":{"name":"<so>", ...}, "spec":{"scaleTargetRef":{"name":"<deploy>"},...}}
            so = None
            deploy = None
            m = re.search(r'"metadata":\{"name":"([^"]+)"', line)
            if m:
                so = m.group(1)
            m2 = re.search(r'"scaleTargetRef":\{"kind":"[^"]+","name":"([^"]+)"', line)
            if m2:
                deploy = m2.group(1)
            if so and deploy and so != deploy:
                result[so] = deploy
    return result


def _longest_common_prefix(a, b):
    """Return the length of the longest common prefix of strings a and b."""
    n = min(len(a), len(b))
    for i in range(n):
        if a[i] != b[i]:
            return i
    return n


def build_identity_map(so_names, deploy_names, so_config, ctrl_log_path, input_report):
    """Build {so_name: {deploy_name, confidence}} mapping.

    Priority:
    1. scaledobject-config.json (explicit, authoritative)
    2. Controller log scaleTargetRef (inferred)
    3. Longest common prefix match (prefix)
    4. Unresolved
    """
    identity = {}

    # 1. Explicit from scaledobject-config.json
    for so_name, cfg in so_config.items():
        deploy = cfg.get("deploy_name")
        if deploy:
            identity[so_name] = {"deploy_name": deploy, "confidence": "explicit"}

    # 2. Controller log scaleTargetRef
    log_mapping = _parse_scale_target_from_log(ctrl_log_path)
    for so_name, deploy in log_mapping.items():
        if so_name not in identity:
            identity[so_name] = {"deploy_name": deploy, "confidence": "inferred"}

    # 3. Longest common prefix match for remaining SO names
    remaining_so = [s for s in so_names if s not in identity]

    for so_name in remaining_so:
        candidates = []
        for deploy in deploy_names:
            plen = _longest_common_prefix(so_name, deploy)
            if plen >= 10:  # minimum prefix length to avoid spurious matches
                candidates.append((plen, deploy))
        candidates.sort(reverse=True)
        if len(candidates) == 1:
            identity[so_name] = {
                "deploy_name": candidates[0][1],
                "confidence": "prefix",
            }
        elif len(candidates) > 1:
            # Ambiguous: pick longest but flag
            best_deploy = candidates[0][1]
            all_candidates = [d for _, d in candidates]
            identity[so_name] = {
                "deploy_name": best_deploy,
                "confidence": "prefix-ambiguous",
                "candidates": all_candidates,
            }
        else:
            identity[so_name] = {"deploy_name": None, "confidence": "unresolved"}

    # Record in input report
    lines = []
    for so_name, v in sorted(identity.items()):
        conf = v.get("confidence", "?")
        deploy = v.get("deploy_name", "?")
        lines.append(f"{so_name} -> {deploy} ({conf})")
    input_report.add(
        "SO identity resolution",
        "found" if all(v.get("confidence") != "unresolved" for v in identity.values()) else "partial",
        note="; ".join(lines) if lines else "no SOs found",
    )

    return identity


def find_controller_log(run_dir):
    """Return path to the WVA controller text log, or None.

    Probes in priority order:
      1. logs/wva-controller.log  — written by capture_wva_controller_log.sh
         called from run_scenario.sh (canonical new location)
      2. wva-controller.log / controller.log / wva_controller.log
         at run_dir root (legacy / manual copies)
    """
    for candidate in (
        os.path.join(run_dir, "logs", "wva-controller.log"),
        os.path.join(run_dir, "wva-controller.log"),
        os.path.join(run_dir, "controller.log"),
        os.path.join(run_dir, "wva_controller.log"),
    ):
        if os.path.isfile(candidate):
            return candidate
    return None


_LOG_TS_RE = re.compile(r'^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?)')
_ANALYZER_RESULT_RE = re.compile(r'analyzer-result')
_SCALING_DECISION_RE = re.compile(r'scaling-decision')


def _parse_log_json(line):
    """Extract the trailing JSON object from a zap console log line."""
    idx = line.find('\t{')
    if idx < 0:
        idx = line.find(' {')
    if idx < 0:
        return {}
    try:
        return json.loads(line[idx:].lstrip())
    except Exception:
        return {}


def parse_controller_log(path, load_start_epoch, harness_start_epoch, harness_stop_epoch):
    """Parse WVA controller text log into scaler event records.

    Returns [event_dict], sorted by t.
    """
    if not path or not os.path.isfile(path):
        return []

    window_start = (harness_start_epoch or 0) - 60
    window_end = (harness_stop_epoch or float('inf')) + 60
    events = []

    with open(path, errors="replace") as f:
        for line in f:
            line = line.rstrip()
            if not line:
                continue
            m = _LOG_TS_RE.match(line)
            if not m:
                continue
            ts = iso_epoch(m.group(1))
            if ts is None:
                continue
            if ts < window_start or ts > window_end:
                continue

            t = ts - (load_start_epoch or ts)
            fields = _parse_log_json(line)

            if _ANALYZER_RESULT_RE.search(line):
                # rc, sc, prc — promoted from fields to top level.
                # They may be direct keys or nested under variants[].
                rc = fields.get("rc")
                sc = fields.get("sc")
                # prc is per-SO: first variant entry matching so_id, or top-level
                so_id = fields.get("variant", fields.get("scaleTargetName", ""))
                prc = fields.get("prc")
                reason = fields.get("reason", "")
                role = fields.get("role", "")
                for v in fields.get("variants", []):
                    if v.get("name") == so_id:
                        prc = v.get("prc", prc)
                        reason = v.get("reason", reason)
                        role = v.get("role", role)
                        break
                events.append({
                    "event_type": "analyzer_result",
                    "t": t,
                    "so_id": so_id,
                    "analyzer": fields.get("analyzer", ""),
                    "role": role,
                    "rc": rc,
                    "sc": sc,
                    "prc": prc,
                    "reason": reason,
                })
            elif _SCALING_DECISION_RE.search(line):
                events.append({
                    "event_type": "scaling_decision",
                    "t": t,
                    "so_id": fields.get("variant", fields.get("scaleTargetName", "")),
                    "action": fields.get("action", ""),
                    "current_replicas": fields.get("from", fields.get("currentReplicas")),
                    "target_replicas": fields.get("to", fields.get("desiredReplicas")),
                })

    events.sort(key=lambda e: e.get("t", 0))
    return events


def scan_wva_scrapes(run_dir):
    """Parse WVA controller Prometheus scrape files.

    Returns:
      so_scrapes: {so_id: [{t_epoch, current_replicas, kv_capacity_tokens,
                            capacity_per_replica, model_name}]}
      ep_demand:  [{t_epoch, model_name, analyzer, value, unit}]
    """
    raw_dir = os.path.join(run_dir, "metrics", "raw")
    if not os.path.isdir(raw_dir):
        return {}, []

    by_so = defaultdict(lambda: defaultdict(dict))   # so_id -> t_epoch -> fields
    ep_demand = []

    for fname in sorted(os.listdir(raw_dir)):
        if not fname.endswith("_metrics.log"):
            continue
        m = _WVA_FILE_RE.match(fname)
        if not m or not _WVA_POD_RE.search(m.group("pod")):
            continue
        ts = int(m.group("ts"))
        path = os.path.join(raw_dir, fname)
        try:
            with open(path, errors="replace") as f:
                for line in f:
                    line = line.rstrip()
                    if not line or line.startswith("#"):
                        continue
                    labels = prom_labels(line)
                    variant = labels.get("variant_name", "")
                    model = labels.get("model_name", "")
                    analyzer = labels.get("analyzer_name", "")
                    val = prom_val(line)
                    if val is None:
                        continue
                    if line.startswith("wva_current_replicas") and variant:
                        by_so[variant][ts].update({
                            "model_name": model,
                            "current_replicas": val,
                        })
                    elif line.startswith("wva_kv_cache_tokens_capacity") and variant:
                        by_so[variant][ts].update({
                            "model_name": model,
                            "kv_capacity_tokens": val,
                        })
                    elif line.startswith("wva_analyzer_target") and variant:
                        by_so[variant][ts].update({
                            "model_name": model,
                            "capacity_per_replica": val,
                            "analyzer": analyzer,
                        })
                    elif line.startswith("wva_analyzer_demand") and model:
                        # endpoint-scoped demand (no variant_name label)
                        role = labels.get("role", "")
                        # infer unit from analyzer name
                        unit = "tokens_per_s" if analyzer == "throughput" else "tokens"
                        ep_demand.append({
                            "t_epoch": ts,
                            "model_name": model,
                            "analyzer": analyzer,
                            "role": role,
                            "value": val,
                            "unit": unit,
                        })
        except OSError:
            pass

    so_scrapes = {}
    for so_id, ts_map in by_so.items():
        so_scrapes[so_id] = sorted(
            [{"t_epoch": t, **fields} for t, fields in ts_map.items()],
            key=lambda x: x["t_epoch"],
        )
    return so_scrapes, ep_demand


def _dedup_replicas(entries, keys):
    """Return entries with consecutive duplicates removed, always keeping first and last.

    Two consecutive entries are considered duplicates when all tracked `keys`
    have equal values (including both None). The first entry is always kept so
    the renderer has an anchor; the last entry is always kept so the step line
    extends to the correct right edge.
    """
    if not entries:
        return []
    result = [entries[0]]
    for entry in entries[1:]:
        prev = result[-1]
        changed = any(entry.get(k) != prev.get(k) for k in keys)
        if changed:
            result.append(entry)
    # Always include the last entry (may already be there if it changed)
    if len(entries) > 1 and result[-1] is not entries[-1]:
        result.append(entries[-1])
    return result


def build_scaled_objects(replica_ts, so_config, scaler_events, wva_so_scrapes,
                         pod_scrapes_by_pod, load_start_epoch, endpoint_url,
                         identity_map, coverage):
    """Assemble scaled_objects.json list."""
    # Collect so_ids from all authoritative sources (WVA names)
    so_ids = set()
    for so_id in wva_so_scrapes:
        so_ids.add(so_id)
    for e in scaler_events:
        if e.get("so_id"):
            so_ids.add(e["so_id"])
    for so_name in identity_map:
        so_ids.add(so_name)
    so_ids.discard("")

    # Reverse identity map: deploy_name -> so_id (for replica_ts which uses deploy names)
    deploy_to_so = {v["deploy_name"]: k for k, v in identity_map.items()
                    if v.get("deploy_name")}
    # Also collect deploy-name-only SOs (no WVA coverage, e.g. KEDA-only) if not already mapped
    for row in replica_ts:
        name = row.get("so_id")
        if name and name not in so_ids and name not in deploy_to_so:
            so_ids.add(name)

    lse = load_start_epoch or 0.0

    result = []
    for so_id in sorted(so_ids):
        # deploy_name for this SO (may be same as so_id if not in identity map)
        deploy_name = identity_map.get(so_id, {}).get("deploy_name", so_id)

        # Harness-sourced replica entries — keyed by deploy_name in replica_ts.
        # Deduplicated: only emit when (desired, ready, available) changes.
        # Always keep the first and last record so the renderer can draw the
        # step line to the correct right edge.
        _h_raw = [
            {
                "t": (row["t_epoch"] - lse),
                "source": "harness",
                "desired": row.get("desired"),
                "ready": row.get("ready"),
                "available": row.get("available"),
            }
            for row in replica_ts
            if row.get("so_id") in (so_id, deploy_name)
        ]
        harness_replicas = _dedup_replicas(_h_raw, ("desired", "ready", "available"))

        # WVA Prometheus-sourced replica entries.
        # Deduplicated: only emit when (current_replicas, kv_capacity_tokens,
        # capacity_per_replica) changes.
        _w_raw = []
        for entry in wva_so_scrapes.get(so_id, []):
            r = {"t": (entry["t_epoch"] - lse), "source": "wva"}
            if "current_replicas" in entry:
                r["current_replicas"] = entry["current_replicas"]
            if "kv_capacity_tokens" in entry:
                r["kv_capacity_tokens"] = entry["kv_capacity_tokens"]
            if "capacity_per_replica" in entry:
                r["capacity_per_replica"] = entry["capacity_per_replica"]
            _w_raw.append(r)
        wva_replicas = _dedup_replicas(_w_raw, ("current_replicas", "kv_capacity_tokens", "capacity_per_replica"))

        replicas = sorted(harness_replicas + wva_replicas, key=lambda x: x["t"])

        # Scaler events for this SO
        events = [e for e in scaler_events if e.get("so_id") == so_id]

        cfg = so_config.get(so_id, so_config.get(deploy_name, {}))
        engine_cfg = engine_config_for_so(deploy_name, pod_scrapes_by_pod)

        # SO-to-model mapping from WVA scrapes
        model_name = None
        for entry in wva_so_scrapes.get(so_id, []):
            if entry.get("model_name"):
                model_name = entry["model_name"]
                break

        result.append({
            "so_id": so_id,
            "config": {
                "endpoint_id": endpoint_url,
                "model": model_name,
                "gpu_count": cfg.get("gpu_count"),
                "role": cfg.get("role"),
                "cost": cfg.get("cost"),
                "min_replicas": cfg.get("min_replicas"),
                "max_replicas": cfg.get("max_replicas"),
                "engine_config": engine_cfg,
            },
            "replicas": replicas,
            "scaler": events,
        })

    # Coverage checks
    harness_rows = [r for r in replica_ts]
    if harness_rows:
        coverage.add("global", "Replica timeseries present", "PASS", f"{len(harness_rows)} rows")
    else:
        coverage.add("global", "Replica timeseries present", "FAIL", "No replica snapshot data found")

    has_decisions = any(e["event_type"] == "scaling_decision" for e in scaler_events)
    if has_decisions:
        n = sum(1 for e in scaler_events if e["event_type"] == "scaling_decision")
        coverage.add("global", "Scaling-decision log present", "PASS", f"{n} events")
    else:
        coverage.add("global", "Scaling-decision log present", "FAIL", "No scaling-decision events in controller log")

    has_scaledown = any(
        e["event_type"] == "scaling_decision" and
        str(e.get("action", "")).lower() in ("scaledown", "scale-down", "scale_down")
        for e in scaler_events
    )
    if not has_scaledown:
        coverage.add("global", "Scale-down present", "WARN", "No ScaleDown action observed in log")

    if len(so_ids) > 1:
        coverage.add("global", "Multi-variant run", "PASS", f"{len(so_ids)} SOs: {', '.join(sorted(so_ids))}")

    # Compute replica_scrape_end_t: latest t across all replica entries emitted
    replica_scrape_end_t = None
    for so in result:
        for r in so.get("replicas", []):
            t = r.get("t")
            if t is not None and (replica_scrape_end_t is None or t > replica_scrape_end_t):
                replica_scrape_end_t = t

    return result, sorted(so_ids), replica_scrape_end_t


# ---------------------------------------------------------------------------
# Sub-task 6 — Per-request data
# ---------------------------------------------------------------------------

def iter_json_objects(path, limit=None):
    """Stream JSON objects from a file that is either a JSON array or newline-delimited JSON."""
    count = 0
    with open(path, errors="replace") as f:
        # Peek at first non-whitespace char
        first_char = ""
        while True:
            ch = f.read(1)
            if not ch:
                return
            if ch.strip():
                first_char = ch
                break

        if first_char == "[":
            # JSON array — track brace depth line by line.
            # A line ending with '{' opens a new object/nested object.
            # A line that is exactly '}' or '},' closes one.
            lines = []
            depth = 0
            for line in f:
                raw = line.strip()
                if not raw:
                    continue
                bare = raw.rstrip(",")
                if bare == "{" and depth == 0:
                    depth = 1
                    lines = [bare]
                    continue
                if depth > 0:
                    lines.append(raw)
                    # Opening: line ends with '{' (e.g. '"info": {')
                    if bare.endswith("{"):
                        depth += 1
                    # Closing: line is just '}' or '},'
                    elif bare == "}":
                        depth -= 1
                        if depth == 0:
                            lines[-1] = bare
                            try:
                                obj = json.loads("\n".join(lines))
                                yield obj
                                count += 1
                                if limit and count >= limit:
                                    return
                            except json.JSONDecodeError:
                                pass
                            lines = []
        else:
            # Newline-delimited JSON (NDJSON)
            line = first_char + f.readline()
            while line:
                line = line.strip()
                if line:
                    try:
                        yield json.loads(line)
                        count += 1
                        if limit and count >= limit:
                            return
                    except json.JSONDecodeError:
                        pass
                line = f.readline()


def find_per_request_files(run_dir, harness):
    """Return (source_type, [paths]).

    source_type: 'guidellm', 'inference-perf-combined', 'none'.

    For inference-perf the per_request_lifecycle_metrics.json file is the flat
    per-request list. Stage files (stage_N_lifecycle_metrics.json) contain only
    aggregate summaries per stage, not individual request records — they are not
    used as a per-request source.
    """
    if harness == "guidellm":
        path = os.path.join(run_dir, "results.json")
        if os.path.isfile(path):
            return "guidellm", [path]
        return "none", []

    # inference-perf: per_request_lifecycle_metrics.json is the per-request source
    combined = os.path.join(run_dir, "per_request_lifecycle_metrics.json")
    if os.path.isfile(combined):
        return "inference-perf-combined", [combined]

    return "none", []


def read_guidellm_requests(path, limit=None):
    """Read guidellm results.json; returns list of request dicts with epoch timestamps.

    Supports two layouts:
    - Nested: {"benchmarks": [{"requests": {"successful": [...], "errored": [...], ...}}]}
    - Flat array / NDJSON of individual request objects with "request_id" at top level
    """
    data = read_json(path)
    if data is None:
        return []

    # Nested layout: benchmarks[*].requests.{successful,errored,...}
    raw_list = []
    benchmarks = data.get("benchmarks") if isinstance(data, dict) else None
    if benchmarks and isinstance(benchmarks, list):
        for bm in benchmarks:
            req_buckets = bm.get("requests", {}) if isinstance(bm, dict) else {}
            if isinstance(req_buckets, dict):
                for bucket in req_buckets.values():
                    if isinstance(bucket, list):
                        raw_list.extend(bucket)
            elif isinstance(req_buckets, list):
                raw_list.extend(req_buckets)
    elif isinstance(data, list):
        raw_list = data
    elif isinstance(data, dict) and "request_id" in data:
        raw_list = [data]

    records = []
    for r in raw_list:
        if not isinstance(r, dict):
            continue
        info = r.get("info", {}) or {}
        timings = info.get("timings", {}) or {}
        in_m = r.get("input_metrics", {}) or {}
        out_m = r.get("output_metrics", {}) or {}

        start = (info.get("started_at") or timings.get("request_start")
                 or r.get("start_time") or r.get("t_arr"))
        end = (info.get("completed_at") or timings.get("request_end")
               or r.get("end_time") or r.get("t_dep"))

        first_tok = timings.get("first_token_iteration") or timings.get("first_output_token_iteration")
        ttft_ms = ((first_tok - start) * 1000) if (first_tok and start) else (
            r.get("time_to_first_token_ms") or
            (r.get("ttft") * 1000 if r.get("ttft") else None))

        in_tok = (in_m.get("total_tokens") or in_m.get("text_tokens")
                  or r.get("prompt_tokens") or r.get("input_tokens"))
        out_tok = (out_m.get("total_tokens") or out_m.get("text_tokens")
                   or r.get("output_tokens") or r.get("generated_tokens"))

        outcome = "error" if (info.get("error") or r.get("error")) else "success"

        records.append({
            "t_arr_epoch": start,
            "t_dep_epoch": end,
            "ttft_ms": ttft_ms,
            "itl_ms": r.get("inter_token_latency_ms") or
                      (r.get("itl") * 1000 if r.get("itl") else None),
            "in_tok": in_tok,
            "out_tok": out_tok,
            "outcome": outcome,
        })
        if limit and len(records) >= limit:
            break
    return records


def read_inference_perf_requests(paths, limit=None):
    """Read inference-perf stage or combined lifecycle_metrics.json files.

    Returns list of request dicts with monotonic timestamps (t_arr_mono, t_dep_mono).
    """
    records = []
    total = 0
    for path in paths:
        for obj in iter_json_objects(path):
            start = obj.get("request_start_time") or obj.get("start_time")
            end = obj.get("request_end_time") or obj.get("end_time")
            ttft_raw = obj.get("time_to_first_token") or obj.get("ttft")
            records.append({
                "t_arr_mono": start,
                "t_dep_mono": end,
                "ttft_ms": (ttft_raw * 1000) if ttft_raw else None,
                "itl_ms": obj.get("inter_token_latency_ms"),
                "in_tok": obj.get("input_token_count") or obj.get("prompt_tokens"),
                "out_tok": obj.get("output_token_count") or obj.get("generated_tokens"),
                "outcome": "error" if obj.get("error") else "success",
            })
            total += 1
            if limit and total >= limit:
                return records
    return records


def apply_time_anchor(requests, anchor, load_start_epoch):
    """Apply time anchor offset to inference-perf monotonic timestamps → epoch."""
    offset = anchor.get("offset_s", 0.0)
    for r in requests:
        if r.get("t_arr_mono") is not None:
            r["t_arr_epoch"] = r["t_arr_mono"] + offset
        if r.get("t_dep_mono") is not None:
            r["t_dep_epoch"] = r["t_dep_mono"] + offset
    return requests


def requests_to_buckets(requests, source, load_start_epoch,
                        bucket_width=BUCKET_WIDTH_S, fast_threshold_s=2.0):
    """Aggregate per-request records into fixed-width time buckets.

    One streaming pass — no full in-memory sort required.  Each bucket is
    keyed on the departure time (t_dep) so the bucket represents work that
    *completed* in that window, matching the viz's departure-rate panels.

    Bucket schema:
      t           — bucket midpoint (seconds from load start)
      arr_rate    — arrivals/s in this bucket
      dep_rate    — departures/s in this bucket
      in_system   — mean requests in-flight (arrivals - departures, running sum)
      n_waiting   — count of records with a recorded vLLM queue wait
      frac_fast   — fraction of departures with e2e < fast_threshold_s
      ttft_p50    — median TTFT ms of departing requests (null if none have ttft)
      source      — "inference-perf" | "guidellm"
    """
    lse = load_start_epoch or 0.0
    w = bucket_width

    # Accumulators keyed by bucket index
    arr_counts = defaultdict(int)    # arrivals per bucket
    dep_counts = defaultdict(int)    # departures per bucket
    fast_counts = defaultdict(int)   # departures with e2e < threshold
    wait_counts = defaultdict(int)   # departures with a ttft value
    ttft_lists = defaultdict(list)   # ttft_ms values per bucket (for p50)

    # Track in-system count at each departure using a running balance
    # We process in arrival order to maintain the balance correctly.
    # First pass: collect all events sorted by time.
    events = []  # (t_rel, kind, extra)  kind: 'arr'|'dep'
    for r in requests:
        arr = r.get("t_arr_epoch")
        dep = r.get("t_dep_epoch")
        if arr is not None:
            events.append((arr - lse, "arr", None))
        if dep is not None:
            e2e_s = ((dep - arr) if arr is not None else None)
            ttft = r.get("ttft_ms")
            events.append((dep - lse, "dep", (e2e_s, ttft)))
    events.sort(key=lambda x: x[0])

    in_system = 0
    in_system_sum = defaultdict(float)
    in_system_n = defaultdict(int)

    for t_rel, kind, extra in events:
        bk = int(t_rel / w)
        if kind == "arr":
            in_system += 1
            arr_counts[bk] += 1
        else:
            in_system = max(0, in_system - 1)
            dep_counts[bk] += 1
            e2e_s, ttft = extra
            if e2e_s is not None and e2e_s < fast_threshold_s:
                fast_counts[bk] += 1
            if ttft is not None:
                wait_counts[bk] += 1
                ttft_lists[bk].append(ttft)
        in_system_sum[bk] += in_system
        in_system_n[bk] += 1

    if not dep_counts and not arr_counts:
        return []

    all_buckets = sorted(set(arr_counts) | set(dep_counts))
    result = []
    for bk in all_buckets:
        t_mid = (bk + 0.5) * w
        dc = dep_counts.get(bk, 0)
        ac = arr_counts.get(bk, 0)
        n = in_system_n.get(bk, 1)
        ttfts = ttft_lists.get(bk, [])
        ttft_p50 = _pct(ttfts, 50) if ttfts else None
        result.append({
            "t": round(t_mid, 3),
            "arr_rate": round(ac / w, 4),
            "dep_rate": round(dc / w, 4),
            "in_system": round(in_system_sum.get(bk, 0.0) / n, 3),
            "n_waiting": wait_counts.get(bk, 0),
            "frac_fast": round(fast_counts.get(bk, 0) / dc, 4) if dc > 0 else None,
            "ttft_p50": round(ttft_p50, 2) if ttft_p50 is not None else None,
            "source": source,
        })
    return result




# ---------------------------------------------------------------------------
# Sub-task 7 — EPP data → endpoints.json
# ---------------------------------------------------------------------------

_EPP_FILE_RE = re.compile(r'epp', re.IGNORECASE)

_EPP_METRICS = {
    "q_dispatch": "inference_extension_flow_control_queue_size",
    "pool_avg": "inference_pool_average_queue_size",
    "ready_pods": "inference_pool_ready_pods",
    "kv_mean": "inference_pool_average_kv_cache_utilization",
    "throughput_req_total": "inference_objective_request_total",
}


def parse_epp_scrape(path):
    """Parse one EPP metrics.log file.

    Returns {unauthorized: bool, fields...} or {unauthorized: True}.
    """
    try:
        with open(path, errors="replace") as f:
            text = f.read()
    except OSError:
        return {"unauthorized": True}

    non_comment = [l.strip() for l in text.splitlines()
                   if l.strip() and not l.strip().startswith("#")]
    if not non_comment:
        return {"unauthorized": True}
    if len(non_comment) == 1 and non_comment[0] == "Unauthorized":
        return {"unauthorized": True}

    result = {"unauthorized": False}
    pod_queues = {}
    for line in non_comment:
        for field, metric in _EPP_METRICS.items():
            if line.startswith(metric) and line[len(metric)] in "{ ":
                v = prom_val(line)
                if v is not None:
                    result[field] = v
                break
        # Per-pod queue
        if line.startswith("inference_pool_per_pod_queue_size{"):
            pod = prom_label(line, "model_server_pod") or prom_label(line, "pod")
            v = prom_val(line)
            if pod and v is not None:
                pod_queues[pod] = v
    if pod_queues:
        result["q_engine_sum"] = sum(pod_queues.values())
        result["q_engine_avg"] = sum(pod_queues.values()) / len(pod_queues)
        result["pod_queues"] = pod_queues
    return result


def scan_epp_scrapes(run_dir):
    """Return [(t_epoch, epp_dict)], sorted by time."""
    raw_dir = os.path.join(run_dir, "metrics", "raw")
    if not os.path.isdir(raw_dir):
        return []
    results = []
    for fname in os.listdir(raw_dir):
        if not fname.endswith("_metrics.log"):
            continue
        if not _EPP_FILE_RE.search(fname):
            continue
        m = _FILE_RE.match(fname)
        if not m:
            continue
        ts = int(m.group("ts"))
        path = os.path.join(raw_dir, fname)
        epp = parse_epp_scrape(path)
        results.append((ts, epp))
    return sorted(results, key=lambda x: x[0])


def find_epp_name(run_dir):
    """Return the EPP Deployment name (strip hash from first EPP pod filename)."""
    raw_dir = os.path.join(run_dir, "metrics", "raw")
    if not os.path.isdir(raw_dir):
        return None
    for fname in sorted(os.listdir(raw_dir)):
        if not fname.endswith("_metrics.log"):
            continue
        if not _EPP_FILE_RE.search(fname):
            continue
        m = _FILE_RE.match(fname)
        if m:
            return so_id_from_pod(m.group("pod"))
    return None


def compute_in_system(requests, scrape_times, load_start_epoch):
    """Compute L(t) concurrency at each scrape time from request events.

    Returns {t_epoch: concurrency}.
    """
    if not requests:
        return {}
    lse = load_start_epoch or 0.0
    events = []
    for r in requests:
        arr = r.get("t_arr")
        dep = r.get("t_dep")
        if arr is not None:
            events.append((arr + lse, +1))
        if dep is not None:
            events.append((dep + lse, -1))
    events.sort()

    result = {}
    for ts in scrape_times:
        conc = sum(d for t, d in events if t <= ts)
        result[ts] = max(0, conc)
    return result


def build_epp_series(raw_epp, in_system_map, load_start_epoch):
    """Build contract epp[] from raw EPP scrapes."""
    lse = load_start_epoch or 0.0
    prev_req_total = None
    prev_t = None
    series = []
    for t_epoch, epp in raw_epp:
        t = t_epoch - lse
        unauth = epp.get("unauthorized", True)
        throughput_rps = None
        if not unauth:
            cur_total = epp.get("throughput_req_total")
            if cur_total is not None and prev_req_total is not None and prev_t is not None:
                dt = t - prev_t
                if dt > 0:
                    throughput_rps = (cur_total - prev_req_total) / dt
            prev_req_total = epp.get("throughput_req_total")
            prev_t = t

        entry = {
            "t": t,
            "q_dispatch": None if unauth else epp.get("q_dispatch"),
            "q_engine_sum": None if unauth else epp.get("q_engine_sum"),
            "q_engine_avg": None if unauth else epp.get("q_engine_avg"),
            "pool_avg": None if unauth else epp.get("pool_avg"),
            "ready_pods": None if unauth else epp.get("ready_pods"),
            "kv_mean": None if unauth else epp.get("kv_mean"),
            "throughput_rps": throughput_rps,
            "in_system": in_system_map.get(t_epoch),
        }
        series.append(entry)
    return series


def build_load_stages(stages, load_start_epoch):
    """Convert raw stage list to contract load[] with t_start/t_end."""
    lse = load_start_epoch or 0.0
    result = []
    cursor = 0.0
    for s in stages:
        dur = s.get("duration_s", 0.0)
        result.append({
            "t_start": cursor,
            "t_end": cursor + dur,
            "rate_rps": s.get("rate_rps"),
            "in_tok": s.get("in_tok_mean"),
            "out_tok": s.get("out_tok_mean"),
        })
        cursor += dur
    return result


def build_endpoints(metadata, stages, epp_scrapes, epp_name, ep_demand,
                    so_ids, requests_contract, load_start_epoch, coverage):
    """Assemble endpoints.json list."""
    lse = load_start_epoch or 0.0
    endpoint_url = metadata.get("endpoint_url", "")
    model_id = metadata.get("model", "")

    in_system_map = {}
    if requests_contract:
        scrape_times = [ts for ts, _ in epp_scrapes]
        in_system_map = compute_in_system(requests_contract, scrape_times, lse)

    epp_series = build_epp_series(epp_scrapes, in_system_map, lse)
    load_contract = build_load_stages(stages, lse)

    # Build demand[] timeseries from WVA Prometheus ep-scoped signals
    demand = []
    for d in sorted(ep_demand, key=lambda x: x["t_epoch"]):
        if d.get("model_name") == model_id or not model_id:
            demand.append({
                "t": d["t_epoch"] - lse,
                "source": "wva",
                "analyzer": d.get("analyzer"),
                "role": d.get("role") or None,
                "value": d.get("value"),
                "unit": d.get("unit"),
            })

    # Coverage check
    ep_scope = f"endpoint:{endpoint_url}"
    all_unauth = all(e.get("unauthorized", True) for _, e in epp_scrapes)
    if not epp_scrapes:
        coverage.add(ep_scope, "EPP metrics present", "FAIL", "No EPP scrape files found")
    elif all_unauth:
        coverage.add(ep_scope, "EPP metrics present", "FAIL",
                     f"All {len(epp_scrapes)} EPP scrapes returned Unauthorized")
    else:
        coverage.add(ep_scope, "EPP metrics present", "PASS",
                     f"{len(epp_scrapes)} EPP scrapes")

    return [{
        "config": {
            "endpoint_id": endpoint_url,
            "model_id": model_id,
            "epp_name": epp_name,
            "scaled_object_ids": sorted(so_ids),
        },
        "load": load_contract,
        "demand": demand,
        "epp": epp_series,
    }]


# ---------------------------------------------------------------------------
# Sub-task 8 — Coverage, InputReport, OutputReport
# ---------------------------------------------------------------------------

class Coverage:
    """Accumulator for coverage.json rows."""

    def __init__(self):
        self._rows = []

    def add(self, scope, capability, result, detail=""):
        self._rows.append({
            "scope": scope,
            "capability": capability,
            "result": result,
            "detail": detail,
        })

    def to_dict(self):
        rows = list(self._rows)
        return {
            "rows": rows,
            "n_pass": sum(1 for r in rows if r["result"] == "PASS"),
            "n_fail": sum(1 for r in rows if r["result"] == "FAIL"),
            "n_warn": sum(1 for r in rows if r["result"] == "WARN"),
            "warnings": [r["detail"] for r in rows if r["result"] in ("FAIL", "WARN")],
        }


class InputReport:
    """Accumulator for extract_input_report.json checks."""

    def __init__(self, run_dirs):
        self._run_dirs = list(run_dirs)
        self._checks = []

    def add(self, source, status, path=None, fallback_path=None, note=""):
        """status: found | missing | fallback | partial"""
        self._checks.append({
            "source": source,
            "status": status,
            "path": path,
            "fallback_path": fallback_path,
            "note": note,
        })

    def to_dict(self):
        return {
            "generated_at": _now_iso(),
            "run_dirs": self._run_dirs,
            "checks": self._checks,
        }


class OutputReport:
    """Accumulator for extract_output_report.json."""

    def __init__(self):
        self._outputs = []

    def add(self, file, status, record_count=None, null_fields=None, note=""):
        """status: written | empty | partial | absent"""
        self._outputs.append({
            "file": file,
            "status": status,
            "record_count": record_count,
            "null_fields": null_fields or [],
            "note": note,
        })

    def to_dict(self):
        return {
            "generated_at": _now_iso(),
            "outputs": self._outputs,
        }


def _null_fields_sample(records, fields):
    """Return list of field names that are null in all records."""
    if not records:
        return list(fields)
    return [f for f in fields if all(r.get(f) is None for r in records)]


def backfill_ttft_p50(requests_buckets, pods, load_start_epoch):
    """Back-fill ttft_p50 in requests_buckets using pod-scrape TTFT histograms.

    For inference-perf runs the harness does not record per-request TTFT, so
    ttft_p50 is null in requests_buckets.  This function fills it in by finding
    the pod-series interval that covers each request bucket's midpoint and
    computing ttft_p50 via linear interpolation of the delta histogram.

    Modifies requests_buckets in place.  Buckets already having a non-null
    ttft_p50 (from guidellm) are left unchanged.

    Returns the number of buckets that were filled.
    """
    if not requests_buckets or not pods:
        return 0

    # Build a list of (t_right_edge, dt, hist) from all pod series entries
    # that have a non-null ttft_hist.  t values are already seconds from load start.
    intervals = []
    for pod in pods:
        for entry in pod.get("series", []):
            hist = entry.get("ttft_hist")
            if hist is None:
                continue
            t = entry.get("t")
            # Reconstruct dt: look for consecutive entries on the same pod.
            # We store dt alongside the interval for coverage.
            # Since pod_series doesn't store dt directly, approximate from the
            # series itself — consecutive entries with the same t spacing.
            intervals.append((t, hist))

    if not intervals:
        return 0

    # Sort intervals by t (right edge of scrape interval)
    intervals.sort(key=lambda x: x[0])

    # Estimate dt for each interval as the gap to the previous one.
    # For the first interval, use the gap to the next one.
    # We need (t_left, t_right, hist) triples.
    dt_intervals = []
    for i, (t, hist) in enumerate(intervals):
        if i == 0:
            if len(intervals) > 1:
                dt = intervals[1][0] - t
            else:
                dt = 30.0  # fallback: 30s default scrape interval
        else:
            dt = t - intervals[i - 1][0]
        if dt <= 0:
            dt = 30.0
        t_left = t - dt
        dt_intervals.append((t_left, t, hist))

    filled = 0
    for bkt in requests_buckets:
        if bkt.get("ttft_p50") is not None:
            continue  # already set (e.g. guidellm)
        # Only fill buckets that have actual departures; arrival-only buckets
        # have no TTFT observation by definition.
        if not bkt.get("dep_rate"):
            continue
        t_mid = bkt.get("t")
        if t_mid is None:
            continue
        # Find the interval that covers t_mid
        hist = None
        for t_left, t_right, h in dt_intervals:
            if t_left <= t_mid <= t_right:
                hist = h
                break
        if hist is None:
            # Nearest interval fallback: use closest right-edge
            best_dist = None
            for t_left, t_right, h in dt_intervals:
                dist = min(abs(t_mid - t_left), abs(t_mid - t_right))
                if best_dist is None or dist < best_dist:
                    best_dist = dist
                    hist = h
        if hist is None:
            continue
        le_list = hist.get("le", [])
        n_list = hist.get("n", [])
        if not le_list or not n_list:
            continue
        p50_s = _hist_percentile(le_list, n_list, 50)
        if p50_s is None:
            continue
        bkt["ttft_p50"] = round(p50_s * 1000, 2)  # seconds → ms
        bkt["source"] = "vllm-scrape"
        filled += 1

    return filled


# ---------------------------------------------------------------------------
# Sub-task 9 — main()
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(
        description="Extract benchmark run directory(ies) into the viz bundle.")
    ap.add_argument("--run", required=True, action="append", metavar="DIR",
                    help="Benchmark run directory (repeat for multiple runs)")
    ap.add_argument("--out", default=None, metavar="DIR",
                    help="Output directory (default: <run_id>/extract/)")
    ap.add_argument("--bucket-width", type=float, default=BUCKET_WIDTH_S, metavar="S",
                    help=f"Bucket width in seconds for requests.json (default: {BUCKET_WIDTH_S})")
    ap.add_argument("--quiet", action="store_true",
                    help="Suppress informational output")
    args = ap.parse_args()

    run_dirs = [os.path.abspath(r) for r in args.run]
    for rd in run_dirs:
        if not os.path.isdir(rd):
            print(f"ERROR: {rd} is not a directory", file=sys.stderr)
            sys.exit(1)

    # Default output: <run_id>/extract/
    # run_dirs[0] is typically <run_id>/results/<run-name>
    if args.out:
        out_dir = args.out
    else:
        run_id = os.path.dirname(os.path.dirname(run_dirs[0]))
        out_dir = os.path.join(run_id, "extract")
    quiet = args.quiet

    def log(msg):
        if not quiet:
            print(msg)

    coverage = Coverage()
    input_report = InputReport(run_dirs)
    output_report = OutputReport()

    # --- Always write provenance first so partial runs leave a record ---
    # Use first run dir for provenance; updated at end with full info.
    write_provenance(out_dir, run_dirs[0], harness="unknown", version=_EXTRACT_VERSION)

    # Currently single-run extraction; multi-run joining is a future extension.
    # With multiple --run args, use the first run as primary and warn.
    run_dir = run_dirs[0]
    if len(run_dirs) > 1:
        warn(f"Multiple --run dirs provided; only the first is processed in this version: {run_dir}")

    # --- Run metadata ---
    log("Reading run metadata...")
    meta_path = os.path.join(run_dir, "run_metadata.yaml")
    if os.path.isfile(meta_path):
        input_report.add("run_metadata.yaml", "found", path=meta_path)
    else:
        input_report.add("run_metadata.yaml", "missing", path=meta_path,
                         note="Required file not found; most fields will be empty")
    metadata = read_run_metadata(run_dir)
    harness = metadata["harness"]
    endpoint_url = metadata.get("endpoint_url", "unknown")

    # --- Workload YAML and load stages ---
    log("Reading workload YAML...")
    yaml_path = find_workload_yaml(run_dir, metadata)
    if yaml_path:
        input_report.add("workload scenario YAML", "found", path=yaml_path)
    else:
        input_report.add("workload scenario YAML", "missing",
                         note="No scenario YAML found; load stages will be empty")
    stages = read_load_stages(yaml_path)

    # --- Load start ---
    load_start_epoch, prewarm_s = compute_load_start(
        metadata["harness_start_epoch"], stages)

    # --- Confirm model from WVA ---
    wva_model = confirm_model_from_wva(run_dir, metadata.get("model", ""))

    # --- Pod scrapes ---
    log("Scanning pod scrapes...")
    pod_scrapes = scan_pod_scrapes(run_dir)
    pod_timings = read_pod_timings(run_dir)
    if pod_scrapes:
        input_report.add("pod Prometheus scrapes", "found",
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note=f"{len(pod_scrapes)} pods")
    else:
        input_report.add("pod Prometheus scrapes", "missing",
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note="No vLLM pod scrape files found")
    _pod_timings_primary = os.path.join(run_dir, "metrics", "processed", "wva_pod_timings.json")
    _pod_timings_fallback = os.path.join(run_dir, "metrics", "processed", "replica_status_timeseries.json")
    if os.path.isfile(_pod_timings_primary):
        input_report.add("pod timings", "found", path=_pod_timings_primary,
                         note=f"{len(pod_timings)} pods with timing data")
    elif pod_timings:
        input_report.add("pod timings", "fallback", path=_pod_timings_fallback,
                         note=f"derived from replica_status_timeseries.json; {len(pod_timings)} pods")
    else:
        input_report.add("pod timings", "missing", path=_pod_timings_primary,
                         note="created_t/ready_t will be null on all pods")

    # --- WVA Prometheus scrapes ---
    log("Scanning WVA controller scrapes...")
    wva_so_scrapes, ep_demand = scan_wva_scrapes(run_dir)
    if wva_so_scrapes or ep_demand:
        input_report.add("WVA controller Prometheus scrapes", "found",
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note=f"{len(wva_so_scrapes)} SOs, {len(ep_demand)} demand points")
    else:
        input_report.add("WVA controller Prometheus scrapes", "missing",
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note="No wva-controller scrape files found")

    # --- Replica timeseries + SO config ---
    log("Reading replica timeseries...")
    processed_dir = os.path.join(run_dir, "metrics", "processed")
    p1 = os.path.join(processed_dir, "replica_status_timeseries.json")
    p2 = os.path.join(processed_dir, "wva_replica_samples.json")
    replica_ts = read_replica_timeseries(run_dir)
    if replica_ts:
        used = p1 if os.path.isfile(p1) else p2
        fallback = p2 if used == p1 else None
        input_report.add("replica timeseries", "found", path=used,
                         fallback_path=fallback if fallback and os.path.isfile(fallback) else None,
                         note=f"{len(replica_ts)} rows")
    else:
        if os.path.isfile(p1):
            input_report.add("replica timeseries", "partial", path=p1,
                             fallback_path=p2 if os.path.isfile(p2) else None,
                             note="File present but no controller entries (label-mismatch bug)")
        else:
            input_report.add("replica timeseries", "missing", path=p1,
                             note="Neither replica_status_timeseries.json nor wva_replica_samples.json found")
    so_config = read_so_config(run_dir)
    _so_cfg_path = os.path.join(run_dir, "scaledobject-config.json")
    _bench_meta_path = os.path.join(run_dir, "wva", "bench-meta.json")
    if os.path.isfile(_so_cfg_path):
        input_report.add("SO config", "found", path=_so_cfg_path,
                         note=f"{len(so_config)} SOs")
    elif so_config:
        input_report.add("SO config", "fallback", path=_bench_meta_path,
                         note=f"derived from wva/bench-meta.json stacks[]; {len(so_config)} SOs; "
                              "gpu_count/role/cost null (not in bench-meta)")
    else:
        input_report.add("SO config", "missing", path=_so_cfg_path,
                         note="gpu_count, role, cost, min/max_replicas will be null")

    # --- Controller log ---
    log("Parsing controller log...")
    ctrl_log_path = find_controller_log(run_dir)
    if ctrl_log_path:
        input_report.add("WVA controller text log", "found", path=ctrl_log_path)
    else:
        input_report.add("WVA controller text log", "missing",
                         note="scaler[] events will be empty; scaling decisions not captured")
    scaler_events = parse_controller_log(
        ctrl_log_path, load_start_epoch,
        metadata.get("harness_start_epoch"),
        metadata.get("harness_stop_epoch"),
    )

    # --- SO identity resolution ---
    log("Resolving SO identity map...")
    so_names_from_wva = set(wva_so_scrapes.keys())
    for e in scaler_events:
        if e.get("so_id"):
            so_names_from_wva.add(e["so_id"])
    deploy_names_from_pods = set(so_id_from_pod(p) for p in pod_scrapes)
    deploy_names_from_replicas = set(r["so_id"] for r in replica_ts if r.get("so_id"))
    all_deploy_names = deploy_names_from_pods | deploy_names_from_replicas
    identity_map = build_identity_map(
        so_names=sorted(so_names_from_wva),
        deploy_names=sorted(all_deploy_names),
        so_config=so_config,
        ctrl_log_path=ctrl_log_path,
        input_report=input_report,
    )

    # --- Per-request data (always attempted; best available source) ---
    requests_buckets = None   # aggregated 1s-bucket timeseries written to requests.json
    raw_requests_for_anchor = []
    time_anchor = {"method": "not-needed", "offset_s": 0.0,
                   "corr": None, "trustworthy": True,
                   "n_scrapes": 0, "shift_from_guess_s": 0.0}

    bw = args.bucket_width
    log("Loading per-request data...")
    source_type, req_paths = find_per_request_files(run_dir, harness)
    ep_scope = f"endpoint:{endpoint_url}"
    if source_type == "inference-perf-combined":
        input_report.add("per-request lifecycle data", "found",
                         path=req_paths[0], note=f"source={source_type}")
        raw_requests_for_anchor = read_inference_perf_requests(req_paths)
        log("Computing time anchor...")
        time_anchor = compute_time_anchor(harness, raw_requests_for_anchor, pod_scrapes)
        apply_time_anchor(raw_requests_for_anchor, time_anchor, load_start_epoch)
        requests_buckets = requests_to_buckets(
            raw_requests_for_anchor, "inference-perf", load_start_epoch, bucket_width=bw)
        coverage.add(ep_scope, "Per-request trace present", "PASS",
                     f"{len(requests_buckets)} buckets ({bw}s) from {source_type}")
    elif source_type == "guidellm":
        input_report.add("per-request lifecycle data", "found",
                         path=req_paths[0], note="source=guidellm")
        raw_req = read_guidellm_requests(req_paths[0])
        requests_buckets = requests_to_buckets(
            raw_req, "guidellm", load_start_epoch, bucket_width=bw)
        coverage.add(ep_scope, "Per-request trace present", "PASS",
                     f"{len(requests_buckets)} buckets ({bw}s) from guidellm")
    else:
        # No native harness file — EPP log is the next best source (currently
        # empty in all known captures; gap for runtools).
        epp_log_path = os.path.join(run_dir, "logs", "epp_pods.log")
        epp_log_size = os.path.getsize(epp_log_path) if os.path.isfile(epp_log_path) else 0
        if epp_log_size > 0:
            # Future: parse EPP log for request arrival/departure timestamps.
            # For now, report as not yet implemented.
            input_report.add("per-request lifecycle data", "fallback",
                             path=epp_log_path,
                             note="EPP log present but parsing not yet implemented")
            coverage.add(ep_scope, "Per-request trace present", "WARN",
                         "EPP log present but not yet parsed; requests.json absent")
        else:
            input_report.add("per-request lifecycle data", "missing",
                             note="No native harness file; EPP log absent or empty "
                                  "(runtools gap — epp_pods.log not populated)")
            coverage.add(ep_scope, "Per-request trace present", "FAIL",
                         "No per-request source available; requests.json absent")

    if not time_anchor.get("trustworthy", True):
        coverage.add("global", "Time anchor trustworthy", "WARN",
                     f"method={time_anchor.get('method')}, corr={time_anchor.get('corr')}")

    # --- EPP scrapes ---
    log("Scanning EPP scrapes...")
    epp_scrapes = scan_epp_scrapes(run_dir)
    epp_name = find_epp_name(run_dir)
    if epp_scrapes:
        all_unauth = all(e.get("unauthorized", True) for _, e in epp_scrapes)
        status = "partial" if all_unauth else "found"
        input_report.add("EPP Prometheus scrapes", status,
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note=f"{len(epp_scrapes)} files; all Unauthorized" if all_unauth
                              else f"{len(epp_scrapes)} files")
    else:
        input_report.add("EPP Prometheus scrapes", "missing",
                         path=os.path.join(run_dir, "metrics", "raw"),
                         note="No EPP scrape files found")

    # --- Assemble meta.json (written again after scaled_objects to include sentinels) ---
    # First pass: write without sentinels so file exists during partial runs.
    # Second pass (below, after scaled_objects) overwrites with full sentinels.
    log("Writing meta.json...")
    _meta_no_sentinels = build_meta(metadata, stages, load_start_epoch, prewarm_s,
                                    time_anchor, wva_model)
    write_bundle(out_dir, "meta.json", _meta_no_sentinels)
    output_report.add("meta.json", "written", record_count=1)

    # --- Assemble pods.json ---
    log("Writing pods.json...")
    pods = build_pods(pod_scrapes, pod_timings, load_start_epoch, identity_map, coverage)
    if pods:
        null_f = _null_fields_sample(
            [s for p in pods for s in p.get("series", [])],
            ["run", "wait", "kv", "ok_rate", "gen_rate", "ttft_ms", "e2e_ms",
             "itl_ms", "prefill_ms", "qwait_s"])
        output_report.add("pods.json", "written" if pods else "empty",
                          record_count=len(pods),
                          null_fields=null_f,
                          note=f"{sum(len(p['series']) for p in pods)} series entries")
    else:
        output_report.add("pods.json", "empty", record_count=0,
                          note="No pod scrape data available")
    write_bundle(out_dir, "pods.json", pods)

    # --- Assemble scaled_objects.json ---
    log("Writing scaled_objects.json...")
    scaled_objects, so_ids, replica_scrape_end_t = build_scaled_objects(
        replica_ts, so_config, scaler_events, wva_so_scrapes,
        pod_scrapes, load_start_epoch, endpoint_url, identity_map, coverage)
    write_bundle(out_dir, "scaled_objects.json", scaled_objects)
    so_null_f = _null_fields_sample(
        [e for so in scaled_objects for e in so.get("scaler", [])
         if e.get("event_type") == "analyzer_result"],
        ["rc", "sc", "prc"])
    output_report.add("scaled_objects.json",
                      "written" if scaled_objects else "empty",
                      record_count=len(scaled_objects),
                      null_fields=so_null_f)

    # --- Assemble endpoints.json ---
    log("Writing endpoints.json...")
    endpoints = build_endpoints(
        metadata, stages, epp_scrapes, epp_name, ep_demand,
        so_ids, None, load_start_epoch, coverage)
    write_bundle(out_dir, "endpoints.json", endpoints)
    ep_null_f = _null_fields_sample(
        [e for ep in endpoints for e in ep.get("epp", [])],
        ["q_dispatch", "q_engine_sum", "ready_pods", "kv_mean", "in_system"])
    output_report.add("endpoints.json", "written",
                      record_count=len(endpoints),
                      null_fields=ep_null_f)

    # --- requests.json ---
    if requests_buckets is not None:
        # Back-fill ttft_p50 from pod-scrape histograms for inference-perf runs
        # where the harness does not record per-request TTFT.
        if pods:
            n_filled = backfill_ttft_p50(requests_buckets, pods, load_start_epoch)
            if n_filled > 0:
                log(f"  ttft_p50 back-filled in {n_filled}/{len(requests_buckets)} request buckets from pod-scrape histograms")
        log("Writing requests.json...")
        write_bundle(out_dir, "requests.json", requests_buckets)
        output_report.add("requests.json", "written", record_count=len(requests_buckets),
                          note=f"aggregated {bw}s buckets")
    else:
        output_report.add("requests.json", "absent",
                          note="No per-request source available; see extract_input_report for details")

    # --- meta.json second pass: overwrite with sentinels now that all data is assembled ---
    load_duration_s = sum(s["duration_s"] for s in stages if s.get("rate_rps", 0) > 0)
    # last_departure_t: max t (bucket midpoint) across all bucket records
    last_dep = None
    if requests_buckets:
        for bkt in requests_buckets:
            td = bkt.get("t")
            if td is not None and (last_dep is None or td > last_dep):
                last_dep = td
    # last_scale_event_t: latest t across all scaler events
    last_scale = None
    for e in scaler_events:
        t = e.get("t")
        if t is not None and (last_scale is None or t > last_scale):
            last_scale = t
    sentinels = {
        "load_end_t": load_duration_s,
        "last_departure_t": last_dep,
        "last_scale_event_t": last_scale,
        "replica_scrape_end_t": replica_scrape_end_t,
    }
    meta = build_meta(metadata, stages, load_start_epoch, prewarm_s, time_anchor,
                      wva_model, sentinels=sentinels)
    write_bundle(out_dir, "meta.json", meta)

    # --- coverage.json ---
    log("Writing coverage.json...")
    write_bundle(out_dir, "coverage.json", coverage.to_dict())
    output_report.add("coverage.json", "written")

    # --- extract_input_report.json ---
    write_bundle(out_dir, "extract_input_report.json", input_report.to_dict())
    output_report.add("extract_input_report.json", "written",
                      record_count=len(input_report._checks))

    # --- extract_output_report.json ---
    write_bundle(out_dir, "extract_output_report.json", output_report.to_dict())

    # --- Final provenance ---
    write_provenance(out_dir, run_dir, harness, _EXTRACT_VERSION)

    log(f"\nBundle written to: {out_dir}")
    log(f"  Files: " + ", ".join(sorted(os.listdir(out_dir))))
    cov = coverage.to_dict()
    if cov["n_fail"] or cov["n_warn"]:
        log(f"  Coverage: {cov['n_pass']} PASS, {cov['n_fail']} FAIL, {cov['n_warn']} WARN")


if __name__ == "__main__":
    main()
