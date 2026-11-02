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

1. **"My SLO is a mean TPOT of 4 ms and a TTFT of 200 ms at p90. What request
   rate can this fleet carry?"** Optionally per token bucket, such as "prompts
   of 6000 tokens and up".
2. **"I run 1 prefill and 1 decode replica. At what rate do they saturate, and
   what SLO do they hold below that?"** The answer is one pair, mean TPOT and
   TTFT at percentile `q`, at one operating rate. It is not a frontier.

Both are answered from a recording of a past run, from a live Prometheus with
no controller installed, or (later, only if asked) inside the controller.

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
TTFT p90        164 ms      [150 .. 212]     UNVALIDATED; V_P ASSUMED (--assume-vp)
flags: no KV transfer series (P/D): T_xfer omitted; prompt spread assumed (cv 1)
estimate from cycles 1-226, not a guarantee; see basis (-o json)

$ whatif slo-to-rate --recording ols-sweep.jsonl.gz --cycle last \
    --model m --prefill 1 --decode 1 --tpot 4ms --ttft 200ms@p90 --assume-vp 30000
role     est. rate at SLO   bound by                              basis
decode   11.32 r/s          operating-point cap (TPOT alone: 12.03)   ols [11.0 .. 11.5]
prefill  11.32 r/s          operating-point cap (TTFT alone: 11.87,   UNVALIDATED; V_P ASSUMED
                            held down by decode admission)
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
`--step` (default 15s, the controller's cycle).

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

Out-of-range inputs are **flags**, never refusals: extrapolation (with its
distance), a borrowed or held line, a lower-bound or assumed `V_P`, no
readiness, an unstable tier, a rate held down by decode admission, and
**UNVALIDATED** on every TTFT figure until the
[staircase run](#validation) passes.

Exit codes: `0` every role answered; `3` at least one role refused (output still
printed); `2` usage or input error; `1` internal error.

## At a glance

| | |
| --- | --- |
| **Decode** | Reuses the ITL line and the SLO analyzer's TPOT pricing unchanged. The functions move to a `signals` package so a CLI can call them. |
| **TTFT** | A new model: own prefill latency, plus a per-pod queue on prefill work, plus decode admission, KV transfer and decode's first step on P/D. |
| **Prefill capacity** | A token throughput `V_P`, from conservative sources only. The fixed per-request cost `a` goes into each request's own latency, not into utilization. |
| **Queue** | Per pod with the measured imbalance. A pooled queue is only an optimistic bound. |
| **Packaging** | `internal/signals/whatif` and `internal/signals/prefill`, a standalone adapter `internal/whatifsource`, a CLI `cmd/whatif`. |
| **Validated** | Decode against the goldens. TTFT is labelled unvalidated until a fixed-replica staircase run passes. |

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
(one pod). A prefill-stress recording is part of the plan (PR 5b).

So, plainly: **no code models TTFT.** The SLO analyzer reports whether the
router's target is met; `priceVariant` returns no-data for prefill ("TTFT is
not priced yet"); saturation and throughput mention TTFT only in comments.

### The model

TTFT at the router, on P/D:

    TTFT = W_router + W_prefill + own_prefill + T_xfer + W_decode + ITL(k_d)

| Term | What it is | From |
| --- | --- | --- |
| `W_router` | EPP flow-control queue | Not separable. Taken as 0, which over-promises |
| `W_prefill` | Wait on the chosen prefill pod | The per-pod queue below |
| `own_prefill` | `a + I/V_P`, this request's own prefill | `a` from low-load minutes plus the overhead; `V_P` below |
| `T_xfer` | KV transfer to decode | `nixl_xfer_time_seconds` once collected; until then omitted and flagged |
| `W_decode` | Decode admission wait | The birth-death chain, [above](#decode-tpot-from-the-itl-line) |
| `ITL(k_d)` | Decode's first step, which streams the first token | The decode line at its fill |

On an aggregated pod the first token comes from the same pod, so the last
three terms collapse into `own_prefill`.

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

**The queue is per pod.** The router sends each request to one pod, which
queues it. One run measured 439 waiting with 3 of 4 prefill pods idle: the
prefill routing profile had one usable scorer, so one pod won every request in
a refresh window. A pooled M/G/c queue would promise capacity that fleet did
not have.

So each pod is an M/G/1 queue on prefill work. With `f` the imbalance (below),
the worst pod sees `λ_w = f·λ/N_p`. With `Cs² = E[I_eff²]/E[I_eff]² − 1`:

    W̄ = λ_w · E[S_work²] / (2·(1 − ρ_w))                   (Pollaczek-Khinchine)
    θ = E[S_work]·(1 + Cs²) / (2·(1 − ρ_w))                  so that W̄ = ρ_w·θ
    W_q = max( 0,  θ · ln( ρ_w / (1 − q) ) )                 from P(W > t) ≈ ρ_w·e^(−t/θ)

The tail matches the P-K mean exactly. For bursty arrivals, `1 + Cs²` becomes
`Ca² + Cs²` (Kingman). The TTFT percentile is then

    TTFT_q ≈ W_q + (a + I_q/V_P) + T_xfer + W_decode,q + ITL(k_d)

with `I_q` the prompt length at percentile `q`. Taking the worst pod's
percentile is stricter than the router's histogram, which mixes all pods; the
tool may instead bisect the mixture `Σ share_i · P(W_i > t) = 1 − q`, which is
cheap. Adding percentiles is usually conservative, not always with heavy
tails. The pooled M/G/c (Allen-Cunneen) figure is printed beside it as an
optimistic bound.

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
`TTFT_q(λ) ≤ T`. The bisection prices `W_decode` from the chain at the same
`λ`. When the prefill terms alone would allow more, the answer is the rate
decode admission holds it to, flagged "held down by decode admission"; it is
not a refusal. If `a + I_q/V_P + T_xfer + ITL(band.lo)` alone exceeds `T`,
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
only). `q` = 0.90.

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
| Decode admission | the chain, 64 slots, at 11.32 r/s | wait probability 0.032; p90 wait 0 |
| Prefill `ρ_w` | `11.32 · 1000 / 30000` | 0.377 |
| Prefill wait p90 | `θ = 0.0333·2/(2·0.623)` = 53.5 ms; `θ·ln(0.377/0.10)` | 71.1 ms |
| Own prefill p90 | `I_q` = 2303 tokens; `0.0126 + 2303/30000` | 89.4 ms |
| First decode step | `ITL(0.474)` | 3.7 ms |
| **TTFT p90** | 71.1 + 89.4 + 0 + 3.7 (no `T_xfer`) | **164 ms** |
| Floors | TPOT `ITL(0.15)`; TTFT `12.6 + 76.8 + ITL(0.15)` | 2.22 ms; 91.6 ms |

**Question 1 on the same fleet: mean TPOT 4 ms, TTFT p90 200 ms.**

| Step | Value |
| --- | --- |
| TPOT inverts to `k* = (0.004 − B)/A` | 0.551, under the batch cap: 12.03 r/s |
| TTFT, prefill terms alone | 14.87 r/s, past the decode knee |
| TTFT, bisected below the decode knee with decode admission priced | 11.87 r/s, held down by decode admission (its p90 wait reaches 200 ms − the other terms there) |
| Operating-point cap | 11.32 r/s |
| **Answer** | **11.32 r/s**, bound by the cap; with `--headroom 1`, 11.87 r/s, bound by TTFT through decode admission |

The round trip, question 1 at the SLO question 2 returned (3.661 ms unrounded,
164 ms), gives back 11.32 r/s, but only because both sit at the cap. The
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
`acceptance` fixture judges a router). Recordings carry means, not histograms.

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
| `W_router` taken as 0 | Over-promises | slo-tiers open question 5 |
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

**What PR 1 moves.** From `slo/price.go`: `priceTPOT`, `reachableFill`,
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
  `learnreplay`, so its "test support only" doc is stale; PR 3 corrects it.

## Validation

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
| Recorded | A learner recording with the SLO analyzer listed (observe mode), Prometheus kept with the `_bucket` series, per-pod `num_requests_waiting` |

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
- With `f` = 1 and one pod, the wait equals Pollaczek-Khinchine exactly.
- `V_P` from any source is never above the busy-minute median on a synthetic
  pod (the slope is excluded by construction).

## PR plan

Learner PRs follow [the learner rules](analyzer-learner.md): one behaviour
change per replay switch, goldens replayed on and off, and a property case that
fails on `main` first.

| PR | What | Goldens | Depends on |
| --- | --- | --- | --- |
| **Decode** | | | |
| 1 | Move the decode pricing into `internal/signals/whatif` at layer 32; one copy of the sequence-cap arithmetic. Proof: input/output vectors captured from `main` before the move, frozen in `testdata`, asserted bit-identical. The fails-on-main rule is waived for a pure move; the frozen vectors stand in for it | None move | — |
| 2 | Closed-form fill, both TPOT inversions, decode admission, `Answer`/`Unanswerable`, property cases | None (library only) | 1 |
| 3 | Tracker and analyzer accessors, the replay function, bands (fixed seed), basis, `cmd/whatif` from a recording or snapshot, the footer, exit codes, `make whatif` | None | 2 |
| 4 | `internal/whatifsource` and `--prometheus`. No replay switch: no controller path changes; the readiness-less admission lives only in the adapter | None | 3 |
| **TTFT** | | | |
| 5a | `internal/signals/prefill`, observing: `V_P` (busy minutes, observed peak under a queue, back-pressure bound), `a` and the overhead from low-load minutes, `f`. Admission reasons and the settled-replica tracker. Logs every cycle; nothing reads it. Switch `prefill-capacity-observe`. On `ols-sweep` it fits `a` and the overhead, and refuses `V_P` and `f` | None (log only) | — |
| 5b | A prefill-stress recording added as a golden: single phase, prefill-heavy (30k prompts), 3 prefill pods, decode held below its ceiling, fixed replicas. It exercises busy minutes and `f` | Adds one fixture | 5a, the run |
| 5c | TTFT in `whatif`: the per-pod queue, the pooled bound, the decode terms, both inversions; printed UNVALIDATED. No switch: the controller does not read it | None | 5b, 2 |
| 6 | Collect what is missing, with presence bits: `request_prefill_time_seconds` (aggregated pods), the queue-time and prompt-length `_bucket` series, `nixl_xfer_time_seconds`. Updates `learnreplay`'s export leak and coverage tests and the `metrics.go` comment. Old recordings replay with the presence bits false | Regenerated by `learnergolden update`; values unchanged, new columns empty | — |
| **Merge** | | | |
| 7 | Per-bucket and mix answers from histograms; aggregated TTFT | None | 5c, 6, #276 |
| 8 | The staircase run; an operator page in `docs/reference/`; drops UNVALIDATED if it passes | — | 4, 7 |
| Only if asked | The controller endpoint and two gauges (the knee at the current count, TPOT at the current rate) | None | 5c |
| Later | Prefill line in the snapshot; the learned batch cap in the snapshot (open question 4); the floor's derived prefill mu from `V_P`; shared prefill/decode budget; #275, #432, #277 | — | — |

The ols-sweep-slo run, in progress on 2026-11-02, scales out, so it cannot
exercise the queue term. It does carry router TTFT. Export its Prometheus data
for the same window now, while the raw `_bucket`, prefill-time and queue-time
series are retained.

## Risks

- **`f` moves.** Measured in one window, it may not hold in the next. The answer
  uses the de-biased recent value and the sensitivity sweep shows the effect.
- **Means in, percentiles out.** Recordings carry means, so their tails rest on
  an assumed spread. The staircase is the check.
- **The answer is read as a promise.** The footer, the UNVALIDATED label, the
  asymmetric pass mark and the refusals exist for that.
- **Drift from the analyzers.** PR 1 removes the copies first.

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
   separate PR with its own switch, not PR 1.

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
| Router TTFT | Only if the SLO analyzer ran | No | `QuerySLORouterTTFTBuckets` |
| Arrival rate | `Input.ArrivalRate` | No | `QueryModelArrivalRate` |

## See also

- [SLO tiers](slo-tiers.md): TPOT pricing, the router's TTFT, the removed step 2a.
- [One learner lifecycle](analyzer-learner.md): the switch and golden rules.
- [Traffic shape shifts](shape-shift-treatment.md) and
  [a per-token economy](per-token-economy.md): the shape and units.
- [Capacity-planner positioning](capacity-planner-positioning.md): a planner at
  a different altitude (hours to days, cost-driven), which would call this
  model.
