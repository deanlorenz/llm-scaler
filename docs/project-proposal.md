# llm-scaling-manager: project proposal

## Summary

**llm-scaling-manager is an analytical autoscaler for llm-d inference.** It
decides replica counts for every model and variant in a fleet at once, from one
GPU budget, using a capacity model rather than a threshold on a raw signal — and
it carries the capabilities that decision needs to be worth anything in
practice: a shared warm pool that bridges the minutes a replica takes to load,
scale-to-zero, and prefill/decode-aware scaling.

It exists because the signals a general-purpose autoscaler can trigger on —
request rate, queue depth, cache occupancy — have no stable relationship to how
loaded a GPU actually is. We have a run where all three said "do nothing" and the
correct answer was to triple the fleet. This document states that problem and the
three others like it, what we do about each, and the measurement that backs it.

It began as a fork of llm-d's Workload Variant Autoscaler
(`llm-d/llm-d-workload-variant-autoscaler`, the path still in this tree's
`go.mod`) and has diverged substantially since. A shorter version of this
argument, for readers outside the repository:
[Sub-second scale-ups on llm-d](blog/sub-second-scale-ups-on-llm-d.md).

## Motivation

KEDA and the HPA are good at what they were built for: turn a signal into a
replica count, quickly and safely. LLM inference breaks that in four ways.

### Problem 1 — the available signals do not mean anything stable

Request rate, queue length and request latency are the levers KEDA gives you. In
LLM serving none has a fixed relationship to GPU load, because the cost of a
request depends on its *shape* — tokens in, tokens out — and on the serving
configuration. Two requests to one model can differ by 1000× in compute. A
threshold that is right at one traffic shape is wrong at the next, and nothing in
the signal says it moved.

Measured, not argued. We ran a trace at a **constant 6 req/s** where only the
request shape changed halfway — 6000 in / 1000 out tokens, then 1000 in /
4000 out. One decode replica sustains ~5.4 req/s at the first shape and ~2.5 at
the second, so the correct fleet is **two replicas, then three**. What the
ordinary signals showed:

| signal | what it showed | what it implies |
| --- | --- | --- |
| request rate | constant, 6 req/s throughout | do nothing |
| gateway queue depth | **flat — no queue ever formed** | do nothing |
| KV-cache occupancy | 5–15 % in the steady first phase | **scale down to one** |

All three are wrong, and the last is dangerous: read alone it would have cut the
fleet to one replica immediately before the work per request quadrupled.

### Problem 2 — reacting after saturation is already too late

A reactive autoscaler is fine when a pod starts in a second. An LLM replica is
not available when it is scheduled; it is available when the model is loaded and
the kernels are compiled.

| | |
| --- | ---: |
| 8B model server, not running → first request served | **~41 s** |
| GLM-5.2-FP8 (744B MoE), warm node, weights from local NVMe | **192 s** |
| ...of which the weights are | **40 s** |
| ...the same start on a node with a cold JIT cache | **463 s** |

**The weights are a fifth of a GLM start.** So faster storage cannot fix this —
the rest is process spawn, imports, memory profiling, kernel warmup and
CUDA-graph capture.

### Problem 3 — the unit of waste is a GPU, and models compete for it

At $2–4/hr per accelerator the cost of being wrong is orders of magnitude higher
than in a CPU fleet, in both directions. And a general-purpose autoscaler decides
**one Deployment at a time**: it cannot know that scaling model A up may mean
scaling model B down, or that both draw on one finite pool.

### Problem 4 — one model is several workloads that scale differently

A production deployment is not one Deployment per model. The same model runs as
several **variants** — accelerators, tensor-parallel widths, quantizations — and
under prefill/decode disaggregation the halves are separate workloads with
opposite bottlenecks: prefill compute-bound, decode memory-bandwidth-bound.
Scaling one without the other moves the queue rather than clearing it.

### Goals

1. Scale on a signal that stays meaningful when traffic shape changes, with no
   per-model or per-workload threshold tuning.
2. Decide before the queue forms, and cover the load-time gap that remains.
3. Decide for the whole fleet against one GPU budget, not per Deployment.
4. Treat variants and P/D roles as first-class scaling units.
5. Publish the cost of every mechanism, including where it loses.

### Non-Goals

- Forecasting. The model is closed-form — we trade anticipation for a decision an
  operator can read and argue with.
- Node provisioning (cluster autoscaler), quota between models (Kueue), tenant
  quota (the gateway), routing and scheduling (gateway and EPP), and the scale
  operation itself (KEDA and the HPA). We decide; Kubernetes actuates.

## Proposal

### For Problem 1 — a universal threshold on a normalised quantity

Scale on **fleet utilization against a universal threshold** — up above 0.85,
release below 0.70 — where utilization comes from a capacity model rather than a
gauge. Demand for each role is floored at what the load requires *in throughput*:
arrival rate divided by the completion rate one replica sustained when last seen
saturated. That ratio is shape-dependent by construction, so when the shape moves
the price moves with it and the threshold never needs re-tuning.

**How we know it works.** In the run above the third replica was ordered at
**+1344 s — the same cycle the new shape's completion rate first came on
record** — and **no queue formed at any point** in the second phase. p95 TTFT
settled at 0.04–0.07 s. A cold controller with no completion rate yet sized the
same window by occupancy instead and paid **4.2 s p95**: the price of the
ordinary signal, measured. Run twice a day apart, landing within a tenth of every
figure.
→ [Scale a P/D-disaggregated model](well-lit-paths/pd-disaggregation/)

### For Problem 2 — order earlier, then bridge the rest

Because demand is priced from throughput rather than observed from a queue, the
order is placed before the queue exists. The remaining load time is covered by a
**shared warm pool**: a Pod holding an accelerator with models resident, lent to
a model that is scaling up so it serves while its own replica starts.

**How we know it works.** The second replica was ordered at **+53 s — from the
load, not from a queue, and before the first replica tipped into preemption**. On
a two-model benchmark with the models bursting out of phase:

| arm | p95 TTFT per rise | GPU-seconds |
| --- | --- | ---: |
| autoscaling alone | 5.1 – 8.8 s | **12 129** |
| + one-Pod warm pool | **0.11 – 0.83 s** | 14 138 (+17 %) |
| floor of 2 replicas per model | 0.09 – 0.13 s | 16 080 (+33 %) |

A pool Pod serving real gateway traffic switched models in **437 ms against a
~41 s cold start**.
→ [What a warm pool buys, measured](well-lit-paths/warm-pool-bridge/measured.md)

### For Problem 3 — one joint decision inside one budget

Every model, every variant, one GPU budget, one allocation, re-solved each cycle.
A declared limiter constrains every decision against that budget. The warm pool
is the same idea in hardware: **one** held accelerator insuring **several**
models, so its economics improve with each model added rather than degrading.

**How we know it works.** The pool costs **12 % less than the floor it replaces**
at the same latency. We also publish the uncomfortable number: **autoscaling
alone is the cheapest arm** — if you hold nothing today and can live with
multi-second rises, keep holding nothing. The pool is for people who would
otherwise hold a floor.
→ [GPU capacity accounting](concepts/gpu-capacity-accounting.md)

### For Problem 4 — the variant is the scaling unit

A **variant** is one ScaledObject and the workload it scales. Variants whose
triggers name the same model are solved together, each role carrying its own
target and its own bottleneck. The cost-aware optimizer chooses among accelerator
variants rather than assuming one GPU type.

**How we know it works.** In the P/D run decode scaled 1 → 2 → 3 while **prefill
was never ordered at all**, because prompts waiting at the scheduler are no
longer charged to it as resident KV.
→ [Accelerator variants](well-lit-paths/accelerator-variants/)

### Also shipped

- **Scale-to-zero and wake**, through KEDA's push path rather than a poll.
  → [scale-to-zero](well-lit-paths/scale-to-zero/)
- **Quota-bounded scaling**, against a GPU budget and against Kueue quota where
  that is the boundary. → [bound by GPUs](well-lit-paths/bound-by-gpus/) ·
  [Kueue-bounded quotas](well-lit-paths/kueue-bounded-quotas/)
- **Workload classes** — interactive and batch tiers sharing a fleet under named
  policies. → [workload classes](well-lit-paths/workload-classes/)

## Design details

The decision path is documented rather than summarised here: what is measured and
how a measurement becomes a replica count in
[the steady-state engine](concepts/steady-state-engine.md); the queueing model
and the optimization in
[modeling and optimization](concepts/modeling-and-optimization.md); what the GPU
budget means, and three ways a naive reading over-states free capacity, in
[GPU capacity accounting](concepts/gpu-capacity-accounting.md).

Actuation is the KEDA external-scaler gRPC contract; KEDA owns the HPA and writes
the scale subresource. Workloads are learned from the KEDA calls themselves —
no watch, no listing, no opt-in annotation — so a newly registered workload is
managed on its first call, which is what a self-service platform needs.

## Alternatives considered

Each of these was tried or measured, not reasoned away.

**Threshold autoscaling on the signals KEDA already has.** Ruled out by the
shape-swap run above: at a constant rate, with no queue, KV occupancy said
"scale to one" when the answer was three.

**Hold a floor of replicas instead.** Works — it is the closest competitor on
latency, within tens of milliseconds of the pool. Rejected on cost: **+33 %
GPU-seconds against autoscaling alone**, where the pool is +17 %, so the pool is
12 % cheaper for the same result. A floor is still the better choice when one
model is always bursting and the fleet never returns to its floor.

**Faster storage, bigger page cache, or peer-to-peer weight transfer.** Ruled out
as a *latency* fix by measurement: the weights are 40 s of a 192 s GLM start, so
a perfect transfer takes it to ~152 s. A weights PVC measured 430 MB/s — it
avoids re-downloads, not cold starts. Peer transfer is worth having (byte-
identical, ~9× faster than reloading from storage) but it does not solve this.

**Process snapshots**, which would remove the ~152 s no transfer can touch.
Built, and it does not work: clean on a single process — GPU released to 0 MiB,
dumped, restored, identical checksum — and it **hangs on a real multi-rank
engine**, with the driver's own thread spinning after the data movement
completes. Fifteen experiments narrowed it to NVIDIA's checkpoint path rather
than anything the inference server can release.

**A GPU-less warm launcher**, holding no accelerator. Ruled out: an unbound
sleeper often cannot wake at all. Holding the accelerator is what makes the wake
possible.

## What is still open

**Prediction.** A replica is ready minutes after the decision, so the right
question is where load will be *then*. Against a 152 s construction floor on a
large model, being closed-form is a real gap, not only a stylistic choice.

**SLA-target-driven scaling.** Scaling on *"will this batch tier miss its
deadline"* rather than *"is utilization above threshold"* is not built; named
policy tiers are a coarse approximation. Nobody upstream has solved this either.

**Maintenance pre-scaling and failure replacement.** Not built.

**A shape never seen saturated** is sized from occupancy until the first
saturated reading arrives — the 4.2 s cold-pass window above is exactly that
cost.

## Status

Running on CoreWeave H200s and on OpenShift. The scaling path, warm pool,
scale-to-zero and quota limiting are built and cluster-verified; replica
reallocation across priorities is designed and not built; P/D role switching is
experimental. Apache 2.0, and the Go module path is unchanged, so imports do not
move.
