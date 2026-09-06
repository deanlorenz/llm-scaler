# Worktree delegation patterns for coders (A / B / C)

Read this when preparing to launch a coder subagent and `coder-orchestration.md` §5 has
pointed you here. This file covers the parent-side mechanics only. For when to pick each
pattern, see `coder-orchestration.md` §5 — the gates live there, not here.

Every pattern keeps the coder's own context separate from the parent's — that part is
automatic (a background `Agent()` call always gets its own context window regardless of
isolation mode). What differs between A/B/C is filesystem/branch isolation, not context
isolation.

## Pattern A — durable, visible worktree

Use when the coder's branch must stay checked out at a stable path after the coder
finishes — a separate reviewer needs to attach independently, or a human may
`EnterWorktree`/open it in the IDE later.

**Setup (parent, before launching):**
1. Create the worktree yourself, from your own cwd, no relocation needed:
   ```bash
   git worktree add worktrees/<name> -b <branch>
   ```
2. Write the task file directly into that path (absolute path or `git -C`).
3. Record the worktree/branch as "in use by a coder" in mission STATE before launching
   (see Concurrency below).

**Launch:**
4. Launch the coder with `isolation` **omitted**. Task file / prompt states the absolute
   worktree path explicitly and instructs: "your first action is `cd <absolute-path>`; if
   that fails, stop and report — do not attempt `EnterWorktree` or any other recovery."
   `EnterWorktree(path=...)` targeting a mission worktree outside `.claude/worktrees/` is
   confirmed (empirically, not assumed) to fail structurally for a subagent — it is never a
   viable recovery step here.
5. Coder's first action: `cd` to the given path, confirm branch (`git branch
   --show-current`) matches the task file. Fail closed on either mismatch — report via
   `Out:` and stop; no self-correction attempt.

**During:**
6. No filesystem sandbox exists here — do not assume containment. This is the deliberate
   trade-off versus B/C's structural containment, accepted for path durability/visibility.
7. Concurrency (see below): never launch a second coder against this worktree/branch while
   one is already active.

**Completion / verification:**
8. Verify with `git -C <worktree-path> status` / `diff` against the expected branch to
   confirm the coder stayed in scope — the explicit compensating control for the missing
   sandbox. This is the reviewer's job when a reviewer is assigned (see `reviewer.md`); the
   parent does it directly only when no reviewer is assigned to the task.

## Pattern B — ephemeral worktree, durable branch

The common default: task doesn't need a durable visible path, only durable commits: parent
wants a coder with zero visible effect on any worktree the user has open.

**Setup (parent, before launching):**
1. Prepare a dedicated branch for the child at a known start point, without checking it out
   anywhere:
   ```bash
   git branch <branch> <start-point>
   ```
2. Commit the task file onto that branch directly, via plumbing — without ever checking
   the branch out in any worktree (checking it out even momentarily, e.g. via a scratch
   `git worktree add`, recreates the exact exclusivity conflict this pattern exists to
   avoid):
   ```bash
   TASK_FILE=/tmp/task-<id>.md
   # ... write the task file content to $TASK_FILE ...
   BLOB=$(git hash-object -w "$TASK_FILE")
   git read-tree <branch>
   git update-index --add --cacheinfo 100644,$BLOB,.session/task-<id>.md
   TREE=$(git write-tree)
   COMMIT=$(git commit-tree "$TREE" -p "$(git rev-parse <branch>)" -m "task: <id>")
   git update-ref "refs/heads/<branch>" "$COMMIT"
   ```
3. Record the branch as "in use by a coder" in mission STATE before launching (git also
   enforces this at checkout time, but STATE tracking keeps it visible without shelling out
   to check).

**Launch:**
4. Launch the coder with `isolation: "worktree"` (mints its own fresh ephemeral worktree
   under `.claude/worktrees/`).
5. Task file / prompt instructs: "your first action is `git checkout <branch>` — checkout,
   not `git reset --hard`. This brings your task file with it at `.session/task-<id>.md`.
   If checkout fails (e.g. 'already checked out at ...'), stop and report — do not force
   it, do not work on a different branch instead."

**During:**
6. Git enforces exclusivity structurally: the branch can only be checked out in one
   worktree at a time. A second coder attempting to check out the same branch fails
   outright, not just by convention.
7. A reviewer for this task must run inside that *same* ephemeral worktree path — not a
   separate one — while the branch remains checked out there (see `reviewer.md`).

**Completion:**
8. Cherry-pick the coder's commits from the branch into the mission branch.
9. Confirm the ephemeral worktree is actually removed — do not assume the harness always
   does this; check, or run `git worktree remove` explicitly — so the branch is freed.
10. Once freed, the branch persists independently: it can be materialized into a visible
    worktree later (Pattern A) if durability/visibility needs change, or picked back up by
    a new coder via the same checkout pattern.

## Pattern C — same worktree as parent (role-switch, no isolation)

Use when the parent session is already in the correct target worktree (e.g. its own
mission worktree) and wants to do coding work without the overhead of a separate
worktree/branch.

**Setup (parent, before launching):**
1. Already in the target worktree — no `git worktree add`, no branch prep.
2. Prepare the task file directly in this worktree (e.g. `.session/task-<id>.md`).
3. Record in mission STATE that a coder is now active in this worktree (Concurrency below).

**Launch — two modes, parent picks:**
4. **Async (background):** launch the coder with isolation omitted (inherits the parent's
   cwd — the only mechanism that works; `EnterWorktree` cannot retarget a subagent into
   this path even when it names the parent's own worktree — confirmed empirically). While
   it runs:
   - The coder may write only its own ledger file in the shared `.session/` (same universal
     rule as A/B — see `coder.md`). Everything else under `.session/` remains off-limits,
     including `STATE.md` and any other session's ledger, since there is only one shared
     `.session/` here rather than a separate one of its own.
   - The parent must not edit any code in this worktree while the coder is active —
     avoids collision with the coder's uncommitted work.
   - The parent may still read, monitor, plan, or launch a concurrent reviewer against this
     same worktree (reviewer reads only committed history, per the continuous-review rule
     in `reviewer.md`).
5. **Sync (foreground, blocking):** launch the coder with isolation omitted, wait for
   completion before doing anything else in this worktree. Then run reviewer, then verify.

**Concurrency:**
6. Exactly one coder at a time in this worktree — never launch a second coder here while
   one is active, regardless of sync/async.

**Completion:**
7. The coder's commits land directly on the worktree's actual branch — no cherry-pick or
   merge step needed (unlike A/B), since there was never a separate branch.

## Concurrency rule (applies uniformly to A, B, and C)

No more than one coder active per worktree/branch, ever — regardless of pattern. For B,
git enforces this structurally (checkout exclusivity). For A and C, nothing enforces it
automatically: the parent must track which worktree/branch currently has an active coder
(in mission STATE) and refuse to launch a second one there until the first finishes or
reports done.
