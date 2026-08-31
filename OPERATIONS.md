# agentbus — Startup & Operations

## Prerequisites

- `nats-server` binary in `~/.local/bin/` (install once: `curl -sf https://binaries.nats.dev/nats-io/nats-server/v2@latest | sh -s -- -b ~/.local/bin`)
- agentbus binaries built and installed: `bash scripts/install.sh` from this worktree
- Project registered: `agentbus-setup llmd-scaler` run once from the repo root (already done for this repo)

## Starting the substrate (manual, run when needed)

```bash
# 1. Start NATS with JetStream
nats-server -js -sd ~/.agentbus/nats -p 4222 &

# 2. Start the relay daemon (watches NATS, writes marker files for the hook)
agentbus-relay &
```

Both run in the background. Stop them with `kill $(pgrep nats-server)` and `kill $(pgrep agentbus-relay)`.

**agentbus is disabled when not started.** If `nats-server` is not running:
- `agentbusd` fails at MCP connection time with: `connect to NATS: nats: no servers available`
- The MCP tool call returns an error immediately — no silent failure
- `agentbus-hook` fails to connect in `fetchNew` and exits 0 silently (no disruption to normal turns)

So the hook is always safe to have registered — it just does nothing if NATS is down.

## Checking status

```bash
pgrep -a nats-server    # is NATS running?
pgrep -a agentbus-relay # is the relay running?
cat ~/.agentbus/repos.json  # which projects are registered?
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
<mission>.<parent>.<child>  — parent's inbox for a specific child (e.g. agentbus.planner.coder-1)
<bus_id>.broadcast          — optional machine-wide signals
```

## Startup at login (optional)

Add to `~/.bashrc` or `~/.profile`:

```bash
# agentbus substrate — start if not running
if ! pgrep -x nats-server > /dev/null; then
  nats-server -js -sd ~/.agentbus/nats -p 4222 > ~/.agentbus/nats-server.log 2>&1 &
fi
if ! pgrep -x agentbus-relay > /dev/null; then
  agentbus-relay > ~/.agentbus/relay.log 2>&1 &
fi
```

## PostToolBatch hook registration (once, global)

Run `bash scripts/install.sh` — it registers `agentbus-hook` in `~/.claude/settings.json` under `PostToolBatch`. After that, any Claude Code session in a registered project will automatically surface new messages on subscribed topics.

For Bob: the MCP config (`~/.bob/settings/mcp.json`) is already wired. Bob calls the tools explicitly — no hook needed since Bob controls its own turn timing.
