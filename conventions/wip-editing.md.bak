# Editing shared files safely — the `.wip` protocol

Read this before editing `STATE.md` or `CONVENTIONS.md`.

`STATE.md` and `CONVENTIONS.md` are the only files here more than one session might want to
write. The design goal is to make concurrent writes to them rare by convention (see
"Who writes what" in `CONVENTIONS.md`) — the `.wip` protocol below is the mechanical backstop, not a
substitute for that convention.

1. **Only the orchestrating session for a mission edits that mission's `STATE.md`.** Only a
   session explicitly asked to update global conventions edits `CONVENTIONS.md`. Every other
   session/agent only *reads* these files.
2. **Claim ownership, atomically, in this worktree:** rename `FILE.md` → `FILE.md.wip`. The
   rename itself is the atomic claim — whoever successfully renames it owns the edit.
3. **Edit via a local copy, not repeated cross-worktree edits.** A session pinned to a
   different worktree (via `EnterWorktree`) cannot reliably write here directly, and even an
   unpinned session shouldn't make every single line-edit a cross-worktree operation. Instead:
   copy `FILE.md.wip` to a scratch path inside the worktree the session is actually working in
   (e.g. `worktrees/<feature-worktree>/.session/FILE.md.local`), make all edits there with
   ordinary same-worktree `Edit`/`Write` calls, and only copy the finished result back over
   `FILE.md.wip` here when done.
4. While `FILE.md.wip` exists and `FILE.md` is absent, that is the visible signal "this file
   is being edited right now." Other sessions must not start their own edit of it — they can
   still read the last-committed version (`git show HEAD:missions/<m>/STATE.md`) or peek at
   the in-progress `FILE.md.wip` directly; reads are never blocked.
5. **To finish:** copy the edited local copy back over `FILE.md.wip` here, then rename
   `FILE.md.wip` back to `FILE.md` (atomic — only meaningful/safe because this session is the
   one that holds the claim), `git add`, commit.
6. `*.md.wip` and any `.session/` scratch dir are excluded via `.git/info/exclude` (not a
   tracked `.gitignore` — this is local-only bookkeeping, not a repo convention to publish) so
   an accidental broad `git add` never picks up a mid-edit file. **Correction, confirmed by
   testing:** `.git/info/exclude` is **not** per-worktree — `git rev-parse --git-common-dir`
   resolves to the *main* repo's `.git`, and `info/exclude` lives there, shared across every
   worktree of that repo. There is no mechanism for a genuinely worktree-local exclude file.
   This turns out fine for `*.md.wip`/`.session/` (every worktree should exclude the same
   patterns anyway) — just don't design around the wrong mental model of "each worktree has
   its own."

**Alternative considered and rejected: symlink-based locking.** Before landing on the
rename-based `.wip` protocol above, a symlink-swap approach was proposed (copy the shared
file locally, replace the shared location with a symlink pointing at the local copy, swap
back at commit time) and rejected: it reinvents a lock without providing real exclusion
(nothing stops a second session from also swapping in its own symlink, or writing the real
file directly while a symlink points elsewhere), and risks committing a broken/dangling
symlink into `session-tracking`'s own history by accident. The rename-based claim (step 2
above) gives the same atomicity without either problem.
