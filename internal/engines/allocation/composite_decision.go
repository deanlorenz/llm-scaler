package allocation

import (
	"math"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
)

// DecisionPath categorizes how one SO's CompositeTotalReplicas was reached
// (spec §5.1.2, A19'): free text naming how one SO's N_com was reached,
// mirroring domain.VariantCapacity.Reason's style for analyzers. Logged per
// SO through the same function analyzers use (spec §6), so a decision is
// traceable end to end.
//
// There is deliberately no C3-default-prc: per spec §5.1.4/D2, saturation's
// existing P0-store ladder is the only source of estimated PRCs for an idle
// or never-seen-before SO, and it flows through as an ordinary contribution
// (A27/A28) rather than as a separate composite-level default mechanism.
type DecisionPath string

const (
	// DecisionAgree means multiple contributors produced a defined N(SO);
	// the max was taken over more than one of them.
	DecisionAgree DecisionPath = "C0-agree"
	// DecisionSingle means exactly one analyzer contributed a defined N(SO).
	DecisionSingle DecisionPath = "C1-single"
	// DecisionSatFallback means no non-saturation analyzer contributed a
	// defined N(SO) for this SO, and saturation itself was eligible, so its
	// N(SO) was used as the fallback (spec §5.1: saturation is a fallback,
	// not a floor — it participates here only because nothing else did).
	DecisionSatFallback DecisionPath = "C2-sat-fallback"
	// DecisionNoSignal means no analyzer at all — including saturation —
	// produced a defined N(SO) for this SO. There is no usable signal, and
	// per spec §5.1.3 this must not be treated as a scale-to-zero decision;
	// it is "no evidence", handled by the gate in composite_signal_gate.go.
	DecisionNoSignal DecisionPath = "C4-no-signal"
)

// TotalReplicas returns D_i[role]/PRC_i(SO) — analyzer i's own measured demand
// for this SO's role, divided by analyzer i's own measured PRC for this SO.
// Both values are analyzer i's own data; never sat's, never the composite's.
// ok is false when vc.PerReplicaCapacity <= 0 or the analyzer has no demand
// entry for the role at all.
func TotalReplicas(result *domain.AnalyzerResult, vc domain.VariantCapacity) (n float64, ok bool) {
	if result == nil {
		return 0, false
	}
	if vc.PerReplicaCapacity <= 0 {
		return 0, false
	}
	demand, present := aggregation.DemandForRole(result, domain.RoleOfVC(vc))
	if !present {
		return 0, false
	}
	return demand / vc.PerReplicaCapacity, true
}

// Decision is how the composite reached one variant's replica need.
type Decision struct {
	// N is the replica need, in saturation's demand units; defined only when
	// OK.
	N  float64
	OK bool
	// Path names the rule that produced N.
	Path DecisionPath
	// Contributors names every analyzer whose need was considered.
	Contributors []string
	// FromSaturation is whether saturation's own need is N, so saturation's
	// own per-replica capacity already prices it exactly.
	FromSaturation bool
}

// ResolveSO decides one variant's replica need from entries, the analyzers
// eligible to contribute, falling back to sat when none does. Each eligible
// analyzer with a finite, non-negative TotalReplicas for the variant
// contributes, and N is the largest. A non-finite need is no reading: one
// analyzer's +Inf demand would otherwise price every replica at zero.
func ResolveSO(entries []NamedAnalyzerResult, sat *NamedAnalyzerResult, variant string) Decision {
	var d Decision
	for _, e := range entries {
		if !Eligible(e) {
			continue
		}
		evc, present := FindVariantCapacity(e.Result, variant)
		if !present || evc.Reason == ReasonNoData || evc.Reason == ReasonError {
			continue
		}
		n, ok := finiteNeed(e.Result, evc)
		if !ok {
			continue
		}
		d.Contributors = append(d.Contributors, e.Name)
		switch {
		case !d.OK || n > d.N:
			d.N, d.OK = n, true
			d.FromSaturation = sat != nil && e.Name == sat.Name
		case n == d.N && sat != nil && e.Name == sat.Name:
			d.FromSaturation = true
		}
	}
	switch len(d.Contributors) {
	case 0:
		d.Path = DecisionNoSignal
		if sat == nil || !Eligible(*sat) {
			return d
		}
		satVC, present := FindVariantCapacity(sat.Result, variant)
		if !present {
			return d
		}
		if n, ok := finiteNeed(sat.Result, satVC); ok {
			return Decision{N: n, OK: true, Path: DecisionSatFallback, Contributors: []string{sat.Name}, FromSaturation: true}
		}
		return d
	case 1:
		d.Path = DecisionSingle
	default:
		d.Path = DecisionAgree
	}
	return d
}

// finiteNeed is TotalReplicas when it is finite and not negative.
func finiteNeed(result *domain.AnalyzerResult, vc domain.VariantCapacity) (float64, bool) {
	n, ok := TotalReplicas(result, vc)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	return n, true
}

// FindVariantCapacity returns variant's VariantCapacity from result, and
// whether it is present.
func FindVariantCapacity(result *domain.AnalyzerResult, variant string) (domain.VariantCapacity, bool) {
	if result == nil {
		return domain.VariantCapacity{}, false
	}
	for _, vc := range result.VariantCapacities {
		if vc.VariantName == variant {
			return vc, true
		}
	}
	return domain.VariantCapacity{}, false
}
