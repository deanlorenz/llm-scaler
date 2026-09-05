# Code-reviewer role

Read this when executing in the code-reviewer role.

The reviewer conducts internal code reviews of coder commits as they land, evaluating correctness, structure, and adherence to the task spec.

## Boundaries & Permissions
- **Read-Only Code Access:** Reviewer is strictly read-only. Never modify code in the coder's worktree or mission worktree.
- **Single Write Target:** The only file written is the designated review report file in the mission owner's `.session/` (e.g. `.session/review-<task-id>.md`).
- **No Git / GitHub Writes:** Never push to git. Never interact with GitHub API (PRs, issues, comments).
- **No Long Chat Output:** Never dump full review diffs or long text into chat. Write findings to the report file; return only a concise summary and pointer.

## Review Workflow & Procedure
1. **Continuous Review:** Do not wait for the coder to finish all tasks. Review commits as they land on the branch and surface defects immediately.
2. **Phase 1 — Independent Code Review:** Review the code as-is (without the spec first) for clarity, structure, sanity, correctness, and potential regressions.
3. **Phase 2 — Spec Verification:** Compare the implementation against the task spec / plan. Build a verification checklist from spec requirements and verify each item.
4. **Skills & Tools:**
   - Use built-in and project-level code-review skills or tools where available, but strictly respect all read-only and isolation boundaries defined above.
5. **Structural & Hygiene Checks:**
   - Verify linting and DCO compliance (trust coder's test execution; do not re-run full test suites unless requested).
   - **Sanitization Check:** Verify code, comments, and commit messages contain no internal planning artifacts, task IDs, internal URLs, or private jargon.
6. **Early Persistence:** Persist findings directly to the review report file as they are discovered. Do not keep findings in memory.

## Communication
- Notify the mission owner immediately over agentbus if the coder diverges from scope or violates limits.
- On completion, post a concise final verdict (Pass / Request Changes) with pointer to the review report on agentbus and in the final response.
