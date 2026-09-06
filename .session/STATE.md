# benchmark-plan

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Planning and cross-worktree coordination for the benchmark
  pipeline (benchmark-init, benchmark-runtools, benchmark-extract, benchmark-viz). Holds
  shared planning docs, commit mappings, and mission-owner coordination notes. Goal not yet
  fully defined — TBD in a future session.
- **Worktree:** `worktrees/benchmark-plan` (branch `benchmark-plan`)
- **Role / scope:** Mission owner. Owns `plans/` tree, commit mapping, and coordination
  between sibling benchmark worktrees.
- **Ledger / log:** `.session/bench-plan2.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** TBD — mission goal not yet fully defined
- **Context:** `plans/commit-mapping.md`, `plans/benchmark/worktree-tasks.md`
- **Refs:** `plans/benchmark/observability-gaps.md`, sibling worktree STATE files
- **Expected output:** TBD
- **Done / completion criteria:** TBD
- **Limits:** Do not edit other worktrees' files without explicit exception.
- **Extra rules / rule refs:** none yet

## Execution

### Steps / subtasks

- [ ] Fully define mission goal and scope (deferred — do in a future session)
- [ ] Review `plans/commit-mapping.md` status table and update worktree statuses
- [ ] Coordinate with sibling worktrees as needed

**Last completed:** Initial .session/ scaffolding (bench-plan2, 2026-09-06)

**Next step / resume point:** Fully define mission goal — confirm scope and next
concrete action with user.

### Status

Scaffolded. Mission goal TBD — not yet fully defined. No active work items.

### Known issues

- `plans/benchmark-viz/` directory is untracked (contains `input-contract.md` placed by
  benchmark-viz worktree). Needs to be committed or clarified.

## Session log

- 2026-09-06 session=bench-plan2 status=active ledger=.session/bench-plan2.md
