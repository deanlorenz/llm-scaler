# benchmark-viz handoff

State capture at end of planning session. Use this to resume in a fresh context.

---

## What this worktree is

**Branch:** `benchmark-viz`
**Workspace:** `worktrees/benchmark-viz`
**Scope:** Visualization tooling only — takes already-processed trace data and
produces graphs and reports. NOT concerned with setup, running benchmarks,
collecting data, or extracting raw metrics.

---

## What was done this session

1. **Scoped the task** — defined what "visualization tooling" means and what it
   explicitly excludes.

2. **Defined the object model** for the input contract. The bundle is a
   directory of independently-optional JSON files organized around five object
   types: `meta`, `endpoints` (EPP + requests), `scaled_objects` (replicas,
   scaler events, pods). Full details in `session.md`.

3. **Read the reference implementation** — the old WVA repo's `autoscaling-viz`
   branch is cloned as a detached worktree at:
   ```
   worktrees/wva-viz-ref/
   ```
   The two key files are copied into this worktree for reference:
   ```
   docs/plans/benchmark-viz/render_real_trace.ref.py   (1639 lines)
   docs/plans/benchmark-viz/extract_real_trace.ref.py  (1626 lines)
   ```
   A reference panels image is at:
   ```
   docs/plans/benchmark-viz/panels-reference.png
   ```

4. **Learned the exact bundle schema** from reading the reference extractor.
   The session doc (`session.md`) has the schema, but it has **not yet been
   updated** with several corrections discovered from reading the code (see
   "What still needs doing" below).

---

## Key findings from reading the reference code

### Bundle structure (current reference)

The reference extractor produces a single `bundle.json` with these top-level keys:
```
meta, requests, replicas, system, pods, derived, self_checks
```

This is the OLD schema. The NEW schema (what we are designing) reorganizes this
into a directory:
```
bundle/
  meta.json
  endpoints.json        (EPP timeseries + request timeseries, per endpoint)
  scaled_objects.json   (replicas, scaler events, per-SO config)
  pods.json             (per-pod scrape timeseries, grouped by SO)
  coverage.json
  provenance.json
```

### Pod series field names (reference uses short keys)

The reference extractor writes pod series with short keys:
- `run` (not `running`)
- `wait` (not `waiting`)
- `kv` (KV utilization, 0–1)

The session doc incorrectly used `running`/`waiting`. This needs correcting.

### System timeseries

The reference `system[]` (renamed to `endpoints[].epp[]` in new schema) uses:
- `in_system` — request-derived L(t) concurrency (from per-request trace)
- `q_engine` — sum of per-pod `vllm:num_requests_waiting`
- `q_dispatch` — EPP `inference_objective_running_requests`
- `q_flow` — `max(0, in_system - q_dispatch)` (flow-control queue, requires requests)

### Panel 6 — scaler signal schema and design rationale

#### Why `k2_decision_table` is NOT the right input

The old reference bundle had a `k2_decision_table` that recorded the internal
k1/k2 capacity-tier decisions from the saturation_v2 analyzer's controller log
(`k2-decision`, `replica-capacity-decision`, etc.). This is **wrong** for the
new schema for two reasons:

1. **It is analyzer-specific** — `k2` is an implementation detail of the
   saturation_v2 analyzer. Other analyzers (throughput, future ones) have their
   own internal logic. A panel that only knows about k2 cannot generalize.

2. **The right abstraction already exists** — the WVA controller emits
   `analyzer-result` log lines, one per analyzer per cycle, which carry
   everything panel 6 needs in a generic, analyzer-agnostic form:
   - `rc` — required capacity (scale-up pressure signal), endpoint-level
   - `sc` — spare capacity (scale-down permission signal), endpoint-level
   - `prc` — per-replica capacity for this variant, in analyzer units
   - `reason` — how PRC was computed (generic across analyzers: `P0-store`,
     `P1-obs`, `P2-hist`, `P3-k2`, `P4-k1` for saturation; `T1-ols`,
     `T2-pinned`, `T2-default`, `T2-failed` for throughput)

   The `k2` reason code is just one of many `reason` values — it shows up as
   `P3-k2` in the `reason` field and means nothing special to the panel.

#### Analyzer metric model (established in discussion)

Each analyzer has its own metric units and computes independently:

- **Demand** — per endpoint (model-level), broken down per role (prefill/decode/both).
  Each analyzer computes its own demand in its own units (tokens for saturation,
  requests/s for throughput). A role acts as a logical sub-analyzer — you can
  treat each role as a separate signal.
- **Supply** — per SO, expressed as `ready_replicas × PRC`. PRC is in the
  analyzer's own units.
- **PRC** — per-replica capacity, expressible two ways:
  1. In analyzer units (e.g. tokens/replica for saturation)
  2. As a fraction of total endpoint demand (useful for combining signals across
     analyzers with different units — the compound optimizer works in this space)
- **RC / SC** — required capacity / spare capacity, computed after applying
  thresholds. Both are `max(0, ...)` at source, so never simultaneously positive.
  These are endpoint-level aggregates.
- **Optimizer allocation** — `desired_replicas × PRC_as_fraction_of_demand` gives
  the fraction of total demand allocated to a SO. The sum across all SOs need not
  equal 1.0 (the optimizer can decide not to satisfy all demand, or D can bound P
  in P/D scenarios).
- **Multiple analyzers** — each has its own independent `rc`/`sc`/`prc`, and each
  produces its own replica delta recommendation. The final `desired` is one signal
  (the optimizer's output). Panel 6 shows **per-analyzer** signed replica-delta
  as separate lines, one per analyzer.

#### What panel 6 shows

Signed replica-delta per analyzer: `(rc - sc) / prc`, mild log2-compressed.
Positive = scale-up pressure, negative = scale-down pressure, zero = neutral.
One line per analyzer (e.g. saturation=blue, throughput=orange).
Reason codes plotted as markers on each line at their first occurrence.
Absent analyzers shown as faded dotted lines (still computed, just not voting).

#### Schema for `scaler[]` entries needed by panel 6

From the `analyzer-result` log line, one record per analyzer per SO per cycle:
```json
{
  "t": "<epoch seconds>",
  "source": "wva",
  "event_type": "analyzer_result",
  "analyzer": "saturation",
  "variant": "primary",
  "rc": 0,
  "sc": 50000,
  "prc": 403391,
  "reason": "P3-k2",
  "role": "both"
}
```
Note: `rc`/`sc` are endpoint-level (same value across all variant records in
the same analyzer cycle). `prc` and `reason` are per-variant/SO.

From the `scaling-decision` log line, one record per SO per cycle:
```json
{
  "t": "<epoch seconds>",
  "source": "wva",
  "event_type": "scaling_decision",
  "variant": "primary",
  "action": "ScaleUp",
  "curr": 1,
  "tgt": 2
}
```

Absent-analyzer marker (when analyzer is configured but not in the list):
```json
{
  "t": "<epoch seconds — first time seen absent>",
  "source": "wva",
  "event_type": "analyzer_absent",
  "analyzer": "saturation"
}
```

The signed replica-delta plotted is `(rc - sc) / prc` with mild log2 compression:
`signed_log2(x) = log2(1+x) if x≥0 else -log2(1+|x|)`.
Tick labels are inverted back to real replica-delta units for readability.

### `derived` — what the renderer computes vs what it needs pre-computed

The renderer computes these itself from the raw timeseries:
- `itl_fit`, `sat_band`, `capacity`, `tput_knee`, `router`, `lags`

The renderer needs these **pre-computed by the extractor** (requires sources
the renderer doesn't have):
- `scaling_log` (needs controller log parsing)
- `drain_windows` per pod (needs heuristic matching against replica timeseries)

In the new schema, the renderer computes derived stats itself. The extractor
only needs to provide `scaling_log` equivalent (the `scaler[]` timeseries).

### `meta.load_duration_s` — this is wrong

`load_duration_s` is not a scalar property of the run — it belongs on the load
profile (stages). The reference extractor does put it in `meta` (derived from
`harness_delta`), but it's a convenience field. In the new schema it should
either move to `endpoints[].load[]` or be kept as a convenience summary field
with a note.

---

## What still needs doing in `session.md`

The session doc has the right structure but several fields need correction based
on the reference code findings:

1. **Pod series field names**: `run`/`wait` not `running`/`waiting`
2. **`scaler[]` schema**: split into three sub-types:
   - `analyzer_result` records (per analyzer per SO per cycle): `t, analyzer, variant, rc, sc, prc, reason, role`
   - `scaling_decision` records (per SO per cycle): `t, variant, action, curr, tgt`
   - `analyzer_absent` marker: `t, analyzer`
   - Replica transition records: `t, action, desired, ready`
3. **`derived` block**: remove everything the renderer computes itself; only keep what needs external data (the scaler log equivalent)
4. **`meta.load_duration_s`**: clarify it's a convenience summary, not a time-dependent field
5. **`endpoints[].epp[]`**: add `in_system` field (request-derived L(t), requires requests[])
6. **Pod static metadata**: add `first_metric_t` (first scrape with non-empty data)

---

## Next steps (in priority order)

1. **Update `session.md`** with the corrections above — then the input contract
   is complete and can be handed to the extractor agent.

2. **Port `render_real_trace.py`** — the reference file is at
   `docs/plans/benchmark-viz/render_real_trace.ref.py`. It needs to be adapted
   to consume the new bundle directory schema instead of the old flat `bundle.json`.
   The rendering logic itself (panels 1–6) is correct and tested — only the
   data-loading path changes.

3. **Port `publish_viz_result.sh`** — the reference is at
   `worktrees/wva-viz-ref/publish_result.sh`. Adapt to the new directory layout.

4. **Makefile targets** — add `benchmark-render` and `benchmark-publish` targets
   following the existing `##` comment convention.

5. **Reports** — deferred, design not started.

6. **Cumulative/comparison reports** — deferred.

---

## Reference locations

| Resource | Path |
|----------|------|
| Session doc (main) | `docs/plans/benchmark-viz/session.md` |
| Reference renderer | `docs/plans/benchmark-viz/render_real_trace.ref.py` |
| Reference extractor | `docs/plans/benchmark-viz/extract_real_trace.ref.py` |
| Reference panels image | `docs/plans/benchmark-viz/panels-reference.png` |
| wva-viz-ref worktree | `worktrees/wva-viz-ref/` (detached, untracked) |
| Old WVA repo | `/home/dean/code/llm-d/llm-d-workload-variant-autoscaler/` |
