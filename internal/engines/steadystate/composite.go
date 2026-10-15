package steadystate

import (
	"context"
	"maps"
	"math"
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
// decides: each role's demand, the model's, and each variant's share of it
// and utilization, and each variant's Reason, which names the decision path.
// Every per-replica capacity (PRC) is saturation's.
//
// Per variant, every eligible analyzer that defines a replica need N
// (allocation.TotalReplicas) contributes, and the composite takes the largest:
//   - one contributor: its N (DecisionSingle);
//   - several: the largest (DecisionAgree), so scale-up follows the analyzer
//     asking for most, and scale-down needs every one of them to agree;
//   - none: DecisionNoSignal, and saturation's demand is kept.
//
// Two rules hold a role whatever the needs say (compositeHolds):
//   - saturation is not listed and no variant of the role has a need: nothing
//     the policy lists can size it, so it neither grows nor shrinks;
//   - a listed analyzer other than saturation is inside its staleness grace
//     with no reading: it cannot ask for more this cycle, so the roles it
//     covers do not shrink until its grace runs out.
//
// A need is expressed in demand, never in capacity: every PRC stays as
// saturation measured it, so variants of one role on different accelerators
// keep their relative worth, and each role's demand becomes the largest of
//   - saturation's own demand, D_sat[role], exactly, when saturation
//     contributed for one of the role's variants;
//   - N x PRC_sat(variant) for each variant another analyzer's need N was
//     read for.
//
// The second holds when D_sat[role] is zero too, so another analyzer can grow
// a role saturation sees as idle (scaleDemand).
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
	// production, where runAnalyzers fails before building any entry
	// without one; every test builds its own slice.
	if sat == nil || sat.Result == nil {
		return emptyComposite()
	}

	warnModelLevelOnly(ctx, *sat, eligibleAnalyzers)

	satDemand := make(map[string]float64)
	for _, role := range rolesPresent(sat.Result.VariantCapacities) {
		if d, ok := aggregation.DemandForRole(sat.Result, role); ok {
			satDemand[role] = d
		}
	}

	needs := make(map[string]roleNeed)
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
		for _, name := range d.Contributors {
			contributors[name] = true
		}
		role := domain.RoleOfVC(vc)
		if _, ok := satDemand[role]; ok {
			needs[role] = needs[role].add(d, vc.PerReplicaCapacity)
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
	scaleDemand(&result, satDemand, needs)

	sized := make(map[string]bool, len(needs))
	for role, n := range needs {
		sized[role] = n.sized()
	}
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

// warnModelLevelOnly logs each listed analyzer that cannot size a P/D model:
// one that reports model-level demand only (no RoleDemand) defines no need for
// any prefill or decode variant, so on P/D it is listed and silently has no
// effect.
func warnModelLevelOnly(ctx context.Context, sat allocation.NamedAnalyzerResult, eligible []allocation.NamedAnalyzerResult) {
	if len(sat.Result.RoleDemand) == 0 {
		return
	}
	for _, e := range eligible {
		if e.Name != sat.Name && allocation.Eligible(e) && len(e.Result.RoleDemand) == 0 {
			ctrl.LoggerFrom(ctx).Info("listed analyzer reports no per-role demand, so it cannot size this P/D model",
				"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace, "analyzer", e.Name)
		}
	}
}

// roleNeed is what one role's variants asked of the composite: whether
// saturation contributed for any of them, and the largest demand another
// analyzer's need comes to, priced at saturation's own PRC of the variant
// the need was read for.
type roleNeed struct {
	saturation bool
	others     float64
	othersOK   bool
}

// add folds in one variant's decision. A need on a variant saturation cannot
// price (PRC <= 0), or one whose demand is not finite, is no reading.
func (n roleNeed) add(d allocation.Decision, prc float64) roleNeed {
	n.saturation = n.saturation || d.Saturation
	if !d.OthersOK || prc <= 0 {
		return n
	}
	if demand := d.Others * prc; !math.IsInf(demand, 0) && (!n.othersOK || demand > n.others) {
		n.others, n.othersOK = demand, true
	}
	return n
}

// sized is whether a listed analyzer set this role's demand this cycle.
func (n roleNeed) sized() bool {
	return n.saturation || n.othersOK
}

// scaleDemand sets each sized role's demand in result to what the composite
// decided, in saturation's units: D_sat[role] when only saturation sized it,
// the other analyzers' largest when saturation did not, and the larger of the
// two when both did.
//
// The model's TotalDemand moves by the same amount (or is the role's demand,
// for an aggregated model), and the role's variants share the new demand in
// the proportions saturation attributed it (evenly when it attributed none),
// with their utilization recomputed from it.
//
// HeldRoleDemand is saturation's demand before its own hold. It is kept for a
// role saturation sized, raised to the other analyzers' demand when theirs is
// larger (a hold on saturation's figure does not lower theirs), and dropped
// for a role saturation did not size, where its hold did not set the demand.
func scaleDemand(result *domain.AnalyzerResult, satDemand map[string]float64, needs map[string]roleNeed) {
	for role, n := range needs {
		if !n.sized() {
			continue
		}
		if before, held := result.HeldRoleDemand[role]; held {
			switch {
			case !n.saturation:
				delete(result.HeldRoleDemand, role)
			case n.othersOK:
				result.HeldRoleDemand[role] = max(before, n.others)
			}
		}
		target := n.others
		if n.saturation {
			target = satDemand[role]
			if n.othersOK {
				target = max(target, n.others)
			}
		}
		if target == satDemand[role] {
			continue
		}
		if result.RoleDemand == nil {
			result.TotalDemand = target
		} else {
			result.RoleDemand[role] = target
			result.TotalDemand += target - satDemand[role]
		}
		redistributeVariantDemand(result.VariantCapacities, role, target)
	}
}

// redistributeVariantDemand sets the TotalDemand of role's variants in vcs to
// sum to demand, in the proportions they already hold (evenly when they hold
// none), and recomputes each one's utilization from it.
func redistributeVariantDemand(vcs []domain.VariantCapacity, role string, demand float64) {
	var sum float64
	var count int
	for _, vc := range vcs {
		if domain.RoleOfVC(vc) == role {
			sum += vc.TotalDemand
			count++
		}
	}
	for i := range vcs {
		vc := &vcs[i]
		if domain.RoleOfVC(*vc) != role {
			continue
		}
		if sum > 0 {
			vc.TotalDemand = demand * vc.TotalDemand / sum
		} else {
			vc.TotalDemand = demand / float64(count)
		}
		vc.Utilization = 0
		if vc.ReplicaCount > 0 && vc.PerReplicaCapacity > 0 {
			vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * vc.PerReplicaCapacity)
		}
	}
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
