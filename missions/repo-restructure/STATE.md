# Mission state — repo restructure (bare repo + worktrees)

**Last updated:** 2026-08-27. Overwritten on each update, not append-only. For global
process rules see `../../CONVENTIONS.md`. For the full plan see
`spec-repo-restructure.md`. Per-session ledgers are in `ledgers/`.

## Worktrees used for this mission

- None yet — this mission is pure investigation/planning so far. Its eventual output
  (Phase 1–4 of the spec) creates an entirely new bare repo + worktrees tree at
  `/home/dean/code/llm-d/llmd-scaler/`, separate from every worktree this session-tracking
  system currently uses.

## Mission, one line

Migrate from the current container-as-checkout layout (`dean-llmd-scaler-sandbox`, a
normal non-bare checkout hosting 31 worktrees, some scattered outside it) to a standard
bare-repo layout: `/home/dean/code/llm-d/llmd-scaler/repo` (bare) +
`/home/dean/code/llm-d/llmd-scaler/worktrees/` (flat, all worktrees). Old container is
left completely untouched throughout — this is a fresh clone, not a move.

## Task status

| Task | Status | Notes |
|---|---|---|
| Investigate current state | **DONE** | Full inventory of 31 worktrees, remotes, disk usage, uncommitted content — see spec doc. |
| Confirm naming/location decisions | **DONE** | 5 decisions confirmed with user. |
| Execute Phase 1 (bare repo + remotes) | **NOT STARTED** | Blocked — see below. |
| Execute Phase 2 (migrate branches/worktrees with real content) | **NOT STARTED** | Depends on Phase 1. |
| Execute Phase 3 (`main` worktree tracking `upstream/main`) | **NOT STARTED** | Depends on Phase 2. |
| Execute Phase 4 (verification) | **NOT STARTED** | Depends on Phase 3. |

## Immediate next step

**Nothing — explicitly paused.** The user approved the plan, then immediately said "do
not start any migration yet, still planning only." No execution step should run without
a fresh, explicit go-ahead from the user, separate from the plan approval itself that
already happened.

## Open questions blocking full completion

None design-wise — the plan is fully specified. The only blocker is the user's explicit
pause: execution needs a new go-ahead, not implied by anything already said.

## Session log

- 2026-08-27 session=2026-08-27-repo-restructure-plan status=retired ledger=ledgers/2026-08-27-repo-restructure-plan.md
