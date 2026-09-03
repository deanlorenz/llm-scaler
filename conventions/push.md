# Pushing git branches

Read this only after the user explicitly authorizes a specific push.

## Authorization is single-use

Every push requires its own explicit authorization. Permission to push one branch, worktree,
remote, or commit does not authorize another push later in the session. Mission ownership,
prior push approval, and approval to commit are not standing push permission.

Before pushing, repeat the exact worktree, branch, remote, and commits that will be pushed. If
any differs from what the user authorized, stop and ask again.

## Required checks

1. Confirm you are operating in your own mission worktree. Never push a different worktree
   without warning the user and receiving explicit authorization naming that worktree.
2. Run `git status --short --branch` and inspect the commits that would be sent.
3. Run `git remote -v`. Push only to `origin`. Any other remote requires explicit
   authorization naming that remote — not inferred from general mission ownership or a prior
   push. PR branch pushes require extra care regardless of remote: show what commits will be
   added or changed and receive explicit approval before pushing.
4. Confirm the destination branch is exactly the authorized branch.
5. Run applicable tests, lint, DCO, and project pre-push checks.
6. If this is a PR branch, verify `.session/` is absent and follow
   `conventions/pr-branch.md`.

## Active PR branches

Never push to an active PR branch without first warning the user that the push will update the
existing PR, showing what commits will be added or changed, and receiving explicit approval for
that update. An earlier approval to create or push the PR branch does not authorize later
updates.

## After pushing

Report the remote, branch, and resulting commit SHA. The authorization is consumed; ask again
before any subsequent push.
