# Split CONVENTIONS.md into a slim core + situational `conventions/` files

**Status: APPROVED, executing.** Persisted here (inside `worktrees/policy-writer`, committed
with the actual work) specifically so this plan survives regardless of what happens to the
transient plan-mode file it was drafted in
(`~/.claude/plans/witty-dreaming-tiger.md`) — a prior approved plan for this same mission was
lost that way before it could be executed. This file is the durable record of what was
approved and why; once the split lands, it should also be copied into
`session-tracking/missions/policy-writer/spec-policy-writer.md` (a `session-tracking` write,
needs its own explicit go-ahead) so it survives even if this worktree is later discarded.

## Context

`CONVENTIONS.md` (433 lines, in `session-tracking`, drafted here in `worktrees/policy-writer`)
has grown too large: every session pays its full context cost even though most sections apply
only in a specific, narrow situation (editing `STATE.md`, dispatching a coder, running
`/resume-mission`). The user's own words: rules stated clearly in it are "sometimes not
followed — either lost in the first read or forgotten as context grows." The general fix,
already used as precedent by a much bigger, still-WIP redesign in the sibling
`llm-d-workload-variant-autoscaler` repo (`plans-tooling/conventions/` + a `conv.sh` fetch
tool): split into small, situational files, read on demand instead of preloaded, so context
cost drops and a rule that matters gets re-read right when it's about to apply — not relied on
to survive from session start.

**Explicitly not doing:** we are not porting the WVA mechanism. No `conv.sh`/`sec.sh` fetch
scripts, no per-rule marker format, no lint/coverage tooling, no roles/collections layer, no
one-rule-per-file granularity. This is a plain markdown split, read with the ordinary `Read`
tool, grouped by the moment a rule is needed — nothing more.

**Where this happens:** entirely inside `worktrees/policy-writer` (branch `policy-writer`,
tracks changes destined for `session-tracking`'s `CONVENTIONS.md`). Nothing is copied into
`session-tracking` as part of this plan — that copy is a separate, later, explicitly-approved
step, per the worktree/mission model already agreed for this session (`session-tracking` is
production space, drafted against, never edited in place).

## Approach

**Two-tier split**, decided with the user:
- **Slim core `CONVENTIONS.md`** — standing rules that apply regardless of task, read by every
  session up front, same as today. Also becomes the discovery mechanism: since there's no
  index tool (deliberately, no WVA-style tooling), the core file itself names every situational
  file and the one-line trigger for reading it.
- **`conventions/*.md`** — situational files, one per **moment**, not one per today's heading.
  Headings that fire at the same real-world moment are merged into a single file even though
  they're separate sections today.

## File-by-file mapping

**Core `CONVENTIONS.md` keeps** (standing, no situational trigger — every session needs these
regardless of task):
- Intro: what this branch is, orphan/origin-only, must-be-explicitly-told-to-read-this
- Remote convention (`origin`/`upstream`/`ofer` push rules)
- Worktree-pinning behavior (reads free once pinned, writes blocked, re-entry needs fresh
  per-call authorization)
- Repo layout on this branch (the directory-tree diagram)
- "Who writes what" / scope boundary / push-authorization-for-session-tracking-itself
- Ground rules (ask when unsure, no chat dumps, don't kill background tasks without being
  told, silent ledger/state appends)
- A new short **index section** — one line per situational file: its path and the moment to
  read it (see "Index section" below)

**New `conventions/wip-editing.md`** — moment: about to edit `STATE.md` or `CONVENTIONS.md`.
- The full `.wip` protocol (claim-by-rename, edit-via-local-copy, finish-by-rename-back, the
  `.git/info/exclude`-is-shared correction)
- The rejected symlink-based-locking alternative (kept as a "don't re-propose this" note)

**New `conventions/state-vs-ledger.md`** — moment: about to write to `STATE.md` or a ledger, or
unsure which one something belongs in.
- `STATE.md` vs. ledger purpose/audience distinction
- The live-ledger-during-a-session cadence (append to local scratch continuously; copy to
  `session-tracking` only at checkpoints — the two-cadence distinction)
- "Persist findings through failures and restarts" / "this means during the session, not only
  at the end" emphasis

**New `conventions/resume-and-handoff.md`** — moment: running `/resume-mission` or
`/wind-down`; taking over or ending work on a mission.
- Session log format and `active`/`retired` semantics (incl. "retired ≠ pausing")
- The ledger-capture contract (what it captures, the `## Verified` marker format)
- The doc-reference path convention (repo-root-relative, name the resolving worktree/branch)

**New `conventions/feature-worktree-setup.md`** — moment: setting up a new feature worktree for
a mission, or `/resume-mission`/`/wind-down` found missing there.
- The one-time per-worktree symlink setup for `resume-mission`/`wind-down`
- The `.git/info/exclude` entries these symlinks need

**New `conventions/coder-orchestration.md`** — moment: about to dispatch/run a coder subagent.
- The 11 coding-task-orchestration rules (role/mission scoping, no unrequested pushes, one task
  at a time, per-task spec, per-task commit, worktree isolation, review isolation, orchestrator
  reviews before next task, orchestrator merges, no settings.json edits by coder/reviewer,
  file-not-chat output)
- The task template (Intent/Expected outcome/Todo/Refs/Status shape) and its application rules

**New `conventions/settings-and-skill-edits.md`** — moment: about to edit
`~/.claude/settings.json` or a `SKILL.md`.
- The `user-approved-settings-change` marker workaround, verbatim (this is a precise mechanical
  workaround — must not be paraphrased or shortened)

**New `conventions/unexplained-files.md`** — moment: finding something on disk you didn't put
there and can't explain.
- The 5-step "read it, check other missions' tracking, leave-and-note if legitimate, flag to
  user if suspicious, record the judgment" procedure

## Index section (in core `CONVENTIONS.md`)

A short section, e.g. titled "Situational rules — read on demand", listing each situational
file as one line: `path — trigger`. Example shape:

```
- `conventions/wip-editing.md` — about to edit STATE.md or CONVENTIONS.md
- `conventions/state-vs-ledger.md` — about to write to STATE.md or a ledger
- `conventions/resume-and-handoff.md` — running /resume-mission or /wind-down
- `conventions/feature-worktree-setup.md` — setting up a new feature worktree
- `conventions/coder-orchestration.md` — about to dispatch a coder subagent
- `conventions/settings-and-skill-edits.md` — editing settings.json or a SKILL.md
- `conventions/unexplained-files.md` — found something on disk you can't explain
```

This is the only "discovery" mechanism — no tooling, just a short list a session sees on its
one required read of the core file.

## Content-preservation rule for this split

**Copy verbatim, do not paraphrase or summarize.** Every sentence in today's `CONVENTIONS.md`
must land in exactly one destination (core or exactly one situational file) with its original
wording intact — this is a reorganization, not a rewrite. Headings/intro sentences may be
lightly adjusted only where needed to stand alone as a new file's opening (e.g. a situational
file needs one sentence of its own framing since it no longer inherits the core file's
opening context) — but rule text, examples, and the incident/origin notes carry over exactly
as written today.

## Verification

Since this is a pure content move with no code and no tooling:
1. **Byte-level coverage check**: for every paragraph in today's `CONVENTIONS.md`, confirm it
   appears verbatim in exactly one of core or one situational file — no paragraph duplicated,
   none dropped. Do this by reading the old file and each new file side by side, not by trusting
   the mapping table above alone.
2. **Index completeness**: every situational file created has exactly one corresponding line in
   the core file's index section, and the trigger wording matches the actual moment named in
   that file's own heading.
3. **No premature move**: confirm nothing gets copied into `session-tracking` as part of this
   work — the diff stays entirely inside `worktrees/policy-writer`. Copying into
   `session-tracking` is a separate, later, explicitly-approved step.

## Phase 2 — trim production files to what/how only (2026-08-30)

**New correction from the user:** `CONVENTIONS.md` and every `conventions/*.md` file are
**production-only** — rules to follow, stated as what/how, short. Not why, not background, not
incident narration, not design rationale. That material belongs in this mission's own spec doc
(`session-tracking/missions/policy-writer/spec-policy-writer.md`), not shipped to every session
that reads a production rule file.

**Ordering, per explicit instruction:** capture every rule's rationale here in this plan FIRST.
Only after that capture is complete does any production file get trimmed. Nothing is deleted
in place — the current committed files (`eb5f5027`) stay as the pre-trim source of truth until
the trimmed versions are written and reviewed.

**Line to draw, per the user's decision:** keep "why" that is *mechanism-explaining* — needed to
correctly apply the rule's own steps (e.g. why a naive add-then-remove marker sequence fails,
which is what makes "place it somewhere inert" make sense as an instruction). Cut "why" that is
*incident/rationale background* — explains why the rule was created, what motivated it, what
went wrong before, alternatives considered and rejected. That class moves here.

### Rationale extracted, by destination file — to fold into `spec-policy-writer.md`

**`CONVENTIONS.md` (core)**
- Orphan branch, origin-only: keeps mission tracking off feature branches so it never
  accidentally goes upstream.
- `DISABLED-no-push` on `upstream`/`ofer`: so a push attempt fails structurally rather than
  relying on remembering not to.
- Scope-boundary rule exists because of an observed failure: a session fetched against `origin`
  and pushed the whole `session-tracking` branch on its own initiative, reasoning (not
  incorrectly, but without being asked) that this was part of being a good citizen of the
  worktree it happened to be using.
- Per-session uniquely-named ledgers: this is what actually prevents write conflicts, not which
  worktree the edit happens from — the `.wip` mechanism is a backstop, not the primary safeguard.
- Push-authorization-for-`session-tracking`-itself needs a higher bar than a feature worktree:
  a single mission session getting a same-turn "yes" to one push should not be read as standing
  authority to push again later, and should not be treated as equivalent to feature-worktree
  push authorization.
- "Never stop/kill a background task" rule's motivation: observed confusion where a complaint
  about chat noise ("stop cluttering my chat") was misread as "kill the task," when it was about
  narration, not execution.

**`conventions/wip-editing.md`**
- The `.wip` rename-claim mechanism exists because the primary safeguard (only the orchestrating
  session edits shared files) makes concurrent writes *rare*, not impossible — `.wip` is the
  mechanical backstop for the rare case, not a substitute for that discipline.
- Edit-via-local-copy exists because a session pinned to a different worktree (via
  `EnterWorktree`) cannot reliably write cross-worktree directly, and even an unpinned session
  making every line-edit a cross-worktree operation is needless friction.
- The `.git/info/exclude`-is-shared note is a **correction**: originally believed to be
  per-worktree (stated that way twice in the doc), corrected after direct testing
  (`git rev-parse --git-common-dir` resolves to the main repo's `.git` for every worktree).
- **Symlink-based locking was proposed and rejected** (full rationale, currently inline, to
  move here in full): it reinvents a lock without providing real exclusion (nothing stops a
  second session from also swapping in its own symlink, or writing the real file directly while
  a symlink points elsewhere), and risks committing a broken/dangling symlink into
  `session-tracking`'s own history by accident. The rename-based claim gives the same atomicity
  without either problem. **Kept in the production file only as a one-line pointer** ("symlink-
  based locking was considered and rejected — see spec doc") so a future session doesn't
  re-propose it, without carrying the full argument inline.

**`conventions/state-vs-ledger.md`**
- The `STATE.md`-vs-ledger distinction needed stating explicitly because it's easy to miss from
  the mechanics sections alone — both have a documented edit protocol, which invites inferring
  they're "the same kind of record" when they're not. Observed directly in a session that made
  exactly that inference and wrote both at the same length, after the fact, in a single batch.
- The two-cadence rule (append-to-local-scratch continuously vs. copy-to-`session-tracking` only
  at checkpoints) needed stating explicitly because a session read "at session end or at any
  natural checkpoint" as license to batch *both* operations, when only the checkpoint-copy step
  is legitimately checkpoint-based — justified specifically because a pinned session must
  `ExitWorktree` to reach the `session-tracking` worktree, and that real friction is what
  justifies batching that one step, and only that one.
- The "append live, not retroactively" emphasis exists because of an observed failure: a session
  that built this very ledger mechanism largely skipped using it live, then had to re-derive
  several real decisions (a rejected design alternative, an operational gotcha, a founding
  rationale) by re-reading its own conversation from the start, at the user's explicit prompting,
  because nothing had captured them as they happened.

**`conventions/resume-and-handoff.md`**
- `retired` ≠ pausing is called out explicitly because marking a paused-but-continuing session as
  `retired` was an observed real mistake, not a hypothetical edge case.
- Ledger-capture treats `active`-and-`retired`-unverified the same because either case might be a
  clean handoff (ledger not yet verified) or an unclean exit (crash/sleep/force-quit) — the
  safety net doesn't need to distinguish which, it just always fires.
- Ledger-capture fixes gaps directly rather than only reporting them because its job is defined
  as *capture*, not just *check* — this was a deliberate rename from an earlier name ("the
  verifier") after the first real run showed the job was broader than checking.
- Ledger-capture's scope is bounded (fixes doc-reference issues *in scope of what it's already
  touching*, doesn't hunt for unrelated broken links) — a deliberate boundary, not an oversight,
  so a capture pass stays proportional to the one ledger it's given.
- Ledger-capture is framed as useful beyond crash recovery specifically because running it before
  any context-loss event (compaction, handoff, planned exit) captures context durably before it's
  gone — not only as a fallback for unclean endings.
- The repo-root-relative doc-reference-path rule exists because a bare filename broke once files
  moved between a flat layout and a nested one, concretely during this mission's own
  `session-tracking` migration — a real incident, not a hypothetical.
- "State which worktree/branch a path resolves in" exists because the same repo-root-relative
  path can point to different content (or nothing) depending on which branch's tree it's read
  against — stated as a general property, not a specific incident.

**`conventions/feature-worktree-setup.md`**
- The per-worktree symlink setup is needed because of a confirmed, tested fact: Claude Code's
  project-skill discovery does not walk up past a git worktree's own root, not to a plain
  filesystem parent directory, not to the worktree's main repo.
- Symlinks are excluded via `.git/info/exclude` (not committed) specifically so they never show
  up in `git status` or get swept into a broad `git add`.
- Verifying the symlink resolves (rather than trusting `ln -s` succeeded silently) is a
  double-check discipline, not narrated as tied to a specific past failure.

**`conventions/coder-orchestration.md`**
- Coder isolation (`isolation: "worktree"`) exists so editing has zero visible effect on the
  user's actually-open worktree/IDE.
- Review isolation (same worktree as the coder, not a third) exists for consistency — the review
  target must match exactly what the coder actually produced.
- Otherwise this file is already mostly what/how — little separable "why" beyond the two points
  above.

**`conventions/settings-and-skill-edits.md`**
- **Kept inline, not moved** (per the user's decision this pass): the explanation of *why* a
  naive add-marker-then-remove-it sequence never finishes (the removal edit's own new content
  still needs the marker present, recreating the same leftover) — this is mechanism-explaining,
  not incident background; without it, "place the marker somewhere inert and don't chase full
  removal" would read as an arbitrary instruction rather than a necessary consequence.
- Moved to spec doc: the observation that this in practice tends to leave more than one inert
  marker copy scattered through a file over several edits (two in one `SKILL.md`, one in
  `settings.json`, observed) — this is a "here's what actually happened" note, not needed to
  apply the rule.

**`conventions/unexplained-files.md`**
- The framing sentence ("expected background noise of a multi-session system, not necessarily a
  problem") is scene-setting, not a rule step — moves to spec doc.
- Also moved: the factual background that sessions from multiple missions, and multiple tools
  (not just Claude Code), can be working concurrently against this repo's shared worktrees — this
  is *why* unexplained files occur at all, not an instruction for what to do about one. Folded
  into the trimmed file's opening as a compressed clause ("you didn't put there and can't
  explain") rather than dropped outright, since a reader needs *some* minimal frame for what kind
  of thing this file is about — full elaboration (multi-mission, multi-tool, concurrent) moves to
  spec doc. The 5 numbered steps themselves are already what/how and stay.

### Explicitly out of scope for Phase 2 (same boundaries as Phase 1)

- Still not copying anything into `session-tracking` — the rationale capture above lands in this
  plan doc first; whether/when it also gets copied into `spec-policy-writer.md` is a
  `session-tracking` write needing its own separate go-ahead, same as the conventions split
  itself.
- Still not touching T7 or the 4 pending `suggestion-box/` entries.

## Explicitly out of scope for this task

- The T7 ledger-capture contract correction (never touch `CONVENTIONS.md`; use
  `suggestion-box/` instead) — drafting that into the new structure happens in a later,
  separate step, after this split lands.
- The 4 pending `suggestion-box/` entries (in-place-editing generalization, destructive-action
  approval, read/write worktree-boundary rules, avoid-`cd`) — per the user's explicit
  instruction: "new suggestions-box rules and rules we did not add to CONVENTIONS yet can
  wait. We first make the rewrite split. Then, we add new rules following the new approach."
- Any port of the WVA mechanism itself (fetch scripts, marker format, lint/coverage tooling).
- Copying the finished split from `worktrees/policy-writer` into `session-tracking` — a
  separate, later, explicitly-approved step.
