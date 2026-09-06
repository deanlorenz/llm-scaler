# composeAnalyzerResults — composition logic plan (2026-09-06)

Role: p3-planner. Planning only — no code changes.

---

## Context

`runAnalyzersAndScore` in `engine_v2.go` already builds a `[]NamedAnalyzerResult` — one
entry per analyzer that ran, each fully processed by `buildNamedResult` + `buildCapacities`
+ `normalizeToCompositeUnits`. It currently picks `namedResults[0]` (sat) unchanged as
`CompositeSignal`.

PR #2 adds `composeAnalyzerResults(namedResults []NamedAnalyzerResult) NamedAnalyzerResult`
called after `updateLivenessAndSetLive` / `recordAnalyzerMetrics` / `logAnalyzerResult`
have all run on the full slice, immediately before `collectV2ModelRequest` picks the
single value to pass downstream.

**Input:** `[]NamedAnalyzerResult` — each entry is fully built (RC, SC, RoleCapacities,
SatDemand, Live, Score all set). Sat is always at index 0.

**Output:** one `NamedAnalyzerResult` — the composite. Sat-only fast path returns
`namedResults[0]` unchanged.

---

## The reduce operation

The design doc establishes that units differ across analyzers so the common currency is
**implied replica count** per variant:

```
impliedReplicas(nr_i, v) = ceil( demand_i[v] / PRC_i[v] )
```

Since `buildCapacities` has already run on every entry, all the fields needed are already
on each `NamedAnalyzerResult`. The math does not require touching `*domain.AnalyzerResult`
directly — we work entirely from the `NamedAnalyzerResult` layer.

### Non-disaggregated model (RoleCapacities == nil)

The composite's `RequiredCapacity` is:

```
maxRC = max over informative, live namedResults of nr_i.RequiredCapacity
```

This is exactly `max_i ceil(demand_i / PRC_i) × PRC_sat` expressed in sat's units after
normalization — but since CT6 normalization sets `TotalDemand = 1.0` and `RC` is derived
from it, `RC` is already the implied-replica signal in coverage units. Taking `max RC`
across analyzers is equivalent to the design doc's `max implied_replicas` in a
post-normalization world.

Sat is always included (index 0), so `compositeRC >= satRC` — floor invariant holds.

The composite's `SpareCapacity` is the **minimum** across live entries (most conservative
scale-down posture):

```
minSC = min over live namedResults with SC > 0 of nr_i.SpareCapacity
```

If no live non-sat analyzer has SC > 0, composite SC = sat's SC (unchanged).

### Disaggregated model (RoleCapacities != nil)

Same logic per role. For each role `r` in `sat.RoleCapacities`:

```
composite.RoleCapacities[r].RequiredCapacity = max over informative live nr_i of
    nr_i.RoleCapacities[r].RequiredCapacity   (use 0 if role absent from nr_i)

composite.RoleCapacities[r].SpareCapacity = min over live nr_i of
    nr_i.RoleCapacities[r].SpareCapacity      (use sat's if no other live entry has it)
```

---

## Reusing the existing multi_backup helpers

The multi_backup helpers (`analyzer_helpers_multi.go`) operate on
`[]NamedAnalyzerResult` and already express max/min over the slice. The compose
function should **reuse their logic directly**, not duplicate it. Specifically:

| Compose need | Multi_backup helper to call or adapt |
|---|---|
| `maxRC` across entries | `roleBottleneckReplicas` — already does `max_i ceil(state[i][role] / PRC_i[v])` in the picker-state space. For the compose step we need the equivalent on `RequiredCapacity` / `RoleCapacities[r].RC` directly. This is structurally the same max but over the already-built RC field rather than picker state. The helper is not directly callable (it takes picker state), but the **pattern** is reused. |
| `minSC` across entries | `needsScaleDownForRole` + `safeRemovalReplicasForRole` — both already express the min/all-agree gate over the slice. For compose, the SC field is already scalar on each `NamedAnalyzerResult`, so the min is a simple loop with the same Live-gating logic as these helpers. |
| `anyRoleNeedsScaleUp` | Directly reusable after `initRoleState` is called on the composite result. No change needed. |

**Approach:** compose builds a mutated copy of `namedResults[0]` (sat), overwriting only
`RequiredCapacity`, `SpareCapacity`, `Remaining`, `Spare`, and `RoleCapacities` RC/SC
fields. All other fields (`Name`, `Score`, `SatDemand`, `Live`, `Result`,
`TotalSupply`, `TotalAnticipatedSupply`, `Utilization`, `ScaleUpThreshold`,
`ScaleDownBoundary`) are inherited from sat unchanged. This means:

- The optimizer receives a `NamedAnalyzerResult` with sat's `Name`, `Score`, `Result`,
  and `SatDemand` — all downstream code that reads those fields is unaffected.
- Only the RC/SC signals (what the optimizer actually scales from) are elevated by
  non-sat demand.
- The multi_backup helpers are then called on a single-element `[]NamedAnalyzerResult`
  containing the composite, exactly as they are called today. **No change to any
  multi_backup helper signature or body.**

---

## Open question resolutions (proposed)

### Q1 — per-variant `TotalDemand` consistency

**Resolution:** update per-variant `TotalDemand` on the composite's `Result` to
`max_replicas[v] × sat.PRC[v]`. This keeps the model-level `TotalDemand` and the
per-variant values consistent. Diagnostic tooling sees the composite demand, not sat-only.
Per-variant utilization figures will reflect the most demanding analyzer.

### Q2 — `RoleDemand` when non-sat doesn't emit it

**Resolution:** a non-sat analyzer with no `RoleDemand` (i.e. `RoleCapacities == nil` on
its `NamedAnalyzerResult`) does not participate in the per-role RC/SC reduce. It
contributes only to the model-level `TotalDemand` path via its `RequiredCapacity` scalar.
This is the safest default: we never invent per-role demand that the analyzer didn't
express.

### Q3 — Name of the composite

**Resolution:** keep `Name = domain.SaturationAnalyzerName` on the composite. The
`hasSaturationResult` check at `engine_v2.go:721` will continue to work. When a future PR
changes the rescale path to be composite-aware, the name check can be revisited. This is
the zero-risk option and defers the naming question to when it actually matters.

### Q4 — Score for the composite

**Resolution:** use sat's `Score` (inherited unchanged). Preserves today's fair-share
behaviour exactly. Non-sat analyzers only affect RC/SC, not priority weighting.

---

## Function signature

```go
// composeAnalyzerResults reduces a fully-built []NamedAnalyzerResult to a single
// composite entry. The composite uses saturation (index 0) as the floor: its RC/SC
// signals are elevated to the maximum RC / minimum SC across all live, informative
// entries, but all other fields (Name, Score, Result, SatDemand, thresholds) are
// inherited from saturation unchanged.
//
// Sat-only fast path: when len(namedResults) == 1, returns namedResults[0] unchanged.
//
// Precondition: namedResults[0] is the saturation entry; liveness has been set on all
// entries (updateLivenessAndSetLive has run).
func composeAnalyzerResults(namedResults []allocation.NamedAnalyzerResult) allocation.NamedAnalyzerResult
```

---

## Placement in `runAnalyzersAndScore`

```
// existing:
e.updateLivenessAndSetLive(ctx, namespace, modelID, namedResults)
e.recordAnalyzerMetrics(namespace, modelID, namedResults)
for _, nr := range namedResults { logAnalyzerResult(...) }

// new — after all per-analyzer observability has run:
composite := composeAnalyzerResults(namedResults)
return composite, nil    // ← was: return namedResults, nil
```

`collectV2ModelRequest` then assigns `CompositeSignal: composite` (was `namedResults[0]`).
The return type of `runAnalyzersAndScore` changes from `[]NamedAnalyzerResult` to
`NamedAnalyzerResult` — matching the pre-PR-34 branch tip `fcf9c905` that the old call
map described.

---

## What does NOT change

- `updateLivenessAndSetLive`, `recordAnalyzerMetrics`, `logAnalyzerResult` — all still
  called with the full `namedResults` slice before compose runs. No signature changes.
- All optimizer helpers (`initRoleState`, `applyAllocation`, etc.) — called on the single
  composite entry exactly as today.
- `hasSaturationResult`, `rescale.go`, `variant_records.go` — read `CompositeSignal`; no
  change needed since composite inherits sat's `Name`.
- `multi_backup/` helpers — untouched; they document the multi-entry originals for
  reference.

---

## Tests

`multi_backup/analyzer_helpers_multi_test.go` contains 5 multi-entry test cases. These
should be adapted (not the backup files themselves) into new test cases for
`composeAnalyzerResults` covering:

1. Sat-only fast path — composite = sat unchanged.
2. Non-sat analyzer with higher RC — composite RC elevated.
3. Non-sat analyzer with lower RC — composite RC = sat's (floor holds).
4. Disaggregated model — per-role RC elevated where non-sat is higher.
5. Non-sat analyzer not live — excluded from reduce; composite = sat.

---

## Summary

| Item | Decision |
|---|---|
| Where compose runs | Inside `runAnalyzersAndScore`, after observability, before return |
| Input | `[]NamedAnalyzerResult` (fully built, liveness set) |
| Output | Single `NamedAnalyzerResult` (sat as base, RC/SC elevated) |
| RC rule | `max` over live informative entries |
| SC rule | `min` over live entries (conservative) |
| RoleDemand absent | Non-sat without RoleCapacities excluded from per-role reduce |
| Name | `domain.SaturationAnalyzerName` (inherited from sat) |
| Score | Sat's Score (inherited) |
| SatDemand | Sat's SatDemand (inherited) |
| Return type change | `runAnalyzersAndScore` → `NamedAnalyzerResult` (single value) |
| Helper reuse | Pattern reused from multi_backup; no multi_backup changes |
