# Hold several large models on one set of GPUs

## Start here

- **Status:** Experimental: the rule that picks which model is awake is covered by unit tests only (below).
- **You need:** [the saturation path](../scale-on-saturation/#start-here) in place; accelerators for a pool Pod sized for the largest model, with a memory limit that holds the whole warm set; the model-cache claim and images [every pool needs](../../guides/warm-pool/install.md#prerequisites).
- **Do this** ([the warm pool guide](../../guides/warm-pool/)):
  1. (namespace owner) [Check the models can be warmed here](../../guides/warm-pool/README.md#should-this-model-be-warmed-here-at-all) (`deploy/warmpool.sh sizing`) and [how many fit in one Pod](../../guides/warm-pool/sizing.md#how-many-models-fit-in-one-pod).
  2. (namespace owner) [Create the pool as retained](../../guides/warm-pool/multi-pool.md#retained-pools-which-model-holds-the-gpus) (`deploy/warmpool.sh create --type retained`) and point the models at it with `warmPool:` ([how](../../guides/warm-pool/install.md#adding-and-removing-a-pool-later)).
  3. (namespace owner) Set the two switch knobs in the trigger metadata of the pool's ScaledObject: `kubectl edit scaledobject wva-warm-pool-<pool> -n <ns>`. `warmPoolSwitchSpareThreshold` (a percent) is optional — unset, the pool switches only when a model needs to scale up; `warmPoolMinSwitchInterval` defaults to `10m` ([What it needs](#what-it-needs)).
- **Check it works:** `retained pool is switching` in the controller log when demand moves ([Verifying it](#verifying-it)); the pool's own state in [Checking it works](../../guides/warm-pool/operating.md#checking-it-works).
- **Undo:** (namespace owner) `deploy/warmpool.sh delete` ([Remove one](../../guides/warm-pool/install.md#adding-and-removing-a-pool-later)). Pass `--type retained` on every re-run of `create`, or the pool turns back into a bridge.

## What it is

> **Experimental.** The rule is covered by nine unit test files and a benchmark
> variant config, and it has been exercised on a cluster. One end-to-end spec,
> on the simulator, covers a retained pool: it keeps the Pod it lent past the
> hold timeout. **No end-to-end spec covers the switch between models**, and no
> benchmark run is written up. Those are the missing legs. Everything else here
> is built and shipped.

Some models are too large to start on demand: by the time one loads, the request
that wanted it has timed out. For those, a warm pool stops being insurance and
becomes the serving capacity itself. The models live in the pool, exactly one is
awake at a time, and no ordinary replicas are coming — so something has to
decide **which model holds the accelerators**, or whichever one woke first keeps
them however the load moves.

**Use it when** you have more large models than you have GPUs to run them on,
their traffic does not peak together, and a switch between them is cheaper than
buying hardware for the sum of them.

**Do not use it when** two of the models are busy at the same time. A retained
pool cannot make two models comfortable at once, and it knows that — it will
refuse to switch rather than move the shortage from one model to the other and
pay a drain and a wake to do it. If your models peak together, this path is the
wrong shape and the answer is more accelerators.

**This is not the bridge case.** If your models start fast enough that ordinary
replicas do arrive, you want
[the warm pool bridge](../warm-pool-bridge/) instead, where the pool covers the
gap and hands the work back.

## What it needs

- A pool sized for the largest model, matching one shape: GPUs per Pod, how
  they are divided, and the accelerator — a warm copy is only reusable on the
  one it was loaded on.
- Memory limits that hold the whole warm set. A level-1 sleeper is charged for
  its weights in shared memory, so the pod template's memory limit is the budget
  and one model too many OOM-kills the launcher, taking **every resident model**
  with it.
- The pool declared as retained. `create` takes it, so the pool is never a
  bridge for the window between creating it and patching it:

```bash
deploy/warmpool.sh create -n <ns> --name <pool> --type retained \
  --models 2 --model-size 8B --accelerator <product> --wva-namespace <wva-ns>
```

  `--type bridge --max-hold 5m` is the other choice; the two together are
  refused, because retention is precisely what switches the hold timeout off.
  Naming neither leaves the controller's own default in force rather than
  writing a second copy of it into the object.

- The remaining retained knobs, in the pool ScaledObject's trigger metadata:

```yaml
warmPoolRetained: "true"              # what --type retained writes
warmPoolSwitchSpareThreshold: "20"    # percent; unset = switch only on scale-up need
warmPoolMinSwitchInterval: "10m"      # default 10m
```

## The rule that decides who is awake

In the order it is applied:

1. **A candidate must be under more pressure than the model already awake** —
   either below the spare threshold when the awake one is not, or wanted for
   scale-up when the awake one is not.
2. **Equal pressure is not enough.** If the awake model is *also* short, nothing
   moves.
3. **Ties go to the tightest** — least spare capacity wins.
4. **No switch within `warmPoolMinSwitchInterval`, unless the candidate needs to
   scale up now.** A model heading toward trouble can wait; one short of
   capacity cannot, and no replicas are coming to relieve it.

Spare is a fraction of each model's *own* supply, so one threshold compares
models of different sizes. Nothing is decided from a reading that does not
exist: an unmeasured model is neither a candidate nor a reason to stay put.

## Setting it up

[The warm pool guide](../../guides/warm-pool/), which covers pool sizing, the
warm-set budget, and the retained section specifically. `deploy/warmpool.sh plan`
groups a namespace's models by the shape a pool can serve and prints the create
lines.

## Verifying it

```bash
# which model the pool is holding awake, and why it moved
kubectl logs -n <controller-namespace> deploy/<wva> | grep "retained pool is switching"
```

The line names the model, the reason and the interval. The decision **not** to
switch is logged too, at debug, with its reason — which is the more useful of
the two when you are asking why nothing happened.

## What it costs

The whole pool, continuously: those accelerators serve one model at a time and
are unavailable to anything else. That is the trade — you are buying the ability
to serve N models from the hardware for one, at the price of a switch whenever
demand moves.

## How it is tested

- Unit: `internal/warmpool/policy/retained_test.go`,
  `policy/switch_test.go`, `policy/intent_test.go`, `retained_pass_test.go`,
  `retained_stay_log_test.go`, `switch_test.go`, `switch_storm_test.go`,
  `internal/decision/awake_test.go`, and
  `internal/engines/analyzers/saturation/warmpool_bridge_test.go` — covering
  the pressure comparison, the tie-break, interval preemption, and the switch
  storm that an earlier version of this rule produced.
- Benchmark: `hack/benchmark/scenarios/guides/variants/v2-retained-switch.yaml`;
  the harness's own start-path settings are in [Replica start time in the harness](../../guides/benchmarking/README.md#replica-start-time-in-the-harness).
- End-to-end: `test/e2e/warm_pool_retained_test.go` (`Warm pool - a retained
  pool does not churn its Pod`, labels `full` and `warmpool-retained`) — a
  retained pool does not reclaim the Pod it lent once the hold time has passed.
  It needs the simulator (`--fake-metrics`) and skips without it.
- **Not covered end to end:** the switch itself — which model is awake, the
  tie-break, interval preemption. Those are unit-tested only.

## Tuning it

The knobs above, and the design reasoning — including why a switch needs both a
threshold and an interval — are in
[proposals/warm-pool-retained.md](../../proposals/warm-pool-retained.md).
