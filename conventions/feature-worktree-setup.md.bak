# Making `/resume-mission` and `/wind-down` available in a feature worktree

Read this when setting up a new feature worktree for a mission, or when `/resume-mission` or
`/wind-down` is found missing in one.

**Confirmed by direct testing:** Claude Code's project-skill discovery does **not** walk up
past a git worktree's own root — not to a plain filesystem parent directory, not to the main
repo a worktree belongs to. Each worktree only sees its own local `.claude/skills/`. So a
feature worktree (e.g. `worktrees/single-analyzer`) needs its own local entry pointing at
these two skills before `/resume-mission` or `/wind-down` will show up there at all.

**One-time setup per feature worktree** (do this once per worktree, not per session — check
first, it may already be done):

```bash
cd worktrees/<feature-worktree>/.claude/skills
ln -s ../../../session-tracking/.claude/skills/resume-mission resume-mission
ln -s ../../../session-tracking/.claude/skills/wind-down wind-down
```

These are **local convenience symlinks, never committed** to the feature branch — add the two
paths to `.git/info/exclude` (the shared one at the main repo's `.git/`, not a per-worktree
file — see the correction in `conventions/wip-editing.md`) so they never show up in
`git status`/get picked up by a broad `git add`:

```
.claude/skills/resume-mission
.claude/skills/wind-down
```

**If a session finds these skills missing** (typed `/resume-mission` and got nothing, or
`ls .claude/skills/` in the current feature worktree doesn't show them): this is exactly that
one-time setup not having been done yet for this worktree. Run the two commands above, verify
with `cat .claude/skills/resume-mission/SKILL.md | head -3` that the symlink actually resolves
(don't just trust `ln -s` succeeded silently), then proceed.
