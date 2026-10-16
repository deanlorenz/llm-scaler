# Model Kueue per ClusterQueue, not per namespace

- **Status:** proposed.
- **Problem:** Kueue admits against a ClusterQueue. The scaling manager reads
  Kueue per namespace, summing or taking the smallest of the queues a
  namespace has a LocalQueue on. Where a namespace maps to more than one queue,
  or a queue serves more than one namespace, the figures are a guess.
- **Decision:** bind each variant to its ClusterQueue through its pod
  template's `kueue.x-k8s.io/queue-name` label, and compute every Kueue figure
  per queue, spent once per pass by every variant bound to it.
- **Shape:** four stages. The first only reads and reports the binding.
- **Replaces:** #186's `multi-queue-namespace` withholding and its
  per-(namespace, queue) charge.

## Summary

Today a Kueue-bounded quota gives each **namespace** a figure: the grant of
the ClusterQueues its LocalQueues point to. Kueue does not work that way. A
replica's pods carry a `kueue.x-k8s.io/queue-name` label naming one LocalQueue,
that LocalQueue names one ClusterQueue, and Kueue admits the replica against
that queue alone.

This proposal makes the scaling manager do the same. Each variant is bound to
the ClusterQueue its pod template names. Nominal, guaranteed, reach, the charge
of other workloads and co-resources are all computed per queue. A queue shared
by two namespaces is one budget, spent once, not a ceiling each namespace sees
in full.

For an operator: a namespace on two queues stops being capped at the smaller
one, a shared queue stops being over-ordered, and a variant whose queue cannot
be told says so with a reason.

## Terms

As in [Kueue borrowing, and limiters that
compose](kueue-borrowing-and-limiter-composition.md#terms): nominal,
guaranteed, within nominal, cohort, lender, flavor. Plus:

- **Binding**: the ClusterQueue a variant's pods are admitted through.
- **Charge**: what Kueue has reserved in a queue for workloads that are not
  this controller's managed replicas (a training job, another tenant).

## Motivation

All figures are GPUs; every replica is 8 GPUs.

### What #186 fixed, and how

#186 ("charge each queue what Kueue charges it") fixed three findings of the
Kueue review with targeted changes, all on the per-namespace model:

1. **Other workloads' reservations were ignored** (KU1). Queue Q, nominal 16,
   `reclaimWithinCohort: Never`; a training job holds 8, peers borrow 8; the
   serving namespace holds 0. Headroom read 16 - 0 = 16 less what peers
   borrow = 8, so one replica was ordered that Kueue could not admit, and it
   was never released. Fixed by `chargeNonManaged`
   (`allocation/quota_charge.go`): each namespace's figures are charged with
   the queue's `Reserved` less what that namespace's managed variants hold.
2. **A namespace on two queues was granted their sum** (KU2). Serving
   variants label CQ-S (16), batch variants label CQ-B (16); the namespace was
   capped at 32, Kueue admits 16 of either kind. Fixed in `namespaceCaps`
   (`kueue/reader.go`): the smallest single queue's figure per type, and
   borrowing of an ambiguous type withheld with
   `multi-queue-namespace`.
3. **Growth into own lent-out nominal preempted own borrowers** (KU3). Fixed by
   `yieldReclaimed` (`allocation/borrow_reclaim_yield.go`): the borrower's
   target drops in the same pass.

Co-resources on the nominal path were also fixed (`24389a4d9`), checked per
namespace (`1ce9bc923`).

### What is still wrong

The per-namespace model cannot be made exact, because the fact it lacks is
which queue a replica goes to.

1. **The interim min under-spends.** Same namespace, CQ-S nominal 16, CQ-B
   nominal 8. Every serving variant labels CQ-S. The namespace is capped at
   8 for the type (the smaller queue), so serving stops at one replica with 8
   GPUs of CQ-S free, and borrows nothing of that type at all.
2. **A shared queue is a ceiling, not a partition.** CQ-T, nominal 16, has
   LocalQueues in namespaces A and B; Kueue has 8 reserved. Each namespace is
   charged the other's usage, so each sees 8 free, and in one pass both order
   a replica: 16 ordered against 8 admissible. The second waits at Kueue's
   gate until the placement timeout releases it. This is documented
   (`quota-limiter.md`, the kueue-bounded-quotas path's "When not to take it",
   the `KueueQuotaSource` comment), not solved.
3. **Co-resources per namespace on a shared queue.** CQ-T has 64 CPUs; each
   replica asks 48. A and B each fit one, so 96 CPUs are ordered against 64.
4. **The charge is a safe bound on the wrong question.** For a type two of
   its queues grant, `namespaceCharge` credits the namespace's managed GPUs to
   every granting queue and gives `min_q(N_q - R_q) + M`: the headroom of the
   fullest queue. That never over-orders, by design, but it answers "what is
   left in the worst queue", when the variant only ever uses one. With the
   binding the answer is `N_q - R_q` for that queue.
5. **The label is never read.** `kueue.QueueNameLabel` is a constant used only
   to say the label is not proof of management (`kueue/eligibility.go`).

## Goals

- Bind every Kueue-bounded variant to one ClusterQueue, from its pod template.
- Compute nominal, guaranteed, reach, charge and co-resource headroom per
  queue, and spend each queue's headroom once per pass across all variants
  bound to it, whatever their namespace.
- Remove `multi-queue-namespace`, the per-namespace min, and the
  per-(namespace, queue) charge.
- Say, with a reason, when a variant cannot be bound.

## Non-goals

- Changing cohort arithmetic. The cohort's free pool is already per cohort,
  flavor and resource (`SharedPools`), and matches Kueue's `available()`.
- Several controllers on one queue. One controller per cohort stays the rule;
  the multi-tenant Lease is separate.
- Flavor choice inside a queue. A queue with several flavors for one type is
  still read as today (`PinRequired`).
- Writing the label. The scaling manager reads pod templates; it never edits
  them.

## Design

### Binding a variant

For each variant whose namespace a Kueue entry governs:

1. Read `kueue.x-k8s.io/queue-name` from the scale target's pod template (for
   a LeaderWorkerSet, the leader's; worker and leader must agree, or the
   variant is unbound).
2. Look up that LocalQueue in the variant's namespace. The reader already
   lists LocalQueues (`kueue/reader.go`); keep the map
   `namespace/name -> spec.clusterQueue`.
3. The variant's binding is that ClusterQueue.

No label:

- If the namespace has a LocalQueue named `default` and Kueue's
  `LocalQueueDefaulting` is on, bind to it, as Kueue would.
- Otherwise, if the namespace has LocalQueues on exactly one GPU queue, bind
  to it (today's behaviour, which is exact in that case).
- Otherwise the variant is **unbound**: it grows by nothing Kueue-backed and
  carries reason `queue-unbound`. Its static caps still apply.

A label naming a LocalQueue that does not exist, or whose ClusterQueue is not
read, gives `queue-not-found`: Kueue would hold the pods pending, so ordering
them only fills the gate.

The binding is read every cycle with the variant's other template fields
(`VariantReplicaState`), so a template change rebinds on the next cycle.

### One entry per queue

`ResourceConstraints` gets one entry per ClusterQueue (in the Kueue package
of the [claims ledger](claims-ledger.md) proposal, not on the shared struct):

```go
type QueueBudget struct {
    Queue      string
    Cohort     string
    Flavors    []FlavorFigures // nominal, guaranteed, within nominal, reach
    Charge     map[string]int  // per flavor/resource: Reserved - managed admitted
    CoHeadroom map[string]int  // CPU, memory per flavor
    Variants   []string        // bound variants, any namespace
}
```

- **Charge** is Kueue's `Reserved` for the queue less the GPUs of the managed
  replicas **bound to it** that Kueue has admitted (gated replicas still do
  not cancel it, as #186 made sure). No per-namespace subtraction.
- **Nominal, guaranteed, within nominal, reach** are the queue's own, as
  `kueue/borrow.go` computes them now per queue, before they are folded into
  a namespace.
- **Co-resources** come off the same queue entry.

The optimizer spends a queue entry like a shared pool: each replica of a bound
variant debits it, so two namespaces on CQ-T see one 8-GPU headroom between
them. Namespace figures (`NamespacePools`) are still built, as the sum of each
namespace's variants' share, for reporting and for the namespace
ResourceQuota bound, which stays per namespace.

### Removed

- `namespaceCaps` and `minCaps` (the per-namespace min).
- `ambiguousType` and `ReasonMultiQueueNamespace`.
- `namespaceCharge` and the namespace fold in `chargeNonManaged`.
- The per-namespace co-resource check on the nominal path.

### Reasons and metrics

- New reasons on `wva_quota_borrow_withheld` and in the `Kueue borrowing
  applied` line: `queue-unbound`, `queue-not-found`.
- `multi-queue-namespace` is kept as an accepted value for one release (an
  alert may match it), never emitted.
- A `cluster_queue` label on the Kueue tiers of `wva_quota_limit_gpus` and on
  `wva_quota_borrow_withheld`. Cardinality is the number of queues, bounded by
  what the controller reads.
- A new gauge `wva_kueue_variant_unbound{namespace, variant_name, reason}`,
  present only while a variant is unbound.
- Limiter status in `wva-controller-status` lists bindings per variant.

## Alternatives considered

- **Keep the targeted fixes.** #186 is safe: it never over-orders on a
  multi-queue namespace. It under-spends (scenario 1) and leaves the shared
  queue a ceiling (scenarios 2, 3). Fine where every tenant has its own
  ClusterQueue, which is what the well-lit path tells operators to do. This
  remains the answer if reading templates turns out unreliable.
- **Partition a shared queue by namespace weight.** Fixes scenario 2 without
  the label, but invents a split Kueue does not make: one namespace may use
  the whole queue legitimately.
- **Read the label from running pods, not templates.** Pods carry what Kueue
  saw, but a variant at zero replicas has none, and scale-from-zero needs the
  binding most.
- **Read the queue from Workload objects.** Exact for admitted replicas, and
  absent for the one about to be ordered.

## Staged rollout

1. **Read and report.** Resolve bindings; publish
   `wva_kueue_variant_unbound` and the status list. No decision changes. This
   shows operators which variants would become unbound before it bites.
2. **Per-queue charge and figures, shadowed.** Compute `QueueBudget` beside
   today's namespace figures; log where they differ per cycle.
3. **Spend per queue.** The optimizer, the wake and the share read queue
   entries. The namespace min and `multi-queue-namespace` go. Unbound
   variants are held with their reason. Gated by a quota-entry field
   `kueue.perQueue: true` for one release, default on in the next.
4. **Remove the per-namespace path** and the opt-out.

## Test strategy

- Unit tables for binding: label present, absent with a `default` LocalQueue,
  absent with one queue, absent with two, LocalQueue missing, LWS leader and
  worker disagreeing.
- One test per motivation scenario, in 8-GPU units, asserting the ordered
  replicas: 16+8 multi-queue serves two replicas; shared CQ-T orders one
  between A and B; 64-CPU queue fits one 48-CPU replica.
- The #186 regression tests stay, rewritten only where they assert the
  per-namespace min.
- The Kueue kind e2e suite gains a shared-queue case with two namespaces.

## Risks

- **Templates without labels.** Many installs rely on Kueue's
  `manageJobsWithoutQueueName` or a namespace default. Stage 1 exists to
  measure this before anything is held.
- **Order of namespaces in one pass.** A shared queue spent once means the
  first namespace in the pass can take it all. Today's fairness across models
  applies across namespaces too; it must be checked, not assumed.
- **More RBAC?** None: LocalQueues and pod templates are read already.

## Open questions

1. An unbound variant on a namespace with several GPU queues: hold it at zero
   Kueue growth, or keep today's per-namespace min for it as a fallback?
2. Should the binding follow Kueue's `LocalQueueDefaulting` only when the
   feature gate is known on, and how is that read without new RBAC?
3. Is a `cluster_queue` label on existing series acceptable, or should the
   per-queue figures be new series so dashboards keep working?
4. On a shared queue, should the pass split headroom between namespaces by
   model score (as today across models), or first come in namespace order?
5. Does this land before or after the [claims ledger](claims-ledger.md)? It
   is smaller, but its `QueueBudget` is cleaner in the Kueue package that
   proposal creates.

## Related

- [Kueue borrowing, and limiters that compose](kueue-borrowing-and-limiter-composition.md)
- [One claims ledger](claims-ledger.md)
- [Quota limiter reference](../reference/quota-limiter.md)
- [Kueue-bounded quotas well-lit path](../well-lit-paths/kueue-bounded-quotas/README.md)
- [Kueue borrowing (developer guide)](../developer-guide/kueue-borrowing.md)
