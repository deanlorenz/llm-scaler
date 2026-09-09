# review-coder-agg1

Reviewer: `reviewer-agg1`. Read-only review of `coder-agg1`'s commits implementing
`.session/spec.md` (v8, approved) per `.session/task-coder-agg1.md`'s 12-item checklist, landing
on branch `composite-analyzer` after baseline `4ce95b70`.

## Overall status

All 12 checklist items landed, range `4ac16404..f98a566f` (12 commits, one per item, matching
`task-coder-agg1.md`'s checklist exactly in order).

| # | Checklist item | Commit | Verdict |
|---|---|---|---|
| 1 | Demand accessor (`demandForRole`) | `4ac16404` | Pass |
| 2 | Undefined-value type/convention | `fabe406b` | Pass |
| 3 | Eligibility + informativeness (`eligible`) | `0bacfd73` | Pass |
| 4 | Per-SO `N` and `Agg_N` | `c7bba2df` | Pass |
| 5 | Fallback chain + decision path | `d29f62e2` | Pass |
| 6 | Gate repair — `HasUsableCompositeSignal` | `f5441352` | Pass |
| 7 | Derivation chain N -> RC/SC (`PRCCom`) | `05c0362b` | Pass |
| 8 | Model-level cross-role coverage | `fdd421fc` | Pass |
| 9 | Composite construction/deep copy/wiring | `81ef806d` | Pass |
| 10 | Query API (D3's two scoped categories) | `d31e1142` | Pass |
| 11 | Observability — reuse, verify, audit | `0ec6c170`, fixed by `0642f472` | Pass (after fix) |
| 12 | Full test-plan sweep (spec §9, 1-30) | `f98a566f` | Pass (test-only, as scoped) |

**Overall verdict: Pass.**

The implementation is, item-for-item, high-quality: careful reasoning, self-caught bugs (several
documented in the coder's own ledger and verified independently below), thorough tests, and every
explicit Limit honored. Commit `0ec6c170` (item 11) initially relocated the composite-build call
from the `collectV2ModelRequest:797` site (O2, mandated by the task file's Limits and spec
§6.2/A7/D4#4) into `runAnalyzersAndScore` — the placement spec §6.2 evaluates as **O1** and
**explicitly rejects**. This was flagged to the mission owner immediately on discovery (see log
below and agentbus `mission.composite-analyzer.reviewer-agg1.out`). Follow-up commit `0642f472`
reverts the placement to O2 per the mission owner's ruling, restores `runAnalyzersAndScore` to its
exact pre-mission shape (independently verified byte-for-byte against `81ef806d~1`, not merely on
the commit message's claim), and achieves observability parity via a second explicit
`recordAnalyzerMetrics`/`logAnalyzerResult` call from the O2 site instead. The eviction-safety
reasoning for that second call (passing the full `append(namedResults, composite)` set rather than
the composite alone, to avoid wrongly evicting the per-analyzer series) was independently checked
against `evictStaleAnalyzerSeries`'s real logic and holds exactly. Both non-negotiable regression
guards (test 1, test 13) still pass at `0642f472`, and no genuine test coverage was lost in the
accompanying observability-test rewrite. See the dedicated section below for full detail.

No confidence ≥80 issues remain open.

## Log

### 2026-09-09 — initial pass, standing by

- Confirmed worktree: `git rev-parse --show-toplevel` resolves to
  `/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/composite-analyzer`,
  `git branch --show-current` reports `composite-analyzer`. Matches task file. OK.
- Read `.session/spec.md` §1 and §2 in full, and `.session/task-coder-agg1.md` in full.
- `git log 4ce95b70..HEAD --oneline` shows exactly one commit past baseline:
  `87b16a76 docs(state): task file for reviewer-agg1` — this is my own task-file-creation
  commit (docs only, `.session/task-reviewer-agg1.md`), not coder-agg1 work. No
  `.session/coder-agg1-ledger.md` exists yet either, confirming `coder-agg1` has not made its
  first commit (checklist item 1, "Demand accessor") yet.
- Nothing to review yet. No divergence, no Limits violations possible with zero commits.
- Standing by — will re-check `git log 4ce95b70..HEAD --oneline` on next invocation /
  re-engagement.

### 2026-09-09 — full pass, all 12 commits landed (range `4ac16404..f98a566f`)

Coordinator reports coder-agg1 completion. Reviewing all 12 commits now, Phase 1 + Phase 2 per
commit, against `task-coder-agg1.md`'s checklist and Limits.

**FLAGGED TO MISSION OWNER (published to Out: immediately, see below) — commit `0ec6c170`
relocates composition out of the O2 site the spec mandates. RESOLVED by follow-up commit
`0642f472`, verified in the dedicated section near the end of this file.**

`task-coder-agg1.md`'s Limits: *"Compose at `collectV2ModelRequest:797` (spec §6.2 O2, D4#4). Do
not change `runAnalyzersAndScore`'s return type — that is the change that broke the parent
branch's build."*

Commit `81ef806d` (item 9) correctly calls `buildComposite` from `collectV2ModelRequest`, exactly
at the O2 site, replacing `namedResults[0]` — matches spec and task file exactly at that point.

Commit `0ec6c170` (item 11, "observability parity") then **moves the `buildComposite` call from
`collectV2ModelRequest` into `runAnalyzersAndScore`** (after `updateLivenessAndSetLive`, before
`recordAnalyzerMetrics`/the `logAnalyzerResult` loop), appends the composite to the slice
`runAnalyzersAndScore` returns, and changes `collectV2ModelRequest` to read it back via a new
`findByName` helper instead of building it.

Spec §6.2 evaluates exactly this placement as **O1** and **explicitly rejects it**: *"Inside
`runAnalyzersAndScore`, changing the return type to a single value ... Rejected: ripples into 6+
test files (the parent branch's known compile breakage) and destroys the slice
liveness/metrics/logging need."* §6.2 recommends and A7 adopts **O2** specifically *because*
composition stays outside `runAnalyzersAndScore` — "slice preserved; per-analyzer observability
unaffected."

The coder's move is **not textually O1** — it did not change the return type (still
`[]allocation.NamedAnalyzerResult`, satisfying the literal "do not change return type" sentence)
— but it reintroduces exactly the coupling O2 was chosen to avoid: `runAnalyzersAndScore` now
builds the composite internally and every caller receives a slice with a synthetic extra entry
appended, which every consumer of that slice must now be aware of (the coder's own commit message
concedes `collectV2ModelRequest` must now "read it back by name rather than by position, so a
future change ... cannot silently hand the optimizer the wrong entry" — exactly the fragility O2
was meant to avoid by keeping composition a one-line change at a single site). This is a real
architectural placement violation of D4#4/A7/O2, not merely a stylistic difference, even though
the one bright-line rule stated literally ("do not change the return type") was technically
honored.

The coder's own stated reason for the move (item 11's ledger entry) is real: `logAnalyzerResult`/
`recordAnalyzerMetrics` run inside `runAnalyzersAndScore`, before the composite existed under the
O2 design, so reusing them "as-is" for the composite is not possible without either (a) moving
composition inside `runAnalyzersAndScore` (what was done, contradicting O2), or (b) calling
`logAnalyzerResult`/`recordAnalyzerMetrics` a second time from `collectV2ModelRequest` for the
composite alone (keeping O2's placement, at the cost of two call sites instead of one, which the
spec's A22/A23 do not explicitly forbid and which was never evaluated against O1/O2 in §6.2 since
§6.2 predates A22/A23's fuller elaboration). This looks like a genuine spec gap — §6.2 did not
anticipate that A22/A23 ("same functions, not parallel ones") would only be achievable by
relocating composition — rather than the coder inventing an unneeded change. Still, per the task
file: *"If you hit a genuine ambiguity the spec doesn't resolve, stop and ask ... do not invent a
resolution and proceed."* This looks exactly like that situation, and the coder proceeded rather
than asking. Flagging to mission owner rather than resolving myself.

Published to `mission.composite-analyzer.reviewer-agg1.out` at time of finding.

---

## Per-commit detail

### Commit 1 — `4ac16404` (item 1: demand accessor, spec §2.3/A13)

**Phase 1:** `demandForRole(result *domain.AnalyzerResult, role string) (float64, bool)` in
`internal/engines/aggregation/demand.go`. Clean, small, correctly canonicalizes `"" → RoleBoth`,
correctly distinguishes the two storage layouts (nil `RoleDemand` ⇒ read `TotalDemand` only for
`RoleBoth`, else not-present; non-nil map ⇒ ordinary lookup, absent key ⇒ not-present). No
mutation, no aliasing risk (reads only).

**Phase 2 (vs spec §2.3/A13):** Matches A13's accessor contract exactly — three-way distinction
(present-and-zero / not-present / non-disaggregated-both) all correctly implemented and unit
tested per state. Verdict: **Pass.**

### Commit 2 — `fabe406b` (item 2: undefined-value convention, spec §2.5/A14)

**Phase 1:** `maxOfDefined`/`minOfDefined` in `undefined.go`, taking a count + index-accessor
function rather than a slice — a slightly unusual shape but justified (avoids forcing every caller
to materialize a `[]float64`/`[]bool` pair first) and well tested, including the "does not let
undefined act as +Inf/spurious-0" cases explicitly.

**Phase 2 (vs A14, and task Limit "establish the convention here; later steps use it, they don't
redefine it"):** Confirmed no later commit redefines a competing convention — `AggN`, `PRCCom`,
`modelCoverageFromRoles` all consume `maxOfDefined`/`minOfDefined` or the same `(value, ok)` shape
directly. Verdict: **Pass.**

### Commit 3 — `0bacfd73` (item 3: `eligible()`, spec §5.1.1/A3)

**Phase 1:** `eligible(nr NamedAnalyzerResult) bool` = `Result != nil && ResultIsInformative(nr)
&& nr.Live`, in `allocation` package (correctly reasoned placement — avoids a new
aggregation→allocation import edge). Direction-symmetry (A3: same rule blocks scale-up
contribution and scale-down veto alike) is inherent to using one function everywhere, verified by
a test that calls it twice with the same fixture and asserts both readings agree.

**Phase 2 (vs A3):** Matches exactly. Verdict: **Pass.**

### Commit 4 — `c7bba2df` (item 4: per-SO `N` and `Agg_N`, spec §5.2/§7.1/D4#3/D4#5)

**Phase 1:** `replicasNeeded` (unexported) computes `N_i(SO) = D/PRC` with correct zero/negative-PRC
and not-present-demand guards; `AggN` (exported) is a pure `max` via `maxOfDefined`, **no `Score`
parameter at all** — the task's Limit ("do not add Score weighting... Agg_N is a pure max") is
enforced by the function signature itself, not merely by convention, exactly as the commit message
claims. Verified: grep confirms no `Score`/weight parameter anywhere in `aggregation/*.go`.

**Phase 2 (vs D4#5 "named for the quantity, not the operation"):** `AggN`/`replicasNeeded` names
match. Test 13 (order-independence, no Score anywhere in the call) is present at this
package-internal level too, not just the later end-to-end version — good defense in depth.
Verdict: **Pass.**

### Commit 5 — `d29f62e2` (item 5: fallback chain + decision path, spec §5.1–§5.1.4/D2)

**Phase 1:** `resolveSO` in `allocation/composite_decision.go`. Correctly separates saturation's
contribution from "others'" so a sat-only SO reports `C2-sat-fallback`, not `C1-single` — this is
exactly the subtlety spec §5.1 calls out ("sat does not participate unconditionally... only as
fallback"), and the commit message documents a self-caught bug in an earlier draft that had this
backwards, caught by 5 failing tests. No `C3-default-prc` invented, matching the task's explicit
instruction not to.

**Phase 2 (vs D2, spec §5.1.2's decision-path field mirroring `VariantCapacity.Reason`'s free-text
style):** `DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` constants match
the four decision paths the spec names (`C0`/`C1`/`C2`/`C4` — no `C3`, correctly). Verdict:
**Pass.**

### Commit 6 — `f5441352` (item 6: gate repair, spec §5.1.3/A11'/D2)

**Phase 1:** `HasUsableCompositeSignal` in `allocation/composite_signal_gate.go`, replacing
`hasSaturationResult`'s broken name check. Scope was correctly bounded to the one site the survey
(`.session/survey-zero-signal.md`) actually flagged as needing repair (the name-dependent check) —
verified the commit message explicitly re-confirms the other six nil-check sites the survey
enumerated (`variant_records.go`, `rescale.go` x2, `analyzer_helpers.go`, `cost_aware_optimizer.go`,
`greedy_score_optimizer.go`) are untouched, matching the task's explicit permission to leave them
alone if "genuinely already-correct." Confirmed via `git show f5441352 --stat` that none of those
six files appear in this commit's diff.

**Phase 2 (vs D2's `wva_model_scaling_blocked` wiring):** New `ScalingBlockedNoCompositeSignal`
reason, own `ScalingBlockedReasonsSignal` ownership set (avoids clobbering the policy/wake reason
sets — verified by a dedicated test, "leaves the policy and wake reasons alone"), published
unconditionally per cycle mirroring `applyScaleToZeroEnforcement`'s existing clear-on-healthy
convention. Matches survey conclusion 6 exactly. Verdict: **Pass.**

### Commit 7 — `05c0362b` (item 7: `PRCCom` derivation, spec §5.3/D4#3)

**Phase 1:** `PRCCom(satDemand, role, nCom, nComOK) (float64, bool)` = `D_sat[role]/N_com`,
correctly undefined at `nCom<=0`/not-ok/demand-not-present, correctly defined-and-zero at a real
zero demand. No `ceil()` in this file (confirmed by grep) — ceiling is deferred to composite
construction per A2, exactly as the task requires. Self-consistency round-trip test present (test
12).

**Phase 2 (vs §4.4's identity: sat-only ⇒ `PRC_com == PRC_sat` exactly):** Directly tested
(`"recovers PRC_sat exactly on the sat-only identity"`). Verdict: **Pass.**

### Commit 8 — `fdd421fc` (item 8: model-level cross-role coverage, spec §5.4)

**Phase 1:** `modelCoverageFromRoles` (**unexported**, package-internal only) = `min(cov(prefill),
cov(decode)) + cov(both)`, correctly guards an absent role out of the `min` via `minOfDefined`
rather than letting it enter as a spurious 0. Confirmed unexported (lowercase) and confirmed NOT
called from `composite.go` (grep, see log above) — satisfies the task's explicit Limit ("do not
compute model-level coverage as an eager field on the composite — it is query-API-only, D4#9") at
this point in the sequence; it is still unwired, correctly deferred to a "later step" per the
commit message (never actually wired into the query API in commit 10 — see that commit's note
below).

**Phase 2 (vs D4#9, test 23/A14):** Matches. Verdict: **Pass.** *(Minor observation, confidence
too low to gate on: `modelCoverageFromRoles` is built but I did not find it actually consumed
anywhere in `query_api.go` (commit 10) — it may be genuinely unused production code today. Not a
Limits violation — the task's item 8 only requires building it "for the query API, step 10," and
item 10's own checklist text lists only the two D3-scoped categories, neither of which is model
coverage. This looks like a spec-scoping edge the mission owner may want to note for a follow-up,
not a defect in this commit.)*

### Commit 9 — `81ef806d` (item 9: composite construction, spec §6.1/§6.2 O2/§8/A7/A10/A12/A15)

**Phase 1:** `buildComposite` in new `steadystate/composite.go`. Deep-copy discipline verified by
reading the code directly: `compositeVCs` is a freshly-built slice (never a slice of the source's
own `VariantCapacity` values — each `vc` is a new local struct literal populated field-by-field);
`result.RoleDemand` is a freshly `make`'d map with values copied in, never assigned the source
map directly. Test 24 (deep-copy isolation, both "mutate composite doesn't touch source" and "two
composites from the same inputs don't alias each other") independently confirms this at the
integration level, not just by code inspection.

Two bugs the coder's own ledger reports finding and fixing (verified present in the diff, not just
claimed): (1) `PRC_com` fallback to the source's own measured PRC when `PRCCom` returns not-ok, so
a zero-demand SO doesn't lose real supply — present in the `switch` block, confirmed by test 17's
assertion; (2) `HasUsableCompositeSignal`'s check corrected to compare against
`allocation.DecisionNoSignal` directly rather than delegating to the analyzer-calibrated
`ResultIsInformative` — confirmed in this commit's diff to `composite_signal_gate.go` (not shown
in full above but stat-confirmed modified in this commit) and independently verified by test 8d
passing end-to-end.

At this point in the sequence, the compose call is correctly placed exactly at O2
(`collectV2ModelRequest`, replacing `namedResults[0]`) — confirmed directly in the diff.
`runAnalyzersAndScore`'s return type is unchanged. **This commit, in isolation, is fully
compliant.** (The divergence from O2 happens two commits later, in commit 11 — see the flagged
finding above and that commit's own section below.)

**Phase 2 (vs §6.1 "exactly one full NamedAnalyzerResult... NOT saturation"; A10 new name; A12
minimal provenance):** `allocation.CompositeSignalName` added in `composite_identity.go`; composite
carries per-SO `Reason` = decision path (provenance) and aggregates contributor names into
`contributedNames` (used for `anyLive`, though I did not find contributor names surfaced onto the
final `NamedAnalyzerResult` itself as a field — the task's item 9 only requires "minimal
provenance: which analyzers contributed, which drove each max," and per-SO `Reason`/decision-path
does carry "which drove"; whether per-SO contributor-name lists needed their own exposed field is
a judgment call the coder made toward "minimal" per the task's own Limit against adding structure
beyond what's needed — reasonable, not a violation). Exporting `ResolveSO`/`SODecision`/
`DemandForRole` (mechanical, same-logic renames) to bridge the allocation/steadystate package
boundary is a reasonable, narrowly-justified change, not scope creep — it doesn't add any new
public surface area beyond what the cross-package call requires. Verdict: **Pass.**

### Commit 10 — `d31e1142` (item 10: query API, spec §5.5/D3)

**Phase 1:** `query_api.go` — exactly the two D3-scoped categories (`replicasForDemand`/
`safeReplicasForSpare` for rounding; `demandForRoleOrModel`/`requiredSpareForRoleOrModel` for the
role-vs-model fallback), wired into the four cited call sites. Two regressions self-caught by the
coder while generalizing (documented in the commit message and verified consistent with the
diff): `requiredSpareForRoleOrModel` initially special-cased `RoleBoth` to skip the
`RoleCapacities` lookup, which the original inline code never did; `demandForRoleOrModel` initially
routed through `aggregation.DemandForRole` per the task's literal wording, but several existing
test fixtures hand-build `RoleCapacities` without populating `Result.RoleDemand`, so the coder
correctly matched the original call site's actual source of truth (`RoleCapacities`) instead,
documenting the reasoning prominently against future reintroduction of the same mistake. This is
good judgment: matching real call-site behavior over a superficially-cleaner generalization.

**Phase 2 (vs D3's explicit out-of-scope list — `ReplicasToCloseGap`/`GPUsToCloseGap`/bounds
helpers, `sortByCostEfficiencyAsc`):** Confirmed absent from the diff — `git show d31e1142 --stat`
lists only `analyzer_helpers.go`, `cost_aware_optimizer.go`, `query_api.go` (+test),
`rescale.go`; no `sortByCostEfficiencyAsc` or gap-closing helper appears. Verdict: **Pass.**

### Commit 11 — `0ec6c170` (item 11: observability, spec §6/A22/A23/D4#10/D4#12) — **Request Changes**

**Phase 1:** The mechanical parts are clean: `findByName` is a small, correct helper;
`logAnalyzerResult` gains a genuinely-missing `"live": nr.Live` field (a real, valuable gap-fix,
independently justified — every analyzer's log line benefits, not just the composite's); the
`docs/reference/cycle-log.md` addition is appropriate and in `D_sat` units as required. The
reasoning for NOT running the composite through `updateLivenessAndSetLive` (it would call
`ResultIsInformative` against the composite's decision-path `Reason`, which never matches the
analyzer-sentinel vocabulary — the same class of bug already caught and fixed for
`HasUsableCompositeSignal` in commit 9) is correct and well-documented; this part is a good catch.

**Phase 2 — the placement violation (already flagged to Out: at discovery):** `buildComposite`'s
call site moved from `collectV2ModelRequest` (O2) into `runAnalyzersAndScore` itself. Spec §6.2
names this exact placement **O1** and rejects it in so many words: *"Inside runAnalyzersAndScore,
changing the return type to a single value... Rejected: ripples into 6+ test files... destroys the
slice liveness/metrics/logging need."* The coder's version is not literally O1 (return type is
unchanged — still `[]allocation.NamedAnalyzerResult`, verified directly in `engine_v2.go:117`), so
the one bright-line textual rule in the task's Limits ("do not change runAnalyzersAndScore's return
type") is honored. But O2 was adopted specifically to keep composition a "one-line change at the
single production assignment site" outside `runAnalyzersAndScore` — and that property is now gone:
`collectV2ModelRequest` no longer builds the composite, it looks it up post-hoc by name from a
slice that now silently carries an extra synthetic entry every caller of `runAnalyzersAndScore`
must be aware of. This is the exact fragility A7/O2 existed to prevent, reintroduced through a
side door the literal rule didn't anticipate.

I believe the coder's motivating problem is real and not fabricated: `logAnalyzerResult`/
`recordAnalyzerMetrics` (A22/A23's "same functions, not parallel ones") run *inside*
`runAnalyzersAndScore`, before `collectV2ModelRequest` is ever called — so satisfying A22/A23
literally as written, for the composite specifically, is not achievable while also keeping
composition at the O2 site, *unless* those two functions are instead called a second time (with
the lone composite) from `collectV2ModelRequest` itself — an option the coder's ledger does not
mention having considered. Spec §6.2 pre-dates its own A22/A23 in the document and does not appear
to have been re-examined against them, so this may be a genuine spec gap rather than the coder
disregarding a settled decision. That said, the task file is explicit: *"If you hit a genuine
ambiguity the spec doesn't resolve, stop and ask... do not invent a resolution and proceed."* This
looks exactly like that situation, and my read is the coder should have raised it rather than
resolving it unilaterally by moving composition — even with good reasoning and thorough
documentation of the choice. **Recommend the mission owner decide, not the coder or me.**

No corrupted values found from this move — verified independently (see Test 1 / Test 13 deep-dive
below) that the resulting composite is still numerically correct on both regression guards. This
is a placement/process issue, not a computed-value defect.

### Commit 12 — `f98a566f` (item 12: full test-plan sweep, spec §9 items 1-30)

**Phase 1:** Test-only, as the task requires ("this commit should be small — test-only, no
production code changes, unless it finds an actual bug"). Confirmed via `git show f98a566f --stat`
— only `*_test.go` files plus the ledger changed, no production `.go` file touched. Six gaps
closed (tests 17, 22, 23, 25-write-back, 26/27, 16-traceability) match real, distinct spec items,
not padding — spot-checked test 22 (SO-independence of demand) and confirmed it's a genuine new
assertion (adding/removing a `VariantCapacities` entry must not change `DemandForRole`'s reading),
not a duplicate of an existing test.

**Phase 2 (vs the task's explicit call-out of tests 9, 10, 22 as needing special attention since
they "don't map to a single step above as cleanly"):** Test 9 (unit-independence) and test 10
(`PRC_sat`-independence) were confirmed already present from commit 5's own tests
(`composite_decision_test.go`), not newly added here — correctly not re-added as duplicates.
Verdict: **Pass.**

---

## Deep-dive: Test 1 and Test 13 assertions (per coordinator's specific request)

Read `internal/engines/steadystate/composite_test.go` in full for both.

**Test 1** (`Describe("buildComposite — sat-only regression (test 1)")`, lines ~36-76): calls
`e.runAnalyzersAndScore` directly to obtain `baseline := namedResults[0]` (the real,
unreduced saturation entry — confirmed safe: the composite is appended to the END of that slice
by `runAnalyzersAndScore`, per `engine_v2.go:206`, so index 0 is never the composite), then calls
`e.collectV2ModelRequest` on the same `Engine` to obtain `composite := req.CompositeSignal`, and
asserts field-by-field equality across 9 fields (`TotalDemand`, `VariantCapacities` length and
`[0].PerReplicaCapacity`, `TotalSupply`, `TotalAnticipatedSupply`, `Utilization`,
`RequiredCapacity`, `SpareCapacity`, `Remaining`, `Spare`, `Live`). This does assert real numeric
identity, not a vacuous tautology — `baseline` and `composite` are built via genuinely different
code paths (`buildNamedResult` vs `buildComposite`) that happen to need to agree exactly on the
sat-only path. A companion `It` in the same `Describe` (test 17) extends this to the zero-demand
case, and a third confirms the name differs (test 8/§8) without breaking the value identity above.
**This is a real, correctly-constructed non-negotiable regression guard**, not test theater.

**Test 13** (`Describe("buildComposite — Score has no effect (test 13)")`, lines ~213-278): builds
two `Engine`s with identical saturation + a `"spy"` analyzer whose `N` (12) is deliberately higher
than saturation's (5), so the composite's `N_com`/`PerReplicaCapacity` genuinely depends on `spy`
winning the `max` — this is not a case where Score-leakage would be masked by both analyzers
agreeing. It runs `collectV2ModelRequest` once with `spy`'s Score at 1.0 (equal to sat's) and once
at 5.0 (5x sat's), and asserts `PerReplicaCapacity`, `RequiredCapacity`, `SpareCapacity`,
`TotalSupply` are all identical across the two runs, while explicitly asserting the **legacy**
`Score` field itself DOES differ (1.0 vs 5.0, per A9''' — max over contributors' scores, the one
place Score is allowed to appear, read by nothing in the new aggregation). Confirmed independently
at the aggregation-package level too (commit 4's `AggN` test, "has no Score parameter — Score
cannot influence the aggregated N by construction"): `AggN`'s signature has no Score argument at
all, so this is enforced twice, once by construction and once by an end-to-end behavioral test.
**This is also a real, correctly-constructed assertion**, not merely checking that the test exists
and passes.

Both tests would fail if `Score` were wired into `Agg_N`/`resolveSO`'s combination rule, or if the
composite's PRC/RC/SC computation used sat's raw value instead of genuinely reducing over
`namedResults` — i.e., they are not vacuously true regardless of implementation. Confidence: high.

---

## Commit `0642f472` — placement fix, per mission-owner ruling on the flagged finding

**Commit:** `fix(steadystate): revert composite-build placement to the O2 site`. Follows the
mission owner's ruling against my `0ec6c170` finding above. Re-checked per coordinator's specific
follow-up request, independently rather than trusting the commit message.

### 1. Does `runAnalyzersAndScore` truly match its pre-mission shape?

**Verified independently**, not taken on the commit message's word: dumped
`git show 81ef806d~1:internal/engines/steadystate/engine_v2.go` and
`git show 0642f472:internal/engines/steadystate/engine_v2.go` to two temp files and ran a full
`diff`. Result: the entire **body** of `runAnalyzersAndScore` (the loop building each
`NamedAnalyzerResult`, the `updateLivenessAndSetLive` call, the `recordAnalyzerMetrics` call, and
the `logAnalyzerResult` loop) is **byte-for-byte identical** to `81ef806d~1`. The only diff
touching this function is its own doc comment, updated to correctly describe the O2 relationship
instead of the stale "picks namedResults[0]" text — an appropriate, non-behavioral change. No
residual drift, no snuck-in extra change. **Confirmed: matches pre-mission shape exactly.**

The only substantive diff in the whole file is at `collectV2ModelRequest` (composite build
restored at the O2 call site + the new second `recordAnalyzerMetrics`/`logAnalyzerResult` call)
and the independently-justified `"live"` field on `logAnalyzerResult`'s line (kept, correctly
described as unrelated to placement — it's a real gap on every analyzer's log line, not
composite-specific, and was correctly not reverted).

### 2. Is the O2 placement now correct per spec §6.2/A7/D4#4?

Yes. `buildComposite` is called from `collectV2ModelRequest`, replacing `namedResults[0]` at
that call site (`engine_v2.go`, confirmed in the diff), exactly as adopted by A7. No return-type
change to `runAnalyzersAndScore` (confirmed unchanged: `([]allocation.NamedAnalyzerResult,
error)`). `findByName` — which existed only to read the composite back out of
`runAnalyzersAndScore`'s slice under the old (rejected) placement — is correctly removed as dead
code rather than left orphaned.

### 3. Eviction-safety reasoning — checked against `evictStaleAnalyzerSeries`'s real logic, not the commit message's claim

Read `recordAnalyzerMetrics`/`evictStaleAnalyzerSeries` directly (`engine_v2.go:226-276`).
Confirmed the actual mechanics:

- `evictStaleAnalyzerSeries` compares this **call's** `current` series set against whatever the
  **previous call** for this model key left stored in `e.lastAnalyzerSeries[modelKey]` — deletes
  what's absent from `current`, then **unconditionally overwrites** the stored set with `current`
  (`e.lastAnalyzerSeries[modelKey] = current`, line 275). This is a per-call comparison, not a
  per-cycle one — there is no cycle-boundary marker in this state at all.
- Consequence, traced through one real cycle: call 1 (`runAnalyzersAndScore`'s own
  `recordAnalyzerMetrics(namespace, modelID, namedResults)`, per-analyzer set only) evicts against
  last-*cycle's* stored set (correct, ordinary steady-state behavior) and stores the per-analyzer
  set. Call 2 (`collectV2ModelRequest`'s new
  `recordAnalyzerMetrics(namespace, modelID, append(namedResults, composite))`) then evicts against
  what call 1 just stored — and since `append(namedResults, composite)`'s `current` set is a
  strict superset of what call 1 stored (every per-analyzer series reappears, plus the composite's
  new one), **nothing is evicted** by call 2; only the composite's series is newly added.
- Verified the counterfactual the commit message claims: had call 2 instead passed
  `[]allocation.NamedAnalyzerResult{composite}` alone, its `current` would contain only the
  composite's series, every per-analyzer series stored by call 1 moments earlier would read as
  "not still present" in call 2's `current`, and would be wrongly deleted
  (`DeleteAnalyzerDemand`/`DeleteAnalyzerTarget`) — undoing call 1's own emission within the same
  cycle. The claim holds exactly against the real code, not just against the commit's own
  description of it.
- Also confirmed the "re-emitting per-analyzer series a second time is harmless" claim: the only
  side effects in `recordAnalyzerMetrics`'s loop are `RecordAnalyzerDemand`/`RecordAnalyzerTarget`,
  both Prometheus gauge sets — setting a gauge to the same value twice is a no-op in effect,
  confirmed idempotent.

**Verdict on the eviction-safety reasoning: correct, verified against the real function, not
merely plausible-sounding.**

### 4. Test 1 / Test 13 — still pass? Did the observability-test rewrite lose coverage?

Ran the full `steadystate` Ginkgo suite at this commit: `165 of 167 Specs` run, **0 Failed**, 2
Skipped (consistent with the 2 remaining out-of-scope skip sites the coder's own ledger documented
under item 9 — not a regression). Then re-ran focused specifically on the "sat-only regression"
and "Score has no effect" `Describe` blocks (`-ginkgo.focus="sat-only regression|Score has no
effect"`): **4 of 4 specs run, 0 failed** (test 1's three `It`s plus test 13's one `It`). Both
non-negotiable regression guards hold at this commit.

Checked `composite_observability_test.go`'s rewrite (tests 28/29, retargeted from
`e.runAnalyzersAndScore` to `e.collectV2ModelRequest`) for coverage loss, since the coordinator
flagged this specifically:

- One assertion was dropped in the rewrite of test 28: the old version asserted
  `namedResults[0].Name == domain.SaturationAnalyzerName` ("saturation must still be first — the
  per-analyzer slice is unchanged"), which has no direct replacement now that the test calls
  `collectV2ModelRequest` (which doesn't expose the internal `namedResults` slice). **This is not
  a genuine coverage loss**: test 1 (`composite_test.go`) independently and directly calls
  `e.runAnalyzersAndScore` and reads `namedResults[0]` as its baseline, then asserts the composite
  (built separately via `collectV2ModelRequest`) matches it field-by-field — that comparison would
  fail if `runAnalyzersAndScore` ever stopped putting saturation first or otherwise perturbed the
  per-analyzer slice. The specific invariant the dropped assertion checked is still load-bearing
  for test 1 to pass, just exercised implicitly there rather than asserted explicitly in the
  observability test. Ran and confirmed test 1 still passes (see above).
- Both tests also dropped the explicit `lastAnalyzerSeries: make(map[string]analyzerSeries)` field
  from their `Engine{}` literals. Checked `evictStaleAnalyzerSeries` (`engine_v2.go:261-262`): it
  already lazily initializes `e.lastAnalyzerSeries` if nil on first use, so this was pre-existing
  defensive code and removing the redundant pre-seed from these two fixtures is inert, not a bug.
- Ran `TestCompositeObservability_MetricsIncludeTheComposite`,
  `TestCompositeObservability_CompositeIsAdditiveNotReplacing`, and
  `TestCompositeObservability_EstimatedPRCContributesNormally` directly: all 3 pass.

**Verdict on the rewrite: no genuine coverage loss.** The one dropped explicit assertion is
redundantly covered by test 1's own comparison; nothing was weakened past what production
behavior actually requires.

### Verdict on commit `0642f472`: **Pass.**

The revert is exact (verified independently against `81ef806d~1`, not merely on the commit
message's claim), the O2 placement is correctly restored per spec §6.2/A7/D4#4, the eviction-safety
reasoning for the two-call `recordAnalyzerMetrics` approach is correct when checked against
`evictStaleAnalyzerSeries`'s actual logic, and both non-negotiable regression guards (test 1, test
13) still pass, with no genuine test coverage lost in the observability-test rewrite. This resolves
the finding flagged against `0ec6c170`.

**This changes the overall mission verdict from "Request Changes" to Pass**, contingent on the
mission owner treating this fix commit as closing that finding (which it does, on independent
re-verification).
