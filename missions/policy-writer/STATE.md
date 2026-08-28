# Mission state — policy-writer

**Last updated:** 2026-08-27. Overwritten on each update, not append-only. For global
process rules see `../../CONVENTIONS.md` — most of this mission's actual output lives
there, not in mission-specific docs, since it IS the global process rules. For the plan
see `spec-policy-writer.md`. Per-session ledgers are in `ledgers/`.

## Worktrees used for this mission

- `worktrees/policy-writer` (branch `policy-writer`, branched off `session-tracking`) —
  this mission's dedicated feature worktree, created 2026-08-28. All drafting of changes
  to `CONVENTIONS.md`/the skills happens here — never by editing `session-tracking`
  directly. Per the corrected worktree model: `session-tracking` is production space
  where every mission's `STATE.md`/spec/ledger is saved (the stated exception to writes
  never crossing worktree boundaries), not a place to draft. This mission's tracking
  files (this doc, the spec, the ledgers) live in `session-tracking` per that same rule
  — only the *drafting* of `CONVENTIONS.md`/skill changes happens in `policy-writer`.
- `worktrees/single-analyzer` — received the one-time symlink setup
  (`.claude/skills/resume-mission`, `wind-down`) as the first feature worktree to use
  this infrastructure. Other feature worktrees will need the same one-time setup
  (documented in `CONVENTIONS.md`'s "Making /resume-mission and /wind-down available in
  a feature worktree" section) as they start using `/resume-mission`/`/wind-down`.

## Mission, one line

Build the cross-mission, cross-worktree session-tracking system itself: the
`session-tracking` branch's layout, the `.wip` concurrent-edit protocol, the Session-log
+ ledger-capture mechanism for resuming/handing off missions without reloading full
history, and the `/resume-mission` + `/wind-down` skills that drive it.

## Task status

| Task | Status | Notes |
|---|---|---|
| Branch/worktree setup — orphan `session-tracking` branch, origin-only, `missions/<name>/` layout | **DONE** | Commit `e65f67ed`. Migrated `analyzer-optimizer-refactor`'s existing docs in as the first mission. |
| `.wip` concurrent-edit protocol for `STATE.md`/`CONVENTIONS.md` | **DONE** | Documented in `CONVENTIONS.md`. Not yet exercised under real concurrent access from two sessions — only ever used single-threaded so far. |
| Session-log format + ledger-capture (formerly "verifier") mechanism | **DONE, validated once** | Documented in `CONVENTIONS.md`'s "Session log" section. First real run: ledger-capture audited the old 94KB `analyzer-optimizer-refactor` ledger, found and fixed 2 genuine gaps (commit `2b4927ba`). Renamed from "verifier" to "ledger-capture" after that run, since its job is capture-and-fix, not just check (commit `14ce29d3`). |
| `/resume-mission` skill | **DONE** | Commit `8942841d` (canonical), path-fixed in `8ffa77e0`. Not yet tested end-to-end (no session has actually typed `/resume-mission` since it was written). |
| `/wind-down` skill | **DONE** | Commit `8942841d`. Not yet tested end-to-end. |
| Skill discoverability across worktrees (symlink mechanism) | **DONE for `single-analyzer` only** | Confirmed by direct testing that skill discovery does not walk up past a worktree's own root — each feature worktree needs its own one-time symlink setup. Documented in `CONVENTIONS.md`. Only `single-analyzer` has actually been set up so far; every other existing feature worktree (the `benchmark-*` ones, `fix-scaledobjects-cold-start`, etc.) still needs the same one-time setup before `/resume-mission`/`/wind-down` will work there. |
| `pr-review` skill disabled | **DONE** | Added `"pr-review": "off"` to `~/.claude/settings.json`'s `skillOverrides` (global, not committed anywhere — a personal setting, not mission content) — the skill itself is untouched on all 30+ branches that carry it (per user: stays in upstream history, just suppressed locally). |
| Correction: `.git/info/exclude` is shared across all worktrees of a repo, not per-worktree | **DONE** | Wrongly assumed per-worktree at first (two places in `CONVENTIONS.md` said so); corrected after direct testing (`git rev-parse --git-common-dir`). |
| Own mission tracking (this doc) | **DONE 2026-08-27** | This mission ran for its entire duration without its own `STATE.md`/spec — its ledger entry was misfiled under `analyzer-optimizer-refactor/ledgers/` instead. This is the retroactive fix, done at the user's explicit request ("clean up your own session... you had 3 different missions and did not separate the work"). |
| Audit pass over session history vs. mission docs | **DONE 2026-08-27** | Found 3 gaps that were this mission's own policy content (folded into `CONVENTIONS.md`: rejected symlink-locking alternative, settings-guard marker friction, promoted the remote-push convention to global) — see `ledgers/2026-08-27-conventions-audit-fixes.md`. 2 more gaps were plain tracking content for other missions, committed separately (`e18d8733`). |
| Processed feedback from a parallel session (`agentbus`) that actually used `CONVENTIONS.md` and got confused by it | **DONE 2026-08-27** | 6 real ambiguities reported, all verified against the actual doc text and fixed: `STATE.md`-vs-ledger purpose/audience never stated; the live-ledger section read as license to batch local scratch writes, not just the copy-to-`session-tracking` step; no scope boundary between "owns my mission" and "citizen of the shared worktree" (led to an unprompted `fetch`/push); `session-tracking` push authorization not distinguished from a feature worktree's; no procedure for handling something unexplained found on disk; `retired` not sharply distinguished from "pausing." See `ledgers/2026-08-27-conventions-audit-fixes.md`'s "Second round" section for full detail on each. This is higher-signal than the self-audit above — an independent session's actual confusion, not a self-review. |
| Corrected worktree/mission model | **DONE 2026-08-28** | Every mission gets its own branch/worktree; `session-tracking` is production space only, never drafted in directly. This mission renamed from `session-tracking-infra` to `policy-writer` and given its own dedicated worktree (`worktrees/policy-writer`, branched off `session-tracking`); `analyzer-optimizer-refactor` renamed to `single-analyzer` to match its existing worktree; `repo-restructure` given a fresh empty orphan worktree. Commits `63ab0d36`, `b918acf0`. See `ledgers/2026-08-28-worktree-model-and-side-notes.md`. |
| Standing rule: never deviate from an approved plan | **DONE 2026-08-28** | 3 incidents in immediate succession of doing unapproved adjacent work right after a specific plan was approved. Saved as a durable memory (`feedback_never_deviate_from_approved_plan.md`), not just a ledger note, per the user's explicit ask for something that persists. See `ledgers/2026-08-28-worktree-model-and-side-notes.md`'s "Second entry" section. |
| Ledger-capture contract correction: never touch `CONVENTIONS.md`; `suggestion-box/` instead | **DECISION RECORDED, NOT YET DRAFTED** | See spec T7 — a decision record only; `CONVENTIONS.md`'s own ledger-capture text has not been updated to match yet (needs its own drafting cycle in `policy-writer`). `session-tracking/suggestion-box/` folder created (commit `a02a474f`), currently empty. Used as a one-off exception for this session's 3-mission ledger-capture pass, ahead of `CONVENTIONS.md` being updated. |

## Immediate next step

Nothing actively blocked. Natural next steps if resumed:
- Draft T7 (the ledger-capture contract correction) into `CONVENTIONS.md`'s actual text
  — currently only a decision record in the spec, not yet applied to the doc itself.
- Actually exercise `/resume-mission` and `/wind-down` end-to-end for the first time
  (neither has been run since being written).
- Set up the symlinks in the other existing feature worktrees that will want these
  skills (not yet done anywhere except `single-analyzer`).
- Consider whether `.wip` needs real testing under actual concurrent access, or whether
  that's acceptable to leave unvalidated until it naturally happens.
- Decide the suggestion-box processed-file lifecycle (explicitly deferred, see spec T7).
- Fix the stale old-mission-name example path in `resume-mission/SKILL.md` (found during
  the renames, correctly left undone since it was never part of an approved plan step —
  needs its own explicit proposal).

## Open questions blocking full completion

None blocking — this mission is functionally complete for its first-pass scope. The
"not yet tested end-to-end" items above, and T7's not-yet-drafted status, are real gaps
but not blockers to using the system as-is.

## Session log

- 2026-08-27 session=2026-08-27-session-tracking-setup status=active ledger=ledgers/2026-08-28-worktree-model-and-side-notes.md
