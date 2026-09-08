# composite-analyzer — mission spec (draft v1)

**Status:** DRAFT — awaiting user review (2026-09-08).
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
mission, with two deliberate departures from CT7's recorded design (§3).

### In scope
- The reduce/aggregation function itself, at both **model level and role level** **[USER]**.
- Normalization **into saturation's units** as part of preparing the composite **[USER]**.
- Whatever aggregations are needed, reusing or reimplementing from the pre-single-analyzer
  helpers **[USER]**.
- A new composite **name**, and repairing whatever that breaks **[USER]**.
- Analyzer **`Score` participating** in the composition **[USER]**.

### Out of scope (this mission)
- Request-based normalization ("100 requests waiting in EPP queue") — the eventual target,
  explicitly deferred **[USER]**.
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
tokens/sec. That is the central hazard this spec must avoid, and it is exactly why the user's
instruction to normalize into sat units is the right call.

---

## 3. The two departures from CT7's recorded design

Both **[USER]**, both overturning `compose-logic-plan.md`'s resolutions:

| CT7/p3 said | This mission |
|---|---|
| Q3: composite keeps `Name = SaturationAnalyzerName` ("zero-risk option") | Composite gets a **new name** |
| Q4: composite inherits sat's `Score`; non-sat affects RC/SC only | **All analyzers' Scores affect the composition** |

Q1 (per-variant `TotalDemand`) and Q2 (`RoleDemand` absent on a non-sat analyzer) are
re-derived from scratch in §5, since p3's answers were normalization-dependent and the user
said its resolutions do not stand.

---

## 4. The common currency: normalize into saturation units

**[USER]** Eventual target: normalize on **requests** — each analyzer computes demand for
"100 requests waiting in the EPP queue". **For now: token capacity, i.e. saturation's units.**

### 4.1 Why a currency is needed at all
Per the parent spec's semantic framework: `D(A, M, R)` and `PRC(A, SO)` are in *analyzer A's
own* unit. Saturation is tokens; a throughput analyzer is tokens/sec; an SLO analyzer is
latency-capacity. Adding, averaging, or `max`-ing raw demand across analyzers is meaningless.

### 4.2 The conversion
The unit-free bridge is **implied replica count**, exactly as the parent framework defines:

```
N_full(A_i, SO) = ceil( D(A_i, SO) / PRC(A_i, SO) )        unit-free
```

Convert each analyzer's demand into saturation's units by asking: *how much saturation-demand
would require the same replica count?*

```
demand_sat_equivalent(A_i, SO) = N_full(A_i, SO) × PRC(sat, SO)
```

Then aggregate in that single currency (§5).

**Why this is normalization "into sat units", not CT6's coverage normalization:** the composite
stays denominated in tokens, with sat's PRC intact. `TotalDemand` remains a token quantity, so
every downstream consumer that reads it — including `rescaleInputsForGroup`'s water-fill weight
— keeps working unchanged. That sidesteps CT6's entire `SatDemand` compensation mechanism and
its correctness bug. **This is the key design consequence of the user's "use sat units" call.**

**[ASSUMPTION] A2 — `ceil` placement.** `N_full` is defined with `ceil`, so
`demand_sat_equivalent` is quantized to whole replicas of sat capacity. Alternative: keep the
ratio continuous (`D_i/PRC_i × PRC_sat`) and let the optimizer's own `ceil` quantize once.
I recommend the **continuous** form for the reduce, quantizing only where the optimizer already
does, to avoid double-rounding inflation when several analyzers are close. Sat's own entry is
unaffected either way (`D_sat/PRC_sat × PRC_sat = D_sat`, exact). Flagging because CT7's text
says `max` over `ceil`ed counts.

### 4.3 Sat-only fast path
`len(namedResults) == 1` → return `namedResults[0]` unchanged (modulo the rename, §6).
Preserves today's only production behavior bit-for-bit. **Non-negotiable invariant** — the
parent mission held it through CT1–CT6 and it must survive here.

### 4.4 The floor invariant
Saturation is always index 0 and always participates, so the aggregate is `>=` sat's own
demand. Composite is never *less* aggressive on scale-up, nor *more* aggressive on scale-down,
than sat alone. Preserved from CT7 verbatim.

---

## 5. The aggregation

Notation: analyzers `i`, variants `v`, roles `r`. `L_i` = `Live`, `I_i` = informative
(`ResultIsInformative`). All demand values already converted to sat units per §4.2.

### 5.1 Gating — who participates
```
eligible(i)  ⟺  Result != nil  ∧  I_i  ∧  L_i
```
Saturation participates unconditionally (floor). Ported from
`needsScaleDownForRole`/`safeRemovalReplicasForRole`'s Live-gating, plus the
`liveCount > 0` safety floor: **if no non-sat analyzer is eligible, composite = sat.**

**[ASSUMPTION] A3 — scale-up and scale-down use the same eligibility.** The backup helpers
gate scale-down on `Live` and treat non-live as non-vetoing. I apply one uniform
`eligible()` to both directions: a stale analyzer neither raises demand nor blocks scale-down.
Simpler and matches "non-live does not constrain".

### 5.2 Demand — model level
```
composite.TotalDemand = max over eligible i of demand_sat_equivalent(i, model)
```
`max`, per CT7's floor invariant and the parent spec's explicit design rule: *"max/min per
field, **never** Score-weighted averaging."* See §7 for how Score enters without violating it.

### 5.3 Demand — role level
```
composite.RoleDemand[r] = max over eligible i (that emit role r) of
                          demand_sat_equivalent(i, r)
```
Roles enumerated from sat's `RoleCapacities` (sat is authoritative on model shape); `""`
canonicalized to `RoleBoth` via `rolesOf`/`variantsForRole` logic.

**Q2 re-derived — non-sat analyzer with no `RoleDemand`.** **[ASSUMPTION] A4:** it does **not**
participate in any per-role max, but **does** participate at model level. Rationale: inventing
per-role demand the analyzer never expressed would fabricate signal, and there is no
non-arbitrary split (equal? proportional to sat? both wrong under skew). Same conclusion p3
reached, re-derived without normalization. Consequence to accept: such an analyzer can raise
model-level demand while leaving role demands at sat's — internally inconsistent for a
disaggregated model. Mitigation in A5.

**[ASSUMPTION] A5 — role/model consistency.** After the per-role max, recompute model-level
demand from the roles using the parent framework's cross-role rule rather than trusting §5.2
alone:
```
C(M) = min( C(M,prefill), C(M,decode) ) + C(M,both)
```
i.e. for a disaggregated model the model-level figure derives from the roles (min across
prefill/decode, plus `both`), then take `max` with §5.2's model-level result to preserve the
floor. This keeps role and model figures mutually consistent — the gap Q1 was gesturing at —
and reuses the framework already established. **Recommend confirming; this is the least
certain piece of the design.**

### 5.4 Spare capacity — the conservative direction
```
composite.SpareCapacity      = min over eligible i of SC_i          (sat units)
composite.RoleCapacities[r].SpareCapacity = min over eligible i of SC_i[r]
```
`min`, mirroring `safeRemovalReplicasForRole`. Plus `needsScaleDownForRole`'s all-agree gate:
**if any eligible analyzer reports no spare for a role, the composite reports none** — one
analyzer objecting is enough to stop a scale-down.

### 5.5 Derived fields
`RequiredCapacity`, `Remaining`, `Spare`, and per-role equivalents are **derived** from
demand/PRC by `buildCapacities`, not aggregated independently — precisely the mistake CT6 made
(normalizing some fields, leaving others raw, producing `1/PRC` errors).

**[ASSUMPTION] A6 — recompute, don't reduce.** Build the composite's demand fields first, then
run the *existing* capacity-building step over it so every derived field is internally
consistent by construction. Requires compose to run **before**/as part of capacity building,
which drives §6's placement question.

---

## 6. Where normalization + compose run

**[USER]** "We normalize when we prepare the composite signal. You check the alternatives and
suggest where."

`runAnalyzersAndScore` today: run analyzers → `buildNamedResult` (+`buildCapacities`) per
entry → `updateLivenessAndSetLive` → `recordAnalyzerMetrics` → `logAnalyzerResult` → return
slice. Then `collectV2ModelRequest` (`:797`) takes `namedResults[0]`.

| Option | Placement | Assessment |
|---|---|---|
| **O1** | Inside `runAnalyzersAndScore`, after observability, before return; change return type to a single value | p3's choice. **Rejected:** the return-type change ripples into 6+ test files (the parent mission's known compile-breakage), and it destroys the per-analyzer slice that liveness/metrics/logging need — the exact churn that left `origin/single-analyzer` non-building. |
| **O2** | New function called at `collectV2ModelRequest:797`, replacing `namedResults[0]` | **RECOMMENDED.** Single-line change at the one production assignment site. `runAnalyzersAndScore` keeps its slice return, so liveness/metrics/logging are untouched. Per-analyzer observability still sees raw, per-analyzer units — which the CT6 spec confirms is correct ("each analyzer's own metric stays in that analyzer's own units"). |
| **O3** | Inside the optimizer | **Rejected.** The optimizer was made deliberately name-blind and single-entry by PR #34; re-introducing multi-entry reduction there reverses a merged design. |
| **O4** | Split: normalize per-entry early, aggregate late | **Rejected for now.** Normalizing before `recordAnalyzerMetrics` would corrupt per-analyzer metrics into sat units. |

**[ASSUMPTION] A7 — adopt O2.** Compose is a pure function `[]NamedAnalyzerResult → NamedAnalyzerResult`
called where `namedResults[0]` is read today. Smallest blast radius, no signature churn, and it
keeps observability honest. Per A6, capacity rebuilding for the composite happens inside this
step.

---

## 7. Score participation

**[USER]** "Scores for all analyzers affect the composition."

Constraint: the parent spec's design rule is explicit — *max/min per field, **never**
Score-weighted averaging* — derived from CT4's fairness investigation. `Score` is a
per-analyzer **trust/priority weight** from `AnalyzerScoreConfig`; it is a different axis from
per-model fairness.

Reconciling the instruction with the rule:

**[ASSUMPTION] A8 — Score gates and orders; it does not average.** Two concrete mechanisms,
both preserving the floor invariant and avoiding weighted averaging:
1. **Score as eligibility threshold** — an analyzer with `Score <= 0` (or below a configured
   floor) is ineligible. Zero-trust analyzers cannot move the composite.
2. **Score as tie-break/attribution** — when several analyzers tie for the max, the
   highest-Score one is recorded as the contributor (for the composite's provenance and
   logging).

**[ASSUMPTION] A9 — composite `Score`.** `max` over contributing analyzers' Scores (not sat's,
not fixed `1.0`). Rationale: `Score` feeds fair-share weighting; if a high-trust analyzer drove
the demand, that trust should follow the signal. `max` keeps it in-range and reduces to sat's
Score on the sat-only path — preserving §4.3.

**This is the assumption I am least confident in.** "Scores affect the composition" admits a
genuinely weighted reading (`Σ score_i × demand_i / Σ score_i`), which I have deliberately
*not* taken because it contradicts the parent mission's recorded max/min rule and breaks the
floor invariant (a low-scored sat could be averaged *down*). If the user wants true weighting,
that rule needs revisiting and the floor invariant needs restating. **Flagged for decision.**

---

## 8. The new composite name

**[USER]** "Composite will have a new name."

Breakage: `hasSaturationResult` (`engine_v2.go:722`) is
`CompositeSignal.Name == domain.SaturationAnalyzerName && Result != nil`. A renamed composite
makes this **false**, silently disabling the engine-side GPU-quota guard it protects. This is
precisely CT7's Q3 hazard, and the user has chosen the path Q3 called risky — so the guard must
be fixed, not left to fail.

**[ASSUMPTION] A10 — new name.** `domain.CompositeAnalyzerName = "composite"`, a new exported
constant beside `SaturationAnalyzerName`.

**[ASSUMPTION] A11 — repair `hasSaturationResult` by intent, not name.** Rename to something
like `hasUsableCapacitySignal`, and test what it actually cares about — that the composite
carries a real capacity signal — rather than an analyzer's identity. Since sat is always the
floor contributor, a non-nil composite `Result` *implies* a sat-derived signal. **[ASSUMPTION]
A12:** carry explicit provenance on the composite (e.g. contributing analyzer names) so the
check can assert sat's participation directly instead of inferring it, and so logs can explain
which analyzer drove each decision.

Also audit for name-dependence before implementing: `rescale.go:344,372,528`,
`variant_records.go:79`, `cost_aware_optimizer.go:246`. PR #34 made the optimizer name-blind
(*"zero references to `SaturationAnalyzerName` ... remain in any non-test production optimizer
file"*), so `engine_v2.go:722` is expected to be the only true name dependency — **to be
verified by grep at implementation time, not assumed.**

---

## 9. Test plan

Adapted from `multi_backup/analyzer_helpers_multi_test.go`'s 5 multi-entry cases (per CT7's
todo) plus this mission's own departures:

1. **Sat-only fast path** — composite numerically identical to sat. The regression guard.
2. Non-sat with higher sat-equivalent demand → composite demand raised.
3. Non-sat with lower demand → composite = sat (floor holds).
4. Disaggregated, per-role max where non-sat is higher for one role.
5. Non-sat not live → excluded; composite = sat.
6. Non-sat not informative (`Reason` = `no-data`/`error`) → excluded.
7. **Unit conversion** — analyzer with a deliberately different PRC scale contributes the
   correct sat-equivalent demand (the test CT6 lacked, which let its `1/PRC` bug through).
8. Non-sat with no `RoleDemand` → model-level only (A4).
9. `Score` gating (A8) and composite `Score` (A9).
10. Renamed composite → quota guard still fires (A11) — the guard-not-silently-disabled test.
11. **End-to-end** `collectV2ModelRequest` → optimizer with nonzero demand, asserting replica
    counts. The parent mission's CT6 bug survived *because* no test exercised this path.
12. Restore the 3 `Skip()`-ed multi-analyzer tests CT7's todo lists as pending this work.

---

## 10. Open items for the user

| # | Item | My recommendation |
|---|---|---|
| 1 | **A9/§7 — Score: gate+tie-break, or true weighted average?** Weighted contradicts the recorded max/min rule and breaks the floor invariant. | Gate + tie-break + `max` for composite Score |
| 2 | **A5** — derive model-level demand from roles via `min(prefill,decode)+both`? Least-certain piece. | Yes, then `max` with the direct model-level figure |
| 3 | **A2** — `ceil` per analyzer, or continuous ratio quantized once? | Continuous |
| 4 | **A7/§6** — placement at `collectV2ModelRequest:797` (O2), keeping the slice return? | Yes — avoids the parent mission's compile breakage |
| 5 | **A10** — name `"composite"`? | Yes |
| 6 | **A1** — reimplement from `multi_backup/` rather than un-ignore it? | Yes; it can't compile as-is |
| 7 | Does this mission also **restore multi-analyzer operation** in the optimizer (`cost_aware_optimizer_multi.go`), or engine-side compose only? | Engine-side only; optimizer stays single-entry per PR #34 |

## 11. Sources read

Parent-mission docs (read-only, cross-worktree reads authorized **[USER]**):
`STATE.p3-planner.md`, `2026-09-06-p3-planner-1.md`, `compose-logic-plan.md`,
`pr-spec-34-composite-signal.md`, `pr-spec-next-coverage-units.md`, `spec.md` §CT7 and
§"Semantic framework", `multi_backup/analyzer_helpers_multi.go`.
Own base branch: `engine_v2.go`, `optimizer_interfaces.go`, and the `CompositeSignal`
consumer sites listed in §2.1.

**Not read:** the parent mission's session ledgers and review docs (not needed for the design;
`session-start.md` says consult ledgers only on demand).
