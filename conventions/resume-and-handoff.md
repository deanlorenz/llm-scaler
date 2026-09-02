# Session log — resuming and handing off a mission

Read this when running `/resume-mission` or `/wind-down`, or otherwise taking over or ending
work on a mission.

## Where mission state lives

`STATE.md` and ledger files live in the mission's own branch/worktree at
`<mission-worktree>/.session/`. Access them directly from that path. If the mission worktree
is not checked out locally, `session-tracking/missions/<mission-name>/` holds symlinks — the
symlink path encodes the branch name so you can reconstruct via:

```bash
git -C <repo-root> show <mission-name>:.session/STATE.md
```

## Session log format

Every mission's `STATE.md` ends with a **Session log** section: one line per session that
worked the mission, appended under the `.wip` protocol (see `conventions/wip-editing.md`) like
any other `STATE.md` edit.

```
## Session log

- 2026-08-27T14:30 session=<id-or-slug> status=active ledger=.session/<name>.md owner=<agentbus-session-id>
- 2026-08-27T18:05 session=<id-or-slug> status=retired ledger=.session/<name>.md
```

`status` is `active` (currently working the mission right now) or `retired` (**the session is
actually ending** — either via a clean wind-down or a takeover by another session). `retired`
does **not** mean "pausing," "idling," or "checkpointing while continuing" — a session that will
keep working the same mission afterward stays `active`. Only mark `retired` when the session is
genuinely done working this mission for now. A retired entry is only **fully resolved** once its
named ledger file itself carries a `## Verified <date>` marker (see "ledger-capture" below).

## Agentbus ownership

When a session takes ownership of a mission (via `/resume-mission` or otherwise), it declares
ownership on agentbus before starting work:

```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<slug> taking ownership of <mission-name>")
```

When winding down, publish a matching release:

```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<slug> releasing ownership of <mission-name>")
```

This makes mission ownership visible to any other session watching the bus — not just recorded
in `STATE.md`.

## On taking over a mission (via `/resume-mission` or otherwise)

1. Scan every existing Session log entry. Any entry that is `active`, or `retired` without a
   `## Verified` marker in its ledger file, is **pending** — normal in a clean handoff (its
   ledger may not be verified yet) or a sign of an unclean exit (crash, sleep, force-quit).
   Either way, treat it the same: mark it `retired` in `STATE.md` if it was still `active`,
   then run ledger-capture (see below) against its ledger, in the foreground, before proceeding.
   This is the safety net — capture always eventually happens for every session's ledger,
   regardless of how that session ended.
2. Only after every pending entry is cleared, declare ownership on agentbus, append this
   session's own `active` entry, and proceed to confirm mission/state to the user.

## ledger-capture

A background agent, launched by the session doing the takeover-scan or by a session winding
down its own work (see the `resume-mission`/`wind-down` skills), given exactly one ledger file
to process.

**ledger-capture must never write to `CONVENTIONS.md`.** Its only two legitimate write
destinations are the mission's own `STATE.md` and its plan/spec doc. Only `policy-writer` may
change `CONVENTIONS.md`, and not while it is itself running ledger-capture on its own ledgers.

When ledger-capture finds something that looks like it should become a global rule or
behavioral directive (previously the kind of thing it might have written into
`CONVENTIONS.md`), it writes one atomic markdown file per individual finding into
`session-tracking/suggestion-box/`, named `YYYY-MM-DD-HHMM-<mission-name>.md`. Only
`policy-writer` reads the suggestion-box and decides whether and how to turn a suggestion into
an actual `CONVENTIONS.md` rule.

1. Read every point in that ledger file and confirm each one is reflected somewhere durable —
   the mission's `STATE.md` or its plan/spec doc. Where something is missing, fix it directly
   (via the `.wip` protocol), rather than just reporting the gap. For anything that looks like
   a global rule or process correction, write to `suggestion-box/` instead (see above).
2. Also fix any doc-reference path in scope of what you're already touching that violates the
   path convention below (a bare filename, a filesystem-absolute path, a path stale after a doc
   move) — but don't go looking for unrelated broken links outside this ledger's own scope.
3. When done, append a marker to the end of that ledger file:

```
## Verified 2026-08-27 — all points already captured
```
or
```
## Verified 2026-08-27 — folded in: <short list of what was missing and where it was added>
```

Run ledger-capture any time a session is about to lose working context (compaction, handoff,
planned exit), not only for an unclean ending.

## Doc-reference path convention

Every reference from one tracked doc to another must be a **repo-root-relative path**
(e.g. `worktrees/policy-writer/.session/STATE.md`) — never a filesystem-absolute path, never
a bare filename. **State which worktree/branch the path resolves in** whenever it isn't obvious
from context.
