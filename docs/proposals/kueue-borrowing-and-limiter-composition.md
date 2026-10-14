# Kueue borrowing, and limiters that compose

**Status:** proposed. Fixes
[#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136) and closes
the in-repo part of #1003 (bounding by `min(physical, quota)`). Nothing here is
built.

## The problem in two sentences

The Kueue quota source bounds a namespace by its ClusterQueue's `nominalQuota`
and ignores cohort borrowing. When idle capacity sits in a lender's queue, Kueue
would admit more replicas, but the scaling manager never asks for them (#136).

Counting borrowed capacity raises a quota above the guaranteed figure. That is
safe only if something else still bounds the decision by real GPUs. Today
nothing does: a `quota` entry and a `gpu-inventory` entry cannot both be in
force, and two quota entries already combine wrongly. So the fix for #136 has
to start with the limiters composing correctly.

## Can all limiters work together today?

No. Every row below was checked against `main` (39b9d0c8). The probe results
come from a throwaway test that called `FitsGPUBudget`; it was not committed.

| Combination | Works? | What happens |
|---|---|---|
| `gpu-inventory` alone | yes | |
| one `quota` entry (cluster or namespace scope) | yes | |
| `quota` entry with `kueue.enabled` | yes | `min(static, kueue)` inside the entry (`config.BoundBy`) |
| `gpu-inventory` + `quota` | **no** | The quota wins and the physical limiter is never built (`config.limiterModeOf`). There is only a startup warning (`cmd/main.go`). |
| two or more `quota` entries that name **different** types | **no** | The allowed types are the **union** of the entries' lists, not the intersection. See below. |
| Kueue borrowing | **no** | Not read (#136). |

### The union leak (measured)

A quota denies every type it does not list. `mergeConstraints` and
`mergeNamespaceConstraints` take the minimum budget across providers, but only
for the types a provider *does* list. A type that one provider omits passes if
any other provider lists it:

| Constraints | Demand | `FitsGPUBudget` |
|---|---|---|
| cluster quota `{H100: 8}` alone | A100 = 2 | `false` (correct) |
| that quota + a provider listing `{H100: 16, A100: 4}` | A100 = 2 | **`true`** |
| namespace quota `x: {H100: 4}` alone | A100 = 2 in `x` | `false` (correct) |
| that + a second namespace entry `x: {A100: 4}` | A100 = 2 in `x` | **`true`** |
| cluster quota `{H100: 8}` + namespace entry `x: {A100: 4}` | A100 = 2 in `x` | **`true`** |

The last row is a config that installs today: a cluster cap plus per-tenant
caps. Each entry on its own denies A100. Together they allow it.

The four places that read constraints all use these merges: the greedy
optimizer, the utilization-share optimizer, rescale and the scale-from-zero
check. Limit attribution (`bindingProvider`) already treats an unlisted type as
a deny. So `LimitedBy` can name a limiter that the allocation itself ignored.

### Smaller gaps that matter once physical and quota compose

These were found by reading the code. They are not measured.

- **Warm-pool headroom reads namespace pools only** (`namespaceHeadroom`,
  `gpu_limit_attribution.go`). A cluster-scope bound, physical or quota, never
  reaches the warm pool, so the pool reads *unbounded* and grows.
- **One provider failing has a different effect in each engine.** Steady state
  skips the failing provider and keeps the others. Scale-from-zero drops all
  of them and wakes with no check (`scalefromzero/candidates.go`). With
  physical and quota composed, a GPU-discovery error in scale-from-zero would
  also lift the quota.
- **`PhysicalUsageConfigured`** answers from the single limiter mode, so in
  quota mode it says no one needs the physical view.

## Design

### Part A: limiters compose

**Rule:** a decision must fit **every** declared limiter. A type is allowed only
if every limiter that has a view on it allows it. The budget is the smallest
available figure among them.

**A1. Merge by intersection.** Add one field to `ResourceConstraints`:

```go
// PoolsClosed reports whether Pools is a closed allowlist: a type it does
// not list is denied. True for a physical inventory (an undiscovered type
// has no GPUs) and for a cluster-scope quota. False for a namespace-scope
// quota, whose Pools is only the sum of its namespace caps.
PoolsClosed bool
```

- **`mergeConstraints`:**
  - A type is kept only if no closed provider omits it.
  - Its budget is the minimum over the providers that list it.
  - The `Limit < 0` (unlimited) sentinel behaves as it does today.
- **`mergeNamespaceConstraints`:**
  - For a namespace held by several providers, a type is allowed only if every
    one of them lists it.
  - A provider that does not hold the namespace (excluded, or cluster-scope
    only) adds no namespace rule.
- `bindingProvider` already uses these semantics. After the change it agrees
  with the allocation.

This is a bug fix and ships first, on its own. Its tests are the five rows of
the table above.

**A2. Build every declared limiter.**

- `NewLimiterFromConfig` builds the physical limiter, when one is declared,
  *and* every quota entry. It groups them in the existing `CompositeLimiter`.
  The steady-state and scale-from-zero engines already compute constraints
  from each constituent, and each provider already gets its own usage view
  (`GPUUsageViews.For`). The quota gets managed usage; the physical limiter
  gets every GPU on the nodes. So no new plumbing is needed.
- `EffectiveLimiterMode` becomes a set. `PhysicalUsageConfigured` is true when
  any `gpu-inventory` entry is present.
- `UnenforcedLimiterTypes` and its startup warning are deleted.
- `EffectiveLimiterSummary` names every entry, e.g.
  `gpu-inventory+quota/gpu,tenant-quota`.
- Validation allows at most one `gpu-inventory` entry.

**A3. Close the gaps above.**

- Warm-pool headroom takes the minimum of the namespace budget and the closed
  cluster budget for the type, using the same merge.
- Scale-from-zero skips a failing provider, as steady state does. It logs the
  provider's name and wakes with no check only when *every* provider failed.

**Upgrade note.** A policy that declares both `gpu-inventory` and `quota` has,
until now, been bounded by the quota alone. After this change it is also
bounded by physical GPUs, which is what it says. This goes in the release
notes. Physical limits can only lower an allocation, and the physical figure
over-states capacity (cordoned nodes still count), so the risk is one-way.

### Part B: count Kueue borrowing (#136)

**B1. Configuration** is on by default for every quota entry with
`kueue.enabled`, and can be turned off per entry:

```yaml
limiters:
  - name: gpu
    type: gpu-inventory          # recommended with borrowing (Part A)
  - name: tenant-quota
    type: quota
    scope: namespace
    kueue:
      enabled: true
      # countBorrowable: false   # default true; false = nominal quota only, as before
    namespaceQuotas:
      default: { H100: -1 }
```

The installer gets `WVA_QUOTA_KUEUE_BORROW=false` to turn it off. If borrowing
is on and no `gpu-inventory` entry is declared, validation still accepts the
config, but it logs a warning at every rebuild. Borrowed capacity is, by
construction, GPUs another tenant owns. The warning stays until physical bounds
are in force. On a cluster-scoped install, the installer adds a
`gpu-inventory` entry with `WVA_QUOTA_KUEUE=true` unless the operator passes
`WVA_LIMITER_PHYSICAL=false`. A namespace-scoped install cannot list nodes, so
it gets only the warning.

**Upgrade note.** Default on means every existing Kueue-bounded install starts
counting borrowable capacity on upgrade. A namespace in a cohort with idle
lenders will then grow past its nominal quota where it used to stop. This goes
in the release notes with the one-line opt-out. Step 3 of
[Order of work](#order-of-work) ships the read-only log one release earlier, so
operators can see the figure before it applies.

**B2. What is read.** No new kinds, and no new RBAC for flat cohorts. Every
field below is on the ClusterQueue objects the reader already lists.

| Field | Used for |
|---|---|
| `spec.cohort` (v1beta1) / `spec.cohortName` (v1beta2) | cohort membership. Read both; the reader is version-agnostic today and must stay so. |
| `spec.resourceGroups[].flavors[].resources[].borrowingLimit` | how far this queue may go past nominal (unset = no limit) |
| `…lendingLimit` | how much of a member's nominal the cohort may use (unset = all of it) |
| `status.flavorsReservation[].resources[].total` | quota reserved by every workload in the queue, per flavor and resource. Kueue admits against reservation, so this is the figure to use. |
| `status.pendingWorkloads` | a lender is waiting for its own capacity (see B5) |

**B3. The ceiling, per ClusterQueue, flavor and GPU resource.** This mirrors
how Kueue decides whether a workload fits by borrowing. For each member `m` of
the cohort:

```
N_m = nominalQuota        L_m = lendingLimit (unset: N_m)
B_m = borrowingLimit      U_m = reserved total
G_m = N_m - L_m                           # kept for m, never lent

cohortFree = max(0, Σ L_m  -  Σ max(0, U_m - G_m))
reach_Q    = min(N_Q + B_Q,  U_Q + max(0, G_Q - U_Q) + cohortFree)
ceiling_Q  = max(N_Q, reach_Q)
borrowable_Q = ceiling_Q - N_Q
```

Three properties, each with a unit test:

- **The queue's own usage is inside `reach_Q`.** A queue that is already
  borrowing keeps its borrowed replicas in the ceiling. A formula built from
  `cohortFree` alone would drop the ceiling as soon as the queue borrowed, cause
  a scale-down, free the capacity, and repeat.
- **The ceiling never drops below nominal.** If another queue is using our
  unused nominal, Kueue gives it back by preemption. That is the guarantee
  nominal already stands for today.
- **Kueue's own example holds.** team-a has `nominal 9`, `borrowingLimit 1`, and
  team-b has `nominal 12` and is idle. The reach is 10.

Per-type aggregation is unchanged: flavors of one product add up and untyped
grants take the largest. A namespace with LocalQueues on several queues adds
their ceilings, as it adds nominals today.

**B4. Only the namespace scope changes.** Borrowing moves capacity between
queues; it does not create any. So the cluster-scope figure (the sum of nominal
over every queue) is already the right total. Hierarchical cohorts with their
own `resourceGroups` are the exception; see B7.

**B5. Lenders that cannot reclaim.** Inference Deployments never finish. Kueue's
default for `preemption.reclaimWithinCohort` is `Never`. A lender with that
setting never gets borrowed capacity back from a serving model, and its own
workloads stay pending. So the scaling manager does not borrow from a lender
that is waiting. If any lender in the cohort has `pendingWorkloads > 0`:

- if that lender's `reclaimWithinCohort` is `Never`, the ceiling drops to
  nominal. The scaling manager's normal scale-down then hands the borrowed
  replicas back;
- otherwise `cohortFree` is counted as 0, so no new borrowing starts.
  Replicas already borrowed stay until Kueue reclaims them.

The well-lit path recommends `reclaimWithinCohort: Any` on lenders.

**B6. The borrowed part is reported, and treated as less certain.**

- `ResourcePool` gets a `Borrowable int` field: the part of `Limit` that is not
  guaranteed. Merges carry it as `min` alongside the budget.
- Scale-from-zero wakes against the guaranteed part only (`Limit -
  Borrowable`). A model woken onto borrowed capacity can be preempted before it
  serves its first request, which is worse than not waking.
- The `Refresh` failure path keeps the last nominal snapshot, as today. The
  borrowed part depends on what others use right now, so a snapshot older
  than `2 × refreshInterval` counts it as 0. This fails toward the guaranteed
  figure.
- If ClusterQueue status is empty (the Kueue CRDs are installed but the
  controller is not running), borrowable is 0, and this is logged once.

**Not proposed: optimizer tiers.** The issue asks the optimizer to release
borrowed replicas first. The scaling manager cannot choose which pod Kueue
preempts: Kueue picks by priority and admission time. Lowering a model's
replica count lets the ReplicaSet pick a pod, also without regard to tier. A
two-pass fair share would decide *which model* shrinks, but not which pods
leave. The lever that works is Kueue's `WorkloadPriorityClass` on the models
that should not lose replicas. The well-lit path documents that instead. If a
real workload shows that the per-model choice matters, a tier pass can be
added on top of `Borrowable` later.

**B7. Hierarchical cohorts.** These are a follow-up. When a queue's cohort
names a `Cohort` object that has a `parentName` or its own `resourceGroups`,
borrowable for that queue is 0, and this is logged once with the reason. The
follow-up walks the tree, needs `get`/`list` on `cohorts.kueue.x-k8s.io`, and
adds a cohort's own quota to the cluster-scope figure.

### Part C: what the operator sees

- **Log.** The existing `quota entry bounded by external caps` line adds a
  per-namespace, per-type split, `nominal` and `borrowable`, plus the reason
  when borrowable is 0 (`lender-waiting`, `stale`, `no-status`,
  `hierarchical-cohort`).
- **`wva-controller-status` ConfigMap.** It carries only the limiter summary
  today. Add a `limiterBudgets` key: per entry, namespace and type, `limit`,
  `borrowable`, `used`. Take these from the same constraints the optimizer
  read, not recomputed.
- **Metric.** A new gauge,
  `wva_quota_limit_gpus{limiter_name, exported_namespace, accelerator, tier}`,
  with `tier` set to `nominal` or `borrowable`.
  `wva_decisions_limited_total{limiter_name}` already says which limiter bound a
  decision. After A1 it is right when entries disagree on types.

## Order of work

Each step is one PR with its own negative control: the new test, run against
the parent commit, has to fail there.

1. **A1, intersection merge.** A standalone bug fix that changes no config.
   Its tests are the probe rows above, plus attribution agreeing with the
   allocation.
2. **A2 and A3, build every declared limiter.** This adds the warm-pool and
   scale-from-zero alignment, the upgrade note, and updates to
   `quota-limiter.md` ("Interaction with `TypeInventory`", "Selection &
   lifecycle") and `gpu-limiter.md`.
3. **Read only.** The reader parses cohort, limits and status, and *logs*
   the borrowable figure it would apply, without applying it. Operators can
   then check the number against `kubectl get clusterqueue` before trusting
   it.
4. **B1 to B6, `countBorrowable`.** Unit tests for the formula, including a
   two-queue cohort, a lender reclaiming, a waiting lender with `Never` and with
   `Any`, a stale snapshot, and v1beta1 `cohort` against v1beta2 `cohortName`.
   An API-server test drives the status through the same sequence.
5. **Live Kueue e2e.** The current suite installs only Kueue's CRDs, so
   ClusterQueue status is never written. This e2e installs the Kueue controller
   from `registry.k8s.io` and runs a two-queue cohort. A lender's workload
   arrives, and the test checks that the borrowed replicas are given back.
   This step also moves the
   [Kueue path](../well-lit-paths/kueue-bounded-quotas/) out of *Experimental*
   for borrowing.
6. **B7, hierarchical cohorts.**

## Open questions

- **Scale-from-zero into borrowed capacity.** Waking against the guaranteed
  part only is proposed. A model whose whole nominal quota is in use then
  cannot wake, even when a lender is idle. A per-model
  `scaleFromZero.allowBorrowed` would cover that if anyone asks.
- **Shared ClusterQueues.** These are still a per-namespace ceiling, not a
  partition. Borrowing does not change that, and it is out of scope here.
