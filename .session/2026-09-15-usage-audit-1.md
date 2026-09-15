# Ledger — 2026-09-15-usage-audit-1

Task: full CompositeSignal usage audit (all consumer categories) against 7 corrected criteria.
Task file: `.session/task-composite-signal-usage-audit-2026-09-15.md`.
In: `composite-analyzer.usage-audit.in` / Out: `composite-analyzer.usage-audit.out`.

## Startup

- Verified branch = `composite-analyzer`.
- Confirmed both required docs exist and read in full:
  - `.session/findings-composite-prc-downstream-2026-09-15.md`
  - `.session/recommendations-composite-prc-guarding-2026-09-15.md`
- Read `worktrees/session-tracking/CONVENTIONS.md` and
  `worktrees/session-tracking/conventions/agentbus.md` for reporting protocol.
- Treating prior docs' file:line citations as re-verifiable leads only, not conclusions.
  Recommendations doc's unify-prcForVariant/prcFromVCs verdict is explicitly overridden by
  correction 7 — will not reuse that conclusion.

## Completion

Full audit written to `.session/composite-signal-full-usage-audit-2026-09-15.md`. Key results:
- `buildCapacities` confirmed as composite-construction (called at `composite.go:226`, inside
  `buildComposite`), NOT downstream — correction 4 verified true.
- New finding: sat's own P0-store zero-replica estimator (`saturation_v2/analyzer.go:744-757`)
  is the real, already-existing mechanism behind "partial-scale-from-zero" — upstream of the
  composite. `composite_decision.go`'s own doc comment confirms this by design. This means the
  composite's `DecisionSatFallback`/`Reason` currently cannot distinguish "real live sat
  fallback" from "deliberate zero-replica what-if estimate" — both produce the same Reason
  string. Surfaced as a concrete ambiguity, not resolved unilaterally.
- `prcForVariant`/`prcFromVCs` NOT unified (per correction 7) — full per-caller strict-vs-what-if
  table written: safeRemovalReplicasForRole/applyDeallocationForRole = strict;
  roleBottleneckReplicas/prcFromVCs's allocateForModelPaired caller = what-if-permissive (but
  flagged as conditionally safe only once the composite-level fix lands);
  sortVariantsForScaleDown = neither (ranking weight); applyAllocation = inherits caller.
- Demand consumers (task item 7, new ground) checked for per-role-broken vs per-SO-fallback
  conflation: none found, but flagged a producer-side gap — no per-role demand-health marker
  exists anywhere, so a broken D_sat[role] (bad PromQL) is indistinguishable from a correct one
  by any consumer.
- Two open judgment calls recorded in §9, not resolved unilaterally, not blocking (not
  criterion-vs-code conflicts, just design gaps) — no kind="question" needed.
- Reported at all 3 phase boundaries (startup, inventory-done, completion) via agentbus topic +
  SendMessage + user.in, all three channels every time.

Task complete. Terminating per task's "Done / completion criteria" and "terminate after
publishing the final report" instruction.

## Plan

1. Inventory: re-verify every call site from findings doc against current code; find any missed
   ones (RC/SC and demand-only sites may not be fully covered by the PRC-focused table).
2. Classify each site by category (supply/demand/RC-SC/identity) and strict-vs-what-if intent.
3. Special deep-dive: prcForVariant/prcFromVCs per-caller (criterion 7).
4. Verify buildCapacities placement (criterion 4/construction-vs-downstream).
5. Check demand consumers for per-role-broken vs per-SO-fallback conflation (criterion 3).
6. Write full audit to `.session/composite-signal-full-usage-audit-2026-09-15.md`.
7. Report at each phase boundary via all 3 channels.
