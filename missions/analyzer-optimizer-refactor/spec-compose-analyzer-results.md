# Spec: composing analyzer results into a single composite metric (T1)

Status: DRAFT — code already written against this seam (see "Code already applied" below);
under review, not yet confirmed as the right insertion point.

## Current call stack (verified, current HEAD — see ledger §25 for full trace)

```
Engine.optimize()                                             [engine.go:530-690]
  └─ e.optimizeV2(ctx, modelGroups, currentAllocations)        [engine.go:670, fn at 934-1079]
        for each (model, namespace) group:
          ├─ e.prepareModelData(...)                           [engine.go:1359]
          └─ e.collectV2ModelRequest(...)                      [engine_v2.go:756-792]
                │
                ├─ namedResults, err := e.runAnalyzersAndScore(...)      [engine_v2.go:768, fn at 101-177]
                │     │
                │     │  ── inside runAnalyzersAndScore ──
                │     ├─ baseResult := e.runV2AnalysisOnly(...)          [:121-122]
                │     ├─ namedResults[0] = buildNamedResult(..., "saturation", baseResult, ...)   [:152-154]
                │     ├─ for entry in e.analyzerRunEntries():            [:155-169]
                │     │     (skip saturation; skip if !config.AnalyzerEnabled(entry.name);
                │     │      else run + buildNamedResult, append)
                │     ├─ e.updateLivenessAndSetLive(ctx, ns, modelID, namedResults)   [:170]
                │     ├─ e.recordAnalyzerMetrics(namespace, modelID, namedResults)    [:171]
                │     ├─ for _, nr := range namedResults { logAnalyzerResult(...) }   [:173-175]
                │     └─ return namedResults, nil                        [:176, PRE-CHANGE]
                │
                └─ return &allocation.ModelScalingRequest{
                       ...
                       AnalyzerResults: namedResults,               [:786]
                       ...
                   }, nil
        requests = append(requests, *req)
  optimizer, constraints := e.selectV2Optimizer(ctx, requests)     [engine.go:1049]
  allDecisions := optimizer.Optimize(ctx, requests, constraints)   [engine.go:1055]
```

**Exactly 3 places in production code touch `NamedAnalyzerResult`/`AnalyzerResults`** (confirmed via
Explore agent, cross-checked node-by-node):

1. `engine_v2.go:152` — saturation entry literal, inside `runAnalyzersAndScore`
2. `engine_v2.go:818` — inside `buildNamedResult`, the shared per-entry constructor
3. `engine_v2.go:786` — `AnalyzerResults: namedResults`, inside `collectV2ModelRequest`

No other path exists today (V1 and queueing-model, which had their own separate construction sites,
were both deleted from this codebase on 2026-08-05 — see ledger §25).

## Where I made the change (already applied — under review)

I inserted a new function, `composeAnalyzerResults`, and changed `runAnalyzersAndScore`'s return
statement to call it:

```go
// engine_v2.go, inside runAnalyzersAndScore, replacing line 176:
- return namedResults, nil
+ return composeAnalyzerResults(namedResults), nil

// new function, appended after runAnalyzersAndScore:
func composeAnalyzerResults(namedResults []allocation.NamedAnalyzerResult) []allocation.NamedAnalyzerResult {
	return namedResults // no-op for now (T1: sat-only passthrough)
}
```

Plus a new test file `engine_v2_compose_test.go` asserting the no-op behavior for the sat-only and
empty cases.

## Why this placement is questionable — the gap I did not check before writing code

`runAnalyzersAndScore` does **two jobs**, not one:
- **Run** every analyzer and build each `NamedAnalyzerResult` (lines 120–169)
- **Record side effects keyed on the full per-analyzer slice** — liveness (`:170`), Prometheus
  metrics (`:171`), logging (`:173-175`) — **before** my inserted call

My change wraps only the *return value*, after those side effects already ran against the
uncomposed, full-N slice. That's arguably correct to leave alone (liveness/metrics probably *should*
stay per-analyzer even after composing, so operators can still see each analyzer's raw signal) —
but I did not verify that assumption. I also did not check:

- Whether `updateLivenessAndSetLive` or `recordAnalyzerMetrics` write anything into `namedResults`
  itself (by pointer/mutation) that a later composed, length-1 result would need to carry forward
  correctly — i.e. does composing *after* these calls silently drop something they attached.
- Whether any other caller of `runAnalyzersAndScore` besides `collectV2ModelRequest` exists and
  expects the pre-compose (full-N) shape. (`grep` shows `collectV2ModelRequest` as the only call
  site, but this was not independently re-verified as part of this spec pass — see Open questions.)
- The stale doc comment on `runAnalyzersAndScore` (lines 91–100) still says it returns "a
  per-analyzer slice" — no longer accurate once composition happens inside it. If the seam is right,
  this comment needs updating; if the seam moves, it's moot.

**The alternative seam**, not yet tried: insert the call to `composeAnalyzerResults` in
`collectV2ModelRequest` instead (right after `runAnalyzersAndScore` returns, at `engine_v2.go:768`),
leaving `runAnalyzersAndScore` itself returning the uncomposed full-N slice as it always has. This
keeps `runAnalyzersAndScore`'s existing contract/doc comment true, and makes the compose step a
visibly separate stage at the one place the slice becomes part of `ModelScalingRequest`, rather than
folding it into a function whose name and doc comment don't mention composing at all.

## Open questions (blocking confirmation of this spec)

1. Is `collectV2ModelRequest` really the only caller of `runAnalyzersAndScore`? (Grep-verify, not
   assumed.)
2. Do `updateLivenessAndSetLive` / `recordAnalyzerMetrics` need to see the pre-compose (full-N) slice,
   or would post-compose (length-1) work just as well / be more correct? This determines whether
   composing belongs *inside* `runAnalyzersAndScore` (current code) or *after* it returns (the
   alternative above).
3. Does `buildNamedResult` (site 2, `:818`) or anything it calls internally assume it's one of
   several entries being built (e.g. does it read or write anything indexed by position in a slice
   of N)? Not yet checked.

## Status

NOT CONFIRMED. Code is written and tests pass (including full non-e2e suite), but the insertion
point itself is under question per the open questions above — do not build T2 on top of this until
resolved.
