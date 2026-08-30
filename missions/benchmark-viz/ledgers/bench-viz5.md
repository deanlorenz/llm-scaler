# Session ledger — bench-viz5
Date: 2026-08-31
Worktree: worktrees/benchmark-viz (branch: benchmark-viz)
Mission: benchmark-viz

## Work done this session

### p6 axis — symlog (final)

Decision: use matplotlib `symlog(linthresh=1, base=2)` with plain integer formatter.
User clarified: replica deltas are integers, no values between 0 and 1 in practice, so
the linear zone is never hit. symlog gives visually even log-2 spacing (1,2,4,8...) on
each half with correct +/- labels. No manual transform needed.

Previous approach (`signed_log2` + `FixedLocator` + `FuncFormatter`) was correct in
principle but the data line had been changed to raw values in the prior session, leaving
data and tick positions mismatched. Resolved by switching fully to symlog rather than
reverting — cleaner and simpler.

False start: briefly re-applied `signed_log2` to data to match the old tick logic before
user confirmed symlog was the right direction. Reverted immediately.

Removed: `signed_log2`, `inv_signed_log2`, `FixedLocator`, `FuncFormatter`,
`AutoMinorLocator`, `offset_copy`, `math` import — 42 lines net deleted.

Commits: `c8dbe44c` (symlog switch), `fad51c0b` (base=2, integer labels).

### All 13 bundles rendered and committed

Rendered all bundles in `hack/benchmark/results/` — 8 older + 5 v040. All clean.
Committed rendered `panels.png` files at `7e6364ad`.

Notable: only `dean-2026081*` runs have controller-log data for p6 (saturation +
throughput analyzer lines with reason-code markers). All other runs show p6 empty
("no scaling-decision data").

### Cherry-pick analysis for benchmark-runtools merge

Identified 9 production-code commits to cherry-pick into benchmark-runtools:
`df51c50b c146f6e1 71049fd7 2ce53cc0 00a4b292 fd1110f0 13d8ff81 c8dbe44c fad51c0b`

Artifacts (`hack/benchmark/results/`) and docs (`docs/plans/benchmark-viz/`) stay in
this worktree only — not for cherry-pick.

### Session doc updated

`docs/plans/benchmark-viz/session.md` updated at `d68f1ee0` — marked p2/p6/v040 resolved,
updated open items list.

## Open items (carried forward)

1. Get a bundle with `scaling_log.by_analyzer` data to exercise p6 with real values
2. 1a TTFT fallback — decision pending
3. Publishing / Makefile bench-* targets (deferred pending runtools merge)
4. Cumulative/comparison reports (deferred)
5. Merge production code into benchmark-runtools (cherry-pick list above)
