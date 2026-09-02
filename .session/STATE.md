# Mission state — policy-writer

**Last updated:** 2026-08-31. Overwritten on each update, not append-only. Mission tracking
files live at `worktrees/policy-writer/.session/` (mission branch). For global process rules
see `worktrees/session-tracking/CONVENTIONS.md`. For the plan see `.session/spec-policy-writer.md`.

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
| Branch/worktree setup — orphan `session-tracking` branch, origin-only, `missions/<name>/` layout | **DONE** | Commit `e65f67ed`. |
| `.wip` concurrent-edit protocol for `STATE.md`/`CONVENTIONS.md` | **DONE** | Documented. Not yet exercised under real concurrent access. |
| Session-log format + ledger-capture mechanism | **DONE, validated once** | First real run found and fixed 2 genuine gaps (`2b4927ba`). Renamed from "verifier" to "ledger-capture" (`14ce29d3`). |
| `/resume-mission` skill | **DONE** | Exercised end-to-end 2026-08-29. Runs cleanly. |
| `/wind-down` skill | **DONE** | Not yet exercised end-to-end. |
| Skill discoverability across worktrees (symlink mechanism) | **DONE for `single-analyzer` only** | Other worktrees still need one-time setup. |
| `pr-review` skill disabled | **DONE** | `skillOverrides: {"pr-review": "off"}` in `~/.claude/settings.json`. |
| Correction: `.git/info/exclude` is shared across all worktrees | **DONE** | Corrected after direct testing. Also removed the old `**/.session/` blanket exclude (2026-08-31) — `.session/` is now tracked on mission branches. |
| Own mission tracking | **DONE 2026-08-27** | Retroactive fix per user request. |
| Audit pass + agentbus feedback fixes | **DONE 2026-08-27** | 6 real ambiguities fixed. See `2026-08-27-conventions-audit-fixes.md`. |
| Corrected worktree/mission model | **DONE 2026-08-28** | Every mission gets its own branch/worktree. Commits `63ab0d36`, `b918acf0`. |
| Standing rule: never deviate from an approved plan | **DONE 2026-08-28** | Saved as durable memory. |
| Ledger-capture contract correction: never touch `CONVENTIONS.md` | **DECISION RECORDED, NOT YET DRAFTED** | See spec T7. `suggestion-box/` folder created (`a02a474f`). |
| Split `CONVENTIONS.md` into core + `conventions/*.md` | **DONE 2026-08-30 (Phase 1)** | Commit `eb5f5027`. 7 new files. Content moved verbatim. |
| Trim conventions files to what/how only | **DONE 2026-08-30 (Phase 2)** | Commit `6e99db7f`. `.bak` files retained for comparison. Phase 2 approved (checkpointed); `.bak` files not yet removed — ask on next resume. |
| PR rules | **DONE 2026-08-31** | `conventions/pr-workflow.md` (3 rules) + `conventions/pr-branch.md` (PR branch lifecycle). Commits `c52c22d1`, `078648d4`. |
| Mission file handling redesign — `.session/` in mission branch | **DONE 2026-08-31** | Full redesign: tracking files move to `.session/` on mission branch; `session-tracking/missions/` becomes symlinks only; agentbus ownership declaration/release; mission owners create symlinks, policy-writer commits. Conventions + skills updated. `policy-writer` itself migrated. Commits `078648d4`–`66d72cf2` (policy-writer), `db11bb23`–`a364a120` + `c50d8b1a` (session-tracking). |

## Immediate next step

- Remove `.bak` files from `worktrees/policy-writer` (Phase 2 trim already committed at `6e99db7f` — `.bak` files were for review; that review is done) — or keep them, user's call.
- Draft T7 (ledger-capture contract correction — never touch `CONVENTIONS.md`) into the actual conventions text.
- Process 4 pending `suggestion-box/` entries (deferred until after rewrite split — that split is now done).
- Set up skill symlinks in other existing mission worktrees (`benchmark-*`, etc.).
- Copy finished `policy-writer` branch content into `session-tracking` (still needs explicit go-ahead).
- Push `session-tracking` to `origin` (50+ commits ahead).

## Open questions

- `.bak` files from Phase 2 trim: keep committed for history, or delete now that review is done?

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=retired ledger=.session/2026-08-27-session-tracking-setup.md
- 2026-08-30 session=2026-08-30-conventions-split-and-trim status=retired ledger=.session/2026-08-30-conventions-split-and-trim.md
- 2026-08-31 session=2026-08-31-policy-writer-7 status=retired ledger=.session/2026-08-31-policy-writer-7.md
- 2026-08-31 session=2026-08-31-policy-writer-8 status=active ledger=.session/2026-08-31-policy-writer-8.md
