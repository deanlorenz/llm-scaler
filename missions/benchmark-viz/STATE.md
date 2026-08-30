# Mission: benchmark-viz

Visualization tooling for llm-d WVA benchmark runs.
Worktree: `worktrees/benchmark-viz` (branch: `benchmark-viz`).

## Goal

Build and maintain a 7-panel PNG renderer (`render_real_trace.py`) and HTML report
generator (`report.py`) for llm-d WVA benchmark bundle directories.
Scope: viz only — no benchmark running, no extraction.

## Current status

All 7 panels rendering correctly on both v0.3 and v0.4.0 bundles.
Production code complete and committed on `benchmark-viz` branch.

## Immediate next step

Cherry-pick production code into `benchmark-runtools` branch.
Commits (in order): `df51c50b c146f6e1 71049fd7 2ce53cc0 00a4b292 fd1110f0 13d8ff81 c8dbe44c fad51c0b`
Files: `hack/benchmark/render_real_trace.py` and `hack/benchmark/report.py` only.
Do NOT cherry-pick artifact or docs commits.

## Open items

1. Get bundle with `scaling_log.by_analyzer` data to exercise p6 with real data
2. 1a TTFT fallback — decision pending
3. bench-* Makefile targets (deferred pending runtools merge)
4. Cumulative/comparison reports (deferred)

## Key files (worktrees/benchmark-viz branch)

- `hack/benchmark/render_real_trace.py` — 7-panel PNG renderer (production)
- `hack/benchmark/report.py` — HTML report generator (production)
- `hack/benchmark/results/` — sample bundles (8 v0.3 + 5 v0.4.0)
- `docs/plans/benchmark-viz/session.md` — living session doc / work items
- `docs/plans/benchmark-viz/input-contract.md` — extractor output spec

## Input contract

Bundles are directories with these files (all optional except meta.json):
`meta.json`, `endpoints.json`, `scaled_objects.json`, `pods.json`,
`requests.json`, `derived.json`, `coverage.json`

## Session log

- 2026-08-31 session=bench-viz5 status=active ledger=missions/benchmark-viz/ledgers/bench-viz5.md
