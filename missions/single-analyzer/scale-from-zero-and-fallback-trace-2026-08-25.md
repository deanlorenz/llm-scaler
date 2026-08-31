# Scale-from-zero and unseen/zero-replica fallback trace (2026-08-25)

> **Errata (2026-08-30):** The headline conclusion in Section 3 ("no fallback mechanism exists
> for zero-replica roles") was incorrect. A purpose-built demand estimator,
> `estimateSchedulerQueueDemand` (`internal/engines/analyzers/saturation_v2/analyzer.go`),
> already covers the ordinary cold-start case by producing a real nonzero `RequiredCapacity`
> from EPP queue-depth signals. The remaining gap is narrower: (a) no EPP queue signal
> available, and (b) discovery-side role omission. See
> `spec-composite-metric-and-optimizer-t2.md` §CT5 for the corrected, full analysis.
> The body of this report is preserved unchanged as a primary-source record.

Scope and method: every claim below was verified by reading the cited file directly on the
current worktree HEAD, in the same pass as this document's authoring, not copied from the prior
document (docs/plans/analyzers/composite-entry-spec-2026-08-25.md, Section C), which was read
first for context and is treated as unverified narrative until re-derived here.

Bottom line up front (expanded on in Section 3): there are two entirely separate "scale from
zero" mechanisms in this repo, and only one of them is relevant to the prior analysis's gap.
internal/engines/scalefromzero wakes a model with literally zero running replicas of any
variant, and it runs completely outside the analyzer/optimizer pipeline -- it never touches
AnalyzerResult, RoleDemand, or RoleCapacities. The saturation V2 analyzer's
CapacityKnowledgeStore is the mechanism that actually intersects the prior analysis's gap: it
lets SaturationAnalyzer.Analyze emit a non-zero PerReplicaCapacity (and hence non-zero
supply/demand) for a variant with zero ready replicas, sourced from history or a compatible
sibling. Critically, it does so by construction over the dense VariantReplicaState list, which
means it also resolves the prior analysis's specific gap for the case where the resolving
mechanism actually engages (see Section 3) -- but the resolution is incidental to a different
design goal (estimating supply for zero-replica variants), not a purpose-built fix for
role-visibility, and it does not cover the "analyzer skipped a variant/role outright" case.

---

## 1. Scale-from-zero / cold-start code: what triggers it, what it does, zero-replica handling

### 1.1 Package identification

Searching for coldstart / scale-from-zero / scale-to-zero across all *.go files surfaces one
whole package by name, internal/engines/scalefromzero/ (engine.go, model_group.go,
candidates.go, selection.go, queue_fallback.go), plus a scale-to-zero counterpart in
internal/config/scale_to_zero.go, internal/collector/registration/scale_to_zero.go, and
enforcement gates in internal/engines/steadystate/engine_scale_to_zero_gate_test.go and
engine_scale_to_zero_enforce_test.go. No "ColdStart"-named type exists; the term appears only in
comments/tests (e.g. test/e2e/scale_from_zero_test.go) describing this same engine. There is no
"wake" logic outside this package -- the rest of the repo does not use the term for engine logic.

### 1.2 Trigger

Engine.optimize (internal/engines/scalefromzero/engine.go:252-351) runs on a 100ms polling
loop (engine.go:238, executor.NewPollingExecutor, Interval: 100 * time.Millisecond), entirely
separate from the steady-state/optimizer cycle. Each tick:

1. utils.InactiveVariantAutoscaling (engine.go:259) lists VariantAutoscaling resources with
   replicas == 0 -- the actual "zero replicas" gate for this whole engine.
2. Groups them by model (groupInactiveByModel, engine.go:282).
3. Per model group, processInactiveModel (engine.go:371-534) checks the EPP's flow-control
   queue-depth metric for that model (pendingRequestsForModel, engine.go:82-99, reading
   llm_d_epp_flow_control_queue_size / the deprecated inference_extension_ alias, engine.go:70-73).
4. If pending requests exist (or a P/D "catch-up" condition holds -- catchUpWanted,
   engine.go:440: decode is up, prefill is not, and a prefill candidate exists), it calls
   selectServingSet (selection.go) to pick which inactive variant(s) to wake, gated by GPU
   budget (e.gpuConstraints, engine.go:466).
5. publishActivation (engine.go:545-665) writes the wake decision to decision.Set (the
   shared decision cache KEDA's external scaler reads) and to common.DecisionCache -- it never
   constructs a domain.AnalyzerResult, never touches RoleCapacities/RoleDemand, and is not
   invoked by, or a caller of, internal/engines/steadystate or internal/engines/allocation at
   all.

### 1.3 What it does with zero-replica variants -- estimating demand for wake decisions

This engine never estimates a numeric demand/capacity value for the zero-replica variant at all --
its entire signal is binary ("does the EPP queue for this model have anything waiting", plus,
for the P/D catch-up path, "is decode up and prefill not"). candidates.go/selection.go pick
which variant(s) to wake (by cost, role coverage, GPU fit) but do not compute a PerReplicaCapacity
or a token-level demand number for the woken variant -- that is entirely the saturation
analyzer's job, once the variant has live replicas reporting metrics on a subsequent steady-state
cycle. So: for the "how much capacity does a zero-replica variant have" question, this engine has
no answer and does not attempt one; it only answers "should something be woken at all."

### 1.4 Relationship to the steady-state engine

internal/engines/steadystate/engine_scale_to_zero_gate_test.go and
engine_scale_to_zero_enforce_test.go show the steady-state engine has scale-to-zero gating
(deciding whether it's safe to let a model's replicas go to 0), which is the inverse operation and
also outside the analyzer/AnalyzerResult contract -- it gates on idle-time/retention policy
(internal/config/scale_to_zero.go), not on analyzer-attributed demand.

Conclusion for Section 1: the scale-from-zero engine is a parallel, EPP-queue-driven wake
mechanism that runs before any analyzer ever sees the model (a model with 0 replicas has no
ReplicaMetrics and is not even in the steady-state engine's per-cycle input set in the same way).
It does not synthesize an AnalyzerResult/RoleDemand/RoleCapacities entry, so it cannot be the
mechanism that closes the prior analysis's "role invisible to scale-up" gap -- that gap is
specifically about a role with a registered, non-zero-replica-eligible variant whose demand the
analyzer under-attributes, which is a saturation-analyzer-pipeline question, not a
wake-from-absolute-zero question.

---

## 2. "Previously seen" vs "never seen" variant fallback: the CapacityKnowledgeStore

### 2.1 What the store holds

internal/engines/analyzers/saturation_v2/capacity_store.go. CapacityRecord
(lines 17-27) holds, per variant: AcceleratorName, GpuCount, NumGpuBlocks, BlockSize,
TotalKvCapacityTokens, EffectiveCapacity, EngineParams (parsed deployment args),
LearnedFrom ("live", "deployment", or "annotation"), LearnedAt. Its own doc comment
(lines 13-16) states its purpose explicitly: "This allows the analyzer to make capacity estimates
for variants that currently have zero replicas, either from their own prior data or from a
compatible variant via FindCompatible."

CapacityKnowledgeStore (lines 36-39) is a sync.RWMutex-guarded map[string]*CapacityRecord
keyed by "namespace|modelID|variantName" (storeKey, line 51-53). It is instantiated once per
steady-state engine (internal/engines/steadystate/engine.go:212,
saturation_v2.NewCapacityKnowledgeStore()) and shared: passed into
saturation_v2.NewSaturationAnalyzer(capacityStore) (engine.go:213) as the analyzer's only
constructor argument, so it persists across analysis cycles for the life of the process (it is not
per-cycle, per-request state).

Three write paths:
- Update (capacity_store.go:57-62) -- unconditional overwrite, called only with
  LearnedFrom: learnedFromLive from computeReplicaCapacity
  (analyzer.go:189-198, every cycle a variant has live ReplicaMetrics). Doc comment: "Live data
  is always authoritative and should always be written via Update."
- LoadFromScaleTarget (capacity_store.go:88-131) -- called from the steady-state engine BEFORE
  analysis, once per variant per cycle, from engine_v2.go:55:
  e.capacityStore.LoadFromScaleTarget(namespace, modelID, va.Name, accelerator, gpuCount, scaleTarget).
  It parses the scale target's deployment args (ParseEngineArgs) into EngineParams and derives
  a conservative EffectiveCapacity estimate from EffectiveMaxBatchedTokens (lines 122-128:
  "Provide a conservative capacity estimate so that brand-new variants with no live data or
  compatible siblings can still be considered for scale-up"). It explicitly refuses to clobber a
  live record (lines 98-101: if an existing record's LearnedFrom == learnedFromLive, return
  immediately without writing) -- this is the "never overwrite a previously-seen-live value with a
  weaker deployment-derived guess" rule.
- EvictStale (capacity_store.go:137-148) -- time-based GC, not relevant to the fallback question
  except that its doc comment notes a long timeout is used "since historical capacity data is
  valuable for zero-replica estimation."

Read path for cross-variant estimation: FindCompatible (capacity_store.go:158-192) scans every
record for one matching modelID + accelerator + gpuCount + EngineParams.IsCapacityCompatible,
preferring a "live"-learned record over a "deployment"-learned one (lines 185-188). This is the
"never seen this variant, but a sibling variant with the same hardware/engine config was seen"
fallback.

### 2.2 How the analyzer consumes the store to fill gaps -- aggregateByVariant

internal/engines/analyzers/saturation_v2/analyzer.go:341-433, aggregateByVariant. The critical
structural fact: the outer loop is "for _, vs := range variantStates" (line 358) -- i.e. it
iterates input.VariantStates, the dense, discovery-sourced list of every variant the model has,
not the (possibly empty, for a zero-replica variant) set of replicas that reported live metrics
this cycle. This mirrors exactly the "dense list drives the loop, sparse data is looked up into"
pattern the prior analysis found for buildVariantRecords (Task C.2) -- except here it is the
analyzer itself, at the point of producing AnalyzerResult, that guarantees density, not a later
optimizer-side reconciliation.

For each variant vs, per-replica capacity is resolved by a four-branch priority chain
(lines 384-408), reproduced here for exact wording:

  if len(replicas) > 0: use the median of the live replicas' EffectiveCapacity (the observed path).
  else if the capacity store has an own record with EffectiveCapacity > 0: call
    estimateStoredCapacity(...) and label the reason satReasonP0Store ("No ready replicas --
    use stored capacity, enhanced with k2 derivation for deployment-derived records when
    workload data is available").
  else if lookupCompatibleCapacity(...) finds a compatible sibling record: use that record's
    EffectiveCapacity directly, also labeled satReasonP0Store ("No own record -- try
    cross-variant estimation from a compatible variant").
  else: label satReasonNoData and leave perReplicaCapacity at its zero value.

This is precisely the "previously seen vs. never seen" fallback ladder the task asked about:

1. Live this cycle (len(replicas) > 0): median of directly-observed EffectiveCapacity.
2. Previously seen, not live now (own store record exists, EffectiveCapacity > 0):
   estimateStoredCapacity (analyzer.go:496-539) -- if the record's LearnedFrom == learnedFromLive,
   use the stored value directly (lines 508-511: "Live records have observed capacity -- use
   directly"); else (a "deployment"-derived record) try the k2-derivation formula
   (estimateCapacityFromParams) using model-wide workload averages, bounded by the record's own
   k1 and by any compatible live sibling's capacity, falling back to the raw stored
   EffectiveCapacity (EffectiveMaxBatchedTokens) if derivation isn't possible.
3. Never seen at all, but a compatible sibling was (lookupCompatibleCapacity,
   analyzer.go:469-476): cross-variant FindCompatible lookup, using the variant's own
   deployment-derived EngineParams (which must itself have been populated by
   LoadFromScaleTarget -- i.e. this branch requires the variant to at least be
   registered/discovered, not truly unknown to the system) to find a hardware/engine-compatible
   sibling's capacity.
4. Truly nothing (satReasonNoData, equal to allocation.ReasonNoData = "no-data",
   analyzer_helpers.go:43): perReplicaCapacity stays 0.

totalDemand for that variant, in branches 2-4, stays whatever accumulated from replicas (which
is empty in all three, since they're the len(replicas) == 0 branches) -- i.e. exactly 0 in every
non-live branch. So the store only ever synthesizes non-zero supply (PerReplicaCapacity) for a
zero-replica variant, never non-zero demand. totalDemand for that variant is always exactly 0
along this path (no ReplicaMetrics exist to compute a per-replica demand from), which matches
domain.VariantCapacity's documented split (per the prior analysis, Task A.1):
VariantCapacities carries per-variant supply-side signal (PerReplicaCapacity), while model- or
role-scoped demand is a separate aggregate (TotalDemand, RoleDemand) computed from queue and
resident-token signals that a zero-replica variant, by definition, cannot generate on its own.

### 2.3 Confirming this is "supply/capacity", not "demand", regardless of branch

Every branch of aggregateByVariant, including the store/compatible-sibling ones, writes into the
domain.VariantCapacity output's PerReplicaCapacity/Reason fields (lines 420-429). None of the
three fallback branches add anything to totalDemand. Scheduler-queue demand
(estimateSchedulerQueueDemand, analyzer.go:723-767, added at Analyze, line 116-117) is a
model-level (not per-variant) addend to totalDemand, attributed to whichever roles are in
activeRoles (built at Analyze, lines 110-113, from variantCapacities -- i.e. from every role
that has an entry in the dense variantStates loop, zero-replica or not, since activeRoles is
populated unconditionally for every vc in the result, not gated on vc.Reason). This is the one
place demand can reach a zero-replica-only role: if the EPP flow-control queue holds requests
for a model whose only variant of some role currently has zero replicas, estimateSchedulerQueueDemand
attributes some of that queued demand to the role in activeRoles, because activeRoles is built
from variantCapacities's roles (present unconditionally) rather than from replicas actually
reporting metrics.

---

## 3. Does this resolve, partially resolve, or not address the prior analysis's gap?

Restating the gap precisely, per composite-entry-spec-2026-08-25.md Task C.3-C.5 (re-verified
independently against internal/engines/allocation/analyzer_helpers.go:131-167,
initRoleState, and internal/engines/steadystate/engine_v2.go:958-983, buildRoleCapacities,
during this pass): initRoleState's roles set is built by ranging over
RoleCapacities's own map keys ("for role, rc := range e.RoleCapacities",
analyzer_helpers.go:556), and buildRoleCapacities builds RoleCapacities by ranging over
result.RoleDemand's own map keys ("for role, demand := range roleDemand", engine_v2.go:965) --
NOT by ranging over the roles present in VariantCapacities/totals. So the operative question
is: does AnalyzerResult.RoleDemand get a key for a role that has variants with zero
analyzer-attributed demand?

Traced precisely:

- aggregateRoleDemand (analyzer.go:445-462) calls aggregation.IsDisaggregated(variantCapacities)
  (line 449) as a gate: if not disaggregated, it returns nil immediately.
  IsDisaggregated (aggregation.go:92-99) returns true iff any vc.Role in the (dense,
  every-discovered-variant) list is non-empty and non-RoleBoth. This gate does not depend on
  demand being non-zero -- it depends only on role identity being present in the dense
  VariantCapacities list, which (per Section 2) is guaranteed populated for every discovered
  variant regardless of replica count or capacity-store hit.
- When the gate passes, DemandByRole(variantCapacities) (line 453) calls
  AggregateByRole (aggregation.go:113-127), which unconditionally creates a map entry for every
  role seen (line 120: reads the zero-value default for a not-yet-seen role, accumulates into it,
  then unconditionally writes it back at line 124, for every vc regardless of its
  TotalDemand/Reason). A role whose only variant is satReasonNoData (capacity-store branch 4,
  zero PerReplicaCapacity, zero TotalDemand) still produces a map entry with
  TotalDemand: 0 -- the map key exists, with value 0, not absent.
- DemandByRole copies TotalDemand verbatim into its returned map (aggregation.go:78-85), so
  RoleDemand[role] == 0 is returned, not omitted -- as long as some other variant in the same
  AnalyzerResult has a non-"both" role, satisfying IsDisaggregated's gate.
- buildRoleCapacities (engine_v2.go:958-983) then ranges over exactly this RoleDemand map
  (line 965), so RoleCapacities[role] gets created with TotalDemand: 0. TotalSupply for that
  role comes from AggregateByRole's PerReplicaCapacity times ReplicaCount; for a genuinely
  zero-ready-replica variant, ReplicaCount itself is readyCount or len(replicas), both 0, so
  TotalSupply for that role is 0 regardless of what the store estimated for PerReplicaCapacity --
  the store's synthesized per-replica number never gets multiplied by anything, because there are
  no replicas to multiply it by.
- initRoleState (analyzer_helpers.go:543-577) ranges over this RoleCapacities map (line 556),
  so the role is included in roleSet/roles, with pickerState[i][role] = rc.RequiredCapacity
  (line 557) computed from TotalDemand: 0 and TotalAnticipatedSupply: 0 via
  applyUniversalThreshold (RC = max(0, 0/scaleUp - 0) = 0).

Conclusion: the gap the prior analysis found -- "a role with real variants but no
analyzer-attributed demand is invisible to scale-up because initRoleState derives its role set
from RoleCapacities's own keys" -- does NOT manifest for a zero-ready-replica role in a model
that is otherwise disaggregated (i.e. has at least one other variant with a distinct
non-"both" role), specifically because aggregateByVariant's dense variantStates loop
(Section 2.2) guarantees every discovered variant -- including zero-replica ones -- contributes a
VariantCapacity entry with its Role set, which is enough for IsDisaggregated/AggregateByRole
to manufacture a RoleDemand[role] = 0 / RoleCapacities[role] = {TotalDemand: 0, ...} entry
rather than omitting the key. The role is present with RequiredCapacity: 0 (correctly: no
capacity should be requested for zero real demand) -- this is "present with zero demand," not
"genuinely absent from the iteration set," which is exactly the distinction the prior analysis
(Task C.5) drew as mattering.

However, this resolution is partial and incidental, not a purpose-built fix, and does not
cover the case the prior analysis was actually most worried about (an analyzer that
under-attributes or never learns about a role's demand at all, as opposed to one that correctly
computes zero demand for a role with zero live replicas):

1. It operates on a completely different axis than the prior analysis assumed. The
   store/FindCompatible fallback machinery exists to estimate per-replica capacity (supply) for
   variants with no live metrics -- its own doc comments frame it exclusively that way
   (capacity_store.go:13-16,29-32). It has no analogous mechanism for demand: there is no
   "estimate what this zero-replica role would need from history/cross-model data." The reason the
   gap doesn't manifest is a side effect of the dense variantStates loop existing for capacity
   reasons, not because anyone built a demand-side fallback for unseen roles.
2. The single-model-must-already-be-disaggregated precondition. IsDisaggregated (and hence
   the whole RoleDemand population) is gated on some variant in the same result having a
   non-"both" role. A model whose only variant of a given role is zero-replica, where every other
   variant is role ""/RoleBoth (i.e. not P/D disaggregated at all, or disaggregated only in
   theory via discovery metadata that the analyzer never sees because VariantReplicaState.Role
   itself is empty/unset for that variant) does not trigger this path; RoleDemand is nil
   entirely, and the model runs the non-disaggregated ("both"-only) path via Remaining/Spare
   instead of RoleCapacities. This is a different, model-scoped mechanism, not per-role, so it
   sidesteps rather than resolves the per-role question for such a model.
3. A role omitted by the analyzer's own logic (as opposed to zero-replica) is unaddressed.
   Nothing in the store/aggregateByVariant/aggregateRoleDemand chain forces a role to appear if
   the analyzer's code simply never runs the branch that would attribute it -- e.g. if
   input.VariantStates itself is missing a variant (a discovery-side gap, not an analyzer-side
   one), aggregateByVariant's dense loop never sees it and can't manufacture a VariantCapacity
   for it at all; the fallback chain traced in Section 2 only helps once a variant is at least
   present in VariantStates. The prior analysis's gap was framed generically ("a role the
   analyzer never attributes demand to") and remains real for any such upstream-of-the-analyzer
   omission -- the capacity store cannot help there since it is keyed and iterated from the same
   VariantStates list that would already be missing the entry.
4. TotalSupply/TotalAnticipatedSupply for a truly zero-replica role stay 0 regardless of the
   store. As shown above, the store's synthesized PerReplicaCapacity never becomes non-zero
   role-level supply for a zero-ready-replica role, because ReplicaCount/PendingReplicas are
   what gate the multiplication in AggregateByRole/SumTotalSupply, and those come from
   VariantReplicaState.CurrentReplicas/PendingReplicas, not from the store. So even though the
   role is now visible to initRoleState (point above), its RequiredCapacity computes to exactly
   0 from TotalDemand: 0 -- a real signal (there is no demand to service, so no capacity should
   be requested) but not "the store synthesized a plausible non-zero demand/capacity that lets the
   role scale up from zero." Nothing in this codebase synthesizes non-zero demand for a role with
   zero replicas from history -- only non-zero capacity (which is multiplied by zero replicas and
   so contributes nothing to RequiredCapacity). If the actual problem the compose spec needs to
   solve is "a role should be able to scale up from 0 based on some signal even before any replica
   exists," this mechanism does not provide that signal -- that job belongs entirely to
   internal/engines/scalefromzero (Section 1), which uses a completely different, binary,
   EPP-queue-depth trigger, not a synthesized demand number, and which only fires when the whole
   model (not a single role within an otherwise-live model) has zero running replicas.

Net answer to the task's Section 3 question: the specific mechanical failure mode described in
the prior analysis's Task C ("role absent from the RoleCapacities/RoleDemand map keys, hence
never iterated by initRoleState, hence permanently invisible to scale-up") is NOT triggered by
the ordinary "this role's only variant currently has 0 ready replicas" case, thanks to the
capacity-store-backed dense-loop construction in aggregateByVariant plus AggregateByRole's
unconditional zero-value map entry -- that specific sub-case resolves to "present with
RequiredCapacity: 0," which is correct, not a gap. But this is a happy accident of a mechanism
built to solve a different problem (capacity/supply estimation for zero-replica variants), it
requires the model to already be recognized as disaggregated via some other live-or-configured
variant, and it does not touch demand estimation at all -- so the general gap (an analyzer, or the
data feeding it, simply never producing a RoleDemand key for a role at all) remains real and
unaddressed for any case upstream of aggregateByVariant's loop over VariantStates, and the
"scale a role up from truly zero based on a synthesized demand estimate" capability the user may be
picturing does not exist anywhere in this codebase -- only a capacity estimate exists for that
case, and only a binary wake trigger (not a demand estimate) exists for the whole-model-zero
case.
