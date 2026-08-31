# Mission state — single-analyzer

**Last updated:** 2026-08-30. This doc is overwritten on each update, not append-only — it
reflects current status only. For global process rules see `../../CONVENTIONS.md`. For the
full task plan see `spec-composite-metric-and-optimizer-t2.md`. For the chronological
reasoning trail (why decisions were made) see `ledger-analyzer-optimizer-refactor.md` —
reference only, not needed to resume. Per-session ledgers are in `ledgers/`.

## Worktrees used for this mission

- `worktrees/single-analyzer` (branch `single-analyzer`) — the feature-work worktree where
  code changes for this mission actually land.
- `worktrees/session-tracking` (branch `session-tracking`, this worktree) — mission plan/state
  and session ledgers, never pushed to `upstream`, only `origin`.
- `/home/dean/code/llm-d/worktrees/pr-single-analyzer` (branch `pr/single-analyzer`) — clean PR branch off `origin/main`; two commits ready for upstream review. Created 2026-08-31.
- Coder-agent worktrees are ephemeral (`.claude/worktrees/agent-<id>`), created and torn down
  per task — not listed individually here; each task's completion note below cites the commit
  hash that was cherry-picked out of one, not the ephemeral worktree path itself.

Coding tasks are dispatched to coder agents in their own separate `isolation: "worktree"`
instances; the orchestrating session reviews each and cherry-picks/merges approved commits
back into `single-analyzer` itself (see `../../CONVENTIONS.md`).

## Mission, one line

Add an engine-side step that composes N analyzer results into one before the optimizer sees
them, so the optimizer can be simplified back to single-analyzer-only logic. Must be a no-op
when saturation is the only enabled analyzer (today's default).

## Task status

| Task | Status | Notes |
|---|---|---|
| T1 — compose analyzer results (engine-side) | **SUPERSEDED** | The `composeAnalyzerResults` stub approach was reverted in session `2026-08-30-ct3-s6`. Clean approach: engine passes `namedResults[0]` (sat) directly as `CompositeSignal`; no intermediate compose step needed for this PR. Engine-side reduce deferred to next PR. |
| CT1a — nil-guard `rescaleModelDecisions` | **DONE** | Commit `8906ef7b` on `single-analyzer`. Report: `ct1a-implementation-report-2026-08-26.md`. |
| CT1b — engine-side guard on nil saturation result | **DEFERRED** | Excluded from PR #1 by user request (not structural — separate bugfix). Will land in a future PR. Commit exists on `single-analyzer` branch (`122d1699`) but not in `pr/single-analyzer`. |
| CT2 — collapse `AnalyzerResults []NamedAnalyzerResult` to single `CompositeSignal` field | **DONE** | Commit `e4106109` on `single-analyzer` (cherry-picked from coder's `40df4066`, dispatched with `git checkout c6e408c4` as its first step per the earlier worktree-base fix). Field renamed across 8 production files + 10 test files; `saturationNamedEntry` deleted. One test (`greedy_score_optimizer_test.go` T1.4) had a genuinely multi-analyzer premise, structurally impossible now — `Skip()`-ed using T1's existing precedent/message, not a new resolution. Reviewed in full by the orchestrating session before merge; independently re-verified post-cherry-pick: `go build ./...` clean, `go test ./internal/engines/allocation/... ./internal/engines/steadystate/...` pass, verification grep (`saturationNamedEntry`/`.AnalyzerResults`) zero hits. Full detail in `ledgers/2026-08-29-ct2-resume.md`. |
| CT3a — write engine-side reduce contract | **SKIPPED** | CT3b landed cleanly without a separate contract doc — the simplification was mechanical (loops → direct reads on a single entry); no design ambiguity required upfront documentation. Contract is implicit in the new single-entry signatures and their doc comments. |
| CT3b — simplify 7 single-entry optimizer helpers | **DONE** | Commit `b980f682` on `single-analyzer`. `RolePairedState` collapsed from `[]map[string]float64` to `map[string]float64`; all 7 helpers + `sortVariantsForScaleDown`, `fairShareValue`, `reclaimRole`, `scaleDownRoleIterated`, `allocateForModelPaired`, `RolePickFn` updated to single-entry signatures. Multi-entry (N>1) originals preserved under `internal/engines/allocation/multi_backup/` (`//go:build ignore`) for the planned engine-side reduce step. Pre/post: 252→249 specs, 3 multi-entry test cases moved to backup file (not deleted). |
| CT4 — Score-weighted aggregation simplification | **BLOCKED on user** | Confirmed real bug/naming mismatch: `fairShareValue` equalizes absolute remaining demand across models, not coverage ratio (two 80%-covered models at 10x different scale get ~10x different GPU shares). See `fairshare-value-correctness-investigation-2026-08-25.md` and spec CT4 section. Fix-now vs. document-and-defer decision is the user's to make; not code-verifiable. Explicitly out of scope for the current implementation pass. |
| CT5 — document `RoleCapacities` role-visibility contract | **DONE** | Commit `fcf9c905` on `single-analyzer`. Corrected contract comment added to `initRoleState`. Errata note added to `scale-from-zero-and-fallback-trace-2026-08-25.md` (session-tracking, commit `997c3220`). |

## Immediate next step

PR #34 created at upstream (https://github.com/ev-shindin/llm-scaler/pull/34, draft). Awaiting review. Prior fork-only PR (deanlorenz/llm-scaler #1) closed, pointing to upstream PR. CT4 still blocked on user's fairness-definition decision. Next PR: engine-side reduce (wire non-saturation analyzer results into CompositeSignal).

## Open questions blocking full completion

- CT4's fairness-definition decision — needs the user, not code investigation (see
  `spec-composite-metric-and-optimizer-t2.md` CT4 section and ledger §36).

## Main refresh + rebase (2026-08-30)

`origin/main` was stale (31 commits behind `upstream/main`). Fast-forwarded and pushed:
`origin/main` now matches `upstream/main` at `f0bc1646`. A local `main` branch was created
(`worktrees/Main`, tracking `upstream/main`) since none existed in this repo checkout.

Rebasing `single-analyzer` onto the refreshed `main` hit a modify/delete conflict on 2 files
in the 2026-08-27 migration commit (`a8a1285c`, see provenance note below) — which led to
discovering that migration was **over-broad**: it deleted 20 files from
`docs/plans/analyzers/`, but 3 of them exist on `main`/upstream and are real project design
docs, not internal mission-tracking artifacts:
- `analyzer-architecture-refactor.md`
- `k2-capacity-model.md`
- `kvcachethreshold-retirement.md`

**Corrected 2026-08-30:** all 3 restored to `single-analyzer`'s `docs/plans/analyzers/`
(commit `47213b07`, content byte-identical to `main`'s current version) and their stale
duplicate copies removed from this mission's `session-tracking` directory. The other 17
moved files were checked against `main` too (none exist there) and correctly stay on
`session-tracking` — they are genuinely internal mission tracking (investigation notes,
task briefs, implementation reports, the running ledger, mission-specific specs).

**Rule going forward, per user:** any file that exists on `main`/upstream is not an internal
doc, full stop — check existence on `main`, don't classify by content/tone alone (a doc
sounding like a "design note" isn't sufficient; a doc's presence on `main` is a fact to check
directly with `git cat-file -e main:<path>`, not inferred).

**Completed 2026-08-30:** rebase re-run after the restore; both file conflicts resolved by
keeping `main`'s (already-correct) content. Final tip `c6e408c4`, `main` confirmed an
ancestor of `single-analyzer`. `go build ./...` clean; `go test
./internal/engines/allocation/... ./internal/engines/steadystate/...` all passing — no
regressions from picking up `main`'s 31 new commits. Backup branch
`single-analyzer-pre-rebase-backup` (at the old pre-rebase tip, before commit `47213b07`)
still exists as a rollback point if ever needed.

## Provenance note (2026-08-27 migration)

This mission's docs previously lived at `worktrees/single-analyzer/docs/plans/analyzers/` on
the `single-analyzer` branch itself. Moved here so mission plan/state/ledger content is
tracked on `session-tracking` (origin-only) instead of the feature branch (which may go
upstream). See this mission's `ledgers/` for the session that did the move. **Note:** this
migration was over-broad — see "Main refresh + rebase" above for the 3 files corrected back
out of it on 2026-08-30.

## Session log

- 2026-08-27 session=2026-08-27-ct1b-review status=retired ledger=ledgers/2026-08-27-ct1b-review.md
- 2026-08-29T22:32 session=2026-08-29-ct2-resume status=retired ledger=ledgers/2026-08-29-ct2-resume.md
- 2026-08-30T08:00 session=2026-08-30-ct3-resume status=retired ledger=ledgers/2026-08-30-ct3-resume.md
- 2026-08-30T17:58 session=2026-08-30-ct3-s6 status=retired ledger=ledgers/2026-08-30-ct3-s6.md
- 2026-08-31T00:00 session=2026-08-31-s7 status=active ledger=ledgers/2026-08-31-s7.md
