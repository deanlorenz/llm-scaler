# Spec — session-tracking infrastructure

## Intent

Enable resuming work on any mission after a restart — whether the prior session is
found in history and resumed directly, or a brand-new session is told to continue a
named topic — without needing to reload that mission's entire history into context.
Concretely: a cross-mission, cross-worktree tracking branch (`session-tracking`) with a
lightweight `STATE.md` per mission, a heavier reference ledger that most sessions never
need to read, and a mechanism (`ledger-capture`) that ensures anything load-bearing in a
session's ledger actually lands in the durable docs before that session's context is
gone — run automatically on handoff/takeover, not left to manual diligence.

**Founding rationale, stated directly by the user** (before any of this existed — the
prior state was one large, undifferentiated per-mission ledger mixing everything
together): (1) a new session should use a small per-session ledger, not load an entire
prior session's history into context to resume; (2) each mission should have exactly
one plan/roadmap doc and one current-state/summary doc — the full ledger is reference
only, not something a resuming session needs to read; (3) general instructions and
behavioral patterns that had been getting mixed into the ledger belong in a separate
conventions file instead. These three points are the direct ancestor of the
`STATE.md`/spec/`CONVENTIONS.md`/`ledgers/` split below — every structural decision in
this spec traces back to satisfying one of these three.

## Settled design (do not re-litigate without a new decision from the user)

- **Location:** an orphan git branch, `session-tracking`, pushed to `origin` only —
  never `upstream` — checked out in its own dedicated worktree
  (`worktrees/session-tracking`), separate from every feature-work worktree. Orphan
  because it has no reason to share history with any feature branch, and keeping mission
  planning/tracking content off feature branches means it never accidentally goes
  upstream.
- **Layout:** `CONVENTIONS.md` (global, rarely-changing process rules) at the branch
  root; `missions/<mission-name>/` per mission, holding that mission's `STATE.md`
  (current status, overwritten not appended), its plan/spec doc, any reference/
  investigation reports, and a `ledgers/` subdirectory of one file per session that
  worked the mission (append-only, uniquely named, so two sessions never collide on the
  same file).
- **The `.wip` protocol** for editing the two genuinely shared, mutable files
  (`CONVENTIONS.md` and any mission's `STATE.md`): rename `FILE.md` → `FILE.md.wip` to
  atomically claim ownership; copy the `.wip` file to a local scratch path in whatever
  worktree the session is actually working in (cross-worktree writes from a
  pinned-via-`EnterWorktree` session don't work reliably — confirmed by testing, not
  assumed); edit the local copy with ordinary same-worktree tools; copy the finished
  result back over the `.wip` file; rename back to `FILE.md`; commit. The convention
  that only the orchestrating session for a mission ever edits shared files (everyone
  else only reads) is what actually makes concurrent edits rare — `.wip` is the
  mechanical backstop, not the primary safeguard.
- **Session log + ledger-capture:** each mission's `STATE.md` ends with a `## Session
  log` section — one line per session (`status=active` or `status=retired`, pointing at
  that session's ledger file). On taking over a mission, a session scans this log for
  any entry that is `active` (a prior session didn't retire cleanly) or `retired`
  without a `## Verified` marker in its named ledger file (a clean handoff whose capture
  step hasn't run yet). Either case is treated the same — mark it `retired` if needed,
  then run **ledger-capture** against that ledger, in the foreground, before proceeding.
  Ledger-capture's job is to *capture*, not just check: confirm every point in that one
  ledger file lands somewhere durable (`STATE.md`, the mission's plan doc, or, for a
  genuinely global point, `CONVENTIONS.md`), fixing gaps directly rather than just
  reporting them, then appending `## Verified <date> — <summary>` to the ledger. This
  was named "the verifier" originally; renamed to "ledger-capture" after its first real
  run made clear the job is broader than checking.
- **Doc-reference path convention:** every reference from one tracked doc to another
  must be repo-root-relative (never filesystem-absolute, never a bare filename that
  breaks when files move between layouts), and should state which worktree/branch it
  resolves in when not obvious. Discovered as a real gap (59 stale references across 13
  files from the original migration into `session-tracking`) but deliberately **not**
  fixed as a one-off manual sweep — left for a future ledger-capture run to fix while
  it's already touching that content, since that's exactly the class of repair
  ledger-capture is scoped to make.
- **`/resume-mission` and `/wind-down` skills** are the user-facing entry points that
  drive all of the above: `/resume-mission [topic]` finds the mission, clears any
  pending sessions via ledger-capture, enters the feature worktree, confirms
  mission/state to the user, and logs its own `active` entry; `/wind-down` is the
  symmetric close — safe stopping point, own ledger entry, `STATE.md` update if
  warranted, commit, ledger-capture on its own ledger (foreground, waited-for — this is
  what makes "safe to close" a real guarantee), mark own entry `retired`, report.
- **Skill discoverability, confirmed by direct testing, not assumed:** Claude Code's
  project-skill discovery does not walk up past a single git worktree's own root — not
  to a plain filesystem parent directory, not to the worktree's main repo. Each feature
  worktree that wants `/resume-mission`/`/wind-down` needs its own local symlink into
  `session-tracking`'s canonical `.claude/skills/{resume-mission,wind-down}`, added to
  the (repo-shared, not per-worktree — see below) `.git/info/exclude` so the symlink
  never gets committed to that feature branch.
- **`.git/info/exclude` is shared across every worktree of a repo**, not per-worktree —
  confirmed via `git rev-parse --git-common-dir` resolving to the same main `.git` for
  every worktree tested. `CONVENTIONS.md` originally stated the opposite (assumed rather
  than verified) and was corrected once this was actually tested.
- **`pr-review` skill disabled locally:** the user doesn't want it available, but it's
  pre-existing upstream content (authored by someone else, PR #1039/#1041/#1078, present
  on 30+ branches) that must stay untouched in git history. Resolved via
  `~/.claude/settings.json`'s `skillOverrides: {"pr-review": "off"}` — a global, personal
  setting, zero git changes anywhere, no per-branch/per-worktree duplication needed
  (unlike the skill files themselves, which are per-worktree-discoverable and needed the
  symlink treatment above).

## Todo

### T1 — Branch, worktree, and layout
**Status.** DONE 2026-08-27. Orphan `session-tracking` branch created, pushed to
`origin`. `CONVENTIONS.md` written. `analyzer-optimizer-refactor`'s pre-existing docs
migrated in verbatim as the first mission (byte-identical, verified via `diff` before
removing the originals from `single-analyzer`).

### T2 — `.wip` protocol
**Status.** DONE 2026-08-27, documented in `CONVENTIONS.md`. Not yet exercised under
genuine concurrent access — only used single-threaded so far. Revisit if that ever
becomes a real problem.

### T3 — Session log format + ledger-capture
**Status.** DONE 2026-08-27, validated once. First real run audited the old 94KB
`ledger-analyzer-optimizer-refactor.md`, found 2 genuine gaps (a global behavioral rule
not yet generalized into `CONVENTIONS.md`; a confirmed bug finding cited only by ledger
section number rather than stated/linked from the durable docs), fixed both directly,
committed as `2b4927ba`. Renamed "verifier" → "ledger-capture" afterward (`14ce29d3`) —
the run itself is what revealed the name undersold the job.

### T4 — `/resume-mission` and `/wind-down` skills
**Status.** DONE 2026-08-27 (`8942841d`, path-corrected `8ffa77e0`). **Not yet tested
end-to-end** — neither skill has actually been invoked by a session since being
written. This is a real gap, not just an unexercised edge case: the first real
`/resume-mission` or `/wind-down` invocation should be treated as a live test, and any
step that doesn't work as documented should be fixed and noted here.

### T5 — Skill discoverability across worktrees
**Status.** IN PROGRESS. Discovery mechanism confirmed by direct testing (does not walk
up past a worktree's own root). Symlink pattern documented in `CONVENTIONS.md`,
`resume-mission`'s own Step 5 self-heals it for whatever worktree it enters. **Only
`single-analyzer` has actually had the one-time setup done.** Every other existing
feature worktree (six `benchmark-*`, `fix-scaledobjects-cold-start`, and any future
ones) needs the same setup before these skills work there — will happen naturally as
`/resume-mission` is used in each, per its self-healing Step 5, or can be done proactively.

### T6 — `pr-review` suppression
**Status.** DONE 2026-08-27. `skillOverrides: {"pr-review": "off"}` in
`~/.claude/settings.json`. Skill files themselves untouched everywhere, per the user's
explicit instruction that upstream content stays as-is.

## Refs

*Reads/writes:* `../../CONVENTIONS.md` (this mission's primary deliverable),
`../../.claude/skills/resume-mission/SKILL.md`, `../../.claude/skills/wind-down/SKILL.md`.

## Open items

- End-to-end test of `/resume-mission` and `/wind-down` — genuinely not done yet (see
  T4).
- Symlink setup for feature worktrees other than `single-analyzer` (see T5).
- The 59 stale doc-reference paths from the original migration — deliberately deferred
  to a future ledger-capture run (see `CONVENTIONS.md`'s doc-reference path convention
  section), not tracked as a task here since it's not this mission's code to fix, it's
  content ledger-capture will fix incidentally.
