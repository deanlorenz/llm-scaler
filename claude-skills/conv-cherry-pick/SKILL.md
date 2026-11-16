---
name: conv-cherry-pick
description: Use when cherry-picking, porting changes from another commit or branch, or committing a staged diff that came from one.
---

# conv-cherry-pick

Runs in your own session. Apply before and while porting changes from another commit.

## Rules

1. Take a commit with `git cherry-pick <sha>`. To take only part of it, use `git cherry-pick -n <sha>`, then unstage what is not wanted with `git restore --staged <path>`. Whole files are the normal case.
   - Hunk level is an option. It works from an agent (verified): pipe one answer per hunk, in `git diff --cached` order, where `y` unstages that hunk and `n` keeps it. Example for two hunks, keep the first and drop the second: `printf 'n\ny\n' | git restore --staged -p <path>`. Without piped input `-p` waits on a prompt nothing answers. Check the result with `git diff --cached --stat`.
2. Never copy whole files from a commit onto files with branch-local edits: no `git checkout <rev> -- <file>`, no `git restore --source <rev> <file>`.
3. Never `git reset` away a clean cherry-pick to redo it by hand. If its scope is wrong, narrow it with rule 1.
4. Before every commit of ported changes, run `git diff --cached --stat` and compare it to the commit message. A deletion the message does not mention is a hard stop: ask the user.
5. A commit message may say "cherry-pick" only if `git cherry-pick` created the commit. Otherwise write "copied from <sha>".
