# agentbus — Startup & Operations

## Prerequisites

- `nats-server` binary in `~/.local/bin/` (install once: `curl -sf https://binaries.nats.dev/nats-io/nats-server/v2@latest | sh -s -- -b ~/.local/bin`)
- agentbus binaries built and installed: `bash scripts/install.sh` from this worktree
- Project registered: `agentbus-setup llmd-scaler` run once from the repo root (already done for this repo)

## Startup (systemd user services — already installed)

Both `nats-server` and `agentbus-relay` run as systemd user services, started automatically
at login. No manual steps after reboot.

```bash
# Check status
systemctl --user status nats-server agentbus-relay

# Restart if needed
systemctl --user restart nats-server agentbus-relay

# Start manually (after reboot before first login triggers it)
systemctl --user start nats-server agentbus-relay
```

Service files: `~/.config/systemd/user/nats-server.service` and `agentbus-relay.service`.

**agentbus is disabled when not started.** If `nats-server` is not running:
- `agentbusd` fails at MCP connection time with: `connect to NATS: nats: no servers available`
- The MCP tool call returns an error immediately — no silent failure
- `agentbus-hook` exits 0 silently (no disruption to normal turns)

The hook is always safe to have registered — it does nothing if NATS is down.

## Checking status

```bash
systemctl --user status nats-server agentbus-relay   # service health
cat ~/.agentbus/repos.json                            # registered projects
```

## One-time setup per session (agent protocol)

When starting a session that participates in agentbus coordination:

```
agentbus_subscribe(topic="<my-in-topic>", session_id="<my-session-id>")
agentbus_publish(topic="<announce-topic>", from_session="<my-session-id>", kind="announce",
  body="session <id> online. in=<my-in-topic> out=<my-out-topic>")
```

## Topic naming convention (not enforced)

```
<mission>.<role>            — a session's outbox  (e.g. agentbus.planner)
<mission>.<parent>.<child>  — parent's dedicated inbox for child (e.g. agentbus.planner.coder-1)
<bus_id>.broadcast          — optional machine-wide signals
user.in                     — human dialogue inbox (incoming questions to user)
user.out                    — human dialogue outbox (replies from user)
```

## Running the Interactive Dialogue (`agentbus-dialogue`)

Open a terminal pane inside VS Code or tmux:

```bash
agentbus-dialogue
# or specify a custom user session ID / bus ID:
agentbus-dialogue -session dean
```

When an agent calls `agentbus_ask_user` or publishes a question to `user.in`:
1. The dialogue prints the question and rings the terminal bell (`\a`).
2. Type your response at the `> ` prompt and press Enter.
3. Your answer is published to `user.out` with `reply_to` pointing to the question sequence number.








## PostToolBatch hook registration (once, global)

Run `bash scripts/install.sh` — it registers `agentbus-hook` in `~/.claude/settings.json` under `PostToolBatch`. After that, any Claude Code session in a registered project will automatically surface new messages on subscribed topics.

For Bob: the MCP config (`~/.bob/settings/mcp.json`) is already wired. Bob calls the tools explicitly — no hook needed since Bob controls its own turn timing.
