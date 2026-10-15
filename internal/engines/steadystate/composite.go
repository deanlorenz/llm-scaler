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
// decides: each role's demand, the model's, each variant's share of it, its
// per-replica capacity (PRC) where another analyzer decided it, its
// utilization, and its Reason, which names the decision path.
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
// Each role is then priced in one frame, saturation's or the other analyzers',
// whichever needs more replicas for the fleet the role runs (priceRole). In
// the others' frame the role's demand, in saturation's units, is the largest
// N x PRC_sat over the variants they size, defined when saturation sees the
// role as idle too, and each variant they size is priced at D / N so it needs
// exactly the N they asked.
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
	if len(eligibleAnalyzers) == 0 {
		logger.Info("no listed analyzer can size this model this cycle, so it holds where it is",
			"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace)
	}

	satDemand := make(map[string]float64)
	for _, role := range rolesPresent(sat.Result.VariantCapacities) {
		if d, ok := aggregation.DemandForRole(sat.Result, role); ok {
			satDemand[role] = d
		}
	}

	byRole := make(map[string][]variantNeed)
	contributors := make(map[string]bool)
	compositeVCs := make([]domain.VariantCapacity, 0, len(sat.Result.VariantCapacities))
	decisions := make([]allocation.Decision, 0, len(sat.Result.VariantCapacities))
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
			byRole[role] = append(byRole[role], newVariantNeed(len(compositeVCs), vc, d))
		}

		logger.V(logging.VERBOSE).Info("composite-contributors",
			"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace,
			"variant", vc.VariantName, "decisionPath", d.Path,
			"contributors", d.Contributors, "totalReplicas", d.N)
		compositeVCs = append(compositeVCs, vc)
		decisions = append(decisions, d)
	}

	result := *sat.Result
	result.AnalyzerName = allocation.CompositeSignalName
	result.VariantCapacities = compositeVCs
	result.RoleDemand = maps.Clone(sat.Result.RoleDemand)
	result.HeldRoleDemand = maps.Clone(sat.Result.HeldRoleDemand)
	result.RoleStartSeconds = maps.Clone(sat.Result.RoleStartSeconds)
	result.VariantBusiestQueueLoad = maps.Clone(sat.Result.VariantBusiestQueueLoad)
	roles := rolesPresent(sat.Result.VariantCapacities)
	sized := make(map[string]bool, len(byRole))
	for _, role := range roles {
		vs, ok := byRole[role]
		if !ok {
			continue
		}
		s, dropped := priceRole(&result, role, satDemand[role], vs)
		sized[role] = s
		for _, i := range dropped {
			logger.Info("a listed analyzer's need is dropped: saturation has no per-replica capacity for the variant",
				"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace,
				"variant", result.VariantCapacities[i].VariantName, "totalReplicas", decisions[i].Others)
		}
	}
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

// variantNeed is one variant's need as each side reads it: saturation's,
// and the largest among the other listed analyzers'.
type variantNeed struct {
	index    int
	sat      float64
	satOK    bool
	others   float64
	othersOK bool
	replicas float64
	// prc is saturation's per-replica capacity for the variant.
	prc float64
}

// newVariantNeed reads one variant's decision. An others' need whose demand
// at saturation's PRC overflows is no reading.
func newVariantNeed(i int, vc domain.VariantCapacity, d allocation.Decision) variantNeed {
	others := d.OthersOK && (!(vc.PerReplicaCapacity > 0) || !math.IsInf(d.Others*vc.PerReplicaCapacity, 0))
	return variantNeed{
		index: i, sat: d.SaturationN, satOK: d.Saturation, others: d.Others, othersOK: others,
		replicas: float64(vc.ReplicaCount), prc: vc.PerReplicaCapacity,
	}
}

// coverage is how much of a need the role's running replicas cover: the sum
// over its variants of w / N, where N is the variant's need under need and w
// its replicas (1 each when none of them has replicas, so a role scaling from
// zero compares the needs themselves). A variant need does not define is left
// out; a zero need is covered however few replicas there are. The lower the
// coverage, the larger the need.
func coverage(vs []variantNeed, need func(variantNeed) (float64, bool)) float64 {
	weighted := slices.ContainsFunc(vs, func(v variantNeed) bool { return v.replicas > 0 })
	var c float64
	for _, v := range vs {
		n, ok := need(v)
		w := 1.0
		if weighted {
			w = v.replicas
		}
		if !ok || w == 0 {
			continue
		}
		c += w / n
	}
	return c
}

// priceRole prices one role of result, whose saturation demand is dSat and
// whose variants' needs are vs, and reports whether a listed analyzer sized
// it. It returns the index of each variant whose need another analyzer
// defined but the role could not carry: saturation has no PRC for it and the
// role is priced in saturation's units.
//
// The role is priced in one frame, saturation's or the other analyzers',
// whichever needs more replicas for the fleet the role runs (lower coverage;
// saturation's on a tie, so it stays exact). Mixing the two per variant asked
// for more than either: with saturation deciding one accelerator and another
// analyzer the other, the role was short by more than either analyzer's need.
//
//   - Saturation's frame: saturation's demand and PRCs stand. Its
//     HeldRoleDemand (its demand before its own hold) is raised to the
//     others' need in its units, D_sat x coverage_sat / coverage_others, and
//     dropped when that reaches D_sat: the hold no longer sets the demand, and
//     kept the share optimizer would give nothing from the role.
//   - The others' frame: the role's demand is the largest N x PRC_sat over the
//     variants they size, which is defined when saturation sees the role as
//     idle, so another analyzer can grow it. Each variant they size is priced
//     at D / N, so it needs exactly the N they asked, and variants on different
//     accelerators keep their view of the relative worth; a variant they do not
//     size keeps saturation's PRC, and so saturation's ratio to the others. The
//     model's TotalDemand moves with the role, the role's variants share it in
//     saturation's proportions, and HeldRoleDemand is dropped: saturation's
//     hold did not set the demand.
func priceRole(result *domain.AnalyzerResult, role string, dSat float64, vs []variantNeed) (sized bool, dropped []int) {
	satAny := slices.ContainsFunc(vs, func(v variantNeed) bool { return v.satOK })
	target, othersAny := 0.0, false
	for _, v := range vs {
		if v.othersOK && v.prc > 0 && (!othersAny || v.others*v.prc > target) {
			target, othersAny = v.others*v.prc, true
		}
	}
	satNeed := func(v variantNeed) (float64, bool) { return v.sat, v.satOK }
	othersNeed := func(v variantNeed) (float64, bool) {
		if v.othersOK {
			return v.others, true
		}
		if v.prc > 0 {
			return target / v.prc, true
		}
		return 0, false
	}
	useOthers := othersAny && (!satAny || coverage(vs, othersNeed) < coverage(vs, satNeed))
	if !useOthers {
		for _, v := range vs {
			if v.othersOK && !(v.prc > 0) {
				dropped = append(dropped, v.index)
			}
		}
		if !satAny {
			return false, dropped
		}
		if before, held := result.HeldRoleDemand[role]; held && othersAny {
			equiv := dSat * coverage(vs, satNeed) / coverage(vs, othersNeed)
			switch {
			case equiv >= dSat:
				delete(result.HeldRoleDemand, role)
			case equiv > before:
				result.HeldRoleDemand[role] = equiv
			}
		}
		return true, dropped
	}

	delete(result.HeldRoleDemand, role)
	if result.RoleDemand == nil {
		result.TotalDemand = target
	} else {
		result.RoleDemand[role] = target
		result.TotalDemand += target - dSat
	}
	redistributeVariantDemand(result.VariantCapacities, role, target)
	for _, v := range vs {
		if !v.othersOK || !(v.others > 0) || !(target > 0) {
			continue
		}
		vc := &result.VariantCapacities[v.index]
		vc.PerReplicaCapacity = target / v.others
		vc.Utilization = 0
		if vc.ReplicaCount > 0 {
			vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * vc.PerReplicaCapacity)
		}
	}
	return true, nil
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
		// On P/D a role that cannot grow stops the other growing too: decode
		// grown against a frozen prefill is capacity prefill cannot feed. The
		// share optimizer freezes such a model whole for the same reason.
		if len(holds) > 0 && len(roles) > 1 {
			for _, role := range roles {
				h := holds[role]
				h.NoScaleUp = true
				holds[role] = h
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
