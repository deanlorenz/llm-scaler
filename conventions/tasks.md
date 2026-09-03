# Task specs (when defining or receiving a task)

Read this when defining a task for any worker (coder, reviewer, researcher, or any delegated
session), or when receiving a task and verifying it is complete enough to start.

## Who writes and who reads

The mission owner writes task specs. Task owners (coder, reviewer, researcher) read them.
A mission's plan doc contains the full list of task specs. When delegating a task the mission
owner copies the relevant task spec(s) from the plan, refines them with the specific context
needed for that worker (exact file paths, line numbers, function names), and places the result
in the worker's worktree as `.session/task-<id>.md` before invocation.

A resuming worker session uses its task file to understand what it should do and where it left
off — the task file must be self-contained enough to orient a fresh session without re-reading
the full mission plan.

## Task spec template

```markdown
### <Task ID> — <short name>

**What / goal.** One or two sentences: what the task produces and why it exists.

**Where / context.**
- Worktree: `worktrees/<name>` (branch `<branch>`)
- Plan doc: `<path>`
- Key files in scope: `<file>`, `<file>`, ...

**Done / completion criteria.** Checkable claims — not "do the work" but "X exists, verified
by Y." List the tests, lint checks, or other validations that must pass.

**Limits / do not change.**
- Files, directories, or behaviors that are out of scope.
- Standing rules the worker must not override.

**Subtasks.**
- [ ] Sub-item, smallest unit worth its own status
- [ ] Sub-item

**Refs.**
- *Reads:* `<doc>`, `<doc>`
- *Writes:* `<file>`, `<ledger>`

**Status.** One of:
- `NOT STARTED`
- `IN PROGRESS — <what's left>` (see continuation below)
- `DONE <date> — <brief completion note>`
- `BLOCKED on <thing>`
```

## Field rules

- Every field is required. If any field is missing when a task is received from the user, ask
  before starting.
- Status is updated in place; completion notes accumulate and are not overwritten.
- A task with sub-tasks: one outer section in this shape, each sub-task nested in the same
  shape; the outer Subtasks list links down to the nested sections.

## Continuation (resuming a previously started task)

When `Status` is `IN PROGRESS` and a new worker session is taking over (due to failure,
restart, or handoff), the mission owner adds a continuation block before the worker resumes:

```markdown
**Continue from.**
- Last completed subtask: `<id>`
- State to preserve: <what must not be changed or discarded>
- Resume point: <file, function, line, or subtask to start from>
- Known issues: <anything the resuming session must know>
```

The mission owner fills this block — the resuming worker does not infer it.

## Alignment note

The fields in this template (what/where/done/limits) are intentionally named to align with
the session ledger header (`conventions/session-start.md`) and the mission `STATE.md` — the
same information at different levels of detail. Full field unification across these three
templates is deferred to a future pass.
