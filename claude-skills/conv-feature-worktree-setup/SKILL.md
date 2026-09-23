---
name: conv-feature-worktree-setup
description: Use when creating or migrating a mission worktree, setting up .session/ layout, or fixing missing resume-mission/wind-down skill symlinks in a worktree.
---

# conv-feature-worktree-setup

**This skill runs as a subagent.** When triggered, spawn a subagent to execute the setup steps below. The subagent has its own context and can focus entirely on the mechanical work.

## Caller instructions (run in your own session before spawning)

1. Identify the mission name and full worktree path.
2. Spawn a subagent:
   ```
   spawn_subagent(
     description: "<paste the Subagent instructions block below, filling in MISSION_NAME and MISSION_WT>",
     name: "general"
   )
   ```
   Model note: this task suits a lighter model. If `CONV_SUBAGENT_MODEL` is set in your environment, pass that; otherwise `claude-haiku-4-5` is recommended.
3. Wait for the subagent to report completion before continuing mission work.
4. If the subagent reports a failure, address it before proceeding.

---

## Subagent instructions

You are performing a one-time worktree setup for mission `MISSION_NAME` at path `MISSION_WT`.
Follow these steps exactly. Stop and report any failure immediately.

### 1. Create `.session/` layout

```bash
mkdir -p MISSION_WT/.session/ledger
```

Verify the directory exists. If `MISSION_WT/.session/STATE.md` already exists, do not overwrite it — skip to step 2.

If STATE.md is missing, create a minimal one using the template from `worktrees/session-tracking/conventions/state-vs-ledger.md`. Fill in the Orientation fields only; leave Execution empty. Do not invent content.

### 2. Commit `.session/` to the mission branch

`.session/` must be tracked on the mission branch so it is recoverable via `git show`:

```bash
cd MISSION_WT
git add .session/
git commit -m "chore: initialize .session/ layout for MISSION_NAME"
```

If nothing to commit (already tracked), skip.

### 3. Set up skill symlinks

Skill discovery does not walk above the worktree root — each worktree needs its own symlinks:

```bash
mkdir -p MISSION_WT/.claude/skills
cd MISSION_WT/.claude/skills
ln -s ../../../session-tracking/claude-skills/resume-mission resume-mission
ln -s ../../../session-tracking/claude-skills/wind-down wind-down
# Add conv-* skills
for skill in worktrees/session-tracking/claude-skills/conv-*/; do
  name=$(basename "$skill")
  ln -s "../../../../session-tracking/claude-skills/$name" "$name"
done
```

Exclude symlinks from git tracking (repo-shared exclude, not per-worktree):

```bash
EXCLUDE=$(git -C MISSION_WT rev-parse --git-common-dir)/info/exclude
grep -q "resume-mission" "$EXCLUDE" || echo ".claude/skills/resume-mission" >> "$EXCLUDE"
grep -q "wind-down" "$EXCLUDE" || echo ".claude/skills/wind-down" >> "$EXCLUDE"
for skill in worktrees/session-tracking/claude-skills/conv-*/; do
  name=$(basename "$skill")
  grep -q "$name" "$EXCLUDE" || echo ".claude/skills/$name" >> "$EXCLUDE"
done
```

### 4. Verify all symlinks resolve

```bash
for skill in MISSION_WT/.claude/skills/*/; do
  name=$(basename "$skill")
  test -r "$skill/SKILL.md" || { echo "BROKEN: $name"; exit 1; }
  printf '%s -> %s\n' "$name" "$(readlink -f "$skill/SKILL.md")"
done
```

All resolved paths must be under `worktrees/session-tracking/claude-skills/`. Report any broken or mis-targeted link.

### 5. Create session-tracking convenience symlinks

```bash
cd worktrees/session-tracking/missions
mkdir -p MISSION_NAME
cd MISSION_NAME
ln -sf ../../../../worktrees/MISSION_NAME/.session/STATE.md STATE.md
ln -sf ../../../../worktrees/MISSION_NAME/.session ledgers
```

Then publish on agentbus so policy-writer commits these on its next resume:

```
agentbus_publish(topic="session-tracking.pending-commits", kind="note",
  body="mission=MISSION_NAME symlinks created in missions/MISSION_NAME/, ready to commit")
```

### 6. Report back

Return a summary:
- `.session/` layout: created / already existed
- Symlinks verified: list of skills, resolved paths
- session-tracking symlinks: created / already existed
- agentbus note: published / skipped (if agentbus unavailable)
- Any failures encountered
