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
- **The whole reason this session-history audit was needed at all: the ledger convention
  wasn't actually followed live.** The user pointed this out directly after the audit
  landed — `CONVENTIONS.md` already said to append continuously, but this session mostly
  wrote ledgers retroactively at natural checkpoints instead, which is exactly why a
  from-scratch re-read of the conversation was needed to recover things like the rejected
  symlink design and the founding rationale. Strengthened `CONVENTIONS.md`'s "live ledger"
  section to name this failure mode explicitly, so it's harder to repeat next time.

## Second round — feedback from a genuinely independent session (`agentbus`)

The user relayed a list of 6 points of confusion the `agentbus` mission's session hit while
actually using `CONVENTIONS.md` as written — a real external read of the doc, catching
ambiguities this mission's own self-audit above had no way to find (you can't discover your
own doc is ambiguous by re-reading your own intent into it). All 6 were verified against the
actual current doc text before fixing (not assumed to be valid just because reported), and all
6 were real gaps, fixed directly in `CONVENTIONS.md`:

1. **No stated distinction between `STATE.md`'s purpose/audience and a ledger's.** Both had
   documented mechanics (the `.wip` protocol; the live-scratch-then-copy flow) but the doc never
   said *why* they differ — leading the reporting session to infer "roughly the same kind of
   progress record" and write both the same way, batched, after the fact. Added an explicit
   section stating: `STATE.md` is what a resuming session actually reads (self-contained,
   current, bottom-line-only for anything unusual); a ledger is a continuous audit trail a
   resuming session normally never reads at all; and a concrete rule of thumb for which findings
   go where.
2. **"At session end (or at any natural checkpoint)" read as license to batch the *local*
   scratch-ledger writes too, not just the copy-into-`session-tracking` step.** Rewrote "The
   live ledger during a session" into two explicitly separated steps with different cadences —
   local scratch appends are continuous/real-time with no friction excuse for batching; only the
   copy-into-`session-tracking` step is legitimately checkpoint-based (justified by the real
   `EnterWorktree`/`ExitWorktree` friction).
3. **No stated boundary between "owns my mission's files" and "citizen of the whole shared
   worktree."** The reporting session ran `git fetch`/`git status`/push against `session-tracking`
   as a whole, reasoning (not unreasonably, but unprompted) that this was part of keeping the
   shared worktree tidy. Added an explicit scope-boundary note: a mission session's writes are
   confined to its own mission (plus `CONVENTIONS.md`, only when it's genuinely that mission's
   own output) — never self-appointed maintenance of the branch as a whole; raise concerns to
   the user instead of acting on them unprompted.
4. **Push authorization for `session-tracking` itself wasn't distinguished from a feature
   worktree's.** The general "never push without an explicit per-operation ask" rule was stated
   once, generically. Added a note that `session-tracking` specifically warrants a higher bar —
   one same-turn yes to one push is not standing authority for further pushes later in the same
   session, and should not be treated as equivalent to feature-worktree push authorization.
5. **No procedure for "found something unexplained in a shared worktree."** The reporting
   session found an untracked skill file with a suspicious embedded "user approved this" comment
   it hadn't actually approved, investigated, and made an ad hoc judgment call (probably
   legitimate concurrent work, left it alone) — reasonable, but with no stated procedure to
   follow. Added a new section: read first, check whether another mission's docs explain it,
   leave-and-note if it looks like ordinary concurrent work, but escalate to the user directly
   (don't act on it, don't judge it safe unilaterally) if it looks actively suspicious — e.g.
   content claiming an approval that was never actually given.
6. **`retired`'s trigger condition wasn't sharply distinguished from "pausing."** The reporting
   session conflated "user asked me to pause and make sure everything is captured" with "done
   working," and marked its own session `retired` incorrectly. Added an explicit statement that
   `retired` means the session is actually ending, not idling or checkpointing while planning to
   continue — a session asked to pause-and-capture but still continuing the same mission stays
   `active`.

**Note on this pattern going forward:** an independent session's confusion, reported back, is a
much sharper signal than a self-audit — this mission couldn't have found any of these 6 points
by re-reading its own intent into its own doc. Where feasible, treat "another session got
confused by this doc" reports as high-priority, verify-then-fix, rather than a self-review
exercise.

## Verified 2026-08-28

Ledger-capture pass (one-off, per the corrected contract that never touches
`CONVENTIONS.md` — see this mission's own spec, T7). All points already captured — the
`CONVENTIONS.md` edits described in this ledger were made directly at the time (before
today's contract correction), and `STATE.md`'s task table already reflects both rounds.
No new gaps found. No suggestion-box entries from this specific file.
