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
- **Ledger / log:** `.session/2026-09-15-composite-analyzer-1.md` — active this session.
  Captured retired ledgers: `.session/ledger/2026-09-08-composite-analyzer-1.md`,
  `.session/ledger/2026-09-08-composite-analyzer-2.md`. `.session/2026-09-14-composite-analyzer-2.md`
  is retired and `## Verified 2026-09-14`-captured but not yet moved to `.session/ledger/`.
  ⚠ DO NOT READ retired ledgers — not yours

## Task

- **Plan / spec for the redesign — READ THIS ONE, NOT spec.md:**
  `.session/composite-signal-redesign.md` **§2 is the current, authoritative, coder-ready spec**
  for the composite-building rewrite — **IMPLEMENTED**, see Status below. Restructured
  2026-09-14 (twice: once into a §1-5 split, then again the same session into an 8-section
  template — §1 orientation, §2 spec/settled-rules, §3 open items, §4 coder task hierarchy, §5
  discussion abstracts, §6 summary of decisions, §7 detailed discussion, §8 revision log; commit
  `37886266`). §2's own subsection numbers (2.1-2.10) were preserved across both restructures —
  every `§2.x` reference below still resolves. `spec.md`'s v9 entry (§12) is **STALE**, not
  re-synced, not blocking — tracked in the doc's §3.
- **v9 implementation task:** `.session/task-coder-composite-redesign.md` — **DONE**, see Status.
  Not yet updated for the pending §2 wording/naming fixes below — do so before any future
  dispatch against it.
- **Pending spec fixes from the user's post-implementation review, NOT YET APPLIED:** eight
  items against `composite-signal-redesign.md` §2.1/§2.1.d/§2.1.e/§2.3/§2.6/§2.7/§2.8/§3/§7 (one
  is a real code change — §2.1.d's `CompositeTotalReplicas` extraction; one names a confirmed
  gap needing a user design ruling — §2.6's `SOHasSignal` zero-caller finding). Full detail,
  code verification, and the per-item edit-plan table: `.session/drafts/2026-09-14-spec-review-response.md`.
- **Two investigations requested, not started:**
  1. Re-verify current implementation against HEAD (quick — was last verified mid-session,
     before the round-2 review; nothing should have changed since, but not re-confirmed).
  2. **Pre-single-analyzer aggregation comparison** (potentially substantial): identify the
     former aggregations as done in the optimizer pre-single-analyzer, confirm they still have
     "similar meaning" under v9 — for sat-only, NO CHANGE; for multi-analyzer, correct math and
     units. Required both for general completeness (§2.10) and specifically for testing. STATE
     previously noted `git log`/`git blame` on `engine_v2.go` as the way to find the
     pre-single-analyzer code if it isn't otherwise obvious; ask the user directly if that
     doesn't turn it up quickly.
- **Survey:** `.session/survey-zero-signal.md` *(pull on demand)*
- **v8 implementation task files (superseded by v9, kept for history):** `.session/task-coder-agg1.md`,
  `.session/task-reviewer-agg1.md`, `.session/review-coder-agg1.md` (verdict PASS on v8's design,
  which v9 restructures but does not invalidate mathematically).
- **Code review (interrupted, not abandoned):** `.session/review/code-review-notes.md` — user's
  own step-by-step code review of the v8 implementation (finished: aggregation package, allocation
  core; steadystate wiring/`composite.go` was started 2026-09-13 but the walkthrough turned into
  the redesign discussion below before any findings were recorded for that file). Also gained a
  new §10 this session (rounding-function naming/duplication in `query_api.go` and friends —
  recorded, not actioned). No code changed during this review — findings/rulings only. This
  review is superseded in scope by the v9 redesign — resuming it should wait until v9 is
  implemented, since v8's `composite.go` will not exist in its reviewed form. Also in that dir:
  `.session/review/composite-diff-review.html` — an HTML diff viewer built earlier (superseded as
  a *process* by the `diff-review-page` agent — see Extra rules/tools below — but the file itself
  is still valid to view).
- **Redesign discussion: RESOLVED** — user called for a complete redesign of `composite.go`
  (2026-09-13/14), fully settled this session. Two corrections worth knowing before editing §2
  again: sat's eligibility check must resolve once, upstream of the per-SO loop, never by name
  inside it; the call stack's outer placement was already decided at spec v4/§6.2/v8. Full
  narrative: `.session/composite-signal-redesign.md` §5 (abstracts), §6 (D1-D3), §7.2.
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
  - Ownership rule for `.wip` (user instruction 2026-09-09, memory `feedback_no_wip_on_own_state`):
    the mission owner OWNS this file — edit it directly, never `.wip`. Any other agent (including
    one the mission owner dispatches) does NOT own it — it MUST use the `.wip` lock, always,
    regardless of who dispatched it. `.wip` is an ownership rule, not a concurrency-count rule.
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
- [x] Redesign discussion resolved (2026-09-14). Authoritative spec: `composite-signal-redesign.md`
      §2 (settled rules); §5 holds the reasoning/history.
- [x] Coder task file fully rewritten: `.session/task-coder-composite-redesign.md` — every
      signature/destination/shape spelled out, `user.in` progress-reporting per step.
- [x] `spec.md` §12 is stale (not re-synced after later corrections to the redesign doc) —
      `composite-signal-redesign.md` §2 is authoritative; re-sync tracked in that doc's §3.
- [x] **Implementation — DONE 2026-09-14 (session -2).** Coder `coder-redesign`, 4 invocations,
      final commits `bff67c6f`, `6eb92892`, `4d864175`, `748261de`. Independently verified
      (build/vet clean, `make test` passing, old functions removed, both regression guards
      tested end-to-end). Incident/decision detail: spec §6 (D3-D6), §7.2.
      - No reviewer attached — decide with the user whether to attach one.
      - Not yet reviewed by the user. Second review round found spec-wording gaps, not code
        bugs — see Task section above and Next step below.
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
- [x] Full `CompositeSignal` usage audit (supply/demand/RC-SC, all consumers, not just
      PRC-touching ones) — `.session/composite-signal-full-usage-audit-2026-09-15.md`. Corrected
      a first, narrower PRC-only pass after user review; confirmed `buildCapacities` is composite
      construction, not downstream; confirmed `DecisionNoSignal` always implies sat's own raw PRC
      is `<=0` (so existing `<=0` guards already exclude every genuine no-signal SO); found
      saturation's P0-store estimator produces the same decision-path string as a live sat
      fallback (open question, deferred, not CC).
- [x] Scoped a minimal guard-fix change ("CC") out of the investigation above, deferring
      everything else to a companion doc. CC = 3 guard fixes, written as new settled-rule
      subsections `composite-signal-redesign.md` §2.11 (missing `<=0` guard in
      `aggregation.go`'s supply sums), §2.12 (model-level `CompositeHasSignal()` guard for demand
      consumers — corrected mid-session from a wrongly-named `Eligible()`, per user catch), §2.13
      (remove `e.Score` from `sortVariantsForScaleDown`, verified against pre-single-analyzer
      code). Deferred items (PRC-accessor contract unification, guard relocation into shared
      helpers, P0-store/live-fallback distinguishability, per-role demand-health markers,
      supply-alternatives write-up) recorded in new companion doc
      `.session/composite-signal-post-cc-followups.md`, same 8-section template.
      **§2.11-2.13 are spec-only — NOT YET implemented in code, not yet a coder task, not yet
      dispatched. Both doc edits are UNCOMMITTED as of this checkpoint.**
- [x] Refreshed the stale (2026-09-09) HTML diff-review page (`.session/review/composite-diff-review.html`)
      via the `diff-review-page` custom agent, scoped `c013012e..HEAD` on `internal/` (full v8+v9
      implementation). Fixed a one-character `id` mismatch bug in the generated page's own script
      that had left every diff panel empty (root-caused directly, single `Edit`). Untracked
      scratch output by existing convention — not committed, matching its prior state.
- [ ] User direction on next steps (PR / more work / wind-down) — deferred until v9 is implemented
      AND the resumed code review both conclude. Now also gated on CC landing first (see Next
      step below) — CC's guard fixes are a real (if small) code change against the same files the
      resumed code review would cover.

**Last completed (2026-09-15, session -1):** full `CompositeSignal` usage audit (corrected from
an initial PRC-only pass per detailed user review), scoped into a minimal "CC" guard-fix change
specified in `composite-signal-redesign.md` §2.11-2.13, everything else deferred to
`.session/composite-signal-post-cc-followups.md`. STATE.md itself was pruned twice this session
(once by the mission owner directly — **a process violation**, the user wanted a background
agent, not the mission owner acting unilaterally after the first dispatch failed; corrected via a
properly-dispatched round 2 with a `.wip` lock). Diff-review page refreshed and a real rendering
bug fixed. Full narrative, every correction, and the exact code citations verified during this
session: ledger `.session/2026-09-15-composite-analyzer-1.md` (not yet ledger-captured — see
Session log; note also records this ledger was not maintained continuously during the session,
only backfilled at checkpoint). Checkpointed at the user's request ("clear the session" → safe
checkpoint, not a retirement) — mission ownership is not released.

**Superseded — prior session (2026-09-14, session -2):** v9 implemented and independently
verified; a second review round found real spec-doc gaps (not code bugs). Full narrative,
incidents, and decisions: spec doc §5 (abstracts), §6 (decisions D1-D7), §7.2 (incidents 1-4), §8
(revision log); ledger `.session/2026-09-14-composite-analyzer-2.md` (`## Verified`).

**Superseded — prior session (2026-09-13/14):** resolved the redesign discussion end to end.
Full narrative lives in spec doc §5-§8 and ledger `.session/2026-09-14-composite-analyzer-1.md`.

**Prior session (2026-09-08/09):** built an HTML diff-review page for this mission's diff,
generalized into the `diff-review-page` custom agent plus a standing WSL2/`wslview` convention
in `~/.claude/CLAUDE.md`; ran the first part of the step-by-step code review (general comments,
aggregation package, allocation core) — paused at the user's request.

**Next step / resume point (as of end of 2026-09-15 session -1, checkpoint):** CC's guard fixes
are fully specified (`composite-signal-redesign.md` §2.11-2.13) but not implemented. In order:
1. Commit the two doc edits from this session (`composite-signal-redesign.md` §2.11-2.13,
   `.session/composite-signal-post-cc-followups.md`) — currently uncommitted.
2. Get the user's go-ahead to turn §2.11-2.13 into a coder task (or apply directly, if small
   enough) — three targeted fixes: `aggregation.go`'s missing `<=0` guard,
   `demandForRoleOrModel`/`requiredSpareForRoleOrModel`/`fairShareValue` gated on
   `allocation.CompositeHasSignal(req.CompositeSignal)`, and `e.Score` removed from
   `sortVariantsForScaleDown`. Per the standing process rule (2026-09-14 session), any new coder
   task file must include a design-validation checkpoint before implementation.
3. Once CC lands and is verified: the pending spec fixes from the 2026-09-14 round-2 review
   (`.session/drafts/2026-09-14-spec-review-response.md` — `eligibleAnalyzers`→`enabledAnalyzers`
   rename, §2.1.d's `CompositeTotalReplicas` extraction, wording fixes) are still open and
   unapplied — batch with CC or do separately, ask the user.
4. Resolve the open design question from §2.6/the post-CC-followups doc §7.3 (P0-store vs. live
   sat-fallback distinguishability) — needs the user's ruling, not more investigation.
5. Do the two pending investigations from the Task section (quick re-verify; the larger
   pre-single-analyzer aggregation comparison).
6. Only after 1-5: decide with the user whether to attach a reviewer, and whether to resume the
   file-by-file code review of `composite.go`/steadystate wiring (now touching CC's changes too).
7. `spec.md` stays stale-by-design; re-sync only if the user asks.

### Status

- Environment: **ready** — branch `composite-analyzer`, rebased onto `upstream/main` @
  `c013012e` (`upstream/main` has since moved further, to `b01a6e17` — not re-rebased, per
  "do not rebase without asking first"). `git status` at checkpoint: 2 uncommitted doc edits
  (`composite-signal-redesign.md` §2.11-2.13, new file
  `.session/composite-signal-post-cc-followups.md`) plus the long-standing untracked
  `.session/review/composite-diff-review.html` (refreshed and bug-fixed this session, still
  correctly untracked per existing convention).
- Mission: v9 implemented/verified/not yet user-reviewed; CC (3 guard fixes) specified but not
  implemented — see Task and Execution above for pointers; nothing pushed, no PR opened.
- Ledger pointers not covered by Orientation: `.session/2026-09-14-composite-analyzer-2.md` and
  `.session/2026-09-14-composite-analyzer-1.md` — both retired and `## Verified`-captured (see
  Session log), full narrative there. This session's own ledger
  (`.session/2026-09-15-composite-analyzer-1.md`) is active, not yet ledger-captured.

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
- 2026-09-12 session=2026-09-12-composite-analyzer-1 status=retired ledger=.session/2026-09-12-composite-analyzer-1.md
- 2026-09-14 session=2026-09-14-composite-analyzer-1 status=retiring (user /clear-ing) ledger=.session/2026-09-14-composite-analyzer-1.md
- 2026-09-14 session=2026-09-14-composite-analyzer-2 status=retired ledger=.session/2026-09-14-composite-analyzer-2.md
- 2026-09-15 session=2026-09-15-composite-analyzer-1 status=active ledger=.session/2026-09-15-composite-analyzer-1.md
