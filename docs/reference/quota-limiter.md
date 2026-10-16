# Quota Limiter

The **quota limiter** enforces GPU caps the operator declares, independent of
the physical inventory. Use it to cap accelerator use whatever nodes exist: to
reserve burst capacity, divide a cluster across tenants, or test a smaller
budget than the hardware allows.

This page covers its configuration, design and place in the pipeline. The
implementation closes
[#1002](https://github.com/llm-d/llm-d-workload-variant-autoscaler/issues/1002).

## Enabling

Add **a `limiters:` list with a `quota` entry** to the saturation-scaling
ConfigMap's `default` entry. That is the whole prerequisite, and the installer
writes it for you:

```bash
# One install's own policy
make deploy-wva-on-k8s WVA_LIMITER=quota WVA_QUOTAS='H200=8'
# The cluster policy every controller reads
make enable-physical-limiter WVA_LIMITER_TYPE=quota WVA_QUOTAS='H200=8'
```

- `WVA_QUOTAS` is **required** by both. An entry that names no accelerator is a
  budget of zero for every type, not unlimited, and stops every managed
  workload from scaling up.
- `WVA_QUOTA_SCOPE` is `namespace` (the default: each managed namespace gets the
  budget) or `cluster` (the sum across all of them).
- The list is the only thing that selects the quota limiter
  ([Selection & lifecycle](#selection--lifecycle)). It applies **live**, except
  where a namespace-scoped controller reads it from outside the namespace it
  manages ([Reload lifecycle](#reload-lifecycle)).
- With no `limiters:` list nothing limits: neither the optimizer budget nor the
  scale-from-zero capacity check.

> **Changed:** this used to also need `enableLimiter: true`. A `quota` entry
> without it was built but never applied, so replicas scaled unconstrained while
> the config looked capped. Declaring the limiter now enables it.

The entries use the schema below, inline under the saturation `default` entry's
`limiters:` list (see
[scaling-policy-config.md](scaling-policy.md#limiters-cluster-default-only-live)).

## Scope

The quota limiter is one of several limiters in the resource-limiting pipeline.
It is intentionally narrow:

- It does **not** discover physical inventory (that is `TypeInventory`'s job).
- It does **not** make scaling decisions (that is the optimizer's job).
- It does **not** compose itself with other limiters. Every declared limiter is
  built separately, and the optimizer intersects their constraints. See
  [Interaction with the physical limiter](#interaction-with-the-physical-limiter).

A `QuotaInventory` instance is tied to exactly one scope. Deployments that need
both cluster-wide and per-namespace caps configure two limiter entries; a
decision must fit both.

## Configuration

The limiter is declared via a ConfigMap parsed into `QuotaLimiterEntries`. The
top-level `limiters` key holds a list of entries; each entry has a `scope` of
either `cluster` or `namespace`.

> **Entry `type:`.** Within a quota entry the `type:` field must be `"quota"`.
> (The saturation `limiters:` list also accepts one `{type: gpu-inventory}`
> entry, which adds the physical limiter beside the quotas — see
> [Selection & lifecycle](#selection--lifecycle).) The field exists so future
> limiter types (e.g. `reservation`) can share this schema.

### Cluster scope

A cluster-scoped entry caps the total GPUs of a given accelerator type across
all namespaces. The `quotas` map keys are accelerator type names (e.g.,
`H100`); values are caps in GPUs.

```yaml
limiters:
  - name: cluster-quota
    type: quota
    scope: cluster
    quotas:
      H100: 16
      A100: 8
      L40S: -1  # unlimited
```

### Namespace scope

A namespace-scoped entry caps per-namespace consumption. The top-level keys of
`namespaceQuotas` are namespace names; inner keys are accelerator types.

```yaml
limiters:
  - name: namespace-quota
    type: quota
    scope: namespace
    exclude:
      - kube-system
      - llm-d-system
    namespaceQuotas:
      team-a:
        H100: 8
      team-priority:
        H100: -1   # unlimited for this tenant
      default:
        H100: 2    # per-namespace fallback for unlisted namespaces
```

Declared alone, this example logs a `WARNING`: nothing bounds the excluded
namespaces. A `gpu-inventory` limiter or a `scope: cluster` quota beside it
bounds them. If they run no models, you can ignore the warning.

> ⚠️ **Reserved key collision.** `default` in `namespaceQuotas` is the fallback
> for unlisted namespaces. So you cannot give the namespace called `default` its
> own quota. Prefer a strict allowlist with no `default` key: every unlisted
> namespace is then denied. If you exclude `default` instead, only the
> cluster bounds limit it.

### Special values

| Value | Meaning |
|-------|---------|
| Positive integer (up to `MaxQuotaValue` = 1,048,576) | Cap in GPUs |
| `-1` | Unlimited (Kubernetes convention; pass-through in the allocator) |
| `0` or missing entry | Denied (no allocation) |

A finite cap must not exceed `MaxQuotaValue` (`1 << 20` = 1,048,576); a larger
value is rejected at startup (see Validation).

### Namespace lookup rules

When the allocator evaluates a request for namespace `N` and type `T` in
`namespace` scope:

1. If `N` appears in `exclude`, this limiter does not apply to `N` and records
   no usage for it. Other limiters still apply.
   - Only the cluster bounds limit `N`: a `gpu-inventory` limiter and any
     `scope: cluster` quota. The tenants' quota does not: `N` does not use it,
     and a full tenant quota does not stop `N`.
   - A `scope: cluster` quota is a closed allowlist for `N`, as for everyone.
     A type it lists with a number bounds `N` at that number. A type it lists
     as `-1` does not bound `N`. A type it does not list is denied.
   - With no cluster bound, the scaling manager does not limit `N`. Without a
     `gpu-inventory` limiter, free GPUs are not checked: if the cluster is
     full, new pods stay `Pending`.
   - The controller logs a `WARNING` when `N` has no GPU limit on some type,
     and when an `exclude` entry is not a valid namespace name (it never
     matches). See [Validation](#validation).
2. If `N` is listed in `namespaceQuotas`, that namespace's **whole map is a
   closed allowlist**: `namespaceQuotas[N][T]` is the cap if present, and a type
   `T` that `N` does **not** list is **denied** (granted = 0). A listed namespace
   never falls through to `default` for a missing type — listing a namespace opts
   it into "only the types I name".
3. Otherwise (`N` is not listed), if `namespaceQuotas["default"]` is present, the
   **`default` map** becomes `N`'s per-namespace budget: each unlisted namespace
   gets its own copy of those caps (a type absent from `default` is still
   denied). This matches the Kubernetes `LimitRange` default semantic; it is
   *not* a shared pool across unlisted namespaces.
4. Otherwise, the request is denied (granted = 0).

The fall-through to `default` is therefore at the **namespace** granularity, not
per `(namespace, type)`: it applies only when `N` is wholly unlisted.

> ⚠️ **An excluded namespace can use GPUs your tenants expect.** Without a
> cluster bound it has no limit, and the scaling manager reserves nothing for
> the tenants. If tenants must keep their GPUs, use a Kubernetes
> `ResourceQuota` or Kueue: those are the real boundary.

<details>
<summary>Details: how an excluded namespace is sized</summary>

- The tenants and `N` share the cluster bounds. Nothing is reserved for a
  tenant. Models take turns by how much they still need. With
  `enableRescale: true` on the `default` entry, `N` is sized before the
  tenants when GPUs run short. So `N` can take the whole cluster quota, even
  while a tenant is below its cap and wants more.
- If a cluster bound is in force, a model in `N` whose pods name no GPU type
  does not scale up, though a wake from zero can still start it. Give its pods
  an accelerator selector. A `scope: cluster` quota that lists `unknown` is
  the exception: it bounds such a model at that number, and `-1` does not
  bound it.
- The utilization-share optimizer shares `N` only when a finite cluster bound
  covers its accelerator type
  ([scaling policy](scaling-policy.md#optimizer-cluster-default-only-live)).

This holds for every decision: scaling up, rescale, and waking from zero. The
limiter a decision names as `LimitedBy` reads the same bounds. GPU names match
by family and product, as everywhere else: a workload on `a100` or
`NVIDIA-A100-SXM4-80GB` spends a cluster quota's `A100`. A namespace that one
namespace quota excludes and another holds is bounded by the second, as a
tenant of it.

</details>

### Kueue as a quota source

The decision above these fields — when to take this, what it costs, the
evidence — is [the Kueue-bounded quotas path](../well-lit-paths/kueue-bounded-quotas/).

A quota entry can be **bounded by Kueue**, the cluster's admission-time quota
authority. The scaling manager then respects Kueue's caps before asking KEDA for
a replica that Kueue would hold pending. Set `kueue.enabled` on the entry:

```yaml
limiters:
  - name: namespace-quota
    type: quota
    scope: namespace
    kueue:
      enabled: true
      # resources: [nvidia.com/gpu]   # which extended resources count as GPUs;
      #                               # default: every vendor resource the scaling manager knows
      # refreshInterval: 30s          # how often Kueue is re-read (default 30s)
      # countBorrowable: false        # default true; see "Cohort borrowing" below
      # borrowNamespaces: [team-a]    # pilot; empty = every governed namespace
      # assumeTestedVersion: true     # default false; see kueue-version-unknown
    namespaceQuotas:                  # optional: the static caps below still apply
      team-a:
        H100: 8
```

**Both sources present → the smaller cap wins**, per namespace and accelerator
type. Neither source can raise what the other granted. With no static map at all
(`namespaceQuotas` / `quotas` omitted), Kueue's **typed** grants are the whole
answer; an untyped grant alone opens nothing, and the entry — an empty static
map — denies every type (see *Merge rules*, row 3).

#### What is read

| Kueue object | Scope | Used for |
|---|---|---|
| `ResourceFlavor` | cluster | `spec.nodeLabels` names the accelerator behind a flavor — the vendor product label (`nvidia.com/gpu.product`, `amd.com/gpu.product-name`, …, or the GKE alias), kept **as written**: a flavor exists to tell one product from another (a 40GB PCIe A100 from an 80GB SXM one), so the grant is keyed by the full product and meets a static key at bound time (see below). A flavor **without** a product label is an **untyped grant** — see the list below the table. |
| `ClusterQueue` | cluster | `spec.resourceGroups[].flavors[].resources[].nominalQuota` for each GPU resource, per flavor, summed per product (typed flavors) or into the untyped grant (unlabelled flavors). Two grants of the **same** product add up; two products of one family (SXM and PCIe H100) stay apart until the static key says they are one. At namespace scope, `borrowingLimit`, `lendingLimit`, cohorts and queue status add **cohort borrowing** on top of this grant where it is safe (see *Cohort borrowing*). |
| `LocalQueue` | namespaced | `spec.clusterQueue` links a namespace to a ClusterQueue. A namespace is granted the caps of every ClusterQueue one of its LocalQueues points at **and whose `spec.namespaceSelector` admits it**; two LocalQueues on one ClusterQueue count it once. |
| `Namespace` | cluster | Labels, read (`get`, one per namespace) only when a ClusterQueue has a non-empty `namespaceSelector`. |

The cluster-scope figure is the sum over **all** ClusterQueues. Six attribution
rules the table does not show:

- **A flavor without a product label is an untyped grant.** Kueue's quickstart
  `default-flavor`, or any single-GPU-type cluster. It is *not* named after the
  flavor: `default-flavor` or `h100-sxm` is not an accelerator type, and treating
  it as one would compete with every static key and zero them all.
- **An inactive ClusterQueue governs, and grants nothing.** A queue that names a
  flavor **not present** on the cluster, or whose `stopPolicy` is `Hold` or
  `HoldAndDrain`, admits nothing in Kueue; here it becomes an untyped grant of
  0, so its namespaces are denied rather than left to the static entry.
- **A ClusterQueue grants only the namespaces its `namespaceSelector` admits.**
  Kueue semantics: absent (null) admits **no** namespace, `{}` admits every
  namespace, anything else is matched against the namespace labels. A
  LocalQueue on a queue that does not admit its namespace is ignored, like a
  dangling one — a tenant cannot borrow another team's grant by pointing a
  LocalQueue at it. If the labels cannot be read (no `get` on namespaces),
  that queue's grant is withheld and the namespace, with no other grant, is
  bounded at nothing; the error is logged once with the remedy.
- **A shared ClusterQueue is a per-namespace ceiling, not a partition.** Every
  namespace with a LocalQueue on it is bounded at the queue's full nominal
  quota, so several such namespaces may in sum still ask for more than the
  queue admits and Kueue holds the excess pending. The bound removes the
  single-namespace over-ask; a shared budget stays Kueue's to arbitrate. Give
  each tenant its own ClusterQueue (cohorts for borrowing) where that matters.
- **Untyped grants carry no vendor.** Within one ClusterQueue the untyped
  figures of different GPU resources (`nvidia.com/gpu`, `amd.com/gpu`) are not
  summed; the largest is taken, the loosest bound no single resource
  contradicts. Pin `kueue.resources` to one vendor when a mixed queue needs the
  distinction.
- **An untyped grant bounds each static type separately, not their sum.** A
  namespace that names two types with `-1` and holds an untyped grant of 8 may
  reach 8 of *each*. The limiter has no per-namespace total; if the total is
  what must hold, give the flavors product labels so the grant is typed.

ClusterQueues, LocalQueues, ResourceFlavors and Cohorts are read unstructured,
straight to the API server, at most once per `refreshInterval`. Kueue
Workloads, which borrowing reads, come from a cluster-wide informer instead
([RBAC](#rbac)). The API version is whatever the cluster
serves (the fields used are the same in `v1beta1` and `v1beta2`); the project
has no Kueue module dependency.

#### Merge rules

Absence means different things at the two levels, and the rules follow from
that:

| Situation | Effective cap |
|---|---|
| Namespace listed in `namespaceQuotas` **and** governed by Kueue | per type, `min(static, kueue)` over the **union** of types; a static type Kueue does not name takes Kueue's **untyped** grant if there is one, else **0**; a Kueue type the static map does not name is **0** (both maps are closed allowlists) |
| Namespace governed by Kueue, unlisted, static `default` present | `min(default, kueue)`; the namespace becomes explicitly listed at that budget |
| Namespace governed by Kueue, unlisted, **no** static map at all | Kueue's **typed** caps, as is. An untyped grant alone names no type and so cannot open anything; the limiter logs it as unapplied — name the types in the entry (`default: {H100: -1}`) and Kueue supplies the figure |
| Namespace governed by Kueue, unlisted, strict static allowlist without `default` | **denied**, as before — Kueue cannot open a namespace the operator's list denies |
| Namespace **not** governed by Kueue | static treatment, unchanged |
| Namespace in `exclude` | bypasses both sources |
| Static `-1` (unlimited) | defers to Kueue's figure |

A namespace is *governed by Kueue* when one of its LocalQueues reaches a
ClusterQueue that **declares a GPU resource**. A LocalQueue on a CPU-only queue,
or on a queue that does not exist, leaves the namespace to the static entry.
Otherwise, enabling the reader on a cluster that uses Kueue for CPU batch jobs
would zero every inference namespace. A ClusterQueue declaring a GPU resource
with `nominalQuota: 0` is a real grant of nothing, and denies.

Kueue never produces the reserved `default` key. Quotas Kueue holds for the
Kubernetes namespace literally named `default` are skipped, for the same reason
that namespace cannot be configured statically (see the reserved-key note above).

Type keys are matched across the two maps by accelerator identity -- the same
name, the same name in another case, or one being the other's short name; two
different full product names are never equated -- and the **static spelling is
kept**. A short static key (`H100`) is bounded by the **sum** of every grant in
its family (PCIe + SXM); a full-product static key (`NVIDIA-H100-PCIE-80GB`)
only by its own grant, which is how a per-flavor Kueue split (PCIe 4, SXM 0) is
honoured. Usage keyed by a raw product label, or by a lower-case GKE value,
lands on the right pool whichever spelling the effective map carries.

**The common Kueue install has no product labels on its flavors.** Then all of
Kueue's grant is untyped. Name the types in the static entry and let Kueue give
the numbers:

```yaml
kueue: { enabled: true }
namespaceQuotas:
  default: { H100: -1 }      # every Kueue-governed namespace: H100 ≤ its Kueue grant
```

#### Failure posture

The effective entry is the static entry bounded by the last Kueue read. A
failed read never fails the quota: a limiter that cannot answer is skipped, so
an error from a Kueue outage would *lift* the declared cap. Instead:

- with a previous snapshot, it stays in force and the error is logged with the
  snapshot's age (the reader hands back its last good read);
- with none — Kueue not installed, RBAC missing, first read failing — the
  error is logged every cycle until a read succeeds. A Forbidden or a missing
  CRD is reported as such, never folded into "Kueue grants nothing". An entry
  that leaves a type at `-1` is then skipped as an unobserved provider
  (`wva_constraint_provider_skipped{reason="unobserved"}`, plus
  `wva_kueue_never_read` and the alert `WVAKueueNeverRead`): the other
  limiters still bound, and Kueue still gates pods past its quota. Otherwise
  the **static entry alone** applies.

With borrowing counted, a failed read after a good one is published too: the
reason `stale` on `wva_quota_borrow_withheld`, and the alert
`WVAKueueReaderStale`.

A change to the effective caps is logged once, at `Info`, with the Kueue figures
and the merged result, so the controller log answers "what is bounding this
namespace?".

#### Cohort borrowing

Kueue lets a ClusterQueue borrow unused quota from other queues in its cohort.
A namespace-scope entry with `kueue.enabled` **counts that borrowing by
default**: a model may grow past its namespace's guaranteed figure into
capacity its cohort lends, and Kueue takes it back for the lender. Borrowing
is experimental but on by default; `countBorrowable: false` on the entry (the
installer writes it for `WVA_QUOTA_KUEUE_BORROW=false`) caps at nominal
exactly as before. A cluster-scope entry is not affected: borrowing moves
capacity between queues and leaves the cluster total as it is.

The steps (prerequisites, enable, pilot, turn off) are in the guide
[let models borrow from a Kueue cohort](../guides/kueue-borrowing/), which
also defines the terms used here.

**When it is counted.** Each namespace shows one reason. When several apply,
the first in table order wins: withheld, then frozen, then counted but 0. The
reason is logged and published on `wva_quota_borrow_withheld`; what to do for
each is in [Borrowing is withheld](troubleshooting.md#borrowing-is-withheld).

**Withheld**: borrowable is 0, and the namespace is capped at its nominal
quota, exactly as with `countBorrowable: false`.

| Borrowing needs | Reason when it fails |
|---|---|
| The namespace is in the entry's `borrowNamespaces`, when that list is set | `not-selected` |
| Kueue Workloads are read cluster-wide, without error. Every install reads them cluster-wide; a namespace-scoped one once a cluster admin has re-run the admin phase with a policy that counts borrowing ([RBAC](#rbac)). A Forbidden or a failed list cannot see a lender wait | `lenders-unreadable` |
| Kueue charges every GPU pod in the namespace: a Workload owned by the pod, or by an owner up its chain, has quota reserved (for a pod at Kueue's admission gate, the Workload exists). The Workload must request a GPU and be owned by a kind a Kueue integration owns Workloads by: Pod, batch Job, JobSet, LeaderWorkerSet or StatefulSet; the reserved Workloads must request at least the GPUs their owners' running pods do, each Workload counted once and at the pod count Kueue admitted. GPU work of other integrations (Kubeflow, Ray, AppWrapper) reads as not managed, as do GPUs asked for only through pod-level resources or DRA claims, and a pod whose owner the controller may not read. Kueue's labels and gate alone are not enough, since a tenant can write them | `not-kueue-managed` |
| The namespace's ResourceQuotas are readable (Forbidden at once; any other failure after five refresh intervals of failing) | `resourcequota-unreadable` |
| Something checks that borrowed GPUs exist: a `gpu-inventory` limiter, or a GPU `ResourceQuota` whose `hard` is at most nominal plus `borrowingLimit` | `no-gpu-bound` |
| The Kueue controller's version is readable from the image tag of `kueue-controller-manager` (not a digest-only image). `kueue.assumeTestedVersion: true` lifts this one reason | `kueue-version-unknown` |
| The Kueue controller runs a [tested version](#supported-kueue-versions) | `kueue-version-untested` |
| The ClusterQueue is in a cohort | `no-cohort` |
| The queue admits: no `stopPolicy` `Hold` or `HoldAndDrain`, and every flavor it names exists | `inactive` |
| Its borrowing fields (`nominalQuota`, `borrowingLimit`, `lendingLimit`) parse | `malformed` |
| Every queue in the cohort has reservation status (`status.flavorsReservation`) | `no-status` |
| The cohort is flat, and its Cohort objects are readable | `hierarchical-cohort`, `hierarchy-unknown` |
| No queue in the cohort uses `status.effectiveQuotas` | `effective-quotas` |
| Kueue Fair Sharing is off | `fair-sharing` |
| No queue in the cohort declares its GPUs in a flavor with Topology-Aware Scheduling (`topologyName`) | `topology-aware` |
| No queue in the cohort names admission checks (`admissionChecks`, or `admissionChecksStrategy` as MultiKueue uses) | `admission-checks` |
| No queue in the cohort sets `preemption.borrowWithinCohort` other than `Never` | `borrow-within-cohort` |
| Where more than one flavor of the queue offers the GPU, every variant's pod template pins the flavor's `nodeLabels`. A queue with one GPU flavor needs no pin | `flavor-unpinned` |
| Every resource the GPU flavor's resource group covers (CPU, memory, ...) has a per-replica request in each variant's pod template | `co-resource-bound` |

**Frozen**: nothing new is borrowed, and the last figures are kept (0 when
there are none yet). Nothing borrowed is given back by the scaling manager. A
freeze never shrinks a running model.

| Borrowing needs | Reason when it fails |
|---|---|
| The Kueue read is fresh: the last read succeeded and is under twice `refreshInterval` old. A Cohort list that fails for a reason other than Forbidden reads as stale too, and so does one namespace's ResourceQuota read failing (other than Forbidden) for up to five refresh intervals | `stale` |
| The Kueue controller is up: its leader-election Lease's `renewTime` is under twice its `leaseDuration` old, or, with no Lease, the `kueue-controller-manager` Deployment has a ready replica. A Deployment or Lease never found, or Forbidden (no Role in the Kueue namespace), counts as down. Only Kueue's default Lease, `c1f6bfd2.kueue.x-k8s.io`, is read: a Kueue built with another leader-election ID always reads as down | `kueue-controller-down` |

**Counted, but 0 this cycle**: borrowing is on, and there is nothing to draw.

| Reason | Meaning |
|---|---|
| `lender-cannot-reclaim` | the cohort's only unused capacity belongs to lenders Kueue would not reclaim for (see *the shared pool*). The namespace stays counted, at its guaranteed figure |
| `lender-waiting` | a lender waits for capacity it lent: what is borrowed stays, and nothing new is borrowed in the cohort |
| `cohort-pool-empty` | the free pool holds no GPUs, after what is in flight and gated |
| `no-borrow-cap` | `borrowingLimit`, the static entry or the namespace ResourceQuota stops the namespace at guaranteed |
| `preemptions-unreadable` | Kueue's preemptions in the namespace could not be read this cycle, so none of its models grows on borrowed capacity |

Withholding stops new borrowing only; it evicts nothing. A withheld
namespace is capped at its nominal quota. One that Kueue reads as borrowing
right now (past its nominal) is frozen instead: it borrows nothing new, and
is capped at its guaranteed figure, so it does not grow into nominal a peer
has borrowed. Its cohort's lenders are still read, so their wait still stops
new borrowing.

A declared `gpu-inventory` limiter that is skipped in a cycle (no node read,
or its view not observed yet; `wva_constraint_provider_skipped`) checks
nothing that cycle, so no model grows on borrowed capacity until it bounds
again. Growth within the guaranteed figure, what is already borrowed, and
the give-back steps carry on.

**Not supported, and not detected**: Kueue's `waitForPodsReady` with
`blockAdmission: true`. Borrowing assumes it is off.

**Known risk: `waitForPodsReady` at all.** With it on, Kueue evicts a
Workload whose pods are not all Ready within its `timeout` (5 minutes by
default) and queues it again. A model server that loads weights for longer
than that is evicted on every start, borrowed or not. The scaling manager
does not detect it. Raise `waitForPodsReady.timeout` above your slowest
replica start, or leave the feature off.

**The three figures.** Where borrowing is counted, a namespace has:

- **guaranteed**: what it keeps whatever the cohort does. A queue that can
  reclaim (`reclaimWithinCohort: Any`) keeps its nominal quota; its growth up
  to nominal still uses the cohort's free pool. A queue that cannot keeps what
  it already uses or holds back from lending; nominal a peer holds stays with
  the peer (qualified as `nominal-lent-out`, not a withheld reason).
  Guaranteed is **per ClusterQueue**: namespaces that share a queue share it.
- **borrow cap**: how far past guaranteed it may go: nominal plus
  `borrowingLimit` (no limit when unset), bounded by the static entry and the
  namespace ResourceQuota.
- **the shared pool**: the cohort's free GPUs, one pool per cohort, flavor and
  GPU resource, spent once across every namespace in the cohort. It counts a
  lender's unused nominal only when Kueue would take it back for that lender:
  always under `reclaimWithinCohort: Any`; under `LowerPriority` only for a
  borrowing model whose own Workload priority (`spec.priority`, set by its
  `WorkloadPriorityClass` or `priorityClassName`) is strictly below the
  lender's, since Kueue reclaims only for a Workload that outranks the
  borrower; never under `Never`, and never when either priority is unknown.
  A borrowing queue counts at the highest `spec.priority` of its Workloads
  that hold reserved quota and request a GPU, the cautious side, since Kueue
  does not say which of them holds the borrowed GPUs. So a `LowerPriority`
  lender's capacity stays closed while such a queue borrows at a priority
  the lender does not outrank, even when the borrowed GPUs are held by a
  lower-priority Workload of that queue.
  One low-priority Workload elsewhere in the cohort opens nothing for the
  other models. A model held back only by such lending -- the GPUs are free,
  and no lender of them outranks it -- is limited by `lender-cannot-reclaim`,
  not by the pool. Borrowing what a
  lender cannot get back would hold its nominal until the borrower shrinks on
  its own. The borrowing namespaces' own queues are not lenders here: their
  unused nominal is theirs. What the borrowers hold is taken from the counted
  part, the cautious side, since Kueue does not say whose lending a borrowed
  GPU is. When the pool counts nothing only for this reason, the reason is
  `lender-cannot-reclaim`.

Kueue's [documented example](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/)
holds: team-a (`nominalQuota` 9, `borrowingLimit` 1) with an idle, reclaiming
team-b (`nominalQuota` 12) reaches 10.

**Growing back into lent nominal.** A queue whose lender can reclaim, and
whose nominal peers are borrowing, may grow back into that nominal although
no node has a free hole yet: Kueue preempts the borrowers' pods for it, and
that frees their nodes. Only the GPUs peers actually borrow on the queue's
flavor, and that Kueue would preempt for it, are placed without a hole. The
rest of its growth must fit a node like any replica. Unused nominal that
nobody borrows is no exemption: with a node down, growth into it is limited
by `node-fit` instead of being ordered into a pod that stays Pending. A
scale-from-zero wake is held to the same rule.

Kueue preempts by priority, not by node, so the pod it takes may free no
node. Growth placed without a hole therefore stays under `placementTimeout`:
still unplaced that long after it was ordered, it is released like a
borrowed replica. Only time after the order counts; a replica of the same
variant stuck since before it does not hurry the release. The release backs
the model off in every cohort its namespace's queues belong to, for
`borrowCooldown` (doubling on a repeat): it borrows nothing new there, and
its growth back into lent nominal must fit a node with a free hole.

**Co-resources.** Kueue admits a pod only when every resource its flavor's
resource group covers fits, not the GPU alone. Each covered resource is
computed the same way as the GPU, and the reach in replicas is the minimum
over them of free capacity divided by the variant's per-replica request.

**Where borrowed capacity is spent.** The optimizer spends guaranteed first,
then borrows one unit at a time (a replica, a P/D pair, or a LeaderWorkerSet
group) from one pool, within the cap. Each borrowed unit must also fit a node
of the pool's flavor, by the flavor's `nodeLabels` and tolerations and the pod
template's own `nodeSelector`, required node affinity and tolerations. A unit
the pool holds and no such node does is limited by `node-fit`, as any replica
is ([A replica must fit one node](gpu-limiter.md#a-replica-must-fit-one-node)),
and a borrowed replica ordered keeps its hole on the flavor's nodes until its
pods hold a node, for up to 5 minutes, like any other. What Kueue
has not reserved yet is taken out of the pool first: growth ordered and not yet
admitted, and pods held at the admission gate in **every** namespace of the
cohort's ClusterQueues, so a second controller's ask is seen. Still, run one
scaling manager per cohort, or set `countBorrowable: false` on the Kueue
entries of all but one: two that borrow at once can count the same free GPU.

- **A P/D pair borrows only whole**, both roles on borrowed capacity. Kueue
  does not admit a P/D pair atomically: each role is its own Workload, and
  Kueue may admit or preempt one without the other. A reclaimed decode would
  leave a guaranteed prefill serving nothing, so a pair never straddles
  guaranteed and borrowed capacity. A half left unplaced is given back by the
  placement timeout.
- **Scale-from-zero, the warm pool and rescale** use the namespace's
  **within-nominal** figure, what Kueue would admit now without borrowing:
  never borrowed capacity, and not guaranteed alone either (under Kueue's
  default `Never`, an idle queue's guaranteed is 0). A wake blocked because
  the queue's nominal is lent to a peer that need not give it back reports
  `nominal-lent-out` (on `wva_model_scaling_blocked`) instead of waking into a
  gated pod.
- **The utilization-share optimizer** keeps borrowed GPUs a group already
  holds, as no debt, and starts no new borrowing.

**Giving it back.** Kueue preempts borrowers for a lender that can reclaim,
and only such a lender's capacity is counted. The scaling manager lowers a
borrowing model itself only where a borrowed unit never placed:

- **Placement timeout** (always on). A borrowed replica still unschedulable, or
  held at Kueue's admission gate, after `placementTimeout` is released (with
  its P/D partner). Only those two states count: a pod that has a node and is
  still loading its weights is placed, however long it takes to become
  Ready. The wait stretches up to three times while the cluster autoscaler
  reports `TriggeredScaleUp` on the pod. Until it places, the variant borrows
  nothing new. Each unplaced replica is timed on its own, from when it became
  unplaced or from when its namespace began borrowing (after a restart: from
  the restart), whichever is later: a replica stuck before the namespace
  borrowed does not time out a fresh borrowed one, and new unplaced replicas
  do not reset the oldest one's clock. A LeaderWorkerSet group is one replica,
  timed from when its newest unplaced pod became unplaced.
- **A waiting lender.** A lender **waits** while a pending Workload in its
  ClusterQueue (no quota reserved, queued through one of its LocalQueues)
  requests a borrowed resource on a flavor its `nodeSelector` and affinity
  allow, and fits within the nominal it does not hold. Where the allowed
  flavors cannot be told, it counts on all of them. A CPU job, or one too
  large to admit, does not count. The **first sighting** stops new borrowing
  in the cohort (reason `lender-waiting`); it stays stopped while the lender
  waits and for `borrowCooldown` after. What is borrowed stays until Kueue
  preempts it: the scaling manager never hands borrowed replicas back to a
  lender itself. The wait is published once it has lasted `lenderDebounce`
  over two fresh reads (`wva_kueue_lender_waiting_seconds`, alert
  `WVAKueueLenderWaitingPastCooldown`).
- **Backoff.** After Kueue preempts one of a model's pods, or a borrowed unit
  of it is released at the placement timeout, the model borrows nothing new
  in that cohort for `borrowCooldown`. A repeat during the backoff doubles it,
  up to four times `borrowCooldown` (40 minutes at the default); one clean
  cooldown resets it. A preemption is seen only while the preempted pod
  (Kueue's `TerminationTarget` condition) or its Workload still exists, so
  give serving pods a `terminationGracePeriodSeconds` of at least one engine
  cycle; a pod gone between two cycles is missed.
- **The ledger.** The cooldowns and the placement-timeout releases in flight
  are kept under `borrowReleaseLedger` in the `wva-controller-status`
  ConfigMap, so a restart neither releases twice nor forgets a cooldown.
  Figures or entry counts past sane bounds are dropped on load.
- **A frozen read freezes everything** (`stale`, `kueue-controller-down`):
  nothing new is borrowed and nothing is given back until reads recover.
- **A failed ResourceQuota read freezes one namespace.** When one namespace's
  ResourceQuotas cannot be read (other than Forbidden), only that namespace
  freezes: it keeps its last figures, borrows nothing new (reason `stale`),
  and its placement-timeout releases still apply. What it already borrows
  still counts against the cohort's pool, so no other namespace can claim it.
  After five refresh intervals of failing it is withheld as
  `resourcequota-unreadable` and capped at nominal, until a read passes.

Counting the capacity of lenders that do not reclaim, and handing it back to
them when they wait, is not in this release; it is planned as a later, opt-in
change.

**Settings**, on the entry's `kueue:` block:

| Key | Default | What it does |
|---|---|---|
| `countBorrowable` | `true` | `false` caps every namespace at nominal, as before borrowing was counted |
| `borrowNamespaces` | empty (all) | a pilot: borrowing is counted only in the listed namespaces; every other namespace is capped at nominal, withheld as `not-selected`, and costs no eligibility read |
| `placementTimeout` | `5m` | how long a borrowed replica may stay unplaced; also growth back into lent-out nominal placed without a node hole, timed from its order. A release starts the model's cooldown |
| `borrowCooldown` | `10m` | how long a cohort borrows nothing new after a lender waited, and a model after a preemption or a placement-timeout release; doubled on a repeat, up to four times. Published on `wva_kueue_borrow_cooldown_seconds` |
| `lenderDebounce` | `60s` | how long a lender must read as waiting, over at least two fresh reads, before its wait is published and alerted on. New borrowing stops at the first sighting regardless |
| `controllerNamespace` | `kueue-system` | where the `kueue-controller-manager` Deployment and its leader Lease are read, for the Kueue version and whether Kueue runs |
| `assumeTestedVersion` | `false` | `true` counts a Kueue whose version cannot be read as tested, lifting `kueue-version-unknown`. It does not lift `kueue-controller-down` |

**Two entries over one pool.** When two Kueue entries govern namespaces in the
same cohort and only one sets `borrowNamespaces`, the controller logs the
warning `borrow-namespaces-split`. Nothing is rejected or withheld; set the
list on both entries, or on neither.

##### Supported Kueue versions

Borrowing reads Kueue fields and conditions that were checked against these
releases. Outside them, borrowing is withheld (`kueue-version-untested`); the
nominal quotas still apply.

| Kueue | Borrowing |
|---|---|
| v0.20.x | counted. The `e2e-tests-kueue-borrow` CI job runs v0.20.0 |
| v0.19.x | counted. Its fields were checked against the source; no CI job runs it |
| anything else | withheld: `kueue-version-untested` |
| a version that cannot be read | withheld: `kueue-version-unknown`, unless `assumeTestedVersion: true` |

**What you see.** On each change, one `Info` line per entry:

```
Kueue borrowing applied where every condition holds and withheld elsewhere; guaranteed is the namespace cap, borrowable is drawn from the cohort's free pool
```

It lists, for the namespaces whose figures changed since the last line, each
queue row (`guaranteed`, `borrowable`, Kueue's own `kueueBorrowed` as a
cross-check, `reason`), the applied figures and the withheld reasons; at most
50 namespaces, with `omittedNamespaces` counting the rest and
`removedNamespaces` naming those that dropped out. Each model's
`scaling-decision` line carries `borrowed`, the GPUs it holds past
guaranteed. The same figures are metrics and the `limiterBudgets` status key
([Prometheus](prometheus.md#kueue-borrowing-metrics)). When replicas drop,
start at [replicas dropped](troubleshooting.md#replicas-dropped-under-kueue-borrowing).
The design, with the formulas, is the Kueue borrowing and limiter
composition proposal (in #161); the internals are on
[Kueue borrowing internals](../developer-guide/kueue-borrowing.md).

#### RBAC

The manager ClusterRole of a cluster-scoped install carries `get`/`list` on
`clusterqueues`, `cohorts`, `localqueues` and `resourceflavors`, and
`get`/`list`/`watch` on `workloads` in `kueue.x-k8s.io`, and reads `events`
in every namespace. Workloads are read cluster-wide through an informer,
indexed by owner, which starts on the first borrowing check (a cluster
without Kueue starts none). Until it has synced, or if `watch` is denied,
every check lists Workloads from the API server instead. A Workload read
revoked while the controller runs is noticed within about 5 minutes; until
then the cached Workloads are used, and then borrowing is withheld as
`lenders-unreadable`. Namespace labels, for
a `namespaceSelector`, are read with one `list` of Namespaces per Kueue read;
where that is denied, with one `get` per namespace.

A namespace-scoped install reads `localqueues` in the namespace it manages
through its Role (`wva-manager-role`), and the cluster-scoped kinds
(`clusterqueues`, `cohorts`, `resourceflavors`), plus `get` on its managed
Namespace, from `components/kueue-reader`. Borrowing also needs, from the
same prereqs phase:

- `get`/`list`/`watch` on `workloads` and `get`/`list` on `localqueues` in
  every namespace: a lender's pending Workload can be in any namespace. This
  is its own ClusterRole, `wva-kueue-borrow-reader-role-<hash>`, which the
  installer grants only while a limiter policy of the install counts
  borrowing, and revokes when none does. Without it borrowing is withheld as
  `lenders-unreadable`;
- a Role in the Kueue namespace (`KUEUE_NAMESPACE`, else the entry's
  `kueue.controllerNamespace`, else `kueue-system`)
  to `get` the `kueue-controller-manager` Deployment and its leader-election
  Lease (`coordination.k8s.io`). Without it the controller cannot tell Kueue
  runs, and borrowing freezes as `kueue-controller-down`.

The prereqs phase applies these, so an install that predates them needs a
cluster admin to re-run it, with the variables the install used: it reads the
install's policy to decide the Workload read. Where Kueue runs elsewhere than
`kueue-system` and the entry names no `controllerNamespace`, name it:

```bash
KUEUE_NAMESPACE=<kueue-ns> NAMESPACE=<namespace> make setup-prereqs
```

The tenant's `make deploy-wva` does not apply Roles. A rule naming an API
group the cluster does not serve is inert, so the grant is harmless without
Kueue. The Workload read is cluster-wide, pod templates included: see
[the security model](security-model.md#kueue-borrowing). An install that must
not read other namespaces cannot count borrowing safely; set
`countBorrowable: false`, and a re-run of the admin phase revokes the read.

To publish a Kueue-backed quota and grant the same reader to each target
controller (`WVA_LIMITER_TYPE` defaults to `gpu-inventory`, so name `quota`):

```bash
make enable-physical-limiter WVA_LIMITER_TYPE=quota WVA_QUOTAS='<type>=-1' WVA_QUOTA_KUEUE=true
```

`<type>` is the accelerator name the controller logs.

- A namespace-scoped controller, tenant-owned or out of reach, gets `get` on
  the namespace it manages, by name: the same grant `make setup-prereqs`
  renders, never fewer names than its role already holds.
- A cluster-scoped controller the installer made already reads every Namespace
  through its manager ClusterRole. Nothing needs naming in
  `WVA_LIMITER_TARGETS`.
- The command warns only when a controller that looks cluster-scoped lacks that
  read (naming the `make setup-prereqs` run for it), or when it cannot tell (it
  may not impersonate the controller).

Borrowing also reads, in the managed namespace: `resourcequotas` (the GPU
bound), `events` (the cluster autoscaler's `TriggeredScaleUp`), and the owners
of GPU pods (`replicasets`, `statefulsets`, `jobs`, `leaderworkersets`) for the
managed check. A missing permission shows as the reason of the read it blocks,
never as "nothing there":

| Read denied | Reason |
|---|---|
| Workloads outside the managed namespace | `lenders-unreadable` |
| A GPU pod's owner kind | `not-kueue-managed` |
| `resourcequotas` in the namespace | `resourcequota-unreadable` |
| Cohorts | `hierarchy-unknown` |
| The Kueue Deployment or its Lease | `kueue-controller-down` |

The nominal cap is unaffected by all of them.

### Validation

The policy entry is rejected for any of:

- Empty or duplicate `name`.
- `type` other than `"quota"`.
- `scope` outside `{cluster, namespace}`.
- `namespaceQuotas` or `exclude` set on a cluster-scoped entry.
- `quotas` set on a namespace-scoped entry.
- Negative quota values other than `-1`.
- A quota value above 1,048,576.
- Empty accelerator type or namespace key.
- In a `kueue:` block, an empty resource name or a `refreshInterval` that is not
  a positive duration; a `kueue:` block on a `gpu-inventory` entry.

A namespace in both `exclude` and `namespaceQuotas` gives a non-fatal
**warning**: `exclude` wins, and the ConfigMap is still accepted.

The controller logs these warnings as Info-level lines that start with
`WARNING:`. They come from the ConfigMap the limiters are read from, at
startup and whenever it changes. The ConfigMap is still accepted. The
`entry[N]` index counts quota entries only. Use the quoted name to find the
entry.

- A namespace in both `exclude` and `namespaceQuotas`:

  ```text
  WARNING: entry[0] ("tenants"): namespace "team-a" is in both exclude and namespaceQuotas; the quota entry will be ignored (exclude wins)
  ```

- An `exclude` entry that can never match a namespace:

  ```text
  WARNING: entry[0] ("tenants"): exclude entry "NS-X" is not a valid namespace name (check for spaces or capitals); it never matches a namespace
  ```

- An excluded namespace with no limit at all: no `scope: cluster` quota, no
  `gpu-inventory` limiter, and no other namespace quota holds it:

  ```text
  WARNING: entry[0] ("tenants"): the namespaces in exclude [ns-x] have no GPU limit: no scope: cluster quota and no gpu-inventory limiter are declared, so the scaling manager does not bound them
  ```

- An excluded namespace with no limit on some types:

  ```text
  WARNING: entry[0] ("tenants"): the namespaces in exclude [ns-x] have no GPU limit on [H100 (bare name), other H100 products]: no gpu-inventory limiter and no scope: cluster quota gives those accelerator types a number (-1 is unlimited)
  ```

- An excluded namespace that another namespace quota holds with `-1`:

  ```text
  WARNING: entry[0] ("tenants"): the namespaces in exclude [ns-x] have no GPU limit on [A100]: the namespace quotas that hold them ("others") set those accelerator types to -1 (unlimited), and no gpu-inventory limiter and no scope: cluster quota gives them a number
  ```

How to read the type list:

- `H100`: every H100 product, and a workload that names only `H100`.
- `H100 (bare name)`: only a workload whose selector names `H100` itself.
- `other H100 products`: the H100 products that no quota lists.
- `unknown`: a workload that names no GPU type.

To fix one, give the named types a number in a `scope: cluster` quota, or
declare a `gpu-inventory` limiter.

<details>
<summary>Details: which types the warnings check, and what they do not report</summary>

The warnings read the configuration the way the planner reads it, with the
same name matching. They check these types: every accelerator name any quota
entry lists, the family of each (`H100` for `NVIDIA-H100-80GB-HBM3`), one
stand-in for the products of each family that no entry lists, and `unknown`.
Any other type is outside every family a quota lists, so it is denied.

- A family key bounds the products a quota does not list. With
  `{H100: -1, NVIDIA-H100-PCIE-80GB: 4}`, only the PCIe product has a number.
  The other H100 products fall back to `H100: -1`.
- A family that a quota lists only through products is bounded by their sum,
  and is unlimited when one of them is `-1`.
- A type a `scope: cluster` quota does not list is denied, so it is not
  named. `unknown` is the exception: a cluster quota that does not list it
  does not bound it. So `unknown` is named when a cluster quota lists it as
  `-1` and none gives it a number.
- A `gpu-inventory` limiter gives every named type a number. With one
  declared, only `unknown` can be named.
- A namespace another namespace quota holds is bounded by that quota's caps
  for it, through its own entry or `default`. It is named only for types every
  such quota sets to `-1`.

Not reported:

- anything, while a `scope: cluster` quota reads Kueue: its numbers come from
  Kueue at runtime;
- a namespace held by a namespace quota that reads Kueue;
- an `exclude` entry that is not a valid namespace name, beyond the warning
  above: it never matches a namespace;
- a `gpu-inventory` limiter the controller may not use because it cannot list
  nodes: the warnings read the configuration only.

</details>

### Reload lifecycle

The quota configuration lives in the saturation-scaling ConfigMap and applies
**live**. The saturation engine rebuilds the GPU limiter at the start of the
next cycle whenever the declared limiters or quota entries change, with no
restart, wherever the controller watches that ConfigMap.

A namespace-scoped controller watches only the namespace it manages. A policy in
`wva-policy`, a labelled policy namespace, or an out-of-reach install's own
namespace is read at startup and needs a restart;
`make enable-physical-limiter` and `make disable-limiter` do it.

## What a quota is charged

A quota supplies limits only; the optimizer decides within them. An entry with
`kueue.enabled` re-reads Kueue at most once per `refreshInterval` (see
[Kueue as a quota source](#kueue-as-a-quota-source)).

The usage charged to a quota counts only what the scaling manager's own
variants hold, not every GPU-holding pod on the nodes, which is what a physical
limiter is charged. See
[GPU capacity accounting](../concepts/gpu-capacity-accounting.md) for the two
measures.

This matters. A quota is an allowance for the scaling manager alone. Charged the
physical figure, a namespace with a 4-GPU quota next to an unrelated 4-GPU
training job would read as fully spent while the scaling manager had placed
nothing, and every scale-up would be refused. Whether the hardware is full is the
physical limiter's question.

So a quota does **not** bound the cluster: it can hand out capacity unmanaged
workloads already took. For both guarantees, compose a physical limiter and a
quota limiter; each is fed the measure it asks about.

A cluster-scope quota bounds the whole cluster, one cap per listed type. A
namespace-scope quota bounds **only the namespaces it lists**, each by its own
caps; a namespace it does not cover is not charged against it. `-1` is
unlimited; `0` is a real cap of nothing, which denies the type.

## How the bound is applied

Every scaling decision, for every model, is judged against the quota with
closed-allowlist semantics:

- A **cluster-scope** quota caps each type it lists, cluster-wide.
- A **namespace-scope** quota makes each non-excluded active namespace a
  **closed allowlist**: a model may scale onto a type only if its namespace
  lists it. For a listed type, the bound is the smaller of the cluster bound
  and that namespace's cap, spent as the optimizer allocates.
- A type the namespace does **not** list is **denied**, so one namespace can
  never draw on another's quota (no cross-namespace leak). An **unlimited**
  (`-1`) per-namespace entry bounds the model only by the cluster bounds for
  that type: the physical limiter or a cluster-scope quota, and nothing when
  there are none. An **excluded** namespace is not covered by the quota at
  all: it too is bounded only by the cluster bounds, never by the sum of the
  other tenants' caps. Every GPU a tenant or an excluded namespace takes is
  charged to the cluster bounds, so they share them.
- Budgets follow the GPU **family and product** names. A family key such as
  `H100` bounds the sum of its products (`NVIDIA-H100-80GB-HBM3`, ...), and a
  GPU spent under one spelling is spent under the other.
  - An allowlist that names the family **and** some products,
    `{H100: 8, NVIDIA-H100-80GB-HBM3: 4}`, admits every other H100 product up
    to the family's 8, and the named product up to its own 4.
  - An allowlist that names **only products** denies the products it does not
    name: `{NVIDIA-H100-80GB-HBM3: 4, NVIDIA-H100-SXM5-80GB: 4}` admits no
    `NVIDIA-H100-PCIE-80GB`.
  - A workload whose accelerator is the bare family (`H100`) is bounded by the
    family's total across the products it may use, and spends from that total.
  - Several roles or wakes on products of one family must fit the family's
    figure **together**: a decode on `NVIDIA-H100-80GB-HBM3` (8) and a prefill on
    `NVIDIA-H100-PCIE-80GB` (8) do not fit an H100 cap of 12 left.
- A workload with **no accelerator selector** can land on any GPU. In a
  namespace with a closed allowlist it is allowed only when the allowlist lists
  `unknown` or exactly one type; otherwise there is no telling which listed
  type it would spend.
- **Several quota entries** all apply: a decision must fit each of them.

Every namespace being decided about is bounded, so a namespace carrying a quota
but currently running zero replicas is still constrained (its `default`
fall-through and `exclude` rules apply).
A namespace with neither an explicit quota nor a `default` fall-through is a
real **deny-all**: it is present in `NamespacePools` with no listed types, and
the optimizer allocates it nothing.

Remaining boundaries:

- A physical limiter declared beside the quotas is a separate constraint set.
  The optimizer intersects them: a type the physical inventory does not list is
  denied, and the budget is the smaller figure.
- A provider that cannot be consulted this cycle (its usage is not observed yet,
  or it errors) is skipped; the others still bound. The skip is logged when it
  starts and ends, and `wva_constraint_provider_skipped` is 1 while it lasts.
  Only when every provider is unavailable is a decision unchecked.
- A namespace allowance that is **unlimited (`-1`) for every type**, with
  nothing else bounding the cluster, scales up to demand.
- An **unlimited (`-1`) cluster-scope** quota scales up too: the type
  allocates up to demand.

### Fair-share interaction

Quotas are **hard ceilings applied on top of** the optimizer's fair-share loop,
not inputs to it. Two properties follow from that:

- The fair-share mean each round is the **average of the active models' remaining
  fair-share metric** (priority × score × unmet demand — see the worked-example
  caveat below) — it is **not** `cluster GPUs / number of active models`, and
  **not** quota-weighted. The cluster GPU total is only the loop's stop condition
  (fair-sharing halts once the aggregate budget is exhausted). Quotas only clamp
  each model after the mean is computed.
- Fairness is **per-model**, not per-namespace. Two models in one namespace each
  get their own fair-share slot; the namespace quota bounds each of them (and
  their running sum) but does not pool one model's allowance for the other.

> **Ceilings, not reservations.** A finite per-namespace cap guarantees a tenant
> will never *exceed* it — it does **not** guarantee the tenant can *reach* it.
> The GPUs it would use may be taken first by another namespace: an
> **unlimited (`-1`)** or **excluded** one is bounded only by the cluster bounds,
> and two utilization-share groups on one GPU type split the free cluster GPUs
> between them. Nobody exceeds their own authorization, but a cap is not a
> floor. If you need it to behave like one, avoid mixing unlimited or excluded
> namespaces on the same type, and give every type a finite cluster-scope cap.
> The utilization-share optimizer shares an excluded namespace only when a
> finite cluster bound covers its GPU type: a `scope: cluster` quota, or a
> `gpu-inventory` limiter with `physicalGroups: true`. Otherwise its models
> stay on the default optimizer.

When a model is capped below its fair-share slot it takes only up to its quota;
the unused remainder is **released to subsequent rounds**, where the
still-hungry models split it — it is not handed to other models within the same
round.

Worked example — cluster of 8 GPUs, three models:

| Model | Wants | Quota |
|-------|-------|-------|
| M1    | 3     | 2     |
| M2    | 4     | 4     |
| M3    | 4     | 4     |

- **Round 1:** the mean is the average of the models' remaining demand,
  ≈ (3 + 4 + 4) / 3 ≈ 3.67. M1 is capped at its quota of 2 (below its demand of 3),
  so part of its fair-share slot goes unused; M2 and M3 pull toward the mean under
  their own quotas.
- **Round 2:** with M1 satisfied at 2, the remaining cluster budget (8 − 2 = 6) is
  split between the still-hungry M2 and M3.

Final allocation: **M1 = 2, M2 ≈ 3, M3 ≈ 3** (total 8). The outcome respects every
quota and exhausts the cluster budget; the multi-round path is why a
quota-constrained model's slack flows to later rounds rather than to its
round-mates. (The exact per-round means come from the fair-share metric —
priority × score × demand — so treat the numbers here as an illustration of the
*path*, not an exact trace.)

## Interaction with the physical limiter

Declare both and both apply:

- the physical limiter (`gpu-inventory`) knows what **exists**: every GPU on the
  nodes, minus every GPU-holding pod, training jobs included;
- a quota knows what is **allowed**: it counts only the scaling manager's own pods.

A decision must fit both. The budget per GPU type is the smaller of the two, and
a type either one denies is denied. Budgets follow the GPU family and product
names: `H100` bounds the sum of its products, such as `NVIDIA-H100-80GB-HBM3`,
and a GPU spent under either name is spent under both.

With the physical limiter, a replica must also fit **one node**: free GPUs
scattered as 4+4+8 hold one 8-GPU replica, not two, and a P/D model grows only
when both of its roles find a node. See
[A replica must fit one node](gpu-limiter.md#a-replica-must-fit-one-node).

Declared alone, a quota does **not** check physical capacity: if it permits more
than exists, the extra replicas stay `Pending`. Add a `gpu-inventory` entry to
bound by real GPUs, and by nodes, as well.

**Upgrading.** Before this release a policy that declared both was bounded by the
quota alone; the physical entry was never built. It is now bounded by both, which
can be tighter when other workloads hold GPUs. For one release the controller
logs `Upgrade note (transitional)`, at most once every 10 cycles, naming each
variant whose target the quota alone would have set differently. A controller
that may not list nodes keeps the quota alone and stays Ready, reporting the
skipped physical limiter; see [the upgrade notes](upgrade-and-uninstall.md#limiters-and-kueue-borrowing)
for the check to run first.

## Selection & lifecycle

The `limiters:` list on the saturation-scaling ConfigMap's **`default`** entry is
the sole source, honored only at that cluster-scope entry (like `enableRescale`).
Every entry in it is built:

| `limiters:` on `default` | Behavior |
|--------------------------|----------|
| absent or empty | No limiter: scaling is bounded only by each workload's `maxReplicaCount`. This is what ships. |
| one `{type: gpu-inventory}` entry | Physical GPUs, discovered via the GPU operator. |
| `{type: quota, ...}` entries | Operator-declared quotas. |
| both | Physical GPUs **and** every quota: a decision must fit all of them. |

**`gpu-inventory` and `inventory`** are two spellings of the one physical
limiter: the policy accepts either. The controller's logs and its
`wva-controller-status` call the kind `inventory`, and an unnamed physical entry
is named `gpu-inventory`: `inventory+quota/gpu-inventory,tenant-quota`.

One physical limiter is built. A second `gpu-inventory` (or `inventory`) entry
is ignored and logged (`Ignoring physical (gpu-inventory) limiters entries after
the first`); it does not reject the policy. The line is logged at startup, and
for the ConfigMap the limiters are read from. A namespace-local ConfigMap's
limiters are ignored whole, so it logs none.

Edits apply without a restart: both engines rebuild the limiters when the list
changes. A limiter that cannot be consulted in a cycle is skipped and the others
still bound; `wva_constraint_provider_skipped` says which, and why.

How the set is read, built and rebuilt in the code is in
[How the limiter set is built](../developer-guide/limiter-composition.md).

### Example configuration

Add a `quota` entry to the saturation `default` entry's `limiters:` list. Add a
`gpu-inventory` entry beside it to bound by real GPUs as well:

```yaml
# saturation-scaling ConfigMap, data."default":
default: |
  analyzers:
    - type: saturation
  limiters:                    # declaring them is what enforces them
    - type: gpu-inventory
      name: gpu
    - type: quota
      name: cluster-h100
      scope: cluster
      quotas: { H100: 32 }
```

Inline quota entries are validated when the ConfigMap is read: each `quota`
entry against the rules in [Validation](#validation) (name uniqueness, scope,
per-type ranges), and a `gpu-inventory` entry must carry no quota fields.
Invalid entries are skipped with an error log and metric, exactly like any
other invalid saturation entry.

## Resource access with quotas only

Quota entries alone read no nodes. When no `gpu-inventory` entry is declared:

- The limiters never list nodes or pods: a quota's usage is what the scaling
  manager's own variants hold. (With `kueue.enabled` it lists Kueue objects,
  which is Kueue API traffic, not Node API traffic.)
- The cluster inventory log that `WVA_LIMITED_MODE=true` turns on is skipped
  too; the controller logs at startup that it is suppressed.

Combination matrix — **limiter-path** Node API access:

| Declared limiters | `WVA_LIMITED_MODE` | Node API access from the limiter? |
|-------------------|--------------------|------------------|
| `gpu-inventory`, with or without quotas | `false` (default) | Yes, every cycle. |
| `gpu-inventory`, with or without quotas | `true` | Yes, every cycle, and for the inventory log. |
| quotas only | `false` | **No.** |
| quotas only | `true` | **No.** The inventory log is skipped; a startup log notes the suppression. |

The periodic observation of cluster GPU usage honours this too: it runs only
when a physical limiter is declared.

So a quota-only deployment runs no periodic observation and lists no nodes or
pods for one. The gate is re-checked every tick, because the limiter set
reloads live.

> **One caveat on "no Node API access".** The gate covers the *timer*, not
> on-demand reads. The scale-from-zero engine observes usage at the moment it
> decides a wake, which is why its capacity check keeps working with the timer
> off. It only asks when a physical limiter is declared, so quotas alone never
> trigger it.

How the quota inventory is fed and read in the code is in
[How the limiter set is built](../developer-guide/limiter-composition.md#the-quota-inventory).

## Future work

- **Kueue borrowing follow-ups** — nested cohorts, Fair Sharing,
  `effectiveQuotas`, new borrowing under the utilization-share optimizer, and
  counting the capacity of lenders that do not reclaim (the proposal,
  `docs/proposals/kueue-borrowing-and-limiter-composition.md`).
- **Reservation-style limiters** — the `type: "quota"` discriminator leaves
  room for additional limiter types (e.g., `reservation`, `priority`) under
  the same ConfigMap schema.
