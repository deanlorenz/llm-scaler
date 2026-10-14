# Install a small llm-d model

## Overview

Deploys one small model — `Qwen/Qwen3-0.6B` — with an EPP and an InferencePool,
on a single GPU, so there is something real for the scaling manager to scale. The model only:
[Install the scaling manager in a namespace](../install-in-namespace/) is the step after it, and
the two are separate because the scaling manager's preflight refuses a namespace with no model
servers — the model has to exist first. Use it to try the scaling manager,
to reproduce a scaling question, or as the target for
[`make benchmark-smoke`](../benchmarking/).

It exists because there is no upstream equivalent. llm-d's own guides deploy
models that need a fleet: [Optimized Baseline][ob] — the inference-scheduling
entry point — defaults to `Qwen/Qwen3-32B` across **16 GPUs** (8 replicas, TP=2),
and the disaggregation guides target `gpt-oss-120b`. Those are the right defaults
for what those guides teach. They are the wrong ones for "I have one card and I
want to see this work".

For correctness work with no GPU at all, use
[Test the scaling manager against a full llm-d stack](../testing-with-llm-d/) instead — it runs
simulators on kind.

## Prerequisites

- one GPU, and a node that will schedule onto it
- `kubectl`, and rights to create workloads in your namespace
- `git`, `helmfile`, and `uv` (or Python 3 with `venv`). The standup clones
  llm-d-benchmark and installs its CLI itself (`make benchmark-install`), and
  stops with install commands if one of these is missing.

<!-- guide:env.static.namespace start -->
```bash
export NAMESPACE=<your-namespace>
export MODEL_ID=Qwen/Qwen3-0.6B
```
<!-- guide:env.static.namespace end -->

<!-- guide:prerequisites.gpu start -->
```bash
# One GPU, and a node that will schedule onto it. This deploys a real model
# server, not a simulator.
kubectl get nodes -o custom-columns=NODE:.metadata.name,GPU:.status.allocatable.'nvidia\.com/gpu' --no-headers
```
<!-- guide:prerequisites.gpu end -->

## Installation Instructions

<!-- guide:deploy.standup start -->
```bash
# Deploys the model server, its EPP and the InferencePool, and holds the
# engine image on the accelerator nodes (the prepull-* pods; BENCHMARK_PREPULL=false
# skips that) -- nothing else. BENCHMARK_WVA_DEPLOY=false keeps the
# autoscaler out of it: installing the scaling manager is the next guide's job.
make benchmark-standup BENCHMARK_NAMESPACE=${NAMESPACE} MODEL_ID=${MODEL_ID} \
  BENCHMARK_WVA_DEPLOY=false
```
<!-- guide:deploy.standup end -->

Doing the two installs separately shows which half failed, and the scaling
manager's preflight refuses a namespace with no model servers in it, so the
model comes first.

The EPP it deploys runs with the `flowControl` feature gate on. The next guide's
install refuses an EPP without it, because that gate publishes the router queue
the scaling manager reads as demand (and the only signal at zero replicas). If
you bring your own EPP, or reuse one an llm-d guide deployed, it has the gate off
unless you turned it on. Check, then add it to the `EndpointPickerConfig` and
restart the EPP:

```bash
kubectl get cm -n ${NAMESPACE} -o yaml | grep -A3 featureGates
```

```yaml
featureGates:
- flowControl
```

With flow control on, a backlog shows up in the EPP's queue (the **EPP
Flow-Control Queue** dashboard panel), not in `vllm:num_requests_waiting`.

**Memory budget.** The standup sets vLLM's `gpuMemoryUtilization` to 0.90 for
every model, deliberately, and a small model then gets so much KV cache that one
replica rarely looks full. To make it scale on a small load, lower
`kvCacheThreshold` in the scaling policy rather than shrinking the cache. Why,
the intermittent out-of-memory failure, and how to override it (`GPU_MEM_UTIL`):
[The engine's memory budget](../../reference/workload-preparation.md#the-engines-memory-budget-gpumemoryutilization).

### Then install the scaling manager into that namespace

Follow [Install the scaling manager in a namespace](../install-in-namespace/) with the same
`NAMESPACE`. In short:

```bash
export NAMESPACE=<the namespace you just used>
make setup-prereqs        # cluster admin, once per namespace
make deploy-wva           # the controller
make scaledobjects-plan WVA_DEFAULT_SO_PLAN=wva-plan.yaml
# edit wva-plan.yaml: apply: yes|no|adopt, the modelID, the replica bounds
make scaledobjects-apply WVA_DEFAULT_SO_PLAN=wva-plan.yaml
```

The ScaledObject is the registration: the scaling manager has no watch and no
listing, so until one exists it is never called and scales nothing.

## Verification

<!-- guide:verify.serving start -->
```bash
kubectl get pods -n ${NAMESPACE}
```
<!-- guide:verify.serving end -->

Both the decode pod and the EPP should be `Running`, and so should one
`prepull-*` pod per accelerator node -- the standup's image holder
([Holding the image on the nodes](../../reference/workload-preparation.md#holding-the-image-on-the-nodes)).
A decode pod in
`CrashLoopBackOff` is almost always the memory budget above — check its log for
`torch.OutOfMemoryError` before looking anywhere else.

<!-- guide:verify.model start -->
```bash
# Ask the endpoint what it loaded. This is the name every request must use.
kubectl run probe -n ${NAMESPACE} --rm -i --restart=Never --quiet \
  --image=registry.k8s.io/e2e-test-images/agnhost:2.47 --command -- \
  /bin/sh -c "curl -s http://$(kubectl get svc -n ${NAMESPACE} -o name | grep epp | head -1 | cut -d/ -f2).${NAMESPACE}.svc.cluster.local:80/v1/models"
```
<!-- guide:verify.model end -->

The `id` it returns is the name every request must use. It is the *served* name,
which is not always the path the weights were loaded from.

## Benchmarking

Once everything above is deployed — and not before, since it drives real load
at the model — this is the one command that shows whether the scaling manager is scaling it:

<!-- guide:verify.smoke start -->
```bash
# Drive load through it and snapshot the dashboard.
make benchmark-smoke NAMESPACE=${NAMESPACE}
```
<!-- guide:verify.smoke end -->

Symmetric 1024-in/1024-out at 15 req/s for five minutes, then a snapshot of the
dashboard over
exactly that window. It checks the whole chain first — KEDA, a controller
managing this namespace, model servers, an EPP, a ScaledObject — and names every
gap at once rather than failing five minutes in.

For numbers you intend to compare between runs, use
[Benchmark the scaling manager](../benchmarking/) instead: this one answers "does it scale", not
"how fast is it".

## Cleanup

<!-- guide:cleanup.teardown start -->
```bash
make benchmark-teardown BENCHMARK_NAMESPACE=${NAMESPACE}
```
<!-- guide:cleanup.teardown end -->

This removes **both halves** — the autoscaler and the model — in that order. The scaling manager
goes first deliberately: a namespace-scoped install still creates cluster-scoped
RBAC, which deleting the namespace would strand, and a controller outliving its
workloads keeps calling a scaler for things that are gone.

To remove only the autoscaler and keep the model serving, use `make undeploy-wva`
instead, as [Install the scaling manager in a namespace](../install-in-namespace/) describes.

Verify nothing cluster-scoped was stranded:

```bash
kubectl get clusterrole,clusterrolebinding -o name | grep -i "${NAMESPACE}"
```

## Configuration

| Parameter | Default | Notes |
| --- | --- | --- |
| `BENCHMARK_NAMESPACE` | — (required) | where everything lands |
| `MODEL_ID` | `Qwen/Qwen3-0.6B` | the served model |
| `GPU_MEM_UTIL` | `0.90` | fraction of **total** GPU memory; one value for every model, deliberately |
| `WARM_REPLICAS` | `0` | FMA warm pool size, if the scenario uses one |

## Weights, once you scale it

This guide deploys one model on one GPU, so the weights are fetched once and
the cost is invisible. It stops being invisible when the scaling manager starts adding
replicas: without a shared cache, each new pod fetches them again.

llm-d's modelservice chart mounts a cache at `/model-cache` by default, so
there is usually nothing to do. Confirm it before you rely on autoscaling:

```bash
make workload-patch NAMESPACE=${NAMESPACE}
```

It reports both settings that make autoscaling safe — the cache, and a preStop
hook so a removed replica finishes what it is writing — and writes a patch for
whichever is missing.

Listing the pod's PVCs is the obvious check and it is the wrong one: a claim
mounted at one path while the engine downloads to another passes it and
re-downloads on every scale-up anyway. What decides it is where the weights
*land*, which is what the command above actually looks at.

[Weights and the model
cache](../../reference/workload-preparation.md#weights-and-the-model-cache) has the
detail, including why it makes scale-up cheaper but not faster. For a model
large enough that reading it through the shared volume is the start path,
`BENCHMARK_MODEL_HOSTPATH=<dir>` on the standup (or `make weights` for any
deployment) puts a copy on every accelerator node's disk --
[Weights on the node's disk](../../reference/workload-preparation.md#weights-on-the-nodes-disk).

## Next

In order:

1. [Install the scaling manager in a namespace](../install-in-namespace/) — the model is now there
   for it to scale. The standup above did not install it (`BENCHMARK_WVA_DEPLOY=false`).
2. `make benchmark-smoke NAMESPACE=<ns>` — drive load at it and snapshot the
   dashboard. Run it **last**, once everything is deployed.
3. [Benchmark the scaling manager](../benchmarking/) — when you want numbers to compare
4. [After the install](../../reference/operations.md) — what the metrics mean

[ob]: https://github.com/llm-d/llm-d/tree/main/guides/optimized-baseline
