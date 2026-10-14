# Kueue borrowing, and limiters that compose

**Status:** proposed, revision 2. Nothing here is built. Revision 2 folds in
a five-person review (see [Review record](#review-record)). Fixes
[#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136), and
the in-repo part of upstream
[llm-d WVA #1003](https://github.com/llm-d/llm-d-workload-variant-autoscaler/issues/1003)
(bounding by `min(physical, quota)`).

## What changes for you

1. **A policy that declares both `gpu-inventory` and `quota` will be bounded by
   both.** Today the quota wins and the physical limiter is silently dropped.
2. **Policies whose limiter entries list different GPU types will stop allowing
   a type that one entry denies.** Today they allow it. This is a bug fix.
3. **A Kueue-bounded quota will count capacity it can borrow from its cohort.**
   This is **on by default** from the release that also ships the live-Kueue
   end-to-end test. Set `countBorrowable: false` on the entry, or
   `WVA_QUOTA_KUEUE_BORROW=false` at install, to keep today's behaviour.
   Borrowing applies only where it is safe; see
   [When borrowing is counted](#b1-when-borrowing-is-counted).

## The problem

Today the Kueue quota source caps a namespace at its ClusterQueue's
`nominalQuota`. When another queue in the same cohort is idle, Kueue would
admit more replicas, but the scaling manager never asks for them (#136).

Counting borrowed capacity raises a quota above the guaranteed figure. That is
safe only if something still bounds the decision by real GPUs. Nothing does
today, because limiters do not compose. So this proposal fixes composition
first (Part A), then adds borrowing (Part B).

## Can all limiters work together today?

No. Rows marked *measured* were run as throwaway tests against `main`
(39b9d0c8), calling `FitsGPUBudget`, `mergeConstraints` and `bindingProvider`.
The rest were read from the code.

| Combination | Works? | What happens | Evidence |
|---|---|---|---|
| `gpu-inventory` alone | yes | | read |
| one `quota` entry | yes | | read |
| `quota` with `kueue.enabled` | yes | the smaller of static and Kueue, inside the entry | read |
| `gpu-inventory` + `quota` | **no** | Quota wins; the physical limiter is never built. Only warnings say so. | read |
| two limiter entries that list **different** types | **no** | The allowed types are the **union**. | measured |
| entries that spell one GPU differently (`H100` vs `NVIDIA-H100-80GB-HBM3`) | **no** | The merge treats them as two types. | measured |
| Kueue borrowing | **no** | Not read (#136). | read |

### Bugs that exist today

**The union leak (measured).** A quota denies every type it does not list. The
merge takes the smallest budget per type, but only over the providers that list
that type. So a type one provider omits passes if any other provider lists it:

| Constraints | Demand | Allowed? |
|---|---|---|
| cluster quota `{H100: 8}` alone | A100 = 2 | no (correct) |
| that quota + a provider listing `{H100: 16, A100: 4}` | A100 = 2 | **yes** |
| namespace quota `x: {H100: 4}` alone | A100 = 2 in `x` | no (correct) |
| that + a second namespace entry `x: {A100: 4}` | A100 = 2 in `x` | **yes** |
| cluster quota `{H100: 8}` + namespace entry `x: {A100: 4}` | A100 = 2 in `x` | **yes** |

The last row is a config that installs today.

Limit attribution, the code that names which limiter capped a decision,
disagrees with this. For namespace caps it treats an unlisted type as a deny,
but it skips an unlisted cluster-scope type. Measured: in row 2 it names the
second provider, not the quota.

**P/D pairs overspend a shared budget (read).** Prefill and decode are each
picked against the same budget, and the budget is reduced only after both are
committed. Every picked role commits at least one replica. A budget of 8 with
two 8-GPU roles orders 16 GPUs. Kueue or the scheduler admits one role and the
other waits. If prefill is the one admitted, the fleet gains nothing. This is
rare today and becomes common with borrowing, where budgets come in small
pieces.

### Gaps that appear once physical and quota compose

Found by the maintainer review. Each was measured or read at the line it cites
in [Details](#details-code-touch-points).

- **A dropped type comes back through the namespace path (measured).** If the
  merge only removes a denied type, the namespace check reads the missing key
  as "no cluster bound", and allows it again.
- **Different spellings for one GPU (measured).** Physical keys are short names
  (`H100`). Quota keys are whatever the operator wrote. Under an intersection
  merge, two spellings of one GPU would deny each other.
- **Workloads with no named accelerator skip cluster bounds (measured).** In a
  namespace with its own cap, such a workload is checked against the namespace
  cap only, never against physical free GPUs.
- **One unavailable view lifts every bound (read).** If any provider's usage
  view has not been observed yet, both engines drop *all* constraints. One
  provider's error has the same effect in scale-from-zero. Steady state already
  skips a failing provider; nothing skips an unobserved one.
- **The warm pool ignores cluster-scope bounds (read).** It reads namespace
  caps only, so with a physical limiter it reads "unbounded" and grows.
- **About ten places read the single limiter mode (read).** These include the
  readiness gate, optimizer selection and the change detector that rebuilds the
  limiter. They are listed in Details.

## Part A: limiters compose

**The rule.** A decision must fit **every** declared limiter. A GPU type is
allowed only if every limiter that has a view on it allows it. The budget is
the smallest available figure among them.

A limiter is **closed** when a type it does not list is denied. Physical
inventory is closed: an undiscovered type has no GPUs. A cluster-scope quota is
closed. A namespace-scope quota is closed **per namespace**. Its cluster-wide
figure is only the sum of its namespace caps, so it is not a cluster bound.

### A1. Merge by intersection

- Every type that a closed limiter omits becomes an **explicit 0**, never a
  missing key. Missing keys keep meaning "no bound".
- Types are matched by accelerator identity, not by exact string, using the
  same rule as the Kueue merge. Short names match long names. Two different long
  names never match. The quota's spelling is kept.
- The set of types considered includes namespace cap keys, not only
  cluster-scope keys.
- The namespace-scope quota's summed figure does not take part in the
  cluster-wide minimum. The greedy optimizer's stop condition becomes
  namespace-aware, so it does not stop the whole fleet when that figure is
  empty.
- A workload with no named accelerator is checked against the effective map,
  meaning namespace caps already bounded by cluster caps.
- Limit attribution uses the same rule, so it names the limiter that actually
  bound the decision.

This is a bug fix and ships first. Its tests are the five union-leak rows, the
spelling case, the namespace re-entry case and the unnamed-accelerator case.
Each test must fail on the parent commit.

### A2. Fix the P/D pair commit

Check the summed cost of all picked roles against the budget before
committing, and scale them down together. Keep the existing rule that a role at
its own `maxReplicas` is dropped from the pair; that fixed a measured
starvation. Test: a budget of 8 with two 8-GPU roles orders 0, not 8+8.

### A3. Build every declared limiter

- The factory builds the physical limiter, when declared, and every quota
  entry, grouped together. Each provider already receives its own usage
  figure: a quota counts only the scaling manager's pods, and the physical
  limiter counts every GPU on the nodes.
- The single limiter mode becomes a set. Every reader of the mode is updated
  (list in Details), including the readiness gate, optimizer selection and the
  limiter change detector.
- Validation allows at most one physical entry, under either spelling
  (`gpu-inventory` or `inventory`).
- The installer accepts both. `WVA_LIMITER` takes a list, and the policy
  helpers stop replacing the whole list. A physical entry needs node read
  access, which only the admin setup phase grants. Without it, the installer
  refuses with that remedy instead of installing a controller that goes
  NotReady.

### A4. One missing input never lifts the others

- A provider that errors, or whose usage view is not yet observed, is skipped
  and logged by name. The other providers still bound the decision. This
  applies to both engines.
- Only when **every** provider is unavailable does a decision go unchecked.
  Scale-from-zero then checks against the last snapshot it has, never against
  nothing.
- The warm pool's headroom takes the same merged figures as the optimizer,
  cluster-scope bounds included.

**Upgrade note for Part A.**

- A policy that declares both kinds is now bounded by physical GPUs as well.
  This can lower allocations.
- A tighter bound only stops growth. It does not shrink running replicas.
- The physical view counts every GPU-holding pod, training included, so on a
  busy night it can be tighter than the quota.
- For one release before enforcing, the controller logs both the old and the
  new allocation for such policies.
- The physical limiter's cost is a walk of the pods on GPU nodes every
  interval. That goes in the release notes too.

## Part B: count Kueue borrowing

### B1. When borrowing is counted

Configuration, per quota entry:

```yaml
limiters:
  - name: gpu
    type: gpu-inventory
  - name: tenant-quota
    type: quota
    scope: namespace
    kueue:
      enabled: true
      # countBorrowable: false   # default true; false = nominal quota only
    namespaceQuotas:
      default: { H100: -1 }
```

On by default means "counted where safe". A namespace's borrowable figure is 0,
with a reason the operator can see, unless **all** of these hold:

| Condition | Reason when it fails |
|---|---|
| The model's pod template carries `kueue.x-k8s.io/queue-name`, so Kueue admits the pods. Otherwise Kueue's usage misses every serving GPU and the cohort looks idle while it is not. | `not-kueue-managed` |
| A placement bound is in force: the physical limiter, or a GPU `ResourceQuota` in the namespace. Borrowed GPUs belong to another tenant; something must check that they exist. | `no-placement-bound` |
| The Kueue snapshot is fresh, and ClusterQueue status has been written. | `stale`, `no-status` |
| The cohort's shape is known (B2). | `hierarchy-unknown`, `hierarchical-cohort` |
| No ClusterQueue in the cohort uses `status.effectiveQuotas`. Kueue then schedules from it, not from `spec`. | `effective-quotas` |
| Fair Sharing is not enabled on the cohort (see B8). | `fair-sharing` |

A namespace `ResourceQuota` also caps the ceiling. Borrowing past the
ResourceQuota's `hard` limit would ask for pods that the API server rejects.

On a cluster-scoped install, `make deploy-wva` with `WVA_QUOTA_KUEUE=true` adds
a `gpu-inventory` entry unless `WVA_LIMITER_PHYSICAL=false` is passed. It prints
the resulting limiter set. A namespace-scoped install cannot list nodes, so it
relies on the namespace ResourceQuota.

### B2. What is read

| Field | Used for |
|---|---|
| ClusterQueue `spec.cohortName` (`spec.cohort` in the deprecated v1beta1) | cohort membership |
| `…resources[].borrowingLimit`, `…lendingLimit` | how far a queue may borrow, and how much it lends |
| `status.flavorsReservation[].resources[].total` | GPUs reserved per queue. Kueue charges quota at reservation; `flavorsUsage` lags it when admission checks are configured. |
| `status.flavorsReservation[].resources[].borrowed` | a cross-check, logged next to the computed figure |
| `status.pendingWorkloads` | input to the lender rule (B6) |
| `spec.preemption.reclaimWithinCohort` | whether a queue can take its nominal back (B3, B6) |
| `status.fairSharing`, `status.effectiveQuotas` | the conditions in B1 |
| `Cohort` objects: `spec.parentName`, `spec.resourceGroups` | the cohort's shape |

The reader keeps resolving the version through the RESTMapper, which prefers
the served storage version (v1beta2).

**New RBAC:** `get`/`list` on `cohorts.kueue.x-k8s.io`. Without it, a flat
cohort and a nested one cannot be told apart, so a Forbidden read sets
`hierarchy-unknown`. A cohort name with no `Cohort` object is a flat cohort,
which is the common case. Reading the namespace's own `ResourceQuota` objects
also needs `get`/`list` on `resourcequotas` in the managed namespaces.

### B3. The ceiling, per ClusterQueue, flavor and GPU resource

**In words.** Each queue keeps part of its nominal quota for itself and lends
the rest. The cohort's free pool is everything lent, minus what members use
beyond the part they keep. A queue can reach its own usage, or its kept part if
that is larger, plus the free pool. It never goes past nominal plus its
borrowing limit. This is the same arithmetic as Kueue's own check. The Kueue
reviewer derived it from Kueue's `resource_node.go`.

```
For each member m of the cohort:
  N_m = nominalQuota
  L_m = min(lendingLimit, N_m)        (unset: N_m)
  B_m = borrowingLimit                (unset: no limit)
  U_m = reserved GPUs (status)
  G_m = N_m - L_m                     kept for m, never lent

cohortFree = max(0, Σ L_m  -  Σ max(0, U_m - G_m))
reach_Q    = min(N_Q + B_Q,  max(U_Q, G_Q) + cohortFree)
ceiling_Q  = max(N_Q, reach_Q)   if Q's reclaimWithinCohort is not Never
           = reach_Q             otherwise
```

- **The queue's own usage is inside `reach`.** A queue that is borrowing keeps
  its borrowed replicas in its ceiling. A formula built from `cohortFree` alone
  would drop the ceiling as soon as the queue borrowed, force a scale-down, and
  repeat.
- **Nominal is a floor only when the queue can reclaim it.** With Kueue's
  default (`reclaimWithinCohort: Never`), unused nominal that a peer borrowed
  stays with the peer. Today's cap gets this wrong too, and the proposal fixes
  both.
- **Kueue's documented example holds.** In the
  [ClusterQueue concepts page](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/),
  team-a has `nominal 9` and `borrowingLimit 1`, and team-b has `nominal 12`
  and is idle. team-a's reach is 10.

### B4. The free pool is shared, not copied

Every queue in a cohort sees the same `cohortFree`. Handed to each namespace
separately, it would be spent several times over. Two tenants would scale into
the same GPUs, Kueue would admit one, and the other's pods would wait gated.

- The constraints carry the free pool once, per cohort, flavor and type, as a
  **shared pool**. The optimizer debits every borrowed allocation in that
  cohort from it.
- A namespace with LocalQueues on several queues of one cohort sums their
  guaranteed parts and adds the shared pool once.
- GPUs the scaling manager has just asked for, which Kueue has not reserved
  yet, are subtracted from `cohortFree`. Otherwise status lags by one cycle and
  the same GPUs are counted twice.

A namespace-scoped controller sees only its own namespace. Two such
controllers in one cohort can still both claim the pool. The ResourceQuota cap
(B1) limits that, and the docs say so plainly.

### B5. Borrowed GPUs must fit whole replicas

Kueue admits on quota alone unless Topology-Aware Scheduling is on. The physical
limiter is a cluster-wide count. Ten free GPUs spread across nodes cannot hold
one 8-GPU replica. Once Kueue admits such a replica, its reservation sits
inside the queue's own usage and stays in the ceiling for good.

- Borrowable is counted per flavor, rounded down to whole replicas of the
  model, and then summed. A replica is admitted in one flavor and one queue, so
  4 + 4 GPUs in two flavors is not room for one 8-GPU replica.
- **Placement hold.** A model with Pending or Kueue-gated pods gets no new
  borrowed growth until they place. This mirrors the hold the
  utilization-share optimizer already applies to its fills.
- An untyped grant (a flavor with no product label) bounds each named type
  separately today. For borrowing it is split across types instead, so the
  same GPUs cannot be ordered once per type.
- A replica that straddles nominal and borrowed capacity is borrowing as a
  whole in Kueue's eyes. All of its GPUs are preemptible, and reporting says
  so.

### B6. Giving capacity back

Inference Deployments never finish. A lender whose queue cannot reclaim
(`reclaimWithinCohort: Never`) never gets its GPUs back unless the borrower
hands them back.

- **A lender is waiting** when `pendingWorkloads > 0` **and** its own GPU
  reservation is below its nominal (`U_m < N_m`). Without the second check, a
  stuck CPU job, or a borrower's own preempted pod, would pin the whole cohort.
- **If a waiting lender cannot reclaim**, the borrower's ceiling drops to its
  guaranteed part, and the scaling manager reduces the borrowed replicas
  itself. Today the optimizer never shrinks a model whose usage is over its
  limit; it only stops growth. Part B adds an over-limit release that applies
  to borrowed replicas only.
- **If the lender can reclaim**, no new borrowing starts, and Kueue preempts.
- **Cooldown.** After a waiting lender, or after Kueue preempted one of a
  model's borrowed pods, that cohort, and separately that model, borrow nothing
  new for a cooldown. The cooldown defaults to 10 minutes and is configurable.
  Lender batch jobs every 20 minutes would otherwise mean a reclaim storm.
- **Stale reads freeze, never shrink.** A stale snapshot stops new borrowing
  and keeps the ceiling at `max(N_Q, last U_Q)`. An API blip must not cause a
  reclaim the cluster did not ask for.

**What preemption does to a serving pod.** Kueue admits a Deployment pod by
pod. When it preempts one, it deletes the pod. The ReplicaSet recreates it
gated, as a new pending workload in the borrower's queue. Kueue does not honour
PodDisruptionBudgets and does not drain the router, so in-flight requests on
that pod are lost.

A LeaderWorkerSet group is one workload, so preemption deletes the whole
multi-node replica. Prefill and decode are separate workloads, so a pair can
lose one side. The well-lit path therefore requires:

- a preStop drain hook on serving pods;
- a Kueue `WorkloadPriorityClass` above batch for models that must keep their
  replicas;
- the group integration for multi-pod replicas.

### B7. Where borrowed capacity may be spent

- **Scale-from-zero** wakes against the guaranteed part only. A model woken
  onto borrowed capacity can be preempted before it serves its first request.
- **The utilization-share optimizer** plans transfers inside the guaranteed
  part only. A borrowed figure that moves with every lender pod would otherwise
  start transfers and then reverse them. Borrowed capacity reaches models
  through growth and idle-fill, which already respect the placement hold.
- **The greedy optimizer** may spend it, subject to the shared pool (B4) and
  the placement hold (B5).

The guaranteed figure is taken across providers as
`min over providers of (Limit − Borrowable)`. Taking the minimum of each field
separately is wrong. Physical has nothing borrowable, so the separate minimum
would erase the borrowed part and report all of it as guaranteed. Scale-from-zero
and the share planner apply a *guaranteed-only* copy of the constraints. They
do not merge a new field.

### B8. Fair Sharing and nested cohorts

- **Fair Sharing.** Under Fair Sharing, Kueue admits borrowers by share, and
  preemption follows different rules. A borrower competes with other borrowers,
  not only with lenders. Borrowable is 0 with reason `fair-sharing` in this
  proposal. A follow-up can cap it at the queue's fair-share entitlement.
  AdmissionFairSharing orders LocalQueues inside one ClusterQueue and does not
  change the ceiling.
- **Nested cohorts.** A cohort with a parent, or with its own
  `resourceGroups`, is counted as 0 with reason `hierarchical-cohort`. A
  cohort's own quota is extra capacity that the formula would miss. A
  follow-up walks the tree. A parent-only tree that declares no
  `resourceGroups` creates no capacity, so the cluster-scope figure stays
  right.

Borrowing moves capacity between queues and creates none. So the cluster-scope
quota figure, the sum of every queue's nominal quota, does not change.

## Part C: what on-call sees

The question this has to answer is "why did my model drop from 6 to 4
replicas?" There are three causes: Kueue preempted pods, the ceiling dropped,
or demand and KEDA. The signals:

- **Log.** The existing `quota entry bounded by external caps` line adds,
  per namespace and type, `nominal`, `borrowable` and the Kueue `borrowed`
  cross-check. When borrowable is 0, it adds the reason.
- **Metric.** `wva_quota_limit_gpus{limiter_name, exported_namespace,
  accelerator, tier}`, with `tier` as `nominal` or `borrowable`. A companion
  `wva_quota_borrow_withheld{…, reason}` is 1 for the active reason.
- **Scaling-blocked reasons.** `borrow-withdrawn` and `lender-reclaimed` are
  added on `wva_model_scaling_blocked`.
- **Events.** An event on the scale target when its ceiling drops below its
  current allocation, naming the lender ClusterQueue.
- **Gated pods.** A gauge counts managed pods behind Kueue's scheduling gate,
  and a counter counts managed pods Kueue preempted.
- **Status ConfigMap.** `wva-controller-status` gets `limiterBudgets`: per
  entry, namespace and type, `limit`, `borrowable`, `used`, `reason` and
  `asOf`. The publisher reads config today, so the engine publishes the figures
  it used through a snapshot, like the existing decision snapshots.
- **Alerts and runbook.** The well-lit path ships:
  - a "replicas dropped" decision tree over the three causes;
  - alerts for: the reader stale for more than 5 minutes; borrowing above 0
    while a lender has waited more than 10 minutes; managed pods gated more
    than 10 minutes; ResourceQuota `FailedCreate`.

## Decided

- **Borrowing is on by default** (owner's decision). It turns on in the release
  that ships the live-Kueue e2e, not earlier. The release before it ships the
  read-only log, so operators can see the figure first. The conditions in B1
  make default-on safe where Kueue does not actually govern the pods.
- **Not proposed: optimizer tiers.** The issue asks the optimizer to release
  borrowed replicas first. The scaling manager cannot choose which pod Kueue
  preempts. Lowering replicas lets the ReplicaSet pick a pod regardless of
  tier. The lever that works is Kueue's `WorkloadPriorityClass`, which the
  well-lit path documents.
- **Scale-from-zero and the share planner use guaranteed capacity only.**
- **Fair Sharing and nested cohorts count nothing borrowable for now.** Each
  shows its reason.

## Order of work

Each step is one PR. Its new tests must fail on the parent commit.

1. **A1, the merge fix.** Intersection, explicit zeros, accelerator identity,
   the unnamed-accelerator check, and attribution. No config change.
2. **A2, the P/D pair commit.**
3. **A3 and A4, composition.** Includes the mode set and every reader of the
   mode, the installer, the readiness gate, skipping unavailable providers, the
   warm pool, the upgrade log, and docs (list in Details).
4. **Read-only borrowing.** The reader parses cohorts, limits, status and
   Cohort objects (new RBAC), and logs the figure it *would* apply, with its
   reason. Ships one release before step 5.
5. **Borrowing, default on, with the live-Kueue e2e in the same release.** B1
   to B7 and Part C. The e2e installs the Kueue controller from
   `registry.k8s.io`, runs a two-queue cohort, preempts a borrowed replica, and
   asserts that the scaling manager lowers replicas and that no gated pods
   remain. Unit tests cover:
   - the clamps;
   - a lender reclaiming, with `Never` and with `Any`;
   - the shared pool with two borrowers;
   - the whole-replica rounding (5 + 5 GPUs over two nodes for one 8-GPU
     replica);
   - the guaranteed-part merge (quota `Limit 12, Borrowable 4` with physical
     `Limit 10` gives guaranteed 8, not 10);
   - stale-read freeze;
   - v1beta1 against v1beta2.
6. **Follow-ups:** nested cohorts, Fair Sharing, Topology-Aware Scheduling.

## Open questions

- **Scale-from-zero into borrowed capacity.** A model whose nominal is fully in
  use cannot wake, even with idle lenders. A per-model `allowBorrowed` could
  cover it. It would have to check node fit, not only the count.
- **Shared ClusterQueues.** These are still a per-namespace ceiling, not a
  partition. This is out of scope.
- **Cooldown default.** 10 minutes is a guess. It should come from a measured
  reclaim cycle in the e2e.

## Details: code touch points

For implementers. Paths are under `internal/` unless noted.

- **Merge and attribution:** `engines/allocation/cost_aware_optimizer.go`
  (`mergeConstraints`, `mergeNamespaceConstraints`),
  `gpu_limit_attribution.go` (`bindingProvider` skips unlisted cluster types,
  `namespaceHeadroom` reads `NamespacePools` only), `gpu_budget.go`
  (`FitsGPUBudget` unnamed-accelerator branch, `GPUBudgets`),
  `greedy_score_optimizer.go` (`effectiveAvailable`, the `totalGPUs` stop
  condition), `utilization_share_groups.go` (`BuildShareGroups`, `PhysicalFree`).
  `multi_backup/` is `//go:build ignore` and is not touched.
- **P/D pair:** `engines/allocation/analyzer_helpers.go`
  (`allocateForModelPaired`: picks at 504-521, commit at 573-581).
- **Readers of the single mode:** `config/config.go` (`limiterModeOf`,
  `EffectiveLimiterSummary`, `UnenforcedLimiterTypes`),
  `config/saturation_scaling.go` (validation, both aliases),
  `engines/allocation/limiter_factory.go` (`LimiterSignature` encodes only
  quota entries), `usage_basis.go` (`PhysicalUsageConfigured`),
  `cmd/main.go` (readiness `gpu-policy` gate, `LimitedMode` and unenforced
  warnings), `scalingpolicy/inventory.go` (`ShouldCollectClusterInventory`),
  `engines/steadystate/engine.go` (optimizer selection, `MissingBasis`
  fallback, `ReportUnresolvedAccelerator`), `engine_v2.go` (idle-fleet
  headroom), `controller/configmap_reconciler.go`,
  `controller/configmap_helpers.go`, `engines/allocation/utilization_share_shadow.go`
  (`ClusterIsQuota`), `engines/scalefromzero/candidates.go` (drop-all on error;
  stale comment at 185-188).
- **Installer:** `deploy/lib/cli.sh` (`WVA_LIMITER`),
  `deploy/lib/limiter_policy.sh`, `deploy/lib/physical_limiter.sh`;
  fixtures in `hack/check-limiter-declaration.sh` and
  `hack/check-physical-limiter-targets.sh`.
- **Kueue figures:** `kueue/reader.go`; `config/quota_limiter.go`
  (`ExternalCaps` and `BoundBy` gain a borrowable map, so the effective
  borrowable is `max(0, min(static, N+B) − min(static, N))`);
  `engines/allocation/quota_inventory.go` (pools, `Refresh` fingerprint).
  The guaranteed-only copy follows `WithholdPromised` in
  `utilization_share_withhold.go`.
- **Status:** `controllerstatus/publisher.go` reads only config today.
- **Tests that encode single-mode behaviour:** `limiter_factory_test.go`,
  `engine_limiter_reload_test.go`,
  `TestPhysicalUsageConfiguredMatchesTheBuiltLimiter`.
- **Docs to update in step 3:** `docs/reference/quota-limiter.md`,
  `docs/reference/gpu-limiter.md`, `docs/reference/scaling-policy.md`,
  `docs/reference/configuration.md`, `docs/reference/metrics.md`,
  `docs/concepts/gpu-capacity-accounting.md`,
  `docs/guides/admin-gpu-bounding/`, `docs/well-lit-paths/bound-by-gpus/`,
  `config/base/manager/scaling-policy-configmap.yaml`.

## Review record

Revision 1 was reviewed on 2026-10-15 by five reviewers. All approved A1 in
principle. Every finding below is folded into revision 2.

| Reviewer | Verdict | Main findings |
|---|---|---|
| Kueue expert | request changes | formula matches Kueue once clamped; free pool double-counted; reclaim-aware nominal; noisy `pendingWorkloads`; Fair Sharing; `effectiveQuotas`; Cohort RBAC; preemption deletes pods |
| Maintainer | request changes | dropped type re-enters via namespace path, spelling mismatch, unnamed accelerator (all measured); attribution skips cluster types; unobserved view lifts all bounds; about ten readers of the single mode; `Borrowable` merge |
| SRE / cluster admin | request changes | default-on before live e2e; pods not Kueue-managed; stale read shrinks; reclaim storms; ResourceQuota vs borrowing; on-call signals, alerts, runbook |
| Whole-node / P/D | request changes | borrowed GPUs counted not placed; P/D pair overspend; per-flavor rounding; utilization-share churn; multi-pod preemption |
| Docs | approve with changes | behaviour change buried; formula unexplained; internals in the main flow; `Borrowable` merge |
