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
// readings at k >= kSat, the pinned fit is set against the line pinned at the
// least ITL of those readings (itl.Admission.OneSidedAboveKSat), compared at
// the k the line is priced at (pricingK; itl.Admission.OneSidedAt), and the
// higher there is the line. A held slope keeps the A that prices the chosen
// line there. A learned B ends it. Off, as before, the fit through the
// default B is the line.
const SwitchITLOneSidedAtCap = "saturation-itl-one-sided-at-cap"

// itlOneSided sets on adm the one-sided price at the cap, under
// SwitchITLOneSidedAtCap, at kPrice, the fill the line is priced at. A
// variant with no stated cap admits nothing at it, and the rule has nothing
// past kSat to read but what the window took at the cap: it is set all the
// same, and does nothing there.
func (a *SaturationAnalyzer) itlOneSided(adm *itl.Admission, kPrice float64) {
	a.mu.Lock()
	on := a.itlOneSidedAtCap
	a.mu.Unlock()
	if on {
		adm.OneSidedAboveKSat, adm.OneSidedAt = true, kPrice
	}
}
