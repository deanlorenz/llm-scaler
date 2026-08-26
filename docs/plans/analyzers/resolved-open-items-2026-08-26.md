# Resolved — §37 open items (2026-08-26)

Resolves items 1, 2, and 4 of the ledger's §37 resume checkpoint
(`ledger-analyzer-optimizer-refactor.md`) by direct code verification, no user input
needed for these three. Item 3 (CT4) stays blocked — a real design decision, not
a code-verifiable question, unchanged from §36.

This doc is the bridge between §37's open questions and the concrete task briefs
handed to the implementing coder. It does not replace
`spec-composite-metric-and-optimizer-t2.md` — the spec still needs these findings
folded in as a v5 revision once implementation confirms them in practice.

---

## 1. CT1b — engine-side guard: confirmed mechanism, no new pattern needed

**Question from §37:** "critical" in this codebase means "abstain from a decision,"
not crash/panic — what does the existing abstain pattern actually look like, so
CT1b's Todo can be corrected to match it instead of inventing something new?

**Verified, `internal/engines/steadystate/engine.go:980-1003`:** the existing,
uniform pattern for "this model's data/analysis is unusable this cycle" is:

```go
req, err := e.collectV2ModelRequest(...)
if err != nil {
    msg := "V2 analysis failed"
    logger.Error(err, msg, "modelID", modelID)
    e.recordOptimizationFailedEvent(modelVAs, msg)
    e.emitSafetyNetMetrics(ctx, modelVAs, currentAllocations, data.scaleTargets)
    continue
}
```

Three things happen on abstain, all already wired at the call site one level above
`runAnalyzersAndScore`: a structured error log, a Kubernetes Event
(`recordOptimizationFailedEvent`), and safety-net metrics — then `continue` to the
next model. No panic anywhere in this path; the whole point of the pattern is
"skip this model, keep the loop alive for every other model."

**Consequence for CT1b:** the guard does not need a new detection mechanism. It
needs `runAnalyzersAndScore` (or `composeAnalyzerResults`) to return a non-nil
`error` for the "no saturation result" case instead of the current code, which
cannot express that case at all today — see below for why.

**Is "no saturation result" actually reachable, and what does it mean precisely?**
Traced `runAnalyzersAndScore` (`engine_v2.go:103-186`) end to end:
- `baseResult, err := e.runV2AnalysisOnly(...)` — if `err != nil`, the function
  already returns `nil, err` at line 126, which already flows into the exact
  abstain pattern above via `collectV2ModelRequest`'s own error propagation. This
  path is **already correct today** — nothing to add here.
- If `err == nil`, `baseResult` (saturation's raw `*domain.AnalyzerResult`) is
  unconditionally used to build `baseResults[0]` at line 152 — there is no
  `baseResult == nil` check. Per `spec-corrections-verification-2026-08-25.md` Q2
  (re-confirmed here by re-reading `SaturationAnalyzer.Analyze`), this analyzer
  never returns `(nil, nil)` — every early return pairs `nil` with a non-nil
  `error`. So `baseResult == nil && err == nil` cannot happen via this call
  today.
- `composeAnalyzerResults` (`engine_v2.go:207-214`) always finds the saturation
  entry by name, because line 152 always puts it there unconditionally,
  regardless of `config.AnalyzerEnabled`. So "saturation entry missing from
  `baseResults`" cannot happen either, by construction, today.

**Confirmed no path exists today that reaches CT1b's guard** — matches the spec's
own framing exactly. The guard is defensive against a future change (e.g. someone
gating saturation behind `AnalyzerEnabled` too, per §27's noted future direction)
breaking the "always present" invariant silently instead of loudly.

**Decided shape for CT1b:** add a nil-check on `baseResult` immediately after the
`runV2AnalysisOnly` call (belt-and-suspenders alongside the `err != nil` check
already there), returning a new sentinel error
(e.g. `fmt.Errorf("saturation analyzer produced no result for model %s", modelID)`)
that flows through the existing `collectV2ModelRequest` → abstain-pattern path
with zero new plumbing. No event/metric/log call needs to be added directly in
`engine_v2.go` — returning `error` from `runAnalyzersAndScore` is sufficient; the
caller one level up already does the logging/event/metric/skip for any error from
this function, uniformly, today.

**No-behavior-change verification:** since no test or production path can
currently produce `baseResult == nil` with `err == nil` (confirmed above), adding
this check has no way to change any currently-passing test's outcome — the new
`if baseResult == nil` branch is simply unreachable by every existing input.
Confirmed by construction, not just by running the suite (though the coder should
still run it as the Todo's last step, per standard practice, not as the primary
evidence).

---

## 2. CT3 — two design suggestions, evaluated

### 2a. Demand=0 as a nil-forcing guard at the optimizer usage site

**Suggestion from §37:** wrap the demand value in a function returning `nil`
(instead of the current 0-or-1 `util` value) so callers must explicitly handle
"not applicable," checked at compile time.

**Evaluated against the actual call site** (`analyzer_helpers.go:369-380`,
`allocateForModelPaired`'s `utilByRole` computation): `utilByRole` is a
`map[string]float64`, consumed immediately in the same function (line 384,
`deltaUtil` = min across roles) and never leaves `allocateForModelPaired`'s local
scope — it is not part of any exported type, struct field, or cross-function
contract. There is exactly one producer and one consumer, both inside the same
20-line block.

**Decision: do not introduce a `*float64`/wrapper type here.** The Go idiom for
"a float that might be N/A" (pointer, or a paired `bool`) earns its cost when the
value crosses a function or package boundary where a caller could plausibly
forget the check. Here it can't — the only reader is three lines below the
writer, in the same loop, in the same function. Converting `float64` to
`*float64` would require a nil-check on every read (line 378's arithmetic,
line 384's comparison) for a value that is always non-nil in practice today
(every role in the loop gets a `utilByRole[role]` entry unconditionally). This
would add allocation and nil-check overhead in a hot per-cycle loop for a
compile-time safety property that already holds by inspection.

**What to do instead, matching the codebase's existing convention:** the
`VariantCapacity.Reason` field (`ReasonNoData`, `ReasonError`,
`analyzer_helpers.go:40-47`) already encodes "why is this value not usable" as an
explicit, named, string-typed sentinel — separate from the numeric value — at the
producer level (per-variant), not the consumer level (per-role aggregate). This is
the established pattern for "distinguish N/A from a real computed value" in this
codebase. `utilByRole`'s demand=0 case already has an unambiguous meaning by
construction (per CT3b's clamp analysis: demand is `roleAggRemaining`'s output,
which is clamped ≥0 and is exactly 0 only when every contributing analyzer's
remaining capacity for that role is 0 or negative) — there is no silent-failure
mode here to guard against; it's a normal, expected value on the same footing as
any other float in the loop. **CT3a's contract should state this explicitly**
(demand=0 is a real, meaningful value — "fully covered," not an error state —
and needs no wrapper type) rather than carry the suggestion forward as
unresolved.

### 2b. A "coverage" unit type to prevent unit-mismatched combination

**Suggestion from §37:** give composite values (coverage ratios, tok/s demand,
replica counts) a distinct type per unit so `min()`/combination across
mismatched units is a compile error, not a runtime bug.

**Evaluated against the actual data flow.** Grepped every arithmetic site in
`analyzer_helpers.go`, `greedy_score_optimizer.go`, `cost_aware_optimizer.go`,
`rescale.go` that combines two `float64`s of different conceptual units (tok/s
demand vs. dimensionless coverage ratio vs. replica-count-derived ints). Found:

- Coverage ratios (`utilByRole`, `deltaUtil`) are dimensionless by construction
  (`float64(n) * prc / demand` — tok/s ÷ tok/s cancels) and are never combined
  with a raw tok/s value anywhere; `deltaUtil` only ever feeds into replica-count
  arithmetic (`kByRole`, line 392 onward) via its own dedicated variables, never
  reused as a tok/s quantity.
- Every place that reads `.Remaining`/`.Spare`/`RoleCapacities[...].RequiredCapacity`
  is consistently tok/s (or the model's native capacity unit) — confirmed via
  `domain.AnalyzerResult`'s doc comments, which already state units per field.
- No site was found today that actually mixes units incorrectly — this is a
  prospective safety net for future code, not a fix for an existing bug.

**Decision: defer, do not build now.** This is a real, reasonable idea, but it's
a cross-cutting typing change to `domain.AnalyzerResult` and every downstream
consumer (dozens of call sites), orthogonal to CT1-CT5's actual scope (which is
"stop wrapping N=1 in a slice," not "re-type every numeric field in the domain
model"). Introducing it now would multiply CT2/CT3's diff size for a benefit
that's about guarding against *future* mistakes, not fixing a *current* one.
**Recorded as a legitimate future improvement, out of scope for this spec** —
consistent with how CT5 already defers the mixed-P/D+"both" role shape for the
same reason (real idea, no current instance, no urgency, real cost to build now).

---

## 4. CT5's zero-guard semantics — surveyed all 11 sites, one clear answer

**Question from §37:** is the `prc <= 0` skip/continue pattern a genuine
allocation-eligibility decision, or just a safe division-by-zero guard that
happens to also skip allocation?

**Surveyed every `PerReplicaCapacity <= 0` / `prc <= 0` site in
`internal/engines/allocation/` (11 total, confirmed via grep, all non-test):**

| File:line | Guard action | Followed immediately by a division? |
|---|---|---|
| `analyzer_helpers.go:77` (`applyAllocation`) | `continue` | No |
| `analyzer_helpers.go:193` (`roleBottleneckReplicas`) | `continue` | Yes (line 196, `state[i][role] / prc`) |
| `analyzer_helpers.go:261` (`safeRemovalReplicasForRole`) | `continue` | (not re-read this pass — same function family, same pattern per §29/§24) |
| `analyzer_helpers.go:288` (nearby helper) | `continue` | (same family) |
| `analyzer_helpers.go:399` (`allocateForModelPaired`, `k` calc) | skips `k` assignment (`k := 0`, no division attempted) | N/A — guard prevents the division, doesn't follow one |
| `rescale.go:438, 472, 593` | `continue` | No |
| `greedy_score_optimizer.go:421` | `continue` | No |
| `cost_aware_optimizer.go:91, 121, 235` | `continue` / early return | No |

**Finding: 10 of 11 sites have no division immediately after the guard at all** —
the guard's only visible effect is removing that variant from a `continue`d loop
(a pick loop, an allocation loop, a cost-sort loop). Only one site
(`roleBottleneckReplicas`, line 196) pairs the guard with an actual division on
the very next line — there, the guard genuinely is (also) a divide-by-zero
guard, incidentally, on top of being the same eligibility gate as everywhere
else.

**Answer: this is an allocation-eligibility decision everywhere it appears, not
primarily a division guard.** The near-universal `continue` shape means "this
variant supplies zero capacity for this role/model, so it contributes nothing
and should not be considered a candidate this cycle" — which is the *correct*
allocation semantic, not an accidentally-correct side effect of avoiding a crash.
A variant with `PerReplicaCapacity <= 0` genuinely cannot serve the role (per the
user's own framing in §37: "it is possible that an SO cannot supply any demand,
especially per specific role") — skipping it from consideration is exactly right,
not a workaround.

**Corroborating evidence:** `VariantCapacity.Reason` (`ReasonNoData`,
`ReasonError`) already exists specifically to distinguish *why* a variant has
`PerReplicaCapacity <= 0` (analyzer-level) — the eligibility-skip behavior at the
consumer level (allocation) is the deliberate, designed downstream consequence of
that producer-level sentinel, not an independent guess. The two layers agree:
producer says "this variant has nothing to offer, here's why"; consumer says
"then skip it," uniformly, everywhere.

**No design gap found here.** CT5's doc-comment task can state this plainly:
`PerReplicaCapacity <= 0` is a first-class "ineligible to serve this role" signal,
consistently treated as such everywhere it's read; the one site
(`roleBottleneckReplicas`) where it also prevents a division is a coincidental
overlap, not evidence the guard exists *for* division-safety elsewhere.

---

## Summary — what this unblocks

- **CT1b**: fully specced now — a nil-check + sentinel error in
  `runAnalyzersAndScore`, no new event/metric/log plumbing needed (inherited free
  from the caller's existing abstain pattern). Ready to implement.
- **CT3a**: the demand=0 question is closed (no wrapper type; document as a
  normal value). The unit-type idea is deferred, documented as future work, not
  built now. CT3a's contract can be written and CT3b can proceed once it is.
- **CT5**: the zero-guard semantic question is answered — eligibility gate,
  confirmed correct as designed. CT5's doc-comment task can proceed as originally
  scoped in the spec, now with this confirmation attached.
- **CT4**: still and only blocked on the user's fairness-definition decision
  (§36) — nothing in this doc changes that. Not addressed here.
