package allocation

// HasUsableCompositeSignal reports whether composite carries a real capacity
// signal (spec §5.1.3, A11'): a non-nil Result with at least one
// VariantCapacity whose decision path (spec §5.1.2 — Reason on the
// composite carries C0-agree/C1-single/C2-sat-fallback/C4-no-signal, not an
// analyzer's own no-data/error/P0-store vocabulary) is not C4-no-signal.
//
// This replaces hasSaturationResult's broken identity check
// (CompositeSignal.Name == domain.SaturationAnalyzerName && Result != nil).
// That check tested "is this specifically saturation", which silently goes
// false the moment the composite carries its own name (spec §8) — the very
// rename this mission makes. The fix is by intent, not identity (A11): test
// what the gate actually needs, which is a usable signal to charge a quota
// against, never which analyzer produced it. Per spec §6.1 the optimizer's
// input is always the composite, so a name check there was never meaningful
// even before the rename.
//
// Deliberately NOT ResultIsInformative: that function's no-data/error
// sentinel check is calibrated to an ANALYZER's own Reason vocabulary. The
// composite's Reason means something different (which decision path
// produced this SO's N_com, not how PRC was measured) and DecisionNoSignal
// ("C4-no-signal") does not equal either analyzer sentinel string — reusing
// ResultIsInformative here would silently always report "usable" regardless
// of whether every SO actually had a signal, defeating the very rename this
// function exists to survive.
//
// A model whose composite fails this check was not usefully measured this
// cycle — every SO's decision path bottomed out at C4-no-signal — so its
// replica counts are not evidence of anything and callers must treat it
// exactly as the absent-Result case they already guard (uniformly safe
// today per the zero-signal survey): skip the model, charge it nothing,
// decide nothing.
func HasUsableCompositeSignal(composite NamedAnalyzerResult) bool {
	if composite.Result == nil {
		return false
	}
	for _, vc := range composite.Result.VariantCapacities {
		if vc.Reason != DecisionNoSignal {
			return true
		}
	}
	return false
}
