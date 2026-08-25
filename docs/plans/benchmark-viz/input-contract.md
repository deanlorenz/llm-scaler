# benchmark-viz input contract

Final extractor-facing input contract for benchmark visualization.

This document is the contract that producer agents, especially benchmark extraction work, should target. It is derived from the finalized schema in [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md).

## Scope

This contract defines the processed bundle consumed by visualization tooling.
It is intentionally about **visualization input only**:
- not benchmark setup
- not benchmark execution
- not raw metric collection
- not renderer implementation details

All time values are **seconds from load start** where `t=0` is the first request arrival.

## Bundle directory layout

```text
bundle/
  meta.json              — run identity and clock alignment, always present
  provenance.json        — extractor version and source traceability, always present
  endpoints.json         — endpoint configs, load profile, EPP timeseries
  requests.json          — per-request records, optional
  scaled_objects.json    — SO configs, replica timeseries, scaler events
  pods.json              — per-pod vLLM scrape timeseries, grouped by SO
  coverage.json          — per-capability PASS FAIL WARN report, always present
```

Missing files must degrade affected panels with a clear data-not-available annotation, never a crash.

## Object model

```text
Run
├── meta
├── endpoints[]
│   ├── config
│   ├── requests[]
│   ├── load[]
│   └── epp[]
└── scaled_objects[]
    ├── config
    ├── pods[]
    │   └── series[]
    ├── replicas[]
    └── scaler[]
```

Notes:
- A single endpoint may manage multiple scaled objects.
- Scaled objects reference their parent endpoint by id.
- Pod scrapes are grouped under the scaled object they belong to.
- The renderer computes derived visualization stats itself from raw timeseries. The extractor should not precompute renderer-only derived summaries except where external sources are required, namely controller-log-derived `scaler[]`.

## `meta.json`

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Unique run identifier |
| `namespace` | string | Kubernetes namespace |
| `extracted_at` | string | ISO timestamp of extraction |
| `extractor_version` | string | Extractor git sha or version |
| `harness` | string | `inference-perf` or `guidellm` |
| `load_duration_s` | number | Convenience summary of total planned load duration. The authoritative load profile is `endpoints[].load[]` |
| `harness_start_epoch` | number | Epoch timestamp of harness start |
| `time_anchor` | object | Clock alignment metadata |

### `meta.time_anchor`

| Field | Description |
|-------|-------------|
| `method` | `cross-correlation`, `not-needed`, or `refused-short-trace` |
| `offset_s` | Solved offset in seconds |
| `corr` | Cross-correlation coefficient, or null |
| `trustworthy` | Boolean. If false, the renderer must surface this prominently |

## `endpoints.json` as `endpoints[]`

### `endpoints[].config`

| Field | Type | Description |
|-------|------|-------------|
| `endpoint_id` | string | Unique endpoint identifier |
| `model` | string | Model being served |
| `epp_name` | string | EPP resource name |
| `scaled_object_ids` | array | Scaled objects managed under this endpoint |

### `endpoints[].load[]`

| Field | Type | Description |
|-------|------|-------------|
| `t_start` | number | Stage start |
| `t_end` | number | Stage end |
| `rate_rps` | number | Target request rate |
| `in_tok` | number | Planned prompt tokens per request |
| `out_tok` | number | Planned output tokens per request |

### `endpoints[].requests[]`

Optional.

| Field | Type | Description |
|-------|------|-------------|
| `t_arr` | number | Arrival time |
| `t_dep` | number | Departure time |
| `endpoint_id` | string | Endpoint identifier |
| `in_tok` | number | Prompt token count |
| `out_tok` | number | Output token count |
| `ttft_ms` | number | Time to first token |
| `itl_ms` | number | Inter-token latency |
| `e2e_ms` | number | End-to-end latency |
| `outcome` | string | `ok`, `error`, or `truncated` |

### `endpoints[].epp[]`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Scrape time |
| `in_system` | number | Request-derived concurrency L of t. Requires `requests[]` |
| `q_dispatch` | number | EPP dispatch queue |
| `q_engine_sum` | number | Sum of per-pod waiting across all pods. Preferred for queue panels |
| `q_engine_avg` | number | Average per-pod waiting. Can mask scale-up relief |
| `ready_pods` | number | Ready pod count as seen by EPP |
| `kv_mean` | number | Mean KV cache utilization |
| `throughput_rps` | number | Observed request completion rate |

## `scaled_objects.json` as `scaled_objects[]`

### `scaled_objects[].config`

| Field | Type | Description |
|-------|------|-------------|
| `so_id` | string | Unique scaled object identifier |
| `endpoint_id` | string | Parent endpoint identifier |
| `model` | string | Model served |
| `role` | string | `prefill`, `decode`, or `both` |
| `gpu_count` | number | GPUs per replica |
| `cost` | number | Cost weight per replica |
| `min_replicas` | number | Configured minimum replicas |
| `max_replicas` | number | Configured maximum replicas |
| `engine_config` | object | vLLM engine config including `num_gpu_blocks`, `block_size`, `kv_tokens`, `gpu_mem_util`, `prefix_caching` |

### `scaled_objects[].replicas[]`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Snapshot time |
| `desired` | number | Desired replica count |
| `ready` | number | Ready replica count |
| `available` | number | Available replica count |

### `scaled_objects[].scaler[]`

Heterogeneous event records distinguished by `event_type`.

#### `analyzer_result`

One record per analyzer per scaled object per cycle.

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `analyzer_result` |
| `analyzer` | string | Analyzer name such as `saturation` or `throughput` |
| `variant` | string | Variant or scaled object identifier |
| `role` | string | Role if reported |
| `rc` | number | Required capacity at endpoint scope |
| `sc` | number | Spare capacity at endpoint scope |
| `prc` | number | Per-replica capacity for this scaled object |
| `reason` | string | Generic analyzer reason code |

Important semantics:
- `rc` and `sc` are endpoint-level and may repeat across all variant records in the same analyzer cycle.
- `prc` and `reason` are per variant.
- `rc` and `sc` are each `max(0, ...)` at source and are never simultaneously positive.
- Panel 6 derives signed replica delta from these records as `(rc - sc) / prc`.
- `k2` is **not** a dedicated schema object. `P3-k2` is only one possible `reason` value.

#### `scaling_decision`

One record per scaled object per cycle.

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `scaling_decision` |
| `variant` | string | Variant or scaled object identifier |
| `action` | string | `ScaleUp`, `ScaleDown`, or `NoChange` |
| `curr` | number | Current replica count |
| `tgt` | number | Target replica count |

#### `analyzer_absent`

Configured analyzer not in the active analyzer list.

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | First time seen absent |
| `event_type` | string | `analyzer_absent` |
| `analyzer` | string | Analyzer name |

#### `replica_transition`

Observed replica lifecycle events.

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Event time |
| `event_type` | string | `replica_transition` |
| `action` | string | `up`, `ready`, `draining`, or `down` |
| `desired` | number | Desired replicas at transition time |
| `ready` | number | Ready replicas at transition time |

## `pods.json` as `scaled_objects[].pods[]`

### Pod metadata

| Field | Type | Description |
|-------|------|-------------|
| `pod_id` | string | Pod name |
| `so_id` | string | Parent scaled object |
| `created_t` | number | Pod creation time |
| `ready_t` | number | Pod ready time |
| `first_metric_t` | number | First scrape with non-empty pod metrics |
| `setup_s` | number | Boot lag equal to `ready_t - created_t` |

### `pods[].series[]`

| Field | Type | Description |
|-------|------|-------------|
| `t` | number | Scrape time |
| `run` | number | `vllm:num_requests_running` |
| `wait` | number | `vllm:num_requests_waiting` |
| `kv` | number | KV cache utilization from 0 to 1 |
| `gen_rate` | number | Token generation rate |
| `prompt_rate` | number | Prompt token processing rate |
| `itl_ms` | number | Inter-token latency from histogram |
| `prefill_ms` | number | Mean prefill time |
| `qwait_s` | number | Mean queue wait time |
| `preempt_rate` | number | Preemptions per second |
| `pfx_hit` | number | Prefix cache hit rate from 0 to 1 |
| `stable` | boolean | True only for intervals considered valid for stable-fit calculations |

## `coverage.json`

Object with summary counts, warnings, and a `rows` array:

```json
{
  "rows": [
    {
      "scope": "so_id or endpoint_id",
      "capability": "Calibrate A",
      "result": "FAIL",
      "detail": "n_intervals=3, need ≥8"
    }
  ],
  "n_pass": 0,
  "n_fail": 0,
  "warnings": []
}
```

## `provenance.json`

```json
{
  "extractor_version": "git-sha",
  "extracted_at": "ISO timestamp",
  "run_dir": "source run directory path",
  "harness": "inference-perf or guidellm"
}
```

## Caveats

- Pod series are sparse.
- `requests.json` may be absent.
- `scaler[]` depends on controller-log capture.
- If `time_anchor.trustworthy` is false, the renderer must visibly warn.
- Prefer `q_engine_sum` over `q_engine_avg` for queue-depth interpretation.
