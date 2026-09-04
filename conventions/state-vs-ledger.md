# `STATE.md` vs. a session ledger

Read this when creating initial state or ledger files for a session, or when unsure which one
a piece of information belongs in.

- **`STATE.md` / session STATE file** is what a session reads to orient itself and what it
  updates as work progresses. Self-contained: mission, role, task, current status, next step.
  Written before the session starts (by the parent or mission owner); updated in place as
  status changes. The session log section is append-only.

- **The ledger** is the append-only log for one session — findings, decisions, corrections,
  false starts, as they happen. One file per session. A resuming session normally never reads
  it; it is consulted later on demand (to recover a detail, investigate an incident, or run
  ledger-capture).

- **Rule of thumb:** if a finding changes what a resuming session needs to know or do, its
  conclusion goes into STATE (short). The full story stays only in the ledger (long).
  Update the ledger continuously during the session, not only at the end.

## Unified STATE / task file template

One template for all session types (mission owner, coder, reviewer, researcher). Level of
detail differs per role; fields do not. For field authoring guidance see `conventions/tasks.md`.

```markdown
# <Name: session slug or mission name>

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **What / goal / mission:** <what this session or mission is for>
- **Worktree:** `worktrees/<name>` (branch `<branch>`)
- **Role / scope:** <role and authority boundary>
- **Ledger / log:** `.session/<slug>.md`

## Task

- **Plan / spec:** `<path to plan doc, spec, or task file>`
- **Context / refs:** <extra orientation reads, related docs — one per line>
- **Expected output:** <file, code, review, report, …>
- **Done / completion criteria:** <checkable claims — "X exists, verified by Y">
- **Limits:** <what not to change / keep as-is / state to preserve>
- **Extra rules / rule refs:** <optional — additional conventions files to read>

## Execution

### Steps / subtasks
- [ ] <step>
- [ ] <step>

**Last completed:** <step id or description, or "none">

**Next step / resume point:** <exact next action — on interactive sessions, confirm with
user before executing; do not auto-run>

### Status
<Coders use: NOT STARTED | IN PROGRESS — <what's left> | DONE <date> | BLOCKED on <thing>>
<Mission owners use: free-form list of items with current state>

### Known issues
<optional>

## Session log
- <date> ledger=.session/<slug>.md status=<active|retired>
```
