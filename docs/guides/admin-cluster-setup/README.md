# Cluster-admin setup for a namespace

## Overview

Creates, for one namespace that already runs llm-d, the things a namespace
admin cannot: the cluster-scoped RBAC and the ServiceMonitor (and the
controller's own namespace, when it runs apart from the one it manages). After this, that
namespace's owner installs and upgrades the controller with no cluster-scoped
rights, and you are not in the loop again unless a release changes what it
needs cluster-wide.

Every permission it grants, with the verbs and the reason, is in the
[security model](../../reference/security-model.md#permissions).

## Prerequisites

Cluster-admin rights, and the namespace you are preparing, with llm-d model
servers **already running in it**. Both commands below refuse a namespace that
does not exist or holds no model servers, and apply the same monitoring and EPP
checks as the [install guide's prerequisites](../install-in-namespace/#prerequisites).
So the order is: deploy llm-d, then `make check-prereqs`, then `make setup-prereqs`.

<!-- guide:env.static.namespace start -->
```bash
export NAMESPACE=team-a
```
<!-- guide:env.static.namespace end -->

`NAMESPACE` is required: the commands refuse to guess which namespace they are
preparing.

<!-- guide:prerequisites.check start -->
```bash
make check-prereqs
```
<!-- guide:prerequisites.check end -->

## Installation Instructions

**Choose the shape first.** On a **shared cluster**, where the GPU bound must
hold against the namespace's owner, use the
[out-of-reach shape](#keeping-the-controller-out-of-the-tenants-reach): the
controller runs in a namespace you own and manages theirs. The tenant-owned
shape below — the controller in the namespace it manages — is for a cluster with
one team, or one where you trust every namespace owner: that owner can edit the
controller, and therefore every bound it carries.

Tenant-owned shape:

<!-- guide:deploy.prereqs start -->
```bash
make setup-prereqs
```
<!-- guide:deploy.prereqs end -->

Once per namespace, not once per upgrade.

### What it creates, and why it needs you

Rendered from `config/overlays/namespace-scoped/<platform>`. Cluster-scoped names
carry a hash suffix (below).

| object | why a namespace admin cannot create it |
| --- | --- |
| the controller's namespace, when `WVA_NS` names a separate one | cluster-scoped |
| `wva-metrics-auth-role` + binding | the metrics filter issues **TokenReview** and **SubjectAccessReview** |
| `wva-metrics-reader` + binding | `nonResourceURLs: /metrics`. On Kubernetes it is bound to `kube-prometheus-stack-prometheus` in `workload-variant-autoscaler-monitoring`; on a cluster with its own Prometheus, rebind it to that Prometheus's ServiceAccount or delete it. On OpenShift it is bound to the controller's own ServiceAccount |
| `wva-epp-metrics-reader-role` + binding | `nonResourceURLs: /metrics`, for the EPP scrape, cluster-wide |
| `wva-node-reader-role` + binding | resolving a variant's accelerator reads **Nodes** |
| `wva-kueue-reader-role` + binding | Kueue **ClusterQueues** and **ResourceFlavors** are cluster-scoped. Inert without Kueue |
| `wva-manager-cluster-monitoring-view` | **OpenShift only**: binds the platform's `cluster-monitoring-view`, so the controller can query Thanos |
| ServiceAccounts `wva-controller-manager` and `wva-epp-metrics-reader` | namespaced, but referenced by the RBAC and the ServiceMonitor above, so they are applied in the same phase |
| Secret `wva-epp-metrics-token` | a **long-lived, non-expiring** ServiceAccount token for `wva-epp-metrics-reader`, which the controller mounts to scrape EPP metrics. On OpenShift also `wva-controller-manager-token`, which the ServiceMonitor scrapes with |
| ConfigMap `wva-metrics-server-ca` | **OpenShift only**: the service CA the ServiceMonitor verifies the controller's certificate with |
| the ServiceMonitor | the stock `admin` ClusterRole does not grant `monitoring.coreos.com` |
| Roles `wva-manager-role`, `wva-leader-election-role`, `wva-utilization-share-history-role`, and their RoleBindings | namespaced, but RBAC forbids granting permissions you do not hold yourself, and the controller's Role names resources a namespace admin does not have |
| Prometheus | Kubernetes only, and only if the cluster has none |
| KEDA | Kubernetes only, and only with `KEDA_HELM_INSTALL=true`. On OpenShift, install the Custom Metrics Autoscaler operator beforehand |

Every cluster-scoped object is named with a hash of the controller's namespace,
so two installs cannot take each other's and an uninstall removes only its own.

Whoever can read Secrets in the controller's namespace holds the
`wva-epp-metrics-token` — in the default shape below, that is the tenant. It can
`get /metrics` on anything in the cluster that authorizes `/metrics` through the
API server. One more reason for the out-of-reach shape.

### Conditional grants

Written so a tenant can forward it to their admin. Each grant matters only under
the condition in the second column.

| grant | needed when | created by | without it |
| --- | --- | --- | --- |
| `wva-node-reader-role`: get, list, watch on nodes, cluster-wide | the policy that governs the controller declares the `gpu-inventory` limiter | `make setup-prereqs` (always, in namespace scope); `make enable-physical-limiter` | with `gpu-inventory`: the controller goes **NotReady**. With no limiter: it runs degraded — accelerators unresolved, no accelerator label on metrics |
| `wva-kueue-reader-role`: get, list on clusterqueues, resourceflavors; get on the managed Namespace | a quota limiter entry has `kueue.enabled: true` | `make setup-prereqs`; `make enable-physical-limiter WVA_QUOTA_KUEUE=true` | logs `external quota source unreadable` every cycle and applies the static quota entry alone. Without the Namespace read only (a role created before it was added): every ClusterQueue with a `namespaceSelector` is withheld, so the namespace is bounded at nothing by it until a cluster admin re-runs `make setup-prereqs` |
| RoleBinding to `wva-policy-reader` in the policy namespace: get, list, watch on its ConfigMaps | the cluster has a policy namespace (`wva-policy`, or one named by a `wva.llmd.ai/policy-namespace` label) and the controller reads policy from it | `make enable-physical-limiter` | the controller **refuses to start** rather than run unbounded |
| `wva-policy-namespace-reader-role`: get on namespaces, by name — the controller's own, the managed one, `wva-policy` (or `WVA_POLICY_NS`) and the namespace the managed one is labelled with | on **Kubernetes**, for a controller that manages its own namespace to see the policy-namespace label or `wva-policy` at all | `make setup-prereqs` (namespace scope); `make enable-physical-limiter`; or [by hand](#grant-the-namespace-read-by-hand) | the label and `wva-policy` are not seen and the controller uses its own ConfigMap; it logs `Could not read the managed Namespace` and `Cannot tell whether a cluster admin published policy` with the fix, and sets `wva_policy_namespace_unverified` to 1 (alert `WVAPolicyNamespaceUnverified`) |

#### Grant the namespace read by hand

`make setup-prereqs` creates this grant for a namespace-scoped install, so an
install whose admin ran it needs nothing here. An install set up before the
grant existed lacks it: re-run `NAMESPACE=${NAMESPACE} make setup-prereqs` as a
cluster admin, then restart the controller, which resolves its policy once at
startup.

The manifest below is the manual alternative, for a cluster whose RBAC is
managed by something else. Only for the tenant-owned shape on Kubernetes, and
only if a policy namespace (`wva-policy`, or a `wva.llmd.ai/policy-namespace`
label) should govern this controller. The grant names exactly the two
Namespace objects the controller reads: its own, and `wva-policy`.

<!-- guide:deploy.namespace_read start -->
```bash
# Tenant-owned shape on Kubernetes only: lets the controller read its own
# Namespace (for the wva.llmd.ai/policy-namespace label) and see whether
# wva-policy exists. Nothing else. The resolution runs once at startup, so
# restart the controller afterwards -- the controller Deployment only, not
# the warm pool.
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: wva-namespace-reader-${NAMESPACE}
rules:
- apiGroups: [""]
  resources: ["namespaces"]
  resourceNames: ["${NAMESPACE}", "wva-policy"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: wva-namespace-reader-${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: wva-namespace-reader-${NAMESPACE}
subjects:
- kind: ServiceAccount
  name: wva-controller-manager
  namespace: ${NAMESPACE}
EOF
kubectl -n ${NAMESPACE} rollout restart deploy/wva-controller-manager
```
<!-- guide:deploy.namespace_read end -->

Then check that the controller took it: the restarted pod logs `Cluster policy
comes from the well-known namespace` (or `... the namespace this one is labelled
with`), no longer logs `Could not read the managed Namespace`, and
`wva_policy_namespace_unverified` reads 0. `make
undeploy-wva` does not know about this manual grant: delete
`clusterrole,clusterrolebinding wva-namespace-reader-${NAMESPACE}` yourself
when the install goes.

### Keeping the controller out of the tenant's reach

By default the controller runs *in* the namespace it manages, so whoever
administers that namespace can edit its Deployment — and **nothing carried on the
controller can bound the person who can edit the controller**. The tenant also
owns, in effect, a ServiceAccount with cluster-wide node and Kueue read and the
EPP metrics token: any pod they start can run as it. On a shared cluster, make
this the default — run the controller in a namespace you own:

<!-- guide:deploy.out_of_reach start -->
```bash
make setup-prereqs   WVA_NS=wva-${NAMESPACE} WVA_WATCH_NS=${NAMESPACE}
make deploy-wva      WVA_NS=wva-${NAMESPACE} WVA_WATCH_NS=${NAMESPACE}
```
<!-- guide:deploy.out_of_reach end -->

with `WVA_NS` yours and `WVA_WATCH_NS` theirs. This is the recommended
multi-tenant shape. Its limiters come from its own ConfigMap in `wva-${NAMESPACE}`,
which you own; `wva-policy` does not apply to it. What the tenant keeps:
a `wva-scaling-policy-config` in **their** namespace is still read, as a
namespace-local map that sets thresholds, tiers and utilization-share weights
for their models — never the limiters
([security model](../../reference/security-model.md#multi-tenancy)).

Pass `WVA_WATCH_NS` to **both** commands. The controller needs permissions in the
namespace it manages, and writing RBAC into someone else's namespace is an
admin's act by definition — so `setup-prereqs` creates that Role and RoleBinding,
and the uninstall removes them. Without it the controller starts, cannot read the
namespace it was pointed at, and crash-loops on its first ConfigMap read.

### Bound the namespace's GPUs at admission

The limiter bounds what the scaling manager *asks* for; a `ResourceQuota`
bounds what the cluster *admits*, whoever owns the controller and whatever
`maxReplicaCount` says. On a shared cluster it is the real bound — create it in
either shape:

<!-- guide:deploy.gpu_quota start -->
```bash
# The bound that holds whoever owns the controller: admission refuses any
# pod that would take the namespace past this many GPUs. Size it to the
# tenant's entitlement; use the resource name your device plugin advertises.
kubectl -n ${NAMESPACE} create quota wva-gpus --hard=requests.nvidia.com/gpu=8
kubectl -n ${NAMESPACE} describe quota wva-gpus
```
<!-- guide:deploy.gpu_quota end -->

## Verification

Before handing over in the tenant-owned shape, check for a cluster-scoped
controller yourself. The tenant's `make deploy-wva` cannot list Deployments
cluster-wide, so it **warns and carries on** rather than refusing when one
exists — and two controllers managing the same workloads each spend the same
free GPUs. `make check-prereqs` run as you already does this check; the
command below is the same question asked directly:

<!-- guide:verify.no_cluster_install start -->
```bash
# A tenant's install cannot list Deployments cluster-wide, so it warns and
# CONTINUES instead of refusing when a cluster-scoped controller already
# exists. Check for them once, as you. An empty result means none; any line
# without --watch-namespace in its args is cluster-scoped and would manage
# the same workloads.
kubectl get deploy -A -l app.kubernetes.io/name=workload-variant-autoscaler,control-plane=controller-manager \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ARGS:.spec.template.spec.containers[0].args
```
<!-- guide:verify.no_cluster_install end -->

<!-- guide:verify.objects start -->
```bash
# The cluster-scoped names end in the first 8 hex digits of the SHA-256 of
# the CONTROLLER's namespace -- NAMESPACE in the default shape,
# wva-NAMESPACE in the out-of-reach one. Grepping for wva- alone lists
# every install on the cluster. On macOS use shasum -a 256.
kubectl get servicemonitor -n ${NAMESPACE}
kubectl get clusterrole,clusterrolebinding | grep "$(printf '%s' "${NAMESPACE}" | sha256sum | cut -c1-8)"
```
<!-- guide:verify.objects end -->

What you see is this namespace's set and no other's. For the out-of-reach shape,
hash `wva-${NAMESPACE}` and look for the ServiceMonitor there. Hand over once they
exist — the namespace's owner can then run `make deploy-wva` without you.

## Cleanup

Undeploy names the namespace the **controller** runs in. For the default shape,
where it runs in the namespace it manages:

<!-- guide:cleanup.undo start -->
```bash
make undeploy-wva WVA_NS=${NAMESPACE}
```
<!-- guide:cleanup.undo end -->

For the out-of-reach shape above:

<!-- guide:cleanup.out_of_reach start -->
```bash
make undeploy-wva WVA_NS=wva-${NAMESPACE}
```
<!-- guide:cleanup.out_of_reach end -->

The namespace, Prometheus, KEDA and EPP stay. Run by you, the uninstall also
removes the hashed ClusterRoles and bindings; run by the tenant, it cannot, and
prints the ones left for you.

## Configuration

| Parameter | Default | Example |
| --- | --- | --- |
| `NAMESPACE` | none — **required**, never discovered | `team-a` |
| `WVA_NS` | `NAMESPACE`: the controller runs in the namespace it manages | `wva-team-a` |
| `WVA_WATCH_NS` | the namespace the controller runs in | `team-a` |

## Next

- [Bounding GPU usage](../admin-gpu-bounding/)
- [Security model](../../reference/security-model.md) — every identity, permission
  and port, and what fails open
- [Install the scaling manager in a namespace](../install-in-namespace/) — what you are
  enabling someone else to do
