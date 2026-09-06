---
name: resume-mission
description: Set up, recover, or onboard into a mission worktree. Resolves target mission, enters worktree (EnterWorktree), verifies prerequisites (.session/, skill symlinks, layout migration), clears pending sessions via ledger-capture with .wip locking, declares agentbus ownership, and executes standard session-start. Supports explicit invocation (/resume-mission <mission>), inferred missions requiring user approval, and subagent execution returning canonical context to parent.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->

# Resume mission

> **Direction:** this skill is a candidate for replacement by a custom-agent (own context,
> simple model). The steps below remain the authoritative procedure until that migration is
> done. When invoked as a Bob subtask or subagent, it already runs in its own context window.

**Arguments:** $ARGUMENTS (a mission name, topic words to match against mission names, or empty)

---

## Part 1: Session Entry Cases

Before touching files or running discovery, determine which case applies and follow its
instructions. Do not proceed to Part 2 unless Case 2 applies.

### Case 1: New mission (no STATE file)
No STATE file exists for this mission yet. Do **not** speculatively scan directories or
read files. Ask the user to define the mission before doing anything:

> *"No existing STATE found for `<mission-name>`. Shall I set up a new mission worktree?"*

Wait for explicit user confirmation, then follow `conventions/feature-worktree-setup.md`
to establish the worktree and create the initial STATE file.

### Case 2: Resume or takeover (STATE file exists)
The user ran `/resume-mission`, or the session started cold and a candidate mission is
evident from the prompt or active worktree. This covers:
- Resuming your own prior session after context reset, compaction, or clear.
- Taking over from a different prior session.

"Resume own" and "takeover" are indistinguishable without user confirmation when context
is gone — always ask first:

> *"Continuing `<mission-name>`? (last: `<last completed step from STATE>`)"*

Wait for explicit user confirmation before executing anything. Then proceed through
Part 2 (Steps 1–9).

### Case 3: Delegated worker session (coder / reviewer / researcher)
When spawned with a dedicated task file (`.session/task-<id>.md` or `STATE.coder`)
specifying mission, role, worktree, and exact task: **skip `/resume-mission` entirely**.
Follow `conventions/session-start.md` directly: verify worktree isolation (`EnterWorktree`),
read the assigned task file, and report completion back to the parent session.

---

## Part 2: Execution Procedure (Case 1 only)

### Step 1: Locate `session-tracking` (conventions access only)

```bash
git worktree list | grep session-tracking
```

If `session-tracking` isn't listed, tell the user and stop — this skill cannot create it.
Record its path as `$TRACKING`. This path is used only to access `CONVENTIONS.md` and
conventions rules. Do not read anything else from `$TRACKING` at this stage.

### Step 2: Find the mission

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
Call it `$MISSION_WT`. Tracking files live at `$MISSION_WT/.session/`.

### Step 3: Enter the mission worktree

```
EnterWorktree(path: "<full path to $MISSION_WT>")
```

This requires the user's interactive approval — expected, not an error. If the user
declines or the path doesn't exist, stop and ask what to do.

**Coders must be isolated:** coders execute inside their own dedicated worktree. Entering
any other worktree is a violation — stop and report to the parent session.

All subsequent file reads and edits happen from inside this worktree. Do not read
cross-worktree files except via explicit paths or `git -C`.

### Step 4: Verify prerequisites and migrate layout if needed

**a. Verify `.session/` exists:**

```bash
ls "$MISSION_WT/.session/" 2>/dev/null || echo "MISSING"
```

If missing: `mkdir -p "$MISSION_WT/.session"`.

**b. Verify skill symlinks:**

```bash
ls .claude/skills/resume-mission .claude/skills/wind-down
```

If missing, set up per `conventions/feature-worktree-setup.md`.

**c. Verify session-tracking convenience symlinks:**

```bash
ls "$TRACKING/missions/$MISSION_NAME/"
```

If missing or broken, create per `conventions/feature-worktree-setup.md` and publish a
note to `session-tracking.pending-commits`.

**d. Migrate old layout if needed:**

If `$MISSION_WT/.session/STATE.md` is missing but files exist in
`$TRACKING/missions/$MISSION_NAME/`:

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
3. Copy only files that do NOT already exist in `.session/`:
   ```bash
   [ ! -f "$MISSION_WT/.session/STATE.md" ] && \
     cp "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
   for f in "$TRACKING/missions/$MISSION_NAME/ledgers/"*.md; do
     [ ! -f "$MISSION_WT/.session/$(basename $f)" ] && cp "$f" "$MISSION_WT/.session/"
   done
   ```
4. Commit `.session/` to the mission branch, then continue with Step 5.

### Step 5: Read conventions and current state

Read, in this order — from inside the entered worktree:
1. `$TRACKING/CONVENTIONS.md` — global process rules.
2. Any role/mission-specific situational rules triggered by your role (per
   `CONVENTIONS.md` index).
3. `.session/STATE.md` — current status, task checklist, last completed, next step,
   open questions, and the Session log (see Step 6).

Do **not** read ledger files — consult them only when debugging history.
Do **not** read the plan/spec doc upfront — pull it on demand only when needed.

### Step 6: Clear pending sessions

Read `conventions/resume-and-handoff.md` for the full takeover protocol. Summary:

Scan `STATE.md`'s Session log. A log entry is **pending** if its `status` is `active`,
or `retired` without a `## Verified` marker in its ledger.

For every pending entry, using the `.wip` protocol (`conventions/wip-editing.md`):
1. Mark it `retired` in `STATE.md` if still `active`.
2. Launch ledger-capture against its ledger file as a background agent. Wait for it in
   the foreground before proceeding.
3. Confirm `## Verified <date>` is appended to the ledger when done.
4. If ledger-capture surfaces anything genuinely ambiguous, ask the user now.

If there are no pending entries, this step is a no-op.

### Step 7: Declare ownership on agentbus

For mission-owner and role-takeover sessions:

```
agentbus_publish(topic="mission.$MISSION_NAME", kind="handoff",
  body="session=<this-session-slug> taking ownership of $MISSION_NAME")
```

Do this before recording the `active` Session-log entry. Delegated worker sessions
(coder, reviewer, researcher) skip this step.

### Step 8: Confirm mission and state back to the user

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

### Step 9: Record this session's start in STATE.md and open new ledger

Using the `.wip` protocol (`conventions/wip-editing.md`):

1. Rename `.session/STATE.md` → `STATE.md.wip`.
2. Append one line to the Session log:
   `- <date> session=<slug> status=active ledger=.session/<slug>.md`
3. Rename `STATE.md.wip` back to `STATE.md`, `git add`, commit on the mission branch:
   `docs(state): record session start — $MISSION_NAME`
4. Create a new ledger file at `.session/<slug>.md`. Open it with:
   `Continues: <path to previous ledger, if any>`
   Append to it as you work throughout this session.

If `STATE.md.wip` already exists, someone else is mid-edit — wait, or tell the user it's
locked and ask how to proceed.

<!-- user-approved-settings-change: marker retained per prior edit's convention -->
