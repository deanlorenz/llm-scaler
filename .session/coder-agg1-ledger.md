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

10. [x] Query API, D3's two scoped categories — `internal/engines/allocation/query_api.go`.
    Verified line numbers first (per task's Known-issues flag): `rescale.go:606`
    (ceil/rounding), `rescale.go:587-591` (demand fallback), `cost_aware_optimizer.go:309`
    (RC/SC fallback) all matched spec citations exactly, no drift.

    - Rounding (category 1): `replicasForDemand(demand, prc) int` = `ceil(demand/prc)`, guarded
      to 0 for `prc<=0` or a negative result; `safeReplicasForSpare(spare, prc) int` = the same
      guard, `floor`. Wired into `roleDemandGPUs` (`rescale.go`), `roleBottleneckReplicas` and
      `safeRemovalReplicasForRole` (`analyzer_helpers.go`), replacing each site's own inline
      `math.Ceil`/`math.Floor` + guard.
    - Demand/PRC lookup (category 2): `demandForRoleOrModel(nr, role) float64` and
      `requiredSpareForRoleOrModel(nr, role) (rc, sc float64)`, wired into `roleDemandGPUs`'s
      demand read and `cost_aware_optimizer.go:303-313`'s RC/SC read respectively.
    - Did **not** implement `ReplicasToCloseGap`/`GPUsToCloseGap`/bounds helpers or touch
      `sortByCostEfficiencyAsc` — explicitly out of scope per D3/A21'.

    **Two real regressions caught by the full test suite, both from the same root cause — a
    plausible-looking design I had to correct against the ORIGINAL code's exact behavior rather
    than my own initial (wrong) generalization:**

    1. `requiredSpareForRoleOrModel`'s first draft special-cased `role == domain.RoleBoth` to
       skip the `RoleCapacities` lookup entirely and return the model-level scalars directly. The
       *original* inline code at `cost_aware_optimizer.go:303-313` does no such thing — it always
       attempts the `RoleCapacities[role]` lookup, RoleBoth included, and only falls back to
       model-level scalars on a genuine map miss. Caught by
       `cost_aware_optimizer_test.go`'s "maps an empty-role variant to the both RoleCapacities
       entry" test (expected 300, got 9999 — the model-level decoy value winning when it should
       have lost to the real "both" entry). Fixed by removing the special case.
    2. `demandForRoleOrModel`'s first draft used `aggregation.DemandForRole(nr.Result, role)`
       directly, per the task's literal instruction to build this helper "on step 1's
       `demandForRole`". This reads `Result.RoleDemand` (analyzer-owned). The *original*
       `roleDemandGPUs` code instead reads `NamedAnalyzerResult.RoleCapacities[role].TotalDemand`
       (engine-built) — equal to `Result.RoleDemand[role]` in real production data (since
       `buildRoleCapacities` derives one from the other), but NOT equal in 4 existing
       `rescale_optimize_test.go` fixtures, which hand-construct `RoleCapacities` directly without
       ever populating `Result.RoleDemand` (a test-fixture gap, not a production one). Caught by 4
       failing `GreedyByScoreOptimizer rescale` P/D-split tests (e.g. expected split 4/4, got 0
       replicas reclaimed for prefill — `demandForRoleOrModel` silently returned 0 instead of the
       fixture's real per-role demand). Fixed by reading `RoleCapacities` instead, matching the
       original call site's actual source of truth exactly — kept `aggregation.DemandForRole` out
       of this specific helper (removed the now-unrelated import) since the two data sources are
       not interchangeable in general, only equal by construction in the real pipeline. Documented
       the reasoning prominently in both functions' doc comments given a future reader could easily
       reintroduce the same mistake.

    Both bugs share a lesson: "read demand/RC/SC the same way the code being replaced did" is
    stricter than "read it from the theoretically-equivalent canonical source" whenever a test
    fixture bypasses the real derivation step — worth flagging to the mission owner as a possible
    test-fixture hygiene gap (several `allocation` test fixtures construct `RoleCapacities`
    without going through `buildRoleCapacities`/populating `Result.RoleDemand`), not something I
    fixed since it's outside this item's scope.

    Tests: `query_api_test.go` — direct unit coverage of all four helpers including the
    RoleBoth-handling asymmetry between the two lookup helpers (deliberate, each mirroring its own
    original call site). Full non-e2e `go test` and `make lint`/`gofmt` clean. Commit pending.

9. [x] Composite construction, deep copy, identity, compose-site wiring — the big item.
   `internal/engines/steadystate/composite.go`: `buildComposite(ctx, namedResults, scaleUp,
   scaleDown) allocation.NamedAnalyzerResult`. Wired in at `collectV2ModelRequest` (old
   line 797, now inside the "compose" comment block), replacing `namedResults[0]`.
   `allocation.CompositeSignalName = "CompositeSignal"` added (`composite_identity.go`).

   **Exported previously-unexported allocation helpers** so the engine-side builder (which must
   live in `steadystate` — it needs `buildRoleCapacities`/`applyUniversalThreshold`, both
   unexported in that package, and `allocation` cannot import `steadystate`, that's the real
   direction) can consume them: `resolveSO`→`allocation.ResolveSO`, `soDecision`→
   `allocation.SODecision` (fields `N/OK/Path/Contributors`), `demandForRole`→
   `aggregation.DemandForRole`. Mechanical rename, same logic, updated all call sites including
   tests (item 5's `composite_decision_test.go`, item 1/7's `demand_test.go`/`prc_com_test.go`).

   **Two real bugs found and fixed during this item, beyond the design work itself:**

   1. **Zero-demand SO must not lose its real PRC.** `PRC_com(SO)` is formally undefined when
      `N_com(SO) == 0` (spec §4.4 — "nothing needed"), which happens whenever an SO's role has
      zero demand — but PRC and demand are independent (§2.4): a zero-demand SO can still hold
      real replicas contributing real spare capacity that a scale-down decision needs to see.
      Leaving `PerReplicaCapacity` at the Go zero value there would silently erase that supply and
      break test 1 (sat-only byte-identical) for any zero-demand SO. Resolved by falling through
      to the SO's own representative analyzer-measured PRC whenever `PRCCom` returns not-ok — this
      is what makes the sat-only identity hold unconditionally, not just when demand happens to be
      positive. Added a dedicated test for this exact case in `composite_test.go`.
   2. **Composite `Reason` (decision path) is not the same vocabulary as an analyzer's own
      `Reason` (capacity provenance) — conflating them silently defeats the no-signal gate.**
      Wrote `HasUsableCompositeSignal` (item 6) to delegate to `ResultIsInformative`, which checks
      `Reason != "no-data" && Reason != "error"`. Once item 9 started writing the composite's own
      `Reason` as the decision-path string (`C0-agree`/.../`C4-no-signal`), a probe test proved
      `HasUsableCompositeSignal` returned **true** for a composite whose only SO was
      `C4-no-signal`, because `"C4-no-signal"` matches neither analyzer sentinel string — the gate
      I built in item 6 would have silently never fired on the real composite construction path.
      Fixed by rewriting `HasUsableCompositeSignal` to check `Reason != allocation.DecisionNoSignal`
      directly instead of delegating to `ResultIsInformative` (which is correctly calibrated to
      analyzers' own vocabulary, not the composite's). Had to also fix item 6's own tests
      (`composite_signal_gate_test.go`) and the `engine_v2_quota_test.go`/quota fixtures, which had
      used analyzer-style sentinel strings (`"P0-store"`, `ReasonNoData`) standing in for composite
      `Reason` values — updated them to real decision-path constants. Caught only because I wrote
      an end-to-end probe test exercising the full `collectV2ModelRequest` path with a genuinely
      no-signal saturation result, rather than trusting the item-6 unit tests (which tested
      `HasUsableCompositeSignal` in isolation with hand-picked `Reason` strings that happened not
      to exercise this exact conflict) — **this is exactly why test 8d needed an end-to-end variant
      in `composite_test.go`, not just the unit-level one from item 6.**

   **Design decisions made without spec-explicit guidance, reasoned from the spec's stated
   philosophy (A16'/A27/A28's "never second-guess or discount a real measured value") rather than
   invented from nothing:**
   - Composite's `.Live` = true iff at least one SO reached a real (non-`C4-no-signal`) decision
     across the whole composite. Required because `needsScaleDownForRole`/
     `safeRemovalReplicasForRole` (in `allocation`) gate on `NamedAnalyzerResult.Live` directly, and
     since the optimizer's per-model record now comes from the composite (not `namedResults[0]`
     raw), leaving `.Live` at its Go zero value would silently disable scale-down for every model.
     Verified via test 1 that this reduces to sat's own `.Live` exactly on the sat-only path.
   - Composite's per-variant `Score` legacy field = max over ALL entries' Scores (A9'''), computed
     directly rather than reusing `resolveSO`'s per-SO contributor list (Score is model/analyzer
     level, not per-SO, so this is simpler and correct independent of per-SO decisions).
   - Per-SO `VariantCapacity.TotalDemand`/`Utilization` on the composite copy the REPRESENTATIVE
     source analyzer's own per-variant values (not `D_sat[role]`, which is shared across every SO
     in a role and would double-count if copied onto each) — `Utilization` is then recomputed with
     `PRC_com` substituted for the source's own PRC, mirroring the same substitution §5.3 makes for
     RC/SC, and reducing to the source's own `Utilization` exactly when `PRC_com` falls through to
     the source's PRC (the zero-demand case above).
   - `buildCapacities` (existing `steadystate` function) is called with `metaByVariant = nil` on
     the composite — confirmed via its own doc comment this is a documented no-op join, safe
     because the composite already carries reconciled Role/ReplicaCount copied from its
     representative source (which itself already went through the real metadata join).
   - Saturation's own `Result` is always non-nil by construction on the real path (verified:
     `runAnalyzersAndScore` returns the error before building any `NamedAnalyzerResult` if
     saturation's own analysis fails) — `findSaturation`/`emptyComposite`'s nil-guard branch is
     unreachable in production, kept only so a test slice with no saturation entry degrades safely
     rather than panicking.

   Tests: `composite_test.go` — test 1 (sat-only regression, including the zero-demand-SO variant
   and the composite's-own-name variant), test 8d (end-to-end no-signal), test 13 (Score has no
   effect, both on PRC_com/RC/SC AND confirming the legacy Score field itself DOES differ per
   A9'''), test 15 (end-to-end higher-demand contributor), test 24 (deep-copy isolation, two
   variants: direct mutation, and no-aliasing across two composites built from the same inputs).
   **Also restored the 3 skipped multi-analyzer tests** (`engine_v2_population_test.go` — Score
   population, Score defaulting, per-analyzer threshold override): removed their `Skip(...)` calls;
   all 3 passed immediately with zero test-body changes, because they exercise
   `runAnalyzersAndScore` directly, a layer beneath composite construction that this mission does
   not change (task explicitly forbids changing its return type) — confirmed this matches the
   ledger's item-1 hypothesis about which "3" the task meant. **Left the other 5-6 skip sites
   untouched** (`engine_v2_test.go:375`, `engine_external_registry_test.go:55`,
   `engine_v2_demand_liveness_test.go` x3, `greedy_score_optimizer_test.go:868`) — task explicitly
   scoped this to "the 3", and at least one of the others I spot-checked also looks
   trivially unskippable by the same reasoning, but restoring those is outside this item's stated
   scope; flagging for the mission owner as a found opportunity rather than acting on it unasked.

   Full non-e2e `go test $(go list ./... | grep -v /test/e2e)` clean, `make lint`/`gofmt` clean on
   all changed files. Commit pending.

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

