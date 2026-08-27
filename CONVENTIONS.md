# Conventions — cross-mission, cross-worktree

Global process/behavioral rules for every mission tracked on this branch. Static — update
only when a rule itself changes, not when any mission's state changes. This file and
`missions/*/STATE.md` are the two shared, mutable, multi-session-visible files in this branch
— see "Editing shared files safely" below before touching either.

This branch (`session-tracking`) holds mission plans, mission state, and session ledgers —
never pushed to `upstream`, only to `origin`. It is checked out in its own dedicated worktree
(`worktrees/session-tracking/`), separate from every feature-work worktree, because it is an
orphan branch with unrelated history and git worktrees are 1:1 with branches.

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

## Repo layout on this branch

```
session-tracking/
  CONVENTIONS.md                  # this file — global, edit with care
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

## Editing shared files safely — the `.wip` protocol

`STATE.md` and `CONVENTIONS.md` are the only files here more than one session might want to
write. The design goal is to make concurrent writes to them rare by convention (see
"Who writes what" below) — the `.wip` protocol below is the mechanical backstop, not a
substitute for that convention.

1. **Only the orchestrating session for a mission edits that mission's `STATE.md`.** Only a
   session explicitly asked to update global conventions edits `CONVENTIONS.md`. Every other
   session/agent only *reads* these files.
2. **Claim ownership, atomically, in this worktree:** rename `FILE.md` → `FILE.md.wip`. The
   rename itself is the atomic claim — whoever successfully renames it owns the edit.
3. **Edit via a local copy, not repeated cross-worktree edits.** A session pinned to a
   different worktree (via `EnterWorktree`) cannot reliably write here directly, and even an
   unpinned session shouldn't make every single line-edit a cross-worktree operation. Instead:
   copy `FILE.md.wip` to a scratch path inside the worktree the session is actually working in
   (e.g. `worktrees/<feature-worktree>/.session/FILE.md.local`), make all edits there with
   ordinary same-worktree `Edit`/`Write` calls, and only copy the finished result back over
   `FILE.md.wip` here when done.
4. While `FILE.md.wip` exists and `FILE.md` is absent, that is the visible signal "this file
   is being edited right now." Other sessions must not start their own edit of it — they can
   still read the last-committed version (`git show HEAD:missions/<m>/STATE.md`) or peek at
   the in-progress `FILE.md.wip` directly; reads are never blocked.
5. **To finish:** copy the edited local copy back over `FILE.md.wip` here, then rename
   `FILE.md.wip` back to `FILE.md` (atomic — only meaningful/safe because this session is the
   one that holds the claim), `git add`, commit.
6. `*.md.wip` and any `.session/` scratch dir are excluded via each worktree's own local
   `.git/info/exclude` (not a tracked `.gitignore` — this is local-only bookkeeping, not a repo
   convention to publish) so an accidental broad `git add` never picks up a mid-edit file.

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

## The live ledger during a session

A session's ledger is written continuously (findings, decisions, false starts, corrections),
so making every append a cross-worktree operation is unnecessary friction for a file nothing
else ever touches. Keep the **live, growing copy** as a local scratch file inside whatever
feature worktree the session is actually working in (e.g.
`worktrees/<feature-worktree>/.session/<unique-session-name>.md`), excluded from that
worktree's git history via its own local `.git/info/exclude` (never committed to the feature
branch, never pushed anywhere from there).

At session end (or at any natural checkpoint), copy the ledger file verbatim into
`missions/<mission>/ledgers/<same-unique-name>.md` in this (`session-tracking`) worktree and
commit it there. This is a plain file copy, not a merge — the unique filename makes a
collision impossible. Since a pinned session must `ExitWorktree` to reach this worktree
anyway (see above), doing the copy+commit as one step at natural checkpoints — rather than
continuously — is the practical cadence.

**Persist findings and decisions through failures and restarts** — the ledger's whole purpose
is that a session that crashes, gets interrupted, or hands off to a fresh session should still
have a durable trail of what was learned and decided, not just what got merged. Append to it
even when nothing landed — a false start recorded is as valuable as a task completed.

## Coding-task orchestration (when running/orchestrating a coder)

1. Every session and every subagent has a role and a mission — never overstep it, and never
   assign the wrong one to a subagent. Before delegating, ask "what is this agent's actual
   mission" and only hand it work that fits — if unsure, ask the user rather than guessing.
2. Never push to git, never publish to GitHub (PRs, etc.) without an explicit per-operation ask
   — not a standing permission, not inferred from an earlier approval.
3. One task at a time — do not batch multiple tasks into one agent invocation expecting it to
   self-sequence unsupervised.
4. Each task gets a written spec before the coder starts (see the task template below).
5. Each task lands as its own commit — not batched, not squashed across tasks.
6. **Coder isolation:** launch coders with `isolation: "worktree"` — a separate git worktree so
   editing has zero visible effect on the user's actually-open worktree/IDE. These land under
   `.claude/worktrees/agent-<id>` (tool-managed, disposable) — record that path in the
   mission's `STATE.md` under "worktrees used"; they don't need to follow the
   `session-tracking` layout themselves.
7. **Review isolation:** the review agent for a coder's task runs against that *same* coder
   worktree (not a third worktree, not the user's open one).
8. The orchestrating session reviews each task's diff itself before starting the next task —
   not delegated to the coder, not skipped.
9. Once a task's coding + review are both satisfied, the orchestrating session
   merges/cherry-picks the approved commit into the real target branch itself.
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

## Session log — resuming and handing off a mission

Every mission's `STATE.md` ends with a **Session log** section: one line per session that
worked the mission, appended under the `.wip` protocol like any other `STATE.md` edit.

```
## Session log

- 2026-08-27T14:30 session=<id-or-slug> status=active ledger=ledgers/<name>.md
- 2026-08-27T18:05 session=<id-or-slug> status=retired ledger=ledgers/<name>.md
```

`status` is `active` (currently working the mission right now) or `retired` (done working,
whether via a clean wind-down or a takeover by another session). A retired entry is only
**fully resolved** once its named ledger file itself carries a `## Verified <date>` marker
(see "The verifier" below) — an entry can be `retired` but not yet verified, e.g. if the
laptop closed before wind-down's verification step ran.

**On taking over a mission (via `/resume-mission` or otherwise):**
1. Scan every existing Session log entry. Any entry that is `active`, or `retired` without a
   `## Verified` marker in its ledger file, is **pending** — normal in a clean handoff (its
   ledger may not be verified yet) or a sign of an unclean exit (crash, sleep, force-quit).
   Either way, treat it the same: mark it `retired` in `STATE.md` if it was still `active`,
   then run the verifier (see below) against its ledger, in the foreground, before proceeding.
   This is the safety net — verification always eventually happens for every session's ledger,
   regardless of how that session ended.
2. Only after every pending entry is cleared, append this session's own `active` entry and
   proceed to confirm mission/state to the user.

**The verifier.** A background agent, launched by the session doing the takeover-scan or by a
session winding down its own work (see the `resume-mission`/`wind-down` skills), given exactly
one ledger file to check. Its job is to **capture**, not just check: read every point in that
ledger entry and confirm each one is reflected somewhere durable — the mission's `STATE.md`,
its plan/spec doc, or (for a genuinely global process point) `CONVENTIONS.md`. Where something
is missing, the verifier fixes it directly (via the `.wip` protocol, same as any other shared
edit) rather than just reporting the gap. When done, it appends a marker to the end of that
ledger file:

```
## Verified 2026-08-27 — all points already captured
```
or
```
## Verified 2026-08-27 — folded in: <short list of what was missing and where it was added>
```

This makes the verifier useful beyond crash recovery — running it whenever a session is about
to lose working context (compaction, handoff, planned exit) captures that context durably
before it's gone, not only as a fallback for unclean endings.

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
