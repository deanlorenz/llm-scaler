# resume-mission lacks two checks the convention requires; wind-down/convention drift

**From:** policy-writer, 2026-11-11. **Type:** skill/convention gap + contradictions.

## Finding

`conventions/resume-and-handoff.md` (Resume / Takeover Protocol) requires two steps that
`claude-skills/resume-mission/SKILL.md` does not contain:

- **Live presence check** (convention step 2): check agentbus for a recent heartbeat from the
  active slug; if alive, stop and ask the user. The skill goes straight from the Case 2
  confirmation to clearing pending sessions (Step 7).
- **Ground-truth check** (convention step 7): if STATE has a checklist or file list derived
  from an external source (diff, file tree, test suite, config), regenerate it in a background
  agent and diff against STATE before starting work. Absent from the skill.

Contradictions / drift between the convention and the skills (skill is the newer text):

1. Convention step 5 says run ledger-capture "in the foreground"; resume-mission Step 7.5 says
   background agent and wait. Owner decision: fg/bg does not matter, resume always waits for
   completion.
2. Convention ledger-capture item 7 says ledger-capture writes global findings to
   `session-tracking/suggestion-box/`. The ledger-capture skill says create no files and flag
   global findings in the report. Owner decision: the main session acts on findings (writes the
   suggestion); the background agent does not.
3. `wind-down/SKILL.md` L19 points to the convention for "the ownership and ledger-capture
   contracts"; the ledger-capture contract now lives in its own skill.

## Need

Each skill must be self-contained for its main path. The convention stays the full
reference / fallback and source of truth. Duplication of the main path is acceptable.

## Proposed solution

- resume-mission: add the presence check before Step 7 and the ground-truth check after Step 7
  (before Step 9), each with its concrete command embedded. Presence check depends on the
  heartbeat suggestion (same date, 1915).
- resume-mission Step 7 and wind-down Step 5: add "when ledger-capture flags a global finding,
  the main session writes the suggestion-box file" (format in `conventions/policy-writer.md`
  "Suggestion-Box Monitoring & Processing" as fallback).
- Convention: step 5 -> "wait for completion"; ledger-capture item 7 -> flag in report, main
  session writes the suggestion.
- wind-down L19: repoint to the ledger-capture skill.
- Does not touch the ledger-maintenance changes in progress elsewhere (resume-mission Step 10
  "append to it as you work", wind-down Step 2).
