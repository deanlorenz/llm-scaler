# task-coder-composite-redesign

## Orientation

- **What / goal / mission:** Rewrite `buildComposite` and its helpers (today spread across
  `internal/engines/steadystate/composite.go`, `internal/engines/allocation/composite_decision.go`,
  and `internal/engines/aggregation/{replicas_needed,prc_com}.go`) per spec.md v9. This is a
  redesign of the *composite-building code's structure and explicitness*, not a new formula — v8's
  underlying math (`PRC_com(SO) = D_sat[role]/N(SO)`) is unchanged. The problem being fixed: today's
  code has saturation-lookup duplicated 3x, an aggregation call (`AggN`) invoked on a 1-element
  slice (dead pattern), unclear provenance when analyzers disagree, and math split across a
  separate package from where it is used.
- **Worktree:** this worktree (`worktrees/composite-analyzer`, branch `composite-analyzer`).
  Startup check: confirm `git rev-parse --show-toplevel` resolves here and
  `git branch --show-current` reports `composite-analyzer`. Stop and report if not.
- **Role / scope:** coder. Implement exactly the checklist below. Do not revisit v1-v8's settled
  design decisions (spec §10 D1-D4, still closed). Do not touch
  `internal/engines/allocation/multi_backup/`. Do not push or open a PR.
- **Read first, in full:** `.session/spec.md`'s **v9 revision entry** (§12, dated 2026-09-14,
  including its 2026-09-14 additions on sat's dual role, typed decision paths, and the two-check
  signal gate — read the WHOLE entry, not just the first half) — it is the authoritative summary
  of every decision this task implements. Then `.session/composite-signal-redesign.md` **§4, §4.1,
  §4.2, §4.3** for the full reasoning and code citations behind each v9 bullet (the rest of that
  doc — §1-§3, §5 — is background, pull on demand only if a checklist item is unclear).
- **Read for context, before starting (small, load-bearing):**
  - `internal/engines/steadystate/composite.go` — the file you are rewriting. Read it whole; every
    function in it is in scope.
  - `internal/engines/allocation/composite_decision.go` — `ResolveSO`, `SODecision`, the
    `DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` constants. In scope.
  - `internal/engines/aggregation/replicas_needed.go`, `internal/engines/aggregation/prc_com.go` —
    `AggN`, `PRCCom`, `replicasNeeded`, `variantCapacity`, `roleOf`. Being relocated/renamed/removed.
  - `internal/engines/allocation/composite_eligibility.go` — `eligible()`. Model-level; the new
    per-SO check (item 3) is additional to this, not a replacement.
  - `internal/engines/allocation/analyzer_helpers.go` — `ResultIsInformative`, `ReasonNoData`,
    `ReasonError` constants. The per-SO check reuses these constants directly.
  - `internal/config/saturation_scaling.go:657` — `ScalingPolicy.AnalyzerEnabled(analyzerName
    string) bool`. Needed for item 3b (sat's contributor-role gating). Note its receiver is
    `ScalingPolicy`, not a pointer — check how it's already obtained elsewhere (e.g.
    `engine_v2.go:170`'s `config.AnalyzerEnabled(entry.name)`) for the right value to pass through.
  - `internal/engines/steadystate/engine_v2.go`: `buildCapacities` (:904, confirm it still runs on
    the composite unchanged — it should, this task does not touch it), `logAnalyzerResult` (:1105,
    the existing per-analyzer log line you are adding a composition-level line alongside),
    `runAnalyzersAndScore` (:106, where `config.AnalyzerEnabled` is already called for non-sat
    analyzers at :170 — sat is exempted upstream and never checked; you are adding the sat-specific
    check at the composite-building step instead, per item 3b), `collectV2ModelRequest` (:777,
    calls `runAnalyzersAndScore` at :789 then `buildComposite` at :818 — confirm your rewrite
    preserves this two-call structure; `buildComposite` is NOT inside `runAnalyzersAndScore`).
  - `internal/engines/aggregation/aggregation.go` — `SumTotalSupply`/`SumTotalAnticipatedSupply`,
    unchanged, for reference on the Supply/AnticipatedSupply formula restated in spec v9.

## Task — checklist

Each item should land as its own commit. Confirm `make test` passes and existing composite tests
(`composite_test.go`, `composite_observability_test.go`, `composite_decision_test.go`,
`composite_eligibility_test.go`, `composite_signal_gate_test.go`, and the aggregation package's
`replicas_needed_test.go`/`prc_com_test.go`) still reflect the new design after each relevant item —
update or move tests alongside the code they test, do not leave stale tests for removed functions.

1. **Rename `N(SO)`/`N_i(SO)` to `TotalReplicas`, and `N_com(SO)` to `CompositeTotalReplicas`**
   (or an equivalent compound built on `TotalReplicas` — the principle is "name states the
   quantity, not the operation"; pick the concrete identifier and note it in your ledger).
   Apply consistently across the functions this task moves/rewrites. Do not rename anything in
   `spec.md` itself or in files outside the composite-building code's scope.

2. **Make saturation the sole source of every composite field except PRC and Reason.** In the
   composite-building loop (today `buildComposite` in `composite.go`), stop using
   `unionOfVariants` and stop falling back to "the first other analyzer that has this variant" in
   `representativeVariantCapacity`. Iterate saturation's own `Result.VariantCapacities` directly.
   Every field copied onto the composite's `VariantCapacity` (`ReplicaCount`, `PendingReplicas`,
   `WarmPoolReplicas`, `WarmPoolPerReplicaCapacity`, per-variant `TotalDemand`) and the model-level
   `TotalDemand`/`RoleDemand` come from saturation's own result, unconditionally — no fallback
   branch to any other analyzer for these fields. Remove `unionOfVariants` and the
   fallback-to-other-analyzer branch of `representativeVariantCapacity` as dead code (or replace
   `representativeVariantCapacity` entirely, since its "representative" framing no longer applies
   when there is only ever one source).

3. **Implement the new per-SO participation check, AND the sat contributor-role gating — two
   related but distinct rules, both in scope:**

   **3a. Per-SO participation** (additional to `eligible()`'s existing model-level check, not a
   replacement for it): an analyzer contributes to a given SO's `TotalReplicas` computation only
   if (a) that SO is present in the analyzer's own `VariantCapacities`, and (b) if present, its
   `Reason` is not `ReasonNoData`/`ReasonError`. This governs whether an analyzer's value counts
   toward `CompositeTotalReplicas`'s aggregation (item 4) — it is unrelated to whether saturation's
   own fields populate the composite (item 2's identity role, always unconditional for sat).

   **3b. Sat's contributor-role gating** (new — this is a genuine signature/data-flow change, not
   a small conditional add): saturation counts as an ORDINARY contributor to
   `CompositeTotalReplicas` — collected symmetrically with every other analyzer, no
   `if e.Name == domain.SaturationAnalyzerName` special case during collection — only when
   `config.AnalyzerEnabled(domain.SaturationAnalyzerName)` is true. When sat is config-disabled, it
   participates ONLY as a fallback: only if no other analyzer contributed a defined value for that
   SO. This means the composite-building step needs access to the `ScalingPolicy`/config that today
   only reaches `runAnalyzersAndScore` — thread it through (as a parameter to whatever
   `buildComposite`/`ResolveSO`'s replacements become, or however else is cleanest given your
   item-4 relocation). Preserve existing "opt out, don't crash" behavior for a non-live or broken
   sat throughout this change — do not let the config check introduce a new crash/panic path.

   **Decision taxonomy implication of 3b:** `single` = exactly one analyzer contributed a defined
   value, symmetric regardless of which analyzer; `sat-fallback` = specifically "sat is
   config-disabled AND nothing else contributed" — narrower than today's implementation, which
   fires whenever sat happens to be the only contributor left standing among analyzers that were
   already being treated as eligible. Do not conflate the two cases.

4. **Move the combined-PRC/TotalReplicas computation into `steadystate/composite.go`.** Per the
   single-caller-relocation rule (verified in spec v9 and the redesign doc's §4.1): `AggN` (one
   external caller, `ResolveSO`) and `PRCCom` (one caller, `buildComposite`) both move out of the
   `aggregation` package. `replicasNeeded`/`variantCapacity`/`roleOf` (private helpers, already
   single-file) move with whichever function they only serve. `DemandForRole` stays in
   `aggregation` (three callers — verify this is still true after your rewrite before leaving it;
   if your rewrite drops a caller, re-check whether it should move too, but do not move it
   speculatively).
   - `replicasNeeded`'s replacement, and `variantCapacity`'s replacement, should take the
     already-resolved `VariantCapacity` as a parameter instead of `(result *domain.AnalyzerResult,
     variant string)` plus an internal search loop — the composite-building loop already has the
     specific `VariantCapacity` in hand once it is iterating saturation's own list (item 2).
   - `PRCCom`'s replacement should not be a separately named, separately tested function with its
     own file — inline its computation (`D_sat[role]/CompositeTotalReplicas`) directly where the
     composite's `VariantCapacity.PerReplicaCapacity` is assigned. If you find a reason it still
     needs to be a standalone function (e.g. for direct unit testing of the zero-guard), flag it in
     your ledger rather than silently keeping the old shape.
   - Where `ResolveSO`'s decision-path logic ends up (still needs to produce
     `DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` and the contributor
     list) follows the same rule: if it has exactly one caller after your rewrite, it belongs
     alongside that caller.

5. **Unify `roleOf`/`roleOfVC`/`AggregateByRole`'s inline role-canonicalization** into one shared
   function. Apply the single-caller-relocation rule to decide where it lives, based on the *actual*
   call graph after items 1-4 land (not today's call graph, which changes once `AggN`/`PRCCom`
   move) — note in your ledger which callers you counted and where you put it.

6. **Make PRC computation loop over roles, not independently per SO.** Look up the composite's
   per-role demand (`D_sat[role]`) once per role, then compute each SO's PRC from that shared
   numerator and the SO's own `TotalReplicas`/`CompositeTotalReplicas` — do not repeat the demand
   lookup once per SO when multiple SOs share a role. This is a shape/efficiency change to item 4's
   relocated PRC computation, not a formula change.

7. **Convert the decision-path values to a typed/enumerated representation.** Today
   `DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` are untyped `string`
   constants (`composite_decision.go`). Replace with a named type with a closed set of values (e.g.
   mirroring any existing `domain`-package enum pattern in this codebase — check for precedent
   before inventing a new style) so an invalid decision-path value is a compile-time error, not
   representable at all.

8. **Split `HasUsableCompositeSignal` into two checks.** Today it is one boolean answering "does
   ANY SO in this model have a signal" (loops `VariantCapacities`, true as soon as one SO is not
   `DecisionNoSignal`). Replace with:
   - a **per-SO** check — does this specific SO have a usable signal (its own decision path is not
     `DecisionNoSignal`);
   - a **model-level** check, separate from the per-SO one — is saturation itself present/healthy
     at all (sat missing/broken is categorically worse than any single SO lacking a signal, since
     sat is the identity/unit source for the whole composite — see item 2).
   Update both call sites (`engine.go:1091`, `engine_v2.go:735` via `hasSaturationResult`) to use
   whichever of the two checks is semantically correct for that call site — do not default both to
   the same one without checking what each site actually needs.

9. **Add composition-level logging.** Alongside (not replacing) the existing per-analyzer
   `logAnalyzerResult` line, add a log line at the composite-building step that records, per SO:
   each contributing analyzer's `TotalReplicas`, `ReplicaCount` (Ready), and `PendingReplicas`. Goal
   is full visibility into what fed `CompositeTotalReplicas`, not just the winning aggregated
   value — this data does not exist in any log today. Match the existing logging style
   (`ctrl.LoggerFrom(ctx)`, structured key-value fields, see `logAnalyzerResult` for the pattern).

10. **Add a code comment flagging the Ready-vs-usefully-serving gap** where the composite's
    `ReplicaCount` feeds Supply/AnticipatedSupply (in `buildCapacities` or wherever the composite's
    `VariantCapacity.ReplicaCount` is consumed for that purpose) — note that it is saturation's raw
    k8s ready count, not a count of replicas verified to be usefully serving, and that this is
    accepted as good enough for now, not a bug to fix in this task.

11. **Verify no formula change to Supply/AnticipatedSupply/RC/SC.** `buildCapacities` should still
    run on the composite unchanged, producing
    `TotalSupply = ReplicaCount(SO) × PerReplicaCapacity(SO)`,
    `TotalAnticipatedSupply = (ReplicaCount(SO)+PendingReplicas(SO)) × PerReplicaCapacity(SO)`, and
    RC/SC via the existing `applyUniversalThreshold`. This should require no code change if items
    1-10 are done correctly — treat any test failure here as a signal something in items 1-10 broke
    an invariant, not as a reason to change this formula.

## Completeness check (not a checklist item — do before declaring done)

Per spec v9: compare your rewrite's behavior against the **pre-single-analyzer aggregation logic at
the engine side** (the code this mission's CT7 originally lifted out of — find it via `git log`/
`git blame` on `engine_v2.go` before CT7's changes, or ask the mission owner if you can't locate
it), not only against v8's own step list. Flag any gap you find rather than silently deciding it
doesn't matter.

## Non-negotiable regression guards

Same two as v8's implementation, still binding — the sat-enabled/disabled contributor change
(item 3b) does not weaken either:
- **Sat-only identity**: when saturation is the only eligible/contributing analyzer for an SO, the
  composite's PRC for that SO must come out numerically identical to saturation's own PRC (today's
  test 1 in spec §9 — locate its current test file and keep it passing under the new code shape,
  moving it if the function it tests relocates). This must hold BOTH when sat is config-enabled
  (an ordinary sole contributor, decision path `single`) AND when sat is config-disabled with
  nothing else contributing (decision path `sat-fallback`) — add a test for the second case if one
  does not already exist, since item 3b makes it newly distinguishable from the first.
- **Score has no effect**: `maxScore`'s behavior (max over contributors' `Score`, legacy field
  only) is unchanged by this task — you are not touching Score.

## Limits

- Do not modify `internal/engines/allocation/multi_backup/`.
- Do not change the underlying `PRC_com(SO) = D_sat[role]/CompositeTotalReplicas` formula, or
  Supply/AnticipatedSupply/RC/SC's formulas — this is a code-structure/naming/logging rewrite, not
  a design change.
- Do not reopen spec §10 (D1-D4) or any earlier v1-v8 decision.
- Do not push or open a PR — report completion and wait for the mission owner's review.
- If you hit a genuine ambiguity this task file doesn't resolve, stop and ask rather than guessing
  — v8's implementation had exactly one incident of proceeding through an ambiguity instead of
  asking, and it had to be reverted (see spec.md v8's "Implementation" entry for the O1/O2
  precedent).
