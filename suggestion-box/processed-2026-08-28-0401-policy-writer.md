# Suggestion — destructive actions need step-by-step approval, keep a backup when unsure

**Source:** `policy-writer`, ledger `2026-08-28-worktree-model-and-side-notes.md`,
found during ledger-capture 2026-08-28.

**Candidate rule.** Destructive actions are rare and need step-by-step approval, almost
always (e.g. `git reset --hard`, `rm -rf`, `stash remove`/drop). If not 100% sure, keep a
backup copy rather than proceeding.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):**
stated directly by the user as a side note during the 2026-08-28 worktree-model
correction, as a general caution about destructive git/filesystem operations.

**Where it might land:** possibly overlaps with existing standing user preferences
already recorded elsewhere (e.g. the global "executing actions with care" guidance) —
worth checking for duplication before drafting a new rule.
