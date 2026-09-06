# agentbus — Session Messaging & Channel Protocol

Read this at session start to configure agentbus channels and subscriptions.

## Overview & Scope

agentbus provides persistent, asynchronous publish/subscribe messaging across sessions, roles, and worktrees.
Every session and every subagent connects to agentbus before work starts.

## Channel Naming & Definition

Each session's inbox and outbox channels are defined in its `.session/STATE.md` (or task file) under the **Orientation** section:

- **Inbox Channel (`In:`):** `<mission>.<role-or-slug>.in` (e.g. `policy-writer.owner.in`, `single-analyzer.coder-1.in`)
- **Outbox Channel (`Out:`):** `<mission>.<role-or-slug>` (e.g. `policy-writer.owner`, `single-analyzer.coder-1`)
- **Announce Channel (`Announce:`):** `mission.<mission-name>` (e.g. `mission.policy-writer`, `mission.single-analyzer`)

Rules:
- The mission owner or parent defines channels in STATE or the task file.
- Every background invocation receives its input and output channels explicitly.
- Every child subscribes to its input channel before work starts and remains subscribed while running.
- A parent sends requests and progress questions to the child's `In:` channel.
- Every child answers requests and progress questions on its `Out:` channel.
- Every child publishes status, findings, questions, and completion to its output channel.
- A parent monitors the child's output channel.
- No subagent runs without agentbus channels.

## Session Startup & Resume Sequence

1. **Verify / Establish Subscriptions:**
   - **New sessions and subagents:** subscribe to the dedicated input channel:
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

## Background invocation contract

Every background agent launch must include:

- `In:` — child input channel;
- `Out:` — child output channel;
- the channel subscription command;
- the channel names in the child task file or prompt.

The child must keep listening on `In:` until it exits. If the parent asks for progress,
clarification, or an interim result on `In:`, the child must answer on `Out:` before continuing.
The child must publish its final result to `Out:` before exiting.

## Status & Progress Notifications (`user.in`)

Any session may publish non-blocking status updates or operational notes to the human user:
```
agentbus_publish(topic="user.in", from_session="<slug>", kind="note",
  body="<progress or status update>")
```
*Note: Publishing to `user.in` is for background visibility and does NOT replace normal interactive session chat responses.*
