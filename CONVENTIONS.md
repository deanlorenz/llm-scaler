# Conventions — cross-mission, cross-worktree

Every session must read this file before starting work. These are the standing rules that apply
to every mission and role.

## Identify your mission and role first

Every session is tied to exactly one mission. Before doing any work, identify:

- the mission name and its branch/worktree;
- your role in that mission;
- the session ledger you will maintain.

If any of these are unknown, ask the user before proceeding. Follow
`conventions/session-start.md` to initialize the session. A session assuming the mission-owner
role must also read `conventions/mission-owner.md`.

## Work only within your mission worktree

Every edit or write must target the session's own mission branch/worktree unless the user grants
a specific exception. Other worktrees are outside the session's scope: do not edit, inspect
their overall health, groom their files, or act as their maintainer.

Never use `cd`, subshells, process substitution, shell redirection, or any other mechanism to
route a write around the worktree boundary. When a cross-worktree write is required, ensure you
have a specific exception or ask the user, then follow
`conventions/writing-outside-worktree.md`.

Reads may cross worktree boundaries when needed (`git -C`, `cat`, full paths, etc.).

## Situational rules — read when triggered

Read the matching file when its situation occurs, not speculatively:

- `conventions/session-start.md` — **every session reads this first, before any work**
- `conventions/mission-owner.md` — assuming or acting in the mission-owner role
- `conventions/state-vs-ledger.md` — creating initial state or ledger files, or unsure which
  one a piece of information belongs in
- `conventions/resume-and-handoff.md` — resuming, taking over, handing off, or explicitly
  winding down a mission
- `conventions/writing-outside-worktree.md` — a write outside the mission worktree is required
  or a pinned session encounters the worktree isolation guard
- `conventions/feature-worktree-setup.md` — creating or migrating a mission worktree, or a skill
  directs you there because required local setup is missing
- `conventions/wip-editing.md` — editing `STATE.md` or `CONVENTIONS.md`, or persisting a newly
  approved plan
- `conventions/tasks.md` — defining a task for any worker, or receiving a task and verifying
  it is complete enough to start
- `conventions/coder-orchestration.md` — dispatching or running a coder agent
- `conventions/settings-and-skill-edits.md` — editing `~/.claude/settings.json` or a `SKILL.md`
- `conventions/unexplained-files.md` — finding an unexplained file or edit
- `conventions/push.md` — considering any git push, after receiving explicit authorization for
  that one push
- `conventions/pr-branch.md` — creating and curating the ephemeral branch that will back a PR
- `conventions/pr-workflow.md` — preparing to open the PR itself: checks, target, and GitHub API

## Repo layout

```text
session-tracking/                  ← global policy worktree; read-only unless specifically authorized
  CONVENTIONS.md                   ← this file
  conventions/                     ← situational rules
  suggestion-box/                  ← atomic proposals for policy-writer
  missions/                        ← read-only convenience symlinks
    <mission-name>/
      STATE.md -> worktrees/<mission-name>/.session/STATE.md
      <plan>.md -> worktrees/<mission-name>/.session/<plan>.md
      ledgers/ -> worktrees/<mission-name>/.session/

worktrees/<mission-name>/          ← mission branch/worktree
  .session/                        ← mission state, ledgers, and internal plans; never in a PR branch
  <normal code tree>               ← mission output
```

If a convenience symlink under `session-tracking/missions/` is broken, follow
`conventions/feature-worktree-setup.md` rather than modifying another mission's files.

## Ground rules

- Never assume. Ask when the mission, role, scope, authorization, or instruction is unclear.
- Do not silently choose between ambiguous or conflicting instructions; ask.
- Never push without explicit authorization for that specific push. Authorization is
  single-use. After receiving it, read `conventions/push.md` before pushing.
- Never stop or kill a running background task unless explicitly told to stop that task. A
  request to reduce chat noise is not permission to terminate work.
- Keep long content out of chat. Put long tool output, reports, and file dumps in the mission's
  `.session/` directory or code tree; reply with a short pointer and status.
- Maintain the session ledger continuously as findings, decisions, corrections, and false
  starts occur. Ledger and state updates do not need chat narration.
- Never edit files outside the mission and role you own.
- Do not use in-place command-line rewriting (`sed -i`, `gawk -i`, Python `fileinput`, or
  equivalents). Normal `Edit`/`Write` operations on owned, git-tracked files are allowed when
  their pre-session state is already checkpointed.
- Destructive actions (`git reset --hard`, `rm -rf`, `git stash drop`, and equivalents) require
  explicit approval for each individual step. If unsure, preserve a backup instead.
