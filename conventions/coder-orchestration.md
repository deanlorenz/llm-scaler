# Coding-task orchestration (when running/orchestrating a coder)

Read this before dispatching or running a coder agent.

## Worker types

Three worker types are available. Choose based on the task's scope and interaction needs.

**Claude custom-agent (FW — foreground/subtask).** A visible, interactive subtask with its own
conversation and breadcrumb. Use when the task benefits from real-time review or the user may
want to interact with it directly. Has its own context window; does not share the parent's.

**Claude custom-agent (BG — background subagent).** Silent; returns a summary to the parent
when done. Its chat cannot be opened directly, but Claude supports attaching to a BG agent to
work interactively — note this option when a BG agent needs mid-task guidance. Use when the
task is clearly self-contained and results can be summarized back.

**Bob CLI coder.** A Bob CLI session launched as a background OS process (`nohup bob run ...
&`). Stays alive across turns. Communicates with the parent via agentbus while running —
this is the primary interaction channel (works reliably, unlike `SendMessage` which has
timing constraints). Use for coding tasks that benefit from a persistent session context or
when a Bob-specific mode is needed.

> Bob CLI launch mechanics: see the old WVA repo under `plans-tooling/conventions/bob-delegation.md`
> and `plans-tooling/planning/atomic-step-protocol-design-v2.md`. Record the `--resume <task-id>`
> in the task file immediately — losing it means the next task starts cold.

Bob support is added later for other worker roles (reviewer, researcher). For now, Bob is
used as a background coder only.

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
8. **Reviewer isolation:** the reviewer for a task runs against the same coder worktree — not a
   third worktree, not the mission owner's open one. A narrowly scoped PR-preparation agent
   (rebase, lint, DCO, test) needs no reviewer, no state file, no ledger — it reports directly
   to the parent.
9. The mission owner reviews each task's diff itself before starting the next task — not
   delegated to the coder, not skipped.
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

## Task file template

Every task gets a written task file before the worker starts. For Claude workers this is the
input to the agent invocation. For Bob CLI workers it lives in the prepared worktree (e.g.
`.session/task-<id>.md`) and is named in the launch command.

```markdown
### <Task ID> — <short name>

**What / goal.** One or two sentences: what the task produces and why it exists.

**Where / context.**
- Worktree: `worktrees/<name>` (branch `<branch>`)
- Plan doc: `<path>`
- Key files in scope: `<file>`, `<file>`, ...

**Done / completion criteria.** Checkable claims — not "do the work" but "X exists, verified
by Y." List the tests, lint checks, or other validations that must pass.

**Limits / do not change.**
- List files, directories, or behaviors that are out of scope.
- List any standing rules the coder must not override.

**Subtasks.**
- [ ] Sub-item, smallest unit worth its own status
- [ ] Sub-item

**Refs.**
- *Reads:* `<doc>`, `<doc>`
- *Writes:* `<file>`, `<ledger>`

**Status.** `NOT STARTED` | `IN PROGRESS — <what's left>` | `DONE <date>` | `BLOCKED on <thing>`
```

Rules for applying it:
- Every field is required. If a field is missing when a task arrives from the user, ask before
  starting.
- Status is updated in place; completion notes accumulate and are not overwritten.
- A task with sub-tasks: one outer section in this shape, each sub-task nested in the same
  shape; the outer Subtasks list links down to the nested sections.
