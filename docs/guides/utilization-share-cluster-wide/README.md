# Share GPUs between models in different namespaces

llm-d serves models in several namespaces. This guide puts them under one GPU
budget. When one model is busy, it takes GPUs that a model in another
namespace is not using. It gives them back when its load drops.
**Experimental.**

For one namespace, see [Turn on the utilization-share optimizer,
safely](../utilization-share/). For a scaling manager with no cluster-wide
rights on workloads, see [Share GPUs between namespaces with a
namespace-scoped install](../utilization-share-namespaced/). To learn what this costs first, see [the
well-lit path](../../well-lit-paths/utilization-share/).

## Before you begin

You need:

- A Kubernetes cluster, and cluster-admin rights on it.
- llm-d serving in two or more namespaces. Each namespace has its model
  servers, an EPP and an InferencePool. A fresh llm-d install is enough: the
  installer adds the monitoring llm-d leaves out.
- GPU nodes. The models that share must run on one GPU product, and every
  model server must ask for its GPUs (`nvidia.com/gpu`).
- `kubectl`, `helm`, `git`, `jq`, `yq`, `make` and `openssl`.
- A checkout of this repository. Run every command from its top directory.

KEDA and Prometheus are optional. If the cluster has none, the installer adds
them.

**Time.** Steps 1 to 14 take about 20 minutes. Then leave the optimizer in
shadow for at least one busy period of each model, typically a day. Turning it
on and seeing the first move takes about 10 minutes more.

**What this changes in your cluster.**

- A new namespace, `workload-variant-autoscaler-system`, with the scaling
  manager, and its cluster roles.
- KEDA, in `keda-system`, if the cluster has none. A Prometheus, if the
  cluster has none.
- In each model namespace: a PodMonitor and a ServiceMonitor where nothing
  scrapes the model servers or the EPP yet, one ScaledObject for each model
  server, and a Role that lets the scaling manager mark pods it moves.
- No model server restarts. KEDA now sets each model's replica count.

Every step can be run again. Step 18 turns sharing off. Step 19 removes
everything the installer added, except KEDA and Prometheus.

## Check the cluster

### Step 1: Check your tools

<!-- guide:steps.tools start -->
```bash
# Prints nothing when every tool is there.
for t in kubectl helm git jq yq make openssl; do command -v $t >/dev/null || echo "missing: $t"; done
```
<!-- guide:steps.tools end -->

You should see nothing. If a tool is listed, install it.

### Step 2: Check you are a cluster admin

<!-- guide:steps.admin start -->
```bash
# Prints yes for a cluster admin.
kubectl auth can-i '*' '*' --all-namespaces
```
<!-- guide:steps.admin end -->

You should see `yes`. If you see `no`, ask a cluster admin to run steps 6 and
7, then go on from step 8.

### Step 3: Check llm-d serves in each namespace

<!-- guide:steps.pools start -->
```bash
# One InferencePool in each model namespace.
kubectl get inferencepools -A
```
<!-- guide:steps.pools end -->

You should see one pool in each model namespace:

```text
NAMESPACE   NAME                 AGE
team-a      optimized-baseline   69s
team-b      optimized-baseline   47s
```

If a namespace is missing, install llm-d there first.

### Step 4: Check each model pod asks for GPUs

<!-- guide:steps.podgpus start -->
```bash
# The GPUs each model pod asks for, and the node it runs on.
kubectl get pods -A -l 'llm-d.ai/role in (decode,prefill)' \
  -o custom-columns='NAMESPACE:.metadata.namespace,POD:.metadata.name,GPUS:.spec.containers[*].resources.limits.nvidia\.com/gpu,NODE:.spec.nodeName'
```
<!-- guide:steps.podgpus end -->

You should see a number in `GPUS` on every model pod:

```text
NAMESPACE   POD                               GPUS   NODE
team-a      model-a-decode-57d866f865-4c2t8   1      cwr-kind-worker
team-a      model-a-decode-57d866f865-vhvrl   1      cwr-kind-worker
team-b      model-b-decode-6986b9b7b7-bxt8r   1      cwr-kind-worker
team-b      model-b-decode-6986b9b7b7-qt5x4   1      cwr-kind-worker
```

If `GPUS` is empty, add `nvidia.com/gpu` to that model's resource limits.

### Step 5: Find the GPU product

<!-- guide:steps.products start -->
```bash
# The GPU product of each node.
kubectl get nodes -L nvidia.com/gpu.product
```
<!-- guide:steps.products end -->

You should see a product on the nodes from step 4:

```text
NAME                     STATUS   ROLES           AGE     VERSION   GPU.PRODUCT
cwr-kind-control-plane   Ready    control-plane   2m41s   v1.32.0   NVIDIA-H100-SXM5-80GB
cwr-kind-worker          Ready    <none>          2m27s   v1.32.0   NVIDIA-A100-PCIE-80GB
cwr-kind-worker2         Ready    <none>          2m27s   v1.32.0
```

Write down the product of the nodes the models run on. Option A in step 8
needs it.

## Install

### Step 6: Run the preflight

This command only reads. It finds every namespace that runs llm-d and checks
each one.

<!-- guide:steps.check start -->
```bash
# Read-only. Checks the tools, your rights, KEDA, Prometheus, and every namespace that runs llm-d.
make check-prereqs SCOPE=cluster KEDA_HELM_INSTALL=true
```
<!-- guide:steps.check end -->

You should see every namespace pass, and `Preflight passed` at the end:

```text
[INFO] KEDA: not on the cluster yet; this run installs it (chart kedacore/keda 2.21.0).
[SUCCESS] Prometheus: http://prometheus-operated.monitoring.svc.cluster.local:9090
[INFO] Checking the 2 namespace(s) with llm-d model servers or an InferencePool:

[INFO] Namespace team-a
[INFO]   Model-server metrics: nothing scrapes them yet. Setup adds PodMonitor wva-modelserver-metrics here.
[INFO]   EPP metrics: nothing scrapes optimized-baseline-epp yet. Setup adds ServiceMonitor wva-epp-metrics here.
[WARNING]   EPP flow control: off on optimized-baseline-epp. Not refused: scaling works without it. Two things do not:
...
[SUCCESS]   team-a: passed (setup adds the model-server PodMonitor, setup adds the EPP ServiceMonitor, EPP flow control off: no scale from zero).
...
[INFO] Namespace summary:
[SUCCESS]   team-a: passed (setup adds the model-server PodMonitor, setup adds the EPP ServiceMonitor, EPP flow control off: no scale from zero)
[SUCCESS]   team-b: passed (setup adds the model-server PodMonitor, setup adds the EPP ServiceMonitor, EPP flow control off: no scale from zero)
[SUCCESS] Namespaces: all 2 passed.
...
[SUCCESS] Preflight passed for ENVIRONMENT=kubernetes, WVA_SCOPE=cluster, WVA_LIMITER=<unset: keeps the live limiter>
```

The `EPP flow control: off` warning is expected on a fresh llm-d install. It
does not stop the install; see [Details](#details).

If not:

- **A namespace `failed`.** The lines under that namespace say what is
  missing and how to fix it. Fix it, then run step 6 again.
- **`KEDA is not installed`.** You left out `KEDA_HELM_INSTALL=true`. Run the
  command as written.
- **A namespace `could not check`.** You may not read that namespace. This is
  not a pass. Run the check as a cluster admin.

### Step 7: Install the prerequisites

This is the cluster-admin step. It takes about 3 minutes. It creates the
scaling manager's namespace and cluster roles, installs KEDA if the cluster
has none, and adds the monitors the preflight said were missing.

<!-- guide:steps.setup start -->
```bash
# Cluster admin, once. Installs KEDA only if the cluster has none.
make setup-prereqs SCOPE=cluster KEDA_HELM_INSTALL=true
```
<!-- guide:steps.setup end -->

You should see the monitors it added, and KEDA:

```text
[INFO] Adding the monitors llm-d did not install (DEPLOY_LLMD_MONITORS=false skips this):
[SUCCESS]   team-a: added PodMonitor wva-modelserver-metrics, ServiceMonitor wva-epp-metrics for optimized-baseline-epp
[SUCCESS]   team-b: added PodMonitor wva-modelserver-metrics, ServiceMonitor wva-epp-metrics for optimized-baseline-epp
...
==========================================
 Prerequisites ready for workload-variant-autoscaler-system
==========================================
  ...
    KEDA: installed by this run (Helm)
    Monitors added in team-a: PodMonitor wva-modelserver-metrics, ServiceMonitor wva-epp-metrics for optimized-baseline-epp
    Monitors added in team-b: PodMonitor wva-modelserver-metrics, ServiceMonitor wva-epp-metrics for optimized-baseline-epp
```

With KEDA already on the cluster, the line reads `KEDA: already on the
cluster`. Where something already scrapes a namespace, no monitor is added
there.

It also warns that no Prometheus is granted the controller's own metrics.
Scaling does not need them; they feed the dashboards. To grant them, run step
7 again with `PROMETHEUS_SA=<namespace>/<serviceaccount>` of your Prometheus.

If not:

- **It cannot tell where KEDA runs.** Run it again with
  `KEDA_NAMESPACE=<the namespace KEDA runs in>`.
- **`no Prometheus Operator CRDs, so no monitors can be added`.** Your
  Prometheus does not use the Prometheus Operator. Scrape the model servers
  and the EPP your own way, then run step 6 again.

### Step 8: Choose the budget

The budget is how many GPUs the models may hold together. Pick one option.
Steps 9, 14 and 15 then have a part for each; every other step is the same.

- **Option A: quota.** A GPU count you set. Pick it when the models get a
  fixed share of a cluster that other work also uses.
- **Option B: the cluster's GPUs** (`gpu-inventory`). Whatever the nodes
  have, less what other pods hold. Pick it when the nodes are for these
  models, or when you want every free GPU used.

For option A, set the GPU product from step 5, and how many of its GPUs the
models may hold together:

<!-- guide:steps.vars start -->
```bash
# Option A: quota. The GPU product from step 5, and how many of its GPUs the models may hold together.
export ACCELERATOR=<gpu product>
export GPUS=<gpus>
```
<!-- guide:steps.vars end -->

This prints nothing. Option B needs no settings.

### Step 9: Install the scaling manager

It takes about 2 minutes.

**Option A: quota.**

<!-- guide:steps.install start -->
```bash
# Option A: quota. The scaling manager, with one GPU budget for every namespace.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva \
  WVA_LIMITER=quota WVA_QUOTA_SCOPE=cluster WVA_QUOTAS="${ACCELERATOR}=${GPUS}"
```
<!-- guide:steps.install end -->

You should see every namespace pass, the quota, and `Deployment on kubernetes
complete!`:

```text
[SUCCESS]   team-a: passed (EPP flow control off: no scale from zero)
[SUCCESS]   team-b: passed (EPP flow control off: no scale from zero)
...
[WARNING] Scaling is now bounded by every limiter in 'quota' (declared in workload-variant-autoscaler-system/wva-scaling-policy-config):
    - name: install-quota
      type: quota
      scope: cluster
      quotas:
        NVIDIA-A100-PCIE-80GB: 4
...
[SUCCESS] Deployment on kubernetes complete!
```

**Option B: the cluster's GPUs.**

<!-- guide:steps.installphysical start -->
```bash
# Option B: the cluster's GPUs. The scaling manager, with the free GPUs on the nodes as the budget.
make deploy-wva SCOPE=cluster INSTALL_PHASE=wva WVA_LIMITER=gpu-inventory
```
<!-- guide:steps.installphysical end -->

You should see every namespace pass, the `gpu-inventory` limiter, and
`Deployment on kubernetes complete!`:

```text
[SUCCESS] The controller can list nodes, which the gpu-inventory limiter requires
...
[SUCCESS] Namespaces: all 2 passed.
...
[WARNING] Scaling is now bounded by every limiter in 'gpu-inventory' (declared in workload-variant-autoscaler-system/wva-scaling-policy-config):
    - type: gpu-inventory
...
[SUCCESS] Deployment on kubernetes complete!
```

If not:

- **It says the prerequisites are missing.** Run step 7 first.
- **A namespace `failed` with `make setup-prereqs` in its fix.** Step 7 has
  not added that namespace's monitors yet. Run step 7 again.
- **Option A: it warns that the quota key matches no node.** Use the exact
  product from step 5.

## Register the models

### Step 10: Write the model plan

<!-- guide:steps.plan start -->
```bash
# Read-only. Lists the model servers and writes a plan. Applies nothing.
make scaledobjects-plan SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:steps.plan end -->

You should see every model server, and `Plan written to share-plan.yaml`:

```text
    APPLY  NAMESPACE  KIND        NAME            MODELID  MIN  MAX  COST  POLICY  FALLBACK
    yes    team-a     Deployment  model-a-decode  model-a  1    10   10.0  -       (+fallback)
    yes    team-b     Deployment  model-b-decode  model-b  1    10   10.0  -       (+fallback)

[SUCCESS] Plan written to share-plan.yaml — nothing applied.
```

The plan lets each model grow to `MAX` replicas. Together the models can want
more GPUs than the budget, so there is something to share. To change `MIN` or
`MAX`, edit `share-plan.yaml`. Keep `MIN` at 1 or more.

If not:

- **A model is missing.** Its pods need the label `llm-d.ai/role: decode` (or
  `prefill`).
- **`# inferencePool: (none)`** in the plan. No InferencePool selects those
  pods. Make the pool's selector match the pod labels.

### Step 11: Check the model servers drain safely

<!-- guide:steps.workloads start -->
```bash
# Read-only. Checks that each model server can be scaled safely. Applies nothing.
make workload-patch SCOPE=cluster
```
<!-- guide:steps.workloads end -->

You should see:

```text
[SUCCESS] 2 model server(s) already drain on scale-down and keep their weights on a volume; no vLLM among them compiles from nothing.
```

If it prints a patch, apply it where the model server's pod spec is owned,
usually your llm-d values. When GPUs move, a pod is removed, and without a
drain hook its requests are cut off.

### Step 12: Register the models

<!-- guide:steps.register start -->
```bash
# Applies the plan. One ScaledObject for each model server.
make scaledobjects-apply SCOPE=cluster WVA_DEFAULT_SO_PLAN=share-plan.yaml
```
<!-- guide:steps.register end -->

You should see one ScaledObject for each model:

```text
[SUCCESS]   team-a/model-a-decode (Deployment) -> ScaledObject model-a-decode-wva (modelID: model-a, 1-10) (+fallback)
[SUCCESS]   team-b/model-b-decode (Deployment) -> ScaledObject model-b-decode-wva (modelID: model-b, 1-10) (+fallback)
[SUCCESS] ScaledObjects: 2 created, 0 adopted, 0 not applied
```

If a model shows `adopt`, it already has a ScaledObject. Keep `adopt`, so the
model does not get a second one.

If you installed the scaling manager here before (step 19, then again), you
may instead see:

```text
[WARNING] KEDA's operator still holds its connection to the scaler from before a reinstall, so every call it makes is refused. These ScaledObjects stay unwired, with their HPAs on the CPU default and WVA's decisions reaching nothing, until KEDA's operator restarts:
    team-a/model-a-decode-wva
    team-b/model-b-decode-wva
[WARNING]   Cause: ... Restart KEDA's operator:  kubectl rollout restart deployment/keda-operator -n keda-system
[WARNING]   Not restarted: KEDA's operator is shared with everything else that uses KEDA. The install goes on. ...
```

KEDA keeps one connection per scaler and never closes it. Restart KEDA's
operator with the command it prints. That briefly pauses scaling for
everything else on the cluster that uses KEDA, so it is your call; with
`KEDA_RESTART=true`, step 12 runs the restart itself.

## Turn on sharing

### Step 13: Name a model namespace

The commands from here on take one model namespace. Any of them will do.

<!-- guide:steps.names start -->
```bash
# One model namespace, for the commands that follow, and the controller's.
export NAMESPACE=<one model namespace>
export CONTROLLER_NAMESPACE=workload-variant-autoscaler-system
```
<!-- guide:steps.names end -->

This prints nothing.

### Step 14: Check the budget

Wait a minute after step 12, then run:

<!-- guide:steps.budget start -->
```bash
# Read-only. Is there a budget to share, and does the GPU product resolve.
make utilization-share-check
```
<!-- guide:steps.budget end -->

**Option A: quota.** You should see the quota, the GPU product, and no
`[FAIL]`:

```text
[OK]   limiter: quota; the optimizer shares each quota group
  ...
  accelerators the controller resolves: NVIDIA-A100-PCIE-80GB
[OK]   no AcceleratorNotResolved events
  optimizer block: none (today's optimizer). Next: shadow
```

**Option B: the cluster's GPUs.** You should see the GPU product, no `[FAIL]`,
and one expected `[WARN]`: the optimizer plans nothing for this limiter until
step 15 turns on physical groups.

```text
[WARN] limiter: gpu-inventory and no quota. The optimizer plans nothing here until the block sets physicalGroups: run shadow with --physical-groups (PHYSICAL_GROUPS=true)
  ...
  accelerators the controller resolves: NVIDIA-A100-PCIE-80GB
[OK]   no AcceleratorNotResolved events
  optimizer block: none (today's optimizer). Next: shadow
```

If not:

- **`the controller has logged no accelerator yet`.** Wait another minute and
  run it again.
- **Option A: the quota has no key for a product.** Run steps 8 and 9 again
  with that exact name.

### Step 15: Turn on shadow

In shadow, the optimizer reports what it would move. It moves nothing.

**Option A: quota.**

<!-- guide:steps.shadow start -->
```bash
# Option A: quota. Shadow: it reports what it would move, and moves nothing.
make utilization-share-shadow
```
<!-- guide:steps.shadow end -->

**Option B: the cluster's GPUs.** `PHYSICAL_GROUPS=true` lets the optimizer
plan across the nodes' GPUs.

<!-- guide:steps.shadowphysical start -->
```bash
# Option B: the cluster's GPUs. Shadow, planning across the nodes' GPUs; it moves nothing.
make utilization-share-shadow PHYSICAL_GROUPS=true
```
<!-- guide:steps.shadowphysical end -->

For either, you should see:

```text
  wrote the optimizer block (shadow) into workload-variant-autoscaler-system/wva-scaling-policy-config
[OK]   the controller applied version 2980: utilization share is shadow
```

Leave it in shadow through each model's busy period. Then read what it would
have done:

<!-- guide:steps.status start -->
```bash
# Read-only. The mode and the recent decisions.
make utilization-share-status
```
<!-- guide:steps.status end -->

You should see `mode shadow` and no warnings. With option A the limiter reads
`quota/install-quota`:

```text
  policy: workload-variant-autoscaler-system/wva-scaling-policy-config; optimizer block: shadow
  controller: mode shadow, limiter quota/install-quota, policy version 2980 accepted=true, ...
  log: ... Utilization share: would rebalance (shadow) {"accelerator": "NVIDIA-A100-PCIE-80GB", "scope": "cluster", "budget": 4, ...
  events counted in the namespaces of this budget: team-a team-b
  UtilizationShareGiving events: 0
  UtilizationShareReceiving events: 0
  UtilizationShareReceived events: 0
[OK]   no UtilizationShare warnings
```

With option B, the block and the limiter read:

```text
  policy: workload-variant-autoscaler-system/wva-scaling-policy-config; optimizer block: shadow, physical groups
  controller: mode shadow, limiter inventory/gpu-inventory, policy version 4746 accepted=true, ...
```

Run step 14 again and option B's `[WARN]` is gone:
`[OK]   limiter: gpu-inventory, and the block plans physical groups`.

### Step 16: Activate

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
  wrote the optimizer block (active) into workload-variant-autoscaler-system/wva-scaling-policy-config
[OK]   the controller applied version 3184: utilization share is active
  for two to three minutes nothing in the group scales (quiet-period); the same follows every controller restart
```

Nothing scales for two to three minutes after this. That is expected.

### Step 17: See GPUs move

Wait about five minutes after step 16, while one model is busy and another is
not. Then run:

<!-- guide:steps.events start -->
```bash
# GPUs moving between namespaces, oldest first.
kubectl get events -A --sort-by=.lastTimestamp | grep UtilizationShare
```
<!-- guide:steps.events end -->

You should see `UtilizationShareGiving` on the model that gives a GPU, and
`UtilizationShareReceiving` on the model that takes it:

```text
team-b   4m46s   Normal   UtilizationShareGiving      deployment/model-b-decode   Utilization share: giving one replica (1 GPUs) to a model in another namespace of its quota group ...
team-a   4m46s   Normal   UtilizationShareReceiving   deployment/model-a-decode   Utilization share: receiving 1 GPUs from a model in another namespace of its quota group ...
```

The status command from step 15 now counts them, across every namespace of
the budget:

```text
  events counted in the namespaces of this budget: team-a team-b
  UtilizationShareGiving events: 1 (team-b 1)
  UtilizationShareReceiving events: 1 (team-a 1)
```

A move waits for the giver's scale-down window, 300 seconds by default. If
you see nothing after ten minutes, see [the troubleshooting
list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

The controller's log has the same moves:

<!-- guide:steps.log start -->
```bash
# The optimizer's decisions, from the controller's log.
kubectl logs -n ${CONTROLLER_NAMESPACE} deploy/wva-controller-manager --tail=-1 \
  | grep -E 'Utilization share: (rebalancing|transfer (started|ended|cancelled))'
```
<!-- guide:steps.log end -->

You should see a `transfer started` line with `"scope": "cluster"`, a donor
in one namespace and a receiver in another:

```text
... Utilization share: transfer started {"accelerator": "NVIDIA-A100-PCIE-80GB", "scope": "cluster", ..., "donor": "team-b/model-b/decode", "receiver": "team-a/model-a/decode", ...
```

## Undo

### Step 18: Turn the optimizer off

This is the rollback. The scaling manager keeps scaling each model on its own;
the budget stays.

<!-- guide:steps.disable start -->
```bash
# Turn the optimizer off. The budget stays.
make utilization-share-off
```
<!-- guide:steps.disable end -->

You should see `utilization share is off`.

### Step 19: Remove the scaling manager

<!-- guide:steps.uninstall start -->
```bash
# Remove the scaling manager. The models keep serving; KEDA and Prometheus stay.
make undeploy-wva SCOPE=cluster
```
<!-- guide:steps.uninstall end -->

You should see each ScaledObject and the added monitors removed, and
`Undeployment complete!`:

```text
[INFO] Removing the ScaledObjects this installer created:
[SUCCESS]   team-a/model-a-decode-wva removed
[SUCCESS]   team-b/model-b-decode-wva removed
[INFO]   KEDA restores each workload to its pre-autoscaling replica count as these go.
[SUCCESS] WVA uninstalled
[INFO] Removed the monitors setup added in the llm-d namespaces (label llm-d.ai/applied-by=wva-setup-prereqs).
[INFO] KEDA's operator is not restarted (it is shared; KEDA_RESTART=true lets the uninstall do it). It keeps its connection to the removed scaler. If you install this scaler in workload-variant-autoscaler-system again, the install finds that connection and prints the restart. To restart it now:  kubectl rollout restart deployment/keda-operator -n keda-system
[SUCCESS] Undeployment complete!
```

The models keep serving. KEDA returns each one to the replica count it had
before step 12. KEDA and Prometheus stay. To remove them too, add
`UNDEPLOY_SHARED=true`.

KEDA's operator is not restarted, because other workloads share it. If you
install again here later, step 12 tells you to restart it then.

## Details

- **What the preflight checks in each namespace.** That something scrapes the
  model servers and the EPP, and whether the EPP has flow control on. It finds
  the namespaces by their llm-d model servers and InferencePools.
- **The monitors step 7 adds.** A PodMonitor for the model servers, and a
  ServiceMonitor for the EPP with a token to read its metrics. Only where
  nothing scrapes them yet. Each is labelled
  `llm-d.ai/applied-by=wva-setup-prereqs`; step 19 removes exactly those.
  `DEPLOY_LLMD_MONITORS=false` turns this off.
- **EPP flow control.** llm-d ships it off. Sharing works without it. Without
  it, a model at zero replicas never wakes, and the arrival rate reads low
  while a model is saturated. The preflight warns and goes on. To turn it on,
  add `featureGates: [flowControl]` to the EPP's `EndpointPickerConfig` and
  re-run your llm-d router install.
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
- **Upgrading.** Re-running step 9 drops the optimizer block. Run steps 15
  and 16 again after an upgrade.
- **OpenShift.** Install the Custom Metrics Autoscaler operator instead of
  KEDA, and drop `KEDA_HELM_INSTALL=true`.
- **KEDA versions.** 2.18, 2.19 and 2.21 are supported. See
  [KEDA](../../reference/keda.md).
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
