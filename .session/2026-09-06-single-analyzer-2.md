Continues: .session/2026-09-06-single-analyzer-1.md

# Session ledger — 2026-09-06-single-analyzer-2

## Session start

- Resumed mission `single-analyzer` via `/resume-mission` skill.
- All prior sessions verified (retired + `## Verified` markers present). No pending ledgers.
- Ownership declared on agentbus (`mission.single-analyzer`, seq=80).
- Read `session-tracking` `CONVENTIONS.md` (user-directed, separate from resume-mission flow).

## Findings — cleanup / orientation pass

User asked: understand where we are, record findings, before deciding on the uncommitted
test-file diff found in the working tree. Investigated two worktrees.

### 1. Working tree has uncommitted test-only changes (not production code)

`git status` showed 6 modified `_test.go` files, 1 deleted `_test.go` file, plus untracked
`.session/` and `docs/plans/analyzers/` docs. Zero non-test production files touched
(`git status --short | grep -v "_test.go\|\.session\|docs/plans"` → empty).

Traced the cause: commit `f20e06f9` (CT6) changed `runAnalyzersAndScore`'s return type from
`allocation.NamedAnalyzerResult` to `[]allocation.NamedAnalyzerResult` (confirmed via
`git log -L 103,114:internal/engines/steadystate/engine_v2.go`) and removed
`composeAnalyzerResults`/`rawAnalyzerResult` as vacuous pass-throughs (per
`.session/2026-09-01-s8.md` line 50-51). But the commit's `git add` never picked up the test
call sites — 6 files still did `result, err := e.runAnalyzersAndScore(...)` then `result.Name`
etc., which does not compile against a slice return (`.Name` is not a field/method on
`[]NamedAnalyzerResult`).

Verified: `go build ./...` and `go vet ./internal/engines/...` both pass cleanly **with** the
uncommitted working-tree edits applied. This confirms the edits are the correct, necessary
fix, not exploratory work — and that HEAD without them does not compile.

`engine_v2_compose_test.go` (deleted from `steadystate/`, re-added at
`internal/engines/allocation/multi_backup/engine_v2_compose_test.go` with `//go:build ignore`,
content byte-identical) tested the now-deleted `composeAnalyzerResults`, so it had to move —
matches the already-documented "pre-CT3b multi-entry originals" backup pattern
(STATE.md context line: `internal/engines/allocation/multi_backup/`).

**Conclusion: this is a missed piece of CT6 itself, not new/experimental work.** It's been
sitting uncommitted since 2026-08-31, unnoticed across 3 subsequent sessions (s8,
2026-09-06-single-analyzer-1, and now).

### 2. PR #34 is already MERGED — STATE.md was stale

`gh pr view 34 --repo ev-shindin/llm-scaler` shows `"state":"MERGED"`, `mergedAt:
2026-09-02T08:01:59Z`, containing exactly 2 commits: `1a0c23be` (CT1a nil-guard) and
`113fec1d` (CT2 CompositeSignal refactor). STATE.md said "open... awaiting review" — corrected
in this session (see STATE.md Status section).

### 3. Second worktree: `/home/dean/code/llm-d/worktrees/pr-single-analyzer`

Branch `pr/single-analyzer`, repo `deanlorenz/llm-scaler` (fork used for PR staging, distinct
from this sandbox monorepo). This branch/worktree **is** PR #34 — now merged, working tree
clean, nothing further pending there. It is not "in progress" anymore.

Checked `origin/single-analyzer` (same fork) via `git fetch` from that worktree: tip
`d90bd565`, confirmed (via `git merge-base --is-ancestor`) to be an ancestor of this session's
local `single-analyzer` HEAD (`23f5039b`) — i.e. origin already has CT3b (`b980f682`), CT5
(`fcf9c905`), the s7 stale-args fix (`b067642a`), and CT6 (`f20e06f9`) pushed. It is missing
only the latest housekeeping/ledger commits, not any code. **No PR branch or PR exists yet for
this payload** — this is the "PR branch in progress, not yet a PR" the user was recalling:
code is staged on origin, but never cut into its own `pr/...` branch or opened.

Full commit list, CT2 to HEAD, on mission branch (`git log --oneline e4106109..HEAD`):
non-doc code commits are `b980f682` (CT3b), `fcf9c905` (CT5), `b067642a` (stale-args fix),
`f20e06f9` (CT6). Everything else in that range is `docs(state)`/`docs(ledger)` housekeeping.

**Important: since the CT6 test-fix (finding #1) was never committed anywhere, origin's
pushed HEAD (`d90bd565` and descendants) does not build either** — same missing fix.

### 4. PR #2 design doc already exists, with unresolved open questions

`docs/plans/analyzers/compose-reduce-design-2026-08-30.md` (untracked) is a real design doc
for the engine-side reduce: floor invariant (composite never less aggressive than sat alone),
implied-replica-count as the common currency across analyzer units, sat-only fast path (zero
behavioral change when sat is the only entry). Four open questions block writing code:
- Q1: does per-variant `TotalDemand` get raised to match the recomputed model-level max, or
  stay as sat's for display?
- Q2: does a non-sat analyzer with no `RoleDemand` participate in the per-role reduce?
- Q3: does the composite keep the name `"saturation"` (so `isSaturationResult` at
  engine_v2.go:747 still matches), or does that check change?
- Q4: what Score does the composite carry — sat's, max across analyzers, or a fixed value?

### 5. Other untracked files inventory

`docs/plans/analyzers/`: 5 more files (`engine-call-map`, `engine-call-stack`,
`optimizer-call-map`, `optimizer-call-stack`, `single-analyzer-call-map`, all
`-2026-08-30.md`) — CT3 research artifacts, correctly placed per AGENTS.md's
`docs/plans/<area>/` convention, just never committed.

`.session/`: several untracked ledgers/reports from already-retired-and-verified sessions
(should have been committed at their own wind-down but weren't), plus `spec.md.local` (stale
pre-CT6 snapshot of the spec, superseded by `spec.md.wip`) and `fallback-trace.md.local` (a
separate, unrelated scale-from-zero investigation doc). `spec.md.wip` itself has a stale
`.wip` lock name (no `spec.md` counterpart) — flagged, not yet resolved, per
`conventions/wip-editing.md`; this is a stale abandoned lock from a prior session, not a live
concurrent editor.

## Open items handed to user (not yet decided)

1. Commit the CT6 test-fix (finding #1) — needed regardless of PR #2 scope, since CT6 as
   pushed doesn't build.
2. Task spec for the next PR: does it carry CT3b+CT5+fix+CT6 only (already on origin, modulo
   the fix), or wait for PR #2 (engine-side reduce) design questions (finding #4) to resolve
   first, or split into two PRs?
3. What to do with the "leftover" test changes — user flagged uncertainty about whether some
   of these are needed later. Needs clarification: the multi_backup move is a deliberate,
   permanent parking spot (already-documented pattern); the 6 modified test files are a
   required compile fix, not optional/experimental.
4. Untracked docs/plans/analyzers files and .session files — commit as-is, or clean up first?
5. Stale `spec.md.wip` lock — release/rename, or leave as active spec?
