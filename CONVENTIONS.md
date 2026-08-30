# Conventions — cross-mission, cross-worktree

Global process/behavioral rules for every mission tracked on this branch. Update only when a
rule itself changes, not when any mission's state changes. This file and `missions/*/STATE.md`
are the two shared, mutable, multi-session-visible files in this branch — see
`conventions/wip-editing.md` before touching either.

This branch (`session-tracking`) holds mission plans, mission state, and session ledgers.
Checked out in its own dedicated worktree (`worktrees/session-tracking/`), separate from every
feature-work worktree.

**Remote convention, repo-wide (not just this branch).** Push only to `origin`. `upstream` and
`ofer` have push disabled. Confirm with `git remote -v` before assuming push behavior in a new
worktree.

**Worktree pinning.** A session pinned to a feature worktree via `EnterWorktree` can read any
other worktree's path but cannot write to it. `ExitWorktree` returns to the repo root;
re-entering a feature worktree afterward requires interactive user authorization each time — do
not rely on it as a free round-trip mid-task. If a cross-worktree write is needed mid-session,
ask the user first.

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
- `conventions/feature-worktree-setup.md` — setting up a new feature worktree for a mission, or
  `/resume-mission`/`/wind-down` found missing in one
- `conventions/coder-orchestration.md` — about to dispatch or run a coder subagent
- `conventions/settings-and-skill-edits.md` — about to edit `~/.claude/settings.json` or a
  `SKILL.md`
- `conventions/unexplained-files.md` — found something on disk you didn't put there and can't
  explain

## Repo layout on this branch

```
session-tracking/
  CONVENTIONS.md                  # this file — global, edit with care
  conventions/                    # situational rules, read on demand — see above
  missions/
    <mission-name>/
      STATE.md                    # per-mission current state — edit with care
      spec docs, task docs, implementation reports, investigation reports  # mission content
      ledgers/
        <unique-session-name>.md  # one file per session, append-only, never shared, no conflict risk by construction
```

A mission can span or outlive a specific feature-worktree, and can have more than one
feature-worktree active in parallel (usually meaning parallel sub-task work) — `STATE.md`
lists which worktree(s) are currently in use for the mission.

This branch also has its own `.claude/skills/` — currently `resume-mission` and `wind-down`
(see `conventions/resume-and-handoff.md` for what they do). This is their canonical, tracked
home.

## Who writes what

- **Orchestrating session for a mission:** owns that mission's `STATE.md` and its spec/task
  docs. Writes its own ledger file. Commits after any real decision, not on every small edit.
- **Sub-task / coder / reviewer sessions and agents:** read the mission's `STATE.md` and spec
  docs for context. Never edit them. Report status by appending to their own uniquely-named
  ledger file. If `STATE.md` needs to change, say so back to the orchestrating session rather
  than editing directly.
- **Ledgers are per-session and uniquely named** (e.g. `<date>-<short-slug>.md`).

**Scope boundary.** A mission session's writes are confined to its own mission's
`STATE.md`/spec/ledger (plus `CONVENTIONS.md`, only when the work is genuinely `policy-writer`'s
own mission output). Not the maintainer of `session-tracking` as a whole: don't run `git
fetch`/`git status` against `session-tracking` to check its overall health, don't push it to
stay current, don't treat anything outside your own mission's directory as yours to groom. If
something about the shared worktree needs attention, raise it with the user rather than acting
on it unprompted.

**Pushing `session-tracking` itself needs its own explicit ask each time** — a same-turn "yes"
to one push is not standing authority for a later push, and is not equivalent to authorization
for a feature-worktree push. If in doubt, ask again, or route the question to whoever the user
indicates maintains that branch.

## Ground rules

- Never assume — ask clarifying questions when unsure.
- Long text stays out of chat. Any long tool output, subagent report, or file dump goes into a
  document under this branch's mission dirs — never pasted inline in chat. Chat replies stay
  short: pointers to where the detail lives, not the detail itself.
- Don't ignore instructions — ask if a user instruction seems ambiguous or in tension with
  something else, rather than silently picking an interpretation.
- **Never stop/kill a running background task unless explicitly told to stop *that task*.** A
  complaint about chat noise means suppress the commentary, not terminate the work.
- **Ledger/state appends happen silently.** No chat reply is needed just to narrate that a
  ledger/`STATE.md`/spec-doc write happened.
