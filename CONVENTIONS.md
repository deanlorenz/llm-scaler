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

### Role & Mission Setup (Read when establishing mission/role at session start)
- `conventions/session-start.md` — **every session reads this first, before any work**
- `conventions/mission-owner.md` — assuming or acting in the mission-owner role
- `conventions/policy-writer.md` — working in any role on the `policy-writer` mission
- `conventions/coder.md` — executing in the coder role
- `conventions/reviewer.md` — executing in the code-reviewer role

### Lifecycle & Session Boundaries
- `conventions/resume-and-handoff.md` — running `/resume-mission` or `/wind-down`, taking over, or ending work
- `conventions/feature-worktree-setup.md` — creating/migrating a mission worktree or setting up missing skill symlinks
- `conventions/state-vs-ledger.md` — creating initial state or ledger files, or unsure which file information belongs in

### Action Triggers (Read immediately before performing the action)
- `conventions/agentbus-user-interaction.md` — when running as a background agent/subtask needing to ask user questions via agentbus
- `conventions/wip-editing.md` — before editing a shared file (`STATE.md`, `CONVENTIONS.md`)
- `conventions/writing-outside-worktree.md` — before performing a permitted cross-worktree write
- `conventions/tasks.md` — before writing or assigning a task specification to any worker
- `conventions/coder-orchestration.md` — before dispatching or orchestrating a coder agent
- `conventions/push.md` — before executing git push (after receiving explicit single-use approval)
- `conventions/pr-branch.md` — before creating or curating an ephemeral PR branch
- `conventions/pr-workflow.md` — before opening a PR via the GitHub API
- `conventions/settings-and-skill-edits.md` — before editing `~/.claude/settings.json` or a `SKILL.md`
- `conventions/unexplained-files.md` — upon finding an unexplained file or uncommitted edit

## Repo layout

```text
session-tracking/                  ← global policy worktree; read-only unless specifically authorized
  CONVENTIONS.md                   ← this file
  conventions/                     ← situational rules
  suggestion-box/                  ← atomic proposals for policy-writer
  .claude/skills/                  ← source storage for skill files; NOT an active skill directory
    resume-mission/SKILL.md        ← canonical source; feature worktrees symlink to this
    wind-down/SKILL.md             ← canonical source; feature worktrees symlink to this
  missions/                        ← read-only convenience symlinks
    <mission-name>/
      STATE.md -> worktrees/<mission-name>/.session/STATE.md
      <plan>.md -> worktrees/<mission-name>/.session/<plan>.md
      ledgers/ -> worktrees/<mission-name>/.session/

worktrees/<mission-name>/          ← mission branch/worktree
  .session/                        ← mission state, ledgers, and internal plans; never in a PR branch
  <normal code tree>               ← mission output
```

- Access other missions' tracking files via `session-tracking/missions/<mission-name>/`.
- If a symlink is broken or worktree not present, read directly from the branch:
  ```bash
  git -C <repo-root> show <mission-name>:.session/STATE.md
  ```
- Do not edit another mission's symlinks — report broken links via `session-tracking/suggestion-box/`.

## Ground rules

- Never assume. Ask when the mission, role, scope, authorization, or instruction is unclear.
- Do not silently choose between ambiguous or conflicting instructions; ask.
- Never jump ahead to subsequent numbered items before the current item is explicitly reviewed and approved.
- When presenting changes or edits, always provide exact file paths and line numbers (or diff pointers) so modifications can be reviewed without searching.
- Never push without explicit authorization for that specific push. Authorization is
  single-use. After receiving it, read `conventions/push.md` before pushing.
- Never stop or kill a running background task unless explicitly told to stop that task. A
  request to reduce chat noise is not permission to terminate work.
- Keep long content out of chat. Put long tool output, reports, and file dumps in the mission's
  `.session/` directory or code tree; reply with a short pointer and status.
- Never read plan/spec docs or ledger files at session start. Pull plan docs on demand; consult
  ledger files only when debugging or digging into history. See `conventions/session-start.md`.
- Maintain the session ledger continuously as findings, decisions, corrections, and false
  starts occur.
- Update STATE after each major step — mark completed items `[x]`, update Last completed,
  Next step, and Status. Do not wait for wind-down. Ledger and STATE updates do not need
  chat narration.
- Never edit files outside the mission and role you own.
- Do not use in-place command-line rewriting (`sed -i`, `gawk -i`, Python `fileinput`, or
  equivalents). Normal `Edit`/`Write` operations on owned, git-tracked files are allowed when
  their pre-session state is already checkpointed.
- Destructive actions (`git reset --hard`, `rm -rf`, `git stash drop`, and equivalents) require
  explicit approval for each individual step. If unsure, preserve a backup instead.
