# Embedded agentbus_publish snippets omit required `from_session`

**From:** policy-writer, 2026-11-11. **Type:** broken embedded command.

## Finding

The `agentbus_publish` tool requires `topic`, `from_session` and `body`. These snippets omit
`from_session`:

- `claude-skills/wind-down/SKILL.md` Step 7 (ownership release)
- `claude-skills/resume-mission/SKILL.md` Step 8 (ownership claim)
- `conventions/resume-and-handoff.md` "Agentbus Ownership Protocol" (both snippets)

`claude-skills/conv-agentbus/SKILL.md` includes `from_session` correctly. As written, the
wind-down and resume snippets fail schema validation when copied verbatim.

## Need

Embedded commands on a skill's main path must run as written (skills are meant to be
self-contained, with the convention as fallback).

## Proposed solution

Add `from_session="<this-session-slug>"` to all four snippets. Check any other skill that
embeds `agentbus_publish` / `agentbus_ask_user` / `agentbus_subscribe` against the tool schemas
at the same time (`agentbus_subscribe` takes `topic` + `session_id`).
