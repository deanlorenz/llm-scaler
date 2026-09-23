---
name: conv-pr-branch
description: Use when creating a PR branch, cherry-picking commits for a PR, or verifying a branch is clean before pushing a pull request.
---

# conv-pr-branch

Runs in your own session. Apply before creating, populating, or pushing a PR branch.

## Rules

1. **Spawn off `main`.** Always branch from `main` (or the project's stated base branch — check `CONTRIBUTING.md` or the issue). Never base a PR branch on the mission branch.

2. **Cherry-pick only.** Select only the commits intended for the PR from the mission branch. Internal bookkeeping, WIP commits, and anything in `.session/` must not appear.

3. **Verify `.session/` is absent before pushing:**
   ```bash
   git ls-files --error-unmatch .session 2>/dev/null && echo "FAIL: .session present" || echo "OK"
   ```
   If this check fails: stop, do not push, remove or re-create the branch without those files.

4. **Push to `origin` only.** Confirm with `git remote -v` before pushing. Never push a PR branch to `upstream` or any other remote.

5. **Use the correct GitHub API.** This repo has two GitHub MCP servers (`gh-public`, `gh-ibm`). Match the server to the repo's hosting — check `git remote -v` for the remote URL. If unsure, ask.

6. **Run all pre-checks before pushing.** Lint, DCO sign-off, and any project-required checks must pass locally first. If the project has a `Makefile` target or CI pre-check script, use it.

7. **Confirm the target base branch.** Check `git remote -v`, `CONTRIBUTING.md`, or the issue. Do not assume `main` is correct — ask if unsure.

8. **PR branch is ephemeral.** Once the PR is merged or closed, delete the PR branch worktree locally. The mission branch is the source of truth.

## Then invoke `conv-push`

After these checks pass, invoke `conv-push` for the actual push step.
