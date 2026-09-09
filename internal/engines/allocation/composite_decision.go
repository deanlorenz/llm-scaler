package allocation

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
)

// Composite decision-path values (spec §5.1.2, A19'): free text naming how
// one SO's N_com was reached, mirroring domain.VariantCapacity.Reason's style
// for analyzers. Logged per SO through the same function analyzers use
// (spec §6), so a decision is traceable end to end.
//
// There is deliberately no C3-default-prc: per spec §5.1.4/D2, saturation's
// existing P0-store ladder is the only source of estimated PRCs for an idle
// or never-seen-before SO, and it flows through as an ordinary contribution
// (A27/A28) rather than as a separate composite-level default mechanism.
const (
	// DecisionAgree means multiple contributors produced a defined N(SO);
	// AggN's max was taken over more than one of them.
	DecisionAgree = "C0-agree"
	// DecisionSingle means exactly one analyzer contributed a defined N(SO).
	DecisionSingle = "C1-single"
	// DecisionSatFallback means no non-saturation analyzer contributed a
	// defined N(SO) for this SO, and saturation itself was eligible, so its
	// N(SO) was used as the fallback (spec §5.1: saturation is a fallback,
	// not a floor — it participates here only because nothing else did).
	DecisionSatFallback = "C2-sat-fallback"
	// DecisionNoSignal means no analyzer at all — including saturation —
	// produced a defined N(SO) for this SO. There is no usable signal, and
	// per spec §5.1.3 this must not be treated as a scale-to-zero decision;
	// it is "no evidence", handled by the gate in composite_signal_gate.go.
	DecisionNoSignal = "C4-no-signal"
)

// SODecision is the outcome of resolving one SO's N_com: the aggregated
// value (if any), which decision path produced it, and which analyzer names
// actually contributed (for provenance, spec A12). Exported so the engine's
// composite-construction step (internal/engines/steadystate) can consume it
// directly — ResolveSO is allocation's public entry point for "how was this
// SO's aggregate reached", and the engine builds the composite
// NamedAnalyzerResult from it rather than re-deriving the decision itself.
type SODecision struct {
	N            float64
	OK           bool
	Path         string
	Contributors []string
}

// ResolveSO computes one SO's (variant's) N_com and decision path from the
// full analyzer slice, per spec §5.1's fallback chain:
//
//	contributors(SO) = eligible NON-SATURATION analyzers with a defined N(SO)
//	if contributors non-empty:      aggregate over them, PLUS saturation if
//	                                 it is itself eligible with a defined N
//	                                 (C0-agree / C1-single)
//	else if saturation is eligible: fall back to saturation's own N alone
//	                                 (C2-sat-fallback)
//	else:                           no signal at all (C4-no-signal)
//
// "Sat included only if itself eligible" (spec §5.1) means saturation is an
// ORDINARY contributor — one voice in the max, not privileged, and the
// aggregate may come out below saturation's own N (spec test 3) — but only
// once at least one OTHER analyzer already qualifies as a contributor.
// Saturation having a defined N all by itself is not "agreement"; it is
// exactly the case the fallback branch exists for. Without this
// distinction, a model with only saturation running would always report
// C1-single, never C2-sat-fallback, which contradicts §5.1's "saturation
// does not participate unconditionally — only as fallback" and defeats the
// spec's #8/#8a/#8b/#8c fallback tests.
func ResolveSO(entries []NamedAnalyzerResult, variant string) SODecision {
	type contribution struct {
		name string
		n    float64
	}
	var others []contribution
	var sat *contribution
	for _, e := range entries {
		if !eligible(e) {
			continue
		}
		n, ok := aggregation.AggN([]*domain.AnalyzerResult{e.Result}, variant)
		if !ok {
			continue
		}
		if e.Name == domain.SaturationAnalyzerName {
			c := contribution{name: e.Name, n: n}
			sat = &c
			continue
		}
		others = append(others, contribution{name: e.Name, n: n})
	}

	if len(others) > 0 {
		contributions := others
		if sat != nil {
			contributions = append(contributions, *sat)
		}
		names := make([]string, 0, len(contributions))
		best := contributions[0].n
		for _, c := range contributions {
			names = append(names, c.name)
			if c.n > best {
				best = c.n
			}
		}
		path := DecisionAgree
		if len(contributions) == 1 {
			path = DecisionSingle
		}
		return SODecision{N: best, OK: true, Path: path, Contributors: names}
	}

	// No non-saturation contributor. Fall back to saturation's own N, if it
	// is itself eligible and defined — never invent a value it did not
	// produce.
	if sat != nil {
		return SODecision{N: sat.n, OK: true, Path: DecisionSatFallback, Contributors: []string{sat.name}}
	}

	return SODecision{OK: false, Path: DecisionNoSignal}
}
