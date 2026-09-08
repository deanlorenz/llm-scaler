# composite-analyzer — mission spec (draft v4)

**Status:** DRAFT v4 — revised after user review #3; awaiting review #4 (2026-09-08).
**Mission:** composite aggregation calculation. Spinoff of `single-analyzer`.
**Branch/worktree:** `composite-analyzer` @ base `upstream/main` `4db060e2`.
**Role:** mission owner.

Everything marked **[ASSUMPTION]** is my own resolution of something the user did not
explicitly settle. Each is flagged for confirm/overturn. Everything marked **[USER]** is a
direct instruction and is not mine to revisit.

---

## 1. Mission definition

Build the **composite aggregation**: reduce the `[]NamedAnalyzerResult` that
`runAnalyzersAndScore` already produces into the single `NamedAnalyzerResult` the optimizer
consumes as `CompositeSignal`, such that **every** enabled analyzer's demand influences the
result — not just saturation's.

This is `single-analyzer`'s **CT7** ("engine-side reduce", née "PR #2"), lifted into its own
mission, with three deliberate departures from CT7's recorded design (§3).

### In scope
- **`N(SO)` — the replica count needed for one SO to cover its role's entire demand — is the
  single aggregated quantity** **[USER]**. Everything else derives from it (§5.3). Aggregators are
  named for the quantity, never the operation (`Agg_N`, not "max") **[USER]** (§4.5), extending the
  existing `internal/engines/aggregation/` package (§2.2).
- **Demand is unchanged:** `D_com[role] == D_sat[role]`, with `D_sat` *defined* as 100% coverage;
  `PRC_com(SO) = D_sat[role(SO)] / N_com(SO)` **[USER]** (§4.4).
- **A consistent query API** over the composite — coverage, missing coverage/capacity, replicas or
  GPUs to close a gap — answered by explicit helpers against an allocation state **[USER]** (§5.5).
- **Zero-guards throughout** — coverage/`N` is meaningless when PRC or demand is zero **[USER]**
  (§2.5).
- **Full observability**, reusing the *same* log and metric functions as any analyzer result
  **[USER]** (§6).
- A new composite **name**, and repairing whatever that breaks **[USER]** (§8).
- **Exactly one full `NamedAnalyzerResult`** into the optimizer — the composite, not saturation
  **[USER]** (§6.1).

### Out of scope (this mission)
- **Normalization** — a shared cross-analyzer demand unit, ideally "per X requests in queue"
  **[USER]**. Deferred: *"Do composition first."* A previous attempt exists on
  `single-analyzer-normalize` and is worth learning from but not building on (§2.6). Until a
  shared unit exists, analyzers meet only in unit-free coverage/replica space (§4.2).
- **Reorganizing `NamedAnalyzerResult`** — legacy, kept as-is; a different mission **[USER]**.
- **`Score` weighting** — needs revisiting **[USER]** (§7). Direction is always the scoreless
  `max`/`min`; only the magnitude may be score-influenced, and the magnitude rule is unsettled.
  `score ≡ 1` today, so direction-only is exactly today's behavior and nothing is blocked.
- CT6's coverage-fraction normalization (`TotalDemand = 1.0`). Not merely deferred — see §2.6,
  it hit real bumps and this mission does **not** build on it.
- CT1b (nil-saturation guard), CT4 (fairness definition). Untouched.
- Changing PRC ownership: saturation owns PRC (per SO — §2.4).

### Phase boundary
**[USER]** "Right now we plan. But the mission is broader." → this document is the deliverable
for the current phase. Implementation is in-mission but follows spec approval.
**[USER]** "Until we have a spec all your work is in your `.session`." → no code, no writes
outside this worktree, until the spec is approved.

---

## 2. Ground truth on the base branch

Verified by reading `composite-analyzer` @ `4db060e2`, not inherited from the parent mission's
docs. This matters: several parent-mission documents describe a state that is **not** upstream.

### 2.1 What exists
| Thing | State on my base |
|---|---|
| `runAnalyzersAndScore` (`engine_v2.go:102`) | returns `[]allocation.NamedAnalyzerResult` |
| `CompositeSignal` assignment | `engine_v2.go:797` — `CompositeSignal: namedResults[0]` (single site) |
| `buildNamedResult`, `buildCapacities` | present (`engine_v2.go:836`, `:850`) |
| `composeAnalyzerResults` | **does not exist** — deleted by CT3b/CT6 as a vacuous pass-through |
| `normalizeToCompositeUnits` | **does not exist** — zero hits |
| `multi_backup/` | present, 3 files, `//go:build ignore` (never compiled) |
| `hasSaturationResult` (`engine_v2.go:722`) | `req.CompositeSignal.Name == domain.SaturationAnalyzerName && req.CompositeSignal.Result != nil` |

`CompositeSignal` consumers (non-test): `cost_aware_optimizer.go:59,246`,
`greedy_score_optimizer.go:117,156`, `rescale.go:344,372,528`, `variant_records.go:79`,
`engine_v2.go:722`.

### 2.2 Existing helpers — the real source of truth **[USER]**

**[USER]** "Don't rely too much on previous analysis, I don't fully trust it. The source of truth
is still the pre-single-analyzer upstream." So this section is built from reading the upstream
code on this base, not from the parent mission's summaries.

#### `internal/engines/aggregation/` — already exists, already the right shape
A package of **pure aggregation helpers named for what they aggregate** — exactly the style §5
requires. Read from source; not mentioned anywhere in the parent mission's docs:

| Helper | What it aggregates |
|---|---|
| `SumTotalDemand(vcs)` | `Σ_v vc.TotalDemand` |
| `SumTotalSupply(vcs)` | `Σ_v replicas × PRC` |
| `SumTotalAnticipatedSupply(vcs)` | `Σ_v (replicas + pending) × PRC` |
| `DemandByRole(vcs)` | role → summed demand (demand-only projection of `AggregateByRole`) |
| `AggregateByRole(vcs)` | role → `ScopeTotals{TotalSupply, TotalAnticipatedSupply, TotalDemand}` |
| `IsDisaggregated(vcs)` | any variant with a role other than `""`/`both` |

It canonicalizes `"" → domain.RoleBoth` in one place, and its doc comment states the linearity
invariant the optimizer depends on. **This is the package this mission extends** — new
cross-analyzer aggregations belong here (or beside it), in the same naming style, rather than as
new inline arithmetic elsewhere.

Both real analyzers already use it: `saturation_v2.aggregateRoleDemand` and
`throughput.aggregateRoleDemand` both call `IsDisaggregated` + `DemandByRole`.

#### `multi_backup/` — of limited value
`analyzer_helpers_multi.go` (`//go:build ignore`) holds the pre-CT3b multi-entry helpers. Its
genuinely reusable *logic*: `ResultIsInformative` (non-nil `Result` + ≥1 non-sentinel `Reason`),
`prcForVariant`, and the Live-gating / all-agree / `liveCount > 0` safety-floor patterns in
`safeRemovalReplicasForRole` / `needsScaleDownForRole`.

But its aggregations operate on **optimizer picker state**, not on analyzer results, and its
`RolePairedState []map[string]float64` is indexed `[analyzerIndex][role]` — a shape PR #34
deliberately collapsed. **[ASSUMPTION] A1** — port the gating logic and the min/max patterns;
do **not** attempt to restore its structures. It cannot compile as-is (`//go:build ignore`,
`package allocation`, depends on unexported `variantRecord`), and un-ignoring it would collide
with the single-entry helpers that replaced it. `multi_backup/` is left untouched.

### 2.3 Where demand actually lives — corrected **[USER]**

**[USER] correction:** "`RoleDemand` map does not always store prefill/decode AND both. It has a
different place to store both." Verified in `internal/domain/analyzer.go` and both analyzers:

```
AnalyzerResult.TotalDemand  float64              -- model-level demand
AnalyzerResult.RoleDemand   map[string]float64   -- per-role; NIL when not disaggregated
```

- `RoleDemand` is **nil** for a non-disaggregated model (`aggregateRoleDemand` returns nil when
  `!IsDisaggregated`). The "both" demand then lives in **`TotalDemand`**, not under a `both` key.
- When disaggregated, `RoleDemand` is keyed by the roles the variants actually serve.
- `VariantCapacity.Role` is `"prefill"` | `"decode"` | `"both"` | `""` (empty ⇒ `both`).

So there are **three demand values** per analyzer — `both`, `prefill`, `decode` **[USER]** — but
they are *not* three entries in one map: `both` may live in `TotalDemand` with `RoleDemand` nil.
Any code reading per-role demand must go through one accessor that handles both layouts.

**[ASSUMPTION] A13 — a single `demandForRole(result, role)` accessor**, in the `aggregation`
package, that returns `(value, present bool)` and encapsulates: nil `RoleDemand` ⇒ `both` reads
from `TotalDemand`; empty role canonicalized to `both`; a role absent from a non-nil map is
**not present** (distinct from present-and-zero). Every aggregation reads demand only through it.
The normalization attempt already needed such a helper (`demandForRole` exists on that branch) —
independent corroboration that this is the right seam.

### 2.4 Demand and PRC are independent **[USER]**

**[USER], and this governs the whole design:**

- **PRC is per SO** (implying model, variant, role).
- **Demand is per (model, role)** — three values: `both`, `prefill`, `decode`. It **does not
  depend on which SOs exist**. Even if a role has one SO today, it could have several; SOs are
  added and removed, and demand per role does not change because of that.
- The converse also holds: **`PRC(SO)` is not tied to demand.** An SO can have a real PRC while
  `demand(role(SO)) == 0`.
- **True for every analyzer, including saturation.**

Consequences this spec must honor:
1. Never derive a role's demand from its SOs, nor treat per-SO demand as authoritative for a
   role. (`VariantCapacity.TotalDemand` exists and is summed by `SumTotalDemand`, but the
   **analyzer owns** role attribution; `RoleDemand`/`TotalDemand` is the authority.)
2. Never assume an SO's existence implies demand for its role, or that demand implies an SO.
3. Adding or removing an SO must not change any role's demand.

### 2.5 Coverage is undefined at zero — guard everywhere **[USER]**

**[USER]** "The coverage value (PRC/demand) is meaningless when either PRC or demand are zero.
Every calculation needs to guard against these cases."

```
coverage(A, SO) = PRC(A, SO) / D(A, role(SO))     undefined if PRC == 0 or D == 0
N_full(A, SO)   = D / PRC                          undefined if PRC == 0
```

**[ASSUMPTION] A14 — represent "no coverage signal" explicitly** rather than encoding it as a
number. Return `(value, ok)` (or a small `coverageResult` type) from every coverage/replica
helper, so an undefined coverage cannot silently enter a `min`/`max` as `0` or `+Inf`. `min` over
a set containing a spurious `0` would wrongly veto scale-down; `max` over a spurious `+Inf` would
wrongly demand infinite replicas. **A skipped contribution must not be a magic number.**

**Corroboration from the deferred normalization attempt (see §2.6):** its commit `77f21355`
("let demand=0 flow through `normalizeToCompositeUnits` unchanged") fixed exactly this class of
bug — three sites unconditionally overwrote a demand field to `1.0` even when real demand was
`0`, producing a phantom demand and an idle-model `Utilization` miscompute. The rule that branch
settled on, which this spec adopts: **`demand == 0` flows through as `0`; it is never manufactured
into a `1.0`.** Downstream consumers (the GPU water-fill's `roleDemandGPUs`, per-analyzer metrics,
RC/SC) already treat `demand <= 0` as an ordinary zero, so a real `0` is safe everywhere — a
phantom `1.0` is not.

### 2.6 Learn from the deferred normalization attempt **[USER]**

**[USER]** "A previous normalization attempt exists in `single-analyzer-normalize` — it fixed
some things but hit some bumps, so I decided to defer it for now. Do composition first. But you
may be able to learn from it."

Read from branch `single-analyzer-normalize` (**not** inherited — that work is deferred and this
mission does not build on it). Three transferable lessons:

1. **Deep-copy is mandatory** (`da0e1ee8`). A plain value copy of a `NamedAnalyzerResult` aliases
   `Result`, `RoleCapacities` and `RoleSpare`, so building the composite from saturation's entry
   **silently mutated saturation's own result**. The composite must deep-copy every reference-typed
   field it inherits. **[ASSUMPTION] A15** — with a test asserting the source entry is byte-identical
   after composition.
2. **Zero-demand must flow through** (`77f21355`) — see §2.5.
3. **Naming precedent** (`da0e1ee8`): that branch introduced `allocation.CompositeSignalName` and
   emitted one extra `analyzer-result` log line for `"CompositeSignal"`, documenting its unit as
   `%` (coverage) alongside `saturation`=tokens and `throughput`=tokens/sec. Our composite is in
   **sat units, not `%`**, so the log's unit column differs — but the *pattern* (composite gets its
   own name and its own log line) is settled and reusable (§8).

Also noted from that branch's ledger: a `fairShareValue` **priority/Score conflation** finding.
Relevant to §7 — flagged, not resolved here.

---

## 3. Departures from CT7's recorded design

All **[USER]**, overturning `compose-logic-plan.md`'s resolutions:

| CT7/p3 said | This mission |
|---|---|
| Q3: composite keeps `Name = SaturationAnalyzerName` ("zero-risk option") | Composite gets a **new name**; the optimizer's input is explicitly not sat (§6, §8) |
| Q4: composite inherits sat's `Score`; non-sat affects RC/SC only | `Score` is the analyzer's **relative weight, applied during aggregation** (§7) |
| Reduce over already-built `RequiredCapacity`/`SpareCapacity` in sat units | Reduce **per SO in unit-free coverage/#replicas**; demand per (model, role); convert back once at the end (§4) |

Q1 (per-variant `TotalDemand`) is **dissolved**, not answered: per-role demands are independent
numbers, and the cross-role coverage rule is the only relation between role and model level
(§5.4) — there is no model-vs-role figure to reconcile. Q2 (`RoleDemand` absent on a non-sat
analyzer) is re-derived in §5.4/A4 on that corrected structure.

---


## 4. What is aggregated, and in what space

### 4.1 The rejected v1 conversion — kept as a record
v1 proposed `demand_sat_equivalent(A_i, SO) = D_i(SO)/PRC_i(SO) × PRC_sat(SO)`. **[USER]** rejected
it, correctly, on two independent grounds:

1. **Circular.** It routes every analyzer's contribution through saturation's PRC estimate — but
   the inaccuracy of `PRC_sat` is *the very reason the other analyzers exist*. A bad `PRC_sat`
   corrupts exactly the signals meant to compensate for it.
2. **Not a unit.** `PRC_sat` varies per SO, so the same analyzer's demand converts by a different
   ratio per SO. A factor that varies over the aggregation domain is not a unit; the result is
   denominated in nothing coherent.

Recorded so it is not reintroduced. The verification lesson: for a conversion, check the factor is
**constant over the domain being aggregated**, not merely that the algebra is self-consistent.

### 4.2 No cross-analyzer demand conversion exists yet **[USER]**

Genuine conversion needs a **shared demand definition** — a request count, ideally **"per X
requests in queue"** (preferred over requests-in-system) **[USER]**. Each analyzer would compute
its demand for that same reference workload. **Deferred**; not this mission.

Until then analyzers meet **only** where their units have already cancelled:

```
coverage(A_i, SO) = PRC(A_i, SO) / D(A_i, role(SO))      unit-free, undefined at 0 (§2.5)
N_full(A_i, SO)   = D(A_i, role(SO)) / PRC(A_i, SO)      unit-free, undefined at PRC=0
```

Each divides a demand by a capacity **from the same analyzer**, in that analyzer's own units, so
the units cancel. That — not any conversion factor — is what makes them comparable.

Note the demand term is `D(A_i, role(SO))`: the **role's** demand (via A13's accessor), looked up
by the SO's role. Demand stays per (model, role) per §2.4; only the *quotient* is per SO.

### 4.3 Request shape — no single-model assumption **[USER, v4 correction]**

**[USER] correction:** "There is no assumption of one model! In the same round you may have
different request shapes in different models."

v3 wrongly claimed a consistent-shape-per-model assumption *licensed* the aggregation. It doesn't,
and no such assumption is needed. The real reason aggregation is safe is **structural**:

> **Every aggregation is either per SO (PRC) or per model (demand).** Neither ever crosses a model
> boundary, so there is no shape comparison to make. **[USER]**

- `PRC` is per SO — within one SO, one shape.
- `D` is per (model, role) — within one model.
- Different models may have entirely different request shapes **in the same round**; irrelevant,
  because nothing in the composition compares across models.

**Where shape *does* matter — and it is not here [USER]:**
- **Across models, in the optimizer**, we **cannot** assume the same request shape. That is the
  optimizer's problem (fair-share, GPU allocation across models), not the composite's.
- **Estimating `PRC` at zero demand:** by definition relies on **measured values from previous
  rounds** — there is nothing to measure this round.
- **Estimating `PRC(SO1)` when demand is nonzero but SO1 has no measurements this round** (e.g.
  SO2 is the one serving): the analyzer may use knowledge from SO2 — e.g. average tokens/request.

All three are **analyzer-internal concerns**. The analyzer owns `PRC` estimation and its
provenance (`VariantCapacity.Reason` already records which estimation path produced it: `P0-store`,
`P1-obs`, `P2-hist`, `P3-k2`, `P4-k1`, `no-data`, `error`). The composite consumes `PRC` as given.

**[ASSUMPTION] A16' — the composite does not second-guess PRC provenance**, but it *does* respect
the no-signal sentinels (`no-data`/`error`) via `ResultIsInformative`, and it never treats an
estimated PRC differently from a measured one. If PRC-confidence should modulate aggregation, that
is the `Score` question (§7), not a separate mechanism.

### 4.4 The composite is `N`; demand is unchanged **[USER, v4 correction]**

**[USER] correction:** "Why `composite.D(r) = D_sat(r) × cov_sat(r)/cov_composite(r)` — this kills
the composite signal. `D_com[role] == D_sat[role]`, i.e. `D_sat` is defined as 100%.
`PRC_com = D_sat/N_com`. `N_com` is derived directly from the coverage numbers."

v3 had this inverted: it scaled *demand* by a coverage ratio, which drains the composite of the very
information it exists to carry. The correct construction:

```
D_com[role]     = D_sat[role]                          demand is UNCHANGED — D_sat is the 100% reference
N_com(SO)       = Agg_N over analyzers of N_i(SO)       the composite signal (§5.2)
PRC_com(SO)     = D_sat[role(SO)] / N_com(SO)           per-SO capacity implied by the aggregate
```

So the composite is expressed in sat units **not by converting demand**, but by holding demand
fixed at `D_sat` and letting the aggregated replica count `N_com` determine `PRC_com`. Everything
else derives from those (§5.7).

Why this is right, and why v3 was wrong:
- `D_sat` **is** the unit. Defining it as 100% coverage makes "sat units" a *definition*, not a
  conversion — so §4.1's circularity cannot recur: no analyzer's contribution passes through
  `PRC_sat` at all.
- The composite's information content lives entirely in `N_com`. v3's ratio-scaling moved the signal
  into demand and left `N` implicit — inverting where the meaning sits.
- Sat-only: `N_com = N_sat` ⇒ `PRC_com = D_sat/N_sat = PRC_sat` exactly. Identity by construction,
  no special-casing.

Zero-guards (§2.5) apply directly: `PRC_com(SO)` is undefined when `N_com(SO) == 0` (nothing
needed) or `D_sat[role] == 0` (no demand). Both are ordinary states, not errors — represented via
A14, never as a division.

### 4.5 Aggregation is explicit about what it aggregates **[USER]**

**[USER] correction:** "Aggregation is not max or min — it should be explicit on what it aggregates
(e.g. `Agg_N(...)`, `AggPRC(...)`) and use a helper to do it. The base computation, for now, can be
max/min or weighted mean."

So `max`/`min` is an **implementation detail of a named aggregator, not the interface**:

```go
// Agg_N aggregates the per-SO replica requirement across analyzers.
func Agg_N(entries []Entry, so SORef) (n float64, ok bool)
```

The **combination rule inside** `Agg_N` is currently `max` (or a weighted variant — §7), and is
expected to change. Callers name the *quantity*, never the operation, so a rule change is contained.

**[ASSUMPTION] A18 — one aggregator per aggregated quantity**, each: named for its quantity;
returning `(value, ok)` per A14; skipping non-contributing analyzers; and taking its combination
rule from one shared, swappable place so `Agg_N` and any sibling cannot drift apart.

### 4.6 Invariants
- **Sat-only:** one contributing analyzer ⇒ composite numerically identical to today.
  Non-negotiable.
- **Direction:** regardless of `Score`, scale up/down in the direction the scoreless `max`/`min`
  would indicate **[USER, §7]**; only the *amount* may be score-influenced.
- **Zero-safety:** nothing computed or compared where PRC, demand, or `N` is zero (§2.5); demand
  `0` stays `0`.
- **SO-independence:** adding/removing an SO does not change any role's demand (§2.4).
- **No cross-model aggregation** (§4.3).

---

## 5. The aggregation

**[USER]** Named helpers stating what they aggregate (§4.5), extending
`internal/engines/aggregation/` (§2.2).

### 5.1 Saturation is a fallback, not a floor **[USER, v4 correction]**

**[USER] correction:** "Sat does not participate unconditionally. Only if no other signal, as
fallback. Even then, need to see if we mark the type of fallback clearly."

**This retires the "floor invariant"** that CT7 recorded and that v1–v3 carried through unexamined.
Saturation is not privileged in the aggregation; it is the **fallback when no other analyzer has a
usable signal**.

```
contributors = eligible analyzers with a defined N(SO)          (§5.1.1)
if contributors is empty:  fall back to saturation, and MARK the fallback
else:                      aggregate over contributors — sat included only if itself eligible
```

Consequences, stated plainly because they are load-bearing:
- The composite may be **lower** than saturation alone would demand. That is intended: sat is one
  estimate among several, and the whole point of other analyzers is to correct it.
- Sat-only is now a *consequence* of sat being the only contributor, not a special rule.
- `D_sat` remains the **unit** (§4.4) even when saturation does not contribute to `N`. Unit and
  contribution are separate roles — worth stating, since conflating them is easy.

**[ASSUMPTION] A19 — fallback is typed and observable.** Record *which* fallback applied, at least:
`no-eligible-analyzer`, `sat-only`, `sat-fallback-for-this-SO`, `no-signal-at-all`. Carried on the
composite (small — §8's legacy constraint) and logged (§6). **[USER]** "need to see if we mark the
type of fallback clearly" — flagged as open item, since the enumeration is mine, not yours.

#### 5.1.1 Eligibility
```
eligible(i)  ⟺  Result != nil  ∧  ResultIsInformative(i)  ∧  Live(i)
```
Separately, a **contribution is skipped** when its `N` is undefined (§2.5) — a distinct concept from
ineligibility, represented explicitly (A14), never as a magic number.

**[ASSUMPTION] A3 — one eligibility rule in both directions.** A stale analyzer neither raises
demand nor blocks scale-down.

### 5.2 `N(SO)` is the single aggregation signal **[USER, v4 correction]**

**[USER] correction:** "Why both N and coverage? Don't they mean the same? (cov = 1/N)"

They do. `coverage = 1/N` up to the ceiling, so v3's "compute both and cross-check" was **not** a
cross-check — it was one quantity written twice, and its `max N` / `min cov` were the same
operation. v3's §5.2/§5.3 are deleted, not amended.

**[USER]** "The most natural aggregation signal per SO is `N(SO)`" — the replica count needed for
this SO to cover the entire demand of its role:

```
N_i(SO) = D_i[role(SO)] / PRC_i(SO)        per analyzer i, unit-free
N_com(SO) = Agg_N over contributors of N_i(SO)
```

Unit-free because each divides a demand by a capacity **from the same analyzer** (§4.2). Demand is
read via A13's accessor; `role(SO)` per §2.4.

**[ASSUMPTION] A2 — quantize once.** `N` stays continuous through aggregation; `ceil` only where a
replica count is finally needed. Aggregating pre-`ceil`ed values double-rounds.

### 5.3 Everything else derives from `N` **[USER]**

**[USER]** "Everything else can be derived from this number." The chain, in order:

```
D_com[role]  = D_sat[role]                                        unchanged (§4.4)
PRC_com(SO)  = D_sat[role(SO)] / N_com(SO)
Supply       = Σ_SO ReplicaCount(SO)            × PRC_com(SO)
Anticipated  = Σ_SO (ReplicaCount+Pending)(SO)  × PRC_com(SO)
RC[role]     = max(0, D_sat[role]/scaleUp   − Anticipated[role])
SC[role]     = max(0, Supply[role]          − D_sat[role]/scaleDown)
```

The `RC`/`SC` formulas are **the existing ones** on `NamedAnalyzerResult` (verified verbatim in
`optimizer_interfaces.go`) — no new arithmetic, only `PRC_com` substituted for the analyzer's own
PRC. The existing capacity-build step and the `aggregation` package's `SumTotalSupply` /
`SumTotalAnticipatedSupply` / `AggregateByRole` already implement the supply sums.

This is why v3's §5.5 "recompute **and** cross-check derived fields against their own aggregation"
is now largely moot: with a single aggregated quantity there is only one place a value can come
from. What remains worth checking **[USER]** is that the *derivation chain* is self-consistent —
e.g. `ceil(D_sat[role]/PRC_com(SO))` recovers `N_com(SO)`. A mismatch means a bug in the code or in
our understanding, so it is asserted in tests and logged in production (A6', open item).

### 5.4 Model-level coverage — the cross-role rule **[USER]**
```go
// modelCoverageFromRoles returns min(cov(prefill), cov(decode)) + cov(both).
func modelCoverageFromRoles(byRole map[string]float64) float64
```
```
coverage(M) = min( coverage(M, prefill), coverage(M, decode) ) + coverage(M, both)
```
The only relation between role and model level (v1's A5 dissolved). Purely disaggregated ⇒
`cov(both) = 0`; non-disaggregated ⇒ `both` only, demand from `TotalDemand` (§2.3). A role with no
defined coverage must not contribute a spurious `0` to the `min` (A14).

**[USER] asked where this is used** — v3 asserted the rule without saying. Honest answer: **not by
today's optimizer**. `cost_aware_optimizer.go:304` reads `RequiredCapacity`/`SpareCapacity` and
`RoleCapacities[role]` per role; `rescale.go:589` reads `RoleCapacities[role].TotalDemand`. Nothing
reads a single model-level coverage number. So it belongs in §5.5's query API — the natural answer
to "what is this model's coverage?" — rather than being a field on the composite. Flagged: if
nothing consumes it, it should not be computed eagerly.

**[ASSUMPTION] A4 — an analyzer silent about role r does not participate in role r.** Inventing a
split it never expressed would fabricate signal, and per §2.4 role demands are independent.
Distinguishable from present-and-zero via A13's `present` flag.

### 5.5 The query API the optimizer actually needs **[USER]**

**[USER]** "I think we need to understand what aggregations are needed on every access to the
aggregate signal as given to the optimizer in `CompositeSignal`. E.g. given the current
allocations, anticipated allocations, partial allocation, etc. it should be able to say something
about coverage of model, missing coverage, missing capacity, num replicas of particular SO needed
to close the gap, num GPUs of particular type needed to close the gap, etc. I would like to have
explicit helper functions that answer these questions, consistently."

This reframes the deliverable: not only *build* a composite, but give it a **consistent query
surface**. Today those questions are answered by ad-hoc arithmetic scattered across the optimizer —
e.g. `roleDemandGPUs` (`rescale.go:585`) recomputes `ceil(demand/best_PRC) × gpusPerReplica`
inline, picking the most cost-efficient variant itself.

Questions to answer, each a named helper, each taking an allocation state (current / anticipated /
partial) so the same function serves every phase:

| Question | Shape |
|---|---|
| Coverage of a model, given an allocation | `CoverageForModel(state) (float64, ok)` |
| Coverage of a role | `CoverageForRole(state, role) (float64, ok)` |
| Missing coverage (gap to 1.0) | `MissingCoverageForRole(state, role) (float64, ok)` |
| Missing capacity, in `D_sat` units | `MissingCapacityForRole(state, role) (float64, ok)` |
| Replicas of one SO to close the gap | `ReplicasToCloseGap(state, so) (int, ok)` |
| GPUs of one accelerator type to close the gap | `GPUsToCloseGap(state, accType, role) (int, ok)` |
| Is scale-up needed / scale-down allowed | `NeedsScaleUp(state, role) bool` / `MayScaleDown(state, role) bool` |

**[ASSUMPTION] A20 — allocation state is an explicit parameter, not mutable fields on the
composite.** Today `Remaining`, `Spare` and `RoleSpare` are *mutable* fields the optimizer
decrements during allocation (`applyAllocation`, `applyDeallocationForRole`), which is why the
signal cannot answer the same question twice. A pure query API over an explicit state is what makes
answers *consistent*, as requested. But `NamedAnalyzerResult` is **legacy [USER]** and its
reorganization is another mission, so those fields stay; the query API is added **alongside**, and
migrating callers off the mutable fields is scoped explicitly. **Open item — this could balloon;
recommend the API is defined and used by the composite path, with existing callers migrated only as
far as needed.**

**[ASSUMPTION] A21 — one shared gap definition.** Every "close the gap" helper derives from the
same `MissingCapacityForRole`, so replicas-to-close and GPUs-to-close cannot disagree — the
inconsistency risk in today's scattered arithmetic.

### 5.6 Spare capacity — the conservative direction
```go
func Agg_Spare(entries []Entry, role string) (spare float64, ok bool)
func allContributorsAgreeSpare(entries []Entry, role string) bool
```
Combination rule currently `min` (§4.5), plus an all-agree gate mirroring `needsScaleDownForRole` —
one contributor objecting stops a scale-down. Per §5.1 this is over *contributors*, not
unconditionally including saturation.

---

## 6. Observability — the composite is a first-class analyzer result **[USER]**

**[USER]** "Need to verify that all analyzer results are fully observable — both as log entries and
as metrics. The `CompositeSignal` should use the same functions to log its composite value and emit
its metrics. (can reuse work already done in normalize code)."

Two requirements:

1. **Verify existing coverage is complete.** `logAnalyzerResult` (`engine_v2.go:1051`) and
   `recordAnalyzerMetrics` (`:222`, emitting `wva_analyzer_demand` / `wva_analyzer_target`) run over
   the full `namedResults` slice. **[ASSUMPTION] A22** — audit that every field a reader needs is
   actually emitted, and that no analyzer is silently omitted (e.g. a non-live or uninformative
   one). This is a verification task with a possible gap-fix, not an assumption to hand-wave.
2. **The composite reuses the same functions** — not parallel ones. This is exactly what the
   normalization branch did (§2.6, `da0e1ee8`): it *merged* a separate `logCompositeSignal` back
   into `logAnalyzerResult` ("one function, one log key, union of fields") and added one extra
   `analyzer-result` line for `"CompositeSignal"`, with `docs/reference/cycle-log.md` gaining a
   per-analyzer unit column. **Adopt that.** One difference: that branch's composite unit was `%`
   (it normalized to coverage); ours is **`D_sat` units** (§4.4), so the doc's unit column differs.

**[ASSUMPTION] A23** — the composite is emitted as an additional series/line, never replacing any
analyzer's own. Per-analyzer observability stays in each analyzer's own units; the composite is one
more row.

### 6.1 What the optimizer receives **[USER]**

**Exactly one full `NamedAnalyzerResult`. It is NOT saturation. It is the new `CompositeSignal`.**

- **One** — the optimizer keeps PR #34's single-entry shape; it is not handed a slice and does not
  reduce.
- **Full** — every field a consumer reads is populated and consistent via §5.3's derivation chain.
- **Not sat** — even when saturation is the only contributor (§5.1), it is the composite, and it
  carries its own name (§8) and its fallback marker (A19).

### 6.2 Placement

| Option | Placement | Assessment |
|---|---|---|
| **O1** | Inside `runAnalyzersAndScore`, changing the return type to a single value | p3's choice. **Rejected:** ripples into 6+ test files (the parent branch's known compile breakage) and destroys the slice liveness/metrics/logging need. |
| **O2** | Compose at `collectV2ModelRequest:797`, replacing `namedResults[0]` | **RECOMMENDED.** One-line change at the single production assignment site; slice preserved; per-analyzer observability unaffected. |
| **O3** | Inside the optimizer | **Rejected.** Reverses PR #34's name-blind single-entry design. |
| **O4** | Normalize per-entry early, aggregate late | **Rejected.** Would corrupt per-analyzer metrics. |

**[ASSUMPTION] A7 — adopt O2**, with §5.3's derivation inside the compose step.

---

## 7. Score — needs revisiting **[USER]**

**[USER]** "Need to revisit this. The weight should mean **confidence**, but we should understand
what will be done." The three cases given, with `cur` = current replicas:

| Case | TA says | sat says | current | Question |
|---|---|---|---|---|
| 1 | 3 | 5 | 4 | Scale down to 3, up to 5, or stay? |
| 2 | 5 | 10 | 2 | Scale up — but to 5 or 10? |
| 3 | 0 | 10 | 5 | One says drain entirely, one says double |

**[USER] conclusions, which are now constraints, not options:**
- **Weighted mean does not seem right in any of these.** (Case 3 makes it vivid: mean of 0 and 10
  is 5 — exactly `cur` — so a violent disagreement produces "do nothing", the one answer neither
  analyzer supports.)
- **`max` is safe but possibly overly conservative.** (Case 3 → 10: never under-provisions, may
  waste. Case 1 → 5: scales up while an analyzer says scale down.)
- **Regardless of score, scale in the direction the scoreless `max`/`min` indicates.** Direction is
  score-independent; **the amount** may be score-influenced.

**[ASSUMPTION] A9'' — split direction from magnitude.** The only structure consistent with all of
the above:
```
direction = sign implied by scoreless Agg_N (max for up, min-with-all-agree for down)
magnitude = a score-influenced value, clamped to the direction's interval
```
So scores never flip a decision, only temper its size. Case 1 → direction up (scoreless max = 5),
magnitude within `[cur, 5] = [4, 5]`. Case 3 → direction up, magnitude within `[5, 10]`, with
`score(TA)=0`'s "drain" unable to invert it.

**Still genuinely open**, and **[USER]** says revisit — I am not deciding it:
- Which magnitude rule inside the interval (score-weighted interpolation toward the max? confidence
  as inverse-variance once a shared unit exists?).
- Whether **case 3's disagreement** should scale at all, or refuse and signal low confidence — a
  0-vs-10 split arguably means *neither* estimate is trustworthy, which no combination rule can fix.
- Whether `Score` is really *confidence* (per-estimate, could vary per round and per SO) or
  *trust/priority* (per-analyzer, static config). The `fairShareValue` **priority/Score conflation**
  finding on the normalization branch (§2.6) suggests this is already muddled in the codebase.

`score ≡ 1` today, so **nothing is blocked** — direction-only (scoreless `max`/`min`) is the correct
behavior *now* and matches today's semantics exactly. Recommend implementing direction-only, with
the magnitude hook left explicit and unused.

### 7.1 Composite `Score`
**[ASSUMPTION] A9 — `max` over contributors' Scores.** Reduces to sat's on the sat-only path.
Secondary to the above, and to be revisited with it.

---

## 8. The composite's identity

**[USER]** The optimizer's input is the new `CompositeSignal` — **not** saturation. So the
composite carries its own name, and every consumer that assumes otherwise is a defect.

**[ASSUMPTION] A10 — reuse the existing naming precedent.** The deferred normalization branch
(§2.6, `da0e1ee8`) already introduced **`allocation.CompositeSignalName`** and emitted one extra
`analyzer-result` log line for `"CompositeSignal"` — documented in `docs/reference/cycle-log.md`
with a per-analyzer unit column. Adopt that constant and that log pattern rather than inventing
`domain.CompositeAnalyzerName`. **One difference:** that branch documented the composite's unit as
`%` (coverage), because it normalized to coverage fractions. Ours is in **saturation's units**, so
the unit column must say so — the doc is not copyable verbatim.

**[USER] `NamedAnalyzerResult` is legacy.** We keep it for now; a *different* mission reorganizes
it. So this mission adds no new structure to it beyond what the composite needs, and does not
attempt cleanup. Provenance (A12) should be as small as possible for that reason.

**Known breakage.** `hasSaturationResult` (`engine_v2.go:722`) is
`CompositeSignal.Name == domain.SaturationAnalyzerName && Result != nil`. With a renamed
composite this becomes **false**, silently disabling the engine-side GPU-quota guard it protects.
This is CT7's Q3 hazard, now unavoidable by design rather than dodged.

**[ASSUMPTION] A11 — fix it by intent, not identity.** Rename to `hasUsableCapacitySignal` (or
similar) and test what it actually needs: that the composite carries a real capacity signal.
Per §6 the optimizer's input is *always* the composite, so a name check there is meaningless.

**[ASSUMPTION] A12 — carry provenance.** Record the contributing analyzers (and which drove each
max/min) on the composite, so the guard can assert saturation's participation explicitly rather
than infer it, and so logs explain each decision. Required by §6's "explainable" property.

**Audit before implementing:** grep every `CompositeSignal` consumer for name dependence —
`rescale.go:344,372,528`, `variant_records.go:79`, `cost_aware_optimizer.go:246`,
`greedy_score_optimizer.go:117,156`. PR #34 made the optimizer name-blind (*"zero references to
`SaturationAnalyzerName` remain in any non-test production optimizer file"*), so `:722` is
**expected** to be the only real dependency — to be verified, not assumed.

---

## 9. Test plan

1. **Sat-only** — saturation the only contributor ⇒ composite numerically identical to today
   (`PRC_com = PRC_sat` by §4.4's identity). The regression guard.
2. Another analyzer with higher `N(SO)` → `N_com` raised, `PRC_com` correspondingly lower.
3. Another analyzer with **lower** `N(SO)` → **`N_com` may fall below saturation's.** Per §5.1 sat
   is a fallback, not a floor — this is the test that would have failed under v1–v3's mistaken
   floor invariant, and it must now pass.
4. Disaggregated: per-role aggregation where another analyzer is higher for one role only.
5. Cross-role rule: `min(cov(prefill), cov(decode)) + cov(both)` — purely disaggregated
   (`cov(both) = 0`) and non-disaggregated (`both` only).
6. Non-live analyzer → not a contributor.
7. Non-informative analyzer (`Reason` = `no-data`/`error`) → not a contributor.
8. **Fallback typing** (A19) — no eligible contributor ⇒ saturation fallback, and the fallback
   *kind* is recorded and logged. Assert the marker, not just the value.
9. **Unit independence** — an analyzer whose demand and PRC are both scaled by an arbitrary
   constant `k` produces an **identical** composite. This is the test that proves the
   aggregation is genuinely unit-free and that v1's per-SO-conversion defect has not returned.
   The parent mission had no such test, which is how CT6's `1/PRC` bug survived.
10. **`PRC_sat` independence** — perturbing `PRC_sat` must not change another analyzer's
    contribution to the composite (only the final back-conversion). Directly guards §4.1's
    defect (1).
11. Analyzer with no `RoleDemand` for a role → not a contributor for that role (A4).
12. **Derivation-chain self-consistency** (§5.3) — `ceil(D_sat[role]/PRC_com(SO))` recovers
    `N_com(SO)`; supply/`RC`/`SC` follow from `PRC_com` and `D_sat`. Asserted, not just logged.
13. **Direction is score-independent** (§7) — with any scores, the scale up/down *direction* equals
    the scoreless `max`/`min` decision. Plus: `score ≡ 1` reproduces today's behavior exactly.
14. Renamed composite → the quota guard still fires (A11). The guard-not-silently-disabled test.
15. **End-to-end** `collectV2ModelRequest` → optimizer with nonzero demand, asserting replica
    counts. CT6's bug survived precisely because nothing exercised this path.
16. Restore the 3 `Skip()`-ed multi-analyzer tests CT7's todo lists as pending this work.

**Zero and independence cases — from the user's §2.4/§2.5 corrections:**

17. **`demand == 0`, `PRC > 0`** — coverage undefined; the SO contributes nothing, and the
    composite's demand for that role stays **`0`**, never manufactured into `1.0`. Directly
    encodes the rule the normalization branch's `77f21355` had to fix.
18. **`PRC == 0`, `demand > 0`** — coverage and `N_full` both undefined; contribution skipped, and
    no division by zero anywhere.
19. **Both zero** — no signal; no panic, no `NaN`, no `±Inf` reaching any `min`/`max`.
20. **Undefined contribution is not a magic number** (A14) — an analyzer whose coverage is
    undefined must not act as a `0` in a `min` (which would wrongly veto scale-down) nor as
    `+Inf` in a `max` (which would wrongly demand infinite replicas). Assert both directions.
21. **`PRC > 0` with `demand(role(SO)) == 0`** — explicitly legal per **[USER]** §2.4; the SO
    keeps its PRC and the model does not scale up for it.
22. **SO-independence of demand** (§2.4) — adding or removing an SO for a role leaves that role's
    **demand** unchanged. The structural invariant, as a test.
23. **Role with no SOs** — a role carrying demand but currently served by no SO: no crash, and
    the model's coverage reflects a genuinely uncovered role rather than a spurious full coverage.
24. **Deep-copy isolation** (A15) — after composition, the source saturation entry is unchanged,
    including its `Result`, `RoleCapacities` and `RoleSpare` maps. The normalization branch's
    `da0e1ee8` proved a plain value copy silently mutates the original.
25. **Non-disaggregated layout** — `RoleDemand == nil` with demand in `TotalDemand` composes
    correctly, and the composite writes its result back in the *same* layout (§2.3/A13), not as a
    `both` map key.

**Query API and observability — from review #3:**

26. **Query-API consistency** (§5.5/A21) — `ReplicasToCloseGap` and `GPUsToCloseGap` agree with
    `MissingCapacityForRole` for the same state; no helper can imply a different gap than another.
27. **Query API across allocation states** — the same helper answers correctly for current,
    anticipated, and partial allocations, and is **repeatable**: calling it twice on the same state
    gives the same answer (the property today's mutable `Remaining`/`RoleSpare` fields lack).
28. **Observability parity** (§6/A22) — every analyzer result, and the composite, produce a log
    entry and metric series through the *same* functions; no analyzer is silently omitted, including
    non-live and uninformative ones.
29. **Composite metrics are additive** (A23) — the composite emits its own series/line without
    replacing or altering any analyzer's own, and per-analyzer series stay in their own units.
30. **`PRC` provenance is not second-guessed** (§4.3/A16') — an estimated `PRC` (`Reason` =
    `P2-hist`, `P3-k2`, …) contributes exactly like a measured one, while `no-data`/`error` do not
    contribute at all.

---

## 10. Open items for the user

**Resolved by user direction across v2–v4** — recorded so they are not reopened:
v1's sat-unit conversion (rejected, §4.1); v3's ratio-scaled demand (rejected — `D_com == D_sat`,
§4.4); **`N(SO)` is the single aggregated quantity** and coverage is just `1/N`, not a second signal
(§5.2); everything derives from `N` (§5.3); **saturation is a fallback, not a floor** (§5.1 — the
"floor invariant" is retired); aggregators named for the quantity, not the operation (§4.5);
**no single-model shape assumption** — safety is structural, PRC per SO and demand per model (§4.3);
model-vs-role reconciliation dissolved (§5.4); the optimizer's input contract (§6.1); demand's
storage layout (§2.3); demand/PRC independence (§2.4); zero-guards (§2.5);
`NamedAnalyzerResult` is legacy and not reorganized here (§8); the shared demand unit is a request
count, ideally "per X requests in queue" (§4.2).

| # | Item | Status |
|---|---|---|
| 1 | **§7 — Score / confidence.** User: "need to revisit". Settled: direction is always the scoreless `max`/`min`; only magnitude may be score-influenced; weighted mean is wrong (case 3: mean(0,10)=5=`cur` ⇒ violent disagreement yields "do nothing"). Unsettled: the magnitude rule; whether case-3 disagreement should scale *at all* or signal low confidence; whether `Score` is per-round *confidence* or static *trust* (the `fairShareValue` conflation finding, §2.6, suggests the codebase already muddles this). `score ≡ 1` today ⇒ direction-only is exactly today's behavior. **Recommend: implement direction-only, leave the magnitude hook explicit and unused.** | **Open — revisit** |
| 2 | **§5.1/A19 — fallback typing.** User: "need to see if we mark the type of fallback clearly." My enumeration (`no-eligible-analyzer`, `sat-only`, `sat-fallback-for-this-SO`, `no-signal-at-all`) is a guess at the right granularity. | **Needs your call** |
| 3 | **§5.5/A20 — query-API scope.** Adding a pure query API alongside the *mutable* `Remaining`/`Spare`/`RoleSpare` fields (which the optimizer decrements during allocation, so the signal can't answer the same question twice). `NamedAnalyzerResult` is legacy, so those fields stay. **How far to migrate existing callers?** This is the item most able to balloon. Recommend: define the API, use it on the composite path, migrate callers only as far as needed. | **Needs your call** |
| 4 | §5.4 — model-level coverage: nothing in today's optimizer reads a single model-level coverage number. Recommend exposing it via the query API rather than computing it eagerly as a field. | Confirm |
| 5 | §2.5/A14 — undefined `N`/coverage as `(value, ok)`, never a sentinel: a spurious `0` in a `min` wrongly vetoes scale-down, a spurious `+Inf` in a `max` wrongly demands infinite replicas. | Confirm |
| 6 | §2.3/A13 — one `demandForRole(result, role) (value, present)` accessor encapsulating the nil-`RoleDemand`/`TotalDemand` split. | Confirm |
| 7 | §5.2/A2 — `N` stays continuous through aggregation; `ceil` only where a replica count is finally needed. | Confirm |
| 8 | §6.2/A7 — compose at `collectV2ModelRequest:797`, keeping the slice return (avoids the return-type churn that broke the parent branch's build). | Confirm |
| 9 | §4.5/A18 — one aggregator per quantity (`Agg_N`, `Agg_Spare`), named for the quantity, with the combination rule in one swappable place; landing in/beside `internal/engines/aggregation/`. | Confirm |
| 10 | §8/A10 — adopt `allocation.CompositeSignalName` and the extra `analyzer-result` log line from the normalization branch, with the unit column saying **`D_sat` units**, not `%`. | Confirm |
| 11 | §6/A22 — the observability audit is a *verification task with a possible gap-fix*, not just reuse. Confirm that scope is wanted here rather than split out. | Confirm |
| 12 | Does this mission also restore multi-analyzer operation in the **optimizer** (`cost_aware_optimizer_multi.go`)? My read of §6.1: no — the optimizer stays single-entry; all aggregation is engine-side. | Confirm |

---

## 11. Sources read

**[USER]** "The source of truth is still the pre-single-analyzer upstream." So every factual claim
in §2 is from upstream source on this base. Parent-mission documents are cited only for history
and intent, never as authority — and two of their claims have already proved not to hold here
(§2.6, and CT7's post-normalization premise).

**Own base branch (authoritative):**
- `internal/domain/analyzer.go` — `AnalyzerResult.{TotalDemand, RoleDemand}` (nil when not
  disaggregated), `VariantCapacity.{Role, PerReplicaCapacity, TotalDemand}`,
  `domain.RoleBoth` (in `saturation_analyzer.go`).
- `internal/engines/aggregation/aggregation.go` — the existing aggregation helpers (§2.2).
- `internal/engines/analyzers/saturation_v2/analyzer.go` — `aggregateRoleDemand`,
  `raiseRoleDemandTo`; `internal/engines/analyzers/throughput/analyzer.go` — its own
  `aggregateRoleDemand`; `internal/engines/analyzers/external/analyzer.go` (RoleDemand nil).
- `internal/engines/steadystate/engine_v2.go`, `internal/engines/allocation/optimizer_interfaces.go`,
  and the `CompositeSignal` consumer sites in §2.1.

**Branch `single-analyzer-normalize`** (deferred work, read for lessons only — §2.6): commits
`77f21355` (zero-demand), `da0e1ee8` (deep-copy, naming, log), `cb723833`, `6ae2eb47`.

**Parent-mission docs** (history/intent only): `compose-logic-plan.md`,
`pr-spec-34-composite-signal.md`, `pr-spec-next-coverage-units.md`, `spec.md` §CT7 and
§"Semantic framework", `STATE.p3-planner.md`, `2026-09-06-p3-planner-1.md`,
`multi_backup/analyzer_helpers_multi.go`.

**Not read:** the parent mission's session ledgers and review docs.

---

## 12. Revision history

- **v1** (2026-09-08): first draft. Conversion via `D_i/PRC_i × PRC_sat` per SO; Score as
  gate+tie-break only; model/role demand reconciliation (A5).
- **v2** (2026-09-08, after user review): **§4 conversion rejected and rewritten** — it was
  hostage to `PRC_sat` (the very number other analyzers exist to correct) and used a different
  conversion factor per SO. Composition is now per-SO in unit-free coverage/#replicas; demand is
  per-(model,role) and independent; back-conversion to sat units happens once at the end.
  Model-level demand is no longer "reconciled" — the cross-role coverage rule is the only
  relation (v1's A5 dissolved). Score moved **into** the aggregation with five candidate
  combinators tabled. Optimizer input contract stated explicitly (§6: exactly one full
  `NamedAnalyzerResult`, not sat). Aggregations must be **named helper functions**. Derived
  fields cross-checked against their own aggregation, with mismatch treated as a bug. Two new
  tests: unit-independence and `PRC_sat`-independence.
- **v3** (2026-09-08, after user review #2): six factual corrections, each verified against
  upstream source rather than taken from the parent mission's docs (**[USER]**: "the source of
  truth is still the pre-single-analyzer upstream").
  - **§2.2 — found `internal/engines/aggregation/`**, an existing package of pure aggregation
    helpers named for what they aggregate, already used by both real analyzers. Not mentioned
    anywhere in the parent mission's documents. This mission extends *it*; `multi_backup/` is
    downgraded to "gating logic worth porting, structures not worth restoring".
  - **§2.3 — demand's storage layout corrected.** `RoleDemand` is **nil** when not disaggregated,
    and the `both` demand then lives in `TotalDemand` — there is no `both` map key. Three demand
    values, two storage layouts. Drives A13's single accessor.
  - **§2.4 — demand and PRC are independent.** PRC is per SO; demand is per (model, role) and
    does **not** depend on which SOs exist; an SO can have a real PRC with zero demand for its
    role, and vice versa. True for every analyzer including saturation. Adding/removing an SO
    must not change a role's demand — now an invariant and a test (#22).
  - **§2.5 — coverage is undefined when PRC or demand is zero**, and every calculation must guard
    it. Drives A14 (undefined is `(value, ok)`, never a magic number) and tests #17–21.
  - **§2.6 — learned from the deferred `single-analyzer-normalize` branch** without building on
    it: deep-copy is mandatory (a value copy aliases `Result`/`RoleCapacities`/`RoleSpare` and
    silently mutates saturation's own entry — `da0e1ee8`); `demand == 0` must flow through as `0`
    and never become a phantom `1.0` (`77f21355`); and `allocation.CompositeSignalName` plus its
    extra log line already exist as a precedent, though its `%` unit does not apply to us.
  - **§4.3 — consistent request shape per model per round** is the assumption that licenses
    aggregating at all; recorded, with aggregation confined to one model and one round.
  - `NamedAnalyzerResult` noted as **legacy**, kept as-is; its reorganization is a different
    mission, so provenance is kept minimal.
  - Test plan grew from 16 to 25 cases, the new ones all zero/independence/isolation cases.
- **v4** (2026-09-08, after user review #3): five substantive corrections.
  - **§5.2 — `N` and coverage are the same signal** (`cov = 1/N`). v3's "compute both and
    cross-check" was one quantity written twice, and its `max N`/`min cov` were the same operation.
    Collapsed to a single aggregated quantity: **`N(SO)`**, the replica count for one SO to cover
    its role's whole demand. Everything else derives from it (§5.3).
  - **§4.4 — the composite is `N`, not scaled demand.** v3 had it inverted:
    `D_com = D_sat × cov_sat/cov_com` *kills* the signal. Correct: `D_com[role] == D_sat[role]`
    with `D_sat` **defined** as 100%, and `PRC_com(SO) = D_sat[role(SO)]/N_com(SO)`. Sat units
    become a definition rather than a conversion, so §4.1's circularity cannot recur.
  - **§5.1 — saturation is a fallback, not a floor.** The "floor invariant" inherited from CT7 and
    carried unexamined through v1–v3 is **retired**. Sat contributes only if eligible, and is the
    fallback when nothing else has a usable signal; the composite may legitimately come out *below*
    sat alone. Fallbacks must be typed and observable (A19).
  - **§4.3 — no single-model shape assumption.** v3 claimed one licensed the aggregation; it does
    not, and none is needed. Safety is **structural**: PRC aggregates per SO, demand per model, so
    nothing crosses a model boundary. Shape matters across models *in the optimizer*, and in the
    analyzer's own PRC estimation (previous rounds at zero demand; sibling-SO knowledge otherwise)
    — both outside this mission.
  - **§4.5 — aggregation is named for its quantity, not its operation** (`Agg_N`, `Agg_Spare`),
    with `max`/`min`/weighted-mean as a swappable rule inside.
  - **§5.5 — new: a consistent query API.** Coverage, missing coverage/capacity, replicas-to-close,
    GPUs-to-close, answered by explicit helpers against an explicit allocation state. Today this
    arithmetic is scattered (e.g. `roleDemandGPUs` recomputes it inline) and the mutable
    `Remaining`/`RoleSpare` fields mean the signal cannot answer the same question twice.
  - **§6 — observability is a requirement, not a note:** verify every analyzer result is fully
    observable in logs *and* metrics, and have the composite use the **same** functions — reusing
    the normalization branch's merge of `logCompositeSignal` into `logAnalyzerResult`.
  - **§7 — Score reframed as confidence** around the user's three cases; weighted mean rejected
    (case 3: `mean(0,10) = 5 = cur` ⇒ disagreement yields "do nothing"). Direction is always the
    scoreless `max`/`min`; only magnitude may be score-influenced. Still open.
  - Test plan 25 → 30 cases; test 3 now asserts the composite **may fall below saturation**, which
    would have failed under the retired floor invariant.
