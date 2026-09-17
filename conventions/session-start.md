# Starting a session

## Reading rules — upfront

Read at session start, in this order:
1. `CONVENTIONS.md` (path in your STATE file, or the canonical session-tracking worktree)
2. This file
3. Your STATE file
4. Any situational rules triggered by your role (listed in `CONVENTIONS.md` index)

If the STATE path is missing or cannot be resolved, stop and report it.
Do not assume the mission from the current folder.

**Never read at session start:**
- Plan/spec docs — pull on demand only
- The previous session's ledger — not yours; do not read it. Create your own.
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

After the block, add one concrete sentence echoing the most relevant `CONVENTIONS.md`
constraint for the upcoming work. Example: "I see the policy-writer mission rule requires
subscribing to two agentbus channels before mission work."
Generic acknowledgements ("I have read CONVENTIONS.md") do not count.

Then wait for the user to confirm before executing anything. No other action — including
opening a ledger, committing to git, or asking a question — may precede that confirmation.

## If you have a STATE file

1. Read it. It contains your conventions path, mission, role, worktree, task, and next step.
2. Read `CONVENTIONS.md` at the path stated in your STATE file.
3. Create a new ledger at `.session/<slug>.md` (slug: `YYYY-MM-DD-<mission>-<N>.md`,
   where N is the next session number). Open it with:
   ```
   Continues: <path to previous ledger, if any>
   ```
4. Append a new line to the session log in STATE:
   ```
   - <date> session=<slug> status=active ledger=.session/<slug>.md
   ```
   Move ledger to `.session/ledger/<slug>.md` when captured and retired.
5. Read any situational rules triggered by your role (listed in `CONVENTIONS.md` index).
6. Present the opening orientation above and wait for the user to confirm.

## If you have no STATE file

1. Read `worktrees/session-tracking/CONVENTIONS.md`.
2. Interact with the user to define the mission: name, worktree, goal, and your role.
3. Once the mission is defined, create `.session/STATE.md` using the template in
   `conventions/state-vs-ledger.md`. Fill in what is known; leave execution fields empty
   until the user approves the plan.
4. Ask the user for approval before doing any mission work.

## Roles and what to read per role

- **Mission owner:** read `conventions/mission-owner.md`.
- **Coder:** your task file defines your work. Read `conventions/coder.md`.
  Maintain your own ledger; do not edit the task file.
- **Reviewer:** read `conventions/reviewer.md`.
- **Researcher:** investigate the assigned question; record findings; do not expand scope.

## When a plan is approved

When a plan is approved (`ExitPlanMode`, or an explicit "go ahead on X, Y, Z"), save it
to `.session/` immediately — before any execution begins.

## All sessions

- Maintain the ledger continuously — findings, decisions, corrections, false starts.
- Update STATE after each major step — mark `[x]`, update Last completed, Next step,
  Status. Do not wait for wind-down.
- Never push to git or publish without explicit per-operation authorization.
- Skills (`/resume-mission`, `/wind-down`, `ledger-capture`) may be invoked as a subtask
  or subagent — they get their own context window.
