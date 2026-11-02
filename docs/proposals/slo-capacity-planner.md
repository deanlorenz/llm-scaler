# What-if capacity: from an SLO to a rate, and from replicas to an SLO

**Status:** proposal, 2026-11-02. Nothing here is built. The decode half reuses
arithmetic the controller runs today. The TTFT half needs a prefill model that
does not exist yet, though most of its inputs are already collected. This page
supersedes [prefill TTFT model](prefill-ttft-model.md) for how prefill
capacity is modelled (see [What it replaces](#what-it-replaces)).

**Audience:** contributors. The operator summary comes first; the derivation
follows. Operators read [the scaling policy reference](../reference/scaling-policy.md)
for what runs today.

## Operator summary

### The two questions

1. **"My SLO is a mean TPOT of 4 ms and an end-to-end TTFT of 200 ms at p90.
   What request rate can this fleet carry?"** Optionally per token bucket, such as "prompts
   of 6000 tokens and up".
2. **"I run 1 prefill and 1 decode replica. At what rate do they saturate, and
   what SLO do they hold below that?"** The answer is one pair, mean TPOT and
   TTFT at percentile `q`, at one operating rate. It is not a frontier.

TTFT here is **end to end, as the client sees it**: the router's own queue
(EPP flow control, when it is on), the engine's queue, the prefill itself, the
KV transfer and decode's first step. Both questions are answered from a
recording of a past run, from a live Prometheus with no controller installed,
or (later, only if asked) inside the controller.

### What a session looks like

The command and flags are proposed. The central values in the first two
sessions are the [worked example](#worked-example-ols-sweep): the decode side
is computed from the `ols-sweep` golden's last ITL line; the prefill throughput
`V_P` is **assumed** (30,000 tok/s), because that golden holds no evidence of
it and the tool would refuse. The bands, and the last two sessions, are
illustrative.

```
$ whatif replicas-to-slo --recording ols-sweep.jsonl.gz --cycle last \
    --model m --prefill 1 --decode 1 --ttft-percentile 90 --assume-vp 30000
knee            13.31 r/s   decode, batch cap binds at fill 0.732
operating rate  11.32 r/s   0.85 x knee (scaleUpThreshold)
mean TPOT       3.66 ms     [3.58 .. 3.79]   line ols, observed fill up to 0.62
TTFT p90        177 ms      [160 .. 231]     end to end; UNVALIDATED; V_P ASSUMED (--assume-vp)
                (154 ms if the prefill-side and decode-side waits were independent)
flags: flow control off (recorded); no KV transfer series (P/D): T_xfer omitted;
       prompt spread assumed (cv 1)
estimate from cycles 1-226, not a guarantee; see basis (-o json)

$ whatif slo-to-rate --recording ols-sweep.jsonl.gz --cycle last \
    --model m --prefill 1 --decode 1 --tpot 4ms --ttft 200ms@p90 --assume-vp 30000
role     est. rate at SLO   bound by                              basis
decode   11.32 r/s          operating-point cap (TPOT alone: 12.03)   ols [11.0 .. 11.5]
prefill  11.32 r/s          operating-point cap (TTFT alone: 11.53,   UNVALIDATED; V_P ASSUMED
                            where decode admission's wait turns on)
fleet    11.32 r/s          operating-point cap
estimate from cycles 1-226, not a guarantee; see basis (-o json)

$ whatif slo-to-rate --prometheus https://prom.example:9091 --bearer-token-file t \
    --namespace llm --lookback 1h --model m --tpot 4ms --input-bucket huge
decode   refused: NoLine (ITL window held 3 readings over fill 0.21-0.24; need spread 0.10)
exit 3

$ whatif slo-to-rate --prometheus https://prom.example:9091 --namespace llm \
    --role-of prefill=app=m-prefill --role-of decode=app=m-decode \
    --batch-cap 64 --model m --tpot 4ms
decode   9.8 r/s   TPOT, fill 0.43   pinned-B (learned B), admitted without readiness [8.9 .. 10.4]
estimate from the last 1h, not a guarantee; see basis (-o json)
```

How to read them:

- **Knee:** the rate at which a role saturates. The fleet's knee is the lower
  of the two roles'.
- **Operating rate:** 0.85 of the knee, the headroom the autoscaler keeps. The
  answers are what the fleet holds there.
- **Fill:** how full a decode replica's KV cache is, 0 to 1.
- **Bound by:** the constraint that set the rate. When several give the same
  rate, all are named; the operating-point cap is named first.
- **Bands:** the 10th and 90th percentiles of a block bootstrap, so not
  symmetric.

Every table ends with the "not a guarantee" footer.

### Modes and what each needs

| | Recording | Standalone (live) | Controller (only if asked) |
| --- | --- | --- | --- |
| Input | A learner recording, `--cycle N` or `last` | Prometheus, plus a pod-to-role mapping | The analyzers' live trackers |
| Must have | The recording | `--prometheus`, `--namespace`, and `--kubeconfig` or `--role-of` | The flag that turns it on |
| Batch cap from | Recorded store records (flags, annotation, learned) | Flags or the `llm-d.ai/max-num-seqs` annotation through a kubeconfig, else `--batch-cap`. **No learned cap** | Live, including learned |
| Length histograms | No (means only) | Yes | No |

**Standalone flags.** `--prometheus URL`, with `--bearer-token-file`,
`--ca-file` and `--insecure-skip-verify` for a managed Prometheus or Thanos;
`--namespace` (vLLM series are namespaced); `--lookback` (default 1h) and
`--step` (default 15s, the controller's cycle). Flow control is detected from
its series; `--flow-control on|off` overrides, and `--fc-queue-threshold`,
`--fc-kv-threshold` or `--fc-max-concurrency` give the detector's settings when
the EPP configuration cannot be read.

**Kubeconfig.** Read-only `get` and `list` on Pods, Deployments and
LeaderWorkerSets in `--namespace`. It reads pod labels (role, variant),
readiness and `ReadySince`, and the engine flags and annotation for the batch
cap. Nothing is written.

**`--role-of`.** `--role-of <role>=<label selector>`, repeated per role, for
example `--role-of prefill=app=m-prefill`. vLLM series carry no role or
variant label, so with neither a kubeconfig nor `--role-of` the tool stops:

```
error: cannot tell prefill from decode pods: pass --kubeconfig, or --role-of <role>=<selector>
exit 2
```

### Results and exit codes

Every answer is either an `Answer{Value, Band, Basis, Flags}` or an
`Unanswerable{Reason}`. The JSON output (`-o json`) carries a `schemaVersion`.

| Reason | When |
| --- | --- |
| `NoLine` | No ITL line, or an invalid fit |
| `PinnedDefaultB` | Only a `pinned-B` fit on the default 6 ms intercept |
| `NoCapacity` | No KV capacity reading or flag |
| `NoBatchCap` | No batch cap known (standalone without a kubeconfig or `--batch-cap`) |
| `NoRoleMap` | Standalone with neither a kubeconfig nor `--role-of` |
| `NoTraffic` | No traffic in the window, so no shape |
| `NoPrefillCapacity` | No `V_P` from a conservative source (SGLang, which has no computed-token series, always lands here) |
| `TailTPOT` | A percentile was asked of TPOT, which is a mean |
| `TargetBelowFloor` | The target is below what any number of replicas can reach |
| `FlowControlUnknown` | Standalone cannot tell whether EPP flow control is on (no flow-control series and no `--flow-control on\|off`), or it is on with a saturation detector whose thresholds are neither readable nor given (`--fc-queue-threshold`, `--fc-kv-threshold`, `--fc-max-concurrency`) |

Out-of-range inputs are **flags**, never refusals: extrapolation (with its
distance), a borrowed or held line, a lower-bound or assumed `V_P`, no
readiness, an unstable tier, a rate held down by decode admission, several
flow-control priority bands (the answer is for one band), and
**UNVALIDATED** on every TTFT figure until the
[staircase run](#validation) passes.

Exit codes: `0` every role answered; `3` at least one role refused (output still
printed); `2` usage or input error; `1` internal error.

## At a glance

| | |
| --- | --- |
| **Decode** | Reuses the ITL line and the SLO analyzer's TPOT pricing unchanged. The functions move to a `signals` package so a CLI can call them. |
| **TTFT** | End to end: the router's flow-control queue and the engine queue, modelled as one queue, plus own prefill, KV transfer, decode admission and decode's first step. Percentiles combine by convolution where the parts are independent and by a union bound where they are not; never by adding percentiles. |
| **Prefill capacity** | A token throughput `V_P`, from conservative sources only. The fixed per-request cost `a` goes into each request's own latency, not into utilization. |
| **Queue** | Per pod with the measured imbalance. A pooled queue is only an optimistic bound. |
| **Packaging** | `internal/signals/whatif` and `internal/signals/prefill`, a standalone adapter `internal/whatifsource`, a CLI `cmd/whatif`. |
| **Validated** | Decode against the goldens. TTFT against the router's TTFT histogram, the end-to-end ground truth; labelled unvalidated until a fixed-replica staircase run, with flow control off and on, passes. |
| **Recorder** | Extended first, so every recording the planner and the SLO analyzer's TTFT pricing need carries TTFT and its parts; plus `whatif capture`, which writes a recording from Prometheus with no controller. |

## Terms

| Term | Meaning |
| --- | --- |
| Fill, `k` | KV cache utilization of a replica, 0 to 1 |
| ITL line | `ITL(k) = A·k + B`, inter-token latency against fill, fitted per variant (`internal/signals/itl`) |
| Tier | Where a line came from: `ols`, a full least-squares fit; `pinned-B`, the slope fitted with the intercept held at the last full fit's (or 6 ms when there never was one); `held`, the last slope kept while too few readings exist; `borrowed`, another variant's line ([learned-state sharing](learned-state-sharing-by-similarity.md)) |
| Line check | The token rate the line predicts at a replica's own fill, against the replica's reported `generation_tokens` rate (`itl.GPSErrorPct`, 15% threshold; the analyzers call it the GPS check) |
| `KVreq` | KV tokens one in-flight request holds on average: `ILeff + OL/2` (`shape.New`) |
| `ILeff` | Prompt length after prefix-cache hits: `IL·(1 − hit)` |
| `V_P` | A prefill pod's prompt-token throughput while it has work, tokens per second |
| `a` | The fixed latency one request pays on a prefill pod, independent of length |
| Batch cap | Most sequences an engine runs at once: flags, else the `llm-d.ai/max-num-seqs` annotation, else a learned cap (#422, #429) |
| Knee | The rate at which a role saturates |
| Presence bit | A `...Known` flag beside a metric, so a missing reading is never read as zero ([learner rules](analyzer-learner.md), PR 0a) |

## Decode: TPOT from the ITL line

**TPOT is a mean.** The line is fitted on the mean inter-token latency, and the
router's TPOT is a mean too (`slo/price.go`: "TPOT is a MEAN ... no tail
model"). A percentile on TPOT gives `TailTPOT`.

A decode replica at fill `k` holds

    n(k) = min( k·C / KVreq ,  batchCap · ranks )

sequences, where `C` is its KV capacity in tokens. Each emits a token every
`ITL(k)` seconds. So TPOT is `ITL(k)`, and the replica's request rate is

    λ_rep(k) = n(k) / ( ITL(k) · OL )

This is `replicaTokenRate / OL` in the SLO analyzer and `deriveMuAtCap` in
saturation. **For a given rate** the fill has a closed form (Little's law with
residence `OL·ITL(k)`):

    k(λ_rep) = λ_rep·OL·B / ( C/KVreq − λ_rep·OL·A )

It holds while the denominator is positive and `n` is below the batch cap.

Corrections that apply:

- **Speculative decoding (#274, PR #430).** TPOT is per accepted token, so the
  line is fitted on `ITLPerToken`.
- **Output-length spread (#276, unmerged).** `KVreq = ILeff + D·OL/2`, with
  `D = E[O²]/E[O]²`.
- **Independence of prompt and output length.** The exact time-average
  footprint is `E[IL·OL]/E[OL] + E[OL²]/(2·E[OL])`. Writing it as
  `E[ILeff] + D·E[OL]/2` assumes the two are uncorrelated. When long prompts
  get long answers, KV per request is under-stated and the rate reads high.
- **The batch cap.** Short requests hit `max_num_seqs` before the cache fills,
  and `reachableFill` clamps `k` to `batchCap·KVreq/C`.

**Decode admission.** A new request waits for a sequence slot when the decode
pod is full. The pod is a birth-death chain on the number of requests `n` it
holds: arrivals at `λ_rep`, completions at

    μ_n = m / ( OL · ITL(m·KVreq/C) ),   m = min(n, c)

with `c = batchCap·ranks` slots. The chain uses the line at each state's own
fill, because a fuller pod serves each sequence more slowly. A constant ITL
(Erlang C at the mean fill) under-states the wait near the knee: in the worked
example it gives a wait probability of 0.0007 at 0.85 of the knee, against the
chain's 0.032, and 0.016 against 0.13 at 0.90. A request that finds all slots
busy waits for `j + 1` completions at rate `μ_c`, so its wait percentile
follows from the chain's distribution. When KV binds before the cap, `c` is the
sequences a **full** cache holds, `C/KVreq`, not the policy's 0.80 of it, and
the answer is flagged approximate (KV admission is by blocks, not slots). The
chain treats residence as exponential, which is the remaining approximation.
The wait is negligible until close to the knee and then rises fast: at 0.90 of
the example's knee its p90 is 203 ms. That is why question 1 caps decode at
the operating point ([below](#question-1-from-an-slo-to-a-rate)).

## TTFT: a model built from what is collected

### What exists, and what is missing

| Quantity | Series | State |
| --- | --- | --- |
| Engine TTFT mean | `QueryAvgTTFT` → `ReplicaMetrics.AvgTTFT` | Collected and recorded. No analyzer reads it |
| Service time, queue excluded | `QueryAvgServiceTime` → `AvgServiceTime`. vLLM: `request_inference_time_seconds`. SGLang: e2e minus `queue_time` | Collected and recorded |
| Prefill tokens computed per second | `QueryPrefillComputedTokenRate` | Collected, **vLLM only**. Read by saturation's prefill lower bound |
| Queue length per pod | `QueryQueueLength`, `QueryMaxRankQueueLength` | Collected |
| Router TTFT histogram | `QuerySLORouterTTFTBuckets` | Collected. The SLO analyzer judges it; nothing prices it |
| Flow-control queue size and bytes | `QuerySchedulerQueueSize`, `QuerySchedulerQueueBytes` (`llm_d_epp_flow_control_queue_size`, or the `inference_extension_` name) | Collected per model into `AnalyzerInput.SchedulerQueue`, and recorded. With the arrival rate, Little's law gives the mean router wait |
| Flow-control arrivals | `flow_control_request_enqueue_duration_seconds_count` | Collected, as the arrival rate (`QueryModelArrivalRate`) |
| Flow-control queue duration | `flow_control_request_queue_duration_seconds` (histogram; `priority`, `outcome` labels) | **Not collected** |
| Flow-control pool saturation | `flow_control_pool_saturation{stage=prefill\|decode\|effective}` | **Not collected** |
| Flow-control outcomes | `flow_control_requests_total{outcome}` (dispatched, rejected, evicted on TTL) | **Not collected** |
| Per-stage prefill time | `vllm:request_prefill_time_seconds` | **Not collected** |
| Queue-time and prompt-length histograms | `request_queue_time_seconds_bucket`, `request_prompt_tokens_bucket` | **Not collected**; only means |
| KV transfer time | `vllm:nixl_xfer_time_seconds` | **Not collected** |

**On a P/D prefill pod the split is derivable.** The sidecar asks the prefill
pod for one token, so its service time is its prefill time, and its mean wait
is `AvgTTFT − AvgServiceTime`. That difference also holds a constant overhead
(tokenization, scheduling, the HTTP path): on `ols-sweep` it is never below
2.5 ms with no queue at all. The model takes the low-load minimum of the
difference as an overhead and adds it to `a`, so the predicted wait is not
biased low. The wait derivation works for vLLM and SGLang; `V_P` does not
(SGLang has no computed-token series).

**On an aggregated pod it is not derivable.** Service time includes decode.
`AvgServiceTime − (OL − 1)·ITLPerToken` would subtract two large numbers. The
per-stage series is needed.

**What `ols-sweep` supports, measured.** 226 cycles, **one** prefill pod, its
queue length 0 on every row, prompts 983 to 6011 tokens. It supports `a`
(10.1 ms at low load) and the low-load slope, and the TTFT mean at about
`ρ = 0.1`. It does **not** support a measured `V_P` (no busy minute) or `f`
(one pod). A prefill-stress recording is part of the plan (P2).

So, plainly: **no code models TTFT.** The SLO analyzer reports whether the
router's target is met; `priceVariant` returns no-data for prefill ("TTFT is
not priced yet"); saturation and throughput mention TTFT only in comments.

### The model

End-to-end TTFT, as the client sees it:

    TTFT = W_router + W_engine + own_prefill + T_xfer + W_decode + ITL(k_d)

| Term | What it is | Model |
| --- | --- | --- |
| `W_router` | EPP flow-control queue: requests held before dispatch | [The router queue](#the-router-queue-flow-control) |
| `W_engine` | The prefill pod's own queue | Per-pod M/G/1 (flow control off) or the residual behind the gate (on) |
| `own_prefill` | `a + I/V_P`, this request's own prefill | `a` from low-load minutes plus the overhead; `V_P` below |
| `T_xfer` | KV transfer to decode (P/D) | `nixl_xfer_time_seconds` once collected; until then omitted and flagged |
| `W_decode` | Decode admission wait | The birth-death chain, [above](#decode-tpot-from-the-itl-line) |
| `ITL(k_d)` | Decode's first step, which streams the first token (P/D) | The decode line at its fill |

Which terms exist depends on the deployment:

| | Flow control off (the default) | Flow control on |
| --- | --- | --- |
| **P/D** | `W_router` = 0. `W_engine` on the prefill pod; `own_prefill`; `T_xfer`; `W_decode` on the decode pod; `ITL(k_d)` | `W_router` holds most of the wait, for prefill **and** decode capacity (the gate reads both stages); `W_engine` is a residual behind the gate; `W_decode` mostly moves into `W_router`; `own_prefill`, `T_xfer`, `ITL(k_d)` |
| **Aggregated** | `W_router` = 0. `W_engine` is the pod's queue, which also holds decode admission; `own_prefill` produces the first token, so no `T_xfer`, no separate `W_decode` or `ITL(k_d)` | `W_router`; a residual `W_engine`; `own_prefill` |

With flow control off, the router admits everything (sheddable requests, with
priority below 0, are rejected under saturation rather than queued), so the
wait is in the engines. With it on, the wait moves to the router: that is its
purpose. **The two waits are therefore one queue seen at two places, not two
independent queues**, and the model treats them so.

**Prefill capacity is a throughput.** A vLLM prefill pod schedules prompts
first come, first served, under a token budget per step
(`--max-num-batched-tokens`). Several short prompts share a step; a long one
spans several. So the pod is a work-conserving server of prompt tokens at rate
`V_P`. A prompt's own prefill duration is not a property of the pod: it
stretches when other prompts share its steps
(`constants.VLLMRequestPrefillTimeSeconds` says the same).

**Where the fixed cost goes.** `a` is measured at low load, where a prompt has
a step to itself. Under load, prompts share a forward pass and a scheduling
round, so most of `a` is paid per step, and busy-minute `V_P` is already net of
it. So `a` enters **only the request's own latency**. Utilization and the queue
use work alone:

    S_work = I_eff / V_P,    ρ_w = λ_w · E[I_eff] / V_P

**`V_P` comes only from sources that cannot over-state it**, in this order:

1. **Busy minutes.** A minute counts when the pod's queue was above zero on
   every sample in it (`max_over_time` and `min_over_time` of
   `num_requests_waiting`, both positive), and decode was not back-pressuring
   it. The back-pressure test is the one `computeK2` already applies
   (`k2ReasonObsDownstream`). `V_P` is the **median** of
   `PrefillComputedTokenRate` over at least 5 such minutes, as saturation's
   measured window uses a median. On P/D this is expected to be rare:
   saturation has never recorded a non-back-pressured prefill saturation
   there.
2. **Observed peak, under a queue.** The highest `PrefillComputedTokenRate` the
   pod sustained over at least 3 minutes **in which it held a queue on some
   sample**. Throughput achieved never exceeds capacity, so this is a lower
   bound, conservative and flagged. A peak with no queue is only the offered
   load and is not used: on `ols-sweep` the pod never queued, its peak
   (about 29,700 tok/s) is the offered load, and taking it as `V_P` would put
   that minute at `ρ = 1` and over-predict its service time about 4.6 times.
3. **Back-pressure bound.** Saturation's prefill lower-bound window
   (`prefill_bound.go`), also a lower bound. Flagged.
4. Otherwise, `NoPrefillCapacity`.

The **low-load slope** of service time against prompt length is not one of
them. A lone prompt runs as `T(I) = s0 + c·I`, and a busy pod's steps of `B`
tokens run at `B/(s0 + c·B) < 1/c`. So `1/slope ≥ V_P`, an upper bound: on
`ols-sweep` it gives about 760k tok/s against an offered peak of 30k. It is
kept as a validation signal only.

For an illustration, or a what-if on hardware not yet measured, `--assume-vp`
supplies `V_P` by hand. Every answer built on it is flagged ASSUMED.

**With flow control off, the queue is per pod.** The router sends each
request to one pod, which queues it. One run measured 439 waiting with 3 of 4
prefill pods idle: the prefill routing profile had one usable scorer, so one
pod won every request in a refresh window. A pooled M/G/c queue would promise
capacity that fleet did not have.

So each pod is an M/G/1 queue on prefill work. With `f` the imbalance (below),
the worst pod sees `λ_w = f·λ/N_p`. With `Cs² = E[I_eff²]/E[I_eff]² − 1`:

    W̄ = λ_w · E[S_work²] / (2·(1 − ρ_w))                   (Pollaczek-Khinchine)
    θ = E[S_work]·(1 + Cs²) / (2·(1 − ρ_w))                  so that W̄ = ρ_w·θ
    P(W_engine > t) ≈ ρ_w · e^(−t/θ)

The tail matches the P-K mean exactly. For bursty arrivals, `1 + Cs²` becomes
`Ca² + Cs²` (Kingman). Taking the worst pod is stricter than the router's
histogram, which mixes all pods; the tool may instead use the mixture
`Σ share_i · P(W_i > t)`. The pooled M/G/c (Allen-Cunneen) figure is printed
beside it as an optimistic bound.

**Measuring `f`.** `f` is the busiest pod's share of prefill work over the mean
share, from per-pod `PrefillComputedTokenRate`. Two corrections:

- **Sticky routing.** When one pod wins every request in a refresh window, it
  sees the whole stream for that window, and a per-minute share averages the
  rotating winners back toward 1. Where the router's refresh is known to be
  sticky, the worst pod gets `Ca²` raised by the burst (the stream arrives in
  windows of length `w`, so `Ca² ≈ 1 + λ_w·w`, which is not capped), and `f` is
  read at the finest resolution Prometheus holds.
- **Noise.** At low load the max over `N_p` Poisson-noisy minute rates exceeds
  the mean by chance. `f` is taken only over minutes with at least 100
  requests per pod, and the expected max of `N_p` Poisson counts at that mean
  is subtracted.

**Decode back-pressure.** A prefill request finishes only when decode admits
its KV. Measured: 41 queued at 77% prefill KV while decode was short, and 820
queued with decode at its ceiling. So the TTFT bisection runs only below the
decode knee, and prices decode admission at every rate it tries (next
section).

### The router queue (flow control)

**What EPP flow control does.** It is off by default (the `flowControl`
feature gate). When on, the EPP holds each request in a per-flow queue and runs
a dispatch cycle that releases **at most one request per cycle**:

1. **Gate.** A saturation detector scores the pool. The default
   (`utilization-detector`) scores each endpoint
   `max(queueDepth/5, kvUsage/0.8)` and takes the **mean** over endpoints; on
   P/D it scores the prefill and decode stages apart and gates on the worse.
   The alternative (`concurrency-detector`) gates on in-flight requests over a
   configured `maxConcurrency`. At saturation ≥ the band's ceiling (1.0 under
   the default static policy), the cycle stops: nothing dispatches, even from
   lower bands (deliberate head-of-line blocking).
2. **Band.** Priority bands are served highest first.
3. **Flow.** A fairness policy picks a flow in the band (round robin by
   default).
4. **Request.** An ordering policy picks within the flow (first come, first
   served by default).

A request waits at most its TTL (60 s by default), then is evicted; band and
global capacity limits reject at enqueue. The scheduler then picks a pod for
the dispatched request, as without flow control.

**Its metrics** (`llm_d_epp_` prefix, or `inference_extension_` on older
EPPs): `flow_control_queue_size` and `_bytes` (collected),
`flow_control_request_queue_duration_seconds` (the router wait, a histogram by
`priority` and `outcome`), `flow_control_pool_saturation` by stage, and
`flow_control_requests_total` by outcome. The last three are not collected
yet (the recorder PR adds them).

**The model.** Because the gate releases work only while the engines are below
their thresholds, the router queue and the engine queues form one queue in
tandem: the router holds the excess, and each engine holds about the
detector's threshold. So, with flow control on:

- **The pre-service wait `W_router + W_engine` is one queue.** The router is a
  central first-come queue in front of the fleet, so the wait to reach a pod is
  that of a pooled M/G/c with `c = N_p`, on prefill work.
- **Plus an engine residual.** A dispatched request still lands behind the
  requests its pod already holds. The gate bounds that: under the default
  detector a pod holds at most about `queueDepthThreshold` (5) when the fleet
  is balanced, and up to `N_p × 5` on one hot pod, because the gate reads the
  mean. The residual is bounded by that depth times `E[S_work]`, or read from
  the per-pod `num_requests_waiting` at percentile `q` when it is measured.
- **The decode gate is in the router too.** With mean decode KV at 0.8 or mean
  decode queue at 5, the router stops dispatching. Decode admission waits there
  instead of on the decode pod, and the decode knee under flow control is the
  lower of the band top, the batch-cap fill and the detector's KV threshold. It
  is counted once, as `W_decode`, never in both places.
- **With the concurrency detector**, each pod holds at most `maxConcurrency` in
  flight; the router is then a central queue in front of `N_p × maxConcurrency`
  slots, and the engine residual is at most the pod's own scheduler queue.
- **Bands.** The model is for one band under the static policy. With several
  bands or a holdback policy, a low band waits for every higher one; the answer
  is given for the highest band and flagged.

With one prefill pod the residual vanishes (the pooled and per-pod queues are
the same server), which is why the worked example's answer does not depend on
flow control.

### Combining the waits into a percentile

**Percentiles do not add.** The p95 of a sum is not the sum of the p95s:
adding them is neither safe nor exact. In the worked example at p95, adding
gives 224 ms, the dependence-free bound below gives 334 ms, and treating every
part as independent gives 210 ms.

The tool combines the parts in two blocks:

1. **The prefill side**: the pre-service wait (one queue, router and engine
   together) and the request's own prefill. These are independent (a
   request's prompt does not depend on the queue ahead of it), so they are
   **convolved** exactly. `T_xfer` joins this block.
2. **The decode side**: decode admission. It is driven by the same arrivals as
   the prefill wait, so it is not independent of block 1.

The answer is the **union bound** across the two blocks, which holds whatever
their dependence:

    TTFT_q  ≤  Q_{1 − (1−q)/2}(block 1)  +  Q_{1 − (1−q)/2}(W_decode)  +  ITL(k_d)

That is the default, and it can only over-state TTFT. The figure for two
independent blocks is printed beside it as the optimistic estimate. Where
decode admission's wait probability is below `(1−q)/2` its term is 0, and the
two figures differ only by block 1 read at `1 − (1−q)/2` rather than at `q`.

**Aggregated pods.** Prefill and decode share each step's budget, so `V_P` falls
as decode load rises. The first version reads `V_P` at the decode load it was
measured under, and flags answers far from it. A shared-budget model is later
work.

### What it replaces

[Prefill TTFT model](prefill-ttft-model.md) proposed fitting
`TTFT(T) = A·T + B` against the prompt tokens resident in the replica, taking
`1/A` as the prefill ceiling for the demand floor. It was built and removed:
TTFT includes waiting, and the intercept read 82 s. This proposal replaces the
model with `V_P`. Its consumer survives: once `internal/signals/prefill`
exists, the floor can take a derived prefill mu from `V_P / ILeff`, in a later
PR with its own switch.

## Inverting the models

### Question 1: from an SLO to a rate

**Decode, mean TPOT target `t`.** As `priceVariant` does:

1. `k* = (t − B)/A` (`priceTPOT`). If `ITL(band.lo) ≥ t`, `TargetBelowFloor`.
2. Clamp `k*` to the band and to the batch cap (`reachableFill`).
3. Rate `N_d · λ_rep(k*)`, **capped at the operating point** (0.85 of the decode
   knee), because past it the admission wait is no longer small. `--headroom 1`
   removes the cap and prints the admission wait instead.

**Prefill, TTFT target `T` at percentile `q`.** TTFT rises with the rate, so
bisect on `λ` below the lower of the two knees for the largest rate with
`TTFT_q(λ) ≤ T`, with `TTFT_q` the union bound above. The bisection prices the
router queue and `W_decode` at the same `λ`. When the prefill terms alone
would allow more, the answer is the rate decode admission holds it to, flagged
"held down by decode admission"; it is not a refusal. If `a + I_q/V_P + T_xfer + ITL(band.lo)` alone exceeds `T`,
`TargetBelowFloor`.

**The fleet** carries the smaller of the two roles, then the operating-point
cap. The answer names every constraint that gives the same rate, the cap
first, so a tie is never read as the TPOT or TTFT binding. With
`--headroom 1` the cap is lifted, and the decode admission wait is printed.

### Question 2: from replicas to an SLO

1. **The knees.**
   - Decode: `N_d · λ_rep(k_full)`, where `k_full` is the lower of the
     **band top** (the policy's `analyzerTuning.itlBand.maxObservableK`, 0.80
     by default) and the batch-cap fill. The band top is not a physical limit:
     it is the highest fill the line is fitted on, and readings above it are
     kept out because they thrash. The batch-cap fill is physical.
   - Prefill: the rate at which the worst pod reaches `ρ_w = 1`,
     `N_p·V_P / (f·E[I_eff])`.
   - The binding role is the one with the lower knee.
2. **The operating point.** `scaleUpThreshold` (0.85) times the knee, in rate,
   applied once, as `realisedFill` does, or `--rate`.
3. **The answer.** One pair at that rate: mean TPOT from the closed-form fill,
   and TTFT at percentile `q`. `q` is `--ttft-percentile`, else the policy's
   TTFT percentile, else 90. The floors are printed too: TPOT never beats
   `ITL(band.lo)`, and TTFT never beats `a + I_q/V_P + T_xfer + ITL(band.lo)`,
   the first decode step at the lowest fill.

**Two conventions for the headroom.** The tool applies 0.85 in rate.
Saturation, which drives scaling, applies it in fill (`pricingK` =
`KvCacheThreshold × ScaleUpThreshold`, 0.68 on defaults). The rate is concave
in fill, so a saturation-driven fleet runs hotter: in the worked example,
`λ_rep(0.68)` = 12.99 r/s against the tool's 11.32. `--headroom-space fill`
gives the saturation convention.

### Token buckets and the current mix

A bucket is a range of prompt length, output length, or both. The default
edges are those of `classifyOutputLength` (under 100, 500, 1500, 3000, 6000,
and above), which `classifyInputLength` reuses for prompts.

| Term | Per bucket `b` | For the current mix |
| --- | --- | --- |
| `KVreq` | `ILeff_b + D_b·OL_b/2` | `E[ILeff] + D·E[OL]/2` |
| Decode rate divisor | `OL_b` | `E[OL]`, the short-window figure `deriveMuAtCap` uses |
| Prefill work | `I_eff` drawn from the bucket | the mix's prompt distribution |
| `I_q`, `Cs²` | from the histogram restricted to the bucket | from the whole histogram |

- **An input-only bucket** (the usual question) keeps the current mix's output
  distribution, by the same independence assumption as `KVreq`.
- **The spread inside a bucket** comes from the histogram restricted to the
  bucket. Without histograms (recordings), it is an assumed coefficient of
  variation of 1, flagged. The top bucket has no upper edge, so without a
  histogram it is answered only with that flag.
- A per-bucket answer means "if all traffic looked like bucket `b`".

## Worked example: ols-sweep

From the `ols-sweep` golden: the last published line (`A` = 0.004438,
`B` = 0.001557 s), `C` = 131,072 tokens (1024 blocks of 128), batch cap 64, one
rank, one prefill and one decode pod. Shape: `IL` = `OL` = 1000, no prefix hits.
Prefill: `a` = 10.1 ms plus the 2.5 ms overhead, both from the golden.
**`V_P` = 30,000 tok/s is assumed** (`--assume-vp`): the golden's prefill pod
never queued, so the tool refuses `V_P` from it (`NoPrefillCapacity`). Prompt
spread: an assumed coefficient of variation of 1 (the recording has means
only). Flow control off, as in that run. `q` = 0.90.

| Step | Formula | Value |
| --- | --- | --- |
| `KVreq` | `IL + OL/2` | 1500 tokens |
| Batch-cap fill | `64 · 1500 / 131072` | 0.732 |
| `k_full` | min(band top 0.80, 0.732) | 0.732: the batch cap binds |
| `ITL(k_full)` | `A·0.732 + B` | 4.81 ms |
| Decode knee | `64 / (0.00481 · 1000)` | **13.31 r/s** |
| Prefill knee | `30000 / 1000` | 30.0 r/s |
| Binding role | the lower knee | decode |
| Operating rate | `0.85 · 13.31` | **11.32 r/s** |
| Decode fill at it | `11.32·1000·B / (131072/1500 − 11.32·1000·A)` | 0.474 (41.4 sequences) |
| Mean TPOT | `ITL(0.474)` | **3.66 ms** |
| Decode admission | the chain, 64 slots, at 11.32 r/s | wait probability 0.032 |
| Prefill `ρ_w` | `11.32 · 1000 / 30000` | 0.377; `θ = 0.0333·2/(2·0.623)` = 53.5 ms |
| Block 1 at p95 | wait (atom 0.623 at 0, else exponential θ) convolved with own (`12.6 ms + I/V_P`, `I` exponential, mean 1000) | 172.9 ms |
| Decode admission at p95 | wait probability 0.032 < 0.05 | 0 |
| First decode step | `ITL(0.474)` | 3.7 ms |
| **TTFT p90** | union bound: 172.9 + 0 + 3.7 (no `T_xfer`) | **177 ms** |
| TTFT p90, blocks independent | the full convolution at p90, + 3.7 | 154 ms |
| TTFT p95 | union bound: 210.0 (block 1 at p97.5) + 119.9 (decode at p97.5) + 3.7 | 334 ms (independent: 210 ms) |
| Floors | TPOT `ITL(0.15)`; TTFT `12.6 + 76.8 + ITL(0.15)` | 2.22 ms; 91.6 ms |

**Question 1 on the same fleet: mean TPOT 4 ms, TTFT p90 200 ms.**

| Step | Value |
| --- | --- |
| TPOT inverts to `k* = (0.004 − B)/A` | 0.551, under the batch cap: 12.03 r/s |
| TTFT p90 ≤ 200 ms, bisected below the decode knee | 11.53 r/s (independent: 11.75). At 11.53 decode admission's wait probability crosses 5% and its p95 term turns on (21.9 ms) |
| Operating-point cap | 11.32 r/s |
| **Answer** | **11.32 r/s**, bound by the cap; with `--headroom 1`, 11.53 r/s, bound by TTFT through decode admission |

At p95 instead, the same 200 ms allows 9.98 r/s (independent: 11.22), and TTFT
binds below the cap.

The round trip, question 1 at the SLO question 2 returned (3.661 ms unrounded,
177 ms), gives back 11.32 r/s, but only because both sit at the cap. The
property case therefore runs below it: a TPOT of 3.0 ms inverts to 9.47 r/s
and the fill there gives back 3.00 ms.

The TTFT figures are unvalidated, and on this golden rest on an assumed `V_P`.
`ols-sweep`'s prefill ran far below its capacity; it fits `a`, not `V_P`.

## Data per mode

**Recording.** The tool replays the recording through the real analyzers, as
`learnreplay` does, stops after the cycle named, and reads the learned state.
The answer is what the controller knew then. Of the real goldens, `ols-sweep`
answers decode and the prefill `a`; `pd-restart` publishes no line; `e1` and
`e1-tuned` list slo as notRun; none has router TTFT (only the synthetic
`acceptance` fixture judges a router). Recordings carry means, not histograms,
until the [recorder](#the-recorder) is extended.

**Capture.** `whatif capture` writes a recording from Prometheus alone, for a
past window, in the same format ([the recorder](#the-recorder)). It answers
offline like any recording and can become a golden.

**Standalone.** The collector's query templates (`source.QueryList.Build`) and
`prometheus.NewPrometheusSource` need only a Prometheus client. The adapter
builds `domain.ReplicaMetrics` from the series and the pod-to-role map, and
refits the lines over the lookback. Two admission gates need cluster state:

- Readiness and `ReadySince` (the settled-replica gate). Without a kubeconfig
  every pod is taken as Ready, and the fit is flagged "admitted without
  readiness".
- The P/D decode view saturation applies inside `Analyze`. Standalone reads
  decode rows as they are.

From a snapshot (the `wva-learned-state` ConfigMap) it may read an ITL line when
the lookback spans too little fill. The snapshot keeps `a, b`, the donor and the
time learned, but no fit statistics and no learned batch cap: tier "snapshot",
with its age and no band.

**Controller.** A read-only handler on the metrics server, which already sits
behind controller-runtime's authentication and authorization filter. Off by
default. Built only if someone needs answers fresher than a snapshot.

## Uncertainty

Every answer carries a **basis**: the tier of each term, how many readings,
over what range, and how old.

**Bands: a block bootstrap.** Resample the fit window's observations in blocks
of 4 consecutive cycles (the 1-minute rate windows of 15 s cycles overlap by
three quarters, so single rows are not independent), rerun admission and tier
selection, refit, re-answer. 200 resamples; the band is the 10th to 90th
percentile. When more than 10% of resamples land in a different tier, the
answer is flagged "tier unstable". The seed is fixed and printed.

Two inputs are extreme statistics, where a bootstrap is not consistent: `V_P`
from an observed peak, and `f`. They get a sensitivity sweep instead: the
answer is recomputed at ±15% of each, and the band is the widest result.

**Extrapolation.** The window records the fills it saw; a fill outside them is
flagged with the distance. Outside the band the tool clamps, as `pricingK` and
`priceTPOT` do. `V_P` and `a` record their prompt-length range the same way.

**The line check** (`GPSErrorPct`) runs on every answer, and a mismatch above
15% away from a ramp is printed beside it. For prefill, the model's predicted
per-request duration is compared with `AvgServiceTime` at the observed load.

## Where it is most wrong

| Limit | Direction | Issue |
| --- | --- | --- |
| Pooled queue, if chosen over per-pod | Over-promises | — |
| Decode assumed balanced across `N_d` | Over-promises | — |
| Exponential wait tail, heavy-tailed prompts | Over-promises | — |
| Poisson arrivals on bursty traffic | Over-promises | — |
| Router queue as one band, first come first served, static ceiling | Over-promises for low bands, under-promises for the highest | — |
| Dispatch-gate delays (metric staleness up to 200 ms, a 50 ms candidate cache) not modelled | Over-promises, slightly, near saturation | — |
| Engine residual under flow control bounded by the detector threshold | Under-promises | — |
| Requests evicted on TTL or rejected are not served, and the router histogram does not record them | The model over-promises attainment near the TTL; the ground truth reads high | slo-tiers |
| Proxy hop before the EPP is outside the router histogram | Over-promises by that hop | slo-tiers |
| Union bound across the prefill and decode sides | Under-promises (the default answer) | — |
| The independent figure printed beside it | Over-promises | — |
| `T_xfer` omitted (not collected) | Over-promises | — |
| KV-bound decode admission approximated by slots | Either way | — |
| Decode residence taken as exponential in the chain | Either way; a constant ITL instead would over-promise near the knee | — |
| Output-length spread ignored (`D` = 1) | Over-promises decode by 7-44% | #276 |
| Prefix-cache hits still occupy KV | Over-promises decode, up to about 4x under multi-turn reuse | #277 |
| Positive prompt/output correlation | Over-promises decode | — |
| ITL depends on shape on aggregated pods | Either way; about 40% of ITL at 1000/500 is prefill interference | #275 |
| SLO analyzer reads step time, not per token | Under-promises on speculating vLLM, by `1 + E` | #274 |
| No saturation line for an unlabeled aggregated variant | Use the SLO analyzer's line | #432 |
| `V_P` from a lower bound | Under-promises | — |
| Worst-pod percentile, sum of percentiles | Usually under-promises | — |

## Packaging

| Package | Holds | Layer in `hack/check-import-direction.sh` |
| --- | --- | --- |
| `internal/signals/itl` | Unchanged | 30 |
| `internal/signals/prefill` (new) | `V_P`, `a`, `f`, their windows and admission, modelled on `itl` | 30 |
| `internal/signals/whatif` (new) | Decode pricing (moved), the closed-form fill, both queues, the inversions, bands, basis, `Answer` and `Unanswerable` | **32**, its own arm before `internal/signals/*`. Above `floor` (31), so an edge from `floor` to `whatif` is caught; `whatif` imports nothing from `floor` |
| `internal/whatifsource` (new) | The standalone adapter: Prometheus series and a pod-to-role map to `domain.ReplicaMetrics`, the history refit, the readiness fallback, `--role-of` parsing, the snapshot reader | Outside the pipeline, like `controller`. The only new package importing `collector/source` and a Kubernetes client |
| `cmd/whatif` | Flags, output, exit codes | Built by a new `make whatif`; not in the manager image |

No package in the pipeline layers imports an analyzer, the engine, the
collector or a client, which respects [engine structure](engine-structure.md)
stage 3 (#345).

**What D1 moves.** From `slo/price.go`: `priceTPOT`, `reachableFill`,
`replicaTokenRate`, `realisedFill`, and the types `fillBand` and `pricing`. From
`saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor`, `derivedMu`.
`pricingK` takes the two thresholds and the band as numbers; the analyzer keeps
reading the policy. The batch-cap order stays in the analyzers
(`pricingBatchCap` and `atCapBatchCap` are methods behind replay switches); the
library takes the cap as a number. The two copies of the sequence-cap
arithmetic become one. About 64 test references in 12 `_test.go` files move or
change import (`slo/price_test.go` 28, `saturation/mu_from_itl_test.go` 10).
The paths cited in `docs/developer-guide/saturation-analyzer.md` and
`slo-analyzer.md` change.

**Accessors**, each added in the PR that first calls it:

- `itl.Tracker`: the window's observations per key (only `WindowLen` is
  exported today).
- The two analyzers: a read-only view of their trackers, batch cap and
  capacity per variant.
- Replay: one function, "replay to cycle N and return the learned state", not
  `learnreplay`'s `Options` and `Report`. `cmd/learnergolden` already imports
  `learnreplay`, so its "test support only" doc is stale; D3 corrects it.

## The recorder

The planner and the SLO analyzer's TTFT pricing (still to be built) both
learn from recordings, and both need TTFT and its parts recorded. So the
recorder is extended **first**, before any recording either depends on.

**What is already recorded.**

| Where | Field | Holds |
| --- | --- | --- |
| Every analyzer's `Analyze` event, `learnrec.Input` | `ReplicaMetrics` per pod | `AvgTTFT`, `AvgServiceTime`, `PrefillComputedTokenRate`, `QueueLength`, `AvgInputTokens`, `AvgOutputTokens` (means), `RunningRequests`, KV fields |
| same | `SchedulerQueue` | Flow-control `QueueSize` and `QueueBytes` per model |
| same | `ArrivalRate` | The EPP arrival rate per model |
| `slo.Analyze` only, `AnalyzeEvent` | `RouterLatency` | `WindowSeconds`, `TTFTBuckets` (the router TTFT histogram as per-second bucket rates, streaming only, 5-minute window), `TPOTSumRate`, `TPOTCountRate`. **Present only when the SLO analyzer is listed and judging** (`RouterCalled`) |

**What is new**, each with a presence bit so old recordings replay unchanged:

| Level | Field | Source |
| --- | --- | --- |
| Per pod | Prompt- and output-length histograms | `request_prompt_tokens_bucket`, `request_generation_tokens_bucket` |
| Per pod | Engine TTFT histogram | `time_to_first_token_seconds_bucket` |
| Per pod | Queue-time mean and histogram | `request_queue_time_seconds` |
| Per pod | Prefill-time mean | `request_prefill_time_seconds` (aggregated pods need it) |
| Per pod | KV transfer time mean | `nixl_xfer_time_seconds` |
| Per model | Router TTFT and TPOT, **on every cycle** | The SLO analyzer's queries, recorded into `learnrec.Input` whether or not the SLO analyzer runs |
| Per model | Flow-control wait histogram, pool saturation by stage, outcomes | `flow_control_request_queue_duration_seconds`, `flow_control_pool_saturation`, `flow_control_requests_total` |
| Per model | Flow control on or off, and the detector's thresholds | Presence of the series; the EPP configuration when readable |

Histograms are recorded as bucket rates over the collector's window, as
`RouterLatency.TTFTBuckets` already is. They hold numbers only, so the
export's pseudonymisation needs no new rule; its leak and coverage tests
(`learnreplay/coverage_test.go`, `export_leak*_test.go`) are extended to the new
fields. The goldens are regenerated with `learnergolden update`: their values do
not change, and the new fields are absent.

**`whatif capture`.**

```
whatif capture --prometheus URL --namespace NS (--kubeconfig K | --role-of ROLE=SELECTOR ...) \
    --from 2026-11-02T10:00:00Z --to 2026-11-02T11:00:00Z [--step 15s] -o run.jsonl.gz
```

It walks the window at the controller's cycle, runs the collector's query
templates at each step through `internal/whatifsource`, and writes one
`capture.Cycle` event per model per step, carrying a `learnrec.Input`. It
records no analyzer results: replaying a capture in `Normal` mode drives the
real analyzers and produces a full recording, which is what
`learnergolden add` takes. The output is pseudonymised by the same export as
`learnergolden export`. A capture needs Prometheus to have kept the series for
the window; for the `_bucket` series that is often days, not weeks.

**What `ols-sweep-slo` gives the TTFT work.** It ran with flow control **off**,
the SLO analyzer listed and streamed load. It carries the router TTFT
histogram (through `slo.Analyze`), and per pod `AvgTTFT`, `AvgServiceTime`,
`QueueLength` and `PrefillComputedTokenRate`. It lacks the length histograms,
engine queue-time and prefill-time, KV transfer time, any flow-control
series, and fixed replicas (it scales out). A capture of its window, if
Prometheus still holds it, adds the histograms.

## Validation

**The ground truth is the router's TTFT histogram**,
`llm_d_epp_request_ttft_seconds`. It starts when the EPP receives the request
headers, before flow control, so it includes the router queue; it misses only
the proxy hop in front of the EPP. It records streaming requests only, at end
of stream, so requests evicted or rejected are absent.

Each term is fitted, and checked, on its own series:

| Term | Fitted from | Checked against |
| --- | --- | --- |
| `W_router` | `flow_control_request_queue_duration_seconds` (new); its mean also from `SchedulerQueue.QueueSize / ArrivalRate` (recorded today) | the router queue-duration histogram |
| `W_engine` | `AvgTTFT − AvgServiceTime` on P/D prefill pods (today); `request_queue_time_seconds` (new) | per-pod queue-time histogram |
| `own_prefill` | `a` from low-load `AvgServiceTime`; `V_P` from busy minutes | `AvgServiceTime` at load; `request_prefill_time_seconds` (new) |
| `T_xfer` | `nixl_xfer_time_seconds` (new) | — |
| `W_decode` | the decode line and batch cap | the decode pod's queue time (new) |
| End to end | the sum, combined as above | the router TTFT histogram; and the mean router TTFT against the sum of the parts' means, whose residual is the proxy hop and scheduling |

### Against the goldens

Learn on the first 60% of a golden's cycles, freeze, and predict each later
cycle. The error is out of sample and signed. This is an **error report with
bounds**, not the pass mark (the goldens scale out, so none holds a fleet at the
operating point):

| Check | Measured | Bound |
| --- | --- | --- |
| Mean TPOT at the observed fill | Engine ITL mean | ±10% median signed error |
| Prefill own latency at the observed prompt | `AvgServiceTime` | ±15%; SKIPPED where `V_P` is refused or assumed (so on `ols-sweep`) |
| Decode knee | Saturated throughput windows, if the golden has at least 5 | ±15% |

A check with no data is **SKIPPED with a reason** in the output, never passed.

### The staircase (the pass mark)

The only test of the queue terms. **The prediction is made before the run**,
from a separate warm-up recording, so the run does not feed its own answer.

| Item | Specification |
| --- | --- |
| Fleet | Fixed `N_p` = 2, `N_d` = 2. ScaledObjects paused (`autoscaling.keda.sh/paused`), so nothing scales |
| Load | guidellm, constant-rate, streamed, from `test/benchmark` behind a make target |
| Buckets | A: prompts 1000, answers 1000 (decode-bound). B: prompts 8000, answers 200 (prefill-bound) |
| Start | 40% of the predicted knee |
| Steps | +10% up to 70% of the knee, then +3% to 110% |
| Hold per step | 10 minutes; the first 3 discarded (fill and 1-minute rates settle) |
| Measured per step | Router TTFT p90 and mean TPOT over the last 7 minutes |
| Runs away | TTFT p90 above twice the target for the whole measured window |
| Repeats | 3 runs; the statistic is the median over runs, per bucket |
| Flow control | Each bucket run twice: flow control **off**, then **on** (default utilization detector, one band, static ceiling). The on phase tests the tandem model, the engine residual and the decode gate in the router |
| Recorded | A learner recording on a build with the [recorder](#the-recorder) fields, with the SLO analyzer listed (observe mode); per-pod `num_requests_waiting` |

**Measured rate at SLO** is the highest step rate, interpolated linearly
between steps, whose TTFT p90 and mean TPOT meet the target.

**Pass:** at the operating point, a predicted rate more than 10% above the
measured one fails (over-promise); more than 25% below fails (under-promise).
The per-pod spread of `num_requests_waiting` is reported, as a direct test of
the per-pod model and of `f`.

**Noise floor.** An M/G/1 queue at `ρ` near 0.85 relaxes on a time of about
`E[S](1 + Cs²)/(1 − √ρ)²`, about 330 `E[S]` at `Cs²` = 1. For a 0.27 s prefill
(8000 tokens at 30k tok/s) that is about 90 s, so the 7-minute window holds
about 4.7 relaxation times. A step whose measured window holds fewer than 4 is
flagged, and the hold is lengthened for that bucket.

Until this run passes, every TTFT figure prints **UNVALIDATED**.

### Property cases

A synthetic engine with a known line, `V_P` and imbalance, in
`internal/signals/whatif/whatiftest` (the saturation generators sit in
analyzer test code, which `whatif` cannot import). Every case is
mutation-checked: a mutant of the formula must fail it.

- The rate never falls when a target loosens, and never rises when prompts or
  answers lengthen.
- Round trip, within 1e-6 relative, with `--headroom 1` and at a `--rate`
  below the operating point: question 1 at question 2's SLO gives back
  question 2's rate, at unclamped points only (fill strictly inside the band,
  `n` under the batch cap, and the rate under the operating-point cap). A
  round trip through the cap passes for any inversion, so it is not a test.
- The chain at a constant ITL reduces to Erlang C exactly.
- Every `Unanswerable` reason has a case asserting it. NaN in any input gives
  a reason, never 0 or +Inf.
- With `f` = 1 and one pod, the wait equals Pollaczek-Khinchine exactly, and
  flow control on gives the same TTFT as off.
- The union bound is never below the independent figure, and adding
  percentiles is never used.
- With flow control on, decode admission is counted once (in the router), never
  also on the decode pod.
- `V_P` from any source is never above the busy-minute median on a synthetic
  pod (the slope is excluded by construction).

## PR plan

Learner PRs follow [the learner rules](analyzer-learner.md): one behaviour
change per replay switch, goldens replayed on and off, and a property case that
fails on `main` first. **The recorder PRs land first**, so every recording made
for this work carries what it needs.

| PR | What | Goldens | Depends on |
| --- | --- | --- | --- |
| **Recorder (first)** | | | |
| R1 | Collect and record the new fields with presence bits ([the recorder](#the-recorder)): length, engine TTFT and queue-time histograms; prefill-time and KV transfer means; router TTFT/TPOT on every cycle; the flow-control wait histogram, pool saturation and outcomes. Extends the export's leak and coverage tests; updates the `metrics.go` comment. No switch: nothing reads the fields | Regenerated; values unchanged, new fields absent | — |
| R2 | `internal/whatifsource` (Prometheus to `domain.ReplicaMetrics`, the pod-to-role map, readiness fallback) and `whatif capture`, with the `capture.Cycle` event and its replay | None | R1 |
| **Recordings** (after R1) | The prefill-stress run (30k prompts, 3 prefill pods, decode below its ceiling, fixed replicas); a capture of the `ols-sweep-slo` window if Prometheus still holds it; the staircase's warm-up recording, flow control off and on | — | R1 |
| **Decode** | | | |
| D1 | Move the decode pricing into `internal/signals/whatif` at layer 32; one copy of the sequence-cap arithmetic. Proof: input/output vectors captured from `main` before the move, frozen in `testdata`, asserted bit-identical. The fails-on-main rule is waived for a pure move; the frozen vectors stand in for it | None move | — |
| D2 | Closed-form fill, both TPOT inversions, decode admission (the chain), `Answer`/`Unanswerable`, property cases | None (library only) | D1 |
| D3 | Tracker and analyzer accessors, the replay function, bands (fixed seed), basis, `cmd/whatif` from a recording or snapshot, the footer, exit codes, `make whatif` | None | D2 |
| D4 | Standalone answers from `--prometheus`. No replay switch: the readiness-less admission lives only in the adapter | None | D3, R2 |
| **Prefill and TTFT (shared library)** | | | |
| P1 | `internal/signals/prefill`, observing: `V_P` (busy minutes, observed peak under a queue, back-pressure bound), `a` and the overhead, `f`. Admission reasons, the settled-replica tracker. Logs every cycle; nothing reads it. Switch `prefill-capacity-observe`. On `ols-sweep` it fits `a` and refuses `V_P` and `f` | None (log only) | — |
| P2 | The prefill-stress recording as a golden | Adds one fixture | P1, R1, the run |
| P3 | The TTFT model in `whatif`: the per-pod queue, the router queue in tandem, the engine residual, the decode terms, the two-block union bound, both inversions; printed UNVALIDATED. No switch: the controller does not read it | None | P2, D2 |
| **The SLO analyzer prices TTFT** | | | |
| S1 | The SLO analyzer prices each prefill (and aggregated) variant against the TTFT target, **in shadow**: the rate per replica at which the union-bound TTFT meets the target, from the same `signals/prefill` and `whatif` queue model as the planner, so the two never disagree. Published as `wva_slo_*` series and the cycle log; no replica count changes. Switch `slo-ttft-price`; goldens move only with it on (`slo-published.csv`); property cases shared with P3 | Moves with the switch on | P3, a golden with router TTFT and the R1 fields |
| S2 | TTFT enforce, as [SLO tiers](slo-tiers.md) phase 2c, after S1's shadow data passes its gate | Moves | S1, the staircase |
| **Merge** | | | |
| M1 | Per-bucket and mix answers from histograms; aggregated TTFT | None | P3, R1, #276 |
| M2 | The staircase run (flow control off and on); an operator page in `docs/reference/`; drops UNVALIDATED if it passes | — | D4, M1 |
| Only if asked | The controller endpoint and two gauges | None | P3 |
| Later | Prefill line in the snapshot; the learned batch cap in the snapshot; the floor's derived prefill mu from `V_P`; shared prefill/decode budget; #275, #432, #277 | — | — |

**What S1 can use from `ols-sweep-slo`.** Router TTFT, `AvgTTFT`,
`AvgServiceTime` and the arrival rate: enough to fit `a` and to check a
predicted TTFT mean at low load. Not enough to fit `V_P` (no queue), the router
queue (flow control off) or a tail (no histograms). Its golden for S1 is
therefore the prefill-stress recording, made after R1.

## Risks

- **`f` moves.** Measured in one window, it may not hold in the next. The answer
  uses the de-biased recent value and the sensitivity sweep shows the effect.
- **Means in, percentiles out.** Recordings carry means, so their tails rest on
  an assumed spread. The staircase is the check.
- **The answer is read as a promise.** The footer, the UNVALIDATED label, the
  asymmetric pass mark and the refusals exist for that.
- **Drift from the analyzers.** D1 removes the copies first, and the SLO analyzer's
  TTFT pricing (S1) uses the same library as the planner.

## Open questions

1. **Does `V_P` vary with prompt length?** *Recommend:* key it by input bucket,
   as saturation keys its prefill windows; add a term only if bucket B of the
   staircase misses by more than 15%.
2. **Burstiness.** *Recommend:* estimate `Ca²` from the arrival rate's variance
   across cycles, cap that estimate at 4, and show it in the basis. The
   sticky-routing term is not capped.
3. **Which imbalance signal?** *Recommend:* the computed-token share in busy
   minutes, de-biased; queue length when no busy minute exists.
4. **Persist the learned batch cap in the snapshot?** *Recommend:* yes, in a
   separate PR; standalone stays on flags, annotation or `--batch-cap` until
   then.
5. **Per-request logs for the joint length distribution?** *Recommend:* an
   optional input; otherwise assume independence and say so.
6. **ITL per token in the SLO analyzer (#430's known limit).** *Recommend:* a
   separate PR with its own switch, not D1.
7. **Is the tandem model right when flow control is on?** The pooled central
   queue plus a bounded residual assumes the gate keeps engines near the
   threshold. *Recommend:* answer with it, flagged, until the staircase's
   flow-control-on phase scores it.
8. **Model the dispatch-gate delay?** The gate reads metrics up to 200 ms old
   and releases one request per cycle. *Recommend:* not until the staircase
   shows a residual at the knee larger than the proxy hop.
9. **Several priority bands.** *Recommend:* answer for the highest band and
   flag; per-band answers need per-band arrival rates, which the router
   histogram does not split.
10. **The union bound's cost.** At p95 in the worked example it is 334 ms
    against 210 ms independent. *Recommend:* keep it as the default, because it
    cannot over-promise, and print the independent figure; revisit if the
    staircase shows the two blocks are nearly independent.

## Appendix: where things are

| Piece | Where |
| --- | --- |
| The line, fit, validity | `internal/signals/itl/model.go`: `Model`, `Fit`, `ValidModel` |
| Tiers | `itl/tracker.go`: `TierOLS`, `TierPinnedB`, `Reading.SlopeHeld`; `itl/fit_pinned.go`: `FitPinnedB`, `DefaultBaselineSec` |
| Admission | `itl/admission.go`, `itl/steady.go` |
| Sequences, token rate, line check | `itl/rate.go`: `Sequences`, `TokenRate`, `GPSErrorPct` |
| Throughput from the line | `saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor`, `pricingK` |
| Batch cap | `saturation/batch_cap.go`; `capacity.EngineParams.BatchCap`, `StatedBatchCap` |
| TPOT pricing | `slo/price.go`; `slo/analyzer.go`: `priceVariant` |
| Prefill bound | `saturation/prefill_bound.go`; `computeK2`'s `k2ReasonObsDownstream` |
| Recording, replay, goldens | `internal/signals/learnrec`; `internal/engines/analyzers/learnreplay`; `cmd/learnergolden` |
| Snapshot | `saturation/learned_snapshot.go`: `LearnedState`, `LineSnapshot` |
| Queries | `internal/collector/registration`: `saturation.go`, `queueing_model.go`, `slo.go`, `throughput_analyzer.go` |

| Term | Recording | Snapshot | Live Prometheus |
| --- | --- | --- | --- |
| `(k, ITL)` per pod | `KvUsageMean`, `AvgITL` | Lines only | `QueryKvUsageMean`, `QueryAvgITL` |
| KV capacity | `TotalKvCapacityTokens` | No | `QueryCacheConfigInfo` |
| Batch cap | Store records | No | Needs the Deployment |
| Shape means | `AvgInputTokens`, `AvgOutputTokens`, `PrefixCacheHitRate` | No | Yes |
| Length histograms | No | No | Yes |
| Prefill service, wait (P/D) | `AvgServiceTime`, `AvgTTFT` | No | Yes |
| `V_P` | `PrefillComputedTokenRate` with `QueueLength` | Lower bounds only | Yes |
| Router TTFT | Only if the SLO analyzer ran (every cycle after R1) | No | `QuerySLORouterTTFTBuckets` |
| Flow-control queue size | `SchedulerQueue` | No | `QuerySchedulerQueueSize` |
| Flow-control wait histogram, saturation, outcomes | After R1 | No | Yes, queried directly |
| Arrival rate | `Input.ArrivalRate` | No | `QueryModelArrivalRate` |

## See also

- [SLO tiers](slo-tiers.md): TPOT pricing, the router's TTFT, the removed step 2a.
- [One learner lifecycle](analyzer-learner.md): the switch and golden rules.
- [Traffic shape shifts](shape-shift-treatment.md) and
  [a per-token economy](per-token-economy.md): the shape and units.
- [Capacity-planner positioning](capacity-planner-positioning.md): a planner at
  a different altitude (hours to days, cost-driven), which would call this
  model.
