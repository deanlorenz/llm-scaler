# Editing shared files safely — the `.wip` protocol

Read this before editing `STATE.md` or `CONVENTIONS.md`.

`STATE.md` and `CONVENTIONS.md` are the only files here more than one session might want to
write.

1. **Only the orchestrating session for a mission edits that mission's `STATE.md`.** Only a
   session explicitly asked to update global conventions edits `CONVENTIONS.md`. Every other
   session/agent only *reads* these files.
2. **Claim ownership, atomically, in this worktree:** rename `FILE.md` → `FILE.md.wip`.
3. **Edit via a local copy, not repeated cross-worktree edits.** Copy `FILE.md.wip` to a
   scratch path inside the worktree the session is actually working in (e.g.
   `worktrees/<feature-worktree>/.session/FILE.md.local`), make all edits there with ordinary
   same-worktree `Edit`/`Write` calls, and only copy the finished result back over `FILE.md.wip`
   here when done.
4. While `FILE.md.wip` exists and `FILE.md` is absent, that is the signal "this file is being
   edited right now." Other sessions must not start their own edit of it — they can still read
   the last-committed version (`git show HEAD:missions/<m>/STATE.md`) or peek at the
   in-progress `FILE.md.wip` directly; reads are never blocked.
5. **To finish:** copy the edited local copy back over `FILE.md.wip` here, then rename
   `FILE.md.wip` back to `FILE.md`, `git add`, commit.
6. `*.md.wip` and any `.session/` scratch dir are excluded via `.git/info/exclude`.
   `.git/info/exclude` is **not** per-worktree — `git rev-parse --git-common-dir` resolves to
   the *main* repo's `.git`, shared across every worktree of that repo. There is no
   genuinely worktree-local exclude file.

**Symlink-based locking was considered and rejected — do not re-propose it.** See the spec doc
for why.
