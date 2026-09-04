# Starting a session

Read this at the start of every session, before any work.

## Reading rules — upfront

Read at session start:
- Your STATE file (or session STATE file if provided)
- `CONVENTIONS.md` at the path in your STATE file
- Any situational rules triggered by your role (listed in `CONVENTIONS.md` index)

**Never read at session start:**
- Plan/spec docs (listed in STATE under `Plan/spec`) — pull on demand only
- Ledger files — consulted only when debugging or digging into history
- Any file listed under `Refs` in your STATE file

## Opening orientation

Before any work, present this to the user:

```
Mission:   <mission name>
Role:      <role>
Worktree:  <worktree path>
Status:    <current status>
Last:      <last completed step>
Next:      <next step>
```

Then wait for the user to confirm before executing anything.

## If you have a STATE file

1. Read it. It contains your conventions path, mission, role, worktree, task, and next step.
2. Read `CONVENTIONS.md` at the path stated in your STATE file.
3. Create a new ledger file for this session (slug: `YYYY-MM-DD-<mission>-<N>.md`).
   Open it with:
   ```
   Continues: <path to previous ledger, if any>
   ```
4. Append a new line to the session log in STATE:
   ```
   - <date> session=<slug> status=active ledger=.session/<slug>.md
   ```
5. Read any situational rules triggered by your role (listed in `CONVENTIONS.md` index).
6. Present the opening orientation above and wait for the user to confirm.

## If you have no STATE file

You are starting a new mission. You do not have a task yet.

1. Read `worktrees/session-tracking/CONVENTIONS.md`.
2. Interact with the user to define the mission: name, worktree, goal, and your role.
3. Once the mission is defined, create `.session/STATE.md` using the template in
   `conventions/state-vs-ledger.md`. Fill in what is known; leave execution fields empty
   until the user approves the plan.
4. Ask the user for approval before doing any mission work.

## Roles and what to read per role

- **Mission owner:** read `conventions/mission-owner.md`. You own STATE, the plan, the
  branch, and integration decisions for this mission.
- **Coder:** your STATE file defines your task. Focus on expected output, done criteria,
  and limits. Do not expand scope beyond what STATE specifies.
- **Reviewer:** read the work you are assigned; record findings in your ledger; do not
  silently modify the work.
- **Researcher:** investigate the assigned question; record findings; do not expand scope.

## All sessions

- Maintain the ledger continuously — append findings, decisions, corrections, false starts
  as they happen.
- Update STATE after each major step — mark completed items `[x]`, update Last completed,
  Next step, and Status. Do not wait for wind-down.
- Never push to git or publish without explicit per-operation authorization.
- Skills (`/resume-mission`, `/wind-down`, `ledger-capture`) may be invoked as a subtask or
  subagent — they get their own context window either way, which is the point.
