# Mission state — analyzer/optimizer refactor

**Last updated:** 2026-08-27. This doc is overwritten on each update, not append-only — it
reflects current status only. For global process rules see `../../CONVENTIONS.md`. For the
full task plan see `spec-composite-metric-and-optimizer-t2.md`. For the chronological
reasoning trail (why decisions were made) see `ledger-analyzer-optimizer-refactor.md` —
reference only, not needed to resume. Per-session ledgers are in `ledgers/`.

## Worktrees used for this mission

- `worktrees/single-analyzer` (branch `single-analyzer`) — the feature-work worktree where
  code changes for this mission actually land.
- `worktrees/session-tracking` (branch `session-tracking`, this worktree) — mission plan/state
  and session ledgers, never pushed to `upstream`, only `origin`.
- Coder-agent worktrees are ephemeral (`.claude/worktrees/agent-<id>`), created and torn down
  per task — not listed individually here; each task's completion note below cites the commit
  hash that was cherry-picked out of one, not the ephemeral worktree path itself.

Coding tasks are dispatched to coder agents in their own separate `isolation: "worktree"`
instances; the orchestrating session reviews each and cherry-picks/merges approved commits
back into `single-analyzer` itself (see `../../CONVENTIONS.md`).

## Mission, one line

Add an engine-side step that composes N analyzer results into one before the optimizer sees
them, so the optimizer can be simplified back to single-analyzer-only logic. Must be a no-op
when saturation is the only enabled analyzer (today's default).

## Task status

| Task | Status | Notes |
|---|---|---|
| T1 — compose analyzer results (engine-side) | **DONE** | Commit `f5283e2a` on `single-analyzer`. `composeAnalyzerResults` added to `engine_v2.go`; passthrough for the sat-only case. Six pre-existing tests asserting old multi-analyzer forwarding are `t.Skip()`-ed, each with a reason citing this change — pending a redesign, not a bug. |
| CT1a — nil-guard `rescaleModelDecisions` | **DONE** | Commit `8906ef7b` on `single-analyzer`. Report: `ct1a-implementation-report-2026-08-26.md`. |
| CT1b — engine-side guard on nil saturation result | **DONE** | Commit `122d1699` on `single-analyzer` (cherry-picked from coder's `75b57b2c`). Report: `ct1b-implementation-report-2026-08-26.md` (note: that report's recorded hash `71c401c2` is stale/cosmetic — self-referential amend artifact; true hash is `122d1699`). |
| CT2 — collapse `AnalyzerResults []NamedAnalyzerResult` to single `CompositeSignal` field | NOT STARTED | Depends on CT1 (touches same functions). Spec in `spec-composite-metric-and-optimizer-t2.md`. |
| CT3a/CT3b — design + apply engine-side reduce, simplify 7 single-entry helpers | NOT STARTED | CT3a depends on CT2; CT3b depends on CT3a being reviewed. |
| CT4 — Score-weighted aggregation simplification | **BLOCKED on user** | Real fairness-definition design decision (ledger §36), not code-verifiable. Explicitly out of scope for the current implementation pass. |
| CT5 — document `RoleCapacities` role-visibility contract | NOT STARTED | Independent of CT1–CT4, can land any time. |

## Immediate next step

CT1a and CT1b are both landed on `single-analyzer`. Next is CT2 — its spec is already
implementation-ready in `spec-composite-metric-and-optimizer-t2.md`.

## Open questions blocking full completion

- CT4's fairness-definition decision — needs the user, not code investigation (see
  `spec-composite-metric-and-optimizer-t2.md` CT4 section and ledger §36).

## Provenance note (2026-08-27 migration)

This mission's docs previously lived at `worktrees/single-analyzer/docs/plans/analyzers/` on
the `single-analyzer` branch itself. Moved here so mission plan/state/ledger content is
tracked on `session-tracking` (origin-only) instead of the feature branch (which may go
upstream). See this mission's `ledgers/` for the session that did the move.

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=active ledger=ledgers/2026-08-27-session-tracking-setup.md
