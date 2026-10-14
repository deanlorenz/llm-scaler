# Scale to zero, and get back

## Start here

- **Status:** Stable.
- **You need:** [the saturation path](../scale-on-saturation/#start-here) in place; the EPP's `flowControl` gate parsed by the running EPP pod (it is the wake signal); a model that has served at least one request.
- **Do this** ([Scale a model to zero, and get it back](../../guides/scale-to-zero/)):
  1. [Check the wake signal](../../guides/scale-to-zero/README.md#prerequisites) before anything parks.
  2. [Allow parking in the policy, and let every variant reach zero](../../guides/scale-to-zero/README.md#installation-instructions): `scaleToZero.enabled` **and** `minReplicaCount: 0`, both halves.
- **Check it works:** no `wva_model_scaling_blocked` reason, the model parks after `retentionPeriod`, and `hack/verify_scale_from_zero.sh` wakes it end to end ([Verification](../../guides/scale-to-zero/README.md#verification)).
- **Undo:** set `scaleToZero.enabled: false` and raise the variants' `minReplicaCount` back ([Cleanup](../../guides/scale-to-zero/README.md#cleanup)).

## What it is

An idle model releases its accelerators entirely, and comes back when a request
arrives. Two mechanisms that fail independently: **parking** is the scaling manager's — a model
that serves nothing for `retentionPeriod` goes to zero — and **waking** is the
EPP's flow-control queue plus KEDA, which brings it off zero when a request
queues for a model with no endpoints.

**Use it when** a model has real idle windows — nights, weekends, a dev
namespace — and the accelerators it holds could serve something else.

**Do not use it when** the model must answer the first request of a burst
inside its cold-start time. Waking is not instant: the first requester waits
for a replica to load. If that is the problem you have, take
[the warm pool path](../warm-pool-bridge/) instead, or both.

## The one risk worth stating first

Parking is the easy half and the dangerous one. **A cluster can park a model
perfectly and be unable to wake it**, and nothing about the park looks wrong.
Waking needs EPP flow control enabled, and EPP reads its config once at
startup — so editing the ConfigMap does not reach a pod already running.

Check the wake path before you let anything park. The guide's first
prerequisite block does exactly this, and asks the EPP process what it
*parsed* rather than what the ConfigMap says.

## What it needs

- EPP with the `flowControl` feature gate actually parsed by the running pod.
- Scale-to-zero enabled on the model **and** `minReplicaCount: 0` on every
  variant's ScaledObject. Set one without the other and you get a valid
  configuration that quietly does not do what it looks like — the scaling manager reports which
  half is missing.
- A `retentionPeriod` longer than one optimization cycle. A parking pass faster
  than `retentionPeriod` is stale state, not success.

Do not judge this from the derived HPA: KEDA always leaves the HPA's
`minReplicas` at 1. Only the ScaledObject's `minReplicaCount` decides.

## Setting it up

[Scale a model to zero, and get it back](../../guides/scale-to-zero/) — the
prerequisite check, both settings, and the round trip.

## Verifying it

Verify the round trip, never just the park:

```bash
# parked
kubectl get deploy -n <ns> <model>            # 0 replicas
# woken — send one request, then watch it come back
kubectl get scaledobject -n <ns> -w
```

`wva_model_scaling_blocked` carries a `reason` label when something prevents a
scale action; it is the first place to look when a wake does not happen.

## What it costs

Nothing while running, and the accelerators are genuinely released. The cost is
latency on the first request after an idle window, and the operational risk
above.

## How it is tested

- End-to-end: `test/e2e/scale_to_zero_test.go`,
  `scale_to_zero_roundtrip_test.go`, `scale_to_zero_sglang_test.go`,
  `scale_from_zero_test.go`, `scale_from_zero_capacity_test.go`.
- **These specs skip unless `SCALE_TO_ZERO_ENABLED=true`** — it defaults to
  false in `test/e2e/config.go`, and a run without it is green while covering
  none of this. A skipped spec reports as a pass; check the spec count.

## Tuning it

`retentionPeriod` and the scale-to-zero settings are in the
[scaling policy reference](../../reference/scaling-policy.md).
