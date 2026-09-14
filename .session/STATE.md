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
- **Ledger / log:** `.session/2026-09-14-composite-analyzer-1.md` — active this session (created
  mid-session after the user pointed out no ledger was being kept, a direct CONVENTIONS.md
  violation — see its own header). A NEW session should create its own dated ledger file rather
  than appending to this one. Captured retired ledgers:
  `.session/ledger/2026-09-08-composite-analyzer-1.md`,
  `.session/ledger/2026-09-08-composite-analyzer-2.md`
  ⚠ DO NOT READ retired ledgers — not yours

## Task

- **Plan / spec for the redesign — READ THIS ONE, NOT spec.md:**
  `.session/composite-signal-redesign.md` **§2 is the current, authoritative, coder-ready spec**
  for the composite-building rewrite. It was restructured 2026-09-14 into §1 (summary), §2 (spec
  — settled rules only, no reasoning/citations), §3 (open items), §4 (roadmap), §5 (discussion —
  every fact/citation/history, on-demand only). `spec.md`'s v9 entry (§12) is **STALE** — it
  reflects an earlier, less precise version of this design (before the sat-visibility-after-
  compose fix and other corrections below) and has **not been re-synced**. Do not read `spec.md`
  for the redesign; read `composite-signal-redesign.md` §2 directly. Re-syncing `spec.md` is
  tracked as an open item in that doc's §3 — not yet done.
- **v9 implementation task, READY TO DISPATCH:** `.session/task-coder-composite-redesign.md` —
  fully rewritten 2026-09-14 against §2 above. Every function signature, file destination, and
  struct/switch shape is spelled out concretely (no naming or placement decisions left to the
  coder). Includes a `user.in` progress-reporting instruction (publish a note after each step,
  per `conventions/agentbus.md`). **Not yet dispatched to a coder** — this is the very next
  action once a new session picks this up, pending final user sign-off on the task file's content.
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
- [ ] **Implementation — NOT YET DISPATCHED.** Task file is ready but no coder has been
      authorized/started on it. Per-operation authorization still required before dispatch (mission
      convention) — do not assume the task file's existence is itself authorization to dispatch.
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

**Last completed (this session, 2026-09-13/14):** resolved the redesign discussion end to end,
through multiple rounds of user correction — recorded plainly since a future session should learn
from these, not just their outcomes:
- Ruled every composite field comes from saturation alone except PRC and Reason, and saturation
  is also the sole source of the variant set (no cross-analyzer union, no fallback).
- Settled naming (`TotalReplicas`, `CompositeTotalReplicas`), file relocation
  (`AggN`/`PRCCom`/`replicasNeeded`/`variantCapacity`/`roleOf` all move out of the `aggregation`
  package, verified via an actual single-caller-per-function count, not assumed), and the per-SO
  participation rule (verified against all 3 analyzers' real failure-path code — only saturation
  needs an explicit Reason check, since throughput/external already opt out by omission).
- **Caught mid-session: sat's config-enabled/disabled contributor-eligibility logic — a real,
  previously-given ruling from an earlier code-review session (`code-review-notes.md`
  §7/§9.1/§9.2/§8.6) — had only been pointer-referenced in the redesign doc, never actually
  folded in.** Folded in properly this time, then corrected TWICE more after that: first because
  the mechanism still let a per-SO check test "is this analyzer sat" (fixed — the config check
  now resolves once, upstream of the loop, into an `eligibleAnalyzers` slice); second because
  `HasUsableCompositeSignal`'s two-check replacement STILL named sat directly even after the
  first fix (the user had to point this out a second time — "why did you ignore me?" — a real
  miss: after any correction, the whole doc needs to be swept for the same pattern, not just the
  one spot pointed at).
- **Separately, a structural complaint:** the redesign doc mixed the code specification with the
  discussion that produced it. Restructured into `conventions/tasks.md`'s existing mission-spec
  shape (§1-2 settled/upfront, §3+ discussion/on-demand) — this convention already existed and
  should have been applied without being told twice.
- **Separately, a process violation:** that restructure was done as one `Write` replacing the
  whole file, which is reconstruction from memory, not a verifiable transform — a direct
  violation of `CONVENTIONS.md`'s ownership rules. Caught by the user ("you deleted first, then
  rewrote from memory"). Recovered by diffing against the last commit (not a blind revert, per
  the user's choice) — found and restored one fully-dropped table plus 4 dropped citations via
  targeted `Edit`s using the original text verbatim, not reconstructed. **Lesson: any full-doc
  restructure must be a sequence of `Edit` calls moving existing text unchanged, never a single
  `Write` regenerating the file.**
- Rewrote `.session/task-coder-composite-redesign.md` completely after the user flagged the
  first version as too verbose, leaving too many decisions to the coder (name picks,
  caller-counting, "flag it in your ledger" escape hatches), and telling the coder to read the
  entire spec. New version: every function signature, file destination, and struct/switch shape
  is spelled out concretely; the coder reads only redesign-doc §2, nothing else.
- Added `user.in` progress-reporting (per `conventions/agentbus.md`) to the task file — the coder
  now publishes a note after each step, on getting stuck, and on completion, in addition to its
  normal `Out:` reporting.
- `spec.md`'s v9 entry was folded in BEFORE most of the corrections above landed and was never
  re-synced — it is now known-stale. Do not use it as the redesign's source of truth.
- Every non-trivial edit this session is committed (see `git log` on this branch,
  2026-09-14) — the user asked mid-session for continuous commits rather than one batch at the
  end; applying from that point forward.
- **Not yet dispatched to a coder** — the task file being ready is not authorization to start it.

**Prior session's last-completed (2026-09-08/09, for reference):** built an HTML diff-review page
for this mission's diff, generalized into the `diff-review-page` custom agent plus a standing
WSL2/`wslview` convention in `~/.claude/CLAUDE.md`; ran the first part of the step-by-step code
review (general comments, aggregation package, allocation core) — paused at the user's request.

**Next step / resume point:** the design is fully settled and the task file is coder-ready — read
`.session/composite-signal-redesign.md` §2 plus `.session/task-coder-composite-redesign.md`
directly (both short, no need to read `spec.md` or the redesign doc's §5). Ask the user for
final sign-off on the task file, then ask whether to dispatch it. If yes: same dispatch pattern
as v8 (`coder-agg1`/`reviewer-agg1` were same-worktree/async with a continuous reviewer) — check
with the user whether to reuse those same idle agents (still holding open on their `In:` channels
per the last session's note, not terminated — find them via `ListAgents` by name
`coder-agg1`/`reviewer-agg1` if their IDs aren't in context) or start fresh ones, since this is
materially different work from what they were dispatched for originally. Do not resume the
file-by-file code review until this redesign is implemented — see the checklist item above for
why. Do not re-sync `spec.md` unless the user asks for it specifically — it's flagged stale but
not blocking dispatch.

### Status

- Environment: **ready** — branch `composite-analyzer`, rebased onto `upstream/main` @
  `c013012e` (note: `upstream/main` has since moved further, to `b01a6e17` as of this session —
  not re-rebased, per "do not rebase without asking first"). `git status`: only
  `.session/review/composite-diff-review.html` is untracked (prior session's review scratch
  output). Everything else this session touched is committed — see `git log` on this branch,
  2026-09-14 commits.
- Mission: **implementation-complete on v8** (still true — this redesign restructures v8's code,
  not the underlying math). The redesign discussion that paused the user's own code review is now
  **fully resolved**, corrected through several rounds, and the coder task is ready to dispatch.
  **Not yet dispatched.** Nothing pushed, no PR opened.
- For the *current* design (what a coder should build): **`.session/composite-signal-redesign.md`
  §2**, plus `.session/task-coder-composite-redesign.md` for the concrete implementation
  checklist. **`spec.md` is stale for this redesign — do not use it as the source of truth**; its
  §1–§9 still describe v8's shape and its §12 v9 entry predates several corrections. For the
  completed part of the v8 code review: `.session/review/code-review-notes.md` (now including a
  new §10 on rounding-function issues, recorded not actioned). For prior session narrative: the
  retired ledgers in `.session/ledger/`; for this session's own detailed narrative including every
  correction and the process violation/recovery: `.session/2026-09-14-composite-analyzer-1.md`.

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
