# Composite-entry specification: what "compose" must produce (2026-08-25)

Scope and method: every claim below was verified by reading the cited file directly on the
current worktree HEAD (not grep-only), in the same pass as this document's authoring. Where a
prior document (docs/plans/analyzers/optimizer-call-map-2026-08-25.md) is referenced, its claims
were independently re-derived from the source files here, not copied.

Purpose: the optimizer's cross-analyzer helper functions in
`internal/engines/allocation/analyzer_helpers.go`, `cost_aware_optimizer.go`, and
`greedy_score_optimizer.go` are not dead multi-analyzer scaffolding to delete -- their actual
math is the load-bearing spec for what a single composite `NamedAnalyzerResult` must carry. This
document is the field-by-field, call-site-by-call-site trace that a compose-step design must be
built from.

---

## Task A -- Does `NamedAnalyzerResult` contain everything the optimizer needs?

### A.1 The struct itself

`internal/engines/allocation/optimizer_interfaces.go:27-68`:

```go
type NamedAnalyzerResult struct {
	Name              string
	Result            *domain.AnalyzerResult
	Score             float64            // per-analyzer weight from AnalyzerScoreConfig; used for fair-share priority
	Remaining         float64            // mutable remaining required capacity; P-scope for disaggregated, model-scope otherwise
	Spare             float64            // mutable remaining spare capacity; model-scope (non-disaggregated only)
	RoleSpare         map[string]float64 // per-role mutable spare; set by initRoleState; nil for non-disaggregated
	ScaleUpThreshold  float64            // resolved scale-up threshold used to compute RC
	ScaleDownBoundary float64            // resolved scale-down boundary used to compute SC

	TotalSupply            float64
	TotalAnticipatedSupply float64
	Utilization            float64

	RequiredCapacity float64 // >0 means scale-up needed
	SpareCapacity    float64 // >0 means scale-down possible

	RoleCapacities map[string]domain.RoleCapacity

	Live bool
}
```

`domain.AnalyzerResult` (`internal/domain/analyzer.go:106-127`):

```go
type AnalyzerResult struct {
	AnalyzerName string
	ModelID    string
	Namespace  string
	AnalyzedAt time.Time
	VariantCapacities []VariantCapacity
	TotalDemand float64
	RoleDemand map[string]float64
}
```

`domain.VariantCapacity` (`internal/domain/analyzer.go:144-175`):

```go
type VariantCapacity struct {
	VariantName string
	Role        string
	ReplicaCount    int
	PendingReplicas int
	PerReplicaCapacity float64
	Reason string
	TotalDemand float64
	Utilization float64
}
```

`domain.RoleCapacity` (`internal/domain/analyzer.go:86-93`):

```go
type RoleCapacity struct {
	Role                   string
	TotalSupply            float64
	TotalDemand            float64
	TotalAnticipatedSupply float64
	RequiredCapacity       float64
	SpareCapacity          float64
}
```

### A.2 How the fields are built (producer side, `internal/engines/steadystate/engine_v2.go`)

`buildNamedResult` (engine_v2.go:847-866):

```go
func buildNamedResult(ctx context.Context, name string, result *domain.AnalyzerResult, config config.ScalingPolicy,
	metaByVariant map[string]domain.VariantMetadata, scaleUp, scaleDown float64) allocation.NamedAnalyzerResult {
	nr := allocation.NamedAnalyzerResult{
		Name:              name,
		Result:            result,
		Score:             config.AnalyzerScore(name),
		ScaleUpThreshold:  scaleUp,
		ScaleDownBoundary: scaleDown,
	}
	buildCapacities(ctx, &nr, metaByVariant, scaleUp, scaleDown)
	nr.Remaining = nr.RequiredCapacity
	nr.Spare = nr.SpareCapacity
	return nr
}
```

`buildCapacities` (engine_v2.go:876-909) -- five steps: (1) join discovery's `Role` onto
`VariantCapacities` from `metaByVariant`; (2) `TotalSupply`/`TotalAnticipatedSupply` via
`aggregation.SumTotalSupply`/`SumTotalAnticipatedSupply`, `Utilization = TotalDemand/TotalSupply`
(0 if supply is 0); (3) `RoleCapacities` via `buildRoleCapacities`; (4) `applyUniversalThreshold`
writes `RequiredCapacity`/`SpareCapacity` (model-level and per-`RoleCapacity`); (5)
`warnUnsizableShortfall` (log-only, no field write).

`buildRoleCapacities` (engine_v2.go:958-983) -- returns `nil` when `result.RoleDemand` is empty
(non-disaggregated); otherwise pairs each role's `RoleDemand` value with
`aggregation.AggregateByRole(result.VariantCapacities)`'s per-role supply totals into a
`map[string]domain.RoleCapacity`, `RequiredCapacity`/`SpareCapacity` left zero (filled by step 4).

`applyUniversalThreshold` (engine_v2.go:562-600) -- `RC = max(0, TotalDemand/scaleUp -
TotalAnticipatedSupply)`, `SC = max(0, TotalSupply - TotalDemand/scaleDown)`, applied both at
model scope (writing `nr.RequiredCapacity`/`nr.SpareCapacity`) and at each `RoleCapacities[role]`
entry in place.

`Live` is NOT set by `buildNamedResult`/`buildCapacities` -- it is set afterward, per entry, by
`updateLivenessAndSetLive` (engine_v2.go:348-388, called from `runAnalyzersAndScore:179`) based on
a per-analyzer last-good-analysis timestamp compared against a staleness threshold
(`analyzerLivenessStaleCycles = 3`, engine_v2.go:89). `RoleSpare` is NOT set by the producer side
at all -- it is `nil` until the optimizer's own `initRoleState` (analyzer_helpers.go:131-167,
consumer side) lazily allocates and populates it on first use per model-optimize-cycle.

### A.3 Every field, cross-referenced against every downstream read in `internal/engines/allocation/*.go`

| Field | Read at (allocation package) | Notes |
|---|---|---|
| `Name` | `saturationNamedEntry` analyzer_helpers.go:96 (`s[i].Name == domain.SaturationAnalyzerName`) | The one name-based lookup; see Task B. |
| `Result` (nil-check gate) | applyAllocation:73, initRoleState:137, roleBottleneckReplicas:189, safeRemovalReplicasForRole:257, applyDeallocationForRole:284, needsScaleDownForRole:311, fairShareValue (greedy_score_optimizer.go:65,82), sortVariantsForScaleDown closure (cost_aware_optimizer.go:165), variant_records.go:80 (`nr.Result == nil`) | Universal nil-guard pattern; every cross-entry loop skips entries with `Result == nil`. |
| `Result.VariantCapacities` | `ResultIsInformative` analyzer_helpers.go:57; `prcForVariant` analyzer_helpers.go:106 (called from applyAllocation, roleBottleneckReplicas, safeRemovalReplicasForRole, applyDeallocationForRole, sortVariantsForScaleDown's `weighted` closure); `buildVariantRecords` variant_records.go:57-58 | Read by variant-name linear scan (`prcForVariant`) or by building a `map[string]domain.VariantCapacity` keyed by `VariantName` (`buildVariantRecords`). |
| `Result.TotalDemand` | rescale.go:561 (`satNamed.Result.TotalDemand`, rescaleInputsForGroup), rescale.go:584 (`roleDemandGPUs`, for the synthetic "both" role only) | Only 2 direct reads in `allocation/`; everywhere else demand reaches the optimizer pre-baked into `Remaining`/`RequiredCapacity`/`RoleCapacities[...].TotalDemand`. |
| `Result.RoleDemand` | not read anywhere in `internal/engines/allocation/*.go` | Consumed only by the producer (`buildRoleCapacities`, engine_v2.go) before the entry ever reaches the optimizer. By the time the optimizer sees the entry, role demand is already folded into `RoleCapacities[role].TotalDemand`/`RequiredCapacity`. |
| `Score` | fairShareValue greedy_score_optimizer.go:74, sortVariantsForScaleDown's `weighted` closure cost_aware_optimizer.go:168 | Both are genuine cross-analyzer sum-i Score_i x X_i sums (see Task A.4). |
| `Remaining` | initRoleState:152 (seeds `pickerState[i][domain.RoleBoth]`); applyAllocation:80-83 (mutated in place) | Only meaningful for the non-disaggregated ("both") synthetic role; for disaggregated models the picker state is seeded from `RoleCapacities`, not `Remaining`. |
| `Spare` | initRoleState:156 (seeds `s[i].RoleSpare[domain.RoleBoth]`) | Same "both"-role-only role as `Remaining`. |
| `RoleSpare` | initRoleState:142-156 (lazily allocated + populated); safeRemovalReplicasForRole:257,264; applyDeallocationForRole:284,287,291-293; needsScaleDownForRole:311 | The scale-down-side mutable per-role spare bookkeeping; entirely built and consumed inside the allocation package (never read back by engine_v2.go). |
| `ScaleUpThreshold` | not read anywhere in `internal/engines/allocation/*.go` | Only consumed as a log field, engine_v2.go:1041 (`logAnalyzerResult`). |
| `ScaleDownBoundary` | not read anywhere in `internal/engines/allocation/*.go` | Only consumed as a log field, engine_v2.go:1042. |
| `TotalSupply` | not read anywhere in `internal/engines/allocation/*.go` | Only consumed as a log field, engine_v2.go:1036, and internally by `applyUniversalThreshold` (producer side) before the entry reaches the optimizer. |
| `TotalAnticipatedSupply` | not read anywhere in `internal/engines/allocation/*.go` | Same -- producer-internal (`applyUniversalThreshold`, engine_v2.go:569) and log-only downstream. |
| `Utilization` (model-level) | not read anywhere in `internal/engines/allocation/*.go` | Only a log field, engine_v2.go:1038. Note: `variantRecord.Utilization` (a different, per-variant field, populated in `buildVariantRecords` from `VariantCapacity.Utilization`, variant_records.go:66) IS read, at cost_aware_optimizer.go:311 (`decision.Utilization = vc.Utilization`) -- do not conflate the two. |
| `RequiredCapacity` | cost_aware_optimizer.go:313 (`satNamed.RequiredCapacity`, gauge field only); rescale.go: not read directly (rescale recomputes its own GPU-denominated demand via `roleDemandGPUs`/`modelDemandGPUs` from `Result.TotalDemand`/`RoleCapacities[...].TotalDemand`, not from `RequiredCapacity`) | The token-level `RequiredCapacity` seeds `Remaining` (via `buildNamedResult`) but is not re-read as `RequiredCapacity` inside the allocation math itself -- the math reads `Remaining`. Direct field access is gauge/observability only. |
| `SpareCapacity` | cost_aware_optimizer.go:313 (`satNamed.SpareCapacity`, gauge field only) | Same pattern as `RequiredCapacity`: seeds `Spare`, itself only read directly for the decision gauge fields. |
| `RoleCapacities` | initRoleState:140,145 (`e.RoleCapacities != nil` branch, `for role, rc := range e.RoleCapacities`); cost_aware_optimizer.go:318 (`satNamed.RoleCapacities[role]`, gauge lookup); rescale.go:586 (`satNamed.RoleCapacities[role]`, `roleDemandGPUs`) | The disaggregated-model per-role RC/SC/TotalDemand source of truth. `nil` means non-disaggregated (synthetic "both" role path via `Remaining`/`Spare`). |
| `Live` | analyzer_helpers.go:254 (`safeRemovalReplicasForRole`), analyzer_helpers.go:308 (`needsScaleDownForRole`) | Both are scale-down-only gates; `Live` is never read on the scale-up path. |

### A.4 What the optimizer helpers actually compute, and whether the inputs are on `NamedAnalyzerResult` or external

All of the "genuinely cross-analyzer" helpers (per the prior call-map, Sec3) read fields that
already exist on `NamedAnalyzerResult` -- no helper reads a field that is missing from the
struct. The struct is a superset, not a subset, of what the math needs:

1. `roleBottleneckReplicas` (analyzer_helpers.go:186-202): `max_i ceil(state[i][role] / PRC_i[v])`.
   `state[i][role]` comes from `pickerState`, itself derived from `RoleCapacities`/`Remaining`
   (both present). `PRC_i[v]` comes from `prcForVariant(e.Result, v)` -- `Result.VariantCapacities`
   (present).
2. `safeRemovalReplicasForRole` (analyzer_helpers.go:250-274): `min_i floor(RoleSpare[role]_i /
   PRC_i[v])` over live analyzers. Reads `Live`, `RoleSpare`, `Result` (all present).
3. `needsScaleDownForRole` (analyzer_helpers.go:305-317): all-live-agree veto over `Live`,
   `Result`, `RoleSpare` (all present).
4. `fairShareValue` (greedy_score_optimizer.go:62-94): `priority x sum_i Score_i x sum_role
   pickerState[i][role]`. Reads `Score`, `Result` (nil-gate), and `pickerState` (from
   `RoleCapacities`/`Remaining`) -- all present.
5. `sortVariantsForScaleDown`'s `weighted` closure (cost_aware_optimizer.go:161-171): `sum_i
   Score_i*PRC_i[v]`. Reads `Score`, `Result` (all present).

Where the helpers get their OTHER inputs -- the ones that are NOT on `NamedAnalyzerResult` and
come from elsewhere on `ModelScalingRequest`, and are unaffected by what compose produces:

- Variant identity (`Cost`, `AcceleratorName`, `GPUsPerReplica`, `MinReplicas`, `MaxReplicas`,
  `Role` canonical value) comes from `req.Variants` (`domain.VariantMetadata`), joined in
  `buildVariantRecords` (variant_records.go:52-70) into `variantRecord`, NOT from
  `NamedAnalyzerResult`/`AnalyzerResult` at all. `domain.VariantCapacity` carries no authoritative
  cost/accelerator (per its own doc comment, domain/analyzer.go:134-137: "Cost and accelerator are
  discovery's ... an analyzer has no business restating them"). This is a genuine, intentional
  gap between the analyzer-result type and what the optimizer consumes -- but it is not a compose
  gap, since `req.Variants` is untouched by composition (it is populated once by discovery,
  independent of how many analyzers ran).
- Current/min/max replica state (`CurrentReplicas`, `MinReplicas`, `MaxReplicas`,
  `GPUsPerReplica`) comes from `req.VariantStates` (`domain.VariantReplicaState`) via
  `buildStateMap` (cost_aware_optimizer.go:197-203), read by `scaleDownVariantSet` (minReplicas
  floor), `costGreedyRolePick`/`fairShareRolePick` (maxReplicas headroom, GPUsPerReplica), etc. --
  again external to `NamedAnalyzerResult`, and again untouched by composition.
- `Priority` (`fairShareValue`'s `priority` term) comes from `req.Priority`
  (`ModelScalingRequest.Priority`), not from any `NamedAnalyzerResult` field.
- GPU budget/constraints (`available`, `availableByNS`) come from `ResourceConstraints`, an
  entirely separate parameter to `Optimize`, not derived from analyzer results at all.

Conclusion for Task A: `NamedAnalyzerResult` is a strict superset of what the optimizer's
cross-analyzer math needs -- every field the helpers actually touch is present, but four fields
(`ScaleUpThreshold`, `ScaleDownBoundary`, `TotalSupply`, `TotalAnticipatedSupply`) and the
model-level `Utilization` are never read by any consumer in `internal/engines/allocation/` --
they exist solely to feed one `logAnalyzerResult` INFO line (engine_v2.go:999-1045) and, in the
case of `TotalSupply`/`TotalAnticipatedSupply`, as producer-internal scratch space consumed by
`applyUniversalThreshold` before the entry is even handed to the optimizer. `Result.RoleDemand` is
likewise dead from the optimizer's point of view -- the producer (`buildRoleCapacities`) fully
consumes it into `RoleCapacities` before the optimizer ever sees the entry. A compose step does
not need to reproduce these five dead fields with cross-analyzer-correct semantics (e.g. it
could sum, average, or leave `TotalSupply`/`Utilization` at whatever the underlying composed
`AnalyzerResult`/thresholds imply) without affecting any optimizer decision -- only the log line's
content would change. There is no gap in the other direction: no helper needs a field that is
absent from `NamedAnalyzerResult` and not already sourced from `req.Variants`/`req.VariantStates`/
`req.Priority`/`ResourceConstraints`, and none of those latter sources are analyzer-result-derived,
so compose does not need to touch them either.

---

## Task B -- `saturationNamedEntry` as the shape of the future composite entry

### B.1 Full definition

`internal/engines/allocation/analyzer_helpers.go:87-101`:

```go
// saturationNamedEntry returns the saturation analyzer's entry from s, or nil if
// not present.
//
// Saturation is no longer the keeper of per-variant metadata -- the optimizer gets
// identity from discovery via buildVariantRecords. What is still special about
// this entry is that its P is the one that sizes replicas, which is the
// coordination math and deliberately unchanged here.
func saturationNamedEntry(s []NamedAnalyzerResult) *NamedAnalyzerResult {
	for i := range s {
		if s[i].Name == domain.SaturationAnalyzerName {
			return &s[i]
		}
	}
	return nil
}
```

It is a linear scan by `.Name`, returning a pointer into the slice (so mutations through it alias
the slice element) or `nil`. Given the confirmed producer behavior (`composeAnalyzerResults`,
engine_v2.go:207-214, always resolves to the entry named `domain.SaturationAnalyzerName` when
present, and `runAnalyzersAndScore` wraps exactly that one composed result in a length-1 literal,
engine_v2.go:174-177), in production today this always returns a non-nil pointer to the sole
element, `&s[0]`.

Is its return value structurally/semantically identical to a first-class "the one composite
entry, always present" concept? Yes, with one caveat: today's value is a `*NamedAnalyzerResult`
found by a linear name-search over a length-1 slice, so structurally it already IS "the one
entry" -- the search is vestigial, not doing real disambiguation work in production. The only
semantic difference between it and a hypothetical guaranteed field is the nilability: every
call site must either nil-check the result (correctly, in most cases) or rely on an
un-enforced invariant that it is non-nil (incorrectly, in one case -- see B.3). A guaranteed
`Composite NamedAnalyzerResult` field (value, not pointer) would remove the nilability question
entirely for the "is it present" axis, while `Result` (the embedded `*domain.AnalyzerResult`)
would still need its own nil-check (compose could still fail to produce underlying analyzer data,
e.g. if every analyzer errored).

### B.2 Every call site, in full surrounding context

1. `variant_records.go:78-84` -- `recordsForRequest` (the common entry point both optimizers
call first):

```go
func recordsForRequest(req ModelScalingRequest) []variantRecord {
	nr := saturationNamedEntry(req.AnalyzerResults)
	if nr == nil || nr.Result == nil {
		return nil
	}
	return buildVariantRecords(req, nr.Result)
}
```

Correctly nil-checked (`nr == nil || nr.Result == nil`). Callers: `CostAwareOptimizer.Optimize`
(cost_aware_optimizer.go:48-51, `records := recordsForRequest(req); if records == nil { continue
}`), `GreedyByScoreOptimizer.Optimize` (greedy_score_optimizer.go:126-129, same pattern),
`applyRescale` (rescale.go:226-229, same pattern), `modelCurrentGPUs` (rescale.go:504-507, same
pattern). Minimal diff if `ModelScalingRequest.Composite NamedAnalyzerResult` (value type)
replaced the search: `nr := saturationNamedEntry(req.AnalyzerResults)` becomes `nr := req.Composite`
(now a value, not a pointer); the `nr == nil` half of the check disappears (a value can't be nil),
leaving only `if nr.Result == nil { return nil }`. `buildVariantRecords(req, nr.Result)` is
unchanged.

2. `cost_aware_optimizer.go:243-254` -- `buildDecisionsWithOptimizer`:

```go
func buildDecisionsWithOptimizer(
	req ModelScalingRequest,
	stateMap map[string]domain.VariantReplicaState,
	vcMap map[string]variantRecord,
	targets map[string]int,
	optimizerName string,
) []domain.VariantDecision {
	decisions := make([]domain.VariantDecision, 0, len(targets))
	satNamed := saturationNamedEntry(req.AnalyzerResults)
	for name, target := range targets {
		...
		if satNamed != nil {
			reqCap, spareCap := satNamed.RequiredCapacity, satNamed.SpareCapacity
			role := state.Role
			if role == "" {
				role = domain.RoleBoth
			}
			if rc, ok := satNamed.RoleCapacities[role]; ok {
				reqCap, spareCap = rc.RequiredCapacity, rc.SpareCapacity
			}
			decision.RequiredCapacity = reqCap
			decision.SpareCapacity = spareCap
		}
		...
	}
	return decisions
}
```

Correctly nil-checked (`if satNamed != nil`), degrading to zero-valued gauge fields if absent.
Call sites of `buildDecisionsWithOptimizer` itself: cost_aware_optimizer.go:68,
greedy_score_optimizer.go:151, greedy_score_optimizer.go:174, rescale.go:386. Minimal diff:
`satNamed := saturationNamedEntry(req.AnalyzerResults)` becomes `satNamed := req.Composite` (value);
`if satNamed != nil { ... }` becomes the body running unconditionally (a value is always "present");
an inner `if satNamed.Result == nil` guard would be needed only if `reqCap`/`spareCap` must default
to zero when analysis itself failed -- today the outer nil-check silently covers that too, since a
nil pointer and a `Result == nil` value both currently zero out the gauge fields.

3. `rescale.go:333-345` -- `rescaleModelDecisions` (the one call site with NO nil check today):

```go
func (o *GreedyByScoreOptimizer) rescaleModelDecisions(
	ctx context.Context,
	req ModelScalingRequest,
	constraints []*ResourceConstraints,
	accType string,
	targetGPUs int,
	freeThisCycle *int,
) []domain.VariantDecision {
	satNamed := saturationNamedEntry(req.AnalyzerResults)
	records := buildVariantRecords(req, satNamed.Result)
	...
```

No nil check on `satNamed` itself -- `satNamed.Result` is dereferenced unconditionally on the
very next line. This is safe today only because the sole caller, `applyRescale`
(rescale.go:308, inside the `for _, req := range reqs` loop at line 307), restricts `reqs` to
requests that already passed `recordsForRequest(req) != nil` at rescale.go:226-229 -- an implicit
cross-function invariant, not a local guard. Minimal diff: `satNamed := req.Composite` removes
the theoretical nil-pointer-deref risk entirely (a value can't be nil), but does NOT remove the
need to eventually check `satNamed.Result == nil` if the upstream filtering invariant is ever
weakened -- the diff here is strictly safer than today, not a new source of risk.

4. `rescale.go:519-568` -- `rescaleInputsForGroup`:

```go
func rescaleInputsForGroup(reqs []ModelScalingRequest, accType string, budget int) ([]rescaleInput, int) {
	inputs := make([]rescaleInput, 0, len(reqs))
	sumDemandGPUs := 0
	for _, req := range reqs {
		satNamed := saturationNamedEntry(req.AnalyzerResults)
		if satNamed == nil || satNamed.Result == nil {
			continue
		}
		records := buildVariantRecords(req, satNamed.Result)
		...
		inputs = append(inputs, rescaleInput{
			ID:        modelKey(req),
			Priority:  req.Priority,
			Demand:    satNamed.Result.TotalDemand,
			FloorGPUs: floorGPUs,
			CapGPUs:   capGPUs,
		})
		sumDemandGPUs += demandGPUs
	}
	return inputs, sumDemandGPUs
}
```

Correctly nil-checked (`if satNamed == nil || satNamed.Result == nil { continue }`). Minimal
diff: `satNamed := req.Composite`; `if satNamed == nil || satNamed.Result == nil` becomes
`if satNamed.Result == nil` (drop the now-impossible nil-value half); `continue` skips the model
from this rescale group exactly as before.

5. `rescale.go:572-578` -- `modelDemandGPUs` (receives an already-resolved pointer, not a new
search):

```go
func modelDemandGPUs(satNamed *NamedAnalyzerResult, records []variantRecord, stateMap map[string]domain.VariantReplicaState, accType string) int {
	total := 0
	for _, role := range modelRolesOnType(records, accType) {
		total += roleDemandGPUs(satNamed, records, stateMap, accType, role)
	}
	return total
}
```

Not itself a `saturationNamedEntry` call site -- it's a downstream consumer of the pointer
`rescaleInputsForGroup` and `rescaleModelDecisions` already resolved. Called from
rescale.go:549 (inside `rescaleInputsForGroup`, after that function's own nil check) and
rescale.go:359 (inside `rescaleModelDecisions`, unchecked per #3 above). Minimal diff: signature
changes from `satNamed *NamedAnalyzerResult` to `satNamed NamedAnalyzerResult` (value) if
`req.Composite` is threaded through as a value from the two call sites; body unchanged, since it
only ever reads through the pointer, never reassigns it.

6. `rescale.go:580-608` -- `roleDemandGPUs` (same shape as #5):

```go
func roleDemandGPUs(satNamed *NamedAnalyzerResult, records []variantRecord, stateMap map[string]domain.VariantReplicaState, accType, role string) int {
	demand := satNamed.Result.TotalDemand
	if role != domain.RoleBoth {
		if rc, ok := satNamed.RoleCapacities[role]; ok {
			demand = rc.TotalDemand
		}
	}
	...
}
```

No nil check on `satNamed` or `satNamed.Result` -- relies on the same caller-side invariant as #3.
Called from rescale.go:575 (`modelDemandGPUs`, forwarding its own already-checked-or-unchecked
pointer) and rescale.go:359 (`rescaleModelDecisions`, unchecked). Minimal diff: same signature
change as #5 (`*NamedAnalyzerResult` to `NamedAnalyzerResult` value); body unchanged.

### B.3 Summary table -- call sites and minimal diffs

| Call site | Nil-checked today? | Diff if `req.Composite NamedAnalyzerResult` (value) replaced the search |
|---|---|---|
| `variant_records.go:79` (`recordsForRequest`) | Yes (`nr == nil OR nr.Result == nil`) | Drop `nr == nil` half; `nr := req.Composite` |
| `cost_aware_optimizer.go:254` (`buildDecisionsWithOptimizer`) | Yes (`if satNamed != nil`) | Drop the `!= nil` wrapper; body runs unconditionally (or keep an inner `Result == nil` check if zero-gauge-on-failure must be preserved) |
| `rescale.go:344` (`rescaleModelDecisions`) | No -- relies on caller invariant | `satNamed := req.Composite`; removes the possibility of a nil-pointer panic (was already safe in practice, now safe by construction) |
| `rescale.go:525` (`rescaleInputsForGroup`) | Yes (`satNamed == nil OR satNamed.Result == nil`) | Drop the `== nil` half of the OR |
| `rescale.go:572` (`modelDemandGPUs`, receives resolved pointer) | N/A (not a search site) | Parameter type `*NamedAnalyzerResult` becomes `NamedAnalyzerResult` |
| `rescale.go:583` (`roleDemandGPUs`, receives resolved pointer) | N/A (not a search site) | Same parameter-type change |

Net finding for Task B: `saturationNamedEntry`'s only real job today is resolving nilability
(is there an entry named saturation at all) over a slice that, by construction of
`composeAnalyzerResults`, is already guaranteed length-1-and-named-saturation. Every call site
either already null-checks correctly (4 of 6) or leans on an un-enforced but currently-true
cross-function invariant (`rescale.go:344`, `rescale.go:583` via `rescale.go:359`). Replacing the
search with a guaranteed value-typed field would not change any allocation math -- it would only
collapse each `nr == nil` (or `satNamed == nil`) check into a no-op deletion, and would convert
`rescale.go:344`'s currently-implicit safety guarantee into a structurally-enforced one.

---

## Task C -- Are per-role/per-variant lists assumed to be "dense"?

### C.1 The dynamic, per-variant/per-role fields

Exactly four fields qualify (verified against the struct definitions in Task A.1):

1. `domain.AnalyzerResult.VariantCapacities []VariantCapacity` -- per-variant, keyed by
   `VariantName` (not an actual map; consumers linear-scan or build their own map).
2. `domain.AnalyzerResult.RoleDemand map[string]float64` -- per-role. Not read anywhere in
   `internal/engines/allocation/*.go` (Task A.3) -- fully consumed by the producer
   (`buildRoleCapacities`) before the optimizer sees the entry, so its sparseness is entirely the
   producer's concern, not the optimizer's. Not analyzed further here since it has no optimizer-
   side reader.
3. `NamedAnalyzerResult.RoleCapacities map[string]domain.RoleCapacity` -- per-role, engine-built.
4. `NamedAnalyzerResult.RoleSpare map[string]float64` -- per-role, optimizer-built (lazily, inside
   `initRoleState`).

(`req.Variants []domain.VariantMetadata` and `req.VariantStates []domain.VariantReplicaState` are
also per-variant lists, but they are NOT part of `NamedAnalyzerResult`/`domain.AnalyzerResult` --
they come from discovery, per Task A.4 -- so they are out of this task's scope by the task's own
framing, though they matter as the "dense" reference set every other list is checked against.)

### C.2 `VariantCapacities` -- every reader, sparse-safe or not

`prcForVariant` (analyzer_helpers.go:105-112):

```go
func prcForVariant(r *domain.AnalyzerResult, v string) float64 {
	for _, vc := range r.VariantCapacities {
		if vc.VariantName == v {
			return vc.PerReplicaCapacity
		}
	}
	return 0
}
```

Sparse-safe by construction: it is a lookup-by-name into a caller-supplied variant name `v`
(sourced from `variantRecord`/`req.Variants`, the dense list), not a range over
`VariantCapacities` that assumes every entry exists. A variant absent from `VariantCapacities`
returns `0`, which every caller (`applyAllocation:76-79`, `roleBottleneckReplicas:192-195`,
`safeRemovalReplicasForRole:260-263`, `applyDeallocationForRole:287-290`, `sortVariantsForScaleDown`'s
`weighted` closure cost_aware_optimizer.go:168) treats as "cannot size a replica off this variant
via this analyzer entry" and skips (`if prc <= 0 { continue }` at each of the four helper call
sites; the `weighted` closure just adds 0, correctly a no-op for the ranking it produces).

`buildVariantRecords` (variant_records.go:52-70) -- the single place that reconciles the dense
`req.Variants` list against the potentially-sparse `VariantCapacities`:

```go
func buildVariantRecords(req ModelScalingRequest, satResult *domain.AnalyzerResult) []variantRecord {
	if satResult == nil || len(req.Variants) == 0 {
		return nil
	}
	capByVariant := make(map[string]domain.VariantCapacity, len(satResult.VariantCapacities))
	for _, vc := range satResult.VariantCapacities {
		capByVariant[vc.VariantName] = vc
	}
	out := make([]variantRecord, 0, len(req.Variants))
	for _, m := range req.Variants {
		vc := capByVariant[m.VariantName]
		out = append(out, variantRecord{
			VariantMetadata:    m,
			PerReplicaCapacity: vc.PerReplicaCapacity,
			Utilization:        vc.Utilization,
		})
	}
	return out
}
```

Explicitly, deliberately sparse-safe: the outer loop ranges over `req.Variants` (the dense,
discovery-sourced set -- "one record per discovered variant, in discovery order", per its own doc
comment lines 40-41), and looks up into `capByVariant[m.VariantName]`, a Go map lookup that
defaults to the zero-value `domain.VariantCapacity{}` when the variant is absent from
`satResult.VariantCapacities`. A variant the analyzer never sized therefore gets
`PerReplicaCapacity: 0, Utilization: 0`, which the doc comment (lines 42-45) states is
intentional: "the optimizer sees the whole fleet and skips what it cannot size -- the same outcome
as the variant being absent, but without the optimizer's view of the model silently depending on
which variants an analyzer happened to emit." Every downstream consumer of `variantRecord` (the
cost-efficiency sort, `costGreedyRolePick`, `fairShareRolePick`, `scaleDownVariantSet`, etc.) already
has a `PerReplicaCapacity <= 0` skip-guard (e.g. cost_aware_optimizer.go:91,121;
greedy_score_optimizer.go:421), so this degrades correctly.

Answer for `VariantCapacities`: a sparse analyzer result (missing a variant `req.Variants`
knows about) is handled correctly everywhere -- it degrades to "treat this variant as having zero
capacity via this analyzer's signal, skip it for sizing," which is the intended fallback per the
type's own documentation, NOT "treat missing as zero demand when it should have been unknown/keep
current." (There is no demand semantics at the per-variant level at all -- `VariantCapacities`
carries supply/capacity, not demand; demand is model- or role-scoped on `AnalyzerResult`/
`RoleCapacities`.)

### C.3 `RoleCapacities` -- every reader, sparse-safe or not

`initRoleState` (analyzer_helpers.go:131-167), the sole place that reads `RoleCapacities`
directly:

```go
func initRoleState(s []NamedAnalyzerResult) (roles []string, pickerState RolePairedState) {
	pickerState = make(RolePairedState, len(s))
	roleSet := make(map[string]struct{})

	for i, e := range s {
		pickerState[i] = make(map[string]float64)
		if e.Result == nil {
			continue
		}
		if e.RoleCapacities != nil {
			if s[i].RoleSpare == nil {
				s[i].RoleSpare = make(map[string]float64, len(e.RoleCapacities))
			}
			for role, rc := range e.RoleCapacities {
				pickerState[i][role] = rc.RequiredCapacity
				s[i].RoleSpare[role] = rc.SpareCapacity
				roleSet[role] = struct{}{}
			}
		} else {
			pickerState[i][domain.RoleBoth] = e.Remaining
			if s[i].RoleSpare == nil {
				s[i].RoleSpare = make(map[string]float64, 1)
			}
			s[i].RoleSpare[domain.RoleBoth] = e.Spare
			roleSet[domain.RoleBoth] = struct{}{}
		}
	}

	roles = make([]string, 0, len(roleSet))
	for role := range roleSet {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles, pickerState
}
```

This is a "roles come FROM the map, not looked up INTO it" pattern -- `roles` (the function's
own return value, later used by every other role-generic helper) is built by ranging over
`RoleCapacities`'s own keys (`for role, rc := range e.RoleCapacities`), not by iterating some
independent "known roles" set and looking up into `RoleCapacities`. There is no "dense" role set
to be sparse against here -- `RoleCapacities`'s keys ARE the authoritative role set for the rest
of the allocation pass. A role that exists among `req.Variants`/`req.VariantStates` (e.g. via
`VariantMetadata.Role`) but is absent from `RoleCapacities` would simply never appear in `roles`,
and every role-generic helper downstream (`roleBottleneckReplicas`, `roleAggRemaining`,
`safeRemovalReplicasForRole`, `needsScaleDownForRole`, `allocateForModelPaired`,
`scaleDownRoleIterated`) only ever iterates `roles` (this function's derived list) or the
variant-level `variantsForRole`/`rolesOf` (analyzer_helpers.go:18-28, 230-242), which derive roles
from `variantRecord.Role`, a separate, independently-dense source (discovery metadata, not the
analyzer result).

This is the one genuine cross-domain sparseness risk found: if an analyzer's `RoleDemand` (and
therefore the `RoleCapacities` the producer builds from it) omits a role that a variant in
`req.Variants`/`req.VariantStates` actually has (e.g. a "decode" variant exists per discovery, but
the analyzer never attributed any demand to "decode"), that role is silently absent from
`roles` in `initRoleState`'s result -- not "present with zero demand," but genuinely absent from
the iteration set. Concretely: `scaleDownRoleIterated` (cost_aware_optimizer.go:427-461) computes
`roles := rolesOf(variants)` (line 441) -- the VARIANT-derived dense role set, decoupled from
`RoleCapacities` -- so scale-down does independently discover a "decode" role exists via discovery
metadata even if the analyzer never demand-attributed it; `needsScaleDownForRole(s, role)` (line
444) then reads `e.RoleSpare[role]` for that role, which (per `initRoleState`) would only have been
populated if `RoleCapacities` also had that key. If `RoleCapacities` never had "decode",
`RoleSpare["decode"]` is nil/absent, so `e.RoleSpare[role] <= 0` is true (Go zero-value for a
missing map key is `0.0`), so `needsScaleDownForRole` returns `false` for that role -- scale-down is
correctly blocked (safe direction: never scale down a role with no analyzer-attested spare), not
silently treated as "infinite spare." Conversely, on the scale-up side, `allocateForModelPaired`
is only ever called with `roles` as returned by `initRoleState` itself (never `rolesOf(variants)`)
-- see `cost_aware_optimizer.go:60-63` (`roles, ps := initRoleState(s); if
anyRoleNeedsScaleUp(ps, roles) { allocateForModelPaired(ctx, s, records, ..., roles) }`) and
`greedy_score_optimizer.go:132-141` (identical shape) -- so a role absent from `RoleCapacities`
never enters the scale-up loop at all: a variant that exists in discovery but whose role the
analyzer never attributed demand to can be scaled up by no path in the optimizer, silently,
every cycle, until the analyzer starts attributing demand to that role. This is the sharpest
concrete "missing role leads to wrong/no answer" case Task C asked to surface: it is not a crash and
not an out-of-bounds read (Go maps make it impossible to panic on a missing key), but it IS a
silent-omission failure mode -- a role with real variants and real (unattributed) demand is
invisible to scale-up until the analyzer catches up, and the compose step inherits this exact
contract: whatever roles the composite `AnalyzerResult.RoleDemand`/`RoleCapacities` does NOT cover
are roles the optimizer will never scale up, no matter how much discovery-side "decode" capacity
exists.

`cost_aware_optimizer.go:312-323` (`buildDecisionsWithOptimizer`) reads `RoleCapacities`
sparse-safely via a two-value map lookup with an explicit `ok`:

```go
if rc, ok := satNamed.RoleCapacities[role]; ok {
	reqCap, spareCap = rc.RequiredCapacity, rc.SpareCapacity
}
```

Falls back to the model-level `satNamed.RequiredCapacity`/`SpareCapacity` (set just above,
line 313) when the role key is absent -- correct, gauge-only, no allocation-math consequence.

`rescale.go:586` (`roleDemandGPUs`) -- same `ok`-checked pattern:

```go
demand := satNamed.Result.TotalDemand
if role != domain.RoleBoth {
	if rc, ok := satNamed.RoleCapacities[role]; ok {
		demand = rc.TotalDemand
	}
}
```

Falls back to the model-level `TotalDemand` when the specific role is absent from
`RoleCapacities` -- this is a real semantic fallback (whole-model demand substituted for a
role's own, potentially very different, demand), but it is explicit and `ok`-guarded, not a
silent zero.

### C.4 `RoleSpare` -- every reader, sparse-safe or not

`RoleSpare` is always populated by `initRoleState` before any other function reads it (both
optimizers call `initRoleState(s)` before `anyRoleNeedsScaleUp`/`allocateForModelPaired`/
`scaleDownRoleIterated` -- cost_aware_optimizer.go:60, greedy_score_optimizer.go:132,171,285,348),
so by the time `safeRemovalReplicasForRole`, `applyDeallocationForRole`, or
`needsScaleDownForRole` run, `RoleSpare` is non-nil for every entry with `Result != nil`, keyed by
exactly the roles `RoleCapacities` had (or the single synthetic `domain.RoleBoth` key for
non-disaggregated entries). All three readers do a direct map index (not a range) --
`e.RoleSpare[role]` (safeRemovalReplicasForRole:264), `s[i].RoleSpare[role]`
(applyDeallocationForRole:291), `e.RoleSpare[role]` (needsScaleDownForRole:311) -- where `role`
comes from the caller's own `roles` iteration (`rolesOf(variants)` in `scaleDownRoleIterated`, the
dense variant-derived set). A `role` present in `rolesOf(variants)` but absent from `RoleSpare`'s
keys (the exact same cross-domain gap as C.3) reads as Go's map zero-value `0.0`, which
`needsScaleDownForRole`'s `e.RoleSpare[role] <= 0` check correctly treats as "no spare, don't
scale down" -- same safe-direction degradation as C.3, not a crash, not a wrong-direction answer.

### C.5 Summary -- dense-assumption audit

| Field | Range-over-map-keys or lookup-into-map? | Sparse-safe? | Failure mode if sparse |
|---|---|---|---|
| `VariantCapacities` | Lookup-into (via `prcForVariant`'s linear scan, or `buildVariantRecords`'s `map[string]VariantCapacity` with zero-value default) -- the DENSE side is `req.Variants`/`variantRecord`, always iterated first | Yes | Missing variant leads to `PerReplicaCapacity=0`, treated as "unsizable," skipped everywhere; matches the type's own documented intent. |
| `Result.RoleDemand` | N/A -- no optimizer-side reader | N/A | Not applicable; fully consumed by the producer before the optimizer sees the entry. |
| `RoleCapacities` | Range-over in `initRoleState` (defines `roles` from its own keys, not from a dense external role set); lookup-into with `ok`-guard in `buildDecisionsWithOptimizer`/`roleDemandGPUs` | Yes for the lookup-into sites (explicit `ok` fallback); structurally silent-omission for the range-over site -- a role known to discovery but absent from `RoleCapacities` never enters `roles`, so it is invisible to `allocateForModelPaired` (scale-up) for as long as the analyzer under-attributes it | Scale-up: silently never happens for that role (no crash, no wrong-direction scale, just permanent inaction until the analyzer catches up). Scale-down: correctly blocked (safe direction) since `RoleSpare[role]` reads as 0. |
| `RoleSpare` | Lookup-into with direct map index (no `ok`, but on a `roles` set the caller controls) | Yes in practice -- a missing key reads as Go's `0.0` zero value, and every reader's own threshold check (`<= 0`) treats that as "no spare," the safe direction | Same as `RoleCapacities`'s range-over gap -- inherited, not independent. |

Conclusion for Task C: nothing in `internal/engines/allocation/*.go` panics or silently
computes a wrong-direction answer (e.g. "treat missing demand as zero when it should be treated as
unknown/keep-current") from a sparse analyzer result -- every consumer either explicitly guards with
`ok`/nil checks or relies on Go's map zero-value defaulting to land on the safe side (skip
sizing, block scale-down). The one real gap is not a bug but a structural blind spot: `roles`
in `initRoleState` is defined by `RoleCapacities`'s own keys, not cross-checked against the
dense variant/role set discovery knows about, so a role the analyzer never attributes demand to is
not merely "handled as zero" -- it is never iterated at all on the scale-up path, which is a
silent no-scale-up outcome for that role rather than a "zero demand, correctly skip" outcome (the
two are behaviorally identical when demand really is zero, but diverge if the analyzer is simply
missing that role's data). This is exactly the contract the compose step inherits: whatever
roles the composite `AnalyzerResult`/`RoleCapacities` omits are roles no scale-up path will ever
reach, regardless of how compose derives its role set from multiple underlying analyzers.
