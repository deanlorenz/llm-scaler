# Share one GPU quota between models

> **Experimental.** Measured on H200s with two aggregated models at 1 GPU per
> replica, one run per configuration ([what was measured](measured.md)). Not
> yet measured with this optimizer: whole-node or multi-GPU pods, P/D models
> (P/D scaling itself is measured on [its own path](../pd-disaggregation/)), a
> cluster-wide quota, and the GPU cost of the current build. The defaults are
> starting points.

Several models share one GPU quota, and their traffic peaks at different times.
Today's optimizer hands the quota out first come, first served: a model that
scaled up earlier keeps its GPUs, and a model that needs them later is refused,
even when the quota as a whole has room for both. The worst case is a swap — one
model's load falls while the other's rises, and the rising model is refused for
the falling model's whole scale-down window. The utilization-share optimizer
shares the quota on purpose: each model gets what it needs, the rest is spread
as headroom by weight, and GPUs move from one model to another when the shares
drift ([how](#how-it-shares-the-quota)).

What it buys: on the latest run, the model that burst first was served at once —
median time to first token 0.07 s, against 25.6 s with the same fleet planned
but not moved — because GPUs were already waiting for it. The model that burst
second was no worse off: the same p95 (287 s), and a median of 144 s against
215 s.

**Use it when** two or more models scale under one quota, their demand moves
between them over hours rather than minutes, and the GPUs are paid for whether
used or not (on-prem hardware, a reservation, a team allotment), so idle
headroom costs nothing extra.

**Do not use it when:**

- **There is nothing to share.** One model per quota gains only headroom.
- **There is no limiter.** Without a `limiters:` list the optimizer has no
  budget and stays off (mode `invalid`).
- **You want idle GPUs given back.** It holds headroom on purpose
  ([What it costs](#what-it-costs)).
- **The model that rises at a swap cannot wait.** GPUs held by another model
  move only through that model's scale-down window: about 5.5 minutes later
  than today's optimizer in the first benchmark.
- **Load swings back faster than a move lands.** A move takes about twelve
  minutes with default settings. Standing still beats moving when load
  reverses within roughly 100 minutes
  ([proposal §6.7](../../proposals/utilization-share-optimizer.md#67-why-it-does-not-oscillate-and-what-it-cannot-follow)).
- **The donors cannot be steered.** A Deployment that is often mid-rollout, or
  has pods Pending or not Ready, is refused as a donor (`donor-not-steerable`).
- **The real limit is something else.** If a Kubernetes `ResourceQuota` or
  unmanaged workloads are what stop pods, a donor's freed GPUs can be taken by
  a pod the scaling manager did not place (`release-taken`).
- **A model runs on more than one accelerator type.** It is never planned; it
  stays on today's optimizer.

## What it needs

- A limiter in the **cluster default** policy entry, with accelerators that
  resolve. Either a quota, which
  [cap what each tenant may take](../tenant-gpu-quotas/) declares (its key must
  be the accelerator name the controller logs for your variants, for example
  `H200` on one cluster and `NVIDIA-H200` on another; its numbers can also be
  read from Kueue's ClusterQueues: [bound tenants by the quotas Kueue already
  holds](../kueue-bounded-quotas/)), or the physical GPU inventory that [bound-by-gpus](../bound-by-gpus/) declares, with
  `physicalGroups: true` in the block. Only the quota has been measured; under
  the physical limiter the budget is every GPU of a type on the cluster
  ([what differs](../../guides/utilization-share/README.md#under-the-physical-gpu-limiter)).
- Model servers that drain on termination (`preStop` and
  `terminationGracePeriodSeconds`): a transfer removes a donor pod through an
  ordinary scale-down.
- A distinct `CONTROLLER_INSTANCE` per controller when several share a
  cluster; each cleans up only its own transfer marks.
- Node information for placement. A namespace-scoped install cannot list
  nodes, so it plans by GPU count alone and never reports `release-taken`,
  `release-shape-mismatch` or `wrong-pod`.

## How it shares the quota

In the order it is applied:

1. **Each model first gets its need:** the GPUs that put it at its
   `scaleUpThreshold`.
2. **The rest is headroom,** shared in proportion to each model's weight
   (`standard` 1, `important` 2, `critical` 4). While the quota cannot hold
   every model's recent peak at once, headroom is capped at
   `maxHeadroomReplicas` (default 1) times the weight, and the rest stays
   unassigned, so a model whose demand rises starts pods into it at once.
3. **A model too far from its share gets GPUs through a transfer:** the
   optimizer marks one donor pod, lowers the donor, waits until the GPUs are
   actually free, and only then raises the receiver. The scale-down always
   comes first.

## Declaring it

One `optimizer:` block in the scaling policy's `default` entry, beside the
limiter, rolled out in two steps: **shadow** (it computes and reports, and
moves nothing), then **active**. Each step is one `make` target
(`utilization-share-check`, `-shadow`, `-activate`, `-status`, `-off`) that
writes the block, waits for the controller to read it and reports a refusal.
The guide has them in order:
[Turn on the utilization-share optimizer, safely](../../guides/utilization-share/).

Model owners choose a weight on their own policy entry (`weightClass:
important`, or `weight:`). A weight buys a larger share of the spare, not
protection: only a floor (`minReplicaCount`) is never taken
([for model owners](../../reference/scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model)).

## Four things that surprise people

**A missing `shadow` key acts.** `shadow` defaults to `false`, so a block
without it moves replicas from the next cycle. Write `shadow: true` until you
have read what it would do.

**Turning it on freezes the group for about three minutes.** When it starts
acting, after a controller restart or leader change, and when a group first
appears, every planned model holds what it runs for one fill timeout and shows
the blocked reason `quiet-period`. Expect it on every upgrade.

**A model that just gave GPUs cannot get them back for a while, even when it is
short.** The reversal hold (`reversal-hold`) lasts about twice a release time,
on the order of 12 minutes with a 300 s scale-down window. Only a hard
imbalance skips it: the short model at 0.9 or more of its scale-up threshold
while a donor would stay at 0.6 or less (`stabilization.skipWaitsWhen`).

**One slow donor slows the whole group.** Release and fill timeouts come from
the slowest donor in the group: one model with a 30-minute scale-down window
stretches every transfer. `wva_utilization_share_effective_seconds` shows the
values in force.

## Verifying it

```promql
# the mode in force: exactly one series is 1 — off, invalid, shadow or active
wva_utilization_share_mode{namespace="<controller-namespace>"} == 1

# once active: transfers that ended, by outcome (done is healthy)
sum by (outcome) (increase(wva_utilization_share_transfers_total{scope="<quota-namespace>"}[1h]))
```

`namespace` on the mode is the controller's namespace and `scope` is the
quota's; on a namespace-scoped install both are yours. Rising `aborted`, `fill-timeout` or `wrong-pod` outcomes mean transfers are not
landing. The guide's [Verification](../../guides/utilization-share/#verification)
has the check for each rollout step, both ways to read the metrics (Prometheus,
scoped to your namespace, as a tenant; the controller's endpoint as a cluster
admin) and what quiet-but-healthy looks like. When it evaluates but never moves
anything, see
[troubleshooting](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## What it costs

- **Headroom held ahead of need.** While every peak fits, the fleet holds the
  whole quota; while they contend, each model holds up to
  `maxHeadroomReplicas` times its weight above its need. That is the point of
  the mode, and a cost wherever GPUs could be given back: 24% and 66% more
  GPU-seconds than today's optimizer in the early runs, when the whole spare
  was spread. The current build's cost has not been measured.
- **Spare given back, then started again.** While the quota is contended, a
  model above its need plus headroom returns the extra after
  `consecutiveCyclesBeforeMove` cycles, once its load is at or below
  `stabilization.returnSpareWhenLoadAtMost`. While more than 0.6 x
  `queueLengthThreshold` requests (3 by default) wait on one of its pods — a
  P/D decode pod only while its KV cache is full, prefill pods not at all — the
  return waits, for at most its scale-down window. If its demand comes back
  after the drain, it starts a pod again, cold
  ([troubleshooting](../../reference/troubleshooting.md#a-model-shrinks-while-no-other-model-grows)).
- **A donor replica, with its in-flight requests.** Requests survive only as
  well as the model server drains.
- **The receiver waits for the release:** the donor's scale-down window, its
  drain, then the receiver's own start.
- **The freeze and the hold** above.
- **Capped headroom while no history is stored.** For a new group, or after a
  restart that finds no history in the ConfigMap
  `wva-utilization-share-history`, every model counts at its `maxReplicas`
  until a `flappingWindow` of history exists (about 100 minutes by default).
- **Pod annotations.** Each donor pod a transfer chose carries a transfer mark
  and a low `pod-deletion-cost` until the transfer ends. A downgrade to a
  version without the optimizer leaves them behind
  ([how to remove them](../../reference/troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade)).

Some traffic shapes still lose something, in simulation (share of demand left
unserved). If a model of yours looks like one of these, give it a floor
([for model owners](../../reference/scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model)):

- **A noisy model beside one that bursts for long.** The noisy model gets its
  headroom on its average demand rather than at each crest (blocked reason
  `fast-swing-capped`): 0.02 to 0.06 more unserved, while the bursting model
  goes from 0.33 to 0.21.
- **Whole-node models bursting for 20 to 30 minutes in a tight quota.** Moves
  that would land after the burst ends keep the usual pace (`burst-ending`);
  still about 0.007 worse than without that rule.
- **A dip right after a group starts.** For about the first 12 minutes no
  model's peak is known, so a model in a dip gives replicas back and
  cold-starts them when demand returns (0.038 unserved for a 12-minute dip).
  Stored history avoids it after a restart.

## How it is benchmarked

[Two models, anti-phase bursts, under one quota](../../guides/benchmarking/two-model-utilization-share.md)
runs the same traffic three times, differing only in the `optimizer:` block:
none (`today`), `shadow`, and acting (`share`). Six runs so far, each arm once,
two 8B models at 1 GPU per replica:

| Run | What it showed |
| --- | --- |
| Same-shape bursts, 4-GPU quota | The first model to rise had headroom serving; the model rising at the swap grew about 5.5 minutes late; +24% GPU-seconds. |
| Different shapes, 10-GPU quota (two runs) | The second model to burst ran on 3 of its 8 replicas (median 575 s, then 564 s, against 1 s). Fixed since. |
| With those fixes | First burst gained (12 s against 35 s); second lost (p95 313 s against 139 s): the spare sat on the idle model, draining long outputs. Led to the headroom cap. |
| With the headroom cap | First burst 0.07 s against 41 s; second reached its fleet sooner; +34% GPU-seconds, mostly an idle-fill bug. |
| With the idle-fill fix | First burst 0.07 s against 25.6 s; second no worse; GPU cost lost to a sampling gap. |
| Both models P/D, four roles, 10-GPU quota | Output-heavy burst 0.095 s against 42 s; prompt-heavy burst overloaded in every arm (p95 59 s against 73 s); +36% GPU-seconds. Run on vLLM v0.31.0 ([why](measured.md#why-vllm-v0310)). |

Every number, table and timeline is in [measured](measured.md).

## How it is tested

- Unit: configuration (`internal/config/utilization_share*_test.go`),
  planning (`internal/engines/allocation/utilization_share_*_test.go`) and the
  cycle (`internal/engines/steadystate/utilization_share_*_test.go`).
- End-to-end on kind with emulated GPUs (`make test-e2e-full`, labels `full`
  and `utilization-share`): shadow then active under a 4-GPU namespace quota
  (`utilization_share_test.go`), a P/D LeaderWorkerSet funded by a donor set
  (`utilization_share_pd_test.go`), and a 2-GPU receiver funded on a full node
  (`utilization_share_nodes_test.go`). They skip when no node carries a GPU
  product label, and a skip reports as success: run them with `-v`
  ([testing](../../developer-guide/testing.md)).
- **Not covered:** real model servers in the e2e; whole-node, multi-GPU or P/D
  pods under real load; a cluster quota and the `clusterNamespaces` canary
  (unit only); a restart that rebuilds transfers from pod marks (unit only).

## Tuning it

Every key (`tolerance`, `reserveGPUs`, `maxHeadroomReplicas`, `shadow`,
`physicalGroups`, `weightClasses`, `namespaces`, `clusterNamespaces`,
`stabilization`) and what the optimizer writes on pods:
[`optimizer` in the scaling policy](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).
Every metric, transfer outcome and blocked reason:
[utilization-share metrics](../../reference/prometheus.md#utilization-share-optimizer-metrics).
Events: [monitoring](../../reference/monitoring.md#events-on-the-models-scale-targets).
When it does not do what you expect:
[troubleshooting](../../reference/troubleshooting.md#utilization-share-optimizer).
The design and its simulators: [the proposal](../../proposals/utilization-share-optimizer.md).
