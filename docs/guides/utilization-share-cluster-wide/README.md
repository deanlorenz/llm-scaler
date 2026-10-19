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

The order:

1. Confirm what you must already have. The installer does not install these.
2. Run the read-only checks.
3. Install the prerequisites the installer provides. This is the cluster-admin
   phase.
4. Install the scaling manager with the budget, register the models, shadow,
   then activate.

## 1. What you must already have

The installer does not install these. Each line below checks one.

<!-- guide:prerequisites.have start -->
```bash
# Cluster-admin rights, for the one-time setup in step 3. Prints yes.
kubectl auth can-i '*' '*' --all-namespaces

# llm-d serving in two or more namespaces, each with its own InferencePool and EPP. Lists a pool in every model namespace.
kubectl get inferencepools -A

# The models that share run on one GPU product, and every model server requests its GPUs. One product name; a number in GPUS on every pod.
kubectl get nodes -L nvidia.com/gpu.product
kubectl get pods -A -l 'llm-d.ai/role in (decode,prefill)' \
  -o custom-columns='NAMESPACE:.metadata.namespace,POD:.metadata.name,GPUS:.spec.containers[*].resources.limits.nvidia\.com/gpu,NODE:.spec.nodeName'

# Only if the cluster already has KEDA. Found means the installer uses it; NotFound means step 3 installs one.
kubectl get crd scaledobjects.keda.sh
kubectl get deploy -A -l app.kubernetes.io/name=keda-operator \
  -o custom-columns='NAMESPACE:.metadata.namespace,IMAGE:.spec.template.spec.containers[0].image'

# The tools, in a checkout of this repository. Prints nothing when all are there.
for t in kubectl helm git jq yq make openssl; do command -v $t >/dev/null || echo "missing: $t"; done
```
<!-- guide:prerequisites.have end -->

Two more things you must already have. Step 2 checks them:

- **The `flowControl` feature gate on every EPP.** `make check-prereqs
  NAMESPACE=<model namespace>` reports it for each namespace.
- **Model servers that drain on termination**, with a `preStop` hook and
  `terminationGracePeriodSeconds`. A transfer removes a donor pod through an
  ordinary scale-down. `make workload-patch` reports this.

**KEDA.** If the cluster already has KEDA, leave `KEDA_HELM_INSTALL` off (the
default). The installer then uses that KEDA and never installs a second one. If
the cluster has none, step 3 installs it on Kubernetes. On OpenShift, install
the Custom Metrics Autoscaler operator and its KedaController in
`openshift-keda` first.

KEDA 2.18 and 2.19 are supported, and CI tests 2.19. Any other version is
untested but not refused. Before 2.17, ScaledObjects get no fallback. KEDA up
to 2.20.2 has CVE-2026-77524, which is fixed in 2.21.0. Consider upgrading. The
installer prints the version it finds and never stops on it. Details: [KEDA
reference](../../reference/keda.md).

A Prometheus that scrapes the model servers is not on this list. Step 3
deploys one if the cluster has none.

## 2. Check

Every command here only reads. Run them once:

<!-- guide:prerequisites.check start -->
```bash
# Read-only. Tools, your rights, KEDA and the Prometheus the install would use. With no KEDA on the cluster, add KEDA_HELM_INSTALL=true.
make check-prereqs SCOPE=cluster

# Read-only. One model namespace, its EPP included. Run it for each model namespace.
make check-prereqs NAMESPACE=<model namespace>

# Read-only. Every llm-d model server on the cluster, its modelID, the InferencePool in front of it, and any ScaledObject it has. Writes a plan, applies nothing.
make scaledobjects-plan SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml

# Read-only. Whether each model server drains on scale-down, and where it keeps its weights and compile caches. Writes a patch, applies nothing.
make workload-patch SCOPE=cluster
```
<!-- guide:prerequisites.check end -->

### `check-prereqs SCOPE=cluster`

A pass ends with `Preflight passed`. It checks the tools, your rights, the
Prometheus the install will use, and KEDA. It does not check each namespace's
EPP at this scope.

What to fix:

- **KEDA is not installed.** There is no KEDA and no `KEDA_HELM_INSTALL=true`.
  Add `KEDA_HELM_INSTALL=true` to this check and to step 3, or install KEDA
  yourself.
- **A KEDA version line.** The line reports the version of the KEDA it found.
  Each version line is advice, and none fails the check. See [what the
  installer prints](../../reference/keda.md#what-the-installer-prints).
- **A missing tool, or a permission.** Install the tool, or run as a
  cluster admin.

### `check-prereqs NAMESPACE=<model namespace>`

A pass ends with `Preflight passed`. Run it once for each model namespace. If it
reports that `flowControl` is off on the EPP, add this to that EPP's
`EndpointPickerConfig` and restart the EPP:

```yaml
featureGates:
- flowControl
```

### `scaledobjects-plan`

A pass lists every model server it found:

```text
  Discovered llm-d model servers (2):

    APPLY  NAMESPACE  KIND        NAME            MODELID  MIN  MAX  COST  POLICY  FALLBACK
    yes    team-a     Deployment  model-a-decode  model-a  1    10   10.0  -       (+fallback)
    yes    team-b     Deployment  model-b-decode  model-b  1    10   10.0  -       (+fallback)
```

What to fix:

- **A model is missing.** Its pod template needs `llm-d.ai/role: decode` (or
  `prefill`). llm-d's charts set it.
- **`# inferencePool: (none)`** in `share-plan.yaml` means no InferencePool
  selects those pods. Make the pool's selector match the pod labels.
- **The `MODELID` is wrong.** It is read from `--served-model-name`, else
  `--model`. Fix it in the plan, or fix the flag.
- **`APPLY` reads `adopt`.** The model already has a ScaledObject. Adopting it
  points that one at the scaling manager.
- **`(no fallback: KEDA version unknown)`** appears before KEDA is installed.
  Nothing to fix: the apply in step 5 reads KEDA again and adds the fallback.
  On a KEDA older than 2.17 the plan writes no fallback at all.

### `workload-patch`

A pass names no model server. Otherwise it names each model server that is
costly to scale, and writes `wva-workload-patch.yaml` with the fix. On a vLLM
server with a drain hook but no shared caches, it reports:

```text
[WARNING]   team-a/model-a-decode RE-DOWNLOADS ITS WEIGHTS on every scale-up: vllm fetches model-a from Hugging Face, outside every volume it mounts.
[WARNING]   team-a/model-a-decode COMPILES FROM NOTHING on every start: vllm keeps its torch.compile, FlashInfer and Triton caches under /tmp (VLLM_CACHE_ROOT is unset).
[SUCCESS] Wrote wva-workload-patch.yaml (1 workload(s) need something).
```

What to fix:

- **No drain hook** (`preStop` and `terminationGracePeriodSeconds`). Fix this
  first. A transfer removes a donor pod, and without the hook that pod's
  requests are cut off.
- **Weights or compile caches outside a volume.** Each replica that a transfer
  adds starts slower. It works, but it costs start time. The patch says which
  claim to create (`make model-cache`, `make engine-cache`).
- **`No vLLM or SGLang container found`.** It could not tell which container
  is the engine, so it checked nothing for that model. Check the drain hook by
  hand.

Put the fixes in the chart values that own the pod spec. Applying the patch to
the live objects (`WVA_WORKLOAD_PATCH_APPLY=true`) restarts the model servers.
More: [Writing the
patch](../../reference/workload-preparation.md#writing-the-patch-make-workload-patch).

## 3. Install the prerequisites the installer provides

This is the cluster-admin phase of the installer. It runs once and installs
everything except the scaling manager itself:

- the controller's namespace and the monitoring namespace
- the cluster RBAC, including the node read the cluster's-GPUs budget needs
- a Prometheus, only when the cluster has none. An existing one is used.
- KEDA, only with `KEDA_HELM_INSTALL=true` and no KEDA on the cluster
- KEDA's side of the scaler's mutual TLS: a client certificate in KEDA's
  namespace, and a ClusterTriggerAuthentication

Run one of these:

<!-- guide:prerequisites.setup start -->
```bash
# The cluster-admin phase, with the KEDA the cluster already has. Namespaces, cluster RBAC, Prometheus when there is none, and KEDA's side of the scaler TLS.
make setup-prereqs SCOPE=cluster

# The same, on a cluster with no KEDA. Also installs KEDA with Helm. Not on OpenShift.
make setup-prereqs SCOPE=cluster KEDA_HELM_INSTALL=true
```
<!-- guide:prerequisites.setup end -->

It ends with `Prerequisites ready for <namespace>` and one KEDA line:

- `KEDA: already on the cluster` means it used your KEDA.
- `KEDA: installed by this run (Helm)` means it installed one.

What to fix:

- **It cannot tell where KEDA runs.** The scaler then stays on plaintext.
  Re-run with `KEDA_NAMESPACE=<the namespace KEDA runs in>`.
- **It asks you to restart KEDA's operator.** Run the `kubectl rollout restart`
  command it prints. KEDA keeps an old client certificate until it restarts.
- **It warns that no Prometheus may scrape the controller.** This happens when
  you have your own Prometheus. Re-run with `PROMETHEUS_SA=<namespace>/<serviceaccount>`.

More on an existing KEDA: [KEDA reference](../../reference/keda.md#a-keda-the-cluster-already-has).

## 4. Install the scaling manager with one budget

Pick the budget:

- **A quota**: a number of GPUs you set, for example the GPUs your team paid
  for. The models never hold more, even when more are free.
- **The cluster's GPUs** (the physical limiter): every GPU of the product that
  exists and is free. Pick it when there is no number to set, and the models
  may use whatever the nodes have. A GPU the models give up can be taken by any
  other pod on the cluster.

For a quota, `ACCELERATOR` is the product from step 1 and `GPUS` is how many of
them the models may hold together.

<!-- guide:deploy.vars start -->
```bash
# The GPU product the models run on, and how many of its GPUs they may hold together.
export ACCELERATOR=<gpu product>
export GPUS=<gpus>
```
<!-- guide:deploy.vars end -->

<!-- guide:deploy.install start -->
```bash
# The controller only, for every namespace, with one GPU budget they all share. Step 3 installed everything else.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva \
  WVA_LIMITER=quota WVA_QUOTA_SCOPE=cluster WVA_QUOTAS="${ACCELERATOR}=${GPUS}"
```
<!-- guide:deploy.install end -->

`WVA_QUOTA_SCOPE=cluster` is what makes the namespaces share. Without it each
namespace gets its own budget of `GPUS`, and nothing moves between them.

`INSTALL_PHASE=wva` installs only the controller. If step 3 left something
out, it stops and names what is missing.

For the cluster's GPUs instead:

<!-- guide:deploy.physical start -->
```bash
# The same install, with the cluster's GPUs as the budget instead of a quota.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva WVA_LIMITER=gpu-inventory
```
<!-- guide:deploy.physical end -->

The controller must list nodes to count the GPUs, and step 3 granted that. The
budget is per product: the GPUs of that product on every node, less what other
pods hold.

## 5. Register the models

<!-- guide:deploy.register start -->
```bash
# Edit the plan step 2 wrote, then apply it. Nothing scales until this runs.
# edit share-plan.yaml: apply: yes|no|adopt, the modelID, the replica bounds
make scaledobjects-apply SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:deploy.register end -->

In the plan:

- Set `maxReplicas` so the models' maximums add up to more than the budget.
  Otherwise there is nothing to share.
- A model that already has a ScaledObject shows `apply: adopt`. Adopt it rather
  than adding a second.
- `minReplicas` is a floor the optimizer never takes. Keep it at 1 or more.
- Each entry shows the InferencePool it found. `(none)` means no pool selects
  those pods.

## 6. Check the budget

<!-- guide:deploy.check start -->
```bash
# Name one model namespace and the controller's. Checks the budget and that every accelerator resolves.
export NAMESPACE=<one model namespace>
export CONTROLLER_NAMESPACE=workload-variant-autoscaler-system
make utilization-share-check
```
<!-- guide:deploy.check end -->

It prints the accelerator names the controller resolves, and fails if a model
in any namespace has an accelerator it cannot resolve. Run it a minute or so
after step 5: until the controller has sized the models it has logged no
accelerator, and the check says so. If it warns that the quota has no key for
one of them, re-run step 4 with that exact name.

Under the cluster's GPUs it warns `limiter: gpu-inventory and no quota. The
optimizer plans nothing here until the block sets physicalGroups`. That is
expected before shadow; the shadow step below sets it.

## 7. Shadow, then active

<!-- guide:deploy.shadow start -->
```bash
# Shadow. It reports what it would move between namespaces, and moves nothing.
make utilization-share-shadow
```
<!-- guide:deploy.shadow end -->

Under the cluster's GPUs, run this instead:

<!-- guide:deploy.shadowphysical start -->
```bash
# Shadow under the cluster's GPUs. PHYSICAL_GROUPS=true is what makes the namespaces share them.
make utilization-share-shadow PHYSICAL_GROUPS=true
```
<!-- guide:deploy.shadowphysical end -->

Without `PHYSICAL_GROUPS=true` the optimizer forms no group under the
physical limiter, and moves nothing. Activate keeps the setting.

Leave it through each model's busy period, then read the `would rebalance`
lines ([Verification](#verification)). `scope: cluster` in them means the
namespaces share one budget, and `budget` is its size: `GPUS`, or the GPUs of
that product the nodes have.

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
# Remove the scaling manager. The model servers keep serving; KEDA and Prometheus stay.
make undeploy-wva SCOPE=cluster
```
<!-- guide:cleanup.uninstall end -->

The ScaledObjects the install created are deleted, and KEDA puts each model
back to the replica count it had before. Adopted ones are kept and listed.

KEDA is never removed, whether it was yours or step 3 installed it. The
uninstall removes only the ClusterTriggerAuthentication and certificate copy it
wrote in KEDA's namespace. Details: [Install the scaling manager for the whole
cluster](../install-cluster-wide/#cleanup) and [KEDA
reference](../../reference/keda.md#uninstall).

## Details

- **Under the cluster's GPUs**, a GPU that a donor gives up can be taken by a
  pod the scaling manager did not place. The transfer then times out while
  filling, and the receiver shows the blocked reason `release-taken`. Events
  and log lines still say "quota group"; under this limiter the group is the
  cluster's GPUs of one product.
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
- **Your own Prometheus.** The install uses it. Until that Prometheus may scrape
  the controller's `/metrics`, the `wva_*` series are only on the controller's
  endpoint (the metrics block above). Step 3 says how to grant it.
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
