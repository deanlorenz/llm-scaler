# Session log — resuming and handing off a mission

Read this when running `/resume-mission` or `/wind-down`, or otherwise taking over or ending
work on a mission.

Every mission's `STATE.md` ends with a **Session log** section: one line per session that
worked the mission, appended under the `.wip` protocol (see `conventions/wip-editing.md`) like
any other `STATE.md` edit.

```
## Session log

- 2026-08-27T14:30 session=<id-or-slug> status=active ledger=ledgers/<name>.md
- 2026-08-27T18:05 session=<id-or-slug> status=retired ledger=ledgers/<name>.md
```

`status` is `active` (currently working the mission right now) or `retired` (**the session is
actually ending** — either via a clean wind-down or a takeover by another session). `retired`
does **not** mean "pausing," "idling," or "checkpointing while continuing" — a session asked by
the user to pause and make sure everything is captured so far, but which is going to keep
working the same mission afterward, stays `active`; marking it `retired` in that situation is
a real mistake (observed directly), not a harmless equivalent. Only mark `retired` when the
session is genuinely done working this mission for now — the ordinary case being an actual
`/wind-down` run. A retired entry is only **fully resolved** once its named ledger file itself
carries a `## Verified <date>` marker (see "ledger-capture" below) — an entry can be `retired`
but not yet verified, e.g. if the laptop closed before wind-down's capture step ran.

**On taking over a mission (via `/resume-mission` or otherwise):**
1. Scan every existing Session log entry. Any entry that is `active`, or `retired` without a
   `## Verified` marker in its ledger file, is **pending** — normal in a clean handoff (its
   ledger may not be verified yet) or a sign of an unclean exit (crash, sleep, force-quit).
   Either way, treat it the same: mark it `retired` in `STATE.md` if it was still `active`,
   then run ledger-capture (see below) against its ledger, in the foreground, before proceeding.
   This is the safety net — capture always eventually happens for every session's ledger,
   regardless of how that session ended.
2. Only after every pending entry is cleared, append this session's own `active` entry and
   proceed to confirm mission/state to the user.

**ledger-capture.** A background agent, launched by the session doing the takeover-scan or by
a session winding down its own work (see the `resume-mission`/`wind-down` skills), given
exactly one ledger file to process. Its job is to **capture**, not just check: read every
point in that ledger entry and confirm each one is reflected somewhere durable — the mission's
`STATE.md`, its plan/spec doc, or (for a genuinely global process point) `CONVENTIONS.md`.
Where something is missing, it fixes it directly (via the `.wip` protocol, same as any other
shared edit) rather than just reporting the gap. This includes structural repairs, not only
missing content: if a doc reference in the ledger — or in whatever it's editing — doesn't
follow the path convention below (a bare filename, a filesystem-absolute path, or a path
that's stale after a doc move), fix the reference itself while it's in scope of what this run
is already touching. It should not go looking for unrelated broken links outside its own
ledger's scope — that's separate cleanup, not this run's job. When done, it appends a marker
to the end of that ledger file:

```
## Verified 2026-08-27 — all points already captured
```
or
```
## Verified 2026-08-27 — folded in: <short list of what was missing and where it was added>
```

This makes ledger-capture useful beyond crash recovery — running it whenever a session is
about to lose working context (compaction, handoff, planned exit) captures that context
durably before it's gone, not only as a fallback for unclean endings.

**Doc-reference path convention.** Every reference from one tracked doc to another (in a
ledger, `STATE.md`, a spec/task doc, an implementation report) must be a **repo-root-relative
path** (e.g. `docs/plans/analyzers/foo.md`), never a filesystem-absolute path and never a bare
filename — a bare filename breaks once files move between a flat layout and a nested one (as
happened in this mission's `session-tracking` migration), while a repo-root-relative path
stays meaningful wherever the referencing content ends up, since mission content is regularly
cherry-picked or merged across branches/worktrees. **State which worktree/branch the path
resolves in** whenever it isn't obvious from context — the same repo-root-relative path can
point to different content (or nothing) depending on which branch's tree it's read against.
A reference that's ambiguous about this is a defect ledger-capture should fix when it's
already touching that content, per the note above.
