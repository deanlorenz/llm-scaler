# agentbus

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Build a local, lightweight, cross-tool (Claude Code + IBM Bob)
  topic-based pub/sub and durable message log for coordinating AI agent sessions on a shared
  mission, using NATS JetStream as the transport and a minimal custom Go MCP server as the
  client-facing layer.
- **Worktree:** `worktrees/agentbus` (branch `agentbus`)
- **Role / scope:** Mission owner. Owns STATE, plan, branch, and integration decisions.
  Commit freely; push only on explicit per-operation user approval.
- **Ledger / log:** `.session/2026-09-03-agentbus3.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `.session/spec-agentbus.md`
  *(do not read upfront — pull on demand only)*
- **Context:** *(none beyond STATE)*
- **Refs:**
  - `worktrees/session-tracking/missions/agentbus/` — convenience symlinks for tracking files
- **Expected output:** Fully working agentbus system: NATS JetStream transport, `agentbusd` MCP
  server (all tools), `agentbus-dialogue` CLI, `agentbus-relay` hook, MCP registered in Bob,
  resume-mission SKILL.md updated, end-to-end round-trip verified.
- **Done / completion criteria:** All spec tasks T1–T7 complete and verified; Bob MCP config
  live; manual round-trip test passes.
- **Limits:** Do not edit files outside `worktrees/agentbus` and `missions/agentbus/**` without
  explicit user approval per operation. Do not push without explicit single-use authorization.
- **Extra rules / rule refs:**
  - Mission-specific: tracking files live in `session-tracking/missions/agentbus/**` only.
    Never run `git status`/`fetch` against `session-tracking`'s `origin`; never push it.
  - The `.git/info/exclude` entries `**/.claude/mailbox/` and `**/.claude/agent-registry.json`
    are not this mission's — agentbus uses its own distinct directory names (see spec T4).
  - Two GitHub PATs were exposed in plaintext in a prior session (unrelated to this mission).
    User was notified. No action pending.

## Execution

### Steps / subtasks

- [x] T1 — NATS JetStream transport layer
- [x] T2 — `agentbusd` MCP server (core 4 tools)
- [x] T3 — Build, install, smoke-test `agentbusd` (pending: live NATS verification step 2)
- [x] T4 — `agentbus-relay` + `PostToolBatch` hook
- [x] T5 — `agentbus-dialogue` CLI (TTY fix, readline, async `agentbus_ask_user`, agentbus-pub)
- [ ] T6 — Register `agentbus` in `~/.bob/settings/mcp.json`; manual round-trip test
- [ ] T7 — (see spec §T7)
- [ ] Verification step 2 — start local `nats-server`, drive `agentbusd` with raw MCP stdio
      client, exercise all 4 tools live (T3's one remaining unchecked item)

**Last completed:** T5 — async `agentbus_ask_user` + code-review fixes (session agentbus3,
2026-09-03). Dialogue async UX broken (sequential blocking loop); noted in spec as open item.

**Next step / resume point:** Verification step 2 (Spec §"Verification plan") — start local
`nats-server`, drive `agentbusd` with a raw MCP stdio client, exercise all 4 tools live.
Confirm with user before executing.

### Status

- T1–T2: DONE
- T3: DONE (live NATS verification step 2 still pending — paused at user request)
- T4: DONE
- T5: DONE (agentbus-dialogue async UX known broken — bubbletea TUI needed, deferred)
- T6: NOT STARTED
- T7: NOT STARTED

### Known issues

- `agentbus-dialogue` async UX: sequential blocking loop — new messages cannot be shown while
  awaiting reply. Needs concurrent input/display model (bubbletea TUI or goroutine split).
  Deferred; does not block T6/T7.

## Session log

- 2026-08-27T20:16 session=2026-08-27-agentbus-design status=retired ledger=.session/2026-08-27-agentbus-design.md
- 2026-09-03 session=2026-09-03-agentbus-dialogue status=retired ledger=.session/2026-09-03-agentbus-dialogue.md
- 2026-09-03 session=2026-09-03-agentbus3 status=retired ledger=.session/2026-09-03-agentbus3.md
