# PR spec — #34: single CompositeSignal at the engine→optimizer boundary

**Status: MERGED 2026-09-02.** https://github.com/ev-shindin/llm-scaler/pull/34, merge commit
`347de1a9`, base `main`, head `deanlorenz:pr/single-analyzer`.

This is a **verified-from-source** spec, written by reading the actual merged PR (`gh pr view
34` / `gh pr diff 34` / file list), not by trusting the mission spec's per-task commit
bookkeeping. The two are not the same shape — see "Squash note" below.

---

## Scope — exactly what's in this PR

Verified against `gh pr diff 34`'s actual content, file-by-file:

| Mission task | In this PR? | Evidence |
|---|---|---|
| CT1a — nil-guard `rescaleModelDecisions` | **Yes** | commit `1a0c23be` in the PR, matches mission commit `8906ef7b`'s intent |
| CT2 — `AnalyzerResults []NamedAnalyzerResult` → `CompositeSignal NamedAnalyzerResult` | **Yes** | commit `113fec1d`; `ModelScalingRequest` field change present in diff |
| CT3b — simplify 7 optimizer helpers to single-entry signatures | **Yes** | `analyzer_helpers.go`, `cost_aware_optimizer.go`, `greedy_score_optimizer.go` all in the PR's file list with the slice→single-entry signature change; `multi_backup/` preserves the originals |
| CT5 — `initRoleState` role-visibility contract doc comment | **Yes** | the exact doc comment text ("Role-visibility contract", `estimateSchedulerQueueDemand` reference, mixed-P/D+"both" deferral) is in the diff at `initRoleState` |
| CT1b — engine-side guard on nil saturation result | **No** | no `baseResult == nil` / sentinel-error text anywhere in the diff. Excluded by user request — separate bugfix, deferred to a future PR. |
| CT6 — normalize sat→composite to coverage units | **No** | no `SatDemand` / `normalizeToCompositeUnits` anywhere in the diff. Not started yet when this PR was cut. |
| s7's stale `named()`/`withScore()`/`makeNamed()` test-arg bug | **N/A — never existed here** | the PR-prep branch already had the correct zero-arg form; the stale-args bug was introduced only on the `single-analyzer` mission branch by an incomplete lint fix, and only ever needed fixing there (mission commit `b067642a`). Not a PR #34 concern at all. |

**Net: PR #34 = CT1a + CT2 + CT3b + CT5.** Nothing else, nothing less.

## Squash note — why this doesn't match the mission branch's commit list

The `single-analyzer` mission branch keeps CT1a/CT2/CT3b/CT5 as 4 separate commits
(`8906ef7b`, `e4106109`, `b980f682`, `fcf9c905`), one per task, per
`conventions/coder-orchestration.md` rule 4 ("each task lands as its own commit"). The PR
branch (`pr/single-analyzer`, prepared independently at
`/home/dean/code/llm-d/worktrees/pr-single-analyzer`) condensed this into **2** commits:
`1a0c23be` (CT1a alone) and `113fec1d`, whose message covers CT2+CT3b+CT5's combined result.
This is a re-implementation of the same end state by a separate coder pass, not a literal
cherry-pick — confirmed by the stale-args bug existing on one branch and not the other despite
both nominally containing "the same" CT3b work.

**Practical implication:** don't use the mission branch's commit SHAs to reconstruct "what's
already upstream." Check the actual PR/merge content instead — as this document does.

---

## What changed — engine side

*(verbatim from `.session/gist_engine.md`, the gist that accompanied this PR's description)*

The only field on `ModelScalingRequest` that changes:

```diff
 type ModelScalingRequest struct {
     ModelID   string
     Namespace string
-    AnalyzerResults []NamedAnalyzerResult  // per-analyzer slice; sat is always first
+    CompositeSignal  NamedAnalyzerResult   // sat entry, handed directly to the optimizer
     VariantStates   []domain.VariantReplicaState
     ...
 }
```

`runAnalyzersAndScore` is **unchanged** by this PR — it still builds and returns
`[]NamedAnalyzerResult` for the full set of enabled analyzers. `collectV2ModelRequest` passes
`namedResults[0]` (sat, always first by construction) as `CompositeSignal`. All other entries
are consumed engine-internally (liveness, metrics, logging) before this point — nothing is
discarded early.

`hasSaturationResult` (engine-side GPU-quota guard) changes from a linear search by name to a
direct field read:

```diff
 func hasSaturationResult(req allocation.ModelScalingRequest) bool {
-    for _, e := range req.AnalyzerResults {
-        if e.Name == domain.SaturationAnalyzerName {
-            return e.Result != nil
-        }
-    }
-    return false
+    return req.CompositeSignal.Name == domain.SaturationAnalyzerName &&
+           req.CompositeSignal.Result != nil
 }
```

**Sat-only equivalence:** when only saturation is enabled (today's default), `namedResults` has
one entry, so `CompositeSignal` = sat's `NamedAnalyzerResult`, identical to before.

**What did NOT change (engine side):** `runAnalyzersAndScore`'s return type/body/callers,
`updateLivenessAndSetLive`, `detectDemandLiveness`, `recordAnalyzerMetrics`,
`runV2AnalysisOnly`, `runRegisteredAnalyzer`, `buildNamedResult`, the scale-from-zero engine
(entirely separate, not touched).

---

## What changed — optimizer side

*(verbatim from `.session/gist_optimizer.md`, the companion gist)*

**The optimizer is name-blind:** zero references to `SaturationAnalyzerName`, `AnalyzerResults`,
or `.Name ==` remain in any non-test, non-backup production optimizer file.

**Contract boundary** — every reader that did `saturationNamedEntry(req.AnalyzerResults)` (a
name-search loop) now reads `req.CompositeSignal` directly; `saturationNamedEntry` is deleted.

**`RolePairedState` collapses:**
```diff
-type RolePairedState []map[string]float64   // indexed [analyzerIndex][role] → demand
+type RolePairedState map[string]float64     // indexed [role] → demand
```

**All 7 helpers drop their slice/loop and take a single entry, arithmetic unchanged for the
N=1 case** (each proven exact in the mission spec's CT3 section):
`initRoleState`, `needsScaleDownForRole`, `safeRemovalReplicasForRole`,
`applyDeallocationForRole`, `applyAllocation`, `roleBottleneckReplicas`, `roleAggRemaining`,
`sortVariantsForScaleDown`, plus `fairShareValue` and `allocateForModelPaired`/
`scaleDownRoleIterated`'s call sites. `initRoleState` also gains CT5's role-visibility contract
doc comment (no behavior change).

**Per-function equivalence tables** (old formula with N=1 vs. new formula — every one reduces
to the identical value) are in `.session/gist_optimizer.md` directly; not repeated here to
avoid drift between two copies of the same table.

**Multi-entry originals preserved, not in this PR:** `internal/engines/allocation/multi_backup/`
(`//go:build ignore`) — `analyzer_helpers_multi.go`, `analyzer_helpers_multi_test.go` (5
multi-entry test cases to restore engine-side), `cost_aware_optimizer_multi.go`. This is where
CT7 (the engine-side reduce, still unstarted) will draw from.

---

## Explicitly not in this PR

- CT1b (engine-side nil-saturation guard) — deferred, separate bugfix, future PR.
- CT6 (coverage-unit normalization) — not started at the time this PR was cut; see the next
  PR's spec (`.session/pr-spec-next-coverage-units.md`).
- CT4 (fairness-definition fix) — blocked on a user decision, out of scope entirely for now.
- CT7 (engine-side reduce for N>1 analyzers) — not started, blocked on 4 open design questions.
- Anything in `multi_backup/` — preserved for future reference, explicitly `//go:build ignore`.

## Verification performed for this document

- `gh pr view 34 --repo ev-shindin/llm-scaler --json ...state,mergedAt,mergeCommit,files,commits`
- `gh pr diff 34 --repo ev-shindin/llm-scaler` — full diff grepped for CT5's doc-comment text,
  CT6's `SatDemand`/`normalizeToCompositeUnits`, and CT1b's sentinel-error text, to confirm
  presence/absence rather than assume from commit messages alone.
