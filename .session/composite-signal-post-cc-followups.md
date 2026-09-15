# Composite signal — post-CC follow-ups

Companion to `composite-signal-redesign.md`. That doc's §2.11-2.13 hold the CC-scoped guard
fixes (settled, minimal, must-have). This doc holds everything else the 2026-09-15 PRC/usage
review surfaced — deferred, not forgotten. Same 8-section structure
(`conventions/tasks.md`'s mission-spec template); §1-2 upfront, rest on demand.

---

## 1. Orientation

The 2026-09-15 review (full `CompositeSignal` usage audit, then a line-by-line user review of
it) found several real gaps beyond the 3 CC-scoped guard fixes. This doc records each one
precisely enough to act on later, without re-deriving the investigation.

## 2. Principles / approach

- CC ships only: the `<=0` supply-sum guard, the model-level `CompositeHasSignal()` demand guard, and the
  `sortVariantsForScaleDown` Score removal (redesign doc §2.11-2.13).
- Everything below is explicitly deferred — deferring is not "rejected," it is "postponed to a
  separate change." Each item below should become its own task file when picked up.

## 3. Needs me (decisions still open)

1. **`prcForVariant`/`vc.PerReplicaCapacity` same-contract unification** — should both
   guarantee "valid XOR guaranteed `<=0`," with no direct field access bypassing the guard.
   Deferred; CC relies on the `<=0` guard already being correct at each call site instead.
2. **Move the `<=0` guard into the shared helper/accessor functions** so it is structural
   (checked once, in one place) rather than repeated per call site. Deferred, post-CC.
3. **P0-store-as-fallback design question (§7.1):** should a P0-store-estimated SO be
   distinguishable from a genuine live sat-fallback anywhere downstream (both currently produce
   identical `DecisionSatFallback`/`C2-sat-fallback`)? Deferred pending a broader no-signal
   design pass.
4. **Per-role and per-analyzer demand-health markers** — nothing today marks a specific
   `D_sat[role]` value as itself broken (e.g. a bad PromQL filter) independent of the model-level
   `CompositeHasSignal()` guard added in CC §2.12. Deferred; producer-side (analyzer) work, out of this
   mission's consumer-side scope as currently framed.
5. **Supply-alternatives analysis** — should each analyzer report its own per-SO supply
   estimate, instead of the composite deriving supply from `ReplicaCount × PRC` alone? Currently
   rejected/deferred, but the alternative itself was never written up — do that before closing
   the question, not just recording the rejection.

## 4. Roadmap / checklist

- [ ] `prcForVariant`/`PerReplicaCapacity` contract unification (§7.1)
- [ ] Guard relocation into shared helpers (§7.2)
- [ ] P0-store vs. live-fallback distinguishability (§7.3)
- [ ] Per-role/per-analyzer demand-health markers (§7.4)
- [ ] Supply-alternatives write-up (§7.5)

## 5. Roadmap outline

| # | Item | One-line summary | Status |
|---|---|---|---|
| 1 | PRC accessor contract | Unify `prcForVariant`/direct field access under one valid/`<=0` contract | Open |
| 2 | Guard relocation | Move `<=0` checks into shared helpers, not per-caller | Open |
| 3 | P0-store distinguishability | Sat-fallback and P0-store estimate currently look identical downstream | Open |
| 4 | Demand-health markers | No signal marks a per-role `D_sat[role]` as broken | Open |
| 5 | Supply alternatives | Per-analyzer per-SO supply estimate — write up the rejected alternative | Open |

## 6. Details

### 7.1 `prcForVariant`/`vc.PerReplicaCapacity` contract

Today both `prcForVariant(r, v)` and direct `vc.PerReplicaCapacity` field reads return the same
raw value with no shared contract. The user's ask: make both guarantee "value is valid" XOR
"guaranteed `<=0`" — i.e. no code path should be able to read a value that isn't already known
to be one or the other. This is distinct from unifying the two *functions*
(`prcForVariant`/`prcFromVCs`) into one — the user separately corrected that unifying the
lookup functions was wrong (they read different shapes for different callers; collapsing them
erases the strict-vs-what-if distinction). The contract fix is about the guard, not the shape.

Not done in CC — CC relies on the fact that every real call site already has its own `<=0`
check (verified during the 2026-09-15 review), so the contract gap is real but not yet causing
an actual bug outside the two `aggregation.go` sums fixed in §2.11.

### 7.2 Guard relocation

Once 7.1's contract is settled, the natural follow-on is moving the `<=0` check into
`prcForVariant`/the composite's own construction step, so callers no longer need to remember to
check it themselves. Deferred until 7.1 is resolved, since the right place to put a relocated
guard depends on what the unified contract actually says.

### 7.3 P0-store vs. live-fallback distinguishability

Full finding, from the 2026-09-15 usage audit: saturation's own P0-store zero-replica estimator
(`saturation_v2/analyzer.go:744-757`, `Reason=satReasonP0Store`) is the existing, deliberate
mechanism for partial-scale-from-zero — confirmed by `composite_decision.go`'s own doc comment
("saturation's existing P0-store ladder is the only source of estimated PRCs for an idle or
never-seen-before SO, and it flows through as an ordinary contribution"). A P0-store estimate
produces a genuinely positive PRC and reaches the composite as `DecisionSatFallback` or
`DecisionSingle` — the identical decision-path string a real, live sat measurement produces.

Verified 2026-09-15 (in response to the user's direct question): `DecisionNoSignal` itself
always implies sat's own raw `PerReplicaCapacity <= 0` (traced through `TotalReplicas`'s guard
at `composite_decision.go:47`) — so P0-store estimates never produce `DecisionNoSignal`. The
ambiguity is narrower than first framed: it's not "no-signal SOs sometimes have positive PRC,"
it's "two different *positive-PRC* cases (real live fallback vs. P0-store estimate) share one
decision-path label." The user confirmed (2026-09-15): P0-store is a legitimate value; CC
accepts sat-fallback as-is (a valid value where sat is simply not enabled) and defers any
finer-grained distinction to this follow-up.

Open question, not resolved: should `roleBottleneckReplicas`/`prcFromVCs`'s scale-up callers
gain an explicit marker distinguishing "real live PRC" from "P0-store estimate," independent of
whatever the `<=0` guard already screens out? Not needed for CC (P0-store is accepted as
legitimate); revisit only if a real problem surfaces from treating them identically.

### 7.4 Demand-health markers

From the 2026-09-15 usage-audit's new-ground check (task item 7): no current consumer
conflates a per-role-broken demand value with a per-SO fallback (verified explicitly — see the
audit doc §8). But the audit also found a real gap: nothing anywhere marks a specific
`D_sat[role]` value as itself broken (e.g. a bad PromQL filter returning a wrong-but-present
number). CC's §2.12 guard (`CompositeHasSignal()` at the model level) catches the case where the entire
composite is unusable — it does NOT catch a single role's demand being wrong while the rest of
the composite is fine. That finer-grained, per-role health marker would need to originate in
the demand-producing analyzer (saturation), which is out of this mission's consumer-side scope
as currently framed — flagged for a future mission/task, not designed here.

### 7.5 Supply-alternatives write-up

The CC fix (§2.11) computes supply as `Σ_SO ReplicaCount(SO) × PerReplicaCapacity(SO)`, unchanged
from the existing formula, just correctly guarded. An alternative was raised and deferred without
being written up: each analyzer could report its own per-SO supply estimate directly, rather than
the composite deriving supply from `ReplicaCount × PRC` alone. Write up why this was rejected (or
confirm it wasn't actually rejected, just postponed) before treating the current formula as
permanently settled — currently there is no record of the actual reasoning, only that it was not
pursued now.

## 7. Refs

- `.session/composite-signal-redesign.md` §2.11-2.13 — the CC guard fixes this doc follows up on.
- `.session/composite-signal-full-usage-audit-2026-09-15.md` — the full per-consumer audit
  (supply/demand/RC-SC tables) this doc's findings are drawn from.
- `.session/findings-composite-prc-downstream-2026-09-15.md` — the original PRC-only downstream
  map (superseded in scope by the full audit above, kept for its call-chain citations).
- `.session/recommendations-composite-prc-guarding-2026-09-15.md` — the mission owner's first
  (partially corrected) recommendation pass; superseded by the corrected findings in this doc
  and in redesign doc §2.11-2.13.
