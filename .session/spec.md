# composite-analyzer — mission spec (draft v2)

**Status:** DRAFT v2 — revised after the user's first review; awaiting second review (2026-09-08).
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
  **[USER]**, at both **model level and role level** **[USER]**.
- Composition **per SO in unit-free coverage and replica count**; demand kept per **(model,
  role)**; back-conversion to saturation's units once at the end **[USER]** (§4).
- Whatever aggregations are needed, reusing or reimplementing from the pre-single-analyzer
  helpers **[USER]**.
- A new composite **name**, and repairing whatever that breaks **[USER]**.
- Analyzer **`Score` applied during aggregation** — it is the analyzer's relative weight
  **[USER]** (§7).
- **Exactly one full `NamedAnalyzerResult`** into the optimizer — the composite, not saturation
  **[USER]** (§6).

### Out of scope (this mission)
- Request-based normalization — a shared demand unit, ideally "per X requests in queue"
  **[USER]**. The eventual target for genuine cross-analyzer demand conversion; deferred. Until
  it exists, analyzers meet only in unit-free coverage/replica space (§4.2).
- CT6's coverage-fraction normalization (`TotalDemand = 1.0`). Not merely deferred — see §2.3,
  it has a known correctness bug and this mission does **not** build on it.
- CT1b (nil-saturation guard), CT4 (fairness definition). Untouched.
- Changing PRC ownership: saturation owns PRC.

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

### 2.2 Multi-entry originals available for reuse
`multi_backup/analyzer_helpers_multi.go` — 13 functions. The **aggregations** (what this mission
needs):

| Helper | Aggregation | Reusable for compose? |
|---|---|---|
| `roleBottleneckReplicas` | `max_i ceil(state[i][role] / PRC_i[v])` | **Pattern**, not call — takes picker state, not results |
| `roleAggRemaining` | `max_i state[i][role]` | Pattern |
| `safeRemovalReplicasForRole` | `min_i floor(RoleSpare_i[role] / PRC_i[v])`, Live-gated | **Directly relevant** — the min/Live-gate shape |
| `needsScaleDownForRole` | all-live-agree gate; `liveCount > 0` safety floor | **Directly relevant** |
| `anyRoleNeedsScaleUp` | `any_i state[i][role] > 0` | Pattern |
| `ResultIsInformative` | non-nil Result + ≥1 non-sentinel `Reason` | **Directly reusable logic** |
| `prcForVariant` | PRC lookup for `(result, variant)` | **Directly reusable logic** |
| `rolesOf`, `variantsForRole` | role enumeration, `"" → RoleBoth` canonicalization | Directly reusable logic |

Non-aggregations (mutators/allocator), **not** in this mission's reduce: `applyAllocation`,
`applyDeallocationForRole`, `initRoleState`, `allocateForModelPaired`.

**[ASSUMPTION] A1 — reuse by re-implementation, not by un-ignoring `multi_backup/`.**
Those files are `//go:build ignore`, in `package allocation`, and depend on unexported types
(`variantRecord`) — they cannot be linked as-is, and un-ignoring them would collide with the
single-entry helpers that replaced them. So "reuse" = port the arithmetic and the Live/
informative gating into the new compose code, preserving semantics exactly. The user asked to
"reuse **or reimplement**", which this satisfies. `multi_backup/` is left untouched.

### 2.3 Finding: the parent plan's premise is absent here
`single-analyzer/.session/compose-logic-plan.md` (the "initial plan under p3") justifies its
`max RC` rule with: *"since CT6 normalization sets `TotalDemand = 1.0` and RC is derived from
it, RC is already the implied-replica signal ... equivalent to max implied_replicas **in a
post-normalization world**."*

That world does not exist on my base — `normalizeToCompositeUnits` is not upstream. Worse,
`single-analyzer/.session/pr-spec-next-coverage-units.md` records that CT6, as implemented on
the parent branch, **has an unfixed correctness bug**: it normalized `PerReplicaCapacity`/
`TotalDemand`/`RoleDemand` but left `RequiredCapacity`/`SpareCapacity`/`Remaining`/`Spare`
raw, so `initRoleState` divides raw demand by fractional PRC — "replica counts wrong by
roughly `1/PRC_fraction` for any model with real, nonzero demand." That code is not upstream
and is not this mission's inheritance.

**Consequence:** taking `max RC` across analyzers on *this* base would be aggregating numbers
in mutually incommensurable units — saturation's tokens vs. a throughput analyzer's
tokens/sec. That is the central hazard this spec must avoid.

The fix is **not** to convert demands into sat units before aggregating — v1 tried that and it
was a logical error (§4.1). It is to aggregate in a space where units have already cancelled:
per-SO **coverage** and **replica count** (§4.3). Sat units appear only in the final
back-conversion, after all composition is done.

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

## 4. The common currency

**[USER, v2 correction]** The v1 conversion was wrong. Recorded here because the reasoning
matters and must not be reintroduced.

### 4.1 Why v1's conversion was a logical error

v1 proposed `demand_sat_equivalent(A_i, SO) = D_i(SO)/PRC_i(SO) × PRC_sat(SO)`. Two independent
defects:

1. **It is hostage to `PRC_sat`'s accuracy.** Every analyzer's contribution gets rescaled by
   saturation's per-replica capacity estimate. But the inaccuracy of `PRC_sat` is *the very
   reason other analyzers exist*. Routing their signal through the number they are meant to
   correct is circular: a bad `PRC_sat` corrupts the contributions that would have compensated
   for it.
2. **It uses a different conversion factor per SO.** `PRC_sat` varies across SOs, so the same
   analyzer's demand is converted by a different ratio for each SO. A "unit" that changes per SO
   is not a unit — the resulting composite is not denominated in anything coherent, and
   cross-analyzer comparison of the converted values is meaningless.

### 4.2 The correct structure **[USER]**

Three separate concerns, kept separate:

- **Demand is per (model, role).** Roles are prefill / decode / both. **Not** per SO. Each
  analyzer produces a model-level demand *for each role*. Conceptually these are **independent
  numbers** — not a split of one model total, and not derived from each other.
- **Composition is per SO, in coverage and replica count.** Both are unit-free, so they compare
  across analyzers with no conversion factor. This is where aggregation happens.
- **Conversion back to saturation units happens once, at the end**, using `D_sat`.

Real cross-analyzer *demand* conversion needs a **shared demand definition** — a count of
requests: requests-in-system, or better, "per X requests in queue". That is the eventual
normalization and remains **deferred**. Until then no analyzer's demand is converted into
another's; they meet only in the unit-free coverage/replica space.

### 4.3 The quantities

From the parent semantic framework, per analyzer `A_i` and SO:

```
N_full(A_i, SO) = ceil( D(A_i, M, R(SO)) / PRC(A_i, SO) )     replicas for full coverage
C(A_i, SO)      = PRC(A_i, SO) / D(A_i, M, R(SO))             per-replica coverage ∈ (0,1]
```

Both unit-free: each divides a demand by a capacity **from the same analyzer**, in that
analyzer's own units, which cancel. This is why they are comparable across analyzers and raw
demand is not.

Note the demand term is `D(A_i, M, R(SO))` — the **role's** demand, looked up by the SO's role,
not a per-SO demand. This is what makes the composition per-SO while demand stays per-(model,
role).

**Grounding in the code** (verified on this base):
- `domain.AnalyzerResult.RoleDemand map[string]float64` — per-(model, role) demand. Exactly the
  right shape; no new structure needed.
- `domain.AnalyzerResult.TotalDemand` — model-level, role-agnostic.
- `domain.VariantCapacity.PerReplicaCapacity` — per-SO (per-variant), analyzer's own units.

So the data model already matches the required structure.

### 4.4 Back-conversion, once, at the end

After aggregating in coverage/replica space, express the composite in saturation's units so
downstream consumers (which read tokens) keep working:

```
composite.RoleDemand[r]  = N_composite(r) × PRC_sat(representative SO of r)     -- see A2'
composite.TotalDemand    = from the roles, per the cross-role rule (§5.4)
```

**[ASSUMPTION] A2' — the back-conversion is a *presentation* step, not part of composition.**
It happens after all aggregation, exactly once. It reuses `D_sat` / `PRC_sat` because the output
contract is "saturation units" **[USER]** — but no analyzer's *contribution* passes through
`PRC_sat` any more, so defect (1) in §4.1 is gone. Open question: for a role with several SOs of
differing `PRC_sat`, which representative to use. Candidates: recover demand from sat's own
`RoleDemand[r]` scaled by the coverage ratio (`D_sat[r] × cov_sat(r)/cov_composite(r)`), which
avoids picking an SO at all. **Recommend that form** — flagged as open item #3.

### 4.5 Invariants preserved

- **Sat-only fast path.** One analyzer ⇒ composite numerically identical to today. Non-negotiable.
- **Floor.** Saturation always participates, and the aggregation is `max` on replicas / `min` on
  coverage, so the composite is never less aggressive on scale-up nor more aggressive on
  scale-down than sat alone.

---

## 5. The aggregation

**[USER]** "I want helper functions that express what they are aggregating." Each aggregation
below is a named helper whose name states the aggregation — not inline arithmetic. This is a
requirement on the code's shape, not only its behavior.

### 5.1 Gating — who participates
```
eligible(i)  ⟺  Result != nil  ∧  ResultIsInformative(i)  ∧  Live(i)
```
Saturation participates unconditionally (floor). If no other analyzer is eligible, composite =
sat. Ported from `needsScaleDownForRole` / `safeRemovalReplicasForRole`'s Live-gating and their
`liveCount > 0` safety floor.

**[ASSUMPTION] A3 — one eligibility rule for both directions.** A stale analyzer neither raises
demand nor blocks scale-down.

### 5.2 Per-SO composition — the core

For each SO, aggregate across eligible analyzers in the unit-free space:

```
replicasForFullCoverage(SO)  = max over eligible i of N_full(A_i, SO)
coveragePerReplica(SO)       = min over eligible i of C(A_i, SO)
```

`max` on replicas / `min` on coverage: take the most demanding estimate, the least-covering
estimate. Identity `N_full × C = 1` (up to ceiling) means these are two views of one decision;
computing both and cross-checking them is the consistency check §5.5 requires.

Helper names express the aggregation, e.g.:
```go
func maxReplicasForFullCoverage(entries []NamedAnalyzerResult, so variantRef) int
func minCoveragePerReplica(entries []NamedAnalyzerResult, so variantRef) float64
```

**[ASSUMPTION] A2 — quantization.** Keep the ratio continuous inside the aggregation; quantize
(`ceil`) once at the end. `max` over already-`ceil`ed counts double-rounds and inflates when
analyzers are close. Sat-only is unaffected either way.

### 5.3 Per-role coverage — combining SOs of the same role

Per the framework, same-role capacities **add**:
```
coverageForRole(M, r) = Σ over SOs with R(SO) = r of coverage(SO)
```
where `coverage(SO) = ReplicaCount(SO) × coveragePerReplica(SO)`.

### 5.4 Model-level coverage — the only cross-role rule **[USER]**

```
coverageForModel(M) = min( coverageForRole(M, prefill), coverageForRole(M, decode) )
                      + coverageForRole(M, both)
```

**This supersedes v1's A5.** v1 tried to reconcile a model-level demand figure with per-role
figures, treating one as derived from the other. That framing was wrong: **[USER]** per-role
demands are *independent numbers*, and this coverage rule is *the only* relation between roles
and the model. There is nothing else to reconcile.

A purely disaggregated model has `coverageForRole(both) = 0`; a non-disaggregated model has only
`both` (`""` canonicalized to `RoleBoth`).

**[ASSUMPTION] A4 — an analyzer that emits no `RoleDemand` for role r.** It does not participate
in that role's aggregation. Inventing a per-role split it never expressed would fabricate signal,
and no split is non-arbitrary. Since demands are per-(model, role) and independent, an analyzer
that speaks only at model level has simply not spoken about that role. Unchanged from v1, but now
resting on the correct structure rather than on Q1's reconciliation worry.

### 5.5 Derived fields — recompute, and cross-check **[USER]**

`RequiredCapacity`, `SpareCapacity`, `Remaining`, `Spare` and per-role equivalents are
**recomputed** from the composite's demand/PRC by the existing capacity-building step — not
aggregated independently. Independent aggregation is exactly CT6's bug (some fields normalized,
others left raw, `1/PRC` errors).

**[USER]** "if they don't match their own aggregation then we probably have a bug (at least in
our understanding)". So the recomputed values are also **compared against** the direct
aggregation of the same field, and a mismatch is treated as a defect — in the code or in the
model — rather than silently reconciled.

**[ASSUMPTION] A6' — mismatch surfaces loudly.** Cross-check in production behind a log (not a
panic), and **assert equality in tests**. Rationale: a mismatch means our understanding is
wrong, which is exactly what CT6's silent divergence cost the parent mission. Tolerance for
float comparison to be specified. Open item #4.

### 5.6 Spare capacity — the conservative direction
```
minSpareCoverage(...)   = min over eligible i of spare coverage
allAnalyzersAgreeSpare  = every eligible analyzer reports spare > 0 for the role
```
`min`, mirroring `safeRemovalReplicasForRole`; plus `needsScaleDownForRole`'s all-agree gate — one
eligible analyzer objecting stops a scale-down.

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

**[ASSUMPTION] A10 — `domain.CompositeAnalyzerName = "composite"`**, a new exported constant
beside `SaturationAnalyzerName`.

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

---

## 10. Open items for the user

Resolved in v2 by user direction: v1's §4 conversion (rejected — §4.1), model-vs-role demand
reconciliation (dissolved — §5.4), Score's role (now in the aggregation — §7), the optimizer's
input contract (§6), helper naming (§5), derived-field cross-checking (§5.5), `multi_backup`
handling (item 6 confirmed OK).

| # | Item | Status |
|---|---|---|
| 1 | **§7.2 — which Score combinator?** C1–C5 tabled with floor-invariant analysis. User is explicitly unsure; `score ≡ 1` today so nothing is blocked. My view: keep `max`+floor now, revisit weighting alongside the shared request-based demand unit, since weighting incommensurable decisions is not statistically meaningful. | **Main open design question** |
| 2 | §4.2 — confirm the eventual shared demand unit is **"per X requests in queue"** (user's stated preference over requests-in-system), so the deferred normalization has a fixed target. | Confirm |
| 3 | §4.4/A2' — back-conversion for a role with several SOs of differing `PRC_sat`. Recommend deriving from sat's own `RoleDemand[r]` × coverage ratio, avoiding SO selection entirely. | Confirm |
| 4 | §5.5/A6' — mismatch handling: log in production + assert in tests. Float tolerance TBD. | Confirm |
| 5 | §5.2/A2 — continuous ratio, quantize once at the end. | Confirm |
| 6 | §6.1/A7 — compose at `collectV2ModelRequest:797`, keep the slice return. | Confirm |
| 7 | §8/A10 — name `"composite"`. | Confirm |
| 8 | Does this mission also restore multi-analyzer operation in the **optimizer** (`cost_aware_optimizer_multi.go`)? My read of §6: no — the optimizer stays single-entry; all reduction is engine-side. | Confirm |

---

## 11. Sources read

Parent-mission docs (read-only; cross-worktree reads authorized **[USER]**):
`STATE.p3-planner.md`, `2026-09-06-p3-planner-1.md`, `compose-logic-plan.md`,
`pr-spec-34-composite-signal.md`, `pr-spec-next-coverage-units.md`, `spec.md` §CT7 and
§"Semantic framework", `multi_backup/analyzer_helpers_multi.go`.

Own base branch: `internal/domain/analyzer.go` (`AnalyzerResult.RoleDemand` — per-(model,role);
`VariantCapacity.PerReplicaCapacity`/`TotalDemand` — per-SO; confirming the data model already
has the structure §4.2 requires), `engine_v2.go`, `optimizer_interfaces.go`, and the
`CompositeSignal` consumer sites in §2.1.

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
