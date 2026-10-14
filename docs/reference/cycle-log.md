# llm-scaling-manager Cycle Log

The scaling manager saturation engine emits two structured INFO log lines per reconcile
cycle per model, and a third, `throughput-demand-floor`, on a cycle where the
throughput floor changes a role's demand. These lines are the primary observability instrument for
understanding what the analyzer and optimizer computed without enabling
verbose debug logging.

The controller logs in zap's development **console** format, not JSON: a
timestamp, the level, the caller, the message, then the fields as one JSON
object. So a line looks like this (wrapped here; it is one line in the log):

```text
2026-10-14T09:12:03.512Z  INFO  steadystate/engine_v2.go:1246  analyzer-result
  {"modelID": "my-model", "namespace": "default", "analyzer": "saturation", ...}
```

The top-level fields are written `"key": value`, with a space after the colon.
Nested values (`variants`, `decisions`) are compact JSON with no spaces. The
greps below rely on both.

---

## Log lines

### `analyzer-result`

Emitted once per analyzer that ran for a model, immediately after the
universal threshold post-step has been applied. The values reflect what the
optimizer actually receives.

```text
2026-10-14T09:12:03.512Z  INFO  analyzer-result  {"modelID": "my-model", "namespace": "default",
  "analyzer": "saturation", "supply": 2362173, "demand": 2240000, "util": 0.95, "rc": 273121,
  "sc": 0, "scaleUpThreshold": 0.85, "scaleDownBoundary": 0.7,
  "variants": [{"name":"primary","prc":1152000,"role":"both","reason":"P3-k2","observed":1},
               {"name":"v2","prc":403391,"role":"both","reason":"P1-obs","observed":3}]}
```

A P/D model also carries `roleRC` and `roleSC`, the per-role required and spare
capacity the optimizer branches on.

| Field | Description |
|---|---|
| `modelID` | the scaling manager model ID (unique within a namespace) |
| `namespace` | Kubernetes namespace |
| `analyzer` | Analyzer name, e.g. `"saturation"`, `"throughput"` |
| `supply` | Total token supply across ready replicas (readyCount × perReplicaCapacity) |
| `demand` | Total token demand. Not purely observed: for saturation V2 it is the sum of three terms — resident KV tokens, a role-aware projection of requests waiting in each replica's local engine queue, and a model-level, prefix-cache-discounted projection of requests still queued upstream in llm-d flow control (`SchedulerQueue`, not attributed to any variant). See [scaling-policy-config.md](scaling-policy.md) |
| `util` | `demand / supply`; > 1.0 means the model is over capacity |
| `rc` | Required capacity signal (post-threshold): > 0 triggers scale-up |
| `sc` | Spare capacity signal (post-threshold): > 0 permits scale-down |
| `scaleUpThreshold` | Scale-up threshold resolved for this analyzer (from config) |
| `scaleDownBoundary` | Scale-down boundary resolved for this analyzer (from config) |
| `variants[].name` | Variant name |
| `variants[].prc` | Per-replica capacity in analyzer units (tokens for saturation) |
| `variants[].role` | Resolved P/D role: `prefill`, `decode`, or `both`. Renders as `both` both when the scale target has no `llm-d.ai/role` label and when the analyzer does not populate the role at all. Saturation V2 charges waiting requests by this role, so it is needed to interpret `demand` |
| `variants[].reason` | How the variant's capacity was computed (see below) |
| `variants[].observed` | Replicas that reported metrics this cycle, before any cap; compare with the target's ready replicas |
| `roleRC`, `roleSC` | Per-role `rc` and `sc`; present only for a P/D model. A role over its own threshold keeps the model scaling up even when the model-level `rc` is 0 |

If an analyzer does not compute per-variant capacity, `variants` is an empty
array. Multiple `analyzer-result` lines appear when more than one analyzer is
enabled; each has the same `modelID`/`namespace` and its own `analyzer` field.

### `scaling-decision`

Emitted once per model after the optimizer has produced its final per-variant
replica targets.

```text
2026-10-14T09:12:03.540Z  INFO  scaling-decision  {"modelID": "my-model", "namespace": "default",
  "decisions": [{"name":"primary","curr":1,"tgt":2,"action":"scale-up"},
                {"name":"v2","curr":3,"tgt":3,"action":"no-change","atMax":true}]}
```

| Field | Description |
|---|---|
| `modelID` | the scaling manager model ID |
| `namespace` | Kubernetes namespace |
| `decisions[].name` | Variant name |
| `decisions[].curr` | Current replica count at the time of this cycle |
| `decisions[].tgt` | Target replica count chosen by the optimizer |
| `decisions[].action` | `scale-up`, `scale-down`, or `no-change` |
| `decisions[].atMax` | `true` when the target sits on the variant's `maxReplicaCount`; absent otherwise. Same as `wva_variant_at_max_replicas` |

### `throughput-demand-floor`

Emitted per role during the analysis, so before that model's
`analyzer-result`, and only on a cycle where the
throughput floor changed that role's demand: the arrival rate divided by the
throughput one replica sustains (`saturatedThroughput`) asked for more than
occupancy measured, or a standing queue was priced as a backlog. Most cycles
of a fleet that is coping have none.

| Field | Description |
|---|---|
| `role` | `prefill`, `decode` or `both` |
| `demandBeforeFloor`, `flooredTo` | the role's demand before and after the floor, in the same unit as `analyzer-result`'s `demand` |
| `arrivalRate`, `saturatedThroughput`, `replicasImplied` | the requests per second arriving, what one replica completes at saturation, and the replicas their ratio asks for |
| `backlogRequests`, `projectedBacklog`, `drainSeconds` | the queue measured, the queue the floor priced over the next replica's start time, and how fast it is to be drained |
| `heldAtFleet` | `true` when the floor was capped at what the current fleet supplies at the scale-up threshold: it may hold the fleet up, but not order more replicas |
| `heldWhy` | why it was capped; see below. Empty when `heldAtFleet` is `false` |
| `mayOrder`, `borrowedOnly` | whether the role's rate is one the floor may order on, and whether every rate it has is borrowed rather than measured by the role's own replicas |

**`heldWhy` values.** Each says which reading the floor would not order on:

| Value | Meaning | What changes it |
| --- | --- | --- |
| `single-sample` | the role's own throughput window holds one reading | more saturated readings |
| `borrowed` | the only rate is borrowed from a neighbouring output-length bucket | a reading in the bucket the traffic is in now |
| `borrowed-line` | the rate is derived from an inter-token-latency line another variant fitted (shared because `enableLearnedStateReuse` is on) | the variant fitting its own line |
| `shape-change` | the traffic's shape changed and the rate was measured on the old one | the new shape settling |
| `borrowed+shape-change`, `borrowed-line+shape-change` | both at once; the borrow is named first because it is the one that does not go away on its own | as above, both |

A borrowed figure never orders replicas here: it can hold demand up while the
role measures itself, never raise it above what the fleet already supplies.

---

## Reason values (`reason` field)

The `reason` field in `analyzer-result` variants is set by each analyzer to
describe how it computed the variant's per-replica capacity. It is free text;
the saturation V2 analyzer sets one of these values:

| Reason | Meaning |
|---|---|
| `P0-store` | capacity came from the **capacity store** (no live replicas) |
| `P1-obs` | k2 came from **observed** tokens-in-use (queue was saturated), blended into the same rolling average `P2-hist` reads -- a single noisy cycle gets 1/N weight rather than taking over outright |
| `P2-hist` | k2 came from the **historical** rolling average. The window is kept for 7 days from when it was last *read* — not written — so a bucket whose capacity is priced from it every cycle keeps its measurement however long the fleet goes without saturating. However old the figure is, while it is retained it answers: it is the most conservative number available for the bucket, since `min(k1, k2)` means every alternative is higher. Past 7 days with nothing reading it the bucket is forgotten and the next decision falls to `P3-k2` or `P4-k1` |
| `P2-borrow` | The variant has no k2 history of its own for this traffic shape, so k2 came from a **sibling variant**. The sibling must share the namespace, model, accelerator, GPU count, role, output-length bucket and queue threshold, and have capacity-compatible engine parameters (the same `--max-num-seqs`, batch-token budget, KV settings and quantization). The borrowed figure is used only when it is **lower** than this variant's own capacity without it (`P3-k2` or `P4-k1`, capped at k1), so it never makes the variant order fewer replicas. A k2 that is too low never corrects itself: lightly loaded replicas never saturate, and saturation is the only time k2 is measured. So the borrow holds for 30 minutes, then relaxes linearly back to the variant's own figure over 6 hours. The clock is per output-length bucket: a donor that disappears and comes back does not restart it, and a bucket the variant has never measured gets its own hold. A week after the relaxation ends, a variant that has still never measured that bucket borrows again (a deliberate re-probe). A controller restart clears the clock along with the donors' windows. The `k2-decision` log line names the `donor`, its `donorK2`, this variant's `ownCapacity` (its own k2 or k1, whichever is lower), and how far it has `relaxed`. The variant's first own saturated reading replaces it. |
| `P3-k2` | k2 was **derived** from deployment parameters (vLLM model args). Never fires for a **prefill** variant: the formula assumes a real per-request output length, which prefill's own avgOutput (~0-1, it hands off before generating anything) collapses to just the batch-token budget echoed back -- not a derived signal |
| `P4-k1` | k2 was unavailable; **fell back** to k1 (memory-bound capacity). For prefill this is the common case, not a degraded one -- see `P3-k2` above |

Two further values appear in the `k2-decision` line's `priority` field but never
as a variant's `reason`, because no capacity comes from either:

| value | meaning |
| --- | --- |
| `P1-obs-invalid` | an observation was discarded for exceeding the KV cache's **physical** ceiling, i.e. a scrape artifact. Note the bound is the ceiling, not k1: k1 is the ceiling times `kvCacheThreshold` (0.80 by default), so occupancy between the two is legitimate and is kept. The analyzer falls through to the next priority |
| `P1-obs-downstream` | a **prefill** replica's saturated queue was left unrecorded because a **decode** replica was full and queued -- resident KV at its k1, queue at the threshold -- in the same cycle or within the last minute (the collector's row window). A prefill request is done only when decode admits it and pulls its KV, so with decode saturated what prefill shows -- held KV, a queue, a completion rate -- is metered by decode: decode's saturation seen from upstream, not a reading of prefill. Neither k2 nor the saturated throughput (`throughput-demand-floor`) is recorded from it; the analyzer falls through to the next priority, and prefill's demand for the cycle is held where it neither orders nor releases (`prefill-demand-held`). A decode queue that is only KV transfers in flight (KV under k1) does not count. A prefill bottleneck reduces decode's arrivals, so both saturate at once only when decode is short at prefill's completion rate; prefill's reading then waits for decode to recover |
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
LOGS="kubectl logs -n $NS -l app.kubernetes.io/name=workload-variant-autoscaler --tail=-1"

# All analyzer results for a specific model
$LOGS | grep analyzer-result | grep '"modelID": "my-model"'

# Saturation analyzer only
$LOGS | grep analyzer-result | grep '"analyzer": "saturation"'

# Scale-up decisions only
$LOGS | grep scaling-decision | grep '"action":"scale-up"'

# Full cycle for one model (both lines)
$LOGS | grep -E 'analyzer-result|scaling-decision' | grep '"modelID": "my-model"'
```

`modelID` is unique only within a namespace; add `| grep '"namespace": "<ns>"'`
when two namespaces serve the same model.

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
