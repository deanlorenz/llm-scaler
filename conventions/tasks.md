# Writing and assigning tasks

Read this when writing a task spec for any worker (coder, reviewer, researcher, or any
delegated session), or when you receive a task directly from the user and need to create a
STATE file from it.

Receivers (sessions that already have a STATE file prepared for them) do not need to read
this file — their STATE file is the task.

## Who writes, who reads

The mission owner (or user) writes task specs. Workers read their STATE file. The mission
owner copies and refines task specs from the plan into a STATE file placed in the worker's
worktree before invocation.

## Field guide

Task file template: `conventions/task_file_template.md` — use this when writing a task file for any worker. The template contains the full field set with all required and optional fields.

**Key field rules:**

- `In:` / `Out:` agentbus channels are required for every background invocation.
- `Startup verification instructions:` — never leave blank.
- `Role / scope:` — be explicit; do not leave scope to inference.
- `Plan / spec:` — workers pull on demand, never read upfront.
- `Context:` — only files genuinely needed; keep short.
- `Extra rules / rule refs:` — when delegating to a subagent that will edit files it doesn't
  own, cite `conv-wip-editing` here and name the files it applies to. The delegator does not
  need to read `conv-wip-editing` itself unless also editing directly.
- `Done / completion criteria:` — checkable claims; workers report actual results, not "passed".
- `Status:` — set to `NOT STARTED` before invocation.

## Task authoring rules

### Commands must have predictable effects
Every command in a task file must have a predictable effect on the specific expected state at
the time the worker runs it. If the author needs an `if` branch — they don't know the state.
Fix: either pin the mechanics down first, or state the *goal* ("ensure X is not staged") and
let the worker choose the mechanics. Never half-specify both: a hedged command plus a hedged
conditional passes uncertainty downstream to a worker with less context, and a more compliant
worker may execute the bad command anyway because the task file said to.

### Use the narrowest command (repeat of global rule — applies especially to task files)
Prefer the minimal, reversible command. A task file's authority can substitute for a worker's
judgment — which means a wrong command in a task file is worse than a wrong command run
interactively. When a guard fires on a task-file command, the worker should substitute the
safer alternative and disclose the substitution, not override the guard.

Reference:

| Intent | Preferred command | Avoid |
|---|---|---|
| Remove from index, keep on disk | `git rm --cached -r <path>` | `git restore --staged --worktree` (deletes) |
| Remove from index and disk | `git rm -r <path>` | |
| Drop a commit from a branch | branch from the upstream base | `git reset --hard` (loses task file) |

### Don't declare something impossible without checking
Before telling the user or worker that something cannot be done, verify it against
`conventions/`. A Known-issues note in one mission's STATE is not proof the whole system
lacks a solution — the convention may already document the working pattern.

### Progress reporting — what a worker must publish
A worker running as a background agent must report progress in two ways:

1. **`Out:` channel** — publish status, findings, interim results, and completion to the
   parent session's monitoring channel. Answer any parent request on `In:` before continuing.
2. **`user.in` publish** — for non-blocking visibility to the human user (fire-and-forget;
   the worker does not wait for a reply):
   ```
   agentbus_publish(topic="user.in", from_session="<slug>", kind="note",
     body="<progress update>")
   ```

Report partial progress at natural checkpoints (e.g. after each major step), not only on
completion. If blocked or stopped early, publish the blocking reason to both channels before
exiting.

### Done criteria — report actual results, not pass/fail
Done criteria in the task file must be checkable claims. The worker reports the actual result
against each criterion — not just "passed" or "done". Examples:
- ✅ "Ran 1 of 150 Specs — `TestNilSaturationGuard` — SUCCESS" (not just "tests pass")
- ✅ "Conflict in `docs/foo.md` lines 12–18 — exact hunk: `<<<< ... >>>>`; stopped" (not "conflict found")

If there is a conflict: do not improvise a resolution. Publish the exact conflicting file and
hunk to `Out:` and stop. The parent decides the resolution.

## Continuation (handing off a partially done task)

When a task is `IN PROGRESS` and a new session is taking over, update the STATE file before
the new session starts:

- Set **Last completed** to the last finished step.
- Set **Next step / resume point** to exactly where the new session should pick up.
- Update **Limits** with any state that must be preserved.
- Add a **Known issues** entry for anything the new session must know.

The new session reads the updated STATE and starts from the resume point — it does not
re-derive context from scratch.

## Delivery

Rules:
- Include `In:` and `Out:` in every background invocation.
- Include the input subscription command in the task file or launch prompt.
- Do not launch a subagent without both channels.

Pass the STATE file path in the invocation message. The session reads it as its first action.

For a persistent CLI coder invoked from Claude, place the STATE file at
`.session/<slug>.STATE.md` in the prepared worktree and pass the path in the launch prompt.

The STATE file must be complete and committed before invocation. A session that starts with
an incomplete STATE must ask the user before proceeding.

## Mission spec / roadmap structure

Mission spec template: `conventions/mission_spec_template.md` — use this structure for
missions with extended history and multiple tasks. Includes reading rules and restructuring
guidance.
