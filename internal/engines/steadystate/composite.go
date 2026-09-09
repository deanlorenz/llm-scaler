package steadystate

import (
	"context"
	"sort"
	"time"

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
// The composite is built fresh, never aliasing any source entry's reference
// fields (spec §2.6/A15): every VariantCapacities entry, RoleDemand and
// RoleCapacities map is a new value, so mutating the composite (e.g. the
// optimizer's allocation bookkeeping) cannot reach back into any analyzer's
// own result.
func buildComposite(
	ctx context.Context,
	namedResults []allocation.NamedAnalyzerResult,
	scaleUp, scaleDown float64,
) allocation.NamedAnalyzerResult {
	sat := findSaturation(namedResults)
	// findSaturation returning nil is unreachable on the production path
	// (see doc comment above) but every unit test builds its own slice, and
	// a slice with no saturation entry at all has no D_sat to express
	// anything in — there is nothing to compose. Report the same way a
	// missing signal always has: an empty, non-nil Result whose
	// informativeness is false, so allocation.HasUsableCompositeSignal
	// correctly reports "no usable signal" rather than the caller having to
	// special-case a nil composite Result it never expected.
	if sat == nil || sat.Result == nil {
		return emptyComposite()
	}

	variants := unionOfVariants(namedResults)
	sort.Strings(variants) // deterministic iteration for stable provenance/logging

	compositeVCs := make([]domain.VariantCapacity, 0, len(variants))
	roleDemandSeen := make(map[string]struct{})
	contributedNames := make(map[string]struct{})
	anyLive := false

	for _, v := range variants {
		sourceVC, role := representativeVariantCapacity(namedResults, v)
		decision := allocation.ResolveSO(namedResults, v)

		vc := domain.VariantCapacity{
			VariantName: v,
			Role:        role,
			Reason:      decision.Path,
		}
		if sourceVC != nil {
			// Metadata the composite does not derive itself: identity,
			// scale-target-clamped counts already reconciled by the
			// per-analyzer capacity-build step, and the representative
			// source's own per-variant demand (see TotalDemand/Utilization
			// below — this is a per-SO figure, distinct from D_sat[role],
			// which is shared by every SO in that role and would double-count
			// if copied onto each of them). Copied by value (all plain
			// fields), never aliased.
			vc.ReplicaCount = sourceVC.ReplicaCount
			vc.PendingReplicas = sourceVC.PendingReplicas
			vc.WarmPoolReplicas = sourceVC.WarmPoolReplicas
			vc.WarmPoolPerReplicaCapacity = sourceVC.WarmPoolPerReplicaCapacity
			vc.TotalDemand = sourceVC.TotalDemand
		}

		if decision.OK {
			for _, name := range decision.Contributors {
				contributedNames[name] = struct{}{}
			}
			anyLive = true
		}

		prcCom, prcOK := aggregation.PRCCom(sat.Result, role, decision.N, decision.OK)
		switch {
		case prcOK:
			vc.PerReplicaCapacity = prcCom
		case sourceVC != nil:
			// PRC_com is undefined exactly when N_com is 0 (no demand for
			// this role — spec §4.4, "nothing needed") or D_sat itself has
			// no defined demand for the role (A4). Neither means the SO's
			// real, measured capacity has vanished — PRC and demand are
			// independent (spec §2.4), and a zero-demand SO can still hold
			// real supply that a scale-down decision needs to see. Fall
			// through to the SO's own analyzer-measured PRC rather than
			// leaving 0, which would silently erase that supply. This is
			// what makes the sat-only identity (test 1) hold even for a
			// zero-demand SO: today's namedResults[0] copy never zeroes a
			// real PRC because demand happens to be 0, so neither may the
			// composite.
			vc.PerReplicaCapacity = sourceVC.PerReplicaCapacity
			prcCom = sourceVC.PerReplicaCapacity
		}
		// Utilization mirrors the analyzer's own definition
		// (totalDemand/totalCapacity, saturation_v2/analyzer.go) but with
		// PRC_com substituted for the source's own PRC — the same
		// substitution §5.3 makes for RC/SC. Recomputed rather than copied
		// so it stays consistent with whatever PerReplicaCapacity ends up
		// being (PRC_com when defined, the source's own PRC otherwise, in
		// which case this recomputation reduces to the source's own value
		// exactly — sat-only identity, test 1).
		if prcCom > 0 && vc.ReplicaCount > 0 {
			vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * prcCom)
		}

		if role != domain.RoleBoth {
			if _, present := aggregation.DemandForRole(sat.Result, role); present {
				roleDemandSeen[role] = struct{}{}
			}
		}

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
// A non-nil, non-informative Result so allocation.HasUsableCompositeSignal
// reports false through its ordinary path rather than the nil-Result path,
// keeping every consumer's behavior identical regardless of which "no
// signal" shape produced it.
func emptyComposite() allocation.NamedAnalyzerResult {
	return allocation.NamedAnalyzerResult{
		Name: allocation.CompositeSignalName,
		Result: &domain.AnalyzerResult{
			AnalyzerName: allocation.CompositeSignalName,
		},
	}
}

// unionOfVariants returns the distinct variant names across every analyzer
// result's VariantCapacities. In the well-formed case every analyzer sees the
// same discovery-joined variant set (buildCapacities overlays the same
// metaByVariant onto each), so this is normally just saturation's own set;
// the union is taken anyway so an analyzer-specific variant is never
// silently dropped from composition.
func unionOfVariants(namedResults []allocation.NamedAnalyzerResult) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, nr := range namedResults {
		if nr.Result == nil {
			continue
		}
		for _, vc := range nr.Result.VariantCapacities {
			if _, ok := seen[vc.VariantName]; !ok {
				seen[vc.VariantName] = struct{}{}
				out = append(out, vc.VariantName)
			}
		}
	}
	return out
}

// representativeVariantCapacity returns variant's VariantCapacity from
// saturation's result when present (saturation is the keeper of per-variant
// identity metadata -- see runAnalyzersAndScore's doc comment), else from the
// first other analyzer result that has it, plus its canonicalized role. Nil
// if no analyzer reports this variant at all (reachable only via a caller
// bug, since variant always comes from unionOfVariants).
func representativeVariantCapacity(namedResults []allocation.NamedAnalyzerResult, variant string) (*domain.VariantCapacity, string) {
	var sat *allocation.NamedAnalyzerResult
	for i := range namedResults {
		if namedResults[i].Name == domain.SaturationAnalyzerName {
			sat = &namedResults[i]
			break
		}
	}
	if sat != nil && sat.Result != nil {
		for i := range sat.Result.VariantCapacities {
			if sat.Result.VariantCapacities[i].VariantName == variant {
				vc := sat.Result.VariantCapacities[i]
				return &vc, roleOfVC(vc)
			}
		}
	}
	for _, nr := range namedResults {
		if nr.Result == nil {
			continue
		}
		for i := range nr.Result.VariantCapacities {
			if nr.Result.VariantCapacities[i].VariantName == variant {
				vc := nr.Result.VariantCapacities[i]
				return &vc, roleOfVC(vc)
			}
		}
	}
	return nil, domain.RoleBoth
}

// roleOfVC canonicalizes a VariantCapacity's role, matching
// aggregation.AggregateByRole's convention.
func roleOfVC(vc domain.VariantCapacity) string {
	if vc.Role == "" {
		return domain.RoleBoth
	}
	return vc.Role
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
