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

Open a dedicated terminal pane in VS Code or tmux **before starting an agent session**:

```bash
agentbus-dialogue            # session defaults to "dean"
agentbus-dialogue -session <name>   # custom identity
agentbus-dialogue --help     # full flag list
```

Input behaviour:
- **Enter** — submit reply immediately
- **Arrow keys, Backspace** — full line editing
- **Trailing `\` + Enter** — continue onto next line; plain Enter submits the whole block

When a question arrives the terminal bell rings, the tab title changes to
`💬 [ACTION REQUIRED]`, and the prompt appears. Type your reply and press Enter.

## Asking the user a question (agent protocol — `agentbus_ask_user`)

Use this tool when an agent needs a blocking human answer before continuing.
It publishes a question to `user.in`, waits for a reply on `user.out`, and
returns the answer text to the caller. **`agentbus-dialogue` must be running.**

```
agentbus_ask_user(
  prompt          = "Your question here",
  from_session    = "<this session's id>",
  timeout_seconds = 300          # optional, default 300
  refs            = ["path/to/relevant/file"]  # optional
)
```

Returns: `{ "reply": "...", "seq": <n>, "from": { "agent": "human", "session": "dean" } }`

On timeout returns: `{ "timed_out": true, "reply": "Timed out waiting for user reply." }`

**Do not** use `agentbus_ask_user` for non-blocking notifications — use `agentbus_publish`
to `user.in` with `kind="note"` instead, and let the user reply in their own time.

## PostToolBatch hook registration (once, global)

Run `bash scripts/install.sh` — it registers `agentbus-hook` in `~/.claude/settings.json` under `PostToolBatch`. After that, any Claude Code session in a registered project will automatically surface new messages on subscribed topics.

For Bob: the MCP config (`~/.bob/settings/mcp.json`) is already wired. Bob calls the tools explicitly — no hook needed since Bob controls its own turn timing.

## CLI tools (quick reference)

| Binary | Purpose |
|---|---|
| `agentbus-dialogue` | Interactive human↔agent terminal (keep open during sessions) |
| `agentbus-pub` | One-shot publish from shell: `-topic <t> -body <b> [-kind <k>]` |
| `agentbus-setup` | Register a project: `agentbus-setup <bus_id>` |
| `agentbusd` | MCP server (started by Claude/Bob, not run directly) |
| `agentbus-relay` | NATS relay daemon (run as systemd service) |
| `agentbus-hook` | PostToolBatch hook (invoked by Claude Code, not run directly) |

All binaries accept `--help` for flag documentation.