# composite-analyzer — mission spec (draft v3)

**Status:** DRAFT v3 — revised after user review #2; awaiting review #3 (2026-09-08).
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
- The reduce/aggregation itself, as **named helper functions that state what they aggregate**
  **[USER]**, at both **model level and role level** **[USER]** — extending the existing
  `internal/engines/aggregation/` package (§2.2).
- Composition **per SO in unit-free coverage and replica count**; demand kept per **(model,
  role)** and independent of which SOs exist (§2.4); back-conversion to saturation's units once
  at the end **[USER]** (§4).
- **Zero-guards throughout** — coverage is meaningless when PRC or demand is zero **[USER]**
  (§2.5).
- A new composite **name**, and repairing whatever that breaks **[USER]** (§8).
- Analyzer **`Score` applied during aggregation** — it is the analyzer's relative weight
  **[USER]** (§7).
- **Exactly one full `NamedAnalyzerResult`** into the optimizer — the composite, not saturation
  **[USER]** (§6).

### Out of scope (this mission)
- **Normalization** — a shared cross-analyzer demand unit, ideally "per X requests in queue"
  **[USER]**. Deferred: *"Do composition first."* A previous attempt exists on
  `single-analyzer-normalize` and is worth learning from but not building on (§2.6). Until a
  shared unit exists, analyzers meet only in unit-free coverage/replica space (§4.2).
- **Reorganizing `NamedAnalyzerResult`** — legacy, kept as-is; a different mission **[USER]**.
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

### 4.3 Consistent shape within a round **[USER]**

**[USER]** "Both Demand and PRC depend on the shape of the requests. Can only be estimated if the
shape is not known. In every analysis round we assume a consistent shape per model, so aggregation
within a model is consistent."

This is the assumption that licenses the whole aggregation: within one round and one model, every
analyzer's `D` and `PRC` refer to the same request shape, so their quotients are commensurable.

Consequences:
- **Aggregate only within a model, within a round.** Never across models; never mix rounds.
- Shape-dependence is a **per-round estimate**, so the composite is a per-round quantity — no
  smoothing or carry-over across rounds in this mission.
- **[ASSUMPTION] A16** — record this assumption in the compose function's doc comment. If
  per-analyzer shape assumptions ever diverge, the aggregation silently stops being meaningful,
  and nothing in the types would catch it.

### 4.4 Back-conversion to saturation units, once, at the end **[USER]**

Aggregate in coverage/replica space, then express the composite in sat units so downstream
consumers (which read tokens) keep working:

```
composite.RoleDemand[r] / TotalDemand  derived from D_sat and the composite/sat coverage ratio
```

**[ASSUMPTION] A2' — back-conversion is presentation, not composition.** It happens once, after
all aggregation. It uses `D_sat` because the output contract is "sat units" **[USER]** — but no
analyzer's *contribution* passes through `PRC_sat`, so §4.1's defect (1) is gone.

**[ASSUMPTION] A17 — derive from `D_sat` by coverage ratio, never by picking a representative
SO.** Per §2.4 demand is per role and independent of which SOs exist, so any SO-selection would
reintroduce an SO-dependent factor:
```
composite.D(r) = D_sat(r) × cov_sat(r) / cov_composite(r)        when both coverages are defined
composite.D(r) = D_sat(r)                                        otherwise (no usable signal)
```
Sat-only: `cov_sat == cov_composite` ⇒ `composite.D(r) == D_sat(r)` exactly. Preserves §4.5's
fast path by construction, and adding/removing an SO cannot change a role's composite demand
except through coverage — which is what SOs legitimately affect.

Layout: write back through A13's accessor so a non-disaggregated model's `both` demand lands in
`TotalDemand` (with `RoleDemand` nil), not under a `both` key.

### 4.5 Invariants
- **Sat-only fast path.** One eligible analyzer ⇒ composite numerically identical to today.
  Non-negotiable.
- **Floor.** Saturation always participates; aggregation is `max` on replicas / `min` on coverage,
  so the composite is never less aggressive on scale-up nor more aggressive on scale-down than sat
  alone.
- **Zero-safety.** No coverage/replica value is computed or compared where PRC or demand is zero
  (§2.5); demand `0` stays `0` (never becomes `1.0`).
- **SO-independence.** Adding/removing an SO does not change any role's demand (§2.4).

---

## 5. The aggregation

**[USER]** "I want helper functions that express what they are aggregating." Every aggregation is
a **named helper stating what it aggregates**, in the style of — and alongside —
`internal/engines/aggregation/` (§2.2). Not inline arithmetic.

### 5.1 Gating — who participates
```
eligible(i)  ⟺  Result != nil  ∧  ResultIsInformative(i)  ∧  Live(i)
```
Saturation participates unconditionally (floor). If no other analyzer is eligible, composite =
sat. Ports `multi_backup`'s Live-gating and its `liveCount > 0` safety floor.

**[ASSUMPTION] A3 — one eligibility rule in both directions.** A stale analyzer neither raises
demand nor blocks scale-down.

Separately from eligibility, an individual **contribution is skipped** when its coverage is
undefined (§2.5) — a distinct concept, represented explicitly, never as a magic number (A14).

### 5.2 Per-SO composition
```go
// maxReplicasForFullCoverage returns the largest N_full across eligible analyzers
// for one SO, and whether any analyzer produced a defined value.
func maxReplicasForFullCoverage(entries []Entry, so SORef) (replicas float64, ok bool)

// minCoveragePerReplica returns the least per-replica coverage across eligible
// analyzers for one SO, and whether any analyzer produced a defined value.
func minCoveragePerReplica(entries []Entry, so SORef) (coverage float64, ok bool)
```
`max` on replicas / `min` on coverage: most demanding, least covering. Both skip undefined
contributions and report `ok=false` when none is defined.

**[ASSUMPTION] A2 — quantize once.** Keep ratios continuous inside the aggregation; `ceil` only
where the optimizer already does. `max` over pre-`ceil`ed counts double-rounds and inflates when
analyzers are close.

Identity `N_full × coverage = 1` (up to ceiling) means these are two views of one decision —
computing both and cross-checking is §5.5's consistency check.

### 5.3 Per-role coverage — combining SOs of a role
Same-role capacities **add**:
```go
// sumCoverageForRole returns Σ over the role's SOs of replicas × coveragePerReplica.
func sumCoverageForRole(entries []Entry, role string, sos []SORef) (coverage float64, ok bool)
```
Per §2.4 a role may span any number of SOs, and that number can change between rounds — so this
sums over whatever SOs currently serve the role, while the role's **demand** is read
independently via A13.

### 5.4 Model-level coverage — the only cross-role rule **[USER]**
```go
// modelCoverageFromRoles returns min(cov(prefill), cov(decode)) + cov(both).
func modelCoverageFromRoles(byRole map[string]float64) float64
```
```
coverage(M) = min( coverage(M, prefill), coverage(M, decode) ) + coverage(M, both)
```
**This dissolves v1's A5.** Per **[USER]**, per-role demands are *independent numbers* and this is
*the only* relation between role and model level — there is no model-vs-role figure to reconcile;
v1 invented that problem.

Cases: purely disaggregated ⇒ `cov(both) = 0`; non-disaggregated ⇒ `both` only, with demand read
from `TotalDemand` (§2.3). A role with no defined coverage must not contribute a spurious `0` to
the `min` — that would wrongly zero the model's coverage (A14).

**[ASSUMPTION] A4 — an analyzer silent about role r does not participate in role r.** Inventing a
split it never expressed would fabricate signal, and no split is non-arbitrary. Since per-role
demands are independent (§2.4), an analyzer that speaks only at model level has simply not spoken
about that role. Distinguishable from present-and-zero via A13's `present` flag.

### 5.5 Derived fields — recompute, then cross-check **[USER]**
`RequiredCapacity`, `SpareCapacity`, `Remaining`, `Spare` and per-role equivalents are
**recomputed** from the composite's demand/PRC by the existing capacity-build step — not
aggregated independently. Independent aggregation is CT6's bug (some fields normalized, others
left raw).

**[USER]** "if they don't match their own aggregation then we probably have a bug (at least in our
understanding)". So recomputed values are **compared against** the direct aggregation of the same
field, and a mismatch is treated as a defect — in the code or in our model — not silently
reconciled.

**[ASSUMPTION] A6' — mismatch surfaces loudly:** log in production (not panic), **assert equality
in tests**. Float tolerance TBD (open item #4).

### 5.6 Spare capacity — the conservative direction
```go
// minSpareCoverageForRole returns the least spare coverage across eligible analyzers.
func minSpareCoverageForRole(entries []Entry, role string) (spare float64, ok bool)

// allEligibleAgreeSpare reports whether every eligible analyzer sees spare > 0.
func allEligibleAgreeSpare(entries []Entry, role string) bool
```
`min` mirrors `safeRemovalReplicasForRole`; the all-agree gate mirrors `needsScaleDownForRole` —
one eligible analyzer objecting stops a scale-down.

---

## 6. What the optimizer receives **[USER]**

**Exactly one full `NamedAnalyzerResult` reaches the optimizer. It is NOT saturation. It is the
new `CompositeSignal`.**

This tightens v1's §8 into a contract:

- **One** entry — the optimizer keeps the single-entry shape PR #34 established. It is not
  handed a slice and does not reduce.
- **Full** — every field a consumer reads is populated and internally consistent (§5.5). No field
  is left as an unconverted leftover from sat's entry.
- **Not sat** — it is not saturation's result passed through, and must not be mistaken for it. In
  the sat-only case it is numerically identical to sat, but it is still the composite.

Consequences:
- The composite carries its **own name** (§8), not sat's.
- Any consumer that name-checks saturation to identify the optimizer's input is wrong by
  construction and must be repaired (§8).
- Provenance (which analyzers contributed, which drove each max/min) belongs on the composite so
  decisions are explainable.

### 6.1 Placement

`runAnalyzersAndScore` today: run analyzers → `buildNamedResult`/`buildCapacities` per entry →
`updateLivenessAndSetLive` → `recordAnalyzerMetrics` → `logAnalyzerResult` → return slice. Then
`collectV2ModelRequest` (`:797`) takes `namedResults[0]`.

| Option | Placement | Assessment |
|---|---|---|
| **O1** | Inside `runAnalyzersAndScore`, change return type to a single value | p3's choice. **Rejected:** ripples into 6+ test files (the parent mission's known compile breakage, which left `origin/single-analyzer` non-building) and destroys the slice liveness/metrics/logging need. |
| **O2** | New compose function called at `collectV2ModelRequest:797`, replacing `namedResults[0]` | **RECOMMENDED.** One-line change at the single production assignment site; slice return preserved; per-analyzer observability keeps each analyzer's own units, which the CT6 spec confirms is correct. |
| **O3** | Inside the optimizer | **Rejected.** Reverses PR #34's deliberate name-blind single-entry design. |
| **O4** | Normalize per-entry early, aggregate late | **Rejected.** Would corrupt per-analyzer metrics into sat units. |

**[ASSUMPTION] A7 — adopt O2**, with capacity rebuilding (§5.5) inside the compose step.

---

## 7. Score — the analyzer's relative weight **[USER]**

**[USER]** `Score` is, per the docs and config, **the relative weight of each analyzer**, and it
must be **applied during aggregation — not later**. v1's "gate + tie-break" reading was too weak:
it kept Score out of the arithmetic.

**[USER]** Simple weighted average is *not* obviously right. If applied, the most natural place
is **when computing composite #replicas per SO**. It may instead belong on `RC` or `SC`.
Currently `score = 1` for every analyzer by default, so **no production behavior depends on this
today** — which buys room to get it right rather than fast.

### 7.1 Where it applies

Following **[USER]**, Score enters at §5.2's per-SO composition — the point where analyzers'
estimates actually meet:

```
replicasForFullCoverage(SO) = <Score-weighted combination> over eligible i of N_full(A_i, SO)
```

### 7.2 Candidate combinators — **for user decision**, not for me to pick

| # | Rule | Behavior | Floor invariant |
|---|---|---|---|
| **C1** | Weighted mean of `N_full` | Low-weight outliers damped; high-weight analyzer dominates | **Breaks it** — a low-scored sat can be averaged *down* below sat-alone |
| **C2** | Weighted mean, then `max` with sat's own `N_full` | Weighting inside a sat floor | Holds by construction |
| **C3** | `max` over `i` of `score_i × N_full(A_i, SO)` (scaled vote) | Score amplifies/attenuates each claim | Holds iff `score_sat = 1`; a `score < 1` on sat weakens the floor |
| **C4** | `max` over eligible, where eligibility needs `score_i ≥ floor` (v1's reading) | Score gates only, no arithmetic | Holds |
| **C5** | Weighted **quantile** (e.g. weighted median) | Robust to a single wild analyzer | Breaks it unless floored like C2 |

With `score ≡ 1`, **C1, C2, C3 and C5 all reduce to** `max`/plain mean over analyzers, and C4 to
`max` — so the sat-only path is identical under every option, and today's default behavior is
unchanged as long as sat-only holds.

**[ASSUMPTION] A9' — recommend C2** (weighted mean, floored by sat) *if* weighting is wanted now:
it puts Score in the arithmetic as instructed while keeping the floor invariant, and it degenerates
correctly at `score ≡ 1`. But **[USER]** flagged genuine uncertainty about the long-run meaning,
so I am **not** treating this as settled. Open item #1, now the mission's main open design question.

**What "relative weight" should mean in the long run — for discussion.** With a shared
request-based demand unit (§4.2), analyzers become genuinely commensurable and a weight could
express *confidence* in each estimate — at which point a weighted combination is statistically
meaningful (inverse-variance weighting). Without that shared unit, weighting `N_full` values
weights *decisions*, not measurements. That argues for landing the shared demand definition
before committing to a weighted combinator — i.e. keep `max` + floor now, revisit weighting with
normalization. Flagged as a recommendation, not a decision.

### 7.3 Composite `Score`
**[ASSUMPTION] A9 — `max` over contributing analyzers' Scores.** Reduces to sat's Score on the
sat-only path. Secondary to §7.2.

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

1. **Sat-only fast path** — composite numerically identical to sat. The regression guard.
2. Non-sat analyzer demanding more replicas for an SO → composite replicas raised.
3. Non-sat demanding fewer → composite = sat (floor holds).
4. Disaggregated: per-role aggregation where non-sat is higher for one role only.
5. Cross-role rule: `min(cov(prefill), cov(decode)) + cov(both)` — including a
   purely-disaggregated model (`cov(both) = 0`) and a non-disaggregated one (`both` only).
6. Same-role SO addition: `cov(M,r) = Σ cov(SO)` over that role's SOs.
7. Non-sat not live → excluded; composite = sat.
8. Non-sat not informative (`Reason` = `no-data`/`error`) → excluded.
9. **Unit independence** — an analyzer whose demand and PRC are both scaled by an arbitrary
   constant `k` produces an **identical** composite. This is the test that proves the
   aggregation is genuinely unit-free and that v1's per-SO-conversion defect has not returned.
   The parent mission had no such test, which is how CT6's `1/PRC` bug survived.
10. **`PRC_sat` independence** — perturbing `PRC_sat` must not change another analyzer's
    contribution to the composite (only the final back-conversion). Directly guards §4.1's
    defect (1).
11. Analyzer with no `RoleDemand` for a role → excluded from that role (A4).
12. Derived-vs-aggregated **cross-check asserted equal** (§5.5) — the bug-detector the user asked
    for, as a test rather than only a log.
13. Score: with `score ≡ 1`, the composite equals the unweighted result (today's default is
    unchanged); plus the chosen combinator's own cases once §7.2 is decided.
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

---

## 10. Open items for the user

**Resolved by user direction across v2/v3** — not open, recorded so they are not reopened:
v1's sat-unit conversion (rejected, §4.1); model-vs-role demand reconciliation (dissolved, §5.4);
Score's role (in the aggregation, §7); the optimizer's input contract (§6); helper naming (§5);
derived-field cross-checking (§5.5); `multi_backup` handling; demand's storage layout (§2.3);
demand/PRC independence (§2.4); zero-guards (§2.5); consistent-shape-per-round (§4.3);
`NamedAnalyzerResult` is legacy and not reorganized here (§8); the shared demand unit is a
request count, ideally "per X requests in queue" (§4.2).

| # | Item | Status |
|---|---|---|
| 1 | **§7.2 — which Score combinator?** C1–C5 tabled with floor-invariant analysis. User explicitly unsure; `score ≡ 1` today, so nothing is blocked. My view: keep `max`+floor now and revisit weighting *with* the shared request-based unit, since weighting incommensurable *decisions* (rather than commensurable measurements) is not statistically meaningful. Note the `fairShareValue` priority/Score conflation finding on the normalization branch (§2.6) may bear on this. | **Main open design question** |
| 2 | §4.4/A17 — back-conversion via `D_sat(r) × cov_sat(r)/cov_composite(r)`, avoiding SO selection entirely (an SO-selected factor would violate §2.4's SO-independence). Sat-only reduces to `D_sat(r)` exactly. | Confirm |
| 3 | §2.5/A14 — represent undefined coverage as `(value, ok)` rather than a sentinel number, so it can never enter a `min`/`max` as `0` or `+Inf`. | Confirm |
| 4 | §2.3/A13 — one `demandForRole(result, role) (value, present)` accessor in the `aggregation` package, encapsulating the nil-`RoleDemand`/`TotalDemand` layout split. | Confirm |
| 5 | §5.5/A6' — derived-vs-aggregated mismatch: log in production, assert in tests. Float tolerance TBD. | Confirm |
| 6 | §5.2/A2 — continuous ratios, quantize (`ceil`) once at the end. | Confirm |
| 7 | §6.1/A7 — compose at `collectV2ModelRequest:797`, keeping the slice return (avoids the return-type churn that broke the parent branch's build). | Confirm |
| 8 | §2.2 — new aggregations land in/beside `internal/engines/aggregation/`, matching its existing naming style, rather than as new inline arithmetic. | Confirm |
| 9 | §8/A10 — adopt `allocation.CompositeSignalName` and the extra `analyzer-result` log line from the normalization branch, but with the unit column saying **sat units**, not `%`. | Confirm |
| 10 | Does this mission also restore multi-analyzer operation in the **optimizer** (`cost_aware_optimizer_multi.go`)? My read of §6: no — the optimizer stays single-entry; all reduction is engine-side. | Confirm |

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
