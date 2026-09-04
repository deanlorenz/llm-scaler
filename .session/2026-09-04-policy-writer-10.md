# Session — 2026-09-04-policy-writer-10

Continues: .session/2026-09-03-policy-writer-9.md

## Goal

Fix conventions and spec structure problems exposed by session-start violations at the
top of this session. Three root causes identified by user:

1. `session-start.md` and `state-vs-ledger.md` do not prevent sessions from reading plan
   docs and ledger files upfront — actively mislead them into doing so.
2. `Context/Refs` field in STATE template conflates must-read context with cited references.
3. `spec-policy-writer.md` has no structure that allows upfront orientation without reading
   the full doc; no canonical spec structure exists for mission specs generally.

## Session violations (self-corrections, session open)

- Read full ledger file (2026-09-03-policy-writer-9.md, ~500 lines) — should never be read
  at session start.
- Read full spec-policy-writer.md (~420 lines) — plan/spec docs not read upfront.
- Started executing (skill symlink check) before user confirmed — standing rule violation.
- Result: ~50K tokens at session open (24K messages).

## Decisions from user (2026-09-04)

### Session startup reading rules

- Plan/spec docs: never read upfront. Pull on demand only.
- Ledger files: never read at session start. Only for debugging/history.
- "Check if prior ledger was captured" step: belongs inside `resume-mission` custom-agent,
  runs before main session tokens are charged.
- New session: always create new ledger at session start; append new line to session log
  in STATE (do not overwrite old lines).

### Context/Refs split in STATE template

Split single `Context/Refs` field into two:
- **Context** — must-read files (what the session needs open to do the work)
- **Refs** — cited related files; do not read unless explicitly needed

### Spec structure (canonical for any mission roadmap/spec)

Sections in order:
1. Quick summary / orientation
2. Principles / approach  ← read up to here upfront; stop
3. At-a-glance (human-readable short, for the user)
4. Needs me (decision points / blocking on user ruling)
5. Roadmap / checklist
6. Outline (titles + one-line summaries + status per discussion/incident)
7. Details (full content per discussion/incident)
8. Other (mission-specific: findings, captured discussions, etc.)

Sessions read sections 1–2 upfront. Sections 3+ are on-demand only.

### resume-mission as custom-agent

- `resume-mission` becomes a custom-agent (simple model, own context).
- Procedural steps can stay as a skill initially, called from the custom-agent.
- Long-term: fold everything into the custom-agent, retire the skill.
- Same pattern likely applies to `wind-down` and `ledger-capture`.

## Plan (approved)

1. Fix `session-start.md`:
   - Add explicit rule: plan/spec docs never read upfront
   - Add explicit rule: ledger files never read at session start
   - Add: new session always creates a new ledger immediately at start
   - Add: session log in STATE gets a new line appended (not overwritten)
   - Note: ledger-capture check belongs in resume-mission custom-agent

2. Fix `state-vs-ledger.md` STATE template:
   - Split `Context/Refs` into `Context` (must-read) and `Refs` (do not read)
   - Add guidance: Context = only files needed to do the work; Refs = citation only

3. Refactor `spec-policy-writer.md` into canonical spec structure (sections 1–8 above).

4. Document the canonical spec structure in `tasks.md` (writer guidance).

5. Update `resume-mission` skill to note custom-agent direction (structural note only —
   full custom-agent spec is T10-adjacent work, not this session unless user extends scope).

6. Install all changes into session-tracking and push.

## Work log

## Wind-down (2026-09-04)

All work this session is committed on both branches. Not yet pushed (awaiting test + authorization).

### Commits this session

**policy-writer branch:**
- `94388bea` — feat(conventions): upfront-read rules, Context/Refs split, canonical spec structure (T9b/T9c/T9d)
- `eb248678` — docs(conventions): clarify session-tracking .claude/skills/ is source storage, not active skill dir

**session-tracking branch:**
- `4e7ecf10` — feat(skills): resume-mission — custom-agent direction, new orientation format, no upfront plan/ledger reads
- `ca005f9f` — feat(conventions): upfront-read rules, Context/Refs split, canonical spec structure, tasks.md spec template
- `6ab92330` — docs(conventions): clarify session-tracking .claude/skills/ is source storage, not active skill dir

### Files changed this session

- `conventions/session-start.md` — reading rules upfront section; opening orientation format; new-ledger-at-start; session-log append; skills-own-context note
- `conventions/state-vs-ledger.md` — split Context/Refs → Context + Refs; Plan/spec (do not read upfront) note
- `CONVENTIONS.md` — standing rule added: never read plan/spec or ledger files at session start; .claude/skills/ source storage note in layout
- `conventions/tasks.md` — Context/Refs field split; Plan/spec upfront note; canonical mission spec structure added
- `conventions/feature-worktree-setup.md` — source storage clarification for .claude/skills/
- `.session/spec-policy-writer.md` — refactored into 8-section canonical structure
- `.session/STATE.md` — updated ledger, steps, next step, session log
- `.session/2026-09-04-policy-writer-10.md` — this ledger (new)
- `session-tracking/.claude/skills/resume-mission/SKILL.md` — custom-agent direction note; Step 4 no upfront plan/ledger reads; Step 8 fixed orientation format; Step 9 new ledger creation

### Next step after push

Update STATE with push commits, then proceed to T10 (resume-mission custom-agent spec).

## Verified 2026-09-04 — all points captured in STATE and commits above
