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

- Assign `In:` and `Out:` agentbus channels. Put both in the task file and launch prompt.
- Confirm the task file is complete and committed.
- Confirm worktree setup — pick one:

| Setup | Use when |
|---|---|
| **same-worktree** | Default. Parent is already in the correct worktree; coding done without a separate branch. |
| **checkout-branch** | Need branch isolation: sandboxed coder, durable commits, no visible effect on open worktrees. |
| **own-worktree** | Branch must stay checked out at a stable visible path after finishing. |

Full mechanics for each setup are in `conventions/worktree-delegation.md` — do not improvise them.

**Concurrency:** no more than one coder active per worktree/branch, ever. Track the active coder in mission STATE.

## Rules

1. Every worker has a role and a mission — never overstep it. Confirm the role fits the task.
2. One task at a time — do not batch multiple tasks into one agent expecting it to self-sequence.
3. Each task gets a written, committed task file before the worker starts. Use `conv-tasks` to write it.
4. Each task lands as its own commit — not batched, not squashed.
5. Coder ledger: every coder maintains a focused ledger in `.session/`. Never in a PR branch.
6. **Wait-for-instructions mode:** the task file may instruct the coder to hold open after reporting done and wait for further instruction on `In:` rather than exiting. Useful for checkout-branch to avoid re-checkout for small follow-ups. The parent (not the coder) terminates it.
7. **Design-validation checkpoint** (hard stop): coder proposes code-level design, publishes to `Out:`, and does not proceed to implementation until the mission owner explicitly approves.
8. **Executor is binding:** if the user specifies who performs a task, that is binding. Do not substitute the current session as executor without stopping to ask first. Report deviation before acting.
9. Before starting the next task, verify the current task's completion: reviewed, meets done criteria, no gaps, committed. Do not re-run tests yourself — that is the reviewer's job.
10. Integrate approved work by cherry-pick onto the mission branch. Never merge a coder worktree directly.
11. Coder and reviewer must never create or modify `.claude/settings.json` or `.claude/settings.local.json`.
12. Workers output to files only — no long content in chat. Return a short pointer plus one-line status.
13. Never push or publish to GitHub without explicit per-operation authorization.

## Bob CLI launch command

```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

Record `--resume <task-id>` in the task file before launch.

## Code reviewer

- Reads commits from the coder's branch as they land — does not wait for all coding to finish.
- If coder diverges from the task, reviewer notifies the mission owner immediately.
- Review output goes to a file in the mission owner's `.session/`, not the coder's worktree.
- A narrowly scoped PR-preparation agent (rebase, lint, DCO, test) needs no reviewer, no state file, no ledger.
