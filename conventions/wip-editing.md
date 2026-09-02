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
2. **Claim ownership by renaming — not copying:**
   ```bash
   mv FILE.md FILE.md.wip
   ```
   The rename is the lock. `FILE.md` must be absent while editing is in progress — that
   absence is the signal other sessions check. **Never use `cp` to create the `.wip` file;
   copying leaves `FILE.md` in place and allows concurrent edits.**
3. **Edit `FILE.md.wip` directly** (for `STATE.md`, which is local in the mission worktree).
   For `CONVENTIONS.md` (cross-worktree): copy `FILE.md.wip` to a scratch path inside the
   worktree the session is actually working in (e.g.
   `worktrees/<mission>/.session/CONVENTIONS.md.local`), make all edits there, then copy the
   finished result back over `FILE.md.wip`.
4. While `FILE.md.wip` exists and `FILE.md` is absent, that is the signal "this file is being
   edited right now." Other sessions must not start their own edit — they can still read the
   last-committed version (`git show HEAD:.session/STATE.md`) or peek at `FILE.md.wip`
   directly; reads are never blocked.
5. **To finish:** rename `FILE.md.wip` back to `FILE.md`, `git add`, commit.
   ```bash
   mv FILE.md.wip FILE.md
   ```
6. `*.md.wip` files are excluded via `.git/info/exclude`. `.git/info/exclude` is **not**
   per-worktree — `git rev-parse --git-common-dir` resolves to the *main* repo's `.git`,
   shared across every worktree of that repo. Note: `.session/` is **not** in
   `.git/info/exclude` — it is tracked on mission branches and must not be globally excluded.

**Symlink-based locking was considered and rejected — do not re-propose it.** See the spec doc
for why.

## When a plan is approved

The moment a plan is approved — `ExitPlanMode`, or an explicit "go ahead on X, Y, Z" — persist
it to a **durable, committed file immediately**. Do not leave it contingent on the transient
plan-mode file (`~/.claude/plans/*.md`) surviving until execution.

- If the plan belongs in a feature worktree (e.g. a plan for drafting `CONVENTIONS.md`
  changes), commit it there, alongside the first commit of the work it describes — not later
  at a checkpoint.
- If the plan belongs in `session-tracking` (a mission's spec doc), commit it there.

A transient plan-mode file is not a durable record. If the session ends or is compacted before
the plan executes, the plan is gone — this has happened and caused real rework.
