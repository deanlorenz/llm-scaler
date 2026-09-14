# Composite signal — redesign

Working doc. Not a replacement for `spec.md`. Fold back into `spec.md` once settled.
Structure follows the revised mission-spec template (`suggestion-box` draft,
`.session/drafts/suggestion-box-2026-09-14-2100.md`, not yet adopted into `conventions/tasks.md`):
§1-2 human-readable orientation+plan, §3 open items (blocking only), §4 coder task hierarchy,
§5-6 discussion abstracts + decision summary, §7 detailed discussion, §8 revision log.

---

## 1. Orientation

Redesign of `buildComposite` (`steadystate/composite.go`) and its helpers
(`composite_decision.go`, `aggregation/{replicas_needed,prc_com}.go`). Same underlying math as
v8 (`PRC(SO) = D_sat[role]/TotalReplicas`) — this changes code structure, naming, and one
behavior (sat's contributor eligibility), not the formula.

**Status:** implemented, verified, both regression-guard paths tested end to end
(`bff67c6f`/`6eb92892`/`4d864175`/`748261de` on branch `composite-analyzer`). Not yet reviewed
by the user; not yet folded back into `spec.md`.

**Analyzer contract, for context on every rule below:** every analyzer's real contract is
exactly two numbers per SO — `Demand(model(SO), role(SO))` and `PRC(SO)` — the same contract
the external KEDA scaler already has. Everything else on a `VariantCapacity` (ready count,
pending, warmpool, cost, GPU count, ...) is infrastructure data, not really "sat's
computation" — read from sat only because that's where it lives today. "Voting" (the
contributor loop, §2's step b-d) only ever touches Demand/PRC, only via eligible analyzers.

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
      (the POLICY's own default thresholds, never an analyzer-specific one — see §7)
      → iterate sat.Result.VariantCapacities directly  (no union, no fallback)
      → per SO (sat's own variant, its model+role):
          a. copy ReplicaCount, PendingReplicas, WarmPoolReplicas,
             WarmPoolPerReplicaCapacity, TotalDemand from sat — unconditional
             (happens even if sat was excluded from eligibleAnalyzers above —
             identity role, never gated; see §5/§7 for why)
          b. contributors := every analyzer in eligibleAnalyzers where:
               (i) the analyzer itself is eligible — `allocation.Eligible()`
                   (Result != nil && ResultIsInformative && Live), AND, for THIS SO:
               (ii) the SO is present in that analyzer's own VariantCapacities, AND
               (iii) Reason for it is not ReasonNoData/ReasonError
             (no analyzer name is ever tested here — sat already got resolved into
             or out of eligibleAnalyzers upstream; this loop cannot tell sat apart
             from any other analyzer)
          c. if contributors is empty AND sat was excluded from eligibleAnalyzers only
             for being disabled (not for being ineligible per (b)'s own SO/model/role
             check) AND `Eligible(sat)` is true: sat contributes alone as fallback —
             this is the ONE place sat is named, before/outside the symmetric loop
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
(`eligibleAnalyzers`) and uses `config.ScaleUpThreshold`/`config.ScaleDownBoundary` directly
instead of an analyzer-specific threshold call — see §7 for why.

Deleted, not relocated: `findSaturation`, `unionOfVariants`,
`representativeVariantCapacity`'s fallback branch, `AggN` called on a 1-element slice, the
`if e.Name == sat` branch inside collection, `ResolveSO`/`SODecision` (`composite_decision.go`)
— step (b)(i)-(iii) above fully absorbs their logic inline, not kept as a parallel
implementation. Existing `composite_decision_test.go` cases port to the new inline logic (via
`TotalReplicas`, §2.3), not dropped.

### 2.2 Naming

| Old | New |
|---|---|
| `N(SO)` / `N_i(SO)` (per-analyzer) | `TotalReplicas` |
| `N_com(SO)` / `AggN`'s result | `CompositeTotalReplicas` |
| `PRCCom` | retired as a named function — inlined at the assignment site |

### 2.3 File placement

Single-caller rule: a helper with exactly one external caller lives in that caller's file,
not a shared package.

- `AggN` (was: one caller, `ResolveSO`) → moved into `composite_decision.go`.
- `PRCCom` (was: one caller, `buildComposite`) → inlined into `composite.go`, not kept as a
  standalone function.
- `replicasNeeded`, `variantCapacity`, `roleOf` (aggregation package private helpers) → moved
  with whichever function absorbed their only caller.
- `DemandForRole` → stays in `aggregation` (3 callers).
- `roleOf`/`roleOfVC`/`AggregateByRole`'s inline duplicate → unified into `domain.RoleOfVC`
  (NOT `steadystate` — see §7 for why). `AggregateByRole`'s 2-line inline copy stays as-is
  (not worth a cross-package call for something that small).

### 2.4 Signatures

Helpers take the already-resolved `VariantCapacity` as a parameter, not
`(result *domain.AnalyzerResult, variant string)` plus an internal search — the
composite-building loop already holds the specific `VariantCapacity` once it iterates sat's
own list directly (§2.1).

`eligible()` (`allocation`, `composite_eligibility.go:18`, was package-private) → exported as
`Eligible` so `steadystate.buildComposite` can call it.

### 2.5 Decision-path type

`DecisionAgree`/`DecisionSingle`/`DecisionSatFallback`/`DecisionNoSignal` become a typed
enum (`DecisionPath string`), not untyped `string` constants.

### 2.6 `HasUsableCompositeSignal` → two checks

Split into two functions, both taking only the composite `NamedAnalyzerResult`:
- `SOHasSignal(composite, variant) bool` — this SO's decision path != `DecisionNoSignal`.
- `CompositeHasSignal(composite) bool` — `Result` non-nil and ≥1 SO's path != `DecisionNoSignal`.

Call sites: `engine.go:1091` → `CompositeHasSignal` (whole-request check, no SO in scope).
`engine_v2.go:735`, previously reached via `hasSaturationResult` → `hasSaturationResult` is
deleted (its body already only delegated to `CompositeHasSignal`; its sat-specific name
violated "sat invisible downstream" — see §7). Its two call sites (`:693`, `:714`) call
`CompositeHasSignal` directly.

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
  `DecisionNoSignal`, never `DecisionSatFallback`.

### 2.10 Completeness check

Before declaring the rewrite done: compare against the pre-single-analyzer aggregation logic
at the engine side (what this mission's CT7 originally lifted out of), not only against this
doc's own step list. **Done** — see §7.

---

## 3. Open items (blocking only)

- `query_api.go`'s rounding-function naming/duplication — tracked in `code-review-notes.md`
  §10, not this doc; not yet actioned.
- `.session/spec.md` still reflects an earlier, since-corrected version of this doc — re-sync
  not yet done, not blocking implementation.
- A demand unit canonical **across models**, not just across analyzers within one model —
  `D_sat` is a stand-in, not the destination. Not this task's scope; see §7.
- User has not yet reviewed the implemented code (`bff67c6f` and follow-ups).

---

## 4. Coder task hierarchy

| Task file | Scope | Status |
|---|---|---|
| `.session/task-coder-composite-redesign.md` | All of §2, 10 steps | DONE — `bff67c6f`/`6eb92892`/`4d864175`/`748261de` |

---

## 5. Discussion abstracts

- **Identity vs. contributor are different questions about the same analyzer** (§2.1.a vs.
  §2.1.b) — sat supplies identity fields unconditionally while separately being excluded from
  ordinary contributor status when disabled. Not a conflict; see §7.
- **Sat's name is checked exactly once**, resolving `eligibleAnalyzers` upstream of the per-SO
  loop — never inside collection. See §7.
- **Per-SO participation check is the existing "present + not-no-data/error" check**, applied
  per-SO instead of `ResultIsInformative`'s old model-wide any-hit check — verified against all
  3 analyzers' actual failure-path code. See §7.
- **Three pre-existing "sat-specific code outside compose" violations were found and fixed
  during implementation, none introduced by this redesign**: the `buildComposite` call site
  naming sat to get its thresholds (fixed → policy defaults); `hasSaturationResult`'s
  sat-specific name (deleted, body already delegated); `RoleOfVC` almost placed in
  `steadystate` (would have been a 4th, caught before landing — see below). See §7.
- **The eligibility gate must survive into the contributor loop, and into the sat-fallback
  branch separately** — two related but distinct catches during implementation (first in the
  main loop, before any code was written; second in the fallback branch, via a failing
  ported test). See §7.
- **Role canonicalization belongs in `domain`, not `steadystate`** — an import-cycle catch,
  verified by `go build`, not just reasoning. See §7.
- **Disagreement between analyzers is not a correctness issue** — SC/RC are computed once,
  after composition, so two analyzers' opposing signals never reach the optimizer directly.
- **§2.8's gap is intentionally deferred** — Supply should conceptually be based on replicas
  verified to be usefully serving, not merely "ready"; accepted as good enough for now.

## 6. Summary of decisions

| # | Decision | Impact | Rejected alternative | Ref |
|---|---|---|---|---|
| D1 | Sat is unconditional identity source for all fields except PRC/Reason | Durable policy, not a narrowing specific to this redesign | Making identity conditional on eligibility/enabled — rejected: conflates two different questions | §7.1 |
| D2 | Contributor eligibility gate (`Eligible()`) unchanged from v8, must gate the per-SO contributor loop | Prevents a stale/uninformative analyzer from moving `CompositeTotalReplicas` | Dropping the gate for a simpler loop — rejected: real behavior change from v8, breaks existing test coverage | §7.2 |
| D3 | Sat-fallback requires `Eligible(sat)`, not just "disabled" | A sat with no real result never participates, fallback or otherwise | Fallback firing on "contributors empty" alone — rejected: reopens the exact staleness hole D2 closed | §7.2 |
| D4 | `buildComposite`'s thresholds come from `config.ScaleUpThreshold`/`ScaleDownBoundary`, never an analyzer-specific lookup | No sat-specific code outside compose | Keeping `config.AnalyzerThresholds(sat)` at the call site — rejected: names sat outside compose | §7.3 |
| D5 | `hasSaturationResult` deleted; callers use `allocation.CompositeHasSignal` directly | Sat invisible downstream, no dead wrapper | Keeping the wrapper for its historical name — rejected: violates "sat invisible downstream" for no functional benefit | §7.3 |
| D6 | Role canonicalization unified as `domain.RoleOfVC` | No import cycle | `steadystate.RoleOfVC` — rejected: `allocation` needs to call it too, and `steadystate` already imports `allocation` | §7.4 |
| D7 | Coder proposes code-level design; mission owner (sometimes user) validates before implementation — new process rule, not specific to this doc | Applies to all future coder task files on this mission | Coder designs and implements in one unvalidated pass — rejected: root cause of this redesign's first-pass code quality problems | ledger `2026-09-14-composite-analyzer-2.md` |

---

## 7. Detailed discussion

### 7.1 Facts about today's per-analyzer computation (verified against code)

There are exactly 3 analyzers: `external`, `saturation_v2`, `throughput`.

**ReplicaCount, PRC, Demand:**

| | ReplicaCount | PRC | Demand |
|---|---|---|---|
| **external** | `CurrentReplicas − PendingReplicas` (raw k8s) `analyzer.go:171` | config constant `body.Threshold`, not measured `:180` | 1 PromQL query, total only `:164` |
| **saturation_v2** | `CurrentReplicas − PendingReplicas` (raw k8s, same formula as external) `analyzer.go:661,673` | `median(ownCapacities)` over ALL `ReplicaMetrics` rows for the variant, minus bridges — no Ready filter `:708-727` | `Σ rc.ReplicaDemand` over ALL rows incl. bridges `:715` |
| **throughput** | `nKV` = count of rows with `TotalKvCapacityTokens > 0` (real signal, not a naive gap) `:319,676-691` | `sum/n` over the same set `:691` | `computeDemand`, unfiltered `variantMetrics` |

Saturation's `ReplicaCount` is k8s-status arithmetic, not a count of monitored rows — verified
at `analyzer.go:673`, `replicaCount := readyCount`, and
`readyCount := vs.CurrentReplicas - vs.PendingReplicas` (`:661`). None of the 3 analyzers read
`ReplicaMetrics.Ready` (grepped; the field is set at collection,
`collector/replica_metrics.go:1148`, never consumed after).

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

**Prior call stack (v8, pre-redesign, reference only):**
```
collectV2ModelRequest → runAnalyzersAndScore → buildComposite(namedResults)
  → findSaturation (lookup #1) → unionOfVariants → per variant:
      representativeVariantCapacity (lookup #2, fallback to non-sat analyzer)
      → ResolveSO: if e.Name==sat (lookup #3, special-cased) → AggN([e.Result], v) [1-elem, dead]
      → inline others/sat combine (duplicated aggregation)
    → PRCCom(sat.Result, role, N, ok)
  → maxScore → buildCapacities
```

Not yet reviewed for correctness: `analyzer_helpers.go`, `query_api.go`,
`cost_aware_optimizer.go`, `rescale.go`, `constants/metrics.go`, `docs/reference/cycle-log.md`,
all test files under `aggregation/`/`allocation/composite_*_test.go` — see `STATE.md`.

### 7.2 How §2's rules were reached, and what went wrong during implementation [USER, verified against code]

**§2.1.a (sat is the sole identity source) and §2.1.b's sat-contributor gating are two
different questions, not competing answers:**
- **Identity role**: which analyzer's raw fields populate the composite's own fields
  (ReplicaCount, PendingReplicas, TotalDemand, the variant set itself). Sat, unconditionally.
- **Contributor role**: whether sat counts as an ordinary voice in `CompositeTotalReplicas`'s
  max, versus only a fallback. Conditional on `config.AnalyzerEnabled(sat)` — sourced from the
  user's own earlier code review (`code-review-notes.md` §7/§9.1/§9.2/§8.6), not invented by
  this redesign.

These compose without conflict: sat can supply identity fields unconditionally while also
being excluded from ordinary contributor status when disabled — different questions about the
same analyzer.

**Mechanism correction [USER]:** the config-enabled check must not appear as a name-based
branch inside the per-SO collection loop. The check happens exactly once, upstream of the loop
(`eligibleAnalyzers`), so the loop itself only ever asks SO/model/role-level questions, never
"is this analyzer sat."

**Per-SO participation (§2.1.b.ii-iii), verified against all 3 analyzers' actual failure-path
code:** throughput and external already opt out of a bad SO by never appending a
`VariantCapacity` for it at all (`throughput/analyzer.go:304-312`'s `continue` on `ok==false`;
external never emits a no-data/error sentinel at all). Only saturation_v2 additionally has a
present-but-bad case (`Reason = satReasonNoData`/`ReasonError`,
`saturation_v2/analyzer.go:759,1209`), because it cannot opt out by omission — it must always
stay in as the identity source.

**Sat's config-enabled requirement, verified against current code, not the review notes'
2026-09-09 snapshot:** `config.AnalyzerEnabled` exists (`saturation_scaling.go:657`), called
today only for non-sat analyzers (`engine_v2.go:170`) — sat is exempted upstream and never
checked. Neither `eligible()` nor `ResolveSO`/`buildComposite` received a `ScalingPolicy`/config
before this redesign — a genuine signature/data-flow change, not a conditional add. Non-live/
broken sat must keep opting out, never crashing (existing controller precedent).

**Incident 1 — eligibility gate omitted from the first dispatch, caught by the coder before
writing any code:** an early draft of §2.1.b listed only the per-SO checks (present, Reason ok)
and omitted the analyzer-level `Eligible()`/`Live` gate — which would have let a stale analyzer
contribute to `CompositeTotalReplicas`, contradicting v8 behavior and existing
`composite_eligibility_test.go` coverage. The dispatched coder caught this itself before
writing any code, refused to guess, escalated on its `Out:` channel. Ruling: keep the gate;
export `eligible` as `Eligible`. Not a new rule — v8's existing `ResolveSO`/`eligible()`
pairing, carried forward unchanged.

**Incident 2 — sat-fallback missing the same gate, caught by the coder's own ported test
failing on its third implementation pass:** §2.1.c as originally drafted gated the fallback
only on "sat was excluded for being disabled," never checking `Eligible(sat)` — reopening the
staleness hole incident 1 closed, one step outside the main loop. Ruling (an already-
established decision this doc had simply failed to carry into §2.1.c, not a new one): sat with
no real result must never participate, fallback or otherwise. Sat-fallback exists only when
sat is disabled but still has an actual, live, informative result.

**Incident 3 — role canonicalization almost placed in the wrong package, caught by the coder
mid-implementation, verified by `go build`:** an early draft of §2.3 placed the unified role
function in `steadystate`, reasoning from the single-caller rule as if `steadystate` were an
ordinary package. It is not: `steadystate` imports `allocation` in three files, and
`allocation.TotalReplicas` needs the role function too — `allocation` importing `steadystate`
would be a compile-time import cycle. `domain` is correct: both packages import it cleanly, it
already owns `VariantCapacity` and `RoleBoth`, and has no reverse dependency on either package.

**Incident 4 — a pre-existing, not-redesign-introduced sat-specific-code-outside-compose bug,
caught by the user by inspection:** `engine_v2.go`'s `buildComposite` call site named
`domain.SaturationAnalyzerName` via `config.AnalyzerThresholds(sat)` to source `satUp`/
`satDown` — this predates v9 entirely. Fixed to use `config.ScaleUpThreshold`/
`ScaleDownBoundary` directly, no analyzer name involved. Same underlying category as
`hasSaturationResult` (§2.6) — a pre-existing violation of "sat invisible downstream," not
something this redesign introduced, caught and fixed during v9's implementation rather than
carried forward.

**Process finding, root-caused with the user after incident 1-3's pattern became clear:** the
common thread across incidents 1-3 is that the coder caught each one itself, correctly refused
to guess, and escalated — the process worked. What was missing is a design-validation
checkpoint BEFORE implementation, not just correctness-checking during it: the coder should
propose its code-level design first, get it validated (by the mission owner, sometimes the
user), and only then implement — catching incidents 1/3's category of mistake before any code
is written, not after. New rule, applies to future coder task files on this mission (see §6/D7).

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
composition-level log (§2.1.f) is the whole answer for observability.

**Not addressed by this redesign:** a demand unit canonical across models (not just across
analyzers within one model) — `D_sat` is today's stand-in, not the durable destination. Traces
to the user's original code-review remark that `satDemand`/`D_sat` naming encodes a
transitional choice.

### 7.3 The analyzer contract, in full [USER, resolved 2026-09-14]

Every analyzer's real contract is exactly two numbers per SO — `Demand(model(SO), role(SO))`
and `PRC(SO)` — the same contract the external KEDA scaler already has. Everything else on a
`VariantCapacity` (ready count, pending, warmpool, cost, GPU count, ...) is not really "sat's
computation" at all; in an ideal world it would be a separate, non-per-analyzer computation,
and it is read from sat today only because that is where the data currently lives, gated on
nil/error only — never on eligibility, enabled/disabled, or anything else. "Voting" (the
contributor loop, `TotalReplicas`) only ever touches Demand and PRC, and only eligible
analyzers vote there. Sat-fallback is the one exception, and only for "sat is disabled but
still has a valid (non-nil/non-error) result" — never a generic "nobody else contributed"
catch-all. This resolves §2.1.a as durable policy, not a narrowing specific to this redesign.

Two related, deferred gaps, not this task's scope: (1) scale-from-zero reuses the PRC field as
a fallback value for the case where PRC cannot be measured (no existing replicas); (2)
`ReplicaCount(SO)` should ideally be a "goodput" replica count for a more accurate
current-supply figure, but only the throughput analyzer provides that today — every other
analyzer, including sat, gives the cruder raw ready count (same gap as §2.8, restated from the
analyzer side).

### 7.4 How the optimizer uses Demand/PRC/RC/SC [USER]

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

---

## 8. Revision log
- 2026-09-12: created; rewritten short; optimizer-usage explanation and granularity
  correction added (aggregate across analyzers only, not SOs or models).
- 2026-09-13: call-stack duplicate heading fixed; code-review items pointer-referenced (not
  yet folded in); combine-across-analyzers resolved — sat sole source of every field except
  PRC/Reason, and of the variant set.
- 2026-09-14 (first pass): call-stack placement corrected (buildComposite is NOT inside
  runAnalyzersAndScore); code-review items actually folded in this time.
- 2026-09-14 (second pass, user review): call-stack inner body, sat-dual-role section length,
  disagreement-logging self-contradiction, query_api.go finding filed elsewhere.
- 2026-09-14 (third pass, user review): full restructure into `tasks.md`'s then-existing
  §1-5 template — settled rules vs. discussion split.
- 2026-09-14 (fourth pass, user review, this revision): full restructure again into the
  revised 8-section template (§6 of this pass's discussion) — §2 compacted back down after it
  had drifted to 1,542 words via four inline "Correction" narrative paragraphs added
  mid-incident; those, plus §3's resolved analyzer-contract paragraph, moved to §7 as
  processed discussion; new §6 (summary of decisions) added; §4 (coder task hierarchy) added
  as its own section. No content dropped — verified against the pre-revision snapshot before
  committing.
