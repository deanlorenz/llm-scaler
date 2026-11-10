---
name: ledger-capture
description: Use when asked to capture, verify, or process one session ledger so its decisions, corrections, rules, and unresolved issues are preserved in the mission's STATE.md or spec. Run by resume-mission, wind-down, or a task file that names one ledger.
---

# ledger-capture

Detect drift between the session as recorded in one ledger and the mission's persisted files (STATE.md and the spec). Capture what is missing, then mark the ledger verified.

You run in your own context. You do not own the mission. Do only what is written here and in your task file, and follow the conventions listed under Read first.

## Inputs

Your task file or prompt gives you:
- `LEDGER`: the ledger file to process.
- `MISSION`: the mission name.
- `In:` and `Out:` agentbus channels.

If any input is missing or unclear, publish a question on `Out:` and stop.

## Before work

1. Subscribe to your `In:` channel and stay subscribed until you finish.
2. Answer any progress, clarification, or interim-result request from `In:` on `Out:`.
3. Publish status, findings, questions, and completion to `Out:`.

## Read first

The conventions are in the `session-tracking` worktree, under `worktrees/session-tracking/`. Read these before any edit:
- `worktrees/session-tracking/CONVENTIONS.md`: ownership and data-safety rules.
- `worktrees/session-tracking/claude-skills/conv-state-vs-ledger/SKILL.md`: what belongs in STATE, the spec, and the ledger, and the settled-or-open rule.
- `worktrees/session-tracking/claude-skills/conv-wip-editing/SKILL.md`: the `.wip` claim, edit, release protocol.

If one of them cannot be read, publish the failure on `Out:` and stop.

## Rules

- Write only to: the mission's `.session/STATE.md`, the mission's spec in `.session/`, and the end of `LEDGER`.
- Create no files anywhere: not in `.session/`, not in the suggestion box, not a draft file.
- Never write to `CONVENTIONS.md`, `conventions/`, or `claude-skills/`.
- Change `LEDGER` only by appending the `## Verified` section at its end.
- Edit `STATE.md` and the spec only with `conv-wip-editing` (claim with `mv`, edit, release). If a `.wip` lock already exists, stop and report.
- Do not change the Session log, the task checklist, or the Last completed and Next step fields, except to add a fact that is missing.
- Commit your own edits as `conv-wip-editing` says, adding only the files you edited. Never push, and run no other git write.
- Never move, rename, or delete a file.
- Do not paste transcript. Each entry you add is short and self-contained.

## Steps

1. Read `LEDGER`. If it contains a `## Verified` heading, treat everything before the last one as already captured and process only what follows. With no such heading, process the whole ledger.
2. Read the mission's `.session/STATE.md` and its spec.
3. For each correction, decision, rule, safety requirement, and unresolved issue in the unprocessed part, decide where it belongs using `conv-state-vs-ledger`, or whether it is already present in STATE or the spec. Check especially for ownership, authorization, and data-preservation rules.
   If a later ledger entry corrects or reverses an earlier one, apply only the final state.
4. A simple progress note or a short settled fact: add it to STATE or the spec, in the section `conv-state-vs-ledger` puts it. Change nothing for a point that is already present.
5. Anything larger, or needing the mission owner's reasoning or decision: do not decide it. Flag it in the report. If you have proposed wording that is too long for the report, add it in the correct destination (STATE or the spec), in the right section, under a line `DRAFT (ledger-capture <date>) — owner to review:`, and name the file and section in the report.
6. A finding that looks like a rule for every mission: flag it in the report. Do not write it anywhere. The mission owner decides whether to propose it.
7. Append to the end of `LEDGER`:

   ```markdown
   ## Verified YYYY-MM-DD — <all points already captured | folded in: short summary>

   | Ledger point | Durable destination | Action taken |
   |---|---|---|
   | <point> | <file and section> | <None needed / Added to X / Folded into Y> |
   ```

## Report

Publish the report to `Out:` and return it. Keep it short and put no large text in it.
- Nothing new: `Verified — no gaps, nothing added`, plus the ledger path. No summary of the ledger.
- Otherwise:
  1. Changes made: one line per gap, `gap -> what was added (file, section)`.
  2. Flagged for the mission owner: anything bigger, or needing a decision or reasoning, one line each. Include a draft's file and section if you wrote one, and a sentence or two of proposed wording only when it is that short.
