# Multi-variant WVA benchmark run — plan and log

## Status: COMPLETE (2026-08-21)

All five sub-tasks done, `observability-gaps.md` updated, `analyze_wva_decisions.py`
built and verified, both runs published under `hack/benchmark/results/`,
cluster parked and pod termination verified. Nothing pending. If resuming
anyway (e.g. to run a TP-asymmetric variant to test the caveat in §1
below), read the Log section in full first — it's in chronological order.

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

1. **[DONE] Add another variant to the llm-d deployment.**
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
   Chose the newly-cherry-picked `burst_4k1000` scenario (RPS 4→14→4 over
   300/600/600s) over `quick_smoke`/`prefill_heavy` specifically because it's
   built to force the scale-up/down cycling that never happened on this
   session's earlier single-variant runs (the Calibrate-A / 0.85-ceiling
   coverage gaps). This worktree had no `llm-d-benchmark` CLI checkout yet
   (`make benchmark-install`), which needed two local fixes to get through
   non-interactively (no sudo available in this environment):
   - the CLI's own `install.sh` runs `sudo apt-get update` unconditionally
     on Ubuntu even when every tool it would install is already present;
     commented that one line out in the gitignored, untracked vendor clone
     (`llm-d-benchmark/install.sh`) — not a repo change.
   - the default `python3` on PATH here is a uv-managed build missing
     `ensurepip`, which broke the venv-creation path; used
     `BENCHMARK_UV=true` (uv-based venv) instead, as the Makefile's own
     error message already suggests.
4. **Collect and extract info for all variants** (`post_run_analyze.sh`,
   `extract_real_trace.py`, the `dump_*` scripts — confirm each buckets
   correctly by variant/ScaledObject name, not just the primary).
   `post_run_analyze.sh` ran clean except two real gaps:
   - `dump_wva_full_timeseries.py` reported 0 WVA snapshots despite 222 raw
     scrapes existing with real per-variant data (both variants' `wva_*`
     series present, 96 series/scrape). Root cause: its `WVA_POD_PATTERN`
     required `...-controller-manager` in the filename, but
     `scrape_wva_metrics.sh` writes the fixed literal `wva-controller`
     (matching `extract_real_trace.py`'s own `scan_raw()`, which already
     used the correct literal match) — a real bug, not an environment
     issue. Fixed the pattern to accept both; re-ran, now 222/222 parsed.
   - `dump_epp_throughput.py` got 0 snapshots because every EPP scrape
     during the run returned `Unauthorized`. Cause: llm-d-benchmark's
     upstream `collect_metrics.sh` reads a bearer token from a hardcoded
     secret name (`inference-gateway-sa-metrics-reader-secret`, overridable
     via `LLMDBENCH_EPP_METRICS_SECRET`), which only exists on a
     `benchmark-standup`-installed stack. `dhl-la-1708` was installed via
     llm-d's own guide instead, and has a differently-named token secret
     (`wva-epp-metrics-token`, created by this repo's own monitoring setup).
     **Not recoverable for this run** — the scrapes already happened and
     failed; EPP-derived request-rate is permanently missing from this
     run's data. For a future run on this same stack, pass
     `LLMDBENCH_EPP_METRICS_SECRET=wva-epp-metrics-token` to fix it live.
5. **Integrate into the visualization** (`plot_two_variant_pipeline.py` /
   `render_real_trace.py`, then `publish_viz_result.sh` the same way the two
   single-variant runs already under `hack/benchmark/results/` were
   published).

Plus, from the original plan:

- [x] Park the namespace and verify the decode pod terminated when done —
      both variants parked (`make so-park SO=all`), pod termination
      confirmed via `kubectl get pods`, not just trusted from ScaledObject
      state. Namespace back to its original baseline.
- [x] Revisit `analyze_wva_decisions.py`'s §3 design in `observability-gaps.md`
      against the real multi-variant data; build it if the design holds up
      — it held (0 counterexamples across 259 dual-variant cycles from the
      real `both-variants-live` run); built as
      `hack/benchmark/analyze_wva_decisions.py` /
      `make benchmark-analyze-decisions`, verified against both published
      runs plus a swapped-cost sanity check that confirms it actually
      discriminates.
- [x] Update `observability-gaps.md` with what the run did or didn't reveal
      — §7 added: the paused-variant-exclusion finding, the two dump-script
      bugs/gaps found and one fixed, and the `analyze_wva_decisions.py`
      build decision.

## Log

Chronological. Each entry is dated 2026-08-21; the actual work spanned
roughly 02:00–07:00 local time (IDT) that night, including the ~25-minute
`burst_4k1000` load window twice and an ~20-minute result-collection tail
each time.

1. Plan written. Read `two-variant-wva-benchmark.md` and
   `observability-gaps.md` §3. Pushed `worktree-multi-variant` to
   `origin/worktree-multi-variant`.
2. Cluster access fixed: `~/.kube/config` had an expired token and no
   `dhl-la-1708` context at all. Copied `~/.kube/la-test` into this
   worktree as `.kube/config` (gitignored) — has a working, current
   `dhl-la-1708` context. Confirmed still parked: decode Deployment 0/0
   replicas, ScaledObject `autoscaling.keda.sh/paused-replicas: "0"`, no
   decode pods. Confirmed small model live: container args
   `Qwen/Qwen3-0.6B --gpu-memory-utilization=0.30`, ScaledObject trigger
   `modelID` matches.
3. Added `biranofer/llm-scaler` (Ofer Biran's fork) as a read-only remote
   `ofer`. The multi-variant setup tooling itself (`add_variant.py`,
   `two-variant-wva.yaml`, `v2-tp1-cheaper.yaml`) is already merged here
   via PR #1308. Four commits on `ofer/benchmark-tooling-fixes` were not
   yet merged: a staged burst scenario, a `.yaml.in` recognition fix for
   the `inference-perf` harness, two-variant plot title/stage-marker/
   live-KEDA-policy metadata, and a Thanos fallback for rotated controller
   logs.
4. Sub-task 1 (add a variant): fixed `add_variant.py`'s primary-detection
   for this stack's `optimized-baseline`-guide topology (see Plan §1) and
   confirmed a clean `--dry-run`.
5. Cherry-picked 3 of Ofer's 4 `benchmark-tooling-fixes` commits: staged
   burst scenarios (`test/benchmark/scenarios/burst_4k{250,1000}.yaml.in`),
   two-variant plot title/stage-marker/live-KEDA-policy metadata
   (`plot_two_variant_pipeline.py`), and the Thanos fallback for rotated
   controller logs (`dump_wva_target_timeseries.py`). Skipped the 4th
   (`.yaml.in` recognition for `inference-perf`, `96b7a71c`) — that exact
   bug was already independently fixed in this tree's `Makefile` (and ours
   also covers a bare `.yaml` suffix theirs didn't); resolved the conflict
   by keeping ours and skipped the commit rather than create an empty one.
6. Applied `add_variant.py` for real against `dhl-la-1708`. Both
   ScaledObjects present, secondary `Ready=True`/`Active=True`/`Paused=False`,
   KEDA scaled the secondary Deployment to its `minReplicaCount=1`
   immediately. Primary stayed parked (paused, 0/0), untouched.
7. Ran the pre-flight gate that should have run first (`make
   benchmark-verify-scaledobjects BENCHMARK_NAMESPACE=dhl-la-1708
   BENCHMARK_REPORT_ONLY=true`): **2 ok, 0 drift** — both variants
   correctly registered. Sub-task 2 (fix the WVA plan) turned out to need
   no action: nothing was drifted to begin with.
8. This worktree had no `llm-d-benchmark` CLI checkout yet. `make
   benchmark-install` needed two local, non-repo fixes to get through with
   no sudo available: commented out `install.sh`'s unconditional `sudo
   apt-get update` in the gitignored vendor clone (every tool it would
   otherwise install was already present), and used `BENCHMARK_UV=true`
   since the default `python3` on PATH is a uv-managed build missing
   `ensurepip`. See Plan §3 for detail.
9. **Run 1** (`burst_4k1000`, primary left paused as an intended inert
   baseline): launched via `make benchmark-run BENCHMARK_ENV=dhl-la-1708
   BENCHMARK_WORKLOAD=burst_4k1000 BENCHMARK_TWO_VARIANT_SECONDARY_SUFFIX=v2`.
   Completed clean: 634 files, 8.3GB `per_request_lifecycle_metrics.json`,
   `controller.log` (4143 lines, live in-run capture), 222 WVA `/metrics`
   scrapes (0 scrape errors). Results dir:
   `dean-20260821-032557-057/results/inference-perf-1787271994-tf4shc_1`
   (local-only, gitignored, ~7.9GB — not published, see step 15).
10. `post_run_analyze.sh` on run 1: fixed a real bug in
    `dump_wva_full_timeseries.py` (wrong pod-filename pattern; 0→222/222
    parsed after the fix) and found — but could not recover — an EPP-scrape
    `Unauthorized` auth gap (wrong secret name for a non-`benchmark-standup`
    stack). See Plan §4.
11. `matplotlib` wasn't installed for the `python3` the Makefile actually
    invokes (PATH resolves to a uv-managed interpreter that refuses
    external installs); installed it for `/usr/bin/python3` instead and
    prefixed `PATH=/usr/bin:$PATH` on every subsequent extraction/render
    command rather than touching the Makefile.
12. Full pipeline on run 1 (`make benchmark-extract-trace` — ran in the
    background, ~15+ min for the 8.3GB file — then `benchmark-render-trace`,
    `benchmark-decision-table`): 13 PASS / 4 FAIL coverage. `Calibrate A`
    and `Router imbalance measurable` now PASS — both were structurally
    blocked on this session's prior single-variant runs. New FAILs:
    `Trust B`, `Queue (a) material` (the EPP gap above), `rho model valid
    at top` (real signal: preempt/s≈0.96 at peak). `Exercise the 0.85
    ceiling` still failed even at the 14 RPS peak. A self-check flagged
    engine occupancy exceeding request-derived in-system count on 6.5% of
    scrapes; read the aggregation code (`anchor_offset` in
    `extract_real_trace.py`) — it sums pod occupancy variant-agnostically
    by design, no evidence this was two-variant-specific rather than an
    artifact of the higher/burstier rate. Recorded as open, not root-caused
    (and didn't recur on run 2, which supports "not two-variant-specific").
13. **Critical finding**: all 241 decision-table rows were the secondary
    variant — the primary never appeared in a single `analyzer-result`
    cycle. Confirmed directly from `controller.log`'s raw JSON payloads:
    `variants` listed only the secondary, every cycle, the whole run.
    **A paused ScaledObject is excluded from WVA's per-model variant
    grouping entirely** — not "correctly not scaled," structurally absent.
    So run 1 validated the deployment/registration path for real, but
    never exercised WVA's actual cross-variant cost-aware comparison.
    Also, separately: `wva_current_replicas` for the paused primary read
    `1` mid-run while `kubectl get pods` showed zero primary pods the
    entire run — a second live occurrence of `observability-gaps.md`'s
    already-logged stale-gauge gap #1.
14. **User decision** (asked mid-run): unpause the primary via `make
    so-resume` and re-run, to get real two-live-variant decision data.
    Chose to proceed rather than stop and document run 1 as-is. Primary
    now holds a GPU continuously until re-parked. User then went to sleep;
    remaining steps ran autonomously, with parking-and-verifying GPU
    release treated as the non-negotiable last step regardless of outcome.
15. Unpaused the primary (`make so-resume WVA_NS=dhl-la-1708
    SO=optimized-baseline-nvidia-gpu-vllm-decode-wva`), waited for its pod
    to become ready, then confirmed directly from a fresh `analyzer-result`
    payload that both variants now appeared together before committing to
    another full run.
16. **Run 2** (`burst_4k1000`, both variants live): same invocation as
    run 1. Completed clean: 452 files, `controller.log` (5829 lines), 236
    WVA scrapes (0 errors). Results dir:
    `dean-20260821-055002-068/results/inference-perf-1787280639-kjxx2t_1`
    (local-only, gitignored, ~7.5GB raw workspace — see step 20 for what
    was actually published from it).
17. `post_run_analyze.sh` on run 2: the `dump_wva_full_timeseries.py` fix
    held (236/236 parsed again); EPP throughput still 0/0 (same
    pre-existing secret-name gap, unrelated to which variant is paused).
18. Full pipeline on run 2: 12 PASS / 5 FAIL coverage, **no self-check
    failure this time** (supports step 12's read that the occupancy-anchor
    warning was rate-related, not two-variant-structural).
    `benchmark-decision-table` produced 518 rows / 259 dual-variant cycles,
    each with per-variant `analyzer_prc`/`decision_action`/`applied_target`.
19. Validated `observability-gaps.md` §3's sketched design against these
    259 real cycles two ways: first a manual/spot-check read of the raw
    rows (grouped by timestamp, comparing `decision_action` and
    `decision_curr` across both variants), then a small ad hoc Python scan
    of the full JSON using the correct `variant`/`decision_curr`/
    `decision_action` fields. **Zero cycles** showed the flagged pattern (a
    more-expensive variant scaling while a cheaper one sits idle with
    spare capacity) — the cheaper secondary was consistently pushed toward
    its max first, and the primary only climbed once the secondary was at
    or near its own max. Caveat: both variants are TP=1 here, so cost and
    capacity-per-replica never diverge in this run — whether the check
    holds when they genuinely diverge (the actual point of saturation-V2's
    cost-awareness) is untested.
20. Published both runs via `hack/benchmark/publish_viz_result.sh`:
    `hack/benchmark/results/20260821-burst_4k1000-secondary-only` and
    `hack/benchmark/results/20260821-burst_4k1000-both-variants-live`
    (bundle.json + coverage.json + panels.png + provenance.json +
    wva_decision_table.{json,txt} each — small, committed, no per-request
    or raw-scrape data). Committed and pushed.
21. **Cleanup, the highest-priority step**: `make so-park WVA_NS=dhl-la-1708
    SO=all`, then polled `kubectl get pods -n dhl-la-1708 -l
    llm-d.ai/role=decode` until it returned empty — actually verified
    termination, not just trusted the ScaledObject's parked state. Both
    decode Deployments back to 0/0. Namespace matches its pre-task
    baseline (gateway, EPP, wva-controller-manager, grafana only).
22. Given step 19's real validation, built `hack/benchmark/analyze_wva_decisions.py`
    (design per `observability-gaps.md` §3, now §7) and wired it as `make
    benchmark-analyze-decisions` (`RUN_DIR=`, `VARIANT_COSTS="name=cost ..."`,
    `MAX_REPLICAS="name=n ..."`). Verified three ways: 0 flags against
    `both-variants-live` with real costs (matches step 19), a clean
    "only one variant present" note against `secondary-only` (no false
    flag), and — to confirm the logic actually discriminates rather than
    trivially always passing — 4 real flags when given the two variants'
    costs *deliberately swapped*.
23. Wrote up everything above in `observability-gaps.md` §7. Committed and
    pushed all of it (the `add_variant.py` fix, the `dump_wva_full_timeseries.py`
    fix, both published bundles, `analyze_wva_decisions.py`, and this log)
    across several commits on `worktree-multi-variant`, all now on
    `origin/worktree-multi-variant`.
24. Final safety sweep: `git status` clean, local `HEAD` matches
    `origin/worktree-multi-variant` exactly, no background processes left
    running, `.kube/` confirmed gitignored (never staged). Cluster
    reconfirmed parked. The two raw run-workspace directories
    (`dean-20260821-032557-057/`, `dean-20260821-055002-068/`, ~15.4GB
    combined) are local-only scratch by design (gitignored, matching the
    pattern every prior run in this session left behind) — not missing
    from persistence, just disk usage; delete them or keep them, either
    is fine.
