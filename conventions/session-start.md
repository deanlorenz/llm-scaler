# Starting a session

Read this at the start of every session, before any work.

## If you have a STATE file

Read it now. It contains your conventions path, mission, role, worktree, task, and ledger
location. After reading:

1. Read `CONVENTIONS.md` at the path stated in your STATE file.
2. Confirm your role and the scope of work with the user.
3. Open or continue your ledger file (path in STATE). Start the ledger with:
   ```
   State: <path to your STATE file>
   Continues: <path to previous ledger, if any>
   ```
4. Read any situational rules triggered by your role (listed in `CONVENTIONS.md` index).
5. Wait for the user to confirm before executing any steps — do not auto-run.

## If you have no STATE file

You are starting a new mission. You do not have a task yet.

1. Read `worktrees/session-tracking/CONVENTIONS.md`.
2. Interact with the user to define the mission: name, worktree, goal, and your role.
3. Once the mission is defined, create `.session/STATE.md` using the template in
   `conventions/state-vs-ledger.md`. Fill in what is known; leave execution fields empty
   until the user approves the plan.
4. Ask the user for approval before doing any mission work.

## Roles and what to read per role

- **Mission owner:** read `conventions/mission-owner.md`. You own STATE, the plan, the branch,
  and integration decisions for this mission.
- **Coder:** your STATE file defines your task. Focus on expected output, done criteria, and
  limits. Do not expand scope beyond what STATE specifies.
- **Reviewer:** read the work you are assigned; record findings in your ledger; do not
  silently modify the work.
- **Researcher:** investigate the assigned question; record findings; do not expand scope.

## All sessions

- Maintain the ledger continuously — append findings, decisions, corrections, false starts as
  they happen.
- Update the Status field in STATE when it changes.
- Never push to git or publish without explicit per-operation authorization.
