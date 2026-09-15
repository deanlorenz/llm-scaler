# Composite PRC downstream-usage audit — 2026-09-15

Factfinding for the open `SOHasSignal`/per-SO-signal design question (STATE.md §2.6 pending
item). Read-only investigation, no code changed. Dispatched as an `Explore` agent; this is its
report, lightly reformatted.

## 1. Composite creation → hand-off

| Hop | file:line |
|---|---|
| `buildComposite` returns `allocation.NamedAnalyzerResult`; `vc.PerReplicaCapacity` set at 163 (`demandByRole/compositeTotalReplicas`) or, when `compositeTotalReplicas <= 0`, falls through to `sourceVC.PerReplicaCapacity` (sat's raw PRC) at 171 | `internal/engines/steadystate/composite.go:160-172` |
| `buildCapacities(ctx, &composite, nil, scaleUp, scaleDown)` computes `composite.TotalSupply/TotalAnticipatedSupply/RequiredCapacity/SpareCapacity/RoleCapacities` from `compositeVCs` **unconditionally over every VC, live or C4** | `composite.go:195` → `engine_v2.go:925-980` → `internal/engines/aggregation/aggregation.go:50-66,113-124` |
| `collectV2ModelRequest` assigns the composite to `ModelScalingRequest.CompositeSignal` | `internal/engines/steadystate/engine_v2.go:814,847` |
| Model-level gate: `CompositeHasSignal(req.CompositeSignal)` — skips the model **only if every SO's decision path is C4**; a model with 1 live SO + 1 C4 SO passes through untouched | `engine_v2.go:693,714`, `engine.go:1091` |
| Request enters the optimizer pipeline (`requests = append(requests, *req)`) | `engine.go:1097` → `CostAwareOptimizer.Optimize` / `GreedyByScoreOptimizer.Optimize` |

## 2. Fan-out from `req.CompositeSignal`

```
req.CompositeSignal
 ├─ recordsForRequest(req) → buildVariantRecords(req, nr.Result)   [variant_records.go:78-84, 52-70]
 │     copies vc.PerReplicaCapacity into variantRecord.PerReplicaCapacity, NO Reason check at all
 │     ├─ CostAwareOptimizer.Optimize                [cost_aware_optimizer.go:48]
 │     ├─ GreedyByScoreOptimizer.Optimize (×2 call sites: scale-up / scale-down) [greedy_score_optimizer.go:112,146]
 │     ├─ applyRescale (records used only for singleAccType filter) [rescale.go:226]
 │     └─ modelCurrentGPUs                            [rescale.go:507]
 ├─ e := req.CompositeSignal (by value); initRoleState(&e) → RolePairedState/RoleSpare  [cost_aware_optimizer.go:59-60; greedy_score_optimizer.go:117-118,156-157]
 ├─ satNamed := req.CompositeSignal → buildDecisionsWithOptimizer            [cost_aware_optimizer.go:246,306]
 ├─ satNamed := req.CompositeSignal → rescaleInputsForGroup / rescaleModelDecisions  [rescale.go:344,528]
 └─ req.CompositeSignal passed directly into reclaimRole                    [rescale.go:372]
```

## 3. Consumer table

| Consumer (file:line) | Fields read | Gated on decision-path/eligibility? | Gated on PRC >0/≤0? |
|---|---|---|---|
| `buildCapacities`→`aggregation.SumTotalSupply/SumTotalAnticipatedSupply/AggregateByRole` (`internal/engines/aggregation/aggregation.go:50-66,113-124`) | `ReplicaCount`, `PendingReplicas`, `PerReplicaCapacity`, `TotalDemand` | **No** | **No** — unconditional sum |
| `applyUniversalThreshold` (`engine_v2.go:536-574`) | `TotalDemand`, `TotalSupply`, `TotalAnticipatedSupply` | No | No |
| `warnUnsizableShortfall` (`engine_v2.go:1048-1067`) | `PerReplicaCapacity` | No | Yes, `>0` (log-only) |
| `buildVariantRecords` (`variant_records.go:52-70`) | `PerReplicaCapacity`, `Utilization` | **No** | **No** |
| `recordsForRequest` (`variant_records.go:78-84`) | delegates to above | No | No |
| `costGreedyRolePick` (`cost_aware_optimizer.go:81-104`) | `PerReplicaCapacity` | No | Yes, `<=0` skip (line 90) |
| `scaleDownVariantSet` (`cost_aware_optimizer.go:110-151`) | `PerReplicaCapacity` | No | Yes, `<=0` skip (line 120) |
| `sortVariantsForScaleDown` (`cost_aware_optimizer.go:157-176`) | `prcForVariant` via `Score*PRC` weight | No | No (0 used as tie-break weight) |
| `costEfficiency` (`cost_aware_optimizer.go:226-231`) | `PerReplicaCapacity`, `Cost` | No | Yes, `<=0` → `math.MaxFloat64` |
| `buildDecisionsWithOptimizer` (`cost_aware_optimizer.go:235-311`) | `vc.Utilization`, composite RC/SC | No | No |
| `requiredSpareForRoleOrModel` (`query_api.go:96-105`) | `RequiredCapacity`, `SpareCapacity`, `RoleCapacities[role]` | No | No |
| `demandForRoleOrModel` (`query_api.go:66-79`) | `TotalDemand`, `RoleCapacities[role].TotalDemand` | No | No |
| `replicasForDemand`/`safeReplicasForSpare` (`query_api.go:19-48`) | `prc` param | No | Yes, `prc<=0`→0 |
| `prcForVariant` (`analyzer_helpers.go:85-94`) | `PerReplicaCapacity` | No | No (returns 0 if absent) |
| `applyAllocation` (`analyzer_helpers.go:65-83`) | `prcForVariant` result | No | Yes, `<=0`→no-op |
| `roleBottleneckReplicas` (`analyzer_helpers.go:188-196`) | `prcForVariant`→`replicasForDemand` | No | Indirect |
| `safeRemovalReplicasForRole`/`needsScaleDownForRole` (`analyzer_helpers.go:230-273`) | `e.Live`, `RoleSpare[role]`, `prcForVariant` | **Yes** — `e.Live` (model-level, not per-SO) | Yes, `<=0`→0/false |
| `applyDeallocationForRole` (`analyzer_helpers.go:245-259`) | `prcForVariant` | No | Yes, `<=0`→no-op |
| `allocateForModelPaired` (`analyzer_helpers.go:292-387`) | `prcFromVCs` per role | No | Implicit via `prc>0 && demand>0` (line 354) |
| `fairShareRolePick` (`greedy_score_optimizer.go:387-443`) | `vc.PerReplicaCapacity` | No | Yes, `<=0` skip (line 403) |
| `fairShareValue` (`greedy_score_optimizer.go:62-80`) | `e.Score`, picker state | No | No |
| `prcFromVCs` (`greedy_score_optimizer.go:475-483`) | `PerReplicaCapacity` | No | No (returns 0 if absent) |
| `reclaimRole`/`fillRole`/`markRoleGPULimited` (`rescale.go:400-485`) | `PerReplicaCapacity` (via `scaleDownVariantSet`/`sortByCostEfficiencyAsc`) | No | Yes, `<=0` skip (lines 441,475) |
| `roleDemandGPUs` (`rescale.go:583-600`) | `PerReplicaCapacity` (best variant's), `demandForRoleOrModel` | No | Yes, `<=0` skip (line 592) |
| `rescaleInputsForGroup` (`rescale.go:522-571`) | `satNamed.Result.TotalDemand`, per-variant PRC via `modelDemandGPUs`/`roleDemandGPUs` | No | Indirect |
| `RecordSaturationMetrics` gauges (`internal/metrics/metrics.go:1314+`, fed by `decision.Utilization/RequiredCapacity/SpareCapacity` at `cost_aware_optimizer.go:303,306`) | composite-derived Utilization/RC/SC | No | No |
| `logAnalyzerResult` (`engine_v2.go:1123-1157`) | `PerReplicaCapacity`, `Reason` | Reason logged, not branched on | No (observability only) |
| `computeCurrentGPUUsage*` (`engine_v2.go:672-720`) | `req.Variants`/`req.VariantStates` (not PRC) | Yes — `CompositeHasSignal` gates whether it runs | N/A |
| Model admission gate (`engine_v2.go:693,714`, `engine.go:1091`) | decision-path via `CompositeHasSignal` | Yes, but **model-scope**: passes if ANY SO ≠ C4 | N/A |
| `SOHasSignal` (`composite_signal_gate.go:15-24`) | per-SO `Reason` | Exists, defined, unit-tested — **zero production call sites** | N/A |

## 4. Supply/AnticipatedSupply formula sites

- `Supply(SO) = ReplicaCount × PerReplicaCapacity`: `aggregation.go:54` (`SumTotalSupply`), `:121` (`AggregateByRole`).
- `AnticipatedSupply(SO) = (ReplicaCount+PendingReplicas) × PerReplicaCapacity`: `aggregation.go:66` (`SumTotalAnticipatedSupply`), `:122` (`AggregateByRole`).
- Both called unconditionally per-VC inside `buildCapacities` (`engine_v2.go:966-967,975`) on `compositeVCs`, **no filter for `Reason == "C4-no-signal"`**. A C4 SO's fallen-through PRC (`composite.go:171`, sat's raw measured PRC) is summed into `TotalSupply`/`TotalAnticipatedSupply`/`RoleCapacities` exactly like a live SO's.
- `engine_v2.go:1389` (`publishVariantPressure`) computes the same `Supply` formula but on `namedResults[0]` (saturation directly), not the composite — out of scope, same pattern elsewhere.

## 5. Test coverage found

- `internal/engines/steadystate/composite_test.go` — tests composite *construction* (test 1: DecisionSatFallback identity; test 8d: whole-model no-signal; tests 13/15/17/24/25). Test 17 explicitly asserts the C4 fall-through preserves sat's raw PRC and that `TotalSupply`/`SpareCapacity` reflect it — documents the fall-through as *intended* at the composite boundary, but never traces into optimizer/rescale.
- `internal/engines/allocation/composite_signal_gate_test.go` — unit-tests `CompositeHasSignal`/`SOHasSignal` directly, including a mixed live+C4 composite (line 69: "is true when at least one SO reached a real decision even if others are C4-no-signal"). Proves `SOHasSignal` works correctly in isolation — but since it has no caller, this coverage never exercises a real optimizer decision under that mix.
- `composite_decision_test.go`, `composite_eligibility_test.go` — isolated unit coverage, not relevant to the gap.
- **No** test in `cost_aware_optimizer_test.go`, `rescale_test.go`, `greedy_score_optimizer_test.go`, `query_api_test.go`, `analyzer_helpers_test.go` constructs a composite with a live+C4 SO mix and asserts on the resulting scale-up/scale-down/GPU decision (grepped all for `DecisionNoSignal`/`C4-no-signal`, no hits outside the two files above).

## 6. Consumers with NO gating at all (decision-path / eligibility / >0 / <=0)

Root of propagation and everything downstream of it that inherits unfiltered:

1. **`aggregation.SumTotalSupply`/`SumTotalAnticipatedSupply`/`AggregateByRole`** (`aggregation.go:50-66,113-124`) — sums a C4 SO's fallen-through PRC like a live SO's. Root cause; every RC/SC downstream inherits this.
2. **`applyUniversalThreshold`** (`engine_v2.go:536-574`) — composite/per-role RC/SC built from (1), no re-filter.
3. **`buildVariantRecords`/`recordsForRequest`** (`variant_records.go:52-84`) — copies PRC unconditionally; shared entry point into all optimizers and rescale.
4. **`sortVariantsForScaleDown`** (`cost_aware_optimizer.go:157-176`) — PRC tie-break weight, 0 is a legitimate weight (not skipped) — a C4 SO's nonzero fallen-through PRC silently changes scale-down ordering.
5. **`buildDecisionsWithOptimizer`**'s Utilization/RC/SC (`cost_aware_optimizer.go:303,306`) — flows to `RecordSaturationMetrics` gauges with no gating; an operator-visible dashboard number can reflect a C4 fall-through PRC undetectably.
6. **`demandForRoleOrModel`/`requiredSpareForRoleOrModel`** (`query_api.go:66-105`) — reads of aggregates that already baked in (1)/(2); no independent gate.
7. **`rescaleInputsForGroup`**'s `Demand`/`CapGPUs`/`FloorGPUs` via `roleDemandGPUs` (`rescale.go:552,564,587-599`) — picks "best PRC among variants with PRC>0," no check that the winning variant's decision path was anything but C4-sat-fallback.
8. **`SOHasSignal`** — defined, tested, **never invoked in production**.

The only decision-path-aware gate anywhere downstream is `e.Live` (`analyzer_helpers.go:236,266`) — model-scope (true if *any* SO is live), and only blocks scale-down. Does nothing to stop a C4 SO's fallen-through PRC from being read by scale-up math (`applyAllocation`, `roleBottleneckReplicas`, `allocateForModelPaired`) or by any cost/rescale weighting above, even within a model where `Live == true` because some *other* SO is live.

## Bottom line

A `DecisionNoSignal` SO's PRC is not distinguished from a real one anywhere past `buildComposite`
— it is live scaling-math input, not inert. `SOHasSignal` is the one function purpose-built to
answer "is this SO's PRC backed by a real decision," fully implemented and tested, with zero
production callers.
