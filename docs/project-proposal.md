# llm-scaling-manager: project proposal

## Summary

llm-scaling-manager is an autoscaler for llm-d inference. It works out how many
replicas each model and variant needs, decides for all of them together against
one GPU budget, and does it from a capacity model rather than a threshold on a
raw metric. Alongside that it ships a shared warm pool to cover the minutes a
new replica spends loading, scale-to-zero, and separate handling for prefill and
decode.

We built it because the signals a general-purpose autoscaler can act on do not
tell you what you need to know. We have a benchmark where request rate, queue
depth and cache occupancy all said the fleet was fine and the right answer was
to triple it.

It started as a fork of llm-d's Workload Variant Autoscaler
(`llm-d/llm-d-workload-variant-autoscaler`, still the module path in `go.mod`)
and has moved a long way since. Shorter version, for readers outside the
repository: [Sub-second scale-ups on llm-d](blog/sub-second-scale-ups-on-llm-d.md).

## The problem

**The available signals do not mean anything stable.** The cost of a request
depends on its shape, so two requests to one model can differ by a factor of a
thousand in compute. A threshold tuned for one traffic pattern is wrong for the
next, and nothing in the signal says so. We ran a trace at a constant 6 req/s
where only the shape changed, from 6000 tokens in and 1000 out to 1000 in and
4000 out. One decode replica sustains about 5.4 req/s at the first shape and 2.5
at the second, so the fleet had to go from two replicas to three:

| signal | what it showed | what it implies |
| --- | --- | --- |
| request rate | constant at 6 req/s | nothing to do |
| gateway queue depth | flat, no queue ever formed | nothing to do |
| KV-cache occupancy | 5–15 % in the steady phase | scale down to one |

**Reacting after saturation is too late.** A replica is ready when the model has
loaded and the kernels have compiled: about 41 s for an 8B server, 192 s for
GLM-5.2-FP8, and 463 s if the JIT cache is cold. Only 40 s of that GLM start is
the weights, so faster storage does not fix it.

**A GPU is the unit of waste, and models compete for it.** At $2–4 an hour an
unnecessary replica burns real money and a missing one breaks an SLO. A
general-purpose autoscaler decides one Deployment at a time and cannot know that
scaling one model up may mean scaling another down.

**One model is several workloads.** The same model runs as several variants, and
where prefill and decode are split they have opposite bottlenecks. Scaling one
without the other moves the queue rather than clearing it.

## What we do about it

**Scale on fleet utilization against a fixed threshold**, up above 0.85 and
release below 0.70, where utilization comes from a capacity model rather than a
gauge. Each role's demand is floored on throughput: arrival rate over the
completion rate one replica sustained when last saturated. That ratio depends on
request shape by construction, so when the shape moves the price moves with it.
The thresholds are defaults in `scaling-policy-configmap.yaml` and are tunable,
but they do not have to change when the model or the traffic does, which is what
a threshold on queue depth cannot say.

**Order before the queue exists, then bridge the rest.** Because demand is
priced from throughput rather than read off a queue, the order goes in early. A
warm pool Pod holds an accelerator with models resident and lends it to a model
that is scaling up, so it serves while its own replica starts.

**Solve the whole fleet at once, inside one budget.** Every model and variant is
solved together each cycle. When the budget binds, GPUs go in order of a
fair-share priority value, priority times remaining demand weighted by score,
and every clamp is recorded against the variant that took it.

**Make the variant the scaling unit.** A variant is one ScaledObject and the
workload it scales; variants of one model are solved as a group, each role
carrying its own bottleneck.

Also shipped: scale-to-zero and wake over KEDA's push path
([scale-to-zero](well-lit-paths/scale-to-zero/)), quota-bounded scaling
([bound by GPUs](well-lit-paths/bound-by-gpus/),
[Kueue quotas](well-lit-paths/kueue-bounded-quotas/)), and workload classes
([workload classes](well-lit-paths/workload-classes/)).

## What is measured

| Claim | Measured | Where |
| --- | --- | --- |
| A shape change is caught without a queue | Third replica ordered at +1344 s, the same cycle the new shape's completion rate came on record. No queue at any point; p95 TTFT 0.08 s in the first phase, 0.04–0.07 s in the second | [P/D path](well-lit-paths/pd-disaggregation/) |
| Missing it has a price | A cold controller sized the same window from occupancy and paid 4.2 s p95 | [P/D path](well-lit-paths/pd-disaggregation/) |
| The decision precedes the queue | Second replica ordered at +53 s, before the first tipped into preemption | [P/D path](well-lit-paths/pd-disaggregation/) |
| Roles scale on their own bottleneck | Decode 1 → 2 → 3, prefill never ordered | [P/D path](well-lit-paths/pd-disaggregation/) |
| The warm pool covers the rise | p95 TTFT per rise falls from 5.1–8.8 s to 0.11–0.83 s | [measured.md](well-lit-paths/warm-pool-bridge/measured.md) |
| It beats the floor it replaces | 14 138 GPU-seconds against 16 080, 12 % less, within 50 ms of the floor's latency on three of four rises | [measured.md](well-lit-paths/warm-pool-bridge/measured.md) |
| And what it costs | 17 % more than holding nothing, which came in at 12 129 | [measured.md](well-lit-paths/warm-pool-bridge/measured.md) |
| A warm Pod switches models fast | 437 ms against roughly 41 s cold, serving real gateway traffic | [fast model loading](proposals/fast-model-loading.md) |
| Cold start is mostly not the weights | 8B ~41 s; GLM 192 s of which 40 s is weights; 463 s on a cold JIT cache | [weight transfer](proposals/warm-pool-weight-transfer.md) |
| The figures repeat | The P/D pair was run twice a day apart, within a tenth of every number | [P/D path](well-lit-paths/pd-disaggregation/) |

How to read them. The warm-pool run is four scale-up events per arm, one run
each, so the 12 % and 17 % are single-run margins. The shape-swap run is
Qwen3-0.6B, chosen because saturation is easy to pin down on a small model; the
result is demonstrated there and expected, not shown, above it.

## Limits

**Scaling only helps if the router uses the new replica.** llm-d's shipped
profile weights the prefix-cache scorer above queue depth and KV utilization,
and its hashes chain from the first token, so a replica that has cached nothing
never receives a request and never starts winning. Measured with a shared-prefix
dataset, the busiest engine per model did 99.0 % and 98.7 % of prompt tokens
while every added replica did 0.3–2.0 %. Our warm-pool benchmark uses synthetic
prompts sharing no prefix, where an empty second replica took 49.4 % in its
first minute, which was necessary to measure a scale-up at all and is a
favourable condition. A bug report is drafted in [upstream/](upstream/).

**The demand floor is learned by being saturated.** A shape has to saturate once
before it can be priced well. A first-principles queueing model would not need
that; we take the trade because a measured rate is true of the hardware in front
of us rather than of a model of it.

**We do not forecast.** Closed-form buys a decision an operator can read and
argue with, and costs the ability to arrive before the load. We have not
measured against an autoscaler that does forecast.

**We assume capacity rises roughly in proportion to replicas.** Batching means
it does not do so exactly. Re-pricing from observed completion rates corrects
the error within a cycle or two, but we have not characterised its size.

**Not built:** SLA-target-driven scaling, where named policy tiers are a coarse
approximation; maintenance pre-scaling; failure replacement.

## Alternatives considered

**Threshold autoscaling on KEDA's own signals.** The shape-swap run settles it:
at a constant rate with no queue, occupancy pointed at one replica when three
were needed.

**A floor of replicas.** The closest competitor on latency, and 33 % more
GPU-seconds against the pool's 17 %. Still the better choice when a model is
always bursting and the fleet never comes back down.

**Faster storage or peer weight transfer.** Weights are 40 s of a 192 s GLM
start, so a perfect transfer only reaches about 152 s. Worth having, and ours is
byte-identical at roughly 9× a storage reload, but it does not fix start latency.

**Process snapshots.** These would remove the ~152 s nothing else touches. Built,
and they do not work: clean on one process, and they hang on a real multi-rank
engine inside NVIDIA's checkpoint path.

**A warm launcher holding no GPU.** An unbound sleeper often cannot wake at all.
Holding the accelerator is what makes the wake possible.

## Scope and status

We decide and Kubernetes actuates, through the KEDA external-scaler contract
with KEDA owning the HPA. One consequence worth knowing: the HPA takes the
maximum published inside its scale-down window, so we hold a published
scale-down across cycles to stop one noisy sample pinning the fleet. Node
provisioning belongs to the cluster autoscaler, quota between models to Kueue,
tenant quota and routing to the gateway.

Running on CoreWeave H200s and on OpenShift. The scaling path, warm pool,
scale-to-zero and quota limiting are built and cluster-verified; replica
reallocation across priorities is designed but not built; P/D role switching is
experimental. Apache 2.0, and the module path has not changed.

Design detail lives in [concepts/](concepts/): what gets measured and how it
becomes a replica count, the queueing model, and what the GPU budget counts.
