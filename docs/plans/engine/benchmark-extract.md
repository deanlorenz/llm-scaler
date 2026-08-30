# Benchmark Extraction Pipeline Plan

## Overview

Write `hack/benchmark/extract.py` — a new extractor that reads one or more raw
benchmark run directories and emits the multi-file bundle defined by the
benchmark-viz input-contract.

**Scope:** data in → structured bundle out. No cluster access, no harness
execution, no visualization.

**Input:** one or more run directories as captured by the benchmark harness plus
the runtools collection scripts. Multiple `--run` paths are joined into one
bundle, one endpoint entry per distinct `endpoint_url`.

**Output:** `<run_id>/extract/` directory (extractor-owned; sibling of `results/`):
```
extract/
  meta.json
  provenance.json
  endpoints.json
  requests.json            (always attempted; absent only if no source available)
  scaled_objects.json
  pods.json
  coverage.json
  extract_input_report.json
  extract_output_report.json
```

**Output path convention:**
- Run directory structure: `<run_id>/results/<harness>-<id>_<N>/` — owned by runtools
- Extractor output: `<run_id>/extract/` — owned solely by extractor, never written by runtools
- Default derived from `--run` path: go up two levels from `results/<run-name>` to `<run_id>`, write to `<run_id>/extract/`
- `--out` overrides the default (used for local testing)

**Design constraints:**
- Clean, focused functions — one function per data source. No monolithic logic
  that does more than one thing.
- stdlib only (no numpy, matplotlib, PyYAML, or third-party deps).
- Every optional artifact degrades gracefully: recorded in `extract_input_report`,
  never a traceback.
- `requests.json` is **always attempted** — no `--per-request` flag. Written whenever
  any source yields data; absent only when all sources are empty. Source priority:
  1. Native harness file (`per_request_lifecycle_metrics.json` for inference-perf,
     `results.json` for guidellm)
  2. EPP log (`logs/epp_pods.log`) — currently always empty, wired as future source
  3. Absent — reported honestly in `extract_input_report` and `coverage.json`
- **`requests.json` should contain aggregated timeseries, not raw per-request records.**
  Viz displays arrival rate, departure rate, in-system count L(t), waiting count,
  running count, fraction with wait < threshold — never individual requests.
  Extraction should do one streaming pass and emit compact bucketed timeseries
  (e.g. 1s buckets). This is a pending redesign — current implementation still
  emits raw records capped at `MAX_REQUESTS=50_000`, which is wrong.
- Per-request data sourcing: `per_request_lifecycle_metrics.json` is the flat
  per-request list for inference-perf. Stage files (`stage_N_lifecycle_metrics.json`)
  contain only aggregate summaries — not used as per-request source.
- Time convention: all `t` values in the bundle are **seconds from load start**
  where `t=0` = first harness stage start. The extractor includes a
  `load_start_epoch` in `meta.json` so viz can convert back to wall clock.

---

## Endpoint identity model

The endpoint is the logical unit of demand — what the harness drives and what
WVA accounts for.

### Identity fields

| Field | Value | Notes |
|-------|-------|-------|
| `endpoint_url` | full URL from `run_metadata.yaml` | stable unique id |
| `model_id` | `model` field from `run_metadata.yaml` | WVA demand accounting key (`model_name` label in WVA scrapes) |
| `epp_name` | EPP deployment name from scrape filenames | included; 1:1 with URL in current runs |
| `scaled_object_ids` | list of SO ids derived from pod names | explicit join key |

### EPP topology (current vs P/D)

In current single-stack runs there is one EPP per endpoint. In P/D deployments
there are up to three EPPs:
- **coordinator** — top-level, associated with the endpoint URL
- **prefill-scoped EPP** — scoped to prefill role
- **decode-scoped EPP** — scoped to decode role

All relate to the same endpoint. The `epp[]` timeseries carries a `role` field
(`coordinator`, `prefill`, `decode`, or `unknown`) to distinguish them. P/D EPP
structure is forward-looking; no P/D run data exists yet.

### Multi-model / multi-endpoint

Harnesses (guidellm, inference-perf) do not support multiple models in a single
run. Each harness run produces one `run_metadata.yaml` with one `model` and one
`endpoint_url`. Multi-model experiments require separate harness invocations,
each producing its own run directory.

The extractor accepts multiple `--run` paths. Each is extracted independently
and joined into one bundle with one endpoint entry per distinct `endpoint_url`.
The runner is assumed to keep run directories flat (separate per harness
invocation); the extractor does not auto-discover runs.

---

## Demand sources and scopes

Demand signal comes from multiple independent sources and must be kept
source-labeled rather than merged into a single field.

| Source | Signal | Scope | Where in bundle |
|--------|--------|-------|-----------------|
| WVA Prometheus | `wva_analyzer_demand` (tokens or tokens/s) | endpoint/model | `endpoints[].demand[]` |
| WVA Prometheus | `wva_analyzer_target` (per-replica capacity P) | SO | `scaled_objects[].replicas[]` with `source: "wva"` |
| WVA Prometheus | `wva_kv_cache_tokens_capacity` (total across replicas) | SO | `scaled_objects[].replicas[]` with `source: "wva"` |
| WVA Prometheus | `wva_current_replicas` | SO | `scaled_objects[].replicas[]` with `source: "wva"` |
| WVA controller log | `rc`, `sc`, `prc` per analyzer | endpoint + SO | `scaled_objects[].scaler[]` `analyzer_result` events |
| Harness replica tracking | `desired`, `ready`, `available` | SO | `scaled_objects[].replicas[]` with `source: "harness"` |
| Pod scrapes | `run` + `wait` concurrency per pod | pod | `pods[].series[]` — viz constructs SO/EP rollups |
| Per-request lifecycle | `t_arr`, `t_dep`, in-system L(t) | endpoint | `requests.json` (optional) |
| EPP logs / fallback | inferred req/s when no per-req lifecycle | endpoint | `endpoints[].demand[]` with `source: "inferred"` |

**Source tag vocabulary:** `harness`, `wva`, `keda`, `hp`, `inferred`

### `replicas[]` source merging

`scaled_objects[].replicas[]` entries from different sources are kept separate
(one entry per source per timestamp), each tagged with `source`. Viz constructs
combined views itself from the raw per-source entries.

### `demand[]` at endpoint scope

`endpoints[].demand[]` is a timeseries of endpoint-scoped demand signals:
```
{ t, source, analyzer, value, unit }
```
- `source`: `"wva"`, `"inferred"`, etc.
- `analyzer`: `"saturation"`, `"throughput"`, etc. (or null for harness-derived)
- `unit`: `"tokens"`, `"tokens_per_s"`, `"requests_per_s"`, etc.
- `value`: the signal value

Note: `wva_analyzer_demand` unit varies by analyzer — saturation uses tokens,
throughput uses tokens/s.

### In-system req/s fallback

When no per-request lifecycle data is available, the extractor must derive
approximate req/s from EPP logs or pod `run+wait` signals. This is labeled
`source: "inferred"` and placed in `endpoints[].demand[]`. Per-pod `run+wait`
numbers are already in `pods[].series[]` so viz can construct SO-level and
EP-level rollups and compare against the reported signals.

---

## `replicas[]` unified schema

Each entry in `scaled_objects[].replicas[]`:

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Snapshot time (seconds from load start) |
| `source` | string | `harness`, `wva`, `keda`, `hp` |
| `desired` | number | Desired replica count (harness source) |
| `ready` | number | Ready replica count (harness source) |
| `available` | number | Available replica count (harness source) |
| `current_replicas` | number | Current replica count as observed by WVA (wva source) |
| `kv_capacity_tokens` | number | Total KV capacity tokens summed across all current replicas of this SO (wva source) |
| `capacity_per_replica` | number | Per-replica capacity P — how much demand (in the analyzer's unit) one replica handles. Time-dependent input to scaling calculation, not a decision output (wva source, from `wva_analyzer_target`) |

Fields not applicable for a given source are omitted (not null-padded).

Notes:
- `capacity_per_replica` (P) is distinct from `target_replicas` in `scaling_decision` events.
  P is the per-replica capacity input; `target_replicas` is the scaling decision output.
- `prc` on `analyzer_result` scaler events is P as captured at decision time from the
  controller log — same concept, different source.

---

## SO identity resolution

Each component in a run uses a different name for the same logical scaled object:

| Source | Name used | Example |
|--------|-----------|---------|
| WVA Prometheus `variant_name` | ScaledObject / VA name | `...-decode-wva` |
| WVA controller log events | ScaledObject / VA name | `...-decode-wva` |
| Controller log `scaleTargetRef.name` | Deployment name | `...-decode` |
| Pod scrape filenames | Deployment name (via pod→RS→Deploy) | `...-decode-<rs>-<pod>` |
| Harness replica tracking | Deployment name | `...-decode` |
| EPP pod filenames | EPP Deployment name | `...-epp-<rs>-<pod>` |

**The `-wva` suffix in observed runs is a deployment convention, not WVA-enforced.**
WVA uses whatever name the KEDA ScaledObject has. The ScaledObject's
`scaleTargetRef.name` is the Deployment name — always independent of the SO name.

### Canonical `so_id`

`so_id` = the ScaledObject / VA name, sourced from WVA Prometheus `variant_name`
labels and controller log events. This is the WVA-canonical identifier.

### SO → Deployment mapping

Required to join pod scrapes and harness replica tracking to the correct SO.
Resolution in priority order:

1. **`scaledobject-config.json`** (when provided by runner) — explicit mapping,
   authoritative. Fields: `so_name`, `deploy_name`.
2. **Controller log error events** — the `VariantAutoscaling` object JSON in
   `event.go` error lines contains `spec.scaleTargetRef.name` = Deployment name.
   Parsed as best-effort; recorded with `status: "inferred"` in `extract_input_report`.
3. **Longest common prefix match** — when SO names and Deployment names share a
   long common prefix (e.g. `...-decode-wva` and `...-decode`), match them.
   Only applied when unambiguous (one-to-one, prefix length ≥ 10 chars).
4. **Unresolved** — listed in `extract_input_report` with all candidates, for
   user inspection.

### Identity map output

The extractor builds an internal `identity_map`:
```
{so_name: {deploy_name, confidence: "explicit|inferred|prefix|unresolved"}}
```
Recorded in `extract_input_report` under `"source": "SO identity resolution"`.

---

## `scaler[]` event schema

All events carry `event_type` and `so_id`.

### `analyzer_result`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `"analyzer_result"` |
| `so_id` | string | Scaled object identifier |
| `analyzer` | string | Analyzer name |
| `role` | string | Role if reported |
| `rc` | number | Required capacity (endpoint scope) |
| `sc` | number | Spare capacity (endpoint scope) |
| `prc` | number | Per-replica capacity for this SO |
| `reason` | string | Analyzer reason code |

### `scaling_decision`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `"scaling_decision"` |
| `so_id` | string | Scaled object identifier |
| `action` | string | `ScaleUp`, `ScaleDown`, `NoChange` |
| `current_replicas` | number | Current replica count |
| `target_replicas` | number | Target replica count |

---

## `coverage.json` schema

Shape matches the viz input contract:
```json
{
  "rows": [
    { "scope": "so_id or endpoint_id", "capability": "...", "result": "PASS|FAIL|WARN", "detail": "..." }
  ],
  "n_pass": 0,
  "n_fail": 0,
  "warnings": []
}
```

---

## `extract_input_report.json` schema

Producer-side diagnostic: what the extractor found or missed when reading inputs.

```json
{
  "generated_at": "ISO timestamp",
  "run_dirs": ["..."],
  "checks": [
    {
      "source": "descriptive name of what was looked for",
      "status": "found | missing | fallback | partial",
      "path": "path used or null",
      "fallback_path": "fallback path used or null",
      "note": "human-readable explanation"
    }
  ]
}
```

---

## `extract_output_report.json` schema

Producer-side diagnostic: what was written, what is null/empty/limited.

```json
{
  "generated_at": "ISO timestamp",
  "outputs": [
    {
      "file": "filename in bundle",
      "status": "written | empty | partial | absent",
      "record_count": 0,
      "null_fields": ["field names that are null across all records"],
      "note": "human-readable explanation"
    }
  ]
}
```

---

## Input contract (what the extractor reads)

Documented here so gaps can be filed as requirements against runtools.

### Always present
| File | Content |
|------|---------|
| `run_metadata.yaml` | harness identity, `model`, `namespace`, `endpoint_url`, `harness_name`, `harness_start`, timestamps |
| `<scenario>.yaml` | workload shape: load stages with `rate`, `duration`, token distributions |
| `metrics/raw/*_<epoch>_metrics.log` | per-pod vLLM Prometheus scrapes, one file per pod per scrape |
| `metrics/raw/wva-controller_<epoch>_metrics.log` | WVA controller `/metrics` scrapes (when runtools captures them) |

### Present on most runs
| File | Content |
|------|---------|
| `metrics/processed/replica_status_timeseries.json` | replica snapshots from harness or `sample_replicas.sh` |
| `metrics/processed/wva_replica_samples.json` | fallback replica source when harness predicate mismatches |
| `metrics/processed/wva_pod_timings.json` | per-pod created/ready timestamps from `sample_replicas.sh` |
| `controller.log` | WVA controller text log (WVA `analyzer-result`/`scaling-decision` lines) |

### Optional / harness-dependent
| File | Content |
|------|---------|
| `results.json` | guidellm per-request records (epoch timestamps, no anchoring needed) |
| `per_request_lifecycle_metrics.json` | inference-perf per-request records (monotonic timestamps, needs anchor) |
| `stage_N_lifecycle_metrics.json` | inference-perf per-stage per-request records |
| `metrics/raw/*epp*_metrics.log` | EPP Prometheus scrapes — **Unauthorized in all known captures; treat as absent** |
| `scaledobject-config.json` | runtools-captured SO config (gpu_count, role, cost, min/max replicas) — **not yet captured; gap for runtools** |

### Known gaps (to file as requirements against runtools)
1. **EPP metrics unauthorized**: the EPP `/metrics` endpoint requires bearer auth that the current scraper doesn't carry.
2. **ScaledObject config not captured**: `gpu_count`, `role`, `cost`, `min_replicas`, `max_replicas` come from the KEDA ScaledObject — not currently dumped by runtools.
3. **WVA doesn't log its EPP association**: EPP-to-SO linkage is currently inferred by naming convention.

---

## Sub-tasks

---

### Sub-task 1 — Scaffolding and shared primitives

**Status:** `[x] done`

---

### Sub-task 2 — Run identity: meta.json

**Status:** `[x] done` — needs contract alignment fixes (see Sub-task 10)

---

### Sub-task 3 — Time anchor

**Status:** `[x] done`

---

### Sub-task 4 — Pod scrapes → pods.json

**Status:** `[x] done` — needs `first_metric_t`, remove `engine_config` from pod (see Sub-task 10)

---

### Sub-task 5 — WVA controller data → scaled_objects.json

**Status:** `[x] done` — needs contract alignment (see Sub-task 10)

---

### Sub-task 6 — Per-request data (optional)

**Status:** `[x] done`

---

### Sub-task 7 — EPP data → endpoints.json

**Status:** `[x] done` — needs contract alignment (see Sub-task 10)

---

### Sub-task 8 — Coverage report → coverage.json

**Status:** `[x] done` — needs shape fix (see Sub-task 10)

---

### Sub-task 9 — Integration, Makefile, and sample validation

**Status:** `[x] done`

---

### Sub-task 10 — Contract alignment and reporting

**Status:** `[ ] pending`

**Intent**

Align `extract.py` output with the finalised viz input contract, add
`extract_input_report.json` and `extract_output_report.json`, and re-validate
against all 5 real runs.

**Changes required**

#### `meta.json`
- Add `extracted_at` (ISO timestamp of extraction)
- Add `extractor_version`

#### `endpoints.json`
- `endpoint_id` = full `endpoint_url` from `run_metadata.yaml` (not `"default"`)
- `config` adds: `model_id`, `scaled_object_ids[]`
- `load[]` populated with `t_start`, `t_end`, `rate_rps`, `in_tok`, `out_tok`
- `demand[]` timeseries added: endpoint-scoped WVA demand + inferred req/s
  signals, each entry tagged `{t, source, analyzer, value, unit}`

#### `scaled_objects.json`
- `so_id` at top level only (remove from config)
- `config` adds: `endpoint_id`, `model`
- `config` keeps: `gpu_count`, `role`, `cost`, `min_replicas`, `max_replicas`, `engine_config`
- `replicas[]` unified: entries from `harness` source keep `desired/ready/available`;
  entries from `wva` source carry `current_replicas`, `kv_capacity_tokens`,
  `target_per_replica`; all tagged with `source`
- `scaler[]` event fixes:
  - `type` → `event_type`
  - `so_id` (keep, not `variant`)
  - `from_replicas`/`to_replicas` → `current_replicas`/`target_replicas`
  - `rc`, `sc`, `prc` promoted from `fields{}` to top level on `analyzer_result`
  - remove `fields{}` raw dump
- Remove `wva_scrapes` top-level key (data merged into `replicas[]` and
  `endpoints[].demand[]`)

#### `pods.json`
- Add `first_metric_t` (t of first series entry with non-null `run` or `kv`)
- Remove `engine_config` from pod entries (lives in `scaled_objects[].config`)

#### `coverage.json`
- Fix shape: `{rows: [...], n_pass: N, n_fail: N, warnings: []}`

#### New: `extract_input_report.json`
- One check entry per input source attempted
- Fields: `source`, `status` (`found`/`missing`/`fallback`/`partial`), `path`,
  `fallback_path`, `note`

#### New: `extract_output_report.json`
- One entry per bundle file
- Fields: `file`, `status` (`written`/`empty`/`partial`/`absent`),
  `record_count`, `null_fields`, `note`

**Validation**
- Re-run against all 5 real runs, all exit 0
- Validate output shape matches contract for all fields listed above
- Update `hack/benchmark/results/EXTRACTION-NOTES.md`

---

### Sub-task 11 — requests.json redesign: aggregated timeseries

**Status:** `[x] done` (session 3)

Implemented. `requests.json` is now a 1s-bucket aggregated timeseries with fields:
`t`, `arr_rate`, `dep_rate`, `in_system`, `n_waiting`, `frac_fast`, `ttft_p50`, `source`.
`ttft_p50` is populated for guidellm (real per-request TTFT); null for inference-perf
(harness does not record per-request TTFT — see Sub-task 12 for pod-scrape-derived fix).

---

### Sub-task 12 — Pod scrape histogram extraction

**Status:** `[ ] pending`

**Context**

Confirmed by runtools (session 3): the vLLM pod scrape files already contain full
Prometheus histograms for TTFT, output tokens, input tokens, and vLLM queue wait.
These are exact measurements recorded by vLLM for every completed request — not
estimates. 4 of 5 runs have these files; the guidellm run is missing vLLM scrapes
(separate runtools issue, not blocking).

**Available histogram metrics and bucket boundaries**

| Metric | Buckets (le values) | Notes |
|--------|---------------------|-------|
| `vllm:time_to_first_token_seconds` | 0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 20.0, 40.0, 80.0, 160.0, 640.0, 2560.0 | 22 finite boundaries. Viz bands `<1s, 1-2.5s, 2.5-10s, >10s` all land on exact boundaries |
| `vllm:request_generation_tokens` | 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000 | 14 finite boundaries. Bands `<200, 200-500, 500-1000, >1000` exact |
| `vllm:request_prompt_tokens` | same 14 boundaries as generation tokens | |
| `vllm:request_queue_time_seconds` | 0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 2.5, 5.0, 10.0, 15.0, 20.0, 30.0, 40.0, 50.0, 60.0, 120.0, 240.0, 480.0, 960.0, 1920.0, 7680.0 | 21 finite boundaries. Covers vLLM-side queue only; EPP dispatch queue unavailable for existing runs |

The `vllm:e2e_request_latency_seconds` histogram is also present (sum/count already
extracted as `e2e_ms`). Buckets not listed here but would enable total-wait distribution
if EPP queue data becomes available.

**Extraction method**

Consecutive scrape file pairs. For interval `[t[i-1], t[i]]`:
- `delta_bucket[le] = scrape[i].bucket[le] - scrape[i-1].bucket[le]`
- Skip if any `delta_bucket[le] < 0` (pod restart — counters reset)
- Skip if `delta_count == 0` (no completions in interval)
- Emit raw delta CDF, not pre-computed bands — viz chooses its own band edges

**Output schema — new fields in `pods[].series[]`**

```json
"ttft_hist":    {"le": [0.04, 0.06, ..., 2560.0], "n": [19, 64, ...]},
"out_tok_hist": {"le": [200, 500, 1000, ...],      "n": [0, 0, 80, ...]},
"in_tok_hist":  {"le": [500, 1000, 2000, ...],     "n": [80, 0, 0, ...]},
"qwait_hist":   {"le": [0.3, 0.5, 1.0, ...],       "n": [80, 0, 0, ...]}
```

- `le`: finite bucket boundaries only (exclude `+Inf`; the last `n` entry covers all remaining)
- `n`: delta count per bucket (not cumulative)
- Null when pod restarted or no completions in interval

**`le` boundary storage decision**

Store `le` inline per series entry (not factored into `meta.json`). Rationale: boundary
lists differ per histogram (22 / 14 / 14 / 21 boundaries), scrape intervals are ~15s so
there are O(10-30) series entries per pod per run — the inline overhead is negligible.
Viz does not need a separate join step.

**Impact on `requests.json`**

`ttft_p50` in each 1s request bucket is currently null for inference-perf (harness
does not record per-request TTFT). Fix: after building `pods[].series[]`, interpolate
`ttft_p50` from the pod-scrape `ttft_hist` at the matching scrape interval and inject
into the corresponding request buckets. All request buckets within one scrape interval
(~15s) get the same `ttft_p50`. Label as `source: "vllm-scrape"` when derived this way.

**EPP queue gap**

Total wait = EPP dispatch queue time + vLLM queue time. EPP metrics are Unauthorized
in all existing runs. EPP auth fixed in runtools commit `521f728f` (secret renamed from
`inference-gateway-sa-metrics-reader-secret` to `wva-epp-metrics-token`). Thanos has
no retention for the Aug 19–21 run windows — no recovery from existing runs. Future
runs will have EPP data, and `qwait_hist` can be extended to include EPP queue time
once that data is available.

---

## Input gaps requiring runtools fixes

| Gap | Impact | Status |
|-----|--------|--------|
| EPP scrapes `Unauthorized` — wrong secret name | All EPP fields in `epp[]` are null | **Fixed** in runtools commit `521f728f` (secret `wva-epp-metrics-token`). No recovery for existing runs (Thanos retention expired). Future runs will have EPP data. |
| `scaledobject-config.json` not captured | `gpu_count`, `role`, `cost`, `min/max_replicas` are null | Open — requires SO config dump in post-run collection |
| `igw_pods.log` not collected | No per-request arrival timestamps from IGW | Open — IGW telemetry enabled on dhl-la-1708 for future runs; collector not yet wired |
| guidellm run missing vLLM pod scrapes | `pods.json` empty for guidellm-decode-heavy | Under investigation by runtools |

## Input gaps requiring WVA logging changes

| Gap | Impact | Proposal |
|-----|--------|----------|
| `analyzer-result` doesn't log `inferencePool` | EPP-to-SO linkage requires naming-convention inference | Add `inferencePool` field to the `analyzer-result` structured log line |
