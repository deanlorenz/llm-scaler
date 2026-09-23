---
name: conv-agentbus-user-interaction
description: Use when running as a background agent or headless subtask that needs to communicate with the user — including progress notes, async questions, blocking confirmations, and re-ask/wait patterns.
---

# conv-agentbus-user-interaction

Runs in your own session (inside the background agent). Apply before asking the user any question via agentbus.

## Scope

- **Background agents only.** Foreground sessions talk to the user directly in chat.
- **Parent-governed decisions.** If the question changes task scope, plan architecture, or requires parent coordination — notify the parent session on `Out:`, do not ask the user directly.

## Pre-check before asking

1. Verify `agentbus-relay` is active:
   ```bash
   systemctl --user is-active agentbus-relay
   ```
2. If uncertain whether `agentbus-dialogue` is open: publish a note to `user.in` first, or check recent responses on `user.out`.
3. If dialogue is unavailable: surface a clear warning to the parent session on `Out:` and log it. Do not block indefinitely.

## Asking the user

**Sync (blocking — cannot proceed without the answer):**
```
agentbus_ask_user(
  prompt          = "<clear, specific question>",
  from_session    = "<slug>",
  timeout_seconds = 300,
  refs            = ["<repo-root-relative-path>"]   # optional context doc
)
```
Returns: `{ "reply": "...", "seq": <n>, "from": { "agent": "human", "session": "dean" } }`

**Async (non-blocking — can continue while waiting):**
```
agentbus_ask_user(
  prompt       = "<question>",
  from_session = "<slug>",
  async        = true
)
```
Returns `{ "seq": <questionSeq> }`. The PostToolBatch hook surfaces the reply when the user responds.

**Re-ask / wait (block on a prior async question):**
```
agentbus_ask_user(
  from_session    = "<slug>",
  previous_seq    = <questionSeq>,
  timeout_seconds = 300
)
```
Re-publishes the original question with a `[reminder]` prefix and blocks until answered.
