package saturation

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"

// SwitchITLOneSidedAtCap is the replay switch for the one-sided price at
// the sequence cap on the ITL line (issue #467; analyzer-learner.md, 4b-3
// round 1, which built it for the throughput line). A replica at its stated
// cap teaches the line from above the band (SwitchBatchCapITLAdmission), at
// a k above itl.DefaultKSat. While the key has no learned B the pinned fit
// pins itl.DefaultBaselineSec, and through readings at a k_cap above kSat it
// prices, at a fill k, A'k + B' + (B0 - B')(1 - k/k_cap): under the truth
// whenever the true B' is above the 6 ms default, so too high a derived mu
// and too few replicas. On, while B is unlearned and the window holds
// readings at k >= kSat, the pinned fit is set against the line through the
// least of those readings' ITL with its intercept a stated upper prior on B
// (itl.Admission.BPrior: itlBPrior), compared at the k the line is priced
// at (pricingK; itl.Admission.OneSidedAt), and the higher there is the line.
// A held slope keeps the A that prices the chosen line there. A learned B
// ends it. Off, as before, the fit through the default B is the line.
const SwitchITLOneSidedAtCap = "saturation-itl-one-sided-at-cap"

// itlOneSided sets on adm the one-sided price at the cap, under
// SwitchITLOneSidedAtCap, at kPrice, the fill the line is priced at, pinned
// at the analyzer's prior on B (itlBPrior). A variant with no stated cap
// admits nothing at it, and the rule has nothing past kSat to read but what
// the window took at the cap: it is set all the same, and does nothing
// there.
func (a *SaturationAnalyzer) itlOneSided(adm *itl.Admission, kPrice float64) {
	a.mu.Lock()
	on := a.itlOneSidedAtCap
	a.mu.Unlock()
	if on {
		adm.OneSidedAboveKSat, adm.OneSidedAt, adm.BPrior = true, kPrice, a.itlBPrior()
	}
}

// itlBPrior is the prior on B the one-sided rule pins (itl.BPrior): the
// largest B learned on any key of the ITL tracker, or of a line published
// for sharing, else itl.DefaultBPrior. A published line through the default
// B is no learned floor (every key without one publishes it) and is left
// out. Called without a.mu held.
func (a *SaturationAnalyzer) itlBPrior() float64 {
	bs := a.itlTracker.Baselines()
	learned := make([]float64, 0, len(bs))
	for _, b := range bs {
		learned = append(learned, b)
	}
	a.mu.Lock()
	for _, l := range a.itlLines {
		if l.model.B != itl.DefaultBaselineSec {
			learned = append(learned, l.model.B)
		}
	}
	a.mu.Unlock()
	return itl.BPrior(learned...)
}
