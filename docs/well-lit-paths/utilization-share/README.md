# Share one GPU quota between models whose peaks do not coincide

> **Experimental.** The optimizer is built, in shadow mode and acting, with
> donor sets, P/D roles and node-aware placement. Unit tests cover it, and three
> end-to-end specs run it on kind with emulated GPUs and the inference
> simulator. **Four benchmark runs, on two aggregated models with 1 GPU per
> replica,** measured it on H200s ([measured](measured.md)). The first, with
> bursts of the same shape, showed idle headroom serving the first rise and a
> delay of about 5.5 minutes at the swap; its effect on latency was
> inconclusive (two arms that scaled identically differed by up to 9 s p95).
> Two runs with different burst shapes found a failure: the second model to
> burst ran its whole burst on 3 of its 8 replicas, at a 575 s and then a
> 564 s median time to first token against 1 s without sharing, while the
> other model, idle, held the GPUs. A fourth run, with those fixes, lost the
> second burst a different way: the spare sat on the idle model as headroom,
> and each replica the bursting model needed waited about six minutes for that
> model's long requests to drain (p95 313 s against 139 s). The response -- the
> headroom capped at one replica times the model's weight by default when the
> quota cannot hold every model's peak at once, the rest left unassigned, and
> a burst that persists taking its whole shortfall at once -- is built
> (`2892aad3`) and **not yet measured** on a cluster. Each arm ran once; whole-node and multi-GPU pods were not
> measured; and P/D could not be measured: prefill never scaled, so prefill
> and decode never competed for GPUs. The defaults may also
> change: the `tolerance` of 0.15, the weight classes, a `reserveGPUs` of 0 and
> a `maxHeadroomReplicas` of 1 are starting points, and the
> [proposal](../../proposals/utilization-share-optimizer.md#14-open-questions)
> says they should be tuned from shadow-mode data. The short scale-down window
> for urgent transfers (stage 3) is still a design.

Several models share one GPU quota, and their traffic peaks at different times.
Today's optimizer hands out a quota first come, first served. A model that
scaled up earlier keeps its GPUs, and a model that needs them later is refused.
The worst case is a swap: one model's load falls while the other's rises. The
falling model keeps its replicas through its whole scale-down window, and the
rising model is refused the GPUs it needs during that time, even though the
quota as a whole has enough for both.

The utilization-share optimizer shares the quota on purpose. Each model first
gets its **need**: the GPUs that put it exactly at its `scaleUpThreshold`. The
rest of the quota is shared as **headroom** above need, in proportion to each
model's **weight**. When the quota is **contended** -- it cannot hold every
model at its recent peak (over the `flappingWindow`) at the same time -- that
headroom is capped at `maxHeadroomReplicas` replicas (one by default) times the
model's weight, rounded up: 1 for `standard`, 2 for `important`, 4 for
`critical`. What is left stays unassigned in the quota, where a model whose
demand rises starts pods into it at once, without waiting for another model's
pods to drain. No model is capped below what the other models' peaks leave of
the quota, so a model that bursts alone keeps its spare. While no model's peak
is known -- a new group, or after a controller restart that finds no stored
history, until a `flappingWindow` of history exists -- every model counts at
its `maxReplicas`, so the cap is on until the peaks are known. The history is
stored in a ConfigMap and read back after a restart or a leader change. A model added to a group whose peaks are
known counts at what it holds. While every peak fits, the whole spare stays
spread: capping there only made the next burst cold-start replicas that the
spread had kept warm, and a model already at its need takes no headroom that
would leave another model below its own recent peak. When the GPUs a model holds drift too far from that target,
the optimizer moves GPUs with a **transfer**:

1. It marks one of the donor's pods, so the ReplicaSet removes that pod and no
   other.
2. It lowers the donor's target.
3. It waits until the donor's GPUs are actually released.
4. Only then does it raise the receiver.

The scale-down always comes first, and the receiver is raised only into GPUs
that are free.

**Use it when** two or more models scale under one quota, as a namespace quota
or a cluster quota per accelerator type, and their demand moves between them
over hours, not minutes. It also fits an allotment that is paid for whether
used or not (on-prem hardware, a reservation, a team quota), where an idle GPU
is pure loss and headroom costs nothing extra.

**Do not use it when:**

- **There is nothing to share.** With one aggregated model per quota there is
  no other model to move GPUs to or from. The only effect is that the model is
  raised into the quota's spare GPUs as headroom.
- **There is no quota limiter.** Without a `limiters:` list the optimizer has no
  budget, and it stays off (`invalid`). Under a `gpu-inventory` limiter alone, it
  plans no cluster group unless you set `physicalGroups: true`. Spending the
  budget there means holding **every GPU of that type** in the cluster.
- **You want to give unused GPUs back.** This mode deliberately trades cost for
  headroom: while every model's peak fits the quota it spends the whole quota;
  while they contend it holds each model a replica (times its weight) above its
  need by default, and with a large `maxHeadroomReplicas` the whole quota. A fleet billed per GPU-hour that can
  return idle GPUs should not turn it on.
- **The donors cannot be steered.** A transfer depends on choosing the pod that
  goes, and some workloads do not allow it. A Deployment that is often
  mid-rollout, a Deployment with pods stuck Pending or not Ready, or a
  LeaderWorkerSet whose next group to go is terminating is refused as a donor
  (`donor-not-steerable`) and backs off. A fleet that is like this most of the
  time gets no transfers.
- **The model that rises at a swap cannot wait.** At a swap, the headroom it
  needs is held by the falling model, and a transfer releases it only through
  that donor's scale-down window. The rising model waits at least one window
  before it grows. In the benchmark it started growing about 5.5 minutes later
  than under today's optimizer, which had already scaled the falling model
  down. When the two peaks do not fit the quota together, the falling model
  now starts giving what it holds beyond its need plus its headroom
  (`maxHeadroomReplicas`, default 1) back to the quota once it has held it for
  `consecutiveCyclesBeforeMove` cycles (two by default) after its load falls;
  the replica goes after its scale-down window, as today's optimizer would
  scale it down, so less of the rise should wait for a transfer; that has not
  been measured yet.
- **Models burst in turn and a burst must not start short.** In two benchmark
  runs with different burst shapes, the second model to burst waited 16 and
  then 18 minutes -- its whole burst -- for GPUs the other, idle, model held.
  The rule that lifts the hold for such a move worked when measured, but the
  burst still lost: each replica it took waited about six minutes for the idle
  model's long requests to drain. Its response (the spare left unassigned,
  and the whole shortfall taken at once when the imbalance holds a second
  cycle) is built but not yet measured, and
  neither covers every case: a model whose burst of long outputs has
  just ended still reads its draining backlog as need, so GPUs can even move
  *to* it as the other model's burst begins. If you take the mode anyway, give
  each such model a floor sized for its burst
  ([for model owners](../../reference/scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model)).
- **A bursting model shares the quota with a noisy one.** In simulation, a
  model whose demand bursts beside a model whose demand swings loses 0.10 to
  0.14 more of its demand than under earlier versions of the optimizer, while
  the noisy model does much better. If you take the mode anyway, give the
  bursting model a floor sized for its burst.
- **Whole-node models burst for 20 to 30 minutes in a tight quota.** In
  simulation, models of one 8-GPU pod per replica with bursts that short do
  slightly worse (about 0.02 more of their demand unserved) than before a hard
  imbalance's moves stopped waiting for the pace: those moves land after a
  short burst has ended. This is deferred, not fixed.
- **The real limit is not the scaling manager's quota.** A quota counts only
  what the scaling manager's own variants hold. If a Kubernetes `ResourceQuota`
  or unmanaged workloads on the same accelerators are what actually stops
  pods, a donor's released GPUs can be taken by a pod the scaling manager did
  not place (`release-taken`). The quota then shows room that the cluster does
  not have.
- **Load swings back faster than a transfer lands.** A move takes effect about
  twelve minutes after it is decided with default settings: two cycles to
  confirm the change, the donor's scale-down window and drain, then the
  receiver's start. In the proposal's simulator, standing still beats moving
  GPUs when the load reverses within about eight of those latencies, roughly
  100 minutes
  ([§6.7](../../proposals/utilization-share-optimizer.md#67-why-it-does-not-oscillate-and-what-it-cannot-follow)).
  The swing rule limits that loss but does not turn it into a gain. These are
  design figures. The first benchmark run measured about 8 minutes from a rise to a
  serving replica on an 8B model, with releases of about 6 minutes
  ([measured](measured.md#what-this-says-and-how-far-it-goes)); a larger model
  takes longer.

Also, a model whose variants run on more than one accelerator type is never
planned: it stays on today's optimizer.

## What it needs

- A quota limiter in the `limiters:` list of the **cluster default** policy
  entry, with accelerators that resolve. [Cap what each tenant may
  take](../tenant-gpu-quotas/) declares the limiter, and
  [bound-by-gpus](../bound-by-gpus/) checks the accelerators first. The
  optimizer block is read from that same entry and nowhere else.
- Model servers that drain on termination. A transfer removes a donor pod
  through an ordinary scale-down, and the only protection for requests in
  flight on that pod is its `preStop` hook and `terminationGracePeriodSeconds`.
- When several controllers share a cluster, a distinct `CONTROLLER_INSTANCE` on
  each one. Every transfer mark records the controller that wrote it, and a
  controller cleans up only its own marks. Two controllers with the same value
  remove each other's live marks.
- For placement, node information. A namespace-scoped install cannot list
  nodes, so it plans by GPU count alone. It does not check that the GPUs a donor
  frees are on a node where the receiver's pod fits, and the two blocked reasons
  that need nodes (`release-taken`, `release-shape-mismatch`) never appear.
  Nor does the `wrong-pod` outcome: without a node-aware plan a release is
  judged by count, so a different pod leaving counts as the release.

## Declaring it

One block in the cluster `default` entry, beside `limiters:`:

```yaml
default: |
  analyzers:
    - type: saturation
  limiters:
    - type: quota
      name: cluster-h100
      scope: cluster
      quotas: { H100: 32 }
  optimizer:
    type: utilizationShare
    utilizationShare:
      shadow: true          # compute, publish and log; move nothing
```

**Roll it out in three steps:**

1. **Shadow.** The block above does everything except move pods. Leave it in
   shadow long enough to cover the load you care about, including each model's
   peak. Then read what it would have done (see
   [Verifying it](#verifying-it)).
2. **Canary, on a cluster quota.** `clusterNamespaces: [team-canary]` makes the
   cluster group plan only the models of the namespaces you list. Every other
   model stays on today's optimizer. On namespace quotas, use
   `namespaces.<ns>.enabled: false` to keep a namespace out instead.
3. **Active.** Set `shadow: false`. The key **defaults to `false`**, so a block
   with no `shadow` key acts from the next cycle.

Model owners pick a weight class on their own scaling-policy entry
(`weightClass: important`, or a `weight:` number). Raising a model's
`minReplicaCount` protects it outright, because a floor is never taken. Every
other model in the group pays for that floor. A weight does not protect: it
buys a larger share of the spare, a smaller share of a shortfall and more
headroom replicas, and that headroom can still be taken for another model's
need. What can be taken from a model,
which pod goes and how to protect one are covered in the reference:
[for model owners](../../reference/scaling-policy.md#for-model-owners-what-the-optimizer-may-take-and-how-to-protect-a-model).

The step-by-step version, with the check after each step, is the guide:
[Turn on the utilization-share optimizer, safely](../../guides/utilization-share/).

## Four things that surprise people

**A missing `shadow` key acts.** `optimizer: {type: utilizationShare}` on its
own moves replicas from the next cycle. Write `shadow: true` explicitly until
you have read what it would do.

**Turning it on freezes the group for a few minutes.** When the optimizer starts
acting, when a controller restarts or the leader changes, and when a group first
appears, every planned model of the group holds what it runs for one fill
timeout. That is about three minutes with default timings, and nothing in the
group scales during it. Each of those models shows the blocked reason
`quiet-period` meanwhile. Expect the same freeze on every upgrade.

**A model that just gave GPUs cannot receive them back for a while, even when it
is short.** That is the anti-oscillation rule. It lasts about twice a release
time from the start of the transfer it gave in; with a 300 s scale-down
window, that is on the order of 12 minutes. It shows as the blocked reason
`reversal-hold`. It is lifted only for a hard imbalance: the short model's
load is at least 0.9 of its scale-up threshold and another model's, after
giving, is at most 0.6 of its own (both set by
`utilizationShare.stabilization.skipWaitsWhen`), the donor's load read on the
higher of its current demand and its peak over its scale-down window. A model
whose burst just ended
usually is not that lightly loaded, so the hold can still last a whole
burst.

**One slow donor slows the whole group.** The release and fill timeouts are
derived per group, from the slowest donor configuration in it: the longest
scale-down window, polling interval and termination grace. One model with a
30-minute scale-down window stretches every transfer in its group.
`wva_utilization_share_effective_seconds` shows the values in force and where
each came from.

## Verifying it

```promql
# the mode in force: off, invalid, shadow or active
wva_utilization_share_mode == 1

# shadow and active: which roles a move would fix, and how many replicas
wva_utilization_share_actionable == 1
wva_utilization_share_replicas_to_move
wva_utilization_share_headroom          # < 0: the role is short

# active only: transfers that ended, by outcome (done is healthy)
sum by (outcome) (increase(wva_utilization_share_transfers_total[1h]))

# models held back, and why (the optimizer sets its reasons only while it acts)
wva_model_scaling_blocked == 1
```

```bash
# the transfers, on the Deployments and LeaderWorkerSets they moved
kubectl get events -n <model-namespace> --field-selector reason=UtilizationShareGiving
kubectl get events -n <model-namespace> --field-selector reason=UtilizationShareReceived
```

`invalid` means the block did not validate, or there is no `limiters:` list. The
controller log says which: `Utilization share: invalid optimizer block; keeping
today's optimizer`. Today's optimizer then runs, and the limiters keep working.

In shadow mode, nothing is blocked by the optimizer, so no
`wva_model_scaling_blocked` reason comes from it. The log line `Utilization
share: would rebalance (shadow)` is written when the set of roles it would move
changes, not every cycle. Its `frozen` field lists the models it leaves to
today's optimizer, with the reason.

Once it acts, rising `aborted`, `fill-timeout` or `wrong-pod` outcomes mean
transfers are not landing. The alerts for that, and for a block that turned
`invalid`, are in
[monitoring](../../reference/monitoring.md#the-metrics-that-answer-specific-questions).
When it evaluates but never moves anything, work down
[the troubleshooting list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## What it costs

- **Headroom held ahead of need.** While every model's peak fits the quota,
  the fleet holds the whole quota as headroom. While they contend, each model
  holds up to `maxHeadroomReplicas` replicas (one by default) times its weight
  above what its load needs, from GPUs that would otherwise sit idle; with a
  value as large as the models' `maxReplicas` the fleet holds the whole quota. That is the point of the mode, and it is a cost
  wherever GPUs could be given back. The benchmark runs measured it with the
  whole spare spread: 24% and 66% more GPU-seconds than today's optimizer.
- **A model above its need plus its headroom shrinks with no other model
  growing.** Only while the quota is contended: after
  `consecutiveCyclesBeforeMove` cycles above it, it starts giving all the
  extra back to the quota in one cycle, while no model that can grow is short
  of its need and only while its load would stay at or below
  `stabilization.returnSpareWhenLoadAtMost`. The replica goes after the
  model's scale-down window, and stays if its demand comes back before then
  ([troubleshooting](../../reference/troubleshooting.md#a-model-shrinks-while-no-other-model-grows)).
  Each return drains a pod; if its demand comes back after the drain, it
  starts a pod again, cold. On the proposal's alternating-burst simulator,
  returns and the fills after them made 2 to 4 times as many pod starts and
  removals as before returns existed (`e55b02b2`; 240 to about 1060 over 30
  simulated hours at 40-minute bursts).
- **A donor replica, with its in-flight requests.** A transfer is a donor pod
  going away through an ordinary scale-down. Requests in flight on it survive
  only as well as the model server drains, and the termination grace also
  lengthens every transfer in the group.
- **The receiver waits for the release.** The receiver is raised only after the
  donor's GPUs are free: after the donor's scale-down window, its drain, and
  then the receiver's own pod start and model load. For that whole time the
  receiver runs short. A spike beyond a model's headroom and the quota's
  unassigned GPUs waits at least one donor scale-down window; at the benchmark's swap that was about 5.5 minutes
  later than under today's optimizer.
- **The freeze and the hold above.** About one fill timeout of no scaling in a
  group after every restart, and a reversal hold that can keep a short model
  waiting.
- **A window of capped headroom when no history is stored.** The need history
  is kept in the ConfigMap `wva-utilization-share-history` in the controller's
  namespace and read back after a restart or a leader change. For a new group,
  and after a restart that finds no usable history -- the first start, a
  deleted or unreadable ConfigMap, or a controller down for more than ten
  minutes -- each model counts at its `maxReplicas` until a `flappingWindow`
  of history exists (about 100 minutes by default) and headroom is capped,
  unless those fit the quota together. Spare that a spread would have kept warm may be returned
  meanwhile: in simulation, whole-node quotas whose peaks fit lost 2 to 10% of
  their demand for about two hours. The window is derived from the scale-down
  window, so a long scale-down window makes it hours.
- **Pod annotations.** While a transfer releases, each donor pod it chose
  carries a transfer mark and a low `pod-deletion-cost`. The controller removes
  both when the transfer ends or the optimizer is switched to shadow or off.
  A downgrade to a version without the optimizer leaves them behind:
  [marks left after a downgrade](../../reference/troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade).

## How it is tested

- Unit, configuration: `internal/config/utilization_share_test.go`,
  `utilization_share_active_test.go` and `utilization_share_canary_test.go`
  cover parsing, validation, the shadow and active modes and
  `clusterNamespaces`.
- Unit, planning: `internal/engines/allocation/utilization_share_*_test.go`
  (ten files) cover targets, groups, the plan, donor sets on nodes, the
  ledger and the history it retains, timings, back-off, withholding and
  concurrency.
- Unit, the cycle: `internal/engines/steadystate/utilization_share_*_test.go`
  (thirteen files) cover shadow, actuation, fills, floors, marks, namespaces,
  nodes, P/D pairs, blocked reasons, pod steering, mode transitions and errors.
- End-to-end, labels `full` and `utilization-share`, run by
  `make test-e2e-full` on kind with emulated GPUs:
  - `test/e2e/utilization_share_test.go` (*Utilization share optimizer*). Under
    a 4-GPU namespace quota, shadow mode evaluates the group and touches nothing.
    Then, active, A's marked pod goes first, and B grows after it.
  - `test/e2e/utilization_share_pd_test.go` (*on a P/D LeaderWorkerSet model*):
    B's decode grows by one group, funded by two of A's replicas as one donor
    set.
  - `test/e2e/utilization_share_nodes_test.go` (*on full GPU nodes*): B's 2-GPU
    pod is funded from two 1-GPU A pods the controller planned on one full node.

  All three skip when no node carries a GPU product label, and the last two
  also skip when no node has enough GPUs of one product. A skip reports as
  success, so run them with `-v`
  ([testing](../../developer-guide/testing.md)).
- **Not covered:**
  - A real model server or real accelerators: the e2e uses the inference
    simulator, so drain behaviour and model load time are not exercised.
  - Repeated measured load: each benchmark arm below ran once, on aggregated
    models only.
  - Whole-node or multi-GPU pods under real load: every benchmark run used
    1 GPU per replica, and the P/D scenario does too. The e2e covers 2-GPU
    receivers and LWS groups on emulated GPUs only.
  - The headroom cap (`maxHeadroomReplicas`), spare returns, and the unpaced
    hard-imbalance move, which no cluster run has measured yet (simulated
    only: [measured](measured.md#what-rounds-16-and-17-change-simulated-not-measured)). The cancel fix
    and the hard-imbalance rule were measured once, on the run before them.
  - A cluster quota group and the `clusterNamespaces` canary, which are
    unit-tested only. All three e2e specs use a namespace quota.
  - A controller restart that rebuilds transfers from pod marks or reads the
    need history back from its ConfigMap, a
    scale-from-zero wake claiming released GPUs (`redirected`), and a donor
    refused as `donor-not-steerable`, which are unit-tested only.

## How it is benchmarked

**Four runs, on two aggregated models with 1 GPU per replica: [what they
measured](measured.md).**

[Two models, anti-phase bursts, under one quota](../../guides/benchmarking/two-model-utilization-share.md)
is the two-model anti-phase load of the warm-pool benchmark. It runs under one
quota smaller than the two models' combined peak, three times, and the arms
differ only in the policy's `optimizer:` block: none (`today`), shadow
(`shadow`) and acting (`share`). One run, with 20-minute bursts under a 4-GPU
quota, showed:

- **The first model to rise had its second replica already serving.** Idle
  headroom had raised it from 1 to 2 before its burst; under today's optimizer
  it asked for a second replica 43 s into the burst and had it 127 s in.
- **The model rising at the swap started growing about 5.5 minutes later.**
  Today's optimizer had already scaled the falling model down to its need, so
  a GPU was free. This optimizer kept that GPU as headroom, so it had to be
  released through a transfer and the donor's scale-down window first. That
  delay is what the designed, unbuilt short window for urgent transfers is for.
- **It cost 24% more GPU-seconds**, because it holds the whole quota.
- The `shadow` arm, which makes today's decisions, differed from `today` by up
  to 9 s p95 on the same replicas. Every latency difference between the arms
  was smaller than that, so the run says nothing about latency on its own; the
  bullets above rest on the replica timelines.

Two more runs gave the models different bursts -- one on long prompts, one on
long outputs -- with up to 8 replicas each under a 10-GPU quota:

- **The second model to burst ran its whole burst short.** It stayed on 3 of
  its 8 replicas while the other model, idle by then, held 7: a 575 s, then a
  564 s median time to first token for the rest of its burst, against 1 s
  under today's optimizer. The first time a cancelled transfer held it; the
  second time the reversal hold did. Both are fixed in the code since, and
  neither fix has been measured yet.
- **Idle headroom still helped the first model to burst**: on the second
  run it began on 5 replicas instead of 1.
- **It cost 66% more GPU-seconds** on the second run (28 111 against 16 976).
  The cost depends on how much of the quota the load leaves idle: with the
  spare spread whole, as in these runs, the mode holds all of it.

A third run of that scenario, with both fixes, lost the second burst another
way ([measured](measured.md#rerun-with-the-hard-imbalance-rule-the-spare-was-parked-on-the-other-model)):

- **The first burst gained**: p50 12 s against 35 s under today's optimizer.
- **The second burst lost**: p95 313 s against 139 s. The spare sat on the
  model whose burst had ended, 7 replicas at 5% load serving 6000-token
  outputs, and each replica the bursting model got waited about six minutes
  for one of them to drain, two at a time: 18 minutes to reach 8 replicas,
  against 2 under today's optimizer, which found the GPUs idle.
- The hard-imbalance rule worked: every move to the bursting model skipped
  its waits from the first cycle. The response -- the headroom capped at
  `maxHeadroomReplicas` (default 1) times the model's weight while the quota
  is contended, the rest left unassigned, and a hard imbalance that holds a second cycle taking the whole
  shortfall up to the receiver's need at once -- is built and not yet measured
  on a cluster; a simulation is on the [measured](measured.md#what-rounds-16-and-17-change-simulated-not-measured)
  page.

A P/D version was attempted and not measured. Prefill never left one replica
under prompt-heavy load, so there was no prefill-versus-decode competition to
resolve: [why](measured.md#pd-not-measured).

## Tuning it

Every key (`tolerance`, `reserveGPUs`, `maxHeadroomReplicas`, `shadow`,
`physicalGroups`, `weightClasses`, `namespaces`, `clusterNamespaces`,
`stabilization`), how weights are set per
model, and what the optimizer writes on pods:
[`optimizer` in the scaling policy](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).
Every metric, transfer outcome and blocked reason:
[utilization-share metrics](../../reference/prometheus.md#utilization-share-optimizer-metrics).
The Events on scale targets:
[monitoring](../../reference/monitoring.md#events-on-the-models-scale-targets).
When it does not do what you expect:
[troubleshooting](../../reference/troubleshooting.md#utilization-share-optimizer).
The design and the simulator it was tested in:
[the proposal](../../proposals/utilization-share-optimizer.md).
