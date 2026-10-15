# Kueue borrowing internals

How limiter composition and Kueue cohort borrowing are built, for contributors.
The design, and what an operator sees, is the proposal
[Kueue borrowing, and limiters that compose](../proposals/kueue-borrowing-and-limiter-composition.md).
Paths are under `internal/` unless noted. Part A lands in PR-A
(`feat/limiters-compose`), Part B in PR-B (`feat/kueue-borrowing`); items marked
*PR-B rescope* are the review fixes added after the first implementation.

## What is read

| Field | Used for |
|---|---|
| ClusterQueue `spec.cohortName` (`spec.cohort` in v1beta1) | cohort membership |
| `…resources[].borrowingLimit`, `…lendingLimit` | B3 formula |
| `status.flavorsReservation[].resources[].total` | reserved GPUs. Kueue charges quota at reservation; `flavorsUsage` lags it under admission checks. |
| `status.flavorsReservation[].resources[].borrowed` | conformance cross-check |
| `spec.preemption.reclaimWithinCohort` | guaranteed figure, lender rule |
| `spec.admissionChecks`, ResourceFlavor `topologyName` | `admission-checks`, `topology-aware` (*PR-B rescope*) |
| `status.fairSharing`, `status.effectiveQuotas` | B1 conditions |
| Cohort `spec.parentName`, `spec.resourceGroups` | nested cohorts, including any Cohort naming this one as parent |
| ResourceFlavor `nodeLabels`, `tolerations` | node fit per flavor |
| Workloads (informer, field index on owner UID) | managed check, pending lender requests and priority, preemption |
| Namespace `ResourceQuota` GPU `hard`, `used` (cached) | GPU bound and ceiling; with several, the tightest `hard` |
| Kueue controller image version | `kueue-version-untested` (*PR-B rescope*) |

The reader resolves the API version through the RESTMapper, which prefers the
served version with the highest discovery priority (v1beta2 today). A cohort
name with no Cohort object is a flat cohort. The `pkg/` constants copied from
Kueue (condition types, reasons, label keys) live in one file with their source
path and the Kueue version they were copied from.

## Code touch points

- **Merge and attribution** (`engines/allocation/`): `cost_aware_optimizer.go`
  (`mergeConstraints`, `mergeNamespaceConstraints`), `gpu_limit_attribution.go`
  (`bindingProvider`, `namespaceHeadroom`), `gpu_budget.go` (`FitsGPUBudget`,
  `GPUBudgets`, `debitBudget`), `greedy_score_optimizer.go`
  (`effectiveAvailable`, `fairShareRolePick`), `utilization_share_groups.go`
  (`BuildShareGroups`; `PhysicalFree` renamed `ClusterFree`).
  `multi_backup/` is `//go:build ignore` and untouched.
- **Spellings.** Every exact-string accelerator lookup routes through
  `accelerator.FindKey`. Two spellings of one GPU that agree on a budget fold to
  one key, the most specific; case-only spellings count once in `budgetTotal`
  and `debitBudget`. The in-loop debit in `allocateForModelPaired`/`Placed` and
  the grouping in `fitPairToBudget` use `debitBudget` and the covers relation,
  so one model's variants on two spellings do not over-spend.
- **P/D pair and shared-pool draws:** `analyzer_helpers.go`
  `allocateForModelPaired`, a per-replica first-fit instead of
  `gpusAvail / gpusPR`.
- **Readers of the old single limiter mode:** `config/config.go`
  (`limiterModeOf`, `EffectiveLimiterSummary`, `UnenforcedLimiterTypes`),
  `config/saturation_scaling.go` (validation, both aliases),
  `limiter_factory.go` (`LimiterSignature`), `usage_basis.go`
  (`PhysicalUsageConfigured`), `cmd/main.go` (readiness `gpu-policy` gate),
  `scalingpolicy/inventory.go`, `engines/steadystate/engine.go` and
  `engine_v2.go`, `controller/configmap_*.go`,
  `utilization_share_shadow.go` (`ClusterIsQuota`),
  `engines/scalefromzero/candidates.go`.
- **Installer:** `deploy/lib/cli.sh` (`WVA_LIMITER`, `WVA_QUOTA_KUEUE_BORROW`),
  `deploy/lib/limiter_policy.sh`, `deploy/lib/physical_limiter.sh`; fixtures in
  `hack/check-limiter-declaration.sh`, `hack/check-physical-limiter-targets.sh`
  and the RBAC checks. *PR-B rescope:* `WVA_QUOTA_KUEUE=true` no longer adds a
  `gpu-inventory` entry, so `WVA_LIMITER_PHYSICAL` goes away.
- **Kueue figures:** `kueue/reader.go`, `kueue/borrow.go` (the B3 formula),
  `kueue/eligibility.go` (B1); `config/quota_limiter.go` (`ExternalCaps`,
  `BoundBy`); `engines/allocation/quota_inventory.go` (pools, `Refresh`
  fingerprint). `ResourceConstraints` gains the borrow caps, the shared pools
  and a namespace-to-cohort map; the within-nominal copy drops all three. A
  borrowing provider's open per-type aggregate is the reach, not the sum of
  guaranteed figures. `mergeSharedPools` ORs `Stale` and keeps the older
  `ReadAt`. `namespaceBorrowed` subtracts gated GPUs per type (`FindKey`), the
  untyped bucket shared as in `DeductUnreserved`.
- **Borrowing in the engine:** `engines/allocation/borrow_*.go`,
  `node_view.go`; `engines/steadystate/borrowing*.go`, `preemption_watch.go`.
- **Status:** `controllerstatus/publisher.go` publishes `limiterBudgets`
  (withheld and changed entries sorted first) and the release ledger.

## Mechanics

**Managed check.** A GPU pod is managed when a Workload with quota reserved
backs it, requests its GPUs, and is owned by Pod, Job, JobSet,
LeaderWorkerSet or StatefulSet. A namespace with no GPU pods keeps its previous
answer; on the first read it counts as not managed. Eligibility is evaluated
only for namespaces with managed variants, and spread over the interval.

**Reserved but not yet visible.** The engine, not the reader, takes from the
shared pool what the scaling manager asked for that Kueue has not reserved:
managed pods behind the `kueue.x-k8s.io/admission` gate, and borrowed growth
not yet visible as pods. It stays deducted until the pods exist or the desired
count goes back down.

**Node view.** Built only in a cycle that counts borrowing and declares a
physical limiter, from the physical limiter's pod walk (`gpuusage`): free GPUs
per schedulable node, with taints, so a flavor's tolerations match. The clone
per cycle shares the immutable labels and taints, and the eligible-node list is
computed once per cycle. Every replica ordered that cycle debits it,
guaranteed and borrowed, across models; a guaranteed replica that fits no node
debits nothing and is reported unplaced. The greedy optimizer places first; the
share planner's idle fill reads the same view afterwards, so the two cannot
place into the same hole. The view counts GPUs only.

**Placement clocks.** `VariantMetadata.UnplacedAt` is keyed by pod name, or
for a LeaderWorkerSet by group index, a group counting from its newest
unplaced pod. Unplaced means `PodScheduled=False` with reason `Unschedulable`,
or the Kueue gate held longer than a fixed 30 s. The timeout and the share
optimizer's `HoldFill` share one setting when `placementTimeout` is set; unset,
`HoldFill` keeps its derived wait.

**Cluster-autoscaler wait.** A `TriggeredScaleUp` Event pauses the clock only
when its `involvedObject.uid` matches the pod's UID; equal timestamps with a
later `NotTriggerScaleUp` read as not pending. Capped at three times the
timeout.

**Preemption.** Kueue sets the pod condition `TerminationTarget` (reason
`WorkloadEvictedDueToPreempted`) before it deletes the pod. Against Kueue
v0.20 on kind, the pod and its Workload were gone 1.4 s later, inside one
engine cycle, so polling misses it. `PreemptionWatch` records the condition
from the pod informer, including the delete tombstone; the Workload informer
covers the rest. The pod is matched to a model by owner name prefix
(`<deployment>-` or `<lws>-`), longest wins. A buffer overflow withholds new
borrowing for a cycle (`preemptions-unreadable`).

**Lender rule** (*PR-B rescope*). A lender waits when a pending Workload in its
ClusterQueue requests the borrowed GPU resource for the flavor and fits within
its unused nominal, debounced over 2 fresh reads and 60 s. For
`LowerPriority`, the pending Workload's priority is compared with the
borrower's: higher means Kueue preempts, so the lender is treated as
reclaiming.

**Release governor** (only with `releaseToNonReclaimingLenders`). One unit per
cycle; a unit is a replica, a P/D pair at its ratio, or a LeaderWorkerSet
group. Bound: the waiting lenders' pending GPU request on that flavor, rounded
up to a whole unit. Order: lowest-priority model, then most recently grown
(after a restart: priority, then cost). A LeaderWorkerSet scales down its
highest group index; the release checks that it is the newest and logs when
not.

**In flight.** A lowered target frees nothing until KEDA's window passes and
the pods are deleted, so a released unit is in flight until its pods are gone
and a fresh read of the lender's reservation postdates the deletion. One unit
in flight per lender and flavor. Still in flight after twice the grace period
plus KEDA's window, it keeps blocking and surfaces as `release-not-actuated`.

**Ledger.** The `borrowReleaseLedger` key of the `wva-controller-status`
ConfigMap, one entry per lender and flavor, reset when the lender stops
waiting and carried over by each new leader term. With no entry, a waiting
lender is treated as in flight until the next fresh read. *PR-B rescope:* the
ledger has a `version` field, a capped entry count, and values clamped on load;
a ledger with unknown or missing fields loads, and a rollback leaves nothing
harmful.

**Scale.** Workloads come from an informer and ResourceQuotas from a cache; no
lock is held across I/O; Namespace labels come from one LIST per refresh; the
bounded log line is emitted only on change.

## Testing

- Unit tests: `make test`. The envtest suites run under WSL; see
  [Testing](testing.md).
- The live-Kueue e2e (`test/e2e/kueue_borrow_test.go`) runs in its own CI job.
  `deploy/ci-pr-checks/install-kueue.sh` installs Kueue from `registry.k8s.io`
  and refuses a non-kind context unless `KUEUE_ALLOW_NON_KIND=true`.
  `REQUIRE_KUEUE_E2E=true` turns its runtime skips into failures; keep
  `E2E_TIMEOUT` below the job timeout.
- Supported Kueue versions: v0.19 and v0.20. Adding one means re-copying the
  constants and running the e2e against it.
