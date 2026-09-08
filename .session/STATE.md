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
- [x] User review #2 → six factual corrections, all re-verified against upstream source; **v3**
- [ ] **User review #3 of `.session/spec.md` v3; decide the 10 open items in §10**
- [ ] Implement (post-approval): compose helpers in/beside `internal/engines/aggregation/`,
      name change + quota-guard repair, 25-case test plan

**Last completed:** `.session/spec.md` **v3** — rewrote §2 (ground truth, now source-verified),
§4 (aggregation space), §5 (helpers), §8 (identity), §9 (25 tests), §10 (open items).

**Next step / resume point:** get spec §10's 10 open items decided. The only real design question
is **#1 — which `Score` combinator** (§7.2 tables C1–C5 against the floor invariant; user
explicitly unsure; `score ≡ 1` today so nothing is blocked in practice; my recommendation is
`max`+floor now, revisiting weighting *with* the shared request-based demand unit). Do not start
implementation before approval.

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
- **Design core (settled by the user, not mine to revisit):**
  - **PRC is per SO** (implies model, variant, role). **Demand is per (model, role)** — three
    values (`both`, `prefill`, `decode`) that do **not** depend on which SOs exist. SOs are added
    and removed; a role's demand does not change because of that. Converse also holds: an SO can
    have a real PRC while its role's demand is 0. True for every analyzer, saturation included.
  - **Storage layout has two shapes:** `AnalyzerResult.RoleDemand` is **nil** when not
    disaggregated, and the `both` demand then lives in **`TotalDemand`** — there is no `both` map
    key. Read demand only through one accessor that handles both.
  - **Composition is per SO in unit-free coverage and #replicas.** Back-conversion to sat units
    happens **once, at the end**, derived from `D_sat` — never by picking a representative SO.
  - **Coverage (PRC/demand) is meaningless when either is zero.** Every calculation guards it;
    an undefined contribution must never enter a `min`/`max` as `0` or `+Inf`. `demand == 0`
    flows through as `0`, never manufactured into `1.0`.
  - Cross-role rule `cov(M) = min(cov(prefill), cov(decode)) + cov(both)` is the *only* relation
    between role and model level.
  - Within a round, a **consistent request shape per model** is assumed — this is what licenses
    aggregating at all. Aggregate only within one model, within one round.
- **Implementation seam:** `internal/engines/aggregation/` already exists — pure helpers named for
  what they aggregate (`SumTotalDemand`, `DemandByRole`, `AggregateByRole`, `IsDisaggregated`, …),
  already used by both analyzers. **New aggregations extend that package.** It is absent from every
  parent-mission document, which is why the user's "upstream is the source of truth" rule matters.
- **Rejected approach — do not reintroduce:** converting each analyzer's demand into sat units
  before aggregating (`D_i/PRC_i × PRC_sat`). It is circular (routes every analyzer through the
  `PRC_sat` estimate those analyzers exist to correct) and uses a different factor per SO, so the
  result is denominated in nothing coherent. Kept in spec §4.1 as a record.
- **Lessons banked from `single-analyzer-normalize`** (deferred branch; learn from, do not build
  on): **deep-copy is mandatory** — a plain value copy of `NamedAnalyzerResult` aliases `Result`/
  `RoleCapacities`/`RoleSpare` and silently mutates saturation's own entry (`da0e1ee8`);
  **`demand == 0` must flow through as `0`** (`77f21355`); `allocation.CompositeSignalName` and an
  extra `analyzer-result` log line already exist as precedent, though its `%` unit does not apply
  to us (ours is sat units).
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
