# benchmark-viz implemented code design

Concrete implementation design snapshot for the current single-run benchmark
visualization work.

This document captures the code that now exists, its concrete components, and
how data flows through them. It should stay aligned with the higher-level plan
in [`benchmark-viz-plan.md`](docs/plans/benchmark-viz/benchmark-viz-plan.md) and
with the contract in [`input-contract.md`](docs/plans/benchmark-viz/input-contract.md).

## Scope of current implementation

The current implementation covers the first executable slice of single-run
visualization:

1. bundle-directory CLI entrypoint
2. bundle file loading
3. normalization into internal Python structures
4. first signal-preparation stage
5. pre-check and post-check reporting
6. minimal PNG output proving the pipeline works end to end

It does **not yet** port the full validated multi-panel renderer from
[`render_real_trace.ref.py`](docs/plans/benchmark-viz/render_real_trace.ref.py).

## Implemented components

### Renderer module

- [`hack/benchmark/render_real_trace.py`](hack/benchmark/render_real_trace.py)

### Primary data structures

- [`BundleData`](hack/benchmark/render_real_trace.py:27)
  - loaded raw bundle inputs
- [`PreparedSignals`](hack/benchmark/render_real_trace.py:41)
  - grouped internal signal collections used by rendering stages
- [`InputCheckReport`](hack/benchmark/render_real_trace.py:56)
  - pre-check status of bundle files, missing data, warnings, and fallbacks
- [`OutputReport`](hack/benchmark/render_real_trace.py:69)
  - post-check status of produced outputs and known limitations
- [`RenderSummary`](hack/benchmark/render_real_trace.py:80)
  - first-step summary object used by the current minimal PNG renderer

## Call map

Current call flow from CLI:

1. [`main()`](hack/benchmark/render_real_trace.py:402)
2. [`parse_args()`](hack/benchmark/render_real_trace.py:103)
3. [`load_bundle()`](hack/benchmark/render_real_trace.py:199)
   - [`require_bundle_file()`](hack/benchmark/render_real_trace.py:127)
   - [`load_json_file()`](hack/benchmark/render_real_trace.py:114)
   - [`load_optional_json()`](hack/benchmark/render_real_trace.py:120)
   - [`normalize_endpoints()`](hack/benchmark/render_real_trace.py:135)
   - [`normalize_scaled_objects()`](hack/benchmark/render_real_trace.py:144)
   - [`normalize_requests()`](hack/benchmark/render_real_trace.py:155)
   - [`normalize_pods()`](hack/benchmark/render_real_trace.py:166)
4. [`prepare_signals()`](hack/benchmark/render_real_trace.py:260)
5. [`build_render_summary()`](hack/benchmark/render_real_trace.py:337)
6. [`render_summary_png()`](hack/benchmark/render_real_trace.py:352)
7. reporting output from:
   - [`print_input_report()`](hack/benchmark/render_real_trace.py:380)
   - [`print_output_report()`](hack/benchmark/render_real_trace.py:391)

## Data flow

### Stage 1: CLI input

The renderer accepts:
- [`--bundle-dir`](hack/benchmark/render_real_trace.py:106)
- [`--out`](hack/benchmark/render_real_trace.py:107)

This establishes the new directory-based input boundary.

### Stage 2: Bundle loading

[`load_bundle()`](hack/benchmark/render_real_trace.py:199) reads the bundle
contract files and returns a [`BundleData`](hack/benchmark/render_real_trace.py:27).

Required files:
- `meta.json`
- `endpoints.json`
- `scaled_objects.json`
- `pods.json`

Optional files:
- `requests.json`
- `coverage.json`
- `provenance.json`

### Stage 3: Input checking and normalization

During bundle load, the code:
- records required files that were present
- records optional files that were absent
- records missing-data warnings
- records replica fallbacks when indicated by coverage warnings
- normalizes top-level JSON layout into stable Python structures

Normalization functions:
- [`normalize_endpoints()`](hack/benchmark/render_real_trace.py:135)
- [`normalize_scaled_objects()`](hack/benchmark/render_real_trace.py:144)
- [`normalize_requests()`](hack/benchmark/render_real_trace.py:155)
- [`normalize_pods()`](hack/benchmark/render_real_trace.py:166)

### Stage 4: Signal preparation

[`prepare_signals()`](hack/benchmark/render_real_trace.py:260) transforms the
raw bundle into grouped internal collections.

Produced groupings:
- `endpoint_by_id`
- `requests_by_endpoint`
- `epp_by_endpoint`
- `scaled_object_by_id`
- `replicas_by_so`
- `scaler_by_so`
- `pods_by_so`

This is the first implementation of the internal signal-preparation stage from
the design.

### Stage 5: Output preparation

[`build_render_summary()`](hack/benchmark/render_real_trace.py:337) creates the
current first-step output model:
- title
- endpoint count
- scaled-object count
- request count
- warning count

[`render_summary_png()`](hack/benchmark/render_real_trace.py:352) writes a
minimal PNG proving the end-to-end pipeline works.

### Stage 6: Post-check reporting

After rendering, the code prints a post-check status via
[`print_output_report()`](hack/benchmark/render_real_trace.py:391), including:
- output PNG path
- generated outputs
- limitations still in force

## Pre-check reporting behavior

The pre-check report is represented by [`InputCheckReport`](hack/benchmark/render_real_trace.py:56)
and printed by [`print_input_report()`](hack/benchmark/render_real_trace.py:380).

It captures:
- required files found
- optional files missing
- warnings
- fallbacks taken

Currently implemented pre-check conditions include:
- missing optional `requests.json`
- missing optional `coverage.json`
- missing optional `provenance.json`
- `time_anchor.trustworthy == false`
- replica fallback inferred from coverage warning text when replica status was
  synthesized from WVA metrics

## Post-check reporting behavior

The post-check report is represented by [`OutputReport`](hack/benchmark/render_real_trace.py:69)
and printed by [`print_output_report()`](hack/benchmark/render_real_trace.py:391).

It captures:
- generated output files
- failed outputs
- limitations

Currently implemented post-check limitations include:
- current PNG is summary-only, not yet the full multi-panel renderer
- panel-specific graceful degradation is not yet implemented

## Compatibility notes

The current code is intentionally compatible with the new contract’s directory
layout, not with the old flat reference bundle.

It is also intentionally only a **first renderer slice**. The validated visual
logic still lives in [`render_real_trace.ref.py`](docs/plans/benchmark-viz/render_real_trace.ref.py).
The next renderer evolution should replace the summary renderer with the real
panel logic while keeping the new loader, signal stage, and reporting structure.
