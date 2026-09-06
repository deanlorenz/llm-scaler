# Session ledger — 2026-08-31 — policy-writer-8

## Status: active

## Goal

Process pending backlog from prior sessions:
- 6 suggestion-box entries (4 from 2026-08-28, 1 from single-analyzer 2026-08-30, 1 from
  policy-writer 2026-08-30) — evaluate and either draft rules or discard with reason
- Draft T7 into conventions text (ledger-capture never touches CONVENTIONS.md)
- Decide whether to remove .bak files
- Push session-tracking to origin

## What happened

### .bak files

User confirmed: keep `.bak` files for now, do not delete.

### Suggestion-box — all 6 entries processed

Evaluated all 6 entries against current conventions content. Drafted rules as follows:

- **0400** (no in-place editing except own files) → new ground rule in `CONVENTIONS.md` Ground rules section
- **0401** (destructive actions need per-step approval) → new ground rule in `CONVENTIONS.md` Ground rules section
- **0402** (reads cross worktree boundaries freely, writes do not) → expanded `CONVENTIONS.md` "Worktree pinning" paragraph into explicit read/write boundary rule
- **0403** (no `cd`/shell tricks to route around boundaries) → folded into same worktree-pinning expansion
- **0500** (`EnterWorktree` isolation veto vs `permissions.allow`) → new paragraph in `CONVENTIONS.md` worktree-pinning section
- **0030-1400** (persist approved plan immediately at approval time) → new "When a plan is approved" section in `conventions/wip-editing.md`; updated index trigger in `CONVENTIONS.md`

Also updated the `CONVENTIONS.md` index entry for `wip-editing.md` to add "plan just approved" as a trigger moment.

Commit: `1f3d05a8` (policy-writer). Session-tracking: `46fcc053` (renamed all 6 to `processed-*`).

### T7 drafted into conventions text

`conventions/resume-and-handoff.md` ledger-capture section updated:
- Added explicit prohibition: ledger-capture must never write to `CONVENTIONS.md`
- Added suggestion-box mechanism as the replacement path
- Corrected step 1 to list only `STATE.md` + plan/spec doc as legitimate write destinations

Commit: `f7508d08` (policy-writer).

STATE.md updated: T7 row → DONE. "Immediate next step" trimmed to remove T7 and suggestion-box items (now done); added suggestion-box lifecycle convention as the one open item.

### User review of CONVENTIONS.md

User reviewed `CONVENTIONS.md` inline and requested a reader-focused restructuring:
- Make the core file apply to every session; move policy-writer and setup mechanics out.
- Require every session to identify its mission and role.
- Strengthen the own-worktree boundary and move exception mechanics to a situational file.
- Split role-specific mission-owner guidance from the core.
- Add a session-start template covering mission, role, and ledger.
- Generalize push safety: never push without explicit one-operation authorization; add a
  dedicated situational file.
- Keep `pr-branch.md` and `pr-workflow.md` separate, but distinguish their index triggers.
- Clarify that normal `Edit`/`Write` on owned, checkpointed files is allowed; prohibit
  in-place command-line rewrites such as `sed -i`, `gawk -i`, and Python `fileinput`.

User approved four new situational files: `session-start.md`, `mission-owner.md`,
`writing-outside-worktree.md`, and `push.md`. Existing situational files remain unchanged
while the user continues reviewing them.

### User review of coder-orchestration.md

User added a second review pass to `conventions/coder-orchestration.md`. Requested direction:
- Coders normally maintain focused state/ledger files that remain recoverable through their
  branch even if an ephemeral worktree is deleted.
- Mission owners track all mission-related worktrees/branches and integrate accepted code into
  the mission branch promptly.
- Coders are non-interactive by default; user interaction normally goes through the mission
  owner. A rare interactive mode should use a visible worktree, local `.session/`, and
  agentbus channels.
- Isolated worktrees are mandatory with concurrent coders, but a single coder/reviewer may be
  able to share the mission worktree using explicit folder/file ownership boundaries.
- A narrowly scoped PR-preparation agent may need no reviewer, state, or ledger and can report
  directly to its parent.
- Orchestration should execute an ordered list of clear subtasks.
- The task template should explicitly define what/goal, where/context, done/completion criteria,
  and limits/do-not-change. Missing fields in a user-assigned task require clarification.

User tentatively selected: one coder/reviewer may share the mission worktree with explicit
file/folder boundaries; concurrent coders require isolated worktrees; PR-preparation agents
are stateless; interactive coder mechanics should be specified now. Before finalizing, user
asked three implementation questions.

Bob documentation establishes:
- Background subagents are silent and return summaries to the parent; their chat cannot be
  opened interactively.
- Subtasks are separate, visible, interactive conversations with their own breadcrumb/history.
- A background subagent cannot be converted into a subtask. Continuing its work interactively
  requires a durable handoff to a new subtask/session against the persisted work.

Still unresolved: Bob documentation does not guarantee whether cleanup of a tool-managed
isolated worktree also deletes its temporary branch. Ordinary Git worktree removal preserves a
branch, but policy must not assume the tool cleanup behaves the same. Proposed safe contract:
commit work, record branch and SHA, and explicitly preserve/promote the branch before cleanup.

### Subtask/subagent model — unresolved, not approved

User explored context-isolated workers for session-bootstrap and PR-publish checklists, and a
long-lived background coder model. No design was approved. Bob and Claude expose different
subtask/subagent semantics, so conventions cannot assume Bob's native worker lifecycle unless
the user chooses that runtime explicitly.

Agentbus is gaining a user-interaction capability. This may let a background worker ask the
user questions asynchronously even when its native chat is not directly interactive, so
"background" must not be treated as synonymous with "unable to interact with the user."

User is considering three possible directions:

- choose one runtime's worker model;
- document separate Bob and Claude implementations; or
- define a runtime-neutral abstraction layer covering worker type, user interaction,
  persistence, worktree/branch ownership, result delivery, and handoff/resume.

Do not rewrite `conventions/coder-orchestration.md` around a concrete worker model until the
user settles this design.

## Remaining / carry forward

- Suggestion-box lifecycle convention (what happens to processed entries — `processed-` prefix is interim only, lifecycle not yet formally defined).
- `.bak` files: kept for now.
- Copy finished `policy-writer` conventions content into `session-tracking` — needs explicit go-ahead.
- Push `session-tracking` to `origin` — blocked on copy step above.
- Skill symlinks for other mission worktrees (`benchmark-*`, etc.) — self-heals on first `/resume-mission` use.

## Final checkpoint

The conversation lost substantial earlier context before wind-down. On resume, the session re-read
this ledger, `STATE.md`, `CONVENTIONS.md`, and `conventions/coder-orchestration.md`. No further
policy design was approved or drafted. The runtime-neutral worker abstraction remained only a
recommendation; the subtask/subagent model is still unresolved and must not be encoded yet.

Wind-down intentionally did not push: the current core convention requires explicit, single-use
authorization for each push, and none was given.

## Verified 2026-08-31 — all points already captured
