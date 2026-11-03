# Passive mode

**Status:** built on `feat/passive-mode`. The operator's page is
[scaling-policy.md, passive](../reference/scaling-policy.md#passive); this note
keeps the decisions and the reasons.

## Summary

In passive mode the controller computes, logs and publishes everything it does
today, and changes no replica count. It is the utilization-share `shadow`
switch, for the whole controller, settable per model. One model can observe
while the others scale, and a passive controller can still scale one model.

There are two shapes, read from the ScaledObject the controller already reads:

| ScaledObject triggers | mode | our trigger answers | who moves the fleet |
| --- | --- | --- | --- |
| ours alone | `hold` | the target's live `spec.replicas`, clamped to its bounds | the operator, by hand |
| ours beside others | `observe` | `0`, inactive | KEDA, from the other triggers |
| beside others, a `scalingModifiers` formula naming ours | `observe-unsafe` | as `observe` | unsupported; warned |

A model with no trigger of ours is not discovered (discovery is KEDA-driven:
`Handler.observe`, `registry.Observe`). The answer is to add our trigger, which
`observe` makes inert. No annotation.

## Configuration

Resolved like `scaleToZero.enabled`: a controller flag as the last fallback,
and a field on every scaling-policy entry (`config.ResolvePassive`).

| key | where | type | absent means |
| --- | --- | --- | --- |
| `WVA_PASSIVE` | `wva-manager-config`, read at start | `"true"`/`"false"` | `false` |
| `passive` | any scaling-policy entry | `*bool` | inherit |

Most specific first: per-model entry, then the tier (the variant's
`scalingPolicy`, else `default.defaultPolicy`), then the `default` entry (the
namespace's own map when it has one), then `WVA_PASSIVE`. With variants in
different tiers, the model is passive if any tier says so. `passive` is a
pointer because `Merge` copies only non-zero fields, and `passive: false` must
be able to turn a passive default back to active for one model.

```yaml
# Passive controller, one model scaling.
# wva-manager-config: WVA_PASSIVE: "true"
data:
  meta_llama-70b.prod: |
    model_id: meta/llama-70b
    namespace: prod
    passive: false
```

```yaml
# Active controller, one namespace observing (its own map replaces the global one).
# wva-scaling-policy-config in namespace "lab"
data:
  default: |
    passive: true
    analyzers: [{name: saturation, score: 1.0}]
```

**Later:** a fixed `passiveReplicas` count per model. Not in v1: an operator
sets the count with `kubectl scale` or the manifest, which `hold` keeps.

## What KEDA is told

The trigger is `metricType: AverageValue`, target 1 (`metricTargetSize`). The
HPA source (Kubernetes v1.33, `pkg/controller/podautoscaler`) gives the rules:

- the current count is `scale.Spec.Replicas` (`reconcileAutoscaler`);
- for AverageValue, `GetExternalPerPodMetricReplicas(statusReplicas, ...)`:
  inside the 10% tolerance it proposes `status.replicas`, else
  `ceil(value / 1)`;
- the HPA takes the highest proposal over its metrics;
- `stabilizeRecommendationWithBehaviors` clamps the current count between the
  minimum of the scale-up window and the maximum of the scale-down window, both
  of which contain the new desired value; so a desired equal to the current
  count returns the current count, whatever came before;
- an invalid metric blocks every scale-down: `computeReplicasForMetrics` errors
  when `invalidMetricsCount > 0 && replicas < specReplicas`.

**hold.** `GetMetrics` answers the live `spec.replicas` of the scale subresource
(`Handler.currentReplicas`, the read the HPA makes), clamped to
`minReplicaCount`/`maxReplicaCount`, on every poll. That equals target ×
current, so the HPA holds. `IsActive` and `StreamIsActive` answer "held count >
0": a target at zero stays at zero, a running one is never deactivated, and a
decision-store wake-up changes nothing. Nothing is latched or saved: a
restart, a lease hand-off or a `kubectl scale` all read the same live count, and
a count set by hand sticks.

**observe.** `GetMetrics` answers `0`: outside tolerance, so the proposal is 0
and never the highest. `IsActive` and `StreamIsActive` answer false. Never an
error, which would block the HPA's scale-downs and count toward KEDA's
fallback; the answer needs no decision, so a standby replica gives it too.

## Writes skipped for a passive model

Every client write in `internal/` and `cmd/` (`Create`, `Update`, `Patch`,
`Delete`, subresource writes) was checked. Nothing writes a scale subresource,
a ScaledObject, an HPA or a Kueue Workload.

| writer | passive |
| --- | --- |
| warm pool adapter: supervisor create/delete, pod patches | refused by `pool.PassiveGuard` |
| `utilization_share_marks.go`, `utilization_share_history.go` | the group runs in shadow when it holds a passive model |
| `warmpool/preflight.go` | kept: a SelfSubjectAccessReview writes nothing |
| `learned_state_store.go`, `controllerstatus/publisher.go` | kept |
| Events `ScaledUp`/`ScaledDown`/`ScaledToZero` | `PassiveRecommendation` instead |

In-process orders skipped too, or they would reserve capacity for replicas that
never come: the in-flight ledger (`inFlightAdmissions`), the borrowed-capacity
draws (`recordBorrowDraws`), and the scale-from-zero wake (no decision, no hole,
no draw; logged once).

## Metrics and logs

- `wva_desired_replicas` is the would-be count, `wva_current_replicas` the real
  one; the side-by-side comparison is those two.
- `wva_passive_mode{namespace,model_name,variant_name,mode}`: 1 for the mode,
  0 for the others.
- `Passive mode entered` / `left` / `changed`, once per change, with `mode`,
  `source` (the layer) and `foreignTriggers`; a warning on `observe-unsafe`;
  a line when KEDA's `paused` annotation is set or cleared.
- `Passive: would scale` per variant per cycle, at V(3).

## Interactions

**Limiter, quota, share.** One limiter pass. A passive model counts at its real
usage, and its would-be count comes from the same pass, so its would-be growth
can take budget from an active model in that pass, and its would-be shrink
frees nothing. A share group holding a passive model is planned in shadow; its
active models get the ordinary optimizer's targets.

**Passive to active.** The HPA's own behavior, no ramp. Under the installer's
ScaledObject a scale-up applies at the next sync and can double every 5 s; a
scale-down waits the 300 s window, since every recent recommendation was the
held count.

**Leader election.** Passive is configuration, the same on every replica.
`hold` needs the live count and is answered by the leader; `observe` is
answered by a standby too.

**KEDA `paused`.** Nothing read it before. Now the enricher reads it
(`Target.KEDAPaused`) and the engine logs it. It is not treated as passive:
while it is set KEDA acts on no answer at all.

## Learning

In `hold` the fleet never moves: the analyzers learn at one replica count, a
fit needs the load to vary, and no replica start is measured. In `observe`
KEDA moves the fleet and the analyzers learn from real changes, start times
included.

Audit of what compares against our own previous output, which in passive is
not what was applied:

| component | reads | passive |
| --- | --- | --- |
| sticky scale-down hold (`scalingpolicy.HoldPublishedScaleDown`) | the published decision | **off**: "a descent in flight" would be true for as long as the model is passive |
| no-decision carry (`scalingpolicy.CarryPublished`) | the published decision | **off**: reports the running count |
| rescale role holds (`allocation.heldRescaleTargets`, `splitRoles`) | `VariantReplicaState.CurrentReplicas`, the applied count | safe |
| shape-change hold (`saturation/shape_change.go`) | the arriving prompt length, a traffic signal | safe |
| saturation analyzer | replica metrics and the scale target; no read of the decision store | safe |
| in-flight ledger, borrow draws | the decision as an order | skipped |
| share ledgers and marks | the plan as an order | shadow |
| scale-to-zero retention | idle requests; `MarkActivated` on a wake | safe; a passive model is not woken |

## Tests

- `config`: precedence table, `passive: false` over `true`, any-tier rule,
  `passive` a known key.
- `passive`: mode from the ScaledObject (alone, beside prometheus, beside
  another external scaler, formula naming ours or not, paused), clamp, lookup.
- `scaler`: hold answers the live spec whatever the decision, follows a hand
  scale, clamps, keeps zero asleep, no `StreamIsActive` push on a wake;
  observe answers 0 and inactive, never errors, also before the lease and with
  the target unreadable, and with a formula. An HPA-and-KEDA loop driven by the
  answers: hold never moves the count across 40 swinging decisions, observe
  follows the other trigger only, active follows the decision.
- `steadystate`: sticky hold and carry off, `PassiveRecommendation` not
  `ScaledDown`, no node hole, a share group with a passive model writes
  nothing (a client that counts every Create, Update and Patch).
- `warmpool/pool`: the guard refuses all four actions and passes an active
  model through.

Not covered by a test: the scale-from-zero engine's passive gate (its harness
needs a live EPP scrape), and the installer's keep of `WVA_PASSIVE`.

## Upgrade and rollback

- Upgrade: both keys absent is active, as before.
- `make deploy-wva` keeps the live `WVA_PASSIVE` and the default entry's
  `passive`; `WVA_PASSIVE=true|false` sets it.
- Rollback to a release without passive mode scales every passive model at
  once: it ignores both keys. Annotate the ScaledObjects that must not move with
  `autoscaling.keda.sh/paused=true` first
  ([upgrade-and-uninstall.md](../reference/upgrade-and-uninstall.md#passive-mode-on-upgrade-and-rollback)).
