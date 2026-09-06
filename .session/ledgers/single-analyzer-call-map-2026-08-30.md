# Single-analyzer refactor: call map of all changed sites

This is the updated call map showing every site touched by the three commits on this branch
(e4106109, b980f682, fcf9c905) relative to the rebase base `c6e408c4`.

---

## 1. The engine side — `internal/engines/steadystate/engine_v2.go`

### `runAnalyzersAndScore` (lines 91–187)

Runs all analyzers, calls `composeAnalyzerResults`, then builds **one** `NamedAnalyzerResult`.

```
baseResults := []rawAnalyzerResult{ sat, ... other enabled analyzers ... }
composed    := composeAnalyzerResults(baseResults)   // → rawAnalyzerResult
namedResult := buildNamedResult(composed.name, composed.result, ...)
namedResults := []allocation.NamedAnalyzerResult{namedResult}  ← still a slice here
updateLivenessAndSetLive(namedResults)
recordAnalyzerMetrics(namedResults)
return namedResult
```

**Note:** `namedResults` is a 1-element slice used as the argument to `updateLivenessAndSetLive`
and `recordAnalyzerMetrics`. Both functions still take `[]NamedAnalyzerResult`. They are
not yet simplified to a single entry.

### `composeAnalyzerResults` (lines 199–215)

```go
func composeAnalyzerResults(baseResults []rawAnalyzerResult) rawAnalyzerResult {
    for _, r := range baseResults {
        if r.name == domain.SaturationAnalyzerName {
            return r   // ← always returns sat unchanged
        }
    }
    return baseResults[0]
}
```

Always returns saturation's raw result. Any other enabled analyzer's result is silently
discarded. This is the stub that needs the real reduce logic.

### `collectV2ModelRequest` (line 819)

```go
CompositeSignal: namedResult,   // single NamedAnalyzerResult, not a slice
```

### `updateLivenessAndSetLive` (lines 349–391) — NOT YET SIMPLIFIED

Still takes `[]NamedAnalyzerResult`. Today always called with a 1-element slice.
Iterates over the slice setting `nr.Live` per entry. Also calls `detectDemandLiveness`.

### `detectDemandLiveness` (lines 436–) — NOT YET SIMPLIFIED

Still takes `[]NamedAnalyzerResult`. Searches for the throughput analyzer entry by name.
Today always called with a 1-element slice (the composite, named "saturation").
The throughput-name check therefore never fires today.

### `recordAnalyzerMetrics` (line 253) — NOT YET SIMPLIFIED

Still takes `[]NamedAnalyzerResult`. Today always called with a 1-element slice.

---

## 2. The optimizer contract — `internal/engines/allocation/optimizer_interfaces.go`

### `ModelScalingRequest` (line 72)

```go
// BEFORE (pre e4106109):
AnalyzerResults []NamedAnalyzerResult

// AFTER:
CompositeSignal NamedAnalyzerResult   // single value, not a slice
```

---

## 3. Optimizer helpers — `internal/engines/allocation/analyzer_helpers.go`

All 7 helpers changed from `s []NamedAnalyzerResult` to single-entry signatures (b980f682):

| Function | Before | After |
|---|---|---|
| `applyAllocation` | `s []NamedAnalyzerResult` | `e *NamedAnalyzerResult` |
| `initRoleState` | `s []NamedAnalyzerResult` | `e *NamedAnalyzerResult` |
| `RolePairedState` | `[]map[string]float64` | `map[string]float64` |
| `roleBottleneckReplicas` | `s []NamedAnalyzerResult` | `e NamedAnalyzerResult` |
| `roleAggRemaining` | `s []NamedAnalyzerResult` | `state RolePairedState` only |
| `safeRemovalReplicasForRole` | `s []NamedAnalyzerResult` | `e NamedAnalyzerResult` |
| `applyDeallocationForRole` | `s []NamedAnalyzerResult` | `e *NamedAnalyzerResult` |
| `needsScaleDownForRole` | `s []NamedAnalyzerResult` | `e NamedAnalyzerResult` |

Also changed:
- `anyRoleNeedsScaleUp(state, roles)` — same signature, `state` type changed
- `allocateForModelPaired` — `s []...` → `e *NamedAnalyzerResult`
- `scaleDownRoleIterated` — `s []...` → `e *NamedAnalyzerResult`
- `RolePickFn` — dropped `s []NamedAnalyzerResult` parameter
- `fairShareValue` — `s []NamedAnalyzerResult` → `e NamedAnalyzerResult`

Multi-entry (N>1) originals preserved under `multi_backup/` (`//go:build ignore`).

---

## 4. Greedy-score optimizer — `internal/engines/allocation/greedy_score_optimizer.go`

### `modelWork` struct

```go
// BEFORE:
s []NamedAnalyzerResult

// AFTER:
e *NamedAnalyzerResult
```

### Call sites — all wrap `req.CompositeSignal` directly (no more `[]NamedAnalyzerResult{...}`)

| Location | Change |
|---|---|
| `Optimize` loop, scale-up path (line 117) | `e := req.CompositeSignal` |
| `Optimize` loop, scale-down path (line 156) | `e := req.CompositeSignal` |
| `buildScaleUpWork` | parameter `s []...` → `e *NamedAnalyzerResult` |
| `fairShareScaleUp` / `allocate` inner call | `w.e` instead of `w.s` |
| `fairShareRolePick` | dropped `s []NamedAnalyzerResult` parameter |

---

## 5. Cost-aware optimizer — `internal/engines/allocation/cost_aware_optimizer.go`

### Call sites

| Location | Change |
|---|---|
| `Optimize` loop (line 59) | `e := req.CompositeSignal` |
| `costGreedyRolePick` | dropped `_ []NamedAnalyzerResult` parameter |
| `sortVariantsForScaleDown` | `s []NamedAnalyzerResult` → `e NamedAnalyzerResult` |
| `scaleDownRoleIterated` | `s []NamedAnalyzerResult` → `e *NamedAnalyzerResult` |

---

## 6. Rescale — `internal/engines/allocation/rescale.go`

| Location | Change |
|---|---|
| `rescaleModelDecisions` (line 344) | `satNamed := req.CompositeSignal` (was `req.AnalyzerResults[0]`) |
| `rescaleModelDecisions` (line 372) | `reclaimRole(ctx, req.CompositeSignal, ...)` |
| `reclaimRole` | `s []NamedAnalyzerResult` → `e NamedAnalyzerResult` |
| `buildDecisionsWithOptimizer` (line 528) | `satNamed := req.CompositeSignal` |

---

## 7. Variant records — `internal/engines/allocation/variant_records.go`

| Location | Change |
|---|---|
| `recordsForRequest` (line 79) | `nr := req.CompositeSignal` (was `req.AnalyzerResults[0]`) |

---

## 8. Skipped tests (WIP markers)

Three tests in the steadystate suite are currently `Skip()`-ed with the message:
> "composeAnalyzerResults now silently drops non-saturation analyzers (WIP single-analyzer
> refactor); rewrite once the multi-analyzer story is redesigned"

Locations:
- `engine_v2_test.go:375` — T1.4 multi-analyzer compose test
- `engine_v2_population_test.go:131` — population test with throughput
- `engine_v2_population_test.go:155` — population test with SLO
- `engine_v2_population_test.go:179` — population test with throughput+SLO

---

## 9. What is NOT in the PR yet

The following still take `[]NamedAnalyzerResult` and are called with a 1-element slice:

- `updateLivenessAndSetLive` in `engine_v2.go`
- `detectDemandLiveness` in `engine_v2.go`
- `recordAnalyzerMetrics` in `engine_v2.go`

These are engine-internal and not part of the optimizer contract. They are simplifiable
but haven't been touched yet.
