Continues: .session/2026-09-17-policy-writer-21.md

# Session 2026-09-22-policy-writer-22

## Session start

- Resumed from session-21. No live heartbeat on mission.policy-writer — session-21 gone (context ended).
- Session-21 ledger has no ## Verified marker; STATUS was active → retiring.
- No new messages on session-tracking.pending-commits (last seq=82) or mission.policy-writer (last seq=220).
- Three unprocessed suggestion-box entries found: 2026-09-17-2330, 2026-09-18-0337, 2026-09-22-1730.
- Ledger opened. STATE updated: session-21 → retired, session-22 → active.

## Suggestion-box processing

### Entry 14 — 2026-09-17-2330-composite-analyzer
Both items already implemented in CONVENTIONS.md (line 9, line 89). No change needed. Prefixed.

### Entry 15 — 2026-09-18-0337-composite-analyzer
Item 1: Added ground rule to CONVENTIONS.md Ground rules: durable doc claims about code state
are past-state; re-derive from current code before repeating to user.
Item 2 (read governing convention before first role dispatch): trigger table already handles
this structurally. No additional rule needed.

### Entry 16 — 2026-09-22-1730-composite-analyzer
Item 1: Added to tasks.md Extra rules / rule refs field: delegator cites wip-editing.md to
subagent; does not read it themselves unless also editing directly.
Item 2: Rewrote ledger-capture Contract in resume-and-handoff.md — added:
  (2) wip-editing.md requirement as standing role rule
  (3) marker-based capture scope (start from last ## Verified, not from scratch)
  (9) report-back rule: terse verdict by default; substantive only when gaps found

Commits: 37ff840b (policy-writer), 006a78e5 (session-tracking).

## Install and push

- Cherry-picked e5ffbe80..ea70162c onto session-tracking (skipping .session/ commits).
  Conflicts were all .session/ files appearing in "save originals to spec" commits — resolved
  by dropping them (they must never go to session-tracking).
- Verified diff empty: CONVENTIONS.md, conventions/, claude-skills/ match policy-writer exactly.
- Install commit 74fabda7 on session-tracking records policy-writer@ea70162c.
- Pushed session-tracking to origin: c5389a317..74fabda7d.
- Pushed policy-writer to origin: 2272f2f70..ea70162c3.

## chat-preferences restoration + install note

- Found: session-20 install (681f32c1) deleted the rich output rules from chat-preferences.md
  (icon set, H3 headers, numbered summaries). Added in ba24f86a, stripped by the install.
  Root cause: policy-writer's chat-preferences.md was at the stripped version when session-21
  trim pass ran — trim pass preserved the stripped state.
- Restored full content from ba24f86a. Committed as dce721b7 on policy-writer.
- Also added .session/ cherry-pick conflict note to install-to-session-tracking.md (same commit).
- To install: needs user authorization and push.

## Install #2 + data-safety audit

- Step 2 content diff: only policy-writer ahead on chat-preferences.md and
  install-to-session-tracking.md — both expected, no session-tracking content missing.
- Cherry-picked ea70162c..policy-writer (dce721b7, 800462c6) — no conflicts.
- Verified diff clean after cherry-pick.
- Install commit 9a693426 on session-tracking records policy-writer@800462c6.
- Pushed session-tracking: 74fabda7..9a693426. Pushed policy-writer: ea70162c..800462c6.
- Data-safety audit: all other direct-to-session-tracking commits on conventions/ scope
  were correctly ported to policy-writer before prior installs. Only chat-preferences.md
  was lost (ba24f86a bypassed policy-writer; 681f32c1 install overwrote it). Now restored.

## Conv-* skills restructure plan

### Deferred: ledger-flush background agent
Idea: bg agent tails session JSON, persists summaries to ledger, signals parent when
ledger-capture is due. Alternative: invoke simple sub-agent at triggers (after tool calls
or user prompts). Deferred — finish skills restructure first.

### Full plan (A–H)

**A. Extract templates** (3 new files, flat in `conventions/`, `_template.md` suffix):
- `conventions/state_template.md` — unified STATE/task file template (from `state-vs-ledger.md`)
- `conventions/task_file_template.md` — worker task file fields (from `tasks.md`)
- `conventions/mission_spec_template.md` — mission spec/roadmap structure (from `tasks.md`)

**B. Update reference docs** to point to templates:
- `conventions/state-vs-ledger.md` — replace embedded template with pointer to `state_template.md`
- `conventions/tasks.md` — replace embedded task-file fields with pointer to `task_file_template.md`;
  replace spec template section with pointer to `mission_spec_template.md`

**C. Update skills** to reference templates by path:
- `conv-state-vs-ledger` — add "Template: `conventions/state_template.md`"
- `conv-tasks` — add pointers to both new template files; remove embedded template

**D. Rewrite `conv-coder-orchestration`** — inline main-path mechanics from `coder-orchestration.md`;
  keep `worktree-delegation.md` reference for alt setups (checkout-branch, own-worktree);
  archive `coder-orchestration.md`.

**E. Archive** `coder-orchestration.md` + 10 action-trigger convention files → `conventions/archived/`.
  Update CONVENTIONS.md trigger table.

**F. Fix skills 10, 11** per feedback:
  - `conv-tasks`: add template references, remove embedded template (done in C)
  - `conv-coder-orchestration`: inline main-path mechanics (done in D)

**G. Fix skill 13** (`conv-agentbus-user-interaction`): broaden description to cover all
  background user communication (progress notes, async questions, blocking confirmations, re-ask).

**H. Update CONVENTIONS.md** trigger table — replace archived file references with skill names.

Session A–C approved to run now.

## Install #3

- Correct range: 800462c6..policy-writer (2 commits: conv-* skills + template refactor).
  Previous attempt used wrong range (9a693426..policy-writer = full policy-writer history);
  aborted on old missions/ conflicts. Fixed by using the policy-writer SHA from the last
  install commit message.
- Cherry-pick clean, no conflicts.
- Verified diff empty.
- Install commit a2d32b36 on session-tracking records policy-writer@c636506d.
- Pushed session-tracking: 9a693426..a2d32b36. Pushed policy-writer: 800462c6..c636506d.

## D–H execution

D. conv-coder-orchestration: inlined same-worktree main-path mechanics (parent pre-steps,
   universal coder startup a–f, launch, post-steps). Non-default setups reference
   worktree-delegation.md. Removed Bob CLI launch from a standalone section — now inline.

E. Archived 11 files to conventions/archived/ (10 action-trigger + coder-orchestration.md).
   All original content preserved. Skills are now the live docs.

G. conv-agentbus-user-interaction: description broadened to cover progress notes, async
   questions, blocking confirmations, re-ask/wait — not just "asking questions".

H. CONVENTIONS.md trigger table: all action-trigger entries replaced with skill names.
   Lifecycle entries for feature-worktree-setup + state-vs-ledger updated to skill names.
   Hard-gates instruction updated to say "invoke the named skill".

Commit: 885aeb99 on policy-writer.
