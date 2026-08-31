# Editing shared files safely — the `.wip` protocol

Read this before editing `STATE.md` or `CONVENTIONS.md`.

`STATE.md` for each mission lives in that mission's own worktree at `.session/STATE.md` — the
mission owner edits it locally with ordinary same-worktree tools, no cross-worktree dance
needed. `CONVENTIONS.md` (in `session-tracking`) is the one remaining file that needs the full
`.wip` treatment, since any session editing it writes across worktree boundaries.

The rule for both:

1. **Only the owner session for a mission edits that mission's `STATE.md`.** Only a session
   explicitly asked to update global conventions edits `CONVENTIONS.md`. Every other
   session/agent only *reads* these files.
2. **Claim ownership, atomically:** rename `FILE.md` → `FILE.md.wip`.
3. **Edit via a local copy.** For `CONVENTIONS.md` (cross-worktree): copy `FILE.md.wip` to a
   scratch path inside the worktree the session is actually working in (e.g.
   `worktrees/<mission>/.session/CONVENTIONS.md.local`), make all edits there with ordinary
   same-worktree tools, and copy the finished result back over `FILE.md.wip` when done. For
   `STATE.md` (already local in the mission worktree): edit directly, no copy needed.
4. While `FILE.md.wip` exists and `FILE.md` is absent, that is the signal "this file is being
   edited right now." Other sessions must not start their own edit — they can still read the
   last-committed version (`git show HEAD:.session/STATE.md`) or peek at the in-progress
   `FILE.md.wip` directly; reads are never blocked.
5. **To finish:** copy the edited local copy back over `FILE.md.wip` (if you used a local
   copy), then rename `FILE.md.wip` back to `FILE.md`, `git add`, commit.
6. `*.md.wip` files are excluded via `.git/info/exclude`. `.git/info/exclude` is **not**
   per-worktree — `git rev-parse --git-common-dir` resolves to the *main* repo's `.git`,
   shared across every worktree of that repo.

**Symlink-based locking was considered and rejected — do not re-propose it.** See the spec doc
for why.
