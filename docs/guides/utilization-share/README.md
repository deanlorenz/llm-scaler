# Turn on the utilization-share optimizer, safely

## Overview

The utilization-share optimizer shares one GPU quota between models. Each model
gets the GPUs its load needs, the rest is shared by weight, and a model whose
demand rises takes GPUs another model does not need. Whether it suits your
fleet is on the well-lit path: [Share one GPU quota between
models](../../well-lit-paths/utilization-share/). **Experimental.**

This guide turns it on in two steps, **shadow** (reports, moves nothing) then
**active**, and back off. It is one block in the scaling policy, read live.

## Prerequisites

- [ ] Two or more models scaled by the scaling manager, under a **limiter**: a
  quota (declared, or [read from Kueue](../../well-lit-paths/kueue-bounded-quotas/)),
  or the [physical GPU limiter](#under-the-physical-gpu-limiter). No models yet?
  See [a trial setup](#a-trial-setup).
- [ ] `kubectl`, `jq`, `yq` and `make`, in a checkout of this repository.

<!-- guide:prerequisites start -->
```bash
# The models' namespace. Also export CONTROLLER_NAMESPACE if the controller runs elsewhere.
export NAMESPACE=<namespace>

# Check there is a limiter and every variant's accelerator resolves. Fails with the reason if not.
make utilization-share-check
```
<!-- guide:prerequisites end -->

## Installation Instructions

**Step 1: shadow.** Leave it long enough to cover each model's peak, then read
what it would have done ([Verification](#verification)).

<!-- guide:deploy.shadow start -->
```bash
# Turn the optimizer on in shadow mode. It reports what it would move, and moves nothing.
make utilization-share-shadow
```
<!-- guide:deploy.shadow end -->

**Step 2: active.**

<!-- guide:deploy.active start -->
```bash
# Let it act. Only after reading what shadow would do.
make utilization-share-activate
```
<!-- guide:deploy.active end -->

For two to three minutes after this, and after every controller restart, nothing
in the group scales and its models show `quiet-period`.

Activate also lets the controller patch pods in `NAMESPACE` (Role
`wva-pod-writer`): the optimizer changes pods' deletion cost, and the
controller's ClusterRole only reads pods. You need the right to create Roles in
that namespace. If models in several namespaces share one budget, run activate
once for each of them. Activate grants only in `NAMESPACE`. It warns about each
other namespace whose models this controller scales and where the grant is
missing, and prints the command to run there.

On a cluster-wide quota, `clusterNamespaces` lets you act on a few namespaces
first. Weights, floors and every other key are in the [scaling policy
reference](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).

## Verification

<!-- guide:verify.status start -->
```bash
# Show the mode, recent decisions, transfer events, warnings and marked pods.
make utilization-share-status
```
<!-- guide:verify.status end -->

Then read the metrics in your Prometheus (Grafana Explore or its API). Scope
every query to your namespace; a shared Prometheus holds other tenants' models.

```promql
# The mode: exactly one of off, invalid, shadow, active is 1
wva_utilization_share_mode{namespace="<namespace>"} == 1
# What a move would fix, and how many replicas
wva_utilization_share_actionable{exported_namespace="<namespace>"} == 1
wva_utilization_share_replicas_to_move{scope="<namespace>"}
# Below 0: the model is short of GPUs
wva_utilization_share_headroom{exported_namespace="<namespace>"}
# Once active: finished transfers by outcome (done is healthy)
sum by (outcome) (increase(wva_utilization_share_transfers_total{scope="<namespace>"}[1h]))
# Models held back, and why
wva_model_scaling_blocked{exported_namespace="<namespace>"} == 1
```

`exported_namespace` is the model's namespace, `namespace` on the mode is the
controller's, and `scope` is the quota's. On a namespace-scoped install all
three are yours.

If you can read Secrets in the controller's namespace, you can also read the
controller's endpoint directly. A 401 or 403 means use Prometheus.

<!-- guide:verify.endpoint start -->
```bash
# Read the optimizer metrics from the controller. Needs Secrets read in the controller's namespace.
CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-$NAMESPACE}
TOKEN=$(kubectl get secret wva-epp-metrics-token -n $CONTROLLER_NAMESPACE -o jsonpath='{.data.token}' | base64 -d)
kubectl port-forward -n $CONTROLLER_NAMESPACE svc/wva-controller-manager-metrics-service 8443:8443 >/dev/null & PF=$!
sleep 2
curl -sSfk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics \
  | grep -E '^wva_(utilization_share_(mode|actionable|replicas_to_move|headroom|spare_gpus|transfers_total)|model_scaling_blocked)'
kill $PF
```
<!-- guide:verify.endpoint end -->

**Quiet but healthy:**

| You see | It means |
| --- | --- |
| no `would rebalance` log line in shadow | nothing would move; the line is written only when the moves change |
| no `headroom` series for a model | it has no load |
| `floor-pinned` on an idle model | it is at its `minReplicaCount`, which is never taken |
| `quiet-period` for two to three minutes | the group holds still after activation and after a restart |
| `spare_gpus` below 0 | the quota cannot cover every model's need; no move fixes that |
| `invalid` mode | the block did not validate, or the policy has no `limiters:` list; the log says which |

It evaluates but never moves anything? Work down [the troubleshooting
list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## Cleanup

<!-- guide:cleanup.disable start -->
```bash
# Turn the optimizer off. Returns to today's optimizer; the limiter is untouched.
make utilization-share-off
```
<!-- guide:cleanup.disable end -->

Turn it off the same way before downgrading to a version without the
optimizer. That version leaves its marks on the pods ([how to remove
them](../../reference/troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade)).

## Under the physical GPU limiter

**A cluster-admin task:** the policy is the cluster's, in `wva-policy`. With a
`gpu-inventory` limiter and no quota, the budget is every GPU of a type on the
cluster. Three things differ:

- **Add `PHYSICAL_GROUPS=true` to the shadow step.** Without
  `physicalGroups: true` the optimizer plans no group and moves nothing. The
  check warns about this.
- **It turns the optimizer on for every self-managed controller**, not just
  yours: they all read `wva-policy`. Admin-owned controllers (cluster-scoped or
  out-of-reach) read their own ConfigMap. A controller reads a policy in another
  namespace only at start, so add `RESTART=1`, or the step stops and says the
  block was written but not read.
- **Spare GPUs are shared with everything on the cluster.** A GPU a donor
  releases can be taken by a pod the scaling manager did not place. That
  transfer ends `release-taken`.

Only the quota benchmarks have been measured. The physical limiter has not been
run with the optimizer on a cluster.

## Details

### A trial setup

Two 8B models, one GPU per replica, under one namespace quota of 4. Use the
accelerator name your nodes carry (`H200`, `NVIDIA-H200`, ...). The check above
reads the exact name the controller uses. [The
runbook](../benchmarking/two-model-utilization-share.md) has the detail.

<!-- guide:trial.setup start -->
```bash
# Build a trial setup, two 8B models under one namespace quota of 4 GPUs.
export BENCHMARK_NAMESPACE=<namespace>
WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-preflight
WVA_LIMITER=quota WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-standup
make benchmark-two-model-verify
```
<!-- guide:trial.setup end -->

To use your own models instead: [install in a namespace](../install-in-namespace/)
with `WVA_LIMITER=quota WVA_QUOTAS='<accelerator>=<n>'`, then [install a
model](../install-small-model/) twice.

### What each step does

- **`utilization-share-check`** names the limiter in the policy and the one the
  controller runs. It fails on no limiter, an unresolved accelerator, or anything
  it could not read. It warns when a quota has no key for an accelerator the
  controller logs; the key must match the logged name exactly.
- **Finding the policy.** The steps read the policy namespace from the
  controller's `wva-controller-status` ConfigMap, else its log, else the
  namespace's labels and `wva-policy`. `POLICY_NAMESPACE` names it when none of
  those can; a value that disagrees with the controller is refused.
- **`utilization-share-shadow`** writes the optimizer block with `shadow: true`,
  keeping everything else, then waits for the controller to read it and checks
  it was not refused. It refuses a policy with no limiter or with another
  optimizer block.
- **`utilization-share-off`** goes back to shadow first, so the controller
  removes the transfer marks it wrote, waits for them to clear, then removes the
  block. If the marks do not clear, or the pods cannot be read, it stops with
  the block in shadow.
- Each step runs `deploy/utilization-share.sh`; `--help` lists its options.

### Who can read the controller's endpoint

The endpoint checks the caller against a cluster-wide `/metrics` grant, which
the `wva-epp-metrics-token` carries once the admin's setup has run. In the
tenant-owned shape that token is in your namespace. In the out-of-reach shape it
is in the admin's namespace, so a tenant uses Prometheus.

## Next

- What it costs, when not to use it, and what was measured:
  [the well-lit path](../../well-lit-paths/utilization-share/).
- Every metric, outcome and blocked reason:
  [utilization-share metrics](../../reference/prometheus.md#utilization-share-optimizer-metrics);
  the alerts: [monitoring](../../reference/monitoring.md#the-metrics-that-answer-specific-questions).
