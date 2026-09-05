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

## Open Items & Deferred Discussions

- [ ] Revisit FG/BG analysis and workflow nuances across all 4 cases.
- [ ] Revisit overlap and division of labor between `CONVENTIONS.md`, `conventions/session-start.md`, and `resume-mission/SKILL.md`.
