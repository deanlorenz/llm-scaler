package steadystate

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// buildComposite reduces namedResults (the full per-analyzer slice
// runAnalyzersAndScore produces) into the single composite
// allocation.NamedAnalyzerResult the optimizer consumes, per spec §6.1: every
// enabled analyzer's demand influences the result, not just saturation's.
//
// Saturation's own entry is always present and its Result always non-nil by
// construction (runAnalyzersAndScore returns an error before building any
// entry if saturation's analysis itself failed) — so D_sat, the unit every
// composite value is expressed in (spec §4.4), is always available even when
// saturation is not itself an eligible contributor.
//
// namedResults is used to find sat (the identity/unit source) and for
// maxScore; eligibleAnalyzers (resolved once by the caller, per
// composite-signal-redesign.md §2.1 — sat's enabled/disabled state is never
// re-tested inside this function or its per-SO loop) is used only for the
// per-SO contributor collection below.
//
// The composite iterates sat's own VariantCapacities directly (no union of
// every analyzer's variants, no fallback source) — sat is always the
// identity/shape source for every SO in the composite.
//
// The composite is built fresh, never aliasing any source entry's reference
// fields (spec §2.6/A15): every VariantCapacities entry, RoleDemand and
// RoleCapacities map is a new value, so mutating the composite (e.g. the
// optimizer's allocation bookkeeping) cannot reach back into any analyzer's
// own result.
func buildComposite(
	ctx context.Context,
	namedResults, eligibleAnalyzers []allocation.NamedAnalyzerResult,
	scaleUp, scaleDown float64,
) allocation.NamedAnalyzerResult {
	sat := findSaturation(namedResults)
	// findSaturation returning nil is unreachable on the production path
	// (see doc comment above) but every unit test builds its own slice, and
	// a slice with no saturation entry at all has no D_sat to express
	// anything in — there is nothing to compose. Report the same way a
	// missing signal always has: an empty, non-nil Result whose
	// informativeness is false, so allocation.CompositeHasSignal correctly
	// reports "no usable signal" rather than the caller having to
	// special-case a nil composite Result it never expected.
	if sat == nil || sat.Result == nil {
		return emptyComposite()
	}

	// demandByRole is computed once, before the per-SO loop, since it is the
	// shared numerator every SO in a given role divides into (step 6).
	demandByRole := make(map[string]float64)
	for _, role := range rolesPresent(sat.Result.VariantCapacities) {
		d, _ := aggregation.DemandForRole(sat.Result, role)
		demandByRole[role] = d
	}

	compositeVCs := make([]domain.VariantCapacity, 0, len(sat.Result.VariantCapacities))
	roleDemandSeen := make(map[string]struct{})
	anyLive := false

	for i := range sat.Result.VariantCapacities {
		sourceVC := &sat.Result.VariantCapacities[i]

		vc := domain.VariantCapacity{
			VariantName: sourceVC.VariantName,
			Role:        sourceVC.Role,
		}
		if sourceVC != nil {
			// Metadata the composite does not derive itself: identity,
			// scale-target-clamped counts already reconciled by the
			// per-analyzer capacity-build step, and sat's own per-variant
			// demand (see TotalDemand/Utilization below — this is a per-SO
			// figure, distinct from D_sat[role], which is shared by every SO
			// in that role and would double-count if copied onto each of
			// them). Copied by value (all plain fields), never aliased. This
			// copy is UNCONDITIONAL and happens even if sat was excluded
			// from eligibleAnalyzers above — identity role, never gated.
			vc.ReplicaCount = sourceVC.ReplicaCount
			vc.PendingReplicas = sourceVC.PendingReplicas
			vc.WarmPoolReplicas = sourceVC.WarmPoolReplicas
			vc.WarmPoolPerReplicaCapacity = sourceVC.WarmPoolPerReplicaCapacity
			// ReplicaCount here is sat's raw k8s ready count, not a count of
			// replicas verified to be usefully serving. Accepted for now;
			// Supply/AnticipatedSupply (buildCapacities) are built from this
			// value as-is.
			vc.TotalDemand = sourceVC.TotalDemand
		}

		// Contributor collection: every eligible analyzer with a defined
		// TotalReplicas for this SO (spec §2.1b).
		type contribution struct {
			name string
			n    float64
		}
		var contributors []contribution
		for _, e := range eligibleAnalyzers {
			if !allocation.Eligible(e) {
				// Stale, uninformative, or nil-Result analyzers contribute to
				// nothing — unchanged from today's ResolveSO/eligible()
				// pairing (redesign §2.1(b)(i)). This is a per-analyzer gate,
				// checked once per analyzer per SO here; it is NOT the same
				// thing as the per-SO Reason check below, which is
				// per-contribution.
				continue
			}
			evc, present := findVariantCapacity(e.Result, vc.VariantName)
			if !present || evc.Reason == allocation.ReasonNoData || evc.Reason == allocation.ReasonError {
				continue
			}
			n, ok := allocation.TotalReplicas(e.Result, evc)
			if !ok {
				continue
			}
			contributors = append(contributors, contribution{name: e.Name, n: n})
		}

		var compositeTotalReplicas float64
		var decisionPath allocation.DecisionPath
		var contributorNames []string
		switch {
		case len(contributors) == 0:
			// sat-fallback: only reachable when sat was excluded from
			// eligibleAnalyzers upstream (step 1) for being disabled, AND no
			// other analyzer contributed, AND sat itself is Eligible (an
			// actual, live, informative result). A sat with no real result
			// (nil, erroring, no-data, or stale) must never produce a
			// fallback value.
			satVC, present := findVariantCapacity(sat.Result, vc.VariantName)
			if satN, ok := allocation.TotalReplicas(sat.Result, satVC); allocation.Eligible(*sat) && present && ok {
				compositeTotalReplicas, decisionPath, contributorNames = satN, allocation.DecisionSatFallback, []string{sat.Name}
			} else {
				decisionPath = allocation.DecisionNoSignal
			}
		case len(contributors) == 1:
			compositeTotalReplicas, decisionPath, contributorNames = contributors[0].n, allocation.DecisionSingle, []string{contributors[0].name}
		default:
			decisionPath = allocation.DecisionAgree
			for _, c := range contributors {
				contributorNames = append(contributorNames, c.name)
				if c.n > compositeTotalReplicas {
					compositeTotalReplicas = c.n
				}
			}
		}

		vc.Reason = string(decisionPath)
		if decisionPath != allocation.DecisionNoSignal {
			anyLive = true
		}

		// PRC(SO) = D_sat[role]/CompositeTotalReplicas(SO), inlined — the
		// entire computation, not a separate PRCCom-equivalent function.
		if compositeTotalReplicas > 0 {
			vc.PerReplicaCapacity = demandByRole[domain.RoleOfVC(vc)] / compositeTotalReplicas
		} else if sourceVC != nil {
			// CompositeTotalReplicas undefined/zero does not mean the SO's
			// real, measured capacity has vanished (PRC and demand are
			// independent, spec §2.4) — fall through to sat's own measured
			// PRC rather than leaving 0, which would silently erase real
			// supply. This is what makes the sat-only identity hold even for
			// a zero-demand SO.
			vc.PerReplicaCapacity = sourceVC.PerReplicaCapacity
		}

		// Utilization mirrors the analyzer's own definition
		// (totalDemand/totalCapacity, saturation_v2/analyzer.go) but with the
		// composite's PRC substituted for the source's own — the same
		// substitution spec §5.3 makes for RC/SC.
		if vc.PerReplicaCapacity > 0 && vc.ReplicaCount > 0 {
			vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * vc.PerReplicaCapacity)
		}

		role := domain.RoleOfVC(vc)
		if role != domain.RoleBoth {
			if _, present := aggregation.DemandForRole(sat.Result, role); present {
				roleDemandSeen[role] = struct{}{}
			}
		}

		// Composition-level logging, per SO — additive; does not replace
		// engine_v2.go's existing logAnalyzerResult call for the composite.
		ctrl.LoggerFrom(ctx).Info("composite-contributors",
			"modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace,
			"variant", vc.VariantName, "decisionPath", decisionPath,
			"contributors", contributorNames,
			"totalReplicas", compositeTotalReplicas,
		)

		compositeVCs = append(compositeVCs, vc)
	}

	result := &domain.AnalyzerResult{
		AnalyzerName:      allocation.CompositeSignalName,
		ModelID:           sat.Result.ModelID,
		Namespace:         sat.Result.Namespace,
		AnalyzedAt:        time.Now(),
		VariantCapacities: compositeVCs,
		TotalDemand:       sat.Result.TotalDemand,
	}
	if len(roleDemandSeen) > 0 {
		// Disaggregated: mirror D_sat's own layout (spec §2.3/A13/test 25) --
		// copy sat's RoleDemand by value into a fresh map, never aliasing it.
		result.RoleDemand = make(map[string]float64, len(sat.Result.RoleDemand))
		for role, demand := range sat.Result.RoleDemand {
			result.RoleDemand[role] = demand
		}
	}

	composite := allocation.NamedAnalyzerResult{
		Name:              allocation.CompositeSignalName,
		Result:            result,
		Score:             maxScore(namedResults),
		ScaleUpThreshold:  scaleUp,
		ScaleDownBoundary: scaleDown,
		Live:              anyLive,
	}
	buildCapacities(ctx, &composite, nil, scaleUp, scaleDown)
	composite.Remaining = composite.RequiredCapacity
	composite.Spare = composite.SpareCapacity

	return composite
}

// findSaturation returns the saturation entry from namedResults, or nil if
// absent (unreachable on the production path -- see buildComposite's doc
// comment -- but every test builds its own slice).
func findSaturation(namedResults []allocation.NamedAnalyzerResult) *allocation.NamedAnalyzerResult {
	for i := range namedResults {
		if namedResults[i].Name == domain.SaturationAnalyzerName {
			return &namedResults[i]
		}
	}
	return nil
}

// emptyComposite is the composite for a cycle with no saturation entry to
// express D_sat in at all (unreachable in production; see buildComposite).
// A non-nil, non-informative Result so allocation.CompositeHasSignal reports
// false through its ordinary path rather than the nil-Result path, keeping
// every consumer's behavior identical regardless of which "no signal" shape
// produced it.
func emptyComposite() allocation.NamedAnalyzerResult {
	return allocation.NamedAnalyzerResult{
		Name: allocation.CompositeSignalName,
		Result: &domain.AnalyzerResult{
			AnalyzerName: allocation.CompositeSignalName,
		},
	}
}

// findVariantCapacity returns variant's VariantCapacity from result, and
// whether it is present at all. This is the one place a by-name search
// remains: looking up a non-sat analyzer's own view of the SO the outer loop
// is currently iterating over (sat's own list) — distinct from the by-name
// search step 3 removed, which searched for an analyzer's own SO redundantly
// when the caller already had it in hand.
func findVariantCapacity(result *domain.AnalyzerResult, variantName string) (domain.VariantCapacity, bool) {
	if result == nil {
		return domain.VariantCapacity{}, false
	}
	for _, vc := range result.VariantCapacities {
		if vc.VariantName == variantName {
			return vc, true
		}
	}
	return domain.VariantCapacity{}, false
}

// rolesPresent returns the distinct roles across vcs, canonicalized and
// deduplicated via domain.RoleOfVC.
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

// maxScore is spec A9-triple-prime: the composite's legacy Score field is max over
// contributors' Scores, reducing to saturation's on the sat-only path.
// Nothing in the new aggregation reads it; it exists only because
// NamedAnalyzerResult.Score is part of the legacy struct (spec §8) and
// fairShareValue currently consumes it (a known, separate, out-of-scope bug
// per spec §7.0 -- Score should never have weighted cross-model priority).
func maxScore(namedResults []allocation.NamedAnalyzerResult) float64 {
	best := 0.0
	for i, nr := range namedResults {
		if i == 0 || nr.Score > best {
			best = nr.Score
		}
	}
	return best
}
