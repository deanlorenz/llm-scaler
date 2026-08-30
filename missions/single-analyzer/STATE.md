# Mission state — single-analyzer

**Last updated:** 2026-08-29. This doc is overwritten on each update, not append-only — it
reflects current status only. For global process rules see `../../CONVENTIONS.md`. For the
full task plan see `spec-composite-metric-and-optimizer-t2.md`. For the chronological
reasoning trail (why decisions were made) see `ledger-analyzer-optimizer-refactor.md` —
reference only, not needed to resume. Per-session ledgers are in `ledgers/`.

## Worktrees used for this mission

- `worktrees/single-analyzer` (branch `single-analyzer`) — the feature-work worktree where
  code changes for this mission actually land.
- `worktrees/session-tracking` (branch `session-tracking`, this worktree) — mission plan/state
  and session ledgers, never pushed to `upstream`, only `origin`.
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
| T1 — compose analyzer results (engine-side) | **DONE** | Commit `f5283e2a` on `single-analyzer`. `composeAnalyzerResults` added to `engine_v2.go`; passthrough for the sat-only case. Six pre-existing tests asserting old multi-analyzer forwarding are `t.Skip()`-ed, each with a reason citing this change — pending a redesign, not a bug. |
| CT1a — nil-guard `rescaleModelDecisions` | **DONE** | Commit `8906ef7b` on `single-analyzer`. Report: `ct1a-implementation-report-2026-08-26.md`. |
| CT1b — engine-side guard on nil saturation result | **DONE** | Commit `122d1699` on `single-analyzer` (cherry-picked from coder's `75b57b2c`). Report: `ct1b-implementation-report-2026-08-26.md` (note: that report's recorded hash `71c401c2` is stale/cosmetic — self-referential amend artifact; true hash is `122d1699`). |
| CT2 — collapse `AnalyzerResults []NamedAnalyzerResult` to single `CompositeSignal` field | IN PROGRESS | Task spec: `task-ct2-2026-08-29.md`. Two coder-agent dispatch attempts failed at verification: `isolation: "worktree"` lands its fresh worktree on a fixed commit (`233f20c1`, an ancestor of `feat/wva-external-scaler`, pre-T1) regardless of the parent session's pinned worktree/branch — not a bug specific to this mission, just how that isolation mode resolves its base here. CT1b's earlier successful coder run started from the same commit and worked around the gap itself (recreated a missing doc file); CT2 can't do the analogous workaround because its entire premise requires T1's code to exist. Fix: instruct the next coder to `git checkout a8a1285c` (single-analyzer's tip) as its literal first step, before any CT2 edits. No code changes made yet. See `ledgers/2026-08-29-ct2-resume.md`. |
| CT3a/CT3b — design + apply engine-side reduce, simplify 7 single-entry helpers | NOT STARTED | CT3a depends on CT2; CT3b depends on CT3a being reviewed. |
| CT4 — Score-weighted aggregation simplification | **BLOCKED on user** | Confirmed real bug/naming mismatch: `fairShareValue` equalizes absolute remaining demand across models, not coverage ratio (two 80%-covered models at 10x different scale get ~10x different GPU shares). See `fairshare-value-correctness-investigation-2026-08-25.md` and spec CT4 section. Fix-now vs. document-and-defer decision is the user's to make; not code-verifiable. Explicitly out of scope for the current implementation pass. |
| CT5 — document `RoleCapacities` role-visibility contract | NOT STARTED | Independent of CT1–CT4, can land any time. |

## Immediate next step

`single-analyzer` needs rebasing onto refreshed `main` before CT2 resumes (see "Main
refresh + rebase" below) — the rebase is in progress/paused for a migration-scope audit, not
yet complete. Once `single-analyzer` is rebased cleanly, re-dispatch CT2 to a coder agent
whose first instruction is `git checkout <single-analyzer's new tip after rebase>` (task
spec ready at `task-ct2-2026-08-29.md`; the earlier blocker — `isolation:"worktree"` starting
from a pre-T1 commit — is understood, see the CT2 row above).

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

The `single-analyzer` rebase itself was aborted (not yet re-attempted) once this scope issue
surfaced — needs to be re-run now that the 3 files are restored, and should no longer
conflict on them (only the other, cleanly-deleted files' rebase behavior needs reconfirming).
Backup branch `single-analyzer-pre-rebase-backup` (at the pre-rebase tip, before commit
`47213b07`) was created before rebasing and still exists — the tip has since moved past it
so it may need recreating from the new tip if it's needed as a rollback point again.

## Provenance note (2026-08-27 migration)

This mission's docs previously lived at `worktrees/single-analyzer/docs/plans/analyzers/` on
the `single-analyzer` branch itself. Moved here so mission plan/state/ledger content is
tracked on `session-tracking` (origin-only) instead of the feature branch (which may go
upstream). See this mission's `ledgers/` for the session that did the move. **Note:** this
migration was over-broad — see "Main refresh + rebase" above for the 3 files corrected back
out of it on 2026-08-30.

## Session log

- 2026-08-27 session=2026-08-27-ct1b-review status=retired ledger=ledgers/2026-08-27-ct1b-review.md
- 2026-08-29T22:32 session=2026-08-29-ct2-resume status=active ledger=ledgers/2026-08-29-ct2-resume.md
