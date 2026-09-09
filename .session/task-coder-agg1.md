# task-coder-agg1

## Orientation

- **In:** `mission.composite-analyzer.coder-agg1.in`
- **Out:** `mission.composite-analyzer.coder-agg1.out`
  (subscribe to `In:` before starting; publish status/findings/questions/completion to `Out:`)
- **Name:** `2026-09-09-coder-agg1`
- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **What / goal / mission:** Implement the composite-aggregation calculation specified in
  `.session/spec.md` (v8, approved) — reduce `[]allocation.NamedAnalyzerResult` into the single
  `CompositeSignal` the optimizer consumes, so every enabled analyzer's demand influences it, not
  just saturation's alone. Aggregation at model and role level, in saturation's token-capacity
  units (request-based normalization is explicitly deferred, out of scope).
- **Worktree:** this worktree (`worktrees/composite-analyzer`, branch `composite-analyzer`) —
  **same-worktree** setup, async/background mode. Work directly in this worktree; no separate
  branch/worktree is created for you.
- **Startup verification instructions:** confirm `git rev-parse --show-toplevel` resolves to this
  worktree and `git branch --show-current` reports `composite-analyzer`. Fail and report via
  `Out:` on any mismatch — do not attempt `EnterWorktree` or any other recovery.
- **Role / scope:** coder. You implement code and tests per the spec and this task file's
  checklist only. You do not: rewrite the spec, revisit decided items (§10 D1–D4 are closed), touch
  `internal/engines/allocation/multi_backup/` (upstream-tracked, `//go:build ignore` — port its
  *logic*, never edit the files), build on/toward request-based normalization, or push/publish
  anything. You do not run the mission-owner's review — a separate reviewer reads your commits as
  they land.
- **Ledger / log:** `.session/coder-agg1-ledger.md` — create on first write; append decisions,
  findings, and any deviation from this checklist as it happens.

## Task

- **Plan / spec:** `.session/spec.md` (v8, approved). Read §1 (mission definition) and §2 (ground
  truth) in full first — they correct several plausible-but-wrong assumptions a fresh reader would
  otherwise make (demand/PRC independence, RoleDemand's two storage layouts, coverage undefined at
  zero). Pull §4–§9 section by section as each checklist item below requires it — do not read the
  whole spec up front. **Do not read §10** — it is a decision log for the mission owner, already
  fully folded into the sections above; nothing in it should change your implementation.
- **Context** (read before starting, small and load-bearing):
  - `internal/engines/aggregation/aggregation.go` — the package you extend. Match its style
    exactly: pure functions named for what they aggregate, doc comments stating the invariant.
  - `internal/engines/allocation/optimizer_interfaces.go` — `NamedAnalyzerResult` (:27),
    `ModelScalingRequest.CompositeSignal` (:75).
  - `internal/domain/analyzer.go` — `AnalyzerResult.TotalDemand`/`RoleDemand`,
    `VariantCapacity.Role`/`Reason`.
  - `internal/engines/steadystate/engine_v2.go` — `runAnalyzersAndScore` (:102),
    `collectV2ModelRequest`'s `CompositeSignal:` assignment (:797), `hasSaturationResult` (:722),
    `buildCapacities` (:850), `logAnalyzerResult` (:1051), `recordAnalyzerMetrics` (:222).
- **Refs** (do not read unless a specific checklist item tells you to):
  - `internal/engines/allocation/multi_backup/analyzer_helpers_multi.go` — port `ResultIsInformative`,
    `prcForVariant`, and the Live-gating / all-agree / safety-floor *patterns* only (spec §2.2, A1).
    Never edit this file.
  - `.session/survey-zero-signal.md` — background for checklist item 6 (gate repair); pull only if
    the gate-repair item is unclear from the spec sections it cites.
- **Expected output:** a sequence of commits on this branch, one per checklist item below (or
  split further if an item is still doing more than one thing), each compiling and each test-plan
  item that item covers passing. Final state: all spec §9 test-plan items (1–30) implemented and
  passing, `make test` clean, `hasSaturationResult`'s replacement wired into the existing
  quota-guard call site with the guard still firing (test 14).
- **Done / completion criteria:**
  - `make test` passes.
  - `make lint` (or whatever the project's lint target is — check the Makefile) passes on changed
    files.
  - Every checklist item below is checked off with its commit SHA noted in your ledger.
  - Spec §9 tests 1–30 all exist and pass — cross-check the final commit against the full list;
    do not stop at "most of them."
  - Test 1 (sat-only ⇒ numerically identical to today) and test 13 (Score has no effect) are the
    two non-negotiable regression guards — call them out explicitly in your completion report.
- **Limits:**
  - Do not modify `internal/engines/allocation/multi_backup/` in any way.
  - Do not implement or build toward request-based normalization, `normalizeToCompositeUnits`, or
    anything from the `single-analyzer-normalize` branch. Composite stays in saturation's token
    units throughout (spec §4.4).
  - `Agg_N` is a pure `max` over contributors. Do not add Score weighting, confidence-weighted RMS,
    or any partial scoring mechanism — spec §7 is explicit that this is withdrawn, not deferred as
    a stub. `Score`'s only role in the composite is `A9'''`: `max` over contributors' Scores, for
    the legacy field only, read by nothing in the new aggregation.
  - Do not add a new structure to `NamedAnalyzerResult` beyond what the composite needs (spec §8 —
    it is legacy, a separate mission reorganizes it). Keep provenance (A12) minimal.
  - Do not implement query-API helpers beyond D3's scoped two categories (ceil/rounding,
    PRC/demand lookup). The demand→replicas→GPUs chain and bounds consistency are explicitly
    deferred to a follow-up mission — do not implement them even partially.
  - Do not compute model-level coverage as an eager field on the composite — it is query-API-only
    (spec §5.4, D4#9).
  - Preserve the sat-only fast path exactly: with saturation as the only contributor, the composite
    must be numerically identical to today's `namedResults[0]` copy. This is the regression guard;
    if any change makes this test fail, stop and report rather than adjusting the test.
  - Compose at `collectV2ModelRequest:797` (spec §6.2 O2, D4#4). Do not change
    `runAnalyzersAndScore`'s return type — that is the change that broke the parent branch's build.
  - No license headers in new files (project convention, `AGENTS.md`) — note the existing
    `aggregation.go` has one; that is pre-existing, do not add the pattern to files you create.
  - If you hit a genuine ambiguity the spec doesn't resolve, stop and ask via `Out:` — do not
    invent a resolution and proceed. Re-litigating a §10-decided item is not an ambiguity; report
    that as a spec question to the mission owner instead of resolving it yourself.

## Execution

### Steps / subtasks

Each numbered item is intended as one commit. Split further if a step is doing two independent
things; do not batch two steps into one commit.

1. [ ] **Demand accessor** (spec §2.3, A13): `demandForRole(result *domain.AnalyzerResult, role string) (value float64, present bool)`
   in the `aggregation` package. Handles both storage layouts (nil `RoleDemand` ⇒ read `both` from
   `TotalDemand`; empty role canonicalized to `domain.RoleBoth`; role absent from a non-nil map is
   not-present, distinct from present-and-zero). Unit tests covering both layouts and the
   present/absent/zero distinction.
2. [ ] **Undefined-value type** (spec §2.5, A14): a small type or `(value float64, ok bool)`
   convention used by every coverage/`N`/replica helper from here on, so an undefined contribution
   can never silently enter a `min`/`max` as `0` or `+Inf`. Establish the convention here; later
   steps use it, they don't redefine it.
3. [ ] **Eligibility + informativeness** (spec §5.1.1, A3): `eligible(entry) bool` — non-nil
   `Result`, informative (port `ResultIsInformative`'s logic per A1, don't revive the file it's
   in), `Live`. Same rule in both directions — a stale analyzer neither raises demand nor blocks
   scale-down. Unit tests: live+informative eligible; not-live excluded; error/no-data `Reason`
   excluded.
4. [ ] **Per-SO `N` and `Agg_N`** (spec §5.2, §7.1, D4#3, D4#5): `N_i(SO) = D_i[role(SO)] / PRC_i(SO)`
   per analyzer (unit-free, uses step 1's accessor, guarded per step 2 when PRC or demand is zero —
   spec §2.5, tests 17–20), and `Agg_N(entries, so) (value, ok)` = pure `max` over contributors'
   defined `N_i(SO)` (spec §7.1 — no Score weighting). `N` stays continuous; no `ceil()` here (A2).
   Named for the quantity, not the operation (D4#5) — combination rule (`max`) swappable in one
   place. Tests 2, 3, 13, 17–20.
5. [ ] **Fallback chain + decision path** (spec §5.1–§5.1.4, D2): when `contributors(SO)` is empty,
   fall back to saturation if saturation itself is eligible (`C2-sat-fallback`); when no contributor
   at all, `C4-no-signal` (no default-PRC mechanism beyond saturation's own ladder needed per the
   survey — do not invent a `C3-default-prc` source, saturation's existing `P0-store` ladder is the
   only source of estimated PRCs and it flows through as an ordinary contribution per A27/A28).
   Record the decision-path field (`C0-agree`/`C1-single`/`C2-sat-fallback`/`C4-no-signal`) per SO,
   mirroring `domain.VariantCapacity.Reason`'s free-text style. Tests 6, 7, 8, 8a, 8b, 8c, 9, 10, 11.
6. [ ] **Gate repair — one shared "usable signal" predicate** (spec §5.1.3, §8, A11', D2 survey
   finding #2): expose one predicate (e.g. `HasUsableCompositeSignal(composite) bool`) testing "is
   there a usable signal" (informed by `C4-no-signal`), and use it to replace **both**
   `hasSaturationResult`'s name check at `engine_v2.go:722` **and** thread it through wherever the
   survey found the other analogous nil-checks are gating on the same underlying question — check
   `.session/survey-zero-signal.md` for the enumerated seven-plus-one sites before deciding scope
   here; if the seven other sites are genuinely already-correct nil guards (survey conclusion #1)
   and only the one name-check needs the predicate, say so in your ledger rather than touching sites
   that don't need it. `C4-no-signal` also publishes the new policy-owned `wva_model_scaling_blocked`
   reason per D2's decision, following the existing convention (published before the empty-decision
   return). Tests 8d, 14.
7. [ ] **Derivation chain from `N` to RC/SC** (spec §5.3, D4#3): `D_com[role] = D_sat[role]`;
   `PRC_com(SO) = D_sat[role(SO)]/N_com(SO)`; supply/RC/SC via the *existing* formulas on
   `NamedAnalyzerResult`, substituting `PRC_com` for the analyzer's own PRC. `ceil()` only here,
   where a replica count is finally produced (A2) — nowhere upstream. Self-consistency assertion:
   `ceil(D_sat[role]/PRC_com(SO))` recovers `N_com(SO)`. Tests 4, 5, 12, 25.
8. [ ] **Model-level cross-role coverage** (spec §5.4): `modelCoverageFromRoles(byRole map[string]float64) float64`
   = `min(cov(prefill), cov(decode)) + cov(both)`, guarding a role with no defined coverage so it
   doesn't enter the `min` as a spurious `0` (A14). Lives in the query API (step 10), not as an
   eager composite field (D4#9). Test 5, part of 23.
9. [ ] **Composite construction, deep copy, identity, and compose-site wiring** (spec §6.1, §6.2 O2,
   §8, A7, A10, A12, A15, D4#4, D4#6, D4#7): the function that builds the composite
   `NamedAnalyzerResult` from `[]NamedAnalyzerResult` using steps 3–8, deep-copying every
   reference-typed field (`Result`, `RoleCapacities`, `RoleSpare`) so the source entries are
   unmutated. New exported name constant (recommend `allocation.CompositeSignalName`). Minimal
   provenance: which analyzers contributed, which drove each max. Wire it in at
   `collectV2ModelRequest:797`, replacing `namedResults[0]` — do not change
   `runAnalyzersAndScore`'s return type. Tests 1 (sat-only regression, the critical one), 15, 16
   (restore the 3 skipped multi-analyzer tests), 24.
10. [ ] **Query API — D3's two scoped categories only** (spec §5.5, D3 decision, A20', A21'):
    - `ceil()`/replica-count rounding: one shared helper for "how many replicas does this demand
      need at this PRC", used everywhere a partial replica is rounded (spec cites
      `roleDemandGPUs`/`rescale.go:606`, `roleBottleneckReplicas`, `safeRemovalReplicasForRole`'s
      `floor` as the repeated-rounding sites — read the actual current code at those sites before
      changing them, line numbers may have shifted since the spec was written).
    - PRC/demand lookup: one shared helper for the role-vs-model demand-read fallback (built on
      step 1's `demandForRole`), used at the sites the spec cites in `rescale.go:587-591` and
      `cost_aware_optimizer.go:309` (again, verify current line numbers).
    - Do **not** implement `ReplicasToCloseGap`/`GPUsToCloseGap`/bounds helpers or touch
      cost-efficiency variant selection (`sortByCostEfficiencyAsc`) — out of scope per D3's decision
      and A21'.
    - Wire the two helpers into the cited call sites, replacing the inline duplicated arithmetic.
    Tests 26 (for the two categories actually in scope), 27.
11. [ ] **Observability — reuse, verify, audit** (spec §6, A22, A23, D4#10, D4#12): confirm
    `logAnalyzerResult` and `recordAnalyzerMetrics` already iterate the full result set including
    the composite once it's an ordinary entry in whatever they iterate; if the composite needs to be
    appended to that iteration explicitly (since it's not literally one of `namedResults` after step
    9's wiring), do that — through the *same* functions, not parallel ones. Audit that every field a
    reader needs is emitted for every analyzer result including non-live/uninformative ones (a gap
    here is a bug to fix, not just a check). Add the composite's row to `docs/reference/cycle-log.md`
    in `D_sat` units. Tests 28, 29, 30.
12. [ ] **Full test-plan sweep**: go through spec §9 items 1–30 explicitly, one by one, and confirm
    each has a passing test (most will already exist from steps 1–11; this step is the checklist
    pass that catches any gap, especially test 9 unit-independence, test 10 `PRC_sat`-independence,
    and test 22 SO-independence-of-demand, which don't map to a single step above as cleanly as the
    others). Fill any gap found. This commit should be small — test-only, no production code
    changes, unless it finds an actual bug.

### Status

`NOT STARTED`

### Known issues

- Spec line-number citations for `rescale.go`/`cost_aware_optimizer.go` call sites (step 10) were
  recorded when the spec was drafted and may have drifted. Verify against current source before
  editing; report via `Out:` if a cited site no longer exists or looks structurally different from
  the spec's description, rather than guessing at the closest match.
