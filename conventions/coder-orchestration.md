# Coding-task orchestration (when running/orchestrating a coder)

Read this before dispatching or running a coder agent.

## Worker types

| Type | When to use |
|---|---|
| **Foreground worker** | Task needs real-time steering or direct user visibility. Interactive; own context; uses agentbus channels. |
| **Background worker** | Self-contained task; only the result matters. Uses agentbus channels; may be silent in chat. |
| **Persistent CLI worker** | Persistent context across multiple invocations is needed. Uses agentbus channels. |

For design rationale and when each type is the right choice, see
`worktrees/policy-writer/.session/spec-policy-writer.md` § T8.

**Before any launch:** assign `In:` and `Out:` agentbus channels. Put both channels and the
subscription command in the task file and launch prompt.

**To launch a Bob CLI coder from Claude,** prepare the coder's worktree and task file, then:

```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

Record `--resume <task-id>` in the task file before launch. Losing it means the next
invocation starts cold with no prior context.

The Bob CLI launch is an explicit exception to the otherwise tool-agnostic rules above.

## Rules

1. Every worker has a role and a mission — never overstep it. Before delegating, confirm the
   worker's role and that the task fits it. If unsure, ask the user.
2. One task at a time — do not batch multiple tasks into one agent invocation expecting it to
   self-sequence unsupervised.
3. Each task gets a written task file before the worker starts (see the task file template below).
4. Each task lands as its own commit — not batched, not squashed across tasks.
5. **Coder isolation — pick a setup, then follow its procedure in
   `conventions/worktree-delegation.md`:**

   | Setup | Gate — use when | 
   |---|---|
   | **own-worktree** | The branch must stay checked out at a stable path after the coder finishes (a separate reviewer attaches independently later, or a human may `EnterWorktree`/open it in the IDE). |
   | **checkout-branch** | Default/common case: no durable visible path needed, only durable commits. Zero visible effect on any worktree the user has open. |
   | **same-worktree** | Parent is already in the correct target worktree (e.g. its own mission worktree) and wants coding done without a separate worktree/branch. |

   The mechanical setup, launch, and completion steps — including the exact task-file
   fields to fill — are in `conventions/worktree-delegation.md`, under the matching
   heading. Do not improvise the mechanics; that file is the single source for how each
   setup actually works (including the confirmed-empirically fact that
   `EnterWorktree(path=...)` cannot retarget a subagent into a mission worktree outside
   `.claude/worktrees/`, in any setup).

   **Concurrency, uniform across all three:** no more than one coder active per
   worktree/branch, ever. For checkout-branch, git enforces this structurally. For
   own-worktree and same-worktree, the parent must track which worktree/branch currently
   has an active coder (in mission STATE) and refuse to launch a second one there until the
   first finishes or reports done.
6. **Coder ledger:** every coder maintains a focused ledger (decisions made, findings,
   alternatives considered) — its one permitted write under `.session/`, named in its task
   file. In own-worktree/checkout-branch this lives in the coder's own worktree's
   `.session/` and remains recoverable via the branch even if the worktree is later
   deleted. It is never part of a PR — keep `.session/` out of any PR branch.
7. **Wait-for-instructions mode:** by default a coder may terminate once it reports
   completion. The task file can instead instruct it to hold open after reporting done and
   wait for further instruction on its `In:` channel (e.g. reviewer-requested fixes) rather
   than exiting — avoiding a fresh launch (and, for checkout-branch, a fresh checkout) for
   small follow-ups. Valid regardless of setup; most useful for checkout-branch. The parent
   decides per-task whether to tell the coder to terminate on completion or hold, and the parent
   (not the coder) makes the call to actually terminate it (`TaskStop` if needed) once done.
8. **Agentbus interaction:** every worker uses agentbus. The parent provides `In:` and `Out:`
channels in the task file and launch prompt. The worker subscribes to `In:` before starting and
publishes status, findings, questions, and completion to `Out:`. Workers are non-interactive by
 default. The user interacts through the mission owner. A foreground worker may be used when
real-time steering is required. A background worker may be attached when mid-task guidance is
needed.
9. **Code reviewer:** the reviewer reads commits from the coder's branch as they land — it
   does not wait for all coding to finish. If the coder diverges from the task the reviewer
   notifies the mission owner immediately. Review output goes to a file in the mission owner's
   `.session/`, not the coder's worktree. The reviewer reads from the coder's worktree and
   branch but is not isolated to it — the mission owner decides where it runs. A narrowly
   scoped PR-preparation agent (rebase, lint, DCO, test) needs no reviewer, no state file, no
   ledger — it reports directly to the parent.
10. Before starting the next task, the mission owner verifies the current task's completion
   state: was it reviewed, does it meet the done criteria, are there gaps, is it committed.
   The mission owner does not re-run tests or re-diff code — that is the reviewer's job.
11. The mission owner integrates approved work into the mission branch (cherry-pick or
    equivalent). The mission branch is the single source of truth. Never merge a coder worktree
    directly without review.
12. Coder and reviewer must never create or modify `.claude/settings.json` or
    `.claude/settings.local.json`.
13. All workers output to files, never dump long content into chat. Reports, findings, and
    review output go to their own worktree or ledger. The chat-visible return is a short pointer
    plus one-line status.
14. Never push to git or publish to GitHub (PRs, issues, etc.) without an explicit
    per-operation authorization from the user — not a standing permission, not inferred from an
    earlier approval.

## Task file

Every task gets a written task file before the worker starts. The task file must include `In:` and
`Out:` agentbus channels and the subscription command. Pass the task file to the worker. For a Bob
CLI coder invoked from Claude, place it in the prepared worktree and name it in the launch command.

The task file format and field rules are defined in `conventions/tasks.md`. The mission owner
is responsible for preparing a complete task file before invoking any worker.
