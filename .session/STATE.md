# policy-writer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **What / goal / mission:** Build and maintain the cross-mission, cross-worktree
  session-tracking system — the conventions, skills, and layout that let any mission resume
  cleanly without reloading full history.
- **Worktree:** `worktrees/policy-writer` (branch `policy-writer`)
- **Role / scope:** Mission owner. Drafts all changes to `CONVENTIONS.md`, `conventions/`,
  and the skills here; copies finished content into `session-tracking`.
- **Ledger / log:** `.session/2026-09-03-policy-writer-9.md` (active)

## Task

- **Plan / spec:** `.session/spec-policy-writer.md`
- **Context / refs:**
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
- [ ] Update `resume-mission` and `wind-down` skills — align with new STATE model
- [ ] Rewrite ledger-capture as a custom-agent (spec + mode)
- [ ] Write T10 session-setup agent spec

**Last completed:** Install + push to `origin` (commit `1427f964` on `session-tracking`,
`d52e4f86` on `policy-writer`)

**Next step / resume point:** Update `resume-mission` skill first — read current
`.claude/skills/resume-mission/SKILL.md`, then draft changes against the new conventions
(unified STATE model, per-session STATE file, session-setup agent). Confirm with user before
editing.

### Status
IN PROGRESS — three tasks remaining: skills update, ledger-capture custom-agent, T10 spec.

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
