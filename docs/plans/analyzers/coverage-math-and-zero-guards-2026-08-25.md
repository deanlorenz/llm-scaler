# Coverage math and zero-guard verification (2026-08-25)

Verifies the user's domain framing (demand/PRC/coverage, the `min(p,d)+both` formula, and 0/0
guards) against actual current code.

## 1. Is PRC genuinely (variant, role, model)-scoped?

**Confirmed, yes.** `domain.VariantCapacity` (`internal/domain/analyzer.go:144-175`) has both
`Role string` and `PerReplicaCapacity float64` on the same struct, and every `VariantCapacity`
slice lives inside one model's `AnalyzerResult`. So PRC is genuinely `(variant, role, model)`-
scoped today, exactly as described. `AnalyzerResult.TotalDemand`/`RoleDemand` are the real demand
fields — `RoleDemand map[string]float64` is model-level-per-role, not per-variant.

## 2. Does "coverage" (replicas×PRC/Demand) exist today?

**Not under that name, but the exact ratio exists** inside `allocateForModelPaired`
(`analyzer_helpers.go:337-434`), called `utilByRole`:

```go
// analyzer_helpers.go:370-380
prc := prcByRole[role]
n := min(roleBottleneckReplicas(s, pickerState, role, variantByRole[role]), capByRole[role])
demand := roleAggRemaining(s, pickerState, role)
if demand <= 0 {
    utilByRole[role] = 1.0
} else {
    utilByRole[role] = float64(n) * prc / demand
}
```

This IS the user's `coverage = replicas*PRC/Demand` formula, already implemented, just named
`utilByRole` rather than "coverage." A separate, unrelated `Utilization` field exists elsewhere
(`VariantCapacity.Utilization`, `NamedAnalyzerResult.Utilization`) computing the reciprocal
direction (demand/supply) — do not conflate the two; `utilByRole` here is the one that matches
the user's formula.

## 3. Does the `min` across roles already exist?

**Yes — `deltaUtil`:**

```go
// analyzer_helpers.go:382-390
deltaUtil := math.MaxFloat64
for _, role := range roles {
    if utilByRole[role] < deltaUtil {
        deltaUtil = utilByRole[role]
    }
}
if deltaUtil <= 0 {
    break
}
```

This is exactly `min(coverage(role))` across whichever roles are present this iteration — the
min-half of the user's `min(coverage(p), coverage(d)) + coverage(both)` formula already exists.

## 4. Does the additive "+ coverage(both)" term exist, or can P/D/both coexist at all?

**No — and structurally cannot, today.** `initRoleState` (`analyzer_helpers.go:131-167`) makes
`roles` either `⊆ {prefill, decode}` (when an entry's `RoleCapacities != nil` — disaggregated) OR
exactly `[both]` (when `RoleCapacities == nil` — non-disaggregated) — **mutually exclusive per
entry**, never both at once:

```go
// analyzer_helpers.go:140-158
if e.RoleCapacities != nil {
    // Disaggregated: per-role RC/SC from engine-calibrated RoleCapacities.
    for role, rc := range e.RoleCapacities { pickerState[i][role] = rc.RequiredCapacity; ...; roleSet[role] = struct{}{} }
} else {
    // Non-disaggregated: synthesize a single "both" role from model-level scalars.
    pickerState[i][domain.RoleBoth] = e.Remaining; ...; roleSet[domain.RoleBoth] = struct{}{}
}
```

**This is a real, structural mismatch with the user's proposed formula**, not a missing feature
that's trivial to bolt on: the current type can't represent "prefill AND decode AND both,
simultaneously, for the same model" — a model is either fully disaggregated or fully not, by
construction of this one `if/else`. `min(p,d) + both` as literally stated requires a data shape
this code doesn't produce. (It's conceivable a real deployment has some variants with role="both"
and others with real P/D roles simultaneously — if so, `RoleCapacities` would need to carry a
`"both"` key alongside `"prefill"`/`"decode"` keys, which nothing currently prevents at the type
level, but `initRoleState`'s `if/else` never produces that shape from a single analyzer entry.
This needs your input on whether that's a real deployment shape to support or not — flagging
rather than assuming.)

## 5. Zero-guard audit — is there a live 0/0 or unguarded division risk?

**Audited every division site in `internal/engines/allocation/`. All are guarded — but not the
way the user wants.**

- `roleBottleneckReplicas`, `safeRemovalReplicasForRole`, `applyDeallocationForRole`,
  `costEfficiency`: all check `prc <= 0` before dividing by it — skip/default, never divide.
- `utilByRole` (Section 2 above): checks `demand <= 0` — but **sets `util = 1.0` (fully covered)
  when demand is zero**, rather than excluding the role from allocation entirely. **This is the
  opposite of what the user wants**: "demand=0 should not try to allocate for the model" (i.e.
  demand=0 should mean "skip this role," not "treat as 100% covered"). Today's code treats
  zero demand as trivially satisfied (`util=1.0`), which happens to produce the same practical
  outcome for that iteration (no allocation attempted, since there's nothing to fill) but is a
  different semantic — "already fully covered" vs. "there is nothing to cover." The distinction
  matters if this value is ever compared/sorted against other roles' real coverage numbers rather
  than just gating a `break`.
- No genuine `0/0` (`NaN`) was found reachable — the PRC guard (`prc <= 0 → skip`) fires before
  demand's value is even considered at every division site, so demand=0 & PRC=0 simultaneously
  never reaches a division; PRC=0 alone always short-circuits first.

**Conclusion for the guard the user asked about:** no live 0/0 crash risk exists today. The real
gap is semantic, not a division hazard: zero demand is currently encoded as "fully covered"
rather than "not applicable / should not attempt allocation," which is the distinction CT5's
design needs to get right if compose's output is meant to carry a coverage-shaped composite
value going forward.

## Summary — what's new vs. what already exists

| User's concept | Status |
|---|---|
| PRC is (variant, role, model)-scoped | **Confirmed, already true** |
| `coverage = replicas*PRC/Demand` | **Already exists**, named `utilByRole` |
| `min(coverage(p), coverage(d))` | **Already exists**, named `deltaUtil` |
| `+ coverage(both)` additive term | **Does not exist — structurally impossible today** (roles are P/D-exclusive-of-both per entry) |
| demand=0 → skip allocation (not "100% covered") | **Does not exist** — current code treats demand=0 as `util=1.0`, a different semantic that happens to produce a similar practical outcome today |
| Compose emits `PRC=coverage, Demand=100%` | **No precedent anywhere** — architecture's stated invariant is the opposite: keep D and P separate, never alias one into the other |
| 0/0 NaN risk | **Not present** — PRC guard always fires first |
