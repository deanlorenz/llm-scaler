# Optimizer call map: every reader of NamedAnalyzerResult / AnalyzerResults (2026-08-25)

Scope: internal/engines/allocation/ (both optimizers, analyzer_helpers.go, optimizer_interfaces.go,
variant_records.go, rescale.go), plus the blast radius outside that package. All line numbers
verified by reading the file directly (not grep-only), on the current worktree HEAD.

Context confirmed from docs/plans/analyzers/ledger-analyzer-optimizer-refactor.md (T1/T2 ledger,
Sec27/Sec28, DONE 2026-08-24): T1 landed and its final, confirmed-intentional behavior is --
internal/engines/steadystate/engine_v2.go:207 composeAnalyzerResults always searches
baseResults for domain.SaturationAnalyzerName and returns that entry; it falls back to
baseResults[0] only if saturation is truly absent from the list (never happens in production,
since saturation runs unconditionally, engine_v2.go:122-127, and errors out before compose is
reached if it fails). runAnalyzersAndScore (engine_v2.go:174-177) then wraps exactly that one
composed result in a length-1 literal:
  namedResults := []allocation.NamedAnalyzerResult{ buildNamedResult(ctx, composed.name, composed.result, ...) }
Consequence for this reports Sec2: the one entry the optimizer ever receives today is not just
length-1, it is also always literally named domain.SaturationAnalyzerName and always at index 0.
Nothing currently produces a length-1 slice whose single entry is NOT saturation. T2 (this ledgers
own next task, NOT STARTED) already lists a near-identical removal set to the one below,
independently arrived at.

---

## 1. Every function reading NamedAnalyzerResult / []NamedAnalyzerResult / AnalyzerResults

### internal/engines/allocation/optimizer_interfaces.go

- type NamedAnalyzerResult struct -- optimizer_interfaces.go:27-68. Not a function, but the
  type itself. Comment at optimizer_interfaces.go:75:
  AnalyzerResults []NamedAnalyzerResult // per-analyzer slice; saturation entry is always first
  -- this comment is a stale multi-analyzer-era claim (first implies among others); post-T1 it is
  trivially true (its the only entry) but the phrasing itself is a latent trap for a future reader
  who assumes there might be a second entry after it.

### internal/engines/allocation/analyzer_helpers.go

- ResultIsInformative(nr NamedAnalyzerResult) bool -- def analyzer_helpers.go:53-63.
  Takes a single entry by value, not a slice. Body:
  for _, vc := range nr.Result.VariantCapacities { if vc.Reason != ReasonNoData && vc.Reason != ReasonError { return true } }
  Iterates one entrys VariantCapacities, not analyzers. No cross-analyzer assumption.
  Call sites: internal/engines/steadystate/engine_v2.go:376 -- if allocation.ResultIsInformative(*nr)
  inside a for i := range namedResults loop in updateLivenessAndSetLive.

- applyAllocation(s []NamedAnalyzerResult, v string, n int) -- def analyzer_helpers.go:71-85.
  Body: for i := range s { if s[i].Result == nil { continue } ... s[i].Remaining -= float64(n) * prc; if s[i].Remaining < 0 { s[i].Remaining = 0 } }
  Iterates all entries, decrementing each ones Remaining. With N=1 this loop body runs once --
  degenerates correctly (no bug), but the loop itself is now pointless machinery for one iteration.
  Call sites: analyzer_helpers.go:428 (allocateForModelPaired, inside the anchor-role loop).

- saturationNamedEntry(s []NamedAnalyzerResult) *NamedAnalyzerResult -- def analyzer_helpers.go:94-101.
  Body: for i := range s { if s[i].Name == domain.SaturationAnalyzerName { return &s[i] } }; return nil
  This is the one name-based lookup that everything else keys off. It does NOT assume index 0 --
  it searches by name and would find saturation at any position. Given T1s confirmed behavior
  (header above), the single entry is always named saturation, so this always succeeds and always
  returns &s[0] in practice, but the code does not hard-code that assumption; it re-derives it by
  name every call.
  Call sites: cost_aware_optimizer.go:254, rescale.go:344, rescale.go:525, variant_records.go:79.

- prcForVariant(r *domain.AnalyzerResult, v string) float64 -- def analyzer_helpers.go:105-112.
  Takes *domain.AnalyzerResult, not NamedAnalyzerResult/slice -- included because every caller
  reaches it by first indexing into one analyzer entrys .Result. Not analyzer-count-sensitive.

- initRoleState(s []NamedAnalyzerResult) (roles []string, pickerState RolePairedState) -- def analyzer_helpers.go:131-167.
  Body: for i, e := range s { pickerState[i] = make(map[string]float64) ...; if e.RoleCapacities != nil { for role, rc := range e.RoleCapacities { pickerState[i][role] = rc.RequiredCapacity; s[i].RoleSpare[role] = rc.SpareCapacity; roleSet[role] = struct{}{} } } else { pickerState[i][domain.RoleBoth] = e.Remaining; ...; s[i].RoleSpare[domain.RoleBoth] = e.Spare; roleSet[domain.RoleBoth] = struct{}{} } }
  Iterates all entries building one pickerState[i] map per entry (pickerState is sized len(s), i.e.
  RolePairedState []map[string]float64 indexed by analyzer-index). With N=1, pickerState has exactly
  one map slot; every later helper reading state[i][role] for i other than 0 is unreachable. This is
  the structural root of the indexed by analyzer position pattern that everything downstream
  (roleBottleneckReplicas, roleAggRemaining, fairShareValue) inherits.
  Call sites: cost_aware_optimizer.go:60, greedy_score_optimizer.go:132, greedy_score_optimizer.go:171,
  greedy_score_optimizer.go:285, greedy_score_optimizer.go:348.

- roleBottleneckReplicas(s []NamedAnalyzerResult, state RolePairedState, role, v string) int -- def analyzer_helpers.go:186-202.
  Body: max := 0; for i, e := range s { ...; n := int(math.Ceil(state[i][role] / prc)); if n > max { max = n } }; return max
  Genuine cross-analyzer max -- max_i ceil(state[i][role] / PRC_i[v]) per its own doc comment
  (analyzer_helpers.go:184-185: computes the cross-analyzer bottleneck replica count). This is
  exactly the kind of only makes sense for N>1 logic Sec3 asks about -- with N=1 it degenerates to a
  single term (max over one value = that value), correct but the max framing is now vacuous.
  Call sites: analyzer_helpers.go:372 (allocateForModelPaired).

- roleAggRemaining(s []NamedAnalyzerResult, state RolePairedState, role string) float64 -- def analyzer_helpers.go:205-213.
  Body: max := 0.0; for i := range s { if d := state[i][role]; d > max { max = d } }; return max
  Doc comment: returns max cross-analyzer remaining demand for role. Same shape as above --
  genuine cross-analyzer max, degenerates correctly to the one value when N=1.
  Call sites: analyzer_helpers.go:374, analyzer_helpers.go:395 (both inside allocateForModelPaired).

- anyRoleNeedsScaleUp(state RolePairedState, roles []string) bool -- def analyzer_helpers.go:217-226.
  Body: for _, role := range roles { for _, m := range state { if m[role] > 0 { return true } } }
  Iterates state (one map per analyzer-index) as an OR-across-analyzers gate -- does ANY analyzer,
  for ANY role, have remaining demand. With N=1 this is just does the one map have any positive
  role value, correct but the double loop is now checking a single-element outer collection.
  Call sites: analyzer_helpers.go:349 (loop condition in allocateForModelPaired), cost_aware_optimizer.go:61, greedy_score_optimizer.go:134.

- variantsForRole(vcs []variantRecord, role string) []variantRecord -- def analyzer_helpers.go:230-242.
  Takes []variantRecord, not []NamedAnalyzerResult -- per-variant, not per-analyzer. Not
  analyzer-count-sensitive. Included for completeness since it is adjacent machinery in the same
  file and referenced by role-generic helpers.

- safeRemovalReplicasForRole(s []NamedAnalyzerResult, v, role string) int -- def analyzer_helpers.go:250-274.
  Body: smallest := math.MaxInt; found := false; for _, e := range s { if !e.Live { continue }; ...; n := int(math.Floor(e.RoleSpare[role] / prc)); if n < smallest { smallest = n }; found = true }; if !found || smallest < 0 { return 0 }; return smallest
  Doc comment: the minimum of floor(RoleSpare[role]_i / PRC_i[v]) across live analyzers. Genuine
  cross-analyzer min, and -- unlike the two max helpers above -- this one has a real per-entry
  gate (if !e.Live { continue }) that changes behavior based on which/how-many entries are live.
  With N=1: if that one entry is non-live, found stays false and the function returns 0 (blocks all
  scale-down of variant v in that role) rather than no other analyzer to check. This is a
  behavior-preserving degeneration (single live-or-not-live analyzer directly gates safe removal),
  not a bug, but it is the sharpest place where is saturation live now singlehandedly controls
  scale-down safety with no other analyzer to fall back on.
  Call sites: cost_aware_optimizer.go:454 (closure inside scaleDownRoleIterated).

- applyDeallocationForRole(s []NamedAnalyzerResult, v, role string, n int) -- def analyzer_helpers.go:282-296.
  Body: for i := range s { if s[i].Result == nil || s[i].RoleSpare == nil { continue }; ...; s[i].RoleSpare[role] -= float64(n) * prc; if s[i].RoleSpare[role] < 0 { s[i].RoleSpare[role] = 0 } }
  Iterates all entries, unconditionally (not Live-gated, per its own comment: not Live-gated:
  non-live entries are already excluded from the veto ... so mutating their RoleSpare here is
  harmless). Degenerates correctly to update the one entry with N=1.
  Call sites: cost_aware_optimizer.go:457 (closure inside scaleDownRoleIterated).

- needsScaleDownForRole(s []NamedAnalyzerResult, role string) bool -- def analyzer_helpers.go:305-317.
  Body: liveCount := 0; for _, e := range s { if !e.Live { continue }; if e.Result == nil || e.RoleSpare == nil || e.RoleSpare[role] <= 0 { return false }; liveCount++ }; return liveCount > 0
  Doc comment: reports whether every live analyzer agrees this role has spare capacity (all-down
  gate...) ... Safety floor: if no live analyzer remains, there is no current basis to scale down.
  This is the clearest all analyzers agree logic in the package -- an AND-across-analyzers veto.
  With N=1 it degenerates to is the one entry live AND does it have spare capacity -- correct, but
  the veto semantics (no OTHER analyzer can override this ones negative vote) is now vacuously
  about a single analyzer, which is exactly the not the same guarantee risk to flag: a reader could
  mistake no live analyzer -> false as still meaning something richer than saturation happens to be
  stale.
  Call sites: cost_aware_optimizer.go:444 (loop guard in scaleDownRoleIterated).

- RolePickFn type -- def analyzer_helpers.go:323-330. Function type whose signature includes
  s []NamedAnalyzerResult as a parameter, but see fairShareRolePick below -- the concrete
  implementations largely ignore it.

- allocateForModelPaired(ctx, s []NamedAnalyzerResult, variants, stateMap, available, targets, pick, pickerState, roles) -- def analyzer_helpers.go:337-434.
  Body relevant excerpt: for anyRoleNeedsScaleUp(pickerState, roles) { ... n := min(roleBottleneckReplicas(s, pickerState, role, variantByRole[role]), capByRole[role]); ...; demand := roleAggRemaining(s, pickerState, role); ... } then later applyAllocation(s, v, kByRole[anchor]).
  Orchestrates the cross-analyzer helpers above; itself does not directly index s[0] but passes s
  straight through to roleBottleneckReplicas/roleAggRemaining/applyAllocation, all of which
  degenerate correctly. Also uses the P-anchor role convention at analyzer_helpers.go:426-430
  (for _, anchor := range []string{prefill, domain.RoleBoth} { if v, ok := variantByRole[anchor]; ok { applyAllocation(s, v, kByRole[anchor]); break } }) -- this is role-based, not analyzer-position-based, so unaffected by T1.
  Call sites: cost_aware_optimizer.go:62, greedy_score_optimizer.go:312.

### internal/engines/allocation/cost_aware_optimizer.go

- Optimize(ctx, requests, constraints) -- def cost_aware_optimizer.go:39-76. Body:
  s := req.AnalyzerResults; roles, ps := initRoleState(s); if anyRoleNeedsScaleUp(ps, roles) { allocateForModelPaired(ctx, s, records, stateMap, nil, targets, costGreedyRolePick, ps, roles) } else { scaleDownRoleIterated(ctx, s, records, targets, stateMap) }
  Reads the raw slice once per request and threads it through; no direct indexing.

- costGreedyRolePick(role string, _ []NamedAnalyzerResult, variants, stateMap, _, targets) (string, int) -- def cost_aware_optimizer.go:81-105.
  First param after role is _ []NamedAnalyzerResult -- explicitly unused (blank identifier). Does
  not read analyzer data at all; picks purely by cost efficiency and MaxReplicas headroom.

- sortVariantsForScaleDown(s []NamedAnalyzerResult, roleVCs []variantRecord) []variantRecord -- def cost_aware_optimizer.go:161-184.
  Body: weighted := func(name string) float64 { sum := 0.0; for _, e := range s { if e.Result == nil { continue }; sum += e.Score * prcForVariant(e.Result, name) }; return sum } then sorts by (Cost desc, weighted asc, name asc).
  This is the exact function with the reduces to a simpler form comment (Sec4).

- buildDecisionsWithOptimizer(req, stateMap, vcMap, targets, optimizerName) -- def cost_aware_optimizer.go:243-328.
  Body excerpt: satNamed := saturationNamedEntry(req.AnalyzerResults) (line 254) then, per target,
  if satNamed != nil { reqCap, spareCap := satNamed.RequiredCapacity, satNamed.SpareCapacity; ...; if rc, ok := satNamed.RoleCapacities[role]; ok { reqCap, spareCap = rc.RequiredCapacity, rc.SpareCapacity }; decision.RequiredCapacity = reqCap; decision.SpareCapacity = spareCap } (lines 312-323).
  Explicit name-based saturation lookup, used purely for observability gauge fields -- not
  allocation math. Correctly nil-checked (if satNamed != nil), so a slice with no saturation entry
  degrades to zero-valued gauge fields rather than crashing. Since T1 guarantees the one entry is
  named saturation, satNamed is always non-nil in production today.
  Call sites: cost_aware_optimizer.go:68, greedy_score_optimizer.go:151, greedy_score_optimizer.go:174, rescale.go:386.

- scaleDownRoleIterated(ctx, s []NamedAnalyzerResult, variants, targets, stateMap...) -- def cost_aware_optimizer.go:430-461.
  Body: roles := rolesOf(variants); for _, role := range roles { if !needsScaleDownForRole(s, role) { continue }; ...; sorted := sortVariantsForScaleDown(s, roleVCs); scaleDownVariantSet(ctx, sorted, targets, states, func(vc) int { return safeRemovalReplicasForRole(s, vc.VariantName, role) }, func(vc, n) { applyDeallocationForRole(s, vc.VariantName, role, n) }) }
  Threads s through to every per-role cross-analyzer helper.
  Call sites: cost_aware_optimizer.go:65, greedy_score_optimizer.go:172.

### internal/engines/allocation/greedy_score_optimizer.go

- type modelWork struct -- greedy_score_optimizer.go:43-52. Field s []NamedAnalyzerResult //
  working slice; Remaining/Spare decremented in place -- struct field, not a function, but the
  whole modelWork lifecycle carries this slice by reference across
  fairShareScaleUp/allocateForModel calls.

- fairShareValue(priority float64, s []NamedAnalyzerResult, ps RolePairedState, roles []string) float64 -- def greedy_score_optimizer.go:62-94.
  Body: weighted := 0.0; for i, e := range s { if e.Result == nil { continue }; roleSum := 0.0; for _, role := range roles { if i < len(ps) { roleSum += ps[i][role] } }; weighted += roleSum * e.Score }; if fsv := priority * weighted; fsv > 0 { return fsv }; // Fallback: max remaining demand across roles when Score=0 or priority=0. maxDemand := 0.0; for i, e := range s { ...; for _, role := range roles { if ps[i][role] > maxDemand { maxDemand = ps[i][role] } } }; return maxDemand
  This is the second explicit Score-weighted cross-analyzer sum (formula documented at
  greedy_score_optimizer.go:59: fsv = priority x SUM_i Score_i x SUM_role pickerState[i][role]).
  With N=1 the outer sum has one term, so weighted == ps[0][...]*Score_0 and the whole function
  degenerates to priority x Score_saturation x SUM_role pickerState[0][role] -- correct, but the
  sum-over-i framing and the fallback branchs second full slice walk are now redundant machinery
  for one term.
  Call sites: greedy_score_optimizer.go:133, greedy_score_optimizer.go:349, greedy_score_optimizer.go:351.

- Optimize(ctx, requests, constraints) -- def greedy_score_optimizer.go:98-183. Body excerpt:
  s := req.AnalyzerResults; roles, ps := initRoleState(s); fsv := fairShareValue(req.Priority, s, ps, roles); if anyRoleNeedsScaleUp(ps, roles) || fsv > 0 { w := o.buildScaleUpWork(req, records, s, ps, roles, fsv); ... } (scale-up path, lines 131-141) and, for scale-down,
  s := req.AnalyzerResults; _, _ = initRoleState(s); scaleDownRoleIterated(ctx, s, records, targets, stateMap) (lines 170-172).

- buildScaleUpWork(req, records, s []NamedAnalyzerResult, ps, roles, fsv) *modelWork -- def greedy_score_optimizer.go:186-200.
  Stores s directly into modelWork.s; no iteration itself.

- fairShareScaleUp(ctx, work []*modelWork, available, availableByNS) -- def greedy_score_optimizer.go:203-262.
  Operates on *modelWork (which embeds s), not directly on NamedAnalyzerResult; delegates per-model
  work to allocateForModel.

- allocateForModel(ctx, w *modelWork, mean float64, available, availableByNS) bool -- def greedy_score_optimizer.go:267-354.
  Body excerpt: _, ps := initRoleState(w.s); for i := range ps { for _, role := range w.roles { if ps[i][role] > target { ps[i][role] = target } } } (cap picker-state at target, lines 285-291), then
  pick := fairShareRolePick(target, w.s, w.roles, w.limited); allocateForModelPaired(ctx, w.s, w.records, stateMap, effAvail, w.targets, pick, ps, w.roles) (lines 311-313), then recompute:
  if len(w.roles) == 1 && w.roles[0] == domain.RoleBoth { _, freshPs := initRoleState(w.s); w.remaining = fairShareValue(w.req.Priority, w.s, freshPs, w.roles) } else { w.remaining = fairShareValue(w.req.Priority, w.s, ps, w.roles) } (lines 347-352).
  Every ps[i]/w.s index here is driven by initRoleStates per-entry indexing (Sec1s initRoleState
  note) -- with N=1, i ranges over exactly {0}.

- fairShareRolePick(target float64, s []NamedAnalyzerResult, roles []string, limited gpuLimitTracker) RolePickFn -- def greedy_score_optimizer.go:403-461.
  Body starts: _ = s     // slice available for future multi-analyzer demand inspection and
  _ = roles // roles available for future per-role budget splitting (lines 404-405) -- s is
  explicitly discarded, unused for any analyzer-count-sensitive logic; the returned closures own
  _ []NamedAnalyzerResult parameter (line 408) is likewise unused. The whole variant-picking body
  works off target (a scalar fair-share budget) and variants/stateMap/available, never touching
  per-analyzer data. This function is a no-op with respect to N already -- nothing to simplify
  here for T2 beyond deleting the two dead s/roles params if desired.

---

## 2. Places that assume/depend on saturation being present/identifiable in the slice

All of these are name-based (.Name == domain.SaturationAnalyzerName), not position-based. None of
the reviewed code assumes entry 0 is saturation without checking the name first -- the one partial
exception is RolePairedState/initRoleStates indexing scheme, which assumes saturations data lives
at some index i and everything downstream reads s[i]/ps[i] generically rather than ever
hard-coding s[0].

- saturationNamedEntry (analyzer_helpers.go:94-101) -- the single canonical lookup. Linear scan by
  .Name, returns nil if absent. Every other saturation-specific read in the package goes through
  this function or receives its result as a parameter (satNamed *NamedAnalyzerResult):
  - variant_records.go:79 -- recordsForRequest: nr := saturationNamedEntry(req.AnalyzerResults); if nr == nil || nr.Result == nil { return nil }. Gates the whole per-model pipeline -- both
    optimizers Optimize call recordsForRequest first and skip the model entirely (continue) if it
    returns nil (cost_aware_optimizer.go:48-51, greedy_score_optimizer.go:126-129, rescale.go:225-229,
    rescale.go:503-506). If T2 (or a future producer change) ever emits the one entry under a
    different name, EVERY model would silently stop being optimized -- this is the highest-leverage
    single point of failure for the guaranteed length-1 but not name-guaranteed risk the user flagged.
  - cost_aware_optimizer.go:254 -- buildDecisionsWithOptimizer: satNamed := saturationNamedEntry(req.AnalyzerResults), used only for the RequiredCapacity/SpareCapacity gauge fields (nil-safe, see Sec1).
  - rescale.go:344 -- rescaleModelDecisions: satNamed := saturationNamedEntry(req.AnalyzerResults) then immediately, unchecked: records := buildVariantRecords(req, satNamed.Result) (line 345) --
    no nil check on satNamed itself (only buildVariantRecords internally nil-checks satResult, i.e.
    satNamed.Result, not satNamed). A nil satNamed here would panic on satNamed.Result. Verified safe
    today only because the sole caller (applyRescale:308) restricts reqs to requests that already
    passed recordsForRequest(req) != nil at rescale.go:226-229, which internally required
    saturationNamedEntry(...) != nil. This is an implicit cross-function invariant, not a local
    guard -- fragile if applyRescales filtering ever changes.
  - rescale.go:525 -- rescaleInputsForGroup: satNamed := saturationNamedEntry(req.AnalyzerResults); if satNamed == nil || satNamed.Result == nil { continue } -- correctly nil-checked, unlike the
    sibling above.
  - rescale.go:572 modelDemandGPUs(satNamed *NamedAnalyzerResult, ...) and rescale.go:583
    roleDemandGPUs(satNamed *NamedAnalyzerResult, ...) both receive an already-resolved satNamed
    pointer and dereference satNamed.Result.TotalDemand (rescale.go:584) with no nil check on
    satNamed or satNamed.Result -- safe only because both call sites (rescale.go:359 inside
    rescaleModelDecisions, rescale.go:549 inside rescaleInputsForGroup after its own nil check)
    already hold a verified-non-nil satNamed.

- optimizer_interfaces.go:75 doc comment -- AnalyzerResults []NamedAnalyzerResult // per-analyzer
  slice; saturation entry is always first -- this is a comment, not enforced by any code path.
  Nothing in the package actually indexes AnalyzerResults[0] and assumes its saturation; every real
  saturation read goes through the name-based saturationNamedEntry. If a hypothetical T2 changed
  the type to drop the name-search and hard-code s[0], that would newly introduce a positional
  assumption that does not exist today. Recommendation embedded here for T2: do not simplify
  saturationNamedEntry into &s[0] unless the producer side additionally guarantees position, not
  just length -- today it only guarantees name.

- analyzer_helpers.go:87-93 doc comment on saturationNamedEntry -- Saturation is no longer the
  keeper of per-variant metadata ... What is still special about this entry is that its P is the
  one that sizes replicas -- confirms saturations specialness is semantic (its P is authoritative
  for replica sizing), not positional.

---

## 3. Places that genuinely reason over MULTIPLE entries as a group

All of the following only make sense for N>1 and are the concrete T2 candidates (matching the
ledgers own T2 Todo list at ledger-analyzer-optimizer-refactor.md:106-108):

1. applyAllocation (analyzer_helpers.go:71-85) -- loops for i := range s decrementing every entrys
   Remaining. N>1-only in the sense that it exists to keep multiple analyzers bookkeeping in sync;
   with N=1 it is decrement the one entry.
2. initRoleState (analyzer_helpers.go:131-167) -- builds pickerState sized len(s), one map slot per
   analyzer. The entire RolePairedState []map[string]float64 indexed by analyzer-index design
   exists for N>1.
3. roleBottleneckReplicas (analyzer_helpers.go:186-202) -- explicit cross-analyzer max:
   max_i ceil(state[i][role]/PRC_i[v]).
4. roleAggRemaining (analyzer_helpers.go:205-213) -- explicit cross-analyzer max: max cross-analyzer
   remaining demand for role.
5. safeRemovalReplicasForRole (analyzer_helpers.go:250-274) -- explicit cross-analyzer min across
   live analyzers: the minimum of floor(RoleSpare[role]_i / PRC_i[v]) across live analyzers.
6. needsScaleDownForRole (analyzer_helpers.go:305-317) -- explicit all-agree veto: whether every
   live analyzer agrees this role has spare capacity.
7. applyDeallocationForRole (analyzer_helpers.go:282-296) -- loops for i := range s, symmetric with
   #1 for scale-down.
8. fairShareValue (greedy_score_optimizer.go:62-94) -- explicit Score-weighted sum:
   SUM_i Score_i x SUM_role pickerState[i][role], plus a second full-slice fallback walk for the
   Score=0/priority=0 case.
9. sortVariantsForScaleDowns weighted closure (cost_aware_optimizer.go:161-171) -- explicit
   Score-weighted sum: SUM_i Score_i*PRC_i[v].

This is exactly the ledgers own count: the 7 helper functions [#1-7 above] and 2 weighted-aggregation
call sites [#8, #9] (ledger-analyzer-optimizer-refactor.md:103).

anyRoleNeedsScaleUp (analyzer_helpers.go:217-226) is a borderline case: it is an OR-across-analyzers
gate structurally, but since it is called immediately after initRoleState (which already only ever
produces one map with N=1), it degenerates to a single boolean read with no behavioral risk --
listed in Sec1 for completeness but not counted as a genuinely needs N>1 item since removing the
outer loop would be pure code cleanup, not a behavior change.

---

## 4. Existing what if theres only 1 analyzer comments

Exactly one such comment exists in the allocation package (confirmed by direct read, matching the
ledgers own citation at ledger-analyzer-optimizer-refactor.md:283):

cost_aware_optimizer.go:154-160, directly above sortVariantsForScaleDown:

  // sortVariantsForScaleDown orders a roles variants for cost-greedy scale-down:
  //  1. Cost descending -- shed the most expensive first.
  //  2. Tie: score-weighted per-replica capacity ascending -- SUM_i Score_i.PRC_i[v].
  //  3. Tie: variant name ascending -- full determinism.
  //
  // With a single analyzer (Score=1) this reduces to Cost-desc then PRC-asc, i.e.
  // #1237s existing tie-break.

No other function in the package has an equivalent comment. fairShareValue
(greedy_score_optimizer.go:54-61) documents its formula in general N-analyzer terms but does NOT
call out the N=1 degenerate case explicitly:

  // fairShareValue computes the fair-share priority metric for one model.
  // Phase 3: reads picker-local role-remaining (sum over roles x analyzer Score)
  // so the metric reflects actual per-role demand remaining rather than the
  // P-anchor model-level scalar.
  //
  //	fsv = priority x SUM_i Score_i x SUM_role pickerState[i][role]
  //
  // Falls back to max remaining demand when the weighted result is zero.

This is a gap relative to sortVariantsForScaleDowns comment -- fairShareValue is equally
N=1-degenerate (Sec3 item 8) but the doc does not say so.

analyzer_helpers.go:333-336 (allocateForModelPaireds doc) documents an arity-1 degenerate case, but
for roles, not analyzers:

  // Handles any set of roles (including the arity-1 both single-role case).
  // ...
  // Arity-1 (roles = [both]) reduces to plain per-variant allocation.

This is a different axis (role count, always >= 1 regardless of T1/T2) and should not be conflated
with the analyzer-count axis this report is about, though the two are easy to confuse since both
use arity-1/single X language.

No test file in the package has an equivalent single analyzer comment (analyzer_fixtures_test.go
was read in full; its named() builder defaults the name to saturation when empty
(analyzer_fixtures_test.go:146-163) but carries no commentary about N=1 vs N>1). No test in
cost_aware_optimizer_test.go / greedy_score_optimizer_test.go / optimizer_equivalence_test.go
constructs a multi-entry AnalyzerResults slice -- every fixture already builds exactly one entry,
confirming the test suite currently only ever exercises the N=1 case in practice (grep-verified:
zero multi-literal AnalyzerResults slices in the packages test files).

---

## 5. Readers outside internal/engines/allocation/ that read NamedAnalyzerResult fields directly

All in internal/engines/steadystate/engine_v2.go (the producer side / T1s own file -- none in
engine.go itself despite that file containing optimizeV2, the call site that invokes
optimizer.Optimize):

- recordAnalyzerMetrics(namespace, modelID string, results []allocation.NamedAnalyzerResult) --
  def engine_v2.go:252-282. Body: for _, nr := range results { if nr.Result == nil { continue }; if len(nr.RoleCapacities) > 0 { for role, rc := range nr.RoleCapacities { e.metricsEmitter.RecordAnalyzerDemand(nr.Name, namespace, modelID, role, rc.TotalDemand); ... } } else { e.metricsEmitter.RecordAnalyzerDemand(nr.Name, namespace, modelID, , nr.Result.TotalDemand); ... }; for _, vc := range nr.Result.VariantCapacities { e.metricsEmitter.RecordAnalyzerTarget(nr.Name, namespace, modelID, vc.VariantName, vc.PerReplicaCapacity); ... } }.
  Iterates the (now length-1) slice generically by index-free range, keying metrics off nr.Name --
  degenerates correctly to emitting exactly one analyzers series (was already reachable via T1s own
  reduction, so this function pre-dates and is unaffected by whatever the optimizer does with the
  same slice). Called from runAnalyzersAndScore:180.

- updateLivenessAndSetLive(ctx, namespace, modelID string, namedResults []allocation.NamedAnalyzerResult) --
  def engine_v2.go:348-388. Body: for i := range namedResults { nr := &namedResults[i]; if allocation.ResultIsInformative(*nr) { ...; perAnalyzer[nr.Name] = at }; lastGood, ok := perAnalyzer[nr.Name]; nr.Live = ok && now.Sub(lastGood) <= threshold }.
  Sets nr.Live in place, keyed by nr.Name, per-entry, before the optimizer ever sees the slice --
  this is where NamedAnalyzerResult.Live (which needsScaleDownForRole/safeRemovalReplicasForRole
  read, Sec1/Sec3) originates. Called from runAnalyzersAndScore:179.

- detectDemandLiveness(ctx, modelID, namespace string, namedResults []allocation.NamedAnalyzerResult, perAnalyzer map[string]time.Time, now, threshold) --
  def engine_v2.go:435-487. Body: var tp *allocation.NamedAnalyzerResult; for i := range namedResults { if namedResults[i].Name == throughput.AnalyzerName { tp = &namedResults[i]; break } }; if tp == nil { return }.
  Another name-based lookup, searching for throughput.AnalyzerName specifically (not saturation) --
  since T1 collapses to saturation-only, tp is always nil in production today and this detector is
  permanently a no-op post-T1 (matches the ledgers Sec28 note that 3 TestDetectDemandLiveness_*
  tests were skipped for exactly this reason). Called from updateLivenessAndSetLive:388.

- applyUniversalThreshold(nr *allocation.NamedAnalyzerResult, scaleUp, scaleDown float64) --
  def engine_v2.go:562-... (body continues past the read window; signature and nil-guard confirmed:
  if nr == nil || nr.Result == nil { return }). Operates on one *NamedAnalyzerResult at a time,
  called once per analyzer inside buildNamedResults construction path -- not slice-level, included
  for completeness of who touches the type.

- warnUnsizableShortfall(ctx context.Context, nr *allocation.NamedAnalyzerResult) -- def
  engine_v2.go:921-941. Body: result := nr.Result; if nr.RequiredCapacity <= 0 || len(result.VariantCapacities) == 0 { return }; for _, vc := range result.VariantCapacities { if vc.PerReplicaCapacity > 0 { return } }; ....
  Single-entry, no cross-analyzer logic. No nil check on nr or nr.Result before dereferencing
  nr.Result at line 922 -- relies on caller discipline.

- logAnalyzerResult(ctx context.Context, modelID, namespace string, nr allocation.NamedAnalyzerResult) --
  def engine_v2.go:999-.... Body starts if nr.Result == nil { return }; logs one entrys fields
  (PRC, Role, etc. per variant) as a structured INFO line. Called once per entry from
  runAnalyzersAndScore:182-184s for _, nr := range namedResults { logAnalyzerResult(...) } -- with
  N=1 this logs exactly one line per model per cycle (was previously up to N lines).

- hasSaturationResult(req allocation.ModelScalingRequest) bool -- def engine_v2.go:745-752. Body:
  for _, e := range req.AnalyzerResults { if e.Name == domain.SaturationAnalyzerName { return e.Result != nil } }; return false.
  A second, independent name-based saturation lookup, structurally identical to
  allocation.saturationNamedEntry but re-implemented locally in the steadystate package (not
  reusing the exported helper -- note saturationNamedEntry is unexported/package-private to
  allocation, so this duplication is somewhat forced by visibility, not sloppiness). Used to gate
  GPU-usage accounting: called from computeCurrentGPUUsage (engine_v2.go:710-718) and
  computeCurrentGPUUsageByNamespace (engine_v2.go:726-739) to decide whether a models replica
  counts should be charged to any accelerator-type quota bucket. This is a real, independent
  blast-radius point: if the one entry were ever emitted under a non-saturation name, quota
  accounting would silently stop charging that models usage anywhere (same failure mode as
  recordsForRequests gate in Sec2, but for the GPU-budget/quota subsystem rather than the
  optimizers replica math).

- buildNamedResult, buildCapacities, buildRoleCapacities (engine_v2.go:847+, 876+, 958+) --
  construct/populate NamedAnalyzerResult values; producer-side, not consumers of an existing slices
  cross-entry structure. Not part of the reads AnalyzerResults blast radius in the sense this
  report tracks (they build one entry at a time, called once per raw analyzer result before compose
  -- or, post-T1, called exactly once on the already-composed result, per runAnalyzersAndScore:176).

No other package under internal/ reads NamedAnalyzerResult fields directly (verified:
grep -rl NamedAnalyzerResult AnalyzerResults across the whole module, excluding tests, returns only
internal/domain/analyzer.go (doc-comment mentions only, no field access --
internal/domain/analyzer.go:83,102,125), internal/engines/allocation/*.go, and
internal/engines/steadystate/{engine.go,engine_v2.go}; engine.go itself has no direct field reads --
its only relevant lines are doc comments at engine.go:185 and engine.go:933).

---

## Risk summary (priority order)

1. hasSaturationResult / saturationNamedEntry name-dependency (Sec2, Sec5) -- highest-priority real
   risk. Two independent name-based lookups (one in allocation, one duplicated in steadystate) both
   gate entire subsystems -- the optimizers per-model pipeline via recordsForRequest, and GPU-quota
   usage accounting via hasSaturationResult -- on the single entry being named exactly
   domain.SaturationAnalyzerName. Today this is guaranteed by composeAnalyzerResults confirmed
   behavior (always resolves to the saturation entry by name). If T2 or any future change ever lets
   the single entry be named something else (e.g. a restructuring that names it after whichever
   analyzer won a fallback, or a bug in composes fallback-to-baseResults[0] path when saturation is
   somehow absent), every model would silently stop being optimized (recordsForRequest returns nil
   -> model skipped) and silently stop being quota-charged (hasSaturationResult returns false ->
   usage undercounted) -- both are silent no-ops, not crashes, making this the hardest failure mode
   to notice in production.

2. rescaleModelDecisions / modelDemandGPUs / roleDemandGPUs unchecked satNamed nil-deref (Sec2) --
   rescale.go:344-345 and rescale.go:572-608 dereference satNamed.Result without a local nil check,
   relying entirely on the implicit invariant that applyRescales upstream recordsForRequest filter
   (rescale.go:226-229) already excluded any request without a saturation entry. This is correct
   today but is exactly the kind of cross-function invariant that a T2 restructuring (especially
   Decide whether AnalyzerResults stays a slice or becomes a single field per the ledgers own T2
   Todo) could break by changing what guarantees hold at the point rescaleModelDecisions is called.
   Needs an explicit nil guard if the type or the calling structure changes.

3. The 7 helper functions + 2 weighted-aggregation sites (Sec3) -- applyAllocation, initRoleState,
   roleBottleneckReplicas, roleAggRemaining, safeRemovalReplicasForRole, needsScaleDownForRole,
   applyDeallocationForRole, plus fairShareValue and sortVariantsForScaleDowns weighted closure --
   all already degenerate correctly to N=1 today (verified: no test exercises N>1 in this package,
   and the math in each is a straightforward max/min/sum that is well-defined and correct for a
   1-element collection). No behavior fix is needed here -- this is pure simplification opportunity
   for T2, not a correctness bug. The one subtlety worth flagging: safeRemovalReplicasForRoles
   Live-gate and needsScaleDownForRoles all-agree veto now mean is saturation live, full stop --
   there is no other analyzer to fall back on if saturation itself is ever stale/erroring, which is
   a legitimate behavior change from the pre-T1 world (where a second live analyzer could still
   permit scale-down even if one was stale), but it is a consequence of T1s producer-side change,
   not a bug in this packages consumer code, and it was already true the moment T1 landed
   regardless of whether T2 ever runs.

4. Stale doc comment (Sec1, Sec2) -- optimizer_interfaces.go:75s // per-analyzer slice; saturation
   entry is always first is misleading in isolation (implies first among several); should be
   corrected to state the real, verified guarantee (currently always exactly one entry, always
   saturation, by construction of composeAnalyzerResults) so a future reader does not assume
   multi-entry behavior still exists or is exercised anywhere.

5. fairShareValues missing N=1 comment (Sec4) -- purely a documentation gap relative to
   sortVariantsForScaleDowns comment; no functional risk, but worth fixing alongside T2 so both
   Score-weighted aggregations get the same reduces to X when N=1 treatment.

6. detectDemandLivenesss permanent no-op (Sec5) -- not a bug (confirmed intentional per the
   ledgers Sec28, tests already skipped), but worth carrying forward as context: this function is
   dead in production today (searches for throughput.AnalyzerName, which T1 never produces) and
   will need to be revisited if/when a future task reintroduces genuine multi-analyzer composition.
