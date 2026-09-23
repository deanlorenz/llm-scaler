---
name: conv-working-outside-worktree
description: Use when about to read from or write to another mission's worktree, or when needing to perform a cross-worktree git operation.
---

# conv-working-outside-worktree

Runs in your own session. Read and apply before any cross-worktree operation.

## Reads — always allowed

From inside your own worktree only:
- File on disk in another checked-out worktree: `cat <full-path-into-other-worktree>`
- File on any branch: `git show <branch>:<path>` — no `-C` needed
- Never `cd` into another worktree or use `git -C <other-path>` for reads

## Writes — require explicit authorization

Before any write outside your own worktree:
1. State the exact destination path and exact change.
2. Receive explicit authorization naming that specific path. Mission ownership, read access, and prior authorizations do not count.
3. Confirm destination path and change match exactly what was authorized.
4. Read the destination file first — never overwrite unseen content.
5. Make only the authorized change — nothing adjacent.
6. Check for a `.wip` lock on the destination file before writing. If locked, stop.

## How to write

**If `mv` works at the destination** (normal session): use the `.wip` protocol — invoke `conv-wip-editing`.

**If `mv` is structurally blocked** (sandboxed session):
1. Verify target does not already exist, or if it does, that it is tracked.
2. Write the file locally in your own worktree first.
3. `cp <local-path> /full/path/into/other/worktree/file`
4. Record the copy in your ledger: full destination path, note it is uncommitted.
5. The destination worktree's own session must `git add` / commit it.

**Append only** (adding to end of file, no replace needed): `>> /full/path/to/file` — does not require `.wip`. Single-line appends only; anything more complex, use `.wip`.

## Never

- `cd` into another worktree
- Shell redirection (`>`, `>>` beyond single appends, `tee`) to write into another worktree without authorization — it bypasses the harness and will silently succeed
- Commit or push another branch from outside its worktree (no `git checkout <branch> -- <path>`, no `git push <remote> <branch>:<branch>`)
