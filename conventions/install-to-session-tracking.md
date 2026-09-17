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
- `conventions/*.bak` — policy-writer working copies; not for production

The install is **not** file-by-file selection. It is a full sync of all non-excluded paths.

## Pinned-session constraint

In a pinned session, `cd` and `git -C <other-path>` are blocked. All steps below must be
executed from **inside the `session-tracking` worktree** (entered via `EnterWorktree`).
References to `policy-writer` content use `git show policy-writer -- <path>` or cross-worktree
full paths — never `-C`.

## Procedure

### Step 1: Verify both worktrees are clean

From inside the `policy-writer` worktree:
```bash
git status --short
```

Then `EnterWorktree` the `session-tracking` worktree and run:
```bash
git status --short
```

Both must show no uncommitted changes. If either has uncommitted changes, stop and
commit or stash before proceeding. Do not install over a dirty `session-tracking`.

### Step 2: Identify what will change

From inside the `session-tracking` worktree, preview what the install will touch:

```bash
diff -r --exclude="*.bak" \
  /path/to/worktrees/policy-writer/conventions \
  conventions
diff /path/to/worktrees/policy-writer/CONVENTIONS.md CONVENTIONS.md
diff -r \
  /path/to/worktrees/policy-writer/claude-skills \
  claude-skills
```

Review every difference. For any file where session-tracking looks **ahead** of
policy-writer, stop — update policy-writer first, commit it, then resume here.

### Step 3: Checkout from the policy-writer branch

From inside the `session-tracking` worktree:

```bash
# Install conventions and CONVENTIONS.md
git checkout policy-writer -- CONVENTIONS.md conventions/
# Remove .bak files that came across from policy-writer working copies
git rm --cached conventions/*.bak 2>/dev/null || true
rm -f conventions/*.bak

# Install skills
git checkout policy-writer -- claude-skills/
```

`git checkout <branch> -- <path>` writes the exact committed content from the named
branch into the working tree. It does not merge, interpolate, or summarize.

If new top-level directories were added to policy-writer that belong in session-tracking,
add them to the checkout command above.

### Step 4: Verify git agrees

After the checkout, re-run the diff from Step 2 (still from inside `session-tracking`).
Output must be empty (or limited to known intentional divergences reviewed in Step 2).
If anything unexpected appears, stop and investigate before committing.

### Step 5: Commit session-tracking

From inside the `session-tracking` worktree:

```bash
git add CONVENTIONS.md conventions/ claude-skills/
git status --short   # review staged files before committing
git commit -m "install: CONVENTIONS.md + conventions/ + claude-skills/ from policy-writer (<session-slug>)"
```

## Fixing a bad install

If a file was installed incorrectly:

1. From inside `session-tracking`, run `git log --oneline -5` to identify the bad commit.
2. Do **not** hand-edit to fix — use `git checkout` from the correct branch again
   (Step 3) and re-verify (Step 4) before committing a correction.
