# Editing a shared file safely — the `.wip` protocol

Read this before editing any file that more than one session may need to write.

## Steps

1. **Claim:** rename `FILE.md` → `FILE.md.wip`. The absent `FILE.md` is the lock signal.
   Never use `cp` — copying leaves `FILE.md` in place and allows concurrent edits.
2. **Edit:** make all changes to `FILE.md.wip` directly (ordinary same-worktree tools).
3. **Release:** rename `FILE.md.wip` → `FILE.md`.
4. **Commit:** `git add FILE.md && git commit`.

Other sessions must not start their own edit while `FILE.md` is absent. Reads are never
blocked — use `git show HEAD:path/to/FILE.md` or read `FILE.md.wip` directly.

`*.md.wip` files are excluded via `.git/info/exclude` (repo-shared, not per-worktree).

## When a plan is approved

On `ExitPlanMode`, or an explicit "go ahead on X, Y, Z", save the plan to a file in
`.session/` immediately — before any execution begins. Do not leave it contingent on the
transient plan-mode file surviving.

The saved plan can later be consolidated into the relevant spec or longer-term doc. The point
is that it must be persisted at the moment of approval, not reconstructed from memory later.
