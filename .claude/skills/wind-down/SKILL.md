---
name: wind-down
description: Wind down work on a mission before the session ends — reach a safe stopping point, stop any of this session's own background agents, append this session's own ledger entry, update the mission's STATE.md if warranted, commit uncommitted work, run ledger-capture on this session's own ledger (waiting for it), mark this session's Session-log entry retired, then report "safe to close." Use when the user says they're done for now, wants to close the session safely, or asks to wind down / wrap up / stop for the day. Invoke with /wind-down.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->

# Wind down

This skill's job is to leave the mission in a state a fresh (or resuming) session can safely
pick up from — not to finish the mission. Skipping steps under time pressure is fine (noted
per-step below); skipping the report at the end is not — always tell the user the actual
outcome, including if something couldn't be done.

## Step 1: Reach a safe stopping point

Finish whatever you're actively doing to a point that isn't mid-edit or half-applied — this
doesn't mean finishing the whole task, just not leaving a file half-written or a multi-step
operation partially done. Stop any background agents *this session* launched that are still
running and not needed further (`TaskStop`) — don't leave orphaned work running that nothing
will collect.

This step cannot be skipped — winding down mid-edit defeats the purpose.

## Step 2: Append this session's own ledger entry

You should already have a live ledger scratch file at
`<feature-worktree>/.session/<this-session's-slug>.md` (started when you began, per
`CONVENTIONS.md`'s "The live ledger during a session" section, or by `/resume-mission` if that
skill started you). If you don't have one yet, create it now — better late than never.

Append (don't rewrite) an entry covering this session's work since the last checkpoint:
findings, decisions, corrections, false starts. Be honest about what didn't land, not just
what did — a false start recorded is as valuable as a task completed (per `CONVENTIONS.md`).

Skippable if genuinely short on time, but skipping this is the biggest loss — it's the one
thing ledger-capture (Step 5) needs to have something to work from.

## Step 3: Update STATE.md if warranted

If anything you did changes the mission's actual state — a task's status, the immediate next
step, a new open question, the set of worktrees in use — update `$MISSION/STATE.md` now, via
the `.wip` protocol (`CONVENTIONS.md` has the mechanics; same shape as `/resume-mission`'s
Step 7). Don't touch it if nothing changed — `STATE.md` reflects current state, it isn't a
log of activity (that's what the ledger and Session log are for).

Skippable if short on time — an out-of-date `STATE.md` will surface as a discrepancy the next
`/resume-mission` catches and can correct, or ledger-capture (Step 5) may catch it directly
from your ledger entry.

## Step 4: Commit uncommitted work

Check `git status` in whichever worktree(s) you touched. Commit anything real (code, docs)
that isn't already committed — following whatever commit-scoping convention applies there
(one task per commit for code changes, per `CONVENTIONS.md`'s coder-orchestration section).

This step can simply fail to complete — the machine sleeps, the terminal closes, whatever —
and that's not a problem: uncommitted work is either still there next time (recoverable) or,
worst case, lost work that a future session will notice via `git status`/`git diff` and can
decide how to handle. Don't treat a failure here as blocking the rest of wind-down.

## Step 5: Run ledger-capture on this session's own ledger

From `$TRACKING` (the `session-tracking` worktree — exit/re-enter if you're pinned elsewhere,
asking the user first since that needs their authorization): launch ledger-capture (see
`CONVENTIONS.md`'s "ledger-capture" section for its full contract) against this session's own
ledger file — the one named in your Session-log entry (Step 6). **Wait for it in the
foreground.** This is what makes "safe to close" in Step 7 a real guarantee rather than a
guess: don't report safety before this has actually finished.

If you're genuinely out of time and cannot wait for this to finish, do not report "safe to
close" — instead tell the user directly that wind-down is incomplete, your Session-log entry
will stay `active`, and the next `/resume-mission` will pick up the unfinished capture step as
part of its normal pending-session scan (this is exactly the safety net that mechanism exists
for — no special handling needed here beyond being honest about it).

## Step 6: Mark this session's Session-log entry retired

Once ledger-capture (Step 5) has actually finished and appended its `## Verified` marker:
update your own entry in `STATE.md`'s Session log from `status=active` to `status=retired`,
via the same `.wip` protocol. This can be the same `.wip` claim/edit/release cycle as Step 3's
`STATE.md` update if that step ran — no need for two separate round-trips.

## Step 7: Report

Tell the user plainly what happened — which steps completed, which were skipped and why, and
whether it's actually safe to close:
- If Steps 2–6 all completed: "Safe to close — ledger captured and verified, state committed."
- If some steps were skipped (time pressure) but Step 1 (safe stopping point) held: say exactly
  what was skipped and that the next `/resume-mission` will pick up the rest.
- If Step 5 didn't finish: say so explicitly — this session's entry is still `active`, not
  `retired`, and that's fine; don't claim "safe to close" when it isn't actually verified yet.
