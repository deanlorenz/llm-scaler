---
name: conv-wip-editing
description: Use when about to edit a file you don't own, or write a new file into a shared folder you don't own. Implements the .wip claim/edit/release protocol.
---

# conv-wip-editing

**This skill runs as a subagent** when the edit is fully scoped — you know the exact file, the exact change, and the change is self-contained. The subagent claims the file, makes the edit, and releases it.

If the edit requires judgment from your session's context (e.g. the content to write depends on decisions being made interactively), run the steps below in your own session instead.

## Caller instructions (sub-agent path)

1. Confirm you know:
   - The exact file path to edit
   - The exact change to make (write it out before spawning)
   - That no `.wip` lock already exists (`test ! -f <path>.wip`)
2. Spawn a subagent:
   ```
   spawn_subagent(
     description: "<paste the Subagent instructions block below, filling in FILE_PATH and CHANGE_DESCRIPTION>",
     name: "general"
   )
   ```
   Model note: `claude-haiku-4-5` (or `$CONV_SUBAGENT_MODEL`) is sufficient for mechanical edits.
3. Do not touch the file while the subagent holds the `.wip` lock.
4. Verify the subagent committed before continuing.

## In-session path (when judgment is needed)

Follow the steps in the Subagent instructions directly in your own session.

---

## Subagent instructions — Case 1: Editing an existing file

File: `FILE_PATH`
Change: `CHANGE_DESCRIPTION`

1. **Verify the file is tracked:**
   ```bash
   git ls-files FILE_PATH
   ```
   Must show the file. If empty — untracked — stop and report. Do not proceed.

2. **Verify no existing `.wip` lock:**
   ```bash
   test ! -f FILE_PATH.wip || echo "LOCKED"
   ```
   If locked: stop and report. Do not proceed.

3. **Claim — rename to `.wip`:**
   ```bash
   mv FILE_PATH FILE_PATH.wip
   ```

4. **Make the change** to `FILE_PATH.wip`.

5. **Release — rename back:**
   ```bash
   mv FILE_PATH.wip FILE_PATH
   ```

6. **Commit:**
   ```bash
   git add FILE_PATH && git commit -m "<describe the change>"
   ```

7. Report: file path, commit SHA, one-line summary of change made.

---

## Subagent instructions — Case 2: Writing a new file into a shared folder

Destination: `FILE_PATH`
Content: `CHANGE_DESCRIPTION`

1. **Verify the target does not already exist:**
   ```bash
   test ! -f FILE_PATH || echo "EXISTS"
   git show HEAD:FILE_PATH 2>/dev/null && echo "IN HISTORY"
   ```
   If it exists on disk or in history: stop and report. This may be a rename, not a new file.

2. **Claim the name:**
   ```bash
   touch FILE_PATH.wip
   ```

3. **Write the content** to `FILE_PATH.wip`.

4. **Release:**
   ```bash
   mv FILE_PATH.wip FILE_PATH
   ```

5. **Commit:**
   ```bash
   git add FILE_PATH && git commit -m "<describe the new file>"
   ```

6. Report: file path, commit SHA, one-line summary.
