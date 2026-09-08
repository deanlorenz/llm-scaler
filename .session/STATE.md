# composite-analyzer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** The **composite aggregation calculation** — reduce the
  `[]NamedAnalyzerResult` from `runAnalyzersAndScore` into the single `CompositeSignal` the
  optimizer consumes, so every enabled analyzer's demand influences it, not just saturation's.
  Aggregation at both model and role level. Normalization into **saturation's units** (token
  capacity) for now; request-based normalization ("100 requests waiting in EPP queue") is the
  eventual target but is deferred. This is `single-analyzer`'s **CT7**, lifted into its own
  mission, with two user-directed departures: the composite gets a **new name**, and **all
  analyzers' Scores** affect the composition.
- **Worktree:** `worktrees/composite-analyzer` (branch `composite-analyzer`)
- **Role / scope:** mission owner — owns STATE, the plan, the `composite-analyzer` branch, and
  integration decisions for this mission only.
- **Ledger / log:** active `.session/2026-09-08-composite-analyzer-1.md`; captured retired
  `.session/ledger/<slug>.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `.session/spec.md` — mission spec, DRAFT v1, awaiting user review.
  *(do not read upfront — pull on demand only)*
- **Context:** (for implementation, once the spec is approved)
  - `internal/engines/steadystate/engine_v2.go` — `runAnalyzersAndScore` (:102),
    `collectV2ModelRequest` `CompositeSignal:` assignment (:797), `hasSaturationResult` (:722),
    `buildCapacities` (:850)
  - `internal/engines/allocation/optimizer_interfaces.go` — `NamedAnalyzerResult`,
    `ModelScalingRequest.CompositeSignal`
- **Refs:** *(do not read unless needed — all cross-worktree reads, authorized by user)*
  - `worktrees/single-analyzer/.session/compose-logic-plan.md` — the p3 "initial plan"; its
    RC-max premise does **not** hold on this base (see spec §2.3)
  - `worktrees/single-analyzer/.session/spec.md` — §CT7 and §"Semantic framework"
  - `worktrees/single-analyzer/.session/pr-spec-34-composite-signal.md` (merged PR #34),
    `pr-spec-next-coverage-units.md` (CT6 + its unfixed correctness bug)
  - `internal/engines/allocation/multi_backup/analyzer_helpers_multi.go` — pre-single-analyzer
    multi-entry aggregations to port (`//go:build ignore`, cannot compile as-is)
- **Expected output:** this phase — an approved mission spec. Mission overall — the composite
  aggregation implemented, with the test plan in spec §9.
- **Done / completion criteria:** spec approved by the user, with the 7 open items in spec §10
  decided.
- **Limits:**
  - **Until the spec is approved, ALL work stays in `.session/`.** No code, no writes outside
    this worktree (user instruction 2026-09-08). Cross-worktree **reads** are authorized.
  - Do **not** modify `internal/engines/allocation/multi_backup/` — upstream-tracked,
    `//go:build ignore`. Port its arithmetic into new code; leave the files alone.
  - **Normalization is deferred.** Do not build on CT6 / `normalizeToCompositeUnits`
    (absent from this base, and its parent-branch implementation has an unfixed correctness
    bug — spec §2.3). Composite stays in saturation's token units.
  - Branch base is `upstream/main` @ `4db060e2` (rebased 2026-09-08 with user approval; the
    original base `778a8893` went stale within hours — upstream is actively moving). Do not
    rebase again without user approval.
  - Preserve the **sat-only fast path** exactly: one analyzer ⇒ composite numerically identical
    to today. Non-negotiable.
  - **Do not invoke the upstream `pr-review` skill.** The user does not want it used on this
    mission (decision 2026-09-08). It ships tracked on `upstream/main` and was left in place
    unmodified — no deletion, no settings override. It already carries
    `disable-model-invocation: true`, so it is never model-invoked; treat it as off-limits even
    so. It runs only if the user explicitly types `/pr-review`.
- **Extra rules / rule refs:** `conventions/mission-owner.md`, `conventions/feature-worktree-setup.md`

## Execution

### Steps / subtasks
- [x] Create mission worktree off `upstream/main`
- [x] Set up `.session/`, skill symlinks, verify clean `git status`
- [x] Create initial `STATE.md`
- [x] Create `session-tracking/missions/composite-analyzer/` symlinks (uncommitted, by design)
- [x] Pin session into the worktree (`EnterWorktree`)
- [x] Commit `.session/` to the mission branch
- [x] Rebase onto current `upstream/main` (`4db060e2`)
- [x] Define mission goal and scope with the user
- [x] Read the parent mission's specs (p3 plan, CT7, semantic framework, PR #34, CT6)
- [x] Draft the mission spec → `.session/spec.md` (v1)
- [x] User review #1 → **v1's core conversion rejected**; spec rewritten as v2
- [ ] **User review #2 of `.session/spec.md` v2; decide the 8 open items in §10**
- [ ] Implement (post-approval): compose helpers, name change + quota-guard repair, tests

**Last completed:** `.session/spec.md` **v2** — rewrote §4 (conversion), §5 (aggregation), §6
(optimizer contract), §7 (Score) after the user's review.

**Next step / resume point:** get spec §10's 8 open items decided. The one real design question
is **#1 — which `Score` combinator** (§7.2 tables C1–C5 with floor-invariant analysis; user is
explicitly unsure; `score ≡ 1` today so nothing is blocked, and my recommendation is to keep
`max`+floor now and revisit weighting alongside the shared request-based demand unit). Also new:
**#3** — back-conversion representative for a role spanning SOs with differing `PRC_sat`. Do not
start implementation before approval.

### Status

- Environment: **ready** — worktree on branch `composite-analyzer`, rebased onto `upstream/main`
  @ `4db060e2`; 0 commits behind upstream, 1 ahead (the `.session/` commit). Only diff vs
  `upstream/main` is `.session/`. `.session/` tracked and committed, not gitignored;
  `resume-mission` + `wind-down` symlinks verified resolving into
  `session-tracking/claude-skills/`; `git status` clean.
- Session is **pinned** into this worktree via `EnterWorktree` — cross-worktree reads must use
  `cat <full-path>` or `git show <branch>:<path>`; `git -C` and `cd` elsewhere are blocked.
- Mission definition: **done** — see Orientation. Normalization deferred; sat units for now.
- Spec: **DRAFT v2**, `.session/spec.md` (522 lines). **Awaiting user review #2** — the only
  thing blocking implementation. 8 open items in §10.
- **Design core (settled by the user, not mine to revisit):** demand is per **(model, role)** —
  prefill/decode/both, independent numbers, not a per-SO split. Composition is **per SO in
  unit-free coverage and #replicas**. Back-conversion to sat units happens **once, at the end**,
  via `D_sat`. Cross-role rule `cov(M) = min(cov(prefill), cov(decode)) + cov(both)` is the
  *only* relation between role and model level. The existing data model already has this shape
  (`AnalyzerResult.RoleDemand` per-(model,role); `VariantCapacity.PerReplicaCapacity` per-SO).
- **Rejected approach — do not reintroduce:** converting each analyzer's demand into sat units
  before aggregating (`D_i/PRC_i × PRC_sat`). It is circular (routes every analyzer through the
  `PRC_sat` estimate those analyzers exist to correct) and uses a different factor per SO, so the
  result is denominated in nothing coherent. Kept in spec §4.1 as a record.
- Key finding: the p3 "initial plan" (`compose-logic-plan.md`) justifies its `max RC` rule with
  post-CT6-normalization reasoning that does **not** hold on this base —
  `normalizeToCompositeUnits` is absent from upstream, and its parent-branch implementation has
  an unfixed `1/PRC` correctness bug. Spec §2.3 records this.
- `session-tracking` symlinks: **created** (`missions/composite-analyzer/{STATE.md,ledgers}`),
  verified resolving. Left **uncommitted** per user instruction — `policy-writer` commits
  `session-tracking`. No agentbus `pending-commits` note published yet (not authorized).

### Known issues

- none

## Session log
- 2026-09-08 ledger=.session/2026-09-08-composite-analyzer-1.md status=active
