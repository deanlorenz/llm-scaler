#!/usr/bin/env bash
# install.sh — build and install agentbus binaries and the PostToolBatch hook.
#
# After running this once:
#   - agentbusd, agentbus-relay, agentbus-fetch are in ~/.local/bin/
#   - agentbus-hook.py is in ~/.local/bin/
#   - ~/.claude/settings.json has the PostToolBatch hook registered globally
#   - ~/.bob/settings/mcp.json has the agentbus MCP server entry
#
# Safe to re-run: all writes are idempotent.
#
# Requires: Go 1.22+, python3, jq, a running nats-server on 4222
# (install nats-server once with: curl -sf https://binaries.nats.dev/nats-io/nats-server/v2@latest | sh -s -- -b ~/.local/bin)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
BIN_DIR="${AGENTBUS_BIN_DIR:-$HOME/.local/bin}"
HOOK_DIR="$BIN_DIR"

echo "==> Building binaries from $REPO_DIR"
(cd "$REPO_DIR" && go build -o "$BIN_DIR/agentbusd" ./cmd/agentbusd)
(cd "$REPO_DIR" && go build -o "$BIN_DIR/agentbus-relay" ./cmd/agentbus-relay)
(cd "$REPO_DIR" && go build -o "$BIN_DIR/agentbus-fetch" ./cmd/agentbus-fetch)
echo "    agentbusd, agentbus-relay, agentbus-fetch -> $BIN_DIR/"

echo "==> Installing hook script"
cp "$REPO_DIR/hooks/agentbus-hook.py" "$HOOK_DIR/agentbus-hook.py"
chmod +x "$HOOK_DIR/agentbus-hook.py"
echo "    agentbus-hook.py -> $HOOK_DIR/"

echo "==> Registering PostToolBatch hook in ~/.claude/settings.json"
CLAUDE_SETTINGS="$HOME/.claude/settings.json"
mkdir -p "$(dirname "$CLAUDE_SETTINGS")"

# Create settings file if it doesn't exist
if [ ! -f "$CLAUDE_SETTINGS" ]; then
    echo '{}' > "$CLAUDE_SETTINGS"
fi

# Add the PostToolBatch hook if not already present
HOOK_CMD="$HOOK_DIR/agentbus-hook.py"
EXISTING=$(jq -r '.hooks.PostToolBatch // [] | map(.hooks // []) | flatten | map(.command // "") | .[]' "$CLAUDE_SETTINGS" 2>/dev/null || true)
if echo "$EXISTING" | grep -qF "$HOOK_CMD"; then
    echo "    PostToolBatch hook already registered, skipping"
else
    TMP=$(mktemp)
    jq --arg cmd "$HOOK_CMD" '
      .hooks.PostToolBatch = ((.hooks.PostToolBatch // []) + [{
        "hooks": [{"type": "command", "command": $cmd, "timeout": 10}]
      }])
    ' "$CLAUDE_SETTINGS" > "$TMP" && mv "$TMP" "$CLAUDE_SETTINGS"
    echo "    PostToolBatch hook -> $CLAUDE_SETTINGS"
fi

echo "==> Registering agentbus in ~/.bob/settings/mcp.json"
BOB_MCP="$HOME/.bob/settings/mcp.json"
mkdir -p "$(dirname "$BOB_MCP")"
if [ ! -f "$BOB_MCP" ]; then
    echo '{"mcpServers":{}}' > "$BOB_MCP"
fi

AGENTBUS_IN_BOB=$(jq -r '.mcpServers.agentbus // empty' "$BOB_MCP" 2>/dev/null || true)
if [ -n "$AGENTBUS_IN_BOB" ]; then
    echo "    agentbus entry already in $BOB_MCP, skipping"
else
    TMP=$(mktemp)
    jq --arg bin "$BIN_DIR/agentbusd" '
      .mcpServers.agentbus = {"command": $bin, "env": {"AGENTBUS_NATS_URL": "nats://127.0.0.1:4222"}}
    ' "$BOB_MCP" > "$TMP" && mv "$TMP" "$BOB_MCP"
    echo "    agentbus entry -> $BOB_MCP"
fi

echo ""
echo "==> Done. Start nats-server and agentbus-relay before use:"
echo "    nats-server -js -sd ~/.agentbus/nats -p 4222 &"
echo "    agentbus-relay &"
echo ""
echo "    Then add this worktree to the watch list for a mission:"
echo "    agentbus_publish_presence(mission=<slug>, worktree=<abs-path>, ...)"
