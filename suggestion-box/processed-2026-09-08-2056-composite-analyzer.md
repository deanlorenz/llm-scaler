# Suggestion — never report "clean vs upstream" from a bare diff against a remote ref

**Source:** `composite-analyzer`, ledger `worktrees/composite-analyzer/.session/2026-09-08-composite-analyzer-1.md`
(section "Finding — my 'zero diff vs upstream/main' claim was wrong"), surfaced during ledger-capture
on 2026-09-08.

**Candidate rule/addition.** A session branched from an explicit upstream SHA and twice told the user
its branch had "zero diff vs `upstream/main`". A later `git diff --stat upstream/main..<branch>`
showed 42 files and ~10.9k deletions. Nothing local had changed: **upstream had moved twice during
the session** (tip advanced within ~90 minutes of the fetch), so the "deletions" were upstream's
newer commits the branch lacked.

Rules that would have prevented it:

- **`git diff <remote-ref>..<branch>` conflates two different facts** — "I changed things" and
  "upstream advanced". It is not a check on your own work.
- **Use `git rev-list --left-right --count upstream/main...<branch>`** (this session: `28 1`, then
  `0 1` after replay) **or diff against the recorded base SHA.** Both isolate your own changes.
- **Record the base SHA in STATE, and re-fetch before relying on any remote tip.** On an active
  repo a fetched tip can go stale within hours; a base recorded as a ref name is not a pin, a base
  recorded as a SHA is.
- **After a rebase, a tree-to-tree diff between the old and new tips is not the check either** — it
  shows the arriving upstream commits (here the mirror image of the earlier "deletions", +10,949
  across 42 files) and looks alarming. **Compare the two commits' specific blobs.**

**Why (context for `policy-writer` to evaluate, not for `CONVENTIONS.md` itself):** the failure mode
is a session confidently reporting a false state of its own branch to the user — twice — and the
correction only came from an incidental post-commit check. It generalises to every mission whose
worktree tracks a moving upstream, which is all of them here. The `--left-right --count` form is a
one-line substitute that is honest by construction.

**Where it might land:** wherever branch/base hygiene is stated for feature worktrees
(`conventions/feature-worktree-setup.md` or `conventions/pr-branch.md` look closest), possibly as a
short "how to check your branch state" block. `policy-writer`'s call.
