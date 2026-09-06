# Engine call map — single-analyzer refactor (updated 2026-09-06)

Shows the current state of `internal/engines/steadystate/engine_v2.go` in upstream `main`
after PR #34 merged (commit `113fec1d`). Base for PR #2 planning.

**Previous base:** `c6e408c4` → tip `fcf9c905` (pre-merge, written 2026-08-30)
**Current base:** `113fec1d` (merged) = `b848b19c` (warmpool merge, no change to these files)

---

## `runAnalyzersAndScore`

**Return type: `([]allocation.NamedAnalyzerResult, error)`** — returns the full slice.

`collectV2ModelRequest` picks `namedResults[0]` (sat, always first) as `CompositeSignal`.
All entries get liveness, metrics, and logging before the slice is returned.

```
runAnalyzersAndScore(ctx, modelID, namespace, replicaMetrics, config, ...)
  |- runV2AnalysisOnly(...)                            → baseResult (*domain.AnalyzerResult, sat only)
  |- buildNamedResult(SaturationAnalyzerName, ...)    → namedResults[0]
  |- publishWarmPoolSupply(namespace, namedResults[0])   (warmpool: sat's bridge contributions)
  |- publishVariantPressure(namespace, namedResults[0])  (warmpool: variant pressure signal)
  |- for each enabled non-sat analyzer:
  |     runRegisteredAnalyzer(...)                     → result
  |     buildNamedResult(entry.name, result, ...)      → namedResults[1..]
  |- updateLivenessAndSetLive(namedResults)            (sets .Live on each entry in-place)
  |- recordAnalyzerMetrics(namedResults)               (emits wva_analyzer_demand/target per entry)
  |- for _, nr := range namedResults: logAnalyzerResult(...)
  `- return namedResults, nil                          (full slice, sat always at [0])
```

Note: `composeAnalyzerResults` does NOT exist in upstream main. It was present in the
pre-merge branch tip (`fcf9c905`) but was not included in the merged commit (`113fec1d`).
The engine-side reduce (PR #2) will add it.

---

## `collectV2ModelRequest`

Calls `runAnalyzersAndScore`, picks `namedResults[0]` as `CompositeSignal`.

```
CURRENT:
    namedResults, err := e.runAnalyzersAndScore(...)   ← receives []NamedAnalyzerResult
    return &allocation.ModelScalingRequest{
        CompositeSignal: namedResults[0],              ← sat entry, always index 0
        ...
    }
```

---

## `hasSaturationResult`

Reads `CompositeSignal` directly (no loop).

```go
func hasSaturationResult(req allocation.ModelScalingRequest) bool {
    return req.CompositeSignal.Name == domain.SaturationAnalyzerName &&
           req.CompositeSignal.Result != nil
}
```

---

## `updateLivenessAndSetLive`

**Signature: UNCHANGED** from pre-PR #34.
`func (e *Engine) updateLivenessAndSetLive(ctx, namespace, modelID string, namedResults []NamedAnalyzerResult)`

Called with the full `namedResults` slice (sat at [0], any registered non-sat entries
following). Sets `.Live` on each entry in-place, then calls `detectDemandLiveness`.

---

## `detectDemandLiveness`

**Signature: UNCHANGED.**
`func (e *Engine) detectDemandLiveness(ctx, modelID, namespace string, namedResults []NamedAnalyzerResult, ...)`

Searches the slice for an entry with `Name == throughput.AnalyzerName`. Today, when only
sat is enabled, no entry matches and this is a no-op. When a non-sat analyzer is enabled
its entry appears in the slice; whether it can match depends on the analyzer's registered
name.

---

## `recordAnalyzerMetrics`

**Signature: UNCHANGED.**
`func (e *Engine) recordAnalyzerMetrics(namespace, modelID string, results []NamedAnalyzerResult)`

Called with the full `namedResults` slice. Emits `wva_analyzer_demand` / `wva_analyzer_target`
per entry.

---

## New in upstream main (warmpool merge, not in call map scope but noting for awareness)

- `publishWarmPoolSupply(namespace, namedResults[0])` — called inside `runAnalyzersAndScore`
  after building `namedResults[0]`, before the non-sat loop.
- `publishVariantPressure(namespace, namedResults[0], satUp)` — called immediately after.
- Both take `namedResults[0]` (sat entry) by value. Not affected by engine-side reduce.

---

## Summary of current state vs pre-PR-34 state

| Function | Pre-PR-34 (c6e408c4) | Current (113fec1d / main) |
|---|---|---|
| `runAnalyzersAndScore` | returned `[]NamedAnalyzerResult` (one per buildNamedResult call) | returns `[]NamedAnalyzerResult` (same shape — sat first, non-sat follow) |
| `composeAnalyzerResults` | did not exist (pre-CT2) | does not exist (not merged in PR #34) |
| `collectV2ModelRequest` | set `AnalyzerResults: namedResults` | sets `CompositeSignal: namedResults[0]` |
| `hasSaturationResult` | looped `req.AnalyzerResults` | reads `req.CompositeSignal` directly |
| `updateLivenessAndSetLive` | called with full slice | called with full slice (unchanged) |
| `detectDemandLiveness` | called with full slice | called with full slice (unchanged) |
| `recordAnalyzerMetrics` | called with full slice | called with full slice (unchanged) |

---

## What PR #2 (engine-side reduce) must add/change here

- Add `composeAnalyzerResults(namedResults) → NamedAnalyzerResult` call inside
  `runAnalyzersAndScore` (or in `collectV2ModelRequest`) to reduce the slice to one entry.
- The 4 open design questions (Q1–Q4 in compose-reduce-design doc) must be resolved first.
- See `.session/ledgers/compose-reduce-design-2026-08-30.md` for details.
