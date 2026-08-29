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
