# llm-scaling-manager

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![CI](https://github.com/ev-shindin/llm-scaling-manager/actions/workflows/ci-pr-checks.yaml/badge.svg?branch=main)](https://github.com/ev-shindin/llm-scaling-manager/actions/workflows/ci-pr-checks.yaml)
[![Go](https://img.shields.io/github/go-mod/go-version/ev-shindin/llm-scaling-manager)](go.mod)

**Right-size every LLM model on your GPUs, before the queue forms, inside one GPU budget.**

llm-scaling-manager is an autoscaler for [llm-d](https://llm-d.ai) on
Kubernetes. It prices load from what each replica can actually serve (KV cache,
queues, token throughput) rather than from request counts, sizes every model and
variant together against one GPU budget, and hands the answer to KEDA. A warm
pool of sleeping models covers the minutes a new replica takes to load.

**Measured on GPU clusters** (single runs unless noted; method and caveats linked):

- **Scale-ups without the latency spike.** p95 time to first token per rise
  falls from **5.1–8.8 s to 0.11–0.83 s** with a one-Pod warm pool, using
  **12 % fewer GPU-seconds** than a floor of two replicas
  ([warm pool, measured](docs/well-lit-paths/warm-pool-bridge/measured.md)).
- **It sees the shape, not just the queue.** At a constant 6 req/s, with no
  queue and 5–15 % of the KV cache in use, it ordered the replica the new
  request shape needed: p95 TTFT **155 ms, 0 errors in 13 288 requests**, run
  twice a day apart ([P/D path](docs/well-lit-paths/pd-disaggregation/)).
- **A warm Pod switches models in 437 ms**, against about 41 s cold
  ([fast model loading](docs/proposals/fast-model-loading.md)).
- **Scale to zero**, and wake on the first queued request
  ([scale to zero](docs/well-lit-paths/scale-to-zero/)).

![Warm pool: near-floor latency for 12 % fewer GPU-seconds](docs/figures/warm-pool-latency-vs-cost.svg)

**[Why llm-scaling-manager](docs/why.md)** (the one-page case, costs and
maturity) · **[Get started](docs/README.md#getting-started-in-three-steps)** ·
**[Pick your scenario](docs/well-lit-paths/)**

![How it fits together](docs/concepts/figures/how-it-fits.svg)

> An independently developed, advanced fork of llm-d's
> [workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler)
> (WVA); not affiliated with or endorsed by the llm-d project. Kubernetes
> objects, make targets and metrics keep their `wva-` / `wva_` names.
> [What is different from upstream](#what-is-different-from-upstream).

### Models and variants

A **model** is what a client asks for by name. A **variant** is one way of
serving it: a GPU type, a serving configuration (tensor parallelism, batch
size, quantization), or a role such as prefill or decode. Each variant is one
KEDA ScaledObject plus the workload it scales, and variants whose triggers name
the same `modelID` are sized together, as one model. Creating the ScaledObject
is the whole registration.

## Key features

- **Scaling from real load**: replica counts follow KV-cache use, queues and
  token throughput, per model and across its variants.
- **Cost-aware variant choice**: when a model has several variants, it grows
  the one with the lowest cost per unit of capacity (`variantCost`).
- **A GPU budget**: optionally bound every decision by the GPUs that are free,
  a per-namespace quota, or Kueue's quotas.
- **Scale to zero and back**: an idle model releases its GPUs; a queued request
  wakes it.
- **Warm-pool bridging**: optionally keep models loaded and asleep, and lend
  one while a variant's own replica starts.

## Installing

```bash
export NAMESPACE=<the namespace running llm-d>

make check-prereqs                    # read-only: tools, namespace, Prometheus
make setup-prereqs                    # ONCE per namespace, by a cluster admin
make deploy-wva                       # the controller — no cluster-scoped rights
make scaledobjects-plan               # list your model servers; nothing is applied
make scaledobjects-apply              # register them — this is what makes it scale
```

The install is split across two people, because it is split across two levels of
permission: a cluster admin runs `setup-prereqs` once for the namespace, and the
namespace's owner does everything after it, including every later upgrade. If you
are both, `make deploy-wva` does the two in one command.

KEDA must already be on the cluster: on Kubernetes, `KEDA_HELM_INSTALL=true`
installs it for you; on OpenShift, use the platform's KEDA. Prometheus is found
on the cluster, or installed if there is none.

The last two steps are not optional. A **KEDA ScaledObject** is how a workload
registers with llm-scaling-manager: it does not list or watch your workloads,
so until a ScaledObject exists it runs and scales nothing.

| Then | |
| --- | --- |
| Full install guide | [deploy/](deploy/) |
| Installing it | [docs/guides/](docs/guides/) — pick a path |
| Running it day to day | [operations.md](docs/reference/operations.md) |
| Watching what it decides | [monitoring.md](docs/reference/monitoring.md) |
| Making a workload scalable | [workload-preparation.md](docs/reference/workload-preparation.md) |
| Choosing what to run | [well-lit paths](docs/well-lit-paths/) — one page per scenario |
| Bridging a slow scale-up | [warm-pool/](docs/guides/warm-pool/) — opt-in; a pool holds accelerators |

## Documentation

Start with [how the scaling manager fits together](docs/concepts/architecture.md):
the parts, one decision cycle, and what gets installed. [docs/concepts/](docs/concepts/)
has how a measurement becomes a replica count and what the GPU budget counts.

See the [docs](docs/) directory for design docs, developer guide, and more.

## How It Works

**Prerequisites:** deploy llm-d infrastructure (model servers), have Prometheus
scraping them, and create a **KEDA ScaledObject** per workload whose trigger points
at llm-scaling-manager's external scaler.

**llm-scaling-manager then:**

1. Learns which workloads it manages **from the KEDA calls themselves**: it does
   not list or watch workloads, and there is no opt-in annotation. Being called
   is being managed, and the trigger `metadata` is the per-workload configuration.
2. Continuously reads request rates and server performance from Prometheus.
3. Runs its capacity model — KV-cache utilization, queue depth, token throughput —
   to decide the replica count each model needs, across all its variants at once
   and within the GPU budget any declared limiter allows.
4. Returns that decision to KEDA over the external-scaler gRPC contract. KEDA owns
   the HPA and actuates it; llm-scaling-manager never writes the scale subresource.

## Example

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: llama-8b-scaler
  namespace: llm-inference
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: llama-8b
  pollingInterval: 5
  minReplicaCount: 1        # 0 to allow scale to zero
  maxReplicaCount: 10
  triggers:
  - type: external-push     # push: wakes a parked workload immediately
    name: wva-external-scaler
    metadata:
      scalerAddress: wva-external-scaler.<controller-namespace>.svc.cluster.local:9090  # your NAMESPACE on a namespace install
      modelID: meta/llama-3-8b   # required — the only field you must supply
      scalingPolicy: interactive # optional — a tier you add to the scaling policy; none ship enabled
      variantCost: "10.0"        # optional — defaults to "10.0"
```

`modelID` is the one required field. The accelerator, the role, GPUs per replica
and the InferencePool are all **derived** from the workload itself, so they cannot
drift from reality.

You do not have to write this by hand: `make scaledobjects-plan` reads `modelID`
off each container's `--served-model-name`, which is the one field nothing else
can check for you — a `modelID` that does not match what the container serves
groups the workload with a model it does not run, and mis-scales both. See
[the guides](docs/guides/).

Every trigger key, its default and who reads it: [the trigger reference](docs/reference/scaledobject-trigger.md). More examples in [config/samples/keda/](config/samples/keda/).

## What is different from upstream

The fork keeps WVA's core idea, variant-aware autoscaling for LLM inference,
and has diverged substantially from it. The largest additions:

- a **KEDA external scaler**, so it answers KEDA directly over gRPC instead of
  publishing a metric;
- a **warm pool** that bridges a slow scale-up by lending a Pod with the model
  already loaded and asleep;
- **ScaledObject discovery and install tooling**: `make scaledobjects-plan`
  writes an editable plan of what would be autoscaled before anything is applied;
- **GPU budgets** shared across models: physical inventory, quotas, Kueue, and
  the experimental utilization-share optimizer;

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

Questions, bugs and ideas: [GitHub issues](https://github.com/ev-shindin/llm-scaling-manager/issues).

## License

Apache 2.0 - see [LICENSE](LICENSE) for details.


## Related Projects

- [llm-d workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler) - the upstream project this fork derives from
- [llm-d infrastructure](https://github.com/llm-d/llm-d-infra)
- [llm-d main repository](https://github.com/llm-d/llm-d)

## References
- [WVA paper](https://arxiv.org/abs/2603.09730)
- [WVA use case doc](https://docs.google.com/document/d/1ZcMXO0x42qn4X5cu6efgMomYC4pKPwm6r7L79y1AQH4/edit?tab=t.0)
- [Saturation based design discussion](https://docs.google.com/document/d/1iGHqdxRUDpiKwtJFr5tMCKM7RF6fbTfZBL7BTn6UkwA/edit?tab=t.0#heading=h.mdte0lq44ul4)