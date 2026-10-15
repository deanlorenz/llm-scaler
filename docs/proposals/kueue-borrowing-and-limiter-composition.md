# Kueue borrowing, and limiters that compose

- **Problem:** a Kueue-bounded namespace never grows into capacity its cohort
  lends ([#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136)),
  and two declared limiters do not bound a decision together.
- **Decision:** first make every declared limiter bound every decision, and a
  replica fit a node (Part A), then count borrowable cohort capacity where that
  is safe (Part B).
- **Shape:** Part A ships as PR-A
  ([#164](https://github.com/ev-shindin/llm-scaling-manager/pull/164),
  `feat/limiters-compose`), Part B and Part C as PR-B (`feat/kueue-borrowing`),
  reviewed in that order. Counting and giving back capacity of lenders Kueue
  does not reclaim for is a follow-up, PR-C (`feat/kueue-borrow-release`).
- **Opt-out:** `countBorrowable: false` on the quota entry gives exactly
  today's Kueue cap. Part A still applies.
- **Status:** proposed, revision 7 (after the conceptual review; see
  [Review](#review)).

When any condition fails, the namespace behaves exactly as today; the reason is
published on `wva_quota_borrow_withheld`. Borrowing is experimental but on by
default: it turns itself off, with a reason, wherever a condition does not hold.

This also covers the in-repo part of upstream
[llm-d WVA #1003](https://github.com/llm-d/llm-d-workload-variant-autoscaler/issues/1003)
(bound by `min(physical, quota)`). How it is built is in the developer page
[Kueue borrowing internals](../developer-guide/kueue-borrowing.md).

## What changes for you

1. **A policy that declares both `gpu-inventory` and `quota` is bounded by
   both.** Today the quota wins and the physical limiter is silently dropped.
   For one release a log line, `Upgrade note (transitional)`, names the models
   the physical limiter now holds back.
2. **Limiter entries that list different GPU types stop allowing a type that
   one entry denies.** Today they allow it. Bug fix.
3. **A replica must fit a node, not just the pool**, whenever a physical
   limiter is declared. Today 16 free GPUs as 4+4+8 order an 8+8 P/D pair whose
   prefill stays Pending forever. Bug fix.
4. **P/D pairs stop ordering both roles past a budget.** Today a budget of 8
   with two 8-GPU roles orders 16 GPUs. Bug fix.
5. **A Kueue-bounded quota counts capacity it can borrow from its cohort**, on
   by default, only where every [B1](#b1-when-borrowing-is-counted) condition
   holds, and only from lenders Kueue would reclaim for. Opt out with
   `countBorrowable: false`, or `WVA_QUOTA_KUEUE_BORROW=false` at install.
6. **Borrowing needs a GPU bound you declare.** Add a `gpu-inventory` entry or
   a GPU `ResourceQuota`. The installer does not add one for you. Without it,
   borrowing is withheld with reason `no-gpu-bound`.
7. **The scaling manager scales a borrowed replica down by itself only when it
   cannot be placed** (B5). Kueue's own preemption hands capacity back.
8. **New RBAC**, all read-only:
   - `get`/`list` on `cohorts.kueue.x-k8s.io`;
   - `get`/`list`/`watch` on `workloads.kueue.x-k8s.io`, cluster-wide (read by
     an informer);
   - `get`/`list` on `resourcequotas` in the managed namespaces;
   - `get`/`list` on `events`, for the cluster autoscaler's `TriggeredScaleUp`;
   - `get` on the owners of GPU pods (`batch` Jobs; the apps kinds and
     LeaderWorkerSets are already granted);
   - a Role in the Kueue namespace: `get` on `deployments` named
     `kueue-controller-manager`, and `get` on `leases`
     (`coordination.k8s.io`), for the version and the leader.

   A namespace-scoped install needs an admin to re-run `make setup-prereqs`.
   **Without the new Role it stops borrowing** (`kueue-version-unknown`).
9. **`WVA_LIMITER` accepts a list** of limiter types, and
   `make disable-limiter WVA_LIMITER_TYPE=gpu-inventory|quota|all` removes one
   kind or all.

Words used below: *nominal* is a ClusterQueue's `nominalQuota`; *guaranteed*
is the part of it the queue can count on now; a *lender* is a cohort member
whose unused nominal others borrow; the *borrow cap* is how far past
guaranteed a namespace may go; a *flavor* is a Kueue ResourceFlavor;
`reclaimWithinCohort` decides whether a lender can take lent capacity back by
preemption.

## The problem

Today the Kueue quota source caps a namespace at its ClusterQueue's
`nominalQuota`. When another queue in the cohort is idle, Kueue would admit
more replicas, but the scaling manager never asks for them.

Counting borrowed capacity raises a quota above the guaranteed figure. That is
safe only if something still checks that the GPUs exist and that a replica
fits on a node. Nothing does today, because limiters do not compose:

| Combination | Works today? | What happens | Evidence |
|---|---|---|---|
| `gpu-inventory` alone, or one `quota` entry | yes | | read |
| `quota` with `kueue.enabled` | yes | the smaller of static and Kueue | read |
| `gpu-inventory` + `quota` | **no** | Quota wins; the physical limiter is never built | read |
| two entries that list **different** types | **no** | the allowed types are the **union** | measured |
| one GPU spelled two ways (`H100`, `NVIDIA-H100-80GB-HBM3`) | **no** | treated as two types | measured |
| P/D pair against a shared budget | **no** | both roles commit before the budget is reduced | read |
| free GPUs split across nodes | **no** | ordered by the pool total; a replica larger than any node's hole stays Pending | read |
| Kueue borrowing | **no** | not read (#136) | read |

*Measured* rows were run as throwaway tests against `main` (39b9d0c8). The
union leak installs today: a cluster quota `{H100: 8}` plus a namespace entry
`x: {A100: 4}` lets `x` run A100s the cluster quota denies.

## Part A: limiters compose (PR-A, #164)

**The rule.** A decision must fit **every** declared limiter and a node. A GPU
type is allowed only if every limiter with a view on it allows it, and the
budget is the smallest available figure among them.

- **A1. Merge by intersection, along the GPU family tree.** A type that a
  *closed* limiter omits becomes an explicit 0. Physical inventory and a
  cluster-scope quota are closed; a namespace-scope quota is closed per
  namespace. GPU names form a family/product tree (`H100` is the family of
  `NVIDIA-H100-80GB-HBM3`), used in the merge and in every consumer (greedy
  optimizer, its debit, rescale, share planner, wake check, warm pool).
  - **An explicit family key covers unlisted products.** `{H100: 8, HBM3: 4}`
    admits any H100 product up to 8 in total, HBM3 up to 4. This is decided per
    provider before the merge. A family key the merge *computed* from an
    allowlist of products (`{HBM3: 4, SXM5: 4}`) bounds their sum and still
    denies a product it does not name.
  - **A workload that names the bare family** resolves to the family's summed
    bound and debits through the family, so 8+8 on a mixed cluster gives it 2
    replicas of 8.
  - **Demand is summed per family key**, in the wake check too: a P/D wake of
    HBM3 8 + PCIe 8 under a family residual of 12 is refused, as are two models
    waking in one tick that together exceed it.
  - **A workload with no named accelerator** is checked against namespace caps
    bounded by the cluster caps. In a namespace with a closed allowlist it fits
    only when the allowlist lists `unknown` or exactly one type; an allowlist
    of several types denies it, because its GPUs would be charged to no type.
- **A2. P/D pair commit.** The summed cost of all picked roles is checked
  against the budget before committing, and the roles scale down together.
  A role at its own `maxReplicas` still drops out of the pair.
- **A3. Build every declared limiter.** The physical limiter and every quota
  entry are built and grouped. At most one physical entry. The installer
  takes a list in `WVA_LIMITER`, and refuses a physical entry without the node
  read access the admin phase grants. `make disable-limiter` (alias
  `disable-physical-limiter`) removes the kind `WVA_LIMITER_TYPE` names, and
  exits non-zero when it removed nothing while the other kind stays.
- **A4. One missing input never lifts the others.** A provider that errors or
  is not yet observed is skipped, logged by name and counted on
  `wva_constraint_provider_skipped`. Only when every provider is unavailable
  does a decision go unchecked; scale-from-zero then uses its last snapshot if
  it is under 5 minutes old, and otherwise counts the wake on
  `wva_scale_from_zero_wake_unchecked_total`.
- **A5. A replica must fit a node.** Whenever a physical limiter is declared,
  one node view per cycle holds the free GPUs of each schedulable node.
  - Every replica ordered that cycle debits it, guaranteed or borrowed, across
    models.
  - "No node fits a whole replica" is scarcity, in the greedy optimizer and in
    the P/D pair check. A P/D pair needs both roles placed.
  - The scale-from-zero wake check uses the same view.
  - Pending pods with a `nodeName` or `nominatedNodeName` debit their node.
    Other Pending pods do not, so a hole can look free for the cycle before
    the scheduler nominates them.
- **A6. The warm pool takes the same bounds.** Its headroom is bounded by the
  closed cluster figure, counted in whole units per node from the node view,
  and published after the optimizer pass from the debited budget. 12 free
  GPUs as 4+4+4 afford no 8-GPU pool pod.

**Upgrade note.** A policy that declares both kinds is now also bounded by
physical GPUs, which count every GPU-holding pod, training included, so it can
be tighter than the quota. A tighter bound only stops growth; it never shrinks
a running model. For one release, the steady-state engine runs a shadow pass
over the quota alone once every 10 cycles and logs `Upgrade note
(transitional)` when the set of models it would size differently changes; it
is removed in the release after.

## Part B: count Kueue borrowing (PR-B)

### B1. When borrowing is counted

```yaml
limiters:
  - name: gpu
    type: gpu-inventory              # or a GPU ResourceQuota per namespace
  - name: tenant-quota
    type: quota
    scope: namespace
    kueue:
      enabled: true
      # countBorrowable: false        # default true
      # borrowNamespaces: [team-a]    # pilot; empty = every governed namespace
      # assumeTestedVersion: true     # default false; see kueue-version-unknown
    namespaceQuotas:
      default: { H100: -1 }
```

A namespace's borrowable figure is 0, with the reason shown, unless every
condition holds. These are all the reasons:

| Condition | Reason when it fails |
|---|---|
| **The namespace** | |
| It is in `borrowNamespaces` (when set) | `not-selected` |
| Kueue manages every GPU pod in it | `not-kueue-managed` |
| A GPU bound is in force | `no-gpu-bound` |
| Its ResourceQuotas are readable | `resourcequota-unreadable` |
| Every variant pins the flavor's `nodeLabels`, when its queue has more than one flavor for the GPU or `flavorFungibility.whenCanBorrow` is `Borrow`/`MayStopSearch` | `flavor-unpinned` |
| Every covered resource's request (CPU, memory, ...) is known from the pod template | `co-resource-bound` |
| **Kueue itself** | |
| The snapshot is fresh | `stale` |
| Every queue in the cohort has reservation status | `no-status` |
| Workloads are read cluster-wide, without error | `lenders-unreadable` |
| The controller's version is known | `kueue-version-unknown` |
| The controller runs a tested version | `kueue-version-untested` |
| The controller is up | `kueue-controller-down` |
| **The cohort** | |
| The queue is in a cohort | `no-cohort` |
| The queue admits (no `stopPolicy`, its flavors exist) | `inactive` |
| Its borrowing fields parse | `malformed` |
| The cohort is flat | `hierarchical-cohort`, `hierarchy-unknown` |
| No queue uses `status.effectiveQuotas` | `effective-quotas` |
| Kueue Fair Sharing is off | `fair-sharing` |
| No GPU flavor has a `topologyName` (Topology-Aware Scheduling) | `topology-aware` |
| No queue has admission checks (including MultiKueue) | `admission-checks` |
| No queue sets `preemption.borrowWithinCohort` other than `Never` | `borrow-within-cohort` |

Counted, but 0 this cycle:

| Reason | Meaning |
|---|---|
| `lender-cannot-reclaim` | the only unused capacity belongs to lenders Kueue would not reclaim for (B3) |
| `lender-waiting` | a lender waits for capacity it lent; no new borrowing in the cohort (B6) |
| `cohort-pool-empty` | the cohort's free pool holds no GPUs, after what is in flight and gated |
| `no-borrow-cap` | `borrowingLimit`, the static entry or the ResourceQuota stops the namespace at guaranteed |
| `preemptions-unreadable` | Kueue's preemptions in the namespace could not be read this cycle |

`nominal-lent-out` is not a withheld reason: it qualifies a guaranteed figure
below nominal (B3). `borrow-namespaces-split` is a warning, not a withholding:
two quota entries share a pool and only one sets `borrowNamespaces`.

- **Managed.** A GPU pod counts as managed only when a Workload with quota
  reserved backs it, requests its GPUs, and is owned by a kind a Kueue
  integration creates Workloads for (Pod, Job, JobSet, LeaderWorkerSet,
  StatefulSet). Labels, a queue name or a scheduling gate are not proof. Any
  unmanaged GPU pod hides GPUs from Kueue's figures, so the cohort looks idle
  while it is not.
- **GPU bound.** Borrowed GPUs belong to another tenant, so something must
  check they exist: the physical limiter, or a GPU `ResourceQuota` whose
  `hard` is at most nominal plus `borrowingLimit`.
- **Fresh.** A read is stale when it failed or is older than twice
  `refreshInterval`. **Forbidden is never "absent"**: a Forbidden read of
  Cohorts, ResourceQuotas, Workloads, the controller Deployment or its Lease
  withholds, it never reads as flat, unbounded, idle or up.
- **Tenant installs.** A namespace-scoped install that cannot list Workloads
  cluster-wide cannot see a lender wait, so it withholds with
  `lenders-unreadable`.
- **Version.** v0.19 and v0.20 are tested. Another version withholds with
  `kueue-version-untested`. A version that cannot be read withholds with
  `kueue-version-unknown`, unless `kueue.assumeTestedVersion: true`.
- **Controller up.** Down means the leader-election Lease's `renewTime` is
  older than twice its `leaseDuration`; without a Lease, the Deployment's
  `readyReplicas` is 0. A Deployment or Lease never found, or Forbidden, counts
  as down. Down freezes borrowing at the last figures.
- The rest are cases the B3 formula does not cover yet ([Not covered](#not-covered)).

### B2. What is read

ClusterQueues, Cohorts, ResourceFlavors, Workloads (cluster-wide), the
namespace's ResourceQuotas, and the Kueue controller's Deployment and leader
Lease, read-only. The field list is in the
[developer page](../developer-guide/kueue-borrowing.md#what-is-read).

### B3. How much a queue can reach

**In words.** Each queue keeps part of its nominal quota and lends the rest.
The cohort's free pool is what lenders that Kueue would reclaim for have lent,
minus what members use beyond the part they keep. A queue reaches its own
usage, or its kept part if larger, plus the free pool, and never past nominal
plus its borrowing limit. Kueue's
[documented example](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/)
holds: team-a (`nominal 9`, `borrowingLimit 1`) with an idle team-b
(`nominal 12`, able to reclaim) reaches 10.

Per flavor and GPU resource, for each cohort member `m`:

```
N_m = nominalQuota      L_m = min(lendingLimit, N_m)   (unset: N_m)
B_m = borrowingLimit    U_m = reserved GPUs             G_m = N_m - L_m

cohortFree = max(0, Σ_{m reclaims} L_m − Σ_m max(0, U_m − G_m))
reach_Q    = min(N_Q + B_Q, max(U_Q, G_Q) + cohortFree)
```

**Only lenders Kueue would reclaim for are counted.** A lender reclaims when
its `reclaimWithinCohort` is `Any`, or `LowerPriority` and its Workloads
outrank at least one Workload of the borrowing namespaces (Kueue's own rule).
Priority is the borrowing namespaces' own Workload priority, not the highest
in their ClusterQueue. A lender with `Never`, or `LowerPriority` that cannot
outrank, lends nothing borrowable: serving does not finish like a batch job,
so that lender would wait indefinitely. When such capacity is all that is
unused, the reason is `lender-cannot-reclaim`. Counting it, and handing it
back, is PR-C.

**Guaranteed.** A queue that can reclaim keeps `N_Q`; its growth up to `N_Q`
still debits `cohortFree` so no peer borrows the same GPUs. A queue that
cannot reclaim has guaranteed `min(N_Q, max(U_Q, G_Q))`, reason
`nominal-lent-out`. Guaranteed is per ClusterQueue: namespaces sharing one
queue share its figure, and Kueue's preemption victims span the queue.

**Co-resources.** Kueue admits per resource group: a lender that keeps its CPU
leaves borrowed GPUs unusable. Every resource the GPU flavor's resource group
covers is computed the same way, and the reach in replicas is the minimum over
those resources of free capacity divided by the variant's per-replica request,
read from its pod template.

The static entry and the namespace ResourceQuota cap the reach further.

### B4. One shape for borrowing

| Figure | Scope | What it is |
|---|---|---|
| guaranteed pool | namespace, type | the namespace's limit, as today, at the guaranteed figure |
| borrow cap | namespace, type | how far past guaranteed the namespace may go |
| shared pool | cohort, type, one chunk per flavor | the cohort's free GPUs, spent once across its namespaces |

- **The greedy optimizer** spends the guaranteed pool, then draws from the
  shared pool up to the borrow cap, one unit at a time. Every draw must also
  fit the closed cluster bounds and a node (A5).
- **A P/D pair borrows only as a whole unit**, both roles on borrowed capacity.
  A pair never straddles guaranteed and borrowed: a reclaimed decode would
  leave its guaranteed prefill serving nothing. Kueue admits each role's
  Workload on its own, not the pair atomically; the placement timeout (B5)
  releases a half left alone.
- **Rescale, the warm pool and scale-from-zero** use capacity within nominal
  only (B7).
- **The utilization-share optimizer** keeps the borrowed GPUs a group holds
  now, as no debt, and starts no new borrowing.
- **Several controllers in one cohort.** Gated GPU demand is counted from the
  pending and gated Workloads of every namespace in the cohort's
  ClusterQueues, not only governed ones, so a peer controller's ask is not
  borrowed twice. Still, run one controller per cohort, or set
  `countBorrowable: false` on all but one. Two quota entries over one pool
  where only one sets `borrowNamespaces` log `borrow-namespaces-split`.

### B5. A borrowed replica must fit on nodes

Kueue admits on quota, not placement. A borrowed replica is spent only if its
pods fit the A5 node view on its flavor's schedulable nodes, a 2x8 group on
two nodes. With a flavor ambiguous (B1 `flavor-unpinned`) the flavor's nodes
are unknown, so nothing is borrowed. A variant with unplaced pods gets no new
borrowed growth until they place.

A borrowed unit (a replica, a P/D pair, or a LeaderWorkerSet group) whose pods
stay **unscheduled or Kueue-gated** past the placement timeout (default 5
minutes) is released and the model's borrowing cooldown starts. The clock runs
per pod, or per LeaderWorkerSet group from its newest unplaced pod. **Pulling
an image or loading weights does not count**: a scheduled pod is placed. A
cluster-autoscaler `TriggeredScaleUp` on the pod pauses the clock, for at most
three times the timeout.

### B6. Giving capacity back

**A lender is waiting** when a pending Workload in its ClusterQueue requests a
borrowed resource (the GPU or a covered co-resource) on a flavor its
`nodeSelector` and affinity allow, and fits within the lender's unused
nominal. When the allowed flavors cannot be determined it counts on all of
them. A stuck CPU job or a workload larger than the lent nominal does not
count.

- **The first sighting stops new borrowing** in the cohort, and for the
  cooldown after. The debounce (2 consecutive fresh reads and 60 seconds)
  applies only to releases and to the alert.
- **The scaling manager does not release for a lender.** Every counted lender
  can reclaim, so Kueue's own preemption hands its GPUs back. A P/D partner or
  LeaderWorkerSet group left without its preempted half is released by the
  placement timeout (B5).
- **Backoff.** After Kueue preempts one of a model's borrowed pods, or a
  placement timeout releases one, that model borrows nothing new in that
  cohort for `borrowCooldown` (default 10 minutes). A repeat within the
  backoff doubles it, per model and cohort, up to 4 times (cap 40 minutes); one
  clean cooldown resets it.
- **Stale reads, or the controller down, freeze, never shrink.** Nothing new
  is borrowed and every figure stays where it was.

**Serving under preemption.** Kueue deletes a preempted pod without the
Eviction API, so PodDisruptionBudgets do not apply. The
[Kueue path](../well-lit-paths/kueue-bounded-quotas/) requires a preStop drain
hook, a `WorkloadPriorityClass` for models that must keep replicas, and the
group integration for multi-pod replicas. Raising serving above batch turns
`LowerPriority` batch lenders into lenders that cannot reclaim, so it shrinks
what is borrowable. `waitForPodsReady` with `blockAdmission` is not supported.

### B7. Where borrowed capacity may be spent

Scale-from-zero, the warm pool and rescale use capacity **within nominal**,
never borrowed: a model woken onto borrowed GPUs can be preempted before it
serves. Within nominal is `min(N_Q, guaranteed + cohortFree)`, what Kueue
would admit now without borrowing, and the wake must also fit a node (A5). It
is deliberately not the guaranteed figure alone: under Kueue's default
`Never`, an idle queue's guaranteed figure is 0, and every idle namespace would
stop waking. When a wake does not fit because the queue's nominal is lent to a
peer that need not give it back, the wake is not attempted and reports
`nominal-lent-out`, instead of waking into a pod Kueue keeps gated. Only
growth, through the greedy optimizer, spends borrowed capacity.

## Part C: what on-call sees

"Why did my model drop from 6 to 4 replicas?" has four answers: Kueue
preempted pods, the scaling manager released a borrowed replica that did not
place, a replica did not place within guaranteed, or demand fell.

| Signal | What it adds |
|---|---|
| Log `Kueue borrowing applied where every condition holds and withheld elsewhere; …` | per namespace and type: `guaranteed`, `borrowable`, Kueue's own `borrowed`, the withheld reason. Logged on change. |
| Log `quota entry bounded by external caps` | the Kueue caps and the effective entry, as today |
| Logs `Kueue snapshot stale: borrowing frozen…`, `Kueue controller down: borrowing frozen…` | the two freezes |
| Log `Upgrade note (transitional)` (Part A) | models the physical limiter now holds back, for one release |
| `borrowed` on the scaling-decision log line | the model's borrowed GPUs |
| `wva_quota_limit_gpus{…, tier}` | `tier` is `guaranteed` or `borrowable` |
| `wva_quota_borrow_withheld{…, reason}` | 1 for the active reason (B1 tables) |
| `wva_kueue_borrowed_gpus{cohort, flavor}` | what this controller holds borrowed |
| `wva_kueue_borrow_drift_gpus` | per ClusterQueue, computed borrowed vs Kueue's `flavorsReservation` borrowed |
| `wva_kueue_borrow_cooldown_seconds` | the configured `borrowCooldown` |
| `wva_kueue_controller_version_known` | 0 when the controller's version cannot be read |
| `wva_kueue_never_read` | 1 for a Kueue entry with a static `-1` and no successful Kueue read |
| `wva_kueue_lender_waiting_seconds{cohort, cluster_queue}` | how long a lender has waited |
| `wva_kueue_unplaced_pods` | pods unplaced past the placement timeout |
| `wva_borrow_releases_total`, `wva_kueue_preempted_pods_total` | releases by cause; Kueue's preemptions of managed pods |
| `wva_model_scaling_blocked` reasons | `borrow-released`, `lender-reclaimed`, `borrow-unplaced`, `nominal-lent-out` |
| `wva_constraint_provider_skipped{provider, reason}` (Part A) | a skipped limiter; a never-read Kueue entry shows as `reason="unobserved"` |
| `wva_scale_from_zero_wake_unchecked_total` (Part A) | wakes no limiter could check |
| Event on the scale target | each release, with its cause |
| `wva-controller-status` `limiterBudgets` | per entry, namespace and type: `guaranteed`, `borrowable`, `used`, `reason`, `asOf` |

| Alert | Fires when |
|---|---|
| `WVAConstraintProviderSkipped` (Part A) | a declared limiter is skipped |
| `WVAKueueReaderStale` | the Kueue reader is stale for 5 minutes |
| `WVAKueueNeverRead` | a Kueue entry with a static `-1` has never read Kueue, for 10 minutes |
| `WVAKueueVersionUnknown` | `wva_kueue_controller_version_known` is 0 |
| `WVAKueueBorrowDrift` | drift above 0 for 15 minutes (warning) |
| `WVAKueueLenderWaitingPastCooldown` | a lender waits longer than `wva_kueue_borrow_cooldown_seconds` while this controller borrows in its cohort |
| `WVAKueuePodsUnplaced` | managed pods unplaced past the placement timeout |
| `WVAKueueBorrowThrashing` | more than 3 releases plus preemptions per model per hour |

**Never-read is not fail-closed.** It is reported like any unobserved
provider (A4): the other limiters still bound, and Kueue still gates pods past
its quota, so the cost is churn of gated pods, not GPUs over-allocated. The
alert says so and how to bound the type meanwhile.

A ResourceQuota `FailedCreate` is not a shipped alert; the guide shows how to
find it in the namespace's events. PR-B also ships a task guide (prerequisites,
enable, verify, pilot with `borrowNamespaces`, read the reasons, roll back), a
troubleshooting table from every reason to what to do, and an "ACTION
REQUIRED" upgrade box. Rolling back with `countBorrowable: false` evicts
nothing: a lowered cap only stops growth, and models return to nominal as they
scale down.

## Shipping

- **PR-A (#164)** carries Part A: the family tree, intersection merge, P/D
  pair commit, every declared limiter with skipping, the warm pool's bounds,
  the transitional log, node fit, and the installer list. No Kueue code. It
  enforces from its release.
- **PR-B** is based on PR-A and carries Part B and Part C, one commit per step:
  read-only figures, counting with node fit, giving back, observability, e2e.
  The read-only figure is logged from PR-B's first step, but there is **no
  separate log-only release**: default-on counting is gated by the live Kueue
  e2e passing in CI.
- **The live e2e runs in its own CI job, `e2e-tests-kueue-borrow`**, with its
  own timeout budget. It installs Kueue from `registry.k8s.io`, runs a
  two-queue cohort, asserts the drift-gauge condition, and scales the Kueue
  controller to 0 for 60 s to show no growth past guaranteed.
  `REQUIRE_KUEUE_E2E=true` turns its runtime skips into failures.
- **PR-C** (`feat/kueue-borrow-release`), opened after PR-B, adds one opt-in
  feature: with `releaseToNonReclaimingLenders: true`, capacity of lenders that
  cannot reclaim is also counted, and handed back when they wait. A waiting
  lender gets `ceil(pending / unit)` units at once, bounded by the smaller of
  its pending GPUs and what it lent, never below guaranteed or `minReplicas`,
  lowest-priority model first, tracked in a versioned release ledger. A unit
  larger than the need is still released; for whole-node models the
  alternative is releasing nothing. The release lands after KEDA's scale-down
  window, and the lender-waiting alert scales with that expected latency.
  It makes the scaling manager Kueue's reclaim agent, which is why it is off by
  default.

## Decided

- **Borrowing is counted by default** (owner's decision), gated by every B1
  condition; `countBorrowable: false` restores exactly today's cap.
- **By default only lenders Kueue would reclaim for are counted.** Lenders
  that cannot reclaim are PR-C, opt-in.
- **`LowerPriority` lenders reclaim when they outrank at least one Workload of
  the borrowing namespaces**, as Kueue decides.
- **No implicit `gpu-inventory`.** The operator declares the GPU bound.
- **An unknown Kueue version withholds**, unless `assumeTestedVersion: true`.
- **A never-read Kueue entry is not fail-closed** (A4); it alerts.
- **No optimizer tiers.** Kueue picks which pod to preempt and a ReplicaSet
  which pod to delete; the lever for everything else is
  `WorkloadPriorityClass`.
- **Within nominal, not borrowed,** for scale-from-zero, the warm pool,
  rescale and new share transfers.

## Not covered

Each shows its reason, and borrowable is 0:

- **Nested cohorts**, including a root named as a parent by another Cohort.
- **Kueue Fair Sharing** and **`effectiveQuotas`**.
- **Topology-Aware Scheduling, admission checks and MultiKueue.**
- **`borrowWithinCohort` preemption** on any queue in the cohort.
- **Lenders that cannot reclaim**, until PR-C.
- **Borrowing under the utilization-share optimizer** (held, not grown).

Not detected, and documented as unsupported: `waitForPodsReady` with
`blockAdmission`.

## Open questions

- **Scale-from-zero into borrowed capacity.** A model whose within-nominal
  capacity is fully used cannot wake, even with idle lenders. A per-model
  `allowBorrowed` would need node fit too.
- **Timeouts.** The cooldown (10 minutes) and placement timeout (5 minutes)
  are guesses; the e2e measures a reclaim cycle and sets them.

## Review

Five review rounds approved revision 5; a nine-person review of the
implementation led to revision 6 (the split into PR-A and PR-B, release opt-in,
no implicit `gpu-inventory`, the pilot). The record is in the
[PR #161](https://github.com/ev-shindin/llm-scaling-manager/pull/161) and
[PR #162](https://github.com/ev-shindin/llm-scaling-manager/pull/162) comments.

**Revision 7**, after a six-person conceptual review: node fit and the warm
pool's node headroom move into Part A; the family-key fallback, family-named
workloads and the unnamed-accelerator deny are stated; only lenders Kueue
would reclaim for are counted, and the rest moves to PR-C; co-resources,
tenant installs, the controller Lease, `kueue-version-unknown`,
`borrow-within-cohort`, `flavor-unpinned`, per-flavor lender signal, P/D whole
units, wake within guaranteed plus the free pool, backoff and several
controllers per cohort are added; B1 lists every reason; Part C lists every
metric and alert; never-read is closed as not fail-closed.
