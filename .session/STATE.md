# policy-writer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  ⚠ **STOP. Read CONVENTIONS.md before any other file. Do not assume you know the rules.
  Do not speculatively read other files you see referenced here. Check the instructions first.**
- **What / goal / mission:** Build and maintain the cross-mission, cross-worktree
  session-tracking system — the conventions, skills, and layout that let any mission resume
  cleanly without reloading full history.
- **Worktree:** `worktrees/policy-writer` (branch `policy-writer`)
- **Role / scope:** Mission owner. Drafts all changes to `CONVENTIONS.md`, `conventions/`,
  and the skills here; copies finished content into `session-tracking`.
- **Ledger / log:** `.session/2026-09-06-policy-writer-18.md` (active)

## Task

- **Plan / spec:** `.session/spec-policy-writer.md`
  *(do not read upfront — pull on demand only)*
- **Context:** *(files to read to do the work — none required upfront beyond STATE + CONVENTIONS)*
- **Refs:** *(do not read unless explicitly needed)*
  - `worktrees/session-tracking/CONVENTIONS.md` (installed copy — production)
  - `worktrees/session-tracking/conventions/` (installed copies)
  - `.claude/skills/resume-mission/SKILL.md`
  - `.claude/skills/wind-down/SKILL.md`
- **Expected output:** Maintain the conventions, skills, and tracking layout; install approved
  production policy changes into `session-tracking` only after explicit authorization.
- **Done / completion criteria:**
  - Ownership and data-safety rules are present in core `CONVENTIONS.md` and installed.
  - Backup copies remain tracked under `backup_rules/`, outside production `conventions/`.
  - `session-tracking` contains no `.bak` files under production `conventions/`.
  - Installation and clean-state verification are recorded in the active ledger.
- **Limits:**
  - Do not put backup copies in production `conventions/` or install them into `session-tracking`.
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
- [x] Item 5: Agentbus conventions (`conventions/agentbus.md` and `conventions/agentbus-user-interaction.md`)
- [x] Item 6: Conventions for coders and coder-reviewers (`coder.md`, `reviewer.md`, and updated `tasks.md`)
- [x] Created `conventions/chat-preferences.md` for interactive session rules
- [x] Item 7: Conventions naming review (`<action>.md`, `<role>.md`, `<context>.md`)
- [x] Item 8: Workflow breakdown across 4 session cases
- [ ] Revisit FG/BG analysis and workflow nuances across all 4 cases
- [ ] Revisit overlap and division of labor between CONVENTIONS.md, session-start.md, and resume-mission
- [ ] Revisit role-specific subscription details in conventions/agentbus.md
- [x] Review & install finished conventions onto session-tracking
- [x] Fix install procedure (git checkout from branch, not hand-copy); add install-to-session-tracking.md convention
- [x] Redesign skills layout: policy-writer/claude-skills/ source of truth; session-tracking/claude-skills/ installed copy; symlinks retargeted
- [x] Reconstruct resume-mission + wind-down skills from session-13/14 ledger; install to session-tracking; push both branches
- [x] Restore ownership and data-safety rules; install approved policy changes
- [x] Move policy backups from `conventions/` to `backup_rules/`; keep them out of production
- [ ] Rewrite ledger-capture as a custom-agent (spec + mode)
- [ ] Write T10 session-setup agent spec
- [x] Process suggestion-box entry (worktree delegation for coders): drafted patterns A/B/C
  in `conventions/worktree-delegation.md`; updated `coder-orchestration.md` (gates,
  concurrency, ledger fix, wait-for-instructions), `coder.md` (`.session/` boundary),
  `reviewer.md` (where-you-run per pattern). Committed on policy-writer only
  (`6cb822af`, `a933d773`).
- [ ] Resolve A/B/C naming (user objected: "item names, will lose track easily" — need a
  different scheme before this can be considered final)
- [ ] Rewrite `tasks.md`'s task-file field guidance to be pattern-specific: exact
  worktree/path/branch + explicit instruction (verify-if-in / cd / checkout) per pattern
- [ ] Resolve duplication between `state-vs-ledger.md`'s STATE template and `tasks.md`'s
  field guide (user: "2 identical templates... confusing... 2 different places")
- [ ] Decide fate of `worktrees/policy-writer/CONVENTIONS.md.bak` (stray tracked file at
  worktree root, found during session-7 ledger-capture; not covered by the `.bak`
  completion criterion since it's not under `conventions/`)
- [ ] Fix `session-tracking` protocol violation: drafted the worktree-delegation changes
  directly on `session-tracking` instead of `policy-writer` before catching the mistake.
  `session-tracking` currently sits dirty/uncommitted with those (now-superseded) drafts —
  deliberately left as-is per user instruction ("we fix session-tracking later"). Do not
  install anything until this is cleaned up and the items above are resolved.

**Last completed:** Drafted and committed worktree-delegation patterns A/B/C on `policy-writer`
(`6cb822af`, `a933d773`) from the suggestion-box entry. Naming, `tasks.md` field-guide rewrite,
and the state-vs-ledger/tasks.md template duplication remain open per user direction.

**Next step / resume point:** Resolve A/B/C naming with the user; rewrite `tasks.md` field
guidance to be pattern-specific; resolve the STATE/tasks.md template duplication; then clean up
the `session-tracking` protocol violation (dirty superseded drafts) before any install.

### Status
IN PROGRESS — worktree-delegation drafting done on policy-writer but not final (naming/tasks.md
open); session-tracking still dirty from a caught protocol violation, fix deferred by user.

### Known issues
- `settings-and-skill-edits.md` describes a `user-approved-settings-change` marker
  requirement. Origin is 2026-08-27 observed harness behavior; user does not recognize the
  rule. Verify before editing any `SKILL.md` — the marker requirement may or may not still
  apply.
- Suggestion-box lifecycle (what happens to `processed-*` entries) formally undefined —
  using `processed-` prefix as interim.
- Deferred: whether `wind-down` should invoke itself as a background agent — defer to
  ledger-capture custom-agent design (that design will also settle wind-down invocation model).
- Session 18 drafted the worktree-delegation changes directly on `session-tracking` instead of
  `policy-writer`, in violation of `conventions/policy-writer.md` ("Drafts all changes to
  conventions and skills in `worktrees/policy-writer`"). Caught by the user mid-session. Root
  cause: `CONVENTIONS.md`'s trigger to read `policy-writer.md` at mission-owner session start
  was read but not acted on. Content was ported to `policy-writer` and committed correctly;
  `session-tracking`'s dirty, now-superseded working tree was deliberately left untouched per
  user instruction, to be cleaned up later.

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=retired ledger=.session/ledger/2026-08-27-session-tracking-setup.md
- 2026-08-30 session=2026-08-30-conventions-split-and-trim status=retired ledger=.session/ledger/2026-08-30-conventions-split-and-trim.md
- 2026-08-31 session=2026-08-31-policy-writer-7 status=retired ledger=.session/ledger/2026-08-31-policy-writer-7.md
- 2026-08-31 session=2026-08-31-policy-writer-8 status=retired ledger=.session/ledger/2026-08-31-policy-writer-8.md
- 2026-09-03 session=2026-09-03-policy-writer-9 status=retired ledger=.session/ledger/2026-09-03-policy-writer-9.md
- 2026-09-04 session=2026-09-04-policy-writer-10 status=retired ledger=.session/ledger/2026-09-04-policy-writer-10.md
- 2026-09-04 session=2026-09-04-policy-writer-11 status=retired ledger=.session/ledger/2026-09-04-policy-writer-11.md
- 2026-09-04 session=2026-09-04-policy-writer-13 status=retired ledger=.session/ledger/2026-09-04-policy-writer-13.md
- 2026-09-05 session=2026-09-05-policy-writer-14 status=retired ledger=.session/ledger/2026-09-05-policy-writer-14.md
- 2026-09-05 session=2026-09-05-policy-writer-15 status=retired ledger=.session/ledger/2026-09-05-policy-writer-15.md
- 2026-09-05 session=2026-09-05-policy-writer-16 status=retired ledger=.session/ledger/2026-09-05-policy-writer-16.md

- 2026-09-06 session=2026-09-06-policy-writer-17 status=retired ledger=.session/ledger/2026-09-06-policy-writer-17.md
- 2026-09-06 session=2026-09-06-policy-writer-18 status=active ledger=.session/2026-09-06-policy-writer-18.md

