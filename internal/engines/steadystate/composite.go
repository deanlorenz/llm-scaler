package steadystate

import (
	"context"
	"maps"
	"slices"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
)

// buildComposite reduces namedResults (every analyzer that ran for the model)
// into the single allocation.NamedAnalyzerResult the optimizer consumes.
//
// Saturation's entry is the composite's identity and unit: its variants are the
// composite's variants, and demand stays in saturation's units (D_sat). The
// composite starts as a copy of saturation's result -- every field saturation
// set, so supply, holds, start times and queue loads reach the optimizer as
// they did when it consumed saturation alone -- and changes only what it
// decides: each variant's per-replica capacity (PRC), its utilization, and its
// Reason, which names the decision path.
//
// Per variant, every eligible analyzer that defines a replica need N
// (allocation.TotalReplicas) contributes, and the composite takes the largest:
//   - one contributor: its N (DecisionSingle);
//   - several: the largest (DecisionAgree), so scale-up follows the analyzer
//     asking for most, and scale-down needs every one of them to agree;
//   - none, with saturation itself eligible: saturation's N
//     (DecisionSatFallback);
//   - none at all: DecisionNoSignal, and saturation's PRC is kept.
//
// The PRC that makes saturation's demand need N replicas is D_sat[role] / N.
// It is used only when N came from another analyzer and D_sat[role] is
// positive. When saturation sets N, its own PRC is kept exactly (D/(D/P) is
// not always bit-identical to P, and a replica count at a boundary would
// flip). When D_sat[role] is zero, saturation's PRC is kept too: a PRC of zero
// would read as "no basis to size" and hold the model, below what either
// analyzer asks for.
//
// eligibleAnalyzers is namedResults less saturation when the policy does not
// list it (the caller decides that once); saturation remains the identity
// source either way.
//
// Every slice and map is copied, so the optimizer's bookkeeping on the
// composite cannot reach back into an analyzer's own result.
func buildComposite(
	ctx context.Context,
	namedResults, eligibleAnalyzers []allocation.NamedAnalyzerResult,
	scaleUp, scaleDown float64,
) allocation.NamedAnalyzerResult {
	logger := ctrl.LoggerFrom(ctx)
	sat := findSaturation(namedResults)
	// No saturation entry: nothing to express demand in. Unreachable in
	// production, where runAnalyzersAndScore fails before building any entry
	// without one; every test builds its own slice.
	if sat == nil || sat.Result == nil {
		return emptyComposite()
	}

	demandByRole := make(map[string]float64)
	for _, role := range rolesPresent(sat.Result.VariantCapacities) {
		d, _ := aggregation.DemandForRole(sat.Result, role)
		demandByRole[role] = d
	}

	compositeVCs := make([]domain.VariantCapacity, 0, len(sat.Result.VariantCapacities))
	for _, source := range sat.Result.VariantCapacities {
		vc := source
		vc.PendingAges = slices.Clone(source.PendingAges)
		// Saturation's own service-rate signal, published from its entry; the
		// composite does not price on it.
		vc.ServiceRate = nil

		d := allocation.ResolveSO(eligibleAnalyzers, sat, vc.VariantName)
		vc.Reason = string(d.Path)
		if r := demandByRole[domain.RoleOfVC(vc)]; d.OK && !d.FromSaturation && d.N > 0 && r > 0 {
			vc.PerReplicaCapacity = r / d.N
			vc.Utilization = 0
			if vc.ReplicaCount > 0 {
				vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * vc.PerReplicaCapacity)
			}
		}

		logger.V(logging.VERBOSE).Info("composite-contributors",
			"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace,
			"variant", vc.VariantName, "decisionPath", d.Path,
			"contributors", d.Contributors, "totalReplicas", d.N)
		compositeVCs = append(compositeVCs, vc)
	}

	result := *sat.Result
	result.AnalyzerName = allocation.CompositeSignalName
	result.VariantCapacities = compositeVCs
	result.RoleDemand = maps.Clone(sat.Result.RoleDemand)
	result.HeldRoleDemand = maps.Clone(sat.Result.HeldRoleDemand)
	result.RoleStartSeconds = maps.Clone(sat.Result.RoleStartSeconds)
	result.VariantBusiestQueueLoad = maps.Clone(sat.Result.VariantBusiestQueueLoad)

	composite := allocation.NamedAnalyzerResult{
		Name:              allocation.CompositeSignalName,
		Result:            &result,
		Score:             maxScore(namedResults),
		ScaleUpThreshold:  scaleUp,
		ScaleDownBoundary: scaleDown,
		Live:              compositeLive(*sat, eligibleAnalyzers),
	}
	buildCapacities(ctx, &composite, nil, scaleUp, scaleDown)
	composite.Remaining = composite.RequiredCapacity
	composite.Spare = composite.SpareCapacity
	return composite
}

// compositeLive is whether the composite takes part in scale-down: when any
// contributing analyzer is live, by the engine's liveness rule (its staleness
// grace included). With no contributor at all, saturation's own liveness, as
// when the optimizer consumed saturation alone.
func compositeLive(sat allocation.NamedAnalyzerResult, eligible []allocation.NamedAnalyzerResult) bool {
	if len(eligible) == 0 {
		return sat.Live
	}
	return slices.ContainsFunc(eligible, func(e allocation.NamedAnalyzerResult) bool { return e.Live })
}

// findSaturation returns the saturation entry from namedResults, or nil.
func findSaturation(namedResults []allocation.NamedAnalyzerResult) *allocation.NamedAnalyzerResult {
	for i := range namedResults {
		if namedResults[i].Name == domain.SaturationAnalyzerName {
			return &namedResults[i]
		}
	}
	return nil
}

// emptyComposite is the composite of a model with no saturation entry: a
// non-nil Result with no variants, so allocation.CompositeHasSignal reports
// false through its ordinary path.
func emptyComposite() allocation.NamedAnalyzerResult {
	return allocation.NamedAnalyzerResult{
		Name:   allocation.CompositeSignalName,
		Result: &domain.AnalyzerResult{AnalyzerName: allocation.CompositeSignalName},
	}
}

// rolesPresent returns the distinct roles across vcs, in first-seen order.
func rolesPresent(vcs []domain.VariantCapacity) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, vc := range vcs {
		role := domain.RoleOfVC(vc)
		if _, ok := seen[role]; !ok {
			seen[role] = struct{}{}
			out = append(out, role)
		}
	}
	return out
}

// maxScore is the composite's Score: the largest of the analyzers' scores,
// which is saturation's when it runs alone. The cross-model fair share reads
// it (greedy_score_optimizer.go).
func maxScore(namedResults []allocation.NamedAnalyzerResult) float64 {
	best := 0.0
	for i, nr := range namedResults {
		if i == 0 || nr.Score > best {
			best = nr.Score
		}
	}
	return best
}
