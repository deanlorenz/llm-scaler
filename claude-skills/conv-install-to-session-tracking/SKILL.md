---
name: conv-install-to-session-tracking
description: Use when installing conventions, skills, or CONVENTIONS.md from policy-writer onto the session-tracking branch.
---

# conv-install-to-session-tracking

`session-tracking` is the production branch of rules and skills. `worktrees/policy-writer` is the working branch. An install moves reviewed, committed policy-writer commits into production. Nothing else goes.

**This skill runs as a subagent.** Installation is mechanical and self-contained — spawn a subagent to run the procedure. The subagent needs explicit authorization confirmation from you first.

## What is installed

- Production paths: `CONVENTIONS.md`, `conventions/`, `claude-skills/`. Nothing else is installed.
- Never installed: `backup_rules/`, `.session/`, `.claude/`, `*.bak`, or any other path.
- Ignore `claude-skills/pr-review` in every check below. It is an upstream skill, left untracked on `session-tracking`. Do not stage, move, or delete it.

## Caller instructions

1. Confirm you have explicit user authorization to install to `session-tracking`.
2. Record the current `policy-writer` HEAD SHA (run in the policy-writer worktree):
   ```bash
   git rev-parse HEAD
   ```
3. Spawn a subagent:
   ```
   spawn_subagent(
     description: "<paste the Subagent instructions block below, filling in HEAD_SHA>",
     name: "general"
   )
   ```
   Model note: `claude-haiku-4-5` (or `$CONV_SUBAGENT_MODEL`) is sufficient.
   Assign `In:` `policy-writer.install-<n>.in` and `Out:` `policy-writer.install-<n>` (see `conv-agentbus`), subscribe to `Out:` yourself, and put both channels and the subscription command in the prompt.
4. Do not modify `policy-writer` files while the subagent is running.
5. After the subagent reports success, record the install in STATE and ledger.

---

## Subagent instructions

You are installing reviewed `policy-writer` commits onto the production branch `session-tracking`. Follow every step. Stop and report any failure immediately — do not improvise, resolve, or work around.

Set these once (full paths; `HEAD_SHA` is the SHA the caller recorded):

```bash
PW=<full path to worktrees/policy-writer>
ST=<full path to worktrees/session-tracking>
PATHS="CONVENTIONS.md conventions claude-skills"
```

Entering `$ST` with `cd` is permitted for this authorized operation. Use `$HEAD_SHA`, not the branch name, for the whole procedure.

Subscribe to your `In:` channel and stay subscribed. After each of Steps 1-6, publish `kind="heartbeat"` on `Out:`, body `session=<slug> step=<i>/7 <what you just did>`; send the Step 7 report on `Out:` too. Answer any request from `In:` on `Out:`.

### Step 1: Verify nothing uncommitted would be overwritten

In `$PW`: the production paths must have no uncommitted changes.

```bash
git status --short -- $PATHS
```

In `$ST`: the production paths must have no uncommitted or untracked changes, apart from `claude-skills/pr-review`.

```bash
git status --short -- $PATHS ':!claude-skills/pr-review'
```

Stop if either prints anything. Untracked files elsewhere in `$ST` (mission links, suggestion-box files) do not block the install and must not be touched.

### Step 2: Find the last install and check for divergence

```bash
git log --oneline --grep='^install:' -1
```

The install commit message contains `from policy-writer@<sha>`. Call that `LAST_SHA`. Stop if there is none.

`session-tracking` must still match what was last installed. From `$ST`:

```bash
git diff $LAST_SHA HEAD --stat -- $PATHS ':!claude-skills/pr-review'
```

(`$LAST_SHA` is a `policy-writer` commit; the diff compares its tree to the current `session-tracking` HEAD.)

**If the diff is non-empty:** `session-tracking` has content that was not installed from `policy-writer`. STOP. Report every differing file. Do not proceed until the caller resolves it on `policy-writer`.

### Step 3: Classify the commits to install

```bash
git log --reverse --no-merges --format=%h $LAST_SHA..$HEAD_SHA
```

For each commit, list its files:

```bash
git show --name-only --format= <sha>
```

Classify every commit:

- **All files are in the production paths:** install it.
- **No file is in the production paths** (for example, only `.session/` or `backup_rules/`): do not install it. List it as skipped.
- **Mixed:** STOP and report the commit and the files on each side. Do not split, edit, or drop files from it. The caller decides.

Report the install list and the skip list to the caller before continuing.

### Step 4: Cherry-pick, one commit at a time

Apply `conv-cherry-pick`. Only the commits classified "install", in order, from `$ST`:

```bash
git cherry-pick <sha>
```

If any pick conflicts or fails: run `git cherry-pick --abort`, stop, and report the commit and the conflicting files. Do not resolve conflicts. Do not `git checkout <branch> -- <path>`, hand-copy files, or delete files to make a pick apply.

### Step 5: Verify

From `$ST`:

```bash
diff -r -x pr-review $PW/claude-skills claude-skills
diff -r $PW/conventions conventions
diff $PW/CONVENTIONS.md CONVENTIONS.md
git diff --name-only <ST HEAD before step 4> HEAD
git ls-files | grep -E '(^backup_rules/|^\.session/|^\.claude/|\.bak$)' || echo none
grep -rohE 'conventions/[A-Za-z0-9_-]+\.md' CONVENTIONS.md conventions claude-skills --exclude-dir=archived | sort -u | while read f; do test -f "$f" || echo "MISSING $f"; done
```

The three `diff` commands must print nothing. Every name from `git diff --name-only` must be inside the production paths. The `git ls-files` command must print `none`. The last command (dangling references) must print nothing: every `conventions/<name>.md` cited in production must exist outside `conventions/archived/`. If anything else appears, stop and report.

### Step 6: Install commit

```bash
git commit --allow-empty -m "install: CONVENTIONS.md + conventions/ + claude-skills/ from policy-writer@$HEAD_SHA"
```

Always record the exact `policy-writer` SHA used.

### Step 7: Report back

Return:
- Commits installed and commits skipped (with the reason)
- Any mixed commit, conflict, or divergence stopped on
- Diff result (clean / any unexpected differences)
- Install commit SHA on `session-tracking`
- Push: this skill does NOT push — the caller handles push authorization separately.
