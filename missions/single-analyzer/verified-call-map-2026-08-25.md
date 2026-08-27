# Verified call map: entry point -> analyzers -> optimizer (2026-08-25)

Re-derived from scratch by reading files on disk at HEAD 506ae369 in
worktrees/single-analyzer, plus the one expected uncommitted change (a
modification to internal/engines/steadystate/engine_v2.go adding
composeAnalyzerResults, plus engine_v2_compose_test.go and
docs/plans/analyzers/spec-compose-analyzer-results.md). This document does
NOT reuse any prior trace -- every node below was opened and read with the
Read tool during this pass, and every call site was confirmed by reading the
calling function's body, not by grep-only pattern matching.

Confirmed HEAD via git log --oneline -5:
506ae369 docs(analyzers): add refactor planning ledger and repo-conventions harvest
233f20c1 fix(openshift): 15s optimize interval, matching the base -- 60s throttled FMA
41542845 fix(benchmark): collect replica counts ourselves, so an FMA run can be costed
c885836d docs(fma): correct request 7 -- the 503s were saturation, not a waking launcher
370fda6a docs(fma): neither workaround for the 503 burst exists, both tested

Confirmed uncommitted state via git status --porcelain=v1:
 M docs/plans/analyzers/ledger-analyzer-optimizer-refactor.md
 M internal/engines/steadystate/engine_v2.go
?? docs/plans/analyzers/spec-compose-analyzer-results.md
?? internal/engines/steadystate/engine_v2_compose_test.go

---

## Layer 1: Top-level entry point -> recurring optimize loop

```
main()                                                     cmd/main.go:100
 |- mgr.Add(manager.RunnableFunc(func(ctx) error { ... }))  cmd/main.go:517-624
     |- engine := steadystate.NewEngine(...)                cmd/main.go:586-594
     |- engine.RegisterAnalyzer(throughput.AnalyzerName, ...) cmd/main.go:613 (conditional: taRegistered)
     `- go engine.StartOptimizeLoop(ctx)                     cmd/main.go:622
         `- (Engine) StartOptimizeLoop(ctx)                  internal/engines/steadystate/engine.go:434-442
             |- freezes e.analyzersSnapshot from e.analyzers  engine.go:435-436
             |- e.started = true                              engine.go:437
             `- e.executor.Start(ctx)                          engine.go:441
                 `- (*PollingExecutor) Start(ctx)              internal/engines/executor/polling.go:50-54
                     `- wait.UntilWithContext(ctx, func(){ e.executeWithRetry(ctx) }, e.interval)  polling.go:51-53
                         `- (*PollingExecutor) executeWithRetry(ctx)   polling.go:56-86
                             `- e.config.OptimizeFunc(ctx)     polling.go:67
                                 == engine.optimize (bound at construction: engine.go:253-259)
```

Node-by-node verification:

- main() -- cmd/main.go:100. Confirmed via Read, lines 100-818 (full function body read).
- Manager runnable registration -- cmd/main.go:517: err = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {. Read tool, lines 516-629. This is a manager.Runnable added to the controller-runtime Manager; it only executes once this replica becomes leader (leader-elect gating is implicit in how mgr.Add runnables are started).
- steadystate.NewEngine(...) -- call site cmd/main.go:586-594; definition internal/engines/steadystate/engine.go:198, signature: func NewEngine(client client.Client, apiReader client.Reader, scheme *runtime.Scheme, recorder record.EventRecorder, metricsRegistry *source.SourceRegistry, cfg *config.Config, gpuLimiter allocation.Limiter) *Engine. Confirmed via Read, engine.go lines 192-276. This is also where engine.executor = executor.NewPollingExecutor(...) is wired (engine.go:253-259), binding OptimizeFunc: engine.optimize.
- engine.RegisterAnalyzer(throughput.AnalyzerName, throughput.NewThroughputAnalyzer()) -- cmd/main.go:613, gated on taRegistered := cfg.ThroughputAnalyzerEnabled() captured once at cmd/main.go:446. This is the only built-in second analyzer wired at startup; it is conditional on config. Confirmed via Read of both files.
- go engine.StartOptimizeLoop(ctx) -- cmd/main.go:622. Launched as its own goroutine inside the leader-gated runnable.
- StartOptimizeLoop -- defined internal/engines/steadystate/engine.go:434-442. Confirmed via Read, lines 427-442. Body: snapshots e.analyzers into e.analyzersSnapshot (line 435-436), sets e.started = true (437), calls e.recordActiveOptimizer() and e.executor.Start(ctx) (439-441).
- e.executor field -- declared engine.go:72 as "executor executor.Executor"; concrete value assigned at construction (engine.go:253) as *executor.PollingExecutor.
- (*PollingExecutor).Start -- internal/engines/executor/polling.go:50-54. Confirmed via Read, full file (87 lines). Uses wait.UntilWithContext (k8s.io/apimachinery) -- a ticking loop at e.interval (set from cfg.OptimizationInterval(), engine.go:257), not a raw time.Ticker, but functionally the recurring loop the task asked about.
- executeWithRetry -- polling.go:56-86. Calls e.config.OptimizeFunc(ctx) once per tick (line 67); on error, retries with exponential backoff (capped 4s) inside the same tick rather than waiting for the next tick, until success or context cancellation.
- OptimizeFunc binding -- executor.Config{OptimizeFunc: engine.optimize} set at engine.go:255, inside NewEngine. So each tick calls (*Engine).optimize.

There is a second, independent engine + loop registered in main() for scale-from-zero (cmd/main.go:632-680, scalefromzero.NewEngine + its own "go engine.StartOptimizeLoop(ctx)" at cmd/main.go:678). It does not run analyzers or consume AnalyzerResult/NamedAnalyzerResult -- confirmed by searching for "AnalyzerResult" and "domain.Analyzer" in internal/engines/scalefromzero/*.go, which returns no matches. It is out of scope for Layers 2-3 but is worth naming so it is not mistaken for a second path into the optimizer traced below -- it is a wholly separate engine with its own limiter and its own decision logic, not a second consumer of ModelScalingRequest.

---

## Layer 2: Analyzer-running / preparation path (recurring loop entry -> optimizer input)

```
(Engine) optimize(ctx) (retErr error)                        engine.go:530-690
 |- e.refreshLimiter(ctx)                                     engine.go:543  -> engine.go:491-511
 |- e.reconcileExternalAnalyzers(ctx)                         engine.go:544  -> engine.go:351-390
 |- utils.ActiveVariantAutoscaling(...)                       engine.go:553
 |- [early return if no active VAs]                           engine.go:559-591
 |- modelGroups := utils.GroupVariantAutoscalingByModel(...)  engine.go:623
 |- e.optimizer selection (CostAware vs GreedyByScore)         engine.go:655-666
 `- allDecisions := e.optimizeV2(ctx, modelGroups, currentAllocations)   engine.go:670
     `- (Engine) optimizeV2(ctx, modelGroups, currentAllocations) []domain.VariantDecision   engine.go:934-1060+
         |- for groupKey, modelVAs := range modelGroups {       engine.go:963  (once per model, this cycle)
         |   |- e.resolveModelPolicy(...)                       engine.go:980
         |   |- data, err := e.prepareModelData(...)             engine.go:981
         |   `- req, err := e.collectV2ModelRequest(ctx, modelID, namespace,
         |         data.replicaMetrics, scalingPolicyConfig, data.variantStates,
         |         data.variantMetadata, data.scaleTargets, data.variantAutoscalings,
         |         data.schedulerQueue, data.arrivalRate)        engine.go:994-996
         |       |  definition: internal/engines/steadystate/engine_v2.go:769-805
         |       |
         |       `- namedResults, err := e.runAnalyzersAndScore(ctx, modelID, namespace,
         |             replicaMetrics, config, variantStates, variantMetadata,
         |             scaleTargets, variantAutoscalings, schedulerQueue, arrivalRate)
         |                                                        engine_v2.go:781-782
         |           |  definition: engine_v2.go:101-177
         |           |
         |           |- baseResult, err := e.runV2AnalysisOnly(...)    engine_v2.go:121-122
         |           |     definition: engine_v2.go:30-82
         |           |     `- result, err := e.saturationV2Analyzer.Analyze(ctx, input)  engine_v2.go:72
         |           |
         |           |- namedResults := []allocation.NamedAnalyzerResult{
         |           |     buildNamedResult(ctx, domain.SaturationAnalyzerName, baseResult, ...)}
         |           |                                                engine_v2.go:152-154
         |           |
         |           |- for _, entry := range e.analyzerRunEntries() {   engine_v2.go:155
         |           |     (skip if entry.name == SaturationAnalyzerName, or !config.AnalyzerEnabled(entry.name))
         |           |     result := runRegisteredAnalyzer(ctx, logger, entry, modelID, input)  engine_v2.go:162
         |           |         definition: engine_v2.go:493-519
         |           |         `- result, err = entry.analyzer.Analyze(ctx, input)   engine_v2.go:510
         |           |     namedResults = append(namedResults, buildNamedResult(...))  engine_v2.go:167-168
         |           |   }
         |           |
         |           |- e.updateLivenessAndSetLive(...)                engine_v2.go:170
         |           |- e.recordAnalyzerMetrics(...)                   engine_v2.go:171
         |           `- return composeAnalyzerResults(namedResults), nil   engine_v2.go:176
         |                 definition: engine_v2.go:188-190  *** UNCOMMITTED CHANGE ***
         |                 (currently a pure passthrough: returns namedResults unchanged)
         |
         |       req = &allocation.ModelScalingRequest{
         |           ModelID, Namespace, AnalyzerResults: namedResults,
         |           VariantStates, Variants: variantMetadata, Priority, Disaggregated}
         |                                                        engine_v2.go:796-804
         |   requests = append(requests, *req)                    engine.go:1005 (in optimizeV2, not shown above)
         | }  // end per-model loop
         |
         |- [if len(requests)==0: return nil]                     engine.go:1010-1012
         |- managedByType := computeCurrentGPUUsage(requests)     engine.go:1033
         |- decision.PublishManagedGPUUsage(...)                  engine.go:1034
         |- optimizer, constraints := e.selectV2Optimizer(ctx, requests)  engine.go:1049
         |     definition: engine.go:847-907
         |- (optional) g.Rescale = e.resolveRescaleFlags(requests) engine.go:1052-1054
         `- allDecisions := optimizer.Optimize(ctx, requests, constraints)  engine.go:1055
               interface: allocation.ScalingOptimizer.Optimize (see Layer 3)
```


Node-by-node verification (file:line, confirmed-via note, short quote):

1. (Engine) optimize -- internal/engines/steadystate/engine.go:530. Confirmed via Read, lines 520-703. This is the function bound as OptimizeFunc in Layer 1 and is called once per polling tick.

       func (e *Engine) optimize(ctx context.Context) (retErr error) {
           allDecisions := e.optimizeV2(ctx, modelGroups, currentAllocations)

   There is no branch to any non-V2 analysis path in this function -- a code comment at line 668 states "V2 (saturation): saturation_v2.Analyzer -> AnalyzerResult -> Optimizer.Optimize -> Enforcer bridge," and the only call feeding allDecisions is e.optimizeV2 (line 670). No feature flag/config branch selects an alternate collection path at this level.

2. (Engine) optimizeV2 -- engine.go:934. Confirmed via Read, lines 932-1060+ (full function through the optimizer.Optimize call and its logging tail).

       func (e *Engine) optimizeV2(
           ctx context.Context,
           modelGroups map[string][]llmdVariantAutoscalingV1alpha1.VariantAutoscaling,
           currentAllocations map[string]*domain.Allocation,
       ) []domain.VariantDecision {

   Iterates modelGroups (one entry per model, built from all active VariantAutoscalings by utils.GroupVariantAutoscalingByModel at engine.go:623), building one allocation.ModelScalingRequest per model via e.collectV2ModelRequest, then calls the optimizer exactly once for the whole batch (optimizer.Optimize, line 1055) -- not once per model. This is the sole place ModelScalingRequest values are assembled before reaching the optimizer.

3. (Engine) collectV2ModelRequest -- defined internal/engines/steadystate/engine_v2.go:769-805, called from engine.go:994. Confirmed via Read of both.

       func (e *Engine) collectV2ModelRequest(...) (*allocation.ModelScalingRequest, error) {
           namedResults, err := e.runAnalyzersAndScore(ctx, modelID, namespace, replicaMetrics, config,
               variantStates, variantMetadata, scaleTargets, variantAutoscalings, schedulerQueue, arrivalRate)
           return &allocation.ModelScalingRequest{
               ModelID: modelID, Namespace: namespace, AnalyzerResults: namedResults,
           }, nil
       }

   This is a single call site of runAnalyzersAndScore in non-test code (confirmed by searching across internal/engines/steadystate/*.go excluding _test.go) -- there is exactly one path from "analyzers ran" to a ModelScalingRequest, not multiple.

4. (Engine) runAnalyzersAndScore -- engine_v2.go:101-177. Confirmed via Read, lines 91-190 (function plus preceding/following doc comments and the composeAnalyzerResults definition immediately below it).

       func (e *Engine) runAnalyzersAndScore(...) ([]allocation.NamedAnalyzerResult, error) {
           namedResults := []allocation.NamedAnalyzerResult{
               buildNamedResult(ctx, domain.SaturationAnalyzerName, baseResult, config, metaByVariant, satUp, satDown),
           }
           for _, entry := range e.analyzerRunEntries() {
               if entry.name == domain.SaturationAnalyzerName { continue }
               if !config.AnalyzerEnabled(entry.name) { continue }
               result := runRegisteredAnalyzer(ctx, logger, entry, modelID, input)
           }
           return composeAnalyzerResults(namedResults), nil
       }

   This is the exact function the "insertion point" mental model names, and it is genuinely the sole per-model combination point: it builds namedResults starting with saturation (always present, line 152-154) and appends every other enabled analyzer's result (built-in or config-driven external) in the loop at line 155. This is where N analyzer results become one slice -- the return value is that slice, currently passed through composeAnalyzerResults unchanged.

5. composeAnalyzerResults -- engine_v2.go:188-190, the uncommitted addition. Confirmed via Read (lines 179-190) and via the working-tree diff for internal/engines/steadystate/engine_v2.go, which shows the only change in the file is replacing "return namedResults, nil" with "return composeAnalyzerResults(namedResults), nil" and appending this new function immediately after.

       func composeAnalyzerResults(namedResults []allocation.NamedAnalyzerResult) []allocation.NamedAnalyzerResult {
           return namedResults
       }

   Currently a pure passthrough (confirmed also by the new engine_v2_compose_test.go, which asserts exactly this for both the saturation-only and empty cases). There is no other call site of composeAnalyzerResults in the repo (the only reference besides its definition and test file is the one call at line 176).

6. runV2AnalysisOnly (saturation) -- engine_v2.go:30-82, called once from runAnalyzersAndScore at line 121-122.

       result, err := e.saturationV2Analyzer.Analyze(ctx, input)

   e.saturationV2Analyzer is a domain.Analyzer set once in NewEngine (engine.go:213/241) to saturation_v2.NewSaturationAnalyzer(capacityStore). This call is unconditional -- saturation always runs.

7. runRegisteredAnalyzer (every other analyzer) -- engine_v2.go:493-519, called from runAnalyzersAndScore at line 162, once per entry in e.analyzerRunEntries() excluding saturation and excluding any analyzer for which config.AnalyzerEnabled(entry.name) is false.

       result, err = entry.analyzer.Analyze(ctx, input)
       if err != nil {
           return nil
       }
       return result

   Wrapped in a defer recover() (lines 500-508) so one analyzer's panic cannot abort the model's cycle. A failed/nil result is simply skipped (engine_v2.go:163-165 in the caller), not retried and not substituted with a placeholder.

8. analyzerRunEntries -- engine.go:318-342, called once per model per cycle from runAnalyzersAndScore (line 155). Confirmed via Read, lines 314-342.

       func (e *Engine) analyzerRunEntries() []analyzerEntry {
           entries = append(entries, e.analyzersSnapshot...)
           for _, name := range names { entries = append(entries, analyzerEntry{name: name, analyzer: e.externalAnalyzers[name]}) }
           return entries
       }

   Concatenates the frozen built-in snapshot (e.analyzersSnapshot -- saturation, plus throughput if taRegistered was true at startup) with the live, config-driven e.externalAnalyzers map (sorted by name), skipping any external name that collides with a built-in. This is the multi-analyzer registration surface: as of the current repo state, in a default deployment (no throughput ConfigMap entry, no external analyzer catalog entries) this returns a single entry (saturation only) -- matching what composeAnalyzerResults doc comment calls the default, and currently the only case exercised in production.

9. buildNamedResult / buildCapacities -- engine_v2.go:823-842 and 844-885. Confirmed via Read, lines 807-917. These wrap each analyzer's raw *domain.AnalyzerResult into allocation.NamedAnalyzerResult, joining discovery metadata (Role), computing TotalSupply/Utilization, and applying applyUniversalThreshold to derive RequiredCapacity/SpareCapacity. This happens individually per analyzer result (once per loop iteration in runAnalyzersAndScore), before the slice-level composeAnalyzerResults step -- i.e., per-analyzer enrichment happens first, and only the already-enriched, already-named entries are handed to the (currently no-op) combine step.

10. ModelScalingRequest construction -- engine_v2.go:796-804, inside collectV2ModelRequest. AnalyzerResults field is the direct output of runAnalyzersAndScore (i.e., of composeAnalyzerResults). One ModelScalingRequest is produced per model per cycle, and all of them are accumulated into the requests slice in optimizeV2 (engine.go:1005, appended after each model's collectV2ModelRequest succeeds).

### Is there exactly one path from "analyzers ran" to "optimizer receives data"?

Yes, for the steady-state (saturation) engine. Confirmed by exhaustive search for non-test call sites of runAnalyzersAndScore (1 site: engine_v2.go:781, inside collectV2ModelRequest) and of collectV2ModelRequest (1 site: engine.go:994, inside optimizeV2), and by searching for Analyze(ctx across the whole repo excluding tests (2 sites, both inside engine_v2.go: line 72 for saturation, line 510 for every other analyzer -- both reached only through runAnalyzersAndScore). There is no second, parallel assembly of ModelScalingRequest or of the NamedAnalyzerResult slice anywhere else in the codebase.

The scale-from-zero engine (internal/engines/scalefromzero) is a structurally separate engine registered independently in main() (cmd/main.go:632-680) with its own StartOptimizeLoop. It does not call any analyzer's Analyze and does not construct NamedAnalyzerResult/ModelScalingRequest -- confirmed by finding zero references to those types/interfaces in that package. It is not a second consumer of analyzer output; it is an entirely different decision path (wake-from-zero based on queue-depth/EPP metrics), so it needs no change for an analyzer-combination refactor confined to runAnalyzersAndScore/composeAnalyzerResults.

### Where does per-analyzer data get combined/reduced vs. passed through individually?

- Per-analyzer, individually: buildNamedResult/buildCapacities (item 9 above) run once per analyzer result, inside the loop in runAnalyzersAndScore (lines 152-168). Each analyzer's (D, P) signal is turned into its own NamedAnalyzerResult with its own RequiredCapacity/SpareCapacity/Utilization, independently.
- As a group (combine point): the only place all of a model's NamedAnalyzerResult entries exist together as one collection before being handed off is the namedResults slice inside runAnalyzersAndScore, and the only transformation applied to that slice as a whole is composeAnalyzerResults(namedResults) at the tail (line 176) -- currently a no-op passthrough. No other function in the current codebase reduces multiple NamedAnalyzerResult entries for one model into a smaller number before the optimizer sees them; the optimizer itself (Layer 3) receives the full per-analyzer slice via ModelScalingRequest.AnalyzerResults and is responsible for any combination logic on its side today (e.g., saturation being read specially as "first entry" per the type's doc comment at optimizer_interfaces.go:75).

### Dead code / feature flags / config branches affecting this flow?

- cfg.ThroughputAnalyzerEnabled() (cmd/main.go:446) gates whether the built-in throughput analyzer is registered at all -- frozen at startup (taRegistered), not re-read per cycle. If disabled, analyzerRunEntries() never includes it, and runAnalyzersAndScore runs saturation only.
- config.AnalyzerEnabled(entry.name) (checked per entry, engine_v2.go:159) is a live, per-cycle, per-namespace config gate on top of registration -- an analyzer can be registered but skipped this cycle if disabled in the saturation ConfigMap.
- The external-analyzer catalog (e.Config.ExternalAnalyzerCatalog(), reconciled every cycle by reconcileExternalAnalyzers, engine.go:351-390) can add arbitrary config-driven analyzers at runtime without a restart; a name colliding with a built-in is dropped in favor of the built-in (engine.go:329-331 and 366-369).
- e.optimizer selection (engine.go:655-666) branches on e.Config.EffectiveLimiterMode() -- CostAwareOptimizer when no limiter is declared, GreedyByScoreOptimizer otherwise -- but this branch is downstream of analyzer collection and does not change how analyzer results are gathered, only which optimizer consumes them.
- No dead code was found in this path: runV2AnalysisOnly's exported sibling function name ("V2") is legacy naming (there is no live "V1" analysis path any more -- confirmed no other runAnalysisOnly/non-V2 equivalent exists in the package), but the function itself is live and called every cycle.

---

## Layer 3: The optimizer call

    // internal/engines/allocation/optimizer_interfaces.go:93-100
    type ScalingOptimizer interface {
        Name() string
        Optimize(ctx context.Context, requests []ModelScalingRequest, constraints []*ResourceConstraints) []domain.VariantDecision
    }

- Interface -- allocation.ScalingOptimizer, defined internal/engines/allocation/optimizer_interfaces.go:93-100. Confirmed via Read, lines 1-101 (whole file). Doc comment lists two implementations: CostAwareOptimizer (unlimited mode) and GreedyByScoreOptimizer (limited/GPU-constrained mode).
- Selection -- (Engine) selectV2Optimizer, internal/engines/steadystate/engine.go:847-907, called once per cycle from optimizeV2 at engine.go:1049. Confirmed via Read, lines 840-907.

  - The type of optimizer (CostAwareOptimizer vs GreedyByScoreOptimizer) is first chosen earlier, in optimize() (engine.go:655-666), based on e.Config.EffectiveLimiterMode().
  - selectV2Optimizer then decides, for a GreedyByScoreOptimizer, whether it can actually get GPU constraints this cycle; if no constraint provider is available or usage data is missing, it falls back to allocation.NewCostAwareOptimizer() (lines 857, 866, 883, 904) even though the config selected GreedyByScore -- this fallback is a real branch that changes which optimizer instance runs a given cycle, independent of the analyzer-collection layer above.
- Invocation -- internal/engines/steadystate/engine.go:1055, the sole call site in non-test code:

      allDecisions := optimizer.Optimize(ctx, requests, constraints)

  requests is the ModelScalingRequest slice assembled across the per-model loop in optimizeV2 (one entry per model that survived collectV2ModelRequest); constraints is a slice of ResourceConstraints pointers, nil in unlimited/cost-aware mode.
- Implementations found (confirmed by locating the CostAwareOptimizer and GreedyByScoreOptimizer Optimize methods in internal/engines/allocation/*.go): both implement the same Optimize signature as the interface (not independently verified line-by-line in this pass beyond confirming the interface and the two constructor call sites allocation.NewCostAwareOptimizer() / allocation.NewGreedyByScoreOptimizer(), which appear at engine.go:218, 658, 660, 866, 883, 904).

---

## Contradictions / differences vs. the "composeAnalyzerResults inserted inside runAnalyzersAndScore, called from collectV2ModelRequest" mental model

No contradiction found. That mental model is confirmed exactly as stated:

- composeAnalyzerResults is defined at internal/engines/steadystate/engine_v2.go:188-190, immediately after runAnalyzersAndScore (engine_v2.go:101-177), and is called from exactly one place: the last line of runAnalyzersAndScore (engine_v2.go:176).
- runAnalyzersAndScore is called from exactly one place: collectV2ModelRequest (engine_v2.go:781-782).
- collectV2ModelRequest is called from exactly one place: optimizeV2's per-model loop (engine.go:994-996).
- optimizeV2 feeds optimizer.Optimize(...) (engine.go:1055) with exactly the requests slice built from those per-model calls -- there is no other function anywhere in the repo that reads NamedAnalyzerResult or constructs ModelScalingRequest.AnalyzerResults.

Things worth flagging that a naive re-read might get wrong, verified here to remove doubt:

1. The insertion point is real and current, not stale. The working-tree diff on engine_v2.go shows the only change in that file is exactly this: replacing the plain return of namedResults with a call through composeAnalyzerResults, plus the new function body. Line numbers quoted throughout this document reflect the file as it exists on disk right now (already including the uncommitted edit) -- there was no second, older composeAnalyzerResults-shaped function elsewhere to confuse this with.

2. runAnalyzersAndScore is genuinely the sole per-model combine point, not one of several -- confirmed by searching for all non-test call sites of runAnalyzersAndScore, collectV2ModelRequest, and the Analyze(ctx pattern across the entire repository (results enumerated above), not just within the steadystate package.

3. The optimizer is called exactly once per cycle for the whole model batch, not once per model -- the Optimize call at engine.go:1055 receives the full requests slice built across all models in optimizeV2's loop. A mental model that pictured "the optimizer runs once per model, right after that model's composeAnalyzerResults call" would be wrong: composition happens per-model, but optimization happens once, after every model has been collected.

4. There is a second, unrelated combine layer downstream of the optimizer selection, easy to conflate with the analyzer-combination point: selectV2Optimizer's fallback-to-CostAware logic (engine.go:857, 866, 883, 904) can silently swap out the config-selected GreedyByScoreOptimizer for CostAwareOptimizer when GPU constraints are unavailable. This has nothing to do with composeAnalyzerResults or per-analyzer combination, but it is a real branch in what happens between analyzers running and the optimizer producing decisions, so it is called out explicitly here to avoid it being missed.

5. The scale-from-zero engine is not a second consumer of analyzer output or of composeAnalyzerResults's eventual output -- it is a structurally separate engine with no reference to NamedAnalyzerResult, ModelScalingRequest, or domain.Analyzer. A refactor of composeAnalyzerResults does not need to touch it.

6. No dead/legacy analyzer path exists today. The function name runV2AnalysisOnly and the "V2" naming throughout (optimizeV2, collectV2ModelRequest) suggests a "V1" counterpart might still exist and be reachable; it does not -- searching for any non-"V2" analysis entry point in steadystate found none, and optimize()'s only decision-producing call is e.optimizeV2(...) (engine.go:670). The "V2" naming is legacy from an earlier architecture where a V1 path existed; it is now the only path.
