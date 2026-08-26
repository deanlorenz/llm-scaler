# `fairShareValue` correctness investigation (2026-08-25)

## Trigger

The domain expert is suspicious of the prior finding
(`score-and-priority-semantics-2026-08-25.md`) that `fsv = priority × Σᵢ Scoreᵢ ×
Σ_role pickerState[i][role]`. Their objection, verbatim: *"I am not convinced on
the FSV findings... For FSV, we should care about relative coverage (using the
composite PRC). Fair share gives model the same average coverage. If priority is
set then the allocation can be skewed... I am beginning to suspect that current
code simply has a bug."*

This doc investigates the claim rigorously: read the full code path, trace the
git archaeology of why the formula looks the way it does, work a concrete
numeric example of what it actually optimizes for, confirm precisely how often
the cross-analyzer sum is exercised, and audit what is actually tested versus
merely assumed.

**Bottom line up front:** the user's suspicion is *directionally correct on the
biggest point* — `fairShareValue` does **not** compute "same average coverage
across models." It computes "same average **absolute remaining demand**," which
is a different (and, by the coverage framing, provably unfair) quantity. This is
not a bug in the sense of "the code doesn't do what it was designed to do" —
git history shows unambiguously that "equalize absolute remaining demand" was
the literal, intentional design from commit 1 (`GreedyBySaturationOptimizer`,
"most starved model gets GPUs first," no coverage ratio ever existed). It is,
however, a **real design/naming mismatch**: the code is named and documented as
"fair share" and reads as if it should track the coverage/proportional-fairness
concept the user is applying, but it was never built to do that, and nothing in
the design docs argues for the coverage framing as an alternative that was
considered and rejected. The cross-analyzer `Σᵢ Scoreᵢ` term, separately, is
real code with one production call site — but that call site *always* passes a
length-1 slice today, so the "sum across analyzers" behavior is exercised by
exactly one synthetic unit test and has never run in production.

---

## 1. Full read of `fairShareValue` and its calling context

### 1a. `fairShareValue` (`internal/engines/allocation/greedy_score_optimizer.go:62-94`)

```go
// fsv = priority × Σᵢ Score_i × Σ_role pickerState[i][role]
func fairShareValue(priority float64, s []NamedAnalyzerResult, ps RolePairedState, roles []string) float64 {
	weighted := 0.0
	for i, e := range s {
		if e.Result == nil {
			continue
		}
		roleSum := 0.0
		for _, role := range roles {
			if i < len(ps) {
				roleSum += ps[i][role]
			}
		}
		weighted += roleSum * e.Score
	}
	if fsv := priority * weighted; fsv > 0 {
		return fsv
	}
	// Fallback: max remaining demand across roles when Score=0 or priority=0.
	maxDemand := 0.0
	for i, e := range s {
		if e.Result == nil {
			continue
		}
		if i < len(ps) {
			for _, role := range roles {
				if ps[i][role] > maxDemand {
					maxDemand = ps[i][role]
				}
			}
		}
	}
	return maxDemand
}
```

`ps[i][role]` (`pickerState`, type `RolePairedState = []map[string]float64`) is
seeded by `initRoleState` (`analyzer_helpers.go:131-167`) directly from
`RoleCapacities[role].RequiredCapacity` (disaggregated) or `e.Remaining`
(non-disaggregated, itself initialized from `Result.RequiredCapacity` by the
engine's capacity-build step). **This is an absolute capacity-unit quantity —
e.g. a token-magnitude deficit — never a normalized 0–1 ratio.** See
`domain/saturation_analyzer.go:218-221`: *"RequiredCapacity indicates whether
scale-up is needed (>0 means yes). It is the token-based deficit."* There is no
division by demand or by supply anywhere inside `fairShareValue` — it is a pure
weighted sum of deficits.

### 1b. What problem is `fairShareValue` actually used to solve — the full call chain

`Optimize` (`greedy_score_optimizer.go:98-183`) computes `fsv` once per model to
decide the scale-up/other split (`anyRoleNeedsScaleUp(ps, roles) || fsv > 0`),
then hands scale-up models to `fairShareScaleUp`.

`fairShareScaleUp` (`greedy_score_optimizer.go:203-262`) is the classic
iterative **max-min water-filling** loop:

1. `mean := computeMean(active)` — arithmetic mean of `w.remaining` (i.e. mean
   of `fsv`) across all still-active models (`computeMean`, :475-484: `total /
   len(active)`).
2. `sortByRemainingDesc(active)` — sort by `fsv` descending; take the highest
   (:237-238).
3. `allocationMean := mean` (with a 1/N tie-break tweak for the equal-remaining
   case, :240-245) — the target level to bring the most-starved model down to.
4. `allocateForModel(ctx, w, allocationMean, ...)` (:267-354): computes
   `target := w.remaining - mean` — **a GPU-allocation budget denominated in
   the same capacity units as `fsv`/`RequiredCapacity`** — caps `ps[i][role]` at
   `target`, and calls `allocateForModelPaired` with a `RolePickFn`
   (`fairShareRolePick`) that converts `target` into a replica cap via
   `fairShareCap := int(math.Ceil(target / vc.PerReplicaCapacity))`
   (`greedy_score_optimizer.go:434`). So `fsv`'s **magnitude**, not just its
   rank, directly bounds how many replicas a model gets this iteration — this
   was independently confirmed by the prior investigation (§3 of
   `score-and-priority-semantics-2026-08-25.md`).
5. `w.remaining` is recomputed via `fairShareValue` again post-allocation
   (:343-352); the model drops out of `active` once `remaining <= mean`
   (actually `> mean` check at :256-260 keeps it active) or is starved of GPUs
   (`w.remaining = -1` at :250, :259).
6. Repeat until no active models remain or the type-summed GPU budget is
   exhausted (`totalGPUs == 0`, :228-231).

**The problem this loop solves, precisely: given competing models each with an
absolute remaining-demand number (`fsv`), allocate scarce GPUs iteratively so
that no model is left with a `remaining` above the current mean of the
remaining active models, most-starved-first.** This is textbook max-min
fairness *on the `fsv` quantity itself* — it equalizes `fsv` (down to zero, then
drops out), never a ratio derived from `fsv` and something else. The docstring
at the top of the file (`greedy_score_optimizer.go:15-24`) is honest about this:
*"Fair-shares GPUs across models (highest-priority model gets GPUs first)"* —
it advertises priority-ordering + equalization of the metric, not
equalization of a coverage ratio.

### 1c. `sortByRemainingDesc` / `computeMean` — nothing hidden here

Both are exactly what they look like: `computeMean` (:474-484) is a plain
arithmetic mean of `w.remaining` (no normalization, no per-model divisor other
than `len(active)`); `sortByRemainingDesc` (:486-491) is a plain descending sort
on the same. Neither introduces or removes any normalization step. **The
"coverage" framing has no foothold anywhere in this file** — it is not that
coverage math is present but subtly wrong; it is entirely absent from the
fair-share loop. (It *does* exist elsewhere — see §3.)

---

## 2. Design rationale — why `Σᵢ Scoreᵢ × Σ_role pickerState[i][role]`? Is there a doc explaining "fair" here?

### 2a. Current developer docs describe the formula, but never justify the choice of absolute-demand equalization over coverage-ratio equalization

`docs/developer-guide/multi-analyzer-pipeline.md:429-465`:

> `fairShareScaleUp` uses iterative mean equalization rather than fixed
> fractions... `fairShareValue = priority × Σᵢ Score_i × Σ_role
> pickerState[i][role]`. A higher `Score` on a high-demand analyzer increases a
> model's priority value and therefore how many GPUs it attracts in a
> constrained environment.

This is a *description* of the mechanism, not a *justification* for why
equalizing absolute weighted demand is "fair." It never engages with the
alternative (equalize a coverage ratio) at all — the concept doesn't appear in
this doc.

`docs/design/modeling-optimization.md` defines **priority/criticality** (line
29: *"used to decide on the assignment of accelerators to variants serving
particular workloads when the total resources are tight"*) and enumerates
allocation policies (`PriorityExhaustive`, `PriorityRoundRobin`, lines 114-115)
but **contains no mention of "fair share," "coverage," or any formula** — it is
a level above the mechanism in question and does not settle the dispute either
way.

**No design document anywhere in the repo states or argues for "fair share
means equal coverage ratio across models."** Nor does one argue explicitly for
"equal absolute remaining demand" as a deliberate choice over the coverage
alternative — the absolute-demand framing is simply what was implemented,
un-debated, from the start (see §2b).

### 2b. Git archaeology: the formula's actual origin

`git log --all --oneline --grep=fair -i` surfaces the optimizer's origin commit,
**`a16e2f09` "Greedy by saturation optimizer (#771)"** (Feb 25, `Evgeny
Shindin`). Reading `internal/engines/pipeline/greedy_saturation_optimizer.go` as
it existed at that commit:

```go
// modelWork tracks per-model allocation state during fair-share iteration.
type modelWork struct {
	req       ModelScalingRequest
	remaining float64        // remaining RequiredCapacity (negative = fully satisfied)
	targets   map[string]int // variant name → target replicas
}
...
if req.Result.RequiredCapacity > 0 {
	targets := initTargets(req.VariantStates)
	scaleUpWork = append(scaleUpWork, &modelWork{
		req:       req,
		remaining: req.Result.RequiredCapacity,   // <-- literally raw RequiredCapacity
		targets:   targets,
	})
}
```

At this point there is **no `Score`, no `Priority`, no `fairShareValue`
function at all** — `remaining` *is* the model's raw `RequiredCapacity` (an
absolute token deficit), full stop. The commit message: *"Implements iterative
mean-based fair-sharing to distribute scarce GPUs across competing models. Most
starved model gets GPUs first."* This is the entire design intent, stated once,
never revisited: **"starved" = has more absolute unmet demand, not "has a worse
coverage ratio."** The PR's own follow-up commit (*"fix: improve greedy
optimizer fairness and pending replica handling"*) reinforces this: it fixes an
*equal-`RequiredCapacity`* deadlock by giving tied models 1/N of the gap — again
operating purely on the absolute quantity, no ratio anywhere.

`Priority` and `Score` were added much later, incrementally, in
**`09e1c386` "engines/pipeline: per-analyzer slice optimizer — delete
engine-side combine (#1246)"** (multi-commit PR, same author as this
investigation's requester, Dean H Lorenz). Reading its commit sequence:

- `Score` is introduced purely as *"per-analyzer weight for fair-share
  priority"* (commit "pipeline: paired helpers + CostAware disaggregated path"),
  added to `NamedAnalyzerResult` alongside disaggregated-model support — not
  motivated by any coverage-fairness argument, but by "we now have more than
  one analyzer feeding an entry; give each one a trust weight."
- `fairShareValue(priority, s)` is introduced in the commit *"pipeline: migrate
  GreedyByScoreOptimizer to per-analyzer slice (both paths)"* as `priority ×
  Σᵢ(Remainingᵢ × Scoreᵢ)` — i.e. the moment `Priority` and cross-analyzer
  `Score`-weighting are bolted onto the pre-existing raw-`RequiredCapacity`
  metric from #771, unchanged in kind (still an absolute-quantity sum, just now
  with two more multiplicative/additive knobs).
- A follow-up commit in the same PR (*"engines/pipeline: B1+T1 — populate
  NamedAnalyzerResult.Score from config"*) admits the wiring was initially
  broken: *"Without this fix NamedAnalyzerResult.Score was always 0, causing
  GreedyByScoreOptimizer.fairShareValue to fall back to
  max_i(Remaining_i)... T1.3: Multi-model fair-share priority integration test.
  Two models with equal RC but different Priority (1.0 vs 5.0)... requires
  Score populated."* This is the origin of the only cross-analyzer-sum test
  that exists (see §4) — and it is explicitly a wiring/regression test for
  `Score` propagation, not a fairness-property test.
- The role-aware rewrite (*"pipeline: Greedy per-role fair-share + drop α"*)
  changes `fairShareValue`'s third argument from the model-level `Remaining`
  scalar to picker-local per-role `pickerState`, arriving at today's exact
  signature — again a mechanical refactor (role-generalization), not a
  fairness-semantics change.

**Conclusion of the archaeology: at no point in the formula's history — from
its #771 origin through its #1246 role-generalization — was "equalize a
coverage ratio" ever proposed, discussed, or rejected. The formula has always
equalized an absolute (possibly weighted/multiplied) demand quantity. There is
no design doc arguing FOR absolute-demand equalization as deliberately chosen
over coverage-ratio equalization; it's simply the only thing ever built.** This
matters for judging "is it a bug": it is not an implementation defect relative
to a stated design — it is a naming/expectation gap between what "fair share"
suggests (to a reader applying standard fairness vocabulary, as the user is)
and what the code has only ever done.

---

## 3. Does the current formula achieve "same average coverage," or does it favor absolute demand regardless of coverage?

### 3a. The coverage ratio does exist in this codebase — just not in `fairShareValue`

Confirmed (also independently verified in
`coverage-math-and-zero-guards-2026-08-25.md` §2) inside
`allocateForModelPaired` (`analyzer_helpers.go:337-434`):

```go
// analyzer_helpers.go:370-380
demand := roleAggRemaining(s, pickerState, role)
if demand <= 0 {
    utilByRole[role] = 1.0
} else {
    utilByRole[role] = float64(n) * prc / demand   // <-- replicas × PRC / demand: the coverage ratio
}
```

So the user's mental model — `coverage = replicas × PRC / demand` — is not
foreign to this codebase; it is exactly `utilByRole`, used to decide **how much
of one role's demand a single allocation step covers within one model's
allocation pass** (the `deltaUtil = min_role utilByRole[role]` joint-commit
bound that couples P and D sizing). It is a *within-model, within-iteration*
tool. It never propagates up into `fairShareValue`, which operates one level
above (across models) and never divides by demand at all.

### 3b. Worked example: two models with equal coverage, unequal absolute demand

Set up two non-disaggregated models, `Priority=1`, single analyzer
(`Score=1`), so `fairShareValue` reduces to `Σ_role pickerState[i][role]` =
`RequiredCapacity` directly (matches every production configuration today, see
§4):

| Model | TotalDemand (tokens/s) | TotalSupply (tokens/s) | RequiredCapacity (= demand − supply, floor 0) | Coverage = supply/demand |
|---|---|---|---|---|
| **Big**   | 100,000 | 80,000 | 20,000 | 0.80 |
| **Small** | 10,000  | 8,000  | 2,000  | 0.80 |

Both models are **identically covered** (80% of demand currently served) — the
user's framing says fair share should treat them identically, or at most skew
by `Priority` (both are 1.0 here, so identically). But:

- `fsv(Big) = 20,000`, `fsv(Small) = 2,000`. `mean = 11,000`.
- `Big` sorts first (`sortByRemainingDesc`), gets `target = 20,000 − 11,000 =
  9,000` worth of GPU budget allocated toward it *first, this iteration*.
  `Small`'s `target` will only be `2,000 − mean'` in a later iteration, and by
  construction of max-min water-filling, `Big` — being 10× larger in absolute
  terms while equally covered — draws roughly 10× the GPU budget across the
  run, not an equal share.
- The **coverage-ratio framing says these two models should receive
  proportionally equal treatment** (both are at 80% coverage; a Priority-only
  skew, per the user, should be the only lever) — but the current formula gives
  `Big` an order of magnitude more of the constrained GPU budget purely because
  its absolute deficit is larger, **despite identical relative need**.
- This is not a contrived edge case — it is the *typical* shape of the
  disagreement: any two models with different traffic volume but the same
  under-provisioning ratio hit this. A large, well-covered-but-huge model can
  out-compete a small, equally-covered model for every incremental GPU, and a
  small, badly-covered (e.g. 10% coverage) model can lose to a large,
  well-covered (e.g. 79% coverage) model if the large model's absolute deficit
  is bigger — the opposite of what "fair share" conventionally means in
  resource-allocation literature (proportional/max-min fairness on a
  *normalized* metric, not an absolute one).

### 3c. Is this "prioritize by absolute urgency" a defensible alternative design, or clearly a mismatch?

It could be a coherent design choice in isolation ("the model that's
furthest behind in absolute tokens/sec gets help first, because that's more
total unserved traffic") — but:

- **It contradicts the module's own name and every doc description that calls
  this "fair share"** and describes `Score`/`Priority` as multipliers on a
  "fairness" computation, without ever caveating that the base metric is
  absolute, not relative.
- Nothing in `docs/design/modeling-optimization.md` or
  `multi-analyzer-pipeline.md` states "absolute demand, not coverage ratio" as
  an intentional tradeoff — it's simply never discussed, which is consistent
  with §2b's finding that the choice was never actually made, just inherited
  from the original one-model-was-easy `RequiredCapacity`-as-`remaining` code.
- The GPU-budget-capping step (`target := w.remaining - mean`,
  `fairShareCap`) *reads* `fsv` as a capacity-unit quantity that directly sizes
  replicas — so the units mismatch is load-bearing, not cosmetic: mixing models
  of very different absolute scale (e.g. a huge model with many replicas vs. a
  tiny one) will systematically starve the small model at any given coverage
  ratio, purely as an artifact of scale.

**Verdict for §3: the user's suspicion is confirmed on the object-level
question.** `fairShareValue` does not compute, and cannot be reinterpreted as
computing, "same average coverage across models, skewed only by Priority." It
computes "same average absolute remaining demand, skewed by Priority and
Score." These coincide only when all competing models have equal `TotalDemand`
— true in most of the existing tests (see §5) — and diverge whenever demand
scales differ, which is the realistic multi-model case. Whether this rises to
"the code has a bug" depends on which specification you hold it to: it is
*correct relative to the only design ever written down* (§2b — equalize
absolute `RequiredCapacity`), and it is *a defect relative to the "fair share"
name and the coverage-based fairness definition the user (and most
resource-allocation literature) expects*.

---

## 4. Is the cross-analyzer summation ever exercised outside a single length-1 slice?

### 4a. Production: `composeAnalyzerResults` always collapses to length 1

`internal/engines/steadystate/engine_v2.go:207-214`:

```go
// composeAnalyzerResults reduces the raw per-analyzer results collected by
// runAnalyzersAndScore into the single composite metric fed to the
// capacity-build step (buildNamedResult) and, from there, to the optimizer.
//
// Today it takes saturation's result as-is: when saturation is the only
// entry — the default, and currently the only case exercised in production —
// it is returned unchanged.
func composeAnalyzerResults(baseResults []rawAnalyzerResult) rawAnalyzerResult {
	for _, r := range baseResults {
		if r.name == domain.SaturationAnalyzerName {
			return r
		}
	}
	return baseResults[0]
}
```

And its one caller, `engine_v2.go:174-177`:

```go
composed := composeAnalyzerResults(baseResults)
namedResults := []allocation.NamedAnalyzerResult{
	buildNamedResult(ctx, composed.name, composed.result, config, metaByVariant, composed.scaleUp, composed.scaleDown),
}
```

`namedResults` is a **literal single-element slice**, unconditionally, every
cycle, regardless of how many analyzers actually ran. This is the *only*
production call site that populates `ModelScalingRequest.AnalyzerResults`
(confirmed by `grep -n "AnalyzerResults:" internal/engines/*/engine_v2.go` —
one hit, `engine_v2.go:823`, feeding straight from this `namedResults`). There
is no other engine variant in the current tree (an earlier
`engine_queueing_model.go` referenced in commit messages no longer exists in
this checkout).

**So in every production configuration that exists today, `s` in
`fairShareValue(priority, s, ps, roles)` has `len(s) == 1`, and the `Σᵢ`
in `Σᵢ Scoreᵢ × Σ_role pickerState[i][role]` sums over exactly one term.**
The formula's cross-analyzer behavior — the part the user specifically
questioned ("why would FSV do a score-weighted sum of analyzers") — is
**dead in production today.** It's a real code path with a real config
knob (`config.AnalyzerScoreConfig.Score` per analyzer type), but nothing
currently wires more than one analyzer's result into one model's
`AnalyzerResults` slice, so the sum never actually sums more than one addend
in practice.

### 4b. Tests: exactly one test constructs a length-≥2 slice for `fairShareValue`

Searched every construction of `AnalyzerResults: []NamedAnalyzerResult{...}`
across `internal/` (`grep -rn "AnalyzerResults: \[\]NamedAnalyzerResult{"`).
Every hit but one is a single-element slice (via `withSatEntry`, `.named("")`,
or an explicit one-entry literal). The **sole exception** is
`greedy_score_optimizer_test.go:867-928`, **"T1.4: non-uniform Score across two
analyzers drives fair-share ordering"**:

```go
// Model A has two AnalyzerResults:
//   saturation: Score=1.0, RC=20000
//   throughput: Score=2.0, RC=20000
//   fsv(A) = 1.0 × (20000×1.0 + 20000×2.0) = 60000
AnalyzerResults: []NamedAnalyzerResult{
	rA.named("saturation").withScore(1.0),
	// throughput shares rA's variant capacity for simplicity;
	// its RC signal adds to the fair-share weight.
	(&satEntryFixture{RequiredCapacity: 20000}).named("throughput").withScore(2.0),
},
```

This is a hand-built, synthetic fixture — the comment admits *"throughput
shares rA's variant capacity for simplicity"*, i.e. it isn't modeling a
plausible real analyzer output, just exercising the summation arithmetic. Its
origin (per §2b's git archaeology) is the B1 commit fixing the Score=0 wiring
bug — **it is a regression/wiring test for `Score` propagation, not a test of
whether cross-analyzer summation is a sound fairness computation.**

**Answer to §4, precisely as asked:** yes, confirmed — in every test and every
production configuration that exists today, the summation in `fairShareValue`
is over a slice of length 1, *except* for this single synthetic unit test
(T1.4), which exists to verify Score-weight arithmetic mechanically, not to
validate that summing multiple analyzers' absolute demands is the right way to
combine their opinions. The prior investigation's §4 conclusion
("Score-weighted composition is validated today only as a component of a
cross-MODEL sort/budget scalar... never as a way to combine disagreeing
analyzers' ... estimates") is corroborated and sharpened here: it's not just
"never used for per-variant blending" — the cross-analyzer sum inside
`fairShareValue` itself has never executed on production data, full stop.

---

## 5. What tests validate the fairness property — versus tests that just check the arithmetic?

Every test in the `"Multi-Model Fair-Share"` and `"Score-Based Priority"`
`Context` blocks (`greedy_score_optimizer_test.go:134-276`, `757-929`) was read.
None constructs a scenario with **different absolute demand but matched
coverage ratio** and asserts equal treatment — the scenario that would test the
user's fairness property. Every one either:

**(a) Tests "most starved by absolute demand wins," which is the opposite
framing from the user's coverage-based fairness:**

- `"should give GPUs to most starved model first"` (:136-177): `RC_A=50000`
  vs `RC_B=10000` (5× apart), single variant each with *identical*
  `PerReplicaCapacity=15000` for both — meaning `TotalSupply` for each model
  before this cycle is implicitly equal-ish (1 replica each) and their coverage
  ratios (`demand/(demand+RC)`... not directly given, but with matched PRC and
  matched starting replicas, coverage is *not* matched — A's absolute deficit
  is what's being tested). Asserts A gets 3 added replicas, B gets 1 — exactly
  proportional to absolute `RequiredCapacity`, not to coverage. Test name
  itself say the quiet part out loud: *"most starved" = highest absolute
  RC*, which is precisely the framing the user is challenging.
- `"should verify 3-model walkthrough from design doc"` (:179-233): `RC =
  50000/30000/10000` → replicas `4/3/2`, again scaling with absolute RC.
  (Note: despite the name, no design doc containing this specific "3-model
  walkthrough" was found in `docs/design/` — the reference in the test's
  comment appears to be to institutional/PR-review knowledge, not a checked-in
  doc; this doc's §2a search turned up no such walkthrough.)
- `"should distribute evenly with equal RequiredCapacity"` (:235-275): both
  models have `RC=20000` and identical PRC → get equal replicas (3 each). This
  is the **one case where the absolute-demand metric and the coverage-ratio
  metric necessarily agree** (equal demand → equal coverage-normalizing
  divisor cancels out identically) — so it validates nothing about *which*
  fairness definition is in play; it only shows the tie-break behaves
  sensibly when there's no distinguishing signal either way.
- `"should give GPUs to higher-score model first"` / T1.3 (:759-865): both
  models have **identical `RequiredCapacity=20000`**; only `Priority` differs.
  Again the equal-RC coincidence masks the absolute-vs-coverage question
  entirely — this test would produce the identical assertion under either
  fairness definition.
- T1.4 (:867-928): as discussed in §4b, tests Score-weighted arithmetic
  summation across two analyzers on one model, not a fairness property across
  models.

**(b) Tests that just check the formula's arithmetic in isolation, no fairness
claim:**

- `"computeMean should return average of remaining"` / `"...0 for empty
  slice"` (:1143-1158) and `"sortByRemainingDesc should sort descending"`
  (:1256-1273) test the two helper functions directly against hand-fed
  `remaining` values — pure unit tests of arithmetic, silent on what
  `remaining` should mean.

**Net finding for §5:** there is **no test anywhere in the suite that
constructs two models with different absolute demand but the same coverage
ratio and asserts they receive equal (or Priority-proportional) treatment** —
the exact property the user says "fair share" should have. Every existing
"fairness" test either (a) uses equal absolute demand, where the two
candidate definitions of fairness coincide and the test can't distinguish
them, or (b) deliberately varies absolute demand and asserts proportional
response to *that*, which is a positive test of the current (absolute-demand)
behavior, and simultaneously would be a **failing test under the user's
proposed coverage-based redefinition** — i.e. `"should give GPUs to most
starved model first"` is not a neutral fixture; it is a checked-in
specification that the current absolute-demand behavior is correct, and
changing to coverage-ratio semantics would have to knowingly break it (or
prove that in that specific fixture's numbers, coverage happens to agree with
absolute demand — worth checking before assuming the test would break, but at
minimum the test was never designed to be a coverage-ratio check and doesn't
document a coverage-ratio equivalence).

---

## Direct answers to the five investigation questions

1. **Read in full** — done; see §1. `fairShareValue` sums absolute per-role
   `RequiredCapacity`-derived quantities across analyzers, weighted by `Score`,
   multiplied by `Priority`; `fairShareScaleUp` runs max-min water-filling
   directly on that sum (equalizes it to the mean, most-starved-first); the
   magnitude directly bounds replica counts via `target := remaining - mean`
   and `fairShareCap := ceil(target / PRC)`.
2. **Design rationale** — none exists for the coverage-ratio alternative; none
   exists arguing FOR absolute-demand equalization either. Git history
   (`a16e2f09` #771) shows the metric was simply `RequiredCapacity` from day
   one ("most starved model gets GPUs first"), and `Priority`/`Score` were
   layered on later (`09e1c386` #1246) as multiplicative/additive knobs on that
   same absolute quantity, never as part of a deliberate fairness-definition
   decision.
3. **Coverage match/contradiction** — contradiction, confirmed by worked
   example in §3b: two models at identical 80% coverage but different absolute
   scale receive wildly unequal GPU shares under the current formula, which
   the coverage framing would call unfair. The coverage ratio (`n×PRC/demand`)
   does exist in this codebase, as `utilByRole` inside
   `allocateForModelPaired` — but scoped within one model's per-iteration
   allocation, never surfaced into the cross-model `fairShareValue`.
4. **Cross-analyzer summation in practice** — confirmed precisely: `len(s) ==
   1` in every production code path today (`composeAnalyzerResults` always
   collapses to one entry before the optimizer ever sees it), and in every
   test except one hand-built synthetic fixture (T1.4) that exists to verify
   `Score`-weight arithmetic, not to validate cross-analyzer combination as a
   fairness computation.
5. **What's tested vs. assumed** — every "fair share" test either uses equal
   absolute demand (masking which fairness definition is being validated) or
   directly asserts proportional-to-absolute-demand behavior (which is the
   thing under dispute, checked in as the expected/correct behavior). No test
   validates the "equal coverage → equal treatment" property; that property is
   assumed by documentation prose ("fair-share") and contradicted by the
   `RequiredCapacity` walkthrough test's own name and assertions.
