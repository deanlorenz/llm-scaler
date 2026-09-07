# Working outside your worktree

## 1. Never leave your worktree for reads, writes, or git operations
Do not use `EnterWorktree`/`ExitWorktree` to relocate just to read or write another
worktree's files. Do not `cd` into another worktree. Do not use `git -C <other-worktree-path>`
— the harness blocks it for a pinned session anyway (including `-C <repo-root>`). This
includes git operations: never leave your worktree to run a git command either.

## 2. Gates
- **Reads:** always allowed, from inside your own worktree, using one of the methods in §3.
- **Writes:** never allowed without explicit, specific authorization from the user (see §4).

## 3. Reading another worktree/branch, from inside your own worktree
- **File on disk, another worktree's checkout:** `cat <full-path-into-other-worktree>` (plain
  shell read — no `-C`, no `cd`).
- **File on a branch, regardless of whether it's checked out anywhere:**
  `git show <branch>:<path>` — run from inside your own worktree, no `-C`.
- **Do not use** `git -C <any-path-outside-your-worktree>` for any purpose, including reads.
  It is blocked structurally for a pinned session.

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

## 5. Git commits and pushes on another worktree's branch

Never commit or push another branch from outside its worktree, by any method — including
`git checkout <branch> -- <path>` and `git push <remote> <branch>:<branch>`.

Only that worktree's own session commits and pushes its branch.
