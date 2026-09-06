# Optimizer call stack — single-analyzer refactor (2026-08-30)

Base: verified-call-map-2026-08-25.md (HEAD 506ae369).
Changes from commits e4106109 [CT2], b980f682 [CT3b], fcf9c905 [CT5]
on branch `single-analyzer` (current tip fcf9c905, base c6e408c4 after rebase).

**Optimizer references to SaturationAnalyzerName / name-based checks:** none.
Grep across all non-test, non-backup production optimizer files finds zero hits for
`SaturationAnalyzerName`, `AnalyzerResults`, `.Name ==`. The optimizer is name-blind.

---

## Contract boundary  [CT2]

```diff
 type ModelScalingRequest struct {
-    AnalyzerResults []NamedAnalyzerResult   // one entry per analyzer that ran
+    CompositeSignal  NamedAnalyzerResult    // single composed entry from the engine
     ...
 }
```

Every reader that previously did `req.AnalyzerResults[0]` now does `req.CompositeSignal`.

---

## RolePairedState type  [CT3b]

```diff
-type RolePairedState []map[string]float64   // indexed [analyzerIndex][role] → demand
+type RolePairedState map[string]float64     // indexed [role] → demand
```

All uses of `ps[i][role]` become `ps[role]`. The analyzer-index dimension is gone.

---

## CostAwareOptimizer.Optimize  (cost_aware_optimizer.go:35)

```diff
 CostAwareOptimizer.Optimize(ctx, requests, constraints)
  └─ for each req in requests:
      ├─ recordsForRequest(req)
      │
-     ├─ s := []NamedAnalyzerResult{req.AnalyzerResults[0]}               [CT2]
+     ├─ e := req.CompositeSignal
      │
-     ├─ roles, ps := initRoleState(s)                                   [CT3b]
+     ├─ roles, ps := initRoleState(&e)
      │    (see initRoleState below)
      │
      ├─ if anyRoleNeedsScaleUp(ps, roles):
-     │    allocateForModelPaired(ctx, s, ...)                           [CT3b]
+     │    allocateForModelPaired(ctx, &e, ...)
      │    (see allocateForModelPaired below)
      │
      └─ else:
-          scaleDownRoleIterated(ctx, s, ...)                            [CT3b]
+          scaleDownRoleIterated(ctx, &e, ...)
           (see scaleDownRoleIterated below)
```

---

## GreedyByScoreOptimizer.Optimize  (greedy_score_optimizer.go:96)

```diff
 GreedyByScoreOptimizer.Optimize(ctx, requests, constraints)
  └─ classification pass — for each req:
      ├─ recordsForRequest(req)
      │
-     ├─ s := []NamedAnalyzerResult{req.AnalyzerResults[0]}               [CT2]
+     ├─ e := req.CompositeSignal
      │
-     ├─ roles, ps := initRoleState(s)                                   [CT3b]
+     ├─ roles, ps := initRoleState(&e)
      │
-     ├─ fsv := fairShareValue(req.Priority, s, ps, roles)               [CT3b]
+     ├─ fsv := fairShareValue(req.Priority, e, ps, roles)
      │    (see fairShareValue below)
      │
      ├─ if anyRoleNeedsScaleUp || fsv > 0:
-     │    buildScaleUpWork(req, records, s, ps, roles, fsv)             [CT3b]
-     │    modelWork{ s: s, ... }
+     │    buildScaleUpWork(req, records, &e, ps, roles, fsv)
+     │    modelWork{ e: &e, ... }
      │
      └─ else (scale-down):
-          s := []NamedAnalyzerResult{req.AnalyzerResults[0]}            [CT2]
+          e := req.CompositeSignal
-          initRoleState(s)                                               [CT3b]
+          initRoleState(&e)
-          scaleDownRoleIterated(ctx, s, ...)
+          scaleDownRoleIterated(ctx, &e, ...)

  └─ fairShareScaleUp(ctx, scaleUpWork, ...)
      └─ for each active modelWork w:
          └─ allocate(w, mean, available, availableByNS)
-             ├─ _, ps := initRoleState(w.s)                             [CT3b]
+             ├─ _, ps := initRoleState(w.e)
              │
-             ├─ for i := range ps {                                     [CT3b]
-             │    for _, role := range w.roles {
-             │        if ps[i][role] > target { ps[i][role] = target }
-             │    }
-             │  }
+             ├─ for _, role := range w.roles {
+             │    if ps[role] > target { ps[role] = target }
+             │  }
              │
-             ├─ pick := fairShareRolePick(target, w.s, w.roles, w.limited)  [CT3b]
+             ├─ pick := fairShareRolePick(target, w.roles, w.limited)
              │
-             ├─ allocateForModelPaired(ctx, w.s, ...)                   [CT3b]
+             ├─ allocateForModelPaired(ctx, w.e, ...)
              │
              └─ if roles == ["both"]:
-                  _, freshPs := initRoleState(w.s)                      [CT3b]
-                  w.remaining = fairShareValue(w.req.Priority, w.s, freshPs, w.roles)
+                  _, freshPs := initRoleState(w.e)
+                  w.remaining = fairShareValue(w.req.Priority, *w.e, freshPs, w.roles)
                 else:
-                  w.remaining = fairShareValue(w.req.Priority, w.s, ps, w.roles)
+                  w.remaining = fairShareValue(w.req.Priority, *w.e, ps, w.roles)
```

---

## Helper: initRoleState  [CT3b] [CT5]

```diff
-func initRoleState(s []NamedAnalyzerResult) (roles []string, pickerState RolePairedState) {
-    pickerState = make(RolePairedState, len(s))
-    roleSet := make(map[string]struct{})
-    for i, e := range s {
-        pickerState[i] = make(map[string]float64)
-        if e.Result == nil { continue }
-        if e.RoleCapacities != nil {
-            if s[i].RoleSpare == nil { s[i].RoleSpare = make(map[string]float64, len(e.RoleCapacities)) }
-            for role, rc := range e.RoleCapacities {
-                pickerState[i][role] = rc.RequiredCapacity
-                s[i].RoleSpare[role] = rc.SpareCapacity
-                roleSet[role] = struct{}{}
-            }
-        } else {
-            pickerState[i][domain.RoleBoth] = e.Remaining
-            if s[i].RoleSpare == nil { s[i].RoleSpare = make(map[string]float64, 1) }
-            s[i].RoleSpare[domain.RoleBoth] = e.Spare
-            roleSet[domain.RoleBoth] = struct{}{}
-        }
-    }
+func initRoleState(e *NamedAnalyzerResult) (roles []string, pickerState RolePairedState) {
+    pickerState = make(RolePairedState)
+    roleSet := make(map[string]struct{})
+    if e.Result == nil { return nil, pickerState }
+    if e.RoleCapacities != nil {
+        if e.RoleSpare == nil { e.RoleSpare = make(map[string]float64, len(e.RoleCapacities)) }
+        for role, rc := range e.RoleCapacities {
+            pickerState[role] = rc.RequiredCapacity
+            e.RoleSpare[role] = rc.SpareCapacity
+            roleSet[role] = struct{}{}
+        }
+    } else {
+        pickerState[domain.RoleBoth] = e.Remaining
+        if e.RoleSpare == nil { e.RoleSpare = make(map[string]float64, 1) }
+        e.RoleSpare[domain.RoleBoth] = e.Spare
+        roleSet[domain.RoleBoth] = struct{}{}
+    }
     roles = make([]string, 0, len(roleSet))
     for role := range roleSet { roles = append(roles, role) }
     sort.Strings(roles)
     return roles, pickerState
 }
```

**Equivalence with one entry:** the old loop ran once (`i=0`), writing `pickerState[0][role]`.
The new code writes `pickerState[role]` directly. Same values, different index shape.

[CT5] adds the role-visibility contract doc comment (no behaviour change).

---

## Helper: needsScaleDownForRole  [CT3b]

```diff
-func needsScaleDownForRole(s []NamedAnalyzerResult, role string) bool {
-    liveCount := 0
-    for _, e := range s {
-        if !e.Live { continue }
-        if e.Result == nil || e.RoleSpare == nil || e.RoleSpare[role] <= 0 { return false }
-        liveCount++
-    }
-    return liveCount > 0
-}
+func needsScaleDownForRole(e NamedAnalyzerResult, role string) bool {
+    if !e.Live { return false }
+    if e.Result == nil || e.RoleSpare == nil || e.RoleSpare[role] <= 0 { return false }
+    return true
+}
```

**Equivalence with one entry:**

| entry state | old result | new result |
|---|---|---|
| not live | skipped → `liveCount=0` → `false` | `false` |
| live, spare ≤ 0 or nil | `return false` immediately | `false` |
| live, spare > 0 | `liveCount=1` → `true` | `true` |

Multi-entry behaviour gone from optimizer: a second live entry with zero spare would have
vetoed even when the first had spare. Preserved in `multi_backup/` for engine-side reduce.

---

## Helper: safeRemovalReplicasForRole  [CT3b]

```diff
-func safeRemovalReplicasForRole(s []NamedAnalyzerResult, v, role string) int {
-    smallest := math.MaxInt; found := false
-    for _, e := range s {
-        if !e.Live { continue }
-        if e.Result == nil || e.RoleSpare == nil { continue }
-        prc := prcForVariant(e.Result, v)
-        if prc <= 0 { continue }
-        n := int(math.Floor(e.RoleSpare[role] / prc))
-        if n < smallest { smallest = n }
-        found = true
-    }
-    if !found || smallest < 0 { return 0 }
-    return smallest
-}
+func safeRemovalReplicasForRole(e NamedAnalyzerResult, v, role string) int {
+    if !e.Live { return 0 }
+    if e.Result == nil || e.RoleSpare == nil { return 0 }
+    prc := prcForVariant(e.Result, v)
+    if prc <= 0 { return 0 }
+    n := int(math.Floor(e.RoleSpare[role] / prc))
+    if n < 0 { return 0 }
+    return n
+}
```

**Equivalence with one entry:**

| entry state | old result | new result |
|---|---|---|
| not live | skipped → `found=false` → `0` | `0` |
| live, Result/RoleSpare nil | skipped → `found=false` → `0` | `0` |
| live, prc ≤ 0 | skipped → `found=false` → `0` | `0` |
| live, n ≥ 0 | `smallest=n`, `found=true` → `n` | `n` |
| live, n < 0 | `smallest<0` → `0` | `0` |

Multi-entry behaviour gone: the `min` across multiple live entries. In `multi_backup/`.

---

## Helper: applyDeallocationForRole  [CT3b]

```diff
-func applyDeallocationForRole(s []NamedAnalyzerResult, v, role string, n int) {
-    for i := range s {
-        if s[i].Result == nil || s[i].RoleSpare == nil { continue }
-        prc := prcForVariant(s[i].Result, v)
-        if prc <= 0 { continue }
-        s[i].RoleSpare[role] -= float64(n) * prc
-        if s[i].RoleSpare[role] < 0 { s[i].RoleSpare[role] = 0 }
-    }
-}
+func applyDeallocationForRole(e *NamedAnalyzerResult, v, role string, n int) {
+    if e.Result == nil || e.RoleSpare == nil { return }
+    prc := prcForVariant(e.Result, v)
+    if prc <= 0 { return }
+    e.RoleSpare[role] -= float64(n) * prc
+    if e.RoleSpare[role] < 0 { e.RoleSpare[role] = 0 }
+}
```

**Equivalence with one entry:** old loop ran once on `s[0]`. New code operates on `e`
directly. Identical arithmetic. Note: old loop was intentionally not live-gated (non-live
entries' RoleSpare is never read back, so mutating it is harmless); new code has no liveness
check for the same reason.

---

## Helper: applyAllocation  [CT3b]

```diff
-func applyAllocation(s []NamedAnalyzerResult, v string, n int) {
-    for i := range s {
-        if s[i].Result == nil { continue }
-        prc := prcForVariant(s[i].Result, v)
-        if prc <= 0 { continue }
-        s[i].Remaining -= float64(n) * prc
-        if s[i].Remaining < 0 { s[i].Remaining = 0 }
-    }
-}
+func applyAllocation(e *NamedAnalyzerResult, v string, n int) {
+    if e.Result == nil { return }
+    prc := prcForVariant(e.Result, v)
+    if prc <= 0 { return }
+    e.Remaining -= float64(n) * prc
+    if e.Remaining < 0 { e.Remaining = 0 }
+}
```

**Equivalence with one entry:** old loop ran once on `s[0]`. Identical arithmetic.

---

## Helper: roleBottleneckReplicas  [CT3b]

```diff
-func roleBottleneckReplicas(s []NamedAnalyzerResult, state RolePairedState, role, v string) int {
-    max := 0
-    for i, e := range s {
-        if e.Result == nil { continue }
-        prc := prcForVariant(e.Result, v)
-        if prc <= 0 { continue }
-        n := int(math.Ceil(state[i][role] / prc))
-        if n > max { max = n }
-    }
-    return max
-}
+func roleBottleneckReplicas(e NamedAnalyzerResult, state RolePairedState, role, v string) int {
+    if e.Result == nil { return 0 }
+    prc := prcForVariant(e.Result, v)
+    if prc <= 0 { return 0 }
+    return int(math.Ceil(state[role] / prc))
+}
```

**Equivalence with one entry:** old computed `max_i ceil(state[i][role] / PRC_i[v])`.
With one entry `i=0`, `max` over one term equals that term. `state[0][role]` → `state[role]`.
Multi-entry max preserved in `multi_backup/`.

---

## Helper: roleAggRemaining  [CT3b]

```diff
-func roleAggRemaining(s []NamedAnalyzerResult, state RolePairedState, role string) float64 {
-    max := 0.0
-    for i := range s {
-        if d := state[i][role]; d > max { max = d }
-    }
-    return max
-}
+func roleAggRemaining(state RolePairedState, role string) float64 {
+    return state[role]
+}
```

**Equivalence with one entry:** old computed `max_i state[i][role]`. With one entry the max
equals `state[0][role]` → `state[role]`. The entry parameter was only used to size the loop
and is dropped entirely. Multi-entry max preserved in `multi_backup/`.

---

## Helper: sortVariantsForScaleDown  [CT3b]

```diff
-func sortVariantsForScaleDown(s []NamedAnalyzerResult, roleVCs []variantRecord) []variantRecord {
-    weighted := func(name string) float64 {
-        sum := 0.0
-        for _, e := range s {
-            if e.Result == nil { continue }
-            sum += e.Score * prcForVariant(e.Result, name)
-        }
-        return sum
-    }
+func sortVariantsForScaleDown(e NamedAnalyzerResult, roleVCs []variantRecord) []variantRecord {
+    weighted := func(name string) float64 {
+        if e.Result == nil { return 0 }
+        return e.Score * prcForVariant(e.Result, name)
+    }
     out := append([]variantRecord(nil), roleVCs...)
     sort.Slice(out, func(i, j int) bool {
         if out[i].Cost != out[j].Cost { return out[i].Cost > out[j].Cost }
         wi, wj := weighted(out[i].VariantName), weighted(out[j].VariantName)
         if wi != wj { return wi < wj }
         return out[i].VariantName < out[j].VariantName
     })
     return out
 }
```

**Equivalence with one entry:** old `weighted` computed `Σ_i Score_i × PRC_i[name]`.
With one entry the sum has one term. Multi-entry sum preserved in
`multi_backup/cost_aware_optimizer_multi.go`.

---

## Helper: fairShareValue  [CT3b]

```diff
-func fairShareValue(priority float64, s []NamedAnalyzerResult, ps RolePairedState, roles []string) float64 {
-    weighted := 0.0
-    for i, e := range s {
-        if e.Result == nil { continue }
-        roleSum := 0.0
-        for _, role := range roles {
-            if i < len(ps) { roleSum += ps[i][role] }
-        }
-        weighted += roleSum * e.Score
-    }
-    if fsv := priority * weighted; fsv > 0 { return fsv }
-    maxDemand := 0.0
-    for i, e := range s {
-        if e.Result == nil { continue }
-        if i < len(ps) {
-            for _, role := range roles {
-                if ps[i][role] > maxDemand { maxDemand = ps[i][role] }
-            }
-        }
-    }
-    return maxDemand
-}
+func fairShareValue(priority float64, e NamedAnalyzerResult, ps RolePairedState, roles []string) float64 {
+    if e.Result != nil {
+        roleSum := 0.0
+        for _, role := range roles { roleSum += ps[role] }
+        if fsv := priority * roleSum * e.Score; fsv > 0 { return fsv }
+    }
+    maxDemand := 0.0
+    for _, role := range roles {
+        if d := ps[role]; d > maxDemand { maxDemand = d }
+    }
+    return maxDemand
+}
```

**Equivalence with one entry:**
- Primary path: old `Σ_i Score_i × Σ_role ps[i][role]` with one entry = `e.Score × Σ_role ps[role]`.
- Fallback path: old `max_i max_role ps[i][role]` with one entry = `max_role ps[role]`.

---

## allocateForModelPaired inner loop  [CT3b]

```diff
-allocateForModelPaired(ctx, s []NamedAnalyzerResult, ..., pick RolePickFn, pickerState, roles)
+allocateForModelPaired(ctx, e *NamedAnalyzerResult, ..., pick RolePickFn, pickerState, roles)

  for anyRoleNeedsScaleUp(pickerState, roles):
      for each role:
-         v, capN := pick(role, s, variants, stateMap, available, targets)
+         v, capN := pick(role, variants, stateMap, available, targets)

-         n := min(roleBottleneckReplicas(s, pickerState, role, v), capByRole[role])
+         n := min(roleBottleneckReplicas(*e, pickerState, role, v), capByRole[role])

-         demand := roleAggRemaining(s, pickerState, role)
+         demand := roleAggRemaining(pickerState, role)

      for each role:
-         for i := range pickerState { pickerState[i][role] -= k × prc }
+         pickerState[role] -= k × prc

-         applyAllocation(s, v, k)
+         applyAllocation(e, v, k)
```

---

## scaleDownRoleIterated  [CT3b]

```diff
-func scaleDownRoleIterated(ctx, s []NamedAnalyzerResult, variants, targets, stateMap...)
+func scaleDownRoleIterated(ctx, e *NamedAnalyzerResult, variants, targets, stateMap...)

  for each role in rolesOf(variants):
-     if !needsScaleDownForRole(s, role): continue
+     if !needsScaleDownForRole(*e, role): continue

-     sorted := sortVariantsForScaleDown(s, roleVCs)
+     sorted := sortVariantsForScaleDown(*e, roleVCs)

      scaleDownVariantSet(ctx, sorted, targets, states,
-         safeRemoval:  safeRemovalReplicasForRole(s, vc.VariantName, role),
+         safeRemoval:  safeRemovalReplicasForRole(*e, vc.VariantName, role),
-         applyDealloc: applyDeallocationForRole(s, vc.VariantName, role, n),
+         applyDealloc: applyDeallocationForRole(e, vc.VariantName, role, n),
      )
```

---

## rescale.go reads  [CT2]

```diff
-satNamed := req.AnalyzerResults[0]     // rescaleModelDecisions, line 344
+satNamed := req.CompositeSignal

-reclaimRole(ctx, []NamedAnalyzerResult{req.AnalyzerResults[0]}, ...)   // line 372
+reclaimRole(ctx, req.CompositeSignal, ...)

-satNamed := req.AnalyzerResults[0]     // buildDecisionsWithOptimizer, line 528
+satNamed := req.CompositeSignal
```

```diff
-func reclaimRole(ctx, s []NamedAnalyzerResult, variants, role, ...)
+func reclaimRole(ctx, e NamedAnalyzerResult, variants, role, ...)
-    sorted := sortVariantsForScaleDown(s, variantsForRole(variants, role))
+    sorted := sortVariantsForScaleDown(e, variantsForRole(variants, role))
```

---

## Multi-entry originals (preserved, not in PR)

`internal/engines/allocation/multi_backup/` (`//go:build ignore`):
- `analyzer_helpers_multi.go` — slice-based originals of every helper above
- `analyzer_helpers_multi_test.go` — 5 multi-entry test cases to restore engine-side
- `cost_aware_optimizer_multi.go` — original `sortVariantsForScaleDown` with `Σ_i` loop
