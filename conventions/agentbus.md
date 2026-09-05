# agentbus — Session Messaging & Channel Protocol

Read this at session start to configure agentbus channels and subscriptions.

## Overview & Scope

agentbus provides persistent, asynchronous publish/subscribe messaging across sessions, roles, and worktrees. Every session connects to agentbus on startup to establish its communication channels.

## Channel Naming & Definition

Each session's inbox and outbox channels are defined in its `.session/STATE.md` (or task file) under the **Orientation** section:

- **Inbox Channel (`In:`):** `<mission>.<role-or-slug>.in` (e.g. `policy-writer.owner.in`, `single-analyzer.coder-1.in`)
- **Outbox Channel (`Out:`):** `<mission>.<role-or-slug>` (e.g. `policy-writer.owner`, `single-analyzer.coder-1`)
- **Announce Channel (`Announce:`):** `mission.<mission-name>` (e.g. `mission.policy-writer`, `mission.single-analyzer`)

*Channel definitions are established by the mission owner or parent session when creating STATE.*

## Session Startup & Resume Sequence

1. **Verify / Establish Subscriptions:**
   - **New Sessions:** Subscribe to your dedicated inbox channel:
     ```
     agentbus_subscribe(topic="<my-in-topic>", session_id="<slug>")
     ```
   - **Resuming Sessions:** Check current subscription status via `agentbus_status(session_id="<slug>")`. Re-subscribe to your inbox channel if subscriptions were dropped across restarts.
2. **Mission Owner Subscriptions:**
   - Mission owners must additionally subscribe to the mission announcement channel (`mission.<mission-name>`) to receive worker presence, lifecycle events, and handoffs.
3. **Policy-Writer Subscriptions:**
   - `policy-writer` sessions must additionally subscribe to `session-tracking.suggestions` and `session-tracking.pending-commits` to receive notifications on incoming proposals and pending convenience symlinks.
4. **Announce Presence:**
   - On startup/resume, publish an announcement to the mission announce channel:
     ```
     agentbus_publish(topic="mission.<mission-name>", from_session="<slug>", kind="announce",
       body="session=<slug> role=<role> online. in=<my-in-topic> out=<my-out-topic>")
     ```

## Status & Progress Notifications (`user.in`)

Any session may publish non-blocking status updates or operational notes to the human user:
```
agentbus_publish(topic="user.in", from_session="<slug>", kind="note",
  body="<progress or status update>")
```
*Note: Publishing to `user.in` is for background visibility and does NOT replace normal interactive session chat responses.*
