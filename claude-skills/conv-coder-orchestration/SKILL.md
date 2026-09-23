---
name: conv-coder-orchestration
description: Use when about to dispatch, launch, or orchestrate a coder agent or any background worker.
---

# conv-coder-orchestration

Runs in your own session (mission-owner context). Apply before launching any worker.

## Worker types

| Type | When to use |
|---|---|
| **Foreground** | Task needs real-time steering or direct user visibility |
| **Background** | Self-contained task; only the result matters |
| **Persistent CLI** | Persistent context across multiple invocations needed |

## Before any launch

1. Write and commit the task file. Use `conv-tasks` / `conventions/task_file_template.md`.
2. Assign `In:` and `Out:` agentbus channels — put both in the task file and launch prompt.
3. Pick a worktree setup (default: `same-worktree`):

| Setup | Use when |
|---|---|
| **same-worktree** | **Default.** Parent already in the correct worktree; no branch isolation needed. |
| **checkout-branch** | Need branch isolation: sandboxed coder, durable commits, zero visible effect on open worktrees. |
| **own-worktree** | Branch must stay checked out at a stable visible path after finishing. |

**Concurrency:** no more than one coder active per worktree/branch, ever. Track the active coder in mission STATE.

---

## same-worktree — default path (inline mechanics)

No branch prep, no separate worktree. Coder operates directly on the mission branch.

### Parent pre-steps

```bash
# 1. Write task file and commit it
git add .session/task-<id>.md && git commit -m "task: <id>"
TASK_SHA=$(git rev-parse HEAD)
```

Record `TASK_SHA`, branch name, and worktree path in the launch prompt.
Mark the branch as "in use by coder `<id>`" in mission STATE.

### Coder startup (every path — do not skip)

Instruct the coder to run these steps before any work:

**a.** Verify worktree path:
```bash
git rev-parse --show-toplevel   # must match Path in task file
```

**b.** Verify branch:
```bash
git branch --show-current   # must match Branch in task file
```

**c.** Verify task file; cherry-pick if not present:
```bash
test -f .session/task-<id>.md || git cherry-pick <TASK_SHA>
```

**d.** Exclude `.session/` from git tracking in this worktree:
```bash
WTID=$(basename "$(git rev-parse --git-dir)")
echo ".session/" >> "$(git rev-parse --git-common-dir)/worktrees/$WTID/info/exclude"
```

**e.** Fail closed: if any of a–d cannot be completed, stop. Publish exact failure to `Out:` and do not proceed.

**f.** Read `.session/task-<id>.md` and begin work.

### Launch

```bash
# Background (async):
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

Parent must not edit code in this worktree while the coder is active. Parent may read, monitor, plan, or launch a concurrent reviewer (reviewer reads committed history only).

### Post-steps

None. Commits land on the branch directly.

---

## Non-default setups — own-worktree and checkout-branch

Full mechanics (branch prep, launch flags, post-steps, PR-branch cleanliness) are in
`conventions/worktree-delegation.md`. Read only the section you need.

---

## Rules

1. Every worker has a role and a mission — never overstep it.
2. One task at a time — do not batch multiple tasks into one agent expecting it to self-sequence.
3. Each task gets a written, committed task file before the worker starts.
4. Each task lands as its own commit — not batched, not squashed.
5. Coder ledger: every coder maintains a focused ledger in `.session/`. Never in a PR branch.
6. **Wait-for-instructions mode:** task file may instruct the coder to hold open after reporting done and wait for further instruction on `In:`, rather than exiting. Useful for checkout-branch. The parent (not the coder) terminates.
7. **Design-validation checkpoint** (hard stop): coder proposes code-level design inside the task file, publishes to `Out:`, and does not proceed to implementation until the mission owner explicitly approves.
8. **Executor is binding:** if the user specifies who performs a task, that is binding. Do not substitute without stopping to ask first.
9. Before starting the next task, verify the current task's completion: reviewed, meets done criteria, committed. Do not re-run tests — that is the reviewer's job.
10. Integrate approved work by cherry-pick onto the mission branch. Never merge a coder worktree directly.
11. Coder and reviewer must never create or modify `.claude/settings.json` or `.claude/settings.local.json`.
12. Workers output to files only — no long content in chat. Return a short pointer plus one-line status.
13. Never push or publish to GitHub without explicit per-operation authorization.

## Code reviewer

- Reads commits from the coder's branch as they land — does not wait for all coding to finish.
- If coder diverges from task, reviewer notifies mission owner immediately.
- Review output goes to a file in the mission owner's `.session/`, not the coder's worktree.
- A narrowly scoped PR-prep agent (rebase, lint, DCO, test) needs no reviewer, no state file, no ledger.
