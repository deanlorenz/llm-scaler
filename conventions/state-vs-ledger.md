# `STATE.md` vs. a ledger — different purpose, different audience

Read this before writing to `STATE.md` or a ledger, or when unsure which one something
belongs in.

This distinction is easy to miss from the mechanics sections alone (both have a documented
edit protocol, and it's natural to infer from that alone that they're two flavors of the same
"progress record" — they are not):
- **`STATE.md` is what a resuming session actually reads.** It must be self-contained and
  current: current task status, current blockers, the immediate next step, pointers into the
  plan/spec doc's specific sections — enough that a fresh session with zero other context can
  orient and continue. If something unusual happened (an incident, a design reversal, a
  correction), `STATE.md` gets only the **actionable bottom line** — e.g. "rotate the leaked
  keys" — not the story of how it was discovered. It is overwritten, not appended to (except
  its Session log subsection, which is itself a short-lines-only exception, not a narrative).
- **A ledger is a continuous, append-as-you-go audit trail that a resuming session normally
  never reads at all.** It exists to be consulted later, on demand — to recover a lost detail,
  investigate an incident, or (via ledger-capture) confirm nothing load-bearing got dropped —
  not to be read as part of ordinary resumption. It can be long, narrative, and exhaustive
  precisely because reading it is the exception, not the rule.
- **Rule of thumb for what earns a ledger line vs. what belongs in `STATE.md` instead:** a
  ledger line is a real finding, decision, correction, or false start, summarized in your own
  words — not raw tool output, not routine step-by-step narration of things that worked as
  expected. If that finding also changes what a resuming session needs to know or do next, its
  **conclusion** additionally goes into `STATE.md` (short) while the **full story** stays only
  in the ledger (long). Writing the same content into both, at the same length, after the fact,
  in a single batch, is the failure mode this note exists to prevent — it was observed directly
  in a session that inferred "roughly the same kind of record" from the mechanics alone.

## The live ledger during a session

**Two distinct cadences here — do not conflate them.** The local scratch file is appended to
continuously, in real time, as the session works. The copy of that file into `session-tracking`
is what happens at checkpoints. These are different operations with different timing; reading
"checkpoint cadence" as license to also batch the *local* writes is the mistake this section
exists to head off (observed directly: a session read "at session end or at any natural
checkpoint" as covering both, and wrote its local ledger only in large retroactive batches
instead of as things happened).

1. **Append to the local scratch copy after every meaningful finding, decision, correction, or
   false start — as it happens, not in a batch later.** Keep this **live, growing copy** as a
   local scratch file inside whatever feature worktree the session is actually working in
   (e.g. `worktrees/<feature-worktree>/.session/<unique-session-name>.md`), excluded from the
   feature branch's git history via `.git/info/exclude` (this is shared across every worktree
   of the repo, not per-worktree; never committed to the feature branch, never pushed anywhere
   from there). This step never needs a cross-worktree operation — it's an ordinary
   same-worktree file write, so there's no friction excuse for batching it.
2. **At session end (or at any natural checkpoint), copy** the by-then-already-continuously-
   written ledger file verbatim into `missions/<mission>/ledgers/<same-unique-name>.md` in this
   (`session-tracking`) worktree and commit it there. This is a plain file copy, not a merge —
   the unique filename makes a collision impossible. *This* step is legitimately
   checkpoint-based, since a pinned session must `ExitWorktree` to reach this worktree, and that
   real friction is what justifies batching **this** step, and only this one.

**Persist findings and decisions through failures and restarts** — the ledger's whole purpose
is that a session that crashes, gets interrupted, or hands off to a fresh session should still
have a durable trail of what was learned and decided, not just what got merged. Append to it
even when nothing landed — a false start recorded is as valuable as a task completed.

**This means during the session, not only at the end.** A session that does substantial work
and only writes its ledger retroactively, after the fact, defeats the entire point of having
one — it recreates exactly the "reconstruct everything from conversation history" problem this
system exists to avoid, just shifted from "next session's problem" to "this session's problem,
solved by re-reading its own transcript instead of a clean record." (Observed directly: a
session that built this very mechanism largely skipped using it live, then had to re-derive
several real decisions — a rejected design alternative, an operational gotcha, a founding
rationale — by re-reading its own conversation from the start, at the user's explicit prompting,
because nothing had captured them as they happened.) Append after each decision or finding, not
in a single batch when winding down.
