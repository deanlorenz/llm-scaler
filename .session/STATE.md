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
- **Pending spec fixes from the user's post-implementation review, NOT YET APPLIED** (full
  detail + code verification: `.session/drafts/2026-09-14-spec-review-response.md`):
  - `eligibleAnalyzers` → rename to `enabledAnalyzers` (matches what it actually filters on) —
    small, mechanical, code + doc, needs a coder task or a direct small edit.
  - §2.1 call-stack block: trim inline WHY-prose, push reasoning to §7 pointers only.
  - §2.1.d: `CompositeTotalReplicas` should become an actual function (currently inline in the
    `switch`) — **real code change**, needs a coder task, not just a doc edit.
  - §2.1.e: reword — only `D_sat[role]` (the numerator) is fetched once per role; PRC itself is
    still computed per SO.
  - §2.3: wording/tense fix only (confirmed still factually correct — "moves into" → "now lives
    in").
  - §2.6: add a line that `engine.go`/`engine_v2.go` are the same `Engine`, not two versions;
    `CompositeHasSignal` is live, called every reconcile cycle. **Real gap found and confirmed,
    not yet resolved as a design question:** `SOHasSignal` (per-SO check) has zero production
    callers anywhere; the actual optimizer files never read the decision-path/Reason at all —
    `CompositeHasSignal` passing at the whole-request level says nothing about which individual
    SOs within it actually have signal. Verified this does NOT crash/corrupt today (a no-signal
    SO's PRC falls through to sat's own PRC, and every optimizer PRC consumer guards `<=0`) —
    but the optimizer cannot currently distinguish a real signal from a no-signal fallback.
    **Open question for the user:** should the optimizer (or something upstream) consume
    `SOHasSignal` per-SO, and if so, do what with a no-signal SO?
  - §2.7: add a TODO comment (both in spec and on `allocation.TotalReplicas`,
    `composite_decision.go:43`) — future work to adjust `TotalReplicas`/`N_i(SO)` by
    per-analyzer thresholds; not this task.
  - §2.8: add the user's exact future-direction correction (true Supply from ready-replica-count;
    Anticipated from CurrentReplicas, not ReplicaCount+Pending) as the eventual-fix target,
    keeping "accepted for now" framing for current behavior.
  - §3: split into explicit Blocking / Non-blocking sub-lists; fix the `query_api.go` item's
    wording — it's not just "naming/duplication," it's 3 duplicated implementations plus
    inconsistent naming (`code-review-notes.md` §10 has the full finding).
  - §7: note that every finding needs re-checking against pre-single-analyzer code specifically,
    once that comparison (below) is done.
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
- **Redesign discussion (RESOLVED — see spec pointer above):**
  `.session/composite-signal-redesign.md` — 2026-09-13/14, user called for a **complete redesign**
  of `composite.go`, not incremental fixes. Fully resolved and restructured this session. Key
  corrections made 2026-09-14 that are easy to miss if skimming: (1) sat's config-enabled/disabled
  contributor-eligibility check must be resolved ONCE, upstream of the per-SO loop (an
  `eligibleAnalyzers` slice built before `buildComposite` is called) — sat's name must never be
  tested inside the per-SO collection loop itself, and must not appear in
  `HasUsableCompositeSignal`'s replacement checks either (both were fixed after the user caught
  the same mistake twice); (2) the call stack's outer placement (`buildComposite` called from
  `collectV2ModelRequest`, never from inside `runAnalyzersAndScore`) was already decided at spec
  v4/§6.2/v8 — an earlier pass at this doc got it wrong and had to be corrected.
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
- [x] **Redesign discussion resolved (2026-09-14):** all design questions settled with the user,
      through several rounds of correction. Current authoritative spec:
      `.session/composite-signal-redesign.md` §2 (restructured this session into a
      spec/discussion split, per `conventions/tasks.md`'s mission-spec structure — §2 is settled
      rules only, no citations; §5 holds all the reasoning/history).
- [x] Coder task file **fully rewritten** (not the original 8-item checklist):
      `.session/task-coder-composite-redesign.md` — every function signature, file destination,
      and struct/switch shape spelled out concretely; no open naming/placement decisions left for
      the coder. Includes `user.in` progress-reporting per step.
- [x] `spec.md`'s v9 entry (§12) was folded in early, then the redesign doc got corrected several
      times AFTER that (sat-visibility-after-compose fix, call-stack outer-placement fix, a
      dropped-content recovery after an improper single-`Write` rewrite) — **`spec.md` was never
      re-synced and is now stale.** Do not treat `spec.md` as authoritative for this redesign;
      `composite-signal-redesign.md` §2 is. Re-sync is tracked in that doc's §3, not yet done.
- [x] **Implementation — DONE 2026-09-14 (session 2026-09-14-composite-analyzer-2).** Coder
      `coder-redesign` ran same-worktree/async, 4 invocations, final commits `bff67c6f`,
      `6eb92892`, `4d864175`, `748261de`. Independently verified by the mission owner (build/vet
      clean, `make test` force-reran and passing, old functions confirmed removed, both
      regression guards tested end-to-end). Incident and decision detail: spec §6 (D3-D6), §7.2.
      - No reviewer was attached — decide with the user whether to attach one.
      - **Not yet reviewed by the user.** Second review round found spec-wording gaps, not code
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
- [ ] User direction on next steps (PR / more work / wind-down) — deferred until v9 is implemented
      AND the resumed code review both conclude.

**Last completed (2026-09-14, session -2):** v9 implemented and independently verified; a
second review round found real spec-doc gaps (not code bugs). Full narrative, incidents, and
decisions: spec doc §5 (abstracts), §6 (decisions D1-D7), §7.2 (incidents 1-4), §8 (revision
log); coverage-gap catch and process corrections: ledger `.session/2026-09-14-composite-analyzer-2.md`
(`## Verified` — chat-hygiene/process notes are correctly ledger-scoped, not duplicated here).
Pending fixes from the second review round: see Task section above and
`.session/drafts/2026-09-14-spec-review-response.md`. Checkpointed at the user's request — safe
checkpoint, not a retirement.

**Superseded — prior session (2026-09-13/14):** resolved the redesign discussion end to end.
Full narrative (eligibility-gate corrections, the structural/process fixes to the spec doc, the
single-`Write`-restructure incident and its lesson) lives in spec doc §5-§8 and ledger
`.session/2026-09-14-composite-analyzer-1.md`.

**Prior session (2026-09-08/09):** built an HTML diff-review page for this mission's diff,
generalized into the `diff-review-page` custom agent plus a standing WSL2/`wslview` convention
in `~/.claude/CLAUDE.md`; ran the first part of the step-by-step code review (general comments,
aggregation package, allocation core) — paused at the user's request.

**Next step / resume point (as of end of 2026-09-14 session -2, checkpoint):** v9 is implemented
and independently verified — do NOT re-dispatch a coder against the old task file without first
applying the pending spec fixes listed in the Task section above (the task file is stale
relative to the spec's next revision). In order:
1. Read `.session/drafts/2026-09-14-spec-review-response.md` in full — it has every pending fix,
   already verified against code, with an edit-plan table.
2. Get the user's go-ahead on the edit plan (some are pure wording, some are real code changes —
   §2.1.d's `CompositeTotalReplicas` function extraction and the `eligibleAnalyzers`→
   `enabledAnalyzers` rename both touch actual source, not just the doc).
3. Apply the doc edits directly (mission owner owns this file, no `.wip` lock needed — user
   instruction, 2026-09-14). Dispatch a coder for the code changes only if the user wants them
   done now rather than batched with the next real implementation task — **per this session's
   new process rule, any new coder task file must include a design-validation checkpoint before
   implementation, not just an implementation checklist.**
4. Resolve the open design question from §2.6 (should `SOHasSignal` be consumed per-SO by the
   optimizer, and do what with a no-signal SO) — needs the user's ruling, not investigation.
5. Do the two pending investigations from the Task section (quick re-verify; the larger
   pre-single-analyzer aggregation comparison) — ask the user which order, per the review
   comment ("recheck all is implemented... [and] identify the former aggregations").
6. Only after 1-5: decide with the user whether to attach a reviewer to the implemented code,
   and whether to resume the file-by-file code review of `composite.go`/steadystate wiring
   (on hold since 2026-09-13, now genuinely unblocked since v9's code exists).
7. `spec.md` stays stale-by-design; re-sync only if the user asks.

**Superseded resume point (pre-dispatch — already acted on, kept for context only):** the v9
design was settled and the task file coder-ready; dispatch was authorized and completed (see
"Last completed" above). No longer actionable.

### Status

- Environment: **ready** — branch `composite-analyzer`, rebased onto `upstream/main` @
  `c013012e` (note: `upstream/main` has since moved further, to `b01a6e17` — not re-rebased, per
  "do not rebase without asking first"). `git status`: only
  `.session/review/composite-diff-review.html` is untracked (long-standing review scratch
  output, not this session's). Everything else this session touched is committed — see `git log`
  on this branch, 2026-09-14 commits (from `57ed16df` through `HEAD` at checkpoint time).
- Mission: **v9 implemented, verified, not yet user-reviewed.** v8 remains
  implementation-complete underneath (v9 restructures code, not the underlying math). The
  redesign discussion is resolved; the coder task (4 invocations) is done; the mission owner
  independently verified the result. A second round of user review on the spec doc found real
  spec-wording/completeness gaps (not code bugs) — pending fixes listed in Task section above,
  none applied yet. **Nothing pushed, no PR opened.**
- For the *current* design (what a coder built, and what the next spec revision must fix before
  any further dispatch): **`.session/composite-signal-redesign.md` §2**, plus
  `.session/task-coder-composite-redesign.md` for the checklist the coder actually followed
  (stale relative to pending fixes — do not re-dispatch against it as-is).
  **`spec.md` is stale for this redesign — do not use it as the source of truth.** For the
  completed part of the v8 code review: `.session/review/code-review-notes.md` (§10 on
  rounding-function issues, recorded not actioned, referenced from spec §3). For this session's
  detailed narrative including every correction, the two review rounds, and the checkpoint
  itself: `.session/2026-09-14-composite-analyzer-2.md` (still active, not yet ledger-captured —
  see Session log). For the prior session's narrative: `.session/2026-09-14-composite-analyzer-1.md`
  (retiring, not yet moved to `.session/ledger/`).

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
