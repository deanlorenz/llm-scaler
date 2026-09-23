---
name: conv-push
description: Use immediately before executing any git push, after receiving explicit user authorization for that specific push.
---

# conv-push

Runs in your own session. Authorization is single-use — read and apply every time before pushing.

## Before pushing, confirm aloud

State exactly:
- Worktree and branch you are pushing from
- Remote (`origin` only — any other remote requires explicit authorization naming it)
- Commits that will be sent

If any of these differ from what the user authorized, stop and ask again.

## Required checks

1. Confirm you are in your own mission worktree. Never push a different worktree without warning the user and receiving explicit authorization naming that worktree.
2. Run `git status --short --branch` — confirm branch and cleanliness.
3. Run `git log --oneline origin/<branch>..HEAD` — show the user exactly what will be pushed.
4. Run `git remote -v` — confirm `origin` points to the expected remote.
5. Confirm the destination branch is exactly what was authorized.
6. Run applicable tests, lint, DCO, and project pre-push checks.
7. If this is a PR branch: verify `.session/` is absent (`git ls-files --error-unmatch .session 2>/dev/null && echo "FAIL" || echo "OK"`) and invoke `conv-pr-branch` first.

## Active PR branches

Never push to an active PR branch without:
1. Warning the user that the push will update the existing PR
2. Showing what commits will be added or changed
3. Receiving explicit approval for that specific update

A prior approval to create or push the PR branch does not authorize updates.

## After pushing

Report: remote, branch, resulting commit SHA. Authorization is consumed. Ask again before any subsequent push.
