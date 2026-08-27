# Moved to `session-tracking`

This mission's plan, state, and reference docs (conventions, ledger, specs, task briefs,
implementation reports) now live on the `session-tracking` branch, not here — see
`worktrees/session-tracking/missions/analyzer-optimizer-refactor/` (checked out at
`worktrees/session-tracking/` in this repo).

**Why:** mission plan/state/ledger content should be tracked on an `origin`-only branch, not
on a feature branch that may go upstream. See `session-tracking`'s `CONVENTIONS.md` for the
full rationale and the editing protocol for shared files.

**Code changes for this mission still land here**, on `single-analyzer`, as before — only the
planning/tracking docs moved.
