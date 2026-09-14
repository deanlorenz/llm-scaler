# Code review — composite-analyzer diff

Running notes from the user's step-by-step review of the implementation
(post spec-v8, PASS 12/12). No code changes are made during this review —
each entry below is a discussion outcome only. Diff scope: `c013012e..composite-analyzer`,
excluding `.session/`.

## 1. Inline comments are too long and spec-coupled

Inline comments throughout the diff (e.g. `demand.go`, `undefined.go`,
`composite_decision.go`, `composite.go`) are excessively long — multi-paragraph
blocks that cite internal spec sections (§2.5, A14, A18, D3...) and internal-only
decision names (C0-agree, D_sat, N_com) that mean nothing without the mission's
spec doc. Reads as spec-transcription rather than code documentation, and ties
the code's readability to a document that won't ship with it.

**Outcome:** Create/extend a developer-guide doc to capture the design
durably, then shrink inline comments to point at that doc instead of
re-explaining the design inline. See item 2.

## 2. Design should live in a dev-guide, not only in spec.md / inline comments

`docs/developer-guide/multi-analyzer-pipeline.md` already exists and covers
the per-analyzer slice, the optimizer, and shared allocation helpers — this is
the natural, already-established home for the composite aggregation design
(the fallback chain C0/C1/C2/C4, the aggregation primitives `DemandForRole`/
`AggN`/`PRCCom`, the undefined-value convention, the `buildComposite` /
O2 wiring step), rather than a new file.

**Outcome:** Extend `multi-analyzer-pipeline.md` with a new section covering
this design. This becomes the durable, spec-decoupled reference; inline
comments then shrink to point at it (per item 1) — not done yet, no changes
made during this review.

## 3. `aggregation` package — concrete implementation concerns

User likes the functions' purposes, not their concrete implementation. Ten
sub-points, each investigated against the actual code and the pre-existing
package it was added to (`internal/engines/aggregation/aggregation.go`,
127 lines, pre-mission, holds `SumTotalSupply`/`SumTotalAnticipatedSupply`/
`DemandByRole`/`IsDisaggregated`/`SumTotalDemand`/`AggregateByRole` — several
functions in one file, one package doc comment).

### 3.1 Many small files (~1 function/file) — Go convention?

**Not idiomatic, and not this codebase's own pattern.** The pre-existing
`aggregation.go` groups 6 related functions in one file under one package
doc. This mission's 5 new files (`demand.go`, `undefined.go`,
`model_coverage.go`, `replicas_needed.go`, `prc_com.go`) each hold ~1
function and break with that. Reads as an artifact of how the work was
planned/dispatched (1 spec-section/coder-checklist-item ↔ 1 file), not a
deliberate Go-style choice.

### 3.2 Who calls this package — engine, optimizer, or both?

**Both, but the new additions are single-consumer.** Import grep:
- Pre-existing functions (`SumTotalDemand`, `DemandByRole`, `IsDisaggregated`)
  are called by the analyzers themselves (`saturation_v2/analyzer.go`,
  `throughput/analyzer.go`) for their own per-analyzer math.
- This mission's new functions (`DemandForRole`, `AggN`, `PRCCom`,
  `modelCoverageFromRoles`, `maxOfDefined`/`minOfDefined`) are called ONLY
  from the new composite path: `internal/engines/allocation/composite_decision.go`
  and `internal/engines/steadystate/composite.go`. No analyzer and no
  optimizer file calls the new functions.

### 3.3 Inefficient ifs/loops; no error checking

- Loops (`DemandForRole`, `maxOfDefined`/`minOfDefined`, `variantCapacity`)
  are O(n) linear scans over small slices (a handful of variants/roles) —
  real but likely immaterial at this scale.
- No error checking is by design: every function returns `(value, ok)`,
  never `(value, error)` — nothing here is exceptional, only "undefined,"
  which these functions treat as a legitimate, non-error outcome. Defensible
  pattern in principle; see 3.5 for a place it's applied inconsistently.

### 3.4 Should use stronger typing (Role, SOName, Coverage, ...)

Agreed as a direction, but **pre-existing codebase-wide pattern, not a
regression this mission introduced** — `domain.RoleBoth` is an untyped
`const = "both"`, `VariantName` is a plain `string` everywhere already.
This mission's new `role string`/`variant string` params are consistent
with existing style. Fixing it would be a larger, codebase-wide typing
change, out of this mission's scope.

### 3.5 `DemandForRole` — unknown role, nil result, `role==""` check placement

Three distinct, verified issues:
- **Nil `result`**: `demand.go:31` dereferences `result.RoleDemand` with no
  nil check on `result` — latent panic if that invariant ever breaks.
  Inconsistent with its own sibling `replicasNeeded` (`replicas_needed.go`),
  which DOES guard `if result == nil { return 0, false }` before calling
  `DemandForRole`. Same package, same undefined-value convention, applied
  inconsistently.
- **Unknown/garbage role string**: doesn't crash, but silently returns
  `(0, false)` — indistinguishable from a legitimately-absent-but-real role.
  No validation that `role` is one of the known values anywhere in the call
  chain. Directly connects to 3.4 — stronger typing would catch this at
  compile time.
- **`role==""` canonicalization placement — user's sharpest catch**: the
  `role="" → RoleBoth` canonicalization is only semantically NEEDED inside
  the `RoleDemand == nil` branch. In the `RoleDemand != nil` branch it's only
  safe to skip if every writer of `RoleDemand` already normalizes empty-string
  keys before storing. Verified: only one production writer
  (`composite.go:141`, which copies `sat.Result.RoleDemand` verbatim) — the
  guarantee is inherited from whatever analyzer populated it, not
  independently verified here. Plausible the invariant holds, but
  undocumented and the current code pays for it defensively on every call
  rather than stating/relying on the guarantee.

### 3.6 `maxOfDefined`/`minOfDefined` — why generator functions?

Both real call sites (`model_coverage.go`'s `minOfDefined(2, ...)`,
`replicas_needed.go`'s `maxOfDefined(len(results), ...)`) pass a closure
that COMPUTES `(value, ok)` from an index, not a pre-built slice — avoids
materializing an intermediate `[]struct{value; ok}` before folding over it.
- Defensible for `AggN`'s case (n = number of analyzers, real per-item
  computation, avoids a throwaway slice).
- **Overkill for `model_coverage.go`'s `n=2` call** — two hardcoded
  ternary-style lookups wrapped in generic higher-order machinery is more
  indirection than a plain 4-line if/else for "min of two optional values"
  would need. User's instinct to question this is fair for that call site.

### 3.7 `modelCoverageFromRoles` — "prefill"/"decode" not constants; "cov" vs "coverage"

**Confirmed real defect, not a nit.** `domain.RolePrefill` and
`domain.RoleDecode` already exist and are used everywhere else in the
codebase (`throughput/analyzer.go`, `saturation_v2/analyzer.go`,
`scalefromzero`, `variantmeta/discovery.go`). `model_coverage.go` hardcodes
the literal strings `"prefill"`/`"decode"` instead — inconsistent with the
rest of the codebase AND with itself (the same file DOES use
`domain.RoleBoth` symbolically two lines away). Should use the existing
constants. Comment's "cov(prefill)" shorthand should also say "coverage" to
match the actual parameter/return naming.

### 3.8 `variantCapacity`/`replicasNeeded` — should note SOs can differ in order/be missing across analyzers

Fair documentation gap (not a code defect). The doc comment explains WHAT
happens when a variant is absent, never WHY a linear scan-by-name is
required: different analyzers' `VariantCapacities` slices are independently
built, not index-aligned — analyzer A's slice can order variants
differently than analyzer B's, and B may lack a variant A has. This is
exactly the kind of hidden-constraint "why" this project's AGENTS.md says
comments should carry, and it's currently missing here (the reader has to
infer it from `buildComposite`'s union-of-variants logic elsewhere).

### 3.9 `AggN` — naming, scores, per-SO cardinality, coverage guarantee

- **Naming**: existing codebase convention for this kind of function is
  `<what>Replicas`/`<what>ReplicasFor<scope>` (`roleBottleneckReplicas`,
  `safeRemovalReplicasForRole`) — confirmed via grep, pre-mission. `AggN`
  breaks that twice: uses the spec's math notation (`N`) instead of the
  domain word "replicas," and a generic `Agg` prefix instead of naming what's
  aggregated. Something like `aggregatedReplicasNeeded` (pairs with the
  already-well-named `replicasNeeded` it wraps) fits the codebase better
  than `AggN`. (User's suggestion `ComposeReplicaCount` is reasonable too,
  though "compose" isn't otherwise used in this codebase for this kind of
  operation.)
- **Scores ignored, should note it**: confirmed intentional —
  `composite.go`'s `maxScore` comment explicitly documents that `Score` is
  legacy/out-of-scope (spec §7.0: "Score should never have weighted
  cross-model priority," a known separate bug in `fairShareValue`). But that
  documentation lives in `composite.go`, not in `AggN`/`replicas_needed.go`
  where a reader would look first when asking "why doesn't this max weight
  by anything." Needs a pointer comment there too.
- **Per-SO? Which SOs? Full coverage?**: Yes — confirmed `ResolveSO` (and
  thus `AggN`) is called once per variant inside `buildComposite`'s
  `for _, v := range variants` loop, where `variants :=
  unionOfVariants(namedResults)` — the union of every VariantName across
  every analyzer's VariantCapacities. That union is what guarantees full
  coverage. But NEITHER `AggN` nor `ResolveSO`'s doc comments state this —
  the cardinality/completeness guarantee exists entirely in `composite.go`,
  one level up. Defensible layering (low-level fn needn't know its caller's
  iteration), but means a reader of `replicas_needed.go` alone cannot answer
  "is this per-SO, are all SOs covered" without jumping to `composite.go`.
  Worth a one-line pointer comment.

### 3.10 `PRCCom` — naming, caller, and the `satDemand` parameter

**Most significant finding in this section.** Confirmed via code read:
`PRCCom` is called exactly once, from inside `buildComposite`'s per-variant
loop (`composite.go:87`), immediately after `ResolveSO` for that same
variant. It is NOT called by the optimizer/downstream code (confirmed —
only `composite.go` and its own tests call it).

- **Is it for the aggregation, or downstream optimizer calls?** Definitively
  for the aggregation (the "former" in the user's framing) — it builds the
  composite's own PerReplicaCapacity fields, called from the composite-build
  step, never from `cost_aware_optimizer.go`/`rescale.go`.
- **Per user's own follow-on**: since it's the "former" case, it should be
  ONE call to compute PRC for all roles, not one call per SO. As implemented
  it's called once per VARIANT inside the loop, when it only actually needs
  to vary once per ROLE — same-role SOs get identical `D_sat[role]` and
  (often) identical `N_com`, so this recomputes the same value redundantly
  per SO. Real, avoidable redundant computation, not just a style
  preference — there are typically 1-3 roles vs. potentially many more
  variants.
- **Bad name — `ComposePRC`?** Agreed, and should follow whatever renaming
  `AggN` gets (3.9) for consistency — e.g. `aggregatedPRC`/`composedPRC`
  rather than `PRCCom`, which directly transliterates the spec's math symbol
  (`PRC_com`) into a Go identifier — the same spec-coupling problem flagged
  at the top of this review (item 1).
- **`satDemand` param name**: the parameter is `satDemand *domain.AnalyzerResult`
  — saturation's ENTIRE result, not a demand value; only used to extract
  `D_sat[role]` via `DemandForRole` inside the function body. The name
  undersells what's actually passed and reads as if it were already a
  `float64`. A name like `saturationResult` would be clearer.

## 4. (Found while investigating §3, not one of the user's original points) `contributedNames` is dead code

`internal/engines/steadystate/composite.go`: `contributedNames :=
make(map[string]struct{})` (line 52) is written into (line 82, inside
`if decision.OK`) but never read anywhere in the file. Looks like a
leftover from an earlier version of the logic (maybe intended for
provenance/logging per spec A12, per `SODecision.Contributors`'s own doc
comment) that never got wired to a consumer. Flagging for the user to
confirm whether it should be removed or actually used.

## 5. User's rulings on §3 (aggregation package) findings

Decisions recorded per sub-item; no code changed yet.

- **3.1 (many small files)**: needs fixing.
- **3.2 (who calls, relationship to AnalyzerResult construction)**: verified
  precisely — pre-existing `aggregation.go` functions (`SumTotalDemand`,
  `DemandByRole`, etc.) operate on a raw `[]domain.VariantCapacity` slice,
  called WHILE an analyzer is still building its own `AnalyzerResult`
  (confirmed: `saturation_v2/analyzer.go:161`,
  `totalDemand := aggregation.SumTotalDemand(variantCapacities)` — the
  result becomes a field of the `AnalyzerResult` under construction). This
  mission's new functions (`DemandForRole`, `AggN`, `PRCCom`) operate one
  level up: on ALREADY-BUILT `*domain.AnalyzerResult`/`[]NamedAnalyzerResult`,
  combining several analyzers' finished results. Not literal duplication,
  but structurally similar reduction helpers at a different level. **User's
  call: since the new ones are single-caller (composite path only), they
  should live with their caller (`allocation`/`steadystate`) rather than in
  the shared `aggregation` package**, whose other consumers (the analyzers)
  work at the pre-AnalyzerResult level and don't use them.
- **3.3**: OK, no action.
- **3.4 (`contributedNames` dead code, found in §4 below originally)**:
  needs fixing — user wants it actually wired up (for error/anomaly
  detection — e.g. surfacing which analyzers contributed, to diagnose
  unexpected aggregation results), not deleted.
- **3.5 (`DemandForRole` nil-guard inconsistency)**: fix by picking ONE
  policy consistently — either drop all defensive guards package-wide
  (trust callers) or add them everywhere. **User's preference: be
  defensive** (add the missing guards, don't remove the existing ones).
- **3.6 (generator functions in `maxOfDefined`/`minOfDefined`)**: user finds
  the generator-closure shape hard to read, independent of the efficiency
  argument. Direction: prefer a more direct/readable shape.
- **3.7 (prefill/decode literals, "cov" wording)**: fix.
- **3.8 (variantCapacity/replicasNeeded missing the "why linear scan" comment)**: fix.

## 6. `AggN` — resolved: this IS the compose operation; scores; signature stability

Investigated further per user's pushback on my earlier framing.

- **Single caller confirms it's not generic aggregation — it's specifically
  the compose-the-CompositeSignal step.** The only production call site
  builds `CompositeSignal` (via `ResolveSO`, called only from
  `buildComposite`). Supports 5's ruling that this should live beside its
  caller rather than in a shared package.
- **Score / fairShareValue are unrelated bugs.** User: `fairShareValue`'s
  misuse of `Score` (spec §7.0's flagged bug) is a separate, already-known
  upstream issue and has nothing to do with whether `AggN`'s own aggregation
  SHOULD weight by score.
- **`AggN` SHOULD use scores once a scoring policy is decided** — but that
  decision is deliberately deferred. Requirement: whatever replaces the
  current plain max must be swappable for a score-weighted combination
  WITHOUT forcing a signature change on callers. I.e. structure the
  plain-max-for-now implementation so score-weighting is an internal
  substitution, not an API change, when that policy lands.

## 7. `PRCCom` — corrected design: loop per role, not per SO; the "satDemand"/canonical-demand naming question

User corrected my earlier justification and laid out the actual intended
shape precisely. Verified against the code:

- **Confirmed: `N_com` (`decision.N` from `ResolveSO`) is genuinely PER-SO**,
  not per-role (`ResolveSO(entries, variant)` takes one variant). So my
  earlier claim ("N_com is often identical across same-role SOs") was
  imprecise/wrong as a justification — N_com can legitimately differ
  SO-to-SO even within one role.
- **What IS actually shared per-role is only the demand numerator,
  `D_com[role]`** — confirmed `VariantCapacity.Role` is a single string
  field (each SO has exactly one role), and the composite's demand is a
  per-role model-level quantity (shared by every SO of that role), while
  PRC is per-SO (`VariantCapacity.PerReplicaCapacity`, confirmed as the only
  field the optimizer ever reads — `prcForVariant`, `prcFromVCs`, direct
  `vc.PerReplicaCapacity` reads across `greedy_score_optimizer.go`/
  `cost_aware_optimizer.go`/`rescale.go` — always per-variant, never a
  separate per-role PRC value).
- **Correct shape (user's design, confirmed consistent with the domain
  model)**: loop over the small set of ROLES, look up `D_com[role]` once
  per role (not once per SO), then for each SO of that role compute
  `PRC(SO) = D_com[role] / N_com(SO)` — sharing the numerator lookup across
  same-role SOs, while N_com and thus the final PRC still correctly vary
  per SO. This is more precise than my original "loop per role, get PRC
  once" framing — only the numerator is shared, not the whole PRC result.
- **`satDemand` naming — bigger picture revealed by user**: the demand value
  functionally needs to be a CANONICAL composite demand, so PRC and demand
  become comparable ACROSS MODELS (not just across analyzers within one
  model). Currently implemented by reusing saturation's own result as that
  canonical source — but this is a stated TRANSITIONAL/implementation
  choice, not the intended permanent design; the project plans to move away
  from anchoring on "sat" specifically. `satDemand` as a parameter name is
  doubly wrong: it undersells what's passed (a whole AnalyzerResult, per
  §3.10) AND encodes a transitional implementation detail (saturation as
  the demand source) into a name that should reflect the durable concept
  (canonical/composite demand) instead.
- **User's emphasis: the more important open question is how the
  DOWNSTREAM OPTIMIZER consumes these composed PRC/demand values** — this
  is flagged as the next thing to focus on, beyond this function's internal
  implementation.

## 8. Allocation core (`composite_eligibility.go`, `composite_identity.go`, `composite_decision.go`, `composite_signal_gate.go`)

### 8.1 Many one-function files, refs to internal design

Same ruling as §3.1/§5 — needs fixing, same pattern repeats here
(`composite_eligibility.go`, `composite_identity.go`, `composite_decision.go`,
`composite_signal_gate.go` are each ~1 function + spec citations).

### 8.2 `eligible()` — A3 provenance; Live vs Informative; missing "enabled" gate

Verified against the actual liveness/enablement code:

- **"A3" is purely an internal spec label** (`.session/spec.md` §5.1.1) —
  not a codebase concept, another instance of the spec-coupled-comments
  problem from item 1 of this review.
- **The "enabled" gate is NOT actually missing — it exists one layer up.**
  Confirmed structurally: a disabled analyzer's `NamedAnalyzerResult` is
  never constructed or appended to `namedResults` at all
  (`engine_v2.go:170-172`: `if !config.AnalyzerEnabled(entry.name) { continue }`,
  BEFORE `buildNamedResult`/`append`). By the time `eligible()` sees an
  entry, it's already from an enabled (or, for saturation, unconditional —
  confirmed "Saturation is first and always runs" per
  `engine_v2.go:151-153`) analyzer. No disabled entry ever reaches
  `eligible()` to be filtered.
- **`Live` is confirmed to mean exactly "broken pipeline," not "low
  confidence in this result."** `updateLivenessAndSetLive`
  (`engine_v2.go:322-361`) sets `nr.Live = ok && now.Sub(lastGood) <=
  threshold` — true iff the analyzer produced an INFORMATIVE result within
  `analyzerLivenessStaleCycles` cycles. Staleness/pipeline-health (broken
  query filter, missing credentials, stopped reporting), exactly matching
  user's description — not a per-result quality judgment.
- **User's core design point (not a bug in current code — an open gap,
  since current code doesn't attempt this at all)**: `Informative` is the
  analyzer's own self-assessed confidence in ITS result, and confidence
  need not be symmetric across scale-up vs. scale-down — e.g., not
  confident enough to justify scale-down, but still safe to allow scale-up
  on. `Live` (broken pipeline) IS symmetric — a broken analyzer's numbers
  are unusable in either direction, correctly excluded from both today.
  Score might interact with Informative's asymmetry. **Explicitly not
  fully designed yet — recorded as an open design question, not something
  to fix now.**
- Sat non-live being "opt out, not fatal" and needing gate verification —
  user flagged this needs verification later, not a finding to act on now.

### 8.3 `CompositeSignalName`/`CompositeSignal` — usage rule, stronger than currently documented

**User's ruling, stronger than the code's own doc comment**: the composite's
name exists ONLY for logging/metrics attribution (so a human reading
logs/dashboards can tell "this line is the composite" — matches
`cycle-log.md`'s own framing). **No downstream consumer should branch on the
composite's name for ANY reason** — every consumer must be handed something
indistinguishable in shape/usage from one ordinary analyzer's result. This is
broader than `composite_identity.go`'s own comment, which only warns against
assuming `.Name == a SPECIFIC analyzer's name` (the bug already fixed in
`composite_signal_gate.go`) — user's rule bans name-based branching on the
composite entirely, not just the one specific mistake already caught.

Verified: current code does NOT currently violate this. All `.Name ==`
checks in the codebase operate on entries INSIDE the pre-composite
`namedResults` slice (identifying which source analyzer contributed —
legitimate), never on the composite's own `.Name` after it's built and
handed off as `ModelScalingRequest.CompositeSignal`. Worth turning into an
explicit invariant/guard (e.g. a test) rather than leaving it implicit.

### 8.4 Decision-path constants — naming, encoding style, "agree" is misleading, sat-fallback isn't unique

- **Existing precedent, verified**: pre-mission Reason/decision constants
  use plain descriptive English (`ReasonNoData = "no-data"`, `ReasonError =
  "error"`, `DecisionReasonSaturationOnly = "saturation-only mode"`, etc.)
  — no numbered/lettered codes. The composite's `C0-agree`/`C1-single`/
  `C2-sat-fallback`/`C4-no-signal` scheme breaks with that convention.
  `domain.DecisionReason` (a similar decision-path label) is also a proper
  named string TYPE, not an untyped const like the composite's — relevant
  to the earlier stronger-typing discussion (§3.4).
- **Confirmed these ARE user-facing, not internal-only**: `docs/reference/cycle-log.md`
  (a user-facing reference doc per this project's own docs taxonomy)
  documents `C0-agree`/`C1-single`/`C2-sat-fallback`/`C4-no-signal` verbatim
  as values an operator will see in real logs. Raises the bar on these
  needing to be clear on their own, not just internally consistent.
- **"Agree" is confirmed misleading.** Verified against the actual logic
  (`composite_decision.go` lines 93-110): `DecisionAgree` fires whenever
  MORE THAN ONE contributor had a defined N — nothing checks or requires
  the values to be close or equal, only a plain max is taken. "Agree"
  implies consensus/matching values; nothing in the code confirms or
  requires that. User's suggested "aggregate" or "composite" is more
  accurate — states only "more than one input," no implied concurrence.
- **"Sat fallback is unique now, just because it's always there" — folded
  into §8.6's larger correction** (sat is not architecturally special; see
  below).

### 8.5 `SODecision` struct — good, no changes.

### 8.6 `ResolveSO` — sat is not special; wrong single/fallback taxonomy; the loop itself is incorrect

**Most significant finding in this section — user's correction, verified
against the code.**

- **Sat is not architecturally different from any other analyzer** — it
  currently, always, provides a usable fallback for every SO, but that's a
  PROPERTY it happens to have (its design guarantees full SO coverage), not
  a PRIVILEGE the aggregation code should special-case it for.
- **Corrected decision taxonomy (user's ruling)**:
  - `single` = exactly ONE analyzer produced a defined N for this SO —
    symmetric, regardless of whether that one analyzer happens to be sat or
    any other. NOT "sat-alone vs. others-alone" as two different paths.
  - `sat-fallback` = should be a much NARROWER case than currently
    implemented: only when sat itself is NOT ENABLED (so it would
    structurally never be an ordinary contributor) yet still contributes as
    an emergency fallback because nothing else did. Currently implemented
    far more broadly — fires whenever sat is the only contributor left
    after others are empty, regardless of any "not enabled" state. Note:
    verified sat currently has NO enabled/disabled state at all (it's
    unconditional per §8.2) — so this specific path may not even be
    reachable today as user intends it; recorded as a design target, not
    confirmed achievable without further changes upstream.
  - `no-signal` = should also specifically cover a BROKEN or MISSING sat
    (not just "zero analyzers had anything from anyone") — i.e., no-signal
    partly describes sat's own health, not purely an aggregate absence.
  - Directly resolves 8.4's "sat fallback is unique now, just because it's
    always there" — the current C2 path conflates "sat happens to be the
    sole survivor" (should be `single`) with the actually-narrow
    not-enabled-yet-contributing case (what `sat-fallback` should mean).
- **"What does it mean to aggregate for a single analyzer over a single
  SO?" — confirmed as a real, precise bug**, and the same defect found
  independently while investigating §3/§6 of this review:
  `composite_decision.go:81`, `aggregation.AggN([]*domain.AnalyzerResult{e.Result},
  variant)`, is called INSIDE the per-analyzer loop, wrapping ONE analyzer's
  result in a 1-element slice — there is nothing to aggregate over a single
  element for one SO. `AggN`'s own max-over-many logic is dead at this call
  site; it should call `replicasNeeded(e.Result, variant)` directly. The
  REAL cross-analyzer aggregation is reimplemented independently, inline,
  via the `others`/`sat` bookkeeping and the second loop (lines 99-105) —
  so aggregation logic exists in two places: once (unused) inside `AggN`,
  once (duplicated) inline in `ResolveSO`.
- **Structural consequence**: the sat/non-sat special-casing during
  collection (`if e.Name == domain.SaturationAnalyzerName` building a
  separate `sat` var, re-merged at line 96) is the wrong shape given the
  above — contributors should likely be collected symmetrically (no
  name-based branching), with the count (0/1/many) driving
  `no-signal`/`single`/`aggregate`, and `sat-fallback` as its own distinct,
  narrow special case layered on top. Not a redesign to implement now —
  recording the corrected model only, per review-mode instructions.

### 8.7 `HasUsableCompositeSignal` — wrong granularity (per-model, should be per-SO)

Verified call sites: `engine.go:1091` and `engine_v2.go:735` (via
`hasSaturationResult`) both call `HasUsableCompositeSignal(req.CompositeSignal)`
— ONE call per `ModelScalingRequest`, i.e. per MODEL. Its own implementation
(`composite_signal_gate.go:44-50`) loops `composite.Result.VariantCapacities`
and returns true as soon as ANY SO is not `C4-no-signal` — so today it
answers "does ANY SO in this model have a signal," a single boolean for the
whole model.

**User's correction**: the right granularity is PER-SO, not per-model. If
SO-A has no signal but SO-B (same model) does, the correct behavior is to
opt out of SO-A specifically — possibly informed by SO-B's data if there's a
legitimate way to borrow across SOs of the same model — not a single
model-wide binary gate. Current implementation conflates "no SO in this
model has ANY signal" (legitimately model-wide opt-out) with "some SO lacks
signal while others don't" (today silently invisible to this check, since it
returns true the moment ANY SO has a signal, ignoring which ones don't).
Real granularity mismatch between what's implemented and what the design
requires — not something to fix now, recording the gap.

## 9. User's corrections to §8 — sat CAN be disabled; AggN's real shape; A3 misattribution; enumerate decision paths; sat-missing is categorically worse

Investigated each against the code; two of these directly overturn findings
recorded in §8.

### 9.1 Sat CAN be disabled via config — §8.2/§8.6's "sat has no enabled state" was WRONG

**Correction to §8.2 and §8.6.** Verified: `config.AnalyzerEnabled`
(`internal/config/saturation_scaling.go:657`) is generic — it looks up
`c.Analyzers` by `EffectiveType()` and returns the entry's `Enabled` value
(or `true` if present-but-undefaulted, or `false` if the analyzer has no
entry at all). Nothing in `AnalyzerEnabled` itself special-cases
saturation — a config author CAN write a `saturation` entry with
`enabled: false`, and `AnalyzerEnabled("saturation")` would faithfully
return `false` for it.

What IS true (and is what my §8.2 finding actually observed): the ENGINE
currently never calls `AnalyzerEnabled` for saturation — confirmed by the
function's own doc comment: "Saturation is exempt: it is guarded by the
SaturationAnalyzerName check upstream (engine_v2.go ~L136) before
AnalyzerEnabled is ever called." So saturation's entry always runs
UNCONDITIONALLY at the engine level today, regardless of what its config
enabled-flag says — the flag exists and is meaningful, but is currently not
consulted for sat specifically.

**User's design point**: a disabled sat should NOT be eligible as an
ordinary contributor — it should only be usable as (a) the fallback source
and (b) the source of the canonical demand unit (D_sat). This requires
checking `config.AnalyzerEnabled(domain.SaturationAnalyzerName)` (or
equivalent) somewhere in the composite path. Verified this is a real gap,
not a small tweak: neither `eligible(nr NamedAnalyzerResult)` nor
`ResolveSO(entries []NamedAnalyzerResult, variant string)` receives config
at all today — wiring this in requires a genuine signature/data-flow
change, not just a conditional add.

### 9.2 `AggN` — correct shape: combine ALL analyzers for one SO; contributor list = all eligible; sat-fallback is the special case

**Correction to my §3.9/§8.6 framing.** User's model: `AggN` should combine
ALL analyzers (sat included, symmetrically) for one SO — the contributor
list is simply "every eligible analyzer" (no sat/non-sat split during
collection), and `sat-fallback` is layered on top as the one special,
narrow case (per §8.6: sat contributing despite not being an ordinary
eligible contributor, i.e. the disabled-but-fallback case from 9.1) —
not a structural branch during the main collection loop. This confirms
and sharpens §8.6's "collect contributors symmetrically" correction: the
current code's `if e.Name == domain.SaturationAnalyzerName` split during
collection is confirmed wrong, and the fix is for the eligible-collection
step to be genuinely name-blind, with the disabled-sat-as-fallback path
handled as an explicit separate branch afterward (only reachable when sat
was excluded from the eligible set for being disabled, per 9.1 — not
reachable, as before, merely because sat happened to be the last one
standing among already-eligible contributors).

### 9.3 "A3" was not user's citation

**Correction to §8.2.** The "A3" spec-citation in `eligible()`'s doc
comment is not something the user specified — it's the mission's own
internal spec-decision label (`.session/spec.md`), attributed to "the
mission" generically in §8.2, not to the user. Correcting the record: this
is a code/spec-authoring artifact, not something the user asked for or
introduced.

### 9.4 Sat non-live → opt-out — needs verification AFTER the change, and matches existing controller behavior

Clarification, not a new finding: "opt out, not fatal" for a non-live sat
is both a requirement AND something that needs verifying once the
sat-disabled/eligibility changes (9.1/9.2) land — not verified yet. Matches
existing precedent: the scaler controller does not crash on a fatal/broken
saturation signal, it simply opts out for that model/SO. Any composite
change must preserve that opt-out-not-crash behavior for a non-live sat.

### 9.5 Decision paths should be enumerated where possible, to avoid errors

Direction for whatever replaces the current `const (... = "C0-agree" ...)`
untyped-string block: prefer an enumerated/typed representation (e.g. a
named type with a closed set of values, mirroring `domain.DecisionReason`
per §8.4's precedent) over free-form untyped strings, specifically to catch
mistakes at compile time rather than allowing an arbitrary string through.
Connects to the earlier stronger-typing discussion (§3.4) but scoped
specifically to decision-path values here.

### 9.6 `HasUsableCompositeSignal` — §8.7 was incomplete, not simply wrong

**Important nuance to §8.7, verified against pre-mission code.** The
pre-mission `hasSaturationResult` (before this mission's rename) checked
specifically "does this request carry a SATURATION result" — verified via
its own doc comment (`git show c013012e`): "A request without one was not
measured this cycle, so its replica counts are not evidence of anything and
must not be charged to a quota." This was ALWAYS a check about saturation
specifically, not "does any analyzer have a signal" — because saturation
missing/broken is categorically worse than any other single analyzer being
broken: saturation defines the canonical demand unit (D_sat) the whole
composite is expressed in, and is the guaranteed-coverage fallback for
every SO. "No sat" means the composite has no unit to express anything in
at all — categorically worse than "SO-X has no data but the model's other
SOs do."

**Reconciling with §8.7**: both concerns are real and distinct, not
contradictory:
- §8.7's per-SO opt-out granularity (SO-A has no signal, SO-B doesn't) is
  still a real, unaddressed gap.
- 9.6 adds: there is ALSO a legitimate, stronger, MODEL-level check
  specifically about sat's own health/presence — categorically worse than
  an ordinary per-SO gap, and this is what the original (pre-rename) check
  was actually for. The composite-analyzer rename preserved the check's
  MECHANISM (loop over VariantCapacities, check decision path) but
  genericized its MEANING (any SO having any signal) in a way that
  quietly dropped the original, specifically-about-sat semantics.
- Both gates likely need to coexist: a per-SO "does this SO have a signal"
  check (§8.7) AND a model-level "is sat itself present/healthy" check
  (9.6, closer to the ORIGINAL pre-mission intent) — not a single boolean
  serving both purposes, which is what `HasUsableCompositeSignal` currently
  is.

---

## 10. `allocation` package shared rounding (`query_api.go`, `rescale.go`) [USER, 2026-09-14]

- **Bad function names — show a misunderstanding of the role.** `replicasForDemand`
  (`query_api.go:19`) and `safeReplicasForSpare` (`query_api.go:39`) compute the same
  quantity the composite redesign already named `TotalReplicas`/`ReplicasNeeded`
  (`demand/prc`, ceil'd or floor'd) — the names here don't reflect that, and were coined
  independently of that naming decision.
- **Duplication, not shared where it should be.** The same ceil(demand/prc)/floor(spare/prc)
  pattern is reimplemented independently in at least three places:
  `multi_backup/analyzer_helpers_multi.go:182,250`, `greedy_score_optimizer.go:416`, and the
  `deltaUtil*demand/prc` floor pattern duplicated verbatim between `analyzer_helpers.go:355`
  and `multi_backup/analyzer_helpers_multi.go:386`.
- **Review comments from before still apply here too** — inefficient, long, spec-section-
  citing comments repeating the same explanation instead of stating the rule once.
- Not yet resolved: whether these functions get renamed/unified now or as part of resuming
  the file-by-file review of `query_api.go` (already on the not-yet-reviewed list,
  `STATE.md`) — recorded here, not actioned.

---

<!-- next items appended below as the review continues -->
