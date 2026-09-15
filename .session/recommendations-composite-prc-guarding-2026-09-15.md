# PRC-guarding recommendations — optimizer call sites

Follow-up to `.session/findings-composite-prc-downstream-2026-09-15.md`, against the user's
4 criteria (2026-09-15):
1. Reoccurring calculations should go through the shared helper; add the guard there if needed.
2. SO-level guard checked early — opt out of scaling (except scale-from-zero) for an SO with no
   valid info.
3. In most places, guarding against PRC=0 is enough; no SO-level guard needed.
4. Every call should use analyzer-independent, consistent units. Using sat's own units directly
   should be rare and deliberate.

Verified directly against current code (not the prior audit's table alone) before writing each
recommendation below.

## Already-shared helpers (criterion 1 — good, no action)

These already exist as the single definition and are called from every real consumer:

| Helper | file:line | Callers | Already gated? |
|---|---|---|---|
| `prcForVariant` | `analyzer_helpers.go:87` | `applyAllocation`, `roleBottleneckReplicas`, `safeRemovalReplicasForRole`, `applyDeallocationForRole`, `sortVariantsForScaleDown` (cost_aware_optimizer.go:162) | No — returns raw value or 0-if-absent, callers gate `<=0` individually |
| `replicasForDemand` / `safeReplicasForSpare` | `query_api.go:19-48` | `roleBottleneckReplicas`, `safeRemovalReplicasForRole` | **Yes** — `prc<=0` → 0, one definition |
| `SumTotalSupply` / `SumTotalAnticipatedSupply` / `AggregateByRole` | `aggregation.go:50-127` | `buildCapacities` (sole caller of all three) | **No** — unconditional sum, no per-VC guard at all |
| `demandForRoleOrModel` / `requiredSpareForRoleOrModel` | `query_api.go:66-105` | `buildDecisionsWithOptimizer`, `rescaleInputsForGroup` | N/A — these read already-aggregated RC/SC/demand, not raw PRC |

**Finding, criterion 1:** the codebase already follows this pattern for rounding
(`replicasForDemand`/`safeReplicasForSpare`) — one function, used everywhere, gated once. It does
**not** yet follow it for the PRC lookup itself: there are **two** separate raw-PRC-lookup
helpers doing the same thing over two different shapes:
- `prcForVariant(r *domain.AnalyzerResult, v string) float64` (`analyzer_helpers.go:87`)
- `prcFromVCs(vcs []variantRecord, v string) float64` (`greedy_score_optimizer.go:476`, **one
  caller**: `analyzer_helpers.go:317`)

Neither guards anything — both return the raw field or `0` if the variant is absent. `<=0`
gating is duplicated at every call site instead of living in one place.

**Recommendation:** unify into one guarded helper. Two options, pick one:
- (a) Keep both signatures (different callers hold different shapes in scope) but give both a
  single shared `<=0` → 0 contract, documented once, so a future change to what counts as "no
  usable PRC" (e.g. adding a decision-path check per criterion 2) only has one place to change
  per shape — or better,
- (b) Since `prcFromVCs` has exactly one caller (`analyzer_helpers.go:317`, inside
  `allocateForModelPaired`'s role-PRC-map build) and that caller already has the `variantRecord`
  slice, consider whether `variantRecord` could carry the AnalyzerResult reference needed to call
  `prcForVariant` instead — collapsing to one function. Not verified whether that's structurally
  easy; flag for the coder-design-validation step if pursued.

This is a mechanical/naming-level cleanup, not a behavior change on its own — the real guard
question is below.

## Where the PRC=0 guard already exists but nothing decision-path-aware does (criterion 3)

Every one of these already has a `PerReplicaCapacity <= 0` (or equivalent) skip:

- `costGreedyRolePick` (`cost_aware_optimizer.go:90`)
- `scaleDownVariantSet` (`cost_aware_optimizer.go:120`)
- `costEfficiency` (`cost_aware_optimizer.go:226-231`, `<=0` → `math.MaxFloat64`)
- `fairShareRolePick` (`greedy_score_optimizer.go:403`)
- `applyAllocation`, `applyDeallocationForRole` (via `prcForVariant` + explicit `<=0` check)
- `roleBottleneckReplicas`, `safeRemovalReplicasForRole` (via `replicasForDemand`/`safeReplicasForSpare`)
- `reclaimRole`/`fillRole`/`markRoleGPULimited`, `roleDemandGPUs` (`rescale.go:441,475,592`)

**Per criterion 3, this is correct as-is and needs no SO-level guard added.** A
`DecisionNoSignal` SO whose fallen-through PRC happens to be `<=0` is already excluded from
every one of these. The remaining risk (below) is only the case where the fallen-through PRC is
a normal positive number — sat's own PRC is usually >0 when sat itself is live, which is
exactly the case where nothing here helps.

**No action recommended for this group** beyond the unification in the section above (once
unified, this guard exists in one place instead of ~8).

## Where PRC=0 guarding is NOT enough — genuinely missing a guard (criteria 1 + 3 combined)

These read PRC (or a value built from it) with **no gate of any kind**, not even `<=0`:

| Consumer | file:line | Why `<=0` alone can't be the fix here |
|---|---|---|
| `aggregation.SumTotalSupply`/`SumTotalAnticipatedSupply`/`AggregateByRole` | `aggregation.go:50-66,113-124` | Root cause. Sums every VC unconditionally — a C4 SO's fallen-through PRC (usually >0, sat's real measured value) is indistinguishable from a live SO's here. This is THE place to add a guard per criterion 1 ("add the guard into the helper"), since every RC/SC downstream is built from this one function's output. |
| `buildVariantRecords`/`recordsForRequest` | `variant_records.go:52-84` | Copies `PerReplicaCapacity` from `satResult.VariantCapacities` with no Reason/decision-path check at all — this is the shared entry point into every optimizer. A guard here (or upstream at composite-build time, see below) would cover all three optimizers and rescale in one place. |
| `sortVariantsForScaleDown` weighting | `cost_aware_optimizer.go:157-176` | `<=0` is not the relevant guard — PRC=0 is used as a legitimate tie-break weight here, not skipped. A C4 SO's nonzero fallen-through PRC silently changes scale-down ordering; no `<=0` check would ever catch this. |
| `buildDecisionsWithOptimizer` → `RecordSaturationMetrics` gauges | `cost_aware_optimizer.go:303,306` | Observability path, not a scaling decision — but an operator-visible number reflecting a fallen-through value with no marker is a real diagnosability gap, independent of whether it affects scaling. |
| `rescaleInputsForGroup`/`roleDemandGPUs` "best PRC among variants with PRC>0" | `rescale.go:552,564,587-599` | Already has a `PRC>0` guard (line 592) — but that guard picks the best PRC *among* qualifying variants without checking whether the winning variant's decision path was C4-sat-fallback specifically. Same class of gap as `SumTotalSupply` — PRC>0 is necessary but not sufficient here. |

**Per criterion 2, the right fix for this group is a guard checked EARLY, at the SO level, not
scattered `>0` patches at each of these 5 sites.** The natural place: `buildComposite`
(`composite.go`), where the per-SO `decisionPath` is already computed (§2.1.d-e) — the SO that
resolves to `DecisionNoSignal` should be excluded from ordinary scaling math right there, before
it ever reaches `buildCapacities`/`recordsForRequest`, rather than requiring 5+ downstream call
sites to each independently know to check decision-path.

**Recommendation:** this maps directly to the open §2.6 design question — "should something
upstream consume `SOHasSignal` per-SO, and do what with a no-signal SO." Given your criteria 2+3
together, the shape of the answer looks like:
- At `buildComposite`'s per-SO loop, when `decisionPath == DecisionNoSignal`: **do not** fall
  through to `sourceVC.PerReplicaCapacity`. Leave PRC at `0` (or some explicit sentinel) so the
  already-existing `<=0` guards (previous section) correctly exclude it everywhere they already
  check — collapsing most of this section's gaps for free, without touching 5 separate files.
  This directly targets criterion 1's "add the guard into the [shared] function" (here,
  `buildComposite` itself is the shared point every consumer's input flows through).
- The two sites that don't already gate on `<=0` (`sortVariantsForScaleDown`'s weighting, the
  metrics-gauge path) would then naturally see PRC=0 for a no-signal SO too — `sortVariantsForScaleDown`
  already tie-breaks correctly on a 0 weight (it's a valid low-priority value, not a crash); the
  metrics gauge would show 0/no-signal instead of a misleadingly normal number, which is a
  strict improvement for observability.
- **Exception, per your "except scale-from-zero" carve-out:** an SO with zero existing replicas
  and no valid info still needs a path to scale up from zero — check whether zeroing PRC here
  breaks that path specifically (scale-from-zero likely uses a different estimate entirely, not
  measured PRC, but this needs verification before landing, not assumption).

This is a design change to `composite.go`, not a chase across 5 files — consistent with
"reoccurring calculations should use the helper... add the guard into those functions" applied
at the composite-construction boundary rather than per-optimizer.

## Units — sat-specific vs. analyzer-independent (criterion 4)

Checked every consumer for whether it reads sat's raw units deliberately vs. incidentally:

- **`recordsForRequest`** (`variant_records.go:78-84`) has an explicit, currently-true code
  comment: *"Saturation is still the analyzer whose P sizes replicas — changing that is the
  coordination math and an explicit non-goal of this refactor."* This reads `req.CompositeSignal`
  (the composite, not sat directly) and passes it as `satResult` to `buildVariantRecords` — under
  v9, the composite's PRC *is* sat's PRC for the identity/no-signal cases (§2.1.a, and the
  fallthrough), and is a max-of-contributors value only for `DecisionAgree`/`DecisionSingle`.
  **This comment may be stale relative to v9**: it still frames PRC as "saturation's P" when v9's
  point was to make PRC a genuinely composite (cross-analyzer) value for contributing SOs. Worth
  a wording pass, separate from the guard question — flagging here rather than fixing now.
- **Every other consumer** (`analyzer_helpers.go`, `cost_aware_optimizer.go`,
  `greedy_score_optimizer.go`, `rescale.go`, `aggregation.go`) reads PRC only off the composite
  (via `variantRecord.PerReplicaCapacity`, `prcForVariant(e.Result, ...)` where `e` is the
  composite `NamedAnalyzerResult`, or `nr.RoleCapacities`/`nr.Result` where `nr` is the
  composite) — **none of them names or special-cases sat directly**. This matches criterion 4:
  sat's own unit only enters intentionally, at exactly one place (`composite.go:171`'s
  fallthrough), and that's precisely the line the guard recommendation above targets.
- **One out-of-scope sighting, flagged for completeness, not action:** `engine_v2.go:1389`
  (`publishVariantPressure`) computes `Supply := ReplicaCount × PerReplicaCapacity` directly off
  `namedResults[0]` (saturation's own result, not the composite) — a genuinely sat-specific
  read, deliberate by construction (it's a saturation-specific metrics publisher, not a
  general-scaling consumer), but worth a second look later if the "canonical composite demand"
  direction (STATE.md's open Known-issue item) ever generalizes this function.

**Per criterion 4: the codebase is already almost entirely compliant** — the one deliberate,
rare, sat-specific read is the composite's own internal fallthrough (`composite.go:171`), which
is also the exact line the criterion-2/3 fix above targets. Fixing that one line serves both
criteria at once.

## Summary — one fix serves three of your four criteria

The `composite.go:171` fallthrough (`vc.PerReplicaCapacity = sourceVC.PerReplicaCapacity` when
`compositeTotalReplicas <= 0`, unconditional on decision path) is the single point where:
- criterion 2's "early SO-level guard" belongs,
- criterion 3's "PRC=0 guard is enough everywhere else" becomes actually true (right now it's
  true syntactically but not semantically, since the fallthrough usually produces a nonzero
  number), and
- criterion 4's "sat units should be rare and deliberate" is already satisfied in name but not
  in effect, since the fallthrough's "deliberate" sat read currently masquerades as ordinary
  composite data downstream.

Separately, unifying `prcForVariant`/`prcFromVCs` (criterion 1) is a smaller, independent
cleanup that doesn't block the above.

**Not resolved by this doc — still needs your ruling:** whether zeroing PRC for a
`DecisionNoSignal` SO breaks the scale-from-zero path, and exactly what "zeroing" should mean
(literal `0`, or something that still lets scale-from-zero read a distinct estimate). Recommend
this be the next concrete investigation before writing any coder task.
