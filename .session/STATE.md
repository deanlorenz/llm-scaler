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

- **Plan / spec:** `.session/spec.md` — mission spec, **v8, APPROVED [USER, 2026-09-08]**.
  Holds the design (§1–§9), decisions (§10), and revision/decision history (§12) — including the
  implementation's outcome. **This is where design content and decision reasoning live, not
  here.** *(do not read upfront — pull on demand only)*
- **Survey:** `.session/survey-zero-signal.md` *(pull on demand)*
- **Task files:** `.session/task-coder-agg1.md`, `.session/task-reviewer-agg1.md` — the
  implementation dispatch. `.session/review-coder-agg1.md` — final review report, verdict PASS.
- **Code review (in progress):** `.session/review/code-review-notes.md` — user's own step-by-step
  code review of the implementation, paused mid-way (finished: aggregation package, allocation
  core; not yet started: steadystate wiring/composite.go, docs, tests). No code changed during
  this review — findings/rulings only. Resume by continuing from where it left off (see resume
  point below). Also in that dir: `.session/review/composite-diff-review.html` — an HTML diff
  viewer built earlier in the same session (superseded as a *process* by the new
  `diff-review-page` agent — see Extra rules/tools below — but the file itself is still valid to
  view).
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
- [ ] Code review in progress: general comments + aggregation package + allocation core done
      (see `.session/review/code-review-notes.md`); steadystate wiring (`composite.go`,
      `engine_v2.go`, `engine.go`), constants/metrics.go, docs, and tests **not yet reviewed**.
- [ ] User direction on next steps (PR / more work / wind-down) — deferred until the code review
      finishes.

**Last completed:** a full session doing two things: (1) built an HTML diff-review page for this
mission's diff, then generalized that into a new user-level custom agent
(`diff-review-page`, untested end-to-end — see Extra rules above) plus a standing WSL2/`wslview`
convention in `~/.claude/CLAUDE.md`; (2) ran a structured, code-only walkthrough of the
implementation with the user (no code changed) covering general comments, the `aggregation`
package, and `allocation` core (`composite_eligibility.go`/`composite_identity.go`/
`composite_decision.go`/`composite_signal_gate.go`) — paused, not finished, at the user's request
("good point to stop... we continue on a new session later"). Full findings/rulings in
`.session/review/code-review-notes.md`; this session's ledger
(`.session/ledger/2026-09-08-composite-analyzer-2.md` after this wind-down) has the narrative
summary. This is a **full retirement** (user's explicit choice when asked checkpoint-vs-retire).

**Next step / resume point:** the new session should read
`.session/review/code-review-notes.md` in full (it is the precise, citation-backed record — this
STATE file only summarizes), then resume the same step-by-step code review with the user starting
from **steadystate wiring** (`internal/engines/steadystate/composite.go` — the `buildComposite`
function — plus its callers in `engine_v2.go`/`engine.go`), continuing through
`internal/constants/metrics.go`, `docs/reference/cycle-log.md`, and the test files, in that order,
matching the module grouping the HTML diff-review page used. Same rules as before: **no code
changes during the review** — discuss each point, investigate it against the real code before
answering, record only the outcome. `coder-agg1` and `reviewer-agg1` are both idle, holding open
on their `In:` channels (not terminated) — resume them via `SendMessage` to their agent IDs/names
rather than launching new agents, if implementation work resumes later. If their IDs are not in
the new session's context, use `ListAgents` to find them by name (`coder-agg1`/`reviewer-agg1`)
first.

### Status

- Environment: **ready** — branch `composite-analyzer`, rebased onto `upstream/main` @
  `c013012e` (note: `upstream/main` has since moved further, to `b01a6e17` as of this session —
  not re-rebased, per "do not rebase without asking first"). `git status`: only
  `.session/review/` is untracked (this session's review scratch output — see Task section).
- Mission: **implementation-complete, reviewed PASS.** User is now doing their own code review
  before deciding next steps (PR / more work / wind-down) — in progress, paused mid-way (see
  Execution checklist). Nothing pushed, no PR opened.
- For the *design* (what was built and why), decision history, rejected approaches, and
  verification detail: **spec.md §1–§9 (design), §10 (decisions), §12 (revision + implementation
  history)** — not restated here. For the ongoing code review's findings: `.session/review/
  code-review-notes.md`. For prior session narrative (how each step happened, findings as they
  occurred): the retired ledgers in `.session/ledger/`.

### Known issues

- none

## Session log
- 2026-09-08 session=2026-09-08-composite-analyzer-1 status=retired ledger=.session/ledger/2026-09-08-composite-analyzer-1.md
- 2026-09-08 session=2026-09-08-composite-analyzer-2 status=retired ledger=.session/ledger/2026-09-08-composite-analyzer-2.md
