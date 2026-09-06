Continues: .session/2026-09-05-policy-writer-16.md

# Session 17 — policy-writer

Date: 2026-09-06

## Start

- Took over policy-writer after confirming user approval.
- Session 16 was retired but lacked a verification marker; verification was appended and all points were already reflected in STATE.
- Declared ownership on agentbus as session 2026-09-06-policy-writer-17.

## Changes

- User clarified that rule files must use short, clear rules rather than prose.
- Updated `resume-mission` to read startup conventions first, verify live STATE and takeover facts, separate takeover confirmation from task continuation, migrate STATE to the unified template, and reject stale/missing skill links.
- Updated `wind-down` to document/classify local changes, commit locally, avoid forced/automatic pushes, and require push authorization plus push-convention checks.
- Updated `session-start.md` and `feature-worktree-setup.md` with conventions-first startup and canonical skill-link rules.
- Validation found all non-policy-writer worktree skill links resolve to `session-tracking/claude-skills/`; policy-writer intentionally resolves to its local source tree while drafting.

## 2026-09-07

- User approved the prior changes.
- Updated `wind-down` to allow agent invocation and fully background execution.
- Agent mode defaults to checkpoint unless retirement is explicitly requested; interactive mode may ask for mode.
- Interactive ledger-capture waits for completion; agent invocation records whether background capture finished.

## Agentbus requirement

- User requires every subagent, regardless of Bob/Claude process type, to use agentbus.
- Added mandatory `In:` and `Out:` channels, child subscription, and output publication to agentbus conventions.
- Added the same channel requirements to task specs, coder orchestration, resume takeover ledger-capture, and wind-down ledger-capture.

## Final review

- Reviewed the edited convention and skill files for unnecessary tool-specific wording.
- Removed the remaining incidental Bob reference from `resume-mission`; retained only the explicitly required Bob CLI coder-from-Claude references in orchestration/task rules.
- Confirmed `settings-and-skill-edits.md` is historical context and was not changed.
- Removed an accidental extra blank line in `conventions/tasks.md`.
- `git diff --check` passes.
- Local changes remain uncommitted pending user review and approval.

## Follow-up: ledger-capture interaction

- Updated the AgentBus contract so every child remains subscribed to `In:` while running.
- Added the requirement that parents send progress questions to `In:` and children answer on `Out:` before continuing.
- Applied the requirement to the general AgentBus rules, `resume-mission`, `wind-down`, and the `ledger-capture` contract.

## Follow-up: retired ledger layout

- Moved all captured retired ledgers from `.session/` into `.session/ledger/`.
- Confirmed the latest captured ledgers contain verification summaries: sessions 14, 15, and 16 include `## Verified` sections with summary tables; older captured ledgers also contain verification markers.
- Kept the active session 17 ledger in `.session/`.
- Updated STATE and lifecycle/skill instructions so active ledgers stay at `.session/<slug>.md`, while captured retired ledgers move to `.session/ledger/<slug>.md`.

- Validation found retired ledgers for sessions 7 and 11 lack `## Verified` markers, so they remain in `.session/` as pending rather than being treated as captured. Captured retired ledgers are the 12 files under `.session/ledger/`.

## Follow-up: install authorization

- Added a policy-writer rule requiring the mission to list changed rule/skill files after verification and ask the user whether to install them into `session-tracking`.
- Installation remains blocked until explicit authorization; approval to draft, review, commit, or push does not authorize installation.
