# What-if capacity: end-to-end TTFT

**Status:** proposal, 2026-11-02. Nothing here is built. The companion to
[what-if capacity](slo-capacity-planner.md), which owns the questions, the
decode model, the CLI and the PR plan. This page owns the TTFT model: what it
is made of, the router's flow-control queue, how the parts combine into a
percentile, and the staircase run that validates it.

**Audience:** contributors.

## In one paragraph

TTFT is end to end, as the client sees it. It is the wait before prefill
(router queue and engine queue), the request's own prefill, the KV transfer and
the decode side's admission wait and first step. Prefill capacity is a token
throughput `V_P`, taken only from sources that cannot over-state it. Below the
point where EPP flow control starts holding requests, the router queue is empty
and each pod queues for itself, so the model is per pod, with the measured
imbalance. The parts are combined into a percentile by convolution where they
are independent and by an optimised union bound where they are not; never by
adding percentiles.

## The terms

    TTFT = W_router + W_engine + own_prefill + T_xfer + W_decode + ITL(k_d)

| Term | What it is | Model |
| --- | --- | --- |
| `W_router` | EPP flow-control queue: requests held before dispatch | 0 below the gate's onset; [the router queue](#the-router-queue-flow-control) above it |
| `W_engine` | The prefill pod's own queue | [Per pod, with the imbalance `f`](#the-engine-queue) |
| `own_prefill` | `a + I/V_P`, this request's own prefill | [Prefill capacity](#prefill-capacity) |
| `T_xfer` | KV transfer to decode (P/D) | `I·κ/BW` once `nixl_xfer_time_seconds` is collected; omitted and flagged until then |
| `W_decode` | Waiting for a decode slot | [The birth-death chain](#decode-admission) |
| `ITL(k_d)` | Decode's first step, which streams the first token (P/D) | The decode line at its fill |

| | Flow control off (the upstream default) | Flow control on |
| --- | --- | --- |
| **P/D** | `W_router` = 0; every other term | Below the onset: as off. Above it: `W_router` holds the excess; engines hold about the detector's threshold |
| **Aggregated** | `W_router` = 0; `W_engine` is the pod's queue, which also holds decode admission; `own_prefill` produces the first token, so no `T_xfer`, `W_decode` or `ITL(k_d)` | As P/D: as off below the onset |

Flow control is off by default upstream, but this repository's scale-to-zero
installs turn it on (`deploy/lib/epp-flow-control.values.yaml`), so both
columns matter. With it off, the router admits everything; sheddable requests
(priority below 0) are rejected under saturation rather than queued.

## What is derivable today

The inputs are inventoried in [one table](slo-capacity-planner.md#what-exists).
Three derivations matter here:

- **On a P/D prefill pod**, the sidecar asks for one token, so
  `AvgServiceTime` is the prefill time and `AvgTTFT − AvgServiceTime` is the
  engine wait. That difference holds a constant overhead (tokenization,
  scheduling, the HTTP path): on `ols-sweep` it is never below 2.5 ms with no
  queue. Its low-load minimum is added to `a`. The wait derivation works for
  vLLM and SGLang (SGLang's service time is e2e minus `queue_time`).
- **On an aggregated pod** it does not work: service time includes decode, and
  `AvgServiceTime − (OL − 1)·ITLPerToken` subtracts two large numbers. The
  per-stage `request_prefill_time_seconds` is needed.
- **`ols-sweep`** has one prefill pod that never queued. It supports `a`
  (10.1 ms at low load) and the low-load slope, not `V_P` or `f`.

## Prefill capacity

A vLLM prefill pod schedules prompts first come, first served, under a token
budget per step (`--max-num-batched-tokens`). It is a work-conserving server of
prompt tokens at rate `V_P`. A prompt's own prefill duration stretches when
other prompts share its steps (`constants.VLLMRequestPrefillTimeSeconds` says
so), so it is not a property of the pod.

**The fixed cost `a` enters only the request's own latency.** Measured at low
load, where a prompt has a step to itself, most of it is paid per step under
load, and busy-minute `V_P` is net of it. Utilization and the queue use work
alone: `S_work = I_eff/V_P`, `ρ = λ·E[I_eff]/V_P`.

**`V_P` comes only from sources that cannot over-state it**, in this order:

1. **Busy minutes.** The pod's queue was above zero on every sample in the
   minute (`min_over_time` of `num_requests_waiting` positive), and decode was
   not back-pressuring it (the test `computeK2` applies,
   `k2ReasonObsDownstream`). `V_P` is the median of `PrefillComputedTokenRate`
   over at least 5 such minutes. On P/D this is rare: saturation has never
   recorded a non-back-pressured prefill saturation there.
2. **Observed peak under a queue.** The highest `PrefillComputedTokenRate`
   sustained over 3 minutes in which the pod held a queue on some sample.
   Throughput never exceeds capacity, so this is a lower bound. Flagged. A peak
   with no queue is only the offered load and is not used.
3. **Back-pressure bound**: saturation's prefill lower-bound window
   (`prefill_bound.go`). Flagged.
4. Otherwise `NoPrefillCapacity`. SGLang always lands here (no computed-token
   series).

The **low-load slope** of service time against prompt length is an upper bound,
not a source. A lone prompt runs as `T(I) = s0 + c·I`, and a busy pod's steps of
`B` tokens run at `B/(s0 + c·B) < 1/c`, so `1/slope ≥ V_P`: on `ols-sweep` about
760k tok/s against an offered peak of 30k. It is a validation signal only.
`--assume-vp` supplies `V_P` by hand for an illustration or for hardware not yet
measured; every answer built on it is flagged ASSUMED.

## The engine queue

The router sends each request to one pod, which queues it. One run measured
439 waiting with 3 of 4 prefill pods idle: the prefill profile had one usable
scorer, so one pod won every request in a refresh window. So each pod is an
M/G/1 queue on prefill work, and the worst pod sees `λ_w = f·λ/N_p`:

    W̄ = λ_w · E[S_work²] / (2·(1 − ρ_w))                    (Pollaczek-Khinchine)
    θ = E[S_work]·(1 + Cs²) / (2·(1 − ρ_w)),   W̄ = ρ_w·θ
    P(W_engine > t) ≈ ρ_w · e^(−t/θ)

with `Cs² = E[I_eff²]/E[I_eff]² − 1`. For bursty arrivals `1 + Cs²` becomes
`Ca² + Cs²` (Kingman). The worst pod is stricter than the router's histogram,
which mixes pods; the mixture `Σ share_i·P(W_i > t)` may be used instead. The
pooled M/G/c (Allen-Cunneen) is printed as an optimistic bound only.

**Measuring `f`**: the busiest pod's share of prefill work over the mean share,
from per-pod `PrefillComputedTokenRate`, over minutes with at least 100
requests per pod, minus the expected max of `N_p` Poisson counts. Sticky routing
(one pod wins a whole refresh window `w`) averages back toward 1 per minute; the
worst pod then gets `Ca² ≈ 1 + λ_w·w`, uncapped.

## Decode admission

A decode pod holding `n` requests completes them at

    μ_n = m / ( OL · ITL(m·KVreq/C) ),   m = min(n, c),   c = batchCap·ranks

a birth-death chain using the line at each state's own fill. Above `c` the chain
is geometric, so the wait of a request that waits is exactly
`Exp(μ_c − λ_rep)`, and the wait probability is the chain's mass at `n ≥ c`. A
constant ITL (Erlang C at the mean fill) under-states the wait near the knee: in
the worked example 0.0007 against 0.032 at 0.85 of the knee. When KV binds
before the cap, `c = C/KVreq` at a full cache, flagged approximate. Residence is
treated as exponential.

**One rule, flow control on or off:** `W_decode` is this chain, on the decode
pods. With the batch cap binding (the worked example's cap fill is 0.732), the
detector's KV term (0.8) never trips, so requests wait for slots on the decode
pods, not in the router. What flow control adds is the router hold above the
decode gate's onset, below.

## The router queue (flow control)

**What EPP flow control does.** Sources: upstream
`pkg/epp/flowcontrol/README.md` and the dispatch cycle in
`pkg/epp/flowcontrol/controller/internal/processor.go` (llm-d router,
gateway-api-inference-extension), the detectors' `config.go` under
`pkg/epp/framework/plugins/flowcontrol/saturationdetector/`, and this repo's
[router configuration](../reference/router-configuration.md).

- Off unless the `flowControl` feature gate is on.
- Requests wait in per-flow queues in the EPP. Each dispatch cycle releases at
  most one request.
- **The gate.** The default `utilization-detector` scores each endpoint
  `max(queueDepth/5, kvUsage/0.8)` and takes the unweighted **mean**. On P/D it
  scores the prefill and decode stages apart (unlabelled pods count as decode,
  interleaved pods in both) and gates on the worse. At a score at or above the
  band's ceiling (1.0 under the default static policy) the cycle stops for every
  band: head-of-line blocking. The `concurrency-detector` reads in-flight
  requests against `maxConcurrency` (100 per endpoint); in its default aggregate
  mode it compares totals, so it has the same mean problem, and only its
  `hybrid` mode bounds each pod.
- **Order.** Bands highest first; within a band the default fairness is
  `global-strict-fairness-policy`, a greedy global order across flows (with one
  flow, first come first served); within a flow, first come first served.
- **TTL.** Version dependent: v0.9.0 resolves `DefaultRequestTTL: 0s` (no
  expiry); newer EPPs default to 60 s; this repo recommends 30 s. Read the
  resolved `FlowControlConfig` from the EPP log.
- **Metrics** (`llm_d_epp_` prefix; upstream GIE also exports
  `inference_extension_` as a deprecated alias): `flow_control_queue_size` and
  `_bytes` (collected); `flow_control_request_queue_duration_seconds`
  (histogram, `priority` and `outcome`); `flow_control_pool_saturation`
  (`inference_pool`, `stage`); `flow_control_requests_total` (`outcome`,
  `priority`, `inference_pool`). The last two carry no model label: they are
  recorded per pool, and joined to a model only when the pool serves one, the
  guard the arrival-rate query already uses.

**When the router holds anything.** The gate stays open while the mean endpoint
score is below 1, so the engines fill first. A prefill pod's mean waiting count
is `ρ²(1 + Cs²)/(2(1 − ρ))`; at `Cs²` = 1 it reaches 5 at `ρ` = 0.854. A decode
pod's mean waiting count comes from the chain. The **onset** of each stage is
the rate at which its mean score reaches 1; the router queue is empty below it.

**The model, in order of rate:**

1. **Below both onsets** (where every operating point sits in the worked
   example): flow control changes nothing. The per-pod engine model with `f`,
   and `W_decode` on the decode pods. `W_router` = 0.
2. **Above the prefill onset**: the router holds the excess, the engines hold
   about the threshold. The answer stays the per-pod model, flagged
   "above flow-control onset". Late binding can only reduce imbalance while the
   gate is closed, but one-per-cycle dispatch and stale scores can add delay,
   so the per-pod model is not proven conservative there; the pooled M/G/c is
   printed as the optimistic bound.
3. **Above the decode onset**: a closed decode gate stops dispatch for every
   request, prefill included, and a released request leaves its decode slot
   idle through prefill and transfer, so the gate is not work conserving. The
   tool does not price TTFT here. The decode onset is an extra knee under flow
   control: question 1 caps at it; question 2 flags an operating point above
   it. Decode admission is counted once, in `W_decode`, never also in
   `W_router`.

In the worked example the decode onset is 12.50 r/s (0.94 of the knee) and the
prefill onset 25.6 r/s, so both answers there are the same with flow control on
or off.

**Detecting it.** The flow-control series exist only when the gate is on. Absence
means off, flagged "flow control inferred off (no series)" (a scrape gap looks
the same). `FlowControlUnknown` is refused only when it is on and the detector's
thresholds are neither readable nor given (`--fc-queue-threshold`,
`--fc-kv-threshold`, `--fc-max-concurrency`).

**Bands.** The model is for one band under the static policy. With several bands
or a holdback policy, the answer is for the highest band, flagged.

## Combining the parts into a percentile

**Percentiles do not add.** In the worked example at p95, adding the parts'
p95s gives 224 ms, the bound below 231 ms, and full independence 210 ms.

Two blocks:

1. **The prefill side**: the pre-service wait and the request's own work,
   `a + I/V_P + I·κ/BW`. `T_xfer` scales with the prompt like prefill does, so
   it is folded into the same function of `I` rather than convolved as an
   independent part. The wait is independent of the request's own work for
   first-come service with Poisson arrivals, so the two are convolved exactly.
   That fails for `edf` or `slo` ordering, which the tool flags.
2. **The decode side**: `W_decode`, driven by the same arrivals, so not
   independent of block 1.

For any split `α + β = 1 − q`:

    TTFT_q ≤ Q_{1−α}(block 1) + Q_{1−β}(W_decode) + ITL(k_d)

holds whatever the dependence between the blocks. The tool **chooses `β` by a
one-dimensional search** to minimise the sum. When decode's wait probability is
below `1 − q`, the best `β` is about that probability and the decode term is 0;
an even split would instead switch the term on abruptly where the probability
crosses `(1 − q)/2`. The figure for independent blocks is printed beside it.

The bound is an upper bound on the quantile of the sum of the **modelled**
parts, never below their independent combination. It does not correct a part
that is itself wrong (the exponential tail, Poisson arrivals, an omitted
`T_xfer`).

## Where it is most wrong

| Limit | Direction |
| --- | --- |
| Exponential wait tail with heavy-tailed prompts | Over-promises |
| Poisson arrivals on bursty traffic | Over-promises |
| Per-pod model above the flow-control onset | Either way; the pooled bound printed beside it over-promises |
| Router queue modelled pooled | Over-promises under imbalance |
| Dispatch-gate delays (scores up to 200 ms stale, a 50 ms candidate cache) | Over-promises, slightly, near the onset |
| One band, static ceiling | Over-promises for low bands, under-promises for the highest |
| TTL eviction (only where a TTL exists) | Evicted requests are not in the router histogram: the ground truth reads high near the TTL |
| Proxy hop before the EPP | Over-promises by that hop |
| `T_xfer` omitted (not collected) | Over-promises |
| `V_P` from a lower bound | Under-promises |
| Worst-pod percentile | Under-promises |
| The union bound | Under-promises against the true combination of the modelled parts; the independent figure beside it over-promises |

## Ground truth and the staircase

**The ground truth is the router's TTFT histogram**,
`llm_d_epp_request_ttft_seconds`. It starts at the EPP's request headers,
before flow control, and misses only the proxy hop and requests that never
finish. The mean router TTFT is also checked against the sum of the parts'
means, each from its own series ([inventory](slo-capacity-planner.md#what-exists));
the residual is the proxy hop and scheduling.

The staircase is the TTFT pass mark (decode is validated against the goldens).
**The prediction is made before the run**, from a separate warm-up recording.

| Item | Specification |
| --- | --- |
| Fleet | Fixed `N_p` = 2, `N_d` = 2; ScaledObjects paused (`autoscaling.keda.sh/paused`) |
| Load | guidellm, constant rate, streamed, from `test/benchmark` behind a make target |
| Buckets | A: prompts 1000, answers 1000 (decode-bound). B: prompts 8000, answers 200 (prefill-bound) |
| Flow control | Each bucket twice: off, then on (utilization detector, one band, static ceiling) |
| Start | 40% of the predicted knee |
| Steps | +10% to 70% of the knee, then +3% to 110% |
| Hold | 10 minutes per step; the first 3 discarded |
| Measured | Router TTFT p90 and p95 and mean TPOT over the last 7 minutes; with flow control on, `pool_saturation` per stage, the direct test of the onset |
| Runs away | TTFT p90 above twice the target for the whole window |
| Repeats | 3; the statistic is the median over runs, per bucket |
| Recorded | A learner recording on a build with the [TTFT recorder fields](analyzer-learner.md#29-recorder-fields-for-ttft-and-capture); per-pod `num_requests_waiting` |

**Measured rate at SLO**: the highest step rate, interpolated between steps,
whose TTFT percentile and mean TPOT meet the target. **Pass**: at the operating
point, a predicted rate more than 10% above the measured one fails; more than
25% below fails. **Noise floor**: an M/G/1 queue at `ρ` 0.85 relaxes in about
`E[S](1 + Cs²)/(1 − √ρ)²`, about 330 `E[S]` at `Cs²` = 1, 90 s for an 8000-token
prefill at 30k tok/s; a step whose window holds fewer than 4 such times is
flagged and lengthened. Until the run passes, every TTFT figure prints
UNVALIDATED.

## Open questions

1. **Is the per-pod model right above the prefill onset?** *Recommend:* keep it,
   flagged, until the flow-control-on phase scores it.
2. **Model the dispatch-gate delay?** *Recommend:* not until the staircase shows
   a residual at the onset larger than the proxy hop.
3. **Is `nixl_xfer_time_seconds` per request or per chunk?** (carried from
   [SLO tiers](slo-tiers.md) open question 4). `T_xfer` as a per-request term
   depends on it. *Recommend:* settle it on the staircase's P/D run before
   `T_xfer` leaves "omitted".
4. **Does `V_P` vary with prompt length?** *Recommend:* key it by input bucket;
   add a term only if bucket B misses by more than 15%.
5. **Burstiness.** *Recommend:* estimate `Ca²` from the arrival rate's variance,
   cap that estimate at 4; the sticky-routing term is not capped.
6. **Several priority bands.** *Recommend:* the highest band only, flagged.
