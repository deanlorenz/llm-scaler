# Sharing a GPU budget between models

## The use case

Several models share a fixed GPU budget: a quota, or a fixed fleet. Load moves
between them during the day. When one model's load drops and another's rises,
the GPUs the first one holds should go to the second, without dropping requests
in flight and without pushing the first model past its own latency target.

llm-scaling-manager does this with the experimental
[utilization-share optimizer](../well-lit-paths/utilization-share/):

- **It picks the donor.** Each model is first given the GPUs its demand
  needs, and the rest is shared by weight. GPUs are taken from the model best
  off against its demand and weight, and only down to what keeps it inside its
  band; never below its floor or its last replica.
- **It plans each move.** The donor is lowered first. The receiver is raised
  only once those GPUs are actually free. A move is called off while it is
  still waiting if the load swings back, and a model that just gave GPUs
  cannot take them back for a while (the reversal hold).
- **It drains through scale-down.** The chosen donor pod is marked to be
  removed first and goes through an ordinary scale-down, so in-flight requests
  are kept as well as the model server drains them.
- **It knows the nodes.** On a cluster-scoped install it opens a hole that fits
  every pod of the receiver's replica, node by node, so a multi-pod replica
  (for example a LeaderWorkerSet group) is funded whole. A P/D model is planned
  as one model, and is held whole when either role cannot be sized.
- **It shares what is left.** What a warm pool wants and does not hold is
  taken out of the shared budget. The budget can be a declared quota, the
  physical GPUs, or the quota Kueue holds, including the cohort borrowing
  it counts as safe to use.

## What llm-d offers today

As of 2026-10-26, llm-d has no counterpart for this use case: using every GPU
in the budget and moving GPUs between models by their load. Its Kueue guide
lets a model borrow GPUs another model is not using, but GPUs go back only
when Kueue preempts a borrowed pod for a model returning to its own floor.
Nothing moves GPUs by load: a lightly loaded model keeps what it holds until
its own HPA scales it down, and a busy model above its floor cannot take GPUs
from a quieter one. The guide's rule is *"you may preempt to reach your own
floor, never to go beyond it. Above your floor you can only take what is
idle."* Each model's HPA scales on its own and does not see the others' load.

In detail:

- **Each Deployment scales on its own.** llm-d scales with KEDA on the
  router's (EPP) metrics, one ScaledObject per Deployment. Its strategies are
  queue (stable), saturation, token-aware (experimental) and SLO-aware
  (experimental / scope-limited)
  ([autoscaling workflow](https://github.com/llm-d/llm-d-autoscaling/blob/main/docs/developer-guide/autoscaling-workflow.md)).
- **Sharing a GPU budget is left to Kueue.** The
  [Kueue rebalancing guide](https://github.com/llm-d/llm-d/tree/main/guides/workload-autoscaling/kueue-rebalancing)
  gives each model a ClusterQueue whose `nominalQuota` is its guaranteed floor,
  in a shared cohort. A model borrows what the others do not use, a pod waits
  at a scheduling gate until quota is free, and a model preempts to take its
  floor back. *"The HPA is never touched"*: each model's HPA still scales on
  its own.
- **Across variants.** llm-d's
  [architecture page](https://llm-d.ai/docs/dev/architecture/autoscaling)
  says *"Each Deployment scales independently; no cost-aware preference between
  variants"* and *"Cost-aware placement across variants has no current
  equivalent"*.

## Kueue rebalancing and utilization share

| | Kueue rebalancing (llm-d) | Utilization share (llm-scaling-manager) |
| --- | --- | --- |
| Use every GPU and follow the load | No. Idle GPUs can be borrowed, but nothing moves GPUs from a quieter model to a busier one above its floor. | Yes. Each model gets its need, the spare is shared by weight (capped while the budget is contended), and GPUs move as demand shifts. |
| Who decides a model shrinks | Nobody. An idle model keeps its GPUs until its own HPA scales it down; a borrowed replica is preempted when its lender wants its floor back. | The optimizer picks the donor and how many GPUs it gives. |
| How GPUs move | The HPA deletes pods, or Kueue preempts a borrowed one; a waiting pod is then admitted. | A planned move: the donor gives first, and the receiver grows once those GPUs are free. The donor drains through scale-down. |
| Placement | GPUs counted against each ClusterQueue's quota. | Node-aware on a cluster-scoped install: the receiver's replica must fit in the holes the donors open, node by node; multi-pod replicas are funded whole. |
| Signal | The replicas each HPA requests. | Each model's demand against its share, with confirmation cycles, a tolerance band and a reversal hold. |

**They compose.** Kueue gives each model a floor and enforces the budget.
llm-scaling-manager can read that quota, borrowing included, and decide which
model holds the shared GPUs above the floors
([Kueue-bounded quotas](../well-lit-paths/kueue-bounded-quotas/)).
