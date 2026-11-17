# Agentbus heartbeat is required by the resume protocol but nothing defines or publishes it

**From:** policy-writer, 2026-11-11. **Type:** undefined mechanism.

## Finding

`conventions/resume-and-handoff.md` (Resume step 2) says: "Check agentbus for a recent
heartbeat from the active slug." No file in `CONVENTIONS.md`, `conventions/`, `claude-skills/`
or `conventions/archived/` defines what a heartbeat message is, who publishes it, or how often.
`conv-agentbus` only publishes a one-time `kind="announce"` at startup. The agentbus
`publish` tool lists `heartbeat` as an example kind, so the kind is possible, but no session
sends it. The check as written would always find nothing.

Owner's recollection: the intent was for background agents to signal that they are alive and
progressing.

Also unverified: whether agentbus messages carry a timestamp. `agentbus_fetch_since` takes
`topic`, `since_seq`, optional `kind`, `limit` — no time filter — so "recent" cannot be
evaluated without a timestamp in the message body or metadata.

## Need

A defined liveness signal that (a) resume can check to decide "previous session still alive",
and (b) lets a parent see that a background agent is progressing.

## Proposed solution (owner to choose)

1. Define in `conv-agentbus`: `agentbus_publish(topic="mission.<name>", from_session="<slug>",
   kind="heartbeat", body="session=<slug> ts=<ISO-8601> status=<short>")`, published by the
   session (or its hook-driven background agent) every N minutes; resume treats the slug as
   alive if the newest heartbeat is younger than 2N.
2. Fallback if sessions cannot publish periodically: resume uses the latest `announce` /
   `handoff` message on `mission.<name>`; an unreleased announce is treated as "possibly alive,
   ask the user".
3. Or drop the automated check and rely on the Case 2 user confirmation.
