# TA on pokprod — Campaign Report

**Status:** current, live. Replaces `ta-pokprod-campaign-20260810-results.md` and
`ta-pokprod-rerun-results-20260813.md` (both superseded, content folded in below, kept on disk
for old section-number citations). **Scope:** cross-cutting conclusions and current data across
every pokprod run to date. No benchmark history/narrative — see
`plans/planning/ta-pokprod-history.md` for how any of this came to be.

**Companion docs:** `plans/planning/ta-pokprod-workload-coverage.md` (which
workloads exist, why) · `plans/planning/ta-pokprod-architecture-design.md`
(Type 1) · `plans/planning/ta-pokprod-execution-plan.md` (Type 3, phased history)
· `plans/planning/ta-pokprod-open-scenarios.md` (Type 3, live checklist) ·
`plans/planning/ta-pokprod-history.md` (decision ledger, `[[D-nn]]` fetchable by
`grep -n '^## D-nn'`).

**Viz output now exists for all 19 runs, resolved 2026-08-14** — regenerated/rendered by the
autoscaling-viz scope with a version stamp (`rendered @ <sha>`), pulled up to the canonical
`runs/<id>/viz/panels.png` location and committed. Panel links below are real, not placeholders.

---

## Section 1 — Workload × WVA-config results

Three WVA configurations appear across this campaign: **sat** (saturation analyzer only), **TA**
(throughput analyzer only), **satTA** (both, saturation+throughput). Cells below are sparse where
a workload hasn't run under every config — see
`plans/planning/ta-pokprod-workload-coverage.md` for the 4 runs in flight to
close the two remaining gaps (prefill-knee, calibration-probe × {sat, satTA}).

### ta_autoscale_staircase.yaml.in

**Load:** 2048 in / 512 out tokens, short plateaus staged low→high rps (5/12/18 rps, ~2 min per
stage), sized to stay within a 2-replica decode cap's serviceable capacity — an autoscaling
harness shakedown, deliberately not a saturation sweep (climbing past capacity would hang the
harness on an unbounded backlog).

| Metric | sat | TA | satTA |
|---|---|---|---|
| Run | `dean-20260810-080708-371` | `dean-20260810-084756-739` | `dean-20260810-064736-555` |
| P99 TTFT (ms) | 79,007 | 2,059 | 2,142 |
| P99 ITL (ms/tok) | 126.34 | 67.01 | 67.46 |
| Avg / max replicas | 2.24 / 8 | 2.24 / 3 | 2.10 / 3 |
| Avg KV% | 16.2% | 11.7% | 12.8% |
| Avg pod startup (s) | 76 | 65 | 85 |
| Errors | 0 | 0 | 0 |
| Report | [REPORT.md](../../runs/dean-20260810-080708-371/REPORT.md) | [REPORT.md](../../runs/dean-20260810-084756-739/REPORT.md) | [REPORT.md](../../runs/dean-20260810-064736-555/REPORT.md) |
| Panels | [panels.png](../../runs/dean-20260810-080708-371/viz/panels.png) | [panels.png](../../runs/dean-20260810-084756-739/viz/panels.png) | [panels.png](../../runs/dean-20260810-064736-555/viz/panels.png) |

*Re-extracted 2026-08-14 through the corrected `postprocess.py` (D-39) — these three runs predated
the fix and had shown `?` for TTFT/ITL until the reprocessing landed
(`benchmark__reprocess-staircase-runs-predate-postprocess-fix.md`, commit `d0ea3840`). sat-only's
79,007ms TTFT vs the two TA-voting configs' ~2,100ms is the same saturation-lags-demand pattern
documented in § *Analysis by topic* below.*

**Cross-config comparison.** sat-only over-provisions AND delivers worse latency: 8 max replicas
vs 3 for both TA-voting configs, on identical offered load/image/request count. Both TA-voting
configs (TA-only, satTA) land at the same 3-replica ceiling with visually all-green latency in the
source panels — saturation-voting is not a logging artifact, it changes the replica trajectory 3×
and (per the original panel read) the user-visible latency band from all-green to failed.

### ta_autoscale_dwell.yaml.in

**Load:** 2048 in / 512 out tokens, sustained rate rungs inside the no-action band `[0.70, 0.85]`
kv%, meant to hold long enough to observe eventual steady-state right-sizing rather than measure
transition speed.

| Metric | sat | TA | satTA |
|---|---|---|---|
| Run (2026-08-10 / rerun 2026-08-13) | `...100827-539` / `...013728-756` | `...105211-685` / `...000928-609` | `...092644-320` / `...005321-943` |
| P99 TTFT (ms), rerun | 91,712 | 3,568 | 3,392 |
| P99 ITL (ms/tok), rerun | 151.97 | 64.90 | 65.98 |
| Avg / max replicas, rerun | 2.93 / 10 | 4.13 / 10 | 3.62 / 6 |
| Avg queue depth, rerun | **32.4** | 0.0 | 0.0 |
| Errors, rerun | 1 | 0 | 1 |
| Report (rerun) | [REPORT.md](../../runs/dean-20260813-013728-756/REPORT.md) | [REPORT.md](../../runs/dean-20260813-000928-609/REPORT.md) | [REPORT.md](../../runs/dean-20260813-005321-943/REPORT.md) |
| Panels (rerun) | [panels.png](../../runs/dean-20260813-013728-756/viz/panels.png) | [panels.png](../../runs/dean-20260813-000928-609/viz/panels.png) | [panels.png](../../runs/dean-20260813-005321-943/viz/panels.png) |

*Original 2026-08-10 runs and the 2026-08-13 clean reruns both exist; the rerun row above is the
authoritative one — the original `m-ta-dwell` was truncated (r²=0.11 ITL fit) and superseded, the
original sat/satTA cells hit the limit cycle described below and remain valid evidence for that
mechanism even though they're not a clean steady-state reading.*

**Cross-config comparison.** `m-sat-dwell`'s tail latency is roughly 25× worse than either
TA-analyzer dwell cell on an otherwise-comparable clean run (P99 TTFT 91,712ms and queue depth
32.4 vs single digits) — the sharpest confirmation yet of saturation-lags-demand (§ *Analysis by
topic* below). **No config has escaped the dwell limit cycle to produce a genuine steady-state
reading** — the original satTA and sat cells both rode to the replica cap (10) and collapsed
twice; this is a known, understood mechanism (§ *Analysis by topic*), not resolved by any config
choice tested so far.

### ta_calibration_probe.yaml.in / ta_calibration_probe_p4.yaml.in

**Load:** 4096 in / 1024 out tokens, 8-stage sweep near-idle→above-saturating (~12 min), meant to
give the throughput analyzer enough KSpread≥0.30 samples to leave its T2-default fallback — "does
TA do anything at all." The p4 variant divides each stage's rate by 4 for use with 4 parallel
harness pods (the OOM fix, D-42).

| Metric | TA (attempt 1, OOM) | TA (retry, clean) | TA-p4 (4 pods) | sat (attempt 1, OOM) | sat (retry, clean) | satTA |
|---|---|---|---|---|---|---|
| Run | `...203217-894` | `...231722-822` | `...130251-004` | `...044129-931` | `...050448-704` | `...053822-692` |
| P99 TTFT (ms) | ? (OOM before postprocess) | 20,088 | 19,053 avg | ? (OOM before postprocess) | 17,105 | **4,798** |
| P99 ITL (ms/tok) | ? | 136.79 | 139.93 avg | ? | 144.33 | 120.20 |
| Avg / max replicas | 6.00 / 9 | 6.25 / 10 | 4.50 / 9 | 3.91 / 10 | 4.64 / 10 | 5.63 / 10 |
| Avg queue depth | — | 1.1 | 3.1 | 2.4 | 3.5 | **0.0** |
| Errors | 0 (partial data, OOM before completion) | 0 | 0 (all 4 pods) | 0 (partial data, OOM before completion) | 0 | 0 |
| Report | [REPORT.md](../../runs/dean-20260812-203217-894/REPORT.md) | [REPORT.md](../../runs/dean-20260812-231722-822/REPORT.md) | [REPORT.md](../../runs/dean-20260813-130251-004/REPORT.md) | [REPORT.md](../../runs/dean-20260814-044129-931/REPORT.md) | [REPORT.md](../../runs/dean-20260814-050448-704/REPORT.md) | [REPORT.md](../../runs/dean-20260814-053822-692/REPORT.md) |
| Panels | [panels.png](../../runs/dean-20260812-203217-894/viz/panels.png) | [panels.png](../../runs/dean-20260812-231722-822/viz/panels.png) | [panels.png](../../runs/dean-20260813-130251-004/viz/panels.png) | [panels.png](../../runs/dean-20260814-044129-931/viz/panels.png) | [panels.png](../../runs/dean-20260814-050448-704/viz/panels.png) | [panels.png](../../runs/dean-20260814-053822-692/viz/panels.png) |

*Both TA and sat hit the same `OOMKilled` mechanism on first attempt (D-41); each resolved by an
unmodified retry, not the p4/rate-divided variant, matching the pattern that worked before —
per the coverage doc's explicit "flag to the planner if OOM recurs rather than silently switching
to p4" constraint.*

**Cross-config comparison, landed 2026-08-14.** satTA clearly helps this workload's shape: P99
TTFT 4,798ms vs sat-only's 17,105ms (~3.5× better) and TA-only's 20,088ms, with queue depth 0.0 vs
2.4–3.5 for the single-analyzer configs. Consistent with the throughput analyzer's stated purpose
— calibration-probe's rate sweep is exactly the shape designed to give TA the KSpread≥0.30 samples
it needs, so it isn't surprising satTA outperforms either analyzer alone here. This is the
opposite pattern from prefill-knee below, where satTA does *not* help — the two workloads'
shapes make different demands on which analyzer's signal is informative.

### ta_prefill_knee.yaml.in

**Load:** ~2000 in / ~100 out tokens, prefill-dominated shape, to probe the ITL lower knee — moves
the stimulus (shape), not the rate, kept as a separate run from the dwell workload for a clean
comparison axis.

| Metric | TA | sat | satTA |
|---|---|---|---|
| Run | `dean-20260812-152105-714` | `dean-20260814-032308-959` | `dean-20260814-035754-869` |
| P99 TTFT (ms) | 40,657 | 59,990 | 61,201 |
| P99 ITL (ms/tok) | 422.06 | 253.16 | 250.11 |
| Avg / max replicas | 3.21 / 10 | 2.62 / 10 | 3.54 / 10 |
| Avg queue depth | 49.2 | 67.5 | 71.1 |
| Errors | 1 | 0 | 0 |
| Report | [REPORT.md](../../runs/dean-20260812-152105-714/REPORT.md) | [REPORT.md](../../runs/dean-20260814-032308-959/REPORT.md) | [REPORT.md](../../runs/dean-20260814-035754-869/REPORT.md) |
| Panels | [panels.png](../../runs/dean-20260812-152105-714/viz/panels.png) | [panels.png](../../runs/dean-20260814-032308-959/viz/panels.png) | [panels.png](../../runs/dean-20260814-035754-869/viz/panels.png) |

**Cross-config comparison, landed 2026-08-14.** sat and satTA are close to each other (P99 TTFT
59,990ms vs 61,201ms, queue depth 67.5 vs 71.1) and both markedly worse than TA-only on tail
latency/queue-depth — the opposite pattern from calibration-probe above. Adding the throughput
analyzer does not help this workload's shape: short-output, prefill-dominated load isn't designed
to give TA the KSpread≥0.30 samples it needs, so satTA's throughput signal here is plausibly
uninformative rather than actively harmful — sat-only and satTA landing at nearly the same result
is consistent with TA simply not contributing. All 3 configs ran under live autoscaling
(controller on) — a separate, still-unmade scenario decision exists on whether a sharper
fixed-replica/autoscaling-off variant is worth building (see coverage doc).

### ta_autoscale_ladder.yaml.in

Superseded by staircase/dwell; 1 run (2026-08-07, pre-`runs/` era), not rerun, no plan to rerun.
Listed for completeness only.

---

## Section 2 — Analysis by topic

### The dwell limit cycle

`m-satta-dwell` and `m-sat-dwell` (original 2026-08-10 run) show the same envelope: ride to the
replica cap of 10, collapse to 2, climb again — two full excursions each, indistinguishable
between analyzer configurations. The dwell is a property of the **controller/workload
interaction**, not the analyzer configuration — steady-state KV under a tracking controller is a
*controlled* variable, so the dwell is a configuration lever, not an offered-rate one.

**Mechanism, traced end-to-end against the actual controller code** (full trace:
`session/status/dwell-deep-dive.md`):
1. **The excursion's trigger is a single anomalous `P1-obs` sample, not accumulated demand.** The
   2→10 replica jump coincides exactly with saturation's `P1-obs` (`k2SrcObserved`) reason code
   reporting `util=3.89` — `util>1` is by design (an unclamped demand/supply ratio), not a units
   bug. Reproduced worse in sat-only than sat+TA — saturation drives the excursion regardless of
   whether TA is also configured.
2. **The lag decomposes into two hops; only one is the bottleneck.** Ordered→created is fast (~1
   tick, ~60s, matching the KEDA poll interval). Created→ready is slow and worsens with concurrent
   boot count (model load + GPU scheduling contention) — the dominant mechanism, physical, not a
   control-loop defect. In the first excursion, `ready` peaked at 9 and never reached the
   ordered/created peak of 10 — the controller retreated from its own peak order before the last
   requested replica ever became ready.
3. **`TotalAnticipatedSupply` is confirmed correctly implemented** — replicas already
   ordered+created are correctly netted out; no double-booking.

**The real gap: no forecast of the queue's own resolution.** `P1-obs` sizes demand off the
*instantaneous* queue snapshot, with no model that already-ordered, already-created (not-yet-
ready) replicas will relieve that queue once they come online. Shared between saturation and TA,
not saturation-specific — new Type-1 design surface, not a bug fix. Scoping deferred by Dean
2026-08-14 — not critical path for tooling/runs.

**A second, additive mechanism (original 2026-08-08 dwell run, not superseded by the above):**
saturation's capacity history (`prc`) keys on a discretized bucket of average output length; this
workload's mean output (512, sd 20) sits 12 tokens above the 500-token bucket edge, so ordinary
sampling noise can flip the bucket key mid-run and swap in a stale or cross-workload history.
Status: strong, code-located hypothesis, not confirmed from logs. Deprioritized 2026-08-14 —
workaround is shifting the workload's output length off the bucket edge; a real WVA fix would be
a separate, lower-priority issue.

**Saturation's `prc` provenance reason codes** (source-checked, `saturation_v2/types.go`, worth
citing precisely rather than by number alone): `P1-obs` = `k2SrcObserved`, "queue saturated:
tokensInUse" — the intended observed path, not an anomaly. `P2-hist` = `k2SrcHistorical`,
"rolling average from prior observations" — the state a `prc` collapse gets stuck in after one bad
`P1-obs` sample. `P3-k2` = `k2SrcDerived`, "estimated from deployment args." `P4-k1` =
`k2SrcFallback`, "fallback to k1 (memory-bound)." **Open, still uninvestigated:** `m-sat-staircase`
also entered `P1-obs` during the 2026-08-10 campaign but its `prc` stayed at 329011 (no collapse),
while `m-ta-staircase` collapsed 329011→195774→62538 (5.26×) after its own `P1-obs` entry — so
entering `P1-obs` is not itself sufficient to trigger the collapse, and what actually
distinguishes a poisoning entry from a benign one is unknown. (Also worth remembering: that
specific `m-ta-staircase` collapse is evidence about saturation's estimator in general, not about
what drove that cell's own scaling — saturation was non-voting there, per Finding 1 above.)

**No run has escaped this limit cycle to produce a genuine steady-state dwell** — true across
both campaigns, all three configs. A limit cycle has no well-defined mean; any dwell-cell KV
number should be read as a distribution (p50/p90/max), not a mean.

### Saturation lags demand

Consistent finding across both campaigns: sat-only configuration delivers markedly worse tail
latency than any TA-voting configuration, on otherwise-comparable runs. Staircase: sat-only
reaches 8 max replicas (both TA-voting configs stay at 3) on identical load. Dwell rerun:
sat-only shows P99 TTFT 91,712ms and queue depth 32.4, roughly 25× worse than either TA-analyzer
cell on the same metric. Saturation-voting changes real replica trajectory and user-visible
latency — not a logging artifact (the engine computes-and-logs-always but votes-conditionally, so
raw log-line counts alone cannot answer the disable question — traced independently via replica
count and latency instead).

**Prefill-knee, landed 2026-08-14, is the exception that sharpens the pattern rather than breaking
it.** Here sat and satTA land close to each other (P99 TTFT 59,990ms vs 61,201ms) and both worse
than TA-only (40,657ms) — TA-only wins, not loses, for this workload's shape. See § *Analyzer
informativeness depends on workload shape* below for why.

**Open, uninvestigated:** why TA-only (saturation non-voting) still drove a live replica path
(2→3→2→3→2, 19 scaling decisions) on the staircase cell — what TA alone is actually doing to
produce that trajectory. The data can answer this; nobody has looked yet.

### Analyzer informativeness depends on workload shape

Two workloads landed under all 3 configs 2026-08-14 show opposite patterns, and the difference
tracks each workload's own design intent rather than a general "TA is better" or "sat is better"
rule:

- **calibration_probe** (4096in/1024out, rate sweep — designed to give TA its KSpread≥0.30
  samples): satTA wins clearly, P99 TTFT 4,798ms vs sat-only's 17,105ms (~3.5×) and TA-only's
  20,088ms, queue depth 0.0 vs 2.4–3.5.
- **prefill_knee** (~2000in/~100out, short-output/prefill-dominated — not designed to produce
  KSpread): TA-only wins, sat and satTA both land near 60,000ms P99 TTFT vs TA-only's 40,657ms.

Read together: TA's signal is only as good as the samples the workload gives it. A shape that
doesn't exercise concurrency variation (prefill-knee) starves TA of the spread it needs, so adding
it (satTA) doesn't help and may even dilute a working sat-only-adjacent decision path; a shape
that does (calibration_probe's rate sweep) lets TA's signal dominate and clearly outperform
sat-alone. Neither result generalizes to "always prefer X" — the workload's own shape is the
deciding factor, consistent with how TA's estimation model works (§ *The knee / piecewise ITL
model* below): a workload that never explores enough of the concurrency range gives TA nothing
to fit.

### Two harness process gaps found during the 2026-08-14 gap-fill runs

**KEDA pause is not auto-cleared by the reset step.** `run_cell.sh`'s reset step
(`make benchmark-reset-run` → `reset_run.py`) does not un-pause a paused ScaledObject — its own
code comment says explicitly this is deliberate ("that is a decision about starting a run, so the
script reports the pause and leaves it"). What looks like an unpause action in the log
(`autoscaling.keda.sh/paused-replicas-`) is a **printed suggested command**, never executed. So
every run implicitly depends on someone having unpaused manually beforehand — nothing in the
automated path does it. Hit once during this round (`m-sat-prefill-knee`'s first attempt failed
immediately with `verify_model: no pods available in datastore`), fixed manually, not patched in
code — flagged as a real gap, not silently changed mid-cycle without discussing whether
printing-not-doing is intentional.

**A failed run's analyze step can silently analyze the wrong (earlier) run's directory.** When
`run_cell.sh`'s `run` step fails before producing a results directory (fast-fail or an OOM'd
harness pod), its `analyse` step falls through to the most-recent-existing `runs/*/results/*`
directory instead of failing cleanly. Hit 3 times this round; twice it silently began overwriting
an *already-committed, different* run's `config/analyzer-config.txt`/`scaledobject.yaml`/
`REPORT.md` — caught via unexpected `git status` modifications, restored with `git checkout --`
each time. The third time a built-in staleness check caught the timeseries JSON specifically
("existing file has 20/20, new parse has 0/0"), but the config files still got clobbered around
it — the guard is partial, not complete. This is a real correctness gap in the failure path (it
should fail closed, not fall through to the wrong directory); flagged, not patched mid-run given
the gap-fill was time-sensitive.

### The knee / piecewise ITL model

TA is a pure rate analyzer, not an SLO enforcer — it asks one question: at `kv% = k_sat` (default
0.85), what is the decode rate? The estimation path is ITL, not rate directly: TA fits ITL vs.
concurrency, converts to rate. The fit is genuinely two-segment piecewise, split at `k_knee`, with
a much steeper slope above the knee — mechanistically real, not a fitting artifact. Past the knee,
there are no longer enough decodes per prefill to amortize prefill cost, so the per-iteration cost
ratio flips toward `prefill_time >> single_token_decode_time`, driving concurrency (and therefore
ITL) up rapidly. If `k_sat` falls above `k_knee` for a workload's shape, the fit must extrapolate
from the post-knee segment, not the pre-knee one, or it badly misestimates `decode-rate(k_sat)`.
(Full correction, viz-panels-planner scope: `session/handoffs/plan__sim-from-benchmark-item6-correction.md`.)

### Queue / drain behavior — what's measured, what isn't

Per-stage latency/failure/token-rate data survives for every stage of every dwell cell even
without a per-request trace (`stage_N_lifecycle_metrics.json` — real numbers, not lost).
Genuinely and only lost without a per-request trace: within-stage latency-distribution shape as
load ramps inside a single rung, per-request identity (router pod, exact arrival instant), and
router-oscillation detection (a 6–11s period is below the ~15.7s scrape cadence's Nyquist limit —
only a per-request trace carrying `UPSTREAM_HOST` can see it). Per-request collection is disabled
by standing policy (its own OOM risk, D-41) — fallback signals are the current direction, not yet
built.

### Controller-restart hold-at-current-replicas policy

A controller restart left decode pinned at 10/10 replicas for 15+ minutes with zero active load
(`demand=0, util=0, rc=0`), never trending down. Read-only source investigation
(`internal/engines/saturation/engine.go`, `applySaturationDecisions`, ~L1601-1701) found a
plausible mechanism: when the optimizer has no fresh decision this cycle, the code deliberately
holds at the current replica count — falls back through persisted CR status, `currentAllocations`,
then the live Deployment's actual replica count — explicitly to avoid unintentionally scaling to
zero on a transient uninformative cycle (stated in the code's own comments). This is a **designed
hold-on-no-decision policy, not a computation bug**. Not confirmed live (static read only); the
open question is whether "hold" is the right policy for a *sustained* window, a policy question,
not a defect report. Possibly the same mechanism family as the still-open replica-oscillation
thread above (both trace through the same hold/current-replicas fallback logic), not proven to be
one single bug.

### The OOM fix (inference-perf's own memory model)

Root cause, source-confirmed at `inference_perf/client/modelserver/openai_client.py` and
`multiprocess.py`: every request's full JSON body and every response's full text are held in one
unbounded Python list for the entire run, never flushed until the run ends. Any sufficiently
long/high-rate profile hits this eventually — not fixable from this side (upstream code). Fix:
`LLMDBENCH_HARNESS_LOAD_PARALLELISM=4` (or `--parallelism`) spawns N harness pods running the
*same* profile unchanged — this multiplies aggregate load, it does not divide it, so the workload's
own rates must be divided by N first to keep the original offered-load intent while dividing each
pod's accumulator share by N. Validated by a real run: 4 parallel pods, 0 errors across all four,
P99 TTFT consistent to within ~1% (18,524–19,320ms).

---

## Section 3 — Run index

One row per run, one experiment per row. **Completed** = the harness ran to full completion
without crashing/OOMing (independent of whether the run's *measurement goal* was achieved — see
Section 1 for measurement outcomes).

| Run ID | Date/time (UTC-ish, from run ID) | Workload | Config | Completed? | Results dir |
|---|---|---|---|---|---|
| `dean-20260810-064736-555` | 2026-08-10 06:47 | staircase | satTA (main image) | ✅ | [runs/dean-20260810-064736-555](../../runs/dean-20260810-064736-555) |
| `dean-20260810-072736-888` | 2026-08-10 07:27 | staircase | satTA (baseline image) | ✅ | [runs/dean-20260810-072736-888](../../runs/dean-20260810-072736-888) |
| `dean-20260810-080708-371` | 2026-08-10 08:07 | staircase | sat | ✅ | [runs/dean-20260810-080708-371](../../runs/dean-20260810-080708-371) |
| `dean-20260810-084756-739` | 2026-08-10 08:47 | staircase | TA | ✅ | [runs/dean-20260810-084756-739](../../runs/dean-20260810-084756-739) |
| `dean-20260810-092644-320` | 2026-08-10 09:26 | dwell | satTA | ✅ (limit-cycled, not steady-state) | [runs/dean-20260810-092644-320](../../runs/dean-20260810-092644-320) |
| `dean-20260810-100827-539` | 2026-08-10 10:08 | dwell | sat | ✅ (limit-cycled, not steady-state) | [runs/dean-20260810-100827-539](../../runs/dean-20260810-100827-539) |
| `dean-20260810-105211-685` | 2026-08-10 10:52 | dwell | TA | ⚠️ truncated (campaign paused mid-run) | [runs/dean-20260810-105211-685](../../runs/dean-20260810-105211-685) |
| `dean-20260812-152105-714` | 2026-08-12 15:21 | prefill-knee | TA | ✅ | [runs/dean-20260812-152105-714](../../runs/dean-20260812-152105-714) |
| `dean-20260812-154829-365` | 2026-08-12 15:48 | calibration-probe | TA | ❌ interrupted, no REPORT.md, not a distinct result | [runs/dean-20260812-154829-365](../../runs/dean-20260812-154829-365) |
| `dean-20260812-203217-894` | 2026-08-12 20:32 | calibration-probe | TA | ❌ OOMKilled at 32Gi, 16 min in | [runs/dean-20260812-203217-894](../../runs/dean-20260812-203217-894) |
| `dean-20260812-231722-822` | 2026-08-12 23:17 | calibration-probe | TA | ✅ (unmodified retry, same 32Gi) | [runs/dean-20260812-231722-822](../../runs/dean-20260812-231722-822) |
| `dean-20260813-000928-609` | 2026-08-13 00:09 | dwell (rerun) | TA | ✅ | [runs/dean-20260813-000928-609](../../runs/dean-20260813-000928-609) |
| `dean-20260813-005321-943` | 2026-08-13 00:53 | dwell (rerun) | satTA | ✅ | [runs/dean-20260813-005321-943](../../runs/dean-20260813-005321-943) |
| `dean-20260813-013728-756` | 2026-08-13 01:37 | dwell (rerun) | sat | ✅ | [runs/dean-20260813-013728-756](../../runs/dean-20260813-013728-756) |
| `dean-20260813-130251-004` | 2026-08-13 13:02 | calibration-probe-p4 | TA (÷4, 4 pods) | ✅ (all 4 pods, 0 errors) | [runs/dean-20260813-130251-004](../../runs/dean-20260813-130251-004) |
| `dean-20260814-032308-959` | 2026-08-14 03:23 | prefill-knee | sat | ✅ | [runs/dean-20260814-032308-959](../../runs/dean-20260814-032308-959) |
| `dean-20260814-035754-869` | 2026-08-14 03:57 | prefill-knee | satTA | ✅ (first try) | [runs/dean-20260814-035754-869](../../runs/dean-20260814-035754-869) |
| `dean-20260814-044129-931` | 2026-08-14 04:41 | calibration-probe | sat | ❌ OOMKilled, partial data kept | [runs/dean-20260814-044129-931](../../runs/dean-20260814-044129-931) |
| `dean-20260814-050448-704` | 2026-08-14 05:04 | calibration-probe | sat | ✅ (unmodified retry) | [runs/dean-20260814-050448-704](../../runs/dean-20260814-050448-704) |
| `dean-20260814-053822-692` | 2026-08-14 05:38 | calibration-probe | satTA | ✅ (first try) | [runs/dean-20260814-053822-692](../../runs/dean-20260814-053822-692) |

**Coverage-matrix gap now closed as of 2026-08-14** — all 6 workload templates have run under
every WVA config the workload's design intends (`ta_autoscale_ladder` excepted, superseded, never
intended for a 3-config comparison).

---

## What the figures do not license (carried forward, still applies)

1. **One run per cell (except the 3 dwell reruns and the p4 4-pod validation). No repeats, no
   noise floor.** Most numbers above are mechanism observations, not statistically-controlled
   benchmark comparisons.
2. **The dwell workload has never produced a genuine steady-state reading, in any config.** Any
   dwell-cell KV/latency number describes a limit-cycling system's transient behavior, not a
   settled operating point — read the mechanism sections above before citing a dwell number as a
   steady-state fact.
3. **`tput_knee()` and `capacity()` (viz toolchain internals cited implicitly via any future panel
   link) have never been formally reviewed by Dean** — treat their outputs as "what the code
   currently does," not "a reviewed and agreed method," until that review happens. **They are two
   different quantities, not one model** — panel 1b's dashed "capacity ceiling" is a *rate*
   (`ready replicas × tput_knee()`'s empirically-observed peak tok/s, an upper-envelope estimate
   calibrated from the same run it's drawn over, which is why it tracks visually). `capacity()`'s
   `max_conc_pred` is a separate *concurrency count* model (`kv_tokens / footprint_tok`, a
   KV-budget formula), and it's the one with a real, measured error: on `m-ta-staircase`,
   pred=212.4 vs obs=78.0, a 63% miss. The suspect is the per-request footprint estimate
   (`I×(1-prefix_hit) + O/2`) — worth checking against real per-request I/O length once that data
   exists. **Neither function's design has actually been reviewed and agreed** — both were
   introduced in the toolchain's first commit (`ca7f2c74`) without a documented design discussion;
   treat any number derived from either as "what the code does," not "a validated method."
4. **Router-oscillation claims need a per-request trace** — scrape-cadence-derived panels cannot
   see sub-Nyquist oscillation by construction.
