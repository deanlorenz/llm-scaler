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
//   - none: DecisionNoSignal, and saturation's PRC is kept.
//
// Two rules hold a role whatever the needs say (compositeHolds):
//   - saturation is not listed and no variant of the role has a need: nothing
//     the policy lists can size it, so it neither grows nor shrinks;
//   - a listed analyzer other than saturation is inside its staleness grace
//     with no reading: it cannot ask for more this cycle, so the roles it
//     covers do not shrink until its grace runs out.
//
// The PRC that makes saturation's demand need N replicas is D_sat[role] / N.
// It is used only when N came from another analyzer and D_sat[role] is
// positive. When saturation sets N, its own PRC is kept exactly (D/(D/P) is
// not always bit-identical to P, and a replica count at a boundary would
// flip). When D_sat[role] is zero, saturation's PRC is kept too: a PRC of zero
// would read as "no basis to size" and hold the model, below what either
// analyzer asks for.
//
// eligibleAnalyzers is the analyzers the policy lists: namedResults less the
// observe-only slo analyzer, less saturation when the policy does not list
// it, plus an entry with no Result for each listed analyzer that returned
// nothing this cycle but is still inside its grace (the caller decides that
// once). Saturation remains the identity source either way.
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

	sized := make(map[string]bool)
	contributors := make(map[string]bool)
	compositeVCs := make([]domain.VariantCapacity, 0, len(sat.Result.VariantCapacities))
	for _, source := range sat.Result.VariantCapacities {
		vc := source
		vc.PendingAges = slices.Clone(source.PendingAges)
		// Saturation's own service-rate signal, published from its entry; the
		// composite does not price on it.
		vc.ServiceRate = nil

		d := allocation.ResolveSO(eligibleAnalyzers, sat, vc.VariantName)
		vc.Reason = string(d.Path)
		if d.OK {
			sized[domain.RoleOfVC(vc)] = true
		}
		for _, name := range d.Contributors {
			contributors[name] = true
		}
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

	roles := rolesPresent(sat.Result.VariantCapacities)
	composite := allocation.NamedAnalyzerResult{
		Name:              allocation.CompositeSignalName,
		Result:            &result,
		Score:             maxScore(*sat, eligibleAnalyzers, contributors),
		ScaleUpThreshold:  scaleUp,
		ScaleDownBoundary: scaleDown,
		Live:              len(contributors) > 0 || sat.Live,
		Holds:             compositeHolds(*sat, eligibleAnalyzers, roles, sized),
	}
	buildCapacities(ctx, &composite, nil, scaleUp, scaleDown)
	applyHolds(&composite, roles)
	for role, h := range composite.Holds {
		logger.Info("composite holds a role",
			"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace, "role", role,
			"noScaleUp", h.NoScaleUp, "noScaleDown", h.NoScaleDown)
	}
	composite.Remaining = composite.RequiredCapacity
	composite.Spare = composite.SpareCapacity
	return composite
}

// compositeHolds returns what each role is forbidden this cycle, or nil.
//
// Saturation unlisted: a role none of whose variants a listed analyzer could
// size is held both ways. Saturation still supplies the units and the
// variants, but sizing the role on it would be sizing on an analyzer the
// policy left out.
//
// A listed analyzer other than saturation that is live (inside its staleness
// grace) but has no reading this cycle blocks scale-down of the roles it
// reports variants for, or of every role when it reports none. Without this,
// one missed scrape from the analyzer asking for most replicas let the
// others' smaller need shrink the fleet for a cycle. Saturation's own grace
// is left as it was before the composite existed.
func compositeHolds(sat allocation.NamedAnalyzerResult, eligible []allocation.NamedAnalyzerResult,
	roles []string, sized map[string]bool) map[string]allocation.RoleHold {
	holds := make(map[string]allocation.RoleHold)
	satListed := slices.ContainsFunc(eligible, func(e allocation.NamedAnalyzerResult) bool { return e.Name == sat.Name })
	if !satListed {
		for _, role := range roles {
			if !sized[role] {
				holds[role] = allocation.RoleHold{NoScaleUp: true, NoScaleDown: true}
			}
		}
	}
	satRole := make(map[string]string, len(sat.Result.VariantCapacities))
	for _, vc := range sat.Result.VariantCapacities {
		satRole[vc.VariantName] = domain.RoleOfVC(vc)
	}
	for _, e := range eligible {
		if e.Name == sat.Name || !e.Live || allocation.Eligible(e) {
			continue
		}
		covered := make(map[string]bool)
		if e.Result != nil {
			for _, vc := range e.Result.VariantCapacities {
				if role, ok := satRole[vc.VariantName]; ok {
					covered[role] = true
				}
			}
		}
		for _, role := range roles {
			if len(covered) == 0 || covered[role] {
				h := holds[role]
				h.NoScaleDown = true
				holds[role] = h
			}
		}
	}
	if len(holds) == 0 {
		return nil
	}
	return holds
}

// applyHolds zeroes the signal each hold forbids: a role's RequiredCapacity
// for NoScaleUp, its SpareCapacity for NoScaleDown, and the model-level one
// when every role in roles is held that way. An aggregated model has one
// role, domain.RoleBoth, and only the model-level signals.
func applyHolds(nr *allocation.NamedAnalyzerResult, roles []string) {
	if len(nr.Holds) == 0 {
		return
	}
	allUp, allDown := true, true
	for _, role := range roles {
		h := nr.Holds[role]
		allUp = allUp && h.NoScaleUp
		allDown = allDown && h.NoScaleDown
		rc, ok := nr.RoleCapacities[role]
		if !ok {
			continue
		}
		if h.NoScaleUp {
			rc.RequiredCapacity = 0
		}
		if h.NoScaleDown {
			rc.SpareCapacity = 0
		}
		nr.RoleCapacities[role] = rc
	}
	if allUp {
		nr.RequiredCapacity = 0
	}
	if allDown {
		nr.SpareCapacity = 0
	}
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

// maxScore is the composite's Score: the largest score among the analyzers
// that contributed a need this cycle, or saturation's when none did. The
// cross-model fair share reads it (greedy_score_optimizer.go), so an analyzer
// that sized nothing cannot raise it.
func maxScore(sat allocation.NamedAnalyzerResult, eligible []allocation.NamedAnalyzerResult, contributors map[string]bool) float64 {
	best, found := 0.0, false
	for _, e := range eligible {
		if contributors[e.Name] && (!found || e.Score > best) {
			best, found = e.Score, true
		}
	}
	if !found {
		return sat.Score
	}
	return best
}
