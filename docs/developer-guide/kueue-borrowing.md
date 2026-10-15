# Kueue borrowing internals

How limiter composition and Kueue cohort borrowing are built, for contributors.
The design, and what an operator sees, is the proposal
[Kueue borrowing, and limiters that compose](../proposals/kueue-borrowing-and-limiter-composition.md).
Paths are under `internal/` unless noted. Part A lands in PR-A (#164,
`feat/limiters-compose`; its own internals page is
`docs/developer-guide/limiter-composition.md`), Part B and C in PR-B
(`feat/kueue-borrowing`), the release to lenders that cannot reclaim in PR-C
(`feat/kueue-borrow-release`).

## What is read

| Field | Used for |
|---|---|
| ClusterQueue `spec.cohortName` (`spec.cohort` in v1beta1) | cohort membership |
| `…resources[].borrowingLimit`, `…lendingLimit` | B3 formula |
| `spec.resourceGroups[].coveredResources` | co-resources: every covered resource is computed like the GPU |
| `status.flavorsReservation[].resources[].total` | reserved GPUs. Kueue charges quota at reservation; `flavorsUsage` lags it under admission checks. |
| `status.flavorsReservation[].resources[].borrowed` | `wva_kueue_borrow_drift_gpus` |
| `spec.preemption.reclaimWithinCohort` | guaranteed figure, which lenders count |
| `spec.preemption.borrowWithinCohort` | `borrow-within-cohort` |
| `spec.flavorFungibility.whenCanBorrow`, flavors per resource | `flavor-unpinned` |
| `spec.admissionChecks`, ResourceFlavor `topologyName` | `admission-checks`, `topology-aware` |
| `status.fairSharing`, `status.effectiveQuotas` | B1 conditions |
| Cohort `spec.parentName`, `spec.resourceGroups` | nested cohorts, including any Cohort naming this one as parent |
| ResourceFlavor `nodeLabels`, `tolerations` | node fit per flavor, flavor pinning |
| Workloads, cluster-wide (informer, field index on owner UID) | managed check, priority, pending lender requests per flavor, gated demand of every cohort namespace, preemption |
| Namespace `ResourceQuota` `hard`, `used` (cached) | GPU bound and ceiling; with several, the tightest `hard` |
| Deployment `kueue-controller-manager` image | `kueue-version-untested`, `kueue-version-unknown` |
| Its leader-election Lease `renewTime`, `leaseDuration` | `kueue-controller-down` |

The reader resolves the API version through the RESTMapper, which prefers the
served version with the highest discovery priority (v1beta2 today). A cohort
name with no Cohort object is a flat cohort. The `pkg/` constants copied from
Kueue (condition types, reasons, label keys) live in `kueue/kueue_constants.go`
with their source path and the Kueue version they were copied from.

## Code touch points

- **Part A** (PR-A): `accelerator/family.go` (`FindBudgetKey`, `Covers`,
  `IsProductOf`); `engines/allocation/budget_merge.go`, `gpu_budget.go`
  (`FitsGPUBudget` sums demand per covering family key, as `fitPairToBudget`
  does), `node_view.go` (`NodeView`, built whenever a physical limiter is
  declared), the warm pool's `Headroom.Cluster`, and
  `engines/steadystate/quota_alone_report.go` (the transitional shadow pass,
  `TODO(#164)`). Details on the PR-A internals page.
- **Kueue figures:** `kueue/reader.go`, `kueue/borrow.go` (the B3 formula,
  `QueueState.CanReclaim`, the `Reason` constants), `kueue/eligibility.go`
  (B1), `kueue/pending.go` (lender requests), `kueue/workloads.go` (the
  informer), `kueue/version.go` (`ControllerVersion`: version and liveness).
  `config/quota_limiter.go` (`ExternalCaps`, `BoundBy`, the `kueue:` block);
  `engines/allocation/quota_inventory.go` (pools, `Refresh` fingerprint, the
  borrowing log lines). `ResourceConstraints` gains the borrow caps, the
  shared pools and a namespace-to-cohort map; the within-nominal copy drops
  all three. `mergeSharedPools` ORs `Stale` and keeps the older `ReadAt`.
  `namespaceBorrowed` subtracts gated GPUs per type (`FindKey`), the untyped
  bucket shared as in `DeductUnreserved`.
- **Borrowing in the engine:** `engines/allocation/borrow_*.go`
  (`borrow_report.go` holds the per-cycle reasons and
  `MarkPreemptionsUnreadable`); `engines/steadystate/borrowing*.go`,
  `preemption_watch.go`; `engines/scalefromzero/candidates.go` (the wake
  check).
- **Installer:** `deploy/lib/cli.sh` (`WVA_LIMITER`, `WVA_QUOTA_KUEUE_BORROW`),
  `deploy/lib/limiter_policy.sh`, `deploy/lib/physical_limiter.sh`; the
  kueue-reader component adds the Role in the Kueue namespace.
- **Status:** `controllerstatus/publisher.go` publishes `limiterBudgets`
  (withheld and changed entries sorted first).

## Mechanics

**Managed check.** A GPU pod is managed when a Workload with quota reserved
backs it, requests its GPUs, and is owned by Pod, Job, JobSet,
LeaderWorkerSet or StatefulSet. A namespace with no GPU pods keeps its previous
answer; on the first read it counts as not managed. Eligibility is evaluated
only for namespaces with managed variants, and spread over the interval.

**Which lenders count.** `CanReclaim` is evaluated against the priority of the
borrowing namespaces' own Workloads, not the highest priority reserved in
their ClusterQueue. `Any` reclaims; `LowerPriority` reclaims when a lender
Workload outranks at least one borrowing Workload, as Kueue decides; `Never`
does not. Only reclaiming lenders' lent nominal enters `cohortFree`. When the
pool is 0 while non-reclaiming lenders hold unused nominal, the reason is
`lender-cannot-reclaim`.

**Co-resources.** For each resource the GPU flavor's resource group covers,
free capacity is computed with the B3 formula and divided by the variant's
per-replica request from its pod template; the reach in replicas is the
minimum. A request that cannot be determined withholds with
`co-resource-bound`.

**Reserved but not yet visible.** The engine, not the reader, takes from the
shared pool what was asked for that Kueue has not reserved: pods behind the
`kueue.x-k8s.io/admission` gate in every namespace of the cohort's
ClusterQueues (so a second controller's ask is seen), and borrowed growth not
yet visible as pods. It stays deducted until the pods exist or the desired
count goes back down.

**Node view.** PR-A's `NodeView`, from the physical limiter's pod walk
(`gpuusage`): free GPUs per schedulable node, with taints, so a flavor's
tolerations match. Pending pods with a `nodeName` or `nominatedNodeName` debit
their node. The clone per cycle shares the immutable labels and taints, and
the eligible-node list is computed once per cycle. Every replica ordered that
cycle debits it, guaranteed and borrowed, across models. The greedy optimizer
places first; the share planner's idle fill reads the same view afterwards.
`placeUnit` places a P/D pair only whole, with both roles on borrowed
capacity, never one role guaranteed and its partner borrowed.

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

**Lender signal.** A lender waits when a pending Workload in its ClusterQueue
requests a borrowed resource (GPU or covered co-resource) on a flavor its
`nodeSelector` and affinity allow (all flavors when that cannot be
determined), and fits within its unused nominal. `BorrowGovernor` closes the
cohort to new borrowing on the raw signal; the debounce (2 fresh reads and
60 s) feeds only releases and `wva_kueue_lender_waiting_seconds`.

**Backoff.** `BorrowGovernor` keeps a cooldown per (model, cohort), started by
a preemption (`MarkPreempted`) or a placement-timeout release. A repeat while
backing off doubles it from `borrowCooldown`, up to 4 times (cap 40m); one
clean cooldown resets it. The configured value is published on
`wva_kueue_borrow_cooldown_seconds`.

**Version and liveness.** `ControllerVersion` reads the
`kueue-controller-manager` Deployment's image and the leader Lease. Down: the
Lease's `renewTime` older than twice its `leaseDuration`, else
`readyReplicas` 0. Not found or Forbidden: down. A version that cannot be read
publishes `wva_kueue_controller_version_known` 0 and withholds with
`kueue-version-unknown`, unless `kueue.assumeTestedVersion`.

**Never-read.** An entry with a static `-1` and no successful Kueue read is an
unobserved provider: `wva_constraint_provider_skipped{reason="unobserved"}`,
plus `wva_kueue_never_read`. It is not fail-closed.

**Scale.** Workloads come from an informer and ResourceQuotas from a cache; no
lock is held across I/O; Namespace labels come from one LIST per refresh; the
bounded log line is emitted only on change.

## PR-C

`releaseToNonReclaimingLenders`, the versioned `borrowReleaseLedger` key of
`wva-controller-status`, and the release path in
`engines/steadystate/borrowing_release.go` leave PR-B and return in PR-C as
one feature: non-reclaiming lenders' capacity is counted, and a waiting one
gets `ceil(pending / unit)` units at once, bounded by
`min(PendingGPUs, LentOut)`. The release overrides the analyzer's demand floor
and is in flight until its pods are gone and a fresh read of the lender's
reservation postdates the deletion.

## Testing

- Unit tests: `make test`. The envtest suites run under WSL; see
  [Testing](testing.md).
- The live-Kueue e2e (`test/e2e/kueue_borrow_test.go`, label `kueue-borrow`)
  runs in its own CI job, `e2e-tests-kueue-borrow`.
  `deploy/ci-pr-checks/install-kueue.sh` installs Kueue from `registry.k8s.io`
  and refuses a non-kind context unless `KUEUE_ALLOW_NON_KIND=true`.
  `REQUIRE_KUEUE_E2E=true` turns its runtime skips into failures; keep
  `E2E_TIMEOUT` below the job timeout.
- Tested Kueue versions: v0.19 and v0.20. Adding one means re-copying the
  constants and running the e2e against it.
