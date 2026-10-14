# Bound every install by real GPUs

## Overview

Publishes cluster policy declaring a GPU limiter, and grants every install on the
cluster the access that policy then requires. Without it, each controller scales
to its workloads' `maxReplicaCount` with no check against the GPUs that exist.

One command, because the ConfigMap is the easy part: the policy must live where a
tenant cannot edit it, every controller must be able to read it, and every
controller must be able to list nodes — and a controller that cannot resolve a
variant's accelerator gives it no budget and never scales it up.

## Prerequisites

Cluster-admin rights, and **accelerators that resolve**. Check first — this is
the failure with no natural symptom:

<!-- guide:prerequisites.accelerators start -->
```bash
# Check accelerators resolve FIRST. A GPU-aware optimizer gives no budget to a
# variant whose accelerator it cannot resolve, and it then never scales up.
# The event is the signal to trust: it is re-emitted for as long as the
# condition holds, and the API server folds the repeats into one entry with a
# count. It hangs on the ScaledObject, so it is in the MODEL's namespace, not
# the controller's -- hence -A. The log line says the same thing but is printed once per
# CHANGE, so on a controller that has been up a while it may have scrolled
# away: an empty grep is not an all-clear.
kubectl get events -A --field-selector reason=AcceleratorNotResolved
# Corroborates, never clears: this line is logged once per change.
kubectl logs -n <wva-namespace> deploy/wva-controller-manager | grep -i "Accelerator not resolved"
```
<!-- guide:prerequisites.accelerators end -->

The scaling manager resolves a variant's accelerator from a GPU key in its `nodeSelector`, or
from the nodes its running pods are on. A workload with neither gets no budget.

## Installation Instructions

<!-- guide:deploy.enable start -->
```bash
# Publishes cluster policy AND grants every controller that passes
# verification the node read it then requires — grants first, because
# declaring the limiter without them takes out every install that lacks node
# access at once. Read the list it prints first: it names any controller it
# will restart, and the summary names any it skipped as NOT bound.
make enable-physical-limiter
```
<!-- guide:deploy.enable end -->

Grants come first, then the policy: declaring the limiter without them takes out
every install that lacks node access at once. Controllers are discovered rather than
configured — the hazard is the install you forgot.

Policy lands where each controller actually reads it: for a self-managed
controller, the namespace its managed namespace's `wva.llmd.ai/policy-namespace`
label names, else the well-known namespace; for an admin-owned one, its own
ConfigMap. A self-managed controller also gets `get` on namespaces, limited to
the names it reads (its own namespace, `wva-policy` and its policy namespace),
which it needs to find its policy at all. A controller installed later gets both
grants from `make setup-prereqs`.

**Expect restarts.** A controller works out where its policy lives once, at
startup. Any self-managed controller that could not see its policy namespace when
it started is restarted with `kubectl rollout restart`. On the first enable after
upgrading to a release that grants the namespace read, that is **every
self-managed namespace-scoped controller** on the cluster. The list printed before
anything changes marks each one `WILL BE RESTARTED`. Changing the
`wva.llmd.ai/policy-namespace` label later has the same cause: re-run
`make enable-physical-limiter` to grant the new namespace and restart the
controller.

Cluster scope in a budget is **per controller**: N controllers each enforce
their own copy, so the cluster permits N times the figure. See
[Configuration](#configuration).

## Verification

<!-- guide:verify.controllers start -->
```bash
# A controller that is not Ready is one this did not reach.
kubectl get pods -A -l app.kubernetes.io/name=workload-variant-autoscaler
```
<!-- guide:verify.controllers end -->

<!-- guide:verify.source start -->
```bash
# Which policy each controller actually reads -- the truth, whatever the
# enable command printed. "the well-known namespace" (or "the namespace
# this one is labelled with") means a policy namespace governs it. The
# own-ConfigMap branch logs only at V(1), so at the default verbosity NO
# such line means the controller reads its own ConfigMap; "Could not read
# the managed Namespace" means it never saw wva-policy. Logged once at
# startup, so a long-running controller's line may have rotated out; a
# restart prints it again.
for ns in $(kubectl get deploy -A -l app.kubernetes.io/name=workload-variant-autoscaler,control-plane=controller-manager \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{end}'); do
  echo "== $ns"
  kubectl logs -n "$ns" deploy/wva-controller-manager \
    | grep -E 'Cluster policy comes from|Could not read the managed Namespace' \
    || echo "   (no line: reads its own ConfigMap in $ns)"
done
```
<!-- guide:verify.source end -->

<!-- guide:verify.budget start -->
```bash
# The ConfigMap is the fact; the log line is not. "GPU budgets available" is
# emitted only by the scale-from-zero engine while it judges a wake, so a
# correctly bounded cluster with nothing parked at zero prints nothing and
# looks like a failure. Read the policy instead, and treat the log as a
# bonus when scale-from-zero is in play.
# Neither of those says the controller ACCEPTED the entry, which is the
# thing that fails silently: a malformed one is rejected on read, costs the
# whole default policy, and leaves the controller with NO limiter while this
# command still prints a healthy-looking ConfigMap. The last line answers it.
# -A12 rather than -A3: a quota entry is eight lines and -A3 cut off
# namespaceQuotas and every number, so the budget that was just
# published could not be read back from the verification step.
kubectl get configmap wva-scaling-policy-config -n wva-policy \
  -o jsonpath='{.data.default}' | grep -A12 limiters
kubectl logs -n <wva-namespace> deploy/wva-controller-manager | grep "GPU budgets available"
kubectl logs -n <wva-namespace> deploy/wva-controller-manager \
  | grep -E 'GPU limiter constructed|Invalid saturation scaling'
```
<!-- guide:verify.budget end -->

The first command reads `wva-policy`, which governs only the controllers whose
startup line names the well-known namespace. For the others, read
`wva-scaling-policy-config` in the controller's own namespace.

## Cleanup

<!-- guide:cleanup.disable start -->
```bash
# Scaling becomes unbounded again for every install that reads this policy.
make disable-physical-limiter
```
<!-- guide:cleanup.disable end -->

## What it does and does not bound

A guardrail, not an enforcement boundary. Whoever owns a controller's Deployment
owns its args and image, so a tenant running the scaling manager inside their own namespace can
change what bounds them — run it
[outside their namespace](../admin-cluster-setup/README.md#keeping-the-controller-out-of-the-tenants-reach)
where the bound must hold, and use a `ResourceQuota` for a tenant you do not
trust. It also bounds only what the scaling manager asks for: raising `maxReplicaCount` or adding
a second KEDA trigger bypasses it.

**Declare one kind, not both.** `limiters:` is a list but only one is built, and
a quota entry wins — declaring `quota` alongside `gpu-inventory` gives the caps
and no physical limiter at all. The scaling manager names what it dropped rather than doing it
silently.

## Configuration

| Parameter | Default | Example |
| --- | --- | --- |
| `WVA_LIMITER_TYPE` | `gpu-inventory` | `quota` |
| `WVA_QUOTAS` | — (**required** by `quota`) | `'H200=8 A100=4'` |
| `WVA_QUOTA_SCOPE` | `namespace` | `cluster` |
| `WVA_QUOTA_KUEUE` | `false` | `true` |
| `WVA_POLICY_NS` | `wva-policy` | `platform-policy` |
| `WVA_LIMITER_TARGETS` | every controller found | `team-a team-b` |
| `WVA_CONTROLLER_SA` | `wva-controller-manager` | `my-wva-sa` |
| `WVA_CONTROLLER_IMAGE_REPOS` | `ghcr.io/ev-shindin/llm-scaling-manager`, plus the repository of `WVA_IMAGE_REPO` | `'registry.local/wva ghcr.io/ev-shindin/llm-scaling-manager'` |

A Deployment carrying the controller label is granted only when it runs as
`WVA_CONTROLLER_SA` and has a `manager` container whose image comes from one of
`WVA_CONTROLLER_IMAGE_REPOS`. Anything else is named as `SKIPPED` and granted
nothing, and the list to be granted is printed first.

These checks are **signals, not proof**. Any tenant can create a ServiceAccount
named `wva-controller-manager` and run the public image in a container named
`manager`. The checks stop a grant to an account that a Deployment merely
names, such as `default`. They do not stop a tenant who sets out to pass them.
Where tenants are not trusted, run their controllers in admin-owned namespaces
([the `WVA_WATCH_NS` shape](../../reference/gpu-limiter.md#the-arrangement-where-the-bound-does-hold))
and name exactly those namespaces:

```bash
make enable-physical-limiter WVA_LIMITER_TARGETS='wva-team-a wva-team-b'
```

A namespace named in `WVA_LIMITER_TARGETS` is vouched for by the admin, so its
controller's image is not checked; the ServiceAccount and the `manager`
container still are. That is also how a controller built from a mirror or a
custom image gets granted. The alternative is to list the mirror in
`WVA_CONTROLLER_IMAGE_REPOS`. When that is unset, it is the published repository
plus the repository of the installer's `WVA_IMAGE_REPO`.

The closing summary names the controllers the limiter is in force for, and lists
every skipped Deployment as **NOT bound**, with the reason. A skipped controller
keeps the policy it read before. `make disable-physical-limiter` does not write a
skipped controller's policy either. If that policy still declares a limiter, it
says so.

`WVA_LIMITER_TYPE` is not `WVA_LIMITER`. This one writes the **cluster** policy
(and the own ConfigMap of each admin-owned controller); `WVA_LIMITER` writes a
single install's own policy, which that install's owner can then edit.

`WVA_QUOTAS` has no default and `WVA_LIMITER_TYPE=quota` refuses to run without
one — before it creates the policy namespace or grants anything. A quota entry
that names no accelerator is not "unlimited": it is a budget of zero for every
type, and published here it would stop every managed workload on the cluster
from scaling up. `WVA_QUOTA_SCOPE=namespace` (the default) gives each managed
namespace that budget; `cluster` caps the sum — **within one controller**. This
command publishes to every controller it targets and each that reads it enforces its own
copy, so with `cluster` scope and N admin-owned controllers the cluster permits
N x the number. The default, `namespace`, is keyed on the reserved
per-unlisted-namespace key and multiplies the same way; that is the semantic,
not a defect, but it is the one to know before picking a number.

```bash
make enable-physical-limiter WVA_LIMITER_TYPE=quota WVA_QUOTAS='H200=8'
```

`WVA_QUOTA_KUEUE=true` takes the numbers from Kueue's ClusterQueues instead:
each namespace gets the smaller of Kueue's grant and `WVA_QUOTAS`, so
`WVA_QUOTAS='H200=-1'` lets Kueue decide. It also grants each target
controller the cluster-scoped half of the Kueue read (ClusterQueues,
ResourceFlavors). Experimental: [bound tenants by the quotas Kueue already
holds](../../well-lit-paths/kueue-bounded-quotas/).

## Next

- [The GPU limiter](../../reference/gpu-limiter.md) — why policy lives where it
  does, and the quota form
