# Suggestion — reads may cross worktree boundaries freely, writes may not

**Source:** `policy-writer`, ledger `2026-08-28-worktree-model-and-side-notes.md`,
found during ledger-capture 2026-08-28.

**Candidate rule.** Reads may cross worktree boundaries freely (full paths, `git -C`,
etc.). Writes may not — except a session's own tracking files saved into
`session-tracking` (the stated exception, per the corrected worktree/mission model).

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):**
stated directly by the user as a side note during the 2026-08-28 worktree-model
correction. Complements/sharpens the existing scope-boundary note already in
`CONVENTIONS.md` (added from the `agentbus` feedback round) — that note covers "don't
act as maintainer of the whole shared worktree"; this one draws the read-vs-write line
explicitly, which the existing note doesn't quite state.

**Where it might land:** likely folds into the existing scope-boundary section rather
than needing an entirely new one — check for overlap before drafting.
