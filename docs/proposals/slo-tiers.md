# SLO tiers: size to a TTFT/TPOT target, not only to a fill threshold

**Status:** proposal, 2026-10-14, revised the same day against the router's
metrics, the engine's analyzer loop and the composite on
`deanscaler/composite-analyzer`. Nothing built. Tracks
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
- the per-variant latency curves and tail ratios
- attainment

It produces an ordinary `(D, P)` per role: demand in tokens/s, and for each
variant the tokens/s one replica serves **while still meeting the target**.
It is combined with saturation by the **composite** on
`deanscaler/composite-analyzer`: per ScaledObject, `N = D ÷ P` from every
eligible analyzer, take the max, and fall back to saturation only where
nothing else has a signal. A tier chooses between two modes:

- **Guard.** Both analyzers contribute. The SLO can only add replicas.
- **Sizer.** Saturation is disabled for the tier. The SLO decides, and
  saturation is the per-variant fallback until the SLO has a curve.

Nothing in the sizing arithmetic is special-cased for the SLO.

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
    scaleUpThreshold: 0.90        # headroom below the SLO's own replica count
    scaleDownBoundary: 0.75
    analyzers:
      - name: saturation
        enabled: false            # sizer mode; omit this line for guard mode
      - name: slo
        shadow: true              # run and publish, contribute nothing
    slo:
      ttft: {target: 3s,    percentile: 90}
      tpot: {target: 100ms, percentile: 90}
```

- **Listing `slo` under `analyzers:` turns the analyzer on.** This is the
  existing `AnalyzerEnabled` mechanism. The `slo` block gives it targets.
  Either objective may be omitted.
- **The mode is the `analyzers:` list.** There is no separate switch.

  | `analyzers:` | Mode | What decides |
  | --- | --- | --- |
  | `slo` with `shadow: true` | observe | saturation alone; the SLO's view is published |
  | `saturation` + `slo` | guard | per SO, the larger of the two replica counts |
  | `saturation {enabled: false}` + `slo` | sizer | the SLO; saturation where the SLO has no signal for that SO |

- **`shadow`** is a new, generic field on the analyzer entry. A shadowed
  analyzer runs and publishes `wva_analyzer_demand` / `wva_analyzer_target`,
  and is left out of the composite's eligible set. It is the same exclusion
  the composite already applies to a disabled saturation (`excludeByName`).
  It is useful to every new analyzer, not only this one.
- **There is no `margin`.** The composite applies the tier's
  `scaleUpThreshold` to the SLO's replica count like any other:
  `RC = D_sat ÷ scaleUp − anticipated`, so the fleet runs
  `N_slo ÷ scaleUp` replicas. The SLO prices its `P` at the fill where the
  percentile **equals** the target, and `scaleUpThreshold` is the headroom.
  That leaves one knob, not two that compound. `scaleDownBoundary` stays the
  hysteresis band.
- **`percentile`** is an integer from 50 to 99. Default 90.
- **Durations use Go syntax.** A value that does not parse is rejected through
  `Validate` → `RecordError`, with the key named. It is never read as zero.
- **The `slo` block is replaced whole, not merged field by field**, by the
  `default → tier → per-model` overlay. A partial override cannot silently keep
  the other objective.

## Where it lives: an analyzer, combined by the composite

### Why a separate analyzer

- **Observe mode is free today.** On `main` the engine runs every enabled
  analyzer, isolates it, publishes its `(D, P)` and uses only saturation's
  (`engine_v2.go:982`). Phase 0 is a new package, a `RegisterAnalyzer` call and
  queries. It needs no engine change and carries no risk to a decision.
- **Ownership.** Latency histograms, quantile interpolation, tail ratios,
  isotonic curves and attainment are a body of code the size of the throughput
  floor. [analyzer-structure](analyzer-structure.md) measured the cost of
  grab-bag files in `saturation/`.
- **Isolation.** A broken latency query or a panic in quantile code must not
  stop saturation from sizing, and the analyzer loop already guarantees that.
- **The contract fits.** [engine-structure](engine-structure.md) says what a
  new analyzer must do and nothing else: demand and per-replica capacity in
  **one unit per result**, per role; mark the roles it has no model for; state
  its holds; register. The SLO analyzer is that, with nothing extra.

### How the results combine: the composite

The composite on `deanscaler/composite-analyzer` is the combine stage that
[engine-structure](engine-structure.md) plans (stage 4, `analyze/combine`).
The rules, as built there:

```
N_i(SO)      = D_i[role(SO)] ÷ P_i(SO)                    each eligible analyzer, in its own units
N_com(SO)    = max over contributors of N_i(SO)            undefined values skipped, never read as 0
               saturation is a contributor unless disabled, and the fallback when nothing contributed
P_com(SO)    = D_sat[role] ÷ N_com(SO)                     back in saturation's units, as a definition
RC, SC       = the existing applyUniversalThreshold, on P_com
```

Each SO carries a decision path: `C0-agree`, `C1-single`, `C2-sat-fallback`
or `C4-no-signal`.

Why this is the right combine for an SLO:

- **The SLO's replica count is computed in its own units.** It is not a
  rescale of saturation's capacity. `P_slo` is the token rate at the SLO's
  fill, from the ITL line (`itl.TokenRate(line, k_slo, kvMax, kvReq)`), so
  `mu(k_slo)` is computed rather than scaled. A rescale would under-order
  when loosening by `ITL(k_slo) ÷ ITL(k_static)`, so this matters.
- **Guard and sizer are already in the rules.** With saturation eligible, the
  max lets the SLO only tighten. With saturation disabled, the spec's §5.1
  applies: *"saturation is a fallback, not a floor ... the composite may
  legitimately come out below sat alone"*. That is the loosening path, per SO.
- **Fallback per variant comes for free.** An SO for which the SLO has no
  curve reports `ReasonNoData`, contributes nothing, and lands on
  `C2-sat-fallback`. The decision path then says, per variant, whether the SLO
  or saturation decided. That is the `source` this proposal would otherwise
  have had to invent.
- **Undefined is not zero.** The branch's `(value, ok)` rule is what an SLO
  needs: a variant without a curve must neither vote 0 nor vote infinity.
- **P/D is per role.** `D` is per role and `P` per SO. The cross-role coverage
  rule, `min(cov(prefill), cov(decode)) + cov(both)`, which per-token-economy
  already adopted, is the right model-level view for TTFT on prefill and TPOT
  on decode.

### What the SLO analyzer returns

| Role | `D` | `P_v` |
| --- | --- | --- |
| decode / both | `λ × O` output tok/s, plus backlog over drain time (`signals/floor`, the same pricing saturation's floor uses) | `itl.TokenRate(line_v, k_v)` at `k_v = min(k_tpot_v, k_ttft_v)` |
| prefill | `λ × ILeff` input tok/s, plus backlog | prefill token ceiling × `u_ttft_v` (the prefill utilization at which TTFT meets its budget) |

The decode half already exists in the throughput analyzer, which prices
decode per token (`λ × O` against `computeVariantSupply`). The SLO analyzer
reuses that arithmetic at a different fill. It does not copy it: the shared
pieces move to `signals` as engine-structure stage 1 did for the floor.

### What the composite does not have yet

These are the parts the SLO needs on top of the branch, in order.

1. **The branch landed.** Our review (engine-structure, 2026-09-21) found three
   things to fix before it merges:
   - It regresses #82: quota charging is tied to `CompositeHasSignal`.
   - The saturation fallback skips the per-SO `Reason` filter.
   - The fallback's eligibility guard is untested on the real
     `buildComposite`.

   The branch is based on our `main` of 2026-09-11, so the rebase overlaps
   #76/#82 and everything since. **Enforce mode depends on this. Observe mode
   does not.**
2. **`shadow: true`.** It excludes an analyzer from the eligible set while
   still running and publishing it. Without it, after the composite lands,
   listing `slo` means it votes.
3. **A per-SO "do not grow" hold, for infeasibility.** Under the branch's
   rules, `P ≤ 0` is *undefined*, and an undefined SO falls back to
   saturation. In sizer mode, a variant that cannot meet the target at any
   fill would then be grown at saturation's price, which is the opposite of
   what we want. Engine-structure already plans holds on `AnalyzerResult`
   (`Role, Reason, Cap`). The SLO needs that at SO grain: `Cap` = the current
   replicas, `Reason = slo-infeasible`. The composite then caps `N_com` there,
   and the cost order grows another variant. This is the one rule to agree
   with the branch's author.
4. **Read access to the ITL line.** It is saturation's private state
   (`itlWindows` / `itlLines`). The options are an accessor on
   `SaturationAnalyzer` (the previous cycle's line; one cycle of 15 s lag
   against a 30 m window is noise), or the engine-owned `signals/itl` store
   engine-structure points at. The accessor does not block on the store.

## TPOT

For variant `v`, with a line `ITL_v(k) = A·k + B` that saturation has fitted:

```
ρ_v      = q_p(per-request TPOT over v's pods) ÷ mean(ITL over v's pods)      over 5 m, ≥ 1
k_tpot_v = (tpotTarget ÷ ρ_v − B) ÷ A
P_v      = itl.TokenRate(line_v, min(k_tpot_v, bounds), kvMax_v, kvReq)       output tok/s
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
- **Infeasible.** If `tpotTarget ÷ ρ_v ≤ A·k_min + B`, no fill meets the target
  on that variant:
  - The analyzer emits the per-SO hold (`Cap` = current replicas,
    `slo-infeasible`), not a `P` of zero. Under the composite's rules a zero
    is *undefined* and would fall back to saturation, which would grow the
    variant.
  - It is not scaled down below what it serves just for being infeasible.
  - If every variant is infeasible, the holds stand and
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
queueing, and they are non-zero only when demand exceeds capacity. The SLO
analyzer's backlog term prices them, as saturation's floor does.

- **The prefill curve.** Keep a monotone (isotonic) curve of mean
  `request_prefill_time` against utilization, binned at 0.05, per variant. Add
  a tail ratio as for TPOT, but from the router's TTFT over the intervals where
  both queues were empty, because the engine's prefill buckets are too coarse
  for a quantile (0.3 … 2.5, 5).
  - **Aggregated variant:** the utilization is KV fill. Chunked prefill
    competes with the decode batch, so `k_ttft_v` caps the same `k` as TPOT.
  - **Prefill role:** the utilization is `PrefillComputedTokenRate ÷` its
    learned ceiling. `P_v` = ceiling × `u_ttft_v`.
  - **No regression, no intercept.** This is the failure the TTFT fit had.
- **The budget.** `u_ttft_v` is the largest bin whose upper bound meets
  `ttftTarget − queueAllowance`, where `queueAllowance` is the
  router-plus-engine queue p-quantile observed at that fill. Steady state
  should leave it near zero; if it is not, the fill is too high.
- **The transient is out of scope for the fill.** A step in arrival lands
  before a new replica is Ready, whatever fill the fleet sat at, and llm-d sees
  the same thing. The levers are start time, the warm pool and
  [in-flight sizing](sizing-against-in-flight-capacity.md). The analyzer
  measures it (attainment in the first 120 s of each rise) and does not claim
  to fix it.
- **P/D.**
  - TPOT sizes **decode** SOs.
  - TTFT sizes **prefill** SOs, with `budget = ttftTarget − q_p(KV transfer)`,
    floored at zero (which means infeasible). The transfer time needs a new
    query on `vllm:nixl_xfer_time_seconds`. Nothing in Go reads a NIXL metric
    today, whatever [prefill-ttft-model](prefill-ttft-model.md) says.
  - The composite's per-role `D` and the cross-role coverage rule tie the two
    roles together. The recipe lists P/D as future work.
- **Aggregated.** The fill is `min(k_tpot_v, k_ttft_v)`.

## Bounds: what the SLO's `P` may never do

The tail of an over-estimate is the costliest failure here. Over-pricing `P`
is what breaks TTFT unrecoverably, so `P` is held to the same rules as k2.

1. **Never above the fitted range.** `k ≤ k_max_observed_v + 0.05`. A loose
   SLO raises fill one step past the observed range per window, and the fleet
   earns the next step by running there.
2. **Never above a hard ceiling.** `k ≤ 0.90` of physical KV, below the
   preemption cliff.
3. **A borrowed line never loosens.** Under `enableLearnedStateReuse` a variant
   may be priced from a sibling's line. In guard mode that is safe, because the
   max can only tighten. In sizer mode a borrowed line would let a sibling's
   measurement run this variant fuller than saturation, so the analyzer
   reports `ReasonNoData` for it and the SO falls back to saturation
   (`C2-sat-fallback`). This is
   [learned-state sharing](learned-state-sharing-by-similarity.md)'s
   cap-never-order.
4. **Fallback is explicit.** With no ready line, no `ρ`, or too few curve bins,
   the SO reports `ReasonNoData`, and its decision path shows who decided.
   A variant under flat load never passes the ITL window's k-spread gate
   (≥ 0.30), so this state can last indefinitely and must look different from
   "SLO decided and met".
5. **Non-finite input means no data.** `A`, `B`, `ρ` and every quantile are
   checked with `math.IsNaN`/`IsInf` first, because `x <= 0` admits NaN.

## Closing the loop on attainment (phase 3)

`P` is open-loop: a model of latency, trusted. Router-measured attainment is
the check. A slow, bounded trim per variant:

```
trim_v ∈ [0.5, 1.0];  k_effective = k × trim_v
model attainment < percentile over 10 min, on an objective v serves   → trim_v −= 0.05 (once per window)
attainment ≥ percentile + 2 pp for 30 min                              → trim_v += 0.02
```

Attainment is per model, because the router has no variant label. On a
multi-variant model the trim goes to the variants whose own engine-side
percentile is over target, and to all of them if none are.

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
| `wva_slo_fill` | + variant, objective, source (`own`/`borrowed`/`none`/`infeasible`) | The fill behind the SLO's `P` |

Two things that already exist cover the rest:

- `wva_analyzer_demand` / `wva_analyzer_target{analyzer="slo"}`: the SLO's
  `(D, P)`. In shadow mode, `D ÷ P` next to the actual count answers "what
  would the SLO want".
- The composite's per-SO decision path and contributor list
  (`composite-contributors` in the cycle log): which analyzer decided each
  variant, this cycle.

**Combined attainment** (both objectives met on one request) cannot come from
two histograms. Where the predictor plugin and the SLO headers are present, the
analyzer also reads `llm_d_epp_request_slo_violation_total`, but publishes it
labelled `clock="post-admission"`, because that counter excludes the router
queue.

## Phases

Phase 0 needs nothing from the composite. Phases 1 and 2 need it landed.

### Phase 0: the analyzer, observing (on today's `main`)

- The `slo` block: parse, validate, resolve.
- The router and engine bucket queries, the Go quantile, and attainment.
- `ρ`, the TPOT fill and `P`, via the ITL line accessor; the metrics above.
- The analyzer registered. **No engine change.** On `main` its result is
  published and discarded, which is shadow mode by construction.
- Delete the dormant `ServiceClass` / `FindModelSLO` /
  `configmap-serviceclass.yaml`. Grep the whole repo first; this needs the
  maintainer's agreement.

This answers "does my tier meet my SLO, and what would it take?", which
nothing answers today.

### Phase 1: TPOT, enforce

- **Depends on the composite branch landing**, with its three review fixes.
- The `shadow` field.
- The per-SO hold, agreed with the branch's author.
- Bounds 1–5.

### Phase 2: TTFT, enforce

- The prefill curve, the queue allowance, the transfer-time query.
- The P/D roles.

### Phase 3: the attainment trim

## Tests

**Unit**, in the `slo` package:

- Go quantile interpolation against `histogram_quantile` on the same buckets,
  including a target exactly on a bound.
- TPOT inversion and `P` against a hand computation.
- Each bound alone.
- NaN/Inf in each input.
- Infeasible: a hold, not a zero `P`.
- Fallback: an unready window, flat load, a missing histogram, and a borrowed
  line in sizer mode. Each returns `ReasonNoData` for that SO.
- The isotonic curve on non-monotone input.
- The trim's asymmetry.
- Old metric names answered by the `or` fallback.

**In the composite:**

- Guard mode: `N_com = max(N_sat, N_slo)` per SO.
- Sizer mode: a variant with an SLO signal is decided by it (`C1-single`),
  and one without falls back (`C2-sat-fallback`).
- A shadowed analyzer changes no `N_com`.
- An infeasible SO is held at current replicas in sizer mode. Without the hold
  it would fall back to saturation and grow; that case is the negative control.
- The loosening case: in sizer mode `N_com` comes out below saturation's.

**e2e.** Two specs need nothing new from the simulator:

- `slo` in shadow mode changes no desired count.
- On the simulator's constant latency, the line never fits, so every SO is
  `C2-sat-fallback` in sizer mode.

A spec where the SLO decides needs the simulator's latency to rise with load.
Check whether `llm-d-inference-sim` v0.9.0 has a load factor; we pass none
today.

**Benchmark.** The G1 replication in
[keda-recipe-comparison](https://github.com/ev-shindin/llm-scaling-manager/blob/bench/keda-recipe-arm/docs/plans/benchmark/keda-recipe-comparison.md)
(branch `bench/keda-recipe-arm`) needs the attainment scorer it lists as to-do.
That scorer and our `wva_slo_attainment_ratio` are cross-checks of each other
on the same requests. Add the arm **ours-slo**: an `interactive` tier in sizer
mode with `ttft 3s / tpot 100ms p90`. These predictions are registered before
any run:

- TPOT does not bind for either arm: a 32B replica at our 0.68 point holds a
  batch of about 30. **ours-slo's advantage over ours must come from loosening
  against TTFT.** Expect `R̄_ours-slo < R̄_ours`, with attainment within 2 pp.
- If ours-slo does not hold fewer replicas than ours on this load, the SLO
  analyzer bought nothing on llm-d's benchmark. The P/D and mixed-accelerator
  cases are then the whole case for it, and they need their own runs.
- Attainment in the first 120 s of each rise is reported apart from steady
  state.

## Alternatives considered

- **SLO logic inside saturation.** It puts histogram handling into a package
  already measured as too big, and it loses the free shadow mode and the
  isolation a registered analyzer gets.
- **Hand saturation a per-variant fill** (this proposal's earlier draft). It
  reaches `pricingK` and k1 directly, but it needs an engine ordering change, a
  new per-variant parameter through saturation, and its own fallback and
  "who decided" reporting. The composite gives all three, and pricing `P` from
  `itl.TokenRate` at the SLO's fill avoids the rescale error that motivated
  the draft.
- **Combining by rescaling saturation's capacity.** It under-orders when
  loosening by `ITL(k_slo) ÷ ITL(k_static)`. The composite's
  `N = D_i ÷ P_i`, each in the analyzer's own units, does not.
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

1. **The per-SO hold.** Does the composite take per-SO holds (`Cap` on one
   ScaledObject), or only the per-role holds engine-structure plans? The SLO
   needs per SO: infeasibility is a property of one accelerator, not of a
   role.
2. **`ρ` across a shape change.** Is it reset with the ITL window on a
   shape-change hold, or kept? Keeping it is conservative only if the new
   shape's tail is no wider.
3. **The window.** 5 m matches the line. llm-d smooths over 45 s. A shorter
   window makes attainment noisy on a small fleet.
4. **Several models behind one pool.** The router's latency metrics carry
   `model_name`, but the arrival-rate join already refuses ambiguity
   (`count == 1`). Should attainment be refused there too, or published with
   a warning?
5. **Scale to zero.** Should an SLO tier warn when the TTFT target is below the
   measured cold-start time? Proposed: warn in the effective-policy log line,
   do not refuse.
6. **The utilization-share optimizer** reads `ScaleUpThreshold` and the
   per-replica capacity directly (`utilization_share_groups.go:359`). Does it
   consume the composite's `P_com`, and does it after the branch lands?
