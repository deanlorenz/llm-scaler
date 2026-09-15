# Task: prune STATE.md to a short current-status file

- **In:** `composite-analyzer.prune-state.in`
- **Out:** `composite-analyzer.prune-state.out`
- **Name:** `2026-09-15-prune-state-1`
- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
- **What / goal / mission:** `.session/STATE.md` in this worktree has drifted from a short
  current-status pointer file into a historical log — the mission owner and user both flagged
  this. Your job is to prune it back to what STATE is supposed to be, WITHOUT losing any fact.
- **Worktree / Path / Branch:** same-worktree — you operate directly in
  `/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/composite-analyzer`, branch
  `composite-analyzer`. Do not create a separate worktree or branch.
- **Startup verification instructions:** confirm you are in the worktree above (`git branch
  --show-current` must print `composite-analyzer`) and that `.session/STATE.md` exists before
  doing anything else. If either check fails, stop and report on `Out:` — do not improvise a
  different target.
- **Role / scope:** documentation-hygiene worker. You edit `.session/STATE.md` only. You may
  READ any file in this worktree (spec doc, ledgers, drafts) to verify content is captured
  elsewhere, but you write to nothing except `.session/STATE.md` and your own ledger.
- **Ledger / log:** `.session/2026-09-15-prune-state-1.md` — create on first write.

## Task

**Progress reporting — required, not optional:** publish a short note to `Out:` after each
numbered phase below finishes (not just at the end), AND publish the same note (or a pointer to
it) via `agentbus_publish` on topic `composite-analyzer.prune-state.out` so the mission owner
sees it whether or not it's actively watching `Out:` at that moment. Use `kind="note"` for
progress, `kind="question"` if you get stuck, `kind="handoff"` for the final completion report.

**Claim the file before editing — `.wip` protocol (`conventions/wip-editing.md`):**
1. Rename `.session/STATE.md` → `.session/STATE.md.wip`. If `STATE.md` is already absent
   (someone else's lock), STOP and report on `Out:` — do not proceed.
2. Make all edits to `STATE.md.wip` directly.
3. Rename back to `STATE.md` only when you are fully done and ready to commit.
4. `git add .session/STATE.md && git commit`.

### Phase 1 — Inventory (report progress after this phase)

Read `.session/STATE.md` in full. List every section/block that is narrative-shaped rather than
current-status-shaped: long prose paragraphs re-explaining history, corrections, incidents, or
"why" reasoning, rather than a short fact + pointer. Candidates already identified by the
mission owner (verify these yourself, don't just trust the list):
- The "Pending spec fixes" bullet list in the Task section (currently ~30 lines of detailed
  per-subsection fix descriptions) — check whether this duplicates
  `.session/drafts/2026-09-14-spec-review-response.md`.
- The "Redesign discussion (RESOLVED...)" bullet in the Task section — historical corrections
  narrative.
- Checklist items in Execution/Steps that carry a full paragraph of "why"/history rather than a
  short status + pointer (e.g. the item citing "the full v1→v8 revision history," the
  `spec.md`-staleness item, the "Implementation — DONE" item).
- The "Superseded resume point" block under Next step (marked "no longer actionable" but still
  present in full).
- The "Status" section — check whether it re-states things already said in Task/Orientation
  rather than adding new information.

Report your inventory (a list of candidate blocks with line ranges) on `Out:` before touching
anything.

### Phase 2 — Verify BEFORE removing (report progress after this phase; this is the critical
safety step — do not skip or compress it)

For EVERY fact in each candidate block from Phase 1, before shortening or removing it, confirm
it already exists in its proper durable home:
- `.session/composite-signal-redesign.md` (§2 settled rules, §3 open items, §5 abstracts, §6
  decisions table, §7 detailed discussion, §8 revision log)
- `.session/drafts/2026-09-14-spec-review-response.md` (the pending-fixes edit-plan)
- The already-`## Verified`-captured ledgers: `.session/2026-09-14-composite-analyzer-2.md`,
  `.session/2026-09-14-composite-analyzer-1.md` (read only their tail/Verified table, per
  standing rule — do not read full retired ledgers otherwise)
- `.session/review/code-review-notes.md` (for the query_api.go / rounding-function item)

Build a small table (fact → where it's already captured, file + section) for every fact you
intend to compress or remove. If you find a fact that is NOT captured anywhere else, do NOT
delete it — either keep it in STATE as-is, or (if you judge it belongs in the spec doc instead)
flag it in your report and leave it in STATE for the mission owner to relocate manually. You do
not have write access to the spec doc or drafts — do not attempt to add anything there yourself.

Publish this verification table on `Out:` and WAIT — do not proceed to Phase 3 until you have
published it. (You do not need to block for a reply; publishing is the checkpoint, so the
mission owner can review the table against the actual removal in Phase 3's diff. But do not
skip straight from Phase 1 to editing — the table must exist and be published first.)

### Phase 3 — Edit (report progress after this phase)

Using targeted `Edit` calls only — never a single `Write` rewrite of the whole file (this
mission's own STATE history records an incident where a full-file `Write` rewrite silently
dropped content; the rule exists because of that, not as a style preference):

- Compress each verified-duplicate block to a short pointer: what happened (one line), where
  the full detail lives (exact doc + section). Follow the pattern already used for the "Last
  completed" blocks (see current STATE.md — that compression was done correctly and can serve
  as your template).
- Remove the "Superseded resume point" block if Phase 2 confirmed it is fully inert
  ("no longer actionable" with nothing else needed) — or compress to one line if the mission
  owner would want a pointer kept.
- Do not touch: Orientation, Session log, the Steps/subtasks checkboxes themselves (only trim
  attached narrative, never remove or reword a checkbox's core status line), Known issues.
- Preserve every §-reference, commit hash, file:line citation, and open question verbatim or by
  exact pointer — you are de-duplicating prose, not summarizing away specifics.

### Phase 4 — Self-check and commit (report progress after this phase)

1. `git diff .session/STATE.md` (against `STATE.md.wip`'s pre-edit state — diff the rename-back
   file against the last commit) and re-read the full result.
2. Confirm: every fact from Phase 2's table is either compressed-with-pointer or intentionally
   kept in full. Confirm nothing outside your verified list was touched.
3. Rename `STATE.md.wip` → `STATE.md`.
4. `git add .session/STATE.md && git commit -m "docs(state): prune remaining historical narrative (round 2)"`
   — do NOT amend, do NOT use `--no-verify`.
5. Publish final `kind="handoff"` report to `Out:` (and the topic) with: line-count before/after,
   list of blocks compressed, list of any facts you flagged as needing manual relocation (Phase
   2's escape hatch), and confirmation the commit landed (include the commit hash).

## Limits

- Do not edit any file other than `.session/STATE.md` and your own ledger
  `.session/2026-09-15-prune-state-1.md`.
- Do not touch the spec doc, drafts, or any ledger file.
- Do not push, do not open a PR, do not rebase.
- Do not remove any fact that Phase 2 could not verify as captured elsewhere.
- If you get stuck or find something ambiguous (e.g. unsure whether a block duplicates the spec
  doc closely enough to remove), publish a `kind="question"` on `Out:` and wait — do not guess.
- Terminate after Phase 4's final report. Do not hold open waiting for further instruction
  unless told to in a reply.

## Done / completion criteria

- `.session/STATE.md` is committed, shorter, every checklist/status fact intact or pointed-to,
  no single-`Write` rewrite used (verifiable via the sequence of Edit calls / diff shape).
- Phase 2's verification table was published BEFORE Phase 3's edits, not after.
- Final `Out:`/topic report includes the commit hash.
