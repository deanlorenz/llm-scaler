---
name: conv-agentbus
description: Use at session startup to configure agentbus channels and subscriptions, or when setting up agentbus for a new session or subagent.
---

# conv-agentbus

Runs in your own session. Apply at session startup — before any work begins.

## Channel naming

Define channels in `.session/STATE.md` (or task file) under Orientation:

- **Inbox (`In:`):** `<mission>.<role-or-slug>.in` — e.g. `policy-writer.owner.in`
- **Outbox (`Out:`):** `<mission>.<role-or-slug>` — e.g. `policy-writer.owner`
- **Announce (`Announce:`):** `mission.<mission-name>` — e.g. `mission.policy-writer`

## Session startup sequence

1. **Subscribe to your inbox:**
   ```
   agentbus_subscribe(topic="<my-in-topic>", session_id="<slug>")
   ```
   On resume: check with `agentbus_status(session_id="<slug>")` — re-subscribe if dropped.

2. **Mission owner:** also subscribe to `mission.<mission-name>` to receive worker lifecycle events and handoffs.

3. **policy-writer sessions:** also subscribe to `session-tracking.suggestions` and `session-tracking.pending-commits`.

4. **Announce presence:**
   ```
   agentbus_publish(topic="mission.<mission-name>", from_session="<slug>", kind="announce",
     body="session=<slug> role=<role> online. in=<my-in-topic> out=<my-out-topic>")
   ```

## Background invocation contract

Every background agent launch must include in the task file and launch prompt:
- `In:` channel
- `Out:` channel
- The subscription command

The child subscribes to `In:` before work starts, remains subscribed while running, answers any parent request on `Out:` before continuing, and publishes its final result to `Out:` before exiting.

## Status updates to user

Any session may publish non-blocking visibility notes to the user:
```
agentbus_publish(topic="user.in", from_session="<slug>", kind="note",
  body="<progress or status update>")
```

This does not replace normal interactive chat responses in foreground sessions.
