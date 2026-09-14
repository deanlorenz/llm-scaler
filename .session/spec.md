# composite-analyzer — mission spec (draft v8)

**Status:** v8 approved 2026-09-08. **Implemented and reviewed PASS, 2026-09-09** — see §12's
implementation entry for the build (commits, coder/reviewer roles) and the one real deviation
found and fixed (O2 placement, §6.2). Mission is implementation-complete; not yet pushed or PR'd.
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
  GPUs to close a gap — so each concept has **one definition** shared by every optimization step,
  instead of each function inventing its own **[USER]** (§5.5).
- **Zero-guards throughout** — coverage/`N` is meaningless when PRC or demand is zero **[USER]**
  (§2.5) — *and* the fallbacks that must still yield a non-zero PRC for an idle or never-seen SO,
  so a partial scale-from-zero is not blocked **[USER]** (§5.1.4).
- **A composite decision-path field**, mirroring analyzers' `Reason`, so every composite value says
  how it was reached **[USER]** (§5.1.2).
- **Consistency of the repeated derivations in the optimizer** — PRC/demand, bounds, and `ceil()`
  computed one way, not re-invented per function **[USER, D3]** (§5.5).
- **Full observability**, reusing the *same* log and metric functions as any analyzer result
  **[USER]** (§6).
- A new composite **name**, and repairing whatever that breaks **[USER]** (§8).
- **Exactly one full `NamedAnalyzerResult`** into the optimizer — the composite, not saturation
  **[USER]** (§6.1).

### Out of scope (this mission)
- **Normalization** — a shared cross-analyzer demand unit, ideally "per X requests in queue"
  **[USER]**. Deferred: *"Do composition first."* **[USER, review #4]** "We are not on top of
  normalization. That branch is deferred for now." So `single-analyzer-normalize` is **not** a
  dependency or a plan of record; §2.6 treats it as hazard-awareness only. Until a shared unit
  exists, analyzers meet only in unit-free `N`/coverage space (§4.2).
- **Reorganizing `NamedAnalyzerResult`** — legacy, kept as-is; a different mission **[USER]**.
- **`Score` in the aggregation** — **deferred [USER, final]**: "leave it out for now." `Agg_N` is a
  pure `max`. The user's outlier-rejection intuition is recorded in §7.2 for a future attempt, along
  with the standing exclusion that weighted average is not the answer.
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

**[USER, review #4]:** "We are not on top of normalization. That branch is deferred for now."

So this section is **hazard-awareness only**. Nothing here is a plan of record, an adopted decision,
or a dependency. Each item below is a bug that branch *hit*, restated as something our code must
avoid — every one independently checkable against our own base:

1. **Deep-copy is mandatory.** A plain value copy of a `NamedAnalyzerResult` aliases `Result`,
   `RoleCapacities` and `RoleSpare` — verifiable from the struct definition alone (three
   reference-typed fields). Building the composite from saturation's entry by value would therefore
   **mutate saturation's own result**. That branch hit exactly this (`da0e1ee8`).
   **[ASSUMPTION] A15** — deep-copy every reference-typed field, with a test asserting the source
   entry is unchanged after composition.
2. **Zero-demand must flow through as zero** — never rewritten to a placeholder. That branch had
   three sites forcing a demand field to `1.0` even at zero demand (`77f21355`), producing phantom
   demand and an idle-model `Utilization` miscompute. Our §2.5/§4.4 zero rules cover this
   independently; the branch is corroboration, not the source.
3. **A composite needs its own identity and its own log line** — see §8 and §6. That branch reached
   the same conclusion, which is mild corroboration only; its `%` unit does not apply to us (§4.4).

Also noted from that branch's ledger: a `fairShareValue` **priority/Score conflation** finding.
Per **[USER]** this is a **bug in a known direction**, not an open question (§7.0): FSV should weight
different *models'* demand by **priority**; `Score` weights different *analyzers'* opinions. Out of
scope here (it is CT4-adjacent), but recorded so it is not mistaken for evidence that `Score`'s
meaning is unsettled — it is not.

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

### 5.1 Contributors, fallbacks, and the decision path **[USER]**

**[USER]:** "Sat does not participate unconditionally. Only if no other signal, as fallback."

**This retires the "floor invariant"** that CT7 recorded and that v1–v3 carried through unexamined.
Saturation is not privileged in the aggregation; it is the **fallback when no other analyzer has a
usable signal**. The composite may therefore come out **below** saturation alone — intended, since
correcting saturation is why other analyzers exist.

```
contributors(SO) = eligible analyzers with a defined N(SO)
if contributors is empty:  fall back (see §5.1.2)
else:                      aggregate over contributors — sat included only if itself eligible
```

`D_sat` remains the **unit** (§4.4) even when saturation does not *contribute* to `N`. Unit and
contribution are separate roles; conflating them is easy and wrong.

#### 5.1.1 Eligibility
```
eligible(i)  ⟺  Result != nil  ∧  ResultIsInformative(i)  ∧  Live(i)
```
Separately, a **contribution is skipped** when its `N` is undefined (§2.5) — distinct from
ineligibility, represented explicitly (A14), never as a magic number.

**[ASSUMPTION] A3 — one eligibility rule in both directions.** A stale analyzer neither raises demand
nor blocks scale-down.

#### 5.1.2 The composite's decision path **[USER, D2 decision]**

**[USER]:** "Analyzers already have a field to describe the decision path that created the results —
this will be logged per analyzer. We need something similar for the composite signal (closer to
option b)."

Verified: `domain.VariantCapacity.Reason` is exactly that field — free text naming the path that
produced the value. Saturation's ladder: `P0-store`, `P1-obs`, `P2-hist`, `P3-k2`, `P4-k1`,
`P1-obs-invalid`, plus the shared `no-data` / `error` sentinels; throughput uses `T1-ols`,
`T2-pinned`, `T2-default`, `T2-failed`.

**[ASSUMPTION] A19' — the composite carries the same kind of field**, at the same granularity as the
analyzers' (per SO, since `Reason` is per `VariantCapacity`), naming how *this* SO's `N_com` was
reached. Candidate values:

| Composite reason | Meaning |
|---|---|
| `C0-agree` | multiple contributors, aggregated normally |
| `C1-single` | exactly one contributor (its identity in the log) |
| `C2-sat-fallback` | no other contributor for this SO; saturation used as fallback |
| `C3-default-prc` | no contributor at all; a default/last-good PRC was used (§5.1.4) |
| `C4-no-signal` | no signal and no usable default — see §5.1.3 |

Logged per analyzer *and* for the composite through the same function (§6), so a decision is traceable
end to end. The exact value set is a proposal; the *mechanism* (mirror `Reason`) is **[USER]**'s.

#### 5.1.3 No signal at all — do not autoscale **[USER]**

**[USER]:** "If no signal at all, or sat is disabled and other analyzers did not provide signal, then
probably should not do any autoscaling (we already have this gate in the code); still need some default
signals to avoid breaking some calculations (need to check which)."

The existing gate is `hasSaturationResult` (`engine_v2.go:722`) — *"A request without one was not
measured this cycle, so its replica counts are not evidence of anything and must not be charged to a
quota."* Same intent, but it currently tests **saturation's name**, which §8 changes.

**[ASSUMPTION] A11' — the gate is repaired to test "is there a usable signal", not "is this
saturation".** With `C4-no-signal` recorded on the composite (§5.1.2), the gate reads that explicitly
rather than inferring it from an analyzer's identity. This makes the *existing* no-autoscaling
behavior survive the rename — which is the whole hazard §8 introduces.

**Open sub-task, explicitly [USER]-flagged: "need to check which" calculations break on a
zero/absent signal.** Not answered here. This requires walking every `CompositeSignal` consumer
(§2.1's list) and recording, per field, what it does when the field is zero or absent — a survey, not
a guess. Tracked as a spec deliverable, not an assumption; see D2 in §10.

#### 5.1.4 Fallbacks that MUST produce a non-zero PRC **[USER]**

**[USER]:** "One important case that needs a fallback is **partial scale from zero** — we have an idle
SO; this typically creates a zero or undefined PRC, but we must still have a non-zero estimate (e.g.
last good value). Also applies to a **never-seen-before new SO** — must have some initial PRC. Sat
already computes these defaults. The never-seen-before estimate can be **over** — at worst we create a
replica, then learn the true values and take it back down. Now we have a more reasonable estimate."

Verified in `saturation_v2/analyzer.go` (≈l.700–735), the ladder is already there — in priority order:
1. **Live replicas** → measured, labelled by `k2SourceLabel` (`P1-obs` / `P2-hist` / `P3-k2` / `P4-k1`).
2. **No ready replicas, own store record** with `EffectiveCapacity > 0` → `estimateStoredCapacity`,
   labelled **`P0-store`**. ← the **idle SO / partial-scale-from-zero** case.
3. **No own record, a compatible variant exists** (same accelerator, GPU count, engine params;
   searched cross-namespace) → borrow its `EffectiveCapacity`, also **`P0-store`**. ← the
   **never-seen-before SO** case.
4. Otherwise → **`no-data`**, `PRC = 0`.

So the defaults **[USER]** refers to are cases 2 and 3, and they are saturation's, not the composite's.

**[ASSUMPTION] A27 — the composite consumes these defaults, it does not reimplement them.** A
`P0-store` PRC is an ordinary contribution: the composite must **not** treat an estimated PRC as
weaker than a measured one (§4.3/A16'), because that is precisely what keeps a scale-from-zero moving.
If estimate quality should influence weight, that is the confidence question (§7), not a separate
mechanism.

**[ASSUMPTION] A28 — over-estimation is acceptable here, by [USER]'s explicit reasoning:** "at worst
we create a replica, then learn the true values and take it back down." So for the never-seen-before
case the composite must not clamp or discount the borrowed estimate defensively — an over-estimate is
self-correcting on the next cycle, whereas a zero PRC **blocks the scale-up that would produce the
measurement**. Recorded because it inverts the usual conservative instinct, and a later reader would
otherwise be tempted to "fix" it.

**Remaining gap:** case 4 (`no-data`, `PRC = 0`) still yields an undefined `N` (§2.5). Whether a
`C3-default-prc` beyond saturation's ladder is needed there — a last-good value with no store record at
all — is part of D2's open sub-task.

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

#### What "consistently" means here — one definition per concept **[USER, review #4]**

**[USER] correction:** "It is not a side effect. It is **tracking the allocation by design**. That
is why the optimizer gets a deep copy. 'Consistently' referred to **different optimization steps and
calls** — spare should mean spare, coverage should mean coverage. **We should not have every
optimization function invent its own.**"

An earlier draft mistook the mutation of `Remaining` / `Spare` / `RoleSpare` for an accidental defect
that made the signal "unable to answer the same question twice", and proposed threading state through
to avoid it. That was wrong on both counts:

- The mutation is **intentional allocation tracking**. The optimizer receives a **deep copy** exactly
  so it can decrement these as it commits replicas, without touching the analyzer's result. The
  deep copy is what makes in-place tracking correct — it is the mechanism, not a leak. (Note this is
  the *same* reason A15 requires the composite to deep-copy: same discipline, two places.)
- So there is **nothing to fix** about the mutable-field pattern, and no state-threading to invent.

The real requirement is **semantic consistency**: each concept has **exactly one definition**, shared
by every optimization step and call.

> `spare` means spare. `coverage` means coverage. No optimization function re-derives them.

Today that is violated by duplication, not by mutation. `roleDemandGPUs` (`rescale.go:585`)
computes `ceil(demand / best_PRC) × gpusPerReplica`, choosing the role's most cost-efficient variant
itself; `cost_aware_optimizer.go:304` reads RC/SC per role and applies its own arithmetic. Each is
locally reasonable; together they are several private definitions of "what closing the gap means",
free to drift.

**[ASSUMPTION] A20' — the query API is the single definition of each concept**, callable at any point
in the allocation (before, mid-loop, after), reading whatever the current tracked state says. It does
**not** try to make answers time-invariant — an answer *should* change as allocation progresses, since
that is what tracking means. What must not change is the *meaning* of the question.

So the helpers read the same fields the optimizer already tracks; the value they add is that
`MissingCapacityForRole` is computed in one place rather than five (A21).

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
2. **The composite reuses the same functions** — not parallel ones **[USER]**. It is logged by
   `logAnalyzerResult` and its metrics emitted by `recordAnalyzerMetrics`, exactly as any analyzer
   result is. `docs/reference/cycle-log.md` gains the composite's row, in **`D_sat` units** (§4.4).

   **[USER, review #4]** "We are not on top of normalization. That branch is deferred for now." So
   the deferred `single-analyzer-normalize` branch is **not** a plan of record here. It happens to
   have done something similar (merging a separate `logCompositeSignal` into `logAnalyzerResult`),
   which is mild corroboration that one shared function is the right shape — but the requirement
   above stands on the user's instruction and on what exists on **this** base, not on that branch.
   Nothing is inherited from it.

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

## 7. Score — deferred; pure `max` for now **[USER, D1 final]**

### 7.0 Score vs. priority — two different axes **[USER, settled]**

| | Weights | Used by |
|---|---|---|
| **Priority** | different **models'** demand against each other | `fairShareValue` / fair-share |
| **Score** | different **analyzers'** opinions about **one** model | the composite aggregation |

`fairShareValue` using `Score` where it should use priority is a **bug in a known direction**
(CT4-adjacent, out of scope), not an ambiguity in `Score`'s meaning.

### 7.1 Decision: leave Score out **[USER, final]**

**[USER]:** "I changed my mind. I prefer the **pure max as default** when all scores are 1.0. The
signal is already normalized to replica count. Let's **defer adding the score to later**. I don't have
a good idea. … **Bottom line: leave it out for now.** Note that weighted average is not the solution."

So this mission implements:
```
N_com(SO) = max over contributors of N_i(SO)
```
Nothing else. No confidence normalization, no RMS discount, no magnitude hook — the earlier
`max − confidence-weighted RMS` design (spec v6 §7.2) is **withdrawn**, not merely unimplemented.

**Why pure `max` is right as the default, not just simplest [USER]:** the signal is already
**normalized to replica count** (§5.2), so `max` compares like with like — the unit problem that made
raw-demand aggregation meaningless (§4.1) does not arise here. `max` is then the honest reading of
"how many replicas does the most demanding analyzer say this SO needs".

`Agg_N` still exists as the named aggregator (§4.5) with `max` as its rule, so a future rule change is
contained to one place. That is the *only* accommodation for future scoring; no hook, no dead
parameter.

### 7.2 The user's intuition, recorded for the future **[USER]**

Kept verbatim because it points at a **different mechanism** than weighting, and would otherwise be
lost:

> "My intuition is that the scores can only help identifying an **outlier that can be ignored** in the
> max calculation and still add some **small bias**. (e.g. `5,5,5,10`, where `10` is lower score may
> turn into a `6`). Not sure."

That is **outlier rejection plus a bias term**, not a weighted average — closer to a robust statistic
(trimmed max / Winsorizing) than to a mean. Note the example's shape: three agreeing analyzers and one
low-confidence dissenter, where the answer lands just above the cluster rather than between cluster
and outlier. A weighted mean of `5,5,5,10` cannot produce `6` while also returning `10` when the
outlier is *trusted* — so the mechanism has to be selection-then-adjustment, not blending.

**[USER]** "Note that weighted average is not the solution." Recorded as a standing exclusion: a future
attempt should not re-propose weighted mean.

### 7.3 Composite `Score` field
With scoring deferred, the composite still needs *some* value in the legacy `Score` field.
**[ASSUMPTION] A9''' — `max` over contributors' `Score`s**, which reduces to saturation's on the
sat-only path. Nothing reads it for aggregation any more; it exists because
`NamedAnalyzerResult.Score` is part of the legacy struct (§8) and `fairShareValue` currently consumes
it — see §7.0 on why that consumption is itself a bug.

---

## 8. The composite's identity

**[USER]** The optimizer's input is the new `CompositeSignal` — **not** saturation. So the
composite carries its own name, and every consumer that assumes otherwise is a defect.

**[ASSUMPTION] A10 — a new exported constant for the composite's name.** The name itself
(`"CompositeSignal"` vs `"composite"`) and where the constant lives (`allocation` beside
`NamedAnalyzerResult`, or `domain` beside `SaturationAnalyzerName`) are both minor; recommend
`allocation.CompositeSignalName` since the composite is an `allocation` concept and
`ModelScalingRequest.CompositeSignal` already uses that word. The deferred normalization branch
happened to pick the same thing (§2.6) — corroboration, not a dependency. Its `%` unit does not
apply: ours is in `D_sat` units (§4.4).

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
8. **Decision path recorded** (A19', §5.1.2) — each of `C0-agree` / `C1-single` / `C2-sat-fallback` /
   `C3-default-prc` / `C4-no-signal` is reached by a constructed case and asserted. The *marker*, not
   just the value.

**Fallbacks that must not block scale-from-zero (§5.1.4):**

8a. **Idle SO, own store record** — `PRC` comes from the store (`P0-store`), `N` is defined, and the
    composite scales up. The partial-scale-from-zero case.
8b. **Never-seen-before SO** — no own record, a compatible variant exists ⇒ borrowed
    `EffectiveCapacity` (`P0-store`) contributes normally. Asserted **not** discounted or clamped for
    being an estimate (A27/A28) — an over-estimate is acceptable and self-corrects; a zero would block
    the scale-up that produces the measurement.
8c. **`no-data` (`PRC = 0`)** — `N` undefined, contribution skipped, decision path records it. No
    division, no panic.
8d. **No signal at all ⇒ no autoscaling** — the existing gate still fires after §8's rename (A11'),
    driven by the recorded `C4-no-signal` rather than by an analyzer's name.
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
13. **`Agg_N` is a pure `max`** (§7) — `N_com = max` over contributors, for equal and for differing
    `Score`s alike. **Score must not affect the result**, asserted explicitly: the same inputs with
    scores `1,1` and with scores `1,5` produce the *same* composite. That pins the deferral so a
    future partial implementation cannot leak in unnoticed.
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

## 10. Decisions needed

**[USER, review #4]:** "Decision items — not clear at all what you want." Rewritten. Previously
these were a mix of real questions and things I had already decided but phrased as questions.

Each item below states: **the question**, **the options**, **what I'd do**, and **what it costs to
defer**. Nothing here is a request to re-litigate something already settled.

---

### D1 — Score — **DECIDED: leave it out [USER, final]**

`Agg_N` is a **pure `max`**. Score is deferred entirely; the `max − confidence-weighted RMS` design
from v6 is withdrawn. Rationale, the user's outlier-rejection intuition, and the standing "weighted
average is not the solution" exclusion are all in §7. Test 13 asserts Score has *no* effect, so a
partial future implementation cannot leak in unnoticed.

*(Superseded: v6's A24/A25 — confidence normalization and the range clamp — are moot and removed.)*

---

### D2 — Fallbacks and the decision path — **DECIDED [USER]**; survey now complete

Mechanism, gate repair, and the scale-from-zero PRC fallbacks are settled — see §5.1.2–5.1.4.

**The survey [USER] asked for is done: `.session/survey-zero-signal.md`.** Six conclusions; the three
that change this spec:

1. **The absent-signal path is already uniformly safe** — seven consumer sites each guard
   `Result == nil` and degrade to "do nothing for this model". No new default *signals* are needed to
   keep calculations from breaking; every zero is either guarded or semantically correct.
2. **But it is seven independent nil checks plus one name check.** `hasSaturationResult` is the only
   one that tests saturation's *name*, so §8's rename would leave the system **partially** gated —
   worse than either extreme. Strengthens A11', and argues for exposing **one shared "is there a usable
   signal" predicate** rather than repairing that single site in isolation.
3. **Zero PRC is the real hazard, because it silently *disables* an SO** rather than failing loudly
   (`if vc.PerReplicaCapacity <= 0 { continue }`, and the parent mission's own survey calls this "a
   designed eligibility gate, not a division-safety guard"). Nothing downstream would notice a missing
   SO. That is exactly the partial-scale-from-zero case, so the composite's decision path must make a
   PRC fallback **visible** (`C3-default-prc` / `C4-no-signal`), not merely correct.

Also confirms **[USER]**'s memory of more gates: `applyScaleToZeroEnforcement`
(`engine.go:1300+`) publishes `wva_model_scaling_blocked` with typed, per-owner reasons
(`variant-floor`, `policy-forbids-zero`, `engine-unsupported`, `activation-retention`, plus
`no-wake-signal` from the wake loop). There is an established convention for "why is scaling not
happening", including the subtlety that reasons are published **before** the empty-decision return so a
stale reason is always cleared.

**DECIDED [USER, 2026-09-08]:** yes — `C4-no-signal` publishes a new **policy-owned** reason on
`wva_model_scaling_blocked`, alongside the existing `variant-floor` / `policy-forbids-zero` /
`engine-unsupported` / `activation-retention` / `no-wake-signal` reasons, following the existing
convention (published before the empty-decision return so a stale reason clears).

---

### D3 — Consistency of repeated derivations in the optimizer — **[USER] scope given**

**[USER]:** "Let's document the full scope. I am not sure about your specific example —
`ceil(demand/best_PRC) × gpusPerReplica` — here `best_PRC` is **not part of analyzer info**. I want the
**repeating calculations of PRC/Demand or Bounds or even `ceil()`** to be consistent in the optimizer."

**My example was wrong, and the correction sharpens the target.** In `roleDemandGPUs`, `best_PRC` comes
from `sortByCostEfficiencyAsc` over variant records — a **cost-efficiency selection**, which is the
optimizer's own policy, not a derivation from the analyzer signal. Picking the cheapest variant is not
duplicated analyzer logic and does not belong in a composite query API.

What *is* in scope is the **repeated derivation** underneath it. Four recurring categories:

| Category | Examples found | Why it needs one definition |
|---|---|---|
| **`ceil()` / replica-count rounding** | `roleDemandGPUs` (`rescale.go:606`), `roleBottleneckReplicas`, `safeRemovalReplicasForRole` (`floor`) | Whether a partial replica rounds up, down, or is clamped at 0 is a **semantic** choice repeated at every site; §5.2/A2 says quantize once, and this is where "once" has to be enforced |
| **demand → replicas → GPUs** | `roleDemandGPUs`, `modelDemandGPUs` (`rescale.go:573`) | The chain `demand / PRC → replicas × gpusPerReplica` appears more than once; the *chain* is shared even where the variant choice is not |
| **PRC / demand lookup** | `prcForVariant`, per-role vs model-level demand reads at `rescale.go:587-591`, `cost_aware_optimizer.go:309` | The role-vs-model fallback (§2.3's two layouts) is re-implemented per site — precisely what A13's accessor centralizes |
| **Bounds** | `floorGPUs`/`maxGPUs`/`CapGPUs` clamping (`rescale.go:536-559`), `initTargets`, `min`/`max` replica clamps | "What are this model's bounds" is answered in several places with slightly different assembly |

**[ASSUMPTION] A21' — the API's job is these four, not variant selection.** Cost-efficiency ordering,
accelerator filtering, and priority handling stay where they are: they are optimizer *policy*. The API
supplies the *derived quantities* that policy consumes.

**Full scope documented as requested — and it is genuinely larger than v5's framing.** The four
categories above span `rescale.go`, `cost_aware_optimizer.go`, `greedy_score_optimizer.go`, and
`analyzer_helpers.go`.

**DECIDED [USER, 2026-09-08]:** this mission scopes to the **first two** categories only —
`ceil()`/replica-count rounding and PRC/demand lookup — per A20''s two-step delivery
recommendation, since these are where a semantic inconsistency actually changes a replica count.
The demand→replicas→GPUs chain and bounds are **deferred** to a follow-up mission.

---

### D4 — Confirmations — **ALL 12 CONFIRMED, no vetoes [USER, 2026-09-08]**

Listed so the user could veto, not to make them choose. Each is recorded in the spec with its
reasoning. Veto pass complete: all 12 stand as specced.

| # | Decision | Where |
|---|---|---|
| 1 | Undefined `N`/coverage is `(value, ok)`, never a sentinel number — a stray `0` in a `min` wrongly vetoes scale-down; a stray `+Inf` in a `max` wrongly demands infinite replicas | §2.5/A14 |
| 2 | One `demandForRole(result, role) (value, present)` accessor, hiding the nil-`RoleDemand`-vs-`TotalDemand` split | §2.3/A13 |
| 3 | `N` stays continuous through aggregation; `ceil` only where a replica count is finally needed | §5.2/A2 |
| 4 | Compose at `collectV2ModelRequest:797`, keeping `runAnalyzersAndScore`'s slice return (the return-type change is what broke the parent branch's build) | §6.2/A7 |
| 5 | One aggregator per quantity (`Agg_N`, `Agg_Spare`), named for the quantity, combination rule swappable in one place; lands in/beside `internal/engines/aggregation/` | §4.5/A18 |
| 6 | Composite carries a new exported name constant; recommend `allocation.CompositeSignalName` | §8/A10 |
| 7 | Deep-copy every reference-typed field when building the composite (`Result`, `RoleCapacities`, `RoleSpare` alias under a value copy) | §2.6/A15 |
| 8 | `hasSaturationResult` is repaired by testing intent, not analyzer identity | §8/A11 |
| 9 | Model-level coverage is exposed via the query API rather than computed eagerly as a field — nothing in today's optimizer reads such a number | §5.4 |
| 10 | Composite metrics/logs are **additive**: the composite gets its own row through the *same* functions; no analyzer's own series changes | §6/A23 |
| 11 | The optimizer stays single-entry; all aggregation is engine-side (`cost_aware_optimizer_multi.go` is not revived) | §6.1 |
| 12 | The observability item includes an audit that every analyzer result is fully emitted — a verification task that may surface a gap to fix | §6/A22 |

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
- **v5** (2026-09-08, after user review #4): three corrections, all to *my* errors rather than to the
  design.
  - **§5.5 — the mutable fields are intentional, not a defect.** **[USER]** "It is not a side effect.
    It is tracking the allocation **by design**. That is why the optimizer gets a deep copy."
    v4 framed `Remaining`/`Spare`/`RoleSpare` mutation as an accidental flaw that made the signal
    "unable to answer the same question twice", and proposed threading state through to avoid it.
    Both wrong: the deep copy is precisely what makes in-place allocation tracking correct.
    **[USER]** "'Consistently' referred to different optimization steps and calls — spare should mean
    spare, coverage should mean coverage. We should not have every optimization function invent its
    own." So the requirement is **semantic**: one definition per concept, shared across steps. The
    violation today is *duplication* (`roleDemandGPUs`, `cost_aware_optimizer.go:304`,
    `greedy_score_optimizer.go:117,156` each re-deriving), not mutation. A20 replaced by A20'.
    **This also reversed D3's recommendation** from "helpers only" to "helpers **plus** migrate the
    known duplicators" — adding a definition alongside the existing ones would make the duplication
    worse, defeating the stated goal.
  - **§2.6/§6/§8 — stop leaning on the deferred normalization branch.** **[USER]** "We are not on top
    of normalization. That branch is deferred for now." v4 cited it as a naming precedent to adopt and
    a logging pattern to inherit. Rewritten so it is **hazard-awareness only**: each item is a bug
    that branch hit, restated as something to avoid and independently checkable on this base (the
    deep-copy aliasing is verifiable from the struct definition alone). No decision depends on it.
  - **§7 — `cur` misread, and the Score/priority axes separated.** **[USER]** "`cur` means current
    replica count. I did not intend to say that this is the desired result. **Always ask me if not
    sure.**" v4 read case 3's `cur = 5` as an implied answer and built an argument on it — an invented
    premise. Also **[USER]**: "FSV should use the **priority** to give weight to different model
    demand, not scores. **Scores** apply to giving weights to **analyzer opinions**." So the two axes
    are distinct and `Score`'s *meaning* is settled; only the combination rule is open. The
    `fairShareValue` conflation finding is therefore a **bug in a known direction**, not evidence of
    ambiguity — v4 had cited it as the latter.
  - **§10 rewritten.** **[USER]** "Decision items — not clear at all what you want." Previously a mix
    of genuine questions and already-made decisions phrased as questions. Now three real decisions
    (D1 Score magnitude, D2 fallback marking granularity, D3 query-API migration scope), each with
    question / options / my recommendation / cost of deferring, plus a flat list of twelve
    confirmations to veto rather than choose.
- **v6** (2026-09-08, D1 and D2 decided by the user):
  - **§7 — Score becomes a confidence in `[0,1]`, and the aggregation rule is settled [USER].**
    `conf_i = score_i / Σ conf`, so raw 3 is 3× as confident as raw 1; then
    `N_com = Nmax − sqrt( Σ_i conf_i · (Nmax − N_i)² )` — max minus the confidence-weighted RMS
    distance from the max. Properties recorded because they are the point: exact max at full agreement
    (so sat-only stays identical), a discount that grows with both spread and the dissenters'
    confidence, and `N_com <= Nmax` always so direction is preserved. Worked cases pinned as regression
    values: case 1 (3 vs 5) ⇒ `≈ 3.59`; case 3 (0 vs 10) ⇒ `≈ 2.93`. Replaces v5's "direction only,
    magnitude hook unused".
  - **§5.1.2 — the composite gets a decision-path field [USER]**, mirroring the analyzers' existing
    `VariantCapacity.Reason` (verified: saturation uses `P0-store`/`P1-obs`/`P2-hist`/`P3-k2`/`P4-k1`,
    throughput `T1-ols`/`T2-*`), at the same per-SO granularity. Proposed values `C0-agree`/`C1-single`/
    `C2-sat-fallback`/`C3-default-prc`/`C4-no-signal`.
  - **§5.1.3 — no signal at all ⇒ do not autoscale [USER]**, via the gate that already exists
    (`hasSaturationResult`, `engine_v2.go:722`). Since §8's rename would silently disable it, it is
    repaired to test "is there a usable signal" rather than "is this saturation" (A11'), reading the
    recorded `C4-no-signal`.
  - **§5.1.4 — fallbacks that must still produce a non-zero PRC [USER].** Verified saturation's ladder
    already covers both cases the user named: an **idle SO** (partial scale-from-zero) falls to its own
    store record (`P0-store`), and a **never-seen-before SO** borrows a compatible variant's
    `EffectiveCapacity` (also `P0-store`); only after both does it emit `no-data` with `PRC = 0`. The
    composite **consumes** these and must not discount an estimated PRC relative to a measured one
    (A27). **Over-estimation is explicitly acceptable** for a new SO **[USER]** — "at worst we create a
    replica, then learn the true values and take it back down" — so no defensive clamping (A28): a zero
    PRC would block the very scale-up that produces the measurement.
  - Test plan gains the confidence-formula cases (13, with pinned worked values) and the
    scale-from-zero fallback cases (8a–8d).
  - **Open, both small and both mine:** A25's clamp of `N_com` to `[Nmin, Nmax]` (the raw formula can
    leave the range when confidence concentrates on a low estimate), and whether "100% confident" meant
    **relative** confidence (the ratio form, `conf = 1` ⇔ sole contributor) or an **absolute**
    per-estimate confidence, which no analyzer currently produces and which would change the formula.
  - **Outstanding survey, flagged by the user's own "need to check which":** what breaks on a
    zero/absent signal, across all nine `CompositeSignal` consumer sites. Recommended to run it now,
    since its outcome could change the no-signal design rather than just its implementation.
- **v7** (2026-09-08): D1 reversed, D3 scoped, survey delivered.
  - **§7 — Score is deferred entirely [USER, final].** "I changed my mind. I prefer the pure max as
    default when all scores are 1.0. The signal is already normalized to replica count. Let's defer
    adding the score to later. … Bottom line: leave it out for now." v6's
    `max − confidence-weighted RMS` design is **withdrawn**, and A24/A25 (confidence normalization,
    range clamp) are moot. `Agg_N` is a pure `max`; test 13 asserts Score has **no** effect so a
    partial future implementation cannot leak in.
    The user's own intuition is recorded verbatim in §7.2 because it points at a **different
    mechanism** than weighting — outlier rejection plus a small bias (`5,5,5,10` with a low-scored
    `10` → maybe `6`), which is a robust statistic, not a mean. Plus the standing exclusion: "weighted
    average is not the solution."
  - **§10/D3 — my example was wrong [USER].** `ceil(demand/best_PRC) × gpusPerReplica` was a poor
    illustration: `best_PRC` comes from `sortByCostEfficiencyAsc`, i.e. it is the optimizer's
    **cost-efficiency policy**, not a duplicated derivation from analyzer info. The real target is the
    **repeating calculations of PRC/demand, bounds, and even `ceil()`**. Full scope now documented as
    four categories (rounding, demand→replicas→GPUs, PRC/demand lookup, bounds) spanning `rescale.go`,
    `cost_aware_optimizer.go`, `greedy_score_optimizer.go`, `analyzer_helpers.go`, with variant
    selection explicitly **excluded** (A21'). Larger than v5's framing; a two-step delivery is proposed
    (A20'').
  - **Survey delivered** — `.session/survey-zero-signal.md`, answering the user's "need to check
    which". Three results that change this spec: (1) the absent-signal path is already uniformly safe
    across seven guarded sites, so **no new default signals are needed**; (2) it is seven independent
    nil checks *plus* one name check, so §8's rename would leave the system **partially** gated — this
    argues for one shared "is there a usable signal" predicate, not just repairing
    `hasSaturationResult`; (3) **zero PRC silently disables an SO** rather than failing, which is
    exactly the partial-scale-from-zero hazard, so a PRC fallback must be *visible* via the decision
    path, not merely correct.
    It also confirmed the user's memory of more gates: `applyScaleToZeroEnforcement` publishes
    `wva_model_scaling_blocked` with typed, per-owner reasons — an established convention the
    composite's decision path should follow rather than duplicate.
- **v8** (2026-09-08): **§10 fully resolved — spec approved pending this final revision.**
  - **D2 decided [USER]:** `C4-no-signal` publishes a new **policy-owned** reason on
    `wva_model_scaling_blocked`, alongside the existing typed reasons, following the existing
    publish-before-empty-return convention.
  - **D3 decided [USER]:** mission scope is the **first two** derivation categories only —
    `ceil()`/rounding and PRC/demand lookup — per A20''s two-step delivery recommendation. The
    demand→replicas→GPUs chain and bounds are **deferred** to a follow-up mission.
  - **D4 veto pass complete [USER]:** all 12 confirmations stand, no vetoes.
  - No open items remain in §10.
- **Implementation** (2026-09-09, no spec text changed — v8 stands as the approved design):
  User approved v8, then separately authorized implementation ("go ahead. implement and
  review"). Dispatched as `coder-agg1` (implementer) and `reviewer-agg1` (continuous reviewer),
  same-worktree/async, per a 12-item task-file checklist decomposing §4–§9 in dependency order.
  Landed as commits `4ac16404..f98a566f` (11 checklist commits) plus `f98a566f`'s test-plan sweep
  (§9, all 30 items) — see `.session/task-coder-agg1.md` and `.session/review-coder-agg1.md` for
  the full checklist-to-commit mapping.

  **One real deviation found and fixed — §6.2's O2 decision.** Commit `0ec6c170` moved
  `buildComposite`'s call from the mandated O2 site (`collectV2ModelRequest`) into
  `runAnalyzersAndScore`, to reuse `logAnalyzerResult`/`recordAnalyzerMetrics` without a second
  call. This is architecturally **O1** (§6.2 evaluates and rejects that placement) even though it
  technically avoided changing `runAnalyzersAndScore`'s return type. `reviewer-agg1` caught it;
  the coder should have stopped and asked (a genuine spec-adjacent ambiguity) but proceeded
  instead.

  **User ruling:** the coder's trade was wrong. §6.2's O2 decision stands as specced — composite
  construction stays at `collectV2ModelRequest` only. Observability parity is achieved by calling
  `logAnalyzerResult`/`recordAnalyzerMetrics` for the composite as a **second explicit call** from
  that same O2 site, not by relocating composition. (Whether the engine's analyze/log/metrics
  pipeline should be restructured more broadly is a separate, later, clean discussion — explicitly
  not opened here.) Fix landed as commit `0642f472` (not a history rewrite); `reviewer-agg1`
  independently verified the revert byte-for-byte against `81ef806d~1` and traced
  `evictStaleAnalyzerSeries`'s actual logic (not the commit message's claim) to confirm the
  double-call is eviction-safe.

  **Final verdict: PASS, 12/12 checklist items.** Both non-negotiable regression guards (test 1
  sat-only identity, test 13 Score-has-no-effect) hold throughout, before and after the fix.
  Mission is implementation-complete; not pushed, no PR opened as of this entry.
- **v9** (2026-09-14): **complete redesign of the composite-building code** [USER], superseding
  v8's `buildComposite`/`ResolveSO`/`AggN`/`PRCCom` implementation while keeping v8's underlying
  math (§4.4/§5.2's `N(SO)`, `PRC_com(SO) = D_sat[role]/N(SO)`) — the redesign is about *where the
  code lives and what it's explicit about*, not a new formula. Reached via
  `.session/composite-signal-redesign.md` (full citation-backed record; this entry summarizes).
  User's stated trigger: v8's implementation had spec-coupled comments, saturation-lookup
  duplicated 3x (`findSaturation`, an inline lookup in `representativeVariantCapacity`, and a
  special case inside `ResolveSO`), unclear provenance when analyzers disagree, and `AggN` called
  on a 1-element slice inside `ResolveSO` (dead pattern — aggregating nothing).
  - **Correction to this entry's own first draft, caught by the user:** the composite is built
    from `collectV2ModelRequest` (`engine_v2.go:818`), a **separate function** that calls
    `runAnalyzersAndScore` first and then calls `buildComposite` on its result — not from inside
    `runAnalyzersAndScore` itself. This is the already-decided "O2" placement (§6.2, reaffirmed in
    this section's own "Implementation" entry above after a coder mistakenly moved it to O1).
    Recorded here because an earlier pass at documenting this redesign got it wrong.
  - **Every composite field is sat's own value, full stop, except PRC and Reason [USER].**
    `ReplicaCount`, `PendingReplicas`, `WarmPoolReplicas`, `WarmPoolPerReplicaCapacity`,
    per-variant `TotalDemand`, model-level `TotalDemand`/`RoleDemand` — all copied directly from
    saturation's own `AnalyzerResult`, never combined. `PerReplicaCapacity` (PRC) and the per-SO
    `Reason` remain the two fields whose value is genuinely aggregation-derived.
  - **Saturation is also the sole source of the variant set**, not just of field values. v8's
    `unionOfVariants` (union across every analyzer's `VariantCapacities`) and
    `representativeVariantCapacity`'s fallback-to-a-different-analyzer branch are retired — the
    composite iterates saturation's own `VariantCapacities` directly. A variant saturation does not
    report is not in the composite at all.
  - **New per-SO participation rule, replacing `eligible()`'s model-level-only check for this
    purpose [USER]:** for a given SO, an analyzer does not contribute to that SO's `N_i(SO)` (so
    cannot move `N(SO)` up *or* down) when either (a) the SO is absent from that analyzer's
    `VariantCapacities` — throughput and external already opt out this way on a per-SO failure, by
    construction, so nothing changes for them — or (b) the SO is present but that analyzer's
    `Reason` for it is `no-data`/`error` — saturation's own case, since saturation cannot opt out
    by omission (it must always stay in, as the sole field/variant-set source above). Verified: no
    new per-analyzer signal is needed; this is exactly `variantCapacity()`'s existing "present"
    check plus the existing `ReasonNoData`/`ReasonError` sentinel, just applied **per-SO** rather
    than `ResultIsInformative`'s current whole-result any-hit check.
  - **Naming, internal to the composite-building code only — verified no external caller
    references any of these names [USER]:**
    - `N(SO)`/`N_i(SO)` (spec's existing term for "replicas needed to cover this SO's role's
      demand") is renamed **`TotalReplicas`** — `N` alone was judged too generic.
    - `N_com(SO)`/`AggN`'s result becomes **`CompositeTotalReplicas`** (or the equivalent compound
      built on `TotalReplicas` — exact identifier decided at implementation time, principle is
      what matters: name states the quantity, not the operation).
    - `PRCCom` is retired as a named, separately-tested function. Its computation
      (`D_sat[role]/CompositeTotalReplicas`) is inlined directly into the composite-building loop
      and assigned straight to the composite `VariantCapacity.PerReplicaCapacity` — there is no
      other consumer and no other reason for it to be a standalone symbol.
  - **Relocation, per the user's general rule: an aggregation-package helper with exactly one
    external caller belongs in that caller's file, not a shared package [USER].** Verified against
    the actual call graph (grep, not assumed) before applying: `AggN` had exactly one external
    caller (`composite_decision.go`'s `ResolveSO`); `PRCCom` had exactly one (`composite.go`'s
    `buildComposite`); `replicasNeeded`/`variantCapacity`/`roleOf` (aggregation package's private
    helpers) were already called only from within `AggN`'s own file. `DemandForRole` has three
    callers and stays in the shared `aggregation` package. Net: the combined-PRC/TotalReplicas
    computation moves into `steadystate/composite.go` itself; `ResolveSO`'s replacement (still
    producing the per-SO decision path) also moves logic out of the `aggregation` package to the
    extent it was only serving that one call site.
  - **Helper signatures take the already-resolved `VariantCapacity`, not `(result, variant
    string)` plus an internal lookup [USER].** The composite-building loop already holds the
    specific `VariantCapacity` for the SO it is processing (it iterates saturation's own
    `VariantCapacities` directly, per the ruling above) — re-searching for it by name inside a
    helper it calls is redundant. Applies to `replicasNeeded`'s/`variantCapacity`'s replacements.
  - **`roleOf`/`roleOfVC` unified [USER].** Today there are three copies of the same
    role-canonicalization logic (`aggregation.roleOf`, `steadystate.roleOfVC`, and inline inside
    `aggregation.AggregateByRole`) — collapsed into one shared function. Exact home not fixed by
    this entry — apply the same single-caller-relocation rule once the rewrite's real call graph
    is known (it changes as soon as `AggN`/`PRCCom` move).
  - **Supply/AnticipatedSupply/RC/SC — confirmed unchanged, restated in terms of the new names
    [USER]:** `buildCapacities` already runs on the composite exactly as on any analyzer result
    (verified, not a new decision) — `TotalSupply = ReplicaCount(SO) × PRC(SO)`,
    `TotalAnticipatedSupply = (ReplicaCount(SO)+PendingReplicas(SO)) × PRC(SO) = TotalSupply +
    PendingReplicas(SO) × PRC(SO)`, and RC/SC follow via the existing `applyUniversalThreshold`,
    with no formula change. **Flagged for a code comment, not a design change:** `ReplicaCount` on
    the composite is saturation's raw k8s ready count, not a count of replicas *usefully serving*
    — accepted as good enough for now, but the gap should be visible in code where `ReplicaCount`
    feeds supply.
  - **New composition-level logging, not previously present anywhere [USER]:** verified today's
    `logAnalyzerResult` (engine_v2.go:1105) already logs, per analyzer per cycle, `Live`,
    `TotalSupply`, `TotalDemand`, per-variant PRC/Role/Reason, and model+role RC/SC — but **not**
    per-variant `ReplicaCount`/`PendingReplicas`, and `TotalReplicas`/`N_i(SO)` is not logged or
    stored anywhere today (purely transient inside the old `AggN` call). The redesign adds a
    composition-level log line covering, per SO: each contributing analyzer's `TotalReplicas`,
    `ReplicaCount` (Ready), and `PendingReplicas` — full visibility into what fed
    `CompositeTotalReplicas`, not just the winning value.
  - **Cross-analyzer disagreement (one analyzer implying scale-up, another scale-down) is a
    non-issue by construction [USER]** — SC/RC are computed exactly once, after the composite's
    single (Demand, Supply, AnticipatedSupply) triple exists, so there is no path where two
    analyzers' opposing signals reach the optimizer directly; the new logging above (not a
    resolution *policy*) is how disagreement stays observable.
  - **Completeness test for the rewrite's step list [USER]:** compare against the
    pre-single-analyzer aggregation logic at the engine side (the code this mission's CT7 lifted
    out of), not just against v8's own step list.
  - **Sat has two separate roles, not one — folded in from the user's earlier code review
    (`code-review-notes.md` §7/§9/§8.6/§8.7), which an earlier pass at this entry omitted despite
    those being real, already-given rulings [USER]:**
    - **Identity/unit role (the bullet above):** which analyzer's raw fields populate the
      composite's own stored fields. Sat, unconditionally, always.
    - **Contributor role (new in this entry):** whether sat counts as an ORDINARY voice in
      `CompositeTotalReplicas`'s aggregation, on equal footing with every other analyzer, versus
      only as a fallback. **This is conditional, not unconditional, and is IN SCOPE for this
      redesign [USER] — not deferred:**
      - Sat is an ordinary contributor when config-enabled
        (`config.AnalyzerEnabled(domain.SaturationAnalyzerName)`) — collected symmetrically with
        every other analyzer, no name-based special case during collection (today's
        `if e.Name == domain.SaturationAnalyzerName` branch inside `ResolveSO`'s collection loop
        goes away).
      - Sat is a fallback only — narrower than today's implementation — when config-disabled: it
        participates only if no other analyzer contributed a defined value for that SO.
      - Corrected decision taxonomy: `single` = exactly one analyzer contributed, symmetric,
        regardless of which one; `sat-fallback` = specifically the disabled-but-nothing-else-
        contributed case, not "sat happened to be the only one already eligible."
      - Verified against current code (not the review notes' 2026-09-09 snapshot): neither
        `eligible()` nor `ResolveSO`/`buildComposite` receives a `ScalingPolicy`/config today —
        this is a genuine signature/data-flow change (config must reach the composite-building
        step), not a small conditional add.
      - Non-live or broken sat must still opt out, not crash — existing controller precedent,
        needs re-verification once this lands.
    - These two roles do not conflict: sat can supply the composite's identity fields
      unconditionally while also being excluded from ordinary contributor status when disabled —
      different questions about the same analyzer.
  - **Decision-path values become a typed/enumerated representation [USER],** not the current
    untyped `string` constants (`DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/
    `DecisionNoSignal`), to catch mistakes at compile time.
  - **`HasUsableCompositeSignal` splits into two checks [USER],** not one boolean serving both: a
    per-SO check ("does this specific SO have a signal") and a separate model-level check ("is sat
    itself present/healthy at all") — sat's absence is categorically worse than any one SO lacking
    a signal, since sat is the identity/unit source for the entire composite.
  - **PRC computation loops over roles, not independently per SO [USER]:** look up the composite's
    per-role demand once per role (shared across every SO of that role), then compute each SO's
    PRC from that shared numerator and the SO's own `TotalReplicas` — a shape/efficiency point for
    where PRC is computed (§ above, "PRCCom is retired... inlined"), not a formula change.
  - **Explicitly not addressed by this revision, left for a later pass:** whether "sat-only for
    every identity field except PRC/Reason" is durable policy or a deliberate narrowing to unblock
    this redesign, and the eventual goal of a demand unit canonical **across models** (not just
    across analyzers within one model) — `D_sat` is today's transitional choice, not the durable
    target (ties to the canonical-composite-demand item in STATE.md's Known Issues).
