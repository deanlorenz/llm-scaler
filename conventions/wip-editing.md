# Editing a file you don't own — the `.wip` protocol

Read this before editing any file or writing any new file into a folder that you don't own.

## Who owns a file

You own a file if you created it in this session, or if it lives in your own mission
worktree and no other active session has been granted write access to it. Everything else —
files in another worktree, shared convention files, another mission's STATE — you do not own.

**When dispatching an agent:** tell the agent explicitly in its task file whether it needs
`.wip` for the files it will touch. Examples: a coder writing new code files it creates does
not need `.wip`; an agent updating STATE.md it does not own does need `.wip`.

## Case 1: Editing an existing file you don't own

1. **Verify the file exists and the current version is tracked:**
   ```bash
   git ls-files <path>   # must show the file; if empty, it is untracked — stop and investigate
   ```
2. **Verify no existing `.wip` lock:**
   ```bash
   test ! -f <path>.wip || echo "LOCKED — stop"
   ```
   If locked, stop — someone else is mid-edit. Do not proceed.
3. **Claim — rename to `.wip`:**
   ```bash
   mv <path> <path>.wip
   ```
   Never use `cp` — that leaves the original in place and allows concurrent edits.
4. **Edit `<path>.wip`** using normal tools.
5. **Release — rename back:**
   ```bash
   mv <path>.wip <path>
   ```
6. **Commit:** `git add <path> && git commit`.

Reads are never blocked — use `git show HEAD:<path>` or read `<path>.wip` directly while
the lock is held.

## Case 2: Writing a new file into a shared folder you don't own

1. **Verify the target does not already exist:**
   ```bash
   test ! -f <destination-path> || echo "EXISTS — stop and investigate"
   git show <branch>:<path>   # also check branch history
   ```
   If it exists (on disk or in history), stop — this may be a rename, not a new file.
2. **Claim the name with a `.wip` placeholder:**
   ```bash
   touch <destination-path>.wip
   ```
3. **Write the file locally** in your own worktree first.
4. **Place at destination — never bypass `.wip`:**
   ```bash
   cp <local-path> <destination-path>.wip   # overwrite placeholder with real content
   mv <destination-path>.wip <destination-path>
   ```
   Only if `mv` at the destination is structurally blocked (sandboxed session) may you `cp`
   directly to the final path — see `conventions/working-outside-worktree.md` §4f.
5. **Commit:** `git add <destination-path> && git commit`.

