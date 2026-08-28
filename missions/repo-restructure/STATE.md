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
| Apply 10 plan corrections received 2026-08-28 | **NOT STARTED** | 10 real corrections to the plan (see ledger `2026-08-27-repo-restructure-plan.md`'s "Second entry" section) received but never reviewed one-by-one with the user or applied to `spec-repo-restructure.md`. This must happen before Phase 1 can start — the plan is no longer fully specified until these are resolved. |
| Execute Phase 1 (bare repo + remotes) | **NOT STARTED** | Blocked — see below, and blocked additionally on the corrections above (several affect Phase 1's own steps). |
| Execute Phase 2 (migrate branches/worktrees with real content) | **NOT STARTED** | Depends on Phase 1. |
| Execute Phase 3 (`main` worktree tracking `upstream/main`) | **NOT STARTED** | Depends on Phase 2. Note: corrections include a capitalization fix (`Main`, not `main`) not yet applied here or in the spec. |
| Execute Phase 4 (verification) | **NOT STARTED** | Depends on Phase 3. |

## Immediate next step

**Nothing — explicitly paused.** The user approved the plan, then immediately said "do
not start any migration yet, still planning only." No execution step should run without
a fresh, explicit go-ahead from the user, separate from the plan approval itself that
already happened.

## Open questions blocking full completion

- **The plan is no longer fully specified** as of 2026-08-28 — 10 real corrections were
  given (see ledger) but never reviewed one-by-one or applied. Point 8 specifically is
  unresolved-ambiguous (unclear which plan item the user's "there is [a real branch tip]"
  comment targets — needs re-confirmation, not a guess) and point 10 appears to conflict
  with an earlier instruction (leave the container's uncommitted items alone vs. check
  them one by one) — flag this conflict to the user rather than picking silently.
- Separately, execution still needs a fresh, explicit go-ahead — not implied by plan
  approval alone (confirmed earlier this session as its own distinct gate).

## Incident (2026-08-27, during this mission's investigation)

While inspecting the container's uncommitted content to check whether it was safe to
touch, part of `mmy.env`'s actual contents (including live `BOB_API_KEY` and
`BOB_SHELL_API_KEY` values) was printed to chat before the sensitivity of the file was
recognized. The user was told immediately and directed that the file be left completely
untouched going forward (reflected in the spec's hard constraints). Flagging this
explicitly, the same way the `agentbus` mission flagged its own unrelated secrets
exposure, since the user should know this happened and may want to rotate those keys —
not otherwise actioned by this mission.

## Session log

- 2026-08-27 session=2026-08-27-repo-restructure-plan status=active ledger=ledgers/2026-08-27-repo-restructure-plan.md
