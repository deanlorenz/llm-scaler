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
| `wva-kueue-reader-role`: get, list on clusterqueues, resourceflavors | a quota limiter entry has `kueue.enabled: true` | `make setup-prereqs`; `make enable-physical-limiter WVA_QUOTA_KUEUE=true` | logs `external quota source unreadable` every cycle and applies the static quota entry alone |
| RoleBinding to `wva-policy-reader` in the policy namespace: get, list, watch on its ConfigMaps | the cluster has a policy namespace (`wva-policy`, or one named by a `wva.llmd.ai/policy-namespace` label) and the controller reads policy from it | `make enable-physical-limiter` | the controller **refuses to start** rather than run unbounded |
| get on namespaces, cluster-wide | on **Kubernetes**, for a controller that manages its own namespace to see the policy-namespace label or `wva-policy` at all | nothing yet — grant it by hand (being fixed) | the label and `wva-policy` are not seen and the controller uses its own ConfigMap; it logs `Could not read the managed Namespace` |

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
which you own; `wva-policy` does not apply to it.

Pass `WVA_WATCH_NS` to **both** commands. The controller needs permissions in the
namespace it manages, and writing RBAC into someone else's namespace is an
admin's act by definition — so `setup-prereqs` creates that Role and RoleBinding,
and the uninstall removes them. Without it the controller starts, cannot read the
namespace it was pointed at, and crash-loops on its first ConfigMap read.

## Verification

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
