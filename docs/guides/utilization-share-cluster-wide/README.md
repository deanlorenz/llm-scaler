# Share GPUs between models in different namespaces

## Overview

llm-d already serves models in several namespaces. This guide installs one
scaling manager for the whole cluster, puts those models under one GPU budget,
and turns on the utilization-share optimizer. A model whose load rises then
takes GPUs that a model in another namespace is not using, and gives them back
when the load turns. **Experimental.**

Nothing in it restarts a model server. Registering a model hands its replica
count to KEDA; from then on the scaling manager sizes it.

For one namespace, or to read what this costs first, see [Turn on the
utilization-share optimizer, safely](../utilization-share/) and [the well-lit
path](../../well-lit-paths/utilization-share/).

## Prerequisites

- [ ] Cluster-admin rights.
- [ ] **llm-d serving in two or more namespaces.** Each namespace has its model
  servers and its own EPP and InferencePool, as llm-d's guides install them.
- [ ] **Every model server requests its GPUs** (`nvidia.com/gpu` in its
  limits). The budget counts these.
- [ ] **The models that share run on one GPU product.** The budget is per
  product.
- [ ] **Model servers drain on termination** (a `preStop` hook and
  `terminationGracePeriodSeconds`). A transfer removes a donor pod through an
  ordinary scale-down.
- [ ] A Prometheus that scrapes the model servers. With none on the cluster,
  the install deploys one, which picks up llm-d's PodMonitors.
- [ ] `kubectl`, `helm`, `jq`, `yq` and `make`, in a checkout of this
  repository.

Check the starting state:

<!-- guide:prerequisites start -->
```bash
# Every model server llm-d runs, with its namespace, the GPUs it requests and its node.
kubectl get pods -A -l 'llm-d.ai/role in (decode,prefill)' \
  -o custom-columns='NAMESPACE:.metadata.namespace,POD:.metadata.name,GPUS:.spec.containers[*].resources.limits.nvidia\.com/gpu,NODE:.spec.nodeName,READY:.status.containerStatuses[*].ready'

# One InferencePool, behind its own EPP, in each model namespace.
kubectl get inferencepools.inference.networking.k8s.io -A

# The GPU product each node carries. The budget is per product.
kubectl get nodes -L nvidia.com/gpu.product
```
<!-- guide:prerequisites end -->

Expect model pods in at least two namespaces, all `true`, each with a GPU
count, and one pool per namespace.

## Installation Instructions

### 1. Install with one budget for every namespace

`ACCELERATOR` is the product from the last command above. `GPUS` is how many
of them the models may hold together.

<!-- guide:deploy.vars start -->
```bash
# The GPU product the models run on, and how many of its GPUs they may hold together.
export ACCELERATOR=<gpu product>
export GPUS=<gpus>
```
<!-- guide:deploy.vars end -->

<!-- guide:deploy.install start -->
```bash
# One controller for every namespace, with one GPU budget they all share. Installs KEDA; on OpenShift, drop KEDA_HELM_INSTALL.
make deploy-wva SCOPE=cluster KEDA_HELM_INSTALL=true \
  WVA_LIMITER=quota WVA_QUOTA_SCOPE=cluster WVA_QUOTAS="${ACCELERATOR}=${GPUS}"
```
<!-- guide:deploy.install end -->

`WVA_QUOTA_SCOPE=cluster` is what makes the namespaces share. Without it each
namespace gets its own budget of `GPUS`, and nothing moves between them.

### 2. Register the models

<!-- guide:deploy.register start -->
```bash
# List every model server on the cluster in a plan, edit it, then apply it. Nothing scales until this runs.
make scaledobjects-plan SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
# edit share-plan.yaml: apply: yes|no|adopt, the modelID, the replica bounds
make scaledobjects-apply SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:deploy.register end -->

In the plan:

- Set `maxReplicas` so the models' maximums add up to more than `GPUS`.
  Otherwise there is nothing to share.
- A model that already has a ScaledObject shows `apply: adopt`. Adopt it rather
  than adding a second.
- `minReplicas` is a floor the optimizer never takes. Keep it at 1 or more.
- Each entry shows the InferencePool it found. `(none)` means no pool selects
  those pods.

### 3. Check the budget

<!-- guide:deploy.check start -->
```bash
# Name one model namespace and the controller's. Checks the budget and that every accelerator resolves.
export NAMESPACE=<one model namespace>
export CONTROLLER_NAMESPACE=workload-variant-autoscaler-system
make utilization-share-check
```
<!-- guide:deploy.check end -->

It prints the accelerator names the controller resolves. Run it a minute or
so after step 2: until the controller has sized the models it has logged no
accelerator, and the check says so. If it warns that the quota has no key for
one of them, re-run step 1 with that exact name.

### 4. Shadow, then active

<!-- guide:deploy.shadow start -->
```bash
# Shadow. It reports what it would move between namespaces, and moves nothing.
make utilization-share-shadow
```
<!-- guide:deploy.shadow end -->

Leave it through each model's busy period, then read the `would rebalance`
lines ([Verification](#verification)). `scope: cluster` in them means the
namespaces share one budget.

<!-- guide:deploy.active start -->
```bash
# Let it act, and let the controller move pods in every namespace that shares the budget.
make utilization-share-activate GRANT_SHARED=true
```
<!-- guide:deploy.active end -->

`GRANT_SHARED=true` lets the controller mark donor pods in every namespace
whose models share the budget, not only in `NAMESPACE`, and names each one.
For two to three minutes after this nothing in the group scales
(`quiet-period`).

## Verification

<!-- guide:verify.status start -->
```bash
# The mode, recent decisions and marked pods. Run it for each model namespace.
make utilization-share-status NAMESPACE=<model namespace>
```
<!-- guide:verify.status end -->

<!-- guide:verify.events start -->
```bash
# Transfers between namespaces, from both ends, oldest first.
kubectl get events -A --sort-by=.lastTimestamp | grep UtilizationShare
```
<!-- guide:verify.events end -->

A transfer shows on the donor as `UtilizationShareGiving` (`giving one replica
... to a model in another namespace of its quota group`), on the receiver as
`UtilizationShareReceiving`, and as `UtilizationShareReceived` once the
receiver holds the GPUs.

<!-- guide:verify.log start -->
```bash
# The optimizer's decisions, from the controller's log.
kubectl logs -n ${CONTROLLER_NAMESPACE} deploy/wva-controller-manager --tail=-1 \
  | grep -E 'Utilization share: (would rebalance|rebalancing|transfer (started|ended|cancelled)|released, raising the receiver)'
```
<!-- guide:verify.log end -->

<!-- guide:verify.metrics start -->
```bash
# The optimizer's metrics, read from the controller.
TOKEN=$(kubectl get secret wva-epp-metrics-token -n ${CONTROLLER_NAMESPACE} -o jsonpath='{.data.token}' | base64 -d)
kubectl port-forward -n ${CONTROLLER_NAMESPACE} svc/wva-controller-manager-metrics-service 8443:8443 >/dev/null & PF=$!
sleep 2
curl -sSfk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics \
  | grep -E '^wva_utilization_share_(mode|spare_gpus|headroom|replicas_to_move|transfers_total)'
kill $PF
```
<!-- guide:verify.metrics end -->

What to look for:

- `wva_utilization_share_mode{mode="active"} 1`.
- `wva_utilization_share_spare_gpus{scope="cluster",...}`: one series for the
  whole cluster, not one per namespace.
- `wva_utilization_share_headroom` below 0: that model is short of GPUs.
- `wva_utilization_share_transfers_total{outcome="done"}` rising: transfers
  land. `cancelled` means demand turned before the donor released its pod;
  that is normal when load swings fast.

A transfer waits for the donor's scale-down window, 300 s by default. Nothing
moving? Work down [the troubleshooting
list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## Cleanup

<!-- guide:cleanup.disable start -->
```bash
# Turn the optimizer off. The budget stays.
make utilization-share-off
```
<!-- guide:cleanup.disable end -->

<!-- guide:cleanup.uninstall start -->
```bash
# Remove the scaling manager. The model servers keep serving.
make undeploy-wva SCOPE=cluster
```
<!-- guide:cleanup.uninstall end -->

The ScaledObjects the install created are deleted, and KEDA puts each model
back to the replica count it had before. Adopted ones are kept and listed.
Details: [Install the scaling manager for the whole
cluster](../install-cluster-wide/#cleanup).

## Details

- **One budget per controller.** The quota caps what this controller places.
  It does not stop pods the scaling manager did not create. Back it with a
  Kubernetes `ResourceQuota` where that matters.
- **The quota key.** Use the product name the check prints. A key no node
  carries is a budget of zero, and the install warns about it.
- **Fewer namespaces first.** `clusterNamespaces` in the optimizer block limits
  sharing to the namespaces it lists. See the [scaling policy
  reference](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).
- **Without `GRANT_SHARED=true`**, activate grants only `NAMESPACE` and prints
  the command to run in each other namespace. A namespace without the grant
  cannot give GPUs.
- **Your own Prometheus.** The install uses it, and warns that no Prometheus
  may scrape the controller's `/metrics`. Until one may, the `wva_*` series
  are only on the controller's endpoint (the metrics block above). To grant
  it: `PROMETHEUS_SA=<namespace>/<serviceaccount> make setup-prereqs
  SCOPE=cluster`.
- **Registering resizes.** Once a model is registered, a model with more
  replicas than its load needs drops to what it needs, after its scale-down
  window. Nothing restarts; a replica that goes drains first.
- **Re-running `make deploy-wva`** drops the optimizer block. Run shadow and
  activate again after an upgrade.

## Next

- [Turn on the utilization-share optimizer, safely](../utilization-share/):
  weights, floors and turning it off.
- [Install the scaling manager for the whole
  cluster](../install-cluster-wide/): the install itself.
- [The well-lit path](../../well-lit-paths/utilization-share/): what it costs
  and what was measured.
