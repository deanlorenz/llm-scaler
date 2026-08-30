#!/usr/bin/env python3
"""agentbus PostToolBatch hook.

Fires once per model turn (after all parallel tool calls resolve, before the
next model call). Does one cheap local file read per turn; only contacts NATS
when the marker file shows something newer than this session's cursor.

Registered globally in ~/.claude/settings.json under PostToolBatch so it runs
in every session automatically without per-project configuration.

Exit codes:
  0  — no new messages (silent; stdout JSON returned to Claude only when needed)
  0  — new messages found; JSON with additionalContext written to stdout
"""

import json
import os
import sys

# ---------------------------------------------------------------------------
# Constants — mirror the paths written by the relay and the MCP server.
# ---------------------------------------------------------------------------
AGENTBUS_DIR = ".claude/agentbus"
PRESENCE_DIR = os.path.join(AGENTBUS_DIR, "presence")
INBOX_DIR = os.path.join(AGENTBUS_DIR, "inbox")
CONSUMER_DIR = os.path.join(AGENTBUS_DIR, "consumer")

NATS_URL = os.environ.get("AGENTBUS_NATS_URL", "nats://127.0.0.1:4222")


def main():
    # --- 1. Read the hook's stdin JSON ---
    try:
        hook_input = json.load(sys.stdin)
    except Exception:
        sys.exit(0)

    cwd = hook_input.get("cwd", "")
    session_id = hook_input.get("session_id", "unknown")

    if not cwd:
        sys.exit(0)

    # --- 2. Check if this session has declared presence for any mission ---
    presence_dir = os.path.join(cwd, PRESENCE_DIR)
    if not os.path.isdir(presence_dir):
        sys.exit(0)  # no missions registered in this worktree — common case

    missions = [
        f[:-5]  # strip ".json"
        for f in os.listdir(presence_dir)
        if f.endswith(".json")
    ]
    if not missions:
        sys.exit(0)

    # --- 3. For each mission, check marker vs cursor ---
    new_messages = []
    updated_cursors = {}  # mission -> new cursor value

    for mission in missions:
        marker_path = os.path.join(cwd, INBOX_DIR, mission + ".marker")
        cursor_path = os.path.join(cwd, CONSUMER_DIR, session_id + "." + mission + ".cursor")

        # Read marker — written by the relay daemon.
        marker_seq = read_marker_seq(marker_path)
        if marker_seq is None:
            continue  # relay hasn't written anything yet

        # Read cursor — this session's own last-seen sequence.
        cursor_seq = read_cursor(cursor_path)

        if marker_seq <= cursor_seq:
            continue  # nothing new

        # --- 4. Fetch new messages from NATS via agentbusd ---
        msgs = fetch_since(mission, cursor_seq)
        if msgs:
            new_messages.extend(msgs)
            updated_cursors[mission] = msgs[-1].get("_seq", marker_seq)
        else:
            # Marker says new but fetch returned nothing (race or transient
            # issue) — advance cursor to marker to avoid re-fetching forever.
            updated_cursors[mission] = marker_seq

    if not new_messages:
        sys.exit(0)

    # --- 5. Update cursors ---
    for mission, new_seq in updated_cursors.items():
        cursor_path = os.path.join(cwd, CONSUMER_DIR, session_id + "." + mission + ".cursor")
        write_cursor(cursor_path, new_seq)

    # --- 6. Emit additionalContext ---
    context_text = format_context(new_messages)
    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolBatch",
            "additionalContext": context_text,
        }
    }
    print(json.dumps(output))
    sys.exit(0)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def read_marker_seq(path):
    """Return the last_seq from a marker file, or None if absent/unreadable."""
    try:
        with open(path) as f:
            data = json.load(f)
        return int(data["last_seq"])
    except Exception:
        return None


def read_cursor(path):
    """Return the cursor integer stored at path, or 0 if absent."""
    try:
        with open(path) as f:
            return int(f.read().strip())
    except Exception:
        return 0


def write_cursor(path, seq):
    """Atomically write seq to the cursor file."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    try:
        with open(tmp, "w") as f:
            f.write(str(seq))
        os.replace(tmp, path)
    except Exception:
        pass  # best-effort; next turn will re-fetch if cursor wasn't saved


def fetch_since(mission, since_seq):
    """Fetch new messages using agentbus-fetch, a one-shot CLI that connects
    to NATS, prints JSON, and exits. Returns [] on any error.
    """
    import subprocess

    fetch_bin = os.environ.get("AGENTBUS_FETCH_BIN", "agentbus-fetch")

    try:
        result = subprocess.run(
            [fetch_bin, mission, str(since_seq), "50"],
            capture_output=True,
            text=True,
            timeout=5,
        )
    except Exception:
        return []

    if result.returncode != 0:
        return []

    try:
        payload = json.loads(result.stdout)
        raw_msgs = payload.get("messages") or []
        last_seq = payload.get("last_seq", since_seq)
        for m in raw_msgs:
            m["_seq"] = last_seq
        return raw_msgs
    except Exception:
        return []


def format_context(messages):
    """Format new messages into the additionalContext string Claude will read."""
    lines = ["[agentbus] New messages on your mission:"]
    for m in messages:
        mission = m.get("mission", "?")
        agent = m.get("from", {}).get("agent", "?")
        session = m.get("from", {}).get("session", "?")
        ts = m.get("ts", "")
        kind = m.get("kind", "")
        body = m.get("body", "")
        refs = m.get("refs", [])

        kind_str = f" [{kind}]" if kind else ""
        refs_str = f"\n  refs: {', '.join(refs)}" if refs else ""
        lines.append(
            f"  [{ts}]{kind_str} {agent}/{session} on mission={mission}:\n"
            f"  {body}{refs_str}"
        )
    return "\n".join(lines)


if __name__ == "__main__":
    main()
