Continues: .session/2026-09-04-policy-writer-11.md

## Session Setup

- Session: 2026-09-04-policy-writer-13
- Role: Mission owner
- Mission: policy-writer
- Worktree: worktrees/policy-writer
- Task: Update resume-mission skill (custom-agent direction note), custom-agent specs (ledger-capture, session-setup)

## Log

- [2026-09-04] Initialized session 13 ledger. Read STATE.md, CONVENTIONS.md, session-start.md, mission-owner.md per conventions.
- [2026-09-04] Updated local `.claude/skills/resume-mission/SKILL.md` to define the 3 session entry modes: (1) explicit command `/resume-mission <mission>`, (2) inferred mission with user approval gate, and (3) clean resume for existing/ongoing sessions via local STATE.md. Did not install to session-tracking.
- [2026-09-04] Restructured `.claude/skills/resume-mission/SKILL.md` per feedback:
  1. Description focuses on mission worktree rather than session-tracking.
  2. Part 1 covers Case 1-4 (including post-compact/clear clean resume, non-owner workers).
  3. Part 2 defines subagent/subtask return contract to supply parent with structured context block.
  4. Step 0 (policy-writer commit check) moved to newly created `conventions/policy-writer.md`.
  5. Indexed `conventions/policy-writer.md` in `CONVENTIONS.md` under situational rules.
  6. Step 1 focuses on finding session-tracking strictly for conventions access.
  7. Step 2 restores interactive vs background ambiguity handling.
  8. Step 3 enforces worktree entry (EnterWorktree) before reading files. Coders must be isolated.
  9. Step 4 restores complete migration diff & copy commands alongside prerequisite verification.
  10. Step 5 reads conventions, role/mission-specific rules, and STATE from within CWD.
  11. Step 6 uses .wip locking and ledger-capture for retired/half-alive session clearing.
  12. Step 7 publishes agentbus handoff for mission-owner/role takeovers.
  13. Step 8-9 initializes ledger under .wip protocol and handles interactive orientation vs subagent structured context return.
- [2026-09-04] Item 1 completed: Unified Part 2 and Step 9 in `resume-mission/SKILL.md` into a single canonical orientation and context contract with STATE and Ledger paths, and mandatory parent read of STATE.
- [2026-09-04] Item 2 completed: Split `session-start.md` and `resume-mission/SKILL.md`. Added prerequisite worktree isolation and STATE checks to `session-start.md`. Streamlined `resume-mission` to focus on discovery, setup, recovery/migration, clearing pending sessions, and agentbus handoff before handing off to standard `session-start.md`.
- [2026-09-04] Item 3 completed: Saved rationale/design background for resume/handoff/wind-down to `spec-policy-writer.md` (§ T14) in correct chronological order after T13 with exact line refs and doc-reference migration incident history. Added broken-symlink reconstruction command and self-healing responsibility clarification in `CONVENTIONS.md`. Explicitly added session-tracking convenience symlink checks to `resume-mission/SKILL.md` Step 4. Aligned `.claude/skills/wind-down/SKILL.md` to support safe checkpointing vs full retirement and reference `resume-and-handoff.md`.
- [2026-09-04] Item 4 completed: Reorganized `CONVENTIONS.md` Situational rules into Role & Mission Setup, Lifecycle & Session Boundaries, and Action Triggers with explicit preconditions.
- [2026-09-04] Item 5 completed: Agentbus conventions created (`conventions/agentbus.md` and `conventions/agentbus-user-interaction.md`), indexed in `CONVENTIONS.md`, and integrated into session startup flow and STATE template.
- [2026-09-04] Item 6 completed:
  - Created `conventions/coder.md` (lines 1-26): worktree isolation, no git push / GH interaction, no settings edits, task contract from plan/spec, ledger tracking, multi-file rebase/refactor plan execution, frequent commits, agentbus status reporting.
  - Created `conventions/reviewer.md` (lines 1-27): read-only, review report only write target, no GH interaction, continuous review of commits as they land, phase 1 independent review, phase 2 spec verification with checklist, built-in/project review skills usage with isolation, structural/lint/DCO checks (trusting coder tests), sanitization check for internal jargon/plan artifacts, early persistence to report file, agentbus status notification.
  - Updated `conventions/tasks.md` (lines 35-38, 55-60): task writer must extract/reference relevant plan sections for coder focus; for multi-file/rebase changes task writer must prepare locations list, step-by-step sequence, and post-change verification checklist.
  - Indexed `conventions/coder.md` and `conventions/reviewer.md` in `CONVENTIONS.md` (lines 38-39).
- [2026-09-04] Standing interaction rules persisted:
  - Created `conventions/chat-preferences.md` (lines 1-14) containing interactive approval gates (do not jump ahead, line numbers/diffs, no speculative actions, concise output).
  - Indexed in `CONVENTIONS.md` (line 47) and added to interactive startup flow in `conventions/session-start.md` (line 19).
  - Purged chat-specific UI rules from `CONVENTIONS.md` Ground rules.
- [2026-09-04] Session wind-down: retiring session-13 for clean handover to session-14.

## Open Items & Deferred Discussions

- [ ] Revisit FG/BG analysis and workflow nuances across all 4 cases.
- [ ] Revisit overlap and division of labor between `CONVENTIONS.md`, `conventions/session-start.md`, and `resume-mission/SKILL.md`.
- [ ] Revisit role-specific subscription details in `conventions/agentbus.md` (separate mission-owner / policy-writer specific subscriptions so generic sessions don't load extraneous rules).
- [x] Item 6: Conventions for coders and coder-reviewers (`conventions/coder.md`, `conventions/reviewer.md`, and updated `conventions/tasks.md`).
- [ ] Item 7: Conventions naming review (`<action>.md`, `<role>.md`, `<context>.md` — remove redundant `-rules` suffix).
- [ ] Item 8: Workflow breakdown across the 4 session cases.
- [ ] Review & install finished conventions onto `session-tracking`.

## Verified 2026-09-04 — folded in: T14 roadmap checkbox marked [x] in spec-policy-writer.md §5

| Ledger 13 point | Found in durable docs? | Action taken |
|---|---|---|
| Items 1–4 completed | ✅ STATE.md steps `[x]` + `Last completed` line | None needed |
| Item 5 (agentbus) open | ✅ STATE.md line 54 `[ ]` | None needed |
| Item 6 (coder/reviewer conventions) open | ✅ STATE.md line 55 `[ ]` | None needed |
| Item 7 (naming review) open | ✅ STATE.md line 56 `[ ]` | None needed |
| Item 8 (workflow breakdown) open | ✅ STATE.md line 57 `[ ]` | None needed |
| FG/BG review deferred | ✅ STATE.md line 58 `[ ]` | None needed |
| Boundary division deferred | ✅ STATE.md line 59 `[ ]` | None needed |
| T14 detail written | ✅ spec-policy-writer.md §7 T14 | Folded in: Marked T14 `[x]` in spec §5 roadmap |
