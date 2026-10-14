# SLO tiers: size to a TTFT/TPOT target, not only to a fill threshold

**Status:** proposal, 2026-10-14. Nothing built. Tracks
[#141](https://github.com/ev-shindin/llm-scaling-manager/issues/141).

## The problem

A tier says how full a replica may run. It does not say how fast a request
must be served. An operator who has a latency SLO, for example p90 TTFT ≤ 3 s
and p90 TPOT ≤ 100 ms, has to turn it into `kvCacheThreshold` and
`scaleUpThreshold` by hand. Then they have to redo it whenever the model, the
accelerator or the request shape changes, because the fill that meets a TPOT
target on an H200 is not the fill that meets it on an L40S. The documented
*Strict (low latency SLO)* preset (`kvCacheThreshold 0.70`,
`queueLengthThreshold 3`) is one such guess, made once for every model. It is
not derived from any target.

llm-d's [SLO-aware recipe](https://github.com/llm-d/llm-d/blob/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/guides/workload-autoscaling/slo-aware/README.md)
takes the target directly:

- **Signal:** `s = max(p90 TTFT ÷ TTFT target, p90 TPOT ÷ TPOT target)`, from
  the EPP's latency histograms (predicted when the latency predictor runs,
  measured otherwise). It is clamped at 2 and smoothed over 45 s.
- **Control law:** scale up above 0.55, scale down below 0.40, and hold in
  between. Replicas that are still starting are credited.

llm-d's benchmark reports **95.8 % combined attainment at 5.93 average
replicas, against a static pool of 8**. Conditions:

- GKE, H100, Qwen3-32B at TP=2
- staged 2→10→1 req/s
- bounds 3–8
- **85.6 %** with a cold predictor

The recipe is scoped to *"one inference pool serving one model variant against
one SLO pair"*, and its residual violations sit *"at the first capacity
crossing"*. Our comparison page
([keda-alone](https://github.com/ev-shindin/llm-scaling-manager/blob/docs/keda-comparison/docs/comparison/keda-alone.md), on branch `docs/keda-comparison`) lists
this as a case where KEDA alone is the better choice today.

The goal is to close that row **without giving up what the recipe lacks**:

- several variants with cost-aware choice
- P/D pairing
- a GPU budget
- scale to zero

## The idea in one paragraph

Keep the capacity model as the sizer. Let a tier declare latency targets, and
replace the tier's static fill with a **fill derived per variant from that
variant's own latency curve**: the largest fill at which the curve predicts the
target is met, minus a margin.

The decode half of this already exists. `internal/signals/itl` fits
`ITL(k) = A·k + B` per variant over KV utilization `k`, and the saturation
analyzer already prices mu from it at a fill `pricingK`. A TPOT target turns
that line around: instead of reading ITL at a configured `k`, solve for the `k`
at which ITL meets the target.

TTFT is harder, because it is mostly queueing (see [TTFT](#ttft)).

Until a variant's curve is trustworthy, the tier's static thresholds apply
unchanged.

## What exists today

The facts this design has to fit, from `main` at `cb37a21e2`:

| | Where | What it means here |
| --- | --- | --- |
| A tier is a ConfigMap entry with no `model_id` | `config/scaling_policy.go:41` (`PolicyEntryKey`) | The SLO fields go on `config.ScalingPolicy`. The issue's `workloadClasses` is only the name of the [well-lit path](../well-lit-paths/workload-classes/); there is no such key |
| A tier resolves per **model**, not per variant | `steadystate/engine.go:998` (`modelPolicy`); variants that disagree get the lexicographically smallest tier | The target is per model. The derived fill has to be per variant, which is new |
| The fill enters sizing in two places | `KvCacheThreshold` in k1 (`replica_capacity.go:78`) and in `pricingK` (`mu_from_itl.go:167`); `scaleUp`/`scaleDown` in `applyUniversalThreshold` (`engine_v2.go:709`) | The scale-out point is `kvCacheThreshold × scaleUpThreshold` of physical KV (0.68 by default) |
| The ITL line is fitted on **means** | `rate(vllm:inter_token_latency_seconds_sum[1m]) / rate(..._count[1m])` per pod (`queueing_model.go:80`) | A p90 target needs a tail term the line does not have |
| No percentile is computed anywhere | No `histogram_quantile` in the collector | Attainment and tails need new queries |
| TTFT is collected and deliberately unused | `AvgTTFT`, `domain/replica_metrics.go:203` | A `TTFT(T) = A·T + B` fit was built and removed: queue wait landed in the intercept (82 s). [prefill-ttft-model](prefill-ttft-model.md) has the record. TTFT must not be regressed as if it were a service time |
| ITL readiness gate | `window.go`: ≥ 10 samples, k-spread ≥ 0.30, max age 30 m | A variant under flat load never fits. Fallback is a steady state, not just a start-up state |
| A dormant SLO surface exists | `domain.ServiceClass{SLOTPOT, SLOTTFT}`, `utils.FindModelSLO` (no callers), `deploy/configmap-serviceclass.yaml` | Inherited from WVA and wired to nothing. See [Phase 0](#phase-0-observe-only) |
| `Analyzer` already anticipates an SLO analyzer | `domain/analyzer.go:14, 175` | This proposal deliberately does **not** add one. See [Alternatives](#alternatives-considered) |

## Configuration

```yaml
data:
  interactive: |
    scaleUpThreshold: 0.85
    scaleDownBoundary: 0.70
    slo:
      ttft: {target: 3s,    percentile: 90}
      tpot: {target: 100ms, percentile: 90}
      margin: 0.10          # aim at target × (1 − margin); default 0.10
      mode: enforce         # observe | enforce; default observe
```

- **Either objective may be omitted.** A tier with only `tpot` is a valid SLO
  tier.
- **`percentile`** is an integer from 50 to 99. Default 90, matching llm-d.
- **`mode: observe`** computes and publishes everything below but changes no
  decision. This is how a tier is first put on a model, and it answers the
  first question an operator has: *would my current thresholds have met this
  SLO?*
- **Durations use Go syntax.** A value that does not parse is rejected through
  the existing `Validate` → `RecordError` path, with the key named. It is never
  read as zero.
- **The whole `slo` block is replaced, not merged field by field.** It resolves
  through the normal `default → tier → per-model` overlay. A per-model entry
  that sets `slo` replaces the tier's block, so a partial override cannot
  silently keep the other objective.
- **Where the static thresholds still apply.** `kvCacheThreshold` and
  `scaleUpThreshold` stay. They are the fallback and the ceiling (see
  [Bounds](#bounds-what-the-derived-fill-may-never-do)).

## TPOT

### Derivation

For variant `v` with a ready line `ITL_v(k) = A·k + B`:

```
budget   = tpotTarget × (1 − margin) ÷ ρ_v        ρ_v: the tail ratio, below
k_tpot_v = (budget − B) ÷ A
```

`k_tpot_v` is the KV fill, as a fraction of **physical** KV, at which the
percentile TPOT reaches the budget. It replaces the composed scale-out point:

```
kvCacheThreshold_v := min(k_tpot_v, k_static) ÷ scaleUpThreshold
```

`k_static` is `kvCacheThreshold × scaleUpThreshold`, the tier's own scale-out
point. With this rewrite, both existing consumers, k1 and `pricingK`, read the
derived fill. Nothing downstream of the analyzer changes:

- `applyUniversalThreshold`
- the allocators
- the limiter
- the cost order

They all see a per-replica capacity that is smaller or larger than before.
That substitution point is the reason the design does not add an analyzer.

### The tail ratio ρ

The line predicts mean ITL. The SLO is on a percentile of per-request TPOT.
Two effects separate them:

- `k` varies across replicas, because EPP placement is uneven.
- `k` varies over a request's lifetime.

Measure the gap instead of modelling it:

```
ρ_v = quantile_p(TPOT over v's pods, 5 m) ÷ mean(ITL over v's pods, 5 m)
```

- **Numerator:** `histogram_quantile` over the variant's pods. Use the
  per-request TPOT histogram where the engine exports one, otherwise the
  inter-token-latency histogram. Which vLLM and SGLang versions export which
  is to be confirmed against the images the e2e and benchmarks pin.
- **Smoothing:** keep `ρ` as a rolling maximum over the line's window. It is
  ≥ 1 by construction, and is floored at 1.

If the histogram's buckets are too coarse around the target, the quantile is
interpolated and can be off by a bucket. Publish the bucket bounds that
bracket the target beside the derived fill, so an operator can see when the
target sits between two wide buckets.

### Infeasible targets

`B` is the per-token cost at near-zero load. If `budget ≤ B + A·k_min`, no
fill meets the target on that variant. The variant is **SLO-infeasible**:

- Its capacity is zero for scale-up, so the cost-aware order skips it.
- It is never scaled down below what it serves now just for being infeasible.
  Shedding a replica that is serving does not make anyone's requests faster.
- It is reported (see [Observability](#observability)).

If every variant of a model is infeasible, the model falls back to its static
thresholds. That is reported as a `slo_infeasible` reason on
`wva_model_scaling_blocked`, not as a new gauge.

This is the part a single-variant recipe cannot do: on a mixed fleet, a tight
TPOT target moves growth to the accelerators that can meet it.

## TTFT

TTFT is not a property of a replica's load in the way ITL is. It has three
parts:

1. the wait in the router's flow-control queue
2. the wait in the engine's queue
3. the prefill itself

The first two are queueing, and they dominate whenever demand exceeds capacity.
That is why fitting TTFT failed before.

So the proposal splits it:

- **The fill condition (steady state).** Use only the intervals in which the
  variant had **no waiting requests**, which removes parts 1 and 2. Over those
  intervals, keep a monotone (isotonic) curve of the TTFT percentile against
  fill `k`, binned at 0.05. `k_ttft_v` is the largest bin whose upper
  confidence bound is under `ttftTarget × (1 − margin)`.
  - For an **aggregated** variant, `k` is KV fill. Prefill competes with the
    decode batch under chunked prefill, so TTFT climbs with `k`.
  - For a **prefill** role, `k` is computed-prefill utilization:
    `PrefillComputedTokenRate` over its learned ceiling, the signal that
    replaced the TTFT fit.
  - **No regression, no intercept.** The curve is read only where it was
    observed. This is non-parametric on purpose: the failed fit was a
    parametric model of a quantity that is not linear.
- **The queue condition.** A TTFT tier needs the queue empty in steady state.
  The queue charge already orders capacity for a backlog. An SLO tier with a
  TTFT target sets the effective `queueLengthThreshold` to 1, unless the
  operator set it lower.
- **The transient (out of scope for the fill).** A fill rule cannot fix the
  first capacity crossing. A step in arrival lands before a new replica is
  Ready, whatever fill the fleet was sized at. llm-d sees the same thing. The
  levers are start time and the warm pool, plus
  [in-flight sizing](sizing-against-in-flight-capacity.md). The proposal
  measures this (attainment per rise window, below) and does not claim to fix
  it.

**Combining the objectives.** On an aggregated variant, the derived fill is
`min(k_tpot_v, k_ttft_v)`. On P/D:

- TPOT constrains **decode** variants.
- TTFT constrains **prefill** variants.
- The KV handoff adds to TTFT. Prefill's budget is
  `ttftTarget − p_q(transfer time)` from the NIXL histograms already scraped,
  and is floored at zero, which means infeasible.

Each role gets a fill against its own objective, and the pair commit is
unchanged. The recipe lists P/D as future work, so this is the second thing it
cannot do.

## Bounds: what the derived fill may never do

The tail of an over-estimate is the costliest failure in this codebase. A
capacity priced too high leaves TTFT unrecoverable, which is why k2 is never
raised by fiat. The derived fill is held to the same rule.

1. **Never above the fitted range.** `k_v ≤ k_max_observed_v + 0.05`. The line
   is valid where it was fitted. A loose SLO raises fill one step past the
   observed range per window, and the fleet earns the next step by running
   there. This replaces `pricingK`'s fixed clamp to [0.15, 0.80] with the
   variant's own range, within that clamp.
2. **Never above a hard ceiling.** `k_v ≤ 0.90` of physical KV, below the
   preemption cliff. A synchronized batch preempts at much lower average fill,
   as shape-shift pass 3 showed, so a later revision may lower this ceiling.
3. **A borrowed line lowers, never raises.** Under `enableLearnedStateReuse`, a
   variant priced from a sibling's line may get a derived fill **below** its
   static one, never above. Only its own fit may loosen. This is the same
   cap-never-order asymmetry as
   [learned-state sharing](learned-state-sharing-by-similarity.md).
4. **Fallback is explicit.** With no ready line, no ρ, or a TTFT curve with
   fewer than N bins observed, the static `k_static` applies, and the source
   is labelled `static`. Under flat load the line never becomes ready (see
   [the ITL readiness gate](#what-exists-today)), so this state can last for
   hours. It must look different on a dashboard from "derived and met".
5. **Non-finite input is a fallback, not a value.** `A`, `B`, `ρ` and every
   quantile are checked with `math.IsNaN`/`IsInf` before any comparison.
   `x <= 0` admits NaN.

## Closing the loop on attainment (phase 3)

Everything above is open-loop: a model of latency, trusted. If the model is
wrong, for example because ρ under-reads or the curve was learned on a shape
that has since changed, the fleet misses the SLO while the derivation reports
success. The correction is a slow trim on measured attainment:

```
trim_v ∈ [0.5, 1.0]
k_v_effective = k_v_derived × trim_v

attainment < objective (10-minute window)   → trim_v −= 0.05, at most once per window
attainment ≥ objective + 2 pp for 30 min    → trim_v += 0.02
```

It is asymmetric (fast down, slow up), bounded, and never above 1, so it can
only correct an over-estimate. The objective is the configured percentile:
p90 means 90 % of requests within target. This is deliberately not llm-d's
control law. That law reacts to latency within about a minute and is the
sizer. Here, the trim reacts over tens of minutes to a model that drifted.

## Observability

These follow the label rules in [signals-as-metrics](signals-as-metrics.md):
the model namespace is `exported_namespace`, and there are no request-level
labels.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `wva_slo_target_seconds` | model, exported_namespace, tier, objective (`ttft`/`tpot`), percentile | The declared target, so a dashboard needs nothing else |
| `wva_slo_observed_seconds` | + variant, source (`epp`/`engine`) | The observed percentile over 5 m |
| `wva_slo_attainment_ratio` | + variant, source | The share of requests within the target over 5 m, from histogram buckets (interpolated when the target is not a bucket bound) |
| `wva_slo_fill` | + variant, objective, source (`derived`/`static`/`borrowed`/`infeasible`) | The scale-out fill in force, as a fraction of physical KV (or prefill utilization) |

Notes:

- **Combined attainment cannot come from two histograms.** llm-d scores it per
  request, with both objectives holding. Two separate histograms cannot give
  that. Publish per-objective attainment. Where the router emits a per-request
  SLO-violation counter, publish the combined figure from it, and label the
  source.
- **The effective-policy log line gains the `slo` block.** The cycle log gains
  `sloFill`, `sloSource` and `rho` per variant.
- **Engine-side and client-side TTFT differ.** Engine-side TTFT excludes the
  EPP flow-control queue. Prefer the EPP's latency histograms where the router
  exports them, and label the source. The metric names are to be confirmed
  against the pinned EPP version before they are written into a query.

## Phases

Each phase can land alone, and none changes a decision until `mode: enforce`.

### Phase 0: observe only

- The `slo` block: parsing, validation and resolution.
- The percentile queries and the four metrics above.
- The derived fill, computed and published with `source`, but not applied.
- Delete the dormant `ServiceClass` / `FindModelSLO` /
  `configmap-serviceclass.yaml`, since this replaces them. That needs a
  grep-wide check and the maintainer's agreement first.

This phase alone answers "does my tier meet my SLO?", which nothing answers
today.

### Phase 1: TPOT, enforce

- The ITL inversion and ρ.
- The per-variant `kvCacheThreshold` substitution, bounds 1–5, and
  infeasibility.

### Phase 2: TTFT, enforce

- The no-queue isotonic curve and the queue-threshold override.
- The P/D split, with the transfer-time budget.

### Phase 3: the attainment trim

## Tests

**Unit**, in `internal/signals/itl` and the saturation analyzer:

- Inversion: hand-computed `(A, B, ρ, target, margin) → k`.
- Each bound in isolation: fitted-range cap, hard ceiling, borrowed-line
  asymmetry, NaN/Inf inputs for each of `A`, `B`, `ρ` and the quantile.
- Infeasible: `B ≥ budget` gives zero scale-up capacity, no scale-down below
  current, and the blocked reason when every variant is infeasible.
- Fallback: unready window, flat load (k-spread < 0.30), missing histogram.
  Each of these must return the static fill **and** label it `static`.
- The isotonic TTFT curve: a non-monotone input, intervals with a queue
  excluded, too few bins.
- Trim: asymmetry, bounds, once per window.
- `mode: observe` changes no `NamedAnalyzerResult`. Assert byte equality of
  the result with and without the block.

**e2e:** first check whether the simulator's latency varies with load. If it
returns a constant ITL, the line never fits and an e2e spec only exercises the
fallback. That is still worth one spec, which asserts `source="static"` and an
unchanged decision. A derived-fill spec needs a simulator latency model that
rises with `k`.

**Benchmark:** the G1 replication in
[keda-recipe-comparison](https://github.com/ev-shindin/llm-scaling-manager/blob/bench/keda-recipe-arm/docs/plans/benchmark/keda-recipe-comparison.md)
(branch `bench/keda-recipe-arm`) gains one arm, **ours-slo**: the default policy plus
an `interactive` tier with `ttft 3s / tpot 100ms p90`, `mode: enforce`, and the
same burn-in. The predictions are pre-registered here, before any run:

- TPOT does not bind for either arm. The plan already reasons that a 32B
  replica at our 0.68 ceiling holds a batch of about 30. **So ours-slo's
  advantage over plain ours must come from TTFT and from loosening fill.**
  Expect `R̄_ours-slo` < `R̄_ours`, with attainment within 2 pp.
- If ours-slo does not hold fewer replicas than ours on this load, the
  derivation bought nothing on llm-d's own benchmark. The P/D and
  mixed-accelerator cases are then the only argument for it, and that has to
  be measured too, not asserted.
- Attainment lost in the first 120 s of each rise is reported separately from
  steady state. If we lose to R5, it should be there, and a fill rule does not
  fix it.

## Alternatives considered

- **A separate `slo` analyzer**, as `domain/analyzer.go` anticipates. Its
  required and spare capacity would be combined with saturation's by the
  engine's max/min. That doubles the sizing paths. It can only add capacity on
  top of saturation, so it can never deliver the cost win of a loose SLO. And
  its result would have no per-variant capacity to give the cost order.
  Rejected: the per-variant fill substitution gets both directions with one
  sizer.
- **llm-d's control law inside the controller** (scale on `p90 ÷ target` with
  a dead band). It is reactive by construction: latency rises after capacity
  is short. It needs no model, which is its strength. It does not price
  variants, and it says nothing about scale-down that a cost order can use.
  Rejected as the sizer. Phase 3 keeps its one good property, correction from
  measured latency, at a slow time scale.
- **A per-request latency predictor** (as the EPP's `predicted-latency-producer`).
  That is a routing feature. Its value to llm-d's recipe is an earlier signal,
  and this design already sizes before the queue forms. If the EPP's predicted
  histograms are present, they can be one more `source` for the observed
  percentile, but they do not become the sizer.
- **Static presets per SLO**, a table mapping targets to thresholds. This is
  what the docs do today with *Strict*. It is wrong whenever the hardware or
  the shape differs from the one the table was made on, which is the problem
  this proposal exists to solve.

## Open questions

1. **ρ under a shape change.** The tail ratio was learned on the old shape. Is
   it reset with the ITL window on a shape-change hold, or kept? Keeping it is
   conservative only if the new shape's tail is no wider.
2. **The percentile window.** 5 m matches the line's horizon. llm-d smooths
   its signal over 45 s. A shorter window makes attainment noisy on a small
   fleet.
3. **Scale to zero.** A model parked at zero has no curve and no attainment.
   On wake, the first decisions are static, and the cold start is outside any
   fill. Should an SLO tier refuse `scaleToZero` when the TTFT target is below
   the measured cold-start time? Proposed: warn in the effective-policy log
   line, do not refuse.
4. **The utilization-share optimizer** reads `ScaleUpThreshold` directly
   (`utilization_share_groups.go:359`). It must read the per-variant fill
   instead, or SLO tiers and share mode are mutually exclusive in phase 1.
