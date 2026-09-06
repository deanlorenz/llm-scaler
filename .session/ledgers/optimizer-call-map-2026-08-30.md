# Optimizer call map — single-analyzer refactor (2026-08-30)

Shows every changed call site in the optimizer layer.
Base: `c6e408c4`. Current tip: `fcf9c905`.

---

## Contract boundary: `ModelScalingRequest`

```
BEFORE:  AnalyzerResults []NamedAnalyzerResult   (slice — one entry per analyzer)
AFTER:   CompositeSignal  NamedAnalyzerResult    (single value — the one composite entry)
```

Every optimizer-side reader was updated from `req.AnalyzerResults[0]` (or a loop over the
slice) to `req.CompositeSignal`.

---

## `RolePairedState` — type changed

```
BEFORE:  type RolePairedState []map[string]float64   // [analyzer-index][role] → demand
AFTER:   type RolePairedState map[string]float64     // [role] → demand
```

The analyzer-index dimension is gone. All downstream uses of `ps[i][role]` become `ps[role]`.

---

## `analyzer_helpers.go` — helper signatures

All 7 helpers changed from `s []NamedAnalyzerResult` to single-entry form.

### `initRoleState`
```
BEFORE:  func initRoleState(s []NamedAnalyzerResult) ([]string, RolePairedState)
AFTER:   func initRoleState(e *NamedAnalyzerResult) ([]string, RolePairedState)
```
Mutates `e.RoleSpare` in place. Returns `map[string]float64` instead of `[]map[string]float64`.

### `applyAllocation`
```
BEFORE:  func applyAllocation(s []NamedAnalyzerResult, v string, n int)
AFTER:   func applyAllocation(e *NamedAnalyzerResult, v string, n int)
```
Decrements `e.Remaining` directly. No loop.

### `roleBottleneckReplicas`
```
BEFORE:  func roleBottleneckReplicas(s []NamedAnalyzerResult, state RolePairedState, role, v string) int
AFTER:   func roleBottleneckReplicas(e NamedAnalyzerResult, state RolePairedState, role, v string) int
```
Returns `ceil(state[role] / PRC[v])` directly. No max-over-analyzers loop.

### `roleAggRemaining`
```
BEFORE:  func roleAggRemaining(s []NamedAnalyzerResult, state RolePairedState, role string) float64
AFTER:   func roleAggRemaining(state RolePairedState, role string) float64
```
Returns `state[role]` directly. Entry not needed; parameter dropped.

### `safeRemovalReplicasForRole`
```
BEFORE:  func safeRemovalReplicasForRole(s []NamedAnalyzerResult, v, role string) int
AFTER:   func safeRemovalReplicasForRole(e NamedAnalyzerResult, v, role string) int
```
Returns `floor(e.RoleSpare[role] / PRC[v])` if live. No min-over-analyzers loop.

### `applyDeallocationForRole`
```
BEFORE:  func applyDeallocationForRole(s []NamedAnalyzerResult, v, role string, n int)
AFTER:   func applyDeallocationForRole(e *NamedAnalyzerResult, v, role string, n int)
```
Decrements `e.RoleSpare[role]` directly. No loop.

### `needsScaleDownForRole`
```
BEFORE:  func needsScaleDownForRole(s []NamedAnalyzerResult, role string) bool
AFTER:   func needsScaleDownForRole(e NamedAnalyzerResult, role string) bool
```
Returns `e.Live && e.RoleSpare[role] > 0`. No all-agree loop.

### `anyRoleNeedsScaleUp` — signature unchanged, body changed
```
BEFORE:  for _, role := range roles { for _, m := range state { if m[role] > 0 ... } }
AFTER:   for _, role := range roles { if state[role] > 0 ... }
```

---

## `analyzer_helpers.go` — other functions

### `RolePickFn` — parameter dropped
```
BEFORE:  type RolePickFn func(role string, s []NamedAnalyzerResult, variants []variantRecord, ...)
AFTER:   type RolePickFn func(role string, variants []variantRecord, ...)
```

### `allocateForModelPaired` — parameter changed
```
BEFORE:  func allocateForModelPaired(ctx, s []NamedAnalyzerResult, variants, ...)
AFTER:   func allocateForModelPaired(ctx, e *NamedAnalyzerResult, variants, ...)
```
Passes `e` to `roleBottleneckReplicas`, `roleAggRemaining`, `applyAllocation`.
`pickerState[role]` decremented directly (no `for i := range pickerState` loop).

### `scaleDownRoleIterated` (in `cost_aware_optimizer.go`) — parameter changed
```
BEFORE:  func scaleDownRoleIterated(ctx, s []NamedAnalyzerResult, variants, ...)
AFTER:   func scaleDownRoleIterated(ctx, e *NamedAnalyzerResult, variants, ...)
```
Passes `*e` to `needsScaleDownForRole`, `sortVariantsForScaleDown`, `safeRemovalReplicasForRole`.
Passes `e` (pointer) to `applyDeallocationForRole`.

---

## `cost_aware_optimizer.go` call sites

### `CostAwareOptimizer.Optimize` — inner loop
```
BEFORE:
    s := []NamedAnalyzerResult{req.CompositeSignal}   // was req.AnalyzerResults
    roles, ps := initRoleState(s)
    allocateForModelPaired(ctx, s, ...)
    scaleDownRoleIterated(ctx, s, ...)

AFTER:
    e := req.CompositeSignal
    roles, ps := initRoleState(&e)
    allocateForModelPaired(ctx, &e, ...)
    scaleDownRoleIterated(ctx, &e, ...)
```

### `costGreedyRolePick` — parameter dropped
```
BEFORE:  func costGreedyRolePick(role string, _ []NamedAnalyzerResult, variants, ...)
AFTER:   func costGreedyRolePick(role string, variants, ...)
```

### `sortVariantsForScaleDown` — parameter changed
```
BEFORE:  func sortVariantsForScaleDown(s []NamedAnalyzerResult, roleVCs []variantRecord) []variantRecord
AFTER:   func sortVariantsForScaleDown(e NamedAnalyzerResult, roleVCs []variantRecord) []variantRecord
```
`weighted(name)` closure: was `Σ_i e.Score × PRC_i[v]`; now `e.Score × PRC[v]` directly.

---

## `greedy_score_optimizer.go` call sites

### `modelWork` struct — field renamed
```
BEFORE:  s []NamedAnalyzerResult
AFTER:   e *NamedAnalyzerResult
```

### `GreedyByScoreOptimizer.Optimize` — scale-up path
```
BEFORE:
    s := []NamedAnalyzerResult{req.CompositeSignal}
    roles, ps := initRoleState(s)
    fsv := fairShareValue(req.Priority, s, ps, roles)
    w := o.buildScaleUpWork(req, records, s, ps, roles, fsv)

AFTER:
    e := req.CompositeSignal
    roles, ps := initRoleState(&e)
    fsv := fairShareValue(req.Priority, e, ps, roles)
    w := o.buildScaleUpWork(req, records, &e, ps, roles, fsv)
```

### `GreedyByScoreOptimizer.Optimize` — scale-down path
```
BEFORE:
    s := []NamedAnalyzerResult{req.CompositeSignal}
    _, _ = initRoleState(s)
    scaleDownRoleIterated(ctx, s, ...)

AFTER:
    e := req.CompositeSignal
    _, _ = initRoleState(&e)
    scaleDownRoleIterated(ctx, &e, ...)
```

### `buildScaleUpWork` — parameter changed
```
BEFORE:  func (...) buildScaleUpWork(req, records, s []NamedAnalyzerResult, ps, roles, fsv) *modelWork
AFTER:   func (...) buildScaleUpWork(req, records, e *NamedAnalyzerResult, ps, roles, fsv) *modelWork
```

### `allocate` (fair-share inner loop) — changed
```
BEFORE:
    _, ps := initRoleState(w.s)
    for i := range ps { for _, role := range w.roles { if ps[i][role] > target ... } }
    pick := fairShareRolePick(target, w.s, w.roles, w.limited)
    allocateForModelPaired(ctx, w.s, ...)
    _, freshPs := initRoleState(w.s)
    w.remaining = fairShareValue(w.req.Priority, w.s, freshPs, w.roles)

AFTER:
    _, ps := initRoleState(w.e)
    for _, role := range w.roles { if ps[role] > target ... }
    pick := fairShareRolePick(target, w.roles, w.limited)
    allocateForModelPaired(ctx, w.e, ...)
    _, freshPs := initRoleState(w.e)
    w.remaining = fairShareValue(w.req.Priority, *w.e, freshPs, w.roles)
```

### `fairShareValue` — signature changed
```
BEFORE:  func fairShareValue(priority float64, s []NamedAnalyzerResult, ps RolePairedState, roles []string) float64
AFTER:   func fairShareValue(priority float64, e NamedAnalyzerResult, ps RolePairedState, roles []string) float64
```
Body: `Σ_i Score_i × Σ_role ps[i][role]` → `e.Score × Σ_role ps[role]` directly.

### `fairShareRolePick` — parameter dropped
```
BEFORE:  func fairShareRolePick(target float64, s []NamedAnalyzerResult, roles []string, limited ...) RolePickFn
AFTER:   func fairShareRolePick(target float64, roles []string, limited ...) RolePickFn
```

---

## `rescale.go` call sites

### `rescaleModelDecisions` — two reads changed
```
BEFORE:  satNamed := req.AnalyzerResults[0]
AFTER:   satNamed := req.CompositeSignal

BEFORE:  reclaimRole(ctx, []NamedAnalyzerResult{req.AnalyzerResults[0]}, ...)
AFTER:   reclaimRole(ctx, req.CompositeSignal, ...)
```

### `buildDecisionsWithOptimizer` — one read changed
```
BEFORE:  satNamed := req.AnalyzerResults[0]
AFTER:   satNamed := req.CompositeSignal
```

### `reclaimRole` — parameter changed
```
BEFORE:  func reclaimRole(ctx, s []NamedAnalyzerResult, variants, role, ...)
AFTER:   func reclaimRole(ctx, e NamedAnalyzerResult, variants, role, ...)
```
Passes `e` directly to `sortVariantsForScaleDown(e, ...)`.

---

## `variant_records.go` call site

### `recordsForRequest`
```
BEFORE:  nr := req.AnalyzerResults[0]
AFTER:   nr := req.CompositeSignal
```

---

## Multi-entry originals (preserved, not in PR)

Saved under `internal/engines/allocation/multi_backup/` with `//go:build ignore`:
- `analyzer_helpers_multi.go` — original slice-based helper implementations
- `analyzer_helpers_multi_test.go` — the 5 multi-entry test cases (to be restored engine-side)
- `cost_aware_optimizer_multi.go` — original `sortVariantsForScaleDown` with slice loop
