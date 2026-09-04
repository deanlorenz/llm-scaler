# Spec — session-tracking infrastructure

> **Reading rule:** read sections 1–2 upfront (orientation + principles). Stop there.
> Sections 3+ are on-demand only — pull as needed, not at session start.

---

## 1. Quick summary / orientation

Build and maintain the cross-mission, cross-worktree session-tracking system: the
conventions, skills, and layout that let any mission resume cleanly after a restart
without reloading full history.

Deliverables live in `worktrees/session-tracking` (branch `session-tracking`). All
drafting happens in `worktrees/policy-writer` (this branch); finished content is copied
into `session-tracking` and pushed.

---

## 2. Principles / approach

- **One STATE per mission.** Short, overwritten-in-place as status changes. Self-contained
  for orientation. Ledgers are append-only per-session; a resuming session never reads them
  upfront.
- **Draft here, install there.** `policy-writer` drafts all changes. `session-tracking`
  receives finished copies only. Never draft directly in `session-tracking`.
- **What/how only in production files.** `CONVENTIONS.md` and `conventions/*.md` carry only
  the rule — stated as what to do and how. Rationale, incident history, and design background
  live in this spec (section 7).
- **No deviation from approved plan.** Once a plan is approved, its listed steps are the
  complete boundary of authorized action. Stop and ask if something adjacent surfaces.
- **Ledger-capture never touches `CONVENTIONS.md`.** Only `policy-writer` may change
  `CONVENTIONS.md`. Ledger-capture writes to `STATE.md` and the plan/spec doc only; global
  findings go to the suggestion box.
- **Skills run in their own context.** `/resume-mission`, `/wind-down`, `ledger-capture`
  may be invoked as a subtask or subagent — they get their own context window either way.

---

## 3. At-a-glance

| | |
|---|---|
| **Branch / worktree** | `policy-writer` / `worktrees/policy-writer` |
| **Installs into** | `worktrees/session-tracking` (branch `session-tracking`) |
| **Status** | IN PROGRESS |
| **Last pushed** | `1427f964` (session-tracking), `d52e4f86` (policy-writer) — 2026-09-03 |
| **Active work** | Conventions fixes (session-start, state-vs-ledger, CONVENTIONS.md); spec refactor; skill updates |
| **Blocking** | Nothing currently — see section 4 for open decisions |

---

## 4. Needs me

Items currently open that require a decision or ruling before they can proceed:

- **`settings-and-skill-edits.md` marker behavior** — origin traced to 2026-08-27 observed
  harness behavior; user does not recognize the rule. Cannot rely on it until verified. Need
  a test or explicit ruling before editing any `SKILL.md`.
- **Suggestion-box lifecycle** — what happens to `processed-*` entries? Delete, archive, or
  leave? Currently using `processed-` prefix as interim; formally undefined.
- **End-to-end test of `/resume-mission` and `/wind-down`** — neither has been invoked by a
  real session since being written. First real invocation should be treated as a live test.
  No decision needed, just acknowledgment that this gap exists.

---

## 5. Roadmap / checklist

- [x] T1 — Branch, worktree, layout (2026-08-27)
- [x] T2 — `.wip` protocol (2026-08-27)
- [x] T3 — Session log + ledger-capture (2026-08-27)
- [x] T4 — `/resume-mission` and `/wind-down` skills written (2026-08-27) *(not end-to-end tested)*
- [~] T5 — Skill discoverability across worktrees *(symlinks: only `single-analyzer` done)*
- [x] T6 — `pr-review` suppression (2026-08-27)
- [x] T7 — Ledger-capture contract: no CONVENTIONS.md writes; suggestion box (2026-08-31)
- [x] T8 — Coder orchestration: worker types + Bob CLI mechanics (2026-09-03)
- [x] T9 — Reader-focused conventions review pass (2026-09-03)
- [x] T9b — Conventions fixes: session-start, state-vs-ledger, CONVENTIONS.md upfront-read rules (2026-09-04)
- [x] T9c — spec-policy-writer.md refactored into canonical structure (2026-09-04)
- [ ] T9d — Canonical spec structure documented in `tasks.md`
- [ ] T10 — Resume-mission as custom-agent (spec + mode)
- [ ] T11 — Wind-down as custom-agent (spec + mode)
- [ ] T12 — Ledger-capture as custom-agent (spec + mode)
- [ ] T13 — Session-setup custom-agent (spec + mode)
- [ ] Install + push all T9b–T9d changes to session-tracking

---

## 6. Outline

| Item | Summary | Status |
|---|---|---|
| T1 — Branch/worktree/layout | Orphan `session-tracking` branch; `missions/` layout; first mission migrated | DONE |
| T2 — `.wip` protocol | Claim-ownership protocol for shared mutable files | DONE |
| T3 — Session log + ledger-capture | Per-session log entries; ledger-capture job definition | DONE |
| T4 — Skills written | `/resume-mission` + `/wind-down` SKILL.md authored | DONE (not e2e tested) |
| T5 — Skill discoverability | Discovery does not walk past worktree root; symlink pattern | IN PROGRESS |
| T6 — pr-review suppression | `skillOverrides` in `~/.claude/settings.json` | DONE |
| T7 — Ledger-capture contract | Never write CONVENTIONS.md; suggestion box for global findings | DONE |
| T8 — Coder orchestration | Three worker types (Claude FW/BG, Bob CLI); Bob launch mechanics | DONE |
| T9 — Conventions review | All `conventions/*.md` files reviewed + revised by user | DONE |
| T9b — Upfront-read fixes | session-start, state-vs-ledger, CONVENTIONS.md — no plan/ledger reads | DONE |
| T9c — Spec refactor | spec-policy-writer.md → canonical 8-section structure | DONE |
| T9d — tasks.md spec template | Canonical spec structure documented as writer guide | TODO |
| T10 — resume-mission custom-agent | Resume as custom-agent (own context, simple model) | TODO |
| T11 — wind-down custom-agent | Wind-down as custom-agent | TODO |
| T12 — ledger-capture custom-agent | Ledger-capture as custom-agent | TODO |
| T13 — session-setup custom-agent | Pre-session setup as FG custom-agent | TODO |
| Worktree/mission model correction | session-tracking is production; policy-writer drafts only | DONE — see T1 detail |
| `.git/info/exclude` shared | Shared across all worktrees of a repo, not per-worktree | DONE — see T5 detail |
| pr-review upstream content | Upstream content untouched; suppressed via settings only | DONE — see T6 detail |
| Plan persistence rule | Approved plan committed immediately to durable file | DONE — see T9 detail |
| Rationale separation rule | Production files: what/how only; rationale → this spec | DONE — see T9 detail |

---

## 7. Details

### T1 — Branch, worktree, and layout

**Status.** DONE 2026-08-27. Orphan `session-tracking` branch created, pushed to
`origin`. `CONVENTIONS.md` written. `analyzer-optimizer-refactor`'s pre-existing docs
migrated in verbatim as the first mission (byte-identical, verified via `diff` before
removing the originals from `single-analyzer`).

### T2 — `.wip` protocol

**Status.** DONE 2026-08-27, documented in `CONVENTIONS.md`. Not yet exercised under
genuine concurrent access — only used single-threaded so far. Revisit if that ever
becomes a real problem.

### T3 — Session log format + ledger-capture

**Status.** DONE 2026-08-27, validated once. First real run audited the old 94KB
`ledger-analyzer-optimizer-refactor.md`, found 2 genuine gaps (a global behavioral rule
not yet generalized into `CONVENTIONS.md`; a confirmed bug finding cited only by ledger
section number rather than stated/linked from the durable docs), fixed both directly,
committed as `2b4927ba`. Renamed "verifier" → "ledger-capture" afterward (`14ce29d3`) —
the run itself is what revealed the name undersold the job.

### T4 — `/resume-mission` and `/wind-down` skills

**Status.** DONE 2026-08-27 (`8942841d`, path-corrected `8ffa77e0`). **Not yet tested
end-to-end** — neither skill has actually been invoked by a session since being
written. This is a real gap, not just an unexercised edge case: the first real
`/resume-mission` or `/wind-down` invocation should be treated as a live test, and any
step that doesn't work as documented should be fixed and noted here.

### T5 — Skill discoverability across worktrees

**Status.** IN PROGRESS. Discovery mechanism confirmed by direct testing (does not walk
up past a worktree's own root). Symlink pattern documented in `CONVENTIONS.md`,
`resume-mission`'s own Step 5 self-heals it for whatever worktree it enters. **Only
`single-analyzer` has actually had the one-time setup done.** Every other existing
feature worktree (six `benchmark-*`, `fix-scaledobjects-cold-start`, and any future
ones) needs the same setup before these skills work there — will happen naturally as
`/resume-mission` is used in each, per its self-healing Step 5, or can be done proactively.

### T6 — `pr-review` suppression

**Status.** DONE 2026-08-27. `skillOverrides: {"pr-review": "off"}` in
`~/.claude/settings.json`. Skill files themselves untouched everywhere, per the user's
explicit instruction that upstream content stays as-is.

**Background.** The `pr-review` skill is pre-existing upstream content (authored by
someone else, PR #1039/#1041/#1078, present on 30+ branches). The user doesn't want it
available. Resolved via a global, personal settings override — zero git changes anywhere,
no per-branch/per-worktree duplication needed (unlike the skill files themselves, which
are per-worktree-discoverable and needed the symlink treatment above).

### T7 — Correct ledger-capture's contract: never touch `CONVENTIONS.md`

**Status.** DONE 2026-08-31 (policy-writer-8). Drafted into
`worktrees/policy-writer/conventions/resume-and-handoff.md` (ledger-capture section):
explicit prohibition added, suggestion-box mechanism documented, step 1 corrected to list
only `STATE.md` + plan/spec doc as legitimate write destinations. Commit `f7508d08`
(policy-writer branch). Not yet copied to `session-tracking`.

**Design record.** Ledger-capture must never write to `CONVENTIONS.md`. Only
`policy-writer` may change `CONVENTIONS.md`. Suggestion-box
(`session-tracking/suggestion-box/`) is the replacement path for global findings.
Lifecycle of processed suggestion-box files remains explicitly deferred.

### T8 — Coder orchestration: worker types and Bob CLI mechanics

**Status.** DONE 2026-09-03 (policy-writer-9). Commits on `policy-writer` branch:
- `0f64564b` — initial rewrite with Claude FW/BG + Bob CLI model
- `c9288a40` — refined: worker types → compact table, WVA ref → invocation snippet,
  rules 8/9 rewritten, task template extracted to `conventions/tasks.md`

`conventions/tasks.md` created (new). Not yet copied to `session-tracking`.

**Worker types — design rationale.**

Three worker types are available. The rule file (`conventions/coder-orchestration.md`)
carries only the compact reference. The rationale is here.

*Why three types:*
- Claude FW (foreground/subtask) and BG (background subagent) differ in interactivity and
  context visibility — not in capability. FW is right when the user or mission owner needs
  to observe or redirect mid-task. BG is right when the task is genuinely self-contained
  and only the result matters.
- Bob CLI is a third category entirely: a separate OS process, not a Claude subagent.
  Right when persistent session context across multiple invocations is needed, or when a
  Bob-specific custom mode is wanted. A Bob session accumulates context incrementally across
  resumed invocations; a Claude BG subagent starts fresh each time.
- The agentbus interaction channel is what makes Bob CLI viable alongside Claude sessions:
  it removes the dependency on `SendMessage` timing constraints and gives a reliable,
  persistent channel for status, questions, and findings.

*Claude FW (foreground/subtask):*
- Implemented as Claude's native subtask mechanism (`start_subtask`).
- Visible in the UI; has its own conversation breadcrumb; user can interact with it directly.
- Own context window; does not share the parent's context.
- Use when: real-time review is needed, task may need mid-course steering, or user wants
  direct visibility.

*Claude BG (background subagent):*
- Implemented as Claude's native background subagent (`spawn_subagent`).
- Silent during execution; returns a summary to the parent when done.
- Claude supports attaching to a BG agent for interactive mid-task guidance.
- Use when: task is clearly self-contained, only the result matters, no mid-course steering
  expected.

*Bob CLI coder:*
- IBM's Bob Shell, invoked as its own OS process — not a Claude subagent.
- Runs headless via `bob run`; interactive via `bob chat`.
- Persistent context via `--resume <task-id>`: `bob run` is one-shot per invocation, but
  `--resume` reopens the same conversation with full prior context.
- Primary interaction channel: agentbus (`SendMessage` has timing constraints).
- Use when: persistent session context is valuable, or a Bob-specific custom mode is needed.

**Bob CLI launch mechanics** (sourced from WVA legacy repo,
`/home/dean/code/llm-d/llm-d-workload-variant-autoscaler/plans-tooling/`):

```bash
nohup bob run --accept-license --workspace <worktree-path> --mode <mode> \
  --resume <task-id> -f stream-json "$PROMPT" \
  > <logfile> 2>&1 &
```

The `--resume <task-id>` value is the whole "persistent" part of this setup. Losing it
means the next task starts cold. Write the task-id into the task file in the coder's
worktree before launch — it must survive independently of the parent session's chat history.

**Bob's write scope.** Bob keeps a local state file inside its own worktree (gitignored).
Anything needed outside its worktree goes to the mission owner via a report/finding in its
ledger. A blocked write is the boundary working as intended, not something to route around.
Learned via a real incident: Bob used `execute_command` + `git commit` to cross the boundary
after `write_file` was blocked; that commit was reverted via `git revert`.

**Task delivery pattern.** Parent prepares task file and places it in the coder's worktree
before launch. Resumes Bob with a short prompt pointing at the task file. Bob reads the task
file itself — parent does not restate spec content in the prompt.

### T9 — Reader-focused conventions review pass

**Status.** DONE 2026-09-03 (policy-writer-9). All `conventions/*.md` files reviewed by
user and processed. Commits on `policy-writer` branch:
- `e407102b` — wip-editing, state-vs-ledger, push, settings-and-skill-edits,
  unexplained-files, writing-outside-worktree
- `bef2d39b` — feature-worktree-setup (LGTM, annotation stripped)
- `cfdf0295` — session-start.md rewritten with per-session STATE model
- `5eb61b84` — CONVENTIONS.md index trigger fixes

Key changes per file:
- `wip-editing.md` — generalized to any shared file; protocol reduced to 4 steps; plan
  section simplified to "save to .session/ on exitPlanMode".
- `state-vs-ledger.md` — trimmed verbose sections; STATE template added.
- `push.md` — removed named remote; generalized to "non-origin requires extra authorization".
- `settings-and-skill-edits.md` — origin flagged as observed harness behavior, not a user
  rule; verify before relying on it.
- `unexplained-files.md`, `writing-outside-worktree.md` — LGTM, annotations stripped.
- `feature-worktree-setup.md` — LGTM, annotation stripped; custom-agent candidacy noted.
- `session-start.md` — rewritten: per-session STATE file model, slug-based discovery,
  ledger refs STATE + previous ledger.

Not yet copied to `session-tracking`.

### T9b — Conventions fixes: upfront-read rules (2026-09-04)

**Status.** DONE 2026-09-04 (policy-writer-10). Root cause: sessions were reading full
plan/spec docs and full ledger files at session start, burning 50K+ tokens before any work
began. Three files updated:

- `conventions/session-start.md` — added "Reading rules — upfront" section (explicit
  never-read list for plan docs and ledger files); added "Opening orientation" block (fixed
  format: mission/role/worktree/status/last/next); new-ledger-at-start step; session-log
  append step; skills-own-context note.
- `conventions/state-vs-ledger.md` — split `Context/Refs` field into `Context` (must-read)
  and `Refs` (do not read); added `(do not read upfront)` note on `Plan/spec` field.
- `CONVENTIONS.md` — added standing rule to Ground rules: never read plan/spec docs or
  ledger files at session start.

### T9c — spec-policy-writer.md refactored (2026-09-04)

**Status.** DONE 2026-09-04 (policy-writer-10). Refactored into canonical 8-section
structure: orientation, principles, at-a-glance, needs-me, roadmap, outline, details, refs.
Reading rule added at top: read sections 1–2 upfront only.

### T10 — Resume-mission as custom-agent

**Status.** DESIGN DIRECTION CAPTURED 2026-09-04 (policy-writer-10). Not yet implemented.

`resume-mission` becomes a custom-agent (simple model, own context — doesn't burn the main
session's tokens). Procedural steps can stay as a skill initially, called from the
custom-agent. Long-term: fold everything into the custom-agent, retire the skill. Same
pattern applies to `wind-down` (T11) and `ledger-capture` (T12).

Key point: the "check if prior ledger was captured" step runs inside the custom-agent,
before main session tokens are charged.

### T11 — Wind-down as custom-agent

**Status.** NOT STARTED. Symmetric to T10.

### T12 — Ledger-capture as custom-agent

**Status.** NOT STARTED. Symmetric to T10/T11. Currently described as a background agent
in `resume-and-handoff.md` but not implemented as a proper custom-agent with its own
spec/mode.

### T13 — Session-setup custom-agent (FG)

**Status.** DESIGN CAPTURED 2026-09-03 (policy-writer-9). Not yet implemented.

A foreground (FG) custom-agent that prepares the environment for a new session before
that session starts. Mechanical tasks, simple model, runs out of the main session's context.

Steps it handles:
- Create the worktree (if needed)
- Create missing symlinks (skill symlinks per `feature-worktree-setup.md`)
- Create the initial STATE file from the unified template (`state-vs-ledger.md`)
- Populate STATE: name, conventions path, mission, role, worktree, ledger path, task fields
- Find and link relevant context files
- Commit the STATE file to the mission branch

Why FG: the setup result (STATE file path) needs to be confirmed before the main session
starts. FG allows the user to verify before handoff.

---

## 8. Refs

*Related files (do not read unless explicitly needed):*

- `worktrees/policy-writer/PLAN-conventions-split.md` — approved plan for the
  CONVENTIONS.md split (Phase 1 + Phase 2); full file-by-file content mapping. Lives in
  `policy-writer` branch only; not yet copied to `session-tracking`.
- `worktrees/session-tracking/CONVENTIONS.md` — production copy (installed)
- `worktrees/session-tracking/conventions/` — production copies (installed)
- `.claude/skills/resume-mission/SKILL.md` — skill file (symlink in this worktree)
- `.claude/skills/wind-down/SKILL.md` — skill file (symlink in this worktree)
- `/home/dean/code/llm-d/llm-d-workload-variant-autoscaler/plans-tooling/conventions/bob-delegation.md` — source for Bob CLI mechanics (T8)
- `/home/dean/code/llm-d/llm-d-workload-variant-autoscaler/plans-tooling/planning/atomic-step-protocol-design-v2.md` — source for Bob step protocol (T8)
