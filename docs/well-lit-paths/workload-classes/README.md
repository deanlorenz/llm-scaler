# Give classes of workloads different scaling behaviour

An interactive assistant and an overnight batch job should not be scaled the
same way, and neither should be configured one model at a time. A **tier** is a
named, reusable policy entry — `interactive`, `standard`, `batch` — that a
variant selects by name from its own ScaledObject. A tier carries no model
identity, which is the entire point: one tier serves many models, and retuning
the tier retunes all of them at once.

**Use it when** your models fall into a small number of classes with different
urgency, and you would rather change a class than edit every model in it.

**Do not use it when** one model genuinely needs its own numbers. That is a
per-model override, and it wins over any tier the variant names — reach for it
deliberately, not as the way you configure everything.

## What it needs

- Tier entries in the scaling-policy ConfigMap. An entry **is** a tier when its
  key is not `default` and its body sets no `model_id`:

```yaml
data:
  default: |
    scaleUpThreshold: 0.85
    scaleDownBoundary: 0.70
    priority: 1.0
    defaultPolicy: standard      # what a variant naming no tier gets
  standard: |                    # must exist: defaultPolicy names it
    priority: 1.0
  interactive: |
    scaleUpThreshold: 0.60
    scaleDownBoundary: 0.45
    priority: 2.0
  batch: |
    scaleUpThreshold: 0.95
    priority: 0.5
```

  The numbers are the shipped examples from the table under
  [Choosing a tier](#choosing-a-tier). **`defaultPolicy` must name a tier that
  exists.** Unlike a tier named on a trigger, a `defaultPolicy` naming no entry is
  never reported: every variant that names no tier silently scales under the
  bare `default` entry. Here `standard` repeats the default numbers, so it is
  only a place to retune the unnamed fleet later; delete it and `defaultPolicy`
  together, never one without the other.

- A `scalingPolicy` key in each variant's KEDA trigger metadata. The trigger is
  already the registration, so selecting a tier needs nothing watched, listed or
  matched:

```yaml
triggers:
  - type: external-push
    metadata:
      modelID: ibm/granite-13b
      scalingPolicy: interactive
```

## Choosing a tier

**No tier exists until you add it.** The shipped policy ConfigMap
(`config/base/manager/scaling-policy-configmap.yaml`) carries `interactive` and
`batch` only as commented-out examples, and `deploy/configmap-scaling-policy.yaml`
carries none. Until the entry is in the ConfigMap, `scalingPolicy: interactive`
on a trigger does nothing except log `Scaling policy not found` and scale under
the default entry. The shipped examples, against the default entry:

| | `default` (shipped) | `interactive` (example) | `batch` (example) |
| --- | --- | --- | --- |
| `scaleUpThreshold` | 0.85 | 0.60 | 0.95 |
| `scaleDownBoundary` | 0.70 | 0.45 | inherits 0.70 |
| `priority` | 1.0 | 2.0 | 0.5 |
| `scaleToZero` | as the entry sets it | `enabled: false` | `enabled: true, retentionPeriod: 2m` |

What the scale-up threshold buys:

- **Lower** (`interactive`): the model scales out at a lower utilization, so a
  new replica is starting while the existing ones still have room. You pay in
  idle KV cache: more replicas for the same traffic, all of them less full.
  Lower `scaleDownBoundary` with it, or the band between the two collapses and
  the model scales down as soon as it scaled up.
- **Higher** (`batch`): replicas are packed harder before another is ordered.
  Fewer GPUs for the same traffic, at the price of queueing and TTFT while a
  replica starts, since the scale-out begins later.

Utilization here is against `kvCacheThreshold`-scaled capacity, so the
effective scale-out point is `kvCacheThreshold × scaleUpThreshold` of physical
KV: 0.80 × 0.85 = 0.68 with the defaults, 0.48 for the `interactive` example.

To use one, copy it out of the comment into `data:` of the ConfigMap in the
controller's namespace, then set `scalingPolicy` on the trigger and check the
logs as in [Verifying it](#verifying-it).

## How a variant's policy is resolved

Most specific wins: **per-model override → named tier → `default.defaultPolicy`
→ `default`**. `defaultPolicy` is read from the `default` entry only, because a
fallback chosen by a tier would be that tier choosing for everyone but itself.

Two things that could be silent are not:

- **A tier that does not exist** falls back to `default` — the right outcome and
  the wrong silence, so it is logged against the variants that asked for it,
  listing the tiers that do exist. This covers a tier named in `scalingPolicy`
  only: a `defaultPolicy` naming a missing tier falls back the same way and is
  **not** logged (`internal/engines/steadystate/engine.go`,
  `resolveModelPolicy`). The `Effective scaling policy` line does not tell the
  two apart either: for a variant naming no tier it reads
  `scalingPolicy="(default entry)"` whether or not `defaultPolicy` resolved, so
  check the thresholds and `priority` it prints against the tier you meant.
- **Variants of one model naming different tiers** resolve to the
  lexicographically smallest, so the allocation comes out the same on every
  cycle rather than flipping with map order.

## Setting it up

The tier surface, the full field list and the per-model override that outranks
it are in [the scaling policy reference](../../reference/scaling-policy.md).
Tiers are edited in place: the policy is dynamic configuration, so the engine
picks up a changed ConfigMap without a restart.

## Verifying it

```bash
# which tier each model resolved to, and the thresholds it got (logged once per change)
kubectl logs -n <controller-namespace> deploy/<wva> | grep "Effective scaling policy"
# a tier that was named but does not exist
kubectl logs -n <controller-namespace> deploy/<wva> | grep "Scaling policy not found"
```

`Effective scaling policy` carries `scalingPolicy` (the tier name, or
`(default entry)`), `scaleUpThreshold`, `scaleDownBoundary`, `kvCacheThreshold`
and `priority`. The per-cycle `analyzer-result` line carries the
`scaleUpThreshold` and `scaleDownBoundary` in force but **no policy name**, so
use it to confirm the numbers, not the tier. A `Scaling policy not found` line
means the variant named a tier the ConfigMap does not declare and is scaling
under the default entry; its `knownPolicies` field lists the tiers that exist.

## What it costs

Nothing to run, but the tiers themselves trade money for latency: a lower
`scaleUpThreshold` holds more idle KV, a higher one queues more requests while a
replica starts. Under a binding GPU budget a low-`priority` tier also gets fewer
replicas than its demand asks for, and that shortfall is paid in queueing and
TTFT. The other cost is a naming decision: a tier is a promise that every
model wearing it wants the same behaviour, and the moment that stops being true
somebody adds a per-model override and the class stops meaning anything.

## How it is tested

End-to-end: `test/e2e/scaling_policy_tier_test.go` — a named tier taking a
workload where the default entry would not, fallback when the tier does not
exist, a per-model override beating the tier, and the tier's thresholds
resuming once that override is removed.

**The suite needs `USE_SIMULATOR=true`** and skips without it: it drives
`llm-d-inference-sim --fake-metrics`, which real vLLM rejects. A run without
the simulator is green while covering none of this.

## Tuning it

Priorities interact with the limiter. When there are not enough accelerators
for every model's demand, priority is the weight in the fair-share split
(`fairShareValue` in `internal/engines/allocation/`) — so a tier's `priority`
is what a class is worth under contention, not just how eagerly it scales when
there is room. See [bounding a fleet by real GPUs](../bound-by-gpus/).
