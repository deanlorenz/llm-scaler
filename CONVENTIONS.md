# Conventions — cross-mission, cross-worktree

Global process/behavioral rules for every mission tracked on this branch. Update only when a
rule itself changes, not when any mission's state changes. This file and the skills here are
maintained by the `policy-writer` mission. See `conventions/wip-editing.md` before editing
this file.

This branch (`session-tracking`) holds global conventions, skills, and a suggestion box.
Checked out in its own dedicated worktree (`worktrees/session-tracking/`), separate from every
mission worktree.

**Remote convention, repo-wide (not just this branch).** Push only to `origin`. `upstream` and
`ofer` have push disabled. Confirm with `git remote -v` before assuming push behavior in a new
worktree.

**Worktree pinning.** A session pinned to a worktree via `EnterWorktree` can read any other
worktree's path but cannot write to it. `ExitWorktree` returns to the repo root; re-entering
a worktree afterward requires interactive user authorization each time — do not rely on it as
a free round-trip mid-task. If a cross-worktree write is needed mid-session, ask the user
first.

**This file is not auto-loaded.** A fresh session must be explicitly told to read
`CONVENTIONS.md` (and the relevant mission's `STATE.md`) by full path. Say so when handing off
to a new session or subagent.

---

## Situational rules — read on demand

Most of what used to live in this file only applies in a specific situation. Read the matching
file below when that situation actually comes up — not upfront, not speculatively.

- `conventions/wip-editing.md` — about to edit `STATE.md` or `CONVENTIONS.md`
- `conventions/state-vs-ledger.md` — about to write to `STATE.md` or a ledger, or unsure which
  one something belongs in
- `conventions/resume-and-handoff.md` — running `/resume-mission` or `/wind-down`; taking over
  or ending work on a mission
- `conventions/feature-worktree-setup.md` — setting up a new mission worktree, or
  `/resume-mission`/`/wind-down` found skills missing in one
- `conventions/coder-orchestration.md` — about to dispatch or run a coder subagent
- `conventions/settings-and-skill-edits.md` — about to edit `~/.claude/settings.json` or a
  `SKILL.md`
- `conventions/unexplained-files.md` — found something on disk you didn't put there and
  can't explain
- `conventions/pr-branch.md` — about to create a PR branch or open a PR
- `conventions/pr-workflow.md` — about to open a PR or prepare a branch for one

## Repo layout

```
session-tracking/                  ← this branch
  CONVENTIONS.md                   ← this file — global, edit with care (policy-writer only)
  conventions/                     ← situational rules, read on demand — see above
  suggestion-box/                  ← shared, any mission can post here
  missions/
    <mission-name>/
      STATE.md -> worktrees/<mission-name>/.session/STATE.md   ← symlink (read-only, convenience)
      <plan>.md -> worktrees/<mission-name>/.session/<plan>.md  ← symlink per internal plan
      ledgers/ -> worktrees/<mission-name>/.session/            ← symlink to ledger dir

worktrees/<mission-name>/          ← mission branch (branch name = worktree name)
  .session/                        ← mission tracking files; NEVER in a PR branch
    STATE.md                       ← current state — mission owner reads/writes locally
    <session-slug>.md              ← per-session ledger, append-only
    <internal-plan>.md             ← internal plans not destined for any PR
  <normal code tree>               ← shareable content: proposals, dev guides, external plans
```

Mission tracking files (`STATE.md`, ledgers, internal plans) live in the **mission's own
branch/worktree** under `.session/` — not in `session-tracking`. The symlinks in
`session-tracking/missions/<name>/` are a read-only convenience; if the worktree is not
checked out locally, the symlink path encodes the branch name so the files can be retrieved
via `git show <mission-name>:.session/STATE.md`.

`policy-writer` creates and commits the symlinks in `session-tracking` — mission owners do not
need to commit `session-tracking` themselves.

## Who writes what

- **Mission owner session:** owns that mission's `.session/STATE.md` and its internal plans.
  Writes its own ledger. Cherry-picks approved code from coder worktrees into the mission
  branch. Commits directly to the mission branch and pushes to `origin`. Declares/releases
  ownership on agentbus (see `conventions/resume-and-handoff.md`).
- **Sub-task / coder / reviewer sessions and agents:** read the mission's `STATE.md` and spec
  docs for context (via the filesystem path or the `session-tracking` symlink). Never edit
  them. Report status by appending to their own uniquely-named ledger file. If `STATE.md`
  needs to change, say so back to the mission owner rather than editing directly.
- **Ledgers are per-session and uniquely named** (e.g. `<date>-<short-slug>.md`), stored in
  `.session/` on the mission branch.

**Scope boundary.** A mission session's writes are confined to its own mission branch and
`.session/` dir (plus `CONVENTIONS.md`, only when the work is genuinely `policy-writer`'s own
mission output). Not the maintainer of `session-tracking` as a whole — that's `policy-writer`.
Don't run `git fetch`/`git status` against `session-tracking` to check its overall health,
don't push it, don't treat anything outside your own mission's `.session/` as yours to groom.
If something about `session-tracking` needs attention, raise it with the user rather than
acting on it unprompted.

**Pushing `session-tracking` itself needs its own explicit ask each time** — a same-turn "yes"
to one push is not standing authority for a later push, and is not equivalent to authorization
for a mission-branch push. If in doubt, ask again, or route the question to whoever the user
indicates maintains that branch.

## Ground rules

- Never assume — ask clarifying questions when unsure.
- Long text stays out of chat. Any long tool output, subagent report, or file dump goes into a
  document under the mission's `.session/` or code tree — never pasted inline in chat. Chat
  replies stay short: pointers to where the detail lives, not the detail itself.
- Don't ignore instructions — ask if a user instruction seems ambiguous or in tension with
  something else, rather than silently picking an interpretation.
- **Never stop/kill a running background task unless explicitly told to stop *that task*.** A
  complaint about chat noise means suppress the commentary, not terminate the work.
- **Ledger/state appends happen silently.** No chat reply is needed just to narrate that a
  ledger/`STATE.md`/spec-doc write happened.
- **Agentbus ownership.** When taking over a mission, declare ownership on agentbus on the
  `mission.<mission-name>` topic before starting work. Release it when winding down. See
  `conventions/resume-and-handoff.md` for the exact calls.
