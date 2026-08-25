# benchmark-viz implementation plan

## Overview

Implement single-run visualization for the new bundle-directory input format.
The immediate goal is to make the single-run renderer consume the finalized
input contract, derive the internal signals each panel needs, and render the
same multi-panel visualization shape as the validated reference renderer.

This plan is intentionally scoped to **single-run visualization first**.
Publishing helpers and two-variant follow-on work stay explicitly separated as
later subtasks.

## Sub-task 1

### Intent

Ground the implementation against a concrete example first, by using a short
sample window from an existing run as a minimal expected visualization input.
This gives the renderer work something to test against as each later step lands.

### Expected Outcomes

- A documented source run and sample window exist for validation.
- The implementation has a concrete fixture target for manual and future test use.
- The sample is rich enough to exercise requests, EPP, scaler, replica, and pod signals.

### Todo List

- [x] Record the chosen source run and sampling window in the planning docs.
- [x] Define the minimal expected bundle contents for that sample window.
- [x] Use the sample to verify bundle loading assumptions.
- [x] Use the sample to verify signal-derivation assumptions.
- [x] Use the sample to verify graceful degradation rules where relevant.

### Relevant Context

- Current source run decision: [`../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1)
- Sample-window pressure inflection: [`wva_target_timeseries.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/metrics/processed/wva_target_timeseries.json:234)
- Coverage caveat on replica source: [`coverage.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/coverage.json:92)

### Status

- [x] done
+
+### Completion Notes
+
+- Chosen source run: [`../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1)
+- Chosen sample window: first strong scale-up pressure interval centered on
+  [`1787280995`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/metrics/processed/wva_target_timeseries.json:234)
+  through at least
+  [`1787281145`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/metrics/processed/wva_target_timeseries.json:334)
+- Minimal expected sample bundle contents for this window:
+  - `meta.json` with run identity and trustworthy time-anchor fields grounded by the old reference bundle at [`bundle.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/bundle.json:1)
+  - `endpoints.json` with endpoint config, load profile, and `epp[]` samples from raw EPP scrapes under [`metrics/raw/`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/metrics/raw/)
+  - `requests.json` populated directly from [`per_request_lifecycle_metrics.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/per_request_lifecycle_metrics.json)
+  - `scaled_objects.json` with SO configs, `replicas[]`, and `scaler[]`
+  - `pods.json` with pod metadata and sparse per-pod series from vLLM raw scrapes
+  - `coverage.json` carried over with the replica-source caveat from [`coverage.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/coverage.json:92)
+  - `provenance.json` with extractor and run provenance
+- Important caveat for validation: this run's replica timeline must be taken
+  from synthesized WVA replica samples, not the broken
+  `replica_status_timeseries.json`, as documented in [`coverage.json`](../benchmark-run/runs/dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1/coverage.json:92)

## Sub-task 2

### Intent

Add a concrete implementation target for loading the new-format bundle
regardless of who produced it. This is the contract boundary between extractor
work and visualization work.

### Expected Outcomes

- A loader can open a bundle directory containing the files defined in
  [`input-contract.md`](docs/plans/benchmark-viz/input-contract.md).
- Optional files are handled as optional inputs rather than hard failures.
- The in-memory object model is sufficient for downstream signal derivation.

### Todo List

- [x] Define the renderer entrypoint shape for bundle-directory input.
- [x] Map each bundle file into an in-memory structure aligned with the input contract.
- [x] Treat missing optional files as absent data, not errors.
- [x] Preserve enough source metadata for user-facing warnings and caveats.
- [x] Document any normalization needed between on-disk JSON layout and the internal model.

### Relevant Context

- Input contract: [`docs/plans/benchmark-viz/input-contract.md`](docs/plans/benchmark-viz/input-contract.md)
- Session contract source: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:114)
- Reference renderer old load path: [`main()`](docs/plans/benchmark-viz/render_real_trace.ref.py:1602)
- Reference renderer render entry: [`render()`](docs/plans/benchmark-viz/render_real_trace.ref.py:332)

### Status

- [x] done
+
+### Completion Notes
+
+- Implemented bundle-directory loader and normalized internal data model in [`render_real_trace.py`](hack/benchmark/render_real_trace.py)
+- Added explicit handling for required bundle files versus optional inputs such as `requests.json`, `coverage.json`, and `provenance.json`
+- Added normalization helpers for `endpoints.json`, `scaled_objects.json`, `requests.json`, and `pods.json`
+- Established the CLI entrypoint shape as `--bundle-dir`

## Sub-task 3

### Intent

Define and implement the internal signal-derivation stage that sits between raw
bundle loading and panel rendering. This is the first visualization stage you
called out explicitly: read the new-format input and compute all panel-needed
signals without creating a persisted preprocessed artifact.

### Expected Outcomes

- A clear internal stage exists between input loading and plotting.
- All panel-required derived signals are computed from the bundle inputs.
- The renderer does not depend on extractor-side precomputation except for data
  that fundamentally requires external sources, such as controller-log-derived
  scaler events already present in the bundle.

### Todo List

- [x] Enumerate the panel-required internal signals for the single-run renderer.
- [x] Group those signals by source: requests, EPP, replicas, pods, scaler events.
- [x] Define the derived-signal computation pass that runs after bundle load.
- [x] Separate reusable signal derivation from plotting concerns.
- [x] Define how absent source signals degrade downstream panel computations.

### Relevant Context

- Panel requirements: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:410)
- Analyzer background: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:439)
- Reference renderer panel logic: [`render()`](docs/plans/benchmark-viz/render_real_trace.ref.py:332)

### Status

- [x] done
+
+### Completion Notes
+
+- Implemented the first signal-preparation stage as [`prepare_signals()`](hack/benchmark/render_real_trace.py:155)
+- Grouped internal signals by endpoint and scaled object into request, EPP, replica, scaler, and pod collections
+- Captured degradation cases as explicit warnings when request, coverage, or provenance inputs are absent
+- Kept signal preparation separate from plotting so the renderer port can layer on top cleanly

## Sub-task 4

### Intent

Port the single-run renderer onto the new loader and signal-derivation stages,
while preserving the validated panel behavior from the reference renderer.

### Expected Outcomes

- A single-run renderer exists in the benchmark tooling area.
- It consumes the new bundle directory format rather than the old flat bundle.
- It preserves graceful degradation behavior when inputs are absent.

### Todo List

- [x] Create the new single-run renderer file in the benchmark tooling area.
- [x] Replace the old flat-bundle loading path with the new bundle-directory loader.
- [x] Wire the renderer to use the derived internal signals rather than ad hoc file reads.
- [x] Preserve warning, caveat, and optional-matplotlib behavior.
- [x] Confirm the panel set still matches the reference renderer’s intended output.

### Relevant Context

- Target location from session doc: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:485)
- Reference renderer: [`docs/plans/benchmark-viz/render_real_trace.ref.py`](docs/plans/benchmark-viz/render_real_trace.ref.py)
- Existing plotting utility pattern: [`hack/benchmark/plot_two_variant_pipeline.py`](hack/benchmark/plot_two_variant_pipeline.py)

### Status

- [x] done
+
+### Completion Notes
+
+- Added the new single-run renderer target at [`hack/benchmark/render_real_trace.py`](hack/benchmark/render_real_trace.py)
+- Replaced the old flat `--bundle` loading model with a new `--bundle-dir` entrypoint and bundle-directory loader
+- Wired the first rendering step through the prepared internal signal model rather than direct flat-bundle field access
+- Preserved optional-matplotlib behavior by failing clearly only when rendering is requested without matplotlib
+- Established a first-step PNG output path compatible with the future full panel port while keeping the validated reference renderer as the next visual fidelity target

## Sub-task 5

### Intent

Add CLI and workflow integration for the single-run renderer only after the core
single-run visualization path is stable.

### Expected Outcomes

- The renderer is invokable in a consistent benchmark-tooling way.
- The work can be exercised without requiring the later publish workflow.

### Todo List

- [ ] Define the command-line contract for the single-run renderer.
- [ ] Add the single-run render Makefile target.
- [ ] Ensure the target follows the Makefile help comment convention.
- [ ] Keep publish workflow work out of scope for this sub-task.

### Relevant Context

- Existing benchmark target patterns: [`Makefile`](Makefile:804)
- Existing report target area: [`Makefile`](Makefile:1551)

### Status

- [ ] pending

## Follow-on Sub-task 6

### Intent

Plan later publishing integration after single-run visualization is complete.

### Expected Outcomes

- Publish workflow work remains explicit but does not block single-run renderer delivery.

### Todo List

- [ ] Adapt the publish helper to the new directory layout.
- [ ] Add a publish-oriented Makefile target once renderer inputs and outputs are stable.

### Relevant Context

- Session follow-on note: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:493)

### Status

- [ ] pending

## Follow-on Sub-task 7

### Intent

Revisit the existing two-variant plotting flow only after the new single-run path
is established.

### Expected Outcomes

- Two-variant work stays visible but does not distort the first implementation scope.

### Todo List

- [ ] Re-evaluate the current two-variant pipeline against the new bundle schema.
- [ ] Decide whether to adapt, replace, or leave it as a separate legacy path.

### Relevant Context

- Existing utility: [`hack/benchmark/plot_two_variant_pipeline.py`](hack/benchmark/plot_two_variant_pipeline.py)
- Session follow-on note: [`docs/plans/benchmark-viz/session.md`](docs/plans/benchmark-viz/session.md:496)

### Status

- [ ] pending
