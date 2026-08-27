# Spec — agentbus: local cross-tool agent pub/sub

## Intent

A local, lightweight, tool-agnostic pub/sub and durable-message-log system for coordinating
multiple AI coding agent sessions (Claude Code, in any of its session modes, and IBM's Bob
agent framework) working on a shared "mission" on one developer machine over time. Sessions
restart and must rediscover peers/history by topic (mission name), not by pre-shared session
IDs. Must not depend on any Claude-specific SDK feature (Claude's own `SendMessage`/`ListAgents`
are explicitly out of scope as the transport) so that Bob, or any other MCP-capable tool, can
participate on equal footing.

Full design rationale, the alternatives considered and rejected (MCP Agent Mail, ZeroMQ/nng,
vendoring `daanrongen/nats-mcp`), and the reasoning behind each settled decision were worked out
in a plan-mode conversation and originally recorded only in a local, uncommitted plan file
(`~/.claude/plans/i-am-looking-mellow-pizza.md`, on the session that ran that conversation — not
visible to any other session). That gap has been closed: the settled-design summary below, the
full task list, and the Verification plan section near the end of this doc are the durable,
shared copy — this spec doc, not that plan file, is the source of truth for anyone continuing
this mission.

## Settled design (do not re-litigate without a new decision from the user)

- **Transport:** local NATS server + JetStream (single static Go binary `nats-server`). Topics
  are NATS subjects (`agentbus.mission.<mission-slug>.msg` / `...presence`), each bound to a
  durable JetStream stream (`AGENTBUS`, `AGENTBUS_PRESENCE`). Offset/sequence-based replay via
  JetStream gives "a restarted session resumes from last-seen point" natively.
- **MCP server:** a new, minimal Go MCP server (`agentbusd`), built from scratch (not vendoring
  `daanrongen/nats-mcp` — its Effect-TS architecture costs more to trim than to write fresh),
  using the official MCP Go SDK (`modelcontextprotocol/go-sdk`) + `nats.go`. Four tools only:
  `agentbus_publish`, `agentbus_fetch_since`, `agentbus_publish_presence`,
  `agentbus_list_missions`. No server-side consumer lifecycle, no ack tool — the read cursor is
  caller-held (a plain integer in a local file), not managed by the server.
- **Wake mechanism, Claude side:** the `PostToolBatch` hook (fires once per model turn, in every
  session mode — interactive, auto, background subagent — and is not blocked by backgrounded
  work). A relay daemon (`agentbus-relay`) subscribes to NATS and writes a small per-mission
  marker file per watched worktree; the hook does one cheap local file read per turn and only
  does a real fetch when the marker shows something new. A genuinely idle session (no turn
  running) is not woken — the message waits for that session's next natural activity. This is
  an accepted trade-off: zero ambient noise outranks instant delivery.
- **Wake mechanism, Bob side:** explicitly stubbed. Bob's own trigger mechanics were not
  verified. Bob integration is MCP config wiring only — Bob calls the same 4 tools whenever it
  naturally runs a turn.
- **Message schema:** plain JSON, tool-agnostic, no Claude-specific fields. See the plan for the
  exact shape (`mission`, `from.agent`/`from.session`, `ts`, `kind`, `reply_to`, `body`, `refs`).
  No separate `id` field — the JetStream sequence number is the identity.

## Composition with session-tracking conventions

agentbus's **code** (Go source, binaries, install scripts, the hook script) lives in its own
dedicated orphan-branch worktree, `worktrees/agentbus/` — separate from this mission's tracking,
mirroring how `session-tracking` itself is a dedicated worktree separate from feature-work
worktrees. This mission's `STATE.md` names `worktrees/agentbus` under "Worktrees used," same as
any other mission naming its feature worktree.

## Todo

### T1 — Worktree + mission scaffolding
**Intent.** Stand up the two homes this mission needs: the code worktree and this tracking
mission.
**Expected outcome(s).** `worktrees/agentbus` exists as an orphan branch with no shared history;
`worktrees/session-tracking/missions/agentbus/` exists with this spec, `STATE.md`, `ledgers/`.
**Todo.**
- [x] Create orphan branch/worktree `worktrees/agentbus`
- [x] Create `missions/agentbus/` (this spec, `STATE.md`, `ledgers/`)
- [x] Set up per-skill symlinks (`resume-mission`, `wind-down`) inside `worktrees/agentbus/.claude/skills/` — verified both resolve
- [x] `.git/info/exclude` already covers `.claude/skills/{resume-mission,wind-down}` globally (added by a concurrent session); nothing new needed
- [x] First commit + push to `origin` on the `agentbus` branch (user confirmed before push)
**Refs.** *Writes:* `worktrees/agentbus/**`, `missions/agentbus/**`.
**Status.** DONE 2026-08-27. Both branches pushed (`origin/agentbus` @ `2cbf771b` initially,
`origin/session-tracking` @ `acc7df88` for the mission registration commit).

### T2 — Go module layout + message schema
**Intent.** Establish the code skeleton before writing real logic.
**Expected outcome(s).** `go.mod`, `internal/schema/message.go`, empty-but-compiling
`cmd/agentbusd`, `cmd/agentbus-relay`.
**Todo.**
- [x] `go.mod` in `worktrees/agentbus` (module `github.com/deanlorenz/agentbus`)
- [x] `internal/schema/message.go` — `Message`, `Presence`, `From`
- [x] `cmd/agentbusd`, `cmd/agentbus-relay` skeletons (agentbusd ended up fully implemented —
      see T3)
**Refs.** *Writes:* `worktrees/agentbus/{go.mod,internal/schema/message.go,cmd/**}`.
**Status.** DONE 2026-08-27. All API calls (MCP Go SDK, `nats.go`/`jetstream`) verified against
the actual vendored source in the module cache before use — caught and fixed two wrong initial
guesses (see T3's completion note). `go build ./...`, `go vet ./...`, `gofmt -l .` all clean.

### T3 — `agentbusd` MCP server: 4 tools over JetStream
**Intent.** The actual pub/sub substrate, reachable via MCP.
**Expected outcome(s).** All 4 tools implemented and independently testable via a raw MCP stdio
client against a local `nats-server`.
**Todo.**
- [x] `internal/bus/stream.go` — idempotent stream ensure (`AGENTBUS`, `AGENTBUS_PRESENCE`)
- [x] `internal/bus/publish.go`, `internal/bus/fetch.go`, `internal/bus/missions.go`
- [x] Tool handlers + registration in `cmd/agentbusd/{main.go,tools.go}`
- [ ] Independent testing via a raw MCP stdio client against a real local `nats-server` — not
      yet done, only `go build`/`go vet`/`gofmt` verified so far (see Verification step 2)
**Refs.** *Writes:* `worktrees/agentbus/{internal/bus/**,cmd/agentbusd/**}`.
**Status.** IN PROGRESS, 2026-08-27 — all 4 tools implemented and compiling; live end-to-end
test against a running `nats-server` still to do. Completion notes: `agentbus_list_missions`
currently returns only the most recent presence announcement's session per mission, not a
merged list of every currently-active session for that mission — a known simplification, fine
for a first pass, revisit in T7 if it matters in practice. Two wrong API guesses caught by
checking vendored source directly rather than trusting memory: (1) `OrderedConsumer` is a
method on `jetstream.JetStream` itself, not on `jetstream.Stream` — there is no
`Stream.CreateOrderedConsumer`; (2) ordered consumers use `AckNonePolicy`, so no per-message
`Ack()` call is needed or wanted, matching the design's caller-held-cursor approach anyway.

### T4 — Relay daemon + `PostToolBatch` hook
**Intent.** The silent-wake mechanism.
**Expected outcome(s).** `agentbus-relay` running against a test worktree updates a marker file
within its poll interval of a published message; `agentbus-hook.py` invoked twice in a row
(no new message, then a new message) prints nothing then prints `additionalContext` correctly.
**Todo.**
- [ ] `cmd/agentbus-relay/main.go` — NATS subscribe, watched-worktrees registry, marker writes
- [ ] `hooks/agentbus-hook.py`
- [ ] Re-verify the live `PostToolBatch` hook JSON contract against current Claude Code docs
      before finalizing the hook script (flagged explicitly in the plan as unverified)
**Refs.** *Writes:* `worktrees/agentbus/{cmd/agentbus-relay/**,hooks/agentbus-hook.py}`.
**Status.** NOT STARTED

### T5 — Session identity wiring (`/resume-mission` integration)
**Intent.** Presence publication piggybacks on the existing mission-start flow instead of a
separate always-on hook.
**Expected outcome(s).** `/resume-mission`'s Step 7 also calls `agentbus_publish_presence` with
the mission/session slug it already has.
**Todo.**
- [ ] Propose the edit to `worktrees/session-tracking/.claude/skills/resume-mission/SKILL.md`
      to the user before making it (shared, tracked skill file, not part of this mission's own
      code worktree)
**Refs.** *Reads:* `.claude/skills/resume-mission/SKILL.md`. *Writes:* same, pending approval.
**Status.** NOT STARTED

### T6 — Bob-side stub + verification
**Intent.** Prove real cross-tool interoperability, not just cross-Claude-session.
**Expected outcome(s).** Bob's live MCP config confirmed and pointed at `agentbusd`; a manual
Bob-side `agentbus_fetch_since` call returns the same history a Claude-side session produced.
**Todo.**
- [x] Ask Bob directly which MCP config file it actually loads at runtime — Bob self-reported:
      global primary key `~/.bob/settings/mcp.json`, global legacy key
      `~/.bob/settings/mcp_settings.json`, project-level `<workspace>/.bob/mcp.json` (does not
      exist for this workspace, would take precedence if created). Project-level takes
      precedence over global when both exist.
- [ ] Add the `agentbus` entry to `~/.bob/settings/mcp.json` (the primary global key, per Bob's
      own report) once `agentbusd` exists (T3)
- [ ] Manual round-trip test
**Refs.** *Writes:* `~/.bob/settings/mcp.json`.
**Status.** IN PROGRESS, 2026-08-27 — config file location confirmed directly by Bob; entry
itself still pending `agentbusd` existing.

### T7 — Reusability polish
**Intent.** Make agentbus trivially addable to any other project, not just this repo.
**Expected outcome(s).** `scripts/install.sh` installs binaries to `~/.local/bin`/hook to
`~/.claude/bin`; the `PostToolBatch` hook registered once in `~/.claude/settings.json` (global,
not per-project).
**Todo.**
- [ ] `scripts/install.sh`
- [ ] Register the global hook (ask user before editing `~/.claude/settings.json`)
- [ ] Document the one `.mcp.json` entry a new project adds
**Refs.** *Writes:* `worktrees/agentbus/scripts/install.sh`, `~/.claude/settings.json`.
**Status.** NOT STARTED

## Verification plan (copied from the approved plan, closing that doc-reference gap)

1. **Substrate:** start `nats-server -js -sd ~/.agentbus/nats`; confirm it listens on 4222.
2. **MCP server standalone:** drive `agentbusd` with a raw MCP stdio test client; publish then
   fetch-since a test mission; confirm round-trip. *(T3's remaining item — this session's
   deferred stopping point.)*
3. **Durability/replay:** publish 3 messages, open a *second, independent* client connection
   (proving state lives in `nats-server`, not tied to any one `agentbusd` process lifetime),
   `fetch_since(since_seq=1)`, confirm exactly messages 2 and 3 come back.
4. **Relay isolated:** start `agentbus-relay` against a scratch test worktree with a fake
   presence file already in place; publish a message; confirm the marker file updates within
   the relay's poll interval.
5. **Hook script isolated:** invoke `agentbus-hook.py` directly with synthetic stdin twice in a
   row — first call surfaces `additionalContext` and updates the cursor; second call (nothing
   new) prints nothing at all.
6. **Two live Claude Code sessions, end to end:** with the global hook registered, run two real
   sessions against the same test mission. Publish from session A. Confirm session B's ordinary
   turns show no visible chatter before the message lands, and that its response naturally
   reacts to the injected context on the turn after the relay updates the marker — without B
   ever making an explicit "check messages" tool call.
7. **Bob stub:** point Bob's actual MCP config (`~/.bob/settings/mcp.json`, confirmed by Bob
   itself — see T6) at `agentbusd`, manually invoke `agentbus_fetch_since` from a Bob session,
   confirm it returns the same history — proving real cross-tool interoperability, not just
   cross-Claude-session.

## Open items

- Confirm the live `PostToolBatch` hook JSON contract against current docs before T4 finalizes
  (still open — see T4).
- The pre-existing `.git/info/exclude` entries `**/.claude/mailbox/` /
  `**/.claude/agent-registry.json` are NOT this mission's to use — origin unconfirmed, likely
  another concurrent session's; agentbus uses its own distinct path names instead (resolved by
  not colliding, not by removing those entries).

This spec doc now carries the design rationale, task list, and verification plan in full — the
plan file at `/home/dean/.claude/plans/i-am-looking-mellow-pizza.md` (local to one Claude
session's plan-mode storage, never committed) can be treated as superseded by this doc rather
than a dependency for anyone continuing this mission.
