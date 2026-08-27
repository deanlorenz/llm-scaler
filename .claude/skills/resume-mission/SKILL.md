---
name: resume-mission
description: Resume work on a mission tracked in the session-tracking branch — whether this is a freshly-started session with no prior context, or a resumed session that should re-verify its mission/state rather than trust stale context. Scans for any pending (unfinished handoff) session and runs ledger-capture on it first, enters the correct feature worktree, reads the mission's plan and current state, confirms understanding back to the user, and records this session's start in the shared STATE.md. Use when the user asks to continue/resume/pick up work on a topic or mission, or names a mission directly. Invoke with /resume-mission [mission-name-or-topic-words] or /resume-mission with no argument to list missions and ask.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->

# Resume mission

**Arguments:** $ARGUMENTS (a mission name, topic words to match against mission names, or empty)

This skill works the same way whether you're a freshly-started session with zero context, or
a resumed session whose conversation history might be stale — always re-verify against the
files below rather than trusting memory of a prior turn.

## Step 1: Locate the tracking worktree

```bash
git worktree list | grep session-tracking
```

If it's not listed, tell the user the `session-tracking` worktree doesn't exist locally on
this machine and stop — this skill cannot create it (that's a one-time setup step, not part of
resuming). If it exists, note its path — call it `$TRACKING`.

## Step 2: Find the mission

```bash
ls "$TRACKING/missions/"
```

- If `$ARGUMENTS` exactly matches a directory name, use it.
- If `$ARGUMENTS` is non-empty but doesn't match exactly, fuzzy-match it against the directory
  names (word overlap, substrings). If exactly one is a clear match, use it. If multiple are
  plausible or none are, list the candidates and use `AskUserQuestion` to have the user pick —
  don't guess silently.
- If `$ARGUMENTS` is empty, list all mission directories and ask the user which one.

Call the chosen mission's directory `$MISSION` (e.g. `$TRACKING/missions/analyzer-optimizer-refactor`).

## Step 3: Read conventions and current state

Read, in this order:
1. `$TRACKING/CONVENTIONS.md` — global process rules: the `.wip` editing protocol, the
   Session-log format and ledger-capture contract (both below depend on this), the doc-
   reference path convention, the task template, the coder-orchestration pattern.
2. `$MISSION/STATE.md` — current status: task table, worktrees in use, immediate next step,
   open questions, and the Session log (see Step 4).

Do **not** read the full ledger file(s) yet, and don't read the mission's plan/spec doc in
full yet either — Step 4 may need to run ledger-capture first, and that should happen before
you invest in reading everything else. Once Step 4 clears, come back and skim the plan/spec
doc `STATE.md` names (usually a `spec-*.md` file in the same directory) for the task list and
each task's current status line — don't restate its content back verbatim; that doc is the
source of truth, not something to summarize into chat.

## Step 4: Clear pending sessions before proceeding

Scan `STATE.md`'s Session log section (create it, at the end of the file, if it doesn't exist
yet — this may be the first resume/wind-down cycle for this mission). A log entry is
**pending** if:
- its `status` is `active` (nothing should still be marked active if you're starting fresh —
  this means a prior session didn't retire itself, whether by crash, sleep, force-quit, or
  simply never having run `/wind-down`), or
- its `status` is `retired` but its named ledger file does **not** yet carry a `## Verified`
  marker (a clean handoff whose capture step hasn't run yet, or ran and failed partway).

For every pending entry, in order:
1. If it's still `active`, mark it `retired` in `STATE.md` now (via the `.wip` protocol —
   Step 6 below has the mechanics; do this as its own small edit, don't batch it with your own
   entry).
2. Launch ledger-capture (see `CONVENTIONS.md`'s "ledger-capture" section for its full
   contract) against that entry's ledger file, as a background agent, and **wait for it in the
   foreground** before moving to the next pending entry or to Step 5. Give it the same brief
   `CONVENTIONS.md` gives: read the one ledger file named, confirm every point lands in
   `STATE.md`, the mission's plan/spec doc, or (if genuinely global) `CONVENTIONS.md`; fix
   gaps directly via `.wip`; also fix any doc-reference paths in scope of what it's already
   touching that violate the path convention; append `## Verified <date> — ...` to the ledger
   when done.
3. If ledger-capture reports something genuinely ambiguous it couldn't resolve, surface that
   to the user now rather than silently accepting or discarding it.

If there are no pending entries, this step is a no-op — proceed directly to Step 5.

## Step 5: Enter the feature worktree

`STATE.md`'s "Worktrees used" section names the feature-work worktree (e.g.
`worktrees/single-analyzer`) where this mission's actual code changes land — distinct from
`$TRACKING` itself. Enter it:

```
EnterWorktree(path: "<the feature worktree's full path from STATE.md>")
```

This requires the user's interactive approval — that's expected, not an error. If the user
declines or the path doesn't exist, stop and ask what to do rather than guessing an
alternative.

**Note for later in the session:** once inside this worktree via `EnterWorktree`, you are
structurally blocked from writing to `$TRACKING` (or anywhere else) without exiting and
re-entering, which itself needs fresh per-call authorization each time — it is not a free
round-trip. See `CONVENTIONS.md`'s "Reaching this worktree from a pinned session" section
before planning any mid-session edit to shared state.

**Check this worktree can see `/resume-mission` and `/wind-down` for next time.** Project-skill
discovery does not walk up past a worktree's own root (`CONVENTIONS.md`'s "Making
`/resume-mission` and `/wind-down` available in a feature worktree" section) — if this feature
worktree's `.claude/skills/` doesn't already have symlinks to `$TRACKING`'s copies, a future
session here won't find these skills at all. A quick `ls .claude/skills/` (from inside the
now-entered feature worktree) shows whether they're already set up; if not, set them up now
per that `CONVENTIONS.md` section so the *next* resume/wind-down cycle in this worktree works
without needing to rediscover this gap.

<!-- user-approved-settings-change: marker retained per prior edit's convention -->


## Step 6: Confirm mission and state back to the user

Now read the mission's plan/spec doc per Step 3. Before doing any actual task work, state back
concisely (not a wall of text — a few sentences plus a short status table if useful):
- Which mission, and its one-line goal (from `STATE.md`).
- Current task status (done / in progress / blocked / not started) — cite the actual table,
  don't paraphrase loosely.
- The immediate next step `STATE.md` names.
- Any open question blocking full completion.
- If Step 4 found and cleared any pending sessions, say so briefly (one line: how many, what
  ledger-capture found, if anything notable).

If anything in `STATE.md` looks stale (e.g. it claims a task is "in progress" but a `git log`
in the feature worktree shows it's already committed), say so and verify against the feature
worktree's actual git state before proceeding — `STATE.md` is a snapshot, not ground truth by
itself.

## Step 7: Record this session's start in STATE.md

Append to `STATE.md`'s Session log, following `CONVENTIONS.md`'s `.wip` protocol:

1. From `$TRACKING` (exit/re-enter if currently pinned elsewhere — ask the user first per Step
   5's note): rename `STATE.md` → `STATE.md.wip`.
2. Copy `STATE.md.wip` to a local scratch path in the feature worktree, append one line there:
   `- <date/time> session=<a short id/slug for this session> status=active
   ledger=ledgers/<same-slug>.md`. Also start (or note the existing) local live ledger scratch
   file at `<feature-worktree>/.session/<same-slug>.md` per `CONVENTIONS.md`'s "The live
   ledger during a session" section — you'll append to it as you work, and it becomes the file
   named in this log entry.
3. Copy the edited `STATE.md` back over `STATE.md.wip` in `$TRACKING`, rename back to
   `STATE.md`, commit in `$TRACKING` with a short message like `docs(state): record session
   start — <mission>`.

If the `.wip` file already exists when you go to claim it (someone else is mid-edit), don't
force past it — wait, or tell the user it's locked and ask how to proceed.

## Step 8: Proceed

Continue with whatever the user actually asked for next — this skill's job ends at "confirmed
and recorded," not at doing the mission's next task automatically. Keep appending to your own
live ledger scratch file as you go (per `CONVENTIONS.md`) — you don't need to touch
`$TRACKING` again until you next checkpoint or run `/wind-down`.
