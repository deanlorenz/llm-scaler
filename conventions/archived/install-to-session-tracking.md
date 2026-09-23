# Installing conventions and skills onto session-tracking

**Never copy content by hand.** Use git only.

## Prerequisites

1. `policy-writer` working tree is clean and committed.
2. Explicit user authorization to write to `session-tracking`.

## What gets installed

Everything on `policy-writer` except:
- `.session/` — never leaves the mission branch
- `.claude/` — local symlinks and config; not for production
- `.git*` files
- `conventions/*.bak` — working copies; not for production

## Pinned-session constraint

`cd` and `git -C <other-path>` are blocked. All steps run from inside the
`session-tracking` worktree (entered via `EnterWorktree`).

## Procedure

### Step 1: Verify both worktrees are clean

```bash
# from policy-writer
git status --short
# then EnterWorktree session-tracking
git status --short
```

Stop if either is dirty. Do not install over a dirty `session-tracking`.

### Step 2: Check for divergence — content diff first

From inside `session-tracking`, diff every in-scope file against `policy-writer`:

```bash
diff -r --exclude="*.bak" conventions ../policy-writer/conventions
diff CONVENTIONS.md ../policy-writer/CONVENTIONS.md
diff -r claude-skills ../policy-writer/claude-skills
```

**If the diff is non-empty:** `session-tracking` has content that `policy-writer` does not.
STOP. Do not proceed. For every differing file:
1. Determine whether the `session-tracking` content is intentional (e.g. a direct commit that bypassed `policy-writer`).
2. Port any missing content to `policy-writer` and commit it there.
3. Re-run this step. Only proceed to Step 3 when the diff is clean.

**Commit-log check** (after content diff is clean):

```bash
git log --oneline HEAD..policy-writer -- CONVENTIONS.md conventions/ claude-skills/
```

Review every commit listed — these are what the cherry-pick will apply.

### Step 3: Cherry-pick the range

Find the `policy-writer` SHA recorded in the last install commit on `session-tracking`:
```bash
git log --oneline | grep "install:"
# install commit message format: "install: ... from policy-writer@<sha>"
```

Cherry-pick all `policy-writer` commits after that SHA up to HEAD:
```bash
git cherry-pick <last-policy-writer-sha>..policy-writer
```

Cherry-pick conflicts loudly on divergence — stop and resolve before continuing.

**`.session/` conflicts:** commits that also touch `.session/` files (e.g. "save originals to spec"
commits) will conflict because `.session/` is absent on `session-tracking`. For each such conflict:
```bash
git rm --cached .session/<file> 2>/dev/null; rm -f .session/<file>
git cherry-pick --continue --no-edit
```
Repeat until the cherry-pick completes. Skip any commit whose only change is `.session/` files
(`git cherry-pick --skip`).

Remove any `.bak` files that came across:
```bash
git rm --cached conventions/*.bak 2>/dev/null || true
rm -f conventions/*.bak
```

### Step 4: Verify

```bash
diff -r --exclude="*.bak" ../policy-writer/conventions conventions
diff ../policy-writer/CONVENTIONS.md CONVENTIONS.md
diff -r ../policy-writer/claude-skills claude-skills
```

Output must be empty. If anything unexpected appears, stop and investigate.

### Step 5: Push

```bash
git log --oneline origin/session-tracking..HEAD   # review commits
git push origin session-tracking
```

## Install commit message format

```
install: CONVENTIONS.md + conventions/ + claude-skills/ from policy-writer@<policy-writer-HEAD-sha>
```

Always record the `policy-writer` HEAD SHA so the next install can find the starting point.

## Fixing a bad install

1. Identify the bad commit: `git log --oneline -5`
2. Do not hand-edit. Revert with `git revert <sha>`, or reset and re-run from Step 3.
