# task-reviewer-agg1

## Orientation

- **In:** `mission.composite-analyzer.reviewer-agg1.in`
- **Out:** `mission.composite-analyzer.reviewer-agg1.out`
  (subscribe to `In:` before starting; publish status/findings/completion to `Out:`)
- **Name:** `2026-09-09-reviewer-agg1`
- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md` — read this, then
  `conventions/reviewer.md`, in full before starting.
- **What / goal / mission:** Review `coder-agg1`'s commits implementing the composite-aggregation
  spec (`.session/spec.md`, v8, approved) as they land on the `composite-analyzer` branch, in this
  worktree. You are strictly read-only.
- **Worktree:** `worktrees/composite-analyzer`, branch `composite-analyzer`. Same worktree the
  coder is committing to (same-worktree setup) — read committed history only via `git log`/`git
  show`/`git diff <baseline>..HEAD`. Never touch uncommitted working-tree state, never edit
  anything in this worktree.
- **Startup verification instructions:** confirm `git rev-parse --show-toplevel` resolves to this
  worktree. Report and stop on mismatch.
- **Role / scope:** reviewer. Read-only. Your only write is this task's designated report file
  (below). Never modify code, never push, never touch GitHub.
- **Ledger / log:** none — findings go directly into the report file (per `reviewer.md`'s "early
  persistence": persist as discovered, don't hold findings in memory).

## Task

- **Baseline commit:** `4ce95b70` — the tip immediately before `coder-agg1` started. Everything
  after this on `composite-analyzer` is the coder's work to review. Re-run `git log
  4ce95b70..HEAD --oneline` periodically to pick up new commits.
- **Plan / spec:** `.session/spec.md` (v8, approved) — read §1, §2 upfront; pull §4–§9 per-commit as
  each commit's checklist item requires, same as the coder does.
- **Context:**
  - `.session/task-coder-agg1.md` — the coder's task file and 12-item checklist. Your primary
    verification checklist is built from this: for each landed commit, identify which checklist
    item(s) it corresponds to and verify against that item's spec citations and the Limits section.
  - `internal/engines/aggregation/aggregation.go` — the existing style the new code should match.
- **Expected output:** `.session/review-coder-agg1.md` (in this worktree — your one write target),
  updated continuously as commits land. One section per reviewed commit: commit SHA, which
  checklist item it implements, Phase 1 findings (clarity/structure/correctness/regressions),
  Phase 2 findings (spec conformance), and a verdict (Pass / Request Changes) for that commit.
  A running top section tracking overall status across all 12 checklist items.
- **Done / completion criteria:** every commit on `composite-analyzer` past the baseline has a
  review section; the final report has an overall verdict once `coder-agg1` reports completion on
  its own `Out:` channel (you can see this by periodically checking
  `mission.composite-analyzer.coder-agg1.out`, or the mission owner will forward it to your `In:`).
- **Limits:**
  - Do not wait for the coder to finish everything before reviewing — review each commit as it
    lands (`coder-orchestration.md` rule 9).
  - If the coder diverges from `task-coder-agg1.md`'s scope, or violates any of its Limits
    (touches `multi_backup/`, adds Score weighting, implements normalization, adds query-API
    helpers beyond D3's two scoped categories, computes model-level coverage as an eager field,
    changes `runAnalyzersAndScore`'s return type, breaks the sat-only regression test, etc.) —
    **notify the mission owner immediately** via `Out:`, don't just note it in the report and move
    on.
  - Trust the coder's own test execution claims per-commit; do not re-run the full suite yourself
    unless something looks wrong enough to warrant it.
  - Check for sanitization: no internal planning artifacts, task IDs, or private jargon leaking
    into code comments or commit messages.

## Execution

### Steps / subtasks
- [ ] Read spec §1–§2 and `task-coder-agg1.md` in full.
- [ ] Poll `git log 4ce95b70..HEAD --oneline` periodically (or react to a ping on `In:` when the
      mission owner forwards word that a new commit landed).
- [ ] For each new commit: Phase 1 (independent read), Phase 2 (spec conformance), write to
      report file, flag divergences immediately via `Out:`.
- [ ] On `coder-agg1` completion: final full-checklist pass across all 12 items, overall verdict.

### Status

`NOT STARTED`
