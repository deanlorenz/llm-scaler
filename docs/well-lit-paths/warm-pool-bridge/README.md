# Bridge a scale-up with a warm pool

## Start here

- **Status:** Stable.
- **You need:** [the saturation path](../scale-on-saturation/#start-here) in place; accelerators free for the pool, of the same shape as the models it warms; the RWX model-cache claim the model servers already mount; the pool's two images, pullable from the pool's nodes: the launcher `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher` (external, from Fast Model Actuation, not built here) and the proxy `ghcr.io/ev-shindin/llm-scaler/warmpool-proxy` (or your `--proxy-image`). On a mirrored or air-gapped registry, mirror both first ([the images](../../guides/warm-pool/install.md#the-images-a-pool-pod-runs)).
- **Do this** ([Bridge a scale-up with a warm pool](../../guides/warm-pool/)):
  1. (namespace owner) [Check the model can be warmed here](../../guides/warm-pool/README.md#should-this-model-be-warmed-here-at-all) (`deploy/warmpool.sh sizing`) and [which pools you need](../../guides/warm-pool/README.md#which-pools-do-you-need) (`deploy/warmpool.sh plan`).
  2. (namespace owner) Meet the [pool's prerequisites](../../guides/warm-pool/install.md#prerequisites): the cache claim and [the images](../../guides/warm-pool/install.md#the-images-a-pool-pod-runs).
  3. (namespace owner) [Create the pool and point the models at it](../../guides/warm-pool/install.md#adding-and-removing-a-pool-later) (`deploy/warmpool.sh create`, then `warmPool:` in each model's trigger), or [at install time](../../guides/warm-pool/install.md#turning-it-on-during-installation).
- **Check it works:** a `warm pool state` line in the controller log, then `wva_warmpool_borrow_total{outcome="hit"}` rising on a scale-up ([Checking it works](../../guides/warm-pool/operating.md#checking-it-works)).
- **Undo:** (namespace owner) `deploy/warmpool.sh delete`, and remove `warmPool:` from the models ([Remove one](../../guides/warm-pool/install.md#adding-and-removing-a-pool-later)).

## What it is

A scale-up is slow because a new replica must load a model before it can serve.
A warm pool holds a few Pods with models already loaded and asleep, and lends
one to a variant that is scaling up. The borrowed Pod joins the model's
InferencePool and serves while the ordinary replica loads, then is handed back
the moment that replica is ready.

**Use it when** spikes arrive faster than a replica starts and the cost of the
gap — queued requests, missed TTFT, a load shed — is worth holding accelerators
to cover. Two shapes of traffic make that true:

- **several models with bursty traffic** that does not all burst at once: one
  pool Pod holding all of them bridges every model's rise, and the more models
  share it the cheaper each one's insurance gets;
- **scale-to-zero with a fast scale-up requirement**: a parked model's first
  request would otherwise wait for a cold start, and the bridge serves it
  while the ordinary replica loads.

How much it buys, and what it costs against a floor of held replicas, is
[measured](measured.md) — in one benchmark run per arm.

**Do not use it when** load is steady, or when accelerators are the scarce
thing. The pool is **insurance, not capacity**: every pool Pod holds its
accelerators continuously, lending or idle, and a pool of N lowers your maximum
fleet by N. That is a cost decision, which is why the scaling manager never creates a pool for
you.

## What it needs

- Accelerators free for the pool itself, matching the shape of the models it
  will warm in **three** ways: GPUs per Pod, how those GPUs are divided (16
  over two Pods and over four are the same count and different engines), and
  the accelerator, since a warm copy is only reusable on the one it was loaded
  on. Nothing *refuses* a mixed pool and it does not fail loudly — the reserve
  is counted pool-wide, so part of it cannot serve the model you are holding it
  for. A model that fails any of the three is never warmed by that pool: it is
  a configuration error, not something to wait out.
- A shared model cache the pool can read, so a warm copy loads from local
  storage rather than a download.
- RBAC letting the scaling manager `patch` Pods. The shipped ClusterRole has it; a hand-scoped
  one may not, and the scaling manager refuses to start a pool it could never lend from.
- Two container images. The pool proxy is ours; the launcher depends on the
  pool shape — a pool of Pods runs upstream Fast Model Actuation's launcher,
  and a pool of **groups** runs our fork (`ghcr.io/ev-shindin/fma-launcher`),
  where `--launcher-image` is required and `create` refuses without it.
- A pool whose replica count **exceeds** its reserve. A pool that does not can
  never warm anything at all.

## Setting it up

[Bridge a scale-up with a warm pool](../../guides/warm-pool/) — sizing, the two
images, the NetworkPolicy, and how the pool is declared. The pool is an ordinary
Deployment; its knobs are trigger metadata on the ScaledObject that declares it,
and `deploy/warmpool.sh create` writes them.

## Several models in one pool

One pool holds many models, and that is the normal case rather than an advanced
one. Three decisions come with it:

**Which models can share a pool.** A pool serves one **(accelerator,
GPUs-per-replica)** shape, because neither is negotiable at run time. Every
other difference between models — size, traffic, policy — a pool absorbs. Two
GPU models on the cluster therefore want two pools: one pool can be pointed at
both, but the reserve is counted pool-wide, so part of it would be held for a
model it cannot serve.

```bash
deploy/warmpool.sh plan -n <namespace>
```

`plan` groups the namespace's model ScaledObjects by that pair, says which could
share a pool, and names models that select a pool nobody declared — worse than
selecting none, because it reads as configured.

**How many fit in one Pod.** The pod template's memory limit is the warm-set
budget, not a safety margin: a level-1 sleeper's weights are charged to the
cgroup in shared memory. Measured on an H100, a resident model costs roughly
2.6 GiB + 1.4x its weights, so a 128Gi Pod holds about five 8B models and hits
the hard ceiling of 16 instances long before memory on anything smaller than
about 3B. Overcommitting does not fail politely: it OOM-kills the launcher and
takes **every model already resident in that Pod** with it. Changing the limit
rolls the pool, so size it at deploy time.

**Which of them stay warm.** By default the pool ranks: parked models first,
then the busiest, then anything that has missed twice — right for almost
everything. Two things it cannot infer are set per model in the ScaledObject
trigger:

| `warmPoolCopies` | means |
| --- | --- |
| *absent* | automatic — the pool ranks it and holds at most one copy |
| `"0"` | never warm this model, and release it if it already is |
| `"1"` | always keep one warm, whatever the ranking thinks |
| `"N"` | keep N warm, so N scale-ups of this model can bridge at once |

If your models are too large for ordinary replicas to arrive at all, the pool
stops being a bridge and becomes the capacity itself — that is
[the retained pool path](../retained-pool/).

## Verifying it

```bash
# the pool exists and holds what it says
kubectl get deploy -n <ns> -l app.kubernetes.io/component=warm-pool

# a bridge was actually lent, and to whom
kubectl logs -n "$NS" deploy/wva-controller-manager | grep warm-pool-bridge   # NS: the controller's namespace
```

A bridge is **not** one of the variant's own serving replicas, and the engine
prices it apart from them — so a bridged variant showing one more Pod than
`wva_current_replicas` is correct, not a miscount.

## What it costs

N accelerators, continuously, for as long as the pool exists. Size it by how
often you spike and how long a replica takes to start — never by peak load.

**Measured:** [What a warm pool buys](measured.md) — two models bursting out
of phase with quiet stretches between, autoscaling alone against a one-Pod
pool against a floor of two replicas, with the tables, the plots and the make
targets to repeat it on your cluster. These figures are **one benchmark run per
arm** on H200 nodes, four scale-up events each, so the margins are one run's,
not a distribution. The short version: the pool turns rises of 5.2–8.8 s at
p95 into 0.11–0.83 s, within 50 ms of the floor on three of the four rises, for
17 % more than autoscaling alone and 12 % less than the floor. The pool's
advantage grows with the number of models sharing it, not with more quiet;
when a model is always bursting, a floor is the cheaper insurance.

## How it is tested

End-to-end, nine suite files in `test/e2e/`. Eight are labelled `full` and
run by `make test-e2e-full`: `warm_pool_test.go`, `warm_pool_borrow_test.go`,
`warm_pool_return_drain_test.go`, `warm_pool_group_test.go`,
`warm_pool_policy_test.go`, `warm_pool_quota_test.go`,
`warm_pool_attribution_test.go` and `warm_pool_retained_test.go` (simulator
only) — covering the borrow, the hand-back, the controller's drain on return,
multi-GPU groups, the policy that vets an engine before pointing at it, quota
refusal, how a bridge is attributed, and a retained pool keeping its lent Pod.
The ninth, `warm_pool_drain_gpu_test.go` (a returned Pod finishing what it was
writing), is labelled `gpu` only, so neither `make test-e2e-smoke` nor
`make test-e2e-full` runs it.

## Tuning it

How long the pool lends before reclaiming is `--max-hold` at create, and it is
the knob this path turns: too short and the bridge is pulled before the ordinary
replicas arrive, too long and the pool is unavailable for the next model.
`wva_warmpool_bridge_seconds` should track the ordinary start time — sitting at
`--max-hold` instead means the scale-up never completed and the pool is masking
it. Pool size, the retained-model rule and the switch interval are trigger
metadata too, documented in the guide. The reasoning behind the design, including what was
measured and rejected, is in
[proposals/fast-model-loading.md](../../proposals/fast-model-loading.md).
