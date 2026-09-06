---
name: wind-down
description: Wind down work on a mission — safe checkpoint or full retirement. Supports explicit user invocation and agent invocation. Can run entirely in the background. Follows conventions/resume-and-handoff.md.
---

<!-- user-approved-settings-change -->

# Wind down

Rules:
- Accept `checkpoint` or `retire` as the mode.
- If no mode is supplied, ask the user when running interactively.
- If an agent invokes the skill without a user, use `checkpoint` unless the task explicitly says
  to retire.
- Safe checkpoint: keep the session `active`; do not release ownership; run Steps 1–5.
- Full retirement: run Steps 1–7; retire the session and release ownership.
- Steps 1 and 3 are mandatory in both modes.
- Agent invocations may run all steps in the background without user interaction.
- Record the outcome in the ledger and STATE; report to the parent agent when one exists.

See `conventions/resume-and-handoff.md` for the ownership and ledger-capture contracts.

## Step 1: Reach a safe stopping point

Finish whatever you're actively doing to a point that isn't mid-edit or half-applied — this
doesn't mean finishing the whole task, just not leaving a file half-written or a multi-step
operation partially done. Stop any background agents *this session* launched that are still
running and not needed further (`TaskStop`) — don't leave orphaned work running that nothing
will collect.

This step cannot be skipped — winding down mid-edit defeats the purpose.

## Step 2: Final pass over the session ledger

The active ledger at `<mission-worktree>/.session/<this-session-slug>.md` should have been
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

## Step 4: Review and commit local work; never force a push

Rules:
- Run `git status --short` and inspect the diff.
- Classify every changed and untracked path.
- Commit real mission work, documentation, STATE, and this ledger.
- Do not track credentials, generated output, scratch files, or excluded local artifacts.
- List every untracked/excluded file in the ledger.
- Do not discard unexplained changes; stop if ownership is unclear.
- Identify mission versus PR/feature branch before committing.
- Do not commit `.session/` on PR branches unless explicitly allowed.
- Review the exact staged file list before committing.
- Do not push automatically.
- Push only after single-use user authorization and reading `conventions/push.md`.
- Before an authorized push, inspect outgoing commits and the remote destination.

## Step 5: Run ledger-capture on this session's own ledger

Launch ledger-capture as a **background agent** against this session's active ledger file at
`<mission-worktree>/.session/<slug>.md`. After capture appends the verification summary and the
session is retired, move the ledger to `<mission-worktree>/.session/ledger/<slug>.md` and update
its STATE log path.

Rules:
- Assign ledger-capture an `In:` channel and an `Out:` channel before launch.
- Include both channels and the subscription command in its task file or prompt.
- Require ledger-capture to subscribe to `In:` before work and remain subscribed while running.
- Send progress, clarification, or interim-result requests to its `In:` channel.
- Require ledger-capture to answer those requests on `Out:` before continuing.
- Require ledger-capture to publish status, findings, questions, and completion to `Out:`.
- Wait for completion when running interactively.
- An agent-invoked wind-down may continue entirely in the background; record whether capture
  finished.
- Ledger-capture confirms every point in the ledger is reflected somewhere durable and appends
  `## Verified <date>` when done.
- Only after it completes may STATE.md be updated with its findings or references to them.

This step runs in both modes (checkpoint and retirement).

If the session closes or there is not enough time before ledger-capture finishes, this step
is effectively skipped — do not wait for it and do not block wind-down on it. The next
`/resume-mission` pending-session scan will pick up the unverified ledger and run
ledger-capture at that point.

## Steps 6–7: Full retirement only

*Skip these steps for a safe checkpoint. Session-log entry stays `active`.*

## Step 6: Mark this session's Session-log entry retired

Once ledger-capture (Step 5) has finished and appended its `## Verified` marker: move the active
ledger to `.session/ledger/<slug>.md`, then update its entry in `STATE.md`'s Session log from
`status=active` to `status=retired` with the new ledger path, via the `.wip` protocol. This can be
combined with Step 3's `STATE.md` update — no need for two round-trips.

## Step 7: Release ownership on agentbus

```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<this-session-slug> releasing ownership of <mission-name>")
```

Do this after the Session-log entry is marked `retired` — the release confirms the session
genuinely ended, not just paused.

## Step 8: Report

Tell the user or parent agent plainly what happened — which steps completed, which were skipped and
why, and whether it is safe to close. Distinguish clearly between committed and pushed:
- **Safe checkpoint:** "Checkpoint saved — state committed; session remains active."
- **Full retirement, local only:** "Retirement saved locally — ledger captured and verified,
  state and work committed, ownership released; no push was authorized."
- **Full retirement, pushed:** "Safe to close — ledger captured and verified, state committed,
  push authorized and completed, ownership released."
- **Full retirement, some steps skipped:** say exactly what was skipped and that the next
  `/resume-mission` will pick up the rest.
- **Ledger-capture didn't finish:** say so explicitly — entry is still `active`, not `retired`;
  don't claim safe to close when it isn't verified yet.
