# Worker task file template

Use this when writing a task file for any worker (coder, reviewer, researcher, delegated session).
This is the full field set. For field authoring guidance see `conventions/tasks.md`.

```
Name: <session-slug>
Conventions: worktrees/session-tracking/CONVENTIONS.md
What / goal / mission: <one or two sentences>
Worktree: <name or "ephemeral — assigned at launch">
Path: <absolute-path or "unknown — verify branch instead">
Branch: <branch-name>
Task SHA: <TASK_SHA>
In: <mission>.<id>.in
Out: <mission>.<id>
Startup verification instructions:
  <worker's required first action and what to do on failure — never leave blank>
Role / scope: <role and explicit limits>
Ledger / log: .session/<slug>.md

Plan / spec: <path>  (pull on demand — do not read upfront)
Context:
  <file — one per line; keep short>
Refs:
  <cited file — do not read unless explicitly needed>
Expected output: <specific deliverable>
Done / completion criteria:
  <checkable claim with actual result, not just pass/fail>
Limits: <what must not change>
Extra rules / rule refs:
  <optional — paths to additional conventions/*.md to read>

Steps / subtasks:
  [ ] <step>
Next step / resume point: (blank — worker fills as it works)
Status: NOT STARTED
Known issues: <optional>
```

## Agentbus channels (required for every background invocation)

- `In:` — child input channel: `<mission>.<id>.in`
- `Out:` — child output channel: `<mission>.<id>`
- Include the subscription command in the task file and launch prompt.
- Do not launch a background worker without both channels.
