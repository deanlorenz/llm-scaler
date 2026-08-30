# Code Review: hack/benchmark/extract.py

Reviewed by background agent. All findings are reports only — nothing has been fixed.

---

## Summary Table

| Severity | Category | Line(s) | Issue |
|----------|----------|---------|-------|
| **HIGH** | Correctness | 705 | `qwait_s` uses `and` short-circuit — double-calls `_hist_mean_ms`, silently drops `0.0` |
| **HIGH** | Correctness | 923 | `remaining_deploy` compares strings to dicts — always evaluates to full list (logic error) |
| **HIGH** | Robustness | 552, 567, 575, 584, 589 | `line[len(name)]` raises `IndexError` when line exactly equals metric name |
| **HIGH** | Correctness | 1374 | `--head` limit divided per file, count resets each file — unpredictable total |
| **MEDIUM** | Correctness | 678 | Histogram sum `ds < 0` not checked — negative latencies possible on counter reset |
| **MEDIUM** | Robustness | 1259–1300 | JSON array parser counts `{`/`}` inside strings — silent corruption on error messages |
| **MEDIUM** | Contract | 705 | `qwait_s` type can be `False`/`0` (not `None` or float) — violates contract |
| **MEDIUM** | Code Quality | 844–856 | `_SCALE_TARGET_RE` and `_SCALE_TARGET_RE2` defined but never used — dead code |
| **MEDIUM** | Code Quality | 923 | `remaining_deploy` computed but never used — dead variable |
| **LOW** | Code Quality | 410, 436 | Magic numbers `5` and `0.2` in `compute_time_anchor` — no named constants |
| **LOW** | Robustness | 1331 | Stage file regex not anchored to end of string — could match unexpected filenames |

---

## Detailed Findings

### HIGH-1 — `qwait_s` double-call and boolean coercion (line 705)

**Location:** `pod_series()`, line 705

```python
"qwait_s": _hist_mean_ms("qwait") and (_hist_mean_ms("qwait") / 1000),
```

Two problems:
1. `_hist_mean_ms("qwait")` is called **twice**. If the result is `0.0` (valid measurement) the `and` operator short-circuits, returning `0` instead of `0.0`. The field becomes falsy `0` instead of `None` or float — violates the type contract.
2. Minor: double computation overhead.

**Fix:**
```python
_qwait = _hist_mean_ms("qwait")
"qwait_s": (_qwait / 1000) if _qwait is not None else None,
```

---

### HIGH-2 — Identity map `remaining_deploy` logic error (line 923)

**Location:** `build_identity_map()`, line 923

```python
remaining_deploy = [d for d in deploy_names if d not in identity.values()]
```

`identity.values()` returns **dicts** like `{"deploy_name": "foo", "confidence": "explicit"}`. A string is never `in` a collection of dicts, so this list is always the full `deploy_names`. The variable is also never used — doubly dead. The intent was to track already-mapped deploy names to avoid re-using them.

**Fix:** Remove line 923 (variable is unused). If the intent to avoid re-use is ever needed:
```python
already_mapped = {v["deploy_name"] for v in identity.values() if v.get("deploy_name")}
remaining_deploy = [d for d in deploy_names if d not in already_mapped]
```

---

### HIGH-3 — `IndexError` on exact-length metric lines (lines 552, 567, 575, 584, 589)

**Location:** `parse_vllm_scrape()` and `parse_epp_scrape()`

```python
if line.startswith(name) and line[len(name)] in "{ ":
```

If `line == name` exactly (no labels or value), `line[len(name)]` raises `IndexError`. Violates the zero-traceback design goal.

**Fix:**
```python
if line.startswith(name) and len(line) > len(name) and line[len(name)] in "{ ":
```

---

### HIGH-4 — `--head` limit not cumulative across stage files (line 1374)

**Location:** `read_inference_perf_requests()`

```python
per_path = limit // max(len(paths), 1) if limit else None
for path in paths:
    count = 0
    ...
    count += 1
    if per_path and count >= per_path:
        break
```

`count` resets per file. With 3 files and `--head 100`, each gets `per_path=33`, giving at most 99 records. If any file has fewer than 33, total is even lower. Result: `--head` is unreliable.

**Fix:** Use a single cumulative counter across all files, breaking out of the outer loop when the limit is reached.

---

### MEDIUM-1 — Negative histogram sum not guarded (line 678)

**Location:** `_hist_mean_ms()` closure inside `pod_series()`

```python
ds = vs_b - vs_a
dc = vc_b - vc_a
if dc <= 0 or dt > MAX_DT:
    return None
return (ds / dc) * 1000
```

If the histogram sum counter resets independently of the count counter (partial restart), `ds` can be negative while `dc > 0`. This produces a negative latency — physically impossible.

**Fix:**
```python
if dc <= 0 or ds < 0 or dt > MAX_DT:
    return None
```

---

### MEDIUM-2 — JSON array parser breaks on `{`/`}` inside strings (lines 1259–1300)

**Location:** `iter_json_objects()`, JSON array branch

The bracket-depth counter increments/decrements on every `{` and `}` character, including those inside JSON string values. For example `{"error": "Config {broken}"}` causes the parser to terminate the object early, yielding malformed JSON.

**Fix:** Track whether the parser is inside a quoted string and skip bracket counting when it is.

---

### MEDIUM-3 — Dead regex patterns (lines 844–856)

**Location:** Module level

```python
_SCALE_TARGET_RE = re.compile(...)
_SCALE_TARGET_RE2 = re.compile(...)
```

Both are defined but never referenced. The actual parsing uses inline `re.search()` calls in `_parse_scale_target_from_log()`. Remove them.

---

### LOW-1 — Magic numbers in `compute_time_anchor` (lines 410, 436)

```python
if n_scrapes < 5:
if req_span < 0.2 * scrape_span:
```

Both thresholds are undocumented. Replace with named constants and a comment explaining the rationale.

---

### LOW-2 — Stage file regex not end-anchored (line 1331)

```python
if re.match(r'stage_\d+_lifecycle_metrics\.json', f)
```

`re.match` anchors to the start but not the end. A file named `stage_1_lifecycle_metrics.json.bak` would match. Use `r'^stage_\d+_lifecycle_metrics\.json$'`.

---

## Contract Compliance

- `qwait_s` — see HIGH-1. Can emit `False`/`0` instead of `None` or float.
- `pods[].created_t`, `ready_t`, `setup_s` — always `None` when `wva_pod_timings.json` is absent (expected; documented in `extract_input_report`).

---

## Well-Implemented

1. **Error handling throughout** — `read_json`, `parse_vllm_scrape`, file I/O all return safe defaults on failure. Zero tracebacks in practice (except HIGH-3).
2. **Streaming JSON parser** — `iter_json_objects` correctly handles multi-GB files without loading into memory (modulo MEDIUM-2).
3. **Counter differencing** — `_rate()` correctly detects resets (negative diff) and collection gaps (`dt > MAX_DT`).
4. **Cross-correlation anchor** — Pearson implementation is correct; `refused-short-trace` guard is sound.
5. **Modular structure** — one function per data source throughout, as designed.
6. **`Coverage`, `InputReport`, `OutputReport`** — clean accumulator pattern; no scattered global state.
7. **ISO timestamp handling** — `iso_epoch()` handles `Z`, `+HH:MM`, and bare `+HHMM` variants robustly.
8. **SO identity resolution** — four-level confidence fallback is well-structured; ambiguity is flagged rather than silently resolved.

---

## Priority Order for Fixes

**Immediate:**
1. HIGH-3 — `IndexError` (crash risk)
2. HIGH-1 — `qwait_s` data loss + type violation
3. MEDIUM-1 — negative latencies
4. MEDIUM-2 — JSON parser string handling

**Soon:**
5. HIGH-2 — dead/broken `remaining_deploy`
6. HIGH-4 — `--head` unreliability
7. MEDIUM-3 — remove dead regexes

**Low priority:**
8. LOW-1 — named constants
9. LOW-2 — regex anchoring
