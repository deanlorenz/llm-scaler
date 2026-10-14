# Install the scaling manager in a namespace

## Overview

This guide installs the scaling manager (llm-scaling-manager) into one namespace, where it
sizes the llm-d model servers running there and nothing else. The scaling manager decides how
many replicas each variant needs from saturation and cost, and hands the decision
to KEDA, which owns the HPA and does the scaling.

It is the common path, and it works the same whether llm-d is already serving or
you have just deployed it. A cluster admin runs one command first, once — see
[Cluster-admin setup](../admin-cluster-setup/) — after which the
namespace's owner installs and upgrades the controller themselves.

## Prerequisites

- **llm-d model servers in the namespace, deployed first.** The scaling manager scales what is
  already serving; it does not deploy models, and the install refuses a namespace
  with none rather than leave a healthy controller with nothing to scale. No
  model yet? [Install a small llm-d model](../install-small-model/) puts one on a
  single GPU.
- KEDA. The install never installs it on its own. On OpenShift, install the
  Custom Metrics Autoscaler operator first. On Kubernetes the install assumes
  the cluster already runs KEDA; if it has none, `KEDA_HELM_INSTALL=true make
  setup-prereqs` installs it with Helm.
- a Prometheus scraping those model servers and their EPP
- **the EPP's `flowControl` feature gate.** `check-prereqs` and `deploy-wva`
  refuse without it: it publishes the router queue the scaling manager reads as
  demand, and it is the only signal at zero replicas. EPPs from llm-d guides have
  it off; add `featureGates: [flowControl]` to the `EndpointPickerConfig` and
  restart the EPP.
- **a model cache on those model servers.** Autoscaling means new replicas,
  and a replica that has to fetch its weights first turns every scale-up into
  a download from Hugging Face. llm-d mounts one by default, so this is
  normally already true — `make scaledobjects-plan` says so per workload if it
  is not. See [Weights and the model
  cache](../../reference/workload-preparation.md#weights-and-the-model-cache), which
  also covers what it does not buy: the cache removes the download, not the
  load.
- **a drain hook on those model servers.** Autoscaling also means replicas being
  *removed*, and a replica removed mid-stream takes its open responses with it:
  the client sees a truncated body, after the request was already paid for in GPU
  time. This one is normally **not** already true: llm-d does not set a preStop
  hook.

`make workload-patch` reports both and writes the fix for whichever is missing.
It writes a file rather than changing your workloads, because the pod spec
belongs to your modelservice chart; see [Writing the
patch](../../reference/workload-preparation.md#writing-the-patch-make-workload-patch).

Every check that can refuse below is listed, with the fix, in [First-line
troubleshooting](../../reference/operations.md#first-line-troubleshooting).
`SKIP_CHECKS=true` turns the refusals into warnings and installs anyway; see
[`SKIP_CHECKS`](../../reference/configuration.md#deployment-flags) for what it
does not skip.

<!-- guide:env.static.namespace start -->
```bash
export NAMESPACE=<your-namespace>
```
<!-- guide:env.static.namespace end -->

Required: the install refuses without it rather than guess which namespace it
manages. (`WVA_NS` is a different setting: where the *controller* runs. It
differs from `NAMESPACE` only in the cluster-wide shape.)

<!-- guide:prerequisites.check start -->
```bash
make check-prereqs
```
<!-- guide:prerequisites.check end -->

Read-only. It reports the namespace it resolved, whether that namespace holds
model servers, and the Prometheus it found. If it says the namespace is empty or
missing, stop and fix `NAMESPACE`: a controller pointed at the wrong namespace
installs cleanly, reports healthy, and scales nothing.

## Installation Instructions

### 1. Cluster admin: prepare the namespace

**This step needs cluster-admin rights** — everything after it does not.

<!-- guide:deploy.prereqs start -->
```bash
make setup-prereqs
```
<!-- guide:deploy.prereqs end -->

Once per namespace, and again when a release adds a prerequisite
([upgrading](../../reference/upgrade-and-uninstall.md#upgrading-to-this-release)).
Add `KEDA_NAMESPACE=<namespace KEDA runs in>` when the installer cannot find KEDA
itself (it says so, and then writes no NetworkPolicy for the scaler port), and
`PROMETHEUS_SA=<namespace>/<serviceaccount>` to grant your Prometheus the scrape
of the controller's `/metrics`; a re-run without it keeps the binding an earlier
run made. It creates the cluster-scoped RBAC, the
ServiceAccounts, Roles and ServiceMonitor: the objects a namespace admin is not
allowed to create — and the controller's namespace when `WVA_NS` names one apart
from yours. Your namespace must already exist. See
[Cluster-admin setup](../admin-cluster-setup/) for what each one is and why it
needs an admin.

On Kubernetes with no KEDA, the step stops and tells you to re-run it as
`KEDA_HELM_INSTALL=true make setup-prereqs`; on OpenShift, install the Custom
Metrics Autoscaler operator and re-run.

Already done for your namespace? Go to step 2: it checks, and names anything
still missing.

On a **shared cluster** your admin should instead run the controller
[out of your reach](../admin-cluster-setup/README.md#keeping-the-controller-out-of-the-tenants-reach),
in a namespace they own: the GPU bound then holds against the namespace's owner.
In that shape the admin does steps 1 and 2, and you start at step 3.

### 2. Install the controller

<!-- guide:deploy.controller start -->
```bash
make deploy-wva
```
<!-- guide:deploy.controller end -->

It works out which half is left to do, and says which it chose. Run by a
namespace owner, or once step 1 has run, it installs the controller only. Run by
a cluster admin before step 1, it does both. If step 1 has not happened and you
cannot do it yourself, it stops and names what is missing for an admin.

### What you own, and what your admin owns

| | owned by | what that means |
| --- | --- | --- |
| the controller Deployment, its Services, `wva-manager-config` | you | you install and upgrade them with namespace rights |
| `wva-scaling-policy-config` in your namespace | you | in a namespace-scoped install this is the controller's **global** map, not a namespace-local override: its `default` entry is the default for everything the controller manages. Thresholds, tiers and per-model entries are yours. So are the limiters — unless a cluster policy namespace governs this controller; see below |
| ServiceAccounts, Roles, RoleBindings, the ServiceMonitor, the token Secrets | your admin | created by step 1, in your namespace; RBAC forbids you granting what you do not hold |
| the hashed ClusterRoles and ClusterRoleBindings | your admin | cluster-scoped |
| the cluster policy namespace (`wva-policy`) | your admin | where a GPU budget lives when the admin publishes one |

Whether a cluster policy governs your controller is in the
[`wva-controller-status` ConfigMap](../../reference/operations.md#what-the-controller-is-running-with)
it publishes in its own namespace. This guide installs the controller in
`${NAMESPACE}`; if your admin instead runs it
[out of your reach](../admin-cluster-setup/README.md#keeping-the-controller-out-of-the-tenants-reach),
it runs in **their** namespace (for example `wva-${NAMESPACE}`), so pass that to
`-n`:

```bash
# -n: the namespace the CONTROLLER runs in, not necessarily ${NAMESPACE}
kubectl get cm wva-controller-status -n ${NAMESPACE} \
  -o jsonpath='{.data.policyNamespace} {.data.limiter}{"\n"}'
```

A `policyNamespace` other than the controller's own (such as `wva-policy`) means
the limiters come from there and your ConfigMap's `limiters:` do not apply;
`limiter` is the limiter in force. Reading it needs `get` on ConfigMaps in the
controller's namespace. In the out-of-reach shape you normally lack that:
`controller-status-role` binds only the controller's ServiceAccount, so the read
returns Forbidden. Forbidden says nothing about the controller; ask your admin
to read it for you, or read the Events the controller records on your
ScaledObjects, where a limiter holding a scale-up shows as `ResourceConstrained`:
`kubectl get events -n ${NAMESPACE} --field-selector involvedObject.kind=ScaledObject`. NotFound means the controller predates the ConfigMap or
lacks the grant to write it. Then fall back to the startup log, which you can
read only with `pods/log` in the controller's namespace:

```bash
kubectl logs -n ${NAMESPACE} deploy/wva-controller-manager \
  | grep -E 'Cluster policy comes from|Could not read the managed Namespace'
```

No `Cluster policy comes from` line (the own-ConfigMap branch logs only at V(1),
hidden at the default verbosity) means your ConfigMap's `limiters:` are in
force; `Could not read the managed Namespace` says why a policy namespace was
not seen. On Kubernetes that means the controller lacks `get` on Namespaces,
which `make setup-prereqs` grants: an install set up before that grant existed
cannot see a published `wva-policy` until a cluster admin re-runs
`NAMESPACE=${NAMESPACE} make setup-prereqs` and the controller restarts. Until
then it also logs `Cannot tell whether a cluster admin published policy` and
sets `wva_policy_namespace_unverified` to 1. The full list of permissions and
their reasons is in the [security model](../../reference/security-model.md).

### 3. Make the workloads safe to scale

The two prerequisites above are stated at the top of this guide and are easy to
skip, so this is the step that checks them. It reports and changes nothing.

<!-- guide:deploy.readiness start -->
```bash
# What the model servers need before anything scales them. Reports first;
# changes nothing until you ask.
make workload-patch NAMESPACE=${NAMESPACE}
```
<!-- guide:deploy.readiness end -->

A model server with no preStop hook loses in-flight responses on every
scale-down, and one that downloads its weights outside every volume it mounts
re-fetches them on every scale-up. Both are pod-spec settings owned by the chart
that deployed the model server, so this writes a patch rather than applying one.
See [Writing the
patch](../../reference/workload-preparation.md#writing-the-patch-make-workload-patch) for
what to do with it, including `make model-cache` when the weights half is what is
missing.

### 4. Register the workloads

Nothing scales until a ScaledObject exists: the scaling manager is only ever
asked about workloads KEDA calls it about.

<!-- guide:deploy.register start -->
```bash
make scaledobjects-plan WVA_DEFAULT_SO_PLAN=wva-plan.yaml
# edit wva-plan.yaml: apply: yes|no|adopt, the modelID, the replica bounds
make scaledobjects-apply WVA_DEFAULT_SO_PLAN=wva-plan.yaml
```
<!-- guide:deploy.register end -->

`scaledobjects-plan` finds the model servers and writes one entry each, applying
nothing:

```yaml
plan:

  - apply: "yes"                        # yes | no | adopt
    namespace: llm-d-sim
    kind: Deployment
    name: dev-model-decode
    modelID: "meta/llama"               # required — what the container serves
    minReplicas: 1
    maxReplicas: 10
    variantCost: "10.0"
    inferencePool: "optimized-baseline" # informational
```

Edit it; `scaledobjects-apply` does exactly what you left in it. Every field is
explained in the comments the file is written with, so there is nothing to look
up. `apply: adopt` is for a workload something else already scales: it repoints
that object at the scaling manager instead of adding a second, because two ScaledObjects on one
target is two HPAs writing the same replica count. A workload whose model could
not be read is written as `no` with the reason, and never created without one.

> **Running Fast Model Actuation?** The plan can carry a second entry for the
> same model — the FMA half — switched off by default, and an entry can arrive as
> `apply: no` until the launcher pods are scraped. See
> [the FMA post-mortem](../../proposals/fma-post-mortem.md).

#### The same file also suggests warm pools

Below the `plan:` entries, `wva-plan.yaml` carries a `warmPools:` section. It
groups the workloads that share an accelerator and a device count, because they
could share a [warm pool](../warm-pool/): engines kept loaded on a GPU, so a
scale-up serves in seconds instead of waiting for a cold model load. A pool holds
its GPUs for as long as it exists, so every suggestion is written `apply: "no"`.
Read [Bridge a scale-up with a warm pool](../warm-pool/) before turning one on;
its `models` and `modelSize` fields are placeholders you must size.

## Verification

### 1. Check KEDA received the decision

<!-- guide:verify.objects start -->
```bash
kubectl get scaledobject,hpa -n ${NAMESPACE}
```
<!-- guide:verify.objects end -->

A working registration looks like this:

```text
NAME                                        SCALETARGETKIND      SCALETARGETNAME    MIN  MAX  READY  ACTIVE  TRIGGERS
scaledobject.keda.sh/dev-model-decode-wva   apps/v1.Deployment   dev-model-decode   1    10   True   True    external-push

NAME                                                  REFERENCE                     TARGETS     MINPODS  MAXPODS  REPLICAS
horizontalpodautoscaler.autoscaling/keda-hpa-dev-...  Deployment/dev-model-decode   1/1 (avg)   1        10       1
```

Three things to read, in this order:

- **`READY True`** — KEDA reached the scaling manager and got a metric spec back. The HPA is
  KEDA's; it creates one per ScaledObject.
- **`TARGETS` showing a number** — the decision is flowing. `1/1 (avg)` is the scaling manager
  saying one replica is the right size.
- **`ACTIVE True`** — there is traffic. `Unknown` on a workload nobody is
  calling is normal, and not a fault.

`TARGETS` reading **`cpu: <unknown>/80%`** means the opposite of it looks: KEDA
could not fetch the metric spec from the scaling manager and fell back to a CPU metric, so the
workload is not being scaled by the scaling manager at all. `READY False` accompanies it. The
usual cause is a trigger naming a scaler it cannot reach, which is what a
ScaledObject written for a different install does — the shipped samples name the
default namespace, and a namespace-scoped install is not there. Repair it with:

```bash
make scaledobjects-repoint
```

It takes no arguments — it finds the install that is running and points the
object at it. It rewrites `scalerAddress` only, leaves your `modelID`,
`variantCost` and bounds untouched, and is idempotent. See
[First-line troubleshooting](../../reference/operations.md#first-line-troubleshooting)
for the other causes.

### 2. Read the decisions

**Give it an optimize cycle first.** The scaling manager decides on a timer, so a ScaledObject
created seconds ago has not been through one yet and this returns nothing —
which reads like a broken install and is not. Measured on a fresh namespace: 0
decision lines immediately after `scaledobjects-apply`, 53 a few minutes later,
with no change in between. If it is still empty after a few minutes, that is
worth investigating; straight away, it is not.

<!-- guide:verify.decisions start -->
```bash
kubectl logs -n ${NAMESPACE} deploy/wva-controller-manager | grep scaling-decision
```
<!-- guide:verify.decisions end -->

```text
scaling-decision {"modelID":"meta/llama","decisions":[{"name":"llama-decode-wva","curr":1,"tgt":3,"action":"scale-up"}]}
```

### 3. (Optional) See it in a dashboard

<!-- guide:visualize.dashboard start -->
```bash
make dashboard
```
<!-- guide:visualize.dashboard end -->

**OpenShift only.** It reads Thanos through the platform's tenancy port and
publishes itself through a Route, neither of which exists on vanilla Kubernetes;
it checks, and says so rather than building a Grafana that cannot work. On plain
Kubernetes, import `deploy/grafana/operational-dashboard.json` into your own
Grafana and point it at the Prometheus `make check-prereqs` reports.

Stands up a Grafana private to this namespace — not the shared cluster
instance, whose dashboards are read-only — with the scaling manager's operational dashboard
already imported, reading Thanos through a namespaced role rather than any
cluster-scoped grant. Requires grafana-operator's CRDs; that is a cluster-admin
install, so this only creates namespaced objects and says plainly if they are
missing. It also refuses a namespace with no model servers in it: the namespace
is what the datasource enforces and what the dashboard pins its variable to, so
the wrong one yields a dashboard that is empty rather than broken. Prints the
dashboard URL and the admin password on every run, so it is safe to re-run for
either. Its datasource ServiceAccount, `wva-grafana-datasource`, gets a
**non-expiring** token Secret, `view` on the namespace and `create` on
`metrics.k8s.io` pods; whoever reads Secrets in the namespace holds it — see the
[security model](../../reference/security-model.md#grants-made-after-install).

### 4. (Optional) Drive some load and look at it

<!-- guide:visualize.smoke start -->
```bash
make benchmark-smoke NAMESPACE=${NAMESPACE}
```
<!-- guide:visualize.smoke end -->

Symmetric 1024-in/1024-out at 15 req/s for five minutes, then a snapshot of the
dashboard over
exactly that window, and a line telling you where it is. It answers one question
— is the scaling manager actually scaling this? — and it needs a GPU cluster, because
that is what serving a model needs.

It is not a measurement of the model: five minutes of Poisson arrivals is not a
latency study, and a small model's TTFT says nothing about a large one's. For
numbers worth comparing, see [Benchmark the scaling manager](../benchmarking/).

## Cleanup

<!-- guide:cleanup.uninstall start -->
```bash
make undeploy-wva
```
<!-- guide:cleanup.uninstall end -->

This removes the ScaledObjects it created, and KEDA restores each workload to the
replica count it had before the scaling manager sized it. Objects you adopted are left alone and
listed by name — they were not this installer's to delete — so repoint or remove
them yourself: their trigger now calls a scaler that is gone, KEDA keeps the HPA,
and a workload parked at zero can never be woken.

`UNDEPLOY_SCALEDOBJECTS=false` keeps everything, for reinstalling in place.

### The model servers are still running

`make undeploy-wva` removes the **autoscaler only**. Every model server, EPP and
InferencePool in the namespace keeps serving, which is usually what you want:
the scaling manager did not create them, and something else may depend on them.

For the same reason, a drain hook applied with `WVA_WORKLOAD_PATCH_APPLY=true`
stays: those fields belong to the model server, and the next `helm upgrade` of
its chart reverts them. A model cache created by `make model-cache` is also left
alone, because it holds data.

If you stood the model up with
[Install a small llm-d model](../install-small-model/), that is what removes it:

<!-- guide:cleanup.model start -->
```bash
make benchmark-teardown BENCHMARK_NAMESPACE=${NAMESPACE}
```
<!-- guide:cleanup.model end -->

Run it **after** `make undeploy-wva`, not instead of it. It tears down the llm-d
releases and the namespace, and a controller left behind would keep calling a
scaler for workloads that no longer exist.

### What an uninstall run by you leaves behind

A namespace-scoped install has cluster-scoped RBAC that you cannot delete. Run
by you, `make undeploy-wva` removes everything you may delete, then prints the
`Forbidden` objects it could not and asks for a cluster admin. Deleting the
namespace strands them too. Send your admin this, which removes this install's
hashed ClusterRoles and ClusterRoleBindings and nothing else:

```bash
# Run by a cluster admin. The suffix is the first 8 hex digits of the SHA-256
# of the controller's namespace (shasum -a 256 on macOS).
SUFFIX=$(printf '%s' "<the namespace>" | sha256sum | cut -c1-8)
kubectl get clusterrole,clusterrolebinding -o name | grep -- "-${SUFFIX}$"
NAMESPACE=<the namespace> make undeploy-wva     # removes them; or delete the listed objects by hand
```

## Configuration

`NAMESPACE` is required; everything else is detected, so set one only to
override what the preflight reported.

| Parameter | Default | Example |
| --- | --- | --- |
| `NAMESPACE` | none — **required**, never discovered | `llm-d-optimized-baseline` |
| `IMG` | the image CI builds from main — a **floating** tag; pin a release for anything you rely on ([pin the version](../../reference/upgrade-and-uninstall.md#pin-the-version)) | `ghcr.io/you/wva:dev` |
| `PROMETHEUS_URL` | detected; Thanos on OpenShift | `https://prom.monitoring.svc:9090` |
| `KEDA_HELM_INSTALL` | `false` on Kubernetes — assumes cluster KEDA | `true`, to install one with Helm |
| `KEDA_NAMESPACE` | found from the KEDA-served external-metrics APIService, else, with `KEDA_HELM_INSTALL=true` and that APIService NotFound, this default (the install puts KEDA there), else an existing scaler NetworkPolicy; with none, no NetworkPolicy is written and `:9090` stays open (admin step) | `keda` |
| `PROMETHEUS_SA` | none — the `metrics-reader` binding goes only to a Prometheus you named; a re-run without it keeps an existing binding (admin step) | `monitoring/prometheus-k8s` |

Full list: [Configuration reference](../../reference/configuration.md).

## Next

- [After the install](../../reference/operations.md)
- [Bound every install by real GPUs](../admin-gpu-bounding/) — otherwise
  scaling is bounded only by each workload's `maxReplicaCount`
- [Bridge a scale-up with a warm pool](../warm-pool/) — hold models loaded and
  asleep so a scale-up serves while its replica is still loading
- [Scaling policy](../../reference/scaling-policy.md)
