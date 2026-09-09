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

