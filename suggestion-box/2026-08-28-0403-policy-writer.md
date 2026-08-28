# Suggestion — avoid `cd`, never use shell tricks to route around worktree boundaries

**Source:** `policy-writer`, ledger `2026-08-28-worktree-model-and-side-notes.md`,
found during ledger-capture 2026-08-28.

**Candidate rule.** Avoid `cd`. Never use shell tricks (subshells, process substitution,
etc.) to route around worktree write boundaries.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):**
stated directly by the user as a side note during the 2026-08-28 worktree-model
correction — a mechanical corollary of the read/write worktree-boundary rule (see the
companion suggestion `2026-08-28-0402-policy-writer.md`).

**Where it might land:** likely the same destination as the read/write-boundary
suggestion — consider drafting together rather than as two separate rules.
