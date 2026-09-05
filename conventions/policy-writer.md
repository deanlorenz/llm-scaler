# `policy-writer` mission rules

Read this when acting in any role on the `policy-writer` mission.

The `policy-writer` mission is the owner of global policy: `CONVENTIONS.md`, `conventions/`, and
canonical `.claude/skills/` stored on the `session-tracking` branch.

## Responsibilities & Scope

- Drafts all changes to conventions and skills in `worktrees/policy-writer`.
- After verification and user approval, installs approved conventions and skills to `worktrees/session-tracking`.
- Processes suggestions from `session-tracking/suggestion-box/`.
- Commits convenience symlinks created by other missions under `session-tracking/missions/`.

## Checking for Pending `session-tracking` Commits

On startup / resume of a `policy-writer` session, check if other missions left pending symlink notes on agentbus:

```
agentbus_fetch_since(topic="session-tracking.pending-commits", since_seq=0)
```

For each unprocessed note found:
1. Inspect git status in `worktrees/session-tracking` for the described changes (e.g. `missions/<name>/`).
2. Commit them if present on the `session-tracking` branch:
   ```bash
   git add missions/<name>/ && git commit -m "chore: commit symlinks for <name>"
   ```
3. Record the processed sequence number in your live ledger.

If no pending notes exist, proceed with standard mission tasks.

## Suggestion-Box Processing

When reviewing `session-tracking/suggestion-box/`:
1. Read unprocessed suggestion files (`YYYY-MM-DD-HHMM-<mission>.md`).
2. Decide whether the finding warrants a global rule in `CONVENTIONS.md`, a situational file in `conventions/`, or belongs only in that mission's local spec.
3. Draft modifications in `worktrees/policy-writer`.
4. Prefix processed files with `processed-` in `session-tracking/suggestion-box/`.
