# Kueue borrowing, and limiters that compose

- **Problem:** a Kueue-bounded namespace never grows into capacity its cohort
  lends ([#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136)),
  and two declared limiters do not bound a decision together.
- **Decision:** first make every declared limiter bound every decision
  (Part A), then count borrowable cohort capacity where that is safe (Part B).
- **Shape:** Part A ships as PR-A (`feat/limiters-compose`), Part B as PR-B
  (`feat/kueue-borrowing`), reviewed in that order.
- **Opt-out:** `countBorrowable: false` on the quota entry gives exactly
  today's Kueue cap. Part A still applies.
- **Status:** proposed, revision 6 (rescoped after implementation review).

When any condition fails, the namespace behaves exactly as today; the reason is
published on `wva_quota_borrow_withheld`.

This also covers the in-repo part of upstream
[llm-d WVA #1003](https://github.com/llm-d/llm-d-workload-variant-autoscaler/issues/1003)
(bound by `min(physical, quota)`). How it is built is in the developer page
[Kueue borrowing internals](../developer-guide/kueue-borrowing.md).

## What changes for you

1. **A policy that declares both `gpu-inventory` and `quota` is bounded by
   both.** Today the quota wins and the physical limiter is silently dropped.
2. **Limiter entries that list different GPU types stop allowing a type that
   one entry denies.** Today they allow it. Bug fix.
3. **P/D pairs stop ordering both roles past a budget.** Today a budget of 8
   with two 8-GPU roles orders 16 GPUs. Bug fix.
4. **A Kueue-bounded quota counts capacity it can borrow from its cohort**, on
   by default, only where every [B1](#b1-when-borrowing-is-counted) condition
   holds. Opt out with `countBorrowable: false`, or
   `WVA_QUOTA_KUEUE_BORROW=false` at install.
5. **Borrowing needs a GPU bound you declare.** Add a `gpu-inventory` entry or
   a GPU `ResourceQuota`. The installer does not add one for you. Without it,
   borrowing is withheld with reason `no-gpu-bound`.
6. **The scaling manager scales a borrowed replica down by itself only when it
   cannot be placed** (B5). Handing capacity back to a lender that cannot
   reclaim is opt-in: `releaseToNonReclaimingLenders: true` (B6).
7. **New RBAC**, all read-only:
   - `get`/`list` on `cohorts.kueue.x-k8s.io`;
   - `get`/`list`/`watch` on `workloads.kueue.x-k8s.io` (read by an informer);
   - `get`/`list` on `resourcequotas` in the managed namespaces;
   - `get`/`list` on `events`, for the cluster autoscaler's `TriggeredScaleUp`;
   - `get` on the owners of GPU pods (`batch` Jobs; the apps kinds and
     LeaderWorkerSets are already granted).

   A namespace-scoped install needs an admin to re-run `make setup-prereqs`.
8. **`WVA_LIMITER` accepts a list** of limiter types.

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
| Kueue borrowing | **no** | not read (#136) | read |

*Measured* rows were run as throwaway tests against `main` (39b9d0c8). The
union leak installs today: a cluster quota `{H100: 8}` plus a namespace entry
`x: {A100: 4}` lets `x` run A100s the cluster quota denies.

## Part A: limiters compose (PR-A)

**The rule.** A decision must fit **every** declared limiter. A GPU type is
allowed only if every limiter with a view on it allows it, and the budget is
the smallest available figure among them.

- **A1. Merge by intersection.** A type that a *closed* limiter omits becomes
  an explicit 0. Physical inventory and a cluster-scope quota are closed; a
  namespace-scope quota is closed per namespace. Types match by accelerator
  identity, not exact string, in the merge and in every consumer (greedy
  optimizer, its debit, rescale, share planner, wake check). A workload with
  no named accelerator is checked against namespace caps bounded by the
  cluster caps.
- **A2. P/D pair commit.** The summed cost of all picked roles is checked
  against the budget before committing, and the roles scale down together.
  A role at its own `maxReplicas` still drops out of the pair.
- **A3. Build every declared limiter.** The physical limiter and every quota
  entry are built and grouped. At most one physical entry. The installer
  takes a list in `WVA_LIMITER`, and refuses a physical entry without the node
  read access the admin phase grants.
- **A4. One missing input never lifts the others.** A provider that errors or
  is not yet observed is skipped and logged by name. Only when every provider
  is unavailable does a decision go unchecked; scale-from-zero then uses its
  last snapshot if it is under 5 minutes old.

**Upgrade note.** A policy that declares both kinds is now also bounded by
physical GPUs, which count every GPU-holding pod, training included, so it can
be tighter than the quota. A tighter bound only stops growth; it never shrinks
a running model. `make disable-physical-limiter` exits non-zero when it removed
nothing while the quota entry stays.

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
      # countBorrowable: false              # default true
      # borrowNamespaces: [team-a]          # pilot; empty = every governed namespace
      # releaseToNonReclaimingLenders: true # default false (B6)
    namespaceQuotas:
      default: { H100: -1 }
```

A namespace's borrowable figure is 0, with the reason shown, unless every
condition holds:

| Condition | Reason when it fails |
|---|---|
| The namespace is in `borrowNamespaces` (when set) | `not-selected` |
| Kueue manages every GPU pod in the namespace | `not-kueue-managed` |
| A GPU bound is in force | `no-gpu-bound` |
| The Kueue snapshot is fresh and status has been written | `stale`, `no-status` |
| The Kueue controller runs a supported version | `kueue-version-untested` |
| The cohort is flat | `hierarchical-cohort`, `hierarchy-unknown` |
| No queue in the cohort uses `status.effectiveQuotas` | `effective-quotas` |
| Kueue Fair Sharing is off | `fair-sharing` |
| The flavor has no `topologyName` (Topology-Aware Scheduling) | `topology-aware` |
| The queue has no admission checks (including MultiKueue) | `admission-checks` |
| Its ResourceQuota and Cohort objects are readable | `resourcequota-unreadable`, `hierarchy-unknown` |

- **Managed.** A GPU pod counts as managed only when a Workload with quota
  reserved backs it, requests its GPUs, and is owned by a kind a Kueue
  integration creates Workloads for (Pod, Job, JobSet, LeaderWorkerSet,
  StatefulSet). Labels, a queue name or a scheduling gate are not proof: they
  record what was asked, not what Kueue did. Any unmanaged GPU pod in the
  namespace hides GPUs from Kueue's figures, so the cohort looks idle while it
  is not.
- **GPU bound.** Borrowed GPUs belong to another tenant, so something must
  check they exist: the physical limiter, or a GPU `ResourceQuota` whose
  `hard` is at most nominal plus `borrowingLimit`.
- **Fresh.** A read is stale when it failed or is older than twice
  `refreshInterval`. A Forbidden read is never treated as "absent" or "flat".
- **Supported Kueue.** v0.19 and v0.20. Any other controller image version
  withholds borrowing until it is tested and added.
- The rest are cases the B3 formula does not cover yet ([Not covered](#not-covered)).

### B2. What is read

ClusterQueues, Cohorts, ResourceFlavors, Workloads and the namespace's GPU
ResourceQuotas, read-only. The field list is in the
[developer page](../developer-guide/kueue-borrowing.md#what-is-read).

### B3. How much a queue can reach

**In words.** Each queue keeps part of its nominal quota and lends the rest.
The cohort's free pool is everything lent, minus what members use beyond the
part they keep. A queue reaches its own usage, or its kept part if larger,
plus the free pool, and never past nominal plus its borrowing limit. Kueue's
[documented example](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/)
holds: team-a (`nominal 9`, `borrowingLimit 1`) with an idle team-b
(`nominal 12`) reaches 10.

Per flavor and GPU resource, for each cohort member `m`:

```
N_m = nominalQuota      L_m = min(lendingLimit, N_m)   (unset: N_m)
B_m = borrowingLimit    U_m = reserved GPUs             G_m = N_m - L_m

cohortFree = max(0, Σ L_m − Σ max(0, U_m − G_m))
reach_Q    = min(N_Q + B_Q, max(U_Q, G_Q) + cohortFree)
```

A queue that can reclaim keeps `N_Q` as its guaranteed figure, as today; its
growth up to `N_Q` still debits `cohortFree` so no peer borrows the same GPUs.
A queue that cannot reclaim (`Never`, or `LowerPriority` without outranking
the borrower) has guaranteed `min(N_Q, max(U_Q, G_Q))`, reason
`nominal-lent-out`, and grows only as far as the pool has room. The static
entry and the namespace ResourceQuota cap the reach further.

### B4. One shape for borrowing

| Figure | Scope | What it is |
|---|---|---|
| guaranteed pool | namespace, type | the namespace's limit, as today, at the guaranteed figure |
| borrow cap | namespace, type | how far past guaranteed the namespace may go |
| shared pool | cohort, type, one chunk per flavor | the cohort's free GPUs, spent once across its namespaces |

- **The greedy optimizer** spends the guaranteed pool, then draws from the
  shared pool up to the borrow cap, one replica at a time; a P/D pair is one
  unit. Every draw must also fit the closed cluster bounds and a node (B5).
- **Rescale, the warm pool and scale-from-zero** use capacity within nominal
  only (B7).
- **The utilization-share optimizer** keeps the borrowed GPUs a group holds
  now, as no debt, and starts no new borrowing.
- Two namespace-scoped controllers in one cohort can both claim the pool; the
  ResourceQuota bounds the damage.

### B5. A borrowed replica must fit on nodes

Kueue admits on quota, not placement. Ten free GPUs on two nodes cannot hold
one 8-GPU replica. With the physical limiter present, a borrowed replica is
spent only if its pods fit first-fit on its flavor's schedulable nodes, a 2x8
group on two nodes, against one node view per cycle that every replica
ordered that cycle debits. A variant with unplaced pods gets no new borrowed
growth until they place. A borrowed unit (a replica, a P/D pair at its ratio,
or a LeaderWorkerSet group) whose pods stay **unscheduled or Kueue-gated**
past the placement timeout (default 5 minutes) is released and the model's
borrowing cooldown starts. The clock runs per pod, or per LeaderWorkerSet
group from its newest unplaced pod. **Pulling an image or loading weights does
not count**: a scheduled pod is placed. A cluster-autoscaler `TriggeredScaleUp`
on the pod pauses the clock, for at most three times the timeout.

### B6. Giving capacity back

**A lender is waiting** when a pending Workload in its ClusterQueue requests
the borrowed GPU resource for that flavor and fits within the lender's unused
nominal, for 2 consecutive fresh reads and at least 60 seconds. A stuck CPU
job or a workload larger than the lent nominal does not count. While a lender
waits, and for the cooldown (10 minutes) after, the cohort starts no new
borrowing.

**By default the scaling manager does not release for a lender.** A lender
that reclaims (`reclaimWithinCohort: Any`, or `LowerPriority` when its pending
Workload outranks the borrower's) gets its GPUs back through Kueue's own
preemption. A lender that cannot reclaim waits until the borrower's demand
falls. After Kueue preempts one of a model's borrowed pods, that model borrows
nothing new for the cooldown. A P/D partner or LeaderWorkerSet group left
without its preempted half is released by the placement timeout (B5).

**Opt-in: `releaseToNonReclaimingLenders: true`.** The steady-state engine
then releases borrowed units for a waiting non-reclaiming lender, one unit per
cycle and one unit in flight per lender, bounded by the pending GPU request
rounded up to a whole unit, never below guaranteed or `minReplicas`,
lowest-priority model first. A unit larger than the need is still released:
for whole-node models the alternative is releasing nothing. The release
overrides the analyzer's demand floor, is logged and evented with the
lender's name, and lands after KEDA's scale-down window (up to 300 s). This
makes the scaling manager Kueue's reclaim agent, which is why it is off.

**Stale reads freeze, never shrink.** A stale snapshot stops new borrowing and
keeps every figure, including the release ledger, where it was.

**Serving under preemption.** Kueue deletes a preempted pod without the
Eviction API, so PodDisruptionBudgets do not apply. The
[Kueue path](../well-lit-paths/kueue-bounded-quotas/) requires a preStop drain
hook, a `WorkloadPriorityClass` above batch for models that must keep
replicas, the group integration for multi-pod replicas, and no
`borrowWithinCohort` preemption on serving queues.

### B7. Where borrowed capacity may be spent

Scale-from-zero, the warm pool and rescale use capacity **within nominal**,
never borrowed: a model woken onto borrowed GPUs can be preempted before it
serves. Within nominal is `N_Q` for a queue that can reclaim, and
`min(N_Q, max(U_Q, G_Q) + cohortFree)` for one that cannot, which is what
Kueue would admit now without borrowing. It is deliberately not the guaranteed
figure: under Kueue's default `Never`, an idle queue's guaranteed figure is 0,
and every idle namespace would stop waking. Only growth, through the greedy
optimizer, spends borrowed capacity.

## Part C: what on-call sees

"Why did my model drop from 6 to 4 replicas?" has four answers: Kueue
preempted pods, the scaling manager released borrowed replicas, a replica did
not place, or demand fell.

| Signal | What it adds |
|---|---|
| Log `quota entry bounded by external caps` | `guaranteed`, `borrowable`, Kueue's own `borrowed`, the withheld reason |
| `wva_quota_limit_gpus{…, tier}` | `tier` is `guaranteed` or `borrowable` |
| `wva_quota_borrow_withheld{…, reason}` | 1 for the active reason |
| conformance gauge | computed borrowed vs Kueue's `flavorsReservation` borrowed |
| `wva_model_scaling_blocked` reasons | `borrow-released`, `lender-reclaimed`, `borrow-unplaced`, `nominal-lent-out` |
| Event on the scale target | each release, naming the lender ClusterQueue |
| `wva_borrow_releases_total`, `wva_kueue_preempted_pods_total` | releases by cause; Kueue's preemptions of managed pods |
| `wva-controller-status` `limiterBudgets` | per entry, namespace and type: `guaranteed`, `borrowable`, `used`, `reason`, `asOf` |

Alerts: the Kueue reader stale for over 5 minutes; **a Kueue-enabled entry
with a static `-1` that has never had a successful Kueue read** (it is
unbounded); borrowing above 0 while a lender waits past the cooldown; managed
pods unplaced past the placement timeout; ResourceQuota `FailedCreate`; and
thrash, more than 3 releases plus preemptions per model per hour. The Kueue
path ships a "replicas dropped" decision tree over the four causes, a command
next to each prerequisite, an "is borrowing working" query, and a rollback
step.

## Shipping

- **PR-A** carries Part A: A1, A2, A3 with A4, one commit each. No Kueue code.
  It enforces from its release; there is no side-by-side log release.
- **PR-B** is based on PR-A and carries Part B and Part C, one commit per step:
  read-only figures, counting with node fit, giving back, observability, e2e.
  The read-only figure is logged from PR-B's first step, but there is **no
  separate log-only release**: default-on counting is gated by PR-B's live
  Kueue e2e passing in CI.
- **The e2e runs in its own CI job** with its own timeout budget. It installs
  Kueue from `registry.k8s.io`, runs a two-queue cohort, asserts the
  conformance figure, and scales the Kueue controller to 0 for 60 s to show no
  growth past guaranteed. `REQUIRE_KUEUE_E2E=true` turns its runtime skips
  into failures.

## Decided

- **Borrowing is counted by default** (owner's decision), gated by every B1
  condition; `countBorrowable: false` restores exactly today's cap.
- **Release to non-reclaiming lenders is opt-in**, off by default (rescope
  after implementation review). Kueue's preemption is the default hand-back.
- **`LowerPriority` lenders are treated as `Never`**, unless the lender's
  pending Workload outranks the borrower's; then Kueue preempts.
- **No implicit `gpu-inventory`.** The operator declares the GPU bound.
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
- **Borrowing under the utilization-share optimizer** (held, not grown).
- **Other resources in the flavor.** Borrowed GPUs with exhausted CPU leave
  pods gated; the placement hold and timeout handle it.

## Open questions

- **Scale-from-zero into borrowed capacity.** A model whose within-nominal
  capacity is fully used cannot wake, even with idle lenders. A per-model
  `allowBorrowed` would need node fit too.
- **Timeouts.** The cooldown (10 minutes) and placement timeout (5 minutes)
  are guesses; the e2e measures a reclaim cycle and sets them.
- **Never-read entries.** Should a Kueue-enabled `-1` entry with no successful
  read fail closed instead of only alerting?

## Review

Five review rounds approved revision 5; a nine-person review of the
implementation led to this rescope. The record is in the
[PR #161](https://github.com/ev-shindin/llm-scaling-manager/pull/161) and
[PR #162](https://github.com/ev-shindin/llm-scaling-manager/pull/162) comments.
