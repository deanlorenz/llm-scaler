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

Core implementation complete. All 4 MCP tools verified live. Relay daemon and PostToolBatch hook
implemented and tested. Ready for use; T5 (resume-mission integration) and full end-to-end
two-session verification still pending.

## Layout

```
cmd/
  agentbusd/          # MCP server: publish, fetch-since, publish-presence, list-missions
  agentbus-relay/     # background daemon: watches NATS, writes per-worktree marker files
  agentbus-hook/      # PostToolBatch hook binary: cheap marker check, surfaces new messages
internal/
  bus/                # shared JetStream helpers (stream ensure, publish, fetch, worktree reg)
  schema/             # message schema (Message, Presence, From)
scripts/
  install.sh          # builds binaries, registers in Claude/Bob config
```

## Quick start

```bash
# 1. Install nats-server (once per machine)
curl -sf https://binaries.nats.dev/nats-io/nats-server/v2@latest | sh -s -- -b ~/.local/bin

# 2. Build and install everything
bash scripts/install.sh

# 3. Start the substrate (add to your shell startup or a service manager)
nats-server -js -sd ~/.agentbus/nats -p 4222 &
agentbus-relay &
```

## How it works

- **4 MCP tools** (`agentbus_publish`, `agentbus_fetch_since`, `agentbus_publish_presence`,
  `agentbus_list_missions`) — any MCP-capable agent calls these directly.
- **Durable log** — messages are stored in NATS JetStream at `~/.agentbus/nats/`. Replay with
  `agentbus_fetch_since(since_seq=0)` at any time.
- **Silent wake** — `agentbus-relay` subscribes to NATS and writes a marker file per worktree
  per mission when a new message arrives. The `PostToolBatch` hook (fires every model turn)
  reads that marker cheaply; only contacts NATS when something new arrived.
- **No conflicts** — JetStream is an ordered, concurrent-safe log. Parallel publishes are safe.
- **Debug** — `agentbus-fetch <mission> 0` replays all messages for a mission from the start.

## File locations

| Path | Purpose |
|---|---|
| `~/.agentbus/nats/` | NATS JetStream storage |
| `~/.agentbus/watched-worktrees.list` | worktrees the relay watches |
| `<worktree>/.claude/agentbus/presence/<mission>.json` | presence sentinel (written by `agentbus_publish_presence`) |
| `<worktree>/.claude/agentbus/inbox/<mission>.marker` | relay writes this when new messages arrive |
| `<worktree>/.claude/agentbus/consumer/<session>.<mission>.cursor` | hook's last-seen seq (per session) |
