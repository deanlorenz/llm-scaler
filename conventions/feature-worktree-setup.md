# Making `/resume-mission` and `/wind-down` available in a mission worktree

Read this when setting up a new mission worktree, or when `/resume-mission` or `/wind-down`
is found missing in one.

## One-time setup per mission worktree

**Create the `.session/` directory** — this is where `STATE.md`, ledgers, and internal plans
live for this mission:

```bash
mkdir -p worktrees/<mission-name>/.session
```

`.session/` is **tracked on the mission branch** — commit it and push to `origin`. This is
what makes the files recoverable via `git show <mission-name>:.session/STATE.md` even after
the local worktree is deleted. Do **not** add `.session/` to `.gitignore` on the mission
branch.

`.session/` must never reach a PR branch — this is enforced by the owner only cherry-picking
code commits (not `.session/` commits) when creating a PR branch. See `conventions/pr-branch.md`
for the pre-push check.

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
layout), migrate in this order — the check steps come first, before any file is touched:

**1. List what is already in `.session/`:**

```bash
mkdir -p "$MISSION_WT/.session"
ls -la "$MISSION_WT/.session/"
```

**2. List what session-tracking has:**

```bash
ls "$TRACKING/missions/$MISSION_NAME/"
ls "$TRACKING/missions/$MISSION_NAME/ledgers/"
```

**3. For each file that already exists in both places, diff before copying:**

```bash
diff "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
```

Keep whichever is newer/more complete. Note any conflict in your live ledger.

**4. Copy only files that do NOT already exist in `.session/`:**

```bash
[ ! -f "$MISSION_WT/.session/STATE.md" ] && \
  cp "$TRACKING/missions/$MISSION_NAME/STATE.md" "$MISSION_WT/.session/STATE.md"
for f in "$TRACKING/missions/$MISSION_NAME/ledgers/"*.md; do
  [ ! -f "$MISSION_WT/.session/$(basename $f)" ] && cp "$f" "$MISSION_WT/.session/"
done
# Copy spec/plan docs that are internal; leave shareable docs in the code tree
```

**5. Commit `.session/` content to the mission branch:**

```bash
git add .session/ && git commit -m "chore: migrate tracking files into .session/"
```

**6. Set up skill symlinks** (if not already present — see "One-time setup" above).

**7. Update `session-tracking` symlinks** (notify `policy-writer`):
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
