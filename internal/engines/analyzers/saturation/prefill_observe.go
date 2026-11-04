package saturation

import (
	"maps"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// Prefill capacity, observed (docs/proposals/slo-capacity-planner.md, PR P1;
// the model is docs/proposals/slo-capacity-planner-ttft.md). Each prefill
// variant's rows go to internal/signals/prefill, which learns V_P, a and f,
// and the result is logged at perReplicaLogLevel as prefill-capacity-observed.
// Nothing sizes from it.
//
// It is wired here, not in the slo analyzer, because two of its inputs are
// this analyzer's: whether decode is back-pressuring prefill this cycle
// (decodeSaturated, the test computeK2 applies), and the prefill lower-bound
// window (prefill_bound.go), V_P's third source. This analyzer also runs for
// every model, where the slo analyzer runs only when a policy lists it.

// SwitchPrefillCapacityObserve is the replay switch for the observation. On,
// each prefill variant's rows are observed and logged every cycle. Off,
// nothing is observed or logged. It moves no golden output: the observation
// writes no state any output reads.
const SwitchPrefillCapacityObserve = "prefill-capacity-observe"

// prefillBoundStashKey names a variant's back-pressure bound for the cycle
// in progress (notePrefillBound writes it, observePrefill reads it).
func prefillBoundStashKey(namespace, modelID, variant string) string {
	return namespace + "|" + modelID + "|" + variant
}

// stashPrefillBound keeps the bound notePrefillBound read for the variant,
// in computed tokens/s, for observePrefill. Called with a.mu held.
func (a *SaturationAnalyzer) stashPrefillBound(rm domain.ReplicaMetrics, tokens float64, samples int) {
	if !a.prefillObserve {
		return
	}
	a.prefillBounds[prefillBoundStashKey(rm.Namespace, rm.ModelID, rm.VariantName)] =
		prefill.Bound{Tokens: tokens, Samples: samples}
}

// observePrefill observes each prefill variant's rows of the cycle and logs
// what its key has learned. After priceReplicas, so the bound each variant's
// replicas read this cycle is stashed.
func (c *cycle) observePrefill() {
	a := c.a
	a.mu.Lock()
	on := a.prefillObserve
	bounds := map[string]prefill.Bound{}
	for _, variant := range slices.Sorted(maps.Keys(c.rolesByVariant)) {
		k := prefillBoundStashKey(c.input.Namespace, c.input.ModelID, variant)
		if b, ok := a.prefillBounds[k]; ok {
			bounds[variant] = b
			delete(a.prefillBounds, k)
		}
	}
	a.mu.Unlock()
	if !on {
		return
	}
	rows := map[string][]domain.ReplicaMetrics{}
	for _, rm := range c.input.ReplicaMetrics {
		if domain.CanonicalRole(c.rolesByVariant[rm.VariantName]) == domain.RolePrefill {
			rows[rm.VariantName] = append(rows[rm.VariantName], rm)
		}
	}
	view := &prefill.Settle{View: a.settleView(), Model: settleModel(c.input.Namespace, c.input.ModelID)}
	for _, variant := range slices.Sorted(maps.Keys(rows)) {
		var engine inferenceengine.Engine
		if params := engineParamsFor(a, c.input.Namespace, c.input.ModelID, variant); params != nil {
			engine = params.Engine
		}
		key := a.itlWindowKey(c.input.Namespace, c.input.ModelID, variant, c.accelByVariant[variant],
			c.gpusByVariant[variant])
		est := a.prefillObserver.Observe(prefill.Cycle{
			Key: key, Engine: engine, Disaggregated: true, Rows: rows[variant],
			DownstreamSaturated: c.decodeSaturated, Bound: bounds[variant],
			Settle: view, Now: a.now(),
		})
		c.logger.V(perReplicaLogLevel).Info("prefill-capacity-observed",
			"modelID", c.input.ModelID, "namespace", c.input.Namespace, "variant", variant, "key", key,
			"engine", string(engine), "pods", len(rows[variant]),
			"vp", est.VP, "vpSource", string(est.VPSource), "vpLowerBound", est.VPLowerBound,
			"vpRefused", string(est.VPRefused), "vpSamples", est.VPSamples,
			"slopeBound", est.SlopeBound, "vpAboveSlopeBound", est.VPAboveSlopeBound,
			"a", est.A, "aIntercept", est.AIntercept, "aOverhead", est.AOverhead,
			"aOverheadKnown", est.AOverheadKnown, "aRefused", string(est.ARefused), "aSamples", est.ASamples,
			"aPromptMin", est.APromptMin, "aPromptMax", est.APromptMax,
			"f", est.F, "fRefused", string(est.FRefused), "fSamples", est.FSamples,
			"downstreamSaturated", c.decodeSaturated,
			"admitted", est.Admitted, "refused", est.Refused)
	}
}
