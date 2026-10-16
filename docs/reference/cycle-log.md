# llm-scaling-manager Cycle Log

Each reconcile cycle, the saturation engine writes two INFO lines per model,
and a third, `throughput-demand-floor`, on a cycle where the throughput floor
changes a role's demand. They show what the analyzer and optimizer computed,
with no debug logging needed.

The log uses zap's development **console** format, not JSON: timestamp, level,
caller, message, then the fields as one JSON object. A line looks like this
(wrapped here; it is one line in the log):

```text
2026-10-14T09:12:03.512Z  INFO  steadystate/engine_v2.go:1246  analyzer-result
  {"modelID": "my-model", "namespace": "default", "analyzer": "saturation", ...}
```

Top-level fields are written `"key": value`, with a space after the colon.
Nested values (`variants`, `decisions`) are compact JSON with no spaces. The
greps below rely on both.

---

## Log lines

### `analyzer-result`

One line per analyzer that ran for a model, written right after the threshold
post-step. The values are what the optimizer receives.

```text
2026-10-14T09:12:03.512Z  INFO  analyzer-result  {"modelID": "my-model", "namespace": "default",
  "analyzer": "saturation", "supply": 2362173, "demand": 2240000, "util": 0.95, "rc": 273121,
  "sc": 0, "scaleUpThreshold": 0.85, "scaleDownBoundary": 0.7,
  "variants": [{"name":"primary","prc":1152000,"role":"both","reason":"P3-k2","observed":1},
               {"name":"v2","prc":403391,"role":"both","reason":"P1-obs","observed":3}]}
```

A P/D model also carries `roleRC` and `roleSC`: the per-role required and spare
capacity the optimizer branches on.

| Field | Description |
|---|---|
| `modelID` | the scaling manager model ID (unique within a namespace) |
| `namespace` | Kubernetes namespace |
| `analyzer` | Analyzer name, e.g. `"saturation"`, `"throughput"` |
| `supply` | Total token supply across ready replicas (readyCount × perReplicaCapacity) |
| `demand` | Total token demand. For saturation V2 it is the sum of three terms: resident KV tokens; a role-aware projection of requests waiting in each replica's engine queue; and a model-level, prefix-cache-discounted projection of requests still queued in llm-d flow control (`SchedulerQueue`, not attributed to any variant). See [scaling-policy-config.md](scaling-policy.md) |
| `util` | `demand / supply`; > 1.0 means the model is over capacity |
| `rc` | Required capacity (post-threshold): > 0 triggers scale-up |
| `sc` | Spare capacity (post-threshold): > 0 permits scale-down |
| `scaleUpThreshold` | Scale-up threshold for this analyzer (from config) |
| `scaleDownBoundary` | Scale-down boundary for this analyzer (from config) |
| `variants[].name` | Variant name |
| `variants[].prc` | Per-replica capacity in analyzer units (tokens for saturation) |
| `variants[].role` | P/D role: `prefill`, `decode`, or `both`. It reads `both` when the scale target has no `llm-d.ai/role` label, and when the analyzer does not set a role. Saturation V2 charges waiting requests by this role, so you need it to read `demand` |
| `variants[].reason` | How the variant's capacity was computed (see below) |
| `variants[].observed` | Replicas that reported metrics this cycle, before any cap; compare with the target's ready replicas |
| `roleRC`, `roleSC` | Per-role `rc` and `sc`; P/D models only. A role over its own threshold keeps the model scaling up even when the model-level `rc` is 0 |

An analyzer that computes no per-variant capacity writes an empty `variants`
array. With several analyzers enabled there are several `analyzer-result` lines,
each with the same `modelID`/`namespace` and its own `analyzer`.

### `scaling-decision`

One line per model, after the optimizer chose each variant's replica target.

```text
2026-10-14T09:12:03.540Z  INFO  scaling-decision  {"modelID": "my-model", "namespace": "default",
  "decisions": [{"name":"primary","curr":1,"tgt":2,"action":"scale-up"},
                {"name":"v2","curr":3,"tgt":3,"action":"no-change","atMax":true},
                {"name":"v3","curr":1,"tgt":1,"action":"no-change","limitedBy":"team-quota"}]}
```

| Field | Description |
|---|---|
| `modelID` | the scaling manager model ID |
| `namespace` | Kubernetes namespace |
| `decisions[].name` | Variant name |
| `decisions[].curr` | Replica count at this cycle |
| `decisions[].tgt` | Target replica count chosen by the optimizer |
| `decisions[].action` | `scale-up`, `scale-down`, or `no-change` |
| `decisions[].atMax` | `true` when the target is the variant's `maxReplicaCount`; absent otherwise. Same as `wva_variant_at_max_replicas` |
| `decisions[].limitedBy` | what held the variant's scale-up back this cycle: a limiter entry's name, `gpu-limiter` for the physical limiter, `node-fit`, `pd-pair-borrows-whole` or `lender-cannot-reclaim`. Absent when nothing did. The same name as the `ResourceConstrained` Event and the `limiter_name` label of `wva_decisions_limited_total` |
| `decisions[].limitedByAlso` | the other limiters that held it back exactly as tightly, in the `limiters:` order; absent when one did alone. The Event names them too; the metric label carries `limitedBy` alone |

### `throughput-demand-floor`

Emitted per role during the analysis, so before that model's
`analyzer-result`, and only on a cycle where the
throughput floor changed that role's demand. The floor takes the larger of
the resident KV demand (occupancy with the queues' residency charge removed,
since the queue is priced as a backlog instead) and what the arrival rate and
the queue ask for at the throughput one replica sustains
(`saturatedThroughput`). So the line also appears when that LOWERS demand: a
queue priced as a backlog can be worth less than its residency charge. Most
cycles of a fleet that is coping have none.

| Field | Description |
|---|---|
| `role` | `prefill`, `decode` or `both` |
| `demandBeforeFloor`, `flooredTo` | the role's demand before and after the floor, in the same unit as `analyzer-result`'s `demand` |
| `residentDemand` | `demandBeforeFloor` with the queues' residency charge removed: what the replicas hold now |
| `perReplicaCapacity` | the per-replica capacity the floor converts replicas into demand with |
| `arrivalRate`, `saturatedThroughput`, `replicasImplied` | the requests per second arriving, what one replica completes at saturation, and the replicas their ratio asks for |
| `backlogRequests`, `projectedBacklog`, `drainSeconds` | the queue measured, the queue the floor priced over the next replica's start time, and how fast it is to be drained |
| `heldAtFleet` | `true` when the floor was capped at what the current fleet supplies at the scale-up threshold: it may hold the fleet up, but not order more replicas |
| `heldWhy` | why it was capped; see below. Empty when `heldAtFleet` is `false` |
| `mayOrder`, `borrowedOnly` | whether the role's rate is one the floor may order on, and whether every rate it has is borrowed rather than measured by the role's own replicas |
| `orderedBehindQueue`, `queueJustifiedReplicas` | whether a held floor still ordered behind a standing queue, and how many replicas the queue was worth when it did |

On a disaggregated fleet the per-role lines do not add up to the model's
total, so a second line, `throughput-demand-floor-total`, gives it:
`modelTotalBefore` and `modelTotalAfter`, and the `roles` the floor moved.

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

Each analyzer sets `reason` to say how it computed a variant's per-replica
capacity. It is free text. The saturation V2 analyzer uses:

| Reason | Meaning |
|---|---|
| `P0-store` | from the **capacity store** (no live replicas) |
| `P1-obs` | k2 from **observed** tokens-in-use (queue saturated), blended into the same rolling average `P2-hist` reads, so one noisy cycle gets 1/N weight |
| `P2-hist` | k2 from the **historical** rolling average. The window is kept for 7 days from when it was last *read*, not written, so a bucket priced from it every cycle keeps its value however long the fleet goes without saturating. While kept it is used: with `min(k1, k2)`, it is the most conservative number available. After 7 days unread the bucket is forgotten and the next decision falls to `P3-k2` or `P4-k1` |
| `P2-borrow` | No k2 history of its own for this traffic shape, so k2 from a **sibling variant**: same namespace, model, accelerator, GPU count, role, output-length bucket and queue threshold, and capacity-compatible engine parameters (same `--max-num-seqs`, batch-token budget, KV settings and quantization), and a measured KV cache total within 3% of this variant's when both have one. Used only when **lower** than the variant's own capacity without it (`P3-k2` or `P4-k1`, capped at k1), so it never orders fewer replicas. A k2 that is too low never corrects itself (light load never saturates, and only saturation measures k2), so the borrow holds 30 minutes, then relaxes linearly to the variant's own figure over 6 hours. The clock is per output-length bucket, and counts only cycles where a Ready replica of the variant's own serves that bucket with requests on it: it stops while the variant is parked, and a warm-pool bridge or a Pod that is not Ready reads the borrow without starting or moving the clock. A donor that leaves and returns does not restart it. Once relaxed, the borrow stays spent: it starts again only when a figure on offer is more than 20% below the one it last ran on, or the variant's engine arguments have changed (a new image alone is not a change). A donor that measures higher does not restart it. `disableK2Borrow` turns it off for the model and pauses its clock. The clock lasts as long as the variant; a controller restart keeps it only through the learned-state snapshot (`LEARNED_STATE_SNAPSHOT`). The `k2-decision` line names the `donor`, its `donorK2`, this variant's `ownCapacity` (its own k2 or k1, whichever is lower), and how far it has `relaxed`. The variant's first own saturated reading replaces it |
| `P2-prior` | k2 from the variant's **own** earlier measurement, made under engine arguments it no longer runs: its flags changed in a rollout, while the controller ran or across a restart. Not a borrow, and `disableK2Borrow` does not turn it off. The same two rules as `P2-borrow`: used only when **lower** than the variant's own capacity under the new arguments, and it relaxes to that capacity (30 minutes, then 6 hours, of serving). A new image alone does not make one. A later change of arguments restarts it only if the variant measured a newer figure first; one that measured nothing leaves its clock running. The `k2-decision` line names the variant itself as `donor`. The variant's first saturated reading under the new arguments replaces it |
| `P3-k2` | k2 **derived** from deployment parameters (vLLM model args). Never for a **prefill** variant: the formula needs a real output length, and prefill's avgOutput (~0-1, it hands off before generating) reduces it to the batch-token budget |
| `P4-k1` | k2 unavailable; **fell back** to k1 (memory-bound capacity). Normal for prefill, not degraded; see `P3-k2` |

These appear in the `k2-decision` line's `priority` field but never as a
variant's `reason`, because no capacity comes from them:

| value | meaning |
| --- | --- |
| `P1-obs-invalid` | an observation above the KV cache's **physical** ceiling was discarded as a scrape artifact. The bound is the ceiling, not k1: k1 is the ceiling times `kvCacheThreshold` (0.80 by default), so occupancy between the two is kept. The analyzer falls through to the next priority |
| `P1-obs-downstream` | a **prefill** replica's saturated queue was not recorded because a **decode** replica was full and queued (resident KV at its k1, queue at the threshold) in the same cycle or the last minute (the collector's row window). Prefill finishes a request only when decode pulls its KV, so with decode saturated, prefill's KV, queue and completion rate measure decode, not prefill. Neither k2 nor the saturated throughput (`throughput-demand-floor`) is recorded; the analyzer falls through, and prefill's demand is held for the cycle, neither ordering nor releasing (`prefill-demand-held`). A decode queue of only KV transfers in flight (KV under k1) does not count. Both roles saturate at once only when decode is short at prefill's completion rate; prefill's reading then waits for decode to recover |
| `P1-obs-rollout` | a saturated queue was not recorded because the variant is mid-rollout: its scale target reports a rollout in progress -- any rollout, image-only included; for a LeaderWorkerSet its `UpdateInProgress` condition, so a scale-up is not one -- or its replicas report more than one pod template revision. Old pods still report while the record already names the new configuration, so the reading may be an old pod's. Nothing is written to a window until the rollout completes. The variant is priced from what it already holds, its own window, or after a change of arguments its previous figure (`P2-prior`); where a saturated reading is lower than that, a second `P1-obs-unadmitted` line (`chainK2`, `chainPriority`, reason ending `mid-rollout`) says the row is priced at the reading for this cycle only, so a bucket with no figure is not priced at k1 while it shows full. Nothing of that reading is kept. The lower bound waits for the rollout: its hold and relaxation run only on cycles that may record, so a rollout that stalls -- a surge pod pending, a LeaderWorkerSet held part way by a rolling-update partition -- does not relax the variant toward k1 while nothing can be measured, and a previous figure keeps its whole hold for when it ends. A LeaderWorkerSet held at a partition reads as mid-rollout for as long as it is held, so nothing is learned for the variant until it is let through. An undo, or a controller restart, in mid-rollout is a rollout like any other |
| `P1-obs-unadmitted` | a saturated queue on a **warm-pool bridge** or a pod that is **not Ready** was not recorded as the variant's k2. A bridge runs the pool's engine configuration (a lower `--gpu-memory-utilization`, a different batch ceiling); a pod that is not Ready gets no traffic, so its queue and resident KV are left over or frozen. These are the rows the saturated throughput already skips. A k2 recorded from them would be too low, and a k2 that is too low never corrects itself, because it orders replicas and nothing saturates again. The analyzer falls through to the next priority. For a **bridge** (and for a saturated Ready replica mid-rollout, see `P1-obs-rollout`) whose reading is lower than what that gives, a second `P1-obs-unadmitted` line (`chainK2`, `chainPriority`) says the row is priced at the reading for this cycle only, and the variant's reason can read `P1-obs-unadmitted`: a variant only a saturated bridge is carrying is priced at what the bridge holds, not at k1. A pod that is not Ready is priced at what the chain gives, mid-rollout too: its reading may be a pod draining or a hang with frozen gauges, and would be re-read every cycle. Nothing is recorded either way, and the borrow clock is not started. A pod that fails its readiness probe under load does not record k2 either; if `P1-obs-unadmitted` names a pod of the variant's own during a load peak, its readiness probe is timing out. When the controller cannot list a namespace's Pods it treats every Pod as Ready, so these readings are recorded; `wva_metrics_collection_errors_total{query_type="pod_list"}` counts those cycles |
| `no-data` | no ready replicas, no stored record, no compatible variant: capacity is 0 this cycle (normal for a new variant) |
| `error` | K2 priority not in the known set: an unlabelled code path. Should not occur |

`P1-obs` to `P4-k1` describe the variant's representative replica: the one whose
effective capacity is the lower median of all ready replicas (the one that set
`prc`). `P1-obs` means live data and a high-confidence estimate; `P4-k1` means
no replica had a compute-bound signal, and the estimate is conservative.

The throughput analyzer uses:

| Reason | Meaning |
|---|---|
| `T1-ols` | capacity from Tier-1 OLS fit (observation window ready) |
| `T2-pinned` | capacity from Tier-2 constrained OLS with a prior fitted B |
| `T2-default` | capacity from Tier-2 constrained OLS with the default baseline B (cold start) |
| `T2-failed` | both tiers failed (all replicas idle or no usable ITL signal); variant skipped this cycle |

Other analyzers may set their own values or leave `reason` empty.

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

`modelID` is unique only within a namespace. Add `| grep '"namespace": "<ns>"'`
when two namespaces serve the same model.

---

## Ordering and timing

In one reconcile cycle, for one model:

1. One `analyzer-result` line per enabled analyzer (saturation first, then the
   others in registration order).
2. One `scaling-decision` line, after the optimizer has processed every model in
   the cycle.

The two are not adjacent in the log: other models' lines can fall between them.
Filter by `modelID` and `namespace` to match them up.

---

## Enabling the log

Both lines are INFO and appear in the default controller log. Nothing to enable.
