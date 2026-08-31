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

## Migrating an existing worktree to the new layout

If the mission previously tracked its files in `session-tracking/missions/<name>/` (the old
layout), migrate as follows. You should already have the files copied into `.session/` by
`/resume-mission`'s Step 3 — this section covers the one-time setup that must follow.

**1. Add `.gitignore` entry** (if not already present on the mission branch):

```bash
grep -q '\.session/' .gitignore 2>/dev/null || echo '.session/' >> .gitignore
git add .gitignore && git commit -m "chore: exclude .session/ from PR branches"
```

**2. Verify what's in `.session/` before doing anything else.** The directory may already
contain some files from earlier work in the new layout (ledgers, a partial `STATE.md`). Do
not overwrite a newer file with an older one. If both the `.session/` copy and the
`session-tracking` copy exist and differ, keep whichever is more recent (check timestamps or
git log) and note the other in your live ledger.

**3. Commit `.session/` content to the mission branch:**

```bash
git add .session/ && git commit -m "chore: migrate tracking files into .session/"
```

**4. Set up skill symlinks** (if not already present — see "One-time setup" above).

**5. Update `session-tracking` symlinks** (notify `policy-writer`):
The old `session-tracking/missions/<name>/STATE.md` and `ledgers/` are now stale real files.
`policy-writer` will replace them with symlinks pointing into `<mission-worktree>/.session/`.
You do not need to do this yourself — raise it with the user so `policy-writer` can handle it
at its next `session-tracking` commit.

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
