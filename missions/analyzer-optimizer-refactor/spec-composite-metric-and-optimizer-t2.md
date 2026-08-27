# Spec: composite metric contract + optimizer single-analyzer simplification (T2)

Status: v5 — CT1a, CT1b, CT2, CT3a, CT3b, CT5 are implementation-ready. **CT4 remains BLOCKED**
on the user's fairness-definition decision (ledger §36) and is explicitly out of scope for the
current implementation pass. Supersedes `spec-compose-analyzer-results.md` (T1's
originally-rejected insertion point; kept for history).

This spec covers two things together, per user direction: (1) the exact contract the composite
metric must satisfy — derived from what the optimizer's own math actually needs, not invented —
and (2) the concrete optimizer-side simplification (T2) that removes the now-vacuous N>1
machinery once that contract is guaranteed.

**Grounding.** Every claim below traces to a verified finding in the ledger
(`ledger-analyzer-optimizer-refactor.md`) or one of its linked reports:
- `optimizer-call-map-2026-08-25.md` — every optimizer-side reader of `NamedAnalyzerResult`.
- `composite-entry-spec-2026-08-25.md` — field-by-field superset/gap analysis (Task A), the
  `saturationNamedEntry` → value-field fit (Task B), and the dense/sparse list audit (Task C).
- `scale-from-zero-and-fallback-trace-2026-08-25.md` — the `RoleCapacities` role-visibility gap
  trace. **Note: this report's own headline conclusion ("no demand-side fallback exists") was
  itself found to be wrong — see `spec-corrections-verification-2026-08-25.md` Q4. Read that
  correction alongside this report, not instead of it — the report's detailed evidence is
  accurate, only its top-level synthesis was wrong.**
- `spec-corrections-verification-2026-08-25.md` — verification of 4 user corrections to this
  spec's first draft (CT1 partial-scale-from-zero, CT2 reachability of `Result == nil`, CT3's
  two non-unconditional "no-ops," CT5's `estimateSchedulerQueueDemand` correction).
- `ct4-score-verification-2026-08-25.md` — confirms `Score` is already compose-time-resolved.

Do not restate those reports' content here beyond what's needed to justify a task's Todo — read
them directly for full detail.

**Standing design rule (applies to every task below):** any fallback/degenerate path that the
optimizer's code takes must resolve to a real value, never propagate `nil` further downstream.
`Result == nil` is acceptable as an internal, checkable state on the composite value type — it is
not acceptable for a helper function to hand a `nil`-shaped result to its own caller without an
explicit, intentional "no-op for this model" contract (see CT1).

---

## Task list

### CT1 — Fix the two unguarded nil-derefs in `rescale.go`

**Intent.** `rescaleModelDecisions` (`rescale.go:344-345`) and its callees `modelDemandGPUs`/
`roleDemandGPUs` (`rescale.go:572-608`) dereference `satNamed.Result` with no local nil-check,
relying on an implicit cross-function invariant from an upstream filter (`recordsForRequest`).
This is safe today only because nothing has changed the calling structure yet. Since every other
task below touches this exact area, fix the fragile spot first, independent of and before the
larger restructuring, so later tasks build on solid ground rather than inheriting a latent panic.

**Verified before writing the fix** (`spec-corrections-verification-2026-08-25.md` Q1): the
concern that "return empty on missing saturation entry" could silently block a *partial*
scale-from-zero case (one variant of a role live, another variant of the same role at zero
replicas, combined role demand exceeds the live variant alone) does **not** apply here.
`rescaleModelDecisions`'s only caller (`applyRescale`) plain-appends its result — an empty return
for one model is an isolated no-op, doesn't affect other models. And the partial-scale-from-zero
scenario itself is already handled correctly by existing, unrelated machinery (`AggregateByRole`
sums demand across all variants of a role regardless of replica count; both optimizers' pick
logic iterates the full variant set for a role with no replica-count gate) — this nil-check
operates at whole-model granularity and never interacts with that per-variant logic. The fix is
safe exactly as originally scoped.

**Expected outcome(s).** `rescaleModelDecisions` has an explicit nil-check on `satNamed` before
dereferencing it (matching the pattern already used correctly at `rescale.go:525`
`rescaleInputsForGroup`). Verified by: a new test that calls `rescaleModelDecisions` (or its
public entry point) with a request whose composite has no saturation-named entry, and asserts it
returns gracefully (no panic) rather than crashing.

**CT1b — engine-side guard, mechanism now confirmed** (`resolved-open-items-2026-08-26.md` §1).
User: "still feels like nil sat is a critical error that should be caught on engine side" —
corrected once more: "critical" in this codebase's own vocabulary means "abstain from a decision,"
not crash/panic. The existing, uniform pattern for exactly this (`engine.go:980-1003`) is: caller
logs the error, records a `recordOptimizationFailedEvent`, emits safety-net metrics, and
`continue`s to the next model — all triggered by `collectV2ModelRequest` returning a non-nil
`error`. **CT1b does not need any new event/metric/log code in `engine_v2.go`** — it needs
`runAnalyzersAndScore` to return a new sentinel error when `baseResult == nil` despite `err ==
nil` from `runV2AnalysisOnly`; the existing caller-side abstain pattern handles the rest for free.
**Verified unreachable today:** traced `SaturationAnalyzer.Analyze` — it never returns `(nil,
nil)`, and `composeAnalyzerResults` always finds saturation by name since it's unconditionally
prepended. So this guard cannot fire on any current input; adding it changes no existing test's
outcome by construction (not just by re-running the suite, though that remains the last Todo
step).

**Todo.**
- [ ] CT1a (optimizer-side, defensive): add `if satNamed == nil || satNamed.Result == nil {
  return nil }` (or the function's appropriate empty-result equivalent) at the top of
  `rescaleModelDecisions`, immediately after the `saturationNamedEntry` call
- [ ] CT1a: add a regression test exercising the previously-unguarded path
- [ ] CT1b (engine-side): in `runAnalyzersAndScore` (`engine_v2.go`, right after the
  `runV2AnalysisOnly` call at line ~123), add `if baseResult == nil { return nil,
  fmt.Errorf("saturation analyzer produced no result for model %s", modelID) }`. No new
  event/metric/log call needed — this error return already flows through
  `collectV2ModelRequest` into the existing abstain pattern (`engine.go:980-1003`: log + Event +
  safety-net metrics + `continue`), unchanged.
- [ ] CT1b: add a unit test on `runAnalyzersAndScore` (or the smallest testable seam around it)
  that forces `baseResult == nil` with `err == nil` and asserts the sentinel error is returned —
  since this path is not reachable via the real analyzer, the test needs a fake/stub that
  violates the `Analyze` contract deliberately, to prove the guard itself works in isolation
- [ ] Run full existing test suite — zero regressions expected (verified by construction in
  `resolved-open-items-2026-08-26.md` §1: no current input can produce `baseResult == nil` with
  `err == nil`, so no existing test's outcome changes)

**Refs.**
*Reads:* `internal/engines/allocation/rescale.go` (`rescaleModelDecisions`, `modelDemandGPUs`,
`roleDemandGPUs`), `docs/plans/analyzers/composite-entry-spec-2026-08-25.md` (Task B.2 item 3),
`docs/plans/analyzers/spec-corrections-verification-2026-08-25.md` (Q1, Q2)
*Writes:* `internal/engines/allocation/rescale.go`, a new/extended `rescale_test.go`,
`internal/engines/steadystate/engine_v2.go` (CT1b's new guard)

**Status.** DONE.
- CT1a: DONE 2026-08-26. Commit `8906ef7b`. Nil-guard added to `rescaleModelDecisions`,
  regression test added. See `ct1a-implementation-report-2026-08-26.md`.
- CT1b: DONE 2026-08-27. Commit `122d1699`. Nil-guard added to `runAnalyzersAndScore`, one new
  test added. See `ct1b-implementation-report-2026-08-26.md`.

---

### CT2 — Replace `AnalyzerResults []NamedAnalyzerResult` with a guaranteed single `CompositeSignal` field

**Intent.** Per `composite-entry-spec-2026-08-25.md` Task B, `saturationNamedEntry`'s
linear-search-by-name is already vestigial in production (T1 guarantees length-1,
always-named-saturation). **Confirmed by re-reading T1's actual current code
(`engine_v2.go:148-214`): T1 already builds the single `NamedAnalyzerResult` from the composed
raw result — `runAnalyzersAndScore` already returns a length-1 slice today.** So this task is
narrower than it first appears: it does not need to construct the single entry (already done);
it needs to stop wrapping that already-single entry in a slice type, and stop every consumer
searching for it by name instead of holding it directly.

**Naming:** user corrected `Composite` to **`CompositeSignal`** — more descriptive, avoids reading
as an adjective with no noun. Used throughout below.

**On `Result == nil`:** verified (`spec-corrections-verification-2026-08-25.md` Q2) this is *not*
a reachable production state today — every path either bails with an error before `Result` would
be nil, or filters nil results before composition. It remains a legitimate defensive/checkable
state on the type (per the standing design rule above), not something expected to occur in
practice — do not read its presence in the struct as evidence it commonly happens.

**Expected outcome(s).** `ModelScalingRequest.AnalyzerResults []NamedAnalyzerResult` is replaced
with `ModelScalingRequest.CompositeSignal NamedAnalyzerResult` (value, not pointer). All 6 call
sites identified in `composite-entry-spec-2026-08-25.md` Task B.3 are updated per their
documented minimal diff. `saturationNamedEntry` is deleted (no longer needed). Verified by: full
test suite passes; no remaining reference to `saturationNamedEntry` or `AnalyzerResults` in
`internal/engines/allocation/` or `internal/engines/steadystate/`.

**Todo.**
- [ ] Change the field on `ModelScalingRequest` from `AnalyzerResults []NamedAnalyzerResult` to
  `CompositeSignal NamedAnalyzerResult`
- [ ] Update `internal/engines/steadystate/engine_v2.go` — `runAnalyzersAndScore` currently
  returns `[]allocation.NamedAnalyzerResult` (a length-1 slice); change its return type to a bare
  `allocation.NamedAnalyzerResult`, dropping the slice-literal wrapper at line 175. Update
  `collectV2ModelRequest` to assign `CompositeSignal: namedResult` directly (no `[0]` indexing)
- [ ] Update `variant_records.go:recordsForRequest` per Task B.3 row 1
- [ ] Update `cost_aware_optimizer.go:buildDecisionsWithOptimizer` per Task B.3 row 2
- [ ] Update `rescale.go:rescaleModelDecisions`, `rescaleInputsForGroup` per Task B.3 rows 3-4
- [ ] Update `rescale.go:modelDemandGPUs`, `roleDemandGPUs` signatures (`*NamedAnalyzerResult` →
  `NamedAnalyzerResult`) per Task B.3 rows 5-6
- [ ] Delete `saturationNamedEntry` (`analyzer_helpers.go:87-101`)
- [ ] Update the stale doc comment at `optimizer_interfaces.go:75` ("saturation entry is always
  first" → state the real, current guarantee)
- [ ] Update `hasSaturationResult` (`engine_v2.go:745-752`, the steadystate-side duplicate lookup
  used by GPU-quota accounting) to read `req.CompositeSignal.Name ==
  domain.SaturationAnalyzerName` directly instead of searching
- [ ] Update `updateLivenessAndSetLive`, `recordAnalyzerMetrics`, `logAnalyzerResult`
  (`engine_v2.go`) — currently take `[]allocation.NamedAnalyzerResult`; decide whether to keep
  the slice signature (call with a length-1 literal at the call site) or change to take a single
  value — either works, pick whichever is less churn during implementation
- [ ] Run full existing test suite — zero regressions expected

**Refs.**
*Reads:* `docs/plans/analyzers/composite-entry-spec-2026-08-25.md` (Task B, full call-site table),
`docs/plans/analyzers/spec-corrections-verification-2026-08-25.md` (Q2)
*Writes:* `internal/engines/allocation/optimizer_interfaces.go`,
`internal/engines/allocation/analyzer_helpers.go`, `internal/engines/allocation/rescale.go`,
`internal/engines/allocation/cost_aware_optimizer.go`,
`internal/engines/allocation/variant_records.go`,
`internal/engines/steadystate/engine_v2.go`, and every test file referencing the old shape

**Status.** NOT STARTED. Depends on CT1 landing first (touches the same functions).

---

### CT3 — Design the engine-side reduce, THEN simplify the 7 now-single-entry helper functions

**Intent — corrected scope, per user direction.** These 7 functions
(`applyAllocation`, `initRoleState`, `roleBottleneckReplicas`, `roleAggRemaining`,
`safeRemovalReplicasForRole`, `needsScaleDownForRole`, `applyDeallocationForRole`) are not
optimizer scaffolding to delete — **their math IS the specification for what any future
engine-side multi-analyzer reduce must compute.** Before removing any of them, we must (1)
precisely verify each is a true no-op for today's N=1 case (not "probably"), and (2) design the
actual engine-side composite-creation reduce that will eventually replace the general N-analyzer
case — using the *same* reduction logic these functions already encode — and confirm every
downstream field access agrees with that one reduction. **If a future engine-side reduce and
these optimizer-side functions computed the same field two different ways, that's a bug waiting
to happen — not "one composite pre-reduced field is not good enough."** This design work must
happen before any removal, not after.

**Verified precisely (`spec-corrections-verification-2026-08-25.md` Q3) — not all 9 are
unconditional no-ops:**

| Function | No-op for N=1? |
|---|---|
| `initRoleState` | Yes, exact |
| `roleBottleneckReplicas` | Yes, exact (max-of-one is harmless; the term is always ≥0 by construction) |
| `roleAggRemaining` | **NO — see below** |
| `safeRemovalReplicasForRole` | Yes, exact |
| `needsScaleDownForRole` | Yes, exact |
| `applyAllocation` | Yes, exact |
| `applyDeallocationForRole` | Yes, exact |
| `fairShareValue` (primary branch) | Yes, exact |
| `fairShareValue` (fallback branch) | **NO — same issue as `roleAggRemaining`** |
| `sortVariantsForScaleDown`'s `weighted` closure | Yes, exact, no caveat |

**The two exceptions, precisely — CORRECTED per user's re-read of the code:** `roleAggRemaining`
computes `max(0.0, max_i state[i][role])` — **two separate operations**, not one: (1) reduce
*across analyzers* via `max_i`, and (2) clamp the result to ≥0 via the `0.0`-seeded accumulator.
Operation (1) is the one that becomes a genuine no-op at N=1 (max of one term is that term).
**Operation (2), the `max(0.0, ...)` clamp, is completely independent of analyzer count** — it
exists regardless of whether there's 1 or 10 analyzers, and must be **kept in the optimizer-side
simplified function**, not treated as part of "the reduce we're removing." `fairShareValue`'s
fallback branch has the identical two-part structure, with an *additional*, separate `max` over
`roles` (picking the highest-demand role) that has nothing to do with analyzer count at all and
was never in question. **Correction to the original spec draft:** these are not "not proven
no-ops" in the sense of needing a design decision — the analyzer-count reduction in both is a
proven no-op; the clamp is simply orthogonal to N and stays exactly as-is. No design ambiguity
here after this correction — CT3a's contract below should state this precisely (which part is
the N-reduce being simplified away, which part is an independent invariant that survives
unchanged) so it isn't miscategorized again.

**Second correction, from the CT4 investigation (`score-and-priority-semantics-2026-08-25.md`):**
CT3a's contract must NOT use Score-weighted averaging to combine disagreeing analyzers' per-model
values into the composite — that pattern is validated today only for the cross-*model* fair-share
scalar (`fairShareValue`, ranking different ScaledObjects against each other), never for
combining disagreeing analyzers' opinions *within* one model. Where the existing code already
combines multiple analyzers' per-model/per-role values, it always uses **max** (scale-up path:
`roleBottleneckReplicas`, `roleAggRemaining`) or **min** (scale-down path:
`safeRemovalReplicasForRole`) — never weighted-sum-as-a-composite-quantity. CT3a's design must
follow this existing precedent (max/min per field, matching each field's own existing reduction),
not invent Score-weighted blending by loose analogy with `fairShareValue`'s unrelated cross-model
use of Score.

**Note on the "demand=0" semantic** in the existing `utilByRole`/`deltaUtil` logic
(`allocateForModelPaired`, `analyzer_helpers.go:370-390`): it treats zero demand as `util = 1.0`
("fully covered") rather than "not applicable." User confirmed the practical outcome is identical
either way (no allocation attempted) — this is a wording difference, not a behavior
discrepancy, and needs no coordination or decision. Preserve as-is in CT3b's simplification.

**Resolved: no nil-forcing wrapper type for demand=0** (`resolved-open-items-2026-08-26.md` §2a).
`utilByRole` never leaves `allocateForModelPaired`'s local scope — one producer, one consumer,
three lines apart, same function. A `*float64`/wrapper would add nil-checks on every read for a
safety property that already holds by inspection. demand=0 is a normal, meaningful value (per the
paragraph above), not a silent-failure state needing a distinct type. Not adopted; CT3a's contract
states this directly rather than carrying it forward as an open question.

**Resolved: coverage/demand unit-typing deferred, not built now** (`resolved-open-items-2026-08-26.md`
§2b). Surveyed every arithmetic site combining values across `analyzer_helpers.go`,
`greedy_score_optimizer.go`, `cost_aware_optimizer.go`, `rescale.go` — no current unit-mismatch bug
found (coverage ratios are dimensionless by construction and never combined with raw tok/s
values). A real idea, but cross-cutting (re-types `domain.AnalyzerResult` and dozens of call
sites) and orthogonal to this spec's scope. Documented as a future improvement, same treatment as
the mixed-P/D+"both" role deferral below — not implemented in CT1-CT5.

**Mixed P/D+"both" role shape — deferred, confirmed zero-risk to defer.** User raised whether a
model can have prefill/decode roles AND a "both" role simultaneously (their coverage formula
implied it: `min(coverage(p), coverage(d)) + coverage(both)`). Verified
(`docs/plans/analyzers/ledger-analyzer-optimizer-refactor.md` §34's follow-up): the current
mutual exclusivity in `initRoleState` (a model's roles are either `⊆ {prefill, decode}` or
exactly `[both]`, never mixed) is a **shallow, local implementation choice**, not a deep
constraint — `RoleDemand`/`RoleCapacities` are plain generic maps with no ≤2-key assumption
anywhere in the aggregation/engine layer; a genuine 3-key map would flow through `initRoleState`'s
existing `if` branch with zero code change. **User's decision: not aware of a real deployment
needing this; out of scope for this mission; document as a future requirement.** CT3a's contract
should note this explicitly so a future reduce-design doesn't need to re-discover it.

**Expected outcome(s), revised into two ordered sub-parts:**

**CT3a — Design the engine-side reduce contract (must land first).** A written contract
(function signatures, or at minimum documented semantics per field) for how a future
multi-analyzer engine-side reduce will compute the composite's `RoleCapacities`, `Remaining`,
`Spare`, `RoleSpare`, `Live` fields — using the exact same max/min/all-agree/sum logic these 7
functions (and the 2 weighted-sum sites) already encode, so there is provably one true way to
reduce each field, not two. This does not need to be implemented for N>1 yet (§5's deferred
combining-rule semantics still govern *when* N>1 actually happens) — it needs to exist as a
written, reviewed contract so CT3b's simplification can point at it and know it's simplifying
*toward* the right target, not just deleting code.

**CT3b — Simplify the 7 functions, referencing CT3a's contract.** Each function takes a single
`NamedAnalyzerResult` (or the relevant piece of it) instead of a slice, loop removed, arithmetic
preserved exactly — including the non-negativity clamp on `roleAggRemaining`/`fairShareValue`'s
fallback, made explicit via a comment pointing at CT3a's contract rather than silently dropped.
`initRoleState`'s `RolePairedState []map[string]float64` becomes a single `map[string]float64`.
Verified by: full test suite passes with identical numeric results (a golden-value test comparing
pre/post outputs for the same input, since `optimizer_equivalence_test.go` may no longer apply
post-CT2).

**Todo.**
- [ ] **CT3a first:** write the engine-side reduce contract — for each of `RoleCapacities`,
  `Remaining`, `Spare`, `RoleSpare`, `Live`, state precisely what a multi-analyzer reduce must
  compute, **using max/min (matching each field's existing precedent in the 7 functions), never
  Score-weighted averaging** — see the second correction above
- [ ] CT3a: state where the non-negativity invariant (the `max(0.0, ...)` clamp) is enforced and
  by whom — independent of, and unaffected by, the analyzer-count reduction
- [ ] CT3a: note the mixed-P/D+"both" deferral explicitly (confirmed zero-risk to defer, see
  above) so it isn't re-litigated by a future reader
- [ ] Get CT3a's contract reviewed/confirmed before starting CT3b
- [ ] CT3b: simplify `initRoleState` — drop the analyzer-index dimension
- [ ] CT3b: simplify `roleBottleneckReplicas` — single term, no `max_i` framing needed (already
  proven exact no-op)
- [ ] CT3b: simplify `roleAggRemaining` — drop the now-trivial `max_i` (analyzer-count reduce),
  KEEP the `max(0.0, ...)` clamp as-is (it's an independent non-negativity guard, unrelated to
  analyzer count — see the correction above)
- [ ] CT3b: simplify `safeRemovalReplicasForRole` — becomes "is this entry live, and if so its
  floor(RoleSpare/PRC)" — document that this makes "no other analyzer to fall back on if
  saturation is stale" (§29/§30) an explicit property of the code, not just emergent
- [ ] CT3b: simplify `needsScaleDownForRole` — same documentation note as above
- [ ] CT3b: simplify `applyAllocation`/`applyDeallocationForRole` — drop the loop, mutate directly
- [ ] CT3b: update every call site in both optimizer files
- [ ] Run full existing test suite — zero regressions, verify numeric equivalence explicitly

**Refs.**
*Reads:* `docs/plans/analyzers/optimizer-call-map-2026-08-25.md` (Sec1, Sec3),
`docs/plans/analyzers/spec-corrections-verification-2026-08-25.md` (Q3),
`docs/plans/analyzers/ledger-analyzer-optimizer-refactor.md` §29 (risk #3), §5 (deferred
combining-rule semantics — CT3a's contract must not contradict whatever gets decided there)
*Writes:* CT3a: a new section in this spec or a standalone contract doc (TBD during
implementation); CT3b: `internal/engines/allocation/analyzer_helpers.go` and its test file,
`internal/engines/allocation/cost_aware_optimizer.go`,
`internal/engines/allocation/greedy_score_optimizer.go`

**Status.** NOT STARTED. CT3a depends on CT2; CT3b depends on CT3a being reviewed/confirmed, not
just written.

---

### CT4 — Simplify the two Score-weighted aggregation sites; Score/Priority disambiguated

**What "Score" and "Priority" actually mean — established this pass, not previously known
precisely** (`docs/plans/analyzers/score-and-priority-semantics-2026-08-25.md`):
- **`Score`** = a per-**analyzer** weight (trust), config field `AnalyzerScoreConfig.Score`
  (doc-confirmed: "configures an individual analyzer's weight in the composite scoring
  function"). Used only inside `fairShareValue` and `sortVariantsForScaleDown`'s tie-break. **It
  is never a per-model/per-ScaledObject field** — this was the ambiguity the user flagged
  ("explain what is being scored? ... sorting SOs...or per analyzer?").
- **`Priority`** = a distinct, separate field — the per-**model**/per-SO fairness weight across
  *different* models competing for GPU budget (`ModelScalingRequest.Priority`, from
  `config.ScalingPolicy.Priority`, doc-confirmed "multiplier for this model's scaling urgency").
  Score and Priority are two different axes (analyzer-trust vs. model-fairness), not the same
  concept under two names.
- **`fsv` (fair-share value) is NOT a sort-key-only quantity** — its magnitude is used
  arithmetically: it becomes a real GPU-allocation budget (`target := w.remaining - mean`) that
  directly bounds replica counts. So the user's conditional ("if this is just sort between SOs
  then no need for score at all") does not resolve to "drop Score" — `fsv`'s absolute value
  matters, and `Score` is one of its real inputs, not decoration.
- **Critical finding directly confirming the user's own instinct:** *"weighted sum probably OK
  for sort order, not sure if OK for composite value."* **Confirmed correct.** Weighted-sum-
  across-analyzers is validated today only for this cross-*model* fair-share scalar — never for
  combining disagreeing analyzers' per-model/per-variant replica-count estimates into one number
  (that's max/min, per CT3's second correction above). This task's two functions
  (`fairShareValue`, `sortVariantsForScaleDown`) are the *only* legitimate uses of Score-weighted
  summation in this codebase, and they operate on the cross-model axis, not the
  composite-construction axis. **Nothing about CT4 licenses using Score-weighting inside CT3a's
  composite-reduce design** — the two are unrelated despite both mentioning "Score."

**Intent (arithmetic simplification, unchanged from before this correction pass).**
`fairShareValue` and `sortVariantsForScaleDown`'s `weighted` closure both compute `Σ_i Score_i ×
X_i` across analyzers. With exactly one entry, this is `Score × X` — no summation needed.
Separately (`ct4-score-verification-2026-08-25.md`): `Score` is already resolved exactly once,
engine-side, at compose time — `config.AnalyzerScore(name)` is a pure static config lookup, baked
into the struct inside `buildNamedResult` (which runs after `composeAnalyzerResults`). Neither
optimizer reads config directly; both only read the pre-baked `.Score` field. This is not a
change to make — it's already the architecture; this task documents it.

**Expected outcome(s).** Both functions take a single `Score`/`NamedAnalyzerResult` and compute
the un-summed product directly. `fairShareValue`'s fallback branch drops its redundant second
full-slice walk, preserving the non-negativity clamp per CT3a's contract. Doc comments state the
single-analyzer form, `Score` vs. `Priority`'s distinct meanings, and `Score`'s compose-time
resolution. Verified by: full test suite passes with identical numeric results.

**Todo.**
- [ ] Simplify `fairShareValue` — single term, no `Σ_i`; simplify the zero-fallback branch,
  preserving the clamp per CT3a
- [ ] Simplify `sortVariantsForScaleDown`'s `weighted` closure — single term
- [ ] Add/update doc comments on both: state the single-analyzer form; state that `Score`
  (per-analyzer) and `Priority` (per-model) are distinct axes, not interchangeable; note `Score`'s
  compose-time resolution
- [ ] Run full existing test suite — zero regressions expected

**Refs.**
*Reads:* `docs/plans/analyzers/optimizer-call-map-2026-08-25.md` (Sec3 items 8-9, Sec4),
`docs/plans/analyzers/ct4-score-verification-2026-08-25.md`,
`docs/plans/analyzers/score-and-priority-semantics-2026-08-25.md`
*Writes:* `internal/engines/allocation/greedy_score_optimizer.go`,
`internal/engines/allocation/cost_aware_optimizer.go`

**Status.** NOT STARTED. Depends on CT2 and CT3a's contract (for the fallback-branch clamp); can
otherwise run in parallel with CT3b.

---

### CT5 — Document the `RoleCapacities` role-visibility contract, CORRECTED for the real demand-fallback mechanism

**Intent, corrected.** Per §30/§31 (ledger), `initRoleState`'s role set is derived from
`RoleCapacities`'s own map keys, not cross-checked against discovery's dense role set. A role
with real variants but no analyzer-attributed demand is permanently invisible to scale-up until
the analyzer starts attributing demand to it. **§31's original characterization of the existing
fallback machinery was wrong and has been corrected** (`spec-corrections-verification-2026-08-25.md`
Q4) — there IS an existing, purpose-built demand-side estimator for zero-replica roles:
`estimateSchedulerQueueDemand` (`internal/engines/analyzers/saturation_v2/analyzer.go:723-767`).
It is a separate function from the `CapacityKnowledgeStore` supply ladder §31 correctly traced,
and it estimates genuine, nonzero *demand* (not supply) for a zero-replica role, from EPP
queue-depth signals blended with the model's other-role live-replica token-shape averages — this
flows through to a real, nonzero `RequiredCapacity` that *does* trigger scale-up for that role,
sized from queue evidence alone, before any replica of that role exists.

**This task does not attempt to fix a gap that turned out to already be (partially) fixed.**
What remains a genuine, unaddressed gap, narrower than originally scoped:
1. No EPP queue signal available (`SchedulerQueue == nil` or empty) — the estimator returns
   all-zeros, same failure mode as originally described.
2. The role is missing from `activeRoles` entirely because no `VariantCapacity` exists for it at
   all — a discovery-side omission upstream of the analyzer, which no mechanism (capacity store
   or queue estimator) can help with, since both are keyed/iterated from the same
   `VariantStates`/`variantCapacities` list that would already be missing the entry.

This task's job is to document the *corrected, narrower* contract precisely — not the originally
overstated one — so a future compose implementation and whoever eventually decides whether to
close the remaining gap has the accurate picture, not the wrong one from the first-draft report.

**Standing design rule (restated from this spec's header, applies here specifically):** any
fallback path built for the remaining gap (if one is ever built) must resolve to a real, usable
value — never `nil` — or it will break every optimizer-side consumer that assumes a checkable-but-
present composite value (per CT2's `Result == nil` framing).

**Domain-math grounding added this pass** (`docs/plans/analyzers/coverage-math-and-zero-guards-2026-08-25.md`)
— precise mapping of the user's "coverage" framing onto real code, since this task and CT3a both
touch the same underlying logic and must agree:
- **PRC is confirmed `(variant, role, model)`-scoped** — `domain.VariantCapacity` has both `Role`
  and `PerReplicaCapacity` on the same struct, exactly as expected.
- **"Coverage" (`replicas × PRC / Demand`) already exists**, under the name `utilByRole`
  (`analyzer_helpers.go:370-380`) — not a new concept to invent, an existing one to name precisely.
- **`min(coverage(p), coverage(d))` already exists**, named `deltaUtil`
  (`analyzer_helpers.go:382-390`).
- **The `+ coverage(both)` additive term does not exist and is structurally impossible today** —
  `initRoleState` makes a model's roles either `⊆ {prefill, decode}` or exactly `[both]`, never
  both. **User's decision (confirmed): out of scope for this mission, document as a future
  requirement.** Separately verified: this exclusivity is a shallow, local implementation choice
  (generic maps, no type-level ≤2-key assumption anywhere in the aggregation/engine layer) — a
  future task could lift it without disturbing anything CT1-CT5 build now. Zero risk to defer.
- **Zero-guard finding: no live 0/0 (`NaN`) risk exists anywhere** — every PRC-divisor site
  guards `prc <= 0` before dividing, which fires before demand's value is even considered.
  Current code sets `demand<=0 → util=1.0` ("fully covered"). This differs in *wording* from "not
  applicable" but **not in outcome** — user confirmed: no allocation is attempted either way,
  which is the only thing that matters here. Not a live discrepancy, not something CT3a/CT5 need
  to coordinate on — the practical behavior is already settled and correct. No further action
  needed on this point.
- **No precedent anywhere for "compose emits `PRC=coverage, Demand=100%`"** — the architecture's
  stated invariant is the opposite: keep D and P separate, never alias one into the other. This
  specific proposed normalization (packaging a ratio as a fake PRC/Demand pair) does not match
  existing design and would need its own justification if pursued — not adopted here by default.
- **Resolved: `PerReplicaCapacity <= 0` is an eligibility gate, not a division guard**
  (`resolved-open-items-2026-08-26.md` §4). Surveyed all 11 sites in `internal/engines/allocation/`
  — 10 of 11 have no division nearby at all; every site's guard action is `continue`/skip,
  meaning "this variant supplies zero capacity for this role, exclude it from consideration this
  cycle." Only `roleBottleneckReplicas` (`analyzer_helpers.go:196`) also prevents a division on
  the next line — a coincidental overlap there, not evidence the pattern exists *for*
  division-safety elsewhere. Corroborated by the existing `VariantCapacity.Reason` sentinel
  (`ReasonNoData`/`ReasonError`, `analyzer_helpers.go:40-47`), which already encodes "why is this
  variant's capacity unusable" at the producer level — the consumer-level skip is the designed
  consequence of that signal, not an independent guess. No design gap here; CT5's doc comment
  states this as a confirmed, correct-by-design contract.

**Expected outcome(s).** A doc comment on `initRoleState` (or wherever compose's role-derivation
logic eventually lives) states plainly: (a) roles are derived from the composite result's own
`RoleCapacities` keys; (b) a queue-driven demand estimate (`estimateSchedulerQueueDemand`) already
covers the ordinary zero-replica-role case when a queue signal exists and the model is
disaggregated; (c) the remaining gap is specifically no-queue-signal and discovery-side omission,
not "no demand fallback exists at all." Cross-reference to this spec doc for the full trace.

**Todo.**
- [ ] Add the corrected doc comment described above to `initRoleState` (post-CT3b, or pre-CT3b if
  this lands first — don't lose the comment across the CT3 rewrite)
- [ ] Note the mixed-P/D+"both" deferral in the same comment (confirmed zero-risk to defer per
  the follow-up investigation) so both gaps are documented together, not separately
- [ ] Cross-link this spec section from that comment (reference by file path / section title, not
  ledger section numbers, since the ledger is append-only and numbers are stable but a code
  comment shouldn't assume the reader has the ledger open)
- [ ] Correct `scale-from-zero-and-fallback-trace-2026-08-25.md`'s own headline conclusion (or add
  a visible errata note at its top) so a future reader of that report alone doesn't inherit its
  original wrong synthesis — it is currently linked from multiple places in this spec and the
  ledger with a "read the correction alongside it" caveat, which is a workable but imperfect fix

**Refs.**
*Reads:* `docs/plans/analyzers/ledger-analyzer-optimizer-refactor.md` (§30, §31, §33, §34),
`docs/plans/analyzers/scale-from-zero-and-fallback-trace-2026-08-25.md`,
`docs/plans/analyzers/spec-corrections-verification-2026-08-25.md` (Q4),
`docs/plans/analyzers/coverage-math-and-zero-guards-2026-08-25.md`
*Writes:* `internal/engines/allocation/analyzer_helpers.go` (doc comment only),
possibly an errata addition to `scale-from-zero-and-fallback-trace-2026-08-25.md`

**Status.** NOT STARTED. Independent of CT1-CT4 — can land any time.

---

## Explicitly out of scope for this spec

- **Actually closing the remaining, narrower `RoleCapacities` role-visibility gap** (no-queue-
  signal case, and discovery-side omission). This is a real design decision, not a mechanical
  simplification — deferred, per CT5's corrected scope.
- **Mixed prefill/decode + "both" role support** (a model with disaggregated roles AND a "both"
  role simultaneously) — confirmed a real-but-currently-unneeded deployment shape; confirmed
  zero-risk to defer (shallow implementation gap, not a deep type constraint). Document as a
  future requirement in CT5's doc comment; do not implement now.
- **§5's deferred combining-rule semantics** (what "compose" does once there is genuinely more
  than one analyzer's raw result to reduce, and saturation's scale-from-zero fallback role within
  that). T1/T2 as scoped here only ever handle the saturation-only case; the real multi-input
  reduction logic is future work, explicitly deferred by the user in §5. **CT3a's engine-side
  reduce contract is preparatory groundwork for that future work — it documents how the reduce
  must behave (max/min per field, per CT4's finding — never Score-weighted averaging), it does
  not implement the N>1 case itself.**
- **`RoleDemand`'s dead status** (never read downstream, per Task A.3) — no action needed, noted
  for awareness only.

## Revision history

- **v1** (this session, first draft): CT1-CT5 as originally scoped.
- **v2** (after first user correction pass, ledger §33): CT1 confidence updated (partial
  scale-from-zero already handled correctly, no design change needed); CT2 narrowed (T1 already
  builds the single entry; renamed `Composite` → `CompositeSignal`; clarified `Result == nil`
  reachability); CT3 restructured into CT3a (design the engine-side reduce contract first) + CT3b
  (simplify, referencing CT3a) and two functions' "no-op" status corrected (not unconditional —
  depend on an external non-negativity invariant); CT4 confirmed already-true (`Score` is already
  compose-time-resolved, not a change to make); CT5 corrected (a real demand-side fallback,
  `estimateSchedulerQueueDemand`, does exist — the original claim that no such mechanism exists
  anywhere was wrong; gap re-scoped narrower).
- **v3** (after second user correction pass, ledger §34): CT1 gained a second sub-task (CT1b,
  engine-side critical-error detection for "no saturation result," distinct from CT1a's
  optimizer-side defensive fix). CT3's clamp analysis corrected — the analyzer-count reduce and
  the non-negativity clamp are two independent operations, not one conflated caveat; also gained a
  design rule from CT4's investigation (max/min per field, never Score-weighted averaging). CT4
  gained a full Score-vs-Priority disambiguation (two distinct axes: per-analyzer trust vs.
  per-model fairness) and confirmed `fsv` is not sort-key-only. CT5 gained precise domain-math
  grounding (PRC/coverage/`deltaUtil` already exist under different names; the `+coverage(both)`
  additive term confirmed structurally impossible today but zero-risk to defer).
- **v4** (this session): removed the demand=0 "coordination requirement" between CT3a/CT5 — user
  clarified the outcome (no allocation attempted) is identical either way, so "fully covered" vs.
  "not applicable" is a wording difference, not a live discrepancy needing a joint decision.
- **v5** (`resolved-open-items-2026-08-26.md`): resolved the three code-verifiable items from
  ledger §37 (CT4's item excluded, still blocked). CT1b's mechanism confirmed and simplified — no
  new event/metric/log code needed, just a sentinel error that flows through the existing
  model-abstain pattern; confirmed unreachable by any current input. CT3a's two open design
  suggestions resolved: no nil-forcing wrapper for demand=0 (value never leaves its producing
  function), coverage/demand unit-typing deferred as future work (no current bug, high cost,
  out of this spec's scope). CT5 gained a full 11-site survey confirming `PerReplicaCapacity <= 0`
  is a designed eligibility gate, not a division-safety guard — corroborated by the existing
  `Reason` sentinel field. **This spec's non-CT4 tasks are now implementation-ready.**
