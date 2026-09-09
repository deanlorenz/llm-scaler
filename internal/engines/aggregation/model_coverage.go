package aggregation

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"

// modelCoverageFromRoles returns coverage(M) = min(cov(prefill), cov(decode))
// + cov(both) (spec §5.4) — the only relation this mission defines between
// per-role and model-level coverage.
//
// byRole holds each role's own coverage, keyed by domain.RoleBoth/"prefill"/
// "decode". A role's ABSENCE from byRole means its coverage is undefined
// (A14) — the caller never stores a spurious 0 for a role it has no defined
// coverage for — and an absent role must not enter the min as a spurious 0
// (which would wrongly report a partially-covered model as fully uncovered)
// nor be skipped in a way that silently drops the min's other operand.
//
// A purely disaggregated model has no "both" entry, so cov(both) contributes
// 0 to the sum by construction (its zero value) — correct, since a
// disaggregated model's coverage is entirely the min(prefill, decode) term.
// A non-disaggregated model has only a "both" entry, so the min term is
// over zero defined values (returns not-ok, contributing 0) and the sum
// reduces to cov(both) alone.
//
// Returns ok == false only when NEITHER role pair nor "both" has a defined
// coverage at all — i.e. byRole is empty or has no recognizable role keys.
func modelCoverageFromRoles(byRole map[string]float64) (coverage float64, ok bool) {
	prefill, prefillOK := byRole["prefill"]
	decode, decodeOK := byRole["decode"]
	both, bothOK := byRole[domain.RoleBoth]

	minRolePair, minOK := minOfDefined(2, func(i int) (float64, bool) {
		if i == 0 {
			return prefill, prefillOK
		}
		return decode, decodeOK
	})
	if !minOK {
		minRolePair = 0
	}

	if !minOK && !bothOK {
		return 0, false
	}
	return minRolePair + both, true
}
