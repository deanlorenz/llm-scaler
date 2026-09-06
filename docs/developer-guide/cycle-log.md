# WVA Cycle Log

The WVA saturation engine emits two structured INFO log lines per reconcile
cycle per model. These lines are the primary observability instrument for
understanding what the analyzer and optimizer computed without enabling
verbose debug logging.

---

## Log lines

### `analyzer-result`

Emitted once per analyzer that ran for a model, immediately after the
universal threshold post-step has been applied — in that analyzer's own units
(tokens for saturation). **A second `analyzer-result` line is emitted for the
composite entry** right after it has been converted to unit-less coverage
fractions for the optimizer (see
[scaling-policy-config.md](scaling-policy-config.md) for the coverage-unit
conversion) — this second line, and only this one, reflects what the
optimizer actually receives. Both lines share the same schema; fields that are
only meaningful post-conversion (`satDemand`, `remaining`, `spare`,
`roleCapacities`) simply read as zero/empty on the first line.

```json
{
  "level": "info",
  "msg": "analyzer-result",
  "modelID": "my-model",
  "namespace": "default",
  "analyzer": "saturation",
  "supply": 658534,
  "demand": 1041047,
  "satDemand": 0,
  "util": 1.58,
  "rc": 0,
  "sc": 50000,
  "remaining": 0,
  "spare": 50000,
  "scaleUpThreshold": 1.1,
  "scaleDownBoundary": 0.7,
  "variants": [
    {"name": "primary", "prc": 1152000, "role": "both", "reason": "P3-k2"},
    {"name": "v2",      "prc":  403391, "role": "both", "reason": "P1-obs"}
  ],
  "roleCapacities": []
}
```

| Field | Description |
|---|---|
| `modelID` | WVA model ID (unique within a namespace) |
| `namespace` | Kubernetes namespace |
| `analyzer` | Analyzer name, e.g. `"saturation"`, `"throughput"` |
| `supply` | Total token supply across ready replicas (readyCount × perReplicaCapacity). Pre-conversion units; not itself normalized |
| `demand` | Total token demand on the first (pre-conversion) line. Not purely observed: for saturation V2 it is the sum of three terms — resident KV tokens, a role-aware projection of requests waiting in each replica's local engine queue, and a model-level, prefix-cache-discounted projection of requests still queued upstream in llm-d flow control (`SchedulerQueue`, not attributed to any variant). See [scaling-policy-config.md](scaling-policy-config.md). On the second (composite) line this is always `1.0` — demand is normalized to "100%" so the optimizer reasons in coverage fractions |
| `satDemand` | The composite line's raw pre-conversion `demand`, captured before normalization overwrites it — the rescale weight (`rescaleInputsForGroup`) uses this so the priority-weighted water-fill stays proportional to real demand. Always `0` on the first (pre-conversion) line |
| `util` | `demand / supply`; > 1.0 means the model is over capacity. On the composite line this is recomputed from the normalized `demand`/`supply`, algebraically identical to the pre-conversion value |
| `rc` | Required capacity signal (post-threshold): > 0 triggers scale-up. Token units on the first line, a coverage fraction on the composite line |
| `sc` | Spare capacity signal (post-threshold): > 0 permits scale-down. Token units on the first line, a coverage fraction on the composite line |
| `remaining` | The optimizer's working copy of `rc`, seeded from it and mutated as allocation proceeds within a cycle. Same units as `rc` on each line |
| `spare` | The optimizer's working copy of `sc`. Same units as `sc` on each line |
| `scaleUpThreshold` | Scale-up threshold resolved for this analyzer (from config) |
| `scaleDownBoundary` | Scale-down boundary resolved for this analyzer (from config) |
| `variants[].name` | Variant name |
| `variants[].prc` | Per-replica capacity. Analyzer units (tokens for saturation) on the first line; a unit-less coverage fraction in `(0, 1]` on the composite line |
| `variants[].role` | Resolved P/D role: `prefill`, `decode`, or `both`. Renders as `both` both when the scale target has no `llm-d.ai/role` label and when the analyzer does not populate the role at all. Saturation V2 charges waiting requests by this role, so it is needed to interpret `demand` |
| `variants[].reason` | How the variant's capacity was computed (see below) |
| `roleCapacities[].role` | Role this entry covers (disaggregated models only; empty for non-disaggregated) |
| `roleCapacities[].rc` / `.sc` | Per-role required/spare capacity — same units as the model-level `rc`/`sc` on each line |
| `roleCapacities[].roleSpare` | The optimizer's per-role working copy of `sc`, once seeded (omitted before it is) |

If an analyzer does not compute per-variant capacity, `variants` is an empty
array. Multiple `analyzer-result` lines appear per cycle: one per enabled
analyzer (pre-conversion, in that analyzer's own units), plus one more for the
composite entry the optimizer actually consumes (post-conversion, coverage
units) — all share the same `modelID`/`namespace` and their own `analyzer`
field.

### `scaling-decision`

Emitted once per model after the optimizer has produced its final per-variant
replica targets.

```json
{
  "level": "info",
  "msg": "scaling-decision",
  "modelID": "my-model",
  "namespace": "default",
  "decisions": [
    {"name": "primary", "curr": 1, "tgt": 2, "action": "ScaleUp"},
    {"name": "v2",      "curr": 1, "tgt": 1, "action": "NoChange"}
  ]
}
```

| Field | Description |
|---|---|
| `modelID` | WVA model ID |
| `namespace` | Kubernetes namespace |
| `decisions[].name` | Variant name |
| `decisions[].curr` | Current replica count at the time of this cycle |
| `decisions[].tgt` | Target replica count chosen by the optimizer |
| `decisions[].action` | `ScaleUp`, `ScaleDown`, or `NoChange` |

---

## Reason values (`reason` field)

The `reason` field in `analyzer-result` variants is set by each analyzer to
describe how it computed the variant's per-replica capacity. It is free text;
the saturation V2 analyzer sets one of these values:

| Reason | Meaning |
|---|---|
| `P0-store` | capacity came from the **capacity store** (no live replicas) |
| `P1-obs` | k2 came from **observed** tokens-in-use (queue was saturated) |
| `P2-hist` | k2 came from the **historical** rolling average |
| `P3-k2` | k2 was **derived** from deployment parameters (vLLM model args) |
| `P4-k1` | k2 was unavailable; **fell back** to k1 (memory-bound capacity) |
| `no-data` | no ready replicas, no stored record, no compatible variant — capacity is 0 this cycle (normal for newly deployed variants) |
| `error` | K2 priority not in known set — indicates an unlabelled code path; should not occur in normal operation |

The `P1-obs`–`P4-k1` values reflect the representative replica for the variant
— specifically the replica whose effective capacity equals the lower median
across all ready replicas (the same replica that determined `prc`). A `P1-obs`
reason means live inference data is available and the capacity estimate is
high-confidence; a `P4-k1` reason means no compute-bound signal was available
for any replica and the estimate is conservative.

The throughput analyzer sets one of these values:

| Reason | Meaning |
|---|---|
| `T1-ols` | capacity from Tier-1 OLS fit (observation window ready) |
| `T2-pinned` | capacity from Tier-2 constrained OLS with a prior fitted B |
| `T2-default` | capacity from Tier-2 constrained OLS with the default baseline B (cold start) |
| `T2-failed` | both tiers failed — all replicas idle or no usable ITL signal; variant skipped this cycle |

Other analyzers may set their own reason values or leave `reason` empty.

---

## Grep patterns

```bash
# All analyzer results for a specific model
kubectl logs <pod> | grep '"msg":"analyzer-result"' | grep '"modelID":"my-model"'

# Saturation analyzer only
kubectl logs <pod> | grep '"msg":"analyzer-result"' | grep '"analyzer":"saturation"'

# Scaling decisions only (scale-up events)
kubectl logs <pod> | grep '"msg":"scaling-decision"' | grep '"action":"ScaleUp"'

# Full cycle for one model (both lines)
kubectl logs <pod> | grep -E '"analyzer-result"|"scaling-decision"' | grep '"modelID":"my-model"'
```

---

## Ordering and timing

Within a single reconcile cycle for one model:

1. One `analyzer-result` line per enabled analyzer (saturation first, then
   any registered non-saturation analyzers in registration order).
2. One `scaling-decision` line after the optimizer has processed all models
   in the cycle.

The two line types are not atomically adjacent in the log — other models'
`analyzer-result` lines may appear between a model's last `analyzer-result`
and its `scaling-decision`. Filter by `modelID` and `namespace` when
correlating them.

---

## Enabling the log

These lines are emitted at INFO level and appear in the default controller
log output. No feature flag or configuration change is required.
