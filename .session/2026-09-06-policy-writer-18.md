Continues: .session/ledger/2026-09-06-policy-writer-17.md

## Session start

- Resumed via `/resume-mission policy-writer`.
- Found two Session log entries (session-7, session-11) marked `retired` but never actually
  captured/verified: ledger files were still sitting loose in `.session/` instead of
  `.session/ledger/`, and neither had a `## Verified` marker. This is a protocol gap from
  those earlier sessions (session-17's own ledger had already flagged this exact gap).
- User directed: fix one at a time, each via a separate background agent, and be careful not
  to overwrite anything already superseded by later work — ask if unsure.
- Moved both files into `.session/ledger/`, then ran `ledger-capture` (as an ad hoc background
  agent per the contract in `conventions/resume-and-handoff.md` — no dedicated ledger-capture
  skill exists yet; that remains an open STATE task) against each, one at a time:
  - **session-7** (2026-08-31): 9 points, all already superseded by sessions 8–17. No folds.
    Incidental finding (not a ledger point): a stray `CONVENTIONS.md.bak` sits at
    `worktrees/policy-writer/CONVENTIONS.md.bak` (worktree root, tracked, dated 2026-08-30).
    Not covered by STATE's completion criterion (that only bars `.bak` under production
    `conventions/`), but looks like leftover Phase-2-trim cruft. Left untouched — open
    question for the user/next session, not decided here.
  - **session-11** (2026-09-04): 12 points, all already superseded by sessions 12–17. No folds.
  - Both ledgers now carry `## Verified 2026-09-06` markers with full audit tables.
- Committed the ledger relocation + verification markers (`6b45820a`), then declared ownership
  on `mission.policy-writer` (agentbus seq 95), then recorded session start in STATE.md
  (`53c4fcf7`).
- Did not read `spec-policy-writer.md` or prior ledgers beyond what the two sub-agents needed
  to check for supersession — per session-start convention, pulling on demand only.

## Open items carried into this session

- `worktrees/policy-writer/CONVENTIONS.md.bak` (tracked, worktree root) — ask user whether to
  delete or intentionally keep.
- STATE's existing open tasks unchanged: revisit FG/BG analysis, revisit CONVENTIONS/session-start/
  resume-mission overlap, revisit agentbus subscription details, rewrite ledger-capture as a
  custom-agent, write T10 session-setup agent spec.
