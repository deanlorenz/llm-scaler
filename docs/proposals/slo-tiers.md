# SLO tiers: size to a TTFT/TPOT target, not only to a fill threshold

**Status:** proposal, 2026-10-14. **Phase 0 (the analyzer, observing) and
step 2a (the TTFT curve's inputs, observed) are built** in
`internal/engines/analyzers/slo`; nothing that changes a replica count is.
Reviewed in several rounds; the findings are folded in and the substantive ones
are listed under [Review findings](#review-findings). Tracks
[#141](https://github.com/ev-shindin/llm-scaling-manager/issues/141).

**Tags.** A behavioural claim is tagged **(built)** when the code on this
branch does it, **(phase N)** when it is planned for that phase, and **(not
built)** when no phase owns it yet.

## At a glance

**Decided**

- A separate `slo` analyzer, combined with saturation later by the composite.
- TPOT is a **mean** that inverts the ITL(k) line; TTFT is a **percentile** on
  the router's histogram.
- Observe is the default, forever; phase 1 needs `slo.mode: enforce`.
- Fill clamped to [0.15, 0.80]; an infeasible target abstains.
- Transients are excluded from judgement (grace at most 30 min); unschedulable
  capacity is judged, not excluded.
- One ITL fitter, `itl.Tracker`, shared by saturation and slo.
- Alert promtool tests ship with the alerts.

**Built** (phase 0 and step 2a): the `slo` block with validation, overlay and
warnings; TPOT pricing with the batch cap; router judgement with the transient
exclusion (`zero-replicas`, `starting`, `unobserved`); the TTFT curve's inputs;
the `wva_slo_*` metrics; alerts `WVAModelSLOMissed` and `WVAModelSLOUnjudged`.

**Open** ([Open questions](#open-questions)): the gates' measurable form;
pinned pricing in sizer mode; the simulator's load factor; nixl transfer time
per request or per chunk; the 2a data questions; a per-SO growth cap,
multi-model pools, data-parallel replicas.

**Gates**: phase 1 (TPOT enforce) needs the composite items, the estimator
fixes and a phase-0 data gate; 2b (TTFT priced, in shadow) a 2a data gate; 2c
(TTFT enforce) its own gate. The data gates are open.

## The problem

A tier says how full a replica may run. It does not say how fast a request
must be served. An operator with a latency SLO has to turn it into
`kvCacheThreshold` and `scaleUpThreshold` by hand, and redo that whenever the
model, the accelerator or the request shape changes. The documented *Strict
(low latency SLO)* preset (`kvCacheThreshold 0.70`, `queueLengthThreshold 3`)
is one such guess, made once for every model.

llm-d's SLO-aware recipe takes the target directly. Its benchmark reports
**95.8 % combined attainment at 5.93 average replicas, where a static pool of 8
attains 100 %**: 26 % fewer replicas for 4.2 points of attainment, averaged over
repeated episodes (GKE, H100, Qwen3-32B at TP=2; TTFT ≤ 3 s and TPOT ≤ 100 ms
at p90; staged rates 2→4→6→8→10→4→1 req/s; bounds 3–8; **85.6 %** with a cold
predictor). It needs no controller of its own and has a measured result; that
is the bar this proposal has to meet.

The recipe is scoped to *"one inference pool serving one model variant against
one SLO pair"*, and its residual violations sit *"at the first capacity
crossing"*. Our comparison page, [keda-alone](../comparison/keda-alone.md),
lists this as a case where KEDA alone is the better choice today. The goal is
to close that row **without giving up what the recipe lacks**: several variants
with cost-aware choice, P/D pairing, a GPU budget, and scale to zero.

## The two objectives are different statistics

- **TPOT is a mean.** It is the average time per output token, and the ITL
  line `ITL(k) = A·k + B` is fitted on a mean of nearly the same quantity:
  `vllm:inter_token_latency_seconds`, sum over count, per variant. A mean-TPOT
  target therefore **inverts the existing line**, with no tail model:

  ```
  k_tpot = (tpotTarget − B) ÷ A
  ```

  "Nearly", in two ways:
  - **Weighting.** The router's TPOT is a request-weighted mean of each
    request's `(e2e − TTFT) ÷ (n − 1)`; the engine's ITL is weighted per output
    step. They differ by the covariance of output length and per-token speed.
    Measuring that gap needs the line's ITL at the fleet's current fill beside
    the router's TPOT; phase 0 does not publish the former yet (a phase-0
    follow-up, not built).
  - **Speculative decoding.** vLLM records one ITL observation per output
    *step*, and a step can emit several tokens, so a line fitted on it
    under-prices `k` by the accepted length. Before phase 1 prices anything,
    the line is fitted on ITL divided by tokens per step (phase 1).

- **TTFT is a percentile.** It is dominated by queueing, it has a long tail,
  and no existing model of it survived (see [TTFT](#ttft-phase-2)). It is
  measured as a percentile from the router's histogram and is the harder half.

A TPOT **percentile** (llm-d scores p90 TPOT) is a later extension; see
[Later](#later-a-tpot-percentile).

## The design in one paragraph

A new **`slo` analyzer**, separate from saturation, owns everything about
latency: the router's and the engines' latency series, each variant's fill at
the target, and attainment. It returns an ordinary `(D, P)` per role. For
decode, that is demand in output tokens/s and, per variant, the tokens/s one
replica sustains **at the fill where its mean ITL equals the TPOT target**. The
**composite analyzer** (summarised in [Appendix: the composite
rules](#appendix-the-composite-rules)) combines it with saturation: per
ScaledObject, `N = D ÷ P` from each eligible analyzer, the max, and saturation
as the fallback where nothing else has a signal. A tier chooses one of two
modes:

- **Guard.** Both analyzers vote, and the SLO can only add replicas.
- **Sizer.** Saturation is explicitly disabled; the SLO decides, and saturation
  is the per-variant fallback.

The SLO's count reaches KEDA through the same optimizer output and
external-scaler push as every other count; there is no new actuation path.
Before the composite lands, the analyzer runs in observe mode with no change to
the engine's decision path (built): its result is published and not consumed.

## What llm-d's KEDA recipes read

At llm-d `7d5f86d`, under `guides/workload-autoscaling/`:

- **`keda-epp`** scales on the router's flow-control queue size and
  `llm_d_epp_request_running` (which excludes queued requests), or on the
  pool-level `llm_d_epp_flow_control_pool_saturation` gauge.
- **`keda-epp-token-aware`** divides in-flight tokens by a hand-calibrated
  tok/s, beside `vllm:kv_cache_usage_perc`.
- **`slo-aware`** takes the p90 of the predictor's TTFT and TPOT (falling back
  to the router's own), with **no label selector** (one pool, one model), and
  scales on `s = max(ttft/3.0, tpot/0.1)`, clamped at 2 and averaged over
  45 s. It scales up above 0.55 and down below 0.40, discounts `s` by
  `ready ÷ provisioned` while replicas start, and runs HPA at 30 s up and 180 s
  down. Attainment is scored from `llm_d_epp_request_slo_violation_total`,
  which exists only with the SLO headers and the latency-predictor plugin.

## Which latency, measured where

| Interval | Router (EPP) | Engine (vLLM) | Client |
| --- | --- | --- | --- |
| proxy before the EPP's ext_proc | — | — | yes |
| **router flow-control queue** | yes | — | yes |
| tokenization + **engine scheduler queue** | yes | yes | yes |
| prefill | yes | yes | yes |

The router's `llm_d_epp_request_ttft_seconds` starts its clock at
`HandleRequestHeaders`, before admission, and needs no plugin; the engine's
`vllm:time_to_first_token_seconds` starts at frontend arrival; the client is
inference-perf / guidellm. **An end-to-end TTFT that includes queueing exists,
and it is the router's**; only the proxy hop is missing. Three catches:

- **Non-streaming requests.** Their TTFT equals the whole request duration.
  Select `streaming="true"`.
- **The predictor plugin uses a different clock.** Its histograms and
  `llm_d_epp_request_slo_violation_total` start in `PreRequest`, **after**
  admission, so they exclude the router queue. Neither is a sizing input here.
- **Everything is recorded at end of stream**, the router's own TTFT and TPOT
  included. A request's TTFT lands in the window when the response
  *completes*, and a stream that never completes (an abort, a timeout, a
  rejection) is never recorded. So attainment counts survivors and **reads high
  exactly when the SLO is being lost** (see [Attainment](#attainment)).

### Metrics this design reads

| Metric | Labels | Used for |
| --- | --- | --- |
| `llm_d_epp_request_ttft_seconds` | `model_name, target_model_name, fairness_id, priority, streaming`; no pool or namespace (scrape-target `namespace`) | Per-model TTFT percentile and attainment: **the number the SLO is judged on**. 3 s and 0.1 s are bucket bounds |
| `llm_d_epp_request_streaming_tpot_seconds` | as above, no `streaming` label | Per-model mean TPOT as the router sees it. **Recorded only when the response carries `usage`**: the client must send `stream_options.include_usage`, or vLLM must run with `--enable-force-include-usage` |
| `vllm:inter_token_latency_seconds` | per pod | Already collected as a 1 m mean; the ITL line is fitted on it |
| `vllm:request_prefill_time_seconds`, `vllm:request_queue_time_seconds` | per pod | Step 2a: per-variant prefill time and engine queue wait, **as means**. Coarse buckets |
| `vllm:time_to_first_token_seconds` | per pod | Diagnostic only, as today |

The router's request metrics have **no pool or namespace label** (#2309). The
analyzer scopes by the scrape target's `namespace`, as llm-d's keda-epp queries
do, which assumes the EPP runs in the model's namespace (llm-d deploys it
there; nothing enforces it). The series carry `model_name` /
`target_model_name` themselves and are resolved to their model in Go.

**The split:** the router's histograms say whether the SLO is met, per model.
The engines' series say which variant to price, and how full it may run.

## What exists today

From `main` at `fee45eb52` (this PR's base), with what this branch changed:

| | Where | What it means here |
| --- | --- | --- |
| A tier is a ConfigMap entry with no `model_id` | `config.PolicyEntryKey`; fields on `config.ScalingPolicy` | The issue's `workloadClasses` is only the name of the [well-lit path](../well-lit-paths/workload-classes/); the key does not exist |
| A tier resolves per **model** | `modelPolicy` in `steadystate/engine.go`; variants that disagree get the lexicographically smallest tier | The target is per model, and the fill is per variant |
| `analyzers:` is replaced whole by an overlay, and an absent name is disabled | `ScalingPolicy.Merge`; `ScalingPolicy.AnalyzerEnabled` returns false for an unlisted name | Dangerous for mode selection; see [Configuration](#configuration) |
| **An entry that lists `analyzers:` resets the scaling band** | `ScalingPolicy.ApplyDefaults` fills `scaleUpThreshold 0.85 / scaleDownBoundary 0.70` per V2-shaped entry; `Merge` copies them over a tuned `default` band | A tier that only adds `slo` changes saturation's sizing. Pre-existing; a separate fix. Until then a tier listing `slo` must restate the band |
| A `Validate` error drops the **whole tier entry** | `parseScalingPolicyConfigRejects` in `controller/configmap_helpers.go` | A bad `slo` block drops the tier, not one key |
| Every enabled analyzer runs; **only saturation's result is used** | `runAnalyzersAndScore`; `collectV2ModelRequest` sets `CompositeSignal: namedResults[0]` | The rest reach only `recordAnalyzerMetrics` (`wva_analyzer_*`), liveness and the cycle log. Observe mode is free |
| The ITL fitter | Was saturation's private, decode-only state. **This branch extracts it as `itl.Tracker`** (built), used by both analyzers | See [Its own lines](#its-own-lines) |
| `itl.TokenRate` has no batch cap | Saturation caps at `max_num_seqs` in `mu_from_itl.go`; the fitted range [0.15, 0.80] is `itl.DefaultMinObservableK` / `DefaultMaxObservableK`, used by `pricingK` | The `slo` analyzer applies the same cap and clamp (built) |
| `AvgTTFT` is diagnostic by decision | `domain.ReplicaMetrics.AvgTTFT` | A `TTFT(T) = A·T + B` fit was built and removed: queue wait landed in the intercept (82 s) |
| A dormant SLO surface exists | `domain.ServiceClass`, `utils.FindModelSLO` (no callers), `deploy/configmap-serviceclass.yaml` | Inherited from WVA, wired to nothing |
| Latency in tests | No e2e spec asserts latency; the simulator runs constant latencies, and many suites pass `--fake-metrics` | An e2e can exercise only the fallback |

## Configuration

```yaml
data:
  interactive: |
    scaleUpThreshold: 0.85          # restated: see "resets the scaling band" above
    scaleDownBoundary: 0.70
    analyzers:
      - name: saturation
      - name: slo
    slo:
      tpot: {target: 100ms}                  # a mean
      ttft: {target: 3s, percentile: 90}     # a percentile; observed only (TTFT enforce is step 2c)
      mode: enforce                          # phase 1: not parsed yet, ignored today
```

**Observe is the default, and stays the default.** Enforcement needs
`slo.mode: enforce` (phase 1). A policy written for phase 0 keeps observing when
phase 1 ships, rather than starting to decide on the day the composite lands.
With `mode: enforce`, the `analyzers:` list picks the mode:

| `analyzers:` (with `mode: enforce`) | Mode | What decides |
| --- | --- | --- |
| `saturation` + `slo` | guard | per SO, the larger of the two replica counts |
| `saturation {enabled: false}` + `slo` | sizer | the SLO; saturation where the SLO has no signal for that SO |

Without `mode: enforce`, any list that includes `slo` is observe.

- **Saturation is disabled only by an explicit `enabled: false`** (phase 1).
  Today `AnalyzerEnabled` returns false for an absent name, and `Merge` replaces
  the list whole, so a per-model override written to "add `slo`"
  (`analyzers: [{name: slo}]`) would silently switch the model to sizer mode.
  The composite's exclusion has to test the explicit flag.
- **`shadow: true`** is a generic field the composite needs for any analyzer
  that should run and publish without a vote (phase 1). The SLO's observe mode
  does not depend on it: `slo.mode` decides that.
- **Half-configured is a warning** (built): an `slo:` block on a tier that does
  not list `slo`, or `slo` listed with no targets, is logged from the model's
  resolved policy once per change (`ScalingPolicy.SLOMisconfiguration`), not a
  `Validate` error that would drop the tier. A warning for an SLO tier that a
  variant names but that does not resolve (missing in a namespace-local map, or
  lost in the smallest-name tie) is not built.
- **Malformed is an error** (built): a duration that does not parse, a target
  ≤ 0, a TPOT percentile, or a TTFT percentile outside 50–99. As for every
  field, the whole entry is dropped and `RecordError` names it.
- **The effective-policy log line prints the SLO** (built: the targets, and
  `observe` or `analyzer not listed`; phase 1 adds `guard` or `sizer`).
- **The `slo` block is replaced whole by the overlay** (built), like
  `analyzers:`, so an override cannot silently keep the other objective.
- **Headroom is the tier's `scaleUpThreshold`.** The composite applies the
  model-level `scaleUpThreshold` to `N_com`, so the fleet runs `N ÷ scaleUp`
  replicas: headroom in **replicas**. Because the token rate is concave in
  `k`, the latency margin it buys differs per variant: at `scaleUp = 0.85` a
  replica priced at `k = 0.60` actually runs nearer `0.50`. The analyzer
  publishes the mean ITL predicted at that realised fill (built). Today it
  takes the `slo` entry's own `scaleUpThreshold` when the entry sets one, the
  tier's otherwise (`AnalyzerThresholds`, built). Under the composite, which
  reads only the model-level pair, a per-analyzer threshold has no effect on
  sizing, so it is to be rejected for `slo` with a warning (phase 1).

## Where it lives: an analyzer, combined by the composite

### Why a separate analyzer

- **Observe mode is free on `main`.** The engine already runs every enabled
  analyzer, isolates it (panics recovered in `runRegisteredAnalyzer`),
  publishes its `(D, P)` and discards it.
- **Ownership.** Bucket queries, quantile interpolation, attainment and the
  prefill curve are a body of code the size of the throughput floor.
  [analyzer-structure](analyzer-structure.md) measured the cost of grab-bag
  files in `saturation/`.
- **The contract fits.** [engine-structure](engine-structure.md) says what a new
  analyzer does: demand and per-replica capacity in one unit, per role; mark
  the roles it does not model; register.

### What the SLO analyzer returns (decode and aggregated)

| | Value |
| --- | --- |
| `D[decode]` / `D[both]` | `ArrivalRate × O` output tok/s, plus `QueueSize × O ÷ 60 s` for the router backlog (built). Priced in the same unit, not through `signals/floor`, which works in saturation's per-replica units and already applies `scaleUpThreshold` |
| `P_v` | `min(itl.Sequences(k_v, C_v, KVreq), maxNumSeqs_v) ÷ ITL_v(k_v)`: the token rate at `k_v` with the engine's batch cap. When the cap binds, the cache never fills past `maxNumSeqs·KVreq ÷ C`, so the ITL is read there (built). `C_v` and `maxNumSeqs_v` come from the capacity store |
| unknown `O` | No demand, and every generating SO no-data (built): an output length nobody has reported is undefined, not a demand of zero |
| `k_v` | `clamp(k_tpot_v, 0.15, 0.80)`, the clamp `pricingK` uses (built) |
| prefill SOs | **Unmodeled until step 2b.** No `RoleDemand[prefill]`, so the SO is undefined and saturation decides prefill (`C1` guard, `C2` sizer). Prefill pods' TPOT is meaningless anyway (the P/D sidecar sends `max_tokens=1`) |

### Its own lines

Saturation fits ITL lines for the decode role only, so an aggregated variant
(the common deployment, and llm-d's benchmark) would never be priced from
saturation's state. Both analyzers therefore use **one fitter, `itl.Tracker`**
in `internal/signals/itl` (built), so a fix to its rules reaches both: Ready
rows only, warm-pool rows out, readings above 0.80 out; a full least-squares
fit when the window spans 0.30 of `k`, otherwise `B` pinned and only `A`
fitted. The `slo` analyzer keeps **its own instance**, because saturation's
instance sees decode rows only. Around that instance (built):

- Lines are keyed on the variant's **last resolved accelerator**
  (`stableAccelerator`), so pods disagreeing about it for a cycle do not open a
  fresh, empty window.
- The analyzer's state is **evicted by the engine's per-cycle sweep**
  (`EvictStale`, after 24 h unseen) and **stamped by `notePresentVariants`**, so
  a variant parked at zero keeps its line.
- The analyzer's intercepts are **not in the learned-state snapshot**. After a
  controller restart a line is `pinned-default` until the fleet spans `k`
  again.

The cost is a second window per decode variant, 20 readings.

**The shape is read the same way, not shared.** `O`, the prompt length and the
prefix hit rate are the request-rate-weighted means of the generating
replicas' `AvgOutputTokens`, `AvgInputTokens` and `PrefixCacheHitRate`, through
`shape.New`. Saturation's short-window divisor during a shape change is not
reproduced. `O` scales both `D` and `P`, so it largely cancels in `N`, except
through `KVreq`. (The throughput analyzer's `computeVariantSupply` is not
reused: it hard-codes `DefaultKSat` and has no batch cap.)

### What the composite needs before phase 1

1. **The branch landed**, with the three fixes the engine-structure review
   (2026-09-21) found: its #82 quota-charge regression; the saturation fallback
   skipping the per-SO `Reason` filter; that fallback's guard tested on the real
   `buildComposite`. The rebase also carries the result fields `main` added
   since 2026-09-11. `VariantBusiestQueueLoad` and `RoleStartSeconds` copy
   verbatim. **`HeldRoleDemand` does not**: utilization-share subtracts the
   hold from demand and divides by `P_com`, which is correct only when
   saturation's held `N` was the max. The composite must record the winning
   contributor per SO and carry `HeldRoleDemand[role]` only when saturation won
   every SO of that role.
2. **Saturation disabled only by an explicit `enabled: false`** (above).
3. **`shadow: true`** (above), which must also leave the analyzer out of
   `maxScore`: `Score` feeds `fairShareValue` and the cost-aware optimizer, so a
   shadowed entry with a non-default `score` would still move cross-model
   priority. The same holds for a disabled saturation.
4. **Warm-pool pressure and supply from the composite.** `publishVariantPressure`
   and `publishWarmPoolSupply` read `namedResults[0]` (saturation) in
   `runAnalyzersAndScore`. Pressure is easy: the composite's entries already
   carry `Utilization` on `P_com`. Supply is not: the composite copies
   `WarmPoolPerReplicaCapacity` in saturation's units. Scale it by
   `P_com ÷ P_sat` whenever `N_com > 0`.
5. **Never erase supply on zero demand.** When `D_sat[role] ≤ 0`, the composite
   takes its saturation-PRC fallback instead of `P_com = 0`.
6. **Hysteresis on the deciding source** (bound 6). The C1↔C2 switch happens
   inside `buildComposite`, which keeps no state between cycles; the composite
   needs per-SO memory of the last `P_com` and decision path.
7. **The SLO count is a floor for utilization-share.** In guard mode the
   [utilization-share](utilization-share-optimizer.md) pass must not hand away
   capacity below the SLO's count; that is a composite/share requirement, not
   something the analyzer can enforce.

## Bounds

The tail of an over-estimate is the costliest failure here. Over-pricing `P`
is what breaks TTFT unrecoverably, so `P` is held to rules like k2's.

1. **The fill is clamped to [0.15, 0.80] of physical KV** (built), the range
   the line is fitted over and that `pricingK` already uses. It is not bounded
   by the highest fill observed, which ratchets (see
   [Review findings](#review-findings)).
2. **A line with no information gives no fill.** The slope must pass
   `ValidModel`; otherwise the SO reports no data. Pinned lines are priced in
   phase 0 and labelled (built): `pinned` when the intercept is one this
   variant was measured at, and `pinned-default` when it is the built-in 6 ms
   floor. A pinned line on the default intercept is not a measurement: on a
   large accelerator the real intercept is several times the floor, and the
   slope fitted against it is too steep. In sizer mode `pinned-default` is no
   data (phase 1). Whether `pinned` may price there is open.
3. **A borrowed line never loosens** (phase 1, not built). The `slo` analyzer
   does not consult shared learned state today. If phase 1 lets a variant be
   priced from a sibling's line under `enableLearnedStateReuse`, that is safe in
   guard mode, where the max only tightens; in sizer mode a borrowed line
   reports `ReasonNoData` and the SO falls back to saturation
   ([learned-state sharing](learned-state-sharing-by-similarity.md)'s
   cap-never-order).
4. **Infeasible means the SLO abstains** (built). If `tpotTarget ≤ B + A·0.15`,
   no fill in range meets the target on that variant. The SO reports no data
   and `wva_slo_source` is `infeasible`; it never caps a variant and never
   votes 0. A `slo-infeasible` reason on `wva_model_scaling_blocked`, when every
   decode variant of the model is infeasible, is phase 1. Steering growth away
   from an infeasible accelerator is open question 6.
5. **Undefined is never a number** (built). `A`, `B`, `C`, `KVreq`, `O`, every
   rate and every quantile are checked for NaN/Inf before any clamp, max or
   division (`x <= 0` admits NaN). An invalid input reports `Reason` `error`
   with source `input-invalid`, not `P = 0`.
6. **`P` drops at once and rises slowly** (phase 1). Each step of a whole-node
   replica is a whole 8-GPU node and minutes of start time, and a source that
   flips would flap `N_com` between `C1` and `C2`. `P_slo` may fall
   immediately; it may rise only after the higher value has held for the
   scale-down stabilization window. A switch from SLO-priced to
   saturation-priced is treated the same way, in the composite (item 6 above).

## Before phase 1 prices anything

Phase 0 publishes `P_slo`; phase 1 lets it decide. These estimator errors do
not matter while nothing reads the number, and do once something does. Every
one biases toward over-pricing `P`. All are phase 1 except item 7, a separate
collector fix.

1. **Speculative decoding** (above): fit on ITL per token
   (`rate(vllm:generation_tokens_total) ÷ rate(…inter_token_latency_seconds_count)`),
   not per step.
2. **Measure the per-request KV footprint.** `KVreq = ILeff + O ÷ 2`
   under-counts twice: a prefix hit on a block no running request holds still
   makes the block resident (a hit rate of 0.8 at a 4000-token prompt reads
   1050 tokens against about 4250 resident), and a heavy-tailed `O` pushes the
   time-averaged footprint well past `O ÷ 2`. Measure it per variant:
   `KVreq_obs = k·C ÷ num_requests_running`.
3. **Regress on a windowed `k`.** `k` is an instantaneous gauge and ITL a
   one-minute mean; noise in `k` flattens `A` and pushes `k_tpot` up (with 0.04
   of noise the median estimate sat at the 0.80 clamp against a true 0.73,
   Monte Carlo). Use `avg_over_time(vllm:kv_cache_usage_perc[1m])`. Saturation's
   mu-from-ITL shares this.
4. **A confidence bound on `k`.** Price where the upper one-sided 90 % band of
   `ITL(k)` meets the target; it widens with `(k − k̄)²` and so penalises
   extrapolation without a ratchet. Store `B` only from fits with a small
   standard error.
5. **The window should count cycles, not replica readings.** 20 readings turn
   over in two cycles on ten replicas. Keep one point per replica per cycle and
   weight by time.
6. **An aggregated line is shape-specific.** On a `both` replica ITL at a given
   `k` rises when prompts lengthen. Reset the window on a `shape.Tracker`
   change, or add prefill tok/s as a second regressor.
7. **The batch cap is per engine instance.** A pod collapsed from several
   data-parallel ranks needs the cap times the ranks; a single-frontend
   data-parallel pod exports ranks under an `engine` label the per-pod queries
   fold with `max`. Collector-wide; it affects saturation too.
8. **`max_num_batched_tokens` can bind first.** With speculative decoding the
   cap is `min(max_num_seqs, max_num_batched_tokens ÷ (1 + n_spec))`.

## TTFT (phase 2)

This is the least certain part. TTFT is priced only in shadow first (2b), and
enforced only in a separate step (2c), each behind its own open gate.

**Step 2a is built: the curve's inputs, observed.** Nothing is priced from
them. vLLM only: SGLang publishes no per-stage prefill figure.

- **For every variant that prefills, under a TTFT target:** its mean prefill
  time, `vllm:request_prefill_time_seconds` (first scheduled to first token;
  not a pure service time, since preemption and chunked-prefill turns are in
  it), and its mean engine queue wait, `vllm:request_queue_time_seconds` (the
  first wait only).
- **For a prefill-role variant only:** its prefill utilization, and the learned
  ceiling it is measured against: the p90 over the last hour of per-cycle
  medians of the prefill token rate on replicas that were queued with KV below
  0.80, from at least ten cycles spread over five minutes. The last ceiling is
  kept when the window runs dry. An aggregated variant gets no ceiling: its
  computed-token counter is recorded a whole generation after the prefill, and
  its queue is mostly KV held by decodes.
- **Cost.** The prefill and queue-time replica queries are fetched only in
  namespaces where a model's resolved policy lists the `slo` analyzer, marked
  as the policy is resolved and before the model is collected
  (`noteSLOPolicy`, `AddSLOInputNamespace`), so a cluster that does not use it
  pays nothing and a namespace that does gets them the same cycle.

**The 2b design, not built.** Recorded so 2a's data can test it.

```
TTFT_router  ≈  router queue  +  engine queue  +  prefill        (for means; quantiles do not add)
```

- **TTFT is its own contributor on the prompt-token axis**, not a second cap on
  the KV axis. `D[prefill]` (or `D[both]`'s prefill share) is
  `ArrivalRate × ILeff` input tok/s; `P_v` is the input tok/s per replica at the
  utilization `u_ttft_v` where the TTFT percentile meets the target. A KV-fill
  curve would be confounded by shape.
- **The curve.** Per variant, from engine means: each cycle contributes
  `(u, mean prefill, mean queue)` with `u = PrefillComputedTokenRate ÷
  ceiling_v`; points in bins 0.05 wide with an isotonic fit; a bin counts only
  with ≥ 20 cycles of effective sample size and is read at its one-sided 90 %
  upper bound.
- **The tail.** The percentile is mapped from the mean by the router's own
  ratio `q_p ÷ mean`, binned by `u` (it climbs from about 1 toward ln 10 ≈ 2.3
  at p90 as queueing dominates), measured only in windows with ≥
  `max(50, 10 ÷ (1 − p))` requests, kept as the 80th percentile of per-window
  ratios.
- **Queue as zero** below the bin where mean engine queue is under 5 % of the
  target: the engine's first queue bucket is 0.3 s.
- **Blame.** A TTFT miss with decode at its KV ceiling, or decode's queue above
  zero, is decode's: prefill queues on decode back-pressure (we measured 439
  queued with 3 of 4 prefill pods idle).
- **P/D transfer.** `vllm:nixl_xfer_time_seconds` is not collected; until it
  is, the transfer is inside the router's tail ratio (open: per request or per
  chunk).
- **SGLang** has no `PrefillComputedTokenRate`; there the TTFT contributor
  abstains.
- **The transient is out of scope.** A step in arrival lands before a new
  replica is Ready whatever fill the fleet sat at. Start time, the warm pool and
  [in-flight sizing](sizing-against-in-flight-capacity.md) are the levers.

## Attainment

From the router, per model and objective, over 5 minutes. All built.

- **The query.** `sum by (model_name, target_model_name, le)(rate(..._bucket{streaming="true"}[5m]))`:
  rate first, then sum, so a counter reset on one EPP replica is handled. Only
  the current `llm_d_epp_*` names are read; the older
  `inference_objective_request_ttft_seconds` belonged to the predictor plugin,
  ran on the post-admission clock, and was removed in llm-d-router #2821.
- **Monotone buckets.** Summed cumulative buckets are repaired in Go, as
  `histogram_quantile` repairs them.
- **TTFT attainment** = `bucket(le=target) ÷ count`, exact when the target is a
  bucket bound (3 s is), interpolated otherwise. Sampling error at the publish
  minimum is about ±3 points at p90 (n = 100) and ±0.3 at p99 (n = 1000).
- **TPOT** is the router's **mean**, `rate(_sum) ÷ rate(_count)`, against the
  target. A mean is met or missed; it is not a share.
- **No data instead of a guess.** Fewer than `max(50, 10 ÷ (1 − p))` requests
  means no attainment (100 at p90, 1000 at p99); the metric stays absent rather
  than reading 0 or 1. On a 1–3-replica whole-node fleet, p99 is usually not
  measurable in 5 minutes. A quantile in the `+Inf` bucket is published as
  `+Inf`, not the top finite bound.
- **It counts survivors.** Requests still queued, shed or abandoned are missing
  from both the count and the misses, so attainment reads high under overload.
  Phase 0 publishes the request count it was judged on
  (`wva_slo_window_requests`). Before anything trims on attainment (phase 3),
  the router's backlog older than the target and its rejected requests
  (`llm_d_epp_request_error_total`) are counted as misses.
- **Comparable with llm-d's score only with care.** llm-d scores from
  `slo_violation_total`, post-admission. This is judged on the router's own
  clock, which includes the flow-control queue, so with flow control on it is
  strictly harsher.

### Transient exclusion

A window is not judged while it may hold requests that waited out capacity no
steady-state sizing could have had ready. `wva_slo_excluded{objective,reason}`
says why; the request count is still published. Reasons:

- **`zero-replicas`** (waking): no ready replica in the set, and some replica
  **starting**. A set at zero whose only Pods are stuck (cannot be scheduled)
  is not waking: that shortfall is a miss and is judged.
- **`starting`**: a not-Ready Pod younger than 30 minutes. An older one was
  Ready and failed its probe: a flap, not a start. A LeaderWorkerSet group
  lists no Pod ages, so a group pending **continuously for 30 minutes** stops
  counting as starting (`boundPending`); an unschedulable group is judged.
- **`unobserved`**: the analyzer did not analyze the model on the previous
  engine cycle. The engine skipped it (for example a waking model whose Pods
  have no metrics yet), or the controller restarted, or the model was just
  configured. The window may hold a transient nothing here saw, so it is
  excluded for window + grace from the first cycle analyzed. Counted in cycles
  (the engine calls `BeginCycle` at the top of every cycle), not read from
  timing: a slow cycle is not a skipped one, and a timing rule excluded a model
  with a recurring slow cycle for ever.

The exclusion lasts for the router window plus a **grace**: the longer of
120 s and 1.5 × the longest mean request duration among ready replicas, **at
most 30 minutes**, because a request queued through a wake is recorded only when
its whole generation ends. A rise is not a reason of its own: its new replicas
are `starting` until they serve, and excluding it after that would drop the
windows in which the new sizing is tested. Per objective: TTFT is excluded on a
transient of any variant (a decode short of capacity back-pressures prefill),
TPOT only on one of a decode or aggregated variant. One scale-up leaves an
objective unjudged for about the start time plus 7 minutes, so a fleet that
scales more often is never judged; `WVAModelSLOUnjudged` says so.

## Observability

Labels follow the shipped `wva_*` convention: `namespace`, `model_name`,
`variant_name`. Prometheus scrapes the model namespace as `exported_namespace`.
All metrics below are built.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `wva_slo_target_seconds` | namespace, model_name, objective, statistic (`mean`/`p90`…) | The declared target |
| `wva_slo_observed_seconds` | namespace, model_name, objective, statistic | The router's mean (TPOT) or percentile (TTFT) over 5 m; absent when too few requests or excluded |
| `wva_slo_attainment_ratio` | namespace, model_name, objective, statistic | The TTFT share within target; absent below the sample minimum or excluded |
| `wva_slo_window_requests` | namespace, model_name, objective, statistic | Completed requests recorded for the objective over the window, so "too few" reads apart from "nothing reported" |
| `wva_slo_excluded` | namespace, model_name, objective, reason | 0/1 per reason (`zero-replicas`, `starting`, `unobserved`): why the window is not judged this cycle |
| `wva_slo_fill` | namespace, model_name, variant_name, objective | The fill `k_v` behind `P_slo`, physical KV fraction |
| `wva_slo_predicted_itl_seconds` | namespace, model_name, variant_name | The mean ITL the line predicts at the **realised** fill (`k_v` after the scale-up headroom) |
| `wva_slo_source` | namespace, model_name, variant_name, source | 0/1 per source: `own`, `pinned`, `pinned-default`, `no-line`, `no-output-length`, `infeasible`, `input-invalid`. Phase 1 may add `borrowed` (bound 3) |
| `wva_slo_prefill_seconds` | namespace, model_name, variant_name | Step 2a: mean prefill time of a variant that prefills |
| `wva_slo_engine_queue_seconds` | namespace, model_name, variant_name | Step 2a: mean engine queue wait of a variant that prefills |
| `wva_slo_prefill_utilization` | namespace, model_name, variant_name | Step 2a: prefill token rate per replica over the learned ceiling; prefill role only |
| `wva_slo_prefill_ceiling_tokens_per_second` | namespace, model_name, variant_name | Step 2a: the learned prefill ceiling; prefill role only |

Alerts (built, with promtool tests):

- **`WVAModelSLOMissed`** (warning): over `[30m:1m]`, misses ÷ judged minutes
  ≥ 0.5 **and** misses ≥ 5, for 2m. Counted over judged minutes only, so an
  exclusion neither fires nor resets it. Gated on the analyzer still publishing
  and on the model having replicas. On this release it says the tier's
  thresholds do not meet the SLO, not that the autoscaler failed one.
- **`WVAModelSLOUnjudged`** (info): a target present for the whole last hour
  (`count_over_time(...[1h:1m]) >= 60`), nothing judged in it, replicas > 0,
  for 15m, **and a cause to act on**: excluded for at least half the hour, the
  objective's router series missing, or TPOT with no requests while TTFT has
  them (no `include_usage`). Too little traffic -- a quiet or idle model, which
  publishes a request count of 0 -- does not fire it.
- Both alerts carry `component: slo`, so an observe-only finding can be routed
  apart from pages.

- **Stale series are deleted** when a model, variant or target disappears
  (built, the per-cycle `SweepSLO`).
- `wva_analyzer_demand` / `wva_analyzer_target{analyzer_name="slo"}` already
  publish the SLO's `(D, P)`: for a single generating variant,
  `D ÷ (P × scaleUpThreshold)` beside the actual count is "what the SLO would
  want" (filter `> 0`; an unpriced variant's target is 0).
- **Not published:** the line's ITL at the **current** fleet fill (the phase-0
  follow-up below); combined attainment, which two histograms cannot give; a
  cross-check against `llm_d_epp_request_slo_violation_total` labelled
  `clock="post-admission"` (not built); the composite's decision path, which
  belongs to the composite.

## Phases

### Phase 0: the analyzer, observing (built)

Everything under **Built** at the top, documented in
`docs/reference/prometheus.md` and `docs/reference/scaling-policy.md`. The
analyzer is registered by `NewEngine` beside saturation and runs only for
models whose policy lists it, so enabling it needs no restart. **No change to
the decision path**: its result is published and discarded. It answers "does
my tier meet my SLO, and what would the SLO want?", which nothing answers
today. The dormant `ServiceClass` / `FindModelSLO` /
`configmap-serviceclass.yaml` are left for a separate change (a repo-wide check
and the maintainer's agreement).

**Phase-0 follow-up (not built):** publish the line's ITL at the current fleet
fill, or `A` and `B`, so the phase-1 gate can compare predicted with observed.

### Phase 1: TPOT enforce

- Needs the composite and the seven items under
  [What the composite needs](#what-the-composite-needs-before-phase-1), and the
  eight under [Before phase 1 prices anything](#before-phase-1-prices-anything).
- `slo.mode: enforce`, then guard and sizer.
- The rise-slowly rule (bound 6).
- **Gate:** the engineering items above, plus the phase-0 data gate (open
  question 1).

### Phase 2: TTFT

- **2a (built):** the curve's inputs and the ceiling state, published.
- **2b:** the prefill-token contributor, the isotonic curve, the tail ratio and
  the blame rule, **in shadow**. Gate: 2a's data answers the
  [2a questions](#open-questions) and meets a fit criterion (open question 1).
- **2c: TTFT enforce.** The TTFT contributor votes under `mode: enforce`. Gate:
  open; at least 2b's shadow predictions tracking observed TTFT attainment.

### Phase 3: the attainment trim

`P` is open-loop: a model of latency, trusted. Phase 3 adds a slow per-variant
trim `trim_v ∈ [0.5, 1.0]` on the priced fill, moved down when the upper
confidence bound of attainment is below target and up when the lower bound has
been above it for a sustained period. It must use bounds that account for
bursty misses, step symmetrically, count the misses the router never records,
and freeze when the SO is not SLO-decided, at a ceiling, quota or hold, in
excluded windows, and until the fleet is Ready after a change. This is not
llm-d's control law, which reacts within a minute; the trim corrects a model
that drifted, over tens of minutes. **Designed later**, after phase 1 data.

### Later: a TPOT percentile

llm-d scores p90 TPOT. A percentile needs a tail ratio
`ρ = q_p(per-request TPOT) ÷ mean(ITL)` from
`vllm:request_time_per_output_token_seconds` (vLLM ≥ 0.11), with a minimum
count, the conservative end of the bracketing bucket (`k` swings 2× inside
one), `ρ` measured near the `k` it is applied at, a high quantile of
per-window `ρ` rather than a rolling max, and a reset on a shape change.
Deferred until a user asks for it.

## Tests

**Unit, in the `slo` package** (built):

- Pricing: `k_tpot` and `P_v` against a hand computation, the batch cap, the
  clamp at both ends, unknown output length, `pinned-default`.
- No-data paths: no line, infeasible, invalid input, NaN/Inf in each input,
  error versus no-data; rows that must not feed a line.
- The histogram against `histogram_quantile`: a target on a bound, monotone
  repair, `+Inf`, the sample minimum, an unparsable bound. Router series
  resolved to their model; a failed router result.
- The transient exclusion per reason and objective, including stuck Pods,
  pending LWS groups and unobserved gaps.
- What `Analyze` publishes, and series deleted when a model or variant goes.
- Engine wiring: router source, query registration, the per-cycle sweep,
  presence stamping, the misconfiguration report.
- The two alerts, with promtool rule tests. Each suite also has negative
  controls (mutations of the guarded code that must fail it).

**In the composite (phase 1):** guard is `max(N_sat, N_slo)` per SO; sizer
gives `C1-single` or `C2-sat-fallback`; saturation absent from an override
stays enabled; a shadowed analyzer changes no `N_com`; the two edges
(`D_sat = 0`, `N_slo = 0`); `N` alternating 2 ↔ 3 is held by the rise-slowly
rule; a prefill SO stays saturation-decided; and the closed loop of bound 1 (a
loose SLO in guard mode never exceeds saturation's `N`).

**e2e** (not built): `slo` without `mode: enforce` changes no desired count, and on
the simulator's constant latency every decode SO is `no-line`. A spec where the
SLO decides needs latency that rises with load (open question 2).

**Benchmark.** The G1 replication in
[keda-recipe-comparison](https://github.com/ev-shindin/llm-scaling-manager/blob/bench/keda-recipe-arm/docs/plans/benchmark/keda-recipe-comparison.md)
(branch `bench/keda-recipe-arm`) needs the attainment scorer it lists as to-do.
That scorer and `wva_slo_attainment_ratio` cross-check each other.

- **Phase 0:** run ours with `slo` in observe mode, and record the SLO's `D ÷ P`
  beside the actual count.
- **Phase 1:** add **ours-slo**, sizer mode, mean TPOT 100 ms. Its attainment
  is judged on the router's clock, so it is not directly comparable with
  llm-d's published 95.8 % (post-admission). Registered before any run: TPOT
  does not bind on that load (a 32B replica at 0.68 holds a batch of about 30),
  so ours-slo ties ours until TTFT is enforced. **A phase-1 tie is the expected
  result, not a failure.** The phase-1 case is a TPOT-bound load, such as long
  outputs on a small accelerator, and it needs its own run.

## Alternatives considered

- **SLO logic inside saturation.** Puts histogram handling in a package already
  measured as too big, and loses the free shadow mode and the isolation.
- **Hand saturation a per-variant fill** (an earlier draft). Needs an engine
  ordering change and its own fallback and "who decided" reporting; the
  composite gives those.
- **Combining by rescaling saturation's capacity.** Under-orders when
  loosening, by `ITL(k_slo) ÷ ITL(k_static)`.
- **Plain HPA/KEDA on a latency percentile** (llm-d's slo-aware recipe is the
  refined form). It needs no controller and takes the target directly, but it
  is reactive by design, has no variant choice, does not yet handle P/D, and a
  proportional law on a percentile oscillates around it. Phase 3 keeps
  correction from measured latency, at a slower time scale.
- **Dynamo Planner's SLA planner.** It sizes from an offline profile of
  throughput against TTFT/ITL per configuration, plus a load forecast. A
  profile goes stale when the model, accelerator or request shape changes,
  which is the hand-maintenance this proposal removes, and it is one more
  artefact per variant and accelerator. Here the line is learned online from
  traffic the fleet already serves. Seeding a line from a profile, before the
  fleet spans `k`, is a possible later step.
- **WVA's original queueing-model / `ServiceClass` sizing.** It needs
  per-model, per-accelerator performance parameters supplied ahead of time and
  kept current by hand, and its surface is wired to nothing today. Reviving it
  would bring back that maintenance; the ITL line this proposal inverts is
  already fitted online.
- **The predictor's histograms.** They need the plugin, and run on a clock that
  excludes the router queue.
- **Static presets per SLO.** That is today's *Strict* preset.

## Open questions

1. **Phase gates (open).** Proposed measurable forms, with N, X, Y and the bin
   minimum to be chosen from shadow data:
   - **Phase 1:** over ≥ N hours of shadow on ≥ 2 variants or accelerators,
     predicted-vs-observed mean TPOT error within X %; `pinned`-vs-`own`
     disagreement on the same variant below Y; and a TPOT-bound benchmark
     showing attainment ≥ target with replicas no higher than saturation-only.
     The error is measured **out of sample**: the prediction one cycle makes,
     scored at the fill the fleet reaches later, bucketed by how far the fill
     moved. A line pinned through a fixed intercept passes near the window's
     own mean point by construction, so an in-sample error tests almost
     nothing. Needs the phase-0 follow-up (the line's ITL, or A and B, at the
     current fill).
   - **2b:** a minimum number of effective cycles per bin, plus a monotonicity
     or fit-residual criterion on the curve.
   - **2c:** to be set from 2b's shadow results.

   **Phase 1 does not ship** while any of these holds: the out-of-sample TPOT
   error is outside X %; in guard mode on a loose target the SLO adds replicas
   saturation did not ask for; no TPOT-bound benchmark has been run and scored;
   or any of "Before phase 1 prices anything" items 1-4 is open.
2. **The simulator's load factor.** We pass none today, and whether v0.9.0 has
   one is unchecked. Without it no e2e spec can have the SLO decide.
3. **Pinned pricing.** Whether `pinned` may price in sizer mode, decided from
   how often `pinned` and `own` disagree on the same variant.
4. **`vllm:nixl_xfer_time_seconds`**: per request or per chunk? Must be checked
   before it enters any TTFT budget.
5. **What 2a's data must answer before 2b prices anything.** The x-axis
   (served utilization pins at 1 under overload, so plot offered prompt load
   `ArrivalRate × ILeff` against the ceiling, or cut where the queue starts
   growing); per-replica points, not variant means (convexity biases pooled
   means up); absolute tokens/s normalised once, not by a ceiling that moves
   every cycle; a ceiling keyed by prompt-length bucket; a defined effective
   sample size (overlapping 1 m and 5 m windows); a saturation gate better than
   the 1-minute peak of the waiting gauge; preemptions flagged per point; and a
   check that router mean TTFT ≈ engine queue + prefill, or the residual (EPP
   queue, sidecar, KV transfer) hides in the tail ratio.
6. **A per-SO growth cap in the composite**, to steer growth away from an
   SLO-infeasible accelerator without ever capping below saturation's `N`. A
   hold (engine-structure plans per-role holds), or a composite rule?
7. **`ArrivalRate` on a pool serving several models** is refused today
   (`count == 1`). Should the SLO analyzer abstain there, or use
   `target_model_name` alone?
8. **Multi-pod and data-parallel replicas.** `pod_collapse.go` merges ranks
   that run their own API server; a single-frontend data-parallel pod's ranks
   are folded with `max` (see "Before phase 1", item 7). For LWS, worker pods
   are dropped, so a group is its leader's row: right for tensor parallelism,
   wrong where data-parallel ranks span nodes. The 2a prefill means must follow
   whatever rule fixes this.
9. **The docs to change with phase 1** in `docs/reference/scaling-policy.md`:
   `enabled` is no longer "reserved"; "saturation drives every decision" is no
   longer true; *Strict* stops being the low-latency SLO answer;
   `queueLengthThreshold` has no effect on an SLO-decided SO.

## Review findings

The design was reviewed against the code, the math and the operator's view;
these are the substantive results, and the design above incorporates them.

**1. A fill bound on the observed maximum ratcheted the fleet.** The first draft
bounded `k ≤ k_max_observed + 0.05`. The fleet runs `N ÷ scaleUp` replicas and
the token rate is concave, so the realised fill sits below the priced one; each
window's maximum then pulled the bound down, to a fixed point near
`0.05·s ÷ (1 − s)`: 0.28 at `s = 0.85`. In sizer mode the SLO could never
loosen; in guard mode a loose SLO kept adding replicas. Replaced by the
[0.15, 0.80] clamp; a closed-loop test (phase 1, in the composite) asserts
a loose SLO never raises `N` above saturation's in guard mode.

**2. Holding an infeasible variant froze single-variant P/D models.** The first
draft held an infeasible variant at its current count. That freezes a
single-variant P/D deployment (most real ones) exactly when load rises, and
holds a parked variant at 0 forever. Replaced by abstaining (bound 4).

**3. A TTFT budget subtracted quantiles.** The first draft's
`target − q_p(queue)` budget was wrong: the decomposition holds for means, and
quantiles do not add. Replaced by a mean curve and a tail ratio.

**4. The trim was a one-way ratchet.** The first draft's `p + 1 pp` up-band
could never be crossed at p99 (it needs a lower bound over 100 %); its dead
band was set by a fixed `n = 200`, not by the SLO; point estimates mis-trim (at
n = 300 a true 90.5 % reads under 90 % about 40 % of the time); bursty misses
at a design effect of 5 raise the false trim-down rate per test at p90 from
2.7 % to 17.6 %; and it fell 7.5× faster than it recovered. Phase 3 is therefore
stated as requirements and designed later.

## Appendix: the composite rules

These rules belong to the composite analyzer, not to this proposal; this is a
summary of what the SLO design relies on. The source is the `composite-analyzer`
branch of [deanlorenz/llm-scaler](https://github.com/deanlorenz/llm-scaler/tree/composite-analyzer)
(remote `deanscaler`; its spec §5.1). It is not merged.

```
N_i(SO)      = D_i[role(SO)] ÷ P_i(SO)                each eligible analyzer, in its own units, no threshold
N_com(SO)    = max over contributors of N_i(SO)        undefined skipped; P ≤ 0 or Reason no-data/error is undefined
               saturation contributes unless disabled; it is the fallback (C2) only when disabled and nothing else voted
P_com(SO)    = D_sat[role] ÷ N_com(SO)                 back in saturation's units, by definition
RC, SC       = applyUniversalThreshold on P_com, with the model-level scaleUp / scaleDown
```

- **Each count is in its analyzer's own units.** `P_slo` is the token rate at
  the SLO's fill, priced from the line, not a rescale of saturation's capacity.
- **Guard and sizer fall out of the rules.** With saturation eligible, the max
  lets the SLO only tighten. With saturation disabled, spec §5.1 applies:
  *"saturation is a fallback, not a floor ... the composite may legitimately
  come out below sat alone"*.
- **The decision path.** An SO the SLO cannot price reports no data: in sizer
  mode it lands on `C2-sat-fallback`, in guard mode on `C1-single` with
  saturation as the contributor. Where the SLO priced it, it is `C1-single`
  (sizer) or `C0-agree` (guard). `C0` lists every contributor, not the winner;
  the composite needs to record the winner (item 1 of what it needs).
- **Two edges.** If `D_sat[role] = 0` while `N_slo > 0`, then `P_com = 0`,
  which zeroes the SO's supply; the composite must fall back to saturation's
  PRC whenever `D_sat[role] ≤ 0`. If the SLO votes a defined `N = 0`, the
  branch falls back to saturation's own PRC for that SO; in sizer mode that is
  saturation's sizing for an idle variant, the right answer, so it is asserted
  rather than changed.
