# Installing conventions and skills onto session-tracking

Read this before copying any file from `worktrees/policy-writer` to `worktrees/session-tracking`.

**Never copy file content by hand or from memory.** Use git to move content between branches.
Hand-copying silently introduces drift, reverts prior work, and swaps files.

## Prerequisites

Before starting:
1. Your working tree on `policy-writer` must be clean and committed.
2. You must have explicit user authorization to write to `session-tracking`.

## What gets installed

Everything on the `policy-writer` branch **except**:
- `.session/` — mission-internal; never leaves the mission branch
- `.claude/` — policy-writer's local symlinks and config; not for production
- `.git*` files

The install is **not** file-by-file selection. It is a full sync of all non-excluded paths.

## Procedure

### Step 1: Verify both worktrees are clean

```bash
git -C worktrees/policy-writer status --short
git -C worktrees/session-tracking status --short
```

Both must show no uncommitted changes. If either has uncommitted changes, stop and
commit or stash before proceeding. Do not install over a dirty `session-tracking`.

### Step 2: Identify what will change

Preview what the install will touch:

```bash
diff -r --exclude="*.bak" \
  worktrees/policy-writer/conventions \
  worktrees/session-tracking/conventions
diff worktrees/policy-writer/CONVENTIONS.md \
     worktrees/session-tracking/CONVENTIONS.md
diff -r \
  worktrees/policy-writer/claude-skills \
  worktrees/session-tracking/claude-skills
```

Review every difference. For any file where session-tracking looks **ahead** of
policy-writer, stop — update policy-writer first, commit it, then resume here.

### Step 3: Checkout from the policy-writer branch

```bash
cd worktrees/session-tracking

# Install conventions and CONVENTIONS.md
git checkout policy-writer -- CONVENTIONS.md conventions/

# Install skills
git checkout policy-writer -- claude-skills/
```

`git checkout <branch> -- <path>` writes the exact committed content from the named
branch into the working tree. It does not merge, interpolate, or summarize.

If new top-level directories were added to policy-writer that belong in session-tracking,
add them to the checkout command above.

### Step 4: Verify git agrees

After the checkout, re-run the diff from Step 2. Output must be empty (or limited to
known intentional divergences that were reviewed in Step 2). If anything unexpected
appears, stop and investigate before committing.

```bash
diff -r --exclude="*.bak" \
  worktrees/policy-writer/conventions \
  worktrees/session-tracking/conventions
diff worktrees/policy-writer/CONVENTIONS.md \
     worktrees/session-tracking/CONVENTIONS.md
diff -r \
  worktrees/policy-writer/claude-skills \
  worktrees/session-tracking/claude-skills
```

### Step 5: Commit session-tracking

```bash
cd worktrees/session-tracking
git add CONVENTIONS.md conventions/ claude-skills/
git status --short   # review staged files before committing
git commit -m "install: CONVENTIONS.md + conventions/ + claude-skills/ from policy-writer (<session-slug>)"
```

## Fixing a bad install

If a file was installed incorrectly:

1. Run `git -C worktrees/session-tracking log --oneline -5` to identify the bad commit.
2. Do **not** hand-edit to fix — use `git checkout` from the correct branch again
   (Step 3) and re-verify (Step 4) before committing a correction.
