# Conventions — cross-mission, cross-worktree

## Identify your mission and role first

Every session has exactly one mission. Identify mission, role, and ledger before doing anything. If any are unknown, ask.

Read `conventions/session-start.md` to initialize. Mission-owner role: also read `conventions/mission-owner.md`.

- **No tool call may precede the opening orientation.** Before orientation: read CONVENTIONS.md, session-start.md, and STATE — nothing else.

## Work only within your mission worktree

- All writes must target your own mission worktree. No exceptions without explicit user authorization. See `conv-working-outside-worktree`.
- Avoid `cd` in shell commands — use full paths instead. `cd` is only permitted when explicitly entering another worktree for an authorized cross-worktree operation. Never use `cd` to avoid typing a full path, and never use it to route writes around the worktree boundary.
- Never use subshells or shell redirection to route writes around the worktree boundary.
- Reads may cross worktree boundaries: `cat <path>`, `git show <branch>:<path>`. In a pinned session, `git -C` is blocked — use `git show` instead.

## Situational rules — read when triggered

Read the matching file only when its trigger occurs — not speculatively.

**Triggers are hard gates.** STOP. Invoke the named skill (or read the named file). Then acknowledge the single most relevant constraint in one line before continuing — e.g. "I invoked `conv-wip-editing`; key constraint: claim with mv, never cp."

### Role & Mission Setup (Read when establishing mission/role at session start)
- `conventions/session-start.md` — **every session reads this first, before any work**
- `conventions/chat-preferences.md` — every interactive foreground session (read before presenting orientation)
- `conventions/mission-owner.md` — assuming or acting in the mission-owner role
- `conventions/policy-writer.md` — working in any role on the `policy-writer` mission
- `conventions/coder.md` — executing in the coder role
- `conventions/reviewer.md` — executing in the code-reviewer role

### Lifecycle & Session Boundaries
- `conventions/resume-and-handoff.md` — running `/resume-mission` or `/wind-down`, taking over, or ending work
- `conv-feature-worktree-setup` — creating/migrating a mission worktree or setting up missing skill symlinks
- `conv-state-vs-ledger` — creating initial state or ledger files, or unsure which file information belongs in

### Action Triggers (skills auto-invoked on trigger — no manual read needed)
- `conv-agentbus-user-interaction` — background agent communicating with user (progress notes, questions, confirmations)
- `conv-wip-editing` — before editing any file you don't own, or writing a new file into a shared folder
- `conv-working-outside-worktree` — before any cross-worktree read or write operation
- `conv-tasks` — before writing or assigning a task specification to any worker
- `conv-coder-orchestration` — before dispatching or orchestrating a coder agent or background worker
- `conv-install-to-session-tracking` — before installing any file from policy-writer onto session-tracking
- `conv-push` — before executing git push (after receiving explicit single-use approval)
- `conv-pr-branch` — before creating or curating a PR branch
- `conv-pr-workflow` — before opening a PR via the GitHub API
- `conv-unexplained-files` — upon finding an unexplained file or uncommitted edit

## Repo layout

```text
session-tracking/                  ← global policy worktree; read-only unless specifically authorized
  CONVENTIONS.md                   ← this file
  conventions/                     ← situational rules
  suggestion-box/                  ← atomic proposals for policy-writer
  claude-skills/                   ← canonical skill source; every worktree's .claude/skills symlinks here
    resume-mission/SKILL.md
    wind-down/SKILL.md
    conv-*/SKILL.md                ← situational-rule skills (auto-invoked)
    pr-review/SKILL.md
  missions/                        ← read-only convenience symlinks
    <mission-name>/
      STATE.md -> worktrees/<mission-name>/.session/STATE.md
      <plan>.md -> worktrees/<mission-name>/.session/<plan>.md
      ledgers/ -> worktrees/<mission-name>/.session/

worktrees/<mission-name>/          ← mission branch/worktree
  .session/                        ← mission state, ledgers, and internal plans; never in a PR branch
  .claude/skills -> <abs-path>/worktrees/session-tracking/claude-skills  ← dir-symlink
  <normal code tree>               ← mission output
```

- Access other missions' tracking files via `session-tracking/missions/<mission-name>/`.
- If a symlink is broken or worktree not present, read directly from the branch
  (run from inside your own worktree — no `-C` needed):
  ```bash
  git show <mission-name>:.session/STATE.md
  ```
- **If `.claude/skills` is missing or broken in your worktree:** skills live at
  `worktrees/session-tracking/claude-skills/` — read any `SKILL.md` directly from there
  until the symlink is fixed. Invoke `conv-feature-worktree-setup` to repair it.
- Do not edit another mission's symlinks — report broken links via `session-tracking/suggestion-box/`.

## Ground rules

- Never assume. Ask when the mission, role, scope, authorization, or instruction is unclear.
- Do not silently choose between ambiguous or conflicting instructions; ask.
- **Use the narrowest command that achieves the goal.** When a guard fires, ask "is there a safer command?" first.
- Do not override a guard because a task file said to run the original command. Disclose any substitution.
- Never push without explicit authorization for that specific push. Authorization is single-use. After receiving it, read `conventions/push.md`.
- Never stop or kill a running background task unless explicitly told to. A request to reduce chat noise is not permission to terminate work.
- Keep long content out of chat. Write tool output, reports, and file dumps to `.session/` or the code tree; reply with a short pointer and status.
- Never read plan/spec docs or ledger files at session start. Pull plan docs on demand; consult ledger files only when debugging. See `conventions/session-start.md`.
- Maintain the session ledger continuously — findings, decisions, corrections, false starts.
- Update STATE after each major step — mark `[x]`, update Last completed, Next step, Status. Do not wait for wind-down.
- **Before closing a previously-investigated item as resolved:** re-read the prior finding's own text first. If the state is reachable via more than one path, trace every path — not just the most salient one — before declaring the claim closed.
- A durable doc's claim that something "landed" or "is done" is a claim about a past state. Before repeating it to the user or building on it, re-derive from the current code or tests directly.

### Ownership and data safety — read and follow literally

- **No in-place editing of anything**, except files you **100% own** — your own code file, your own plan, your own ledger append. Everything else: write new, then remove/replace old.
- Never remove a file you do not own or did not create without explicit permission.
- Before removing anything, verify its content is captured in the correct location. Do not lose data.
- Destructive actions need step-by-step approval: `git reset --hard`, `rm -rf`, `git rm`, stash drop, and equivalents. If not 100% sure, preserve a backup first.
- Before any `rm` or `rm -rf`: list the exact files first (`ls -la <path>`), confirm each entry is a symlink or a file you created and own, and verify no untracked content would be lost. Never use `rm -rf` on a directory unless you own every file in it and have listed them all.
- Never edit files outside the mission and role you own.
- Do not use in-place command-line rewriting (`sed -i`, `gawk -i`, Python `fileinput`, or equivalents). Normal `Edit`/`Write` on owned, git-tracked files are allowed when their pre-session state is checkpointed.
