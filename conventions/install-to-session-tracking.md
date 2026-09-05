# Installing conventions and skills onto session-tracking

Read this before copying any file from `worktrees/policy-writer` to `worktrees/session-tracking`.

**Never copy file content by hand or from memory.** Use git to move content between branches.
Hand-copying silently introduces drift, reverts prior work, and swaps files.

## Prerequisites

Before starting:
1. Your working tree on `policy-writer` must be clean and committed.
2. You must have explicit user authorization to write to `session-tracking`.

## Procedure

### Step 1: Verify both worktrees are clean

```bash
git -C worktrees/policy-writer status --short
git -C worktrees/session-tracking status --short
```

Both must show no uncommitted changes. If either has uncommitted changes, stop and
commit or stash before proceeding. Do not install over a dirty `session-tracking`.

### Step 2: Identify the exact files to install

List the files that differ between the two worktrees:

```bash
diff -r --exclude="*.bak" \
  worktrees/policy-writer/conventions \
  worktrees/session-tracking/conventions
diff worktrees/policy-writer/CONVENTIONS.md \
     worktrees/session-tracking/CONVENTIONS.md
diff -r \
  worktrees/policy-writer/.claude/skills \
  worktrees/session-tracking/.claude/skills
```

For each differing file, decide: is policy-writer's version newer (install it), or is
session-tracking's version newer (update policy-writer first, then come back)?
**Never install a file from policy-writer that is behind session-tracking.**

### Step 3: Checkout files from the policy-writer branch

Use `git checkout` to pull exact file contents from the `policy-writer` branch into the
`session-tracking` worktree — no copying, no writing from memory:

```bash
# For each file to install, e.g.:
git -C worktrees/session-tracking checkout policy-writer -- conventions/<file>.md
git -C worktrees/session-tracking checkout policy-writer -- CONVENTIONS.md
git -C worktrees/session-tracking checkout policy-writer -- .claude/skills/resume-mission/SKILL.md
git -C worktrees/session-tracking checkout policy-writer -- .claude/skills/wind-down/SKILL.md
```

`git checkout <branch> -- <path>` writes the exact committed content from the named
branch into the working tree. It does not merge, interpolate, or summarize.

### Step 4: Verify git agrees

After the checkout, confirm `git diff` between the two worktrees shows only the changes
you intended — no surprises, no accidental reversions:

```bash
diff -r --exclude="*.bak" \
  worktrees/policy-writer/conventions \
  worktrees/session-tracking/conventions
diff worktrees/policy-writer/CONVENTIONS.md \
     worktrees/session-tracking/CONVENTIONS.md
```

The output must be empty (or limited to known, intentional divergences). If anything
unexpected appears, stop and investigate before committing.

### Step 5: Commit session-tracking

```bash
git -C worktrees/session-tracking add conventions/ CONVENTIONS.md .claude/skills/
git -C worktrees/session-tracking status --short
git -C worktrees/session-tracking commit -m "chore: install conventions and skills from policy-writer"
```

Review `git status` before committing to confirm exactly which files are staged.

## Fixing a bad install

If a file was installed incorrectly (wrong content, wrong direction):

1. Run `git -C worktrees/session-tracking log --oneline -5` to identify the bad commit.
2. Do **not** hand-edit to fix — use `git checkout` from the correct branch again
   (Step 3) and re-verify (Step 4) before committing a correction.
