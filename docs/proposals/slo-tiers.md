# SLO tiers: size to a TTFT/TPOT target, not only to a fill threshold

**Status:** proposal, 2026-10-14. Revised against the router's metrics, the
engine's analyzer loop, the composite on `deanscaler/composite-analyzer`, and
three adversarial reviews (the code claims, the math, the operator), whose
findings are folded in below. Phase 0 is being implemented; nothing else is
built. Tracks
[#141](https://github.com/ev-shindin/llm-scaling-manager/issues/141).

## The problem

A tier says how full a replica may run. It does not say how fast a request
must be served. An operator with a latency SLO has to turn it into
`kvCacheThreshold` and `scaleUpThreshold` by hand, and redo that whenever the
model, the accelerator or the request shape changes. The documented *Strict
(low latency SLO)* preset (`kvCacheThreshold 0.70`, `queueLengthThreshold 3`)
is one such guess, made once for every model.

llm-d's SLO-aware recipe takes the target directly. Its benchmark reports
**95.8 % combined attainment at 5.93 average replicas, against a static pool of
8**:

- GKE, H100, Qwen3-32B at TP=2
- TTFT ≤ 3 s and TPOT ≤ 100 ms at p90
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

## The two objectives are different statistics

- **TPOT is a mean.** It is the average time per output token. That is the
  same quantity the ITL line `ITL(k) = A·k + B` is already fitted on (the
  token-weighted mean of `vllm:inter_token_latency_seconds`, per variant). A
  mean-TPOT target therefore **inverts the existing line directly**, with no
  tail model:

  ```
  k_tpot = (tpotTarget − B) ÷ A
  ```

- **TTFT is a percentile.** It is dominated by queueing, it has a long tail,
  and no existing model of it survived (see [TTFT](#ttft-phase-2)). It is
  measured as a percentile from the router's histogram and is the harder half.

A TPOT **percentile** (llm-d scores p90 TPOT) is an optional later extension;
it needs a tail model of its own and is listed under
[Later](#later-a-tpot-percentile).

## The design in one paragraph

A new **`slo` analyzer**, separate from saturation, owns everything about
latency:

- the router's and the engines' latency series
- each variant's fill at the target
- attainment

It returns an ordinary `(D, P)` per role. For decode, that is demand in output
tokens/s and, per variant, the tokens/s one replica sustains **at the fill
where its mean ITL equals the TPOT target**. The **composite** on
`deanscaler/composite-analyzer` combines it with saturation: per ScaledObject,
`N = D ÷ P` from each eligible analyzer, the max, and saturation as the
fallback where nothing else has a signal. A tier chooses one of two modes:

- **Guard.** Both analyzers vote, and the SLO can only add replicas.
- **Sizer.** Saturation is explicitly disabled; the SLO decides, and saturation
  is the per-variant fallback.

Before the composite lands, the analyzer runs in observe mode on today's
`main` with no engine change.

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

### The interval each metric covers

| Interval | Router (EPP) | Engine (vLLM) | Client |
| --- | --- | --- | --- |
| proxy before the EPP's ext_proc | — | — | yes |
| **router flow-control queue** | yes | — | yes |
| tokenization + **engine scheduler queue** | yes | yes | yes |
| prefill | yes | yes | yes |

- **Router:** `llm_d_epp_request_ttft_seconds`. Its clock starts at
  `HandleRequestHeaders`, before admission. It is always emitted.
- **Engine:** `vllm:time_to_first_token_seconds`. Its clock starts at frontend
  arrival.
- **Client:** inference-perf / guidellm.

**An end-to-end TTFT that includes queueing exists, and it is the router's.**
`llm_d_epp_request_ttft_seconds` covers the flow-control queue, scheduling, the
engine queue and prefill. Only the proxy hop before ext_proc is missing.

Two catches:

- **Non-streaming requests.** Their TTFT equals the whole request duration.
  Select `streaming="true"`.
- **The predictor plugin uses a different clock.** Its histograms and
  `llm_d_epp_request_slo_violation_total` start in `PreRequest`, **after**
  admission, so they exclude the router queue. They are also recorded at end of
  stream. Neither is a sizing input here.

### Metrics this design reads

| Metric | Labels | Buckets at the targets | Used for |
| --- | --- | --- | --- |
| `llm_d_epp_request_ttft_seconds` | `model_name, target_model_name, fairness_id, priority, streaming` (no pool, no namespace; scrape-target `namespace`/`service`) | 3 s and 0.1 s are bounds | Per-model TTFT percentile and attainment, **the number the SLO is judged on** |
| `llm_d_epp_request_streaming_tpot_seconds` | as above | 0.1 is a bound | Per-model mean TPOT as the router sees it |
| `vllm:inter_token_latency_seconds` | per pod | — | Already collected as a 1 m mean; it is what the ITL line is fitted on |
| `vllm:request_prefill_time_seconds`, `vllm:request_queue_time_seconds` | per pod | coarse (0.3 … 2.5, 5) | Phase 2: per-variant prefill time without queue, and engine queue wait, **as means** |
| `vllm:time_to_first_token_seconds` | per pod | no 3 s bound | Diagnostic only, as today |

The router's request metrics have **no pool or namespace label** (#2309). The
arrival-rate query already scopes by the scrape target's `namespace` and joins
by `target_model_name`. The `slo` analyzer reuses that pattern, including the
`count == 1` guard that refuses a pool serving several models.

**The split:** the router's histograms say whether the SLO is met, per model.
The engines' series say which variant to price, and how full it may run.

## What exists today

From `main` at `cb37a21e2`:

| | Where | What it means here |
| --- | --- | --- |
| A tier is a ConfigMap entry with no `model_id` | `config/scaling_policy.go:41` (`PolicyEntryKey`); fields on `config.ScalingPolicy`, `config/saturation_scaling.go:16` | The issue's `workloadClasses` is only the name of the [well-lit path](../well-lit-paths/workload-classes/); the key does not exist |
| A tier resolves per **model** | `steadystate/engine.go:998` (`modelPolicy`); variants that disagree get the lexicographically smallest tier | The target is per model, and the fill is per variant |
| `analyzers:` is replaced whole by an overlay, and an absent name is disabled | `Merge` (`saturation_scaling.go:547`); `AnalyzerEnabled` (`:793`) returns false when the name is not listed | Dangerous for mode selection; see [Configuration](#configuration) |
| A `Validate` error drops the **whole tier entry** | `controller/configmap_helpers.go:56-61` | A bad `slo` block drops the tier, not one key |
| Every enabled analyzer runs; **only saturation's result is used** | `engine_v2.go:174-199`; `:982` `CompositeSignal: namedResults[0]` | `namedResults[1:]` reach only `recordAnalyzerMetrics` (`wva_analyzer_*`), liveness bookkeeping and the cycle log. Observe mode is free |
| Saturation runs first in the cycle | `engine_v2.go:146-150` | The `slo` analyzer can read **this** cycle's ITL line |
| Analyzers are registered once, before the loop starts | `cmd/main.go:673`; throughput only if config listed it at boot | The `slo` analyzer is registered unconditionally, and per-model enablement gates it at no cost (below) |
| The ITL line is saturation's private state | `itlWindows` / `itlLines` on `SaturationAnalyzer` (`analyzer.go:85-117`) | Needs a read accessor |
| `TokenRate` has no batch cap | `itl.TokenRate(m, k, kvMax, kvReq)` (`signals/itl/rate.go:40`); saturation caps at `max_num_seqs` before dividing (`mu_from_itl.go:186-190`), with C from `capacityTokensFor(params, …)` | The `slo` analyzer needs the capacity store's engine params too |
| The line's fitted range is [0.15, 0.80] of physical KV | `itl.DefaultMinObservableK` / `DefaultMaxObservableK` (`window.go`); `pricingK` clamps to it (`mu_from_itl.go:176`) | The SLO's fill uses the same clamp |
| No percentile is computed anywhere in Go | Engine latency is read as `rate(_sum)/rate(_count)` means only; router latency appears only in `operational-dashboard.json` | Bucket queries are new |
| `AvgTTFT` is diagnostic by decision | `domain/replica_metrics.go:222` | A `TTFT(T) = A·T + B` fit was built and removed: queue wait landed in the intercept (82 s) |
| A dormant SLO surface exists | `domain.ServiceClass`, `utils.FindModelSLO` (no callers), `deploy/configmap-serviceclass.yaml` | Inherited from WVA, wired to nothing |
| Latency in tests | No e2e spec asserts latency; the simulator runs constant `--time-to-first-token` / `--inter-token-latency`, and many suites pass `--fake-metrics` | An e2e can exercise only the fallback |
| Latency in the benchmark | Client-side TTFT/TPOT (`postprocess.py`, `phase_ttft.py` on `bench/keda-recipe-arm`) | No attainment scorer exists yet |

## Configuration

```yaml
data:
  interactive: |
    analyzers:
      - name: saturation            # listed explicitly: guard mode
      - name: slo
    slo:
      tpot: {target: 100ms}                  # a mean
      ttft: {target: 3s, percentile: 90}     # a percentile (phase 2 enforces it)
```

**The mode is the `analyzers:` list, with one rule changed.**

| `analyzers:` | Mode | What decides |
| --- | --- | --- |
| `slo` with `shadow: true` (and `saturation`) | observe | saturation alone; the SLO's view is published |
| `saturation` + `slo` | guard | per SO, the larger of the two replica counts |
| `saturation {enabled: false}` + `slo` | sizer | the SLO; saturation where the SLO has no signal for that SO |

- **The changed rule: saturation is disabled only by an explicit
  `enabled: false`.** Being absent from the list must not disable it. Today
  `AnalyzerEnabled` returns false for an absent name, and `Merge` replaces the
  list whole. On the composite branch, a per-model override written to "add
  `slo`" (`analyzers: [{name: slo}]`) would therefore silently switch the
  model to sizer mode. The composite's exclusion has to test the explicit flag.
- **`shadow: true`** is a new, generic field on the analyzer entry. The analyzer
  runs and publishes, and it is left out of the composite's eligible set (the
  same exclusion `excludeByName` applies to a disabled saturation). On today's
  `main` every non-saturation analyzer is already in that position, and the
  field is accepted and changes nothing.
- **Misconfiguration is reported, not silent.**
  - An `slo:` block on a tier that does not list `slo`, an `slo` entry with no
    targets, or an SLO tier that a variant names but that does not resolve
    (missing in a namespace-local map, or lost to another tier in the
    smallest-name tie) are logged as warnings with the tier and model named.
    They are not `Validate` errors, which would drop the whole tier.
  - Malformed values **are** `Validate` errors: a duration that does not parse,
    a target ≤ 0, or a percentile outside 50–99. As for every field, the whole
    entry is dropped and `RecordError` names it.
- **The effective-policy log line prints the resolved mode** (`observe`,
  `guard` or `sizer`) and the targets per model.
- **The `slo` block is replaced whole by the overlay**, like `analyzers:`. A
  per-model override cannot silently keep the other objective.
- **Headroom is the tier's `scaleUpThreshold`.** The composite applies the
  model-level `scaleUpThreshold` to `N_com`, so the fleet runs `N ÷ scaleUp`
  replicas. That is headroom in **replicas**. Because the token rate is concave
  in `k`, the latency margin it buys differs per variant: at `scaleUp = 0.85` a
  replica priced at `k = 0.60` actually runs nearer `0.50`. The analyzer
  publishes the mean ITL predicted at that realised fill, so the margin is
  visible. A per-analyzer `analyzers[slo].scaleUpThreshold` has **no effect**
  under the composite, which reads only the model-level pair, so it is rejected
  for `slo` with a warning.

## Where it lives: an analyzer, combined by the composite

### Why a separate analyzer

- **Observe mode is free on `main`.** The engine already runs every enabled
  analyzer, isolates it (panics recovered, `engine_v2.go:672`), publishes its
  `(D, P)` and discards it.
- **Ownership.** Bucket queries, quantile interpolation, attainment and, in
  phase 2, the prefill curve are a body of code the size of the throughput
  floor. [analyzer-structure](analyzer-structure.md) measured the cost of
  grab-bag files in `saturation/`.
- **The contract fits.** [engine-structure](engine-structure.md) says what a new
  analyzer does: demand and per-replica capacity in one unit, per role; mark
  the roles it does not model; register.

### How the results combine: the composite

The rules, as built on the branch:

```
N_i(SO)      = D_i[role(SO)] ÷ P_i(SO)                each eligible analyzer, in its own units, no threshold
N_com(SO)    = max over contributors of N_i(SO)        undefined skipped; P ≤ 0 or Reason no-data/error is undefined
               saturation contributes unless disabled; it is the fallback (C2) only when disabled and nothing else voted
P_com(SO)    = D_sat[role] ÷ N_com(SO)                 back in saturation's units, by definition
RC, SC       = applyUniversalThreshold on P_com, with the model-level scaleUp / scaleDown
```

- **The SLO's count is computed in its own units.** `P_slo` is the token rate
  at the SLO's fill, priced from the line, not a rescale of saturation's
  capacity. A rescale would under-order when loosening by
  `ITL(k_slo) ÷ ITL(k_static)`.
- **Guard and sizer fall out of the rules.** With saturation eligible, the max
  lets the SLO only tighten. With saturation disabled, the branch's spec §5.1
  applies: *"saturation is a fallback, not a floor ... the composite may
  legitimately come out below sat alone"*.
- **The decision path says who decided.** An SO the SLO cannot price reports
  `ReasonNoData`.
  - In sizer mode it lands on `C2-sat-fallback`.
  - In guard mode it is `C1-single` with saturation as the contributor.
  - Where the SLO priced it, it is `C1-single` (sizer) or `C0-agree` (guard).
- **Two edges, both tested.**
  - If `D_sat[role] = 0` while `N_slo > 0`, then `P_com = 0` and the SLO cannot
    order. The SLO's demand comes from the same `ArrivalRate`, so they agree
    except across a scrape gap.
  - If the SLO votes a defined `N = 0` (no demand), the branch falls back to
    saturation's own PRC for that SO. In sizer mode that is a silent return to
    saturation's sizing for an idle variant, which is the right answer, so it
    is asserted rather than changed.

### What the SLO analyzer returns (phase 1: decode and aggregated)

| | Value |
| --- | --- |
| `D[decode]` / `D[both]` | `ArrivalRate × O` output tok/s, plus `QueueSize × O ÷ drainSeconds` for the router backlog. This is priced in the same unit, not through `signals/floor`, which works in saturation's per-replica units and already applies `scaleUpThreshold` |
| `P_v` | `min(itl.Sequences(k_v, C_v, KVreq), maxNumSeqs_v) ÷ ITL_v(k_v)`: the token rate at `k_v`, with the engine's batch cap, exactly as `deriveMu` caps it. `C_v` and `maxNumSeqs_v` come from the capacity store's engine params |
| `k_v` | `clamp(k_tpot_v, 0.15, 0.80)`: the same clamp `pricingK` uses (see [Bounds](#bounds)) |
| prefill SOs | **Unmodeled in phase 1.** No `RoleDemand[prefill]`, so `DemandForRole` is absent, the SO is undefined, and saturation decides prefill (`C1` guard, `C2` sizer). Prefill pods' TPOT is meaningless anyway (the P/D sidecar sends `max_tokens=1`) |

**The `O` and the `KVreq` are saturation's.** They come from the same
`ReplicaMetrics` fields (`AvgOutputTokens`, `AvgInputTokens`,
`PrefixCacheHitRate`), through `shape.New`, so the two analyzers price the same
shape.

**Not reused from the throughput analyzer.** `computeVariantSupply` hard-codes
`DefaultKSat`, has no batch cap and uses its own single window. The reusable
arithmetic is already in `signals/itl`. What is new is a `k` parameter and the
cap.

### What the composite needs before phase 1

1. **The branch landed**, with the three fixes our review (engine-structure,
   2026-09-21) found:
   - its #82 quota-charge regression
   - the saturation fallback skipping the per-SO `Reason` filter
   - that fallback's guard tested on the real `buildComposite`

   The rebase also has to carry the fields `main` added to the result since
   2026-09-11: `HeldRoleDemand`, `VariantBusiestQueueLoad` and
   `RoleStartSeconds`. The branch copies only `TotalDemand` and `RoleDemand`
   into the composite, which would silently disable utilization-share's hold,
   busiest-queue and start-time handling.
2. **Saturation disabled only by an explicit `enabled: false`** (above).
3. **`shadow: true`** (above).
4. **Warm-pool pressure and supply from the composite.** `publishVariantPressure`
   and `publishWarmPoolSupply` read `namedResults[0]` (saturation) with its
   threshold (`engine_v2.go:182-187`). In sizer mode, saturation can report
   pressure on a variant the composite will not grow, and the pool then keeps
   lending to it. Both must read `P_com` / `N_com`. This is a phase-1 blocker.

## Bounds

The tail of an over-estimate is the costliest failure here. Over-pricing `P`
is what breaks TTFT unrecoverably, so `P` is held to rules like k2's.

1. **The fill is clamped to [0.15, 0.80] of physical KV**, the range the line
   is fitted over and that `pricingK` already uses. The first draft's
   `k ≤ k_max_observed + 0.05` was **wrong** and is removed. The fleet runs
   `N ÷ scaleUp` replicas, and the token rate is concave, so the realised fill
   sits below the priced one. Each window's observed maximum then pulled the
   bound down, to a fixed point near `0.05·s ÷ (1 − s)`: 0.28 at `s = 0.85`.
   In sizer mode the SLO could never loosen, and in guard mode a loose SLO
   kept adding replicas. A unit test runs the closed loop and asserts a loose
   SLO never raises `N` above saturation's in guard mode.
2. **A line with no information gives no fill.** The line must be the
   variant's own fit (`Fit`, not the pinned-B fallback), and its slope must
   pass `ValidModel`. Otherwise the SO reports `ReasonNoData`. With a tiny
   slope, `k_tpot` runs to the clamp; the clamp is the only protection, which
   is why it is the same one saturation trusts.
3. **A borrowed line never loosens.** Under `enableLearnedStateReuse` a variant
   may be priced from a sibling's line. In guard mode that is safe, because the
   max can only tighten. In sizer mode the analyzer reports `ReasonNoData` for a
   borrowed line, and the SO falls back to saturation. This is
   [learned-state sharing](learned-state-sharing-by-similarity.md)'s
   cap-never-order.
4. **Infeasible means the SLO abstains.** If `tpotTarget ≤ B + A·0.15`, no fill
   in range meets the target on that variant. The SO reports a no-data reason
   `slo-infeasible`: it never caps a variant and never votes 0.
   - The first draft held the variant at its current count. That would freeze
     a single-variant P/D deployment (most real ones) exactly when load rises,
     and would hold a parked variant at 0 forever.
   - Steering growth away from an infeasible accelerator needs a per-SO hold
     that caps only the SLO's own growth above saturation. That is a later
     composite feature, not phase 1.
   - Infeasibility is reported on `wva_model_scaling_blocked` with reason
     `slo-infeasible` (kebab-case, like the existing reasons) only when every
     decode variant of the model is infeasible. Per-variant infeasibility
     shows in `wva_slo_source` (below).
5. **Undefined is never a number.** `A`, `B`, `C`, `KVreq`, `O`, every rate and
   every quantile are checked with `math.IsNaN`/`IsInf` before any clamp, max
   or division (`x <= 0` admits NaN). An invalid input gives the reason
   `slo-input-invalid`, not `P = 0`. A data fault must not look like an
   ordinary fallback.
6. **`P` drops at once and rises slowly.** Each step of a whole-node replica is
   a whole 8-GPU node and minutes of start time. A source that flips (the line
   dropping below the k-spread gate and back) would flap `N_com` between
   `C1` and `C2`. So:
   - `P_slo` may fall immediately.
   - It may rise only after the higher value has held for the scale-down
     stabilization window.
   - A switch from SLO-priced to saturation-priced is treated the same way.

## TTFT (phase 2)

This is the least certain part. It is enforced only after phase 0 has run in
shadow on real traffic and shown the curve can be learned.

```
TTFT_router  ≈  router queue  +  engine queue  +  prefill
```

The decomposition holds for **means**. Quantiles do not add, so the first
draft's `target − q_p(queue)` budget was wrong.

- **TTFT is its own contributor on the prompt-token axis**, not a second cap on
  the KV axis.
  - `D[prefill]` (or `D[both]`'s prefill share) is `ArrivalRate × ILeff`
    input tok/s.
  - `P_v` is the input tok/s per replica at the utilization `u_ttft_v` where
    the TTFT percentile meets the target.
  - Prefill time is driven by prompt tok/s per replica, not by KV fill. A
    long-output shape has high fill and almost no prefill load, so a KV-fill
    curve would be confounded by shape.
- **The curve.** It is per variant, from engine means only:
  - Each cycle contributes the point
    `(u, mean(request_prefill_time), mean(request_queue_time))`.
  - `u` is `PrefillComputedTokenRate ÷ ceiling_v`. The ceiling is **new learned
    state**: the highest sustained prefill token rate the variant has shown
    while its engine queue was non-empty. Nothing stores it today (saturation's
    nearest window is in req/s, keyed by shape).
  - The points go in bins 0.05 wide, with an isotonic (monotone) fit.
  - A bin counts only with ≥ 20 cycles, and is read at its one-sided 90 %
    upper bound.
- **The tail.** TTFT's percentile is mapped from the mean by the router's own
  ratio `q_p(TTFT_router) ÷ mean(TTFT_router)`, per model. It is measured only
  in windows with ≥ `max(50, 10 ÷ (1 − p))` requests, and kept as the 80th
  percentile of per-window ratios, not a rolling max (one preemption burst must
  not pin it).
- **Queue as zero.** Below the bin where mean engine queue time is under 5 % of
  the target, the queue counts as zero. The engine's first queue bucket is
  0.3 s, so a quantile there would charge 0.28 s for a 1 ms wait.
- **Blame.** A TTFT miss with decode at its KV ceiling, or with decode's queue
  above zero, is **decode's**. Prefill queues on decode back-pressure (we
  measured 439 queued with 3 of 4 prefill pods idle). Growing prefill then
  cannot help, so that miss is attributed to decode's contributor, not
  prefill's.
- **P/D transfer.** The KV transfer adds to TTFT. `vllm:nixl_xfer_time_seconds`
  is not collected today, and whether it is per request or per chunk must be
  checked before it enters any budget. Until then, the transfer is inside the
  router's tail ratio.
- **SGLang** has no `PrefillComputedTokenRate`. On SGLang the TTFT contributor
  abstains.
- **The transient is out of scope.** A step in arrival lands before a new
  replica is Ready, whatever fill the fleet sat at, and llm-d sees the same
  thing. Start time, the warm pool and
  [in-flight sizing](sizing-against-in-flight-capacity.md) are the levers.

## Attainment

From the router, per model and objective, over 5 minutes:

- **The query.** `sum by (le, target_model_name)(rate(..._bucket{streaming="true"}[5m]))`.
  It is rate first, then sum, so a counter reset on one EPP replica is
  handled. Only the current name is read: the `inference_objective_*` twin is
  used only when the current name returns nothing, so a migration cannot
  double-count.
- **Monotone buckets.** Summed cumulative buckets are repaired to be monotone
  in Go, as `histogram_quantile` repairs them.
- **TTFT attainment** = `bucket(le=target) ÷ count`. It is exact when the target
  is a bucket bound (3 s is), and interpolated otherwise. The interpolation is
  labelled.
- **TPOT** is reported as the router's **mean**, `rate(_sum) ÷ rate(_count)`,
  against the target. A mean is met or missed; it is not a share.
- **No data instead of a guess.**
  - Fewer than `max(50, 10 ÷ (1 − p))` requests in the window means no
    attainment: 100 at p90, 1000 at p99. On a 1–3-replica whole-node fleet,
    p99 is usually not measurable in 5 minutes. The metric then stays absent
    rather than reading 0 or 1.
  - A quantile landing in the `+Inf` bucket is `+Inf`, not the top finite
    bound.
- **Transients are excluded and counted.** Windows in which the model had 0
  ready replicas, had a replica starting, or were within 120 s of a rise are
  excluded, and the excluded share is published. A parked model's wake (192 s
  measured for a GLM-class model) would otherwise read as a capacity miss.

## Observability

Labels follow the shipped `wva_*` convention: `namespace`, `model_name`,
`variant_name`. Prometheus scrapes the model namespace as `exported_namespace`.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `wva_slo_target_seconds` | namespace, model_name, objective, statistic (`mean`/`p90`…) | The declared target |
| `wva_slo_observed_seconds` | namespace, model_name, objective, statistic | The router's observed mean (TPOT) or percentile (TTFT), over 5 m |
| `wva_slo_attainment_ratio` | namespace, model_name, objective | The TTFT share within target; absent below the sample minimum |
| `wva_slo_fill` | namespace, model_name, variant_name, objective | The fill `k_v` behind `P_slo`, physical KV fraction |
| `wva_slo_predicted_itl_seconds` | namespace, model_name, variant_name | The mean ITL the line predicts at the **realised** fill (`k_v` after the scale-up headroom): the margin an operator actually has |
| `wva_slo_source` | namespace, model_name, variant_name, source | 0/1 per source, like `wva_replica_capacity_source`: `own`, `borrowed`, `no-line`, `infeasible`, `input-invalid` |

- **Stale series are deleted** when a model, variant or target disappears, as
  `recordAnalyzerMetrics` already does for its own series.
- **Already exists:** `wva_analyzer_demand` / `wva_analyzer_target{analyzer="slo"}`
  publish the SLO's `(D, P)`. In observe mode, `D ÷ P` next to the actual count
  answers "what would the SLO want".
- **After the composite:** its per-SO decision path is in the cycle log
  (`composite-contributors`). A metric for it belongs to the composite, not to
  this proposal.
- **Combined attainment** (both objectives met on one request) cannot come from
  two histograms. Where the predictor plugin and the SLO headers exist,
  `llm_d_epp_request_slo_violation_total` is a cross-check. It is published
  with `clock="post-admission"`, because it excludes the router queue.

## Closing the loop on attainment (phase 3)

`P` is open-loop: a model of latency, trusted. A slow trim per variant corrects
it:

```
trim_v ∈ [0.5, 1.0];  k_effective = clamp(k_v) × trim_v       applied after the bounds
down: Wilson upper bound of attainment < p,       n ≥ 200, at most once per 10 min  → trim −= 0.05
up:   Wilson lower bound of attainment > p + 1 pp, n ≥ 200, held 30 min           → trim += 0.02
```

- **Gated on Wilson bounds.** Point estimates would fail: at 0.5 req/s and
  n = 300, a true 90.5 % falls under 90 % about 40 % of the time, and the trim
  would drift to its floor in hours.
- **Anti-windup.** The trim freezes:
  - when the SO's decision path is not SLO-decided
  - at `maxReplicas`, a quota or a hold
  - in the windows attainment excludes
  - until the fleet is Ready after a change
- **Router-only misses do not trim.** A miss whose engine-side means are within
  budget (the router queue dominated) is published as a reason, not trimmed on
  every variant.

This is not llm-d's control law. That law reacts within a minute and is the
sizer. The trim corrects a model that drifted, over tens of minutes.

## Phases

### Phase 0: the analyzer, observing (on today's `main`, being built)

- The `slo` block: parse, validate, overlay, warnings, and the mode in the
  effective-policy line.
- The router queries: TTFT buckets and TPOT mean, per model, with the
  attainment rules above.
- The TPOT pricing: `k_tpot`, the bounds, `P_v` with the batch cap, and `D`.
- The metrics above.
- Supporting changes:
  - The analyzer registered unconditionally (it runs only for models whose
    policy lists it).
  - A read accessor on `SaturationAnalyzer` for a variant's line and its
    source (this cycle's).
  - Access to the capacity store's engine params.
- **No engine change.** On `main` its result is published and discarded.
- The dormant `ServiceClass` / `FindModelSLO` / `configmap-serviceclass.yaml`
  are left for a separate change: deleting them needs a repo-wide check and
  the maintainer's agreement.

This answers "does my tier meet my SLO, and what would the SLO want?", which
nothing answers today.

### Phase 1: TPOT enforce

- Needs the composite and the four items under
  [What the composite needs](#what-the-composite-needs-before-phase-1).
- `shadow`, guard and sizer.
- The rise-slowly rule (bound 6).

### Phase 2: TTFT

The prefill-token contributor, the curve, the ceiling state and the blame
rule. Shadow first.

### Phase 3: the attainment trim

### Later: a TPOT percentile

llm-d scores p90 TPOT. A percentile needs a tail ratio
`ρ = q_p(per-request TPOT) ÷ mean(ITL)` from
`vllm:request_time_per_output_token_seconds` (vLLM ≥ 0.11). The math review
showed what it needs to be safe:

- A minimum count: completions are rare against tokens when outputs are long.
- The conservative end of the bracketing bucket, not interpolation: `k` swings
  2× inside one bucket.
- `ρ` measured near the `k` it is applied at.
- A high quantile of per-window `ρ`, not a rolling max.
- A reset on a shape change.

It is deferred until a user asks for a TPOT percentile.

## Tests

**Unit**, in the `slo` package:

- Pricing:
  - `k_tpot` and `P_v` against a hand computation.
  - The batch cap binding.
  - The clamp at both ends.
- Bounds and fallback:
  - The closed loop of bound 1: a loose SLO in guard mode never exceeds
    saturation's `N`. This is the negative control for the removed bound.
  - Each `ReasonNoData` path: no line, pinned-B line, borrowed line in sizer,
    infeasible, invalid input.
  - NaN and Inf in each input.
- Attainment and router data:
  - The Go quantile against `histogram_quantile` on the same buckets, a target
    on a bound, non-monotone input repaired, `+Inf`, below the sample minimum.
  - The current/old metric name preference.
  - The `count == 1` multi-model refusal.
- Metrics:
  - Every series is deleted when its model or variant disappears.

**In the composite (phase 1):**

- Guard mode: `N_com = max(N_sat, N_slo)` per SO.
- Sizer mode: a priced SO is `C1-single`, an unpriced one `C2-sat-fallback`.
- Saturation absent from an override's list stays enabled.
- A shadowed analyzer changes no `N_com`.
- The two edges: `D_sat = 0`, and `N_slo = 0`.
- `N` alternating 2 ↔ 3 as the line toggles ready: the rise-slowly rule holds
  it.
- A prefill SO stays saturation-decided in both modes.

**e2e.**

- `slo` in shadow mode changes no desired count.
- On the simulator's constant latency, the line never fits, so every decode SO
  is `no-line`.

A spec where the SLO decides needs the simulator's latency to rise with load.
We pass no load factor today, and whether v0.9.0 has one is unchecked.

**Benchmark.** The G1 replication in
[keda-recipe-comparison](https://github.com/ev-shindin/llm-scaling-manager/blob/bench/keda-recipe-arm/docs/plans/benchmark/keda-recipe-comparison.md)
(branch `bench/keda-recipe-arm`) needs the attainment scorer it lists as to-do.
That scorer and `wva_slo_attainment_ratio` cross-check each other.

- **Phase 0:** run ours with `slo` in shadow, and record the SLO's `D ÷ P`
  beside the actual count.
- **Phase 1:** add **ours-slo**, sizer mode, mean TPOT 100 ms. This
  prediction is registered before any run: TPOT does not bind on that load (a
  32B replica at 0.68 holds a batch of about 30), so ours-slo ties ours until
  phase 2 adds TTFT. **A phase-1 tie is the expected result, not a failure.**
  The phase-1 case is a TPOT-bound load, such as long outputs on a small
  accelerator, and it needs its own run.

## Alternatives considered

- **SLO logic inside saturation.** It puts histogram handling in a package
  already measured as too big, and loses the free shadow mode and the
  isolation.
- **Hand saturation a per-variant fill** (an earlier draft). It needs an
  engine ordering change and its own fallback and "who decided" reporting. The
  composite gives those.
- **Combining by rescaling saturation's capacity.** It under-orders when
  loosening, by `ITL(k_slo) ÷ ITL(k_static)`.
- **llm-d's control law.** It is reactive, does not price variants, and is
  blind to P/D. Phase 3 keeps its one good property at a slow time scale.
- **The predictor's histograms.** They need the plugin, are recorded at end of
  stream, and run on a clock that excludes the router queue.
- **Static presets per SLO.** That is today's *Strict* preset.

## Open questions

1. **A per-SO growth cap in the composite**, to steer growth away from an
   SLO-infeasible accelerator without ever capping below saturation's `N`. Is
   that a hold (engine-structure plans per-role holds), or a composite rule?
2. **`ArrivalRate` on a pool serving several models** is refused today
   (`count == 1`). Should the SLO analyzer abstain there, or use
   `target_model_name` alone?
3. **Multi-pod replicas** (LWS, DP or EP across nodes). The ITL means are
   already collapsed per scale-target unit (`pod_collapse.go`). The phase-2
   prefill means must follow the same rule, and worker pods without request
   histograms must not read as "no data" forever.
4. **The docs to change with phase 1** in `docs/reference/scaling-policy.md`:
   - `enabled` is no longer "reserved".
   - "saturation drives every decision" is no longer true.
   - *Strict* stops being the low-latency SLO answer.
   - `queueLengthThreshold` has no effect on an SLO-decided SO.
