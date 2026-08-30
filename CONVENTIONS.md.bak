# Conventions — cross-mission, cross-worktree

Global process/behavioral rules for every mission tracked on this branch. Static — update
only when a rule itself changes, not when any mission's state changes. This file and
`missions/*/STATE.md` are the two shared, mutable, multi-session-visible files in this branch
— see `conventions/wip-editing.md` before touching either.

This branch (`session-tracking`) holds mission plans, mission state, and session ledgers —
never pushed to `upstream`, only to `origin`. It is checked out in its own dedicated worktree
(`worktrees/session-tracking/`), separate from every feature-work worktree, because it is an
orphan branch with unrelated history and git worktrees are 1:1 with branches.

**Remote convention, repo-wide (not just this branch).** This repo has three remotes:
`origin` (the user's own fork, push-enabled — the only one anything is ever pushed to),
`upstream` (the real upstream project), and `ofer` (a collaborator's fork) — the latter two
both have their push URL deliberately set to the literal string `DISABLED-no-push`, so a
push attempt to either fails structurally rather than relying on remembering not to. Every
worktree of this repo shares this same remote configuration. Confirm with `git remote -v`
before assuming push behavior in a new worktree, but expect this convention to hold.

**Reaching this worktree from a pinned session.** A session that entered a feature worktree
via `EnterWorktree` is structurally blocked from writing to any other worktree's path (reads
are still allowed while pinned — only writes are blocked). `ExitWorktree` gets a pinned
session back to the repo root, but re-entering the feature worktree afterward
(`EnterWorktree` with `path`) requires interactive user authorization each time — it is **not**
a free, automated round-trip a session can rely on mid-task. Treat a pinned session as staying
pinned for its whole run; don't plan work that assumes it can hop out to edit here and hop
back unattended. If cross-worktree edits are actually needed mid-session, ask the user first
rather than attempting the round-trip and assuming it will succeed.

**A fresh session started inside a worktree does not automatically read this file.** It must
be explicitly told to read `CONVENTIONS.md` (and the relevant mission's `STATE.md`) by full
path — there is no auto-discovery. Say so plainly when handing off to a new session or
subagent: give it the full path and tell it to read this file first.

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
  docs for context. Never edit them. Report their own status by appending to their own
  uniquely-named ledger file — never by writing into shared state. If a sub-task session
  believes `STATE.md` itself needs to change, it says so back to the orchestrating session
  rather than editing directly.
- **Ledgers are per-session and uniquely named** (e.g. `<date>-<short-slug>.md`) specifically
  so no two sessions ever write the same ledger file — this is what actually prevents
  conflicts, not which worktree the edit happens from.

**Scope boundary: your mission's files, full stop — not "citizen of this worktree, keep it
tidy."** A mission session's writes are confined to its own mission's `STATE.md`/spec/ledger
(plus `CONVENTIONS.md`, but only when the work genuinely is `session-tracking-infra`'s own
mission output — see that mission's own note on this). A mission session is **not** the
maintainer of `session-tracking` as a whole, and should not act like one: don't run `git
fetch`/`git status` against `session-tracking` to check its overall health, don't decide
independently that the branch "needs" a push to stay current, don't treat anything outside
your own mission's directory as yours to groom. If something about the shared worktree itself
seems to need attention (stale content, a questionable file, the branch falling behind
`origin`), that's a question to raise with the user, not a maintenance task to take on
unprompted — this was observed as a real failure mode: a session fetched against `origin` and
pushed the whole branch on its own initiative, reasoning (not incorrectly, but without being
asked) that this was part of being a good citizen of the worktree it happened to be using.

**Pushing `session-tracking` itself needs a higher bar than pushing a feature worktree.** The
general rule ("never push without an explicit per-operation ask") applies to every push, but
`session-tracking` specifically is shared, cross-mission infrastructure — a single mission
session getting a same-turn "yes" to one push should not be read as standing authority to push
again later in the same session, and should not be treated as equivalent to authorization for
a feature-worktree push. If in doubt, ask again for `session-tracking` specifically, or route
the question to whoever the user indicates actually maintains that branch, rather than treating
one earlier yes as settled.

## Ground rules

- Never assume — ask clarifying questions when unsure.
- Long text stays out of chat. Any long tool output, subagent report, or file dump goes into a
  document under this branch's mission dirs — never pasted inline in chat. Chat replies stay
  short: pointers to where the detail lives, not the detail itself.
- Don't ignore instructions — ask if a user instruction seems ambiguous or in tension with
  something else, rather than silently picking an interpretation.
- **Never stop/kill a running background task unless explicitly told to stop *that task*.** A
  complaint about chat noise (e.g. "stop cluttering my chat") is about narration, not execution —
  it means suppress the running commentary, not terminate the work. Don't infer "kill it" from
  "it's noisy"; ask if unsure which is meant.
- **Ledger/state appends happen silently.** Writing a finding to a ledger, `STATE.md`, or a spec
  doc does not itself need a matching chat reply narrating "I just logged X." Chat replies carry
  new substance (answers, questions, content for the user to react to) — not a turn-by-turn
  description of bookkeeping that already happened in the file.
