# llm-scaling-manager and KEDA alone

**KEDA scales your pods in both setups.** This page is about the one thing that
differs: what computes the replica count KEDA applies.

- **KEDA alone** turns metrics into replicas with thresholds. With a Prometheus
  trigger at `metricType: AverageValue`, the count is the metric's sum divided
  by the threshold, rounded up; when a ScaledObject has several triggers, the
  HPA takes the largest count.
- **With llm-scaling-manager**, KEDA's trigger is an
  [external scaler](../reference/scaledobject-trigger.md) that answers with a
  replica count. The count comes from a capacity model of each replica, decided
  for every model and variant it manages together, inside a GPU budget when
  you configure one. KEDA still owns activation, the HPA and its behaviour
  windows, fallback and pausing.

So the question is not which tool to install: KEDA is required either way. It
is whether a threshold per workload is enough for your fleet.

## What "KEDA alone" means here

llm-d's own guidance, at llm-d commit
[`7d5f86d`](https://github.com/llm-d/llm-d/tree/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/guides/workload-autoscaling)
(2026-10-14). llm-d now recommends KEDA with the router's (EPP) metrics and has
stopped developing its workload-variant-autoscaler, of which this project is an
advanced fork. Its recommended default, the
[queue recipe](https://github.com/llm-d/llm-d/blob/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/guides/workload-autoscaling/keda-epp/optimized-baseline/overlays/k8s/queue/scaledobject.yaml),
is:

| Setting | Value |
| --- | --- |
| Triggers | `sum(llm_d_epp_flow_control_queue_size)` at 1 per replica, and `sum(llm_d_epp_request_running)` at 16 per replica |
| Windows | 300 s stabilization on scale-up **and** scale-down; polling 15 s; cooldown 300 s (scale to zero only) |
| Bounds | min 1 (0 optional), max 8 |

llm-d also publishes experimental recipes: router pool saturation, token-aware
sizing (in-flight tokens over a calibrated prefill throughput, and summed KV
use, with per-role ScaledObjects for prefill/decode), an SLO-aware control law,
and Kueue cohorts for a GPU budget below the autoscaler. The table names them
where they answer a need the default does not.

llm-d states the default's limits itself: *"No cross-variant placement"*, *"No
cost-aware choice of hardware"*, *"Warmup is paid at every scale-up"*, and its
thresholds *"are not universal … Do not assume a value validated for one
model, accelerator, or tensor-parallel degree transfers to another"*
([guide](https://github.com/llm-d/llm-d/blob/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/guides/workload-autoscaling/keda-epp/README.md)).
Its architecture page adds that KEDA + EPP *"does not replace WVA feature for
feature"* and that deployments relying on cost-aware placement or fair sharing
of a scarce accelerator pool should *"stay on WVA for now"*
([architecture](https://github.com/llm-d/llm-d/blob/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/docs/architecture/autoscaling/README.md)).

## What each can do

Evidence levels follow [why.md](../why.md#how-mature-each-part-is).

| Need | llm-d's KEDA recipes | With llm-scaling-manager | Evidence for our column |
| --- | --- | --- | --- |
| **What sets the count** | Thresholds per ScaledObject, tuned per model, accelerator and parallelism (llm-d's caveat). Token-aware recipe: one calibrated prefill throughput per hardware and model. | A capacity model per replica, from KV-cache use, queues and the token throughput one replica sustains, learned while it serves. Thresholds are tiers of how full a replica may run, not per-model numbers. | [Scale on saturation](../well-lit-paths/scale-on-saturation/): end-to-end suites |
| **Requests of very different cost** | Running requests count a 100-token and an 8 000-token request alike; summed KV use follows occupancy, which lags a shape change. | Demand is priced from measured throughput, so a change in request shape can order capacity before a queue forms. | Experimental P/D path, [run twice](../well-lit-paths/pd-disaggregation/#measured): held 2 replicas at 5–15 % KV where occupancy alone said 1; ordered a 3rd at the shape change; no queue formed. That was the second exposure; a cold controller sizes from occupancy first |
| **Reacting to a rise** | The default delays every scale-up by its 300 s window, which llm-d sets to absorb startup overshoot and calls *"blunt"*. | Scale-up acts at once (0 s window); the capacity model counts replicas that are still starting, so it does not re-order them while they load. | End-to-end suites; no benchmark against the 300 s recipe yet |
| **The cold start of a new replica** | Paid at every scale-up. llm-d's fast-model-actuation benchmark measured its cold baseline at p95 TTFT 52–74 s on Qwen3 14B/32B ([results](https://github.com/llm-d/llm-d/tree/7d5f86d1de99c9df39f58eba0c8a7e20c00cfaa4/guides/fast-model-actuation/fast-model-actuation-keda/benchmark-results)). | Optional [warm pool](../well-lit-paths/warm-pool-bridge/): sleeping models lend a replica while the variant's own starts. | p95 TTFT per scale-up 0.11–0.83 s with a one-Pod pool against 5.2–8.8 s without it, on two 8B models; one run per arm. Different models and load from llm-d's figures, so not a head-to-head |
| **Several variants of one model** (GPU types, TP sizes) | One ScaledObject per Deployment; *"no cross-variant placement"*. | Variants decided together: scale-up grows the one with the lowest cost per unit of capacity, scale-down sheds the highest `variantCost` first. | [Accelerator variants](../well-lit-paths/accelerator-variants/): one GPU type, two TP sizes; not measured across GPU types |
| **Prefill and decode** | Token-aware and blueprint recipes: one ScaledObject per role, independent, no coupling of the two. | Each role sized from its own demand, and the pair committed together so one role cannot outrun the other. | [P/D disaggregation](../well-lit-paths/pd-disaggregation/): experimental, benchmark run twice |
| **A GPU budget** | Below the autoscaler: Kueue cohorts with per-model floors, borrowing and preemption (experimental); demand over budget waits as Pending Pods. | Optional `limiters:` (GPUs free, a quota per namespace, or the quotas Kueue holds). Each model is fitted inside the budget before KEDA sees a number, and a capped decision raises `wva_decisions_limited_total` and a `ResourceConstrained` Event. Advisory: a `ResourceQuota` stays the hard bound. | [Bound by GPUs](../well-lit-paths/bound-by-gpus/): end-to-end; [tenant quotas](../well-lit-paths/tenant-gpu-quotas/): unit tests, refusal path end-to-end; [Kueue](../well-lit-paths/kueue-bounded-quotas/): experimental |
| **Many models on one fleet** | Each ScaledObject scales alone; Kueue decides who is admitted. | Opt-in priority-weighted rescale under a budget (unit-tested only), and the experimental [utilization-share optimizer](../well-lit-paths/utilization-share/), which moves GPUs between models as demand shifts. | Utilization share: single benchmark runs per arm |
| **A latency SLO** | The SLO-aware recipe scales on p90 TTFT/TPOT against the SLO, *"one variant, one SLO pair"*; llm-d measured 95.8 % attainment at 5.93 average replicas against a static 8. | No SLO target: replicas are sized from capacity, and tiers set how full they may run. | Not applicable |
| **Interactive and batch urgency** | A different threshold per ScaledObject. | Named tiers in one policy ([workload classes](../well-lit-paths/workload-classes/)). | End-to-end suite (simulator) |
| **Scale to zero** | KEDA's own: `minReplicaCount: 0`; the router buffers requests and its queue metric wakes the target. A request queued at zero is lost if the router restarts (llm-d's caveat). | KEDA's own mechanism, plus the controller reads the router's queue every 100 ms and pushes activation, instead of waiting for the next poll. Only the 0 → 1 step is pushed. The wake is still a cold start, about 41 s on an H100 test cluster, unless a warm pool holds the model. | [Scale to zero](../well-lit-paths/scale-to-zero/): end-to-end, cluster-verified |
| **Flapping** | HPA behaviour windows (300 s both ways in the default) and the HPA's 10 % tolerance. | The same HPA behaviour (our generated ScaledObject: 0 s up, 300 s down), plus holds in the controller (sticky scale-down, shape-change holds). | End-to-end suites |
| **Explaining a decision** | Each trigger's metric value, the HPA's current and desired counts. | Two log lines per model per cycle that say why ([cycle log](../reference/cycle-log.md)), Events, `wva_model_scaling_blocked` reasons, dashboards. | [Monitoring](../reference/monitoring.md) |
| **When the signal source fails** | KEDA `fallback`, if configured; Prometheus is the only dependency. | An extra controller in the path. Our generated ScaledObjects set no `fallback` (the template sample sets `currentReplicasIfHigher`); if the controller is unreachable when KEDA first builds the HPA, KEDA falls back to a CPU metric. | [Trigger reference](../reference/scaledobject-trigger.md) |

## Where KEDA alone is enough

KEDA alone is one component fewer, and llm-d's recipes are maintained upstream.
It is the better choice when:

- **One model, one GPU type, one request shape**, and you can tune a threshold
  for it and re-tune when traffic changes.
- **No shared GPU budget**: each model has GPUs reserved for its maximum, or
  Kueue cohorts are enough.
- **You scale to a latency SLO** on a single variant: llm-d's SLO-aware recipe
  targets it directly; we do not.
- **Latency first, cost second**: a low threshold over-provisions, which is the
  safe direction when the tail is the SLO.

## What it costs to add llm-scaling-manager

- **One more controller in the scaling path.** KEDA asks it for every count. It
  runs leader-elected and holds no GPU, but its availability now matters; set a
  `fallback` on your ScaledObjects.
- **Engine and router metrics.** vLLM or SGLang scraped by Prometheus, and the
  router's (EPP) metrics for queues and waking from zero.
- **It learns.** A controller that has never seen a model or a request shape
  saturated sizes it from occupancy first; the throughput model applies from the
  second exposure ([known gaps](../why.md#known-gaps)).

## Measured against KEDA alone

A five-run study in July 2026 compared the two on single-variant workloads, with
KEDA actuating both arms: the
[scaling study](../../comparison/wva-vs-keda-scaling-study.md). The threshold
arm held 1.9–2.7× the GPUs at equal service in the four runs below saturation;
past saturation on prefill-heavy traffic, it had the better tail.

**Read it as history.** Both sides have moved since:

- **The recipe.** Our arm used 16 running requests and a queue of 1 per replica,
  as llm-d's recipe still does, but with max 10 and a 120 s scale-down window,
  where the recipe shipped max 8 and 300 s. llm-d has since added a 300 s
  scale-up window against startup overshoot, the very behaviour the study
  measured.
- **The controller.** The capacity model has changed: backlog priced by
  throughput, the arrival floor, shape-change holds and sticky scale-down. The
  study's one loss (Run 3's tail) has not been re-run.

A re-run of the threshold arm on llm-d's current default recipe is planned.
