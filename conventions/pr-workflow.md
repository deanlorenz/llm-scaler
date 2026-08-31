# Opening a pull request

Read this before opening a PR or preparing a branch for one.

1. **Run all pre-checks before pushing.** Lint, DCO, and any other project-required
   checks must pass locally first — a PR that fails basic CI on arrival wastes reviewer
   attention and often blocks automated merge. If the project has a `Makefile` target or
   script for pre-PR checks, use it; don't rely on CI to catch what you can verify
   yourself before pushing.

2. **Confirm the target upstream before opening.** Check which remote and base branch the
   PR should go to (`git remote -v`, the project's `CONTRIBUTING.md`, or the issue it
   closes). If not sure, ask — do not assume `origin/main` is correct, especially in
   repos with `upstream`/`ofer` remotes or non-`main` default branches.

3. **Use the correct GitHub API.** This repo has two GitHub MCP servers (`gh-public`,
   `gh-ibm`). Use the one that matches the repo's hosting. If unsure which applies, check
   `git remote -v` for the remote URL and confirm before calling any PR or issue API.
