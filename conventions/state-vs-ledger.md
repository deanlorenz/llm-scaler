# `STATE.md` vs. a ledger — different purpose, different audience

Read this before writing to `STATE.md` or a ledger, or when unsure which one something
belongs in.

- **`STATE.md` is what a resuming session actually reads.** Self-contained and current: current
  task status, current blockers, the immediate next step, pointers into the plan/spec doc's
  specific sections. If something unusual happened, `STATE.md` gets only the **actionable
  bottom line** — e.g. "rotate the leaked keys" — not the story of how it was discovered. It is
  overwritten, not appended to (except its Session log subsection, short-lines-only, not a
  narrative).
- **A ledger is a continuous, append-as-you-go audit trail that a resuming session normally
  never reads at all.** Consulted later, on demand — to recover a lost detail, investigate an
  incident, or (via ledger-capture) confirm nothing load-bearing got dropped. Can be long,
  narrative, exhaustive.
- **Rule of thumb:** a ledger line is a real finding, decision, correction, or false start,
  summarized in your own words — not raw tool output, not routine narration. If that finding
  also changes what a resuming session needs to know or do next, its **conclusion** additionally
  goes into `STATE.md` (short) while the **full story** stays only in the ledger (long).

## The live ledger during a session

**Two distinct cadences — do not conflate them.**

1. **Append to the local scratch copy after every meaningful finding, decision, correction, or
   false start — as it happens, not in a batch later.** Keep this **live, growing copy** as a
   local scratch file inside whatever feature worktree the session is actually working in
   (e.g. `worktrees/<feature-worktree>/.session/<unique-session-name>.md`), excluded from the
   feature branch's git history via `.git/info/exclude`.
2. **At session end (or at any natural checkpoint), copy** the ledger file verbatim into
   `missions/<mission>/ledgers/<same-unique-name>.md` in this (`session-tracking`) worktree and
   commit it there. Only this step is checkpoint-based — step 1 is never batched.

**Persist findings and decisions through failures and restarts.** Append to the ledger even
when nothing landed — a false start recorded is as valuable as a task completed. This means
during the session, not only at the end.
