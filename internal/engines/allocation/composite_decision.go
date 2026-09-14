package allocation

import (
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
