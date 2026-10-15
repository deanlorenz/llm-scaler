package aggregation

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// DemandForRole returns result's demand for role, and whether that demand is
// present at all.
//
// An AnalyzerResult stores its per-role demand in one of two layouts: when the
// model is not P/D-disaggregated, RoleDemand is nil and the "both" demand
// lives in TotalDemand instead of under a "both" key; when disaggregated,
// RoleDemand is keyed by the roles the analyzer attributed demand to. An empty
// role string is canonicalized to domain.RoleBoth, matching AggregateByRole's
// canonicalization. Every aggregation must read demand through this accessor
// rather than indexing RoleDemand directly, or it silently breaks on the nil
// layout.
//
// present distinguishes three states a caller must not conflate:
//   - role absent from a non-nil RoleDemand: not present (analyzer said
//     nothing about this role — domain.go's A4: silence is not zero).
//   - role present with value 0: present, value 0 (an ordinary, ordinary zero
//     demand — never treated as absent).
//   - RoleDemand nil and role canonicalizes to domain.RoleBoth: present, value
//     from TotalDemand (the non-disaggregated layout).
//   - RoleDemand nil and role does NOT canonicalize to domain.RoleBoth (e.g.
//     "prefill" asked of a non-disaggregated result): not present — a
//     non-disaggregated analyzer never had an opinion about that role.
func DemandForRole(result *domain.AnalyzerResult, role string) (value float64, present bool) {
	if role == "" {
		role = domain.RoleBoth
	}
	if result.RoleDemand == nil {
		if role == domain.RoleBoth {
			return result.TotalDemand, true
		}
		return 0, false
	}
	value, present = result.RoleDemand[role]
	return value, present
}
