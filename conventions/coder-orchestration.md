# Coding-task orchestration (when running/orchestrating a coder)

Read this before dispatching or running a coder agent.

## Worker types

| Type | When to use |
|---|---|
| **Claude FW** (foreground subtask) | Task needs real-time steering or direct user visibility. Interactive; own context window. |
| **Claude BG** (background subagent) | Self-contained task; only the result matters. Silent; attach to interact mid-task if needed. |
| **Bob CLI coder** | Persistent context across multiple invocations needed, or Bob-specific mode required. Launched as a background OS process; communicates via agentbus. |

For design rationale and when each type is the right choice, see
`worktrees/policy-writer/.session/spec-policy-writer.md` § T8.

**To launch a Bob CLI coder,** prepare the coder's worktree and task file, then:

```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

Record `--resume <task-id>` in the task file before launch. Losing it means the next
invocation starts cold with no prior context.

Bob is used as a background coder only for now; reviewer and researcher roles are deferred.

## Rules

1. Every worker has a role and a mission — never overstep it. Before delegating, confirm the
   worker's role and that the task fits it. If unsure, ask the user.
2. One task at a time — do not batch multiple tasks into one agent invocation expecting it to
   self-sequence unsupervised.
3. Each task gets a written task file before the worker starts (see the task file template below).
4. Each task lands as its own commit — not batched, not squashed across tasks.
5. **Coder isolation:** coders work in their own worktree.
   - Claude FW/BG: launch with `isolation: "worktree"`. Tool-managed worktrees land under
     `.claude/worktrees/agent-<id>` and are disposable. Record the path and the branch in the
     mission `STATE.md` under "worktrees used" before the worker starts.
   - Bob CLI: parent prepares a dedicated worktree/branch before launch. Pass the path in the
     task file.
   - When multiple coders run concurrently, isolated worktrees are mandatory. A single
     coder + reviewer may share the mission worktree if file/folder ownership boundaries are
     stated explicitly in the task file.
6. **Coder state and ledger:** every coder maintains a focused ledger (decisions made, findings,
   alternatives considered) and a state file inside its worktree. These must remain recoverable
   via the branch even if the worktree is later deleted. They are not PR branches — keep
   `.session/` out of any PR.
7. **Interaction:** coders are non-interactive by default. The user interacts through the mission
   owner. Exceptions:
   - Claude FW subtasks are inherently interactive; use them when that is wanted.
   - A BG agent can be attached to for interactive work when mid-task guidance is needed.
   - Bob CLI coders communicate via agentbus. The mission owner monitors the agentbus channel
     for that coder's task; the coder posts status, findings, and questions there.
8. **Code reviewer:** the reviewer reads commits from the coder's branch as they land — it
   does not wait for all coding to finish. If the coder diverges from the task the reviewer
   notifies the mission owner immediately. Review output goes to a file in the mission owner's
   `.session/`, not the coder's worktree. The reviewer reads from the coder's worktree and
   branch but is not isolated to it — the mission owner decides where it runs. A narrowly
   scoped PR-preparation agent (rebase, lint, DCO, test) needs no reviewer, no state file, no
   ledger — it reports directly to the parent.
9. Before starting the next task, the mission owner verifies the current task's completion
   state: was it reviewed, does it meet the done criteria, are there gaps, is it committed.
   The mission owner does not re-run tests or re-diff code — that is the reviewer's job.
10. The mission owner integrates approved work into the mission branch (cherry-pick or
    equivalent). The mission branch is the single source of truth. Never merge a coder worktree
    directly without review.
11. Coder and reviewer must never create or modify `.claude/settings.json` or
    `.claude/settings.local.json`.
12. All workers output to files, never dump long content into chat. Reports, findings, and
    review output go to their own worktree or ledger. The chat-visible return is a short pointer
    plus one-line status.
13. Never push to git or publish to GitHub (PRs, issues, etc.) without an explicit
    per-operation authorization from the user — not a standing permission, not inferred from an
    earlier approval.

## Task file

Every task gets a written task file before the worker starts. For Claude workers this is the
input to the agent invocation. For Bob CLI workers it lives in the prepared worktree
(e.g. `.session/task-<id>.md`) and is named in the launch command.

The task file format and field rules are defined in `conventions/tasks.md`. The mission owner
is responsible for preparing a complete task file before invoking any worker.
