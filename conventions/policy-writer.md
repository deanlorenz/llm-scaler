# `policy-writer` mission rules

Read this when acting in any role on the `policy-writer` mission.

The `policy-writer` mission is the owner of global policy: `CONVENTIONS.md`, `conventions/`, and
canonical `.claude/skills/` stored on the `session-tracking` branch.

## Responsibilities & Scope

- Drafts all changes to conventions and skills in `worktrees/policy-writer`.
- After verification, tells the user which rule or skill files changed and asks whether to install them
  in `worktrees/session-tracking`.
- Installs only after the user explicitly authorizes that installation.
- Approval to draft, review, commit, or push does not authorize installation.
- Processes suggestions from `session-tracking/suggestion-box/`.
- Commits convenience symlinks and other explicitly reported mission-tracking artifacts created by
  other missions under `session-tracking/missions/`.

## Session-tracking maintenance order

At session start, before mission work:
1. Subscribe to `session-tracking.pending-commits` and `session-tracking.suggestions`.
2. Fetch pending-commit messages from the last recorded cursor.
3. Inspect and process each reported artifact in a separate, reviewable step.
4. Never delete, move, overwrite, or clean up an unowned artifact. If an artifact is unexplained,
   stop and ask the user or its owner before changing it.
5. Commit session-tracking maintenance changes before installing policy changes.
6. Record a short, human-readable summary of what was committed in the policy-writer ledger.

At session end, and before any install:
1. Check the pending-commit channel again.
2. Process newly reported artifacts before installing policy changes.
3. Publish a short, human-readable completion note describing what was committed.

Do not combine mission-tracking maintenance and policy installation into one commit or one
unreviewed operation.

## Checking for Pending `session-tracking` Commits

On startup / resume of a `policy-writer` session, check if other missions left pending symlink notes on agentbus:

```
agentbus_fetch_since(topic="session-tracking.pending-commits", since_seq=0)
```

For each unprocessed note found:
1. Inspect git status in `worktrees/session-tracking` for the described changes (e.g. `missions/<name>/`).
2. Review the exact paths and file types before staging. Do not delete, move, or overwrite an artifact
   without explicit authorization from its owner or the user.
3. Commit only the reviewed maintenance artifacts on the `session-tracking` branch:
   ```bash
   git add missions/<name>/ && git commit -m "chore: commit symlinks for <name>"
   ```
4. Record the processed sequence number and a short human-readable description of the committed
   paths and purpose in your live ledger. Commit identifiers are optional implementation details.

If no pending notes exist, proceed with standard mission tasks.

## Suggestion-Box Monitoring & Processing

1. **Agentbus Channel Subscription:** On startup, `policy-writer` subscribes to `session-tracking.suggestions` (and/or `session-tracking.pending-commits`) per `conventions/agentbus.md` to receive notifications when new suggestion files land in `session-tracking/suggestion-box/`.
2. **Reviewing Suggestions:**
1. Read unprocessed suggestion files (`YYYY-MM-DD-HHMM-<mission>.md`).
2. Decide whether the finding warrants a global rule in `CONVENTIONS.md`, a situational file in `conventions/`, or belongs only in that mission's local spec.
3. Draft modifications in `worktrees/policy-writer`.
4. Prefix processed files with `processed-` in `session-tracking/suggestion-box/`.
