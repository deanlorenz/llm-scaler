# policy-writer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Build and maintain the cross-mission, cross-worktree
  session-tracking system — the conventions, skills, and layout that let any mission resume
  cleanly without reloading full history.
- **Worktree:** `worktrees/policy-writer` (branch `policy-writer`)
- **Role / scope:** Mission owner. Drafts all changes to `CONVENTIONS.md`, `conventions/`,
  and the skills here; copies finished content into `session-tracking`.
in - **Ledger / log:** `.session/2026-09-04-policy-writer-13.md` (active)

## Task

- **Plan / spec:** `.session/spec-policy-writer.md`
  *(do not read upfront — pull on demand only)*
- **Context:** *(files to read to do the work — none required upfront beyond STATE + CONVENTIONS)*
- **Refs:** *(do not read unless explicitly needed)*
  - `worktrees/session-tracking/CONVENTIONS.md` (installed copy — production)
  - `worktrees/session-tracking/conventions/` (installed copies)
  - `.claude/skills/resume-mission/SKILL.md`
  - `.claude/skills/wind-down/SKILL.md`
- **Expected output:** Updated skills (`resume-mission`, `wind-down`); ledger-capture
  custom-agent spec + mode; session-setup agent spec (T10).
- **Done / completion criteria:**
  - `resume-mission` and `wind-down` skills reflect unified STATE model and new conventions
  - ledger-capture rewritten as a custom-agent with its own spec and mode definition
  - T10 session-setup agent has a written spec
  - All changes installed into `session-tracking` and pushed to `origin`
- **Limits:**
  - `.bak` files: keep, do not delete (user decision 2026-08-31)
  - `settings-and-skill-edits.md`: do not change until marker behavior verified
  - `session-tracking` agentbus files: not this mission's — do not touch
- **Extra rules / rule refs:** `conventions/settings-and-skill-edits.md` before editing any
  `SKILL.md`

## Execution

### Steps / subtasks
- [x] Rewrite `conventions/coder-orchestration.md` with Claude/Bob worker model (T8)
- [x] Create `conventions/tasks.md` — task spec / writer guide
- [x] Full review pass of all `conventions/*.md` files (T9)
- [x] Unified STATE/task template; `session-start.md` simplified; `tasks.md` as writer guide
- [x] Verify old CONVENTIONS content captured; fix gap (commit-cadence rule)
- [x] Install `CONVENTIONS.md` + `conventions/` onto `session-tracking`; push to `origin`
- [x] Fix session-start.md, state-vs-ledger.md — upfront reading rules, Context/Refs split (T9b, session-10)
- [x] Refactor spec-policy-writer.md into canonical spec structure (T9c, session-10)
- [x] Document canonical spec structure in tasks.md (T9d, session-10)
- [x] Update resume-mission skill — note custom-agent direction, unify orientation contract, and integrate prerequisites (Items 1 & 2)
- [x] Align conventions/resume-and-handoff.md and skills (resume-mission, wind-down); save rationale to spec before purging (Item 3)
- [x] Group CONVENTIONS.md situational rules by role/mission, lifecycle, and action triggers (Item 4)
- [ ] Item 5: Agentbus conventions (`conventions/agentbus.md`)
- [ ] Item 6: Conventions for coders and coder-reviewers (`coder.md`, `reviewer.md`)
- [ ] Item 7: Conventions naming review (`<action>.md`, `<role>.md`, `<context>.md`)
- [ ] Item 8: Workflow breakdown across 4 session cases
- [ ] Revisit FG/BG analysis and workflow nuances across all 4 cases
- [ ] Revisit overlap and division of labor between CONVENTIONS.md, session-start.md, and resume-mission
- [ ] Rewrite ledger-capture as a custom-agent (spec + mode)
- [ ] Write T10 session-setup agent spec

**Last completed:** Items 1, 2, 3, 4 completed (unified orientation return contract, session-start split, resume-and-handoff alignment, CONVENTIONS index grouping)

**Next step / resume point:** Implement Item 5 (`conventions/agentbus.md`). Confirm with user before proceeding.

### Status
IN PROGRESS — Items 1-4 completed; remaining items 5-8, FG/BG review, and custom-agent specs.

### Known issues
- `settings-and-skill-edits.md` describes a `user-approved-settings-change` marker
  requirement. Origin is 2026-08-27 observed harness behavior; user does not recognize the
  rule. Verify before editing any `SKILL.md` — the marker requirement may or may not still
  apply.
- Suggestion-box lifecycle (what happens to `processed-*` entries) formally undefined —
  using `processed-` prefix as interim.

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=retired ledger=.session/2026-08-27-session-tracking-setup.md
- 2026-08-30 session=2026-08-30-conventions-split-and-trim status=retired ledger=.session/2026-08-30-conventions-split-and-trim.md
- 2026-08-31 session=2026-08-31-policy-writer-7 status=retired ledger=.session/2026-08-31-policy-writer-7.md
- 2026-08-31 session=2026-08-31-policy-writer-8 status=retired ledger=.session/2026-08-31-policy-writer-8.md
- 2026-09-03 session=2026-09-03-policy-writer-9 status=retired ledger=.session/2026-09-03-policy-writer-9.md
- 2026-09-04 session=2026-09-04-policy-writer-10 status=retired ledger=.session/2026-09-04-policy-writer-10.md
- 2026-09-04 session=2026-09-04-policy-writer-11 status=retired ledger=.session/2026-09-04-policy-writer-11.md
- 2026-09-04 session=2026-09-04-policy-writer-13 status=active ledger=.session/2026-09-04-policy-writer-13.md
