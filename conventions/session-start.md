# Starting a session

Read this at the start of every session (interactive, delegated worker, or resuming), before any work.

## Prerequisite & Worktree Isolation Check

Before reading files or taking any task action:
1. **Verify Worktree Isolation:** Confirm current working directory is inside the designated mission worktree (`worktrees/<mission>`). Coders and workers must be strictly isolated to their assigned worktree.
2. **Verify STATE File:** Confirm local `.session/STATE.md` (or the task file provided by parent) exists and is readable.
3. **If any check fails:**
   - **Interactive session:** Stop immediately. Do not guess or perform speculative searches. Ask the user: *"Prerequisites not met. Would you like me to run `/resume-mission` to set up and enter the worktree?"*
   - **Delegated worker session:** Stop immediately and return an error block to the parent session.

## Reading rules — upfront

Read at session start:
- Your local `STATE.md` (or task file provided by parent)
- `CONVENTIONS.md` at the path stated in your STATE file
- `conventions/agentbus.md` — verify and initialize agentbus channels & subscriptions
- Any situational rules triggered by your role/mission (listed in `CONVENTIONS.md` index)

**Never read at session start:**
- Plan/spec docs (listed in STATE under `Plan/spec`) — pull on demand only when executing that specific step
- Ledger files — consulted only when debugging or digging into history
- Any file listed under `Refs` in your STATE file

## Standard Session Startup Flow

Once prerequisite checks pass:
1. Read `.session/STATE.md` (identifies mission, role, worktree, task, conventions path, agentbus channels, next step).
2. Read `CONVENTIONS.md` (at path stated in STATE).
3. Read `conventions/agentbus.md` and initialize/verify agentbus channels:
   - Subscribe to own inbox channel (`In:`)
   - If mission owner: subscribe to `Announce:` (`mission.<name>`)
   - Publish presence announcement to `Announce:` channel
4. Read situational rules triggered by role/mission:
   - Mission owner: `conventions/mission-owner.md`
   - Coder / Worker: `conventions/coder-orchestration.md`
   - `policy-writer` mission: `conventions/policy-writer.md`
5. Apply the `.wip` protocol (`conventions/wip-editing.md`) to record session start in `STATE.md`:
   - Rename `STATE.md` → `STATE.md.wip`
   - Append to Session log: `- <date> session=<slug> status=active ledger=.session/<slug>.md`
   - Rename `STATE.md.wip` → `STATE.md`, stage, and commit.
6. Open active session ledger at `.session/<slug>.md` starting with:
   ```markdown
   Continues: <path to previous ledger, if any>
   ```
7. Present the canonical orientation block and wait for confirmation before executing.

## Canonical Orientation & Context Block

Every session produces this exact canonical block:

```text
Mission:   <mission name — one-line goal>
Role:      <role: mission-owner | coder | reviewer | researcher>
Worktree:  <worktree path>
Agentbus:  in=<in-channel> out=<out-channel> announce=<announce-channel>
STATE:     <path to .session/STATE.md>
Ledger:    <path to active .session/<slug>.md>
Status:    <current status string>
Last:      <last completed step>
Next:      <immediate next action requiring confirmation>
Notes:     <pending sessions cleared, migration, or setup actions taken, if any>
```

- **Interactive session:** Output this block in chat and halt for user confirmation on `Next`.
- **Delegated worker / Subagent:** Return this block to the calling parent session. The parent must read the returned `STATE` file to establish full mission context.

## If starting a brand new mission (No STATE file exists)

If starting a new mission from scratch:
1. Follow `/resume-mission` (or `conventions/feature-worktree-setup.md`) to create the worktree, `.session/` directory, and skill symlinks.
2. Read `worktrees/session-tracking/CONVENTIONS.md`.
3. Interact with the user to define mission name, worktree, goal, role, and initial plan.
4. Create `.session/STATE.md` using the template in `conventions/state-vs-ledger.md`.
5. Ask the user for explicit plan approval before executing mission tasks.

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
