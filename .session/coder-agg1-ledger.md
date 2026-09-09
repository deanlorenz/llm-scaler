# coder-agg1 ledger

Session: coder-agg1. Mission: composite-analyzer. Role: coder. Branch: composite-analyzer
(same-worktree). Task: `.session/task-coder-agg1.md`. Spec: `.session/spec.md` v8.

## Pre-work verification

- `git rev-parse --show-toplevel` / `git branch --show-current` confirm this worktree, branch
  `composite-analyzer`. OK.
- Read CONVENTIONS.md, conventions/coder.md, spec §1-§2 (per task instructions, stopped before
  §10 as instructed).
- Read context files: aggregation.go, optimizer_interfaces.go, domain/analyzer.go, engine_v2.go
  (lines 1-430ish, 690-1070ish covering hasSaturationResult:722, collectV2ModelRequest:797,
  buildNamedResult/buildCapacities:821-905, logAnalyzerResult:1051, recordAnalyzerMetrics:222).
- Dispatched a research subagent (read-only) to confirm current state before writing any code:
  - `allocation.ResultIsInformative` is **already** a real, exported, compiling function in
    `internal/engines/allocation/analyzer_helpers.go:53-65` — NOT confined to the dead
    `multi_backup` file. Good: step 3 doesn't need to "revive" anything, just reuse it directly.
  - `prcForVariant` likewise already compiling, unexported, in `analyzer_helpers.go:87-98`.
  - `multi_backup/analyzer_helpers_multi.go` (dead, `//go:build ignore`) holds the *slice-based*
    multi-analyzer patterns worth porting logic from: `safeRemovalReplicasForRole` (min-of-floor
    across live analyzers, safety floor via `found`/`smallest<0`→0) and `needsScaleDownForRole`
    (all-agree veto + `liveCount > 0` safety floor). These are the patterns item 5/6 should mirror
    for `Agg_N`/`Agg_Spare`-style fallback and all-agree logic — logic only, file untouched.
  - Step-10 query-API site line numbers verified NOT drifted: `rescale.go:606` (ceil/rounding),
    `rescale.go:587-591` (demand fallback read), `cost_aware_optimizer.go:309` (RC/SC per-role
    fallback read) all match spec citations exactly.
  - `roleBottleneckReplicas` lives at `analyzer_helpers.go:190-199` (single-entry ceil site).
  - No existing `DecisionPath` type/field anywhere; A19' composite decision-path field is a new
    concept, not a rename of anything existing.
  - "3 skipped multi-analyzer tests" (item 9) most likely = the trio in
    `engine_v2_population_test.go:131,155,179` (Score population / defaulting / per-analyzer
    threshold override) — these are inside a `runAnalyzersAndScore config-bridge` Describe block
    with fixtures already built, gated on `Skip()` for the same root cause. There are 6 other
    skip sites elsewhere sharing the same root cause but the task's "3" phrasing matches this
    trio most precisely. Will confirm scope when reaching item 9; flag here now in case it turns
    out the task means a different trio.

## Checklist progress

1. [x] Demand accessor — `demandForRole` in `internal/engines/aggregation/demand.go`, tests in
   `demand_test.go` (same-package, unexported access; merged into the existing `TestAggregation`
   Ginkgo suite rather than defining a second `RunSpecs` call, which Ginkgo rejects within one
   package — fixed after first test run failed with "calling RunSpecs more than once").
   `make lint` clean. Commit `4ac16404`.

8. [x] Model-level cross-role coverage — `internal/engines/aggregation/model_coverage.go`:
   `modelCoverageFromRoles(byRole map[string]float64) (coverage, ok)` = `min(cov(prefill),
   cov(decode)) + cov(both)`. A role's ABSENCE from the map (not a stored 0) means undefined
   coverage, using `minOfDefined` from step 2 so an undefined role never enters the `min` as a
   spurious 0 (test 23/A14). Kept in `aggregation`, not wired as an eager composite field —
   per D4#9/spec §5.4, it lives only in the query API (step 10), which will build it from
   whatever `PRCCom`/coverage-per-role values it has in hand at query time. Tests cover test 5
   (both disaggregated and non-disaggregated cases) and the absent-role part of test 23.
   Commit pending.

7. [x] Derivation chain N -> RC/SC — `internal/engines/aggregation/prc_com.go`: `PRCCom(satDemand,
   role, nCom, nComOK) (prc, ok)` = `D_sat[role]/N_com(SO)`, undefined when `N_com` isn't ok/<=0 or
   `D_sat` has no defined demand for the role at all (A4), defined-and-zero for a real zero demand
   (never manufactured). Deliberately does NOT build a full derived `NamedAnalyzerResult` here —
   that's composite construction (item 9), which will build a synthetic `VariantCapacities` with
   `PerReplicaCapacity = PRCCom(...)` per SO and run it through the *existing*
   `aggregation.SumTotalSupply`/`SumTotalAnticipatedSupply`/`buildRoleCapacities`/
   `applyUniversalThreshold` pipeline (confirmed these are the exact "existing formulas" the spec
   means — read `applyUniversalThreshold` at `engine_v2.go:532`, verbatim `RC = max(0,
   TotalDemand/scaleUp − TotalAnticipatedSupply)` / `SC = max(0, TotalSupply − TotalDemand/scaleDown)`,
   confirming no new RC/SC arithmetic is needed anywhere). No `ceil()` in production code here —
   the self-consistency assertion (`ceil(D_sat[role]/PRC_com(SO))` recovers `N_com(SO)`) is a test
   only, per spec §5.3 ("asserted in tests and logged in production" — logging happens in item 11's
   observability pass, since it needs the composite to exist first). The shared production `ceil()`
   helper for actual replica-rounding call sites is item 10's scope (query API), not this item's —
   this item's "ceil() only here" instruction (A2) is about *where in the pipeline* rounding may
   happen (nowhere upstream of a final replica count), not about building the helper itself.

   Also closed test 4 (disaggregated, per-role — a higher contributor for one role must not affect
   the other) and the read-half of test 25 (non-disaggregated layout composes correctly through
   `PRCCom`) here, since both are `AggN`+`PRCCom`-level facts that fit naturally beside test 12's
   round-trip tests; the write-back-in-the-same-layout half of test 25 is composite construction
   (item 9). Commit pending.

6. [x] Gate repair — `internal/engines/allocation/composite_signal_gate.go`:
   `HasUsableCompositeSignal(composite) bool` = `composite.Result != nil && ResultIsInformative(composite)`
   (mirrors the exact standard every analyzer is held to via `eligible()`; A11's own wording —
   "test what it actually needs: that the composite carries a real capacity signal" — is precisely
   this). Wired into `hasSaturationResult` at `engine_v2.go:721` (kept the name — it documents the
   call sites' original intent as a per-cycle measurement gate; the two callers,
   `computeCurrentGPUUsage`/`computeCurrentGPUUsageByNamespace`, only ever use it as a boolean
   short-circuit and touch nothing else on `CompositeSignal`, confirmed by research agent, so this
   was a safe, fully localized swap).

   **Caught a second real bug while wiring this in**: `HasUsableCompositeSignal` is *stricter*
   than the old bare `Result != nil` check (it also requires informativeness) — correctly so, per
   A11 — but this broke 2 pre-existing tests in `engine_v2_quota_test.go` whose shared
   `managedRequest` fixture built `Result: &domain.AnalyzerResult{}` (empty, zero
   `VariantCapacities`), which used to slip past the old weak guard. Fixed the fixture (not my
   predicate) to include one realistic `VariantCapacity` with a real `Reason`, since an empty
   result genuinely carries no capacity signal — the old guard's looseness was exactly what the
   survey flagged (finding 2: seven independent, unequally-strict nil checks). Ran the FULL
   non-e2e repo test suite (`go test $(go list ./... | grep -v /test/e2e)`) after this fix to
   confirm no other fixture relied on the same gap — all green.

   **Seven-plus-one site survey** (`.session/survey-zero-signal.md`) re-verified against current
   source via research agent: the other 6 nil-check sites (`variant_records.go:80`,
   `rescale.go:345`, `rescale.go:529`, `analyzer_helpers.go:142`, `cost_aware_optimizer.go:159`,
   `greedy_score_optimizer.go:63` — line numbers drifted slightly from the survey's own citations,
   confirmed via research agent, but all are still plain `Result == nil`/`!= nil` checks with no
   name comparison) are genuinely already-correct per survey conclusion #1 — **left untouched**,
   per the task's explicit instruction to say so rather than touch sites that don't need it.

   **`wva_model_scaling_blocked` wiring (D2)**: added `constants.ScalingBlockedNoCompositeSignal`
   ("no-composite-signal") + new ownership slice `constants.ScalingBlockedReasonsSignal`
   (`internal/constants/metrics.go`), and one new call site in `engine.go`'s per-model loop
   (`optimizeV2`, right after `collectV2ModelRequest` succeeds, before appending to `requests`) —
   publishes unconditionally every cycle the composite is built, mirroring
   `applyScaleToZeroEnforcement`'s own "publish before any early return, so this call clears a
   stale reason" convention. This is a **new** call site, not a rename of `applyScaleToZeroEnforcement`
   itself: that function operates on `decisions []domain.VariantDecision` (post-optimizer,
   per-model) and its "empty-decision return" is unrelated to composite-signal absence, which is
   known earlier, at collection time, before the optimizer runs at all.

   **Flagged, not touched**: research turned up two more genuine `domain.SaturationAnalyzerName`
   identity checks in production code beyond `hasSaturationResult` — `composite_decision.go:81`
   (my own step 5 code, distinguishing saturation's contribution for the fallback rule) and
   `engine_v2.go:163` (skip re-running saturation since it's built first, unconditionally). Neither
   is a `CompositeSignal.Name` check (the thing §8 renames) — both are legitimate uses of
   saturation's *analyzer* identity, which is not being renamed. PR #34's claim ("zero references
   to `SaturationAnalyzerName` remain in production optimizer code") is accurate for
   `CompositeSignal.Name` dependencies specifically, not for "does any code know saturation's
   name" — noting this so it isn't mistaken for a gap I missed.

   Tests: `composite_signal_gate_test.go` (unit, covers test 8d, test 14, informativeness edges),
   `engine_v2_quota_test.go` new `Describe` block (covers test 14 and 8d at the
   `computeCurrentGPUUsage`/`ByNamespace` level — the actual quota-guard call sites), and
   `engine_signal_blocked_wiring_test.go` (metric-plumbing level, modeled directly on the existing
   `engine_scaling_blocked_wiring_test.go`'s "leaves the wake reason alone" pattern — deliberately
   NOT driving the full `optimizeV2` loop end to end, since that belongs with test 15/item 9's
   end-to-end composite test). `make lint`/`gofmt` clean; full non-e2e `go test` sweep clean.
   Commit pending.

5. [x] Fallback chain + decision path — `internal/engines/allocation/composite_decision.go`:
   `resolveSO(entries, variant) soDecision` with `DecisionAgree/Single/SatFallback/NoSignal`
   constants (`C0-agree`/`C1-single`/`C2-sat-fallback`/`C4-no-signal`; no `C3-default-prc` per the
   task's explicit instruction not to invent one). **Caught and fixed a real bug in my own first
   draft during test-writing**: my first implementation put saturation into the same "eligible
   analyzers with a defined N(SO)" bucket as everyone else (a literal reading of the spec's
   3-line pseudocode), which made a sat-only SO report `C1-single` instead of `C2-sat-fallback` —
   contradicting the spec's actual instruction ("Sat does not participate unconditionally. Only
   if no other signal, as fallback" and the `C2-sat-fallback` definition itself: "no OTHER
   contributor for this SO"). Fixed by tracking saturation's contribution separately: it only
   joins the aggregation (as an ordinary, unprivileged voice, per A3/no-floor) once at least one
   non-sat analyzer already qualifies as a contributor; otherwise it is either the sole fallback
   (C2) or, if not itself eligible/defined either, no-signal (C4). 5 tests initially failed on
   this exact distinction (test 8, 8a, 8b, and the two eligibility-gating tests 6/7, which all
   rely on sat being the *only* one with a defined N and therefore expect C2, not C1) — this is
   precisely the kind of case the task file wanted me to get right rather than paper over.
   Lives in `allocation` (needs `NamedAnalyzerResult`, `Live`, `Name`), imports `aggregation.AggN`
   — new one-directional edge `allocation` → `aggregation`, no cycle (`aggregation` still imports
   nothing from `allocation`). Tests cover spec §9 items 3, 6, 7, 8, 8a, 8b, 8c, 9, 10, 11.
   `make lint`/`gofmt` clean on new files (baseline pre-existing issues elsewhere unaffected).
   Commit pending.

4. [x] Per-SO N and AggN — `internal/engines/aggregation/replicas_needed.go`:
   `replicasNeeded(result, variant) (n, ok)` is `N_i(SO)`; `AggN(results, variant) (n, ok)` is
   `Agg_N`, a pure max via `maxOfDefined` (no Score param at all — Score cannot leak in through
   this seam by construction). "SO" = variant name within one model's composition (confirmed by
   re-reading spec §2.4 "PRC is per SO (implying model, variant, role)" — no other concrete type
   exists in the codebase for "SO"; `VariantCapacity.VariantName` + its `Role` is the SO). Kept
   both functions in `aggregation`, not `allocation`: they only need `*domain.AnalyzerResult`, no
   `NamedAnalyzerResult`/`Live` — eligibility filtering is the caller's job (step 3's `eligible()`,
   applied before building the `[]*domain.AnalyzerResult` slice passed in), consistent with "AggN
   aggregates whatever it's given" being pure. Tests cover spec §9 items 2, 3, 9, 13, 17-21 (test
   17 zero-demand-flows-through, 21 PRC>0/demand==0 legal, both landed here since they're
   `replicasNeeded`-level facts, not composite-level). Commit pending.

3. [x] Eligibility — `eligible(nr) bool` in `internal/engines/allocation/composite_eligibility.go`
   (not `aggregation` package: needs `NamedAnalyzerResult`/`ResultIsInformative`/`Live`, all
   `allocation`-owned; `aggregation` does not currently import `allocation` and I did not want to
   introduce that edge for one predicate when `allocation` already owns `ResultIsInformative`
   right next to it in `analyzer_helpers.go`). Confirmed `ResultIsInformative` is already real,
   exported, compiling code in `allocation/analyzer_helpers.go:53` (not confined to the dead
   `multi_backup` copy) — so this item is a straight composition, no porting needed. Tests in
   `composite_eligibility_test.go` using the existing `makeNamed` fixture helper from
   `analyzer_helpers_test.go`. Commit pending.

2. [x] Undefined-value type — `internal/engines/aggregation/undefined.go`. Decided against a new
   named type: kept the plain `(value float64, ok bool)` pair `demandForRole` already established
   (self-describing at call sites, composes with Go multi-return, no wrapper to unwrap). Added
   `maxOfDefined`/`minOfDefined` — the one shared, swappable combination-rule place per A18 that
   step 4 (`Agg_N`) and step 6/step 5.6 (`Agg_Spare`/all-agree gate) will call into, so undefined
   contributors are skipped identically everywhere rather than each aggregator reinventing the
   skip logic. `make lint` clean on new files (repo has 4 pre-existing staticcheck SA5011 findings
   in unrelated files — `locator_test.go`, `loader_test.go` — confirmed present on HEAD before my
   changes via `git stash`; not mine to fix, out of scope).

