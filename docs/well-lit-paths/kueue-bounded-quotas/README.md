# Bound tenants by the quotas Kueue already holds

If Kueue already decides your GPU quotas, the scaling manager can read them
instead of keeping a second copy. It caps each namespace and accelerator type
at **the smaller of Kueue's grant and its own static cap**, so it never asks
KEDA for a replica Kueue would hold pending. Where it is safe, the grant
includes what the namespace's ClusterQueue can **borrow** from its cohort, only
from lenders Kueue would reclaim for, and Kueue takes borrowed replicas back
when a lender needs them.

**Maturity:** **Experimental.** Borrowing is experimental but on by default:
`countBorrowable: false` on the entry turns it off and keeps the Kueue cap.
What backs it: [Evidence](#evidence).

## What it buys

- One source of truth for tenant GPU numbers: change the ClusterQueue, and the
  scaling manager follows within one `refreshInterval` (30s) plus one
  [optimize cycle](../../reference/cycle-log.md).
- No replica ordered that Kueue would hold at its admission gate, so no
  Pending pod a scale-up counted on.
- With borrowing: a model grows into GPUs its cohort leaves idle (batch quota
  between jobs, say), and gives them back when Kueue reclaims them.

## What it costs

- **API load.** ClusterQueues, LocalQueues, ResourceFlavors and Cohorts are
  listed once per `refreshInterval`. Workloads come from a cluster-wide
  informer (one watch, every Workload held in memory). ResourceQuotas in
  managed namespaces are cached.
- **A lag**: a change in Kueue takes effect after at most `refreshInterval`
  plus one cycle.
- **With borrowing: lost replicas.** Kueue preempts borrowed replicas when a
  lender asks for its quota, and the scaling manager gives back a borrowed
  replica that did not place within `placementTimeout` (5m). Each loss is a
  cold start.
- **Prerequisites to keep true**: a GPU bound, Kueue-managed GPU pods, and,
  for a namespace-scoped install, a Role in the Kueue namespace and a read of
  every namespace's Kueue Workloads
  ([the guide's prerequisites](../../guides/kueue-borrowing/#prerequisites)).
- **A wider read**: borrowing reads every tenant's Kueue Workloads, pod
  templates included ([security model](../../reference/security-model.md#kueue-borrowing)).

Failures are visible. A failed read freezes borrowing at the last figures
(`wva_quota_borrow_withheld{reason="stale"}`, alert `WVAKueueReaderStale`).
An entry never read shows as `wva_kueue_never_read` (alert `WVAKueueNeverRead`):
its finite caps still bound, and a type at `-1` is bounded by the other
limiters.

## When not to take it

- **Several namespaces share one ClusterQueue and the shared total must
  hold.** Each namespace is capped at the queue's full grant, and guaranteed
  is per ClusterQueue: a shared queue is a ceiling per namespace, not a
  partition. Give each tenant its own ClusterQueue
  ([details](../../reference/quota-limiter.md#what-is-read)).
- **Tenants can create LocalQueues on a queue that admits any namespace**, and
  your static cap is `-1`. They would get that queue's grant
  ([details](../../reference/quota-limiter.md#what-is-read)).
- **The controller must stay bounded when Kueue cannot be read**, with `-1`
  caps only. Until the first read succeeds, an entry that leaves a type at
  `-1` is skipped, and only the other limiters bound. Use a finite static
  cap, which Kueue can only tighten.
- **A model must never lose a replica**, and its queue lends to or borrows
  from a cohort. Give it the quota as nominal, or set `countBorrowable: false`.
- **Kueue features borrowing does not support**: hierarchical cohorts, Fair
  Sharing, Topology-Aware Scheduling, admission checks (MultiKueue),
  `borrowWithinCohort` preemption, and `waitForPodsReady` with
  `blockAdmission`. Each but the last withholds borrowing with a reason; the
  last is not detected. `waitForPodsReady` at all is a risk: Kueue evicts a
  model whose pods are not Ready within its timeout (5 minutes by default).
- **Two scaling managers borrow from one cohort.** Run one per cohort, or set
  `countBorrowable: false` on the Kueue entries of all but one.

## Start here

1. Read [capping what each tenant may take](../tenant-gpu-quotas/#start-here);
   everything there still applies.
2. Follow [let models borrow from a Kueue cohort](../../guides/kueue-borrowing/):
   prerequisites, enable, verify, pilot on a few namespaces, read the reasons,
   turn it off. To keep the Kueue cap without borrowing, add
   `WVA_QUOTA_KUEUE_BORROW=false` to its enable step.

## Evidence

No benchmark scenario: a bound changes what the optimizer may order, not how
it scales. Two end-to-end suites back it:

- `test/e2e/kueue_quota_test.go` (label `kueue-quota`): the read-and-merge
  chain against Kueue's CRDs, in the full tier.
- `test/e2e/kueue_borrow_test.go` (label `kueue-borrow`), against a live
  Kueue: an unmanaged GPU pod withholds borrowing, a stopped Kueue controller
  stops growth past guaranteed, a `Never` lender's capacity is not counted,
  borrowing with every pod placed, and recovery after Kueue preempts. The live
  e2e runs in CI job `e2e-tests-kueue-borrow`.

Not covered end to end: inactive queues, finalizers and `stopPolicy`
transitions (unit fixtures only), and enforcement past the effective entry,
the same gap as [tenant-gpu-quotas](../tenant-gpu-quotas/). Defaults that may
still move: `refreshInterval` 30s, `placementTimeout` 5m, `borrowCooldown`
10m. The unit suites, by package:
[Kueue borrowing internals](../../developer-guide/kueue-borrowing.md#testing).

## Read next

- Every field, the merge rules, what is read and when borrowing is counted:
  [Kueue as a quota source](../../reference/quota-limiter.md#kueue-as-a-quota-source).
- Why a namespace does not borrow, and what to do:
  [Borrowing is withheld](../../reference/troubleshooting.md#borrowing-is-withheld).
  Replicas dropped:
  [the runbook](../../reference/troubleshooting.md#replicas-dropped-under-kueue-borrowing).
- The signals and alerts: [Prometheus](../../reference/prometheus.md#kueue-borrowing-metrics).
- Upgrading to a release that counts borrowing:
  [upgrade notes](../../reference/upgrade-and-uninstall.md#limiters-and-kueue-borrowing).
- Tenants and Kueue's Workloads: [security model](../../reference/security-model.md#kueue-borrowing).
- Why a quota counts only the scaling manager's own consumption:
  [GPU capacity accounting](../../concepts/gpu-capacity-accounting.md).
