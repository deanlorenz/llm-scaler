---
name: conv-pr-workflow
description: Use when opening a pull request via the GitHub API or preparing to submit a PR for review.
---

# conv-pr-workflow

Runs in your own session. Apply before opening a PR or calling any GitHub PR API.

## Checklist

1. **Run all pre-checks before pushing.** Lint, DCO, and any project-required checks must pass locally first — a PR that fails basic CI on arrival wastes reviewer attention and often blocks automated merge. Use the project's `Makefile` target or pre-PR script if one exists.

2. **Confirm the target upstream before opening.** Check:
   ```bash
   git remote -v
   cat CONTRIBUTING.md   # or check the issue being closed
   ```
   If not sure which remote and base branch to target, ask — do not assume `origin/main`.

3. **Use the correct GitHub API.** This repo has two GitHub MCP servers (`gh-public`, `gh-ibm`). Use the one that matches the repo's hosting. Check `git remote -v` for the remote URL. If unsure which applies, confirm before calling any PR or issue API.

4. **Invoke `conv-pr-branch` first** if the PR branch has not yet been verified clean.
