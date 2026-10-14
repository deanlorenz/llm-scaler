# Serve one model on two accelerator variants

One model, deployed twice on different hardware or different tensor-parallel
widths, behind one InferencePool and one EPP. Each deployment has its own
ScaledObject, and both carry the same `modelID` in their trigger metadata — so
The scaling manager groups them and scales the group, not the deployments.

On scale-up the optimizer grows the variant with the **lowest cost per unit of
capacity** — `variantCost` divided by the capacity one replica of that variant
serves — which is not the same as the cheapest replica. Scale-down does **not**
use that ratio: it removes replicas from the variant with the highest
`variantCost` per replica first (ties broken by the analyzer's score, then by
name), and keeps at least one replica on the cheapest variant while no more
expensive variant holds any. So a variant that is grown first for efficiency can
also be the one shed first, if its replicas are the dearer ones. With the shipped
example pricing — a TP=2 primary at cost 10 and a TP=1 secondary at cost 5 —
both cost the same per GPU, a TP=2 replica serves more than twice a TP=1 one, so
the primary is the more efficient variant and grows first.

**Use it when** you have more than one accelerator type, or a model that runs at
two tensor-parallel widths, and you would rather the autoscaler chose between
them than have a human split the traffic.

**Do not use it when** the variants serve different models. Grouping is by
`modelID`; two models are two problems, and each takes
[the saturation path](../scale-on-saturation/) separately.

## What it needs

- One InferencePool and one EPP in front of both deployments.
- The same `modelID` in each ScaledObject's trigger metadata. This is the join;
  get it wrong and you have two independent variants that happen to serve the
  same weights.
- A relative cost per variant, as `variantCost` in the trigger metadata
  (default `"10.0"`; see [the trigger reference](../../reference/scaledobject-trigger.md)).
  Set it to the price of **one replica**: GPUs per replica × the relative price
  of that GPU. It is a relative scalar, not a currency: the scaling manager does
  not read real prices from anywhere. The `llm-d.ai/variant-cost` annotation is
  no longer read; a value left there is ignored.

## Setting it up

The topology, the manifests and the run are in
[the two-variant benchmark](../../developer-guide/two-variant-wva-benchmark.md),
which is where this path is exercised end to end. For an ordinary install,
register both deployments the usual way — `make scaledobjects-plan` shows them
as two entries sharing one model — and set each entry's `variantCost` in the
plan before `make scaledobjects-apply` (the plan writes it into the trigger).

## Verifying it

```bash
# both variants under one model, with their own targets
wva_desired_replicas{exported_namespace="<ns>"}

# which variant the optimizer grew, and why it was the efficient one
kubectl logs -n <controller-namespace> deploy/<wva> | grep analyzer-result
```

The `variants` array in the `analyzer-result` line names each variant, its
per-replica capacity and the reason code behind it — see
[the cycle log](../../reference/cycle-log.md).

## What it costs

No extra accelerators of its own; the point is to use fewer. What it costs is
accuracy in `variantCost` — a wrong relative cost produces a confident
decision in the wrong direction.

## How it is tested

- Benchmark scenario:
  `hack/benchmark/scenarios/guides/two-variant-wva.yaml`, with the variant
  definitions under `scenarios/guides/variants/`; the harness's own start-path settings are in [Replica start time in the harness](../../guides/benchmarking/README.md#replica-start-time-in-the-harness).
- **No end-to-end suite covers two variants of one model.** The evidence for
  this path is the benchmark scenario and its written-up run, not a CI spec —
  which is the leg to add if you depend on it.

## Tuning it

Per-variant scoring, analyzer selection and policy tiers:
[scaling policy](../../reference/scaling-policy.md). How capacity per variant is
derived, including from deployment arguments:
[the steady-state engine](../../concepts/steady-state-engine.md).
