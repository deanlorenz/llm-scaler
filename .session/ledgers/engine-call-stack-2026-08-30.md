# Engine call stack — single-analyzer refactor (2026-08-30)

Diff vs `main`. Covers the engine-side changes only.
Optimizer-side changes are in `optimizer-call-stack-2026-08-30.md`.

---

## 1. ModelScalingRequest contract  [CT2]

The only field on `ModelScalingRequest` that changes:

```diff
 type ModelScalingRequest struct {
     ModelID   string
     Namespace string
-    AnalyzerResults []NamedAnalyzerResult  // per-analyzer slice; sat is always first
+    CompositeSignal  NamedAnalyzerResult   // sat entry, handed directly to the optimizer
     VariantStates   []domain.VariantReplicaState
     ...
 }
```

`runAnalyzersAndScore` is **unchanged** — it still builds and returns
`[]NamedAnalyzerResult` for the full set of enabled analyzers.
`collectV2ModelRequest` passes `namedResults[0]` (sat, always first by
construction) as `CompositeSignal`. All other entries are consumed
engine-internally before this point (see §3).

---

## 2. collectV2ModelRequest  [CT2]

```diff
  namedResults, err := e.runAnalyzersAndScore(...)   // unchanged
  ...
  return &allocation.ModelScalingRequest{
      ModelID:   modelID,
      Namespace: namespace,
-     AnalyzerResults: namedResults,
+     CompositeSignal: namedResults[0],              // sat is always index 0
      VariantStates:   variantStates,
      ...
  }
```

---

## 3. What happens to namedResults before [0] is picked

Inside `runAnalyzersAndScore` (unchanged vs main):

```
runAnalyzersAndScore
  |- buildNamedResult(SaturationAnalyzerName, ...)   → namedResults[0]
  |- for each enabled non-sat analyzer:
  |     buildNamedResult(entry.name, ...)             → namedResults[1..]
  |
  |- updateLivenessAndSetLive(namedResults)           full slice — liveness for all
  |- recordAnalyzerMetrics(namedResults)              full slice — metrics for all
  |- logAnalyzerResult for each nr in namedResults    full slice — logs for all
  `- return namedResults
```

`collectV2ModelRequest` then takes `namedResults[0]`.
Nothing is discarded before liveness, metrics, and logging have run.

---

## 4. hasSaturationResult  [CT2]

Used engine-side to guard GPU usage accounting before calling the optimizer.

```diff
 func hasSaturationResult(req allocation.ModelScalingRequest) bool {
-    for _, e := range req.AnalyzerResults {
-        if e.Name == domain.SaturationAnalyzerName {
-            return e.Result != nil
-        }
-    }
-    return false
+    return req.CompositeSignal.Name == domain.SaturationAnalyzerName &&
+           req.CompositeSignal.Result != nil
 }
```

---

## 5. Nil guard on baseResult  [CT1b — separate bugfix]

```diff
  baseResult, err := e.runV2AnalysisOnly(...)
  if err != nil { return nil, err }
+ if baseResult == nil {
+     return nil, fmt.Errorf("saturation analyzer produced no result for model %s", modelID)
+ }
```

This is a pre-existing latent bug (nil baseResult → panic in `buildNamedResult`),
not structural. Included in this branch as CT1b.

---

## 6. What did NOT change vs main

- `runAnalyzersAndScore` — return type `[]NamedAnalyzerResult`, body, all callers unchanged.
- `updateLivenessAndSetLive` / `detectDemandLiveness` / `recordAnalyzerMetrics` — unchanged.
- `runV2AnalysisOnly`, `runRegisteredAnalyzer`, `buildNamedResult` — unchanged.
- `analyzerRunEntries`, `analyzerRunEntries`, `selectV2Optimizer` — unchanged.
- Scale-from-zero engine — entirely separate, not touched.

---

## 7. Sat-only equivalence

When only saturation is enabled (the default today):
`namedResults` has one entry → `namedResults[0]` = sat entry.
`CompositeSignal` = sat's `NamedAnalyzerResult`.

On the optimizer side, every previous `saturationNamedEntry(req.AnalyzerResults)`
call (which looped to find the sat entry by name) is replaced by `req.CompositeSignal`
directly. Same value, no loop.
