# Working outside your worktree

## 1. Never leave your worktree for writes or git operations
Do not use `EnterWorktree`/`ExitWorktree` to relocate just to read or write another
worktree's files. Do not `cd` into another worktree. This includes git operations: never
leave your worktree to run a git command.

## 2. Gates
- **Reads:** always allowed, from inside your own worktree, using one of the methods in §3.
- **Writes:** never allowed without explicit, specific authorization from the user (see §4).

## 3. Reading another worktree/branch, from inside your own worktree
- **File on disk, another worktree's checkout:** `cat <full-path-into-other-worktree>` or
  `git -C <other-worktree-path> <command>` — both work for reads.
- **File on a branch, regardless of whether it's checked out anywhere:**
  `git show <branch>:<path>` — run from inside your own worktree, no `-C` needed.

## 4. Writing to another worktree

### 4a. Never bypass the gate
Never use `cd`, a subshell, process substitution, shell redirection (`>`, `>>`, `tee`), or any
other shell mechanism to write into another worktree's path without authorization. Shell
redirection is **not** blocked by the harness the way `Edit`/`Write`/`git -C` are — it will
silently succeed. That makes it an unauthorized write, not a safe workaround.

### 4b. When you need explicit authorization
Before any write outside your own worktree. Every instance needs its own authorization:
mission ownership, an earlier exception, read access, or a tool permission do not count.
State the exact destination path and the exact change before asking.

### 4c. Checks before writing
1. Confirm the destination and change match exactly what was authorized.
2. Read the destination's own conventions.
3. Check for a `.wip` lock or other active ownership marker on the destination file.
4. If the destination file already exists, read it first — never overwrite unseen content.
5. Make only the authorized change — nothing adjacent.

### 4d. Can you `mv` at the destination?

**Yes — use `.wip` protocol:** follow `conventions/wip-editing.md` (Case 1 for an existing
file, Case 2 for a new file in a shared folder). Shell `mv` at the destination path is all
that is needed — no special pinned-session tooling required.

**Append** is a separate alternative when you only need to add to the end of a file and
don't need full replace: `>> /full/path/to/other-worktree/file`. Append does not require
`.wip` — it is non-destructive and atomic enough for single-line appends. For anything
beyond a single append, use `.wip`.

**No — `mv` is structurally blocked (sandboxed session):** `Edit`/`Write` tools and `git`
operations targeting the other worktree are hard-blocked by the harness. Use `cp` as a
fallback of last resort:

1. **Verify the target does not already exist**, or if it does, that it is tracked:
   `cat <full-destination-path>` or `git show <branch>:<path>`. Never overwrite untracked
   content — it has no recovery path.
2. **Write the file locally** in your own worktree first.
3. **`cp` to destination:**
   ```bash
   cp <local-path> /full/path/into/other/worktree/file
   ```
4. **Record the copy explicitly in your own ledger**, naming the full destination path and
   noting it is uncommitted — the next session in the target worktree needs to `git
   add`/commit promptly.
5. **The destination commit is the target worktree session's job** (same as §5).

## 5. Git commits and pushes on another worktree's branch

Never commit or push another branch from outside its worktree, by any method — including
`git checkout <branch> -- <path>` and `git push <remote> <branch>:<branch>`.

Only that worktree's own session commits and pushes its branch.
