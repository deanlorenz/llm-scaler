# How the limiter set is built

Developer notes for the code behind
[Selection & lifecycle](../reference/quota-limiter.md#selection--lifecycle). An
operator needs the reference page, not this one.

## The declared set

`config.Config.EffectiveLimiterTypes()` returns a `config.LimiterTypes`: the kinds
the `limiters:` list on the saturation `default` entry declares,
`LimiterTypeInventory` first, then `LimiterTypeQuota`. Empty means no limiter.
It reads the live ConfigMap under the config lock, so a caller asking more than
one question takes one snapshot and asks it (`Declared`, `HasPhysical`,
`HasQuota`). `String` is the one rendering for logs and the controller status:
the kinds joined by `+`, or `none` for the empty set. `LimiterTypeNone` is only
that spelling, never a member of a set.

`Config.HasPhysicalLimiter()` is the shorthand for callers with that one
question. `Config.EffectiveLimiterSummary()` adds every entry's name
(`inventory+quota/gpu,tenant-quota`); it is what `wva-controller-status` publishes
as `limiter`.

`gpu-inventory` and `inventory` are two spellings of the physical kind
(`isPhysicalLimiterType`). A second physical entry is accepted and ignored;
`ScalingPolicy.IgnoredPhysicalLimiters` / `Config.IgnoredPhysicalLimiters` name
it, and both log sites use `config.IgnoredPhysicalLimitersMessage`.

## The factory

`allocation.NewLimiterFromConfig` (`internal/engines/allocation/limiter_factory.go`)
builds:

- the physical kind: a `DefaultLimiter` named `allocation.PhysicalLimiterName`
  (`gpu-limiter`) around `TypeInventoryWithUsage`;
- each quota entry: a `DefaultLimiter` named after the entry, around one
  `QuotaInventory`;
- more than one: a `CompositeLimiter`, physical first. It has no logic: the
  engines reach the constituents through `Constituents()`.

`allocation.LimiterSignature` is the types plus every quota entry, marshalled
with sorted keys. Both engines compare it every cycle and rebuild on a change.
The physical entry itself is not in it: it takes no fields, and its name only
labels the summary.

## The quota inventory

`QuotaInventory` implements `Inventory` (`limiter_interfaces.go`) and the
`NamespaceAwareInventory` extension; it is tied to one scope. Each cycle
`DefaultLimiter.ComputeConstraints` calls `SetUsed(usedByType)` and, through
feature detection, `SetUsedByNamespace(usedByNS)`; each instance ignores the one
irrelevant to its scope. It then reads `GetResourcePools` (and, at namespace
scope, `NamespaceResourcePools`) into the `ResourceConstraints` the optimizer
allocates within. `UsageBasis()` is `ManagedUsage`: the usage it is fed is the
saturation engine's population sum, never the pod walk.

`GetResourcePools` is one pool per configured type at cluster scope. At
namespace scope it is the per-type sum over the listed namespaces, a summary
only (`ResourceConstraints.NamespaceScoped`): the bounds are the per-namespace
caps in `NamespaceResourcePools`. An unlimited `-1` is a sentinel pool
(`Limit == -1`) at either scope, so `Limit = 0` is always a real deny;
`TotalLimit`, `TotalAvailable` and `Remaining` leave the sentinels out.

`Refresh` is a no-op for a static entry. With `kueue.enabled` it re-reads Kueue
through `internal/kueue.Reader` (at most once per `refreshInterval`) and installs
`static.BoundBy(snapshot)` as the effective entry. It never returns an error: a
failed `ComputeConstraints` skips the provider, so a Kueue outage would lift the
cap; the last snapshot, or the static entry alone, stays in force instead. Type
keys across the static and Kueue maps match by `accelerator.SameName`, and
usage reconciliation finds its pool by `accelerator.FindKey`.

`QuotaLimiterEntries.Validate()` holds the rules the reference lists, with
`MaxQuotaValue` the per-type ceiling; `SaturationScalingConfig.validateLimiters`
runs it at parse time.

Quotas alone read no nodes: the limiter paths never call
`K8sWithGpuOperator.Discover`, `DiscoverUsage` or `DiscoverNodes`;
`collector.CollectInventoryK8S` (the `WVA_LIMITED_MODE` log) is gated by
`scalingpolicy.ShouldCollectClusterInventory`; and the `gpuusage.Refresher` timer
by `Refresher.Periodic` (wired in `cmd/main.go`). The scale-from-zero engine's
`EnsureFresh` is ungated but asks only for a provider on the physical basis.

## Budgets and the family tree

The budget model is in `budget_merge.go`. `mergeConstraints` intersects the
cluster bounds and `mergeNamespaceConstraints` the namespace allowlists, each
per provider through `allowlistBound` (a listed key; a family listed only
through products bounds their sum; a product falls back to an EXPLICITLY listed
family key). The merged map no longer says which family keys were explicit, so
`accelerator.FindBudgetKey` denies an unlisted product beside a listed sibling,
and a family name never resolves to one of several product keys.

Both merges therefore take the accelerator names the cycle's workloads declare
(`declaredAccelerators`) and materialize each as a key of its own when every
allowlist admits it (`materialized`): an unlisted product under an explicit
family key, and a bare family name at its summed bound. `FitsGPUBudget`
materializes its demand's names, and fits the demand per budget key and per
key that covers several of its names (`accelerator.Covers`), as
`fitPairToBudget` fits a P/D pair.

## Node fit

`NodeView` (`node_view.go`) is one cycle's free GPUs per node, consumed in
order. `NodeViewFor` builds it from `decision.NodeGPUStore.Read` (no label
copy; it keeps the snapshot's label and taint slices, which `Publish` copied
and nothing writes) when the physical limiter's constraints are among the
cycle's (`PhysicalBounds`), so a skipped physical provider skips node fit with
it; with no snapshot younger than `decision.NodeGPUsMaxAge` it returns a reason
for the caller to log. Nodes are `nodeSlot`s in name order, addressed by index.
A `Clone` shares the slots and the eligibility cache (`eligible`, keyed by
accelerator and tolerations, and `largest` per accelerator) and copies only the
sparse debits; `adopt` commits a clone's debits.

- Placement (`place`, `bestFit`): a replica's pods, largest first, each into
  the smallest hole that holds it (first in name order on a tie). A node is
  eligible (`eligibleNode`) when schedulable, of the accelerator
  (`accelerator.SameName`), and every `decision.NodeGPU.Taints` entry
  (`gpunodes.placementTaints`: NoSchedule/NoExecute taints `refusesNewPods`
  does not already judge) is tolerated by `ReplicaShape.Tolerations`
  (`scaletarget.PodTolerations`, the leader's template). With
  `ReplicaShape.DomainKey` (`scaletarget.ExclusiveTopology`) and more than one
  pod, all pods land in one value of that label (`domainsOf`, tried in order).
  `ReplicaShape.Placements` (the templates' nodeSelector and required node
  affinity) narrows the nodes further (`eligibleFit`) on every path: the
  greedy pass (`nodeFit.replica`), the borrowing placer, a wake (`shapeOf`)
  and the ledger's holes (`steadystate.admitInFlight`).
  `VariantReplicaState.Tolerations`/`TopologyKey`/`Placements` and
  `Candidate.Tolerations`/`DomainKey`/`Placements` (`variantmeta.PodPlacements`)
  carry them from the scale target, which discovery already reads.
- Owners: every debit is charged to `NodeView.owner` (`setOwner`): the model
  (`modelKey`) for the greedy and rescale passes, `PromisedOwner` for
  `ReservePromised`, `InFlightOwner` for the ledger. `DebitsExcept` and
  `without` take one owner's placements back.
- Steady state: `selectV2Optimizer` sets `GreedyByScoreOptimizer.Nodes`
  (`ShadowOptimize` clears it: the quota-alone pass has no physical limiter);
  `Optimize` clears it when the pass ends, and drops the previous pass's
  `pass` when it starts. `decideV2` holds the share's promises on it first
  (`ReservePromised`: the freest nodes, whole, as `shareFreeNodes` does).
  `fairShareRolePickOn` caps a variant at the replicas `fits` on nodes (0 is
  scarcity), and `fitPairToBudget` runs `fitPairToNodes` after the pool fit:
  the roles are placed a replica at a time, the one furthest behind its share
  first, until each has its k or one does not place; a role with k > 0 that
  placed none fails the iteration. A cut the nodes made is
  `gpuLimitTracker.markNodeFit`, and `applyGPULimitAttribution` sets
  `LimitedBy` to `NodeFitLimiterName` (`node-fit`) for it; the steady-state
  engine's `constrainedMessage` adds `(limited by <LimitedBy>)` to the
  `ResourceConstrained` Event.
- Rescale: `rescaleModelDecisions` fills each role by pool (`fillRole`), then
  `fitRescaleToNodes` places the fill like `fitPairToNodes` does, charges the
  model, and takes back what did not place (refunding the cycle's free GPUs).
  A model filling more than one role grows only if each placed a replica.
- A replica's pods are `VariantReplicaState.PodGPUs` when they sum to the
  replica, else one pod; `replicaPods` returns nil (not judged) for an unknown
  layout larger than every node of the accelerator, logged once per variant in
  the engine's `LayoutLog` (`WithLayoutLog`).
- Scale-from-zero: `decideWake` (`wake_tick.go`) holds `wakeMu` across the check
  and the debit, withholds `tickWakes` (this tick's admitted demand, by
  namespace) from fresh constraints with `WithholdPromised`, and fits sets to
  `tickNodes`, built on first use per tick with the promises held on it;
  `recordWake` debits both and admits the wake to the in-flight ledger.
  `claimShareTransfer` and snapshot-checked wakes have no node view and are
  admitted to nothing (commented at `recordWake`).
- The warm pool: `GreedyByScoreOptimizer.Optimize` publishes headroom once the
  pass is over from `passHeadroom` (the debited budgets and `NodeView.Holes`).
  `RepublishHeadroom(planned, promised)` republishes it `without` the models
  the utilization share planned this cycle (`plannedByOverrides`: their
  targets are the share's) and with the share's promises made since withheld
  from the pools and held on the nodes. The idle-fleet path publishes holes
  through `PublishNamespaceHeadroomOnNodes`. The reconciler caps growth by
  `decision.HeadroomStore.Units`, which is (0, known) for a Pod larger than
  every node.
- The utilization share after the pass: `decideV2` hands the evaluation
  `GreedyByScoreOptimizer.LastPass` (`utilizationShareState.left`).
  `BuildShareGroups` replays the pass's spends (`spendOp`, recorded per model
  in order) for every model it does not plan onto its own merge of the
  constraints (`PassLeft.Replay`), so each group's `Budget` and `ClusterFree`
  are net of them; `shareNodeInputs` subtracts `PassLeft.NodeDebits` (other
  models' placements and the in-flight holds) from the node snapshot
  (`shareFreeNodesAfter`) before the share holds its own promises.
- The utilization share's cluster budget is the cluster bounds alone
  (`clusterBudgets`, over `mergeConstraints`): a namespace-scoped quota is no
  budget over every namespace. A model of a namespace such a quota excludes is
  planned in no group (`ShareGroupOptions.Unshared`) unless a finite cluster
  bound covers its type. The cluster group's budget and `ClusterFree` are what
  that bound leaves after the namespace groups' claims, in whole replicas; then
  `splitClusterFree` divides each finite cluster key between every group that
  draws on it, also in whole replicas of each group's smallest variant.
- `gpunodes.usageWalk` counts a Pending pod on its `Status.NominatedNodeName`
  (`occupiedNode`), in every view.

### The in-flight ledger

`allocation.InFlightLedger` (`inflight.go`; `DefaultInFlight` for both
engines) holds the holes of replicas admitted but not yet holding a node.
`NodeViewFor` applies it to every view it builds (`holdOn`, charged to
`InFlightOwner`). An entry is per variant (`namespace/name`): `want` replicas
of a `ReplicaShape` to hold a node, `base` placed when admitted, and later
placed counts with when each was first seen. A count is applied only to a view
whose snapshot (`NodeView.takenAt`, from `NodeGPUStore.Read`) was taken after
it was seen; before, the snapshot does not show those pods on their nodes
either. `settle` reads each entry's `placed` func (`variantmeta.PlacedReplicas`:
scheduled or nominated pods, not deleting; LWS groups whole) outside the lock,
and ends an entry at `want`.

- Admission: the steady-state engine admits each decision's target after the
  enforcer (`admitInFlight`, only when the cycle had a node view), skipping
  the models the share plans (their fills are promises); placed-now is
  `FilledReplicas` when the pods were listed. The scale-from-zero engine admits
  each woken candidate at want 1 (`wake_tick.go`).
- TTL: `InFlightTTL` (5 minutes), from the admission that raised `want`; an
  equal or lower `want` keeps the clock, so a replica Pending for good is not
  held anew every cycle. An expired entry holds nothing and is dropped a TTL
  later.
- Re-placing its own growth: `holdOn` charges each held replica to its own
  owner (`inFlightReplicaOwner`: the variant and the replica's number). The
  steady-state pass sizes a variant from its current replicas, so before
  `selectV2Optimizer` hands the view to the optimizer it calls
  `ReleaseUncreated` with each variant's `CurrentReplicas`: the holes of the
  replicas numbered above them, whose pods do not exist yet, are the pass's to
  re-place. Held against it, the target alternated 3/2/3 on one free node. A
  replica whose pod exists keeps its hole, and the wake's view releases
  nothing.

`BenchmarkNodeFitCycle` measures one cycle on 2000 nodes with 80 labels each and
50 models, on a half-free cluster and on a mostly-full one fragmented into
4-GPU holes, with and without node fit;
`TestFragmentedClusterDecisionsDifferFromPoolsOnly` checks the fragmented arms
decide differently.

## Skipping an unavailable provider

`allocation.ComputeAvailableConstraints` computes constraints from each
provider on the usage basis it asks for and returns the ones it skipped as
`SkippedProvider`, with `Cause` one of `constants.ProviderSkip*`
(`unobserved`, `failed`, `no_constraints`). The survivors are intersected, so a
skip can only make the answer more permissive.

`wva_constraint_provider_skipped{provider, reason}` has two producers, each
owning its reasons (`metrics.SetConstraintProvidersSkipped`):

- the steady-state engine (`providerSkipReport`,
  `internal/engines/steadystate/provider_skip_report.go`) owns `unobserved`,
  `failed` and `no_constraints`. It replaces its series every time it computes
  constraints, on the optimizing and idle-fleet paths, and logs only when the
  skipped set changes;
- the `gpu-policy` readiness gate (`gpuPolicyReady`, `cmd/main.go`) owns
  `node_read_denied`. It fails the pod only for a physical limiter declared
  alone. Beside quota entries it probes node read, publishes a denial here, and
  logs on change (`composedNodeReadState`).

## Scale-from-zero

`scalefromzero.Engine.placementFor` returns the `placement` one namespace's wakes
are judged against. When every provider is unavailable it falls back to the
namespace's last successful constraints younger than `lastConstraintsMaxAge`
(5 minutes). Each wake admitted against that snapshot is debited from it
(`recordWake` -> `debitSnapshot`), and `SelectionInput.Admitted` adds the debit
to every later fit check, so two wakes cannot pass on the same free GPUs. The
debit is per namespace and per accelerator name as each wake declared it:
best-effort. With limiters declared, every provider unavailable and no young
snapshot, a wake is counted in `wva_scale_from_zero_wake_unchecked_total`.

Against fresh constraints, the wakes of one tick are carried by hand
(`tickWakes`, see [Node fit](#node-fit)): their pods do not hold a node yet,
so the next wake's usage does not count them. Across ticks the in-flight
ledger carries a wake's node hole ([The in-flight ledger](#the-in-flight-ledger)).
Its pool demand is not carried: a quota counts it once the steady-state engine
next publishes the managed usage, the physical pool once its pods land. Under
a quota alone, a second wake in that window can pass on the same quota.

## Per-namespace usage feeding

`DefaultLimiter.ComputeConstraints` feature-detects `NamespaceAwareInventory`
and calls `SetUsedByNamespace(usedByNS)` beside the always-safe `SetUsed`. For a
quota the caller supplies the managed view: `CurrentReplicas * GPUsPerReplica`
summed by `(Namespace, AcceleratorName)` over the cycle's population
(`computeCurrentGPUUsageByNamespace`).

Every namespace being decided about must be **present** in that map, with an
empty inner map when it holds nothing. `NamespaceResourcePools` builds caps only
for the namespaces it is given, so an absent one silently loses its quota. Both
callers guarantee it: the saturation engine for every namespace it optimizes,
the scale-from-zero engine for the namespace it places into.

## The transitional shadow pass

For a policy declaring both kinds, `reportQuotaAloneAllocation`
(`internal/engines/steadystate/quota_alone_report.go`) runs the optimizer a
second time over the quota constraints alone, once every 10 eligible cycles, and
logs `Upgrade note (transitional)` when the set of differing variants changes.
It is marked `TODO(#164)`: remove it, `quotaAloneReportEvery`, the two
`quotaAlone*` engine fields and `GreedyByScoreOptimizer.ShadowOptimize` one
release after the first release that composes limiters.
