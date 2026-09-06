# Engine call map — single-analyzer refactor (2026-08-30)

Shows every changed call site in `internal/engines/steadystate/engine_v2.go`.
Base: `c6e408c4`. Current tip: `fcf9c905`.

---

## `runAnalyzersAndScore`

**Return type changed.**

```
BEFORE:  ) ([]allocation.NamedAnalyzerResult, error)
AFTER:   ) (allocation.NamedAnalyzerResult, error)
```

The function used to return a slice (one entry per built result). It now returns a single
`NamedAnalyzerResult` — the one composite entry built from the composed raw result.

**Internal shape** — unchanged between before and after:

```
runV2AnalysisOnly(...)                         → *domain.AnalyzerResult  (sat only)
runRegisteredAnalyzer(...) × N                 → *domain.AnalyzerResult  (other analyzers)
composeAnalyzerResults(baseResults)            → rawAnalyzerResult        (stub: returns sat)
buildNamedResult(composed.name, composed.result, ...) → NamedAnalyzerResult
updateLivenessAndSetLive(namedResults)         (see below)
recordAnalyzerMetrics(namedResults)            (see below)
logAnalyzerResult(namedResult)
return namedResult
```

**What changed around `namedResults` internally:**

```
BEFORE:
    namedResults := []NamedAnalyzerResult{
        buildNamedResult(...),
    }
    updateLivenessAndSetLive(ctx, namespace, modelID, namedResults)
    recordAnalyzerMetrics(namespace, modelID, namedResults)
    for _, nr := range namedResults { logAnalyzerResult(...) }
    return namedResults, nil               ← returned the slice

AFTER:
    namedResult := buildNamedResult(...)
    namedResults := []NamedAnalyzerResult{namedResult}   ← temporary 1-element slice
    updateLivenessAndSetLive(ctx, namespace, modelID, namedResults)
    namedResult = namedResults[0]                        ← read back after liveness sets .Live
    recordAnalyzerMetrics(namespace, modelID, []NamedAnalyzerResult{namedResult})
    logAnalyzerResult(ctx, modelID, namespace, namedResult)
    return namedResult, nil               ← returns single value
```

The `namedResults` slice still exists **inside** `runAnalyzersAndScore` as a temporary
wrapper so that `updateLivenessAndSetLive` (which sets `.Live` in-place) can mutate it and
the result is read back via `namedResults[0]`. This is an artefact of those functions not
yet being simplified to single-entry. It does not escape the function.

---

## `updateLivenessAndSetLive`

**Signature: UNCHANGED.**  
`func (e *Engine) updateLivenessAndSetLive(ctx, namespace, modelID string, namedResults []NamedAnalyzerResult)`

**Body: UNCHANGED.**  
Still iterates over the slice, sets `.Live` on each entry in-place, then calls
`detectDemandLiveness`.

**What changed at the call site:**  
Before, `namedResults` could in principle hold more than one entry (one per `buildNamedResult`
call). After, it always holds exactly one entry — the composite. The function's own code
is unaware of this; it loops as before and happens to iterate once.

---

## `detectDemandLiveness`

**Signature: UNCHANGED.**  
`func (e *Engine) detectDemandLiveness(ctx, modelID, namespace string, namedResults []NamedAnalyzerResult, ...)`

**Body: UNCHANGED.**  
Still searches the slice for an entry with `Name == throughput.AnalyzerName`. Because the
only entry in the slice is now named `"saturation"` (the composite name), this search
**never matches** and the function always returns early. It is a no-op today.

**Before:** could have matched a throughput entry if throughput was a separate built result.  
**After:** can never match — there is only the composite entry, named "saturation".

---

## `recordAnalyzerMetrics`

**Signature: UNCHANGED.**  
`func (e *Engine) recordAnalyzerMetrics(namespace, modelID string, results []NamedAnalyzerResult)`

**Body: UNCHANGED.**  
Iterates results, emits `wva_analyzer_demand` / `wva_analyzer_target` metrics per entry.

**Before:** called with `namedResults` (the slice built directly, then mutated by liveness).  
**After:** called with `[]NamedAnalyzerResult{namedResult}` (re-wrapped after liveness
read-back). Functionally identical — always one entry, always the composite.

---

## `collectV2ModelRequest`

**Changed: how it consumes `runAnalyzersAndScore` and what it puts in the request.**

```
BEFORE:
    namedResults, err := e.runAnalyzersAndScore(...)   ← received []NamedAnalyzerResult
    return &ModelScalingRequest{
        AnalyzerResults: namedResults,                 ← slice field
        ...
    }

AFTER:
    namedResult, err := e.runAnalyzersAndScore(...)    ← receives NamedAnalyzerResult
    return &ModelScalingRequest{
        CompositeSignal: namedResult,                  ← single-value field
        ...
    }
```

---

## `hasSaturationResult`

**Body changed** to match the new `ModelScalingRequest` shape.

```
BEFORE:
    func hasSaturationResult(req ModelScalingRequest) bool {
        for _, e := range req.AnalyzerResults {
            if e.Name == domain.SaturationAnalyzerName {
                return e.Result != nil
            }
        }
        return false
    }

AFTER:
    func hasSaturationResult(req ModelScalingRequest) bool {
        return req.CompositeSignal.Name == domain.SaturationAnalyzerName &&
               req.CompositeSignal.Result != nil
    }
```

---

## `composeAnalyzerResults` — NEW function (added in T1 commit `f5283e2a`, unchanged since)

```go
func composeAnalyzerResults(baseResults []rawAnalyzerResult) rawAnalyzerResult {
    for _, r := range baseResults {
        if r.name == domain.SaturationAnalyzerName {
            return r   // returns sat's raw result unchanged
        }
    }
    return baseResults[0]
}
```

Stub. When sat is the only enabled analyzer, returns sat unchanged. When other analyzers
also ran, their results are collected in `baseResults` but this function discards them and
still returns sat. The real reduce logic goes here in the next step.

---

## Summary of what changed vs what did not

| Function | Signature changed | Body changed | Notes |
|---|---|---|---|
| `runAnalyzersAndScore` | **Yes** — returns single value | **Yes** — internal slice is temporary | Core change |
| `composeAnalyzerResults` | No (new function, no prior) | — | Stub; sat passthrough |
| `collectV2ModelRequest` | No | **Yes** — `AnalyzerResults` → `CompositeSignal` | Consumes new return type |
| `hasSaturationResult` | No | **Yes** — reads `CompositeSignal` instead of slice loop | |
| `updateLivenessAndSetLive` | No | No | Called with 1-element slice; behavior unchanged |
| `detectDemandLiveness` | No | No | Never matches today; throughput entry never in slice |
| `recordAnalyzerMetrics` | No | No | Called with 1-element slice; behavior unchanged |
