# composite-analyzer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** Spinoff from the `single-analyzer` mission. Concrete goal, scope,
  and deliverables are **not yet defined** — to be established with the user before any work.
- **Worktree:** `worktrees/composite-analyzer` (branch `composite-analyzer`)
- **Role / scope:** mission owner — owns STATE, the plan, the `composite-analyzer` branch, and
  integration decisions for this mission only.
- **Ledger / log:** active `.session/2026-09-08-composite-analyzer-1.md`; captured retired
  `.session/ledger/<slug>.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** none yet — to be created once the user defines the mission goal.
  *(do not read upfront — pull on demand only)*
- **Context:** <none yet — pending mission definition>
- **Refs:** <none yet>
- **Expected output:** <not yet defined>
- **Done / completion criteria:** <not yet defined>
- **Limits:**
  - Do **not** read the `single-analyzer` mission's files — the user explicitly deferred this
    ("do not read that mission yet"). Wait for explicit instruction.
  - Branch base is pinned to `upstream/main` @ `778a8893`; do not rebase or re-base without
    user approval.
  - **Do not invoke the upstream `pr-review` skill.** The user does not want it used on this
    mission (decision 2026-09-08). It ships tracked on `upstream/main` and was left in place
    unmodified — no deletion, no settings override. It already carries
    `disable-model-invocation: true`, so it is never model-invoked; treat it as off-limits even
    so. It runs only if the user explicitly types `/pr-review`.
- **Extra rules / rule refs:** `conventions/mission-owner.md`, `conventions/feature-worktree-setup.md`

## Execution

### Steps / subtasks
- [x] Create mission worktree off `upstream/main` (`778a8893`)
- [x] Set up `.session/`, skill symlinks, verify clean `git status`
- [x] Create initial `STATE.md`
- [ ] Define mission goal and scope with the user
- [ ] Create the mission plan doc; get user approval

**Last completed:** initial environment setup — worktree, `.session/`, skill symlinks, STATE.md

**Next step / resume point:** ask the user to define the mission goal/scope (the spinoff's
actual objective), then write the plan doc and get approval. Do not begin mission work before
that approval.

### Status

- Environment: **ready** — worktree on branch `composite-analyzer` off `upstream/main`
  `778a8893` (fetched and verified current 2026-09-08); `.session/` present and not gitignored;
  `resume-mission` + `wind-down` symlinks verified resolving into
  `session-tracking/claude-skills/`; `git status` clean.
- Mission definition: **pending** — goal, scope, deliverables not yet defined by the user.
- `session-tracking` symlinks: **not yet created** for this mission.

### Known issues

- none

## Session log
- 2026-09-08 ledger=.session/2026-09-08-composite-analyzer-1.md status=active
