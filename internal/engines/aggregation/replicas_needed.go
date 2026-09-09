package aggregation

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// roleOf canonicalizes a VariantCapacity's role, matching AggregateByRole's
// convention: an empty role is domain.RoleBoth.
func roleOf(vc domain.VariantCapacity) string {
	if vc.Role == "" {
		return domain.RoleBoth
	}
	return vc.Role
}

// variantCapacity returns the VariantCapacity for variant in result, and
// whether it is present at all. A variant absent from VariantCapacities means
// this analyzer never measured it — distinct from measuring it at PRC 0.
func variantCapacity(result *domain.AnalyzerResult, variant string) (vc domain.VariantCapacity, present bool) {
	for _, c := range result.VariantCapacities {
		if c.VariantName == variant {
			return c, true
		}
	}
	return domain.VariantCapacity{}, false
}

// replicasNeeded returns N_i(SO) = D_i[role(SO)] / PRC_i(SO) — the replica
// count one analyzer's result implies variant needs to cover its role's
// entire demand (spec §5.2). Unit-free: demand and PRC both come from the
// same analyzer, in that analyzer's own units (spec §4.2).
//
// Undefined (ok == false, per the §2.5/A14 convention) when:
//   - the variant is absent from result's VariantCapacities (never measured);
//   - PRC_i(SO) <= 0 (spec §2.5 — division by a zero or negative capacity is
//     meaningless, not a large number);
//   - the analyzer's demand for the variant's role is not present at all
//     (spec §5.4/A4 — an analyzer silent about a role does not participate
//     in it; distinct from present-and-zero, which IS defined, see below).
//
// A defined demand of exactly 0 with a positive PRC yields N_i(SO) == 0 —
// legitimate (spec test 21: PRC>0 with demand==0 is explicitly legal), not
// undefined. Zero demand is never manufactured into a non-zero placeholder.
func replicasNeeded(result *domain.AnalyzerResult, variant string) (n float64, ok bool) {
	if result == nil {
		return 0, false
	}
	vc, present := variantCapacity(result, variant)
	if !present {
		return 0, false
	}
	if vc.PerReplicaCapacity <= 0 {
		return 0, false
	}
	demand, present := demandForRole(result, roleOf(vc))
	if !present {
		return 0, false
	}
	return demand / vc.PerReplicaCapacity, true
}

// AggN aggregates N_i(SO) across contributing analyzers for one variant
// (spec §5.2, §7.1): a pure max over the defined per-analyzer values,
// skipping undefined ones per A14. Named for the aggregated quantity, not the
// operation, so a future change to the combination rule (spec §7 records the
// user's outlier-rejection intuition for later) is contained to this one
// function.
//
// Callers pass only the results of analyzers already known to be eligible
// (spec §5.1.1's eligible()) and contributing (their N for this variant is
// defined) — AggN itself does not re-check eligibility; it aggregates
// whatever it is given and represents "nothing to aggregate" as ok == false.
//
// N stays continuous here: no ceil() is applied. Quantizing to a replica
// count happens exactly once, downstream, where a replica count is finally
// needed (spec §5.2, A2) — aggregating pre-ceiled values would double-round.
func AggN(results []*domain.AnalyzerResult, variant string) (n float64, ok bool) {
	return maxOfDefined(len(results), func(i int) (float64, bool) {
		return replicasNeeded(results[i], variant)
	})
}
