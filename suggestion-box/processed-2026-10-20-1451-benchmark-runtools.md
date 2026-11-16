# Suggestion — 2026-10-20 — benchmark-runtools

**Source:** Triage of `benchmark-runtools` (user asked whether sibling benchmark-* worktrees were
integrated into its make targets). Found that commit `681dc7e5a` ("benchmark-extract: operational
files only", 2026-08-30) deleted 369 Makefile lines — the whole `bench-*` run layer plus
`benchmark-init`, `-preflight`, `-reset-run`, `-run-only`, `-run-only-check`,
`-verify-scaledobjects`, `-guard` — and replaced them with 41 added lines (two extract targets).
Nothing replaced the removed targets. The loss went unnoticed for ~7 weeks.

## What actually happened (verified from reflog and blob hashes)

- 03:06 `git cherry-pick` produced `d5673902a`: Makefile **+31 lines only**. This was correct.
- 03:44 `git reset` ("moving to HEAD~1") discarded that clean cherry-pick.
- 03:46 `git commit` produced `681dc7e5a`. Its Makefile, `extract.py` and `AGENTS.md` blobs are
  byte-identical to those at `dab4b97c`, a different branch's commit. The files were copied
  wholesale (the reflog does not record the exact command, e.g. `git checkout <rev> -- <files>`).
- The commit message said "Cherry-picked operational files". It was not a cherry-pick.
- `git cherry-pick` itself behaved correctly. The failure was a whole-file copy onto files with
  branch-local edits, an unreviewed staged diff (`-369`), and a misleading message.

Recovery: `d5673902a` kept as tag `backup/benchmark-extract-clean-cherry-pick-d5673902a`.

## Suggested rules (for `CONVENTIONS.md` ground rules or a `conv-*` skill; compatible with `conv-pr-branch` "cherry-pick only")

1. Never use `git checkout <rev> -- <file>` or `git restore --source <rev> <file>` on a file that
   has branch-local edits. To take part of a commit, use `git cherry-pick -n <sha>` and unstage
   what is not wanted with `git restore --staged -p`.
2. Never `git reset` away a clean cherry-pick to redo it by hand. If its scope is wrong, narrow it
   with `cherry-pick -n` plus `restore --staged -p`.
3. Before every commit run `git diff --cached --stat` and compare it to the message. A deletion
   count the message does not mention is a hard stop — ask the user.
4. A commit message may say "cherry-pick" only if `git cherry-pick` created the commit. Otherwise
   write "copied from <sha>".
5. After porting changes to `Makefile` (or any file defining named targets), diff the target list
   before and after; it must differ only by the intended names:
   `diff <(git show HEAD:Makefile | grep -oE '^[a-zA-Z0-9_.-]+:' | sort -u) <(grep -oE '^[a-zA-Z0-9_.-]+:' Makefile | sort -u)`

## Open question for policy-writer

Rule 3 could be enforced mechanically (e.g. a commit hook that refuses when `--cached --numstat`
deletions exceed a threshold on a file whose message says "cherry-pick"). Worth deciding whether
a convention suffices or a hook is warranted.
