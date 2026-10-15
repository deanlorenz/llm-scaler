# Let models borrow from a Kueue cohort

## Overview

With a Kueue-bounded quota, a model may grow past its ClusterQueue's own quota
into GPUs its cohort lends, and Kueue takes them back when the lender needs
them. This guide checks the prerequisites, turns borrowing on, pilots it on a
few namespaces, reads why it is withheld, and turns it off.

Borrowing is **experimental, but on by default** for every namespace-scope
quota entry with `kueue.enabled`. Where a condition does not hold, the
namespace stays at its guaranteed figure and a reason says why.

Whether to take it at all (what it buys, what it costs, when not to):
[the Kueue path](../../well-lit-paths/kueue-bounded-quotas/).

### Terms

| Term | Meaning |
| --- | --- |
| **nominal** | a ClusterQueue's `nominalQuota` |
| **guaranteed** | the part of nominal the queue can count on now. It is per ClusterQueue: namespaces that share one queue share it |
| **lender** | a ClusterQueue in the cohort whose unused nominal others borrow |
| **`reclaimWithinCohort`** | whether a lender takes lent capacity back by preemption: `Any`, `LowerPriority` or `Never` |
| **borrow cap** | how far past guaranteed a namespace may go: nominal plus `borrowingLimit`, bounded by the static entry and the namespace's ResourceQuota |
| **flavor** | a Kueue ResourceFlavor: which nodes a quota is on |

## Prerequisites

- [ ] The scaling manager installed ([in a namespace](../install-in-namespace/)),
  every variant's accelerator resolving
  ([check](../admin-gpu-bounding/README.md#prerequisites)), Kueue's
  `ClusterQueue`, `LocalQueue`, `ResourceFlavor`, `Cohort` and `Workload`
  kinds served, and a LocalQueue in each managed namespace.
- [ ] **Kueue runs a tested version**, and each managed ClusterQueue is in a
  cohort. Lenders that must get capacity back from serving use
  `reclaimWithinCohort: Any`, and no queue in the cohort sets
  `borrowWithinCohort` (or sets it to `Never`):

<!-- guide:prerequisites.kueue start -->
```bash
# Kueue runs, and its image carries a version tag. List the ClusterQueues with their cohort and preemption settings (v1beta1 calls the cohort field spec.cohort).
kubectl get deploy kueue-controller-manager -n kueue-system \
  -o jsonpath='{.status.readyReplicas} ready, image {.spec.template.spec.containers[*].image}{"\n"}'
kubectl get clusterqueue -o custom-columns='NAME:.metadata.name,COHORT:.spec.cohortName,RECLAIM:.spec.preemption.reclaimWithinCohort,BORROW_WITHIN:.spec.preemption.borrowWithinCohort'
```
<!-- guide:prerequisites.kueue end -->

  The tested versions are the ones the `e2e-tests-kueue-borrow` CI job runs
  ([the table](../../reference/quota-limiter.md#supported-kueue-versions)).
  Kueue elsewhere than `kueue-system`: set `kueue.controllerNamespace` on the
  entry.

- [ ] **(cluster admin) The controller can read what borrowing reads.** A
  namespace-scoped install needs the admin phase re-run. Without the Role in
  the Kueue namespace, borrowing stops with `kueue-controller-down`; without
  the cluster-wide Workload read, with `lenders-unreadable`:

<!-- guide:prerequisites.rbac start -->
```bash
# (cluster admin) A namespace-scoped install: re-run the admin phase, with the variables the install used. It creates the Role in the Kueue namespace and the cluster-wide reads borrowing needs. Then check what the controller may read: every line must print yes.
NAMESPACE=<namespace> make setup-prereqs
SA=system:serviceaccount:<wva-namespace>:wva-controller-manager
kubectl auth can-i list workloads.kueue.x-k8s.io --all-namespaces --as="$SA"
kubectl auth can-i list cohorts.kueue.x-k8s.io --as="$SA"
kubectl auth can-i get deployments/kueue-controller-manager -n kueue-system --as="$SA"
kubectl auth can-i get leases.coordination.k8s.io/c1f6bfd2.kueue.x-k8s.io -n kueue-system --as="$SA"
kubectl auth can-i list resourcequotas -n <namespace> --as="$SA"
```
<!-- guide:prerequisites.rbac end -->

  For the out-of-reach shape add `WVA_NS=wva-<namespace> WVA_WATCH_NS=<namespace>`.
  `c1f6bfd2.kueue.x-k8s.io` is Kueue's leader-election Lease.

- [ ] **A GPU bound.** Something must check that borrowed GPUs exist: a
  `gpu-inventory` limiter (it needs the node read `make setup-prereqs`
  grants), or a GPU `ResourceQuota` in the namespace whose `hard` is **at most
  nominal plus `borrowingLimit`**. The installer adds neither. Without one,
  borrowing is withheld as `no-gpu-bound`:

<!-- guide:prerequisites.bound start -->
```bash
# Borrowing needs a GPU bound. Print the queue's nominal and borrowingLimit per flavor, then the namespace's ResourceQuotas: a GPU ResourceQuota must be at most nominal plus borrowingLimit.
kubectl get clusterqueue <cluster-queue> \
  -o jsonpath='{range .spec.resourceGroups[*].flavors[*]}{.name}{":"}{range .resources[*]}{" "}{.name}{" nominal="}{.nominalQuota}{" borrowingLimit="}{.borrowingLimit}{end}{"\n"}{end}'
kubectl get resourcequota -n <namespace> -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.hard}{"\n"}{end}'
```
<!-- guide:prerequisites.bound end -->

  A queue with `nominalQuota: 8` and `borrowingLimit: 8` takes a quota of at
  most 16:

  ```yaml
  apiVersion: v1
  kind: ResourceQuota
  metadata:
    name: gpu
    namespace: <namespace>
  spec:
    hard:
      requests.nvidia.com/gpu: "16"
  ```

- [ ] **Kueue settings borrowing does not support are off**: Fair Sharing, and
  `waitForPodsReady` with `blockAdmission: true`. Fair Sharing is detected and
  withholds borrowing; `blockAdmission` is not detected:

<!-- guide:prerequisites.kueueconfig start -->
```bash
# Kueue settings borrowing does not support. Expect no fairSharing block and no waitForPodsReady with blockAdmission true.
kubectl get configmap kueue-manager-config -n kueue-system \
  -o jsonpath='{.data.controller_manager_config\.yaml}' | grep -A4 -E 'fairSharing|waitForPodsReady'
```
<!-- guide:prerequisites.kueueconfig end -->

- [ ] **Every GPU pod in the namespace is Kueue-managed**, and tenants cannot
  write Kueue Workloads there. One unmanaged GPU pod withholds borrowing
  (`not-kueue-managed`):

<!-- guide:prerequisites.managed start -->
```bash
# Every GPU pod in the namespace is Kueue-managed: one Workload per replica (or replica group), with quota reserved. Tenants must not write Workloads: expect no.
kubectl get workloads.kueue.x-k8s.io -n <namespace> \
  -o custom-columns='NAME:.metadata.name,OWNER:.metadata.ownerReferences[0].kind,RESERVED:.status.conditions[?(@.type=="QuotaReserved")].status'
kubectl auth can-i create workloads.kueue.x-k8s.io -n <namespace> --as=<tenant>
```
<!-- guide:prerequisites.managed end -->

- [ ] **Serving pods survive a preemption cleanly**: a drain hook
  (`make workload-patch NAMESPACE=<namespace>`; Kueue deletes a preempted pod
  without the Eviction API, so a PodDisruptionBudget does not apply), a
  `terminationGracePeriodSeconds` of at least one engine cycle, and a
  `WorkloadPriorityClass` above batch for models that must keep replicas.
- [ ] **Each variant's pod template sets requests for every resource its GPU
  flavor's resource group covers** (CPU, memory), and **pins the flavor's
  `nodeLabels`** when the queue has more than one flavor for the GPU.
  Otherwise borrowing is withheld (`co-resource-bound`, `flavor-unpinned`).
- [ ] **One scaling manager per cohort**, or `countBorrowable: false` on the
  Kueue entries of all but one. Two controllers that borrow from one cohort
  can count the same free GPUs.

Two things Kueue does that the scaling manager does not change:

- **P/D pairs are not admitted atomically.** A P/D model deployed as two
  workloads is two Kueue Workloads, and Kueue may admit or preempt one role
  without the other. The scaling manager borrows a P/D pair only whole, both
  roles on borrowed capacity, and gives back a half left unplaced after the
  placement timeout.
- **Kueue picks which pod to preempt**, by `WorkloadPriorityClass`.

## Installation Instructions

<!-- guide:deploy.enable start -->
```bash
# Namespace owner. A GPU bound plus a Kueue-backed quota; -1 lets Kueue set the figure. Borrowing is counted by default. With a GPU ResourceQuota as the bound instead, use WVA_LIMITER=quota.
make deploy-wva NAMESPACE=<namespace> \
  WVA_LIMITER=gpu-inventory,quota WVA_QUOTAS='<accelerator>=-1' WVA_QUOTA_KUEUE=true
```
<!-- guide:deploy.enable end -->

<!-- guide:deploy.cluster start -->
```bash
# Or, cluster admin: the same quota in the cluster policy every controller reads. A gpu-inventory entry already published there is kept.
make enable-physical-limiter WVA_LIMITER_TYPE=quota WVA_QUOTAS='<accelerator>=-1' WVA_QUOTA_KUEUE=true
```
<!-- guide:deploy.cluster end -->

`<accelerator>` is the name the controller logs for each model. The entry it
writes:

```yaml
limiters:
  - type: gpu-inventory
  - name: install-quota
    type: quota
    scope: namespace
    kueue:
      enabled: true               # countBorrowable defaults to true
    namespaceQuotas:
      <namespace>:
        <accelerator>: -1         # the TYPE is yours; the NUMBER is Kueue's
```

## Verification

<!-- guide:verify.log start -->
```bash
# The effective caps, then the borrowing figures. Both are logged when they change.
kubectl logs -n <wva-namespace> deploy/wva-controller-manager --tail=-1 \
  | grep -E 'quota entry bounded by external caps|Kueue borrowing applied|external quota source' | tail -5
```
<!-- guide:verify.log end -->

<!-- guide:verify.budgets start -->
```bash
# Per namespace and accelerator: guaranteed, borrowable, used, and the reason borrowable is 0. No reason and borrowable above 0: borrowing is counted.
kubectl get configmap wva-controller-status -n <wva-namespace> -o jsonpath='{.data.limiterBudgets}' \
  | jq -c '.entries[] | {namespace, accelerator, guaranteed, borrowable, used, reason}'
```
<!-- guide:verify.budgets end -->

The same in Prometheus, where the model's namespace is the
`exported_namespace` label:

```promql
wva_quota_limit_gpus{tier="borrowable"} > 0     # may borrow now
wva_quota_borrow_withheld == 1                  # why not, per namespace
wva_kueue_borrowed_gpus                         # what this controller holds borrowed, per cohort and flavor
```

A model on borrowed capacity shows `used` above `guaranteed` with no Pending
pod (`kubectl get pods -n <namespace> --field-selector status.phase=Pending`).

## Pilot on a few namespaces

To count borrowing in some namespaces only, set `borrowNamespaces` on the
Kueue-backed quota entry. Every other namespace keeps its guaranteed figure,
withheld as `not-selected`:

```yaml
    kueue:
      enabled: true
      borrowNamespaces: [team-a, team-b]   # empty or absent: every governed namespace
```

<!-- guide:pilot.set start -->
```bash
# Count borrowing only in the namespaces listed. Find where the policy lives, then set borrowNamespaces on the Kueue-backed quota entry.
POLICY_NS=$(kubectl get cm wva-controller-status -n <wva-namespace> -o jsonpath='{.data.policyNamespace}')
kubectl get configmap wva-scaling-policy-config -n "${POLICY_NS:-<wva-namespace>}" -o jsonpath='{.data.default}' \
  | yq '(.limiters[] | select(.type == "quota" and .kueue.enabled == true) | .kueue.borrowNamespaces) = ["<namespace>"]' \
  > policy-default.yaml
kubectl patch configmap wva-scaling-policy-config -n "${POLICY_NS:-<wva-namespace>}" --type=merge \
  -p "$(jq -n --rawfile d policy-default.yaml '{data: {default: $d}}')"
```
<!-- guide:pilot.set end -->

A controller that watches the policy's namespace applies it on its next cycle.
A namespace-scoped controller whose policy lives in another namespace
(`wva-policy`, or `wva-<namespace>` for the out-of-reach shape) reads it at
startup: restart it with
`kubectl rollout restart deploy/wva-controller-manager -n <wva-namespace>`.

<!-- guide:pilot.check start -->
```bash
# Every namespace left out now reads not-selected.
kubectl get configmap wva-controller-status -n <wva-namespace> -o jsonpath='{.data.limiterBudgets}' \
  | jq -c '.entries[] | select(.reason == "not-selected") | {namespace, accelerator}'
```
<!-- guide:pilot.check end -->

**Keep the list across re-installs.** Re-running `make deploy-wva` **without**
`WVA_LIMITER` keeps the live `limiters:` block as it is, `borrowNamespaces`
included. Re-running it **with** `WVA_LIMITER` (and `WVA_QUOTA_KUEUE=true`)
rewrites the quota entry from the variables and **drops `borrowNamespaces`**,
so borrowing is counted everywhere again. `make enable-physical-limiter
WVA_LIMITER_TYPE=quota` does the same to the cluster policy. Set the list
again after either.

Widen the pilot by adding namespaces; remove the key to borrow everywhere.

## Read why borrowing is withheld

<!-- guide:reasons.read start -->
```bash
# Every namespace and accelerator whose borrowing is withheld, frozen, or counted at 0, with its reason.
kubectl get configmap wva-controller-status -n <wva-namespace> -o jsonpath='{.data.limiterBudgets}' \
  | jq -c '.entries[] | select(.reason) | {namespace, accelerator, reason}'
```
<!-- guide:reasons.read end -->

Each namespace shows one reason, in three groups:

- **Withheld**: a condition does not hold; borrowable is 0 and the namespace
  stays at guaranteed. For example `no-gpu-bound`, `not-kueue-managed`,
  `lenders-unreadable`.
- **Frozen**: Kueue cannot be trusted right now (`stale`,
  `kueue-controller-down`); nothing new is borrowed, and nothing borrowed is
  taken away.
- **Counted, but 0 this cycle**: borrowing is on, there is nothing to draw now
  (`lender-waiting`, `cohort-pool-empty`, ...).

What each reason means and what to do:
[Borrowing is withheld](../../reference/troubleshooting.md#borrowing-is-withheld).

## Cleanup

<!-- guide:cleanup.stop start -->
```bash
# Stop counting borrowing; the Kueue cap stays, at guaranteed. Nothing is evicted. This rewrites the quota entry, so a borrowNamespaces list goes too.
make deploy-wva NAMESPACE=<namespace> \
  WVA_LIMITER=gpu-inventory,quota WVA_QUOTAS='<accelerator>=-1' WVA_QUOTA_KUEUE=true WVA_QUOTA_KUEUE_BORROW=false
# the cluster policy instead:
# make enable-physical-limiter WVA_LIMITER_TYPE=quota WVA_QUOTAS='<accelerator>=-1' WVA_QUOTA_KUEUE=true WVA_QUOTA_KUEUE_BORROW=false
```
<!-- guide:cleanup.stop end -->

By hand, set `countBorrowable: false` under the entry's `kueue:` block. A
lowered cap only stops growth: models on borrowed capacity keep it until they
scale down, or until Kueue preempts them for a lender:

<!-- guide:cleanup.drain start -->
```bash
# Models move back under guaranteed only as they scale down. Lists the ones still above it; empty when nothing borrowed is left.
kubectl get configmap wva-controller-status -n <wva-namespace> -o jsonpath='{.data.limiterBudgets}' \
  | jq -c '.entries[] | select(.guaranteed >= 0 and .used > .guaranteed) | {namespace, accelerator, guaranteed, used}'
```
<!-- guide:cleanup.drain end -->

To hand GPUs back sooner, lower the model's `maxReplicaCount` on its
ScaledObject. To roll back to a release without borrowing, turn it off first
and wait for this list to empty, then follow
[upgrade and uninstall](../../reference/upgrade-and-uninstall.md#limiters-and-kueue-borrowing).

To drop the Kueue cap too, **replace** the entry rather than only dropping
`WVA_QUOTA_KUEUE`: a type left at `-1` without Kueue has no cap at all. Re-run
`make deploy-wva` with `WVA_LIMITER=quota` and a finite `WVA_QUOTAS`, or with
`WVA_LIMITER=none`. For the cluster policy, re-run
`make enable-physical-limiter WVA_LIMITER_TYPE=quota` with a finite
`WVA_QUOTAS`, or remove the quota with
`make disable-limiter WVA_LIMITER_TYPE=quota`.

## Configuration

Every setting on the entry's `kueue:` block (`placementTimeout`,
`borrowCooldown`, `lenderDebounce`, `controllerNamespace`,
`assumeTestedVersion`, `borrowNamespaces`):
[cohort borrowing](../../reference/quota-limiter.md#cohort-borrowing). The
signals and alerts: [Prometheus](../../reference/prometheus.md#kueue-borrowing-metrics).
How it is built and tested: [Kueue borrowing internals](../../developer-guide/kueue-borrowing.md).
