# single-analyzer-normalize — findings and discussion

Local session tracking for this worktree (`.claude/worktrees/single-analyzer-normalize`,
branch `single-analyzer-normalize`). Created because the PR-prep work grew beyond a clean,
single-PR scope and needs its own persisted record, separate from the `single-analyzer`
mission's own `.session/` (which lives in a different worktree this session cannot write to
directly — see `single-analyzer-4.md` copied alongside this file for that mission's own
ledger up to the point this branch was created).

## Branch provenance

- `single-analyzer-normalize` created off `upstream/main` (`b848b19c`).
- Contains 3 squashed commits cherry-picked from the `single-analyzer` mission branch's CT6
  work (10 original commits → 3 logical commits):
  1. `498c8950` feat: normalize saturation demand to coverage units before optimizer
     (squashes `f20e06f9` + `18f4d4ff`)
  2. `f3c51698` fix: normalize RC/SC/Remaining/Spare/supply alongside PRC
     (squashes `c5af5696` + `290ca75f` + `896879d5` + `e4b1e77d`)
  3. `062331bc` fix: composite signal naming, log completeness, deep-copy safety
     (squashes `65c344af` + `991800ce` + `44a7f5e6` + `e3ce4abc`)
- All 3 commits build clean, pass `go test ./internal/engines/...` and the full non-E2E suite.
- **Not pushed.** Old `pr/single-analyzer` (stale, = merged PR #34) and
  `single-analyzer-pre-rebase-backup` (unrelated leftover) intentionally left untouched.

## Code review (8 finder agents + verification pass)

Ran the `code-review` skill comparing this branch's full diff against `upstream/main`.
Converged, cross-confirmed findings — full detail in the agent transcripts (not repeated here,
this is the durable summary):

1. `greedy_score_optimizer.go` `fairShareValue` — cross-model fair-share now compares
   normalized (coverage-fraction) capacity instead of raw tokens across *different models*
   competing for the same GPU budget. **Not a bug this branch introduced in the formula** —
   `greedy_score_optimizer.go` and `initRoleState` (`analyzer_helpers.go`) are byte-identical
   to upstream. The bug is entirely in the *meaning* of the value normalizeToCompositeUnits
   now produces for a field this pre-existing formula consumes.
2. `wva_required_capacity`/`wva_spare_capacity` Prometheus gauges — still labeled
   `unit=continuous` (tokens) but now emit coverage fractions (0-1). `engine.go` and
   `cost_aware_optimizer.go`'s field-copy are also byte-identical to upstream; the gauge has
   *always* been fed from `CompositeSignal` (pre-CT6 that was literally raw sat tokens since
   sat is the only analyzer) — no metric substitution happened in code, only a silent units
   change on the one gauge that's always existed. The separate, distinct
   `wva_analyzer_demand`/`wva_analyzer_target` family (fed from raw pre-normalization
   `namedResults`, unaffected) already gives sat's own D/P in sat's own units.
3. `rescale.go` `roleDemandGPUs` — reads `RoleCapacities[role].TotalDemand`, which
   `normalizeToCompositeUnits` force-overwrites to `1.0` **unconditionally, even when the
   role's real demand is `<=0`**. `roleDemandGPUs` itself is unchanged and not the bug —
   dividing demand by PRC is correct and always has been. The bug is that `TotalDemand` no
   longer honestly reports `0` for a zero-demand role.

Also reported (lower severity, not yet actioned): idle-model Utilization miscompute (log-only),
dead `roleSpare` log field, unread `SatRoleDemand` field (YAGNI), duplicate sort logic vs
`rolesOf`, package-name mismatch in an ignored test file.

## User corrections to the review framing (2026-09-07, this session)

The initial framing of findings 1-3 (both in the finder-agent reports and in my own follow-up
investigation) was corrected by the user on all three points. Recorded verbatim in substance
because these are decisions, not just clarifications:

### Point 1 — fair-share comparison units

**User's correction:** "Required and remaining should both be in GPU units OR both be in
coverage units — in both cases the unit should be discarded by the division
(RequiredCapacity/Remaining). The best option to compare *different* models under same GPU is
to compare in GPU units — this is known for all models and is comparable."

**What this means concretely:** `fairShareValue` currently consumes `ps[role]` (from
`initRoleState`), a capacity-unit quantity whose meaning is ambiguous under normalization
(tokens before this branch, coverage fraction after). The user's direction: convert to a
GPU-count quantity before comparing across models — the same computation
`rescale.go`'s `roleDemandGPUs`/`modelDemandGPUs` already does
(`ceil(demand/PRC) × GPUsPerReplica`), because that division's result is comparable across
models regardless of whether the numerator/denominator are in tokens or coverage-fraction
terms (the unit cancels either way — same insight as the user's framing).

**Status:** direction agreed in principle; NOT a units-passthrough fix — this is a real
semantic change to `fairShareValue`'s contract (what it reads, not just what units it's in).
Scope size (does this belong in the same PR, given CT4 is separately blocked on the user's
fairness-definition decision) — NOT YET DECIDED. Awaiting continued discussion, explicitly
paused by the user before further code changes.

### Point 2 — zero-demand must flow through unchanged (a already-decided spec point I initially missed)

**User's correction:** "if demand is zero then total demand should stay zero. normalized PRC
is meaningless. My point was that this was ALREADY DISCUSSED before. Somehow you lost the
discussion and the spec ignores it. The current implementation is simply WRONG."

**Verified in spec.md (lines 657-661, already committed, not new):**
> **Special cases.**
> - `demand = 0` (for a role): `ceil(0 / anything) = 0` — no replicas needed. Per-role
>   coverage is not meaningful when demand is zero; `demand=0` must flow through to the
>   composite **unchanged** (the optimizer already handles it correctly via the
>   `demand <= 0 → util = 1.0` path in `allocateForModelPaired`).

This settles it: the spec already decided `demand=0` must pass through unchanged (stay `0`),
not get overwritten to `1.0`. The current code
(`engine_v2.go` `normalizeToCompositeUnits`, both the per-role loop around line 1174-1184 and
the model-level line 1188) sets `TotalDemand = 1.0` **unconditionally**, contradicting this
already-decided spec text directly. This is a confirmed implementation bug, not a design
ambiguity, not something newly discovered in this session — the decision predates this PR's
implementation and the implementation simply didn't follow it.

`roleDemandGPUs` in `rescale.go` was correctly identified by the user as NOT the bug — dividing
demand by PRC is fine, always has been, and normalizing PRC before this division is
"meaningless" per the user (i.e., the fix belongs entirely upstream, in
`normalizeToCompositeUnits`, not in any consumer). Fix direction: guard `TotalDemand = 1.0`
(both call sites) behind the same `demand > 0` check that already guards the other fields in
the same loop.

**Exact planned diff (`internal/engines/steadystate/engine_v2.go`, `normalizeToCompositeUnits`),
approved by user before implementation — three sites, not two (a third, `RoleDemand[role] =
1.0` at the model level, was found while pinning down the exact lines; same bug shape,
previously folded into "the model-level line 1188" without being called out separately):**

1. **Per-role `RoleCapacities[role].TotalDemand`** (currently lines 1174-1184) — move
   `rc.TotalDemand = 1.0` inside the existing `if demand > 0` guard:
   ```go
   for role, rc := range nr.RoleCapacities {
       demand := demandForRole(role)
       if demand > 0 {
           rc.RequiredCapacity /= demand
           rc.SpareCapacity /= demand
           rc.TotalSupply /= demand
           rc.TotalAnticipatedSupply /= demand
           rc.TotalDemand = 1.0          // moved inside the guard (was unconditional)
       }
       nr.RoleCapacities[role] = rc
   }
   ```
2. **Model-level `Result.TotalDemand`** (currently line 1188) — guard with `modelDemand > 0`
   (the same variable already computed and used by the model-level RC/SC/supply block above
   it):
   ```go
   if modelDemand > 0 {
       nr.Result.TotalDemand = 1.0
   }
   ```
3. **Model-level per-role `Result.RoleDemand[role]`** (currently lines 1189-1191) — guard each
   entry by its own value before overwriting:
   ```go
   for role, d := range nr.Result.RoleDemand {
       if d > 0 {
           nr.Result.RoleDemand[role] = 1.0
       }
   }
   ```

**Verified safe for every downstream reader** (grepped all `.TotalDemand`/`.RoleDemand[`
consumers in `internal/engines/`): no consumer treats `0` as a missing/sentinel value —
`roleDemandGPUs` (`rescale.go`), `RecordAnalyzerDemand`, the RC/SC formulas
(`v := rc.TotalDemand/scaleUp - ...`), and the Utilization recompute (below) all already treat
`demand<=0` as an ordinary, correct zero. No other file reads `Result.RoleDemand[role]` outside
this function itself.

**Side effect (a genuine bonus, not scope creep):** the Utilization recompute at line
1197-1198 (`nr.Utilization = nr.Result.TotalDemand / nr.TotalSupply`) currently produces a
small spurious nonzero value for an idle model (`1.0/raw_TotalSupply`) — one of the review's
5 lower-severity findings ("idle-model Utilization miscompute"). With `TotalDemand` correctly
staying `0` per the fix above, this recompute naturally yields `0/TotalSupply = 0`, the correct
value — fixed for free by the same guard, no extra code needed.

**Status:** diagnosis confirmed and settled; exact diff written above and shown to user for
review before implementation, per user's explicit request ("show me all expected changes +
update your PR spec" before fixing). NOT YET IMPLEMENTED — awaiting go-ahead to write the code.

### Point 3 — SatDemand/SatRoleDemand is not designed for the metrics use case

**User's correction:** "'that's exactly what SatDemand/SatRoleDemand exist for' — no, that is
NOT why they exist. This is a *temporary* fix to keep the gauge unchanged."

The doc comment on `SatDemand` (`optimizer_interfaces.go:74-80`) states its one documented
purpose: used by `rescaleInputsForGroup` as the rescale weight. Reusing it for the
metrics-unit-correction fix (multiplying `RequiredCapacity`/`SpareCapacity` back to token scale
before publishing) is convenient but not what the field exists for — the user is flagging this
as exactly the kind of hidden-coupling shortcut that caused problems in this branch already.

**Status:** an attempted implementation of the metrics fix (multiply reqCap/spareCap by
SatDemand/SatRoleDemand) was written, found to break 4 existing tests (because
`satEntryFixture.named()`, the test helper, hands the optimizer already-final RC/SC values
without ever setting SatDemand/SatRoleDemand — they default to 0, so multiplying zeroed out
otherwise-correct raw fixture values), and was **reverted** at the user's explicit instruction
before any further discussion. Not committed. Two questions were raised but not yet answered
by the user:
(a) is reusing SatDemand/SatRoleDemand for this fine as an explicitly-labeled temporary
    stopgap (with the doc comment updated to note the second use), or
(b) should this be a separate, explicit mechanism instead, even if temporary, to avoid
    conflating "rescale weight" and "metrics unit correction" on one field?
User has not yet answered (a) vs (b) — paused to discuss 1 and 3 together first.

## Current code state

- Working tree is clean (the attempted point-3 fix was reverted via
  `git checkout -- internal/engines/allocation/cost_aware_optimizer.go`, user-confirmed).
- **Rebased onto the updated `upstream/main`** (`b848b19c` → `185b3eb8`) — one conflict in
  `docs/reference/cycle-log.md` (upstream renamed it from `docs/developer-guide/cycle-log.md`
  during a docs restructure), resolved by merging upstream's corrected link with our commit's
  composite-signal doc additions. All 4 commits rebuilt clean, full engine test suite passes.
  New SHAs post-rebase: `6ae2eb47` (feat), `cb723833` (fix RC/SC/etc), `da0e1ee8` (fix naming/
  logging/deep-copy), `97441e3e` (local `.session` docs commit).
- **Pushed to `origin`** (`deanlorenz/llm-scaler`, branch `single-analyzer-normalize`,
  user-confirmed) — new branch, no PR opened yet.
- No fixes for findings 1, 2, or 3 have been implemented or committed yet. Point 2's exact
  diff is now written out above, shown to the user, awaiting go-ahead to implement.
- **This PR can no longer be "100% clean"** per the user's own assessment — the scope has grown
  from "rebase CT6 onto upstream/main" to "rebase CT6 + resolve at least the zero-demand
  regression (point 2) it depends on, and decide how deep to go on points 1 and 3." This file
  and the local `.session/` exist specifically to track that expanded, messier scope without
  losing anything, since it can no longer live cleanly in the original mission's `.session/`
  tracking (different worktree, not reachable from this sandboxed session).

## Open items / next steps (not yet done)

1. Implement point 2's fix (diff above) once user gives go-ahead — diagnosis and exact diff
   are both settled; nothing left to decide, only to write and test.
2. Continue discussing points 1 and 3 (paused mid-discussion for the upstream rebase + push).
3. Decide fix for point 1 (fair-share GPU-unit comparison) — direction agreed, implementation
   not started, scope/PR-boundary not decided.
4. Decide fix for point 3 (metrics) — mechanism not yet decided (reuse SatDemand vs. new
   explicit path).
5. Re-verify all 3 squashed commits still build/test clean after any of the above fixes land.
6. Decide final PR scope/boundary given this is no longer a clean 1:1 rebase of the original
   CT6 work — may need its own PR-history note distinguishing "rebased CT6" from "CT6 + these
   additional fixes."
7. Nothing has been pushed. No push should happen until user explicitly authorizes it, per
   standing convention.
