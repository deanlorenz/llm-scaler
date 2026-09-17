# Resuming, handing off, and winding down a mission

## Session Log

The session log in `.session/STATE.md` is append-only. Active ledgers live under `.session/`;
move to `.session/ledger/` on retirement. Example format:

```markdown
## Session log

- 2026-08-27 session=<slug> status=active ledger=.session/<slug>.md
- 2026-08-27 session=<slug> status=retired ledger=.session/ledger/<slug>.md
```

Status values:
- `active` — session is working, checkpointing, or safely paused.
- `retired` — session has ended or transferred ownership.
- Fully resolved: `status=retired` AND ledger carries a `## Verified <date>` marker.

## Resume / Takeover Protocol

1. **Ask user:** "Continuing `<slug>`?" — confirm mission and session before proceeding.
2. **Live presence check:** Check agentbus for a recent heartbeat from the active slug.
   If still alive: stop, do not take over, ask the user.
3. **Pending scan:** Any session log entry that is `active`, or `retired` without a
   `## Verified` marker, is pending. Captured retired ledgers must be under `.session/ledger/`.
4. **Lock & retire:** Update any unretired pending session to `status=retired`.
5. **Run `ledger-capture`:** Execute against the pending ledger in the foreground.
6. **Verify:** Confirm `## Verified <date>` is appended to the processed ledger.
7. **Ground-truth check:** If STATE has any checklist or file list derived from an external
   source (a diff, file tree, test suite, config), launch a background agent to regenerate
   and diff it against STATE before starting work.
8. **Declare ownership:** Publish on agentbus before recording the new active session.

## Checkpoint & Wind-Down Protocol

1. **Stop active operations:** Stop any background workers/subagents launched by this session.
2. **Update ledger:** Append all unrecorded findings, decisions, corrections, false starts.
3. **Update STATE:** Update task checklist, `Last completed`, `Next step`, `Status`.
4. **Run `ledger-capture`:** Run against own active ledger; confirm `## Verified <date>` appended.
5. **Retirement:**
   - Ending or transferring: update session log entry to `status=retired`.
   - Checkpointing to continue: keep `status=active`.
6. **Agentbus release (if retiring):** Publish release on `mission.<mission-name>`.

## Agentbus Ownership Protocol

Taking ownership:
```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<slug> taking ownership of <mission-name>")
```

Releasing ownership:
```
agentbus_publish(topic="mission.<mission-name>", kind="handoff",
  body="session=<slug> releasing ownership of <mission-name>")
```

## `ledger-capture` Contract

A focused agent assigned to process exactly one ledger file:

1. **Agentbus:** Follow standard agentbus contract (`conventions/agentbus.md`).
2. **Capture:** Read the entire ledger. Identify every correction, decision, rule, safety
   requirement, and unresolved issue that must survive the session. Check especially for
   ownership, authorization, and data-preservation rules.
3. **Write destinations:** The mission's own `.session/STATE.md` and its internal plan/spec only.
4. **Prohibition:** Never write to `CONVENTIONS.md` or `conventions/`.
5. **Global findings:** Any finding warranting a global rule goes to
   `session-tracking/suggestion-box/YYYY-MM-DD-HHMM-<mission-name>.md`.
   Only `policy-writer` processes suggestion-box entries.
6. **Completion — append to the processed ledger:**
   ```markdown
   ## Verified YYYY-MM-DD — <all points already captured | folded in: summary>

   | Ledger point | Durable destination | Action taken |
   |---|---|---|
   | <point / finding> | <doc path & section> | <None needed | Added to X | Folded into Y> |
   ```

## Doc-Reference Path Rule

Every reference across tracked docs must be a repo-root-relative path
(e.g. `worktrees/policy-writer/.session/STATE.md`) — never absolute, never a bare filename.
