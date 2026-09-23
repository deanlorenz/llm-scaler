---
name: conv-tasks
description: Use when writing or assigning a task specification to any worker (coder, reviewer, researcher, or delegated session).
---

# conv-tasks

Runs in your own session (orchestrator context). Apply before writing a task file or STATE for any worker.

**Templates:**
- `conventions/task_file_template.md` — full task file field set; use this when writing a worker's STATE/task file.
- `conventions/mission_spec_template.md` — mission spec/roadmap structure; use this when creating a mission-level plan doc.
- `conventions/state_template.md` — unified STATE template for all session types (mission owner, coder, reviewer, researcher).

## Who writes, who reads

Mission owner (or user) writes task specs. Workers read their STATE file. Copy and refine task specs from the plan into a STATE file placed in the worker's worktree before invocation.

## Key field rules

Full field definitions are in `conventions/task_file_template.md`. Critical rules:

- `In:` / `Out:` — agentbus channels; required for every background invocation.
- `Startup verification instructions:` — never leave blank.
- `Role / scope:` — explicit authority boundary; do not leave to inference.
- `Plan / spec:` — workers pull on demand, never read upfront.
- `Context:` — only files genuinely needed; keep short.
- `Extra rules / rule refs:` — when delegating to a subagent that will edit files it doesn't own, cite `conv-wip-editing` and name the files. The delegator does not need to read `conv-wip-editing` itself unless also editing directly.
- `Done / completion criteria:` — checkable claims; workers report actual results, not "passed".
- `Status:` — `NOT STARTED` before invocation.

## Authoring rules

**Commands must have predictable effects.** If you need an `if` branch, you don't know the state — fix that first, or state the goal and let the worker choose mechanics.

**Narrowest command.** Prefer minimal, reversible commands. A wrong command in a task file is worse than one run interactively — a compliant worker may execute it anyway.

**Report actual results, not pass/fail.** Done criteria must be checkable. Workers report the actual result against each criterion. If a conflict occurs: publish the exact file and hunk to `Out:` and stop. Do not improvise a resolution.

**Design-validation checkpoint.** Before writing any implementation, a coder proposes its code-level design (types, function boundaries, key constraints) inside the task file, then stops. The coder does not proceed to implementation until the mission owner (and, when escalated, the user) explicitly approves. Hard stop, not advisory.

## Delivery

- Include `In:` and `Out:` in every background invocation.
- Include the input subscription command in the task file or launch prompt.
- Do not launch a subagent without both channels.
- The STATE file must be complete and committed before invocation.

## Invoke `conv-coder-orchestration` next

After writing the task file, invoke `conv-coder-orchestration` before launching any coder worker.
