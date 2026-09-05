# agentbus — Background Agent User Interaction

Read this when running as a background agent or subtask that needs to ask questions or interact directly with the human user.

## Scope & Boundaries

- **Background Agents Only:** Applies exclusively to background agents, headless workers, or ephemeral subtasks that have no direct interactive chat channel with the user.
- **Foreground (FG) Sessions:** FG sessions talk to the user directly in chat and use standard interactive prompts (e.g. `AskUserQuestion`). Do not route standard FG chat turns through agentbus dialogue.
- **Parent-Governed Decisions:** If a question fundamentally changes task scope, plan architecture, or requires parent coordination, notify the parent session rather than asking the user directly.

## Dialogue Process Pre-Check

Interactive user dialogue requires the `agentbus-dialogue` terminal process to be running.
Before initiating user questions:
1. Verify `agentbus-relay` service is active (`systemctl --user is-active agentbus-relay`).
2. If uncertain whether `agentbus-dialogue` is open, publish an initial note to `user.in` or check recent responses on `user.out`.
3. If no response arrives or dialogue is unavailable, surface a clear warning to the parent session / log.

## Asking the User (`agentbus_ask_user`)

### 1. Sync Mode (Blocking — wait for user reply)
Use when the background agent cannot proceed without an immediate answer:
```
agentbus_ask_user(
  prompt          = "<clear, specific question>",
  from_session    = "<slug>",
  timeout_seconds = 300,
  refs            = ["<repo-root-relative-path>"]
)
```
Returns: `{ "reply": "...", "seq": <n>, "from": { "agent": "human", "session": "dean" } }`.

### 2. Async Mode (Non-blocking — publish and continue)
Use when the background agent can continue working while awaiting user feedback:
```
agentbus_ask_user(
  prompt       = "<question>",
  from_session = "<slug>",
  async        = true
)
```
Returns: `{ "seq": <questionSeq> }`. The PostToolBatch hook automatically surfaces the reply when the user responds.

### 3. Re-ask / Wait (Block on a prior async question)
When the background agent reaches a point where it can no longer proceed without the answer to an earlier async question:
```
agentbus_ask_user(
  from_session    = "<slug>",
  previous_seq    = <questionSeq>,
  timeout_seconds = 300
)
```
Fetches the original question, re-publishes with a `[reminder]` prefix in the terminal, and blocks until answered.
