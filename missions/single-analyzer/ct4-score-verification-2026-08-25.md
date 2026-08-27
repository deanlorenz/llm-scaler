# CT4 Score verification (2026-08-25)

## Verdict: YES — safe/equivalent to bake `Score` into the composite result at compose time. It already is.

**1. `config.AnalyzerScore(name)` — pure config lookup, no runtime data.**
`internal/config/saturation_scaling.go:617-631`:
```go
func (c ScalingPolicy) AnalyzerScore(analyzerName string) float64 {
	for _, aw := range c.Analyzers {
		if aw.EffectiveType() == analyzerName {
			if aw.Score > 0 {
				return aw.Score
			}
			return 1.0
		}
	}
	return 1.0
}
```
Only walks static config (`AnalyzerScoreConfig.Score`, defaulted to 1.0). Never reads demand,
capacity, or any optimize-time value.

**2. N=1 production value: `1.0`.** `ApplyDefaults` seeds `{Name: "saturation", Score: 1.0,
Enabled: true}` when `Analyzers` is empty.

**3. Same config object, both places — timing is irrelevant.** `Score: config.AnalyzerScore(name)`
is set once at `buildNamedResult` (`engine_v2.go:858`), called from `runAnalyzersAndScore` after
`composeAnalyzerResults` collapses to the single composite (`engine_v2.go:174-177`) — i.e.
**already** at compose time, once per cycle. Neither optimizer call site (`fairShareValue`,
`greedy_score_optimizer.go:74`; `sortVariantsForScaleDown`'s `weighted` closure,
`cost_aware_optimizer.go:168`) calls `config.AnalyzerScore` or touches `config.ScalingPolicy` at
all — confirmed via search, zero references to either in either optimizer file. They only read
the pre-baked `.Score` field.

**4. No per-optimizer variation.** Both optimizers read the identical `.Score` field off the
identical `NamedAnalyzerResult` — no optimizer-specific recomputation or alternate config path.

**Conclusion:** Score's current architecture already IS "resolved once at compose time, read
verbatim downstream" — there is no "read fresh at optimizer time" behavior to preserve or move
away from. CT4 as scoped is confirmed safe; it should be stated as documenting/formalizing
existing behavior, not as a functional change.
