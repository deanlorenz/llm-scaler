# benchmark-viz session doc

Living record of decisions, findings, and plans for the benchmark-viz worktree.
**Scope:** Visualization tooling only — takes already-processed trace data and
produces graphs and reports. NOT concerned with environment setup, running
benchmarks, collecting data, or extracting it.

**How to use this doc:** This is the single source of truth. Read it cold — no
other context is needed to continue work. The work items section at the bottom
is the authoritative list of what's done and what's next.

---

## Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Scope is visualization only: input = processed trace data; output = plots + reports | Explicitly scoped by user. Does NOT include: environment setup, running benchmarks, collecting data, extracting raw metrics |
| 2 | Reports deferred — focus on input contract and plots first | User direction: "start from input" |
| 3 | This document also serves as the extractor output spec — viz defines what it needs, extractor must produce it | The two agents (viz and extractor) are separate; viz specifies the interface |
| 4 | No P/D runs yet; no multi-model runs yet — design must not assume 1:1 EPP:SO, but no new panels needed for those cases now | No P/D data exists to test against; design must accommodate the topology without requiring new panels for it yet |
| 5 | Per-request-lifecycle file from inference-perf will likely be disabled; extractor must reconstruct request timeseries from other data | File can be multi-GB; the viz tool should not depend on it existing |
| 6 | Bundle is a directory of files — one per object type, so each is independently optional | Enables partial bundles: a run with no controller log still produces a valid (partial) bundle |
| 7 | Viz tools are standalone — not coupled to any specific benchmark harness or run tooling. Missing data is surfaced on panels/report, not a hard failure | A missing file → "data not available" annotation on affected panels, never a crash |
| 8 | `requests[]` timeseries is the extractor's responsibility to produce — the renderer only consumes it. Reconstruction method is an extractor concern, not a viz concern | Even if inference-perf per-request file is disabled, the extractor may reconstruct from histograms; the renderer doesn't care how |
| 9 | `k2_decision_table` is NOT the right input for panel 6 — use `analyzer-result` log lines instead | `k2` is a saturation_v2 internal detail. The `analyzer-result` log line already provides the generic abstraction: `rc`, `sc`, `prc`, `reason` per analyzer per variant. `reason=P3-k2` is just one possible value |
| 10 | The renderer computes derived stats (`itl_fit`, `sat_band`, `capacity`, `tput_knee`, `router`, `lags`) itself from the raw timeseries in the bundle. Only data that requires external sources (controller log → `scaler[]`) must come from the extractor | Keeps the bundle lean; renderer is self-contained for all analytics that only need scrape data |
| 11 | `drain_windows` per pod is computed by the renderer from `replicas[]` + pod series, not pre-computed by extractor | It's a heuristic match that the renderer has all inputs for; no need to put it in the bundle |
| 12 | `q_engine_avg` (EPP average queue) is included in the bundle but panels must prefer `q_engine_sum` | The average falls the moment a new pod goes Ready, before any work moves to it — gives phantom relief signal. Sum does not have this failure mode |
| 13 | The old cherry-pick task (10 commits from `commit-mapping.md`) is superseded by this redesign | The old commits port the reference tool against the old flat `bundle.json` schema. The new work ports against the new directory schema instead |
| 14 | `plot_two_variant_pipeline.py` will be re-evaluated against the new bundle schema | Currently reads `post_run_analyze.sh` processed JSONs (old schema). Needs reassessment once bundle schema is finalized |
| 15 | Single-run output is plots only (PNG). Reports are deferred | Design not started; depends on first agreeing what a report contains |
| 16 | Cumulative/comparison reports across multiple runs are deferred | No design started |
| 17 | Canonical per-request verdict field is `outcome`, not `result` | Re-read contract doc confirmed `endpoints[].requests[]` uses `outcome`; earlier concern about extractor mismatch was a mistake |
| 18 | Current renderer smoke path expects top-level `scaled_objects[].so_id` and request `outcome` | Refreshed extracted bundles validated these names in practice |

---

## Reference implementation

The reference renderer (`render_real_trace.ref.py`, 1639 lines) and extractor
(`extract_real_trace.ref.py`, 1626 lines) from the old WVA repo are stored at:

```
docs/plans/benchmark-viz/render_real_trace.ref.py
docs/plans/benchmark-viz/extract_real_trace.ref.py
```

A rendered example is at `docs/plans/benchmark-viz/panels-reference.png`.

### What the reference renders (already correct and tested)

The reference renderer produces a 6-panel PNG from a flat `bundle.json`.
Panels 1–6 rendering logic is **correct and tested against real runs**.
The only thing that changes in the port is the **data-loading path** — from
flat `bundle.json` to the new directory schema.

### Key field names from the reference (override any earlier notes)

The reference uses **short field names** in pod series:
- `run` — `vllm:num_requests_running`
- `wait` — `vllm:num_requests_waiting`
- `kv` — KV cache utilization (0–1)

The reference `system[]` (what we call `endpoints[].epp[]`) carries:
- `in_system` — request-derived L(t) concurrency (requires `requests[]`)
- `q_dispatch` — EPP `inference_objective_running_requests`
- `q_engine_sum` — sum of per-pod `vllm:num_requests_waiting`
- `q_engine_avg` — average per-pod waiting (⚠ prefer sum — see decision 12)

Panel 6 in the reference reads `der['scaling_log']['by_analyzer']` — a dict
keyed by analyzer name → list of `{t, variant, rc, sc, prc, reason}`. The
signed replica-delta formula is `(rc - sc) / prc` with mild log2 compression:
`signed_log2(x) = log2(1+x) if x≥0 else -log2(1+|x|)`. Tick labels are
inverted back to real replica-delta units. Reason codes appear as markers at
their first occurrence on each analyzer line.

### What the port must do

In the new schema the equivalent of `der['scaling_log']['by_analyzer']` is
assembled by the renderer from `scaled_objects[].scaler[]` records of type
`analyzer_result`. The renderer groups them by `analyzer` → list of records,
same structure. No change to panel rendering logic.

The reference `render()` function reads these top-level bundle keys:
```python
meta      = bundle['meta']
reqs      = bundle.get('requests') or []
reps      = bundle.get('replicas') or []
system    = bundle.get('system') or []
pods      = bundle.get('pods') or {}
der       = bundle.get('derived') or {}
```

In the new schema these map to:
```
meta          → meta.json
reqs          → endpoints[0].requests[]      (from endpoints.json)
reps          → scaled_objects[0].replicas[] (from scaled_objects.json)
system        → endpoints[0].epp[]           (from endpoints.json)
pods          → scaled_objects[0].pods[]     (from pods.json)
der.capacity  → computed by renderer from pod series
der.sat_band  → computed by renderer from pod series
der.lags      → computed by renderer from replicas + pods
der.tput_knee → computed by renderer from pod series
der.router    → computed by renderer from pod series
der.itl_fit   → computed by renderer from pod series
der.scaling_log → assembled by renderer from scaler[] records (see above)
```

Multi-SO / multi-endpoint cases: the renderer iterates all SOs and all
endpoints. Panel aggregations sum across SOs under the same endpoint.

### Current validated bundle compatibility

Smoke-tested refreshed extracted bundles under
[`../benchmark-extract/hack/benchmark/results/`](../benchmark-extract/hack/benchmark/results)
with local [`.venv`](.venv) and
[`render_real_trace.py`](hack/benchmark/render_real_trace.py). All rendered a
summary PNG successfully:

- `quick-smoke-inference-perf` — 1 endpoint, 1 scaled object, 990 requests
- `guidellm-decode-heavy` — 1 endpoint, 1 scaled object, 5811 requests
- `burst-variant1` — 1 endpoint, 3 scaled objects, 12000 requests
- `early-inference-perf` — 1 endpoint, 1 scaled object, 990 requests
- `burst-variant2` — 1 endpoint, 2 scaled objects, 12000 requests

Observed useful current-shape facts from refreshed bundles:
- `requests.json` is now present in all refreshed sample bundles
- request records use `outcome`, `ttft_ms`, `itl_ms`, and `e2e_ms`
- `scaled_objects.json` uses top-level `so_id`; the renderer was adjusted to use
  that instead of `config.so_id`
- some runs still set `meta.time_anchor.trustworthy=false`; this is a data
  quality warning, not a render blocker

---

## Input contract

This section is the **extractor output spec**: the schema the viz tooling
requires as input. The extractor must produce this; the renderer consumes it.

All time values are **seconds from load start** (t=0 = first request arrives).

### Bundle directory layout

```
bundle/
  meta.json              — run identity and clock alignment (always present)
  provenance.json        — extractor version + git sha (always present)
  endpoints.json         — endpoint configs, load profile, EPP timeseries
  requests.json          — per-request records (optional)
  scaled_objects.json    — SO configs, replica timeseries, scaler events
  pods.json              — per-pod vLLM scrape timeseries, grouped by SO
  coverage.json          — per-capability PASS/FAIL/WARN (always present)
```

Any missing file causes affected panels to render with a clear "data not
available" annotation. Never a crash.

### Object model

```
Run
├── meta                         (run-level config and identity)
├── endpoints[]                  (one per model/EPP endpoint)
│   ├── config
│   ├── requests[]               (per-request lifecycle records — optional)
│   ├── load[]                   (planned load timeseries)
│   └── epp[]                    (EPP scrape timeseries)
└── scaled_objects[]             (one per WVA-managed SO / "variant")
    ├── config
    ├── pods[]                   (per-vLLM pod scrape timeseries)
    │   └── series[]
    ├── replicas[]               (replica state timeseries)
    └── scaler[]                 (scaler events: analyzer results, decisions, transitions)
```

Notes:
- A single EPP may manage multiple SOs (multi-variant). EPP is at endpoint
  level; SOs reference their endpoint by id.
- Pod scrapes are always grouped under the SO they belong to.
- The renderer computes all derived stats itself (`itl_fit`, `sat_band`,
  `capacity`, `tput_knee`, `router`, `lags`, `drain_windows`) from raw
  timeseries. Nothing pre-computed except `scaler[]` (needs controller log).

---

### `meta.json`

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Unique run identifier |
| `namespace` | string | Kubernetes namespace |
| `extracted_at` | string | ISO timestamp of extraction |
| `extractor_version` | string | Extractor git sha / version |
| `harness` | string | `inference-perf` or `guidellm` |
| `load_duration_s` | number | Convenience summary of total planned load duration (seconds). Authoritative load profile is `endpoints[].load[]` |
| `harness_start_epoch` | number | Epoch timestamp of harness start |
| `time_anchor` | object | Clock alignment metadata |

#### `meta.time_anchor`

| Field | Description |
|-------|-------------|
| `method` | `cross-correlation`, `not-needed`, or `refused-short-trace` |
| `offset_s` | Solved offset in seconds (0.0 if not needed) |
| `corr` | Cross-correlation coefficient (null if not applicable) |
| `trustworthy` | Boolean — false if fit was weak. Renderer must surface this prominently |

---

### `endpoints.json` — `endpoints[]`

#### `endpoints[].config`

| Field | Type | Description |
|-------|------|-------------|
| `endpoint_id` | string | Unique identifier |
| `model` | string | Model being served |
| `epp_name` | string | EPP resource name |
| `scaled_object_ids` | array | SO ids managed under this endpoint |

#### `endpoints[].load[]`

| Field | Type | Description |
|-------|------|-------------|
| `t_start` | number | Stage start (s from load start) |
| `t_end` | number | Stage end |
| `rate_rps` | number | Target request rate (requests/s) |
| `in_tok` | number | Planned prompt tokens per request |
| `out_tok` | number | Planned output tokens per request |

#### `endpoints[].requests[]` (optional)

| Field | Type | Description |
|-------|------|-------------|
| `t_arr` | number | Arrival time (s from load start) |
| `t_dep` | number | Departure time |
| `endpoint_id` | string | Which endpoint |
| `in_tok` | number | Prompt token count |
| `out_tok` | number | Output token count (server-side) |
| `ttft_ms` | number | Time to first token (ms) |
| `itl_ms` | number | Inter-token latency (ms/token) |
| `e2e_ms` | number | End-to-end latency (ms) |
| `outcome` | string | `ok`, `error`, or `truncated` |

#### `endpoints[].epp[]` — EPP scrape timeseries

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Scrape time (s from load start) |
| `in_system` | number | Request-derived concurrency L(t). Requires `requests[]` |
| `q_dispatch` | number | `inference_objective_running_requests` |
| `q_engine_sum` | number | Sum of per-pod waiting across all pods (PREFER THIS) |
| `q_engine_avg` | number | Average per-pod waiting (⚠ masks scale-up relief — see decision 12) |
| `ready_pods` | number | Ready pod count as seen by EPP |
| `kv_mean` | number | Mean KV cache utilization (0–1) |
| `throughput_rps` | number | Observed request completion rate |

---

### `scaled_objects.json` — `scaled_objects[]`

#### `scaled_objects[].config`

| Field | Type | Description |
|-------|------|-------------|
| `so_id` | string | Unique SO identifier |
| `endpoint_id` | string | Parent endpoint |
| `model` | string | Model served |
| `role` | string | `prefill`, `decode`, or `both` |
| `gpu_count` | number | GPUs per replica |
| `cost` | number | Cost weight per replica |
| `min_replicas` | number | Configured minimum |
| `max_replicas` | number | Configured maximum |
| `engine_config` | object | vLLM config: `num_gpu_blocks`, `block_size`, `kv_tokens`, `gpu_mem_util`, `prefix_caching` |

#### `scaled_objects[].replicas[]`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Snapshot time (s from load start) |
| `desired` | number | Desired replica count |
| `ready` | number | Ready replica count |
| `available` | number | Available replica count |

#### `scaled_objects[].scaler[]` — scaler event timeseries

Heterogeneous records, distinguished by `event_type`.

##### `analyzer_result` — one per analyzer per SO per cycle

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time (s from load start) |
| `event_type` | string | `analyzer_result` |
| `analyzer` | string | e.g. `saturation` or `throughput` |
| `variant` | string | Variant / SO identifier |
| `role` | string | Role if reported |
| `rc` | number | Required capacity (endpoint scope) |
| `sc` | number | Spare capacity (endpoint scope) |
| `prc` | number | Per-replica capacity for this SO |
| `reason` | string | Capacity tier code: `P0-store`, `P1-obs`, `P2-hist`, `P3-k2`, `P4-k1` (saturation); `T1-ols`, `T2-pinned`, `T2-default`, `T2-failed` (throughput) |

Note: `rc` and `sc` are endpoint-level (same value across all variant records
in the same analyzer cycle). `prc` and `reason` are per-variant/SO.
Both `rc` and `sc` are `max(0, ...)` at source — never simultaneously positive.

##### `scaling_decision` — one per SO per cycle

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `scaling_decision` |
| `variant` | string | Variant / SO identifier |
| `action` | string | `ScaleUp`, `ScaleDown`, or `NoChange` |
| `curr` | number | Current replica count |
| `tgt` | number | Target replica count |

##### `analyzer_absent` — configured analyzer not in active list

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | First time seen absent |
| `event_type` | string | `analyzer_absent` |
| `analyzer` | string | Analyzer name |

##### `replica_transition` — observed replica lifecycle events

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `replica_transition` |
| `action` | string | `up`, `ready`, `draining`, or `down` |
| `desired` | number | Desired at this transition |
| `ready` | number | Ready at this transition |

---

### `pods.json` — `scaled_objects[].pods[]`

#### Pod metadata (static)

| Field | Type | Description |
|-------|------|-------------|
| `pod_id` | string | Pod name |
| `so_id` | string | Parent SO |
| `created_t` | number | Pod creation time (s from load start) |
| `ready_t` | number | Pod ready time |
| `first_metric_t` | number | First scrape with non-empty pod metrics |
| `setup_s` | number | Boot lag: `ready_t − created_t` |

#### `pods[].series[]` — pod scrape timeseries (sparse)

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Scrape time |
| `run` | number | `vllm:num_requests_running` |
| `wait` | number | `vllm:num_requests_waiting` |
| `kv` | number | KV cache utilization (0–1) |
| `gen_rate` | number | Token generation rate (tokens/s) |
| `prompt_rate` | number | Prompt token processing rate (tokens/s) |
| `itl_ms` | number | Inter-token latency from histogram (ms/token) |
| `prefill_ms` | number | Mean prefill time (ms) |
| `qwait_s` | number | Mean queue wait time (s) |
| `preempt_rate` | number | Preemptions per second |
| `pfx_hit` | number | Prefix cache hit rate (0–1) |
| `stable` | boolean | True if `\|Δrunning\| ≤ 25` AND no replica transition in this interval. Only stable intervals feed ITL fits — load-bearing, not cosmetic |

---

### `coverage.json`

Array of rows:

```json
{
  "scope": "so_id or endpoint_id",
  "capability": "Calibrate A",
  "result": "FAIL",
  "detail": "n_intervals=3, need ≥8"
}
```

| Capability | What it checks |
|------------|----------------|
| `Calibrate A` | ITL linear regime well-sampled (kv span ≥ 0.4, n ≥ 8) |
| `Trust B` | Empty-system ITL measured (n ≥ 5 at kv < 0.05) |
| `Characterize saturation` | Saturated band well-sampled (n ≥ 10 at kv ≥ 0.85) |
| `Exercise the 0.85 ceiling` | Mid-band dwell present (n ≥ 3 in kv 0.80–0.90) |
| `Locate the throughput knee` | Knee confident |
| `Scale-down present` | Any `ready` decrease observed |
| `Drain-vs-kill measurable` | Scale-down with in-flight requests |
| `Router imbalance measurable` | Per-pod dispersion computable |
| `ρ model valid at top` | Preemption rate ≈ 0 in saturation band |
| `Regime` | memory-bound vs compute-bound classified |
| `Signal completeness` | Scrape coverage per pod (endpoint-level) |
| `Time anchor trustworthy` | `time_anchor.trustworthy = true` (endpoint-level) |
| `Queue (a) material` | Flow-control queue non-negligible (endpoint-level, needs requests[]) |
| `Shape` | Workload shape characterization (endpoint-level) |

---

### `provenance.json`

```json
{
  "extractor_version": "git-sha",
  "extracted_at": "ISO timestamp",
  "run_dir": "source run directory path",
  "harness": "inference-perf or guidellm"
}
```

---

### Signal completeness and caveats

- Pod series are **sparse** — not every pod has a scrape at every `t`.
- `requests[]` may be absent — panels degrade gracefully.
- `scaler[]` WVA fields require controller log capture at run time.
- `time_anchor.trustworthy: false` must be surfaced prominently by the renderer.
- Prefer `q_engine_sum` over `q_engine_avg` for any queue-depth panel.

---

## Output contract

### Single-run visualization: `render_real_trace.py`

Reads the bundle directory → multi-panel PNG (`panels.png`).

| Panel | Name | Required signals |
|-------|------|-----------------|
| 1a | Request quality (arrival/departure rates, wait quality bands) | `endpoints[].requests[]` |
| 1b | Token throughput (generation tok/s vs capacity ceiling) | `endpoints[].requests[]`, pod series, replicas |
| 2 | Replica timeline (desired/ready, scale markers, boot lag) | `so.replicas[]`, `so.scaler[]` |
| 3 | Running/waiting per pod (stacked bars + EPP queue overlay) | `endpoints[].epp[]`, `so.pods[].series[]` |
| 4 | KV heatmap (per-pod KV cache utilization over time) | `so.pods[].series[]` |
| 5 | Concurrency L(t) vs slot capacity | `endpoints[].epp[]`, pod series, replicas |
| 6 | Signed replica-delta per analyzer (scale-up/down pressure) | `so.scaler[]` (`analyzer_result` records) |

All panels degrade gracefully when their required signals are absent.

### Published results layout

```
hack/benchmark/results/<YYYYMMDD>-<label>/
  bundle/
    meta.json
    provenance.json
    endpoints.json
    requests.json          (optional)
    scaled_objects.json
    pods.json
    coverage.json
  panels.png
```

---

## Analyzer metric model (panel 6 background)

Each analyzer (saturation, throughput, ...) computes independently in its own units.

- **Demand** — per endpoint, broken down per role. Each analyzer has its own units.
- **Supply** — per SO: `ready_replicas × PRC`
- **PRC** — per-replica capacity, in analyzer units OR as fraction of total endpoint demand
- **RC / SC** — required/spare capacity, endpoint-level aggregates. Both `max(0,...)` — never simultaneously positive
- **Signed replica-delta** — `(rc - sc) / prc` per analyzer. Positive = scale-up pressure, negative = scale-down pressure
- **Panel 6** shows one line per analyzer, log2-compressed, reason codes as markers

Why `k2` is NOT the right input for panel 6: `k2` is a saturation_v2 internal
implementation detail. `reason=P3-k2` is just one possible `reason` value in
`analyzer_result`. The `analyzer_result` record is the correct generic
abstraction — it works for all current and future analyzers.

---

## Open questions

| # | Question | Status |
|---|----------|--------|
| 1 | Report format and content — what goes in a single-run report? | Deferred |
| 2 | Cumulative / comparison reports across multiple runs | Deferred |
| 3 | Multi-variant panels — design deferred until P/D or multi-model data exists | Deferred |

---

## Fresh-start handoff

### What is complete

- **Full 7-panel renderer** —
  [`hack/benchmark/render_real_trace.py`](hack/benchmark/render_real_trace.py)
  renders all panels from the new directory-format bundle. Panel logic is
  ported verbatim from the validated reference renderer; only the data-loading
  path changed.

- **Data mappings implemented:**
  - Times are already relative (s from load start) — `t0 = min(origins)` still
    computed correctly from relative values
  - `ttft_ms` (ms) → `ttft` (s) normalised in `_assemble()`
  - `outcome: ok` works with reference's `== 'error'` / `== 'truncated'` checks
  - `meta.run_id` / `meta.wva_confirmed_model` used (not old `run` / `model`)
  - `meta.prewarm_s` used as `warmup_offset_s` fallback
  - replicas aggregated across all SOs (sorted by `t`)
  - `epp[]` from first endpoint → `system`
  - pods: `pods_by_so` flattened to `{pod_id: {series, drain_windows}}`
  - `scaler[]` `analyzer_result` events → `der['scaling_log']['by_analyzer']`
  - `analyzer_absent` events → `der['scaling_log']['saturation_absent_at']`
  - coverage: new list-of-rows schema normalised to old `{rows, warnings, n_pass, n_fail}`

- **Sample bundles copied** into
  [`hack/benchmark/results/`](hack/benchmark/results/) (5 runs):
  `quick-smoke-inference-perf`, `guidellm-decode-heavy`, `burst-variant1`,
  `burst-variant2`, `early-inference-perf`

- **All 5 bundles smoke-tested** — render cleanly, no crashes. Panels 1a, 1b,
  2, 5 are live and populated. Panels 3, 4, 6 degrade gracefully (no pod
  series or scaler events in current bundles).

- **Single-run HTML report** —
  [`hack/benchmark/report.py`](hack/benchmark/report.py)
  generates a self-contained HTML file (panels.png embedded as base64, metrics
  table, coverage table, caveats). No dependencies beyond stdlib.

- **`.gitignore`** — `results/*/panels.png` and `results/*/report.html` ignored.

- **`.venv`** exists with matplotlib + numpy installed:
  ```
  uv venv .venv
  uv pip install --python .venv/bin/python matplotlib numpy
  ```

- **Reference panels extracted** —
  [`docs/plans/benchmark-viz/panels-ref-decode-heavy.png`](docs/plans/benchmark-viz/panels-ref-decode-heavy.png)
  is the reference render from the old renderer against the same
  `guidellm-decode-heavy` data. Use this for panel-by-panel comparison.

### Known renderer bugs (approved to fix, not yet done)

1. **Spurious vertical scale-event lines** — extractor emits a replica record
   every scrape interval even when `desired`/`ready` don't change. The renderer
   draws a vline on every consecutive change. Fix: dedup `reps` in `_assemble()`
   — only keep records where `desired` or `ready` actually changes value, plus
   first and last.

2. **X-axis overrun** — replica scraping continues long after load ends
   (`early-inference-perf` reps run to t=57190s, load ends at ~193s).
   Fix: clip x-axis to `max(last t_dep, last epp t)` — not the replica tail.

### Current data gaps in sample bundles (not renderer bugs)

These require the extractor to produce new data:

- `out_tok: null` on inference-perf requests → panel 1b empty (guidellm has it)
- No pod series → panels 3 and 4 show graceful-degradation message
- No scaler events → panel 6 shows graceful-degradation message
- `q_dispatch`, `q_engine_sum`, `throughput_rps` all null in epp[] → panel 5
  missing served/slots/capacity lines
- `ttft_ms` null on inference-perf requests → panel 1a bars uncoloured
  (guidellm has it; fallback for inference-perf: use `e2e_ms` as pessimistic
  proxy, annotate clearly; or show uncoloured bars with a note)

### How to render

```bash
# render panels
.venv/bin/python hack/benchmark/render_real_trace.py \
    --bundle-dir hack/benchmark/results/guidellm-decode-heavy

# generate HTML report (embeds panels.png)
.venv/bin/python hack/benchmark/report.py \
    --bundle-dir hack/benchmark/results/guidellm-decode-heavy
```

---

## Work items

### Done
- [x] Scope defined (visualization only)
- [x] Object model defined (5 types, hierarchy, grouping rules)
- [x] Bundle directory layout defined (7 files, each independently optional)
- [x] Read reference renderer (`render_real_trace.ref.py`, 1639 lines) in full
- [x] Read reference extractor (`extract_real_trace.ref.py`, 1626 lines) in full
- [x] Established mapping from old flat `bundle.json` keys to new directory schema
- [x] Finalized the input contract schema in this worktree
- [x] Understood why `k2_decision_table` is wrong and replaced it with generic `analyzer_result`-driven panel-6 inputs
- [x] Published the extractor-facing input contract as [`input-contract.md`](docs/plans/benchmark-viz/input-contract.md)
- [x] Implemented full 7-panel renderer (`render_real_trace.py`) — data mappings only, rendering logic verbatim from reference
- [x] Smoke-tested all 5 sample bundles — all render cleanly
- [x] Implemented single-run HTML report generator (`report.py`)
- [x] Panel review session — compared output against reference render, identified all gaps
- [x] Merged all panel body fixes from `worktree-benchmark` (commits `82049884`–`29a8616c`) into the directory-schema renderer:
  p1a time axis, p2 proportional offset + delta labels, p3 legend clear of colour-key strip,
  p3/p5 in-system line aligned to pod-scrape grid, p4 amber KV gradation + wider colour ramp
  + header spacing, p5 WVA supply/demand as requests + demand label, p6 minor ticks +
  visible-window first label + silent-stretch tail annotation.
  Fixed tight_layout right edge (0.97→1). All 8 sample bundles smoke-tested. Committed `df51c50b`.

### Resolved bugs

1. ✅ **p2 empty** — fixed: duplicate `offset_copy` block removed; `source=wva`
   replica records filtered before dedup so only `source=harness` records feed
   the step plot. (`fd1110f0`)

2. ✅ **p6 y-axis** — fixed: replaced `FixedLocator`+`FuncFormatter`+manual
   `signed_log2` transform with `symlog(linthresh=1, base=2)` + plain integer
   formatter. (`c8dbe44c`, `fad51c0b`)

3. ✅ **v0.4.0 bundles** — ingested; `req_buckets` p1a branch restored; p5
   `in_system` fallback from bucketed requests; x-axis clip uses
   `max(last_departure_t, last_scale_event_t)`. (`13d8ff81`)

### Open / next steps

1. **Get bundles with scaler events** — need a run that captures
   `analyzer_result` / `scaling_decision` in `derived.json`'s `scaling_log`
   to exercise p6 with real data.

2. **1a fallback when ttft absent** — decision pending.

3. **Publishing and Makefile integration** — `benchmark-render` /
   `benchmark-publish` make targets; adapt `publish_result.sh`.

4. **Cumulative/comparison reports** — deferred.
