Continues: .session/2026-09-16-policy-writer-20.md

# Session 2026-09-17-policy-writer-21

## Session start

- Subscribed to session-tracking.pending-commits and session-tracking.suggestions.
- Fetched pending-commits since seq=82: no new messages.
- Fetched session-tracking.suggestions since seq=0: no new messages on channel (suggestion landed in suggestion-box file instead).
- Ledger opened; STATE updated with active session entry.

## Session-tracking maintenance scan (policy-writer.md Step 1)

- pending-commit channel: last seq=82, no new messages.
- session-tracking git status: one untracked file `suggestion-box/2026-09-17-2330-composite-analyzer.md` — new suggestion filed by composite-analyzer. No other unexplained changes.

## Suggestion processing

### Entry 14 — `2026-09-17-2330-composite-analyzer`

Two items. Read the suggestion file in full.

**Item 1:** Session-start reading sequence skipped entirely — no tool call that touches mission state, code, or the user (including AskUserQuestion) may precede presenting the opening orientation block and receiving user confirmation. The very first tool calls must be the reads required to identify mission/role, with nothing else interleaved before orientation is presented.

Decision: This finding is distinct from existing rules. The suggestion is to add an explicit hard prohibition: no action (tool call touching mission state, code, or the user) may precede orientation. Add to `CONVENTIONS.md` and/or `session-start.md`.

**Item 2:** Closing an investigation as "resolved" by checking only one sub-case. New rule needed: before writing a durable "resolved" entry for any previously-investigated item, (1) re-read prior finding's text first, (2) enumerate every path before declaring the disjunctive claim closed.

Decision: This fits as a global rule, likely in `CONVENTIONS.md` Ground rules or as a new conventions file. It parallels `feedback_dont_deprioritize_unexercised_paths.md` in spirit but targets closure completeness rather than prioritization. Adding to `CONVENTIONS.md` Ground rules under investigation/verification sub-section.

## CONVENTIONS.md prose trim (session-21)

Filed under "conventions compliance pass" — trim all files to short bullets; original prose
saved to spec.

### CONVENTIONS.md — DONE (commit 2855307e)

Changes:
- Dropped intro paragraph ("Every session must read this file...")
- "Identify your mission" section: 3-line intro + bullets + close → 2 sentences + 1 bullet
- "Work only within your worktree" section: 3 paragraphs → 3 bullets
- "Situational rules" intro: 2 verbose paragraphs → 2 short sentences
- "narrowest command" bullet: 3-line run-on → split into 2 bullets
- Misc: tightened phrasing throughout Ground rules / Ownership section

Original prose saved to spec-policy-writer.md §7 "CONVENTIONS.md trim — session 21".

Subagent test (fresh context, CONVENTIONS.md only):
- All 7 questions answered correctly.
- Q7 noted two genuine gaps (where is STATE / how to ask in background context) — both
  answered by mandatory next reads (session-start.md, agentbus-user-interaction.md trigger).
  No change needed to CONVENTIONS.md.

Next file: conventions/session-start.md
