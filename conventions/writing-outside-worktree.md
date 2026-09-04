# Writing outside the mission worktree


Read this only when a write outside the session's mission worktree is required or a pinned
session encounters the worktree isolation guard.

## Authorization first

A cross-worktree write requires a specific exception from the user. General mission ownership,
read access, an earlier exception, or a tool permission does not authorize it. State the exact
destination and intended change when asking.

After authorization:

1. Confirm the destination and scope match the granted exception.
2. Read the destination's own conventions and ownership signals before writing.
3. Check for `.wip` locks or other active ownership markers.
4. Make only the specifically authorized change.
5. Validate and report it; do not groom adjacent files or inspect the other worktree's general
   health.

## Pinned-session mechanics

A session pinned via `EnterWorktree` cannot use `Edit` or `Write` outside the pinned worktree,
even when `permissions.allow` explicitly names the destination. The isolation guard is a
harness-level structural veto and runs before the permissions allowlist.

Plain shell redirection may bypass that veto. This is an inconsistency in guard coverage, not a
sanctioned workaround. Never use shell redirection, `cd`, a subshell, process substitution, or
another shell mechanism to bypass the boundary.

If the user authorizes the cross-worktree write but the isolation guard prevents it, stop and
ask the user how to proceed. `ExitWorktree` returns to the repository root, and re-entering a
worktree requires interactive authorization; do not assume a free round trip.
