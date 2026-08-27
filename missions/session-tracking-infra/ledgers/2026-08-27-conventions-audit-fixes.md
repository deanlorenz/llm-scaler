# Session ledger — 2026-08-27 — CONVENTIONS.md audit fixes

## What happened

At the user's request, ran a pass over this session's actual conversation history from
the start, checking every load-bearing point against the three mission docs
(`analyzer-optimizer-refactor`, `session-tracking-infra`, `repo-restructure`) — the same
spirit as ledger-capture, but against live conversation history rather than a written
ledger file, since none of the three missions' own ledgers had been processed yet.

Found 5 gaps. 3 were genuinely this mission's own policy/process content and belong in
`CONVENTIONS.md` (this file's edits); 2 were plain mission-tracking content for other
missions, committed separately as ordinary updates (see `e18d8733`).

The 3 `CONVENTIONS.md` additions, made as this mission's own work (not as a plain user
of the tracking system):

1. **Rejected symlink-locking design alternative**, added to the `.wip` protocol
   section — a real alternative (symlink-swap for shared-file concurrency) was proposed
   and explicitly rejected earlier this session, with reasoning (no real exclusion,
   risk of committing a broken symlink), but that reasoning had only ever existed in an
   unprocessed session ledger, not in the durable convention it actually informed.
2. **Settings-guard marker-requirement operational note**, added as a new section — a
   real, repeatable friction pattern (every edit to `~/.claude/settings.json` or a
   `SKILL.md` needs the literal approval marker text physically present in that edit's
   new content, including removal edits, which naively loops forever) that any future
   session editing these files will hit again. Documented the working pattern found by
   trial: place the marker somewhere inert and stop chasing full removal, since further
   edits only need the marker present *somewhere* in the file, satisfied automatically
   when an edit's old-string region already contains a prior instance.
3. **`origin`/`upstream`/`ofer` remote-push convention promoted to global** — this fact
   was previously only stated inline in `repo-restructure`'s spec, but it's a repo-wide
   convention (every worktree of this repo shares the same three remotes with the same
   push restrictions), not specific to that one mission.

## Not yet done

- The 5th gap identified in this same audit pass (that this mission itself had, until
  now, only ever recorded its founding rationale and remote convention — see the
  separate plain-tracking commit for those) is already resolved by the companion
  commit; nothing outstanding from this specific audit pass.
- This ledger itself has not been through ledger-capture (per the user's own framing:
  "we will process the ledgers later" — this is a fresh entry, not yet verified).

## Corrections/false starts worth remembering

- **Distinguish "editing as a user of session-tracking" from "editing as the
  session-tracking-infra mission."** Editing another mission's `STATE.md`/spec with
  routine tracking content is normal, expected use of the system by any mission.
  Editing `CONVENTIONS.md` itself is *session-tracking-infra's own mission output* and
  should be committed/ledgered as such, separately — conflating the two in one commit
  was flagged directly by the user as wrong, mid-task, before committing.
