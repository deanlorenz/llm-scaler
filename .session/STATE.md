# Mission state — policy-writer

**Last updated:** 2026-09-03 (policy-writer-9, continued). Overwritten on each update, not append-only.
Mission tracking files live at `worktrees/policy-writer/.session/` (mission branch). For
global process rules see `worktrees/session-tracking/CONVENTIONS.md`. For the plan see
`.session/spec-policy-writer.md`.

## Worktrees used for this mission

- `worktrees/policy-writer` (branch `policy-writer`) — mission branch. All drafting of
  changes to `CONVENTIONS.md`/the skills/conventions files happens here. Tracking files
  (`STATE.md`, ledgers, spec) live in `.session/` on this branch per the new layout.

## Mission, one line

Build and maintain the cross-mission, cross-worktree session-tracking system: the conventions,
skills, and layout that let any mission resume cleanly without reloading full history.

## Task status

| Task | Status | Notes |
|---|---|---|
| Branch/worktree setup | **DONE** | Commit `e65f67ed`. |
| `.wip` concurrent-edit protocol | **DONE** | Documented. |
| Session log format + ledger-capture | **DONE, validated once** | Commits `2b4927ba`, `14ce29d3`. |
| `/resume-mission` skill | **DONE** | Exercised end-to-end 2026-08-29. |
| `/wind-down` skill | **DONE** | Not yet exercised end-to-end. |
| Skill discoverability across worktrees | **DONE for `single-analyzer` only** | Others self-heal on first `/resume-mission` use. |
| `pr-review` skill disabled | **DONE** | `skillOverrides: {"pr-review": "off"}`. |
| Own mission tracking | **DONE 2026-08-27** | |
| Audit pass + agentbus feedback fixes | **DONE 2026-08-27** | 6 ambiguities fixed. |
| Corrected worktree/mission model | **DONE 2026-08-28** | Commits `63ab0d36`, `b918acf0`. |
| Standing rule: never deviate from approved plan | **DONE 2026-08-28** | Saved as durable memory. |
| Ledger-capture contract correction | **DONE 2026-08-31** | Commit `f7508d08`. |
| Split `CONVENTIONS.md` into core + `conventions/*.md` | **DONE 2026-08-30** | Commits `eb5f5027`, `6e99db7f`. |
| PR rules | **DONE 2026-08-31** | Commits `c52c22d1`, `078648d4`. |
| Mission file handling redesign — `.session/` in mission branch | **DONE 2026-08-31** | Multiple commits. |
| Coder orchestration — Claude/Bob worker model (T8) | **DONE 2026-09-03** | Commits `0f64564b`, `c9288a40`. New: `conventions/tasks.md`, spec T8 section. |
| Reader-focused conventions review pass (T9) | **DONE 2026-09-03** | Commits `e407102b`–`5eb61b84`. All `conventions/*.md` reviewed and processed. |
| Unified STATE template + session-start redesign (T9 continued) | **DONE 2026-09-03** | Commit `743a5443`. Unified STATE/task template; session-start simplified; tasks.md as writer guide. T10 (session-setup agent) captured in spec. |
| Verification + install onto session-tracking | **DONE 2026-09-03** | Gap found and fixed (commit-cadence rule, `4492b8cf`). Installed: `1427f964` on session-tracking. |

## Immediate next step

- Update skills: `wind-down` and `resume-mission` — reflect new conventions (unified STATE
  model, per-session STATE file, session-setup agent concept).
- Rewrite ledger-capture as a proper custom-agent (spec + mode).
- T10 (session-setup agent): spec not yet written.
- Suggestion-box lifecycle convention — deferred.
- Verify `settings-and-skill-edits.md` marker behavior still applies.
- `.bak` files: keep for now.

## Open questions

- `settings-and-skill-edits.md`: is the `user-approved-settings-change` marker requirement
  still a real harness constraint?

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=retired ledger=.session/2026-08-27-session-tracking-setup.md
- 2026-08-30 session=2026-08-30-conventions-split-and-trim status=retired ledger=.session/2026-08-30-conventions-split-and-trim.md
- 2026-08-31 session=2026-08-31-policy-writer-7 status=retired ledger=.session/2026-08-31-policy-writer-7.md
- 2026-08-31 session=2026-08-31-policy-writer-8 status=retired ledger=.session/2026-08-31-policy-writer-8.md
- 2026-09-03 session=2026-09-03-policy-writer-9 status=active ledger=.session/2026-09-03-policy-writer-9.md
