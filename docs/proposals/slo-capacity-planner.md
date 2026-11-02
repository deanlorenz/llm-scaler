# What-if capacity: from an SLO to a rate, and from replicas to an SLO

**Status:** proposal, 2026-11-02. Nothing here is built. The decode half reuses
arithmetic that runs in the controller today; the TTFT half needs a prefill
model that does not exist yet, and this page says so where it matters.

**Audience:** contributors. Operators read
[the scaling policy reference](../reference/scaling-policy.md) for what runs
today.

## The two questions

An operator asks one of two things about a model.

1. **"My SLO is a TTFT of 2 s at p90 and a TPOT of 50 ms. How many requests per
   second can this fleet carry?"** Optionally: per token bucket, such as "8k
   prompts with 500-token answers".
2. **"I run 2 prefill and 6 decode replicas. What is the tightest SLO they hold,
   and at what rate do they saturate?"** Optionally per token bucket.

Both must be answerable in three places:

- **From a recording** of a past run, with no cluster.
- **From a live cluster**, with Prometheus and no controller installed.
- **Inside the controller**, from the state it has already learned.

Every answer comes with the evidence it rests on, and the tool refuses to
answer when that evidence is missing.

A session looks like this. The command and flags are proposed, not built, and
the numbers are illustrative:

```
$ whatif slo-to-rate --recording ols-sweep-slo.jsonl.gz --variant decode-h100 \
    --tpot 50ms --ttft 2s@p90
role     replicas  max rate   bound by          basis
decode   6         31.4 r/s   TPOT at k=0.52    ols (n=212, k 0.18-0.71), ±6%
prefill  2         22.0 r/s   TTFT p90          prefill line (n=88), ±18%
fleet              22.0 r/s   prefill TTFT

$ whatif replicas-to-slo --prometheus http://prom:9090 --model llama-70b \
    --prefill 2 --decode 6 --mix live
saturates at 24.1 r/s (prefill, rho=1)
at 85% of that (20.5 r/s): TPOT 41 ms, TTFT p90 1.3 s
```

## At a glance

| | |
| --- | --- |
| **Decided by this proposal** | One library at the `signals` layer, a CLI, and an optional read-only controller endpoint. Decode answers reuse the ITL line and the SLO analyzer's TPOT pricing unchanged. TTFT gets a model of its own: queue wait plus prefill compute plus KV transfer. Unfit inputs give a refusal, never a number. |
| **Reused as is** | `itl.Model`, `itl.Fit`, `itl.FitPinnedB`, `itl.Tracker`, `itl.Sequences`, `itl.TokenRate`, `itl.GPSErrorPct`; `shape.New` and the #276 dispersion; the SLO analyzer's `priceTPOT`, `reachableFill`, `replicaTokenRate`, `realisedFill`; the saturation analyzer's `deriveMuAtCap`, `capacityTokensFor`, `pricingK`; the batch-cap sources of #422 and #429; the collector's query templates; the learner recorder, replay and goldens; the learned-state snapshot. |
| **Moved, not changed** | The pricing functions above move from the two analyzers into the new library, so the CLI can call them without the engine. Zero golden change. |
| **New** | A prefill line and its learner; a queueing model for the wait; the inversions for both questions; per-bucket answers from the token histograms; the CLI. |
| **Not built anywhere today** | Any model of TTFT. See [The TTFT gap](#the-ttft-gap-stated-plainly). |

## What exists today

### Decode: everything the TPOT answer needs

| Piece | Where | What it gives the tool |
| --- | --- | --- |
| The ITL line `ITL(k) = A·k + B` | `internal/signals/itl/model.go`: `Model`, `Fit` (OLS), `ValidModel` | Latency per output token at KV fill `k` |
| Fit tiers | `itl/tracker.go`: `TierOLS`, `TierPinnedB`; `Reading.SlopeHeld` for a held slope; `fit_pinned.go`: `FitPinnedB`, `DefaultBaselineSec` (6 ms) | How much to trust the line. A borrowed line comes from [learned-state sharing](learned-state-sharing-by-similarity.md) |
| Admission | `itl/admission.go`: `Admission`, the band `[0.15, 0.80]`, `atSequenceCap`; `itl/steady.go`: the steady gate | Which readings the line was fitted on, and the `k` range it covers |
| Sequences and token rate | `itl/rate.go`: `Sequences(k, C, KVreq) = k·C/KVreq`, `TokenRate = Sequences/ITL(k)` | Throughput at any fill |
| The line's self-check | `itl/rate.go`: `GPSErrorPct` against `vllm:generation_tokens_total` | Whether the line still describes the replica |
| Throughput from the line | `saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor`, `pricingK` | Requests per second per replica at the pricing fill, capped by the batch cap |
| Batch cap | `saturation/batch_cap.go`: `pricingBatchCap`, `atCapBatchCap`; `capacity.EngineParams.BatchCap`, `StatedBatchCap`, `DeclaredBatchCap` | Flags first, then the `llm-d.ai/max-num-seqs` annotation, then a learned cap (#422, #429) |
| TPOT pricing | `slo/price.go`: `priceTPOT` (`k = (target − B)/A`), `reachableFill`, `replicaTokenRate`, `realisedFill`; `slo/analyzer.go`: `priceVariant` | The exact inversion question 1 needs for decode |
| Speculative decoding | PR #430: `ReplicaMetrics.ITLPerToken = AvgITL/(1+E)` | Per accepted token, not per step. Saturation reads it; the SLO and throughput analyzers do not yet |
| Shape | `internal/signals/shape`: `shape.New` gives `ILeff = IL·(1 − hit)` and `KVreq = ILeff + OL/2` | The token footprint of a request |
| Output-length spread | branch `fix/276-output-length`, not merged: `shape.Dispersion`, `Shape.WithOutputDispersion`, `domain.SquaredMeanLowerBound` | `KVreq = ILeff + D·OL/2`, with `D = E[O²]/E[O]²` from the output histogram |

### Prefill and TTFT: what is collected, and what is used

| Piece | Where | Used for anything? |
| --- | --- | --- |
| TTFT target | `config/slo.go`: `SLOPolicy.TTFT`, a percentile (default p90) | Parsed and validated |
| Router TTFT histogram | `collector/registration/slo.go`: `QuerySLORouterTTFTBuckets`, `llm_d_epp_request_ttft_seconds{streaming="true"}` | **Judged only.** `slo.judge` reports the percentile and attainment. Nothing prices it |
| Router TPOT mean | same file, `llm_d_epp_request_streaming_tpot_seconds` | Judged only |
| Engine TTFT mean | `registration/queueing_model.go`: `QueryAvgTTFT` → `ReplicaMetrics.AvgTTFT` | **No reader.** No analyzer reads `AvgTTFT` |
| Prefill token rate | `QueryPrefillComputedTokenRate` → `ReplicaMetrics.PrefillComputedTokenRate` | Saturation's `prefill_bound.go` (a lower bound on prefill throughput, opt-in) and `shape_change.go` (prefill mu at a saturated reading). vLLM only |
| Prefill queue length | `QueryQueueLength`, `QueryCapacityQueueLength`, `QueryMaxRankQueueLength` (`num_requests_waiting`) | Saturation's occupancy, the prefill holds and the lower bound's trigger. A count, not a wait: nothing converts it to seconds |
| Engine prefill time, queue time | `vllm:request_prefill_time_seconds`, `vllm:request_queue_time_seconds` | **Not collected** |
| KV transfer time (P/D) | `vllm:nixl_xfer_time_seconds` | **Not collected** |
| Prompt and output length | `QueryAvgInputTokens`, `QueryAvgOutputTokens` (means); `QueryPrefixCacheHitRate` | The shape. The histograms behind the means are not collected, except the output one on the #276 branch |

### The TTFT gap, stated plainly

**Nothing in the codebase predicts TTFT.** The SLO analyzer reads the router's
TTFT histogram and reports whether the target is met. It does not price a
prefill replica: `priceVariant` returns no-data for the prefill role ("TTFT is
not priced yet"). The saturation and throughput analyzers mention TTFT only in
comments. Prefill capacity, where it exists, is a throughput (computed tokens
per second) read while a replica is saturated, or a lower bound read while it
queues.

Two attempts were made and removed. Both are worth knowing before building a
third:

- **A fit of `TTFT(T) = A·T + B`** on the engine's TTFT mean
  ([prefill TTFT model](prefill-ttft-model.md)). Queue wait landed in the
  intercept, which read 82 s. TTFT includes waiting, so a line through it is
  not a property of the replica.
- **Step 2a of [SLO tiers](slo-tiers.md)**, per-variant means of prefill time
  and queue wait. Means could not answer what the TTFT curve needs, and there
  was no x-axis for aggregated variants.

The lesson this proposal takes: **fit the service time on a signal that has no
waiting in it, and model the waiting separately.** vLLM publishes both halves
(`request_prefill_time_seconds`, `request_queue_time_seconds`), and nothing
collects them yet.

## The models

### Decode: TPOT from the ITL line

One decode replica at KV fill `k` holds

    n(k) = min( k·C / KVreq ,  batchCap·ranks )

sequences, where `C` is its KV capacity in tokens (`capacityTokensFor`: the
live `cache_config_info` reading, else the flag). Each sequence emits a token
every `ITL(k) = A·k + B` seconds. So TPOT is `ITL(k)`, and the replica's request
rate is

    λ_rep(k) = n(k) / ( ITL(k) · OL )

This is `deriveMuAtCap` at `k = pricingK`, and `replicaTokenRate / OL` at any
other `k`. Nothing new.

Three corrections apply, all existing or in flight:

- **Speculative decoding (#274, PR #430).** TPOT is per accepted token, so the
  line is fitted on `ITLPerToken`, not the step time.
- **Output-length spread (#276).** `KVreq = ILeff + D·OL/2`. With `D = 1` the
  tool over-counts resident sequences by 7-44% on real length distributions.
- **The batch cap.** Short requests hit `max_num_seqs` before the cache fills.
  `reachableFill` then clamps `k` to `batchCap·KVreq/C`, and TPOT is read
  there.

**For a given rate**, the fill is closed form. Per replica, set
`λ_rep·OL·(A·k + B) = k·C/KVreq` and solve:

    k(λ_rep) = λ_rep·OL·B / ( C/KVreq − λ_rep·OL·A )

valid while the denominator is positive and `n(k)` is below the batch cap.
Past the cap, `n` is fixed and `ITL` must be read at the capped fill. Past the
pricing fill, the replica is saturated.

### Prefill and TTFT

TTFT, as the router measures it, has four parts:

    TTFT = W_router + W_engine + S_prefill(I) + T_xfer

| Part | What it is | Source |
| --- | --- | --- |
| `W_router` | The EPP flow-control queue | Only inside the router histogram. Not separable today |
| `W_engine` | The engine scheduler queue | `vllm:request_queue_time_seconds` (not collected) |
| `S_prefill(I)` | Computing the prompt | `vllm:request_prefill_time_seconds` (not collected) |
| `T_xfer` | Shipping KV from prefill to decode (P/D only) | `vllm:nixl_xfer_time_seconds` (not collected) |

The two waits are modelled together as one queue `W`. The model has three
pieces.

**1. The prefill line.** Fit the prefill service time against the effective
prompt length:

    S(I) = a + I_eff / V_P,    I_eff = I·(1 − hit)

`V_P` is the replica's prefill token velocity, the same quantity as `1/A` in
[prefill TTFT model](prefill-ttft-model.md) and TokenScale's token velocity.
`a` is the fixed cost per request. The regressand is the engine's prefill time,
which has no waiting in it, so the queue cannot leak into `a` the way it
leaked into the 82 s intercept.

The regressor needs care. A per-pod 1-minute mean pairs mean prefill time with
mean prompt length, and the line through those means is the line through the
requests only when the prompt lengths in a minute are alike. That holds on a
bucketed trace and not on a wide mix. Two options, in order of preference:

- Fit per input bucket: within a bucket the means are close to the requests.
  This needs the prompt histogram (`vllm:request_prompt_tokens_bucket`) to say
  which minutes are dominated by one bucket.
- Cross-check `V_P` against `PrefillComputedTokenRate` during busy minutes.
  The computed rate during a saturated minute is `V_P` itself. This is the
  "GPS check" for prefill: a signal never fitted into the line.

Long prompts make attention quadratic. A line is enough below a few thousand
tokens; above that a `c·I_eff²` term may be needed. The validation decides
(see [Open questions](#open-questions)).

Batching matters too. A prefill replica runs several prompts per step, up to
`--max-num-batched-tokens`. The line above is the service time of one request
in a replica that is busy with others. So `V_P` is the replica's token
throughput, and `1/V_P` is the marginal cost of a prompt token. This is the
same argument the ITL line makes for decode.

**2. The wait.** Treat the prefill fleet as `c = N_p` servers fed at rate `λ`.
The router spreads requests across them, imperfectly (see
[Risks](#risks)). Per request the service time is `S(I)`, with `I` drawn from
the prompt-length distribution. From the histogram:

    E[S] = a + E[I_eff]/V_P
    E[S²] = a² + 2a·E[I_eff]/V_P + E[I_eff²]/V_P²
    ρ = λ·E[S] / c

The mean wait uses the Allen-Cunneen approximation for M/G/c:

    W̄ ≈ P_wait(c, ρ) · E[S] / ( c·(1 − ρ) ) · (1 + Cs²)/2

with `P_wait` the Erlang C probability and `Cs² = E[S²]/E[S]² − 1`. For `c = 1`
this is the Pollaczek-Khinchine formula, exactly. Arrivals are taken as
Poisson. Bursty traffic waits longer; the general form adds the arrivals'
squared coefficient of variation `Ca²` to the `1 + Cs²`, and the tool can
estimate it from the arrival rate's variance across cycles (open question 2).

TTFT is a percentile, so the tail matters. Use the standard exponential tail
of the M/G/c wait:

    P(W > t) ≈ P_wait · exp( −t · 2·c·(1 − ρ) / ( E[S]·(1 + Cs²) ) )

and so

    W_q = max( 0,  E[S]·(1 + Cs²) / ( 2·c·(1 − ρ) ) · ln( P_wait / (1 − q) ) )

for the `q` percentile. The prefill service time has a tail of its own (long
prompts), so the TTFT percentile is approximated as

    TTFT_q ≈ W_q + S(I_q) + T_xfer

where `I_q` is the `q` percentile of the prompt length. Adding percentiles
over-states the sum's percentile, so this is conservative. The validation
measures by how much.

**3. KV transfer (P/D).** `T_xfer` is the mean of `vllm:nixl_xfer_time_seconds`,
taken as a constant per bucket. It grows with `I_eff`, so per bucket it is a
second line, `T_xfer(I) = t0 + I_eff/V_BW`. The prefill TTFT model proposal
names `V_BW` the third bottleneck. Whether the metric is per request or per
chunk is open ([slo-tiers.md](slo-tiers.md) open question 4).

**Decode back-pressure.** A prefill request completes only when decode admits
its KV. When decode is short, prefill queues on decode's account: measured, 41
queued at 77% prefill KV while decode was short, and 820 queued while decode
sat at its ceiling. So the TTFT answer is valid only while decode is below its
own saturation. The tool checks decode first and reports "TTFT bound by decode"
when decode is the limit.

### P/D versus aggregated

| | P/D | Aggregated (`both`) |
| --- | --- | --- |
| TPOT | The decode role's ITL line | The variant's ITL line, which includes chunked-prefill interference (#275) |
| TTFT | The prefill role's line and queue, plus `T_xfer` | Queue plus prefill, sharing each step with decode |
| Saturation | The lesser of the two roles' rates | One rate |
| Today's line | Fitted for decode | **Not fitted by saturation** for an unlabeled variant (#432); the SLO analyzer fits it |

Aggregated TTFT is harder. Prefill and decode share each step's token budget,
so `V_P` falls as decode load rises. The first version fits `S(I)` on the
aggregated pod's own prefill-time series, which already contains that
interference at the load it was measured at, and marks the answer
**low confidence** when the queried rate is far from the fitted one. A model
of the shared budget is later work ([Phases](#pr-plan)).

### How a token bucket enters

A bucket is a box of prompt length and output length, such as `8k-16k in,
250-500 out`. The bucket edges are those of `classifyOutputLength` (`short`
under 100, `medium`, `long`, `xlong`, `xxlong`, `huge` at 6000 and up), on
both axes. Users may pass their own edges.

| Model term | Per bucket `b` | For the current mix |
| --- | --- | --- |
| `KVreq` (decode) | `ILeff_b + D_b·OL_b/2` | `E[ILeff] + D·E[OL]/2` from the histograms (#276) |
| `OL` divisor (decode rate) | `OL_b` | `E[OL]` |
| `ITL(k)` | One line; #275 says it moves with shape on aggregated pods | Same |
| `S(I)` (prefill) | `a + ILeff_b/V_P` | `E[S]`, `E[S²]` from the prompt histogram |
| `T_xfer` | `t0 + ILeff_b/V_BW` | Its mean over the mix |

A per-bucket answer means "if all traffic looked like bucket `b`". A mix answer
uses the moments of the current distribution.

Prometheus gives the two length histograms separately, not jointly. The tool
assumes prompt and output length are independent when it needs a joint
distribution. A recording made with a load generator that logs each request
can supply the joint one instead.

## Inverting the models

### Question 1: from an SLO to a rate

**Decode, TPOT target `t`.** Exactly `priceVariant`:

1. `k* = (t − B)/A` (`priceTPOT`). If `ITL(band.lo) ≥ t`, the target is
   infeasible on this variant: say so, do not answer zero.
2. Clamp `k*` to the band `[0.15, 0.80]` and to the batch cap
   (`reachableFill`).
3. `λ_rep = replicaTokenRate(k*) / OL`; the fleet carries `N_d·λ_rep`.

If no TPOT target is given, use the saturation point `k = pricingK`
(`deriveMuAtCap`).

**Prefill, TTFT target `T` at percentile `q`.** `TTFT_q(λ)` rises with `λ`,
so bisect on `λ` in `(0, c/E[S])`. The answer is the largest `λ` with
`TTFT_q(λ) ≤ T`. If `S(I_q) + T_xfer ≥ T` the target is infeasible at any
rate: the prompt alone takes longer than the target. Say so.

**The fleet.** The supported rate is the minimum over the roles, and the
answer names the binding role and objective.

**Per bucket.** Run the same with that bucket's `KVreq`, `OL`, `I_eff` and
distribution moments. A bucket's rate can be far from the mix's: a long-prompt
bucket is often prefill-bound while the mix is decode-bound.

### Question 2: from replicas to the tightest SLO

Given `N_p` and `N_d`:

1. **The saturation rate.** Decode: `N_d · deriveMuAtCap`. Prefill: the rate
   where `ρ = 1`, `N_p / E[S]`. The fleet's is the smaller. This is the knee.
2. **The curve.** For `λ` from 0 to the knee, predict TPOT from the closed-form
   fill `k(λ/N_d)` and TTFT from the queue model. Report it as a table.
3. **The tightest SLO.** The SLO the fleet holds at an operating rate. The
   default operating rate is `scaleUpThreshold` times the knee (0.85), the
   headroom the autoscaler itself keeps, so the answer matches what a fleet the
   autoscaler manages would see. The user may pass a rate instead.

The tightest TPOT at any rate is `ITL(band.lo)`, the line's floor. The tightest
TTFT is `S(I_q) + T_xfer`, with no queue. Both are reported, because they say
what no number of replicas can buy.

### The current mix

The current mix comes from the same histograms the bucket answers use, read
over a window. Defaults: 5 minutes for a live cluster, the whole recording or a
chosen cycle range offline. The shape uses the short-window output length for
the rate divisor, as `deriveMuAtCap` already does, and the 5-minute one for
`KVreq`.

## Data sources and modes

### What the models need, per term

| Term | Learned from | Recording | Snapshot | Live Prometheus |
| --- | --- | --- | --- | --- |
| ITL line `A, B` | Per-pod `(k, ITL)` each cycle | Yes: `ReplicaMetrics.KvUsageMean`, `AvgITL` per row | Yes: `itlLines`, `itlSlopes`, `itlBaseline`, `itlLinesPerToken` | Yes: `QueryKvUsageMean`, `QueryAvgITL` |
| Fit quality (n, k range, residuals) | The tracker's window | Yes, by replay | **No.** `LineSnapshot` holds `a, b, fromVariant, learnedAt` only | Yes, by refitting |
| KV capacity `C` | `cache_config_info` | Yes: `TotalKvCapacityTokens` | No | Yes: `QueryCacheConfigInfo` |
| Batch cap | Flags, annotation, learned | Yes: the store `Records` in each saturation event | No (the learned cap is not persisted) | **No**: needs the Deployment, or a flag |
| Shape means | Prompt and output length, prefix hits | Yes: `AvgInputTokens`, `AvgOutputTokens` (+`Recent`), `PrefixCacheHitRate` | No | Yes |
| Length histograms | `_bucket` series | **No**: rows carry means only (the #276 branch adds `AvgOutputTokensSquared`) | No | Yes, by querying the buckets directly |
| Prefill line | Prefill time vs `I_eff` | **No**: not collected (PR 4) | No | Yes, once PR 4's queries exist; the raw series are already in Prometheus |
| `V_P` cross-check | `PrefillComputedTokenRate` | Yes | No | Yes |
| Engine TTFT mean | `AvgTTFT` | Yes, per row, unused | No | Yes |
| Router TTFT histogram | EPP | Only when the SLO analyzer ran: the replay's `recordedRouter` serves it. **The real goldens list slo as notRun** | No | Yes: `QuerySLORouterTTFTBuckets` |
| Arrival rate | EPP | Yes: `Input.ArrivalRate` | No | Yes: `QueryModelArrivalRate` |

### Mode 1: from a recording (offline)

A recording is the learner recorder's JSONL (`internal/signals/learnrec`): one
event per analyzer call, a cycle being the events that share a `Now`. Each
`saturation.Analyze` and `slo.Analyze` event carries the full analyzer input:
raw `ReplicaMetrics` rows, variant states, the merged policy, the scheduler
queue and the arrival rate.

The tool replays it with `learnreplay.Replay` in `Normal` mode, which builds the
real analyzers on a fake clock and feeds them every event. After the cycle the
user names (default: the last), it reads the trackers' state and answers. So
the offline answer is, by construction, what the controller knew at that
moment.

Two facts constrain this:

- `learnreplay` says "test support only; nothing in the controller imports
  it". A `cmd/` importing it is allowed by the import check, but the package
  becomes a supported API. PR 3 states that in its doc comment.
- The goldens fit only some questions. `ols-sweep` (226 cycles, P/D, 195
  published OLS lines, a concurrency and shape sweep) answers the decode half.
  `pd-restart` publishes no line, so it only tests the P/D roles and the
  restart path. `e1` and `e1-tuned` have lines but no SLO events. **None has a
  router TTFT histogram.** `ols-sweep-slo`, being recorded now with the SLO
  analyzer and streamed load, is the first that will.

### Mode 2: live, without the controller (standalone)

The collector's query templates and `source.QueryList.Build` are pure, and
`prometheus.NewPrometheusSource` needs only a `promv1.API` client. What needs a
Kubernetes client is turning the series into `domain.ReplicaMetrics`:
`CollectReplicaMetrics` attributes pods to variants and reads readiness and
`ReadySince`. The standalone mode replaces that with label-based attribution
(the pod's `llm-d.ai/role` and model labels as scraped), and marks every row
Ready unless `--kubeconfig` is given.

What the standalone mode **must learn itself**:

- The ITL line, by running `itl.Tracker.Observe` over a lookback. It replays
  Prometheus history at the controller's cadence (one row per 15 s step over,
  say, the last hour), so it gets a fit with its quality, not just a number.
- The prefill line, the same way, once PR 4's series are queried.
- The shape and the length histograms over the window.

What it **may read from a snapshot** (the `wva-learned-state` ConfigMap, key
`v1`, a JSON `saturation.LearnedState`):

- An ITL line when the lookback has too little spread in `k`. A balanced fleet
  rarely has much. The answer is then tier "snapshot", flagged, with the line's
  age and no band, because the snapshot carries no fit statistics.
- k2 and throughput windows, as a cross-check of the decode saturation rate.

It must **be told**: the batch cap (`--batch-cap`), unless a kubeconfig lets it
read the flags or the annotation.

### Mode 3: inside the controller

The analyzers already hold every tracker the answers need. An endpoint reads
them under the analyzers' locks and calls the same library. No new learning,
no new state. See [Packaging](#packaging).

## Uncertainty and refusals

Every answer carries a **basis**: which tier each term came from, how many
readings, over what range, and how old. The tool refuses rather than guesses.

| Term | Answer with | Answer and flag | Refuse |
| --- | --- | --- | --- |
| ITL line | `ols` with `Ready` (steady spread met) | `pinned-B` with a learned B; a held slope younger than the hold; a borrowed line (says from which variant) | No line; `ValidModel` false; `pinned-B` on the default 6 ms B (`BaselineLearned` false) |
| KV capacity `C` | Live `cache_config_info` | The flag (`TotalKvTokensOverride`) | Neither |
| Batch cap | Flags, or the declared annotation | Learned (#422) | None stated and engine is vLLM with no flags read: answer with 256 only under `--assume-default-batch-cap` |
| Prefill line | Fit with enough readings across a spread of `I_eff` | `V_P` from saturated `PrefillComputedTokenRate` only, no `a` | No readings; SGLang (no per-stage prefill series) |
| `T_xfer` | Collected | Absent on P/D: answer without it and say so | — |
| Shape | Histograms | Means only (`D = 1`, no `E[I²]`) | No traffic in the window |

**Confidence bands.** An OLS fit has standard errors on `A` and `B` and their
covariance. `itl.Fit` does not compute them today; the library computes them
from the window's observations, which the tracker already holds (a snapshot
does not, so a snapshot line has no band). The band on `ITL(k)` is `±t·se(k)`, and the band on the rate
follows through `λ = n(k)/(ITL(k)·OL)`. For the prefill line the same. The
queue model has no fitted parameters of its own, but it amplifies the service
time's error by `1/(1 − ρ)`: near the knee a 5% error in `V_P` is a large error
in TTFT. The band is computed by evaluating at the edges of the parameter
bands, not by a linearised formula, because of that amplification.

**Extrapolation.** The window records the range of `k` it saw. Asking about a
fill outside that range is extrapolation and is flagged with the distance.
Outside the policy band `[0.15, 0.80]` the tool clamps, exactly as `pricingK`
and `priceTPOT` do: above 0.80 the engine preempts rather than slowing down,
and the line says nothing there. The prefill line records its `I_eff` range the
same way; asking about a bucket outside it is flagged.

**The self-check.** `GPSErrorPct` compares the token rate the line predicts at
each replica's own fill against `vllm:generation_tokens_total`. A mismatch above
15% (`DefaultGPSMismatchThresholdPct`) away from a ramp is reported beside the
answer. The analyzers only log it; the tool shows it, because it is the one
check made against a signal the line never saw. Prefill gets the equivalent
from `PrefillComputedTokenRate`.

## Packaging

### A library at the signals layer

The math must be callable without the engine. Today the pieces live in two
analyzers (layer 40 in `hack/check-import-direction.sh`) and take analyzer
types. The proposal moves them down to `signals` (layer 30), where the CLI and
any analyzer can call them.

| Package | Holds | Imports |
| --- | --- | --- |
| `internal/signals/itl` | Unchanged: the line, tiers, tracker | `domain` |
| `internal/signals/prefill` (new) | The prefill line `S(I) = a + I_eff/V_P`, its window and tracker, modelled on `itl` | `domain` |
| `internal/signals/whatif` (new) | Decode pricing (moved), the closed-form fill, the queue model, both inversions, the bands, the basis | `itl`, `prefill`, `shape`, `domain` |
| `internal/signals/shape` | Unchanged, plus #276 | `domain` |

What moves into `whatif`, with no change of behaviour:

- From `slo/price.go`: `priceTPOT`, `reachableFill`, `replicaTokenRate`,
  `realisedFill`, `fillBand`.
- From `saturation/mu_from_itl.go`: `deriveMuAtCap`, `capacityTokensFor`.
- `pricingK` needs `config.ScalingPolicy`; `config` is an infrastructure leaf,
  importable from anywhere, so it can move too. Better: it takes the two
  thresholds and the band as numbers, and the analyzer reads the policy.

`capacityTokensFor` takes `*capacity.EngineParams`. `signals/capacity` is at
the same layer, so the import is allowed. A narrower signature (the override
and the ranks as numbers) is cleaner and is preferred.

The analyzers then call `whatif` for the arithmetic they do today. That is one
PR with a replay of every golden and zero change, under the
[learner discipline](analyzer-learner.md): no switch is needed because no
decision moves, and the goldens prove it.

No package in this list imports an analyzer, the engine, the collector or a
Kubernetes client. That is what makes the CLI possible, and it respects
[engine structure](engine-structure.md) stage 3 (#345): no upward edge, and the
import-direction check enforces it.

### A CLI

`cmd/whatif`, one binary, two subcommands (`slo-to-rate`, `replicas-to-slo`)
and three sources (`--recording`, `--snapshot`, `--prometheus`). Output as a
table by default and JSON with `-o json`, so a benchmark script can read it.

### A controller endpoint (optional)

A read-only HTTP handler on the controller's existing metrics server:
`GET /whatif?model=…&tpot=…&ttft=…` and `…&prefill=2&decode=6`. It answers
from the analyzers' live trackers, so it is free of new learning. Behind a
flag, off by default, because it exposes model names.

**Not a metric.** The questions take parameters; a gauge per target and bucket
would be unbounded cardinality. Two gauges per variant are worth publishing:
the predicted saturation rate at the current replica count, and the predicted
TPOT at the current rate. Both are already implied by
[every signal as a metric](signals-as-metrics.md).

## Validation

**1. Replay and predict, then check on held-out cycles.** Take a golden. Replay
the first part, say 60% of cycles, to learn the lines. At each later cycle,
predict from the frozen state and compare against what the recording shows:

| Prediction | Checked against |
| --- | --- |
| TPOT at the observed fill | Router `streaming_tpot` mean; engine ITL mean |
| Decode saturation rate | Measured saturated throughput windows, where the run saturated |
| TTFT p90 at the observed rate and replicas | Router TTFT histogram |
| Prefill saturation rate | `PrefillComputedTokenRate / I_eff` during saturated minutes |

The error is **out of sample** and **signed**: a rotated line over-prices on
one side and under-prices on the other, and an unsigned mean hides it.
[SLO tiers](slo-tiers.md) open question 1 sets the same rule.

**2. The recordings it needs.**

| Recording | Decode half | TTFT half |
| --- | --- | --- |
| `ols-sweep` | Yes: OLS lines over a concurrency and shape sweep | No router TTFT, no prefill series. `AvgTTFT` and `PrefillComputedTokenRate` per row give a weak `V_P` check only |
| `pd-restart` | No published line | No. Tests P/D roles and the restart path |
| `e1`, `e1-tuned` | Yes, two models | No |
| `ols-sweep-slo` (being recorded) | Yes | Router TTFT, through the SLO analyzer's events. No engine prefill or queue time unless PR 4 lands first |
| The next recording after PR 4 | Yes | Everything: router TTFT, engine prefill time, queue time, transfer time, prompt histogram |

To fit and validate the TTFT model a recording must carry, per cycle: the
router TTFT histogram (`slo.Analyze` events, so the SLO analyzer must be
listed, in observe mode); per pod, prefill time, queue time and transfer time
as means; the prompt-length histogram; the arrival rate; prefill and decode
replica counts. And it needs load that crosses the prefill knee at a fixed
replica count, so the queue term is exercised: a staircase, not a sweep that
scales out.

**3. A cluster run.** A staircase at fixed replicas: hold `N_p` and `N_d`,
step the rate up by 10% every 5 minutes until TTFT p90 runs away. Two buckets:
a short-prompt one (decode-bound) and a long-prompt one (prefill-bound). The
pass mark: the predicted knee within 15% of the measured one, and the
predicted TTFT p90 within 25% at 70% of the knee. Run the question-1 answer
back: set the rate to what the tool said a target allows, and check the
attainment.

**4. Property cases.** In the style of the existing known-truth cases (for
example `known-truth/itl/speculative-accepted-tokens`): a synthetic engine with
a known line and known `V_P`, and properties that must hold for every seed.

- The supported rate never falls when a target loosens.
- The supported rate never rises when prompts or answers lengthen.
- Round trip: answering question 1 at the SLO question 2 returned gives back
  the rate question 2 was asked at, within the bisection tolerance.
- An unfit input refuses; it never returns 0 or +Inf.
- A NaN anywhere refuses (`x <= 0` admits NaN).
- At `c = 1` the queue model equals Pollaczek-Khinchine exactly.
- The moved pricing functions return bit-identical results to the analyzer
  copies they replace (the move PR only).

## PR plan

Each PR is landable alone and says which goldens it moves.

| PR | What | Changes a decision? | Depends on |
| --- | --- | --- | --- |
| 1 | Move decode pricing (`priceTPOT`, `reachableFill`, `replicaTokenRate`, `realisedFill`, `deriveMuAtCap`, `capacityTokensFor`) into `internal/signals/whatif`. Analyzers call it | No; zero golden change | — |
| 2 | `whatif` decode: closed-form fill, both inversions for TPOT, the basis, the OLS bands. Property cases | No (library only) | 1 |
| 3 | `cmd/whatif` offline: from a recording (replay) and from a snapshot. Decode only | No | 2 |
| 4 | **Collect the TTFT parts**: `vllm:request_prefill_time_seconds`, `request_queue_time_seconds`, `nixl_xfer_time_seconds`, and the prompt-length histogram, as `ReplicaMetrics` fields with presence bits (the 0a rule of #341: a missing reading is not zero). The recorder carries them | No (observe only) | — |
| 5a | **Prefill learner, observing**: `internal/signals/prefill`, under #341. Admission with a reason per refused row (not Ready, warm pool, settling, decode back-pressure, no presence bit); the settled-replica tracker (`internal/signals/settle`); OLS with a pinned-`a` fallback, mirroring `itl`; the `V_P` cross-check. Logs its line every cycle; nothing reads it. A replay switch from day one, so turning it into an input later is one flip. Known-truth property cases and mutants | No; goldens unchanged | 4; #341 PRs 1-3 (done) |
| 5b | **Prefill golden**: add the first recording made after PR 4 as a fixture (`learnergolden add`), with a `prefill-lines.csv` output beside `lines.csv` | No | 5a, that recording |
| 5c | The prefill line in the learned-state snapshot, keyed like the ITL line, with its fit statistics | No | 5a |
| 6 | **TTFT in `whatif`**: the queue model, the percentile, both inversions, the decode-bound check. Validated on the 5b fixture's held-out cycles against the router TTFT | No | 5b |
| 7 | Standalone live mode: `--prometheus`, reusing the collector's query templates with a plain client | No | 3 |
| 8 | Per-bucket and mix answers from the histograms | No | 6, #276 merged |
| 9 | Controller endpoint and the two gauges | No | 6 |
| 10 | Cluster validation run, and the page in `docs/` for operators | No | 7, 8 |
| Later | Aggregated TTFT with a shared token budget; shape-keyed lines (#275); a `both` line in saturation (#432); prefix-cache residency (#277) | — | — |

PRs 4, 5a and 5b are the critical path for TTFT. PR 4 should land first and
soon: every recording made without it cannot fit a prefill line, so it is
cheap now and expensive later. The decode track (1, 2, 3, 7) and the TTFT
track (4, 5a-c, 6) run in parallel and meet at 8.

Every learner PR follows [the learner rules](analyzer-learner.md): one
behaviour change per replay switch, the goldens replayed with the switch on
and off, the moved rows explained in the PR, and a property case that fails on
`main` before it passes on the branch.

## Modelling limits to name in every answer

| Limit | Effect on the answer | Issue |
| --- | --- | --- |
| Speculative decoding | Fixed for saturation's line (per accepted token). The SLO analyzer still reads step time, so a line borrowed from it reads TPOT high by `1+E` | #274, PR #430 |
| ITL depends on shape on aggregated pods | A line learned at one shape mis-states TPOT at another, either way; about 40% of ITL is prefill interference at 1000/500. Per-bucket answers on aggregated pods are flagged | #275 |
| Output-length spread | Without `D`, resident sequences are over-counted by 7-44%, so the decode rate reads high | #276 |
| Prefix-cache hits still occupy KV | `KVreq` may be understated by up to about 4x under multi-turn reuse, so the decode rate reads high | #277 |
| Aggregated variants get no saturation line | The tool must take the line from the SLO analyzer's tracker for `both` | #432 |
| Router queue is not separable | `W_router` sits inside the router TTFT only | slo-tiers open question 5 |
| Joint length distribution | Assumed independent from Prometheus | this proposal |
| SGLang | No per-stage prefill series: no prefill line, TTFT refused | sglang #14303 |

## Risks

- **The router does not balance prefill.** The queue model assumes an M/G/c
  fleet, where any idle server takes the next request. Measured on one run,
  prefill held 439 waiting with 3 of 4 pods idle, because the prefill profile
  has a single usable scorer. M/G/c is then optimistic. Mitigation: validate
  against the router's own TTFT, and add a measured imbalance factor (the ratio
  of the busiest pod's queue to the mean) as a correction if the cluster run
  shows it is needed.
- **Means in, percentiles out.** The prefill line is fitted on means. The TTFT
  answer is a percentile, built from the prompt histogram and an exponential
  tail. That tail is a textbook approximation; measured heavy tails may beat it.
  The cluster run is the check.
- **A recording made before PR 4 has no prefill series.** Its TTFT answers
  fall back to `V_P` from saturated minutes, with no `a`, flagged.
- **The tool's answer is read as a promise.** A predicted rate depends on a
  line that can be stale. The basis column and the refusals exist for this,
  and the operator page must say it plainly.
- **Drift from the analyzers.** Two copies of the pricing would drift. PR 1
  removes the copy before anything else is built.

## Open questions

1. **Linear or quadratic prefill?** Is `S(I) = a + I_eff/V_P` enough at 30k
   prompts, or is a `c·I_eff²` attention term needed? *Recommendation:* fit the
   line per input bucket first; add the term only if the cluster run's
   long-prompt bucket misses by more than 15%.
2. **Which wait model?** Allen-Cunneen M/G/c, or a fitted curve of measured
   queue wait against utilization, as [SLO tiers](slo-tiers.md) step 2b
   proposes? *Recommendation:* Allen-Cunneen first, because it needs no
   saturated data; record the measured queue wait from PR 4 and switch if it
   misses.
3. **The operating point for question 2.** `scaleUpThreshold` of the knee, or
   a user-given rate? *Recommendation:* default to `scaleUpThreshold`, so the
   answer matches a fleet the autoscaler runs, and accept `--rate`.
4. **Where does the standalone tool get `max_num_seqs`?** Reading the
   Deployment needs Kubernetes access, which the standalone mode wants to
   avoid. *Recommendation:* a `--batch-cap` flag, the `llm-d.ai/max-num-seqs`
   annotation if a kubeconfig is present, and the learned cap from a snapshot.
   Refuse when none is known.
5. **Per-request logs for the joint distribution?** *Recommendation:* accept a
   load generator's per-request file as an optional input; without it, assume
   independence and say so.
6. **The controller endpoint.** Worth it, or is the CLI pointed at a snapshot
   enough? *Recommendation:* build the CLI first; add the endpoint only if a
   user asks for answers fresher than a snapshot.
7. **Speculative decoding in the SLO analyzer.** Should PR 1 also move the SLO
   analyzer to `ITLPerToken`? *Recommendation:* no, keep PR 1 a pure move;
   that is a decision change and needs its own switch.

## See also

- [Prefill capacity from a fitted TTFT model](prefill-ttft-model.md): the
  first attempt at `V_P`, and why it fitted on the wrong signal.
- [SLO tiers](slo-tiers.md): TPOT pricing, the router's TTFT, step 2a/2b.
- [One learner lifecycle](analyzer-learner.md): the switch and golden
  discipline every learner PR above follows.
- [Traffic shape shifts](shape-shift-treatment.md) and
  [a per-token economy](per-token-economy.md): the shape and units the model is
  built on.
- [Capacity-planner positioning](capacity-planner-positioning.md): a planner
  at a different altitude (hours to days, cost-driven). This tool is the
  per-model latency model such a planner would call.
