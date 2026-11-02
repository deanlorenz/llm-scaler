# What-if capacity: from an SLO to a rate, and from replicas to an SLO

**Status:** proposal, 2026-11-02. Nothing here is built. The decode half reuses
arithmetic the controller runs today. The TTFT half needs a prefill model that
does not exist yet, though most of its inputs are already collected. This page
supersedes [prefill TTFT model](prefill-ttft-model.md) for how prefill
capacity is modelled (see [What it replaces](#what-it-replaces)).

**Audience:** contributors. Operators read
[the scaling policy reference](../reference/scaling-policy.md) for what runs
today.

## The two questions

1. **"My SLO is a TTFT of 2 s at p90 and a mean TPOT of 50 ms. What request
   rate can this fleet carry?"** Optionally per token bucket, such as "8k
   prompts with 500-token answers".
2. **"I run 2 prefill and 6 decode replicas. At what rate do they saturate,
   and what SLO do they hold below that?"** Optionally per token bucket.

Both are answered in three places: from a recording of a past run, from a live
Prometheus with no controller installed, and inside the controller. Every
answer names the evidence it rests on. With no evidence, the tool refuses
rather than guessing.

A session would look like this. The command and flags are proposed, and the
numbers are illustrative:

```
$ whatif slo-to-rate --recording run.jsonl.gz --model m --tpot 50ms --ttft 2s@p90
role     replicas  max rate   bound by           basis
decode   6         31.4 r/s   TPOT, fill 0.52    ols (n=212, fill 0.18-0.71) ±6%
prefill  2         19.0 r/s   TTFT p90, worst pod V_P from 41 busy minutes ±18%
fleet              19.0 r/s   prefill TTFT

$ whatif replicas-to-slo --prometheus http://prom:9090 --kubeconfig ~/.kube/config \
    --model m --prefill 2 --decode 6
knee 26.8 r/s (prefill, physical: worst pod at rho=1)
operating point 0.85 x knee = 22.8 r/s: TPOT 41 ms (mean), TTFT p90 1.6 s
```

The knee is physical for both roles. The 0.85 operating fraction is the
autoscaler's `scaleUpThreshold`, applied once (see
[Question 2](#question-2-from-replicas-to-an-slo)).

## At a glance

| | |
| --- | --- |
| **Decode** | Reuses the ITL line and the SLO analyzer's TPOT pricing unchanged. The functions move to a `signals` package so a CLI can call them. |
| **TTFT** | A new model, built mostly from series the collector already reads: queue wait per pod, plus prefill service, plus KV transfer and the decode side's first step on P/D. |
| **Prefill capacity** | A throughput: tokens per second a pod computes while busy. It is not the slope of per-request prefill time, which stretches under batching. |
| **Queue** | Per-pod queues with the measured imbalance by default. A pooled queue is only an optimistic bound. |
| **Packaging** | `internal/signals/whatif` and `internal/signals/prefill`, a CLI `cmd/whatif`, and an optional read-only endpoint. |
| **Refusals** | No line, no capacity, no pod-to-role mapping, or a percentile on TPOT each give a refusal, not a number. |

## Terms

| Term | Meaning |
| --- | --- |
| Fill, `k` | KV cache utilization of a replica, 0 to 1 |
| ITL line | `ITL(k) = A·k + B`, inter-token latency against fill, fitted per variant (`internal/signals/itl`) |
| Tier | Where a line came from: `ols` is a full least-squares fit; `pinned-B` fits the slope with the intercept held at the last full fit's (or 6 ms if there never was one); `held` keeps the last slope while too few readings exist; `borrowed` is another variant's line ([learned-state sharing](learned-state-sharing-by-similarity.md)) |
| Line check | Compare the token rate the line predicts at a replica's own fill with the replica's reported `generation_tokens` rate (`itl.GPSErrorPct`, 15% threshold). The analyzers call it the GPS check |
| `KVreq` | KV tokens one in-flight request holds on average: `ILeff + OL/2` (`shape.New`) |
| `ILeff` | Prompt length after prefix-cache hits: `IL·(1 − hit)` |
| `V_P` | A prefill pod's token throughput while busy, tokens per second |
| Batch cap | Most sequences an engine runs at once: the flags, else the `llm-d.ai/max-num-seqs` annotation, else a learned cap (#422, #429) |
| Knee | The rate at which a role saturates physically |
| Presence bit | A `...Known` flag beside a metric, so a missing reading is never read as zero ([learner rules](analyzer-learner.md) PR 0a) |

## Decode: TPOT from the ITL line

**TPOT is a mean.** The line is fitted on the mean inter-token latency and the
router's TPOT is a mean too (`slo/price.go`: "TPOT is a MEAN ... no tail
model"). The tool takes a mean TPOT target and refuses `--tpot 50ms@p99`: no
tail model exists for TPOT.

A decode replica at fill `k` holds

    n(k) = min( k·C / KVreq ,  batchCap · ranks )

sequences, where `C` is its KV capacity in tokens. Each emits a token every
`ITL(k)` seconds. So TPOT is `ITL(k)`, and the replica's request rate is

    λ_rep(k) = n(k) / ( ITL(k) · OL )

This is `replicaTokenRate / OL` from the SLO analyzer, and `deriveMuAtCap` from
saturation. **For a given rate** the fill has a closed form (Little's law with
residence `OL·ITL(k)`):

    k(λ_rep) = λ_rep·OL·B / ( C/KVreq − λ_rep·OL·A )

It holds while the denominator is positive and `n` is below the batch cap.

Corrections that apply:

- **Speculative decoding (#274, PR #430).** TPOT is per accepted token, so the
  line is fitted on `ITLPerToken`.
- **Output-length spread (#276, unmerged branch).** `KVreq = ILeff + D·OL/2`,
  with `D = E[O²]/E[O]²`.
- **Independence of prompt and output length.** The exact time-average
  footprint is `E[IL·OL]/E[OL] + E[OL²]/(2·E[OL])`. Writing it as
  `E[ILeff] + D·E[OL]/2` assumes the two lengths are uncorrelated. When long
  prompts get long answers, KV per request is under-stated and the decode rate
  reads high.
- **The batch cap.** Short requests hit `max_num_seqs` before the cache fills.
  `reachableFill` then clamps `k` to `batchCap·KVreq/C`.

## TTFT: a model built from what is collected

### What exists, and what is missing

Most of what a TTFT model needs is already collected per pod and recorded.
Nothing reads it for TTFT.

| Quantity | Series | State |
| --- | --- | --- |
| Engine TTFT mean | `QueryAvgTTFT` → `ReplicaMetrics.AvgTTFT` | Collected and recorded. No analyzer reads it |
| Service time, queue excluded | `QueryAvgServiceTime` → `AvgServiceTime`. vLLM: `request_inference_time_seconds`. SGLang: e2e minus `queue_time` | Collected and recorded |
| Prefill tokens computed per second | `QueryPrefillComputedTokenRate` | Collected, vLLM only. Read by saturation's prefill lower bound |
| Queue length per pod | `QueryQueueLength`, `QueryMaxRankQueueLength` | Collected |
| Router TTFT histogram | `QuerySLORouterTTFTBuckets` | Collected. The SLO analyzer judges it; nothing prices it |
| Per-stage prefill time | `vllm:request_prefill_time_seconds` (`constants.VLLMRequestPrefillTimeSeconds`) | **Not collected** |
| Queue-time histogram | `vllm:request_queue_time_seconds_bucket` | **Not collected**; the mean is derivable (below) |
| Prompt-length histogram | `vllm:request_prompt_tokens_bucket` | **Not collected**; only `_sum`/`_count` |
| KV transfer time | `vllm:nixl_xfer_time_seconds` | **Not collected** |

**On a P/D prefill pod, the per-stage split is derivable today.** The P/D
sidecar asks the prefill pod for one token (`slo/analyzer.go`, `priceVariant`),
so its service time is its prefill time. Its mean queue wait is
`AvgTTFT − AvgServiceTime`. This works for vLLM and SGLang. The `ols-sweep`
golden already carries both fields on its prefill rows.

**On an aggregated pod it is not.** Service time there includes decode.
`AvgServiceTime − (OL − 1)·ITLPerToken` would recover prefill, but it subtracts
two large numbers. The per-stage series is needed.

So the gap, stated plainly: **no code models TTFT.** The SLO analyzer reports
whether the router's TTFT target is met. `priceVariant` returns no-data for
prefill ("TTFT is not priced yet"). Saturation and throughput mention TTFT only
in comments. The inputs, by contrast, are mostly there.

### The model

TTFT at the router, on P/D:

    TTFT = W_router + W_prefill + S_prefill + T_xfer + W_decode + ITL(k_d)

| Term | What it is | From |
| --- | --- | --- |
| `W_router` | EPP flow-control queue | Only inside the router histogram; not separable. Treated as 0 below the knee, which over-promises |
| `W_prefill` | Wait on the chosen prefill pod | The queue model below |
| `S_prefill` | Prefill service: `a + I_eff/V_P` | The prefill model below |
| `T_xfer` | KV transfer to decode | `nixl_xfer_time_seconds` once collected. Until then absent and flagged |
| `W_decode` | Decode admission wait | 0 below the decode knee: the decode pod has KV room. At or above it, the answer is "TTFT bound by decode" |
| `ITL(k_d)` | Decode's first step, which streams the first token | The decode line at its fill |

On an aggregated pod, the first token comes from the same pod, so the last
three terms collapse into `S_prefill`.

**Prefill capacity is a throughput.** A vLLM prefill pod schedules prompts
first come, first served, under a token budget per step
(`--max-num-batched-tokens`). Several short prompts share a step; a long one
spans several steps. So the pod is a work-conserving server of prompt tokens.
Its rate is `V_P`, the tokens per second it computes while it has work.

A prompt's own prefill duration is **not** a property of the pod. It stretches
when other prompts share its steps. The constant's own comment says so:
"concurrent requests each report their own prefill duration, so the sum
over-counts wall-clock". Fitting `V_P` as the inverse slope of per-request
duration would put waiting-like time back into the regressand, the defect
that removed the first attempt.

`V_P` therefore comes from throughput, in this order:

1. **Busy minutes.** `PrefillComputedTokenRate` in minutes when the pod had
   prompts waiting and decode was not back-pressuring it. Then the pod is
   computing at its rate. This is the reading saturation already treats as a
   saturated prefill reading.
2. **Lower bounds.** Saturation's prefill lower-bound window
   (`prefill_bound.go`), or the isolated-request slope at low load. A single
   prompt at low load does not fill the step budget, so `1/slope` there
   under-states `V_P`. Both under-state, so both give conservative answers,
   and they are flagged.
3. Otherwise, refuse.

`a`, the fixed cost per request, is the intercept of `AvgServiceTime` against
`ILeff` in **low-load** minutes only, where a prompt rarely shares a step. The
per-request duration at higher load is kept as a validation signal: the model
must predict it, not be fitted on it.

A request's service demand is then `S = a + I_eff/V_P`. From the prompt-length
distribution:

    E[S] = a + E[I_eff]/V_P
    E[S²] = a² + 2a·E[I_eff]/V_P + E[I_eff²]/V_P²
    Cs² = E[S²]/E[S]² − 1

**The queue is per pod.** The router sends each request to one pod and the
engine queues it there. One run measured 439 waiting with 3 of 4 prefill pods
idle: the prefill routing profile had one usable scorer, so one pod won
every request in a refresh window. A pooled M/G/c queue, where any idle pod takes the next request,
promises capacity this fleet does not have.

So each pod is an M/G/1 queue, fed its share of the arrivals. Let `f` be the
imbalance: the busiest pod's share of the prefill work over the mean share,
measured from per-pod `PrefillComputedTokenRate`, or from `QueueLength` when
the pods are busy. The worst pod sees

    λ_w = f · λ / N_p,    ρ_w = λ_w · E[S]

Its mean wait is Pollaczek-Khinchine, `W̄ = λ_w·E[S²] / (2·(1 − ρ_w))`. For the
percentile `q`, the standard exponential tail gives

    W_q = max( 0,  E[S]·(1 + Cs²) / (2·(1 − ρ_w)) · ln( ρ_w / (1 − q) ) )

and the TTFT percentile is approximated as

    TTFT_q ≈ W_q + S(I_q) + T_xfer + ITL(k_d)

with `I_q` the prompt length at percentile `q`. The answer is the worst pod's.
The pooled M/G/c (Allen-Cunneen) figure is reported beside it as an optimistic
bound, and becomes the default only once a run shows the router balancing
prefill (`f` near 1).

Arrivals are taken as Poisson. Bursty traffic waits longer: the general form
replaces `1 + Cs²` by `Ca² + Cs²`, with `Ca²` estimated from the arrival rate's
variance across cycles (open question 2).

**Decode back-pressure.** A prefill request finishes only when decode admits
its KV. Measured: 41 queued at 77% prefill KV while decode was short, and 820
queued with decode at its ceiling. So the tool checks decode first. Above the
decode knee, TTFT is reported as "bound by decode", not priced.

**Aggregated pods.** Prefill and decode share each step's token budget, so `V_P`
falls as decode load rises. The first version reads `V_P` from the pod's own
busy minutes, at the decode load they had, and marks answers far from that
load **low confidence**. A shared-budget model is later work.

### What it replaces

[Prefill TTFT model](prefill-ttft-model.md) proposed fitting
`TTFT(T) = A·T + B` against the prompt tokens resident in the replica
(`num_requests_running × avgInput`), taking `1/A` as the prefill ceiling for
the demand floor's `SaturatedThroughputDerived`. It was built and removed:
TTFT includes waiting, and the intercept read 82 s. This proposal replaces its
model with `V_P` from busy-minute throughput. Its consumer survives: once
`internal/signals/prefill` exists, the floor can take a derived prefill mu
(requests per second a replica serves) from `V_P / ILeff`. That is a
later, separate PR with its own switch.

## Inverting the models

### Question 1: from an SLO to a rate

**Decode, mean TPOT target `t`.** Exactly `priceVariant`:

1. `k* = (t − B)/A` (`priceTPOT`). If `ITL(band.lo) ≥ t`, no fill meets the
   target on this variant: say so; never answer zero.
2. Clamp `k*` to the band `[0.15, 0.80]` and to the batch cap
   (`reachableFill`).
3. The fleet carries `N_d · replicaTokenRate(k*) / OL`.

**Prefill, TTFT target `T` at percentile `q`.** `TTFT_q` rises with the rate,
so bisect on `λ` below the prefill knee. The answer is the largest `λ` with
`TTFT_q(λ) ≤ T`. If `S(I_q) + T_xfer + ITL(k_d) ≥ T`, the target is infeasible
at any rate: the prompt alone takes longer. Say so.

**The fleet** carries the smaller of the two, and the answer names the binding
role and objective.

### Question 2: from replicas to an SLO

1. **The knee, physical for both roles.**
   - Decode: `N_d · λ_rep(k_full)`, with `k_full` the lower of the band's top
     (0.80) and the batch-cap fill. **Not** `deriveMuAtCap`, which prices at
     `pricingK = KvCacheThreshold × ScaleUpThreshold` (0.68 on defaults) and so
     already contains the headroom.
   - Prefill: the rate at which the worst pod reaches `ρ = 1`:
     `N_p / (f · E[S])`.
   - The binding role is the one with the lower knee.
2. **The operating point.** `scaleUpThreshold` (0.85) times the knee, applied
   once, as `realisedFill` applies it for the SLO analyzer. The user may pass
   `--rate` instead.
3. **The SLO there.** TPOT from the closed-form fill at that rate, and TTFT from
   the queue model. Also a table of both from zero to the knee.

The floor of each is reported too: TPOT can never beat `ITL(band.lo)`, and TTFT
can never beat `S(I_q) + T_xfer + ITL(k_d)`. No number of replicas buys less.

### Token buckets and the current mix

A bucket is a box of prompt length and output length. The default edges are
those of `classifyOutputLength` (under 100, 500, 1500, 3000, 6000, and above),
which `classifyInputLength` reuses for prompts.

| Term | Per bucket `b` | For the current mix |
| --- | --- | --- |
| `KVreq` | `ILeff_b + D_b·OL_b/2` | `E[ILeff] + D·E[OL]/2` |
| Decode rate divisor | `OL_b` | `E[OL]`, the short-window figure, as `deriveMuAtCap` uses |
| `S` (prefill) | `a + ILeff_b/V_P` | `E[S]`, `E[S²]` from the prompt histogram |
| `I_q` | inside the bucket | the mix's prompt percentile |

A per-bucket answer means "if all traffic looked like bucket `b`". Prometheus
gives the two length histograms separately. The mix answer therefore assumes
independence (see Decode), unless a per-request log from the load generator
supplies the joint distribution.

## Modes

| | Recording (offline) | Standalone (live, no controller) | Inside the controller |
| --- | --- | --- | --- |
| Source | A learner recording, replayed | Prometheus, plus a pod-to-role mapping | The analyzers' own trackers |
| ITL line | Replayed tracker | Refitted from history | Live tracker |
| Prefill `V_P`, `a` | From recorded rows | From history | From `signals/prefill`, once built |
| Batch cap | Recorded store records | Flags or annotation through a kubeconfig, or `--batch-cap` | Live |
| Histograms | No (means only) | Yes | No |

**Recording.** The tool replays the recording through the real analyzers, as
`learnreplay` does, stops after the cycle the user names, and reads the
learned state. The answer is what the controller knew at that moment. The real
goldens fit only part of this: `ols-sweep` (226 cycles, P/D, 195 OLS lines)
answers decode and the P/D prefill mean model. `pd-restart` publishes no line.
No real golden has router TTFT events (`e1` and `e1-tuned` list slo as notRun;
only the synthetic `acceptance` fixture judges a router). Recordings carry
means, not histograms, so the tail terms assume a prompt-length spread (a
coefficient of variation of 1) and flag it.

**Standalone.** The collector's query templates (`source.QueryList.Build`)
and `prometheus.NewPrometheusSource` need only a Prometheus client. Turning
series into `domain.ReplicaMetrics` does need Kubernetes: vLLM series carry no
role or variant label, and those are pod labels that reach the series only
through a ServiceMonitor relabel. So the standalone mode **requires** a
mapping: a kubeconfig, or a `--role-of` flag of pod-name selectors. Without
one it refuses.

It refits the lines from a lookback of history, one row per 15 s step. Two
admission gates need cluster state it may not have:

- Readiness and `ReadySince`, which feed the settled-replica gate. Without a
  kubeconfig every pod is taken as Ready, and the fit is flagged "admitted
  without readiness".
- The P/D decode view that saturation applies inside `Analyze`. Standalone
  reads decode rows as they are.

From a snapshot (the `wva-learned-state` ConfigMap) it may read an ITL line when
the lookback spans too little fill. The snapshot keeps `a, b`, the donor and
the time learned, but no fit statistics and no learned batch cap. Such a line
is tier "snapshot", with its age and no band.

**Inside the controller.** A read-only HTTP handler on the metrics server
answers from the live trackers, behind a flag, off by default. Not a metric:
the questions take parameters. Two gauges per variant are worth publishing,
the knee at the current replica count and the predicted TPOT at the current
rate ([signals as metrics](signals-as-metrics.md)).

## Uncertainty and refusals

Every answer carries a **basis**: which tier each term came from, how many
readings, over what range, and how old.

| Term | Answer | Answer and flag | Refuse |
| --- | --- | --- | --- |
| ITL line | `ols`, steady spread met | `pinned-B` on a learned intercept; `held`; `borrowed` (names the donor); snapshot | No line; an invalid fit; `pinned-B` on the default 6 ms intercept |
| KV capacity `C` | Live `cache_config_info` | The flag | Neither |
| Batch cap | Flags, or the annotation | Learned | None known (the CLI accepts `--batch-cap`) |
| `V_P` | Own busy minutes | Lower bound | Neither |
| Pod-to-role mapping | Kubeconfig | `--role-of` | Neither (standalone) |
| `T_xfer` (P/D) | Collected | Absent: answer without it | — |
| Shape | Histograms | Means only (`D = 1`, assumed prompt spread) | No traffic in the window |

**Bands.** The library takes a parametric bootstrap from the fit's window:
resample the observations, refit, re-answer, and report the 10th and 90th
percentile answers. That respects the covariance of `A` and `B`, and the
`1/(1 − ρ)` amplification near the knee. `itl.Fit` returns no standard errors
today; the window's observations are enough.

**Extrapolation.** The window records the fills it saw. A fill outside them is
flagged with the distance. Outside the band `[0.15, 0.80]` the tool clamps, as
`pricingK` and `priceTPOT` do: above 0.80 the engine preempts rather than
slowing down. `V_P` and `a` record their prompt-length range the same way.

**The line check** (`GPSErrorPct`) runs on every answer, and a mismatch above
15% away from a ramp is shown beside it. Prefill's equivalent compares the
predicted per-request duration with `AvgServiceTime` at the observed load.

## Where it is most wrong

| Limit | Direction | Issue |
| --- | --- | --- |
| Pooled queue, if chosen over per-pod | Over-promises | — |
| Decode assumed perfectly balanced across `N_d` | Over-promises | — |
| Exponential wait tail with heavy-tailed prompts | Over-promises (the true tail is heavier) | — |
| Poisson arrivals on bursty traffic | Over-promises | — |
| `W_router` taken as 0 | Over-promises | slo-tiers open question 5 |
| Output-length spread ignored (`D = 1`) | Over-promises decode rate by 7-44% | #276 |
| Prefix-cache hits still occupy KV | Over-promises decode rate, up to about 4x under multi-turn reuse | #277 |
| Positive prompt/output correlation | Over-promises decode rate | — |
| ITL depends on shape on aggregated pods | Either way; about 40% of ITL at 1000/500 is prefill interference | #275 |
| SLO analyzer reads step time, not per token | Under-promises on speculating vLLM, by `1 + E` | #274 |
| No saturation line for an unlabeled aggregated variant | No answer from saturation; use the SLO analyzer's line | #432 |
| `V_P` from a lower bound | Under-promises | — |
| Sum of percentiles for TTFT | Usually under-promises; not guaranteed with heavy tails | — |

## Packaging

| Package | Holds | Layer |
| --- | --- | --- |
| `internal/signals/itl` | Unchanged | 30 |
| `internal/signals/prefill` (new) | `V_P` and `a`, their windows and admission, modelled on `itl` | 30 |
| `internal/signals/whatif` (new) | Decode pricing (moved), the closed-form fill, the queue model, both inversions, bands, basis | 31, with its own arm in `hack/check-import-direction.sh` before `internal/signals/*`, as `signals/floor` has |

`whatif` composes the measuring packages (`itl`, `prefill`, `shape`,
`capacity`), the relation `signals/floor` has to them. Ranking it at 31 is what
makes the import check catch an edge back. No package here imports an analyzer,
the engine, the collector or a Kubernetes client, which respects
[engine structure](engine-structure.md) stage 3 (#345).

**What PR 1 moves.** From `slo/price.go`: `priceTPOT`, `reachableFill`,
`replicaTokenRate`, `realisedFill` and the types `fillBand` and `pricing`. From
`saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor` and
`derivedMu`. `pricingK` is rewritten to take the two thresholds and the band as
numbers; the analyzer keeps reading the policy. The batch-cap order stays in
the analyzers: `pricingBatchCap` and `atCapBatchCap` are methods behind replay
switches, and the library takes the cap as a number. Two copies of the
sequence-cap arithmetic (`replicaTokenRate` and `deriveMuAtCap`) become one,
bit-identical. About 64 test references in 12 `_test.go` files move or change
import (`slo/price_test.go` 28, `saturation/mu_from_itl_test.go` 10), and the
paths cited in `docs/developer-guide/saturation-analyzer.md` and
`slo-analyzer.md` change. All goldens replay unchanged.

**Accessors the CLI and the endpoint need.** None exists today:

- `itl.Tracker`: the window's observations per key (only `WindowLen` is
  exported).
- The SLO and saturation analyzers: a read-only view of their trackers and
  per-variant batch cap and capacity (the fields are unexported).
- Replay: one narrow function, "replay to cycle N and return the learned
  state", in place of exposing `learnreplay`'s `Options` and `Report`.
  `cmd/learnergolden` already imports `learnreplay`, so its "test support only"
  doc is already stale; PR 3 corrects it and keeps the surface small.

**The CLI** is `cmd/whatif`: two subcommands, three sources (`--recording`,
`--snapshot`, `--prometheus`), table or `-o json`.

## Validation

1. **Replay, predict, check on held-out cycles.** Learn on the first 60% of a
   golden's cycles, freeze, and predict each later cycle. TPOT against the
   router and engine means; the decode knee against saturated throughput
   windows; prefill `S` against `AvgServiceTime`; the prefill queue wait against
   `AvgTTFT − AvgServiceTime`. The error is out of sample and **signed**, so a
   line that over-prices on one side and under-prices on the other cannot
   cancel.
2. **Score at the operating point.** The tool's default answer is at 0.85 of
   the knee, where the `1/(1 − ρ)` amplification is largest, so that is where it
   is scored. **The pass mark is asymmetric:** a predicted rate above the
   measured one by more than 10% fails; below it, by more than 25%.
3. **A fixed-replica staircase on a cluster.** Pin `N_p` and `N_d` (scale-out
   off), step the rate up 10% every 5 minutes until TTFT p90 runs away. Two
   buckets: short prompts (decode-bound) and long prompts (prefill-bound). It
   is the only run that exercises the queue term; a run that scales out never
   holds a pod near `ρ = 1`. Record, per pod, the spread of
   `num_requests_waiting`: it measures `f` and tests the per-pod model directly.
   The recording must list the SLO analyzer (observe mode), so router TTFT is in
   it, and Prometheus must keep the prompt and queue-time `_bucket` series for
   the window.
4. **Property cases**, in the style of the known-truth cases such as
   `known-truth/itl/speculative-accepted-tokens`: a synthetic engine with a known
   line, `V_P` and imbalance.
   - The rate never falls when a target loosens, and never rises when prompts
     or answers lengthen.
   - Round trip: question 1 at the SLO question 2 returned gives back question
     2's rate, within the bisection tolerance, **at unclamped points only**
     (where the fill is clamped, many SLOs map to one rate).
   - An unfit input refuses, never 0 or +Inf. NaN refuses.
   - With `f = 1` and one pod, the queue model is exactly Pollaczek-Khinchine.
   - PR 1 only: the moved functions return bit-identical results.

## PR plan

Two tracks, merging at PR 7. Each PR says which goldens it moves. Learner PRs
follow [the learner rules](analyzer-learner.md): one behaviour change per
replay switch, goldens replayed with it on and off, and a property case that
fails on `main` before it passes on the branch.

| PR | What | Depends on |
| --- | --- | --- |
| **Decode** | | |
| 1 | Move the decode pricing into `internal/signals/whatif`, rank it at 31; one copy of the sequence-cap arithmetic. Zero golden change | — |
| 2 | `whatif` decode: closed-form fill, both inversions for TPOT, basis, bootstrap bands, property cases. Tracker accessors | 1 |
| 3 | `cmd/whatif` from a recording (the narrow replay function) and from a snapshot. Decode | 2 |
| 4 | Standalone: `--prometheus`, the pod-to-role mapping, history refit | 3 |
| **TTFT** | | |
| 5a | `internal/signals/prefill`, observing: `V_P` from busy minutes and its lower bounds, `a` from low-load minutes, P/D prefill rows only, with admission reasons and the settled-replica tracker. Logs every cycle; nothing reads it. A replay switch from the start. Fits on `ols-sweep` today | — |
| 5b | TTFT in `whatif`: per-pod queue with `f`, the pooled bound, the decode-side terms, the percentile, both inversions. Validated on `ols-sweep` held-out cycles against `AvgTTFT` | 5a, 2 |
| 6 | Collect what is truly missing, with presence bits: `request_prefill_time_seconds` (aggregated pods), the queue-time and prompt-length `_bucket` series (tails), `nixl_xfer_time_seconds` (P/D transfer). Not on the P/D critical path | — |
| **Together** | | |
| 7 | Per-bucket and mix answers from the histograms; aggregated TTFT | 5b, 6, #276 |
| 8 | Controller endpoint and the two gauges | 5b |
| 9 | The staircase run, and an operator page in `docs/` | 4, 7 |
| Later | Prefill line in the snapshot; the floor's derived prefill mu from `V_P`; shared prefill/decode budget on aggregated pods; #275, #432, #277 | — |

The ols-sweep-slo run, in progress on 2026-11-02, scales out, so it cannot
exercise the queue term. It does carry router TTFT. Export its Prometheus data
for the same window now, while the raw `_bucket`, prefill-time and queue-time
series are still retained, so it can be joined offline.

## Risks

- **The router does not balance prefill, and `f` moves.** `f` measured in one
  window may not hold in the next. The answer uses the recent maximum and says
  so.
- **Means in, percentiles out.** Recordings carry means, so their tails rest on
  an assumed distribution. The staircase run is the check.
- **The answer is read as a promise.** The basis column, the asymmetric pass
  mark and the refusals exist for that, and the operator page must say it.
- **Drift from the analyzers.** PR 1 removes the copies before anything else is
  built.

## Open questions

1. **Linear prefill, or an attention term?** Is `S = a + I_eff/V_P` enough at
   30k prompts, or is `V_P` itself a function of prompt length? *Recommend:*
   key `V_P` by input bucket, as saturation keys its prefill windows; add a
   term only if the long-prompt bucket misses by more than 15%.
2. **Burstiness.** Estimate `Ca²` from the arrival rate's variance, or keep
   Poisson? *Recommend:* estimate it, cap it at 4, and show it in the basis.
3. **Which imbalance signal?** Per-pod computed-token rate, or per-pod queue
   length? *Recommend:* the token rate when the pods are busy (it is a share of
   work); queue length otherwise.
4. **The batch cap in standalone.** *Recommend:* kubeconfig first (flags, then
   the annotation), `--batch-cap` otherwise, and refuse when neither. Persisting
   the learned cap in the snapshot is a separate PR.
5. **Per-request logs for the joint length distribution?** *Recommend:* an
   optional input; otherwise assume independence and say so.
6. **The controller endpoint.** *Recommend:* CLI first; build the endpoint when
   someone needs answers fresher than a snapshot.
7. **ITL per token in the SLO analyzer (#430's known limit).** *Recommend:* not
   in PR 1, which must stay a pure move; a separate PR with its own switch.

## Appendix: where the decode pieces are

| Piece | Where |
| --- | --- |
| The line, fit, validity | `internal/signals/itl/model.go`: `Model`, `Fit`, `ValidModel` |
| Tiers | `itl/tracker.go`: `TierOLS`, `TierPinnedB`, `Reading.SlopeHeld`; `itl/fit_pinned.go`: `FitPinnedB`, `DefaultBaselineSec` |
| Admission | `itl/admission.go`: `Admission`, band, `atSequenceCap`; `itl/steady.go` |
| Sequences, token rate, line check | `itl/rate.go`: `Sequences`, `TokenRate`, `GPSErrorPct` |
| Throughput from the line | `saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor`, `pricingK` |
| Batch cap | `saturation/batch_cap.go`; `capacity.EngineParams.BatchCap`, `StatedBatchCap` |
| TPOT pricing | `slo/price.go`: `priceTPOT`, `reachableFill`, `replicaTokenRate`, `realisedFill`; `slo/analyzer.go`: `priceVariant` |
| Speculative decoding | `ReplicaMetrics.ITLPerToken` (PR #430) |
| Shape | `internal/signals/shape`: `New`; #276 branch: `Dispersion`, `WithOutputDispersion` |
| Recording and replay | `internal/signals/learnrec`; `internal/engines/analyzers/learnreplay`; `cmd/learnergolden` |
| Snapshot | `saturation/learned_snapshot.go`: `LearnedState`, `LineSnapshot` |

## Appendix: what each mode can read

| Term | Recording | Snapshot | Live Prometheus |
| --- | --- | --- | --- |
| `(k, ITL)` per pod | `KvUsageMean`, `AvgITL` | Lines only | `QueryKvUsageMean`, `QueryAvgITL` |
| KV capacity | `TotalKvCapacityTokens` | No | `QueryCacheConfigInfo` |
| Batch cap | Store records | No | Needs the Deployment |
| Shape means | `AvgInputTokens`, `AvgOutputTokens`, `PrefixCacheHitRate` | No | Yes |
| Length histograms | No | No | Yes, queried directly |
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
