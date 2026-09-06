---
name: wind-down
description: Wind down work on a mission — either a safe checkpoint (pause, stay active, no retirement) or a full retirement (end session, mark retired, release agentbus ownership). Steps 1–4 apply to both. Steps 5–7 apply only to full retirement. Always reports actual outcome. Follows conventions/resume-and-handoff.md. Invoke with /wind-down.
disable-model-invocation: true
---

<!-- user-approved-settings-change -->

# Wind down

This skill covers two modes. Determine which applies before starting:

- **Safe checkpoint** — pausing mid-session (pre-compaction, break, handoff to next context
  window). Session stays `active`. No retirement, no agentbus release.
  Run Steps 1–5. Safe to run as a background subagent when possible.
- **Full retirement** — genuinely ending this session's engagement on the mission (closing,
  handing ownership to another session). Run all Steps 1–7.

If the user's intent is unclear, ask before proceeding.

Steps 1 and 3 are never skippable in either mode. Skipping the report at the end is never
acceptable — always tell the user the actual outcome, including if something couldn't be done.

See `conventions/resume-and-handoff.md` for the ownership and ledger-capture contracts.

## Step 1: Reach a safe stopping point

Finish whatever you're actively doing to a point that isn't mid-edit or half-applied — this
doesn't mean finishing the whole task, just not leaving a file half-written or a multi-step
operation partially done. Stop any background agents *this session* launched that are still
running and not needed further (`TaskStop`) — don't leave orphaned work running that nothing
will collect.

This step cannot be skipped — winding down mid-edit defeats the purpose.

## Step 2: Final pass over the session ledger

The ledger at `<mission-worktree>/.session/<this-session-slug>.md` should have been
maintained throughout the session. This step is a safety-net pass — go back over this
session's work and confirm everything is captured: findings, decisions, corrections, false
starts. Append anything missing now.

If the ledger is missing significant work, that is a protocol violation from earlier in the
session — record it honestly here and continue. Do not skip this step on that account.

## Step 3: Update STATE.md

This step is **not skippable**. Update `<mission-worktree>/.session/STATE.md` now, via the
`.wip` protocol (`conventions/wip-editing.md`). `STATE.md` is local in the mission worktree —
no cross-worktree exit/re-enter needed.

Required every wind-down, regardless of time pressure:
- Mark completed steps `[x]` in the checklist
- Set **Last completed** to the last finished step
- Set **Next step / resume point** to exactly where the next session should pick up
- Update **Status**
- If you changed the STATE template (`conventions/state-vs-ledger.md`) or any field annotation
  this session: apply the same change to this living STATE.md in this same step — template and
  living file must stay in sync

The only part ledger-capture (Step 5) may still update afterward is findings it surfaces from
the ledger text — that is what "skippable if out of time" applies to, not the continuation
fields above.

## Step 4: Commit uncommitted work and push

Check `git status` in the mission worktree. Commit anything real (code, docs, ledger updates)
that isn't already committed. Then push the mission branch to `origin`:

```bash
git push origin <mission-name>
```

This push durably persists the ledger, `STATE.md`, and any code — the `.session/` dir is
tracked on the mission branch (excluded from PR branches, not from the mission branch itself).

This step can simply fail to complete — the machine sleeps, the terminal closes, whatever —
and that's not a problem: uncommitted work is still there next time or recoverable via git.
Don't treat a failure here as blocking the rest of wind-down.

## Step 5: Run ledger-capture on this session's own ledger

Launch ledger-capture as a **background agent** against this session's ledger file at
`<mission-worktree>/.session/<slug>.md`. Ledger-capture confirms every point in the ledger
is reflected somewhere durable and appends `## Verified <date>` when done. Only after it
completes may STATE.md be updated with its findings or references to them.

This step runs in both modes (checkpoint and retirement).

If the session closes or there is not enough time before ledger-capture finishes, this step
is effectively skipped — do not wait for it and do not block wind-down on it. The next
`/resume-mission` pending-session scan will pick up the unverified ledger and run
ledger-capture at that point.

## Steps 6–7: Full retirement only

*Skip these steps for a safe checkpoint. Session-log entry stays `active`.*

## Step 6: Mark this session's Session-log entry retired

Once ledger-capture (Step 5) has finished and appended its `## Verified` marker: update your
own entry in `STATE.md`'s Session log from `status=active` to `status=retired`, via the `.wip`
protocol. This can be combined with Step 3's `STATE.md` update — no need for two round-trips.

## Step 7: Release ownership on agentbus

```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<this-session-slug> releasing ownership of <mission-name>")
```

Do this after the Session-log entry is marked `retired` — the release confirms the session
genuinely ended, not just paused.

## Step 8: Report

Tell the user plainly what happened — which steps completed, which were skipped and why, and
whether it's actually safe to close:
- **Safe checkpoint:** "Checkpoint saved — state committed, session remains active."
- **Full retirement, all steps completed:** "Safe to close — ledger captured and verified,
  state committed, pushed to origin, ownership released."
- **Full retirement, some steps skipped:** say exactly what was skipped and that the next
  `/resume-mission` will pick up the rest.
- **Ledger-capture didn't finish:** say so explicitly — entry is still `active`, not `retired`;
  don't claim "safe to close" when it isn't verified yet.
