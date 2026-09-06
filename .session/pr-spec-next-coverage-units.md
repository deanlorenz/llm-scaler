# PR spec — next: coverage-unit normalization at the composite boundary (CT6)

**Status: NOT YET OPENED. No PR branch cut yet.** Code is done and pushed to
`origin/single-analyzer` (deanlorenz/llm-scaler fork, tip `d90bd565`) — minus one blocking gap
(see below).

---

## Scope — exactly what goes in this PR

**CT6 only.** Verified: CT3b and CT5, which the mission spec's own bookkeeping made look like
still-pending PR payload, are **already merged** in PR #34 (see
`.session/pr-spec-34-composite-signal.md`) — they are not part of this PR's diff. This PR's
base is upstream `main` post-PR-#34, i.e. it only needs to add CT6's own change on top of
what's already there.

| Item | In this PR? |
|---|---|
| CT6 — `SatDemand` field, `normalizeToCompositeUnits`, `rescaleInputsForGroup` weight change, 7 new unit tests | **Yes** — commit `f20e06f9` on the mission branch |
| CT6 test-fix — 6 test files' call sites updated for `runAnalyzersAndScore`'s slice return type; `engine_v2_compose_test.go` moved to `multi_backup/` | **Yes — but not yet committed anywhere.** This is a **blocking prerequisite**, not optional polish: without it, this PR's code does not compile. See "Blocking gap" below. |
| CT1b, CT4, CT7 | **No** — explicitly excluded, see "Explicitly not in this PR" |

## What CT6 actually does

**Intent.** `collectV2ModelRequest` used to pass saturation's raw result straight through as
`CompositeSignal`, in saturation's own units (tokens for KV-cache saturation). CT6 converts it
into a unit-less coverage signal before it leaves the engine, so the optimizer's math is
already in coverage units. Sat-only behavior (today's only production case) is numerically
identical before and after.

**The conversion**, for each `(variant, role)`:
```
C(SO) = PRC(A, SO) / D(A, M(SO), R(SO))          per-replica coverage fraction, (0, 1]
```
The composite then carries `TotalDemand = 1.0`, `RoleDemand[role] = 1.0` for every role, and
`VariantCapacity.PerReplicaCapacity = C(SO)`. This preserves the optimizer's
`ceil(demand / PRC)` replica-count math exactly: `ceil(1.0 / C(SO)) = N_full(SO)`.

**Rescale-weight fix bundled in:** because `TotalDemand` becomes `1.0` for every model after
normalization, the rescale water-fill's weight (which used raw `TotalDemand` in tokens) would
collapse to priority-only — wrong. CT6 adds `SatDemand float64` on `NamedAnalyzerResult`,
capturing the pre-normalization token demand, and `rescaleInputsForGroup` reads that instead of
`Result.TotalDemand`. Numerically identical to pre-CT6 behavior.

**Concrete changes** (`.session/ct6-implementation-report.md`, full detail):
1. `SatDemand float64` added to `NamedAnalyzerResult` (`optimizer_interfaces.go`).
2. `normalizeToCompositeUnits` written in `engine_v2.go`, called from `collectV2ModelRequest`
   after `buildCapacities`/`buildRoleCapacities` have already run (RC/SC computed from raw
   tokens first; only the composite copy handed to the optimizer gets normalized).
3. `rescaleInputsForGroup` (`rescale.go`) reads `SatDemand` instead of `Result.TotalDemand`.
4. `satEntryFixture.named()` test helper updated to set `SatDemand: f.TotalDemand` — needed
   because 16 pre-existing rescale tests construct `NamedAnalyzerResult` directly, bypassing
   `normalizeToCompositeUnits`.
5. 7 new Ginkgo specs in `engine_v2_normalize_test.go`: non-disaggregated, disaggregated
   per-role, `demand=0`, `PRC=0`, nil `Result` (no-panic), `RoleCapacities.TotalDemand`
   normalization, `SatDemand` capture.

**Full semantic framework and the coverage-normalization derivation** are in `.session/spec.md`
CT6 section — not repeated here; that section is now correctly marked DONE with commit
`f20e06f9`.

## Blocking gap — must be fixed before this PR opens

Commit `f20e06f9` changed `runAnalyzersAndScore`'s return type from
`allocation.NamedAnalyzerResult` to `[]allocation.NamedAnalyzerResult` and deleted
`composeAnalyzerResults`/`rawAnalyzerResult` (vacuous pass-throughs once the optimizer only
ever consumed saturation) — but never updated 6 test-file call sites that still treat the
return value as a single struct. This does not compile as committed:

```
$ git diff --stat  (uncommitted, working tree only)
 internal/engines/allocation/cost_aware_optimizer_test.go        |  8 +---
 internal/engines/steadystate/engine_external_registry_test.go   |  4 +--
 internal/engines/steadystate/engine_v2_demand_liveness_test.go  | 18 +++---
 internal/engines/steadystate/engine_v2_liveness_test.go         | 32 +++++-----
 internal/engines/steadystate/engine_v2_population_test.go       | 16 +++---
 internal/engines/steadystate/engine_v2_test.go                  | 13 ++---
 6 files changed, 45 insertions(+), 46 deletions(-)
```

Plus one file move: `engine_v2_compose_test.go` (tested the now-deleted
`composeAnalyzerResults`) → `internal/engines/allocation/multi_backup/engine_v2_compose_test.go`
(`//go:build ignore`), matching the existing pre-CT3b-originals pattern.

**Verified:** `go build ./...` and `go vet ./internal/engines/...` both pass cleanly with these
changes applied; both fail without them. This fix has existed only as uncommitted working-tree
changes since 2026-08-31 — `origin/single-analyzer` (tip `d90bd565`) carries `f20e06f9` without
it, so **origin's HEAD does not build either.**

Its original coder's worktree/branch (if one existed) could not be located: checked every
`.claude/worktrees/*` entry, every `worktree-*` tracking branch, and 73 dangling commits via
`git fsck --unreachable` — none matched. `conventions/coder-orchestration.md` rule 5 (record
the coder's worktree+branch in STATE.md) wasn't followed for this dispatch. The working-tree
copy may be the only surviving trace.

**Action needed before this PR can be prepared:** commit this fix on the mission branch (its
own commit, per the one-task-one-commit convention), then push to `origin/single-analyzer`.

## Correctness bug — found 2026-09-06, blocks this PR alongside the compile-fix gap

`normalizeToCompositeUnits` converts `PerReplicaCapacity`/`TotalDemand`/`RoleDemand` to
coverage-fraction units, but never touches `RequiredCapacity`, `SpareCapacity`, `Remaining`,
`Spare`, or `RoleCapacities[role].RequiredCapacity`/`.SpareCapacity` — those are computed by
`applyUniversalThreshold` *before* normalization runs, from raw demand, and never revisited.
`initRoleState` seeds the optimizer's `pickerState`/`Remaining` from those still-raw fields,
then divides them against the now-fractional `PerReplicaCapacity` — producing replica counts
wrong by roughly `1/PRC_fraction` for any model with real, nonzero demand. Full trace,
including why no existing test catches it, is in `.session/spec.md`'s CT6 section
("Correctness bug found 2026-09-06").

### Candidate fixes — a real tradeoff, not yet decided

**Option A — divide `RequiredCapacity`/`SpareCapacity`/`Remaining`/`Spare`/per-role
equivalents by the same `demand` already used for the `PerReplicaCapacity` division, inside
`normalizeToCompositeUnits`.** Mathematically sound (verified: `ceil((RC_raw/D) /
(PRC_raw/D)) == ceil(RC_raw/PRC_raw)`) and minimal — same function, same place, extends the
existing pattern. **But:** `cost_aware_optimizer.go:303-311` reads these exact fields
(`satNamed.RequiredCapacity`/`.SpareCapacity`/`.RoleCapacities[role]`) directly into
`decision.RequiredCapacity`/`.SpareCapacity` — the source of the `wva_required_capacity`/
`wva_spare_capacity` observability gauges. This fix would turn those gauges from meaningful
token/capacity counts into unit-less coverage fractions (e.g. `0.43` instead of `3411.76`) —
correct internally, but a real behavior change to what operators see on dashboards.

**Option B — add coverage-space fields alongside the existing raw ones** (e.g.
`RequiredCapacityCoverage`/`SpareCapacityCoverage` on `NamedAnalyzerResult`, and a per-role
equivalent), leaving `RequiredCapacity`/`SpareCapacity`/`Remaining`/`Spare` untouched for
metrics compatibility. Update `initRoleState` (the *only* place that reads these fields
directly into `pickerState`/`Remaining` — everything downstream operates on `pickerState`/
`RoleSpare`, not the struct fields again) to read the new coverage fields instead. Touches
`optimizer_interfaces.go` (new field), `internal/domain/analyzer.go` (new field on
`RoleCapacity`), `engine_v2.go` (`normalizeToCompositeUnits` populates the new fields), and
`analyzer_helpers.go` (`initRoleState`'s read). Keeps metrics semantics unchanged; more
surgical about scope but touches 4 files instead of 1.

**Option C — reorder the pipeline**: run the coverage conversion on the raw `Result` (before
`buildNamedResult`/`buildCapacities` ever runs on the composite candidate), so
`TotalSupply`/`TotalAnticipatedSupply`/`RequiredCapacity`/`SpareCapacity` all get computed
*once*, already consistent, from normalized inputs. Cleanest conceptually — no duplicate
computation — but changes `wva_required_capacity`/`wva_spare_capacity`/
`wva_saturation_utilization` the same way Option A does (they'd end up in coverage-fraction
units too), and is a bigger structural change to `runAnalyzersAndScore`'s already-fragile loop
(the same loop the compile-fix gap above is patching).

**Not yet decided which to take.** Needs the user's call on whether the observability-metrics
behavior change (Option A/C) is acceptable, or whether the extra-field approach (Option B) is
worth the wider file touch to preserve it.

## Explicitly not in this PR

- **CT1b** — engine-side nil-saturation guard. Deferred by user request since PR #34; still
  deferred here. Its own future PR.
- **CT4** — `fairShareValue` fairness-definition fix. Blocked on a user decision (fix-now vs.
  document-and-defer). Out of scope until that decision is made.
- **CT7** (engine-side reduce, née "PR #2") — combining N>1 analyzer results into one
  composite. Not started; blocked on 4 open design questions (spec CT7 section). This PR's own
  CT6 work is a prerequisite for CT7 (CT7 builds on `runAnalyzersAndScore`'s current
  slice-returning shape), but CT7 is not bundled into this PR — it needs its own scope decision
  once the open questions are resolved.

## Todo before opening

- [ ] Decide the correctness-bug fix approach (Option A/B/C above) with the user
- [ ] Implement the chosen fix, with a new test that exercises the real
  `collectV2ModelRequest` → optimizer path with nonzero demand end-to-end (closing the test
  gap that let this bug through)
- [ ] Commit the CT6 test-fix (blocking gap above) on the mission branch
- [ ] Push to `origin/single-analyzer`
- [ ] Confirm `go build ./...` and `go test ./internal/engines/...` clean on the pushed tip
- [ ] Cut a fresh PR-prep branch from current upstream `main` (which already has PR #34's
  content) and apply CT6 + the fix on top — do not assume `pr/single-analyzer` (now merged and
  stale for this purpose) is the right base; verify against current `main` first
- [ ] Follow `conventions/pr-branch.md` / `conventions/pr-workflow.md` before opening

## Verification performed for this document

- `.session/spec.md` CT6 section (semantic framework, per-site arithmetic audit).
- `.session/ct6-implementation-report.md` (what actually landed, test results).
- This session's own build/vet verification of the uncommitted fix.
- Cross-checked against `.session/pr-spec-34-composite-signal.md` to confirm CT3b/CT5 are not
  duplicated into this PR's scope.
