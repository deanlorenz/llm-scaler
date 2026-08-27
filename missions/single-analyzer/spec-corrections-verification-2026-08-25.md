# Spec corrections verification (2026-08-25)

Verifies four corrections the user raised against `spec-composite-metric-and-optimizer-t2.md`
(CT1, CT2, CT3, CT5). Every claim below is verified against the current worktree HEAD, not copied
from prior reports (though those were consulted for context — see the CT2/CT3 references to
`composite-entry-spec-2026-08-25.md` and `scale-from-zero-and-fallback-trace-2026-08-25.md`).

---

## Question 1 (CT1): `rescaleModelDecisions` consumption; partial scale-from-zero within a role

**Every caller of `rescaleModelDecisions`:** exactly one call site — `internal/engines/allocation/rescale.go:308`, inside `applyRescale`'s `for _, req := range reqs` loop:
```go
d := o.rescaleModelDecisions(ctx, req, constraints, k.accType, targets[modelKey(req)], &freeThisCycle)
decisions = append(decisions, d...)
handled[modelKey(req)] = true
```
Its return value is unconditionally appended to `applyRescale`'s `decisions` slice — no filtering, no nil-check, no merge logic. `append(decisions, nil...)` is a no-op, so an empty/nil `d` for one model contributes nothing and does not affect any other model in the batch. `applyRescale`'s return is consumed once, at `greedy_score_optimizer.go:116`, and appended again at `greedy_score_optimizer.go:181` — same plain-append pattern.

**Does an existing path handle "partial scale-from-zero" (variant A live, variant B zero-replica, same role, combined demand exceeds A alone)?** Yes — this is the ordinary multi-variant-per-role path, not a special case:
1. `AggregateByRole` (`aggregation.go:113-127`) sums `TotalDemand` per role across every `VariantCapacity` with that role, unconditionally — no per-variant replica-count gate on the demand side. Confirmed by an existing test: "should sum demand across multiple variants sharing a role" (`analyzer_test.go:927-935`).
2. The role's `RequiredCapacity` (`applyUniversalThreshold`) is computed from that combined role-level demand — already reflects both A and B's contribution, not just A's.
3. `initTargets` seeds `targets[v] = state.CurrentReplicas` for every variant including zero-replica B. Both `costGreedyRolePick` and `fairShareRolePick` pick a variant to grow by iterating the full variant set for the role, sorted by cost efficiency — with no check on the picked variant's current replica count. B is fully eligible.
4. `buildDecisionsWithOptimizer` iterates the whole `targets` map, producing `ActionScaleUp` for any variant (including B) whenever `target > CurrentReplicas` — no special-casing tied to "was previously zero."
5. `rescaleModelDecisions`'s own `fillRole` does the same (iterates the full variant set for the role, not gated on current replicas).

No test or code path names "partial scale from zero" as a special case anywhere — it's handled as an unremarkable instance of ordinary multi-variant-per-role allocation.

**Verdict:** "Return nil/empty on missing saturation entry" is safe for this case — that guard operates at whole-model granularity (does this model get optimized at all this cycle), not per-variant/per-role. If the saturation entry is present, the model proceeds through the ordinary machinery above, which never distinguishes B's zero-replica status from any other variant. The *only* way B fails to scale up despite real demand is the separate, already-documented role-omission gap (§30/§31) — not this nil-check.

---

## Question 2 (CT2): Is `Result == nil` on a `NamedAnalyzerResult` reachable in production, or only defensive?

Traced the full call chain: `SaturationAnalyzer.Analyze` never returns `(nil, nil)` — its only nil returns are paired with a non-nil error (config-cast failure, context cancellation). `runV2AnalysisOnly` propagates that: `result` is non-nil whenever `err == nil`. `runAnalyzersAndScore` returns early (never reaching `buildNamedResult`) if `runV2AnalysisOnly` errors. Non-saturation analyzer results are filtered (`if result == nil { continue }`) before ever reaching composition. `composeAnalyzerResults` therefore only ever receives non-nil `.result` values, and the single production call site of `buildNamedResult` (`engine_v2.go:176` — confirmed the only one via grep) always passes a non-nil result.

**Verdict:** `Result == nil` reaching the optimizer is **not a valid/reachable production state today** — it would only occur via a bug (a hypothetical future direct call to `buildNamedResult` bypassing the chain) or test-only fixture construction. Every nil-check on `.Result` in `internal/engines/allocation/` is defensive code guarding a state the current producer never actually produces — not dead code by mistake, but also not exercising a reachable branch today. CT2's framing ("a composed result with `Result == nil` is a valid, *checkable* state") is accurate as defensive design; it should not be strengthened to imply this is commonly reached in practice.

---

## Question 3 (CT3): Manual N=1 trace of every loop/reduce over `[]NamedAnalyzerResult`

All 9 functions read in full and symbolically traced for `s = [e]`. Summary:

| Function | No-op for N=1? | Caveat |
|---|---|---|
| `initRoleState` | **Yes, exact** | none |
| `roleBottleneckReplicas` | **Yes, exact** | `max` accumulator harmless — the one candidate is always ≥0 by construction (ceil of a non-negative ratio) |
| `roleAggRemaining` | **NOT unconditionally** | reduces to `max(0.0, state[role])`, not a bare `state[role]` read — only equals the naive identity because `applyUniversalThreshold` upstream already guarantees non-negative `RequiredCapacity`. **A mechanical simplification that drops this clamp is a latent behavior change if that upstream guarantee is ever weakened.** |
| `safeRemovalReplicasForRole` | **Yes, exact** | `math.MaxInt` sentinel always overwritten or discarded via `!found` branch |
| `needsScaleDownForRole` | **Yes, exact** | three-way outcome matches a direct boolean expression exactly |
| `applyAllocation` | **Yes, exact** | pure mutation, no accumulator |
| `applyDeallocationForRole` | **Yes, exact** | pure mutation, no accumulator |
| `fairShareValue` (primary branch) | **Yes, exact** | — |
| `fairShareValue` (fallback branch) | **NOT unconditionally** | same `max(0, ...)` caveat as `roleAggRemaining` |
| `sortVariantsForScaleDown`'s `weighted` closure | **Yes, exact, no caveat** | `+=` of at most one term to a zero start is exact regardless of sign — unlike the `max` cases, addition has no clamping asymmetry |

**Verdict:** 7 of 9 are unconditional, caveat-free no-ops. **Two — `roleAggRemaining`, and `fairShareValue`'s fallback branch — are NOT proven no-ops by their own internal logic alone.** Both rely on a `max(0.0, x)` clamp whose result only coincides with a bare single-value read because of an *external* invariant (upstream non-negativity, enforced by `applyUniversalThreshold`, not by these functions). CT3 must document this dependency explicitly rather than assume it away when simplifying these two.

---

## Question 4 (CT5): Existing demand-side (not just supply-side) estimation for a zero-replica role

**The prior report (`scale-from-zero-and-fallback-trace-2026-08-25.md`) was wrong on its headline claim.** It found and quoted the relevant function in its own §2.3, but its top-level conclusion ("no demand-side fallback exists anywhere in the codebase — only capacity") directly contradicts its own detailed finding.

**The mechanism: `estimateSchedulerQueueDemand`** (`internal/engines/analyzers/saturation_v2/analyzer.go:723-767`). It is a **separate, independent function** from the `CapacityKnowledgeStore`/`aggregateByVariant` supply ladder — called once per `Analyze` invocation, computing demand, not supply:

- `activeRoles` (built from the dense, every-discovered-variant `variantCapacities` list) includes a role even when its only variant has zero ready replicas.
- `computeModelWorkloadAverages(replicaMetrics)` computes `avgInput`/`avgOutput`/`avgHitRate` from whichever replicas are actually reporting metrics model-wide — not scoped to the zero-replica role's own (nonexistent) replicas. This is genuinely "estimate from other roles' live data."
- The queue-depth signal (`sq.QueueSize`/`sq.QueueBytes`) drives `inputTokens`/`outputTokens`, attributed per-role via `byRole[domain.RoleDecode] = inputTokens + outputTokens` etc. — a real, nonzero demand number assigned to a zero-replica role, before that role has any replicas of its own.
- This flows into `AnalyzerResult.RoleDemand[role]` → `RoleCapacities[role].TotalDemand` → (`TotalAnticipatedSupply` = 0 for a genuinely zero-replica role) → nonzero `RequiredCapacity` → `initRoleState`'s `pickerState[role]` is positive → `anyRoleNeedsScaleUp` triggers scale-up for that role, **sized from queue evidence alone, with zero replicas of that role currently existing.**

This satisfies the user's framing precisely: sources (a) (other roles'/model-wide live data) and (c) (queue signals) both apply. Source (b) (history) is NOT used by this specific mechanism — history-based estimation in this codebase is supply-only (via `CapacityKnowledgeStore`), exactly as the prior report correctly traced for that separate mechanism.

**Precisely what is and isn't covered (for spec accuracy):**
- **Covered:** a zero-replica role, in a model already recognized as disaggregated, with a nonzero EPP queue — gets a real, nonzero demand estimate.
- **Not covered:** (i) no/empty scheduler queue — returns all-zeros, same gap as the pure-capacity-store path; (ii) model not yet recognized as disaggregated at all — the queue term still folds into model-level `TotalDemand`, just not attributed per-role; (iii) a role missing from `activeRoles` entirely because no `VariantCapacity` for it exists — the discovery-side gap, still genuinely unaddressed by any mechanism (this part of the prior report's finding stands).

**Verdict:** The prior report's "no demand-side fallback exists" is incorrect as a general statement. `estimateSchedulerQueueDemand` is exactly the analyzer-owned demand estimator for zero-replica roles the user described. The narrower gap (discovery-side omission, no queue signal available) remains real and is the part of §30/§31's finding that still stands.
