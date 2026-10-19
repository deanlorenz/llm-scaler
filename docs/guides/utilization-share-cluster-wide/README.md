# Share GPUs between models in different namespaces

## Overview

llm-d serves models in several namespaces. This guide puts them under one GPU
budget. When one model gets busy, it takes GPUs that a model in another
namespace is not using. It gives them back when its load drops.
**Experimental.**

No model server restarts. The steps below are the whole path:

1. Check what you already have.
2. Run the preflight.
3. Write the model plan.
4. Install the prerequisites.
5. Install the scaling manager.
6. Register the models.
7. Check the budget.
8. Shadow.
9. Activate.
10. Verify.
11. Clean up.

For one namespace, see [Turn on the utilization-share optimizer,
safely](../utilization-share/). To learn what this costs first, see [the
well-lit path](../../well-lit-paths/utilization-share/).

## What you need

The installer does not install these. Step 1 checks each one.

- A Kubernetes cluster, and cluster-admin rights on it.
- llm-d serving in two or more namespaces. Each namespace has its model
  servers, an EPP and an InferencePool.
- GPU nodes. The models that share must run on one GPU product, and every
  model server must ask for its GPUs (`nvidia.com/gpu`).
- `kubectl`, `helm`, `git`, `jq`, `yq`, `make` and `openssl`, and a checkout of
  this repository.

KEDA and Prometheus are optional. If the cluster has them, the installer uses
them. If it does not, step 4 installs them.

## Step 1: Check what you already have

Run each command in turn.

<!-- guide:steps.tools start -->
```bash
# Prints nothing when every tool is there.
for t in kubectl helm git jq yq make openssl; do command -v $t >/dev/null || echo "missing: $t"; done
```
<!-- guide:steps.tools end -->

You should see nothing. If a tool is listed, install it.

<!-- guide:steps.admin start -->
```bash
# Prints yes for a cluster admin.
kubectl auth can-i '*' '*' --all-namespaces
```
<!-- guide:steps.admin end -->

You should see `yes`. If you see `no`, ask a cluster admin to run steps 2 to 4.

<!-- guide:steps.pools start -->
```bash
# One InferencePool in each model namespace.
kubectl get inferencepools -A
```
<!-- guide:steps.pools end -->

You should see one pool in each model namespace:

```text
NAMESPACE   NAME                 AGE
team-a      optimized-baseline   116s
team-b      optimized-baseline   96s
```

If a namespace is missing, install llm-d there first.

<!-- guide:steps.gpus start -->
```bash
# The GPU product of each node, and the GPUs each model pod asks for.
kubectl get nodes -L nvidia.com/gpu.product
kubectl get pods -A -l 'llm-d.ai/role in (decode,prefill)' \
  -o custom-columns='NAMESPACE:.metadata.namespace,POD:.metadata.name,GPUS:.spec.containers[*].resources.limits.nvidia\.com/gpu,NODE:.spec.nodeName'
```
<!-- guide:steps.gpus end -->

You should see a GPU product on your GPU nodes, and a number in `GPUS` on every
model pod:

```text
NAMESPACE   POD                               GPUS   NODE
team-a      model-a-decode-674b6769c5-blvkx   1      cwp-kind-worker
team-b      model-b-decode-67d87b5789-6vhvr   1      cwp-kind-worker
```

Write down the product the models run on, such as `NVIDIA-A100-PCIE-80GB`.
You need it in step 5. If `GPUS` is empty, add `nvidia.com/gpu` to that model's
resource limits.

<!-- guide:steps.keda start -->
```bash
# Is KEDA installed? NotFound means no.
kubectl get crd scaledobjects.keda.sh
```
<!-- guide:steps.keda end -->

If you see `scaledobjects.keda.sh`, the cluster has KEDA, and the installer will
use it. If you see `NotFound`, the cluster has no KEDA. Steps 2 and 4 then add
`KEDA_HELM_INSTALL=true`, and step 4 installs KEDA. On OpenShift, install the
Custom Metrics Autoscaler operator instead.

KEDA 2.18 and 2.19 are supported. See [KEDA](../../reference/keda.md) for
other versions.

## Step 2: Run the preflight

This command only reads. It finds every namespace that runs llm-d and checks
each one.

<!-- guide:steps.check start -->
```bash
# Read-only. Checks the tools, your rights, KEDA, Prometheus, and every namespace that runs llm-d.
make check-prereqs SCOPE=cluster
```
<!-- guide:steps.check end -->

If step 1 found no KEDA, run this instead:

<!-- guide:steps.checknokeda start -->
```bash
# The same check, on a cluster with no KEDA yet.
make check-prereqs SCOPE=cluster KEDA_HELM_INSTALL=true
```
<!-- guide:steps.checknokeda end -->

You should see every namespace pass, and `Preflight passed` at the end:

```text
[INFO] Checking the 2 namespace(s) with llm-d model servers or an InferencePool:

[INFO] Namespace team-a
[SUCCESS]   Model-server metrics: WVA's own PodMonitor is in place.
[SUCCESS]   EPP metrics: optimized-baseline-epp is scraped by servicemonitor/epp (arrival rate, and so the throughput analyzer, depends on this).
[SUCCESS]   EPP flow control: enabled on optimized-baseline-epp (its config declares featureGates: [flowControl]).
[SUCCESS]   team-a: passed.

[INFO] Namespace team-b
...
[INFO] Namespace summary:
[SUCCESS]   team-a: passed
[SUCCESS]   team-b: passed
[SUCCESS] Namespaces: all 2 passed.
[SUCCESS] Preflight passed for ENVIRONMENT=kubernetes, WVA_SCOPE=cluster, WVA_LIMITER=<unset: keeps the live limiter>
```

If a namespace fails, the check names it and says what is missing:

```text
[INFO] Namespace team-b
[WARNING]   Monitoring not enabled for your llm-d servers; please enable Model-server metrics.
...
[WARNING]         kubectl apply -n team-b -k config/modelserver-metrics
[WARNING]   Router monitoring not enabled; please enable EPP metrics.
[WARNING]     llm-d ships the monitors: kubectl apply -n team-b -k $REPO_ROOT/guides/recipes/observability
[WARNING]   Router queue not enabled; please turn on flow control for EPP.
...
[WARNING]   team-b: failed (model servers not scraped, EPP not scraped, EPP flow control off). Fix what is listed above.

[INFO] Namespace summary:
[WARNING]   team-a: failed (model servers not scraped, EPP not scraped)
[WARNING]   team-b: failed (model servers not scraped, EPP not scraped, EPP flow control off)
[WARNING] Namespaces: 0 passed, 2 failed, 0 could not be checked.
```

Fix each item in that namespace, then run the check again:

- **Model servers not scraped.** Run the `kubectl apply` command the check
  prints for that namespace.
- **EPP not scraped.** Apply llm-d's monitors, as the check prints.
- **EPP flow control off.** Add this to the EPP's `EndpointPickerConfig`.
  Then re-run your llm-d router install and restart the EPP.

  ```yaml
  featureGates:
  - flowControl
  ```

- **`could not check`.** You may not read that namespace. This is not a pass.
  Run the check as a cluster admin.
- **KEDA is not installed.** Add `KEDA_HELM_INSTALL=true`, as shown above.

## Step 3: Write the model plan

<!-- guide:steps.plan start -->
```bash
# Read-only. Lists the model servers and writes a plan. Applies nothing.
make scaledobjects-plan SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:steps.plan end -->

You should see every model server, and `Plan written to share-plan.yaml`:

```text
    APPLY  NAMESPACE  KIND        NAME            MODELID  MIN  MAX  COST  POLICY  FALLBACK
    yes    team-a     Deployment  model-a-decode  model-a  1    10   10.0  -       (no fallback: KEDA version unknown)
    yes    team-b     Deployment  model-b-decode  model-b  1    10   10.0  -       (no fallback: KEDA version unknown)
```

`(no fallback: KEDA version unknown)` is expected before KEDA is installed. Step 6
adds the fallback.

If not:

- **A model is missing.** Its pods need the label `llm-d.ai/role: decode` (or
  `prefill`).
- **`# inferencePool: (none)`** in the plan. No InferencePool selects those
  pods. Make the pool's selector match the pod labels.
- **The `MODELID` is wrong.** Fix it in the plan.

Next, check that each model server can be scaled safely:

<!-- guide:steps.workloads start -->
```bash
# Read-only. Checks that each model server can be scaled safely. Applies nothing.
make workload-patch SCOPE=cluster
```
<!-- guide:steps.workloads end -->

You should see no warnings. If not:

- **No drain hook.** Add a `preStop` hook and `terminationGracePeriodSeconds`
  to the model server. When GPUs move, a pod is removed, and without the hook
  its requests are cut off.
- **Weights or caches outside a volume.** It works, but each new pod starts
  slower. The output says which claim to create.
- **`No vLLM or SGLang container found`.** The check could not tell which
  container is the engine. Check the drain hook by hand.

## Step 4: Install the prerequisites

This is the cluster-admin step. Run it once. It creates the scaling manager's
namespace and cluster permissions. If the cluster has no Prometheus, it
installs one.

If the cluster already has KEDA:

<!-- guide:steps.setup start -->
```bash
# Cluster admin, once. Uses the KEDA the cluster already has.
make setup-prereqs SCOPE=cluster
```
<!-- guide:steps.setup end -->

If step 1 found no KEDA:

<!-- guide:steps.setupkeda start -->
```bash
# The same, on a cluster with no KEDA. Also installs KEDA.
make setup-prereqs SCOPE=cluster KEDA_HELM_INSTALL=true
```
<!-- guide:steps.setupkeda end -->

You should see:

```text
==========================================
 Prerequisites ready for workload-variant-autoscaler-system
==========================================
  ...
    KEDA: installed by this run (Helm)
```

With your own KEDA, the line reads `KEDA: already on the cluster`.

If not:

- **It cannot tell where KEDA runs.** Run it again with
  `KEDA_NAMESPACE=<the namespace KEDA runs in>`.
- **It asks you to restart KEDA's operator.** On a first install, you can skip
  this. If the scaling manager was installed here before, run the
  `kubectl rollout restart` command it prints.
- **It warns that no Prometheus is granted the controller's metrics.** Run it
  again with `PROMETHEUS_SA=<namespace>/<serviceaccount>` of your Prometheus.

## Step 5: Install the scaling manager

Set the GPU product from step 1, and how many of its GPUs the models may hold
together:

<!-- guide:steps.vars start -->
```bash
# The GPU product the models run on, and how many of its GPUs they may hold together.
export ACCELERATOR=<gpu product>
export GPUS=<gpus>
```
<!-- guide:steps.vars end -->

<!-- guide:steps.install start -->
```bash
# The scaling manager, with one GPU budget for every namespace.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva \
  WVA_LIMITER=quota WVA_QUOTA_SCOPE=cluster WVA_QUOTAS="${ACCELERATOR}=${GPUS}"
```
<!-- guide:steps.install end -->

You should see the budget, and `Deployment on kubernetes complete!`:

```text
[WARNING] Scaling is now bounded by every limiter in 'quota' (declared in workload-variant-autoscaler-system/wva-scaling-policy-config):
    - name: install-quota
      type: quota
      scope: cluster
      quotas:
        NVIDIA-A100-PCIE-80GB: 4
...
[SUCCESS] Deployment on kubernetes complete!
```

If not:

- **It says the prerequisites are missing.** Run step 4 first.
- **It warns that the quota key matches no node.** Use the exact product name
  from step 1.

To use every free GPU of the product instead of a fixed number, run this
instead:

<!-- guide:steps.installphysical start -->
```bash
# The same, with the cluster's free GPUs as the budget instead of a number.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva WVA_LIMITER=gpu-inventory
```
<!-- guide:steps.installphysical end -->

## Step 6: Register the models

Open `share-plan.yaml` and edit it:

- Set `maxReplicas` so the models together can want more GPUs than the budget.
  Otherwise there is nothing to share.
- Keep `minReplicas` at 1 or more. The optimizer never goes below it.

Then apply it:

<!-- guide:steps.register start -->
```bash
# Edit share-plan.yaml first: set maxReplicas so the models together can want more than the budget.
make scaledobjects-apply SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:steps.register end -->

You should see one ScaledObject for each model:

```text
[SUCCESS]   team-a/model-a-decode (Deployment) -> ScaledObject model-a-decode-wva (modelID: model-a, 1-4) (+fallback)
[SUCCESS]   team-b/model-b-decode (Deployment) -> ScaledObject model-b-decode-wva (modelID: model-b, 1-4) (+fallback)
[SUCCESS] ScaledObjects: 2 created, 0 adopted, 0 not applied
```

If a model shows `adopt`, it already has a ScaledObject. Keep `adopt`, so the
model does not get a second one.

## Step 7: Check the budget

Wait a minute after step 6, then run:

<!-- guide:steps.budget start -->
```bash
# Name one model namespace and the controller's, then check the budget.
export NAMESPACE=<one model namespace>
export CONTROLLER_NAMESPACE=workload-variant-autoscaler-system
make utilization-share-check
```
<!-- guide:steps.budget end -->

You should see the GPU product, and no `[FAIL]`:

```text
[OK]   limiter: quota; the optimizer shares each quota group
  accelerators the controller resolves: NVIDIA-A100-PCIE-80GB
[OK]   no AcceleratorNotResolved events
  optimizer block: none (today's optimizer). Next: shadow
```

If not:

- **No accelerator yet.** Wait another minute and run it again.
- **The quota has no key for a product.** Run step 5 again with that exact
  name.

## Step 8: Shadow

In shadow, the optimizer reports what it would move. It moves nothing.

<!-- guide:steps.shadow start -->
```bash
# Shadow. It reports what it would move, and moves nothing.
make utilization-share-shadow
```
<!-- guide:steps.shadow end -->

If you chose the cluster's free GPUs in step 5, run this instead:

<!-- guide:steps.shadowphysical start -->
```bash
# Shadow, when the budget is the cluster's GPUs.
make utilization-share-shadow PHYSICAL_GROUPS=true
```
<!-- guide:steps.shadowphysical end -->

You should see:

```text
[OK]   the controller applied version 2623: utilization share is shadow
```

Leave it in shadow through each model's busy period. Then read what it would
have done:

<!-- guide:steps.status start -->
```bash
# The mode and the recent decisions, for one model namespace.
make utilization-share-status NAMESPACE=<model namespace>
```
<!-- guide:steps.status end -->

You should see `mode shadow` and no warnings:

```text
  controller: mode shadow, limiter quota/install-quota, policy version 2623 accepted=true, ...
[OK]   no UtilizationShare warnings
```

`no utilization-share decision logged` is normal while load is flat.

## Step 9: Activate

<!-- guide:steps.activate start -->
```bash
# Let it act in every namespace that shares the budget.
make utilization-share-activate GRANT_SHARED=true
```
<!-- guide:steps.activate end -->

You should see every model namespace granted, and `active`:

```text
[OK]   the controller in workload-variant-autoscaler-system may patch pods in team-a (role/wva-pod-writer)
[OK]   the controller in workload-variant-autoscaler-system may patch pods in team-b (role/wva-pod-writer), which shares this budget
[OK]   the controller applied version 3144: utilization share is active
  for two to three minutes nothing in the group scales (quiet-period); the same follows every controller restart
```

Nothing scales for two to three minutes after this. That is expected.

## Step 10: Verify

See GPUs move between namespaces:

<!-- guide:steps.events start -->
```bash
# GPUs moving between namespaces, oldest first.
kubectl get events -A --sort-by=.lastTimestamp | grep UtilizationShare
```
<!-- guide:steps.events end -->

You should see `UtilizationShareGiving` on the model that gives a GPU:

```text
team-b   108s   Normal   UtilizationShareGiving   deployment/model-b-decode   Utilization share: giving one replica (1 GPUs) to the quota group's unassigned GPUs ...
```

The model that takes it shows `UtilizationShareReceiving`, then
`UtilizationShareReceived`. A move waits for the giver's scale-down window, 300
seconds by default.

Read the optimizer's decisions:

<!-- guide:steps.log start -->
```bash
# The optimizer's decisions, from the controller's log.
kubectl logs -n ${CONTROLLER_NAMESPACE} deploy/wva-controller-manager --tail=-1 \
  | grep -E 'Utilization share: (would rebalance|rebalancing|transfer (started|ended|cancelled)|released, raising the receiver)'
```
<!-- guide:steps.log end -->

You should see `transfer started` lines with `"scope": "cluster"`.

Read the optimizer's metrics:

<!-- guide:steps.metrics start -->
```bash
# The optimizer's metrics, read from the controller.
TOKEN=$(kubectl get secret wva-epp-metrics-token -n ${CONTROLLER_NAMESPACE} -o jsonpath='{.data.token}' | base64 -d)
kubectl port-forward -n ${CONTROLLER_NAMESPACE} svc/wva-controller-manager-metrics-service 8443:8443 >/dev/null & PF=$!
sleep 2
curl -sSfk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics \
  | grep -E '^wva_utilization_share_(mode|spare_gpus|headroom|replicas_to_move|transfers_total)'
kill $PF
```
<!-- guide:steps.metrics end -->

You should see `mode="active"` set to 1, and one `scope="cluster"` series for
the whole cluster:

```text
wva_utilization_share_mode{mode="active"} 1
wva_utilization_share_spare_gpus{accelerator_type="NVIDIA-A100-PCIE-80GB",scope="cluster"} 1
```

Over time, `wva_utilization_share_transfers_total{outcome="done"}` rises as
moves complete.

If nothing moves, see [the troubleshooting
list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## Step 11: Clean up

Turn the optimizer off. The budget stays.

<!-- guide:steps.disable start -->
```bash
# Turn the optimizer off. The budget stays.
make utilization-share-off
```
<!-- guide:steps.disable end -->

You should see `utilization share is off`.

Remove the scaling manager:

<!-- guide:steps.uninstall start -->
```bash
# Remove the scaling manager. The models keep serving; KEDA and Prometheus stay.
make undeploy-wva SCOPE=cluster
```
<!-- guide:steps.uninstall end -->

You should see each ScaledObject removed, and `Undeployment complete!`:

```text
[SUCCESS]   team-a/model-a-decode-wva removed
[SUCCESS]   team-b/model-b-decode-wva removed
...
Left in place (shared; pass UNDEPLOY_SHARED=true to remove):
  - Prometheus, the scaler backend (KEDA, in keda-system), EPP
```

The models keep serving. KEDA returns each model to the replica count it had
before.

## Details

- **What the preflight checks in each namespace.** It checks that something
  scrapes the model servers and the EPP, and that the EPP has flow control on.
  It finds the namespaces by their llm-d model servers and InferencePools.
- **What step 4 creates.** It creates the controller's namespace, its cluster
  roles, a Prometheus when there is none, and KEDA with
  `KEDA_HELM_INSTALL=true`. It also writes KEDA's side of the scaler's mutual
  TLS. See [KEDA](../../reference/keda.md).
- **`WVA_QUOTA_SCOPE=cluster`** is what makes the namespaces share one budget.
  Without it, each namespace gets its own budget of `GPUS`.
- **The cluster's GPUs as the budget.** A GPU the models give up can be taken
  by any other pod. The receiver then shows the blocked reason
  `release-taken`. Events still say "quota group".
- **The budget caps only what this controller places.** It does not stop
  other pods. Back it with a Kubernetes `ResourceQuota` where that matters.
- **Fewer namespaces.** `clusterNamespaces` in the optimizer block limits
  sharing to the namespaces it lists. See the [scaling policy
  reference](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).
- **Without `GRANT_SHARED=true`**, activate grants only `NAMESPACE`. A
  namespace without the grant cannot give GPUs.
- **Registering resizes.** A model with more replicas than its load needs
  drops to what it needs, after its scale-down window. Nothing restarts.
- **Re-running `make deploy-wva`** drops the optimizer block. Run shadow and
  activate again after an upgrade.
- **Uninstall details:** [Install the scaling manager for the whole
  cluster](../install-cluster-wide/#cleanup) and
  [KEDA](../../reference/keda.md#uninstall).

## Next

- [Turn on the utilization-share optimizer, safely](../utilization-share/):
  weights, floors and turning it off.
- [Install the scaling manager for the whole
  cluster](../install-cluster-wide/): the install itself.
- [The well-lit path](../../well-lit-paths/utilization-share/): what it costs
  and what was measured.
