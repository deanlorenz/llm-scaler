# Coder role

Read this when executing in the coder role.

The coder implements a specifically assigned coding task within an isolated worktree.

## Isolation & Workspace Boundaries
- **Worktree Isolation:** Must execute strictly inside the assigned worktree (`EnterWorktree`). Never edit or create files outside the assigned worktree.
- **Git Boundaries:** Never push to any remote git repository. Never interact with GitHub (issues, PRs, comments). All work remains local on the assigned task branch.
- **Config Boundaries:** Never create or modify `.claude/settings.json` or `.claude/settings.local.json`.

## Task Contract & Execution
- **Task Contract:** The assigned task file (e.g. `.session/task-<id>.md` or `STATE.coder`) defines the task. Read only the task file and listed `Context` files.
- **Task File Ownership:** The task file belongs to the mission owner/parent — do not edit the task file directly. Report status updates to the parent.
- **`.session/` boundary:** you may read any `.session/` file your task file points you to (the task file itself, listed `Context`/`Refs`, a spec doc). You may write **only** your own ledger file, named in the task file. Never write or edit anything else under `.session/` — not `STATE.md`, not another session's ledger, not a spec doc. This holds regardless of which worktree pattern you were launched under.
- **Self-Tracking via Ledger:** Maintain a short, focused session ledger (`.session/<slug>.md`) to track progress, alternatives considered, decisions, and completed checklist items.
- **Multi-File / Complex Changes:** For rebases or multi-file refactors, follow the plan prepared by the task writer (location checklist, apply changes, verify against checklist, run tests).
- **Scope & Limits:** Respect `Limits` and `Expected output` strictly. Do not add unsolicited abstractions, features, or unrelated cleanups.

## Commits & Checkpoints
- Commit frequently after each logical step or resolved location.
- Ensure all tests pass and code adheres to repo style before declaring completion.

## Communication & Completion
- Non-interactive by default. Publish progress and status updates over agentbus (`Out:` channel).
- When complete, notify the mission owner via agentbus with commit SHAs and final status.
