package aggregation

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// PRCCom returns PRC_com(SO) = D_sat[role(SO)] / N_com(SO) (spec §4.4, §5.3):
// the per-SO capacity implied by the aggregated replica count, holding
// demand fixed at saturation's own value (the D_sat unit, spec §4.4) rather
// than converting it.
//
// satDemand must be saturation's *own* AnalyzerResult (its TotalDemand/
// RoleDemand define D_sat — the unit every composite value is expressed in),
// even when saturation did not itself contribute to N_com (spec §5.1: unit
// and contribution are separate roles). role is the SO's role, already
// resolved by the caller (e.g. from the analyzer result that carried this
// variant — resolveSO's callers know it from roleOf).
//
// Undefined (ok == false) when nCom <= 0 (spec §4.4: "PRC_com(SO) is
// undefined when N_com(SO) == 0") or when D_sat has no defined demand for
// role at all (A4 — silence is not zero). A defined D_sat of exactly 0 with
// nCom > 0 yields PRC_com == 0 exactly, not undefined — the SO
// self-consistently needs no capacity for a role with no demand.
func PRCCom(satDemand *domain.AnalyzerResult, role string, nCom float64, nComOK bool) (prc float64, ok bool) {
	if !nComOK || nCom <= 0 {
		return 0, false
	}
	demand, present := DemandForRole(satDemand, role)
	if !present {
		return 0, false
	}
	return demand / nCom, true
}
