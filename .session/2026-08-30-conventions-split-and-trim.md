# Session ledger — 2026-08-30 — CONVENTIONS.md split + why/how trim

## What happened

1. Resumed the `policy-writer` mission via `/resume-mission`. Found one pending Session-log
   entry (`2026-08-27-session-tracking-setup`, still `active`) — marked it `retired` and ran
   ledger-capture against its ledger (`2026-08-29-retired-semantics-and-skill-symlinks.md`) in
   the foreground before proceeding. No gaps found; ledger already fully captured.

2. User asked to find and revisit an earlier discussion about splitting `CONVENTIONS.md` into
   smaller `conventions/` files. Searched `~/.claude/plans/` (15 files) and this mission's own
   ledgers — the actual plan-mode file for this specific discussion no longer exists on disk;
   only pointers to its prior existence survived (two ledgers referencing "already planned in
   detail in a local plan-mode file, approved once already"). **User was very upset about this
   — a previously-approved plan should never be lost before being carried out.** This is a real
   process gap: nothing durable captures an approved plan at approval time, only at some later
   checkpoint that may not happen before the transient plan-mode file is gone.

3. Investigated the sibling `llm-d-workload-variant-autoscaler` repo's own, much bigger,
   still-WIP redesign of the same underlying problem (`plans-tooling/conventions/` + a
   `conv.sh`/`sec.sh` fetch-tool mechanism, roles/collections, lint/coverage tooling). Read
   `micro-rules-migration-plan.md`, one real convention file, and the `policy-writer` role file
   there. Confirmed with the user: **not porting that mechanism** — no fetch scripts, no marker
   format, no lint tooling, no one-rule-per-file granularity. Only the core idea (split into
   smaller, situational files, read on demand) carries over.

4. Asked several clarifying questions (some redundant with what the user had already stated —
   user called this out once, correctly; corrected by not re-asking already-answered points).
   Settled: slim core `CONVENTIONS.md` (standing rules) + situational `conventions/*.md` files,
   grouped by the real-world *moment* a rule applies, not by today's heading boundaries.

5. Wrote a plan (`EnterPlanMode`/`ExitPlanMode`), user approved it. **Immediately persisted the
   approved plan as `PLAN-conventions-split.md` inside this worktree** (not just left in the
   transient `~/.claude/plans/witty-dreaming-tiger.md`) specifically because of the lost-plan
   incident in step 2 — committed alongside the actual work so it can't vanish independently.

6. Executed the split: created 7 new `conventions/*.md` files (`wip-editing.md`,
   `state-vs-ledger.md`, `resume-and-handoff.md`, `feature-worktree-setup.md`,
   `coder-orchestration.md`, `settings-and-skill-edits.md`, `unexplained-files.md`), trimmed
   `CONVENTIONS.md` to core-only content, added an index section. Verified via phrase/word-count
   diffing that no content was dropped (pure move, not a rewrite). Committed as `eb5f5027`.

7. **User caught a real process violation.** After committing the split, I started drafting a
   *second* restructuring (moving "why/background" content out of the conventions files into
   the spec doc) and, in the course of asking a clarifying question, phrased it as if deleting
   the why-content and only-later-maybe-capturing-it-in-the-spec-doc was already the agreed
   plan — without having actually confirmed that ordering, and in apparent tension with the
   very memory/feedback rules already known to this session (no in-place destructive edits;
   destructive actions need step-by-step approval; keep a backup when unsure). User's reaction
   was sharp and explicit: "why violate the rules????? did you already forget the rules about
   deleting anyting?????? STOP. confirm you understand the rules before anything else." This is
   a real incident, not a minor phrasing issue — I asked a question that presupposed a
   destructive action's ordering/safety without the user's actual confirmation.
   - Corrected immediately: restated the actual rule (nothing deleted in place; new content
     written alongside old; backups kept; capture-before-cut ordering) and got explicit
     confirmation before proceeding.
   - User's actual instruction, once given: capture the reasons behind every rule in the plan
     doc FIRST; only then work on removing the reasons from the rules.

8. Extracted rationale/background for every rule across all 8 files (`CONVENTIONS.md` + 7
   `conventions/*.md`), sorted into "mechanism-explaining why" (kept inline — needed to
   correctly apply a rule's own steps, e.g. why the settings.json marker workaround's naive
   remove-it sequence fails) vs. "incident/rationale why" (moved out — explains why the rule
   was created, what motivated it, alternatives rejected). User confirmed this line explicitly
   via `AskUserQuestion` before I wrote anything. Captured all of it into
   `PLAN-conventions-split.md`'s new "Phase 2" section, organized by destination file.

9. User asked where the captured reasons came from — clarified: manual extraction from the
   already-read file contents earlier in the conversation, not a new read or invented content;
   traced each item back to the specific sentence it came from.

10. User then asked to actually produce the short (what/how-only) version of each of the 8
    files, keeping the old version as `.bak` for comparison, and explicitly required verifying
    that everything cut is captured in the plan doc before finalizing — not just trusting the
    extraction from step 8.
    - Backed up all 8 files (`cp` to `.bak` siblings) before editing any of them.
    - Trimmed each file. Then **diffed every trimmed file against its own `.bak`** and
      cross-checked every removed passage against the plan doc's Phase 2 rationale section,
      one file at a time, not by spot-checking or trusting memory.
    - **Found one real gap this way**: `unexplained-files.md`'s cut included the sentence
      "Sessions from multiple missions, and multiple tools (not just Claude Code), can be
      working concurrently against this repo's shared worktrees" — this factual background
      (why unexplained files occur at all) was not in the plan doc's rationale entry for that
      file, which had only captured the "expected background noise... not necessarily a
      problem" framing sentence. Fixed by adding it to the plan doc's rationale section, and by
      keeping a compressed clause of it inline in the trimmed file (rather than dropping it
      outright) since the file needs *some* minimal frame for what kind of thing it's about.
    - Every other cut passage across all 8 files was confirmed present in the plan doc.

## Current state — Phase 2 committed as a checkpoint, awaiting user's final review

11. Wound down via `/wind-down`. Updated `missions/policy-writer/STATE.md` (via `.wip`) with
    both the completed Phase 1 split and the in-progress Phase 2 trim — commit `0a169723` in
    `session-tracking`. Asked the user whether to leave Phase 2's trim uncommitted (their
    original review posture) or commit it as a checkpoint; user chose **checkpoint** — committed
    in `worktrees/policy-writer` as `6e99db7f` (`.session/` correctly left unstaged/untracked,
    excluded from the feature branch). The user's review of the trimmed files against `.bak` is
    **still not marked complete** — the checkpoint commit doesn't imply approval, just safety
    from loss; `git diff 6e99db7f~1 6e99db7f` (or the retained `.bak` files) still shows exactly
    what changed for review.

## Not yet done

- User has not yet given final approval of the trimmed (Phase 2) versions of the 8 files —
  checkpointed at `6e99db7f`, but review itself is still open.
- `.bak` files are committed alongside the trim at `6e99db7f` — not yet decided whether they
  stay long-term or get removed once the trim itself is approved.
- T7 (ledger-capture contract correction — never touch `CONVENTIONS.md`) still not drafted into
  the actual conventions text — unrelated to this session's work, still open from before.
- The 4 pending `suggestion-box/` entries — still explicitly deferred per the user's own
  ordering instruction ("we first make the rewrite split... then we add new rules").
- Copying anything from `worktrees/policy-writer` into `session-tracking` — still not done,
  still needs its own explicit go-ahead, unaffected by any of this session's work.
- Whether to add `*.md.wip` and `.session/` to the shared `.git/info/exclude` — attempted from
  inside `worktrees/policy-writer` while pinned and was correctly blocked (writing to
  `.git/info/exclude` reaches outside the pinned worktree's own tree); not yet done from
  `session-tracking` either. `.session/` is currently just left unstaged manually each time
  instead of truly gitignored — works, but relies on remembering to exclude it from `git add`
  rather than a real safety net. Worth doing properly next session.

## Corrections/incidents worth remembering

- **The lost-plan incident (step 2 above)** is the direct cause of `PLAN-conventions-split.md`
  now existing as a durable, committed plan record inside the feature worktree, rather than
  relying on the transient `~/.claude/plans/*.md` file surviving until execution. Worth
  generalizing: any approved plan should be persisted to a durable, committed file *before* or
  immediately as execution begins, not left contingent on the plan-mode file's survival.
- **The destructive-editing near-miss (step 7 above)** is a real incident: I asked a question
  that presupposed an unconfirmed destructive-edit ordering as already-settled, in direct
  tension with feedback memories already known to this session at the time
  (`feedback_git_destructive_confirm.md`-adjacent territory — re-confirm each destructive step;
  keep a backup when unsure). The user's correction was sharp specifically because this wasn't
  a new rule being taught, it was an existing one being missed in the moment. Worth a durable
  memory, not just this ledger note, if a pattern like this recurs.
- **Verify-by-diffing, not by trusting your own extraction**, caught a real gap
  (`unexplained-files.md`'s multi-mission/multi-tool sentence) that a "did I capture everything"
  self-check from memory would likely have missed, since it wasn't dramatic or rule-like — just
  ordinary scene-setting prose that still counted as content needing a home.

## Verified 2026-08-30 — folded in: spec doc lacked the Phase 1 conventions/-split structure,
the Phase 2 mechanism-vs-incident why-split rule, and the never-delete/backup-first + diff-verify
discipline — all added to `spec-policy-writer.md`'s "Settled design" section (previously only in
`PLAN-conventions-split.md`, a different worktree/branch). `STATE.md` had no record at all of the
lost-plan incident or the destructive-editing near-miss — added a new task-table row noting both,
with the lost-plan incident generalized into a new `suggestion-box/2026-08-30-1400-policy-writer.md`
entry (genuinely global: persist an approved plan durably and immediately, not just at a
checkpoint). The destructive-editing incident needed no new memory — confirmed it's the existing
`feedback_git_destructive_confirm.md` rule being momentarily missed, not a gap in the rule. The
WVA-research decision, the split itself, the Phase 2 trim, and the verification gap were already
adequately captured in `STATE.md`'s task table. Doc-reference paths touched in this pass follow the
existing bare-filename-within-mission-dir style already used throughout `STATE.md`; no stale or
ambiguous paths found in scope. `CONVENTIONS.md` itself was not touched, per the corrected
ledger-capture contract.
