---
name: resume-mission
description: Set up, recover, or onboard into a mission worktree. Resolves target mission, enters worktree (EnterWorktree), verifies prerequisites (.session/, skill symlinks, layout migration), clears pending sessions via ledger-capture with .wip locking, declares agentbus ownership, and executes standard session-start. Supports explicit invocation (/resume-mission <mission>), inferred missions requiring user approval, and subagent execution returning canonical context to parent.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->
<!-- user-approved-settings-change: adding Step 3a mission-rule gate, this same edit -->

# Resume mission

> **Direction:** this skill is a candidate for replacement by a custom-agent (own context,
> simple model). The steps below remain the authoritative procedure until that migration is
> done. When invoked as a subtask or subagent, it already runs in its own context window.

**Arguments:** $ARGUMENTS (a mission name, topic words to match against mission names, or empty)

---

## Part 1: Session Entry Cases

Rules:
- Read canonical `CONVENTIONS.md` from `session-tracking` first.
- Read `conventions/session-start.md` second.
- Never use the main repository's copy as fallback.
- Do not select a mission before reading both files.
- Do not ask to continue the task before verifying mission, role, worktree, and takeover status.

### Case 1: New mission (no STATE file)
Rules:
- Select the mission only after reading startup prerequisites.
- Verify that `<worktree>/.session/STATE.md` does not exist.
- Do not infer a new mission from a missing convenience symlink.
- Ask the user to approve setup:

> *"No `.session/STATE.md` exists for `<mission-name>` in `<worktree>`. Shall I set up a new
> mission worktree?"*

Wait for confirmation. Follow `conventions/feature-worktree-setup.md` to create STATE.

### Case 2: Resume or takeover (STATE file exists)
Rules:
- Verify the live STATE in the mission worktree.
- Confirm mission, role, worktree, branch, and session-log status.
- Treat `session-tracking/missions/` as a convenience index only.
- Present this confirmation before entering the worktree or starting the next task:

> *"I found `.session/STATE.md` for mission `<mission-name>` in `<worktree>` (branch `<branch>`).
> Role is `<role>`. The previous session is `<active|retired|pending>`; takeover means I will
> clear pending state, declare ownership, and create a new ledger. Continue with this mission
> takeover?"*

Wait for confirmation. This confirms takeover only, not permission to continue the task.
Then execute the mission procedure.

### Case 3: Delegated worker session (coder / reviewer / researcher)
When spawned with a dedicated task file (`.session/task-<id>.md` or `STATE.coder`)
specifying mission, role, worktree, and exact task: **skip `/resume-mission` entirely**.
Follow `conventions/session-start.md` directly: verify worktree isolation (`EnterWorktree`),
read the assigned task file, and report completion back to the parent session.

---

## Part 2: Execution Procedure (after Case 1 or 2 confirmation)

### Step 1: Verify startup conventions and the active worktree

Rules:
- Verify `CONVENTIONS.md` and `conventions/session-start.md` were read.
- Verify role/mission rules required by STATE were read.
- Stop if either startup file is missing.
- Do not use the main repository as fallback.
- Verify active worktree root and branch with `git worktree list`.
- Read the candidate worktree's `.session/STATE.md` directly.

### Step 2: Locate `session-tracking` (conventions access only)

```bash
git worktree list | grep session-tracking
```

If `session-tracking` isn't listed, tell the user and stop — this skill cannot create it.
Record its path as `$TRACKING`. This path is used only to access `CONVENTIONS.md` and
conventions rules. Do not read anything else from `$TRACKING` at this stage.

### Step 3: Find and verify the mission

```bash
ls "$TRACKING/missions/"
```

- **Exact match:** if `$ARGUMENTS` matches a directory name under `missions/`, use it.
- **Fuzzy match / ambiguity:**
  - Interactive session: list candidates, use `AskUserQuestion` to have the user pick.
    Do not guess silently.
  - Background / subagent: report ambiguity to the parent session via agentbus.
- **Empty arguments:** list all mission directories and ask the user which one.

Call the chosen mission `$MISSION_NAME`. The worktree path is `worktrees/$MISSION_NAME`.
Call it `$MISSION_WT`. Tracking files live at `$MISSION_WT/.session/`. Verify that
`$MISSION_WT/.session/STATE.md` exists before treating this as an existing mission. If it exists,
read its Orientation fields and perform the Case 2 confirmation. If the selected index entry points
to a missing STATE, stop and report the mismatch; do not silently create or use a STATE elsewhere.

<!-- user-approved-settings-change: Step 3a gate added this edit -->

### Step 3a: Mission-specific rule gate — MANDATORY, do not skip

This step does not rely on `CONVENTIONS.md`'s own index of situational rules, and does not
trust that reading that index is the same as having read the actual file — check directly:

```bash
test -f "$TRACKING/conventions/$MISSION_NAME.md" && echo EXISTS || echo NONE
```

- If `EXISTS`: **read `$TRACKING/conventions/$MISSION_NAME.md` in full, right now, before
  Step 4.** This is a hard gate, not an optional cross-reference — do not proceed to enter
  the worktree, confirm takeover, or do any mission work until this file has actually been
  read this session. Having read it in a prior session does not satisfy this gate; read it
  again now, every time.
- If `NONE`: no mission-specific rule file exists for this mission. Proceed.

### Step 4: Enter the mission worktree

```
EnterWorktree(path: "<full path to $MISSION_WT>")
```

This requires the user's interactive approval — expected, not an error. If the user
declines or the path doesn't exist, stop and ask what to do.

**Coders must be isolated:** coders execute inside their own dedicated worktree. Entering
any other worktree is a violation — stop and report to the parent session.

All subsequent file reads and edits happen from inside this worktree. Do not read
cross-worktree files except via explicit paths or `git -C`.

### Step 5: Verify prerequisites and migrate layout if needed

**a. Verify `.session/` exists and use the live STATE:**

```bash
ls "$MISSION_WT/.session/" 2>/dev/null || echo "MISSING"
```

If missing: `mkdir -p "$MISSION_WT/.session"`.

**b. Verify skill links resolve to the canonical source:**

```bash
for skill in resume-mission wind-down; do
  test -r ".claude/skills/$skill/SKILL.md" || { echo "BROKEN: $skill"; exit 1; }
  readlink -f ".claude/skills/$skill/SKILL.md"
done
```

Rules:
- Resolved paths must be under `$TRACKING/claude-skills/`.
- Links into the main repository or missing links are invalid.
- Repair links using `conventions/feature-worktree-setup.md`.
- Do not copy skills into mission worktrees.

**c. Verify session-tracking convenience symlinks:**

```bash
ls "$TRACKING/missions/$MISSION_NAME/"
```

If missing or broken, create per `conventions/feature-worktree-setup.md` and publish a
note to `session-tracking.pending-commits`.

**d. Migrate tracking layout and STATE if needed:**

If `$MISSION_WT/.session/STATE.md` is missing but files exist in
`$TRACKING/missions/$MISSION_NAME/`, perform the old-layout migration below. If the live STATE
already exists, skip copying and perform the template migration in step 4 against the live file.

1. List both sides before touching anything:
   ```bash
   ls -la "$MISSION_WT/.session/"
   ls "$TRACKING/missions/$MISSION_NAME/"
   ls "$TRACKING/missions/$MISSION_NAME/ledgers/"
   ```
2. For each file present in both places, diff before copying:
   ```bash
   diff "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
   ```
   Keep whichever is newer/more complete. Note any conflict in your live ledger.
3. Copy only files that do NOT already exist in `.session/ledger/`:
   ```bash
   [ ! -f "$MISSION_WT/.session/STATE.md" ] && \
      cp "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
    mkdir -p "$MISSION_WT/.session/ledger"
    for f in "$TRACKING/missions/$MISSION_NAME/ledgers/"*.md; do
      [ ! -f "$MISSION_WT/.session/ledger/$(basename $f)" ] && cp "$f" "$MISSION_WT/.session/ledger/"
   done
   ```
4. Migrate live STATE to `conventions/state-vs-ledger.md`.
   - Preserve mission content, checklist, session log, and known issues.
   - Add missing fields and the current ledger annotation.
   - Do not replace valid STATE with a blank template.
   - Record migration in the new ledger.
5. Commit changed STATE or layout before continuing.

### Step 6: Read conventions and current state

Read, in this order — from inside the entered worktree:
1. `$TRACKING/CONVENTIONS.md` — global process rules.
2. `.session/STATE.md` — current status, task checklist, last completed, next step,
   open questions, and the Session log (see Step 6).

<!-- user-approved-settings-change: removing the soft CONVENTIONS-index rule reference -->

The mission-specific rule gate already happened at Step 3a — do not re-derive it from
`CONVENTIONS.md`'s index here or anywhere else. Role-specific situational rules other than
the mission-specific one (e.g. `mission-owner.md`, `coder.md`) are still read when their own
documented trigger occurs, per their own files — not resolved from this step.

Do **not** read ledger files — consult them only when debugging history.
Do **not** read the plan/spec doc upfront — pull it on demand only when needed.

### Step 7: Clear pending sessions

Read `conventions/resume-and-handoff.md` for the full takeover protocol. Summary:

Scan `STATE.md`'s Session log. A log entry is **pending** if its `status` is `active`,
or `retired` without a `## Verified` marker in its ledger. Active ledgers remain in
`.session/`; captured retired ledgers belong in `.session/ledger/`.

For every pending entry, using the `.wip` protocol (`conventions/wip-editing.md`):
1. Mark it `retired` in `STATE.md` if still `active`.
2. Assign ledger-capture `In:` and `Out:` channels. Include both channels and the subscription
   command in its task file or prompt.
3. Require ledger-capture to subscribe to `In:` before work, remain subscribed while running, and
   answer progress, clarification, or interim-result requests on `Out:`.
4. Require ledger-capture to publish status, findings, questions, and completion to `Out:`.
5. Launch ledger-capture against its ledger file as a background agent. Send progress questions to
   its `In:` channel when needed and wait for its completion message before proceeding.
6. Confirm `## Verified <date>` is appended to the ledger.
7. If ledger-capture surfaces anything genuinely ambiguous, ask the user now.

If there are no pending entries, this step is a no-op.

### Step 8: Declare ownership on agentbus

For mission-owner and role-takeover sessions:

```
agentbus_publish(topic="mission.$MISSION_NAME", kind="handoff",
  body="session=<this-session-slug> taking ownership of $MISSION_NAME")
```

Do this before recording the `active` Session-log entry. Delegated worker sessions
(coder, reviewer, researcher) skip this step.

### Step 9: Confirm mission and state back to the user

Present the opening orientation in this fixed format, then **wait for user confirmation
before executing anything further**:

```
Mission:   <mission name — one-line goal>
Role:      <role>
Worktree:  <worktree path>
Status:    <current status>
Last:      <last completed step>
Next:      <next step>
```

Add one line if Step 6 cleared any pending sessions.

**Subagent / subtask return contract:** when running as a subagent or subtask, return
this block to the parent session. The parent reads the returned STATE file for immediate
grounded context. Do not auto-proceed — the parent decides the next action.

### Step 10: Record this session's start in STATE.md and open new ledger

Using the `.wip` protocol (`conventions/wip-editing.md`):

1. Create `.session/ledger/` if it does not exist.
2. Rename `.session/STATE.md` → `STATE.md.wip` (or stop if the lock already exists).
3. Append one line to the Session log:
   `- <date> session=<slug> status=active ledger=.session/<slug>.md`
4. Rename `STATE.md.wip` back to `STATE.md`, `git add`, commit on the mission branch:
   `docs(state): record session start — $MISSION_NAME`
5. Create a new active ledger file at `.session/<slug>.md`. Open it with:
   `Continues: <path to previous ledger, if any>`
   Append to it as you work throughout this session.

If `STATE.md.wip` already exists, someone else is mid-edit — wait, or tell the user it's
locked and ask how to proceed.

<!-- user-approved-settings-change: marker retained per prior edit's convention -->
