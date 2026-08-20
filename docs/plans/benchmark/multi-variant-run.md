# Multi-variant WVA benchmark run — plan and log

## Why

Every run captured so far this session (`quick_smoke` via `inference-perf`,
`decode_heavy` via `guidellm`, both under `hack/benchmark/results/`) is
single-variant, against `dhl-la-1708`'s one existing decode deployment.
Several of `extract_real_trace.py`'s own coverage checks stay "not supported
by this run" on both — some because these scenarios never generated enough
load (Calibrate A, exercise the 0.85 ceiling), some structurally because they
need more than one variant to mean anything (router imbalance, a real
two-variant efficiency comparison).

`docs/plans/benchmark/observability-gaps.md` §3 sketches
`analyze_wva_decisions.py`'s design but stops short of building it, explicitly
because "no multi-variant run exists yet to validate against." A real
multi-variant run is what unblocks building that tool, not just exercising
more coverage checks. `docs/developer-guide/two-variant-wva-benchmark.md`
already documents the two-variant efficiency-aware benchmark design this run
follows.

## Cluster safety (standing rules, still in force)

1. **This is a shared cluster and this account has cluster-admin rights — do
   NOT run any cluster-scoped command, ever.** Stay inside the `dhl-la-1708`
   namespace for every read and every write. If a step would require a
   cluster-scoped verb (anything not `-n dhl-la-1708`/namespaced), stop and
   ask rather than running it.
2. **Free all GPUs when done — no exceptions.** Park the namespace
   (`make so-park SO=<name> ...`) as soon as the run is finished, and verify
   the decode pod(s) actually terminated (not just the ScaledObject scaled
   to 0 — confirm with `kubectl get pods -n dhl-la-1708`) before considering
   the run closed out. This applies even if a later step in this plan fails
   or is abandoned partway — GPUs get freed regardless.
3. Run `make benchmark-verify-scaledobjects BENCHMARK_NAMESPACE=dhl-la-1708 BENCHMARK_REPORT_ONLY=true`
   before any real run — also wired as a hard gate into `benchmark-run`.
4. Never patch the llm-d Deployment directly, never hand-patch a ScaledObject
   trigger — see `deploy/lib/scaledobject.sh` for the sanctioned path
   (`scaledobjects-plan`/`scaledobjects-apply`).
5. Be conservative with GPUs generally — a sweep agent reaps idle GPUs on
   this shared cluster, and other users share it.
6. Coordinated with the user before starting (2026-08-21): confirmed before
   driving any load against `dhl-la-1708`.

## Plan

Five sub-tasks (as directed):

1. **Add another variant to the llm-d deployment.**
   `dhl-la-1708`'s stack was installed via llm-d's own **optimized-baseline**
   guide, not the `modelservice` Helm chart `benchmark-standup` uses — its
   `InferencePool` selects pods purely on `llm-d.ai/guide: optimized-baseline`,
   and its decode Deployment never sets the `llm-d.ai/inference-serving`
   marker `add_variant.py` required. Fixed `add_variant.py`'s primary-detection
   to accept that marker's absence (only an explicit `"false"` now
   disqualifies), and stripped stale `gpu-reaper.io/*` annotations that were
   getting cloned onto the secondary Deployment (real history about the
   primary, false about a brand-new object). Dry-run against the default
   `variants/v2-tp1-cheaper.yaml` config confirmed clean output.
   **Caveat**: that config's efficiency note assumes a TP=2 primary (matching
   `two-variant-wva-benchmark.md`'s scenario); this stack's primary is TP=1.
   With both variants TP=1/1-GPU, cost and capacity-per-replica no longer
   diverge — the optimizer will simply prefer the cheaper (5.0 vs 10.0)
   variant once load exceeds one replica's worth. Still a real, valid
   multi-variant run (two variants, one modelID, real k1/k2 + decision-table
   data, router-imbalance and cost-column coverage all become exercisable) —
   just not a demonstration of the TP-driven "not-simply-cheapest" nuance.
2. **Fix the WVA registration/plan for both variants.** Use
   `make scaledobjects-plan WVA_DEFAULT_SO_PLAN=<path>` /
   `make scaledobjects-apply WVA_DEFAULT_SO_PLAN=<path>` (arbitrary path, can
   copy/edit before applying — this is the sanctioned way to fix any
   drift/registration gap, not hand-patching) if the secondary's ScaledObject
   needs any adjustment after `add_variant.py` creates it.
3. **Run a regular benchmark workload** against the shared endpoint
   (`make benchmark-run ... BENCHMARK_TWO_VARIANT_SECONDARY_SUFFIX=v2`).
4. **Collect and extract info for all variants** (`post_run_analyze.sh`,
   `extract_real_trace.py`, the `dump_*` scripts — confirm each buckets
   correctly by variant/ScaledObject name, not just the primary).
5. **Integrate into the visualization** (`plot_two_variant_pipeline.py` /
   `render_real_trace.py`, then `publish_viz_result.sh` the same way the two
   single-variant runs already under `hack/benchmark/results/` were
   published).

Plus, from the original plan:

- [ ] Park the namespace and verify the decode pod terminated when done
- [ ] Revisit `analyze_wva_decisions.py`'s §3 design in `observability-gaps.md`
      against the real multi-variant data; build it if the design holds up
- [ ] Update `observability-gaps.md` with what the run did or didn't reveal

## Log

- **2026-08-21** — Plan written. Read `two-variant-wva-benchmark.md` and
  `observability-gaps.md` §3. Pushed `worktree-multi-variant` to
  `origin/worktree-multi-variant`.
- **2026-08-21** — Cluster access fixed: `~/.kube/config` had an expired
  token and no `dhl-la-1708` context at all. Copied `~/.kube/la-test` into
  this worktree as `.kube/config` (gitignored) — has a working, current
  `dhl-la-1708` context. Confirmed still parked: decode Deployment 0/0
  replicas, ScaledObject `autoscaling.keda.sh/paused-replicas: "0"`, no
  decode pods. Confirmed small model live: container args
  `Qwen/Qwen3-0.6B --gpu-memory-utilization=0.30`, ScaledObject trigger
  `modelID` matches.
- **2026-08-21** — Added `biranofer/llm-scaler` (Ofer Biran's fork) as a
  read-only remote `ofer`. The multi-variant setup tooling itself
  (`add_variant.py`, `two-variant-wva.yaml`, `v2-tp1-cheaper.yaml`) is
  already merged here via PR #1308. Four commits on
  `ofer/benchmark-tooling-fixes` are not yet merged: a staged burst scenario,
  a `.yaml.in` recognition fix for the `inference-perf` harness, two-variant
  plot title/stage-marker/live-KEDA-policy metadata, and a Thanos fallback
  for rotated controller logs — the last two are directly relevant to this
  run's reliability and post-processing; not cherry-picked yet, pending call.
- **2026-08-21** — Sub-task 1 in progress: fixed `add_variant.py` (see Plan
  §1 above) and confirmed a clean dry-run. Not yet applied for real.
