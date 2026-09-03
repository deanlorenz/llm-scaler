# agentbus — Settled Design (v2)

This document supersedes the original spec's "Settled design" section. It reflects
decisions made during implementation and the design conversation that followed.
Implement against this doc, not the original spec.

---

## Purpose

A local, lightweight pub/sub and durable message log for coordinating AI agent
sessions (Claude Code, Bob, any MCP-capable tool) working on a shared project on
one developer machine. Sessions are intermittent processes that restart; the bus
provides durable replay so a resumed session can catch up on what it missed.

---

## Transport

Single `nats-server` instance per machine, JetStream enabled, persisting to
`~/.agentbus/nats/`. One stream `AGENTBUS` captures all topic subjects:
`agentbus.>` (NATS `>` wildcard = all levels below). Per-stream retention TBD
(configure max age when it becomes a concern).

---

## Scope / Bus ID

Each project using agentbus has a **bus ID** — a short human-chosen slug
(e.g. `llmd-scaler`). All topic names are internally prefixed with the bus ID
so topics from different projects never collide on the shared NATS server.

### Registration (once per project)

```
agentbus-setup <bus_id>
```

Run once from anywhere inside the project. Writes `cwd → bus_id` into
`~/.agentbus/repos.json`. Any subdirectory of that path is considered the
same project. Example:

```json
{
  "/home/dean/code/llm-d/dean-llmd-scaler-sandbox": "llmd-scaler"
}
```

A session running in `…/worktrees/agentbus` matches the entry above via
longest-prefix lookup.

### Resolution at runtime

`agentbusd` reads `CLAUDE_PROJECT_DIR` from its environment at startup, looks
up the bus ID via prefix match in `~/.agentbus/repos.json`. If no match is
found, it exits with a clear error: `run: agentbus-setup <bus_id>`. No silent
fallback, no auto-generated IDs.

The bus ID is held in memory for the lifetime of the `agentbusd` process
(one ephemeral process per session, started by the MCP framework, exits with
the session). The file is the only persistent store — no separate in-memory
cache needed.

---

## Topics

Topics are **free strings** chosen by the caller. The system enforces no
hierarchy — dotted names like `M1.C1` or `M1.P1.C1` are a naming convention,
not a structural requirement.

Internally, `agentbusd` prepends the bus ID before publishing/fetching:
`<bus_id>.<topic>` → NATS subject `agentbus.<bus_id>.<topic>`.
Callers never see the prefix — they use short topic names only.

### Naming convention (not enforced)

```
<mission>                   mission broadcast / announcements
<mission>.<role>            a session's outbox  (e.g. M1.C1)
<mission>.<parent>.<child>  parent's dedicated inbox for child (e.g. M1.P1.C1)
<bus_id>.broadcast          optional machine-wide broadcast channel
user.in                     human dialogue inbox (incoming questions to user)
user.out                    human dialogue outbox (replies from user)
```

---

## Message schema

```json
{
  "topic":      "M1.C1",
  "from":       {"agent": "claude-code", "session": "2026-08-30-coder"},
  "ts":         "2026-08-30T10:00:00Z",
  "kind":       "note",
  "seq":        42,
  "reply_to":   null,
  "body":       "free text",
  "refs":       ["repo-relative/path.md"]
}
```

- `seq` is the JetStream stream sequence number, assigned on publish and
  returned to the caller. It is the message's identity for replay/cursor.
- `kind` is an open vocabulary. Reserved values by convention:
  - `announce` — session announcing its in/out topics (published on parent topic)
  - `presence` — session announcing liveness (published on `<bus_id>.presence`)
  - `heartbeat` — periodic liveness signal from a long-running tool/process
  - anything else is application-defined

---

## Session start protocol

When any session starts it should know (passed in its instructions or read
from context):

| Parameter | Description |
|---|---|
| `in_topic` | where to read assignments from parent |
| `out_topic` | where to publish results |
| `announce_topic` | where to announce itself (typically the parent's outbox or mission root) |
| `broadcast_topic` | optional; `<bus_id>.broadcast` for machine-wide signals |

At start, the session:
1. Calls `agentbus_subscribe(in_topic, session_id)` — registers relay watch
2. Publishes `kind=announce` on `announce_topic` with body containing its
   `in_topic` and `out_topic`, so the parent knows it is alive and ready
3. Optionally calls `agentbus_subscribe(broadcast_topic, session_id)`

The orchestrator that spawned it:
1. Calls `agentbus_subscribe(out_topic, session_id)` to watch subagent output
2. Already knows `in_topic` because it chose the name when spawning

A top-level session (human-started, no parent) uses the mission root topic as
both its `out_topic` and `announce_topic`, and subscribes to any subagent
outboxes it spawns dynamically.

---

## MCP tool surface

All tools are on `agentbusd`. Bus ID is resolved server-side — callers never
pass it.

### `agentbus_publish`
```
topic       string   — destination topic (short name, no bus_id prefix)
from_session string  — caller's session slug/id
kind        string?  — open vocabulary (note, question, handoff, announce, presence, …)
body        string   — message text
reply_to    uint64?  — seq of message being replied to
refs        []string? — repo-root-relative doc paths
```
Returns: `{seq: uint64}`

### `agentbus_fetch_since`
```
topic       string   — topic to read from
since_seq   uint64   — return messages with seq > this
limit       int?     — max messages, default 50
kind        string?  — optional filter; only return messages of this kind
```
Returns: `{messages: [...], last_seq: uint64}`
Can be called on demand at any time — not only by the hook.

### `agentbus_subscribe`
```
topic       string   — topic to watch
session_id  string   — this session's id
```
Writes subscription to `~/.agentbus/subs/<bus_id>/<session_id>.json` (a list
of topics). The relay reads this file and ensures it writes marker files for
this session when new messages arrive on these topics.
Returns: `{}`

### `agentbus_unsubscribe`
```
topic       string
session_id  string
```
Removes topic from the session's subscription file.
Returns: `{}`

### `agentbus_status`
```
session_id  string?  — if provided, also return this session's subscriptions/cursors
```
Returns all topics with recent activity under this bus: last seq, last
message timestamp, last sender. If `session_id` given, adds that session's
current subscription list and per-topic cursor values.

### `agentbus_ask_user`
```
prompt          string   — question / prompt text for the user
from_session    string   — caller's session slug/id
timeout_seconds int?     — max seconds to wait for user reply (default 300)
refs            []string? — repo-root-relative doc paths
```
Publishes message with `kind: "question"` to `user.in`, obtains assigned `seq`,
and synchronously waits for a reply message on `user.out` where `reply_to == seq`.
Returns: `{reply: string, seq: uint64, from: {...}, timed_out: bool}`

---

## Human Dialogue Protocol (`agentbus-dialogue`)

An interactive terminal CLI (`agentbus-dialogue`) allows the developer to interact with agents in real time via a dedicated VS Code terminal pane:

1. Subscribes to `user.in` for the current project (`bus_id`).
2. When a message arrives (e.g. `kind: "question"`), displays sender, timestamp, and message body with terminal bell (`\a`).
3. Prompts the user (`> `) for input.
4. On Enter, publishes an answer message to `user.out` with `reply_to: <question_seq>` and `kind: "answer"`.
5. The calling agent either receives this via `agentbus_ask_user` (synchronously) or via `agentbus_fetch_since` on `user.out` (asynchronously).

---

## Silent-wake mechanism

### `agentbus-relay` (one per machine, always running)

- Subscribes to `agentbus.>` on NATS (push consumer, `DeliverNewPolicy`)
- On each incoming message, reads `~/.agentbus/subs/<bus_id>/` to find all
  sessions subscribed to that topic
- For each matching session, atomically writes/overwrites:
  `~/.agentbus/markers/<bus_id>/<session_id>/<topic>.marker`
  contents: `{"last_seq": N, "updated_at": "…"}`

### `agentbus-hook` (PostToolBatch hook, global, one per machine)

Registered once in `~/.claude/settings.json`. Fires on every model turn.

1. Reads `session_id` and `cwd` from stdin JSON
2. Resolves bus_id from `cwd` via `~/.agentbus/repos.json`
3. Reads `~/.agentbus/subs/<bus_id>/<session_id>.json` — list of topics
4. For each topic, compares:
   - `~/.agentbus/markers/<bus_id>/<session_id>/<topic>.marker` (relay wrote)
   - `~/.agentbus/cursors/<bus_id>/<session_id>/<topic>.cursor` (hook wrote)
5. If marker seq > cursor seq: connects to NATS, calls `bus.FetchSince`,
   collects new messages
6. If any new messages: emits `hookSpecificOutput.additionalContext` to stdout,
   updates cursor files
7. If nothing new: exits 0 with no output (the common case, ~1ms)

All files are under `~/.agentbus/` — no worktree dependency, sandbox-safe.

---

## Filesystem layout

```
~/.agentbus/
  repos.json                                         project-root → bus_id map
  nats/                                              JetStream storage (nats-server -sd)
  subs/<bus_id>/<session_id>.json                   subscribed topics list
  markers/<bus_id>/<session_id>/<topic>.marker      relay writes on new message
  cursors/<bus_id>/<session_id>/<topic>.cursor      hook writes after fetch
```

Topic names in filenames have `/` replaced with `_` to avoid path issues.

---

## Binaries

| Binary | Role | Lifetime |
|---|---|---|
| `nats-server` | message broker | always running, per machine |
| `agentbus-relay` | marker writer | always running, per machine |
| `agentbusd` | MCP server (6 tools) | ephemeral, one per session |
| `agentbus-hook` | PostToolBatch hook | subprocess per turn |
| `agentbus-setup` | one-time project init | CLI, run once |
| `agentbus-dialogue` | human CLI dialogue | interactive terminal pane |

---

## What the current code implements (pre-v2)

The committed code implements a simpler v1 design: `mission` as the only routing
key, no bus ID, no subscribe/unsubscribe tools, markers and cursors in the
worktree. The v2 design above supersedes it. The internal `bus` package
(`stream.go`, `publish.go`, `fetch.go`) is largely reusable; the tool surface,
schema, relay, hook, and filesystem layout all need updating.
