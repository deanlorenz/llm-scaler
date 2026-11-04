# What-if capacity: end-to-end TTFT

**Status:** proposal, 2026-11-02. The model is built as a library (P3,
2026-11-04: [as built](#as-built-p3)), UNVALIDATED and on no switch; nothing
calls it yet. The companion to
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
| `W_router` | EPP flow-control queue: requests held before dispatch | Almost always 0 below the gate's onset. Above the prefill onset it has no formula of its own: the per-pod wait covers `W_router + W_engine` together. Above the decode onset, not priced ([the router queue](#the-router-queue-flow-control)) |
| `W_engine` | The prefill pod's own queue | [Per pod, with the imbalance `f`](#the-engine-queue) |
| `own_prefill` | `a + I/V_P`, this request's own prefill | [Prefill capacity](#prefill-capacity) |
| `T_xfer` | KV transfer to decode (P/D) | `I·κ/BW` once `nixl_xfer_time_seconds` is collected; omitted and flagged until then |
| `W_decode` | Waiting for a decode slot | [The birth-death chain](#decode-admission) |
| `ITL(k_d)` | Decode's first step, which streams the first token (P/D) | The decode line at its fill |

| | Flow control off (the upstream default) | Flow control on |
| --- | --- | --- |
| **P/D** | `W_router` = 0; every other term | Below the onsets: as off, approximately. Above the prefill onset: `W_router` holds the excess, engines hold about the detector's threshold. Above the decode onset: not priced |
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
  and the low-load slope, not `V_P` or `f`. `a` is 11.04 ms: an
  intercept of 8.36 ms, fitted against the effective prompt over low-load
  minutes, plus the least overhead, 2.68 ms. An earlier
  revision said 10.1 ms: that was an intercept fitted against
  `AvgInputTokens`, the long-window prompt mean, not the minute the service
  time covers, and it left the overhead out.

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
520k tok/s against an offered peak of 30k. It is a validation signal only.
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
requests a pod on average, minus the expected max of `N_p` Poisson counts.
That de-bias errs both ways ([where it is most wrong](#where-it-is-most-wrong)):
low on a real imbalance at few requests, high on equal pods with variable
prompts. Sticky routing
(one pod wins a whole refresh window `w`) averages back toward 1 per minute; the
worst pod then gets `Ca² ≈ 1 + λ_w·w`, uncapped.

## Decode admission

A decode pod holding `n` requests completes them at

    μ_n = m / ( OL · ITL(m·KVreq/C) ),   m = min(n, c),   c = batchCap·ranks

a birth-death chain using the line at each state's own fill, clamped to the
fitted band (a fill below it reads the band's bottom). Above `c` the chain
is geometric, so the wait of a request that waits is exactly
`Exp(μ_c − λ_rep)`, and the wait probability is the chain's mass at `n ≥ c`. A
constant ITL (Erlang C at the mean fill) under-states the wait near the knee: in
the worked example 0.0007 against 0.032 at 0.85 of the knee. When the band's
top holds fewer requests than the cap, `c = floor(band.Hi·C/KVreq)`, flagged
approximate. Either way `μ_c` is the decode knee per replica, so the chain and
the knee saturate at the same rate. Residence is treated as exponential.

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
the rate at which its mean score reaches 1; the router queue is **almost
always** empty below it. The onset is a time average, while the gate reads the
current score, so below the onset the gate still closes for the share of time
`P(score ≥ 1)`, and every request waits while it does. In the worked example,
with one decode pod, that is 0.8%, 1.4% and 3.4% of the time at 11.10, 11.32 and
11.64 r/s (about `ρ⁶`, 0.3%, on the prefill side). The tool prints this closed
fraction in the basis. The union bound adds block 1 and `W_decode` rather than
overlapping them, so the answer is expected to stay conservative; the staircase
checks it.

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
p95s gives 224 ms, the optimised bound 231 ms, full independence 210 ms.

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
| Wait tail at prompt cv above 1: `ρ_w·e^(−γt)`, `γ` the Cramér-Lundberg root ([as built](#the-model-as-built)) | Under-promises at p99 by 3–15% against a Lindley simulation (cv 2 and 3, `ρ` 0.3 and 0.6); not a proven bound. Kingman's `1/θ` alone over-promised by up to 10% here |
| Wait tail at prompt cv below 1: the Lundberg bound `min(ρ_w, e^(−γt))` | Under-promises, provably: by 11–24% at p90 and 6–18% at p99 against a simulation (cv 0 and 0.5), and 1–17% against Erlang's exact M/D/1 wait. The exponential body it replaced read an M/D/1 p90 sojourn 10% short at `ρ` 0.2 |
| Wait tail with bursty arrivals (`Ca²` ≠ 1): Kingman's `1/θ`, never above the work's own tail rate | Not checked against a simulation; burstiness lengthens the tail, and Kingman's decay is taken as its conservative side |
| Poisson arrivals on bursty traffic | Over-promises |
| Per-pod model above the flow-control onset | Either way; the pooled bound printed beside it over-promises |
| Dispatch-gate delays (scores up to 200 ms stale, a 50 ms candidate cache) | Over-promises, slightly, near the onset |
| One band, static ceiling | Over-promises for low bands, under-promises for the highest |
| TTL eviction (only where a TTL exists) | Evicted requests are not in the router histogram: the ground truth reads high near the TTL |
| Proxy hop before the EPP | Over-promises by that hop |
| `T_xfer` omitted (not collected) | Over-promises |
| `V_P` from a lower bound | Under-promises |
| `V_P` from the peak under a queue: a max over up to 358 overlapping 3-minute windows' minimums of a one-minute `rate()` | Over-promises, by about the noise of a one-minute rate |
| `f` de-biased for Poisson arrivals, on a real imbalance | Over-promises: the busiest pod's share holds little chance excess, but the equal pods' excess is subtracted anyway. Four pods at 2:1:1:1 (`f` 1.6) read about 1.50 at 100 requests a pod (6% low) and about 1.57 at 1500 |
| `f` from computed-token shares with variable prompts | Under-promises: a pod's token count varies about `√(1+Cs²)` times its request count, and only the request count's excess is subtracted. Four equal pods with `Cs²` = 1 read about 1.03 at 100 requests a pod |
| Worst-pod percentile | Under-promises |
| The mean TTFT is the worst pod's mean | Under-promises the fleet's mean, which mixes in the quieter pods |
| Question 1 capped at the decode onset | Over-promises: the onset is where the mean score reaches 1, and the gate reads the current score; `E[max] ≥ max E`, so the gate already closes for part of the time below it |
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
| Measured | Router TTFT p90 and p95, mean TPOT and per-request TPOT p90 (router TPOT histogram, and guidellm per request) over the last 7 minutes; with flow control on, `pool_saturation` per stage, the direct test of the onset |
| Runs away | TTFT p90 above twice the target for the whole window |
| Repeats | 3; the statistic is the median over runs, per bucket |
| Recorded | A learner recording on a build with the [TTFT recorder fields](analyzer-learner.md#29-recorder-fields-for-ttft-and-capture); per-pod `num_requests_waiting` |

**Measured rate at SLO**: the highest step rate, interpolated between steps,
whose TTFT percentile and TPOT (mean, and per-request percentile) meet the
target. **Pass**: at the operating point, a predicted rate more than 10% above
the measured one fails; more than 25% below fails. The same mark is applied
separately to the mean TPOT and to the per-request TPOT percentile. **Noise floor**: an M/G/1 queue at `ρ` 0.85 relaxes in about
`E[S](1 + Cs²)/(1 − √ρ)²`, about 330 `E[S]` at `Cs²` = 1, 90 s for an 8000-token
prefill at 30k tok/s; a step whose window holds fewer than 4 such times is
flagged and lengthened. Until the run passes, every TTFT figure prints
UNVALIDATED.

## As built (P3)

`internal/signals/whatif`, files `ttft.go`, `ttft_queue.go`, `ttft_result.go`
and `ttft_prefill.go`. Library only: no switch, no caller in the controller,
no CLI wiring (that follows D3), no golden moves.

**Built before its validation.** The plan has P3 depend on P2, the
prefill-stress recording kept as a golden. That run is blocked on cluster
access, so P3 was built against known truth and synthetic fleets instead.
Every TTFT figure carries the flag `UnvalidatedTTFT`, and nothing here has
been compared with a measured TTFT.

### The API

| Call | Answers |
| --- | --- |
| `TTFTAt(fleet, rate, q)` | Question 2's TTFT at a rate, at percentile `q`, or the mean for `q` = 0. Returns a `TTFT`: the union bound (`Seconds`), the independent figure, the pooled optimistic figure, the floor, the decode share `β`, the parts' means, `ρ_w`, `θ`, both knees and the onsets |
| `RateForTTFT(fleet, target)` | Question 1: the largest rate whose TTFT meets the target. An `Answer` whose `Basis.TTFT` carries the lower knee, the TTFT floor and the prefill side's own rate (`PrefillOnlyRate`); `BoundBy` says what set the rate. The TPOT fields of `Basis` (`KneeRate`, `Floor`, `Uncapped`) stay 0 |
| `PrefillKnee(prefill)` | `N_p·V_P/(f·E[I_eff])`, in `Basis.TTFT.KneeRate` |
| `FlowControlOnsets(fleet)` | Each stage's onset; zero with flow control off |
| `PrefillFromEstimate(est)` | Maps P1's `prefill.Estimate` to `V_P`, `a` and `f`, and carries P1's refusals |

A `TTFTFleet` is a `Prefill`, an optional `*Decode` and a `FlowControl`.
With no decode role, the prefill side is priced alone and flagged
`DecodeSideOmitted`. This is the shape S1 needs to price one prefill variant
in shadow; the rate per replica is the answer over `N_p`, priced at the
actual `N_p` with `f` held. With flow control on and no decode role, the
decode onset cannot be placed: the answer is flagged `DecodeOnsetUnchecked`,
not refused, and may stand above the onset. The operating-point cap is not
applied: as on the decode side, the caller applies it to the smaller of the
roles' rates.

### The model as built

    ρ_w   = f·λ·E[I]/(N_p·V_P)
    θ     = (E[I]/V_P)·(Ca² + Cs²) / (2(1 − ρ_w))          Cs² = cv²
    γ     : λ_w·(M_S(γ) − 1) = γ        (Cramér-Lundberg root; M_S the work's MGF)
    P(W_engine > t) = ρ_w·e^(−rt),  r = min(1/θ, γ)          Poisson arrivals, cv ≥ 1
                    = min(ρ_w, e^(−γt))                     Poisson arrivals, cv < 1 (Lundberg bound)
                    = ρ_w·e^(−rt),  r = min(1/θ, 1/(cv²·E[S]))  bursty arrivals (Kingman)
    own   = a + I·(1/V_P + κ/BW),   I ~ Gamma(mean E[I], cv); constant below cv 1e-4
    block 1 = W_engine + own        (convolved exactly, closed form via the incomplete gamma)
    TTFT_q = min over β of  Q_{1−(1−q−β)}(block 1) + Q_{1−β}(W_decode) + ITL(k_d)
    mean  = ρ_w·θ + a + E[I]·(1/V_P + κ/BW) + E[W_decode] + ITL(k_d)
    floor = a + I_q·(1/V_P + κ/BW) + ITL(band.lo)

- **The wait tail errs only toward more replicas.** At cv 1 the two decays
  agree and the tail is exact (M/M/1). Above 1, the Cramér-Lundberg root is
  the slower decay: Kingman's `1/θ` alone read a p99 6–11% short of a
  Lindley simulation (cv 2, 3), and the root now reads it 3–15% long. Below 1,
  an exponential body behind the atom under-states the wait just past it,
  so the tail is the Lundberg inequality's bound, which holds for every
  M/G/1 queue. For bursty arrivals there is no root without the arrival
  law, so the tail keeps Kingman's decay and never decays faster than one
  request's work. The mean wait stays Pollaczek-Khinchine's (Kingman's)
  `ρ_w·θ`, exact for M/G/1, rather than the tail's own mean.
- **The mean is the worst pod's**, like every figure here: the mean TTFT a
  request on the busiest pod sees, not the fleet's.
- A prompt cv below 1e-4 is priced as constant: the gamma's shape is then
  above 1e8, where the incomplete gamma cancels (cv 1e-7 read 8% high).
- `W_decode` is D2's admission chain (`DecodeAdmission`), bounded at 1e6
  slots. `ITL(k_d)` is the line at the closed-form decode fill, read at the
  band's bottom below it.
- The search over `β` is a log grid on `(0, min(P_wait, 1 − q)]` followed by
  a golden-section refinement. The even split is a candidate as well, so the
  answer is never above it.
- The independent figure convolves block 1 with `W_decode` (a two-rate
  hypoexponential). The pooled figure is Allen-Cunneen M/G/`N_p` with no
  imbalance. It describes a router that holds requests and dispatches each
  to the first free pod, which flow control does only above the prefill
  onset. Below the onset each pod queues for itself, and the pooled figure
  is an optimistic bound, not an estimate.
- **Flow control.** The prefill onset assumes the worst pod is at `f` and the
  rest split the remainder evenly. Each pod scores `Lq/threshold`, with
  `Lq = ρ²(Ca² + Cs²)/(2(1 − ρ))`, and the onset is where the mean score
  reaches 1. Prefill KV is not scored. The decode onset is where a balanced
  decode pod's `max(Lq/threshold, fill/KVthreshold)` reaches 1, with `Lq`
  and the fill taken from the chain. At or above the decode onset TTFT is
  refused (`AboveDecodeOnset`), and question 1 caps at it. At or above the
  prefill onset the answer is flagged `AboveFlowControlOnset`. Below both,
  flow control changes nothing. The gate's closed fraction `P(score ≥ 1)` is
  **not built**.
- **The question-1 cap at the decode onset is not conservative.** The onset
  is where the mean endpoint score reaches 1, while the gate acts on the
  current score. Since `E[max] ≥ max E`, the gate already closes for part of
  the time below the onset.
- **The gate rule** (the mean over endpoints of
  `max(queue/threshold, kv/KVthreshold)`) is the newer llm-d router's
  utilization detector, not GIE v1.2.1's. On a v1.2.1 EPP the onsets are not
  these.
- **Refusals**: `NoPrefillCapacity`, `NoFixedLatency`, `NoImbalance` (more
  than one pod with no `f`; `f` = 1 is never assumed silently),
  `NoPrefillReplicas`, `FlowControlUnknown` and `AboveDecodeOnset`, plus
  `Saturated` at the prefill knee, and `NoTraffic`, `TargetBelowFloor` and
  `InvalidInput` as on decode.
- **Flags**: `UnvalidatedTTFT` (always), `AssumedVP`, `VPLowerBound`,
  `VPAboveSlopeBound`, `AssumedImbalance`, `AssumedPromptSpread`,
  `TransferOmitted`, `DecodeSideOmitted`, `FlowControlInferredOff`,
  `AboveFlowControlOnset`, `HeldByDecodeAdmission` and
  `DecodeOnsetUnchecked`. When decode
  admission holds the TTFT rate below what the prefill side alone allows,
  the answer is bound by `TTFT` and then `decode admission`. The worked
  example's 11.10 r/s at p95 is such a case: decode takes `β` ≈ 0.03 of the
  tail.

### What the tests check

- **Known truth.** M/M/1 sojourn (exact at cv 1, `f` = 1, one pod), the
  Pollaczek-Khinchine mean, Allen-Cunneen against an independent Erlang C,
  the Cramér-Lundberg root against its equation, and the incomplete gamma
  against its closed forms.
- **Never below the truth.**
  - At cv 0 the wait's quantiles are compared with Erlang's exact M/D/1
    waiting-time distribution (summed in 600-bit floats). The model is at
    or above it at every `ρ` from 0.1 to 0.9 and `q` of 0.9, 0.99 and 0.999.
  - At p99 the sojourn is compared with frozen figures from a Lindley
    simulation outside the code (3M arrivals, numpy, seed 20261104). The
    cases are cv 0, 0.5, 1, 2 and 3 at several `ρ`. The model must reach
    the simulation less 3%, the spread across seeds.
  - The model/simulation p99 ratios are:

    | cv | `ρ` 0.3 | `ρ` 0.6 |
    | --- | --- | --- |
    | 2 | 1.099 | 1.036 |
    | 3 | 1.145 | 1.031 |

    The old tail read 0.937 / 0.944 and 0.895 / 0.907, and fails this
    test. The worked example
  is reproduced to 1e-6: 160 / 154 ms at p90, 231 / 210 ms at p95, floor
  91.6 ms, 11.64 / 11.10 r/s for 200 ms, onsets 12.50 and 25.6 r/s. Six
  cases with no closed form (cv 0.5 and 2, bursty arrivals with `f` 1.5
  over four pods, a transfer term, prefill alone, constant prompts) are
  checked against a scipy reference that integrates a different form of the
  convolution.
- **Properties** on seeded random fleets. TTFT rises with the rate and with
  the prompt, and falls with either role's replicas. Question 1's rate
  rises with the target and the prefill pods, and round-trips to 1e-6. The
  model refuses at the knees and at the decode onset. The union bound is
  never below the independent figure or above the even split, and the
  pooled figure is never above the per-pod one. NaN and infinite inputs are
  refused.
- **Mutants.** Each mutant listed in the P3 commit messages is killed by a
  named test, including one that reverts each part of the tail rule.

### What is UNVALIDATED, and what P2 must check

1. **`V_P` as a throughput.** Does a pod under a queue serve prompt tokens
   at the busy-minute `V_P`, and does it stay constant with prompt length
   (open question 4)?
2. **The wait tail.** How far above per-pod `request_queue_time_seconds`
   (R1's histograms) does the tail rule sit, at the real prompt spread? It
   is conservative against queueing theory. A real pod's chunked
   prefill is not the M/G/1 server it assumes.
3. **The gamma prompt.** Is a gamma with the measured cv a fair stand-in for
   the prompt histogram? P2 should compare the floor and the low-load
   quantiles.
4. **`f` and the worst pod.** P1's `f` is biased both ways
   ([where it is most wrong](#where-it-is-most-wrong)). P2 must show
   whether the worst-pod quantile, the per-pod mixture or the pooled figure
   tracks the router's histogram.
5. **The mean is the worst pod's.** Compare it with the busiest pod's
   `AvgTTFT`, not the router's mean.
6. **`a` at load.** `a` is measured at low load and added to every request.
   Check whether it grows under a queue.
7. **The union bound's conservatism.** Compare the end-to-end p90 and p95
   against `llm_d_epp_request_ttft_seconds` across the step rates.
8. **Flow-control onsets.** Do `pool_saturation` per stage and the router
   queue start where the onsets say? This is the staircase's flow-control-on
   phase (M2), not P2.
9. **`T_xfer`**, once `nixl_xfer_time_seconds` is collected (open question 3).

Until P2 and the staircase pass, no TTFT figure drives a replica count. S1
publishes in shadow only.

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
7. **Which imbalance signal for `f`?** *Recommend:* the computed-token share in
   busy minutes, de-biased; queue length when no busy minute exists.
