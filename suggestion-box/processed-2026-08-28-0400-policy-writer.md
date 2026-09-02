# Suggestion — no in-place editing, except own-scope files

**Source:** `policy-writer`, ledger `2026-08-28-worktree-model-and-side-notes.md`,
found during ledger-capture 2026-08-28.

**Candidate rule.** No in-place editing of anything, except files you 100% own in your
own session's context (your own code file, your own plan, your own ledger append).
Everything else: write new, then remove/replace old — never edit in place.

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** a
generalization of an earlier, narrower rule ("never `sed -i`") to cover all in-place
editing, not just shell-based tools — stated directly by the user as a side note during
the 2026-08-28 worktree-model correction.

**Where it might land:** likely a new short file under a future `conventions/` split
(see the pending `CONVENTIONS.md` refactor plan), or a section in whatever file covers
general editing discipline.
