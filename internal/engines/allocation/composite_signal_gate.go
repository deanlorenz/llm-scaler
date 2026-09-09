package allocation

// HasUsableCompositeSignal reports whether composite carries a real capacity
// signal (spec §5.1.3, A11'): a non-nil Result that is informative, i.e. has
// at least one VariantCapacity whose Reason is not a no-data/error sentinel
// (ResultIsInformative — the same standard every analyzer is held to via
// eligible()).
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
// A model whose composite fails this check was not usefully measured this
// cycle — every SO's decision path bottomed out at C4-no-signal (spec
// §5.1.2) — so its replica counts are not evidence of anything and callers
// must treat it exactly as the absent-Result case they already guard
// (uniformly safe today per the zero-signal survey): skip the model, charge
// it nothing, decide nothing. There is deliberately no separate "which
// decision path" inspection here — informativeness is the one signal this
// gate needs, matching every other eligibility check in this package.
func HasUsableCompositeSignal(composite NamedAnalyzerResult) bool {
	return composite.Result != nil && ResultIsInformative(composite)
}
