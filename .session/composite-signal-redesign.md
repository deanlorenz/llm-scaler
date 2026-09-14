# Composite signal — redesign

Working doc. Not a replacement for `spec.md`. Fold back into `spec.md` once settled.
Structure follows `conventions/tasks.md`'s "Mission spec / roadmap structure": **§1-2 are the
spec** — settled rules only, what a coder implements, no reasoning or citations. **§3+ is
discussion** — facts, citations, history, open questions. Read §1-2 to implement. Read §3+
only to understand *why* or to resolve something not yet decided.

---

## 1. Quick summary

Redesign of `buildComposite` (`steadystate/composite.go`) and its helpers
(`composite_decision.go`, `aggregation/{replicas_needed,prc_com}.go`). Same underlying math as
v8 (`PRC(SO) = D_sat[role]/TotalReplicas`) — this changes code structure, naming, and one
behavior (sat's contributor eligibility), not the formula. Not yet implemented.

## 2. Spec — settled rules, what the coder builds

### 2.1 Call stack

Outer placement unchanged (already decided at spec v4/§6.2, reaffirmed v8): `buildComposite`
is called from `collectV2ModelRequest`, never from inside `runAnalyzersAndScore`.

```
collectV2ModelRequest                                 [engine_v2.go:777]
  → runAnalyzersAndScore(...)                         [:789, defined :106]
      → returns namedResults (sat first, always present; others per-enabled)
  → eligibleAnalyzers := namedResults, minus sat if !config.AnalyzerEnabled(sat)
      (resolved ONCE, here, before compose — not inside the per-SO loop)
  → buildComposite(ctx, namedResults, eligibleAnalyzers,
                    config.ScaleUpThreshold, config.ScaleDownBoundary)   [:818 → composite.go]
      (the POLICY's own default thresholds — NOT satUp/satDown from
       config.AnalyzerThresholds(sat); that call named sat outside compose,
       which is exactly the "sat-specific code outside of compose" this
       redesign forbids — corrected 2026-09-14, caught by the user)
      → iterate sat.Result.VariantCapacities directly  (no union, no fallback)
      → per SO (sat's own variant, its model+role):
          a. copy ReplicaCount, PendingReplicas, WarmPoolReplicas,
             WarmPoolPerReplicaCapacity, TotalDemand from sat — unconditional
             (this copy is UNCONDITIONAL and happens even if sat was excluded
             from eligibleAnalyzers above — identity role, never gated)
          b. contributors := every analyzer in eligibleAnalyzers where:
               (i) the analyzer itself is eligible — `allocation.eligible()`'s existing
                   gate (Result != nil && ResultIsInformative && Live), UNCHANGED from
                   today — a stale or uninformative analyzer contributes to nothing,
                   exactly as it does not today (this is the gate the v8 ResolveSO/
                   eligible() pairing already enforced; the redesign does not loosen it
                   — see the correction note below), AND, for THIS SO:
               (ii) the SO is present in that analyzer's own VariantCapacities, AND
               (iii) Reason for it is not ReasonNoData/ReasonError
             (no analyzer name is ever tested here — sat already got resolved into
             or out of eligibleAnalyzers upstream; this loop cannot tell sat apart
             from any other analyzer)
          c. if contributors is empty AND sat was excluded from eligibleAnalyzers only
             for being disabled (not for being ineligible per (b)'s own SO/model/role
             check) AND `Eligible(sat)` is true (sat has an actual, live, informative
             result — an error/no-data/stale sat must NEVER produce a fallback value,
             corrected 2026-09-14, see below): sat contributes alone as fallback — this
             is the ONE place sat is named, and it happens before/outside the symmetric
             loop, never inside it
          d. for each contributor: TotalReplicas(SO) = its own Demand(model,role) /
             its own raw PRC(SO) — both this analyzer's OWN measured values, not the
             composite's and not sat's
             CompositeTotalReplicas(SO) = max over contributors' TotalReplicas
             decision path: "single" (1 contributor) / "agree" (2+) /
                             "sat-fallback" (only step c fired) / "no-signal" (none)
          e. PRC(SO) = D_sat[role]/CompositeTotalReplicas(SO)
             — computed once per (model,role) pair (shared numerator), not once per SO
          f. log per SO: every contributor's TotalReplicas, ReplicaCount, PendingReplicas
      → maxScore(namedResults)   [unchanged, legacy field only]
  → buildCapacities(ctx, &composite, ...)   [:904, unchanged]
```

`config` needed above is already in scope — `collectV2ModelRequest` already receives
`config config.ScalingPolicy` as a parameter (`engine_v2.go:781`). No new plumbing into
`collectV2ModelRequest` itself — only the `buildComposite` call gains an argument
(`eligibleAnalyzers`, resolved right above it from `config` already in scope) and drops its
existing `satUp`/`satDown` arguments in favor of `config.ScaleUpThreshold`/
`config.ScaleDownBoundary` directly (see correction above) — the pre-existing
`config.AnalyzerThresholds(domain.SaturationAnalyzerName)` call and its surrounding comment
are deleted, not kept as dead code.

Deleted, not relocated: `findSaturation`, `unionOfVariants`,
`representativeVariantCapacity`'s fallback branch, `AggN` called on a 1-element slice, the
`if e.Name == sat` branch inside collection (replaced by the one-time upstream resolution at
`eligibleAnalyzers` — sat's name is checked ONCE, before compose, never inside the per-SO
collection loop), and `ResolveSO`/`SODecision` (`composite_decision.go`) — their only caller was
the line this section's contributor loop replaces; step (b)(i)-(iii) above fully absorbs their
logic inline. Not kept as a parallel implementation. Existing `composite_decision_test.go`
cases port to the new inline logic (via `TotalReplicas`, §2.3), not dropped.

**Correction (2026-09-14, caught during first dispatch attempt):** an earlier pass at this
section wrote (b) as only (ii)+(iii) above, omitting the `eligible()`/`Live` gate entirely.
That would have let a stale analyzer contribute to `CompositeTotalReplicas` — a real behavior
change from v8, contradicting this doc's own §1 claim ("same underlying math as v8") and the
existing `composite_eligibility_test.go` coverage. The dispatched coder caught this itself,
correctly refused to guess, and escalated rather than silently keeping or dropping the gate.
User ruling: keep the gate. `eligible()` (`composite_eligibility.go:18`, currently package-private
in `allocation`) must be reachable from `buildComposite` in `steadystate` — export it as
`Eligible` (capitalize) rather than
duplicating its three-condition check inline.

**Second correction (2026-09-14, caught during coder's third invocation, `make test`
failure):** step (c) as originally written gated the fallback only on "sat was excluded for
being disabled" — it never checked `Eligible(sat)`. That reopens exactly the staleness hole
the first correction closed, one step outside the main contributor loop: a sat with no actual
result (error, no data, stale) would still produce a fallback value whenever `contributors`
was empty, because the code never asked whether sat itself had anything real to fall back on.
User ruling (already an established, previously-discussed decision — not a new one; this doc
had simply failed to carry it into (c)): **sat with no real result must never participate in
scaling decisions, fallback or otherwise.** Sat-fallback exists ONLY for the case where sat is
disabled (`!config.AnalyzerEnabled`) but still has an actual, live, informative result — "sat
can be a fallback when it is DISABLED but still has actual results," never when sat's own
result is missing/erroring/stale. (c) now requires `Eligible(sat)` as a third, separate
condition alongside "excluded only for being disabled" — see the updated (c) above.

### 2.2 Naming

| Old | New |
|---|---|
| `N(SO)` / `N_i(SO)` (per-analyzer) | `TotalReplicas` |
| `N_com(SO)` / `AggN`'s result | `CompositeTotalReplicas` |
| `PRCCom` | retired as a named function — inlined at the assignment site |

### 2.3 File placement

Single-caller rule: a helper with exactly one external caller lives in that caller's file,
not a shared package.

- `AggN` (was: one caller, `ResolveSO`) → moves into `composite_decision.go`.
- `PRCCom` (was: one caller, `buildComposite`) → inlined into `composite.go`, not kept as a
  standalone function.
- `replicasNeeded`, `variantCapacity`, `roleOf` (aggregation package private helpers) → move
  with whichever function absorbs their only caller.
- `DemandForRole` → stays in `aggregation` (3 callers).
- `roleOf`/`roleOfVC`/`AggregateByRole`'s inline duplicate → unify into one function,
  `domain.RoleOfVC` (see correction below) — NOT `steadystate.RoleOfVC` as an earlier pass at
  this section said. `AggregateByRole`'s 2-line inline copy stays as-is (not worth a
  cross-package call for something that small); everywhere else calls `domain.RoleOfVC`.

**Correction (2026-09-14, caught by the coder during implementation, verified by `go build`):**
this section originally placed the unified role-canonicalization function in `steadystate`
(`steadystate.RoleOfVC`), reasoning from the single-caller rule as if `steadystate` were just
another package. It is not: `steadystate` already imports `allocation` in three files
(`composite.go`, `engine_v2.go`, `engine.go`), and `allocation.TotalReplicas` (§2.4/step 3)
needs to call the role function too — `allocation` importing `steadystate` would be a
compile-time import cycle. `domain` is the correct home: both packages already import it
cleanly, it already owns `VariantCapacity` (the function's only parameter) and `RoleBoth`, and
it has no reverse dependency on either package. This is a placement fix only — the function's
logic (canonicalize empty role to `RoleBoth`) is unchanged.

### 2.4 Signatures

Helpers take the already-resolved `VariantCapacity` as a parameter, not
`(result *domain.AnalyzerResult, variant string)` plus an internal search — the
composite-building loop already holds the specific `VariantCapacity` once it iterates sat's
own list directly (§2.1).

### 2.5 Decision-path type

`DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` become a typed
enum, not untyped `string` constants.

### 2.6 `HasUsableCompositeSignal` → two checks

Split into two functions, both taking only the composite `NamedAnalyzerResult`:
- (a) per-SO: does this SO's decision path differ from `DecisionNoSignal`.
- (b) whole-composite: is `Result` non-nil and at least one SO's decision path differs from
  `DecisionNoSignal`.

Update both call sites (`engine.go:1091`, `engine_v2.go:735`) to whichever check each needs.

### 2.7 Formulas (unchanged — restated only)

```
PRC(SO)                = D_sat[role(SO)] / CompositeTotalReplicas(SO)
Supply(SO)              = ReplicaCount(SO) × PRC(SO)
AnticipatedSupply(SO)   = (ReplicaCount(SO) + PendingReplicas(SO)) × PRC(SO)
SC = max(0, TotalSupply − Demand/scaleDown)
RC = max(0, Demand/scaleUp − TotalAnticipatedSupply)
```
`buildCapacities` already applies this to the composite exactly as to any analyzer result — no
change needed there.

### 2.8 Known accepted gap (code comment only, not a fix)

Composite `ReplicaCount` is sat's raw k8s ready count, not a count of replicas verified to be
usefully serving. Accepted for now; flag with a comment where it feeds Supply.

### 2.9 Non-negotiable regression guards

- **Sat-only identity**: sat as sole contributor ⇒ composite PRC for that SO exactly equals
  sat's own PRC. Must hold for BOTH `single` (sat enabled) and `sat-fallback` (sat disabled,
  nothing else contributed) — test both.
- **Score has no effect**: `maxScore` unchanged, not touched by this task.
- **Stale analyzer never contributes**: an analyzer failing `Eligible()` (not `Live`, or not
  informative, or nil `Result`) must not appear in `contributors` for any SO, regardless of
  what its per-SO `Reason` says — test with an otherwise-qualifying analyzer marked `!Live`.
- **Ineligible sat never falls back**: sat-fallback (path (c)) must only fire when `Eligible(sat)`
  is true. A sat with a nil/erroring/no-data/stale result and no other contributor must produce
  `DecisionNoSignal`, never `DecisionSatFallback` — test with sat disabled AND `!Eligible(sat)`
  (e.g. stale), confirming the SO gets no signal at all rather than silently falling back on
  sat's stale data.

### 2.10 Completeness check

Before declaring the rewrite done: compare against the pre-single-analyzer aggregation logic
at the engine side (what this mission's CT7 originally lifted out of), not only against this
doc's own step list.

---

## 3. Needs decision / still open

- **RESOLVED (2026-09-14):** "sat is the sole source of every identity field except PRC/Reason"
  (§2.1.a) is durable policy, not a narrowing. User's fuller explanation, worth keeping verbatim
  here since it reframes what an analyzer even is: every analyzer's real contract is exactly
  two numbers per SO — `Demand(model(SO), role(SO))` and `PRC(SO)` — the same contract the
  external KEDA scaler already has. Everything else on a `VariantCapacity` (ready count,
  pending, warmpool, cost, GPU count, ...) is not really "sat's computation" at all; in an
  ideal world it would be a separate, non-per-analyzer computation, and it is read from sat
  today only because that is where the data currently lives, gated on nil/error only — never
  on eligibility, enabled/disabled, or anything else. "Voting" (the contributor loop,
  `TotalReplicas`) only ever touches Demand and PRC, and only eligible analyzers vote there.
  Sat-fallback is the one exception, and only for the case "sat is disabled but still has a
  valid (non-nil/non-error) result" — never a generic "nobody else contributed" catch-all (see
  §2.1(c)'s second correction). Two related, not-yet-addressed gaps the user flagged for later,
  not for this task: (1) scale-from-zero reuses the PRC field as a fallback value for the case
  where PRC cannot be measured (no existing replicas); (2) `ReplicaCount(SO)` should ideally be
  a "goodput" replica count for a more accurate current-supply figure, but only the throughput
  analyzer provides that today — every other analyzer, including sat, gives the cruder raw
  ready count (this is the same gap as §2.8's known accepted gap, restated from the analyzer
  side rather than the composite side).
- A demand unit canonical **across models**, not just across analyzers within one model —
  `D_sat` is a stand-in, not the destination (see §5.3).
- `query_api.go`'s rounding-function naming/duplication — tracked in
  `code-review-notes.md` §10, not this doc; not yet actioned.
- `.session/spec.md`, `.session/task-coder-composite-redesign.md` still reflect an earlier,
  since-corrected version of this doc (stale call stack, over-long §4.3) — need re-sync to
  §1-2 above before the coder task is usable. Not yet done.

---

## 4. Roadmap

| Item | Status |
|---|---|
| Trace each analyzer's current ReplicaCount/PRC/Demand/RC/SC computation | done, §5.1 |
| Call stack — current vs. planned | done, §5.2 |
| How the optimizer consumes Demand/PRC/RC/SC | done, §5.3 |
| Decide what "combine across analyzers" means, per quantity | done, §5.4 |
| Sat's dual role (identity vs. contributor) reconciled with code-review rulings | done, §5.4 |
| Fold into `spec.md` v9 + coder task file | **not done — stale, needs re-sync (§3)** |
| Implement | not started |

---

## 5. Details

### 5.1 Facts about today's per-analyzer computation (verified against code)

There are exactly 3 analyzers: `external`, `saturation_v2`, `throughput`.

**ReplicaCount, PRC, Demand:**

| | ReplicaCount | PRC | Demand |
|---|---|---|---|
| **external** | `CurrentReplicas − PendingReplicas` (raw k8s) `analyzer.go:171` | config constant `body.Threshold`, not measured `:180` | 1 PromQL query, total only `:164` |
| **saturation_v2** | `CurrentReplicas − PendingReplicas` (raw k8s, same formula as external) `analyzer.go:661,673` | `median(ownCapacities)` over ALL `ReplicaMetrics` rows for the variant, minus bridges — no Ready filter `:708-727` | `Σ rc.ReplicaDemand` over ALL rows incl. bridges `:715` |
| **throughput** | `nKV` = count of rows with `TotalKvCapacityTokens > 0` (real signal, not a naive gap) `:319,676-691` | `sum/n` over the same set `:691` | `computeDemand`, unfiltered `variantMetrics` |

Saturation's `ReplicaCount` is k8s-status arithmetic, not a count of monitored rows — verified
at `analyzer.go:673`, `replicaCount := readyCount`, and
`readyCount := vs.CurrentReplicas - vs.PendingReplicas` (`:661`) (an earlier version of this
doc claimed otherwise — corrected). None of the 3 analyzers read `ReplicaMetrics.Ready`
(grepped; the field is set at collection, `collector/replica_metrics.go:1148`, never consumed
after).

**Bridge (`FromWarmPool`) filtering** is a separate axis from readiness, implemented twice
(saturation: inline loop `analyzer.go:711-724`; throughput: named func `:701`):
- saturation: bridges excluded from PRC's median only; Demand sums every row including
  bridges (deliberate, `:715` — a bridge's traffic is real); ReplicaCount uses neither.
- throughput: bridges pre-excluded upstream (`:248`), before `nKV`/PRC are computed — coupled
  by construction, unlike saturation's disconnected ReplicaCount/PRC.

**Which output field each filter actually touches, traced precisely:**

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

**`nKV` correction [USER]:** not an oversight that includes booting replicas — it attempts to
count replicas actually serving via a real signal (KV cache configured, gated only by
`TotalKvCapacityTokens>0`, a real not proxy signal that the engine's KV cache is allocated,
`collector/replica_metrics.go:755-767`), not a proxy. Gap: KV-cache-configured and k8s-Ready
are two independently-timed signals with no code-level guarantee they align.

**SC traced end to end:** `SC = max(0, TotalSupply − Demand/scaleDown)`
(`engine_v2.go:550-554`); `TotalSupply = Σ ReplicaCount(v)×PRC(v)` (`aggregation.go:51-57`) —
uses `ReplicaCount`, not `AnticipatedSupply`'s ready+pending sum. Pending replicas never enter
SC. Bridges excluded from `TotalSupply`'s numerator but included in `Demand` → SC subtracts
bridge-served demand without crediting bridge-served supply. Net: SC reflects only
"ready, non-bridge" capacity, minus demand that includes bridge traffic.

**Cross-SO leakage**: both saturation_v2 and throughput group `ReplicaMetrics` by
`VariantName` (`byVariant` map, `saturation_v2/analyzer.go:632-633`,
`throughput/analyzer.go:92,282`) before any of the above, so another SO's pods are excluded by
that grouping key (whether the tag itself is trustworthy is a separate, unchecked
discovery-level question).

**CurrentReplicas/PendingReplicas** are computed once upstream, shared by every analyzer
(`variantmeta/discovery.go:79-84`):
```
currentReplicas := scaleTarget.GetStatusReplicas()  (fallback: GetReplicas())
readyReplicas   := scaleTarget.GetStatusReadyReplicas()
pendingReplicas := currentReplicas - readyReplicas   (clamped ≥ 0)
```
Read directly from the k8s scale target's status, once per variant, in `Discover()` — not
per-analyzer. Flows into `domain.VariantMetadata` → `VariantReplicaState`, the same shared
input every analyzer receives (`saturation_analyzer.go:220-228`).

### 5.2 Call stack — prior (v8, pre-redesign, reference only)

```
collectV2ModelRequest → runAnalyzersAndScore → buildComposite(namedResults)
  → findSaturation (lookup #1) → unionOfVariants → per variant:
      representativeVariantCapacity (lookup #2, fallback to non-sat analyzer)
      → ResolveSO: if e.Name==sat (lookup #3, special-cased) → AggN([e.Result], v) [1-elem, dead]
      → inline others/sat combine (duplicated aggregation)
    → PRCCom(sat.Result, role, N, ok)
  → maxScore → buildCapacities
```
An earlier version of this doc showed `buildComposite` nested inside `runAnalyzersAndScore` —
wrong; corrected to the outer placement in §2.1 (the O2 decision was already settled at spec
v4/§6.2, reaffirmed in v8 after a coder mistakenly moved it to O1 and had to revert).

Not yet reviewed for correctness: `analyzer_helpers.go`, `query_api.go`,
`cost_aware_optimizer.go`, `rescale.go` — see `STATE.md`.

### 5.3 How the optimizer uses Demand/PRC/RC/SC [USER]

1. Optimizer decides on Demand and PRC.
2. Demand that matters: RC/SC, per role (prefill/decode/both).
3. Decision is in replica/GPU counts → the number that matters is ~always Demand/PRC.
4. Optimizer also needs cost + other metadata.
5. CompositeSignal's job: one Demand, one PRC (+ metadata) per dimension, collapsed across
   analyzers only — not across SOs, not across models.

Demand is per model_id, reported per role by the analyzer directly:
- `AnalyzerResult.TotalDemand` — role "both"/"" (`analyzer.go:117-119`)
- `AnalyzerResult.RoleDemand[role]` — "prefill"/"decode" (`analyzer.go:121-126`)

RC/SC are derived, not analyzer-emitted, same formula at every scope
(`applyUniversalThreshold`, `engine_v2.go:536-574`):
```
RC = max(0, Demand/scaleUp − AnticipatedSupply)
SC = max(0, Supply − Demand/scaleDown)
```
This happens at the Engine via a helper, not something the composite re-derives from scratch.

Supply/AnticipatedSupply, verified `aggregation.go:50-69`:
```
Supply(analyzer)            = Σ_v ReplicaCount(v) × PRC(v)
AnticipatedSupply(analyzer) = Σ_v (ReplicaCount(v) + PendingReplicas(v)) × PRC(v)
```
Computed per analyzer, over that analyzer's own `VariantCapacities` — `ReplicaCount` is
analyzer-specific, so Supply/AnticipatedSupply inherit that difference directly.

PRC is per SO (model, role, GPU, variant metadata): `VariantCapacity.PerReplicaCapacity`.

The 3 real inputs, per [USER]: Demand, (Total) Supply, Anticipated Capacity — each
reported/computed by every analyzer, in its own unit, per role. CompositeSignal's job is to
combine these across analyzers, per role (and PRC per SO) — not re-derive RC/SC from zero.

### 5.4 How §2's rules were reached [USER, verified against code]

**§2.1.a (sat is the sole identity source) and §2.1.b's sat-contributor gating are two
different questions, not competing answers** — this was corrected once already in this
session, recorded here so it isn't re-litigated:
- **Identity role**: which analyzer's raw fields populate the composite's own fields
  (ReplicaCount, PendingReplicas, TotalDemand, the variant set itself). Sat, unconditionally.
- **Contributor role**: whether sat counts as an ordinary voice in
  `CompositeTotalReplicas`'s max, versus only a fallback. Conditional on
  `config.AnalyzerEnabled(sat)` — this is new, sourced from the user's own earlier code review
  (`code-review-notes.md` §7/§9.1/§9.2/§8.6), not something this redesign invented.

These compose without conflict: sat can supply identity fields unconditionally while also
being excluded from ordinary contributor status when disabled — different questions about the
same analyzer.

**[USER] correction on mechanism, applied in §2.1:** the config-enabled check must not appear
as a name-based branch inside the per-SO collection loop — after compose, sat must not be
visible as a special case there. The check happens exactly once, upstream of the loop
(`eligibleAnalyzers`), so the loop itself only ever asks SO/model/role-level questions
(present? Reason ok?), never "is this analyzer sat." Sat's name is checked in exactly one
place — resolving `eligibleAnalyzers` — not scattered through collection.

**Per-SO participation (§2.1.b.i-ii), verified against all 3 analyzers' actual failure-path
code:** throughput and external already opt out of a bad SO by never appending a
`VariantCapacity` for it at all (`throughput/analyzer.go:304-312`'s `continue` on `ok==false`;
external never emits a no-data/error sentinel at all). Only saturation_v2 additionally has a
present-but-bad case (`Reason = satReasonNoData`/`ReasonError`,
`saturation_v2/analyzer.go:759,1209`), because it cannot opt out by omission — it must always
stay in as the identity source. So the per-SO check (present AND not no-data/error) is exactly
what `variantCapacity()`'s existing "present" check plus the existing sentinel check already
give, just applied per-SO instead of `ResultIsInformative`'s current model-wide any-hit check.

**Sat's config-enabled requirement (§2.1.b, §2.3's `AggN`/`PRCCom` relocation), verified
against current code, not the review notes' 2026-09-09 snapshot:** `config.AnalyzerEnabled`
exists (`saturation_scaling.go:657`), called today only for non-sat analyzers
(`engine_v2.go:170`) — sat is exempted upstream and never checked. Neither `eligible()` nor
`ResolveSO`/`buildComposite` receives a `ScalingPolicy`/config today — this is a genuine
signature/data-flow change, not a conditional add. Non-live/broken sat must keep opting out,
never crashing (existing controller precedent).

**§2.1.d (PRC loops per role):** demand is shared by every SO of a role — look the numerator
up once per role, not once per SO, to avoid a repeated lookup.

**§2.6 (two checks, no sat mention):** neither replacement function takes sat as an input —
both operate only on the composite's own fields. By the time either runs, sat's identity is
already folded into the composite (§2.1.a), so "the composite has no usable Result" and "sat
itself produced nothing" are the same fact, not two separate things to check.

**§2.7 (Supply/AnticipatedSupply/RC/SC unchanged):** `buildCapacities` (`engine_v2.go:904`)
already runs on the composite exactly as on any analyzer result, confirmed by reading the
call site (`composite.go:153`) — nothing about this redesign changes that pipeline.

**§2.8's gap** is intentionally deferred, not fixed: Supply should conceptually be based on
replicas verified to be usefully serving, not merely "ready" — accepted as good enough for now.

**Disagreement between analyzers is not a correctness issue** — SC/RC are computed once, after
composition, so two analyzers' opposing signals never reach the optimizer directly. The
composition-level log (§2.1.e) is the whole answer for observability; nothing further to
decide.

**Not addressed by this redesign:** a demand unit canonical across models (not just across
analyzers within one model) — `D_sat` is today's stand-in, not the durable destination. This
traces to the user's original code-review remark that `satDemand`/`D_sat` naming encodes a
transitional choice.

### 5.5 Not yet reviewed at all (per STATE.md)

`query_api.go`, `analyzer_helpers.go`, `cost_aware_optimizer.go`, `rescale.go`,
`constants/metrics.go`, `docs/reference/cycle-log.md`, all test files. The line-by-line review
of `composite.go`/steadystate wiring was interrupted by this redesign; resume only after the
redesign is implemented, since `composite.go` will not exist in its reviewed (v8) form after.

---

## Revision log
- 2026-09-12: created; rewritten short; optimizer-usage explanation and granularity
  correction added (aggregate across analyzers only, not SOs or models).
- 2026-09-13: call-stack duplicate heading fixed; code-review items pointer-referenced (not
  yet folded in); §4 (combine-across-analyzers) resolved — sat sole source of every field
  except PRC/Reason, and of the variant set.
- 2026-09-14 (first pass): call-stack placement corrected (buildComposite is NOT inside
  runAnalyzersAndScore); code-review items actually folded in this time (sat's dual role,
  typed decision paths, two-check signal gate, PRC-per-role shape) — previously only
  pointer-referenced, not applied.
- 2026-09-14 (second pass, user review): four fixes — (1) call-stack's inner body still
  showed old v8 internals under the corrected outer placement; (2) the sat-dual-role section
  was too long/citation-heavy for a coder to use directly; (3) disagreement-logging text
  contradicted itself by calling something "open" that was already answered; (4) a new,
  separate finding (query_api.go rounding-function naming/duplication) was filed to
  code-review-notes.md instead of this doc.
- 2026-09-14 (third pass, user review): **full restructure**, not a patch — user identified
  that this doc was mixing the code specification with the discussion that produced it,
  against the already-established mission-spec structure in `conventions/tasks.md` (§1-2
  upfront/settled, §3+ on-demand/discussion). Rewritten as §1 (summary), §2 (spec — rules
  only, no citations, what the coder implements), §3 (open items), §4 (roadmap), §5 (details —
  every fact, citation, and piece of reasoning previously mixed into §1-4, preserved, not
  deleted). No content dropped; only reorganized. `spec.md`/the coder task file are now known
  stale relative to this version — re-sync is the next step, tracked in §3.
