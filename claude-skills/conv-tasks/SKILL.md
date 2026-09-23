---
name: conv-tasks
description: Use when writing or assigning a task specification to any worker (coder, reviewer, researcher, or delegated session).
---

# conv-tasks

Runs in your own session (orchestrator context). Apply before writing a task file or STATE for any worker.

## Who writes, who reads

Mission owner (or user) writes task specs. Workers read their STATE file. Copy and refine task specs from the plan into a STATE file placed in the worker's worktree before invocation.

## Required fields

**Orientation fields** — the worker must orient without reading anything else first:
- `Name:` — session slug, unique and sortable
- `Conventions:` — always `worktrees/session-tracking/CONVENTIONS.md`
- `What / goal / mission:` — one or two sentences
- `Worktree / Path / Branch:` — where the worker operates
- `Startup verification instructions:` — worker's required first action; never leave blank
- `Role / scope:` — explicit authority boundary; do not leave to inference
- `Ledger / log:` — name the file before invocation; worker creates it on first write
- `In:` / `Out:` — agentbus channels; required for every subagent

**Task fields:**
- `Plan / spec:` — exact path; workers pull on demand, not upfront
- `Context:` — files the worker must read; keep short
- `Refs:` — cited files; do not read unless explicitly needed
- `Expected output:` — specific deliverable; completion must be unambiguous
- `Done / completion criteria:` — checkable claims with actual results, not "passed"
- `Limits:` — what not to change; state to preserve for resuming sessions
- `Extra rules / rule refs:` — additional `conventions/*.md` to read. When delegating to a subagent that will edit files it doesn't own, cite `conv-wip-editing` here and name the files it applies to. The delegator does not need to read `conv-wip-editing` itself unless also editing directly.

**Execution fields** (initial values; worker updates as it works):
- `Steps / subtasks:` — checklist; smallest unit worth its own status
- `Next step / resume point:` — blank initially; worker fills as it works
- `Status:` — `NOT STARTED` before invocation
- `Known issues:` — fill in known constraints before invocation

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
