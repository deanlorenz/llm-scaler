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
  - Branch base is `upstream/main` @ `4db060e2` (rebased 2026-09-08 with user approval; the
    original base `778a8893` went stale within hours — upstream is actively moving). Do not
    rebase again without user approval.
  - **Do not invoke the upstream `pr-review` skill.** The user does not want it used on this
    mission (decision 2026-09-08). It ships tracked on `upstream/main` and was left in place
    unmodified — no deletion, no settings override. It already carries
    `disable-model-invocation: true`, so it is never model-invoked; treat it as off-limits even
    so. It runs only if the user explicitly types `/pr-review`.
- **Extra rules / rule refs:** `conventions/mission-owner.md`, `conventions/feature-worktree-setup.md`

## Execution

### Steps / subtasks
- [x] Create mission worktree off `upstream/main`
- [x] Set up `.session/`, skill symlinks, verify clean `git status`
- [x] Create initial `STATE.md`
- [x] Create `session-tracking/missions/composite-analyzer/` symlinks (uncommitted, by design)
- [x] Pin session into the worktree (`EnterWorktree`)
- [x] Commit `.session/` to the mission branch
- [x] Rebase onto current `upstream/main` (`4db060e2`)
- [ ] Define mission goal and scope with the user
- [ ] Create the mission plan doc; get user approval

**Last completed:** rebased onto `upstream/main` @ `4db060e2`; `.session/` committed as
`d6bbc722`.

**Next step / resume point:** ask the user to define the mission goal/scope (the spinoff's
actual objective), then write the plan doc and get approval. Do not begin mission work before
that approval. Note: the user deferred reading the `single-analyzer` mission — that deferral
likely needs lifting before the goal can be pinned down, since this mission is its spinoff.

### Status

- Environment: **ready** — worktree on branch `composite-analyzer`, rebased onto `upstream/main`
  @ `4db060e2`; 0 commits behind upstream, 1 ahead (the `.session/` commit). Only diff vs
  `upstream/main` is `.session/`. `.session/` tracked and committed, not gitignored;
  `resume-mission` + `wind-down` symlinks verified resolving into
  `session-tracking/claude-skills/`; `git status` clean.
- Session is **pinned** into this worktree via `EnterWorktree` — cross-worktree reads must use
  `cat <full-path>` or `git show <branch>:<path>`; `git -C` and `cd` elsewhere are blocked.
- Mission definition: **pending** — goal, scope, deliverables not yet defined by the user.
  **This is the only thing blocking mission work.**
- `session-tracking` symlinks: **created** (`missions/composite-analyzer/{STATE.md,ledgers}`),
  verified resolving. Left **uncommitted** per user instruction — `policy-writer` commits
  `session-tracking`. No agentbus `pending-commits` note published yet (not authorized).

### Known issues

- none

## Session log
- 2026-09-08 ledger=.session/2026-09-08-composite-analyzer-1.md status=active
