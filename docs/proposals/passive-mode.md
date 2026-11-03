# Passive mode

**Status:** design. Nothing here is built. Decisions still open are listed in
the last section.

## Summary

In passive mode the controller computes, logs and publishes everything it does
today, and moves no replica count. It is the utilization-share `shadow` switch,
applied to the whole controller and settable per model. One model can observe
while the others scale, and a passive controller can still let one model scale.

There are two shapes, chosen from the ScaledObject the controller already reads:

| shape | ScaledObject triggers | what our trigger answers | who moves the fleet |
| --- | --- | --- | --- |
| **hold** | ours alone | the held count, so the HPA holds | nobody |
| **observe** | ours beside others (prometheus, cpu, ...) | the lowest value, inactive | KEDA, from the other triggers |

A model with no trigger of ours is not seen at all today (see
[No trigger of ours](#no-trigger-of-ours)).

## Configuration

Two keys, resolved like `scaleToZero.enabled`, which already has a controller
flag as its last fallback and a field on every scaling-policy entry
([scaling-policy.md](../reference/scaling-policy.md#scaletozero)):

| key | where | type | absent means |
| --- | --- | --- | --- |
| `WVA_PASSIVE` | `wva-manager-config` (controller-wide; read at start) | `"true"`/`"false"` | `false` |
| `passive` | any scaling-policy entry: `default`, a tier, a per-model entry | `*bool` | inherit |
| `passiveReplicas` | the same entries | `*int`, >= 0 | hold the current count |

`passive` must be a `*bool`: `ScalingPolicy.Merge` overlays only non-zero
fields, so a plain `bool` could not say `passive: false` over a `default` that
says `true`.

**Precedence, most specific first** (the existing `ResolveScalingPolicyForTier`
order):

1. per-model entry (body names `model_id` and `namespace`);
2. the tier the variant names in `scalingPolicy`, else `default.defaultPolicy`;
3. the `default` entry — of the namespace-local `wva-scaling-policy-config` when
   the namespace has one (it replaces the global map whole), else the
   controller's;
4. `WVA_PASSIVE`.

When the variants of one model name different tiers, the model is passive if
**any** resolved tier says passive (the side that moves nothing). A per-model
entry is merged into every tier, so a per-model `passive: false` makes the whole
model active.

Passive controller, one model active:

```yaml
# wva-manager-config
data:
  WVA_PASSIVE: "true"
---
# wva-scaling-policy-config
data:
  meta_llama-70b.prod: |
    model_id: meta/llama-70b
    namespace: prod
    passive: false
```

Active controller, one namespace observing (namespace-local map), one model held
at 4:

```yaml
# wva-scaling-policy-config in namespace "lab"
data:
  default: |
    passive: true
    analyzers: [{name: saturation, score: 1.0}]
---
# controller's wva-scaling-policy-config
data:
  ibm_granite-13b.prod: |
    model_id: ibm/granite-13b
    namespace: prod
    passive: true
    passiveReplicas: 4
```

No annotation: per-model settings live in per-model entries today, and the
annotations the controller reads (`llm-d.ai/max-num-seqs`,
`llm-d.ai/forget-learned-state`) are facts about a pod, not policy.

## What KEDA is told

The trigger is `metricType: AverageValue` with target 1
(`metricTargetSize`, `internal/scaler/externalscaler.go`; the installer writes
AverageValue and warns on Value, `deploy/lib/scaledobject.sh`). `GetMetricSpec`
is unchanged.

### hold

Let `H` be the held count per variant (per ScaledObject).

- `GetMetrics` answers `H`, whatever the decision store holds.
- `IsActive` answers `H > 0`. `StreamIsActive` sends `H > 0` on open and on the
  keepalive, and ignores decision-store wake-ups for the target.

Why `H` holds, from the Kubernetes HPA source (v1.33.0,
`pkg/controller/podautoscaler`):

- The HPA's current count is `scale.Spec.Replicas` (`reconcileAutoscaler`).
- For an AverageValue external metric it calls
  `GetExternalPerPodMetricReplicas(statusReplicas, ...)`: the ratio is
  `H / (1 × status.replicas)`. Inside the 10% tolerance it proposes
  `status.replicas`; outside it proposes `ceil(H / 1) = H`.
- With `H == spec == status` the ratio is 1 and the proposal is `H`.
- `stabilizeRecommendationWithBehaviors` clamps the current count between the
  minimum of the scale-up window and the maximum of the scale-down window.
  Both contain the desired value, so a desired value equal to the current count
  always returns the current count, whatever the earlier recommendations were.
  Holding cannot be undone by history.

So the value is `H`, which equals target × current because the target is 1.
There is one existing quirk, the same in active mode: while `status.replicas`
lags `spec` within 10% (pods being created), the proposal is the status count.
That is a scale-down, and the 300 s scale-down window the installer ships keeps
it from acting unless the lag lasts longer than the window.

**What `H` is.**

- Default: `spec.replicas` of the scale target's scale subresource
  (`Handler.currentReplicas`, the same read the HPA uses), read once when the
  variant enters hold. It is not the HPA's `status.currentReplicas`, which is the
  pod count.
- With `passiveReplicas: n`: `n`.
- Either way `H` is clamped to `[minReplicaCount, maxReplicaCount]` of the
  ScaledObject (`registry.Target`, `enrich.go`), as KEDA and the HPA would clamp
  it. A `passiveReplicas` outside the bounds is clamped and logged once per
  change (`WARNING: passiveReplicas is outside the ScaledObject's bounds; holding
  at <clamped>`). A later bound change re-clamps `H`.
- `passiveReplicas: 0` answers inactive, so KEDA parks the target after
  `cooldownPeriod` if `minReplicaCount` is 0; otherwise the clamp makes it the
  minimum.
- `passiveReplicas` above 0 on a target at zero answers active: KEDA wakes it and
  the HPA scales to `n`. With a fixed count, passive mode **can change a
  count**: to `n`, once, at the operator's explicit request. Default hold never
  does.

**Across a restart.** `H` is persisted per variant, so a restart or a lease
hand-off keeps it rather than re-reading a count that drifted. Proposed store: a
ConfigMap `wva-passive-holds` in the controller namespace, one key per
`<namespace>.<scaledObject>`, value `{"replicas":H,"since":...,"source":"latched|passiveReplicas"}`,
written by the leader only when a hold starts, changes or ends. The scaler
refuses (as before the lease) until the new leader has loaded it. A passive
target with no record is latched fresh, and that is logged.

**A hand `kubectl scale`** on a held target is reverted at the next HPA sync
(15 s): the metric still says `H`, and the scale-down window holds `H` because
every recent recommendation was `H`. A scale-up toward `H` is immediate under the
installer's 0 s scale-up window. To set a count by hand, change `passiveReplicas`,
or use KEDA's own `autoscaling.keda.sh/paused` (below).

### observe

The HPA takes the highest count over its metrics, and KEDA activates when any
trigger is active.

- `GetMetrics` answers **0**. The ratio is `0 / status.replicas`, outside
  tolerance, so the proposal is `ceil(0/1) = 0`, the lowest an AverageValue
  metric can propose. The HPA's `max` over metrics never picks it. (At
  `status.replicas = 0` the ratio is NaN, also outside tolerance, still 0.)
- `IsActive` answers false; `StreamIsActive` sends false and never sends true.
- **Never an error.** An erroring metric blocks scale-down for every metric:
  `computeReplicasForMetrics` returns an error when
  `invalidMetricsCount > 0 && replicas < specReplicas` (HPA source above). On
  KEDA's side an error also counts toward `fallback.failureThreshold`, after
  which a configured `fallback.replicas` would set the count
  (`pkg/fallback/fallback.go`, as the scaler's `errNotLeader` comment records).
  The observe answer does not depend on a decision, so a standby replica can
  give it too, and the lease hand-off stops blocking KEDA's scale-down.
- `advanced.scalingModifiers` (a KEDA formula over the triggers) breaks the
  argument above: our 0 would enter the formula (an average, for example). The
  controller logs a warning per change and reports the shape as
  `observe-unsafe`; it still answers 0. See the open questions.

**Detection.** The enricher already reads each ScaledObject uncached
(`registry.TargetFromScaledObject`). It adds `ForeignTriggers int`: triggers
that are not `external`/`external-push` addressed to our scaler. Zero means
hold, more means observe. A trigger added or removed is seen at the next
enrichment (`DefaultTargetMaxAge`).

### No trigger of ours

Discovery is KEDA-driven: the controller manages only the ScaledObjects KEDA
calls it about (`Handler.observe`, `registry.Observe`;
`docs/plans/engine/keda-driven-discovery.md`). A model with no trigger of ours is
never seen.

The cheapest opt-in is to add our trigger and let observe mode make it inert.
That edits the user's ScaledObject, but changes nothing it does.

If that edit is unwanted, an annotation `llm-d.ai/observe: "true"` on the
Deployment or LeaderWorkerSet would opt it in. Cost:

- a second discovery path beside the KEDA one: entries kept alive by a watch, not
  by a TTL;
- `list`/`watch` on deployments is already granted cluster-wide
  (`manager-clusterrole.yaml`); LeaderWorkerSets need the same; a
  namespace-scoped install needs it per namespace;
- the collector needs what the trigger carries today: the model ID (a second
  annotation, reviving the removed `llm-d.ai/model-id`) and the bounds (from the
  ScaledObject that targets it, found by a scaledobjects `list` the controller
  dropped on purpose, or no bounds at all).

Recommendation: no annotation in the first version.

## Writes skipped for a passive model

Grep of every client write in `internal/` and `cmd/` (`Create`, `Update`,
`Patch`, `Delete`, `SubResource(...).Update/Patch`):

| writer | in passive |
| --- | --- |
| `warmpool/pool/adapter.go`: supervisor create/delete, pod patches | skipped |
| `warmpool/trigger.go`: bridge started on a decision-store write | skipped (it would start a bridge for a wake that never happens) |
| `steadystate/utilization_share_marks.go`, `utilization_share_history.go` | skipped; a passive model is in share shadow |
| `warmpool/preflight.go` | kept: a `SelfSubjectAccessReview` asks a question, it writes nothing |
| `learned_state_store.go`, `controllerstatus/publisher.go` | kept |
| Events `ScaledUp` / `ScaledDown` / `ScaledToZero` (`steadystate/engine.go`, `scalefromzero/engine.go`) | replaced by `PassiveRecommendation` (Normal, "would scale to n"), so an event never claims a scale that did not happen |

No other writer: nothing writes a scale subresource, a ScaledObject, an HPA, or a
Kueue Workload; the scaler and the enricher only read.

The in-process ledgers that treat a decision as an order are also skipped for a
passive target, or they reserve capacity for replicas that never come:
`allocation.InFlightLedger` (node holes) and the share ledgers.

## What stays live

- `wva_desired_replicas`, `wva_analyzer_target`, the share metrics, the cycle log:
  all report what the controller **would** do. `wva_current_replicas` (same
  labels) is the actual count, so the side-by-side comparison is the two gauges.
- New gauge `wva_passive_mode{namespace,model_name,variant_name,mode}`: 1 for the
  mode the variant is in, `mode` in `active|hold|observe|observe-unsafe`. Like
  `wva_utilization_share_mode`, one series per mode, 0 for the others.
- New gauge `wva_passive_held_replicas{namespace,model_name,variant_name}`: `H`,
  only in hold.
- Log, once per change: `Passive mode entered` / `Passive mode left`, with
  `mode`, `heldReplicas`, `source` (`latched|passiveReplicas`) and the layer that
  decided it (`perModel|tier:<name>|default|namespaceDefault|WVA_PASSIVE`).
- Per cycle, at V(1), per passive variant: `Passive: would scale` with
  `current`, `wouldBe`, `held` (hold) and `mode`.

## Interactions

**Limiter, quota, share accounting.** A passive model holds real GPUs whatever
the controller plans. Two passes per cycle when the fleet is mixed:

1. the would-be pass: every model as if active; its results are what passive
   models publish;
2. the actuated pass, only when some model is passive: passive variants pinned
   at their actual count (minimum = maximum = current, no demand beyond it);
   its results are what active models act on.

Without pass 2, an active model is planned against GPUs a passive model was
"told" to release, and its new pods sit Pending on the quota; or it loses budget
to a passive model's growth that never happens. Analyzers run once; only the
limiter and the optimizer run twice. A share group whose models are all passive
is a shadow group.

**Scale-to-zero and scale-from-zero.** Both are computed and logged, neither is
actuated. A passive model at zero with requests waiting is not woken: the wake
decision appears in the log and in `wva_desired_replicas`, and the requests wait
or fail at the gateway. The docs must say this plainly.

**Passive to active.** The scaler answers the decision from the next poll or
stream wake-up. Under the installer's behavior:

- up: 0 s window, 100% per 5 s policy, so the HPA doubles per sync until it
  reaches the decision. That is a step, not a ramp.
- down: the 300 s window holds the maximum recommendation seen, which is `H`;
  the scale-down starts 300 s after the switch, then 100% per 120 s.

The hold record is deleted. A model parked by `scaleToZero` whose retention ran
out while passive parks at the first active cycle (after `cooldownPeriod`).

**Active to passive.** `H` is latched from `spec.replicas` at the switch, so a
scale-up already ordered counts in `H`.

**Leader election.** Passive resolution is config only, the same on every
replica. Hold answers need `H`, so a standby refuses them as today; observe
answers need nothing and a standby can give them. The new leader loads
`wva-passive-holds` before it answers a hold.

**KEDA `autoscaling.keda.sh/paused`.** Nothing in `internal/` reads it; the
installer writes it (`deploy/lib/scaledobject.sh`, park/freeze). It is KEDA's
switch, not ours, and a paused ScaledObject is not called. Proposal: do not treat
it as passive; when the enricher sees it, report `paused` in the cycle log line
so a model that stopped moving is explained.

## Learning

- **hold:** the analyzers keep learning from readings, but only at one replica
  count. Per-replica load still varies with traffic, so fits get spread unless
  traffic is flat (a flat load cannot fit the ITL line). Overload is not scaled
  away, so the saturation knee is seen more often than in active mode. Nothing
  learns from a replica start: start time, wake latency, and the outcome of a
  scale-down are never observed.
- **observe:** KEDA moves the fleet, so the analyzers learn from real changes,
  including real start times.
- **What assumes our own decisions.** Anything that reads "what we decided last
  cycle" as "what is happening": the in-flight ledger and the share ledgers
  (skipped above), and the engine's holds and stabilization (shape-change hold,
  sticky scale-down, rescale held drift), which compare our previous output with
  the current count. In passive they run on the would-be series, which is
  self-consistent as a recommendation but not what happened. Each one is audited
  during implementation and either runs on the would-be series or is reset per
  cycle to the actual count; the audit's result goes into this note.
- Learned state persists as today, and is valid after a switch to active.

## Tests

- **config:** the precedence table (`WVA_PASSIVE` × default × namespace-local
  default × tier × per-model), `passive: false` over `true`, mixed-tier models,
  `passiveReplicas` validation (negative, non-integer) and the clamp warning.
- **scaler:** in hold, `GetMetrics` answers `H` while the store holds 0, 1, 10
  and a scale-from-zero wake; `IsActive` and `StreamIsActive` never flip;
  observe answers 0/false and never errors, also on a standby; detection of
  foreign triggers and `scalingModifiers`.
- **e2e-style through the scaler:** fake client with a Deployment and its scale
  subresource, plus a small loop that applies the HPA's AverageValue rule, its
  stabilization and KEDA's activation to the scaler's answers. Over many cycles
  with the decision swinging, `spec.replicas` never changes; with a foreign
  trigger, the count follows the foreign trigger only; after a switch to active
  it reaches the decision under the behavior above. Restart mid-hold: same `H`.
- **writes:** an engine cycle against a recording client: no Create, Update,
  Patch or Delete on pods or the supervisor for a passive model; marks and
  history for an active model in the same group still written.
- **kind e2e** (`make test-e2e-smoke`): a passive model under load keeps its
  replica count for several minutes while `wva_desired_replicas` rises; a
  per-model `passive: false` scales; documented in
  `docs/developer-guide/testing.md`.

## Upgrade and rollback

- **Upgrade:** both keys absent means active, as today. No CRD change.
- **Switch on:** `passive: true` in a policy entry applies at once;
  `WVA_PASSIVE` needs a restart.
- **Installer re-runs:** `make deploy-wva` re-applies the `default` entry from
  the shipped manifest, and the manager ConfigMap. It must preserve `passive`
  and `passiveReplicas` in `default` (as it preserves `limiters:`), and take
  `WVA_PASSIVE` from an install variable, keeping the live value when unset.
  Otherwise a re-run silently turns a passive controller active.
- **Rollback** to a version without passive mode: it ignores both keys (an
  unknown policy key is logged and the rest of the entry applies), so every
  model becomes active at once. Before rolling back, set
  `autoscaling.keda.sh/paused: "true"` on the ScaledObjects that must not move.
- **Switch off:** remove the key; the hold records are deleted; the HPA behavior
  governs the step (above).

## Open questions

1. Hold semantics: latch `H` at entry (this note), or answer the live
   `spec.replicas` every poll? Live lets a hand `kubectl scale` stick and needs
   no persistence; the latch reverts it.
2. Where `H` persists: a new `wva-passive-holds` ConfigMap (needs one
   `resourceNames` line in the controller-status Role) or a key in
   `wva-controller-status` (no RBAC change, but it mixes state into a status
   contract).
3. `passiveReplicas` is per model; a P/D model has one count per role. Add a
   per-variant `passiveReplicas` in trigger metadata, the existing most specific
   layer?
4. Mixed fleets: the two-pass limiter (this note), or pin only and publish no
   would-be limiter result for passive models?
5. `scalingModifiers` in observe: answer 0 and warn (this note), or refuse the
   shape?
6. Passive to active: rely on the HPA behavior, or add a ramp in the controller?
7. `paused`: report only (this note), or treat as passive?
8. No trigger of ours: no annotation for now (this note)?
