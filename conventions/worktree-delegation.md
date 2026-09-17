# Worktree delegation setups for coders

Read this when `coder-orchestration.md`'s gate table has pointed you here. This file covers
the parent-side mechanics — the gates (which setup to pick, when) live in
`coder-orchestration.md`, not here.

Every background `Agent()` call always gets its own context window regardless of isolation
mode. What differs between the three setups is filesystem/branch isolation, not context.

**Default:** use **same-worktree** unless you need branch isolation or a durable visible
path. Read only the section you need.

---

## Universal steps — every launch path

### Parent pre-steps (always, before launching)

1. Write the task file in the parent's own worktree and commit it:
   ```bash
   # write .session/task-<id>.md
   git add .session/task-<id>.md && git commit -m "task: <id>"
   TASK_SHA=$(git rev-parse HEAD)
   ```
2. Record the branch/worktree as "in use by coder `<id>`" in mission STATE.
3. Include `TASK_SHA`, branch name, and worktree path (where known) in the launch prompt.

### Task file template — fill every field

```
Name: <session-slug>
Conventions: worktrees/session-tracking/CONVENTIONS.md
What / goal / mission: <one or two sentences>
Worktree: <name or "ephemeral — assigned at launch">
Path: <absolute-path or "unknown — verify branch instead">
Branch: <branch-name>
Task SHA: <TASK_SHA>
In: <mission>.<id>.in
Out: <mission>.<id>
Startup verification instructions:
  See worktree-delegation.md §"Universal coder startup" — follow steps a–f exactly.
  Fail and report on Out: if any step cannot be completed.
Role / scope: <role and explicit limits>
Ledger / log: .session/<slug>.md
Expected output: <specific deliverable>
Done / completion criteria: <checkable claims with actual results, not just pass/fail>
Limits: <what must not change>
Steps / subtasks:
  [ ] ...
Status: NOT STARTED
```

### Universal coder startup (every path, in order — do not skip)

**a. Verify worktree** (same-worktree and own-worktree only; skip for checkout-branch
   where the path is unknown at launch):
   ```bash
   git rev-parse --show-toplevel   # must match Path in task file
   ```

**b. Checkout branch if needed:**
- same-worktree: already on the correct branch — verify with `git branch --show-current`.
- own-worktree: already on the correct branch — verify with `git branch --show-current`.
- checkout-branch: `git checkout <branch>` — not `git reset --hard`. If the checkout
  fails (e.g. "already checked out elsewhere"), stop and report; do not force it.

**c. Verify task file; cherry-pick if not present:**
   ```bash
   test -f .session/task-<id>.md || git cherry-pick <TASK_SHA>
   ```
   After cherry-pick, confirm the file exists before continuing.

**d. Exclude `.session/` from git tracking in this worktree:**
   ```bash
   WTID=$(basename "$(git rev-parse --git-dir)")
   echo ".session/" >> "$(git rev-parse --git-common-dir)/worktrees/$WTID/info/exclude"
   ```
   This is a worktree-local exclude — it has no effect on already-tracked files and
   disappears when the worktree is removed. Run it on every path for consistency.

**e. Fail closed:** if any of steps a–d cannot be completed, stop immediately. Publish
   the exact failure to `Out:` and do not proceed.

**f. Read `.session/task-<id>.md` and begin work.**

---

## same-worktree — parent's own worktree, no isolation

**When to use:** default for a single coder. No branch prep, no separate worktree.

**Limitations:** no filesystem sandbox — coder operates directly on the mission branch.
Parent must not edit code in this worktree while the coder is active.

**Where results land:** directly on the mission branch. No cherry-pick needed.

**Extra pre-steps:** none beyond the universal pre-steps.

**Launch:**
- **Async (background):** launch with isolation omitted (inherits parent's cwd — the only
  mechanism that works; `EnterWorktree` cannot retarget a subagent into the parent's own
  path, confirmed empirically). Parent must not edit code while coder runs; may still read,
  monitor, plan, or launch a concurrent reviewer (reviewer reads committed history only).
- **Sync (foreground):** launch with isolation omitted, wait for completion, then run
  reviewer, then verify.

**Extra post-steps:** none. Commits land on the branch directly.

---

## own-worktree — durable, visible worktree

**When to use:** the coder's branch must stay checked out at a stable path after finishing
— a reviewer needs to attach independently, or the user may open it in the IDE.

**Limitations:** no filesystem sandbox — explicit scope verification required after
completion.

**Where results land:** on the coder's own branch. Parent cherry-picks code commits.

**Extra pre-steps:**
```bash
git worktree add worktrees/<name> -b <branch> <start-point>
# Write task file to the new worktree (parent already committed it in step 1 above;
# now also make it available at the coder's path):
cp .session/task-<id>.md worktrees/<name>/.session/task-<id>.md
git -C worktrees/<name> add .session/task-<id>.md
git -C worktrees/<name> commit -m "task: <id> (copy for coder worktree)"
```

**Launch:** isolation omitted. Pass the absolute worktree path in the launch prompt.

**Extra post-steps:**
- Verify scope: `git -C <worktree-path> diff <start-point>..HEAD` — confirm coder stayed
  in scope. Reviewer's job if a reviewer is assigned; parent's job otherwise.
- Cherry-pick code commits onto mission branch (not the task-file commit).

---

## checkout-branch — ephemeral worktree, durable branch

**When to use:** sandboxed, git-isolated coder; work must survive as durable commits on a
named branch; no persistent worktree needed.

**Limitations:** worktree path is unknown at launch (assigned by harness). Coder verifies
branch name, not path (step a is skipped). `.session/` files in the ephemeral worktree are
lost when it is removed — copy them out before removing if they need to survive.

**Where results land:** on the coder's own branch. Parent cherry-picks code commits.

**Extra pre-steps:**
```bash
git branch <branch> <start-point>
```
Launch with `isolation: "worktree"` (harness mints ephemeral worktree under
`.claude/worktrees/`).

Note: step (a) of universal coder startup (verify worktree path) is **skipped** for this
setup — the ephemeral path is not known ahead of time. The coder verifies branch name only.

**Extra post-steps:**
1. If the coder produced ledger files or other `.session/` content that must survive,
   copy them to the mission's own `.session/` **before** removing the worktree:
   ```bash
   cp <ephemeral-path>/.session/<slug>.md worktrees/<mission>/.session/
   ```
2. Remove the ephemeral worktree to free the branch:
   ```bash
   git worktree remove <ephemeral-path>
   # or confirm the harness already removed it: git worktree list
   ```
3. Cherry-pick the coder's *code* commits onto the mission branch. The task-file commit
   stays on the child branch as history — do not cherry-pick it.

**PR-branch cleanliness:** if the work is destined for a PR, branch the coder from the
*upstream base* (e.g. `upstream/main`), not from the mission branch. `.session/` and
mission artifacts are then never present. The child's branch still carries the task-file
commit after completion — cherry-pick only the code SHA(s) onto the PR branch. State this
in the task file's Done criteria so the coder doesn't declare "PR-ready" prematurely.

---

## Concurrency rule (all setups)

No more than one coder active per worktree/branch, ever. For checkout-branch, git enforces
this structurally (checkout exclusivity). For own-worktree and same-worktree, the parent
must track the active coder in mission STATE and refuse to launch a second one until the
first finishes.

---

## Alternatives — task file delivery (read only if the default fails)

> **Do not read this section upfront.** Use it only if the default (commit on mission
> branch → child cherry-picks) is blocked.

### Alt A — plumbing (task file on child branch only; never touches mission branch)

Use when committing the task file on the mission branch is not acceptable.

```bash
TASK_FILE=/tmp/task-<id>.md
# ... write task file content to $TASK_FILE ...
BLOB=$(git hash-object -w "$TASK_FILE")
# REQUIRED: use a temp index — git read-tree + update-index clobber the parent's own
# index if GIT_INDEX_FILE is not set:
export GIT_INDEX_FILE=$(mktemp)
git read-tree <branch>
git update-index --add --cacheinfo 100644,$BLOB,.session/task-<id>.md
TREE=$(git write-tree)
COMMIT=$(git commit-tree "$TREE" -p "$(git rev-parse <branch>)" -m "task: <id>")
git update-ref "refs/heads/<branch>" "$COMMIT"
unset GIT_INDEX_FILE
```

Task file is already on `<branch>` — child's startup step (c) finds the file after
`git checkout <branch>` and skips the cherry-pick.

### Alt B — `/tmp` path (no commit, ephemeral only)

Use only for short-lived tasks exempt from requiring a ledger (see `coder-orchestration.md`
rule 9). Write to `/tmp/task-<id>.md` and pass the path in the launch prompt. No commit
anywhere; child reads directly from `/tmp`.
