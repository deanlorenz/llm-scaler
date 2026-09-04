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
- **Ledger / log:** `.session/2026-09-04-policy-writer-11.md` (active)

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
- [ ] Update resume-mission skill — note custom-agent direction
- [ ] Rewrite ledger-capture as a custom-agent (spec + mode)
- [ ] Write T10 session-setup agent spec

**Last completed:** T9b/T9c/T9d + session-tracking install + push (session-10, commits
`94388bea`/`eb248678` on policy-writer; `4e7ecf10`/`ca005f9f`/`6ab92330` on session-tracking)

**Next step / resume point:** Update resume-mission skill — add custom-agent direction note
(structural note only, not full implementation). Confirm with user before proceeding.

### Status
IN PROGRESS — remaining: resume-mission skill note (T10-adjacent), T10/T11/T12/T13 custom-agent specs.

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
