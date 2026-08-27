# Spec — migrate to bare-repo layout: `repo/` + `worktrees/`

**Status: approved plan, execution not yet started.** This spec is the durable copy of
a plan first written to one session's local plan-mode storage
(`~/.claude/plans/sunny-zooming-key.md`, approved by the user, not visible to any other
session or after that session ends). This doc is now the source of truth for continuing
this mission — do not treat the local plan file as authoritative once this doc exists.

## Intent

The current setup has a git repo (`dean-llmd-scaler-sandbox`, a clone of
`origin=deanlorenz/llm-scaler.git`, upstream push disabled) that is itself a normal
checkout — not bare — hosting 31 worktrees both under it (`worktrees/*`,
`.claude/worktrees/*`) and scattered outside it (`wva-integration`, `wva-prs/*`). The
user wants to move to a cleaner, more standard layout: a **bare repo** and a single
`worktrees/` folder for all worktrees — everything living only under
`llm-d/llmd-scaler`, still a clone of `origin`, still blocked from pushing upstream.

This is being done because the current container-as-checkout layout is confusing (one
worktree, the container itself, is indistinguishable from "the repo"; other worktrees
live in unrelated directories like `wva-integration`, `wva-prs/`) and because the
`policy-writer` mission's own work already hit one surprise from this same
structure (`.git/info/exclude` turned out to be shared across every worktree of a repo,
not per-worktree). A bare repo with all worktrees in one place is the standard git
pattern and avoids this class of surprise going forward.

**Hard constraint, stated explicitly by the user: do not lose any local file, including
gitignored files.** Do not delete anything from the current container until 100%
verified. The old container is left completely alone throughout this plan — cleanup is
explicitly a future, separate, explicitly-approved step, not part of this plan.

## Settled design (do not re-litigate without a new decision from the user)

**Verified state, at time of writing (2026-08-27) — re-verify before executing if this
mission is picked up much later, since worktree state changes over time:**

- **The container**: `/home/dean/code/llm-d/dean-llmd-scaler-sandbox` — normal
  (non-bare) checkout, `origin=git@github.com:deanlorenz/llm-scaler.git` (push
  enabled), `upstream`/`ofer` remotes both push-disabled. On `feat/wva-external-scaler`,
  **not clean** (8 changed/untracked items: `.gitignore`/`AGENTS.md` modified; `bobi`,
  `bob-tasks-*.json`, `docs/plans/verification/`, three `.code-workspace` files, and
  `mmy.env` untracked). **User said: leave these in place, ignore them.** `mmy.env`
  contains live API keys in plaintext — never read, print, move, or commit it.
- **31 worktrees total** (`git worktree list`), across three physical locations:
  - `worktrees/*` (17) — `single-analyzer`, `session-tracking`, six `benchmark-*`,
    `fix-scaledobjects-cold-start`, and `agentbus` (unborn branch, no commits, one
    untracked `README.md` — belongs to a parallel session's mission, see
    `../agentbus/`, not this one).
  - `.claude/worktrees/*` (7) — tool-managed, ephemeral coder-agent worktrees.
  - Outside the container, directly under `/home/dean/code/llm-d/`: `wva-integration`
    (branch `integration-e2e`) and `wva-prs/pr1..pr12` (12 worktrees) — confirmed
    genuine worktrees of this same repo via `git rev-parse --git-dir`.
- **11 of the 31 worktrees have real uncommitted content** — most importantly
  `worktrees/benchmark-run`, whose untracked `runs/` is **17GB** of benchmark output
  data, gitignored, not in git history anywhere. Must be physically copied, not
  recreated.
- **The 12 `wva-prs/pr*` worktrees and `wva-integration`, checked individually**: all 13
  have zero uncommitted working-tree changes. 7 of the 12 PR worktrees are
  byte-identical to `origin/<branch>` (`pr1-discovery-fixes`,
  `pr3-remove-ineffective-binding`, `pr4-modelserver-scrape-check`, `pr8-preflight-perf`,
  `pr10-guide-text-fixes`, `pr11-dashboard`, `pr12-terse-check-messages`) — not
  migrated, recreatable later from `origin` whenever actually needed. 5 differ
  (real local commits not on `origin`): `pr2-servicemonitor-ordering`,
  `pr5-epp-signals-required`, `pr6-so-park-freeze-resume`,
  `pr7-scope-and-preconditions`, `pr9-guide-review-docs` — must be migrated.
  `wva-integration`'s `integration-e2e` has no remote branch at all — entire history is
  local-only — must be migrated.
- Disk footprint: container + worktrees ≈ 19GB (dominated by `runs/`),
  `wva-integration` 11MB, `wva-prs/` 119MB. Destination had 611GB free.
- `.git/info/exclude` is shared across every worktree of a repo — needs to carry over
  to the new bare repo's shared exclude, including the two lines added by
  `policy-writer` (symlinks to `session-tracking`'s `.claude/skills/`).
- No worktree is `git worktree prune`-broken.

**Naming/location decisions — confirmed with the user:**
1. Bare repo path: `/home/dean/code/llm-d/llmd-scaler/repo` (brand-new directory; `repo`
   itself is the bare repo, not `repo/<name>.git` nested inside).
2. Worktrees folder: `/home/dean/code/llm-d/llmd-scaler/worktrees/` — one flat folder,
   no subgrouping.
3. New worktree for `upstream/main`: named `main`.
4. `.claude/worktrees/*` (7 coder-agent worktrees): **not migrated** — disposable by
   design.
5. 7 of the 12 `wva-prs/pr*` worktrees: **not migrated**. 5 of the 12, plus
   `wva-integration`: **migrated**.

## Approach

**Fresh clone, not a migration-in-place.** The new bare repo is cloned fresh from
`origin`; the old `.git` is never renamed, moved, or touched — a rollback safety net.

### Phase 1 — Build the new bare repo and its remotes
1. `mkdir -p /home/dean/code/llm-d/llmd-scaler`
2. `git clone --bare git@github.com:deanlorenz/llm-scaler.git /home/dean/code/llm-d/llmd-scaler/repo`
3. Add `upstream` (fetch `https://github.com/ev-shindin/llm-scaler.git`) and `ofer`
   (fetch `git@github.com:biranofer/llm-scaler.git`) remotes, both push-disabled the
   same way the container does it. Verify `git -C repo remote -v` matches the
   container's exactly.
4. `git -C repo fetch upstream && git -C repo fetch ofer`.
5. Copy the container's `.git/info/exclude` content verbatim into the new bare repo's
   `info/exclude`.

### Phase 2 — Migrate the branches/worktrees that need real content
6. For the 5 differing PR branches and `wva-integration`'s `integration-e2e`: fetch from
   the **old container's `.git`** (a local-path fetch — these branches don't exist on
   `origin`), verify the fetched SHA matches the old worktree's `HEAD` exactly.
7. For every worktree in `worktrees/*` (17) plus the 5 from step 6: `git worktree add`
   into the new `worktrees/` folder. For `agentbus` (unborn): `git worktree add -b
   agentbus` from the bare repo's default-branch tip instead.
8. For every worktree with uncommitted content (the 11 identified, most importantly
   `benchmark-run`'s 17GB `runs/`): copy the actual modified/untracked files (from each
   worktree's own `git status --short` list, not a blind whole-directory copy) from old
   to new. Use `rsync -a --info=progress2 --stats` for `runs/` specifically.
9. Recreate the two skill symlinks in the new `single-analyzer`. Verify the new bare
   repo's `info/exclude` (from step 5) already covers them.

### Phase 3 — `main` worktree tracking `upstream/main`
10. `git -C repo worktree add worktrees/main upstream/main`. Confirm remotes match.

### Phase 4 — Verification, before anything old is touched
11. `git status --short` diff, old vs. new, for every migrated worktree — zero
    differences expected.
12. `du -sb` + `find | wc -l` match, old vs. new, for `runs/`.
13. `git remote -v` diff, old container vs. new bare repo — must match exactly.
14. Confirm the two skill symlinks resolve in the new `single-analyzer`.
15. Present a summary table to the user and **stop**. Do not suggest, plan, or execute
    any removal of the old container as part of this — a separate future step requiring
    its own explicit approval.

## Todo

### T1 — Investigate current state
**Status.** DONE 2026-08-27. Full inventory above, verified by direct inspection (not
assumed) of every worktree, remote, and disk-usage figure.

### T2 — Confirm naming/location decisions with the user
**Status.** DONE 2026-08-27. All 5 decisions above confirmed via `AskUserQuestion`.

### T3 — Execute Phase 1 (bare repo + remotes)
**Status.** NOT STARTED. **User explicitly said "do not start any migration yet, still
planning only" after the plan was approved** — this task, and every task after it, is
blocked on explicit go-ahead to begin execution, not just plan approval.

### T4 — Execute Phase 2 (migrate branches/worktrees with real content)
**Status.** NOT STARTED. Depends on T3.

### T5 — Execute Phase 3 (`main` worktree)
**Status.** NOT STARTED. Depends on T4.

### T6 — Execute Phase 4 (verification)
**Status.** NOT STARTED. Depends on T5.

## Refs

*Reads:* the container (`/home/dean/code/llm-d/dean-llmd-scaler-sandbox`) and every
worktree listed above, read-only, for verification purposes only until T3 is explicitly
approved to start.
*Writes (once approved):* `/home/dean/code/llm-d/llmd-scaler/` (new — everything created
there), never the old container.

## Open items

- **Execution has not started and needs a fresh explicit go-ahead** — plan approval
  (which already happened) is not the same as approval to execute; the user was
  explicit about this distinction.
- The exact 8th "identical" PR worktree name was miscounted once in an earlier draft of
  this plan (corrected to the accurate 7-identical/5-differing split above, verified
  twice) — a reminder to re-verify this split again if resuming much later, since it's
  exactly the kind of count that's easy to get wrong from memory rather than by
  re-running the check.
