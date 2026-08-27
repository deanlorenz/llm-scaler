# agentbus

A local, lightweight, cross-tool pub/sub and durable message log for coordinating AI coding
agent sessions (Claude Code, IBM Bob, or any other MCP-capable tool) working on a shared
"mission" on one developer machine.

This is infrastructure, not a feature of any one project — meant to be installed once and
reused across any repo. This branch/worktree holds only its source code, scripts, and hook
script; it is an orphan branch with no shared history with this repo's actual feature work, and
is tracked on `origin` only (never pushed to `upstream`).

**Design, task tracking, and session ledgers** for this work live under the `session-tracking`
branch's mission-tracking convention, not here:
`worktrees/session-tracking/missions/agentbus/` (spec doc, `STATE.md`, `ledgers/`).

## Status

Design complete; implementation starting. See the mission's spec doc (path above) for the
full settled design, task list, and open items.

## Layout (planned)

```
cmd/
  agentbusd/          # MCP server: publish, fetch-since, publish-presence, list-missions
  agentbus-relay/      # background daemon: watches NATS, writes per-worktree marker files
internal/
  bus/                 # shared JetStream helpers
  schema/              # message schema
scripts/
  install.sh           # installs binaries + hook script to ~/.local/bin, ~/.claude/bin
hooks/
  agentbus-hook.py      # PostToolBatch hook: cheap marker check, surfaces new messages
```
