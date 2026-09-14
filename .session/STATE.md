# composite-analyzer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** The **composite aggregation calculation** — reduce the
  `[]NamedAnalyzerResult` from `runAnalyzersAndScore` into the single `CompositeSignal` the
  optimizer consumes, so every enabled analyzer's demand influences it, not just saturation's.
  This is `single-analyzer`'s **CT7**, lifted into its own mission. Full design: `.session/spec.md`.
- **Worktree:** `worktrees/composite-analyzer` (branch `composite-analyzer`)
- **Role / scope:** mission owner — owns STATE, the plan, the `composite-analyzer` branch, and
  integration decisions for this mission only.
- **Ledger / log:** none active — this session retiring. Captured retired ledgers:
  `.session/ledger/2026-09-08-composite-analyzer-1.md`,
  `.session/ledger/2026-09-08-composite-analyzer-2.md`
  ⚠ DO NOT READ retired ledgers — not yours

## Task

- **Plan / spec:** `.session/spec.md` — mission spec, **v9, redesign recorded [USER, 2026-09-14]**.
  Holds the design (§1–§9, describing v8's shape — not yet rewritten for v9, see below), decisions
  (§10), and revision/decision history (§12, where v9's full redesign summary lives). **This is
  where design content and decision reasoning live, not here.** *(do not read upfront — pull on
  demand only; for the redesign specifically, read §12's v9 entry directly)*
- **Survey:** `.session/survey-zero-signal.md` *(pull on demand)*
- **v8 implementation task files (superseded by v9, kept for history):** `.session/task-coder-agg1.md`,
  `.session/task-reviewer-agg1.md`, `.session/review-coder-agg1.md` (verdict PASS on v8's design,
  which v9 restructures but does not invalidate mathematically).
- **v9 implementation task (NEW, not yet dispatched):** `.session/task-coder-composite-redesign.md`
  — 8-item checklist implementing spec §12's v9 entry (rename N(SO)→TotalReplicas, sat-only fields,
  per-SO participation check, relocate AggN/PRCCom into composite.go, unify roleOf, new composition
  logging, Ready-vs-usefully-serving code comment, verify Supply/RC/SC formulas unchanged). Not yet
  authorized/dispatched to a coder — see Execution below.
- **Code review (interrupted, not abandoned):** `.session/review/code-review-notes.md` — user's
  own step-by-step code review of the v8 implementation (finished: aggregation package, allocation
  core; steadystate wiring/`composite.go` was started 2026-09-13 but the walkthrough turned into
  the redesign discussion below before any findings were recorded for that file). No code changed
  during this review — findings/rulings only. File unchanged this session (still ends at §9, dated
  2026-09-09). This review is now superseded in scope by the v9 redesign — resuming it should wait
  until v9 is implemented, since v8's `composite.go` will not exist in its reviewed form. Also in
  that dir: `.session/review/composite-diff-review.html` — an HTML diff viewer built earlier
  (superseded as a *process* by the `diff-review-page` agent — see Extra rules/tools below — but
  the file itself is still valid to view).
- **Redesign discussion (RESOLVED, folded into spec v9):**
  `.session/composite-signal-redesign.md` — 2026-09-13/14, user called for a **complete redesign**
  of `composite.go`, not incremental fixes (comments too spec-coupled, duplicate saturation-lookup
  logic x3, unclear why max-score, unclear provenance when analyzers disagree, duplicated
  aggregation, hard to follow). Escalated into a design-level discussion, now concluded: §1–§4.2 are
  all settled and cited against code; §4's core ruling (sat is the sole source of every field except
  PRC/Reason, and of the variant set itself), §4.1's implementation-plan draft (naming, relocation,
  per-SO participation rule), and §4.2's Supply/AnticipatedSupply/RC/SC restatement are all folded
  into `spec.md` §12's v9 entry — that entry is now the authoritative summary; this doc remains the
  citation-backed detail behind it. **§5 (parked: disagreement-logging — resolved into v9's new
  composition-level log line; duplicated-lookup cleanup — resolved into v9's relocation items) not
  formally closed out in the doc itself but effectively superseded by v9 — low priority to tidy.**
- **Expected output:** mission is implementation-complete (see Status). User has chosen to review
  the code manually before deciding on PR / more work / wind-down.
- **Done / completion criteria:** implementation matches spec v8, reviewed, tests pass — **met**.
  Mission fully closes when the user decides next steps (PR / more work / wind-down) — deferred
  further while the user's own code review is ongoing.
- **Limits:**
  - Do **not** modify `internal/engines/allocation/multi_backup/`.
  - Never push or open a PR without a fresh, per-operation authorization.
  - Do not rebase without asking first.
  - Do not reopen spec §10 (D1–D4) or amend the approved design without the user raising it.
  - No pipeline redesign under this mission — a separate, later, clean discussion (user, 2026-09-09).
  - Do not invoke the upstream `pr-review` skill on this mission (user decision).
  - Edit this STATE.md directly — no `.wip` rename-lock (user instruction, 2026-09-09; see memory
    `feedback_no_wip_on_own_state`).
- **Extra rules / rule refs:** `conventions/mission-owner.md`, `conventions/coder-orchestration.md`,
  `conventions/worktree-delegation.md`
- **New tooling (session-external, not mission-scoped):** a user-level custom agent
  `~/.claude/agents/diff-review-page.md` (subagent_type `diff-review-page`, runs on Haiku by
  default) now exists — turns a git diff into a browsable HTML review page, saved to disk, and
  opens it automatically via `wslview` (this machine is WSL2 — also now documented in
  `~/.claude/CLAUDE.md`). Untested end-to-end as of this mission's last session (created and
  edited in-session, so it wasn't visible to the `Agent` tool until a fresh session — custom
  agent definitions load once at session start).

## Execution

### Steps / subtasks
- [x] Mission defined, spec drafted and approved through v8 (see spec §12 for the full v1→v8
      revision history — corrections, user decisions, veto pass)
- [x] Implementation authorized (2026-09-09) and dispatched: `coder-agg1` (same-worktree/async) +
      `reviewer-agg1` (continuous review), per `.session/task-coder-agg1.md`'s 12-item checklist
- [x] All 12 checklist items landed and independently verified (build, `make test` scope, both
      non-negotiable regression guards)
- [x] Reviewer found one real deviation (O1/O2 placement, commit `0ec6c170`); user ruled on it;
      coder fixed (`0642f472`); reviewer independently re-verified the fix
- [x] **Final verdict: PASS, 12/12.** Full detail: spec §12's implementation entry,
      `.session/review-coder-agg1.md`.
- [x] User direction on next steps: **user chose to do their own step-by-step code review of the
      implementation** before deciding PR / more work / wind-down.
- [x] Code review: general comments + aggregation package (production files only:
      `demand.go`, `undefined.go`, `model_coverage.go`, `replicas_needed.go`, `prc_com.go`) +
      allocation core (production files only: `composite_eligibility.go`, `composite_identity.go`,
      `composite_decision.go`, `composite_signal_gate.go`) done (see
      `.session/review/code-review-notes.md`, §1-§9).
- [ ] Code review of `composite.go`/steadystate wiring **started 2026-09-13, then diverted into
      a redesign discussion** (see Task section above) — no findings recorded for this file in
      `code-review-notes.md`; superseded for now by `.session/composite-signal-redesign.md`.
      **Remaining-file list below is still accurate but ON HOLD until the redesign discussion
      concludes** — do not resume file-by-file review until §4 of the redesign doc is resolved,
      since the outcome may change what "reviewing composite.go" even means.
      - `internal/engines/allocation/analyzer_helpers.go`
      - `internal/engines/allocation/query_api.go` (+ `query_api_test.go`)
      - `internal/engines/allocation/cost_aware_optimizer.go`
      - `internal/engines/allocation/rescale.go`
      - `internal/engines/steadystate/composite.go` (+ `composite_test.go`,
        `composite_observability_test.go`)
      - `internal/engines/steadystate/engine.go`, `engine_v2.go` (+
        `engine_v2_log_test.go`, `engine_v2_population_test.go`, `engine_v2_quota_test.go`,
        `engine_signal_blocked_wiring_test.go`)
      - `internal/constants/metrics.go`
      - `docs/reference/cycle-log.md`
      - all test files under `aggregation/` and `allocation/composite_*_test.go` (`demand_test.go`,
        `undefined_test.go`, `model_coverage_test.go`, `replicas_needed_test.go`,
        `prc_com_test.go`, `composite_decision_test.go`, `composite_eligibility_test.go`,
        `composite_signal_gate_test.go`)
- [x] **Redesign discussion resolved (2026-09-14):** `.session/composite-signal-redesign.md` §4
      (sat-only field/variant-set ruling), §4.1 (implementation-plan draft: per-SO participation
      rule, naming, relocation), §4.2 (Supply/AnticipatedSupply/RC/SC restatement, unchanged
      formula) all settled with the user and folded into `spec.md` §12's **v9** revision entry.
- [x] v9 folded into spec.md (§12); dedicated coder task drafted:
      `.session/task-coder-composite-redesign.md` (8-item checklist).
- [ ] **v9 implementation — NOT YET DISPATCHED.** Task file exists but no coder has been
      authorized/started on it. Per-operation authorization still required before dispatch (mission
      convention) — do not assume the drafting of the task file is itself authorization to dispatch.
- [ ] Resume code review of `composite.go`/steadystate wiring — **wait until v9 is implemented**,
      since v8's `composite.go` (what `code-review-notes.md` would otherwise review) will no longer
      exist in its current form. Remaining-file list (unchanged, still accurate):
      - `internal/engines/allocation/analyzer_helpers.go`
      - `internal/engines/allocation/query_api.go` (+ `query_api_test.go`)
      - `internal/engines/allocation/cost_aware_optimizer.go`
      - `internal/engines/allocation/rescale.go`
      - `internal/engines/steadystate/composite.go` (+ `composite_test.go`,
        `composite_observability_test.go`) — **will change shape under v9**
      - `internal/engines/steadystate/engine.go`, `engine_v2.go` (+
        `engine_v2_log_test.go`, `engine_v2_population_test.go`, `engine_v2_quota_test.go`,
        `engine_signal_blocked_wiring_test.go`)
      - `internal/constants/metrics.go`
      - `docs/reference/cycle-log.md`
      - all test files under `aggregation/` and `allocation/composite_*_test.go` (`demand_test.go`,
        `undefined_test.go`, `model_coverage_test.go`, `replicas_needed_test.go`,
        `prc_com_test.go`, `composite_decision_test.go`, `composite_eligibility_test.go`,
        `composite_signal_gate_test.go`) — **`replicas_needed_test.go`/`prc_com_test.go` will
        likely move/disappear under v9's relocation**
- [ ] User direction on next steps (PR / more work / wind-down) — deferred until v9 is implemented
      AND the resumed code review both conclude.

**Last completed (this session, 2026-09-13/14):** resolved the redesign discussion end to end.
§4: ruled every composite field comes from saturation alone except PRC and Reason, and saturation
is also the sole source of the variant set (no cross-analyzer union, no fallback). §4.1: drafted
and refined an 8-step implementation plan (verified against code at each step — per-SO
participation rule traced through all 3 analyzers' actual failure-path code, confirming only
saturation needs an explicit Reason check since throughput/external already opt out by omission;
naming settled as `TotalReplicas`/`CompositeTotalReplicas`; relocation of `AggN`/`PRCCom` out of
the `aggregation` package into `composite.go` decided via a verified single-caller-per-function
rule; `roleOf`/`roleOfVC` duplication flagged for unification). §4.2: confirmed
Supply/AnticipatedSupply/RC/SC formulas are unchanged, restated in terms of the new names, plus a
flagged (not fixed) Ready-vs-usefully-serving gap in what `ReplicaCount` represents. Folded
everything into `spec.md` §12 as a new **v9** revision entry, then wrote a dedicated coder task
file, `.session/task-coder-composite-redesign.md`, translating v9 into an 8-item implementation
checklist plus a completeness check (compare against the pre-single-analyzer engine-side logic)
and the same non-negotiable regression guards (sat-only identity, Score has no effect) v8's task
carried. **Not yet dispatched to a coder** — drafting the task is not authorization to start it.

**Prior session's last-completed (2026-09-08/09, for reference):** built an HTML diff-review page
for this mission's diff, generalized into the `diff-review-page` custom agent plus a standing
WSL2/`wslview` convention in `~/.claude/CLAUDE.md`; ran the first part of the step-by-step code
review (general comments, aggregation package, allocation core) — paused at the user's request.

**Next step / resume point:** ask the user whether to dispatch `.session/task-coder-composite-redesign.md`
now. If yes: same dispatch pattern as v8 (`coder-agg1`/`reviewer-agg1` were same-worktree/async
with a continuous reviewer) — check with the user whether to reuse those same idle agents (still
holding open on their `In:` channels per the last session's note, not terminated — find them via
`ListAgents` by name `coder-agg1`/`reviewer-agg1` if their IDs aren't in context) or start fresh
ones, since this is materially different work from what they were dispatched for originally. If
the user wants to discuss anything else about v9 first (e.g. picking the exact
`CompositeTotalReplicas`-equivalent identifier, which the task file left open), that takes
priority over dispatch. Do not resume the file-by-file code review until v9 lands — see the
checklist item above for why.

### Status

- Environment: **ready** — branch `composite-analyzer`, rebased onto `upstream/main` @
  `c013012e` (note: `upstream/main` has since moved further, to `b01a6e17` as of this session —
  not re-rebased, per "do not rebase without asking first"). `git status`: `.session/review/` is
  untracked (prior session's review scratch output), plus this session's edits to `spec.md`,
  `STATE.md`, `composite-signal-redesign.md`, and the new `task-coder-composite-redesign.md`.
- Mission: **implementation-complete on v8** (still true — v9 is a restructuring, not a new
  design). The redesign discussion that paused the user's own code review is now **resolved**
  and folded into spec v9. **v9's implementation is drafted but not dispatched.** Nothing pushed,
  no PR opened.
- For the *design* (what was built and why), decision history, rejected approaches, and
  verification detail: **spec.md §1–§9 (design, still describes v8's shape), §10 (decisions),
  §12 (revision history — v9 is the most recent entry and the one to read for the current design)**
  — not restated here. For the completed part of the v8 code review:
  `.session/review/code-review-notes.md`. For the resolved redesign discussion's full citation
  detail: `.session/composite-signal-redesign.md`. For the v9 implementation task:
  `.session/task-coder-composite-redesign.md`. For prior session narrative: the retired ledgers in
  `.session/ledger/`.

### Known issues

- **Pending project-direction item, not yet actioned:** during the code review (§7/§9.6 of
  `.session/review/code-review-notes.md`), the user flagged that `satDemand`/`D_sat` naming
  encodes a transitional implementation choice (using saturation's result as the demand source)
  rather than the durable intended concept — a **canonical composite demand**, meant to make
  PRC/demand comparable **across models**, not just across analyzers within one model. The
  project plans to move away from anchoring the demand unit on "sat" specifically. **v9 (spec §12)
  makes this MORE true, not less** — every composite field, not just demand, is now sat-only by
  explicit ruling — and v9's own text flags this same tension as "explicitly not addressed by this
  revision." Still not folded into a resolved design; flagged here so it isn't lost.

## Session log
- 2026-09-08 session=2026-09-08-composite-analyzer-1 status=retired ledger=.session/ledger/2026-09-08-composite-analyzer-1.md
- 2026-09-08 session=2026-09-08-composite-analyzer-2 status=retired ledger=.session/ledger/2026-09-08-composite-analyzer-2.md
- 2026-09-12 session=2026-09-12-composite-analyzer-1 status=active ledger=.session/2026-09-12-composite-analyzer-1.md
