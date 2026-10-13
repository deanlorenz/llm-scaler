# Turn on the utilization-share optimizer, safely

## Overview

The utilization-share optimizer shares one GPU quota between the models under
it: each model gets the GPUs its load needs, the rest is headroom shared by
weight, and when one model's demand rises it takes GPUs another model does not
need. Whether it suits your fleet, and what it costs, is on the well-lit path:
[Share one GPU quota between models whose peaks do not coincide](../../well-lit-paths/utilization-share/).

This guide turns it on in two steps -- **shadow**, where it computes and reports
but moves nothing, then **active** -- and back out. It is one block in the
scaling policy, read live. **Experimental.**

## Prerequisites

Two or more models scaled by the scaling manager, under a **quota limiter**,
with `kubectl` and `jq`. If you do not have that yet, the shortest route is the
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
# check further down reads the exact name the controller uses.
export BENCHMARK_NAMESPACE=<namespace>
WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-preflight
WVA_LIMITER=quota WVA_QUOTAS='<accelerator>=4' make benchmark-two-model-standup
make benchmark-two-model-verify
```
<!-- guide:prerequisites.setup end -->

Every command below uses two variables. On a namespace-scoped install they are
the same namespace.

<!-- guide:prerequisites.vars start -->
```bash
# On a namespace-scoped install the models, the controller and its policy
# share one namespace. POLICY_NS differs only when a cluster admin
# published the policy in wva-policy.
export NS=<namespace>
export POLICY_NS=$NS
```
<!-- guide:prerequisites.vars end -->

Check the quota is in force, and read the accelerator name it must use:

<!-- guide:prerequisites.limiter start -->
```bash
# The quota in the policy, then the limiter the controller runs NOW: the
# last matching log line. A "constructed" line from startup with type none,
# followed by "(re)built from config" with type quota, means the quota
# arrived after the controller started -- tail -1 shows the later one.
# "Invalid saturation scaling" as the last line means the entry was refused
# and no limiter is in force.
kubectl get configmap wva-scaling-policy-config -n $POLICY_NS -o jsonpath='{.data.default}' | grep -A8 '^limiters'
kubectl logs -n $NS deploy/wva-controller-manager \
  | grep -E 'GPU limiter (constructed|\(re\)built from config)|Invalid saturation scaling' | tail -1
```
<!-- guide:prerequisites.limiter end -->

<!-- guide:prerequisites.accelerator start -->
```bash
# The quota key must be one of these names exactly. The second command must
# print "No resources found": a variant whose accelerator does not resolve
# gets no budget.
kubectl logs -n $NS deploy/wva-controller-manager | grep -o '"accelerator": "[^"]*"' | sort -u
kubectl get events -n $NS --field-selector reason=AcceleratorNotResolved
```
<!-- guide:prerequisites.accelerator end -->

## Installation Instructions

<!-- guide:deploy.save start -->
```bash
# Save the policy as it is. Each step below writes this copy plus one
# block, and the cleanup puts this copy back. It prints one line: that
# the policy was saved, or why it stopped.
if ! kubectl get configmap wva-scaling-policy-config -n $POLICY_NS -o jsonpath='{.data.default}' > policy-default.yaml; then
  rm -f policy-default.yaml
  echo "FAILED: could not read the policy in $POLICY_NS (the kubectl error is above); nothing was saved" >&2; false
elif grep -q '^optimizer:' policy-default.yaml; then
  echo "STOP: this policy already has an optimizer: block; change that block instead of adding a second" >&2; false
else
  echo "no optimizer block yet; the policy is saved in policy-default.yaml"
fi
```
<!-- guide:deploy.save end -->

**Step 1: shadow.** Leave it in shadow long enough to cover each model's peak,
then read what it would have done ([Verification](#verification)).

<!-- guide:deploy.shadow start -->
```bash
# shadow: true is written out on purpose: a block without it ACTS from the
# next cycle. Read live within a cycle (30 s). With POLICY_NS other than NS,
# restart the controller afterwards: it reads that policy only at start.
{ cat policy-default.yaml; printf '\noptimizer:\n  type: utilizationShare\n  utilizationShare:\n    shadow: true\n'; } > policy-shadow.yaml
kubectl patch configmap wva-scaling-policy-config -n $POLICY_NS --type merge \
  -p "$(jq -n --rawfile d policy-shadow.yaml '{data: {default: $d}}')"
```
<!-- guide:deploy.shadow end -->

**Step 2: active.**

<!-- guide:deploy.active start -->
```bash
# Only after reading what shadow would do. For two to three minutes after
# this, nothing in the group scales and its models show quiet-period; the
# same happens after every controller restart.
sed 's/^    shadow: true$/    shadow: false/' policy-shadow.yaml > policy-active.yaml
kubectl patch configmap wva-scaling-policy-config -n $POLICY_NS --type merge \
  -p "$(jq -n --rawfile d policy-active.yaml '{data: {default: $d}}')"
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

<!-- guide:verify.logs start -->
```bash
# The policy reload, then what shadow would move. No "would rebalance" line
# is normal with no load: it is written only when the set of moves changes.
kubectl logs -n $NS deploy/wva-controller-manager \
  | grep -E 'Updated global scaling policy|Utilization share: (would rebalance|invalid optimizer block)' | tail -3
```
<!-- guide:verify.logs end -->

<!-- guide:verify.endpoint start -->
```bash
# For a cluster admin. The endpoint checks the caller against a
# cluster-wide grant: the install's metrics-reader token passes where the
# admin's install step created its binding. A 401 or 403 here means use
# Prometheus instead.
TOKEN=$(kubectl get secret wva-epp-metrics-token -n $NS -o jsonpath='{.data.token}' | base64 -d)
kubectl port-forward -n $NS svc/wva-controller-manager-metrics-service 8443:8443 >/dev/null & PF=$!
sleep 2
curl -sSfk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics \
  | grep -E '^wva_(utilization_share_(mode|actionable|replicas_to_move|headroom|spare_gpus|transfers_total)|model_scaling_blocked)'
kill $PF
```
<!-- guide:verify.endpoint end -->

<!-- guide:verify.events start -->
```bash
# Once active: every transfer is an Event on the Deployment it moved.
# Warnings are the ones to read.
kubectl get events -n $NS --field-selector reason=UtilizationShareGiving
kubectl get events -n $NS --field-selector reason=UtilizationShareReceived
kubectl get events -n $NS --field-selector type=Warning | grep UtilizationShare
```
<!-- guide:verify.events end -->

**Quiet but healthy:**

| You see | It means |
| --- | --- |
| no `would rebalance` log line in shadow | nothing would move; the line is written only when the set of moves changes |
| no `headroom` series for a model | it has no load to need GPUs for |
| `floor-pinned` on an idle model | it is at its floor (`minReplicaCount`), which is never taken |
| `quiet-period` for two to three minutes | the group holds still after the switch to active and after a controller restart |
| `spare_gpus` below 0 | the quota cannot cover every model's need; no move fixes that |
| `invalid` mode | the block did not validate, or there is no quota limiter; the log says which |

When it evaluates but never moves anything, work down
[the troubleshooting list](../../reference/troubleshooting.md#the-optimizer-evaluates-but-never-moves-anything).

## Cleanup

<!-- guide:cleanup.rollback start -->
```bash
# Back to shadow first, and wait a cycle: the controller removes the
# transfer marks it wrote. The check must print nothing. A FAILED line is
# not a pass: the pods were not read.
kubectl patch configmap wva-scaling-policy-config -n $POLICY_NS --type merge \
  -p "$(jq -n --rawfile d policy-shadow.yaml '{data: {default: $d}}')"
sleep 60
if pods="$(kubectl get pods -n $NS -o json)"; then
  printf '%s' "$pods" | jq -r '.items[] | select(.metadata.annotations["llm-d.ai/utilization-share-transfer"] != null) | .metadata.name'
else
  echo "FAILED: could not list pods in $NS; the marks were not checked" >&2
fi
```
<!-- guide:cleanup.rollback end -->

<!-- guide:cleanup.remove start -->
```bash
# Put the saved policy back. The mode reads off and today's optimizer
# runs; the quota is untouched.
kubectl patch configmap wva-scaling-policy-config -n $POLICY_NS --type merge \
  -p "$(jq -n --rawfile d policy-default.yaml '{data: {default: $d}}')"
```
<!-- guide:cleanup.remove end -->

Back out the same way before a downgrade to a version without the optimizer:
that version leaves the marks on the pods
([how to remove them](../../reference/troubleshooting.md#transfer-marks-left-on-pods-after-a-downgrade)).

## Next

- What it costs, when not to use it, and what was measured:
  [the well-lit path](../../well-lit-paths/utilization-share/).
- Every metric, outcome and blocked reason:
  [utilization-share metrics](../../reference/prometheus.md#utilization-share-optimizer-metrics);
  the alerts: [monitoring](../../reference/monitoring.md#the-metrics-that-answer-specific-questions).
