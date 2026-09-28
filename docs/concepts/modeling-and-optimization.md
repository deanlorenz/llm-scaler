# Modeling and optimization

llm-scaling-manager decides how many replicas each variant of each model should
have, in response to changes in the request rate and in the shape of the
requests (their input and output token counts). It is a *global* autoscaler
rather than a set of independent per-Deployment ones: every variant in the
system is solved together, each cycle, against one accelerator budget, using
the available accelerators, live load statistics, the SLOs declared for each
model, and workload priority.

This page covers the terms that decision is stated in, what is modelled, and
how a set of per-variant demands becomes a replica count. For the measurement
path that produces those demands, read
[the steady-state engine](steady-state-engine.md) first.

## Definitions and assumptions

- An **accelerator** is a unit of allocation of (GPU) devices, of a given type and multiplicity, e.g. 2xH100 is an accelerator consisting of two H100 GPUs, in order to satisfy model memory constraints and/or performance.

- An **accelerator arrangement** consists of one or more accelerators in parallel (tensor or pipeline parallelism) assigned to a model server.

- A **variant** is a collection of model servers (variant instances or **replicas**) serving a given model, using the same accelerator arrangement. In the running system it is exactly one KEDA ScaledObject and the workload it scales: variant identity is the managed scaler a replica's `ownerReferences` lead to, and the variant's name is that scaler's name.

- The **model SLOs** define target values for two metrics:

  1. *TTFT*: The TTFT component includes request queueing time as well as waiting and performing prefill processing.
  2. *ITL*: This is simply the decode time to generate an output token. It is subject to elongation due to congestion, resulting from batching requests, injection of prefill processing during a long decode cycle, and factors related to KV caching and potential memory swapping.

- **Workload priority** (aka **criticality**) is an indicator of the importance of requests of a particular application (workload). It may serve different functions depending on the component that is handling the workload. For an admission controller, it may be used to decide on which stream of requests is more likely to be dropped. For a request scheduler, it may influence the position of a request in the queue and/or when dispatching a request. Here it decides the assignment of accelerators to variants when resources are tight, i.e. cannot accommodate the SLOs for all models.

## What is modelled, and what is not

Everything below is learned from live metrics. There is no performance-profile
store in this repository, no offline benchmarking step, and nothing is looked
up per model-accelerator pair: a variant that has never been observed under
load gets a conservative estimate rather than a profiled one. That is a
deliberate trade, and its cost is stated in the project proposal: a shape has
to saturate once before it can be priced well.

**Per-replica capacity, in KV-cache tokens.** A replica's usable capacity is
the smaller of a memory ceiling (its physical KV-cache size scaled by
`kvCacheThreshold`) and a compute ceiling (how many concurrent tokens it can
process before scheduling rather than memory binds). A variant's capacity is
the median across its ready replicas. This is what makes a decision divisible:
a replica count is `ceil(required tokens / per-replica capacity)`, so one cycle
can order three replicas rather than one.

**The saturated completion rate, per role.** The quantity the demand floor is
priced against is mu: the completion rate one replica sustained the last time
it was observed saturated, recorded per output-length bucket. Occupancy is a
state of the fleet and falls as replicas are added; mu is a property of the
hardware and the request shape and does not move when the fleet does. A fleet
needs `lambda / mu` replicas to keep up. See
[the throughput floor](steady-state-engine.md#the-throughput-floor).

**Inter-token latency as a function of KV utilization.** A separate throughput
analyzer fits `ITL(k) = A*k + B` over observed KV utilization `k` by ordinary
least squares, evaluates it at the saturation point to derive a per-replica
decode rate, and compares that against arrival rate
(`internal/signals/itl`, `internal/engines/analyzers/throughput`). It is not
enabled in the shipped configuration, which names the saturation analyzer only.

**What is not modelled.** There is no queueing-theoretic model of the server,
no batch-size-to-latency fit, and no parameter tuner. Earlier versions of this
page described all three, inherited from the project this one forked from; none
of that machinery is in this repository, and documenting it here was worse than
saying so. There is also no forecasting: the decision is taken on what is
measured now, which buys a decision an operator can read and argue with and
costs the ability to arrive before the load.

## Optimization

The optimizer turns each analyzer's per-variant demand into a replica count for
every variant of every model, inside whatever accelerator budget the limiter
declares. Two things decide the outcome.

**Cost, when there is room.** Variants are ranked by serving capacity per unit
of cost — the relative scalar each carries as `llm-d.ai/variant-cost` — so the
*most efficient* variant grows first, which is not always the cheapest one. A
TP=2 variant at twice the cost of a TP=1 variant is the better buy whenever it
serves more than twice as much. See `cost_aware_optimizer.go` and
`greedy_score_optimizer.go`.

**Priority, when there is not.** When the budget cannot meet every model's
demand, allocation is a priority-weighted fair share: a variant's `priority`,
which normally comes from its [policy tier](../well-lit-paths/workload-classes/),
is the weight in that split (`fairShareValue`). The optimizer records what it
could not give — which variant was limited, by what, and how many GPUs it did
get — and that surfaces as `wva_model_scaling_blocked` with a reason rather than
as silence.

**Constraints, not modes.** Limiters are ordinary constraints on the optimizer —
GPU inventory, declared quota, or none — chosen by the `limiters:` list on the
scaling policy, with no mode switch anywhere: see
[bounding a fleet by real GPUs](../well-lit-paths/bound-by-gpus/) and
[GPU capacity accounting](gpu-capacity-accounting.md).
