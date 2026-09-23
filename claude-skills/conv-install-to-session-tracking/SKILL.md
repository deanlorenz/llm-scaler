---
name: conv-install-to-session-tracking
description: Use when installing conventions, skills, or CONVENTIONS.md from policy-writer onto the session-tracking branch.
---

# conv-install-to-session-tracking

**This skill runs as a subagent.** Installation is mechanical and self-contained — spawn a subagent to run the procedure. The subagent needs explicit authorization confirmation from you first.

## Caller instructions

1. Confirm you have explicit user authorization to install to `session-tracking`.
2. Note the current `policy-writer` HEAD SHA:
   ```bash
   git rev-parse HEAD   # run in policy-writer worktree
   ```
3. Spawn a subagent:
   ```
   spawn_subagent(
     description: "<paste the Subagent instructions block below>",
     name: "general"
   )
   ```
   Model note: `claude-haiku-4-5` (or `$CONV_SUBAGENT_MODEL`) is sufficient.
4. Do not modify `policy-writer` files while the subagent is running.
5. After the subagent reports success, record the install in STATE and ledger.

---

## Subagent instructions

You are installing the current `policy-writer` conventions and skills onto `session-tracking`. Follow every step. Stop and report any failure immediately — do not improvise.

### Step 1: Verify both worktrees are clean

```bash
# In policy-writer worktree:
git status --short
# In session-tracking worktree:
git status --short
```

Stop if either is dirty.

### Step 2: Check for divergence — content diff first

From inside `session-tracking`:

```bash
diff -r --exclude="*.bak" conventions ../policy-writer/conventions
diff CONVENTIONS.md ../policy-writer/CONVENTIONS.md
diff -r claude-skills ../policy-writer/claude-skills
```

**If the diff is non-empty:** `session-tracking` has content that `policy-writer` does not. STOP. Report every differing file to the caller. Do not proceed until the caller resolves the divergence on `policy-writer` and re-runs this skill.

**Commit-log check** (after content diff is clean):

```bash
git log --oneline HEAD..policy-writer -- CONVENTIONS.md conventions/ claude-skills/
```

Review every listed commit — these are what the cherry-pick will apply.

### Step 3: Find the last install SHA

```bash
git log --oneline | grep "install:"
```

The most recent install commit contains `from policy-writer@<sha>`. Use that SHA as the range start.

### Step 4: Cherry-pick the range

```bash
git cherry-pick <last-policy-writer-sha>..policy-writer
```

**`.session/` conflicts:** any commit that also touches `.session/` files will conflict because `.session/` is absent on `session-tracking`. For each such conflict:
```bash
git rm --cached .session/<file> 2>/dev/null; rm -f .session/<file>
git cherry-pick --continue --no-edit
```
Skip any commit whose only change is `.session/` files:
```bash
git cherry-pick --skip
```

Remove any `.bak` files:
```bash
git rm --cached conventions/*.bak 2>/dev/null || true
rm -f conventions/*.bak
```

### Step 5: Verify

```bash
diff -r --exclude="*.bak" ../policy-writer/conventions conventions
diff ../policy-writer/CONVENTIONS.md CONVENTIONS.md
diff -r ../policy-writer/claude-skills claude-skills
```

Output must be empty. If anything appears, stop and report.

### Step 6: Install commit

```bash
git commit --allow-empty -m "install: CONVENTIONS.md + conventions/ + claude-skills/ from policy-writer@<policy-writer-HEAD-sha>"
```

Always record the exact `policy-writer` HEAD SHA.

### Step 7: Report back

Return:
- List of commits cherry-picked
- Any `.session/` conflicts encountered and how resolved
- Diff result (clean / any unexpected differences)
- Install commit SHA on `session-tracking`
- Push: this skill does NOT push — the caller handles push authorization separately.
