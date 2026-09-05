# Resuming, handing off, and winding down a mission

Read this when executing `/resume-mission` or `/wind-down`, or when taking over or ending work on a mission.

## Session Log Lifecycle & Status Rules

Every mission's `.session/STATE.md` maintains an append-only **Session log** section under `.wip` protocol (`conventions/wip-editing.md`):

```markdown
## Session log

- 2026-08-27 session=<slug> status=active ledger=.session/<slug>.md
- 2026-08-27 session=<slug> status=retired ledger=.session/<slug>.md
```

### Status Values:
- **`active`**: Currently working the mission, checkpointing during active work, or safely paused across turn boundaries / compactions.
- **`retired`**: The session is genuinely ending its engagement on this mission (transferred ownership, finished work, or closed out).
- **Resolution**: An entry is **fully resolved** only when its status is `retired` AND its named ledger file carries a `## Verified <date>` marker.

## Takeover Protocol (Executed during `/resume-mission`)

1. **Pending Scan:** Scan all Session log entries in `.session/STATE.md`. Any entry that is `active`, or `retired` without a `## Verified` marker in its ledger, is **pending**.
2. **Lock & Retire:** Under `.wip` protocol, update any unretired pending session to `status=retired`.
3. **Run `ledger-capture`:** Execute `ledger-capture` in the foreground against that pending ledger to fold uncaptured findings into durable docs (`STATE.md` or internal plan).
4. **Append Verification:** Confirm `## Verified <date>` is appended to the processed ledger.
5. **Agentbus Ownership:** Declare mission ownership on agentbus before recording the new active session.

## Checkpoint & Wind-Down Protocol (Executed during `/wind-down`)

Wind-down establishes a durable, recoverable checkpoint so work is preserved across turn boundaries, compactions, clears, or reloads.

1. **Stop Active Operations:** Stop any background workers/subagents launched by this session.
2. **Update Live Ledger:** Append all unrecorded findings, decisions, corrections, and false starts to `.session/<slug>.md`.
3. **Update STATE.md:** Under `.wip` protocol, update task checklist, `Last completed`, `Next step / resume point`, and `Status`.
4. **Run `ledger-capture`:** Run `ledger-capture` on own active ledger to ensure durable reflection in `STATE.md` or internal spec, and append `## Verified <date>`.
5. **Retirement / Status Update (if ending engagement):**
   - If closing the session or transferring ownership: update Session log entry to `status=retired`.
   - If checkpointing while intending to continue: keep entry `status=active`.
6. **Agentbus Release (if retiring):**
   - If retiring ownership, publish release message on `mission.<mission-name>`.

## Agentbus Ownership Protocol

- **Taking Ownership:**
  ```
  agentbus_publish(topic="mission.<mission-name>", kind="handoff",
    body="session=<slug> taking ownership of <mission-name>")
  ```
- **Releasing Ownership:**
  ```
  agentbus_publish(topic="mission.<mission-name>", kind="handoff",
    body="session=<slug> releasing ownership of <mission-name>")
  ```

## `ledger-capture` Contract

A focused agent assigned to process exactly one ledger file:
1. **Allowed Write Destinations:** The mission's own `.session/STATE.md` and its internal plan/spec doc only.
2. **Prohibition:** `ledger-capture` must **never** write directly to `CONVENTIONS.md` or `conventions/`.
3. **Global Findings (Suggestion Box):** Any finding that warrants a global rule must be written as an atomic file into `session-tracking/suggestion-box/` named `YYYY-MM-DD-HHMM-<mission-name>.md`. Only `policy-writer` processes suggestion-box entries.
4. **Completion Marker & Summary Table:**
   Append a verification marker to the end of the processed ledger, including a summary table of findings and actions taken:
   ```markdown
   ## Verified YYYY-MM-DD — <all points already captured | folded in: summary>

   | Ledger point | Durable destination | Action taken |
   |---|---|---|
   | <point / finding> | <doc path & section> | <None needed | Added to X | Folded into Y> |
   ```
## Doc-Reference Path Rule

Every reference across tracked docs must be a **repo-root-relative path** (e.g. `worktrees/policy-writer/.session/STATE.md`) — never filesystem-absolute, never a bare filename. Always state the worktree/branch if not obvious from context.
