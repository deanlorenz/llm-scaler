# Opening a PR branch

Read this before creating a PR branch or pushing a PR.

## Lifecycle

A PR branch is **ephemeral and purpose-built** — never the mission branch itself. The mission
branch tracks work in progress; a PR branch is a clean, curated slice of it destined for
upstream review.

1. **Spawn off `main`.** Always branch from `main` (or the project's stated base branch —
   check `CONTRIBUTING.md` or the issue being closed). Never base a PR branch on the mission
   branch.

2. **Mission owner selects commits.** Cherry-pick only the commits intended for the PR from
   the mission branch. Internal bookkeeping, WIP commits, and anything in `.session/` must
   not appear.

3. **`.session/` must never be in a PR branch.** The mission branch has `.session/` in its
   `.gitignore` as a safety net. Before pushing, verify explicitly:

   ```bash
   git ls-files --error-unmatch .session 2>/dev/null && echo "FAIL: .session present" || echo "OK"
   ```

   If that check fails, stop — do not push. Remove or re-create the branch without those
   files.

4. **Push to `origin` only.** Never push a PR branch to `upstream` or any other remote. Confirm
   with `git remote -v` before pushing if unsure which remote is `origin` for this repo.

5. **Use the correct GitHub API.** This repo has two GitHub MCP servers (`gh-public`, `gh-ibm`).
   Match the server to the repo's hosting — check `git remote -v` for the remote URL. If unsure,
   ask before calling any PR API.

6. **Run all pre-checks before pushing.** Lint, DCO sign-off, and any other project-required
   checks must pass locally first. If the project has a `Makefile` target or CI pre-check
   script, use it. Don't rely on CI to catch what you can verify yourself first.

7. **Confirm the target base branch.** Check which base branch the PR should merge into
   (`git remote -v`, `CONTRIBUTING.md`, or the issue). Do not assume `main` is correct —
   ask if unsure.

8. **PR branch worktree is ephemeral.** Once the PR is merged (or closed), delete the PR branch
   worktree locally. It has no ongoing tracking role — the mission branch is the source of
   truth.
