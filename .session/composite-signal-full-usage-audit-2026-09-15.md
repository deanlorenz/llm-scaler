# Full `CompositeSignal` usage audit — 2026-09-15

Redo, from scratch, of the PRC-only guarding pass, per the user's 7 corrections (see
`.session/task-composite-signal-usage-audit-2026-09-15.md`). Scope: every consumer of
`ModelScalingRequest.CompositeSignal` / the composite `allocation.NamedAnalyzerResult` — supply,
demand, and RC/SC consumers alike, not just PRC-touching ones. Every file:line below was
re-checked against the current tree during this pass, not copied from either prior doc.
`internal/engines/allocation/multi_backup/*.go` carries `//go:build ignore` (confirmed dead code,
excluded from the build) and is out of scope throughout. `publishVariantPressure`
(`engine_v2.go:1380`, called at `:165` with `namedResults[0]`) reads saturation's own result
directly, never the composite, so it is also out of scope for a `CompositeSignal`-usage audit.

## 0. Headline

- **Correction 4 verified true by direct trace, not assumption:** `buildComposite`
  (`internal/engines/steadystate/composite.go:226`) calls `buildCapacities(ctx, &composite, nil,
  scaleUp, scaleDown)` as its own last construction step, before returning the composite. There
  is no separate downstream call — `buildCapacities` executes *inside* `buildComposite`, in the
  same call, using the composite's own just-built `VariantCapacities`. It is composite
  construction, not a downstream consumer. Criterion 2 (supply, summed-across-SOs, no-signal SO's
  own term → 0) applies to it directly, exactly as correction 4 says.
- **New finding, criterion 3/5/7 relevant, not previously surfaced:** saturation's own analyzer
  already has a purpose-built, *upstream-of-the-composite* mechanism for the
  partial-scale-from-zero case — the P0-store estimator
  (`internal/engines/analyzers/saturation_v2/analyzer.go:744-757`, label `satReasonP0Store`,
  distinguished from `satReasonNoData`/`allocation.ReasonNoData`). `composite_decision.go`'s own
  doc comment (lines 14-17) confirms this is deliberate design: *"saturation's existing P0-store
  ladder is the only source of estimated PRCs for an idle or never-seen-before SO, and it flows
  through as an ordinary contribution... rather than as a separate composite-level default
  mechanism."* This means a P0-store-estimated SO reaches the composite as `DecisionSatFallback`
  (`C2-sat-fallback`) or even `DecisionSingle`/`DecisionAgree` if another analyzer also
  contributes — **the same `Reason` string a live, ordinarily-measured sat fallback produces.**
  The composite's `Reason` field currently cannot distinguish "sat has a genuine live measurement
  it is falling back with" from "sat has a deliberate zero-replica what-if estimate." Both look
  identical downstream. This is the concrete shape of the ambiguity criterion 5/7 ask every call
  site to resolve — flagged here as a fact about the code, addressed per-site below.
- `SOHasSignal` (`composite_signal_gate.go:15-24`) remains fully implemented, unit-tested, and
  has zero production call sites — unchanged from the prior pass's finding.

## 1. Consumer inventory, re-verified

Re-walked every fan-out point from `req.CompositeSignal` in current code:

```
req.CompositeSignal (allocation.NamedAnalyzerResult)
 │
 ├─ recordsForRequest(req) → buildVariantRecords(req, nr.Result)      variant_records.go:78-84, 52-70
 │    called from: CostAwareOptimizer.Optimize (cost_aware_optimizer.go:48)
 │                 GreedyByScoreOptimizer.Optimize ×2 (greedy_score_optimizer.go:112,146)
 │                 applyRescale (rescale.go:226, records used only for singleAccType filter)
 │                 modelCurrentGPUs (rescale.go:507)
 ├─ e := req.CompositeSignal (by value) → initRoleState(&e)           cost_aware_optimizer.go:59-60
 │                                                                     greedy_score_optimizer.go:117-118,156-157
 ├─ satNamed := req.CompositeSignal → buildDecisionsWithOptimizer      cost_aware_optimizer.go:246,306
 ├─ satNamed := req.CompositeSignal → rescaleInputsForGroup/           rescale.go:344,528
 │              rescaleModelDecisions/roleDemandGPUs
 ├─ req.CompositeSignal passed directly into reclaimRole                rescale.go:372
 ├─ allocation.CompositeHasSignal(req.CompositeSignal)                 engine_v2.go:693,714; engine.go:1091
 └─ CompositeSignal: composite  (construction, the write side)          engine_v2.go:847
```

Every table row below cites the current file:line, re-checked directly (not trusted from either
prior doc).

## 2. Supply-related consumers

Supply is `Σ_SO ReplicaCount(SO) × PerReplicaCapacity(SO)` (and the anticipated variant with
`+PendingReplicas`), always summed **across every SO in scope** (model-level or per-role).
Criterion 2 (corrected): a no-signal SO's own term in that sum should read as 0; it must not zero
out other SOs' real terms in the same sum.

| # | Consumer (file:line) | Category | Caller's intent | Current behavior | Recommendation (criterion) |
|---|---|---|---|---|---|
| S1 | `buildCapacities` → `aggregation.SumTotalSupply`/`SumTotalAnticipatedSupply` (`internal/engines/steadystate/engine_v2.go:966-967`, sums impl at `aggregation.go:51-69`) | Supply, model-level sum | N/A — this IS the sum, not a caller with intent | Unconditional `Σ_v ReplicaCount×PRC` over every VC in the composite, live or no-signal alike. No guard for `Reason == "C4-no-signal"`. **Re-verified per correction 4: this runs inside `buildComposite` (`composite.go:226`), i.e. it is composite construction, not a downstream consumer.** | **Criterion 2 + 4.** This is the single place the fix belongs: the per-SO term for a `DecisionNoSignal` VC should read 0 in this sum (it already does today only incidentally, IF the no-signal VC's PRC happens to be 0 — see §0's P0-store finding, which shows it is not always 0). Fix belongs at the SO level inside `buildComposite`'s per-SO loop (where `decisionPath` is already known), not by adding a second pass here. Other SOs' real terms in the same sum are unaffected either way — this is a per-VC term inside a `for _, vc := range vcs` loop already, so zeroing one VC's contribution cannot touch another's. |
| S2 | `aggregation.AggregateByRole` (`aggregation.go:113-127`), consumed via `buildRoleCapacities` (`engine_v2.go:1085-1110`) | Supply, per-role sum | Same as S1, scoped per role | Same unconditional per-VC sum, grouped by role instead of model-wide. | **Criterion 2.** Identical reasoning to S1 — a fix at `buildComposite`'s per-SO loop covers this for free, since both S1 and S2 read the same `VariantCapacities` slice. |
| S3 | `applyUniversalThreshold` (`engine_v2.go:536-574`) | Supply/demand → RC/SC, model- and role-level | N/A — pure derivation from S1/S2's already-summed totals | `rc := demand/scaleUp - nr.TotalAnticipatedSupply`; `sc := nr.TotalSupply - demand/scaleDown`. Reads only the already-aggregated scalars, no per-VC visibility at all. | No independent guard needed or possible here — it inherits whatever S1/S2 already produced. Once S1/S2's per-SO zeroing (criterion 2) is correct, RC/SC here are correct by construction. Not a separate site to patch. |
| S4 | `warnUnsizableShortfall` (`engine_v2.go:1048-1067`) | Supply, diagnostic only | N/A — observability, not a scaling decision | Logs when `RequiredCapacity > 0` and every VC has `PerReplicaCapacity <= 0`. Already effectively criterion-3-shaped (a `<=0` check), and it is diagnostic, not a decision. | No change needed. If S1's fix (zeroing a no-signal SO's PRC contribution) lands, this log's trigger condition becomes MORE accurate, not less — a fleet of only no-signal SOs would correctly show as "no variant can absorb the shortfall." |
| S5 | `costGreedyRolePick` (`cost_aware_optimizer.go:81-104`) | Supply, per-variant pick | Strict — picking a variant to size a real scale-up replica count needs a real PRC | `if vc.PerReplicaCapacity <= 0 { continue }` (line 90) — already skips. | **Criterion 3 confirms this is already correct as-is.** A `DecisionNoSignal` SO whose PRC is correctly 0 (once S1's fix lands) is excluded here automatically. No SO-level guard needed in addition — this is exactly the "PRC=0 guard is enough here" case criterion 3 describes. |
| S6 | `scaleDownVariantSet` (`cost_aware_optimizer.go:110-151`) | Supply, per-variant removal cap | Strict — same reasoning as S5, for scale-down | `if vc.PerReplicaCapacity <= 0 { continue }` (line 120). | Same as S5 — criterion 3, already correct, no action. |
| S7 | `sortVariantsForScaleDown` (`cost_aware_optimizer.go:157-176`) | Supply, tie-break weight, not a gate | Ambiguous by construction — PRC=0 is a legitimate low-priority WEIGHT here, not a value to skip | `weighted := e.Score * prcForVariant(...)`; used only as a sort tie-break (cost is the primary key). A no-signal SO with a real (P0-store or stale-live) PRC silently gets an ordinary, non-zero tie-break weight today. | **Criterion 2 (indirectly) + 7.** This does not need its own `<=0` skip (0 is a valid weight, already handled correctly as a tie-break). Once S1's fix makes a genuine `DecisionNoSignal` SO's PRC read 0 at the composite level, this site inherits a correctly-low tie-break weight for free — it tie-breaks toward being scaled down last among equally-costed variants, which is a reasonable (not obviously wrong) default for an unmeasured SO, but this is a secondary effect of the S1 fix, not something to hand-tune here directly. Flagging, not proposing a separate change to this function. |
| S8 | `costEfficiency` (`cost_aware_optimizer.go:226-231`) | Supply, per-variant cost/PRC ratio | Strict — `cost/PRC` is meaningless without a real PRC | `if vc.PerReplicaCapacity <= 0 { return math.MaxFloat64 }` — already treats 0 (or negative) as "worst," sorting it last. | Criterion 3, already correct, no action. |
| S9 | `fairShareRolePick` (`greedy_score_optimizer.go:387-443`) | Supply, per-variant pick (GPU-constrained path) | Strict — same reasoning as S5, for the fair-share optimizer | `if vc.PerReplicaCapacity <= 0 { continue }` (line 403). | Criterion 3, already correct, no action. |
| S10 | `rescaleInputsForGroup`/`modelDemandGPUs`/`roleDemandGPUs` "best PRC among variants with PRC>0" (`rescale.go:552,564,587-599`) | Supply (via chosen variant's PRC), demand-to-GPU conversion | Strict — the rescale water-filling needs a real per-replica GPU conversion factor | Already has `vc.PerReplicaCapacity <= 0 { continue }` (line 592) inside a loop that picks the cheapest QUALIFYING variant's PRC as `best`. Correct exclusion of PRC<=0 variants, but the winning variant's decision path is never checked — a no-signal SO with a nonzero (P0-store-estimated or stale) PRC can still win "best." | **Criterion 2, same fix point as S1.** Once a genuine no-signal SO's PRC reads 0 at the composite (S1's fix), it is excluded here by the existing `<=0` check with no separate change. Not a separate gap once S1 lands. |
| S11 | `buildDecisionsWithOptimizer` → `RecordSaturationMetrics` gauges (`cost_aware_optimizer.go:303,306`, gauges in `internal/metrics/metrics.go`) | Supply/RC/SC, observability | N/A — dashboard numbers, not a decision | `decision.Utilization = vc.Utilization`; `decision.RequiredCapacity, decision.SpareCapacity = requiredSpareForRoleOrModel(...)`. No gating; reflects whatever S1-S3 already computed. | No separate fix — inherits S1's correction. An operator-visible gauge for a genuinely no-signal SO will read 0/low once S1 lands, which is the correct, honest number instead of a silently-normal-looking one. |

## 3. Demand-related consumers

Demand is `D_sat[role]` — **per-role, not per-SO** (correction 3). No analyzer currently derives
demand from per-SO metrics; per the user's correction, demand consumers should not have, and do
not need, any per-SO fallback logic at all. The only question for a demand consumer is whether
the per-role value itself is broken.

| # | Consumer (file:line) | Category | Caller's intent | Current behavior | Recommendation (criterion) |
|---|---|---|---|---|---|
| D1 | `demandForRoleOrModel` (`query_api.go:66-79`) | Demand, per-role/model read | N/A — pure accessor, the single shared role-vs-model fallback | `role == RoleBoth` → `nr.Result.TotalDemand`; else `nr.RoleCapacities[role].TotalDemand`, falling back to the model-level scalar **only on a genuine map miss** (role not present in `RoleCapacities` at all), never on a zero or suspect value. | **Criterion 3 — checked explicitly, no conflation found.** This function's only "fallback" is role-key-absent → model-level, which is a structural/shape fallback (the role literally has no entry), not a per-SO-style value fallback. It never substitutes a guessed value for a broken per-role demand; a broken `D_sat[role]` (e.g. bad PromQL filter) flows straight through as whatever bad number the analyzer produced — there is no code here trying to "fix" it, which is the correct behavior per correction 3 (an error here should read as unusable, not be silently patched). No change needed. **Residual gap, not this function's job:** nothing today marks a per-role `D_sat[role]` as itself broken (e.g. from a bad PromQL filter) — see §5 below; that is a producer-side (analyzer) concern, not something this accessor should grow logic for. |
| D2 | `aggregation.DemandByRole`/`SumTotalDemand` (`aggregation.go:71-85,101-108`) | Demand, aggregation-side | N/A — pure sum, analyzer-side (used to populate `RoleDemand`/`TotalDemand` before the composite is even built) | `Σ_v vc.TotalDemand`, grouped by role or not. | Not a `CompositeSignal` consumer per se — this runs on an analyzer's own `VariantCapacities` before composition (see `composite.go:62`: `aggregation.DemandForRole(sat.Result, role)`). No per-SO fallback logic exists here; each `vc.TotalDemand` is per-SO information that legitimately differs (individual SOs really do serve different demand), which is distinct from `D_sat[role]` the shared per-role figure the composite copies verbatim (`composite.go:207,212-215`, `TotalDemand`/`RoleDemand` copied from `sat.Result`, not recomputed). No conflation found — flagging as re-verified, not previously checked (task item 7). |
| D3 | `requiredSpareForRoleOrModel` (`query_api.go:96-105`) | RC/SC, per-role/model read (demand feeds in via S3's formula, not read directly here) | N/A — same role-vs-model fallback shape as D1, applied to RC/SC scalars | `nr.RoleCapacities[role]` present → use it; else model-level `nr.RequiredCapacity/SpareCapacity`. Same "map-miss only" fallback shape as D1. | Same as D1 — criterion 3, no conflation, no change. |
| D4 | `fairShareValue` (`greedy_score_optimizer.go:62-80`) | Demand-derived priority metric | N/A — cross-model fair-share weighting, not a per-SO decision | `priority × Σ_role pickerState[role] × e.Score`; falls back to `max_role pickerState[role]` when the weighted result is ≤0 (Score=0 or priority=0). `pickerState` is per-role remaining demand from `initRoleState`, itself derived from `RoleCapacities[role].RequiredCapacity` (D3) or the model-level scalar — never a per-SO PRC. | **Criterion 3 — checked, no conflation.** The fallback here (`Score=0`→use raw demand) is about a MODEL-scoring input (`Score`) being degenerate, not about a per-role demand value being broken vs. a per-SO signal being absent. These are different axes; nothing here substitutes a per-SO guess for a broken per-role figure. No change needed, but note (not a defect): if `D_sat[role]` itself were broken (bad PromQL filter, corrected per criterion 3 the model should be treated as unusable for that role), `fairShareValue` has no way to know that today — it would just fair-share on whatever bad number came through, same gap noted in D1. This is a producer-side gap (see §5), not something to patch in this consumer. |

## 4. RC/SC (Required/Spare Capacity) consumers — derived from both

RC/SC are engine-derived from demand and supply together (`applyUniversalThreshold`). Consumers
here read the already-derived scalars/maps, not raw PRC directly (except where noted).

| # | Consumer (file:line) | Category | Caller's intent | Current behavior | Recommendation (criterion) |
|---|---|---|---|---|---|
| R1 | `initRoleState` (`analyzer_helpers.go:138-171`) | RC/SC, per-model setup | N/A — seeds picker-local state from the already-computed RC/SC | Disaggregated: `pickerState[role] = RoleCapacities[role].RequiredCapacity`; `RoleSpare[role] = RoleCapacities[role].SpareCapacity`. Non-disaggregated: synthesizes `"both"` from the model-level `Remaining`/`Spare` scalars. No per-SO logic — reads only engine-built aggregates. | No change — this is purely a read of already-derived RC/SC (criterion 2's fix point is upstream, at S1). Once S1 lands, `RequiredCapacity`/`SpareCapacity` here are correct by construction. |
| R2 | `demandForRoleOrModel`/`requiredSpareForRoleOrModel` reads inside `buildDecisionsWithOptimizer`, `rescaleInputsForGroup`, `roleDemandGPUs` | RC/SC + demand, various | See D1/D3 above | Same as D1/D3 | Same as D1/D3 — no conflation, already correct given upstream aggregates are correct. |
| R3 | `applyAllocation` (`analyzer_helpers.go:71-83`) | RC/SC bookkeeping, scale-up commit | **Strict** — this decrements `e.Remaining` by a real replica commitment; a guessed PRC here would silently under/over-charge the model's remaining required capacity | `prc := prcForVariant(e.Result, v); if prc <= 0 { return }` — no-op if PRC is not usable. | **Criterion 5 + 7.** This function does not itself decide strict-vs-what-if — it is called AFTER a variant has already been picked and sized (by `roleBottleneckReplicas`/`allocateForModelPaired`). Its `<=0` guard is correct defensively, but the real question (per correction 5) is whether the CALLER should have asked for this variant at all. See §6 (per-caller `prcForVariant` breakdown) for the actual classification — this row exists for completeness of the RC/SC bookkeeping trace, not as an independent decision point. |
| R4 | `applyDeallocationForRole` (`analyzer_helpers.go:245-259`) | RC/SC bookkeeping, scale-down commit | Same shape as R3, for `RoleSpare` | Same `<=0` guard. | Same as R3 — see §6. |
| R5 | `safeRemovalReplicasForRole`/`needsScaleDownForRole` (`analyzer_helpers.go:230-273`) | RC/SC, scale-down eligibility | **Conservative by construction already** — `e.Live` (model-level: true if ANY SO is live) gates both; `RoleSpare[role] <= 0` also gates | `if !e.Live { return 0/false }`; then `Result == nil \|\| RoleSpare == nil \|\| RoleSpare[role] <= 0 { return 0/false }`. | **Criterion 6 — this is exactly the "no good signal → no spare" shape criterion 6 asks for, and it is already implemented this way.** `safeReplicasForSpare` (the shared rounding helper it calls into) also returns 0 whenever `prc <= 0` (`query_api.go:39-42`) — there is no path here that falls back to a guesstimate the way `replicasForDemand`'s what-if case might. No change needed; flagging as verified-correct against the corrected criterion, not merely "already had a guard" as the prior pass framed it. |

## 5. Identity/metadata consumers (not PRC/demand math)

| # | Consumer (file:line) | Category | Notes |
|---|---|---|---|
| I1 | `buildVariantRecords`/`recordsForRequest` (`variant_records.go:52-84`) | Identity + capacity carrier | Copies `PerReplicaCapacity`/`Utilization` from `satResult.VariantCapacities` (i.e. from the COMPOSITE, since `recordsForRequest` reads `req.CompositeSignal`) into the optimizer's `variantRecord`. This is the shared entry point into every optimizer and rescale — it does not itself gate on decision-path or `<=0`, by design: a variant the analyzer did not size gets `PerReplicaCapacity == 0` rather than being dropped (doc comment, lines 42-45), and every real consumer of the resulting slice (S5, S6, S9, etc.) already checks `<=0` itself. **Re-verified: no gap here distinct from S1's fix** — this function is a value carrier, not a decision point; whatever PRC the composite produced for a no-signal SO (0, once S1 lands) flows through unchanged. |
| I2 | `logAnalyzerResult` (`engine_v2.go:1126-1160+`) | Identity + PRC, observability | Logs `PerReplicaCapacity`, `Role`, `Reason` per VC. `Reason` (the decision path) is logged but never branched on — pure observability, matches criterion-3's "diagnostic only" carve-out. No change needed. |
| I3 | `ReplicaCount`/`PendingReplicas` reads throughout (`aggregation.go`, `variant_records.go`, etc.) | Pure identity, copied unconditionally from sat at `composite.go:87-88` ("UNCONDITIONAL... identity role, never gated," per that file's own comment) | Not PRC/demand math; correctly never gated by decision path — identity fields describe the fleet's actual state regardless of whether any analyzer could size it this cycle. No recommendation. |

## 6. `prcForVariant`/`prcFromVCs` — per-caller classification (task item 5, criterion 7)

**Correction 7 is explicit: the mission owner's prior recommendation to unify these two lookup
functions into one was wrong.** They read different shapes (`*domain.AnalyzerResult` vs.
`[]variantRecord`) held in scope by different callers, and — more importantly per the user's
correction — the REAL fix is not a shape-level merge at all: it is naming that makes
strict-vs-what-if explicit at each call site. This section does the per-caller classification a
future accessor-splitting design would need, per the task's explicit instruction not to propose
that design in detail here.

Both functions today have an identical, minimal contract: return the raw `PerReplicaCapacity` for
a named variant, or `0` if the variant is absent. Neither distinguishes a real measurement from a
sat-fallback from a P0-store what-if estimate — that distinction lives one level up, in the
composite's `Reason`/decision-path field, which none of these callers currently consult.

| Caller (file:line) | What it actually wants | Strict or what-if? (criterion 7) | Reasoning |
|---|---|---|---|
| `applyAllocation` (`analyzer_helpers.go:71-83`, via `prcForVariant` at :75) | A per-replica capacity to size a scale-up commitment it has ALREADY decided to make (called after `roleBottleneckReplicas`/`allocateForModelPaired` picked this variant and a replica count) | **Not independently decidable — inherits its caller's classification.** `applyAllocation` itself has no opinion; it is invoked with a variant name and count already chosen upstream (`allocateForModelPaired` line 381, and both `CostAwareOptimizer`/`GreedyByScoreOptimizer`'s scale-up paths). | This is bookkeeping (decrementing `Remaining`), not a decision point. The real question — should this variant have been considered for scale-up at all — belongs to the picker (`costGreedyRolePick`/`fairShareRolePick`, which already gate `PerReplicaCapacity <= 0`, i.e. supply-side criterion-3 guards) and, per correction 5, to whether the model/SO should have been in the scale-up path at all. Most callers of `prcForVariant` here want `roleBottleneckReplicas`'s question (a replica count), confirmed below — `applyAllocation` is the commit step after that question was already answered. |
| `roleBottleneckReplicas` (`analyzer_helpers.go:191-196`, via `prcForVariant` at :195) | `ceil(demand/PRC)` — a REPLICA COUNT for scale-up sizing | **Ambiguous per-call, resolved by the caller's own context — see below.** In isolation this function has no way to know if its caller wants strict-only or a what-if. | Called from `allocateForModelPaired` (:327), which is the shared scale-up loop for BOTH `CostAwareOptimizer` and `GreedyByScoreOptimizer`'s ordinary scale-up path — i.e. every ordinary scale-up, not a special what-if path. Per correction 5's real question ("should the caller be asking for a replica estimate at all for this SO"): `allocateForModelPaired` is driven by `pick(role, variants, ...)` (`costGreedyRolePick`/`fairShareRolePick`), which already refuses to return a variant with `PerReplicaCapacity <= 0` (S5/S9 above) — so by the time `roleBottleneckReplicas` is called, the picker has already screened out unusable variants. **This means today's ordinary scale-up path is implicitly the "legitimate what-if" case for a zero-replica, P0-store-estimated SO**: the picker does not distinguish "PRC is real because sat has live data" from "PRC is a genuine P0-store estimate for a currently-0-replica SO" — both pass the `>0` gate identically, and BOTH are cases the user says should be honored (ordinary live scale-up, and legitimate partial-scale-from-zero). The only case this path should NOT be honoring — a genuinely broken/no-signal SO — is exactly the case S1's composite-level fix (criterion 2) is meant to zero out upstream, which would then correctly fail this same `>0` gate. **Verdict: this caller wants the what-if-permissive variant, and today's behavior is consistent with that IF AND ONLY IF S1's fix lands** (so that "broken" and "legitimate zero-replica what-if" are actually distinguishable by the time PRC reaches here — right now they are not fully distinguishable, per §0's finding, though a genuinely-no-data SO does correctly produce `PerReplicaCapacity == 0`/absent-from-VariantCapacities and is already screened out). |
| `safeRemovalReplicasForRole` (`analyzer_helpers.go:230-243`, via `prcForVariant` at :242) | `floor(spare/PRC)` — a replica count for scale-DOWN safety | **Strict — no-fallback.** Per correction 6 explicitly: `safeReplicasForSpare` (which this feeds) must be conservative — no good signal → no spare, never a guesstimate. | This function already gates on `e.Live` (model-level) before ever calling `prcForVariant`, and `safeReplicasForSpare` independently returns 0 for `prc<=0`. There is no legitimate "what-if" reading for a REMOVAL decision — removing replicas based on a guessed PRC risks under-provisioning capacity that is actually needed, which criterion 6 explicitly forbids. This caller should always want the strict variant once one exists; today's `prcForVariant` cannot express that distinction, but the surrounding guards happen to produce the conservative outcome already (criterion 6's own note: "it should NOT fall back to a guesstimate... default to 0"). |
| `applyDeallocationForRole` (`analyzer_helpers.go:245-259`, via `prcForVariant` at :251) | Decrement `RoleSpare[role]` by a commitment already decided by `safeRemovalReplicasForRole` | **Strict — inherits `safeRemovalReplicasForRole`'s classification.** Same bookkeeping-not-decision relationship as `applyAllocation`/`roleBottleneckReplicas`. | Same reasoning as the `applyAllocation` row — this is the commit step after `safeRemovalReplicasForRole` already answered the strict question. |
| `sortVariantsForScaleDown`'s weighting (`cost_aware_optimizer.go:157-176`, via `prcForVariant` at :162) | A TIE-BREAK WEIGHT, not a replica count or a capacity commitment | **Neither strict nor what-if in the same sense — a ranking input, not a decision gate.** | As discussed in S7: 0 is a legitimate, meaningful weight here (ranks a no-signal/zero-PRC variant toward being scaled down last among cost-tied variants), so this caller does not need the strict-vs-what-if distinction the way a sizing decision does. Once S1's composite fix distinguishes genuine-no-signal (→0) from live/P0-store (→real value), this weight becomes more accurate automatically; no independent classification needed for this call site beyond that. |
| `prcFromVCs`'s one caller: `allocateForModelPaired`'s `prcByRole[role] = prcFromVCs(variants, v)` (`analyzer_helpers.go:317`) | Same question as `roleBottleneckReplicas` above — this PRC feeds `roleBottleneckReplicas` (line 327) and the per-role utilization/commit math (lines 333-371) in the SAME function | **Same verdict as `roleBottleneckReplicas`: what-if-permissive, correct once S1 lands.** | `prcFromVCs` reads from the `variants []variantRecord` slice already screened by `pick(...)` for this role (same picker functions as above), so it is answering the identical question `roleBottleneckReplicas` answers, just from the `variantRecord` shape instead of `*domain.AnalyzerResult` — this is exactly why correction 7 says most `prcForVariant`/`prcFromVCs` callers actually want `replicasForDemand`'s question (a replica count), not a raw PRC read in isolation. Confirmed here directly: `prcFromVCs`'s single caller uses the result only to compute a replica count and a utilization ratio in the same loop body, never as a standalone value. |

**Summary of the strict-vs-what-if split, per corrected criterion 7:**
- **Strict (must refuse fallback):** `safeRemovalReplicasForRole` → `applyDeallocationForRole`
  (scale-down/removal path). This matches criterion 6 exactly.
- **What-if-permissive (may legitimately accept a P0-store/zero-replica estimate):**
  `roleBottleneckReplicas` and `prcFromVCs`'s sole caller inside `allocateForModelPaired`
  (scale-up path) — but this is only actually SAFE today conditioned on S1's composite-level fix
  landing, so that "broken/no-signal" and "legitimate zero-replica what-if" stop being
  indistinguishable at the point PRC is read. Until then, this path is arguably tolerating BOTH
  cases as if they were the legitimate one, which happens to be mostly harmless for the ordinary
  live-fallback case but is exactly the ambiguity the corrections ask to be resolved with real
  analysis, not assumed away — flagging per the task's explicit instruction to say so rather than
  guess.
- **Not a strict/what-if question at all:** `sortVariantsForScaleDown` (a ranking weight,
  criterion 7 doesn't apply the same way), `applyAllocation` (pure bookkeeping, inherits its
  caller).
- **Not resolved by this audit, flagged rather than guessed (per task's explicit instruction):**
  whether `roleBottleneckReplicas`/`prcFromVCs`'s scale-up path should gain an EXPLICIT
  what-if-only marker (e.g. reading the composite's per-SO `Reason`/decision-path via something
  like `SOHasSignal`, or a new "is this a P0-store estimate" predicate) so that a genuinely broken
  SO and a legitimate zero-replica what-if are distinguishable independent of whether S1's fix
  alone is sufficient. This is a real design question the corrections raise but do not settle,
  and the task explicitly says not to propose the accessor-splitting design here — surfacing it
  as the open question for the next step.

## 7. `buildCapacities` construction-vs-downstream re-verification (task item 6)

Directly traced, not assumed from either prior doc:

```
buildComposite(...)                                    composite.go:40
  ... per-SO loop builds compositeVCs ...               composite.go:70-199
  composite := allocation.NamedAnalyzerResult{...}       composite.go:218-225
  buildCapacities(ctx, &composite, nil, scaleUp, scaleDown)   composite.go:226   <-- HERE, inside buildComposite
  composite.Remaining = composite.RequiredCapacity        composite.go:227
  composite.Spare = composite.SpareCapacity                composite.go:228
  return composite                                        composite.go:230
```

`buildCapacities` (`engine_v2.go:925-980`) is called exactly once from this site with the
composite's own just-built `Result` — it is the LAST STEP of `buildComposite`, in the same
function call, before the composite is ever handed to `collectV2ModelRequest`
(`engine_v2.go:847`) or read by any optimizer. There is no separate "downstream" call to
`buildCapacities` with the composite as an already-published input — the prior audit's framing of
it as a "root of propagation" downstream site is **confirmed wrong**, exactly as correction 4
states. `buildCapacities` only computes supply-side aggregates
(`SumTotalSupply`/`SumTotalAnticipatedSupply`/`RoleCapacities`/RC/SC) from the SAME per-SO data
the composite's own loop just produced — criterion 2 applies to it directly as part of
construction, not as a separate case.

## 8. Demand-consumer conflation check (task item 7 — new ground, not checked by either prior pass)

Explicitly checked every demand-related consumer (D1-D4 above, plus the analyzer-side
`aggregation.DemandByRole`/`SumTotalDemand`) for whether it conflates a per-role-broken case with
a per-SO fallback case, per corrected criterion 3.

**Finding: no current consumer conflates the two**, but the corrected criterion exposes a
different, real gap: **nothing anywhere in this call graph currently marks a per-role `D_sat[role]`
value as itself broken.** `demandForRoleOrModel`'s fallback (D1) is a structural map-miss fallback
(role absent from `RoleCapacities` entirely), never a value-quality fallback — if
`D_sat[role]`'s underlying PromQL query returned a wrong-but-present number (the scenario
criterion 3 names), every consumer downstream (`fairShareValue`, `applyUniversalThreshold`,
`roleDemandGPUs`, `rescaleInputsForGroup`) would use that wrong number with no way to detect it,
because there is no "this role's demand is suspect" signal anywhere in
`domain.AnalyzerResult`/`RoleCapacities`/`NamedAnalyzerResult` to check. This is a
**producer-side gap** (the analyzer that computes `D_sat[role]` would need to emit some kind of
broken/suspect marker for that role, analogous to how a VariantCapacity's `Reason` marks a
per-SO PRC as no-data/error) — it is not something any of the consumer functions audited here can
fix by adding a guard, since none of them has a signal to guard ON. Flagging as the concrete,
new-ground finding task item 7 asked for, not resolving it: whether and how to add such a
per-role demand-health marker is a producer-side design question outside this audit's scope
(read-only investigation of `CompositeSignal` usage, not a design task for the demand analyzers).

## 9. Open questions / judgment calls (not resolved here, per task's explicit instruction)

1. **§6's flagged item:** whether `roleBottleneckReplicas`/`prcFromVCs`'s scale-up callers need an
   explicit what-if marker independent of S1's fix, to distinguish a genuinely broken SO from a
   legitimate zero-replica (P0-store) what-if at the point PRC is read, rather than relying on
   S1's composite-level zeroing to make the distinction moot in practice.
2. **§8's finding:** whether/how a per-role demand-health marker should be added upstream (in the
   demand-producing analyzer), so `fairShareValue`/`applyUniversalThreshold`/etc. can distinguish
   "this role's demand is a real number" from "this role's demand came from a broken query" —
   currently no such signal exists anywhere in the composite or its inputs.

Neither of these is a criterion-vs-code conflict (the code does not yet implement the corrected
criteria in these two respects, which is expected — they are gaps, not contradictions), so neither
was published as a `kind="question"` blocking item; both are recorded here as the two genuine
design judgment calls this audit surfaced, for the mission owner to decide how to sequence.

## 10. Summary table — every recommendation's criterion citation

| Site | Criterion(s) |
|---|---|
| S1 `buildCapacities`'s `SumTotalSupply`/`SumTotalAnticipatedSupply` | 2, 4 |
| S2 `AggregateByRole` | 2 |
| S3 `applyUniversalThreshold` | inherits 2 |
| S4 `warnUnsizableShortfall` | none (diagnostic, no change) |
| S5/S6/S8/S9 (`costGreedyRolePick`/`scaleDownVariantSet`/`costEfficiency`/`fairShareRolePick`) | 3 (already correct) |
| S7 `sortVariantsForScaleDown` | 2 (indirect), 7 |
| S10 `roleDemandGPUs`/`rescaleInputsForGroup` | 2 |
| S11 metrics gauges | inherits 2 |
| D1 `demandForRoleOrModel` | 3 (verified, no conflation) |
| D2 `DemandByRole`/`SumTotalDemand` | 3 (verified, no conflation; producer-side) |
| D3 `requiredSpareForRoleOrModel` | 3 (verified, no conflation) |
| D4 `fairShareValue` | 3 (verified, no conflation) |
| R1-R2 (`initRoleState`, downstream reads) | inherits 2 |
| R3/R4 `applyAllocation`/`applyDeallocationForRole` | 5, 7 (inherits caller) |
| R5 `safeRemovalReplicasForRole`/`needsScaleDownForRole` | 6 (verified correct) |
| I1/I2/I3 (identity/observability) | none (not PRC/demand math, or diagnostic-only) |
| `buildCapacities` placement | 4 (construction, not downstream — confirmed) |
| `prcForVariant`/`prcFromVCs` per-caller (§6) | 7 |
| Demand conflation check (§8) | 3 (new ground — no conflation found, producer-side gap flagged) |
