# Why llm-scaling-manager

A one-page case for evaluators: what it solves, what it was measured to do,
what it costs, how mature each part is, and how to try it and back out.

## The problem

Serving LLMs on Kubernetes, the signals a general-purpose autoscaler reads do
not say how much capacity a model needs. Two requests to one model can differ
by a thousand times in work. In one benchmark, request rate, queue depth and
KV-cache occupancy all said the fleet was fine, while the new request shape
needed a third replica. Scale late and time to first token spikes for the
minutes a new replica spends loading. Scale early, or keep a floor, and GPUs
sit idle. With several models on one GPU budget, whichever scales first takes
the GPUs.

## What it does

llm-scaling-manager computes each model's replica count from a capacity model
(KV cache, queues and token throughput, per replica) and decides for every
model and variant together inside one GPU budget. KEDA and the HPA carry the
decision out. Around that core:

- a **warm pool** of sleeping models that covers the minutes a new replica takes
  to load;
- **scale to zero**, waking a model when a request queues for it;
- **GPU budgets**: the GPUs physically free, per-namespace or cluster quotas,
  or quotas read from Kueue, and an experimental optimizer that shares one
  quota between models;
- **prefill and decode** scaled as separate roles.

[How it fits together](concepts/architecture.md) has the diagrams.

## Who it is for

Teams that run [llm-d](https://llm-d.ai) on Kubernetes or OpenShift, with
several models or several ways of serving one, a GPU budget that binds, and
KEDA on the cluster.

## What operators need, and what it gives them

| What an operator running LLMs on Kubernetes needs | What llm-scaling-manager does |
| --- | --- |
| **New replicas that serve fast when traffic rises.** A replica can take minutes to load, and the spike does not wait. | A [warm pool](well-lit-paths/warm-pool-bridge/) of sleeping models serves during those minutes: measured p95 TTFT per scale-up of 0.11–0.83 s instead of 5.1–8.8 s. `make prepull`, `make weights` and `make engine-cache` take image pulls, weight downloads and kernel compilation off the [start path](reference/workload-preparation.md). |
| **Scaling early enough**, because by the time a queue forms the new replica is minutes late. | Demand is priced from the throughput one replica sustains, so a change in request shape orders capacity before a queue exists (measured: the third replica ordered with no queue and 5–15 % KV in use). |
| **Load measured as work, not as requests.** One request can cost a thousand times another. | Sized from KV-cache use, queues and token throughput per replica, never from request counts. |
| **Idle models off the GPUs, back when needed.** Most models in a large catalogue are idle most of the time. | [Scale to zero](well-lit-paths/scale-to-zero/), woken by the first request queued at the router, checked every 100 ms. A warm Pod switches models in 437 ms. |
| **Prefill and decode sized for their own bottlenecks.** | [P/D roles](well-lit-paths/pd-disaggregation/) are sized separately from their own demand and committed together, so one role cannot outrun the other. |
| **Many models on one GPU fleet**, where one model's growth is another's shrink. | Every model and variant is decided together inside one GPU budget. Priority-weighted rescale (off by default) and the experimental [utilization-share optimizer](well-lit-paths/utilization-share/) move GPUs between models as demand shifts. |
| **Mixed GPU generations.** | Each [variant](well-lit-paths/accelerator-variants/) carries its own per-replica capacity and cost; the optimizer grows the one with the lowest cost per unit of capacity. |
| **Hard limits per team.** | [Quotas per namespace or per cluster](well-lit-paths/tenant-gpu-quotas/), [quotas read from Kueue](well-lit-paths/kueue-bounded-quotas/), or the [GPUs physically free](well-lit-paths/bound-by-gpus/). |
| **Different urgency for interactive and batch work.** | [Workload classes](well-lit-paths/workload-classes/): named threshold tiers, set per ScaledObject. |
| **Decisions an operator can trust and explain**, when every replica costs dollars an hour. | A closed-form capacity model, two log lines per model per cycle that say why ([cycle log](reference/cycle-log.md)), Events and metrics for every decision. It decides; KEDA acts, so nothing new runs your pods. |

**Not covered yet:** forecasting load ahead of time, scaling to an SLA target,
pre-scaling before planned maintenance, and telling a failed node from a
replica that is still starting.

## What it was measured to do

All on H200 nodes unless noted. Each row is a single run per arm unless it
says otherwise; the linked page has the method and every caveat.

| Result | Number | Conditions | Source |
| --- | --- | --- | --- |
| The warm pool covers scale-ups | p95 time to first token per rise: 5.1–8.8 s without it, 0.11–0.83 s with a one-Pod pool | two 8B models bursting out of phase, 1 GPU per replica, 4 rises per arm | [warm pool, measured](well-lit-paths/warm-pool-bridge/measured.md) |
| ... and costs less than a floor | 14 138 GPU-seconds against 16 080 for a floor of 2 replicas (12 % less); 17 % more than no pool (12 129) | same run | [warm pool, measured](well-lit-paths/warm-pool-bridge/measured.md) |
| It sizes for a request shape before a queue forms | at a constant 6 req/s, no queue and 5–15 % KV in use, it ordered the replica the new shape needed: p95 TTFT 155 ms, 0 errors in 13 288 requests | Qwen3-0.6B, P/D path (experimental); **run twice, a day apart**, within a tenth | [P/D path](well-lit-paths/pd-disaggregation/) |
| A warm Pod switches models fast | 437 ms against about 41 s cold | H100, serving real gateway traffic, one measurement | [fast model loading](proposals/fast-model-loading.md) |
| Sharing one quota serves the first burst at once | p95 TTFT of the model whose burst comes first: 95 ms with sharing, 42 s without | two P/D 8B models, four roles under one 10-GPU quota; costs 36 % more GPU-seconds | [utilization share, measured](well-lit-paths/utilization-share/measured.md) |

![Warm pool: p95 time to first token per scale-up against GPU-seconds for autoscaling alone, a one-Pod warm pool, and a floor of 2 replicas](figures/warm-pool-latency-vs-cost.svg)

**Not yet measured:** a head-to-head of the default saturation scaling against
plain KEDA or HPA thresholds on this engine, and repeated runs with confidence
intervals for the warm pool.

## What it costs

- **The controller** is one Deployment that reads Prometheus. It holds no GPUs.
- **The warm pool** holds its GPUs continuously: in the run above, 17 % more
  GPU-seconds than scaling without one, 12 % fewer than a replica floor.
- **Sharing a quota** spends idle quota as headroom: 24–66 % more GPU-seconds
  in the measured runs.
- **Adopting it** takes a one-time cluster-admin setup per namespace, the EPP's
  `flowControl` feature gate, Prometheus scraping the model servers, and per
  workload: a ScaledObject (generated for you), a drain hook and fast starts.

## How mature each part is

| Scenario | Status | Evidence |
| --- | --- | --- |
| [Scale on saturation](well-lit-paths/scale-on-saturation/) | Stable | end-to-end suites; no benchmark against plain KEDA yet |
| [Scale to zero](well-lit-paths/scale-to-zero/) | Stable | end-to-end suites, cluster-verified |
| [Warm pool](well-lit-paths/warm-pool-bridge/) | Stable | end-to-end suites; one benchmark run per arm |
| [Bound by GPUs](well-lit-paths/bound-by-gpus/), [tenant quotas](well-lit-paths/tenant-gpu-quotas/) | Stable | end-to-end suites |
| [Workload classes](well-lit-paths/workload-classes/), [accelerator variants](well-lit-paths/accelerator-variants/) | Stable | end-to-end suites |
| [P/D disaggregation](well-lit-paths/pd-disaggregation/) | Experimental | benchmark run twice |
| [Kueue quotas](well-lit-paths/kueue-bounded-quotas/) | Experimental | unit and API-server tests; no live Kueue run |
| [Retained pool](well-lit-paths/retained-pool/) | Experimental | unit tests and a benchmark scenario; no end-to-end suite |
| [Sharing a quota](well-lit-paths/utilization-share/) | Experimental | unit and end-to-end tests; single benchmark runs |

Each path's *How it is tested* section names its suites and what they do not
cover.

## Against the alternatives

- **Plain KEDA or HPA on a metric** (queue length, KV use): no model of what a
  replica can serve, so it reacts after the queue forms, and it has no shared
  GPU budget across models. Not yet benchmarked head to head.
- **llm-d's workload-variant-autoscaler**: the project this one forked from.
  This fork answers KEDA directly over gRPC and adds the warm pool, the GPU
  budgets, scale-from-zero on a queued request and the install tooling.
- **NVIDIA Dynamo Planner**: forecasts load and profiles before deployment, for
  one model on one GPU type. [The comparison](comparison/) has the detail.

## Known gaps

No load forecasting; no scaling to an SLA target, only threshold tiers. A model
has to saturate once before its capacity is priced for a new request shape.
The warm pool and P/D numbers are single runs on one cluster type.

## Trying it

1. Install it in one namespace that already runs llm-d:
   [install in a namespace](guides/install-in-namespace/).
2. Register one model and watch its decisions for a day:
   [scale on saturation](well-lit-paths/scale-on-saturation/).
3. Add a GPU limiter, then the warm pool if traffic is bursty and models are
   slow to start. Treat the experimental paths as pilots.

## Backing out

Remove its ScaledObjects and KEDA returns each workload to the replica count it
had before autoscaling, or point their triggers back at KEDA's own scalers;
then uninstall ([upgrade and uninstall](reference/upgrade-and-uninstall.md)).
Kubernetes objects, make targets and metrics keep their `wva-` / `wva_` names
from upstream, which is the only naming to change in dashboards.

## Project status and support

An independently developed, advanced fork of
[llm-d/llm-d-workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler),
not affiliated with or endorsed by the llm-d project. Apache 2.0. Questions and
bugs: [GitHub issues](https://github.com/ev-shindin/llm-scaling-manager/issues).
