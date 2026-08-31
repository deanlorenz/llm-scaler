# Making `/resume-mission` and `/wind-down` available in a mission worktree

Read this when setting up a new mission worktree, or when `/resume-mission` or `/wind-down`
is found missing in one.

## One-time setup per mission worktree

**Create the `.session/` directory** — this is where `STATE.md`, ledgers, and internal plans
live for this mission:

```bash
mkdir -p worktrees/<mission-name>/.session
```

Add `.session/` to the repo's shared `.gitignore` (or the mission branch's own `.gitignore`)
so it is never accidentally committed to a PR branch:

```
.session/
```

**Skill symlinks.** Claude Code's project-skill discovery does not walk up past a git
worktree's own root. Each worktree needs its own local symlink to these two skills before
`/resume-mission` or `/wind-down` will show up there.

```bash
cd worktrees/<mission-name>/.claude/skills
ln -s ../../../session-tracking/.claude/skills/resume-mission resume-mission
ln -s ../../../session-tracking/.claude/skills/wind-down wind-down
```

These symlinks are **never committed** to the mission branch — add them to `.git/info/exclude`
(the repo-shared one at the main `.git/`, not per-worktree — see `conventions/wip-editing.md`)
so they never show up in `git status`:

```
.claude/skills/resume-mission
.claude/skills/wind-down
```

Note: `.git/info/exclude` is **not** per-worktree — it resolves to the main repo's `.git`,
shared across every worktree of that repo.

**Verify both** before proceeding:

```bash
cat .claude/skills/resume-mission/SKILL.md | head -3
ls .session/
```

## If a session finds skills missing

Typed `/resume-mission` and got nothing, or `ls .claude/skills/` doesn't show them: the
one-time setup above hasn't been done yet for this worktree. Run the symlink commands, verify
they resolve, then proceed.

## Setting up the session-tracking symlinks (policy-writer's job)

Once a mission worktree is set up, `policy-writer` creates the corresponding entry in
`session-tracking/missions/<mission-name>/` — a folder of relative symlinks pointing into the
mission worktree's `.session/`:

```bash
cd worktrees/session-tracking/missions
mkdir <mission-name>
cd <mission-name>
ln -s ../../../../worktrees/<mission-name>/.session/STATE.md STATE.md
# add further symlinks for each internal plan as they are created
```

These symlinks are committed to `session-tracking` by `policy-writer` — the mission owner
does not need to commit `session-tracking` themselves. If the symlinks don't exist yet,
other sessions can still access the mission's files directly via the mission branch:

```bash
git -C <repo-root> show <mission-name>:.session/STATE.md
```
