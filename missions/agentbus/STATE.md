# STATE — agentbus

**One-line goal.** Build a local, lightweight, cross-tool (Claude Code + IBM Bob) topic-based
pub/sub and durable message log for coordinating AI agent sessions on a shared mission, using
NATS JetStream as the transport and a minimal custom Go MCP server as the client-facing layer.

**Plan.** `missions/agentbus/spec-agentbus.md`. Read its "Settled design" section for
orientation, then go to the specific task section named below for what's actually next — the
per-task detail (intent/expected-outcome/todo/status) lives there, not here.

**Worktrees used.**
- `worktrees/agentbus` — this mission's own dedicated orphan-branch worktree, holds all Go
  source/binaries/scripts/hook script. Not based on `feat/wva-external-scaler` or upstream.
  This session may check/commit it freely; push only on the user's explicit per-operation ask.

**Mission-specific addendum (read this before doing anything else in this mission):**
- **This session's own tracking files live in `session-tracking`, but this session does not
  own that worktree.** Only ever touch `missions/agentbus/**` here. Never run `git status`/
  `fetch` against `session-tracking`'s `origin` as if checking its health, and never push
  `session-tracking` — that worktree belongs to the whole repo's session-tracking convention,
  not to this mission.
- `worktrees/agentbus` is different: it's this mission's own code worktree, safe to check and
  commit locally without asking; only pushing it needs a per-operation ask.
- The `.git/info/exclude` entries `**/.claude/mailbox/` and `**/.claude/agent-registry.json`
  are **not** this mission's to reuse — origin unconfirmed, likely a different concurrent
  session's. agentbus uses its own distinct directory names instead (see spec's T4/Open items).

**Immediate next step.** Plan's Verification step 2 (Spec §"Verification plan"): start a local
`nats-server`, drive `agentbusd` with a raw MCP stdio client, exercise all 4 tools live. This is
also T3's one remaining unchecked item (Spec §T3). Paused here at the user's request before
attempting it — not yet started.

**After that, in order:** T4 (Spec §T4 — relay + `PostToolBatch` hook; note its open
verification item: re-confirm the live hook JSON contract against current docs first), T5
(Spec §T5 — needs a proposal to the user before editing `resume-mission`'s `SKILL.md`, a file
outside this mission's own worktrees), T6's remaining items (Spec §T6 — add the `agentbus`
entry to `~/.bob/settings/mcp.json`, then the manual round-trip test), T7 (Spec §T7).

**Security note (bottom line only — full detail in this session's ledger if ever needed):** two
GitHub PATs were exposed in plaintext in conversation during this session, unrelated to this
mission's own work. User was told and advised to rotate both. No action pending on this
mission because of it.

## Session log

- 2026-08-27T20:16 session=2026-08-27-agentbus-design status=active ledger=ledgers/2026-08-27-agentbus-design.md
