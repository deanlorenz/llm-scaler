# Turn on the utilization-share optimizer, safely

## Overview

The utilization-share optimizer shares one GPU quota between the models under
it: each model gets the GPUs its load needs, the rest is headroom shared by
weight, and when one model's demand rises it takes GPUs another model does not
need. Whether it suits your fleet, and what it costs, is on the well-lit path:
[Share one GPU quota between models](../../well-lit-paths/utilization-share/).

This guide turns it on in two steps -- **shadow**, where it computes and reports
but moves nothing, then **active** -- and back out. It is one block in the
scaling policy, read live. **Experimental.**

## Prerequisites

Two or more models scaled by the scaling manager, under a **limiter**: a
quota (declared, or [read from Kueue](../../well-lit-paths/kueue-bounded-quotas/)),
or the physical GPU inventory
([under the physical limiter](#under-the-physical-gpu-limiter) has what
differs). You need `kubectl`, `jq`, `yq` and `make` in a checkout of this
repository. If you do not have the models yet, the shortest route is the
benchmark's two-model standup, which installs the scaling manager in your
namespace with the quota and two models
([the runbook](../benchmarking/two-model-utilization-share.md) has the detail).
To build it from your own models instead:
[install in a namespace](../install-in-namespace/) with
`WVA_LIMITER=quota WVA_QUOTAS='<accelerator>=<n>'`, then
[install a model](../install-small-model/) twice.

<!-- guide:prerequisites.setup start -->
```bash
# Only if you do not run two models under one quota yet. This builds a trial
# setup: two 8B models, 1 GPU per replica, under one namespace quota of 4.
# Use the accelerator name your nodes carry (H200, NVIDIA-H200, ...); the
# check below reads the exact name the controller uses.
export BENCHMARK_NAMESPACE=<namespace>
WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-preflight
WVA_LIMITER=quota WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-standup
make benchmark-two-model-verify
```
<!-- guide:prerequisites.setup end -->

Every command below reads one variable:

<!-- guide:prerequisites.vars start -->
```bash
# The models' namespace. On a namespace-scoped install the controller and
# its policy are there too; otherwise also export CONTROLLER_NAMESPACE and
# POLICY_NAMESPACE.
export NAMESPACE=<namespace>
```
<!-- guide:prerequisites.vars end -->

Check there is a budget to share:

<!-- guide:prerequisites.check start -->
```bash
# Is there a budget to share, and does every variant's accelerator
# resolve. It names the limiter in the policy and the one the controller
# runs, and fails on no limiter, an unresolved accelerator, or anything it
# could not read. It warns when a quota has no key for an accelerator the
# controller logs: that key must be the logged name exactly.
make utilization-share-check
```
<!-- guide:prerequisites.check end -->

## Installation Instructions

Each step is one `make` target that runs `deploy/utilization-share.sh`; run
the script with `--help` for its options.

**Step 1: shadow.** Leave it in shadow long enough to cover each model's peak,
then read what it would have done ([Verification](#verification)).

<!-- guide:deploy.shadow start -->
```bash
# Writes the optimizer block with shadow: true, keeping everything else in
# the policy, then waits for the controller to read it and checks it was
# not refused. It refuses a policy with no limiter or with another
# optimizer block.
make utilization-share-shadow
```
<!-- guide:deploy.shadow end -->

**Step 2: active.**

<!-- guide:deploy.active start -->
```bash
# Only after reading what shadow would do. For two to three minutes after
# this, nothing in the group scales and its models show quiet-period; the
# same happens after every controller restart.
make utilization-share-activate
```
<!-- guide:deploy.active end -->

On a cluster-wide quota you can act on a few namespaces first with
`clusterNamespaces`; weights, floors and every other key are in the
[scaling policy reference](../../reference/scaling-policy.md#optimizer-cluster-default-only-live).

## Verification

There are two ways to read the optimizer's metrics:

- **As a namespace tenant: Prometheus.** Use the Prometheus your cluster gives
  you (Grafana's Explore, or its API). The controller's own endpoint needs a
  cluster-wide grant a tenant does not have. Scope every query to your
  namespace: a shared Prometheus holds other tenants' models too.
- **As a cluster admin: the controller's endpoint**, with the commands further
  down.

```promql
# the mode: exactly one series is 1 -- off, invalid, shadow or active
wva_utilization_share_mode{namespace="<namespace>"} == 1
# what a move would fix, and how many replicas
wva_utilization_share_actionable{exported_namespace="<namespace>"} == 1
wva_utilization_share_replicas_to_move{scope="<namespace>"}
wva_utilization_share_headroom{exported_namespace="<namespace>"}     # < 0: the model is short
# once active: transfers that ended, by outcome (done is healthy)
sum by (outcome) (increase(wva_utilization_share_transfers_total{scope="<namespace>"}[1h]))
# models held back, and why
wva_model_scaling_blocked{exported_namespace="<namespace>"} == 1
```

`exported_namespace` is the model's namespace; `namespace` on the mode is the
controller's; `scope` is the quota's namespace. On a namespace-scoped install
all three are yours.

<!-- guide:verify.status start -->
```bash
# The mode, the last decisions in the log (what shadow would move;
# transfers once active), the transfer events, any UtilizationShare
# warning, and the pods marked for a transfer.
make utilization-share-status
```
<!-- guide:verify.status end -->

<!-- guide:verify.endpoint start -->
```bash
# For a cluster admin. The endpoint checks the caller against a
# cluster-wide grant: the install's metrics-reader token passes where the
# admin's install step created its binding. A 401 or 403 here means use
# Prometheus instead.
TOKEN=$(kubectl get secret wva-epp-metrics-token -n $NAMESPACE -o jsonpath='{.data.token}' | base64 -d)
kubectl port-forward -n $NAMESPACE svc/wva-controller-manager-metrics-service 8443:8443 >/dev/null & PF=$!
sleep 2
curl -sSfk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics \
  | grep -E '^wva_(utilization_share_(mode|actionable|replicas_to_move|headroom|spare_gpus|transfers_total)|model_scaling_blocked)'
kill $PF
```
<!-- guide:verify.endpoint end -->


**Quiet but healthy:**

| You see | It means |
| --- | --- |
| no `would rebalance` log line in shadow | nothing would move; the line is written only when the set of moves changes |
| no `headroom` series for a model | it has no load to need GPUs for |
| `floor-pinned` on an idle model | it is at its floor (`minReplicaCount`), which is never taken |
| `quiet-period` for two to three minutes | the group holds still after the switch to active and after a controller restart |
| `spare_gpus` below 0 | the quota cannot cover every model's need; no move fixes that |
| `invalid` mode | the block did not validate, or the policy has no `limiters:` list; the log says which |

When it evaluates but never moves anything, work down
[the troubleshooting list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## Cleanup

<!-- guide:cleanup.disable start -->
```bash
# Back to shadow first, so the controller removes the transfer marks it
# wrote; it waits for them to clear, then removes the block. Today's
# optimizer runs again and the limiter is untouched. If the marks do not
# clear, or the pods cannot be read, it stops with the block in shadow.
make utilization-share-off
```
<!-- guide:cleanup.disable end -->

Back out the same way before a downgrade to a version without the optimizer:
that version leaves the marks on the pods
([how to remove them](../../reference/troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade)).

## Under the physical GPU limiter

With a `gpu-inventory` limiter and no quota, the budget is every GPU of a type
on the cluster. Three things differ:

- **Set `PHYSICAL_GROUPS=true` on the shadow step.** Without
  `physicalGroups: true` the optimizer plans no group and moves nothing; the
  check warns about this.
- **The policy is the cluster's.** `make enable-physical-limiter` publishes it
  in `wva-policy`, so export `POLICY_NAMESPACE=wva-policy`. Every controller on
  the cluster reads that policy, so the block turns the optimizer on for every
  one of them, not just yours. A controller reads a policy in another namespace
  only at start: add `RESTART=1`, or the step stops and says the block was
  written but not read.
- **Spare GPUs are shared with everything else on the cluster.** Headroom fills
  idle GPUs, and a GPU a donor releases can be taken by a pod the scaling
  manager did not place. That transfer ends `release-taken`.

The quota benchmarks are the only ones measured; the physical limiter has not
been run on a cluster with the optimizer.

## Next

- What it costs, when not to use it, and what was measured:
  [the well-lit path](../../well-lit-paths/utilization-share/).
- Every metric, outcome and blocked reason:
  [utilization-share metrics](../../reference/prometheus.md#utilization-share-optimizer-metrics);
  the alerts: [monitoring](../../reference/monitoring.md#the-metrics-that-answer-specific-questions).
