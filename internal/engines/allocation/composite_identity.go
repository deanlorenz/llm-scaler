package allocation

// CompositeSignalName is the Name every composite NamedAnalyzerResult carries
// (spec §8, A10). The optimizer's input is always the composite — never
// literally saturation, even on the sat-only path — so every consumer that
// assumes ModelScalingRequest.CompositeSignal.Name == a specific analyzer's
// name is a defect (spec §8's audit found exactly one such defect in
// production code: the gate repaired in composite_signal_gate.go).
//
// Lives beside NamedAnalyzerResult, since the composite is an allocation
// concept and ModelScalingRequest.CompositeSignal already uses this word.
const CompositeSignalName = "CompositeSignal"
