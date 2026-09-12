# Composite signal — redesign

Working doc. Not a replacement for `spec.md`. Fold back into `spec.md` once settled.
Rule: short. Facts cited (file:line). Open questions stay open. `[USER]` = user decided.

---

## 1. Facts (verified against code)

**There are exactly 3 analyzers: `external`, `saturation_v2`, `throughput`.**

### 1.1 Per-analyzer: ReplicaCount, PRC, Demand — corrected, all 3 checked directly

| | ReplicaCount | PRC | Demand |
|---|---|---|---|
| **external** | `CurrentReplicas − PendingReplicas` (raw k8s) `analyzer.go:171` | config constant `body.Threshold`, not measured `:180` | 1 PromQL query, total only `:164` |
| **saturation_v2** | `CurrentReplicas − PendingReplicas` (raw k8s, **same formula as external**) `analyzer.go:661,673` | `median(ownCapacities)` over ALL `ReplicaMetrics` rows for the variant, minus bridges — no Ready filter `:708-727` | `Σ rc.ReplicaDemand` over ALL rows incl. bridges `:715` |
| **throughput** | `nKV` = count of rows with `TotalKvCapacityTokens > 0` (real signal: KV cache allocated — see §1.3, not a naive gap) `:319,676-691` | `sum/n` over the same set `:691` | `computeDemand`, unfiltered `variantMetrics` (not checked line-precisely yet) |

**Correction to an earlier wrong claim in this doc:** saturation's `ReplicaCount` is NOT
built from counting monitored rows — verified at `analyzer.go:673`,
`replicaCount := readyCount`, and `readyCount := vs.CurrentReplicas - vs.PendingReplicas`
(`:661`). It's the same k8s-status arithmetic as `external`. Only throughput's
`ReplicaCount` (`nKV`) is actually a count of `ReplicaMetrics` rows.

**None of the 3 analyzers read `ReplicaMetrics.Ready` anywhere** (grepped directly —
zero hits in any analyzer file for that field; the field is set once at collection,
`collector/replica_metrics.go:1148`, and never consumed after that).

### 1.2 What `own`/bridge filtering actually is — corrected

`ownReplicas` (saturation, inline loop `analyzer.go:711-724`) and `ownReplicasOnly`
(throughput, named func `analyzer.go:701`) both filter on **`FromWarmPool`** — own-pod vs.
borrowed-bridge-pod. This is **not** a readiness filter; it's a separate axis. Same
condition, implemented twice (one function, one inline loop) — a duplication.

- saturation: `ownReplicas`/`ownCapacities` (bridges excluded) feeds **PRC only**
  (`median(ownCapacities)`). Demand (`totalDemand`) sums **every** row, bridges included
  (`:715`, comment states this is deliberate — a bridge's traffic is real).
  `ReplicaCount` uses neither — it's the k8s formula above, computed independently.
- throughput: `ownReplicasOnly` filters `input.ReplicaMetrics` once, upstream, before
  `variantMetrics`/`healthyMetrics` are built (`:248`) — so throughput's PRC/ReplicaCount
  (`nKV`) already exclude bridges by construction, unlike saturation where the exclusion
  happens per-field.

### 1.3 Which output field each filter actually touches — traced precisely

| | readiness (k8s Ready/Pending) | `FromWarmPool` (bridge) |
|---|---|---|
| **saturation ReplicaCount** | YES — it IS the k8s ready count (`:661,673`) | no effect — computed independently of `replicas` |
| **saturation PRC** | no effect — `ownCapacities` never checks readiness | YES — bridges excluded from the median (`:727`) |
| **saturation Demand** | no effect | no effect — every row summed, bridges included (`:715`, deliberate) |
| **throughput ReplicaCount (`nKV`)** | no direct check — gated only by `TotalKvCapacityTokens>0`, a real (not proxy) signal that the engine's KV cache is allocated (`collector/replica_metrics.go:755-767`) — but this and k8s-Ready are two independently-timed signals with no cross-check | YES — bridges pre-excluded via `ownReplicasOnly` before `nKV` is computed |
| **throughput PRC** | same as `nKV` — computed in the same loop, same filtered set (`:676-691`) | same — pre-excluded |
| **throughput Demand** | not yet traced line-precisely | not yet traced line-precisely |

**Key structural difference:** in saturation, `ReplicaCount` and PRC come from two
disconnected computations (k8s arithmetic vs. a median over monitored rows) — they can
disagree with nothing reconciling it. In throughput, `ReplicaCount` (`nKV`) and PRC are
computed together, in one loop, over the same filtered set — coupled by construction.

**[USER] correction on `nKV`:** not an oversight that includes booting replicas — it is
*attempting* to count replicas actually serving, via a real signal (KV cache configured),
not a proxy. The unresolved gap is narrower than "no guard at all": it's that
KV-cache-configured and k8s-Ready are two independently-timed signals with no code-level
guarantee they align.

### 1.4 SC traced end to end

`SC = max(0, TotalSupply − Demand/scaleDown)` (`engine_v2.go:550-554`).
`TotalSupply = Σ ReplicaCount(v) × PRC(v)` (`aggregation.go:51-57`) — uses `ReplicaCount`,
**not** `AnticipatedSupply`'s ready+pending sum.

- **Pending replicas**: never in `TotalSupply` → never in SC (they're in
  `AnticipatedSupply`, which feeds RC, not SC).
- **Bridges**: excluded from `ReplicaCount`/PRC on both analyzer paths (§1.3) → never in
  `TotalSupply` → never in SC's supply side. But bridges ARE in `Demand` (both analyzers
  sum every row) → bridges raise the number SC subtracts, without raising the number SC
  starts from.
- Net: **SC reflects only "ready, non-bridge" capacity, minus demand that includes
  bridge-served traffic.**

**Cross-SO leakage**: both saturation_v2 and throughput group `ReplicaMetrics` by
`VariantName` (`byVariant` map, `saturation_v2/analyzer.go:632-633`,
`throughput/analyzer.go:92,282`) before any of the above — so another SO's pods are
excluded by that grouping key. Whether the `VariantName` tag itself is trustworthy is a
separate, unchecked discovery/collector-level question.

### 1.5 CurrentReplicas/PendingReplicas — computed once, upstream, shared by all analyzers
`variantmeta/discovery.go:79-84`:
```
currentReplicas := scaleTarget.GetStatusReplicas()  (fallback: scaleTarget.GetReplicas())
readyReplicas   := scaleTarget.GetStatusReadyReplicas()
pendingReplicas := currentReplicas - readyReplicas   (clamped ≥ 0)
```
Read directly from the k8s scale target's status, once per variant, in `Discover()` — not
per-analyzer. Flows into `domain.VariantMetadata` → `VariantReplicaState`, the same shared
input every analyzer receives (`saturation_analyzer.go:220-228`).

---

## 2. Call stack — current (as built)

```
runAnalyzersAndScore                          [engine_v2.go]
  → per analyzer: build NamedAnalyzerResult    (analyzer-specific ReplicaCount semantics differ)
  → buildComposite(namedResults)               [steadystate/composite.go:29]
      → findSaturation(namedResults)           :163  (lookup #1)
      → unionOfVariants(namedResults)          :193
      → per variant v:
          → representativeVariantCapacity(namedResults, v)  :216
              → inline sat lookup              :217  (lookup #2)
          → allocation.ResolveSO(namedResults, v)            [allocation/composite_decision.go]
              → per analyzer e in namedResults:
                  → if e.Name == sat            :85  (lookup #3, special-cased)
                  → aggregation.AggN([e.Result], v)          [1-element, dead]
              → inline others/sat combine       (real aggregation, duplicated)
          → aggregation.PRCCom(sat.Result, role, N, ok)
      → maxScore(namedResults)
  → buildCapacities(composite)                 [engine_v2.go:904]
```

Not yet reviewed for correctness: `analyzer_helpers.go`, `query_api.go`,
`cost_aware_optimizer.go`, `rescale.go` — see `STATE.md`.

---

## 3. How the optimizer uses this [USER]

1. Optimizer decides on Demand and PRC.
2. Demand that matters: RC/SC, per role (prefill/decode/both).
3. Decision is in replica/GPU counts → the number that matters is ~always Demand/PRC.
4. Optimizer also needs cost + other metadata.
5. CompositeSignal's job: **one Demand, one PRC (+ metadata) per dimension — collapsed
   across analyzers only.** Not collapsed across SOs. Not collapsed across models.

**Granularity, per [USER] correction, verified against code:**

Demand is per model_id, reported per role by the analyzer directly:
- `AnalyzerResult.TotalDemand` — role "both"/"" (`analyzer.go:117-119`)
- `AnalyzerResult.RoleDemand[role]` — "prefill"/"decode" (`analyzer.go:121-126`)

RC and SC are 3 values per analyzer (model-level + one per role), **derived**, not
analyzer-emitted — same formula at every scope, verified `engine_v2.go:536-574`
(`applyUniversalThreshold`):
```
RC = max(0, Demand/scaleUp − AnticipatedSupply)
SC = max(0, Supply − Demand/scaleDown)
```
This computation happens at the Engine via a helper — **not** something the composite
redoes from scratch. Open question: does the composite call the same helper on its own
combined (Demand, Supply, AnticipatedSupply), or does it combine RC/SC after each analyzer
computes them?

Supply and AnticipatedSupply, verified `aggregation.go:50-69`:
```
Supply(analyzer)            = Σ_v ReplicaCount(v) × PRC(v)
AnticipatedSupply(analyzer) = Σ_v (ReplicaCount(v) + PendingReplicas(v)) × PRC(v)
```
Computed **per analyzer**, over that analyzer's own `VariantCapacities`. `ReplicaCount` is
analyzer-specific (§1) — confirms "each analyzer may see a different N" [USER]: Supply and
AnticipatedSupply inherit that difference directly, since they're built from it.

PRC is per SO (= model, role, GPU, variant metadata): `VariantCapacity.PerReplicaCapacity`.

**The 3 real inputs, per [USER]:** Demand, (Total) Supply, Anticipated Capacity — each
reported/computed by every analyzer, in its own unit, per role. CompositeSignal's job is to
combine these **across analyzers**, per role (and PRC per SO) — not to re-derive RC/SC from
zero.

---

## 4. Open decision: what does "combine across analyzers" mean, per quantity?

Not decided. For Demand(model,role), Supply(model,role), AnticipatedSupply(model,role), and
PRC(SO):
- combination rule when analyzers agree/disagree,
- what to do when one analyzer lacks a value,
- does combined (Demand,Supply,AnticipatedSupply) feed the *same* `applyUniversalThreshold`
  formula to get composite RC/SC, unchanged?
- whether analyzers' units differ and need reconciling first (normalization — out-of-scope
  per original spec; still true?).

---

## 5. Parked

- §1.1-1.4 traced HOW each analyzer computes its own values today, and WHERE readiness/
  bridge filtering does and doesn't reach — not yet decided: does the composite need a
  disagreement-resolution POLICY (e.g. reconcile ReplicaCount across analyzers), or does
  §4's per-quantity combination rule make this moot by construction?
- Disagreement: log/surface it, or not?
- Duplicated lookups/aggregation (§2's call stack — `findSaturation`/inline sat-lookup x3,
  `AggN` dead-called on 1 element): fix regardless of §4, or a symptom of the unclear
  design this whole doc exists to resolve?

### 5.1 From the earlier line-by-line code review (`code-review-notes.md`) — still open,
directly relevant to this redesign, not yet folded in:
- §7/§9.6: `D_sat`/`satDemand` naming assumes sat as the demand source; intent was a
  canonical cross-model unit (this doc's §3/§4 questions bear on this directly).
- §9.6: `HasUsableCompositeSignal` conflates "sat itself healthy" (model-level) with "this
  SO has a signal" (per-SO) — two real, distinct checks, not one.
- §9.1/§9.2: sat CAN be disabled via config but the engine never checks it for sat
  specifically; `AggN`'s correct shape is ALL analyzers symmetric, sat-fallback layered on
  top as a narrow special case, not a structural branch during collection.
- STATE.md's Known Issues: the canonical-composite-demand direction (moving away from
  anchoring the demand unit on "sat" specifically) — this doc's whole §3/§4 is that
  discussion, now at the decision level instead of a naming complaint.

### 5.2 Not yet reviewed at all this session (still true, per STATE.md)
`query_api.go`, `analyzer_helpers.go`, `cost_aware_optimizer.go`, `rescale.go`,
`constants/metrics.go`, `docs/reference/cycle-log.md`, all test files. The line-by-line
review of `composite.go`/steadystate wiring was INTERRUPTED by this redesign discussion —
`code-review-notes.md` was not appended to this session; §9 (2026-09-09) is still its last
entry.

---

## Revision log
- 2026-09-12: created; rewritten short; §3 replaced with user's optimizer-usage explanation
  and granularity correction (aggregate across analyzers only, not SOs or models).
- 2026-09-13: fixed duplicate "1.3" heading (renumbered to 1.5); §5 updated — disagreement
  question narrowed to what §1 already answered; added §5.1 (open items from
  code-review-notes.md that bear directly on this redesign) and §5.2 (files still
  unreviewed, review interrupted by this redesign discussion, not resumed).
