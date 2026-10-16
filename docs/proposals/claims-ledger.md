# One claims ledger for replicas ordered but not yet visible

- **Status:** proposed.
- **Problem:** "this capacity is already spoken for" is kept in six places,
  each with its own keys, clocks and settle rules. Every limiter defect fixed
  in #183, #184, #186 and #187 was one of those places missing a fact another
  one had.
- **Decision:** one ledger of replica claims, written by every producer, read
  through one view function by both engines. Kueue borrowing moves to its own
  package, and its fields leave `ResourceConstraints`.
- **Shape:** five stages, each behind today's behaviour, none allowed to
  change a scaling decision (golden decisions unchanged).

## Summary

When the scaling manager orders a replica, the cluster does not show it at
once. Its pods are created, wait at Kueue's admission gate, are scheduled, and
only then appear on a node and in a Kueue read. Until then, the controller must
remember that the node, the queue's GPUs and the cohort's free pool are taken,
or the next decision spends them again.

Today that memory is split across six stores. This proposal replaces them with
one **claims ledger**: one entry per ordered replica, pinned at order time to
the node or domain it was fitted to and to the pool GPUs it draws, with one
lifecycle, one timeout tied to `placementTimeout`, persisted across a restart.
Both engines read capacity through one function that applies every deduction
in a fixed order.

For an operator nothing changes, except that the same wait is reported the
same way wherever it comes from, and the hold survives a restart.

## Motivation

### The six stores today

| Store | Where | Key | Clock | Settles when |
|---|---|---|---|---|
| In-flight node ledger | `allocation.InFlightLedger` (`DefaultInFlight`, `inflight.go`) | `namespace/variant`, plus per-pod `partialSeen` | `InFlightTTL` (5m), or `InFlightAdmission.TTL` (placement timeout) on a Kueue flavor; `sightedFor` 10m | placed count reaches `want`, or TTL |
| Borrow record | `allocation.BorrowInFlight` (`DefaultBorrowInFlight`, `borrow_inflight.go`) | `inFlightKey{namespace, variant, cohort, flavor, resource, reclaim}` | none of its own; `lagging` until a read after resolve | `ObserveAt` sees pods created, then a Kueue read after that |
| Gated and admitted stages | `BorrowInFlight.SetCycle` (`gated`, `admitted`, `blocked`, `cycleAt`) | namespace and accelerator | the steady cycle that set them | the next steady cycle |
| Wake's copies | `RecordWake`, `WithinNominalNow`, `Unreserved().WithoutVariants`, `debitTick` | as above, plus the tick's own `tickWakes` | the tick | end of tick, or the steady cycle |
| Share promises | `decision.DefaultSharePromised`, applied by `ReservePromised` and `WithholdPromised` | scope and accelerator | `SharePromisedMaxAge` (5m) | receiver's pods land, or the share drops it |
| Governor's no-hole watch and persisted ledger | `BorrowGovernor` (`observeNoHole`, `NoHoleOf`), `BorrowLedger` in the controller-status ConfigMap | `namespace/variant`, `namespace/model@cohort` | placement timeout, backoff 4x capped | release, or timeout |

A seventh global, `DefaultBorrowCooldowns`, exists only so the wake can read
the governor that the steady-state engine owns.

Each store is correct on its own. The defects are in the gaps between them.

### What the gaps cost (8-GPU units)

These are the findings of the limiter review. Each was fixed with a targeted
change; the point here is how many places each fix had to touch.

1. **A wake ignored the borrowing cooldown** (AR1). A no-hole wake in its
   cooldown was admitted as reclaim-exempt, preempted a peer's 8 GPUs, timed
   out after `placementTimeout`, and did it again. The cooldown lived in the
   governor; the wake read the borrow record. Fixed in #187 (`29a49bd35`) by
   reading `DefaultBorrowCooldowns` before the exemption.
2. **The no-hole safety net did not survive a restart** (AR2). It was fed only
   from the in-memory borrow record, so after a restart a Pending 8-GPU
   replica within guaranteed was held forever. Fixed in #187 (`daa317a93`) by
   adding `NoHole` to `BorrowLedger`, and again (`6e4460207`) because a restored
   watch must wait for a cycle that sees its variant.
3. **A node was promised twice in one cycle** (WN5). The steady pass picked N1
   (8 free) for a decode replica, but only admitted it to the node ledger at
   the end of the cycle. A wake in between saw N1 free and took it. Fixed in
   #187 (`06c4f1c29`) with `Provisional` and `Commit`.
4. **A half-placed gang lost its hole** (WN2). A 16-GPU LeaderWorkerSet group
   whose leader landed on N1 no longer fit as a whole, so nothing was held for
   its worker and N2 went to someone else. Fixed in #187 (`c48b67020`) with
   per-pod `partialSeen`.
5. **A wake's borrow draw settled only through the steady engine** (minor).
   If the steady cycle skipped the model, the draw held its 8 GPUs for the
   full TTL. Fixed in #187 (`296ef2aeb`).
6. **A gated replica cancelled another workload's Kueue charge** (KU1,
   #186 `a64010e16`). An 8-GPU replica at Kueue's gate counted as managed
   usage, so it hid 8 GPUs a training job held. The fix taught the quota
   inventory to read the borrow record's gated pods (`SetGatedSource`): a
   seventh consumer of the same fact.

Every fix was right. Together they show the shape: a new fact about a pending
replica has to be written into the store that owns it and then copied, by
hand, into each consumer that needs it. The two engines also apply the same
transforms in a different order: `prepareBorrowing` (`steadystate/borrowing.go`)
runs `DeductUnreservedAt`, `DeductReclaimed`, `BlockCohorts`,
`WithholdByVariants`, then the governor; `placementForModel`
(`scalefromzero/candidates.go`) runs `WithholdPromised`, then
`WithinNominalNow`, `DeductReclaimed`, `ReclaimableNominal`. Two orders mean
two sets of edge cases to review for every change.

### Layering

`internal/engines/allocation` holds about 5,400 lines of Kueue policy in its
`borrow_*.go` files, and nine non-test files there import `internal/kueue`.
`ResourceConstraints` (`limiter_interfaces.go`) carries seven fields only a
Kueue quota sets: `NamespaceBorrow`, `SharedPools`, `NamespaceCohorts`,
`BorrowWithheld`, `NamespaceNominal`, `BorrowNamespacesSet`, `KueueNeverRead`.
Every consumer must know to strip them (`WithinNominal`, `GuaranteedOnly`).

## Goals

- One record of every replica the controller ordered and the cluster does not
  show yet, whoever ordered it: steady pass, wake, share fill.
- One lifecycle and one timeout for that record, tied to `placementTimeout`.
- The record survives a restart.
- One function both engines call to turn a snapshot and the ledger into the
  capacity a decision may spend, with every deduction in one fixed order.
- Kueue borrowing in its own package, behind the `ConstraintProvider`
  interface, so `ResourceConstraints` carries no Kueue-only fields.

## Non-goals

- Changing any scaling decision. Every stage must leave golden decisions
  unchanged; a behaviour change is its own PR.
- Changing what Kueue figures mean (that is [per-ClusterQueue
  modelling](kueue-per-clusterqueue.md)).
- Coordinating several controllers. One ledger is per process; cross-controller
  claims remain the multi-tenant Lease question.
- Changing the governor's policy (backoff, cooldown, release order). Only where
  its inputs come from changes.

## Design

### A claim

One entry per replica, written when the replica is ordered:

```go
type Claim struct {
    ID        string    // namespace/variant#n, stable across cycles
    Producer  Producer  // Steady, Wake, ShareFill
    Shape     ReplicaShape
    Nodes     []PodHole // per pod: node or domain chosen at order time, GPUs
    Pool      []PoolDraw // queue/cohort/flavor/resource GPUs, reclaim flag
    Flags     ClaimFlags // NoHole, Reclaim, Promise, Borrowed
    Stage     Stage
    OrderedAt time.Time
    StageAt   time.Time
}
```

`Nodes` is what the node ledger holds today, pinned per pod (as
`partialSeen` is now). `Pool` is what the borrow record holds today.
`Flags` replaces the no-hole watch, the reclaim key bit and the share
promise: a share fill is a claim with `Promise` set, not a separate store.

### One lifecycle

```
ordered -> created -> scheduled -> visible
```

- **ordered**: the decision was made. Holds its node holes and its pool GPUs.
- **created**: its pods exist (`VariantObservation.Current` rose). Pods at
  Kueue's admission gate are in this stage; the gated-pod deduction becomes
  "created claims not yet admitted", not a separate figure.
- **scheduled**: every pod holds a node (`PlacedGroups`). Node holes are
  released; pool GPUs are still held, because the Kueue read may predate the
  admission.
- **visible**: a node snapshot and a Kueue read, both taken after
  `StageAt`, show it. The claim is removed.

The "a read after the event" rule that `lagging`, `readAfter` and the
in-flight `seen` list each implement today becomes one check on one field.

### One timeout

A claim that has not reached `scheduled` within `placementTimeout` (three times
it while a cluster-autoscaler scale-up is pending, as now) expires: it stops
holding, and the governor is told, exactly as `observeNoHole` and
`timeoutReleases` are told now. `InFlightTTL`, `SharePromisedMaxAge` and
`sightedFor` go away. Where today the node ledger holds an unflavored replica
for 5 minutes and the placement timeout is set longer, the claim holds for the
longer one; stage 1 measures whether any test depends on the shorter hold.

### Persisted

The ledger is written to the controller-status ConfigMap beside the governor's
clocks, replacing `BorrowLedger.NoHole` and the first-sighting rule
(`InFlightAdmission.ExistingOnly`). On restart a claim is restored with its
stage and clocks, never younger than it was, and dropped at its first cycle if
its variant is gone. Leader-only writes, bounded size, as `BorrowLedger` now.

### One view

```go
func (l *Ledger) ViewFor(consumer Consumer, own map[string]bool,
    snap Snapshot, now time.Time) (View, Gates)
```

`View` is the constraints and the node view a decision may spend. `Gates` is
the governor's answers (cohort blocked, model cooling down, lender waiting).
Deductions apply in one order, for both engines:

1. claims' node holes onto the node view, except `own`;
2. claims' pool draws off the shared pools, except `own`;
3. reclaimed nominal off what is reclaimable;
4. promised GPUs withheld;
5. governor gates: blocked cohorts, cooldowns, backoff;
6. the consumer's scope: within nominal for a wake, the warm pool and rescale;
   guaranteed only for the share; the full figures for the steady pass.

The engines stop calling `DeductUnreservedAt`, `DeductReclaimed`,
`BlockCohorts`, `WithholdPromised`, `ReservePromised` and `WithinNominalNow`
directly. `DefaultBorrowCooldowns` goes away: the gates come with the view.

Writers call one method, `l.Claim(...)`, at the point they decide, so the
provisional admission of #187 becomes the normal path rather than a repair.

### Kueue borrowing in its own package

`borrow_*.go`, the governor and the ledger's pool half move to
`internal/engines/allocation/kueueborrow` (name open). It wraps the Kueue
quota reader as a `ConstraintProvider` decorator: the base reader produces
plain `ResourceConstraints`, the decorator holds the Kueue figures and answers
the view's pool and gate questions. The seven Kueue-only fields leave
`ResourceConstraints`; `WithinNominal` and `GuaranteedOnly` become consumer
scopes inside `ViewFor`. `allocation` no longer imports `internal/kueue`.

## Alternatives considered

- **Keep the targeted fixes.** Every known defect is fixed, and the stores
  work. The cost is in the next change: a new producer (the warm pool's
  growth, which no limiter bounds today) has to be wired into five stores and
  two transform orders, and each review has to check every gap again. This is
  the fallback if stage 1 shows the ledger cannot reproduce today's decisions.
- **Share the stores through one lock, not one ledger.** Fixes ordering
  races (WN5) but not the duplicated keys and clocks, and not the restart.
- **Rebuild everything from pods every cycle.** The pod list tells what was
  created, not where the decision put it or which pool it drew from, and a
  namespace-scoped install cannot list every pod.
- **Persist each store separately.** Keeps six settle rules and adds six
  restore paths.

## Staged rollout

Each stage is one PR. Each must pass the golden-decision suite unchanged.

1. **Golden decisions.** Record, from today's envtest and unit fixtures, the
   targets, reasons and released units of every borrowing, wake, share and
   node-fit scenario. No production change.
2. **The ledger, shadowed.** Every producer also writes claims; a debug log
   compares `ViewFor` with today's figures per cycle. Nothing reads it.
3. **The wake reads the view.** `placementForModel` and the wake tick call
   `ViewFor`; the wake's copies, `debitTick` and `DefaultBorrowCooldowns` go.
4. **The steady pass reads the view.** `prepareBorrowing` and `NodeViewFor`
   call it; `BorrowInFlight`, `InFlightLedger`, the share promise store and
   `BorrowLedger.NoHole` go. The persisted ledger replaces the first-sighting
   rule.
5. **Move Kueue borrowing.** The package move and the decorator; the
   `ResourceConstraints` fields go. Mechanical, but last, because it touches
   every file above.

## Test strategy

- The golden-decision suite from stage 1 runs on every stage.
- One table test per lifecycle edge: ordered to created, created but gated,
  scheduled before the Kueue read, visible, timeout, restart in each stage.
- Every regression test added in #183, #184, #186 and #187 stays and must
  pass unchanged; their names are the list of cases the ledger has to cover.
- The kind e2e suites for borrowing and scale-from-zero run on stages 3 and 4.

## Risks

- **A silent decision change.** Merging clocks changes when an unflavored
  hold ends. Stage 2's comparison log is there to find it before any reader
  switches.
- **Bigger restore.** A persisted ledger can restore a claim whose pods were
  deleted while the controller was down. The first cycle must drop claims
  whose variant or pods are gone, and never restore one younger.
- **One big move.** Stage 5 touches nearly every borrowing file. It lands
  alone, with no logic change, so review is a diff of moves.

## What it removes

`InFlightLedger`, `BorrowInFlight` (with `lagging`, `SetCycle`, `Unreserved`),
`SharePromisedStore`, `DefaultBorrowCooldowns`, `BorrowLedger.NoHole`,
`InFlightTTL`, `SharePromisedMaxAge`, `sightedFor`, the per-engine transform
chains, and seven fields of `ResourceConstraints`.

## Open questions

1. Should an unflavored replica be held for `placementTimeout` (one clock) or
   keep a shorter node hold, as `InFlightTTL` gives today?
2. Should the warm pool's growth write claims? It would then be bounded by
   the limiters, which it is not today; that is a behaviour change and needs
   its own decision.
3. Does a share fill become a claim with `Promise`, or does the share keep
   its own ledger (`utilization_share_ledger.go`) and only publish claims?
   The share's ledger also tracks transfers, which are not replicas.
4. Package name and boundary for the Kueue decorator: does the governor move
   with it, or stay in `allocation` as a consumer of `Gates`?
5. How big may the persisted ledger grow in the controller-status ConfigMap
   before it needs its own object?

## Related

- [Kueue borrowing, and limiters that compose](kueue-borrowing-and-limiter-composition.md)
- [Model Kueue per ClusterQueue](kueue-per-clusterqueue.md)
- [Sizing against in-flight capacity](sizing-against-in-flight-capacity.md)
- [Limiter composition (developer guide)](../developer-guide/limiter-composition.md)
