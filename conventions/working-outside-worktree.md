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

### 4d. `.wip` protocol for shared files
Follow `conventions/wip-editing.md`. Summary: rename `<file>` → `<file>.wip` before editing;
if `.wip` already exists, stop — someone else is mid-edit; rename back after editing and commit.

### 4e. Pinned-session writes — two sanctioned methods
1. **Append via full path, using shell:** `>> /full/path/to/other-worktree/file`. Only the
   exact authorized file, only append, never truncate/overwrite.
2. **Write locally, then copy via full path, using shell:** write the file in your own
   worktree, then `cp /full/path/in/your/worktree/file /full/path/into/other/worktree/file`.
   For full replace/overwrite. Before copying, `diff` source and destination and confirm the
   overwrite is expected — never `cp` onto a destination you haven't read.

Neither method commits anything. A destination commit is the destination session's job (§5).

### 4f. When all write paths are blocked (worktree-isolated / sandboxed session)

A worktree-isolated session may find that `Edit`/`Write` tools and `git` operations targeting
another worktree are hard-blocked by the harness. Raw shell `cp` may be the only path that
gets through. Rules for that fallback:

1. **Never copy blindly.** Before copying, read what is already at the destination:
   `cat <full-destination-path>` or `git show <branch>:<path>`.
2. **If the destination file already exists, verify it is tracked before overwriting:**
   confirm `git ls-files <path>` (run from inside a session positioned in the target worktree,
   or via `git show <branch>:<path>`) shows the file is in git history. If it is not tracked,
   do not overwrite — the existing content has no recovery path.
3. **Record the copy explicitly in your own ledger**, naming the full destination path and
   noting it is uncommitted. This is the breadcrumb the next session positioned in the target
   worktree needs to `git add`/commit promptly rather than discovering an unexplained untracked file.
4. **The destination commit is the target worktree session's job** (same as §5) — the
   session that did the `cp` cannot commit it.

## 5. Git commits and pushes on another worktree's branch

Never commit or push another branch from outside its worktree, by any method — including
`git checkout <branch> -- <path>` and `git push <remote> <branch>:<branch>`.

Only that worktree's own session commits and pushes its branch.
