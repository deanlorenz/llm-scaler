# STATE — agentbus

**One-line goal.** Build a local, lightweight, cross-tool (Claude Code + IBM Bob) topic-based
pub/sub and durable message log for coordinating AI agent sessions on a shared mission, using
NATS JetStream as the transport and a minimal custom Go MCP server as the client-facing layer.

**Spec.** `missions/agentbus/spec-agentbus.md` — task list, settled design decisions, refs.

**Worktrees used.**
- `worktrees/agentbus` — dedicated orphan branch, holds all Go source/binaries/scripts/hook
  script. Not based on `feat/wva-external-scaler` or upstream; tracked on `origin` only,
  pushed (`origin/agentbus` at commit `3f9aa44b` as of this session's end).

**Immediate next step.** T3's remaining piece: start a local `nats-server`, drive `agentbusd`
with a raw MCP stdio client, exercise all 4 tools live (this is Verification step 2 in the
spec) — deferred at the user's request to pause here, not yet attempted. After that: T4 (relay
+ `PostToolBatch` hook), T5 (`/resume-mission` wiring — needs a proposal to the user before
editing that shared skill file), T6's remaining piece (add the `agentbus` entry to
`~/.bob/settings/mcp.json`, confirmed as the right file by asking Bob directly; then the manual
round-trip test), T7 (install script, global hook registration, reusability docs).

**Open questions blocking full completion.**
- The live `PostToolBatch` hook JSON contract must be re-confirmed against current Claude Code
  docs before T4's hook script is finalized (explicitly flagged as unverified in the plan this
  mission implements) — still open.
- ~~Which MCP config file Bob actually loads at runtime~~ — **resolved**: Bob self-reported
  `~/.bob/settings/mcp.json` (global, primary) with `~/.bob/settings/mcp_settings.json` as a
  legacy fallback and `<workspace>/.bob/mcp.json` (doesn't exist yet) taking precedence over
  both if created.

**Progress snapshot (2026-08-27, end of this session).** T1 done and pushed. T2 done. T3 done
except the live NATS test. T6's config-location question resolved; the actual config entry and
round-trip test still pending. T4/T5/T7 not started. Both `worktrees/agentbus` and
`session-tracking` are committed and pushed clean — no uncommitted work on either branch as of
this session's end.

**Unrelated incident flagged during this session, not part of this mission's work:** two GitHub
PATs appeared in plaintext in the conversation (surfaced as a side effect of an unrelated
MCP-config query). User was told directly and advised to rotate both. See this session's ledger
for detail — not otherwise actioned by this mission.

## Session log

- 2026-08-27T20:16 session=2026-08-27-agentbus-design status=active ledger=ledgers/2026-08-27-agentbus-design.md
- 2026-08-27T21:40 session=2026-08-27-agentbus-design status=retired ledger=ledgers/2026-08-27-agentbus-design.md
