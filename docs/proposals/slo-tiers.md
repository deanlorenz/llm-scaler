# SLO tiers: size to a TTFT/TPOT target, not only to a fill threshold

**Status:** proposal, 2026-10-14, revised the same day against the router's
metrics and the engine's analyzer loop. Nothing built. Tracks
[#141](https://github.com/ev-shindin/llm-scaling-manager/issues/141).

## The problem

A tier says how full a replica may run. It does not say how fast a request
must be served. An operator with a latency SLO, for example p90 TTFT ≤ 3 s and
p90 TPOT ≤ 100 ms, has to turn it into `kvCacheThreshold` and
`scaleUpThreshold` by hand. They then have to redo that whenever the model, the
accelerator or the request shape changes. The documented *Strict (low latency
SLO)* preset (`kvCacheThreshold 0.70`, `queueLengthThreshold 3`) is one such
guess, made once for every model.

llm-d's SLO-aware recipe takes the target directly. Its benchmark reports
**95.8 % combined attainment at 5.93 average replicas, against a static pool of
8**:

- GKE, H100, Qwen3-32B at TP=2
- staged 2→10→1 req/s
- bounds 3–8
- **85.6 %** with a cold predictor

The recipe is scoped to *"one inference pool serving one model variant against
one SLO pair"*, and its residual violations sit *"at the first capacity
crossing"*. Our comparison page,
[keda-alone](https://github.com/ev-shindin/llm-scaling-manager/blob/docs/keda-comparison/docs/comparison/keda-alone.md)
(on branch `docs/keda-comparison`), lists this as a case where KEDA alone is the
better choice today.

The goal is to close that row **without giving up what the recipe lacks**:

- several variants with cost-aware choice
- P/D pairing
- a GPU budget
- scale to zero

## The design in one paragraph

A new **`slo` analyzer**, separate from saturation, owns everything about
latency:

- reading the router's and the engines' latency histograms
- the per-variant latency curves
- the tail ratios
- attainment
- the **fill** at which each variant meets the tier's target

The saturation analyzer stays the sizer. When a tier sets `mode: enforce`, the
`slo` analyzer's fill replaces the tier's static scale-out point for that
variant, inside saturation, where it reaches both k1 and the price of the
demand floor. Until a variant's curve is trustworthy, the static thresholds
apply, and the source is labelled.

[Why a separate analyzer, and why it does not hand the engine a second
(D, P)](#where-it-lives-a-separate-analyzer-that-hands-saturation-a-fill)
explains the split.

## What llm-d's KEDA recipes read

All of these are at llm-d `7d5f86d`, under `guides/workload-autoscaling/`.

| Recipe | Signal | Notes |
| --- | --- | --- |
| `keda-epp` (queue) | `sum(llm_d_epp_flow_control_queue_size{namespace,service,model_name})` at 1, and `sum(llm_d_epp_request_running{…})` at 16 | `request_running` is incremented after admission, so it **excludes** queued requests |
| `keda-epp` (saturation) | `max(llm_d_epp_flow_control_pool_saturation{inference_pool,namespace})` at 0.7 | A pool-level gauge with `stage` = prefill / decode / effective |
| `keda-epp-token-aware` | `sum(llm_d_epp_inflight_tokens{producer_name="inflight-load-producer"}) ÷ <hand-calibrated tok/s>` at 1.5, and `vllm:kv_cache_usage_perc` at 0.8 | On P/D, a regex on `endpoint_name` / `pod` picks the role |
| `slo-aware` | p90 of `llm_d_epp_request_predicted_ttft_seconds`, falling back to `llm_d_epp_request_ttft_seconds`. TPOT: `…predicted_tpot_seconds`, falling back to `…streaming_tpot_seconds` | `histogram_quantile(0.9, sum by (le)(rate(…_bucket[1m])))` with **no label selector**: one pool, one model. Then `s = max(ttft/3.0, tpot/0.1)`, clamped at 2, averaged over 45 s |

The slo-aware formula is a proportional law on `s`. It scales up above 0.55,
scales down below 0.40, and on scale-up credits starting replicas with
`ready ÷ desired`. HPA settings are 30 s up and 180 s down, at most
max(100 %, 4) pods per 60 s up and 1 pod per 120 s down.

Attainment is scored from `llm_d_epp_request_slo_violation_total{type}`. That
counter exists only when requests carry the `x-llm-d-slo-ttft-ms` /
`x-llm-d-slo-tpot-ms` headers and the latency-predictor plugin is configured.

## Which latency, measured where

This is the question everything else depends on.

### The interval each metric covers

| Interval | Router (EPP) | Engine (vLLM) | Client |
| --- | --- | --- | --- |
| proxy before the EPP's ext_proc | — | — | yes |
| **router flow-control queue** | yes | — | yes |
| tokenization + **engine scheduler queue** | yes | yes | yes |
| prefill | yes | yes | yes |

- **Router:** `llm_d_epp_request_ttft_seconds`. Its clock starts at
  `HandleRequestHeaders`, *before* admission. It is always emitted, with no
  plugin.
- **Engine:** `vllm:time_to_first_token_seconds`. Its clock starts at frontend
  arrival.
- **Client:** inference-perf / guidellm.

**So yes, an end-to-end TTFT that includes queueing exists, and it is the
router's.** `llm_d_epp_request_ttft_seconds` covers:

- the flow-control queue
- scheduling
- the engine's queue
- prefill

Only the proxy hop before ext_proc is missing, and only a client sees that.
Two catches:

- **Non-streaming requests.** For these, the router's TTFT *equals the whole
  request duration*. Select `streaming="true"`.
- **The plugin's clock is different.** The predictor plugin's histograms and its
  `llm_d_epp_request_slo_violation_total` run on a clock set in `PreRequest`,
  **after** flow-control admission, so they **exclude** the router queue. The
  slo-aware rule's "predicted" path and its "actual" fallback measure different
  intervals. They also record **at end of stream**, so they lag by the request
  duration. Neither is a sizing input here.

### Metrics this design reads

| Metric | Labels | Buckets at the targets | Used for |
| --- | --- | --- | --- |
| `llm_d_epp_request_ttft_seconds` | `model_name, target_model_name, fairness_id, priority, streaming` (no pool, no namespace; scrape-target `namespace`/`service` exist) | **3 s and 0.1 s are bounds** | Per-model TTFT percentile and attainment, **the number the SLO is judged on** |
| `llm_d_epp_request_streaming_tpot_seconds` | as above | 0.1 is a bound | Per-model TPOT percentile and attainment |
| `vllm:request_time_per_output_token_seconds` | `model_name, engine` + pod | 0.1 is a bound | Per-variant TPOT tail ratio `ρ` (vLLM ≥ 0.11) |
| `vllm:inter_token_latency_seconds` | as above | 0.1 is a bound | Already collected as a mean. Its histogram is the fallback for `ρ` on vLLM < 0.11 and SGLang |
| `vllm:request_prefill_time_seconds` | as above | 0.3 … 2.5, 5 (coarse) | Per-variant prefill time **without any queue** (scheduled → first token) |
| `vllm:request_queue_time_seconds` | as above | same | Per-variant engine-queue wait |
| `vllm:time_to_first_token_seconds` | as above | 2.5 and 5, **no 3 s bound** | Diagnostic only, as today |

The old metric names need the same `or` fallback our queue queries already
carry:

- GAIE v1.3–v1.5 published `inference_objective_request_ttft_seconds` and
  friends.
- llm-d-router lists `inference_extension_*` as no longer emitted.

The router's request metrics have **no pool or namespace label**, which is the
known gap tracked as #2309. The arrival-rate query already scopes by the
scrape target's `namespace` and joins the model by `target_model_name`. The
`slo` analyzer reuses that pattern, including its guard that drops the join
when one pool serves several models.

**The split in one line:** the router's histograms say whether the SLO is
met, per model. The engines' histograms say which variant to blame, and how
full it may run. The router cannot attribute a request to a variant, because
it has no pod label on these metrics. The engines cannot see the router queue.

## What exists today

From `main` at `cb37a21e2`:

| | Where | What it means here |
| --- | --- | --- |
| A tier is a ConfigMap entry with no `model_id` | `config/scaling_policy.go:41` | The issue's `workloadClasses` is only the name of the [well-lit path](../well-lit-paths/workload-classes/); the key does not exist. SLO fields go on `config.ScalingPolicy` |
| A tier resolves per **model** | `steadystate/engine.go:998` (`modelPolicy`) | The target is per model. The fill has to be per variant, which is new |
| Every registered analyzer runs; **only saturation's result is used** | `engine_v2.go:174-196` builds `namedResults`; `:982` sets `CompositeSignal: namedResults[0]` | A second analyzer is observe-only **today, with no engine change**. It runs isolated (panics recovered), and its `(D, P)` is published as `wva_analyzer_demand` / `wva_analyzer_target{analyzer=…}` |
| The fill enters sizing in two places, both inside saturation | k1 = physical KV × `KvCacheThreshold` (`replica_capacity.go:78`); `pricingK` = `KvCacheThreshold × ScaleUpThreshold` prices mu for the demand floor (`mu_from_itl.go:167`) | Changing one without the other mis-prices (see below) |
| The ITL line is saturation's private state | `itlWindows` / `itlLines` / `itlBaseline` on `SaturationAnalyzer` (`analyzer.go:85-117`) | The `slo` analyzer needs read access |
| Engine latency is read as **means only** | `max by (model_name, instance, pod)(rate(_sum[1m]) / rate(_count[1m]))`, `queueing_model.go` | No `_bucket` series and no `histogram_quantile` anywhere in Go. Percentiles are new |
| Router latency is read **nowhere in Go** | Only in `deploy/grafana/operational-dashboard.json:2477-2511` (p50/p95 of `llm_d_epp_request_ttft_seconds`, `model_name!=""`) | The dashboard already proves the query works, and shows the `model_name!=""` filter it needs |
| `AvgTTFT` is diagnostic by decision | `domain/replica_metrics.go:203` | A `TTFT(T) = A·T + B` fit was built and removed: queue wait landed in the intercept (82 s). Hence `request_prefill_time`, which has no queue in it |
| A dormant SLO surface exists | `domain.ServiceClass`, `utils.FindModelSLO` (no callers), `deploy/configmap-serviceclass.yaml` | Inherited from WVA, wired to nothing |
| Latency in tests | No e2e spec asserts latency. The simulator runs with **constant** `--time-to-first-token` / `--inter-token-latency`, and many suites pass `--fake-metrics` | An e2e can exercise only the fallback until the simulator's latency rises with load |
| Latency in the benchmark | Client-side TTFT/TPOT from guidellm / inference-perf (`postprocess.py`, `phase_ttft.py` on `bench/keda-recipe-arm`) | **No attainment scorer exists.** The plan lists it as to-do |

## Configuration

```yaml
data:
  interactive: |
    scaleUpThreshold: 0.85
    scaleDownBoundary: 0.70
    analyzers:
      - name: saturation
      - name: slo
    slo:
      ttft: {target: 3s,    percentile: 90}
      tpot: {target: 100ms, percentile: 90}
      margin: 0.10          # aim at target × (1 − margin)
      mode: observe         # observe | enforce
```

- **Listing `slo` under `analyzers:` turns the analyzer on.** This is the
  existing `AnalyzerEnabled` mechanism. The `slo` block gives it targets.
  Either objective may be omitted.
- **`mode: observe`** (the default) runs the analyzer and publishes everything,
  and changes no decision. **`mode: enforce`** hands its fills to saturation.
- **`percentile`** is an integer from 50 to 99. Default 90.
- **Durations use Go syntax.** A value that does not parse is rejected through
  `Validate` → `RecordError`, with the key named. It is never read as zero.
- **The `slo` block is replaced whole, not merged field by field**, by the
  `default → tier → per-model` overlay. A partial override cannot silently keep
  the other objective.
- **The static thresholds remain** as the fallback and as the ceiling.

## Where it lives: a separate analyzer that hands saturation a fill

### Why a separate analyzer

- **Observe mode is free.** The engine already runs every enabled analyzer,
  isolates it, publishes its `(D, P)` and discards it. Phase 0 is a new
  package, a `RegisterAnalyzer` call and queries. It needs no engine change and
  carries no risk to a decision.
- **Ownership.** Latency histograms, quantile interpolation, tail ratios,
  isotonic curves and attainment are a body of code the size of the throughput
  floor. [analyzer-structure](analyzer-structure.md) measured the cost of
  grab-bag files in `saturation/`, so this belongs in its own package.
- **Isolation.** A broken latency query or a panic in quantile code must not
  stop saturation from sizing. The analyzer loop already guarantees that for
  a non-saturation analyzer.
- **The interface anticipated it.** `domain/analyzer.go` names "slo" as a
  future analyzer.

### Why its `(D, P)` is not combined with saturation's

The obvious enforcement is to give the engine two results and combine them.
There are two ways to combine them, and both are wrong.

1. **Max of required, min of spare** (any analyzer may grow, all must agree to
   shrink). The SLO can then only *add* capacity. A loose SLO, the case where
   the cost win is, can never let the fleet run fuller than saturation's
   static point.
2. **Rescale saturation's per-replica capacity by `k_slo ÷ k_static`.** The
   demand floor is priced in replicas, `λ ÷ mu(k)`, where
   `mu(k) ∝ N(k) ÷ ITL(k)`, and it is converted into capacity with saturation's
   own `P`. Rescaling `P` by `r = k_slo ÷ k_static` moves the floor's replica
   count by `1/r`. The true count moves by
   `mu(k_static) ÷ mu(k_slo) = (1/r) × ITL(k_slo) ÷ ITL(k_static)`. When
   **loosening** (`r > 1`, so `ITL(k_slo) > ITL(k_static)`), the rescale
   **under-orders** the floor by that ITL ratio. That is the expensive
   direction: an over-priced capacity is what breaks TTFT unrecoverably in this
   codebase.

The fill has to reach `pricingK` and k1 **inside** saturation, where
`mu(k_slo)` is computed from the line rather than scaled.

### So the split is

| | `slo` analyzer | saturation |
| --- | --- | --- |
| Reads | router + engine latency histograms, saturation's ITL line (read-only), the tier's targets | as today |
| Produces | per variant: `Fill{K, Source, Objective}`; per model: attainment and observed percentiles; a `(D, P)` for publication (below) | `(D, P)`, the composite, as today |
| In `observe` | published, discarded | unchanged |
| In `enforce` | its fills go to saturation's per-variant scale-out point | k1 and `pricingK` read the variant's fill when present, the static value otherwise |

**Its own `(D, P)`.** For publication only, the analyzer reports
`P_v = mu_v(k_slo_v)` (req/s one replica serves within the SLO) and
`D = ArrivalRate`. In observe mode, `wva_analyzer_target{analyzer="slo"}` and
`wva_analyzer_demand{analyzer="slo"}` then give an operator "the SLO alone would
want `D ÷ P` replicas" next to what saturation ordered. Nothing new is needed
on a dashboard to read it. It is never the composite.

### Changes in the engine

These are small, and none are needed in observe mode.

1. **Order.** In enforce mode the engine runs `slo` before saturation and
   passes `map[variant]Fill` into `runV2AnalysisOnly`. It is a new parameter,
   not a field on the model-level `ScalingPolicy`, because the policy is per
   model and the fill is per variant.
2. **The line.** `SaturationAnalyzer` gains a read-only accessor for a
   variant's current line and its source: `own`, `borrowed` or `none`. The
   `slo` analyzer reads the line from the previous cycle. At a 15 s cycle, one
   cycle of lag on a line fitted over a 30 m window is noise. Extracting the
   line store into an engine-owned `signals/itl` store is cleaner, and is
   proposed in analyzer-structure. The accessor does not block on it.
3. **Saturation.** `memoryBound` and `pricingK` take the variant's fill when
   one is present: `k_scale_out = fill.K`, so
   `kvCacheThreshold_v = fill.K ÷ scaleUpThreshold`. Otherwise they use the
   static value. `applyUniversalThreshold`, the allocators, the limiter and
   the cost order are untouched.

## TPOT

For variant `v`, with a line `ITL_v(k) = A·k + B` that saturation has fitted
(or borrowed, see bounds):

```
ρ_v      = q_p(per-request TPOT over v's pods) ÷ mean(ITL over v's pods)      over 5 m, ≥ 1
budget   = tpotTarget × (1 − margin) ÷ ρ_v
k_tpot_v = (budget − B) ÷ A
```

- **The tail quantile.** Quantiles are taken from
  `vllm:request_time_per_output_token_seconds_bucket`, queried
  `sum by (le, pod)(rate(…[5m]))`. Buckets are summed per variant in Go, using
  the same pod → variant mapping (`locator.Locate`) the means use, and the
  quantile is interpolated in Go. On engines without the per-request
  histogram, the `inter_token_latency` buckets stand in. That histogram is
  per token, not per request, so it under-reads a per-request tail. That case
  is labelled.
- **The bucket.** 0.1 s is a bucket bound on both histograms, so a 100 ms
  target is not interpolated.
- **Smoothing.** `ρ` is a rolling maximum over the line's window.
- **Infeasible.** If `budget ≤ A·k_min + B`, no fill meets the target on that
  variant:
  - Its fill is `0`, `Source=infeasible`. Saturation gives it no scale-up
    capacity, so the cost order grows another variant.
  - It is not scaled down below what it serves just for being infeasible.
  - If every variant is infeasible, the model keeps its static fill, and
    `wva_model_scaling_blocked{reason="slo_infeasible"}` is set. It is a
    reason, not a new gauge.

## TTFT

The SLO is judged on the router's TTFT, which decomposes as:

```
TTFT_router  ≈  router queue  +  engine queue  +  prefill
                (flow control)   (vllm:request_   (vllm:request_
                                  queue_time)      prefill_time)
```

The **fill** can only buy the last term in steady state. The first two are
queueing, and they are non-zero only when demand exceeds capacity. That is the
saturation analyzer's queue charge and the demand floor, which already order
capacity for a backlog.

- **The prefill curve.** Keep a monotone (isotonic) curve of mean
  `request_prefill_time` against `k`, binned at 0.05, per variant. Add a tail
  ratio as for TPOT, but from the router's TTFT over the intervals where both
  queues were empty, because the engine's prefill buckets are too coarse for a
  quantile (0.3 … 2.5, 5).
  - **Aggregated variant:** `k` is KV fill. Chunked prefill competes with the
    decode batch.
  - **Prefill role:** `k` is `PrefillComputedTokenRate ÷` its learned ceiling.
  - **No regression, no intercept.** This is the failure the TTFT fit had.
- **The budget.**
  `k_ttft_v` = the largest bin whose upper bound meets
  `ttftTarget × (1 − margin) − queueAllowance`, where `queueAllowance` is the
  router-plus-engine queue p-quantile observed at that fill. Steady state
  should leave it near zero; if it is not, the fill is too high.
- **The queue condition.** An SLO tier with a TTFT target sets the effective
  `queueLengthThreshold` to 1, unless the operator set it lower.
- **The transient is out of scope for the fill.** A step in arrival lands
  before a new replica is Ready, whatever fill the fleet sat at, and llm-d sees
  the same thing. The levers are start time, the warm pool and
  [in-flight sizing](sizing-against-in-flight-capacity.md). The analyzer
  measures it (attainment in the first 120 s of each rise) and does not claim
  to fix it.
- **P/D.**
  - TPOT constrains **decode** variants.
  - TTFT constrains **prefill** variants, with
    `budget = ttftTarget − q_p(KV transfer)`, floored at zero (which means
    infeasible). The transfer time needs a new query on
    `vllm:nixl_xfer_time_seconds`. Nothing in Go reads a NIXL metric today,
    whatever [prefill-ttft-model](prefill-ttft-model.md) says.
  - The pair commit is unchanged.
  - The recipe lists P/D as future work.
- **Aggregated.** The fill is `min(k_tpot_v, k_ttft_v)`.

## Bounds: what the fill may never do

The tail of an over-estimate is the costliest failure here, so the fill is held
to the same rules as k2.

1. **Never above the fitted range.** `K ≤ k_max_observed_v + 0.05`. A loose
   SLO raises fill one step past the observed range per window, and the fleet
   earns the next step by running there.
2. **Never above a hard ceiling.** `K ≤ 0.90` of physical KV, below the
   preemption cliff.
3. **A borrowed line lowers, never raises.** Under `enableLearnedStateReuse`, a
   variant priced from a sibling's line may get a fill **below** its static
   point, never above. This is
   [learned-state sharing](learned-state-sharing-by-similarity.md)'s
   cap-never-order.
4. **Fallback is explicit.** With no ready line, no `ρ`, or too few curve bins,
   there is no fill, saturation uses its static point, and the published
   source is `static`. A variant under flat load never passes the ITL window's
   k-spread gate (≥ 0.30), so this state can last indefinitely and must look
   different from "derived and met".
5. **Non-finite input means fallback.** `A`, `B`, `ρ` and every quantile are
   checked with `math.IsNaN`/`IsInf` first, because `x <= 0` admits NaN.

## Closing the loop on attainment (phase 3)

The fill is open-loop: a model of latency, trusted. Router-measured attainment
is the check. A slow, bounded trim per variant:

```
trim_v ∈ [0.5, 1.0];  K_effective = K × trim_v
model attainment < percentile over 10 min, on an objective v constrains   → trim_v −= 0.05 (once per window)
attainment ≥ percentile + 2 pp for 30 min                                  → trim_v += 0.02
```

Attainment is per model, because the router has no variant label. On a
multi-variant model the trim goes to the variants whose own engine-side
percentile is over budget, and to all of them if none are.

This is not llm-d's control law. That law reacts to latency within a minute
and is the sizer. The trim corrects a model that drifted, over tens of
minutes.

## Observability

The labels follow [signals-as-metrics](signals-as-metrics.md): the model
namespace is `exported_namespace`.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `wva_slo_target_seconds` | model, exported_namespace, tier, objective, percentile | The declared target |
| `wva_slo_observed_seconds` | + scope (`model` from the router, `variant` from the engines), variant | Observed percentile over 5 m |
| `wva_slo_attainment_ratio` | model, exported_namespace, objective | From the router: `bucket(le=target) ÷ count`. Exact when the target is a bucket bound (3 s and 0.1 s are), interpolated otherwise |
| `wva_slo_fill` | + variant, objective, source (`derived`/`static`/`borrowed`/`infeasible`) | The fill computed, applied or not; `mode` says which |

Plus `wva_analyzer_demand` / `wva_analyzer_target{analyzer="slo"}`, which
already exist.

**Combined attainment** (both objectives met on one request) cannot come from
two histograms. Where the predictor plugin and the SLO headers are present, the
analyzer also reads `llm_d_epp_request_slo_violation_total`, but publishes it
labelled `clock="post-admission"`, because that counter excludes the router
queue.

## Phases

Each phase can land alone, and nothing changes a decision before phase 1 with
`mode: enforce`.

### Phase 0: the analyzer, observing

- The `slo` block: parse, validate, resolve.
- The router and engine bucket queries, the Go quantile, and attainment.
- The TPOT fill via the line accessor, `ρ`, and the metrics above.
- The analyzer registered. **No engine change.**
- Delete the dormant `ServiceClass` / `FindModelSLO` /
  `configmap-serviceclass.yaml`. Grep the whole repo first; this needs the
  maintainer's agreement.

This answers "does my tier meet my SLO, and what would it take?", which
nothing answers today.

### Phase 1: TPOT, enforce

- The engine's order and the `map[variant]Fill` parameter.
- The per-variant scale-out point in `memoryBound` and `pricingK`.
- Bounds 1–5 and infeasibility.

### Phase 2: TTFT, enforce

- The prefill curve, the queue allowance, the queue-threshold override.
- The P/D split with the transfer budget.

### Phase 3: the attainment trim

## Tests

**Unit**, in the `slo` package:

- Go quantile interpolation against `histogram_quantile` on the same buckets,
  including a target exactly on a bound.
- TPOT inversion against a hand computation.
- Each bound alone.
- NaN/Inf in each input.
- Infeasible: zero fill, no scale-down below current, and the blocked reason
  when every variant is infeasible.
- Fallback: an unready window, flat load, a missing histogram. Each returns no
  fill **and** publishes `static`.
- The isotonic curve on non-monotone input.
- The trim's asymmetry.
- Old metric names answered by the `or` fallback.

**In saturation:**

- With an empty fill map, `NamedAnalyzerResult` is byte-identical to today's.
- With a fill, k1 and `pricingK` move together.
- The loosening case of the floor: the under-order a rescale would produce
  does not occur.

**e2e.** Two specs need nothing new from the simulator:

- `slo` in observe mode changes no desired count.
- On the simulator's constant latency, the line never fits, so the published
  source is `static`.

A derived-fill spec needs the simulator's latency to rise with load. Check
whether `llm-d-inference-sim` v0.9.0 has a load factor; we pass none today.

**Benchmark.** The G1 replication in
[keda-recipe-comparison](https://github.com/ev-shindin/llm-scaling-manager/blob/bench/keda-recipe-arm/docs/plans/benchmark/keda-recipe-comparison.md)
(branch `bench/keda-recipe-arm`) needs the attainment scorer it lists as to-do.
That scorer and our `wva_slo_attainment_ratio` are cross-checks of each other
on the same requests. Add the arm **ours-slo**: an `interactive` tier with
`ttft 3s / tpot 100ms p90` and `mode: enforce`. These predictions are
registered before any run:

- TPOT does not bind for either arm: a 32B replica at our 0.68 point holds a
  batch of about 30. **ours-slo's advantage over ours must come from loosening
  the fill against TTFT.** Expect `R̄_ours-slo < R̄_ours`, with attainment
  within 2 pp.
- If ours-slo does not hold fewer replicas than ours on this load, the
  derivation bought nothing on llm-d's benchmark. The P/D and mixed-accelerator
  cases are then the whole case for it, and they need their own runs.
- Attainment in the first 120 s of each rise is reported apart from steady
  state.

## Alternatives considered

- **SLO logic inside saturation.** It needs no ordering change, but it puts
  histogram handling into a package already measured as too big. It also loses
  the free observe mode and the isolation a registered analyzer gets.
- **A combined `(D, P)` from two analyzers.** It can only tighten (max/min), or
  it under-orders the floor when loosening (rescale). See
  [above](#why-its-d-p-is-not-combined-with-saturations).
- **llm-d's control law in the controller.** It is reactive (latency rises after
  capacity is short), it does not price variants, and it is blind to P/D.
  Phase 3 keeps its one good property, correction from measured latency, at a
  slow time scale.
- **The predictor's histograms as the signal.** They are present only with the
  plugin, recorded at end of stream, and on a clock that excludes the router
  queue. They are a routing feature, not a measurement of the SLO.
- **Static presets per SLO.** That is today's *Strict* preset. It is wrong
  whenever the hardware or the shape differ from where it was made.

## Open questions

1. **`ρ` across a shape change.** Is it reset with the ITL window on a
   shape-change hold, or kept? Keeping it is conservative only if the new
   shape's tail is no wider.
2. **The window.** 5 m matches the line. llm-d smooths over 45 s. A shorter
   window makes attainment noisy on a small fleet.
3. **Several models behind one pool.** The router's latency metrics carry
   `model_name`, but the arrival-rate join already refuses ambiguity
   (`count == 1`). Should attainment be refused there too, or published with
   a warning?
4. **Scale to zero.** Should an SLO tier warn when the TTFT target is below the
   measured cold-start time? Proposed: warn in the effective-policy log line,
   do not refuse.
5. **The utilization-share optimizer** reads `ScaleUpThreshold` directly
   (`utilization_share_groups.go:359`). It must read the variant's fill
   instead, or the two are mutually exclusive in phase 1.
