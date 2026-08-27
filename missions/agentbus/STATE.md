# STATE — agentbus

**One-line goal.** Build a local, lightweight, cross-tool (Claude Code + IBM Bob) topic-based
pub/sub and durable message log for coordinating AI agent sessions on a shared mission, using
NATS JetStream as the transport and a minimal custom Go MCP server as the client-facing layer.

**Spec.** `missions/agentbus/spec-agentbus.md` — task list, settled design decisions, refs.

**Worktrees used.**
- `worktrees/agentbus` — dedicated orphan branch, holds all Go source/binaries/scripts/hook
  script. Not based on `feat/wva-external-scaler` or upstream; tracked on `origin` only (not
  yet pushed as of this writing).

**Immediate next step.** T1: finish worktree scaffolding — per-skill symlinks into
`worktrees/agentbus/.claude/skills/`, add their paths to `.git/info/exclude`, first commit,
confirm with user before pushing to `origin`.

**Open questions blocking full completion.**
- Which MCP config file Bob actually loads at runtime (T6) — must ask Bob directly.
- The live `PostToolBatch` hook JSON contract must be re-confirmed against current Claude Code
  docs before T4's hook script is finalized (explicitly flagged as unverified in the plan this
  mission implements).

## Session log

- 2026-08-27T20:16 session=2026-08-27-agentbus-design status=active ledger=ledgers/2026-08-27-agentbus-design.md
