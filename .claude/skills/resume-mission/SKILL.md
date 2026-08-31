---
name: resume-mission
description: Resume work on a mission tracked in the session-tracking branch — whether this is a freshly-started session with no prior context, or a resumed session that should re-verify its mission/state rather than trust stale context. Scans for any pending (unfinished handoff) session and runs ledger-capture on it first, enters the correct mission worktree, reads the mission's plan and current state, declares ownership on agentbus, confirms understanding back to the user, and records this session's start in STATE.md. Use when the user asks to continue/resume/pick up work on a topic or mission, or names a mission directly. Invoke with /resume-mission [mission-name-or-topic-words] or /resume-mission with no argument to list missions and ask.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->

# Resume mission

**Arguments:** $ARGUMENTS (a mission name, topic words to match against mission names, or empty)

This skill works the same way whether you're a freshly-started session with zero context, or
a resumed session whose conversation history might be stale — always re-verify against the
files below rather than trusting memory of a prior turn.

## Step 1: Locate the tracking worktree and mission worktree

```bash
git worktree list | grep session-tracking
git worktree list
```

If `session-tracking` isn't listed, tell the user it doesn't exist locally and stop — this
skill cannot create it. Note its path as `$TRACKING`.

## Step 2: Find the mission

```bash
ls "$TRACKING/missions/"
```

- If `$ARGUMENTS` exactly matches a directory name under `missions/`, use it.
- If `$ARGUMENTS` is non-empty but doesn't match exactly, fuzzy-match against the directory
  names. If exactly one is a clear match, use it. If multiple are plausible or none match,
  list candidates and use `AskUserQuestion` to have the user pick — don't guess silently.
- If `$ARGUMENTS` is empty, list all mission directories and ask the user which one.

Call the chosen mission `$MISSION_NAME`. The mission's worktree path is
`worktrees/$MISSION_NAME` (branch name = worktree name). Call it `$MISSION_WT`.
The mission's tracking files live at `$MISSION_WT/.session/`.

## Step 3: Migrate to the new layout if needed

Check whether the mission worktree is on the new layout or the old one:

```bash
ls "$MISSION_WT/.session/" 2>/dev/null || echo "MISSING"
```

**If `.session/` exists and contains `STATE.md`:** already on the new layout — skip this step.

**If `.session/` is missing or `STATE.md` is not in it:** the worktree is on the old layout
(files in `session-tracking/missions/$MISSION_NAME/`). Migrate now before proceeding:

```bash
mkdir -p "$MISSION_WT/.session"
# Copy STATE.md and any ledger files from session-tracking into .session/
cp "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
cp "$TRACKING/missions/$MISSION_NAME/ledgers/"*.md "$MISSION_WT/.session/" 2>/dev/null || true
# Copy any spec/plan docs that are internal (not destined for a PR)
# — leave shareable docs in the code tree, only move tracking files
```

Some worktrees may already have a partial `.session/` with some ledgers or a `STATE.md`
already in it (from earlier work in the new layout). In that case: check what's already
there, keep the newer/more complete version of each file, and don't overwrite blindly.

After copying, do the one-time `.gitignore` and symlink setup per
`conventions/feature-worktree-setup.md`'s "Migrating an existing worktree" section, then
continue with Step 4 using `$MISSION_WT/.session/STATE.md` as the authoritative source.

## Step 4: Read conventions and current state

Read, in this order:
1. `$TRACKING/CONVENTIONS.md` — global process rules.
2. `$MISSION_WT/.session/STATE.md` — current status, task table, worktrees in use, immediate
   next step, open questions, and the Session log (see Step 5).
   If the worktree is not checked out locally, read via:
   ```bash
   git -C <repo-root> show $MISSION_NAME:.session/STATE.md
   ```
   The symlink at `$TRACKING/missions/$MISSION_NAME/STATE.md` points to the same file if the
   worktree is present — either path works.

Do **not** read the full ledger file(s) yet — Step 5 may need to run ledger-capture first.
Once Step 5 clears, skim the plan/spec doc named in `STATE.md` for the task list and current
status lines.

## Step 5: Clear pending sessions before proceeding

Scan `STATE.md`'s Session log section (create it, at the end of the file, if it doesn't exist
yet). A log entry is **pending** if:
- its `status` is `active` (a prior session didn't retire cleanly), or
- its `status` is `retired` but its named ledger file does **not** yet carry a `## Verified`
  marker.

For every pending entry, in order:
1. If still `active`, mark it `retired` in `STATE.md` now (via the `.wip` protocol — see
   `conventions/wip-editing.md`; since `STATE.md` is local in the mission worktree, no
   cross-worktree dance is needed).
2. Launch ledger-capture against that entry's ledger file (at `$MISSION_WT/.session/<slug>.md`),
   as a background agent, and **wait for it in the foreground** before moving on. Brief:
   read the one ledger file named, confirm every point lands in `STATE.md`, the mission's
   plan/spec doc, or (if genuinely global) `CONVENTIONS.md`; fix gaps directly via `.wip`;
   also fix any doc-reference paths in scope that violate the path convention; append
   `## Verified <date> — ...` to the ledger when done.
3. If ledger-capture reports something genuinely ambiguous, surface it to the user now.

If there are no pending entries, this step is a no-op.

## Step 6: Enter the mission worktree

```
EnterWorktree(path: "<full path to $MISSION_WT>")
```

This requires the user's interactive approval — expected, not an error. If the user declines
or the path doesn't exist, stop and ask what to do.

**Check skills are present.** From inside the now-entered worktree:
```bash
ls .claude/skills/
```
If `resume-mission` and `wind-down` symlinks are missing, set them up now per
`conventions/feature-worktree-setup.md` so the next resume/wind-down cycle works.

## Step 7: Declare ownership on agentbus

```
agentbus_publish(topic="mission.$MISSION_NAME", kind="handoff",
  body="session=<this-session-slug> taking ownership of $MISSION_NAME")
```

This makes ownership visible to any other session watching the bus. Do this before recording
the `active` Session-log entry — the bus declaration comes first.

## Step 8: Confirm mission and state back to the user

State back concisely (a few sentences plus a short status table if useful):
- Which mission, and its one-line goal (from `STATE.md`).
- Current task status (done / in progress / blocked / not started) — cite the actual table.
- The immediate next step `STATE.md` names.
- Any open question blocking full completion.
- If Step 4 found and cleared any pending sessions, say so briefly (one line).

If anything in `STATE.md` looks stale (e.g. it claims a task is "in progress" but `git log`
shows it's committed), verify against the actual git state before proceeding.

## Step 9: Record this session's start in STATE.md

`STATE.md` is already local in the mission worktree — no cross-worktree exit/re-enter needed.
Using the `.wip` protocol (`conventions/wip-editing.md`):

1. Rename `$MISSION_WT/.session/STATE.md` → `STATE.md.wip`.
2. Append one line to it:
   `- <date/time> session=<slug> status=active ledger=.session/<slug>.md owner=<this-session-id>`
3. Rename `STATE.md.wip` back to `STATE.md`, `git add`, commit on the mission branch with a
   short message like `docs(state): record session start — $MISSION_NAME`.
4. Start (or note the existing) live ledger scratch file at `$MISSION_WT/.session/<slug>.md` —
   append to it as you work throughout this session.

If the `.wip` file already exists, someone else is mid-edit — wait, or tell the user it's
locked and ask how to proceed.

## Step 10: Proceed

Continue with whatever the user actually asked for next. Keep appending to your live ledger
at `$MISSION_WT/.session/<slug>.md` as you go. Push the mission branch to `origin` at any
natural checkpoint to durably persist ledger + state updates.

<!-- user-approved-settings-change: marker retained per prior edit's convention -->
