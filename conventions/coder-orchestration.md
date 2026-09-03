# Coding-task orchestration (when running/orchestrating a coder)

Read this before dispatching or running a coder subagent.

1. Every session and every subagent has a role and a mission — never overstep it, and never
   assign the wrong one to a subagent. Before delegating, ask "what is this agent's actual
   mission" and only hand it work that fits — if unsure, ask the user rather than guessing.
2. Never push to git, never publish to GitHub (PRs, etc.) without an explicit per-operation ask
   — not a standing permission, not inferred from an earlier approval.
3. One task at a time — do not batch multiple tasks into one agent invocation expecting it to
   self-sequence unsupervised.
4. Each task gets a written spec before the coder starts (see the task template below).
5. Each task lands as its own commit — not batched, not squashed across tasks.
6. **Coder isolation:** launch coders with `isolation: "worktree"`. These land under
   `.claude/worktrees/agent-<id>` (tool-managed, disposable) — record that path in the
   mission's `STATE.md` under "worktrees used"; they don't need to follow the
   `session-tracking` layout themselves.
   > REVIEW: They do need to maintain a ledger and state (See review in CONVENTIONS). If it is an emphemeral worktree that gets deleted at termination of coder session then these tracking files should still be accessible via their branch. Note that these are not the PR branches -- PR branches must be rebased on main and clean. The mission owner should track all mission branches and worktrees.
   > I exepct the coder's ledger to be very focused (decisions it made, findings, alternatives) -- most of these belong in the STATE and can be returned to parent when coder is done.

   > REVIEW: I typically don't want interactive coders. I prefer to interact via the mission owner.
   > Need to setup the agentbus channels so interacting with a session would be possible.
   > Need an option to open a coder in "interactive mode" -- mostly for more detailed code review and guidance --
   > this should be rare. I think the simplest option would be for the mission owner to setup a visible worktree + .session in that worktree
7. **Review isolation:** the review agent for a coder's task runs against that *same* coder
   worktree (not a third worktree, not the user's open one).
   > REVIEW: we need to anlyze the overhead of those emphemeral worktrees. I think they are a must-have when multiple coders are launched under the same mission. IF the mission is more focused, it may be possible for the (single) coder and the (single) reviewer to work directly on the mission worktree. The isolation would be by folder boundaries (owner works on docs, coder on code) and by file name boundaries (for .session files). My concern is that the branches get out of hand. In any case, the mission owner should update the main mission worktree code as soon as possible and commit it. Coders that work on a PR branch -- prepare the PR for final push, rebase, test, lint, DCO, etc. -- may be even more focused -- not interactive, no reviewer, no ledger, no state file, just report back to parent.
8. The orchestrating session reviews each task's diff itself before starting the next task —
   not delegated to the coder, not skipped.
   > REVIEW: yes. We need to try to run the tasks as a list of subtasks. Each with a clear step.
9. **The mission owner cherry-picks approved commits from coder worktrees into the mission
   branch.** The mission branch is the single source of truth for that mission's code — coders
   work in their own isolated worktrees, and the mission owner integrates by cherry-picking
   (or equivalent) once a task is reviewed and approved. Never merge a coder worktree directly
   into the mission branch without review.
10. Coder and reviewer must never create or modify `.claude/settings.json` or
    `.claude/settings.local.json`.
11. All subagents output to files, never dump long content into chat — coder reports,
    review reports, research findings all go to a file (their own worktree, or their ledger
    entry); the chat-visible return is a short pointer plus one-line status.

## Task template

Each roadmap task must follow this shape:

```
### <Task ID> — <short name>

**Intent.** One or two sentences: why this task exists, what problem it closes.

**Expected outcome(s).** The concrete artifact(s)/state this task produces, stated as a checkable
claim — not "do the work" but "X exists, verified by Y."

**Todo.**
- [ ] Sub-item, smallest unit worth its own status
- [x] Sub-item already done — keep it checked, don't delete it once done

**Refs.** Every doc/file this task reads from or writes to. Group by role if the list is long:
*Reads:* / *Writes:*.

**Status.** One line, dated: `DONE <date>` | `IN PROGRESS, <what's left>` | `NOT STARTED` |
`BLOCKED on <thing>`. Followed by completion notes if DONE — what actually landed, which
commit(s), any real finding worth a reader knowing without re-deriving it.
```

Rules for applying it:
- Not every field needs prose — a one-line task gets a one-line Todo/Refs.
- A task with sub-tasks: one outer section in this shape, each sub-task gets its own nested
  section in the same shape; the outer Todo list becomes a checklist of sub-task names linking
  down to their sections.
- Status is updated in place (current-state field); completion notes accumulate, they are not
  overwritten.

> REVIEW: when invoking a task or a background task or receiving a task from me:
> make sure all these are defined and the appropriated granularity --
> what -- goal: ...
> where -- file: ..., line: ... , function: ..., plan doc: ...  (relevant context, as specific as possible)
> done -- complition criteria: ... (eg which tests must pass)
> limits -- dont not change: ...
>
> The above should be defined in the task "mission file" when you invoke a new background task.
> When I give you a task and you are not sure about any of these -- ask.
>
> On second thought, maybe these fields can just be part of the task template.
