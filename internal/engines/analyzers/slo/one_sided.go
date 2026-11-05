package slo

import "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"

// SwitchITLOneSidedAtCap is the replay switch for the one-sided price at the
// sequence cap on the slo line (issue #467; analyzer-learner.md, 4b-3 round
// 1, which built it for the throughput line). A replica at its stated cap
// teaches the line from above the band (SwitchBatchCapAdmission), at a k
// above itl.DefaultKSat. While the key has no learned B the pinned fit pins
// itl.DefaultBaselineSec, and through readings at a k_cap above kSat it
// prices, at a fill k, A'k + B' + (B0 - B')(1 - k/k_cap): under the truth
// whenever the true B' is above the 6 ms default, so the TPOT target is met
// at a higher fill than the replica reaches, and too few replicas are
// ordered. On, while B is unlearned and the window holds readings at k >=
// kSat, the pinned fit is set against the line through the least of those
// readings' ITL with its intercept a stated upper prior on B
// (itl.Admission.BPrior: bPrior), each priced at the fill where it meets the
// TPOT target (itl.Admission.OneSidedTarget), and the one with the lower
// fill is the line. It still abstains (infeasible) where even that line
// meets the target nowhere in the band. A held slope keeps the A whose line
// meets the target at that fill. A learned B ends it. Off, as before, the
// fit through the default B is the line.
const SwitchITLOneSidedAtCap = "slo-itl-one-sided-at-cap"

// oneSided sets on adm the one-sided price at the cap, under
// SwitchITLOneSidedAtCap, at the TPOT target in seconds (0 with none, where
// the line is fitted and not priced, and the rule judges at
// itl.DefaultKSat), pinned at the analyzer's prior on B (bPrior).
func (a *Analyzer) oneSided(adm *itl.Admission, tpotTarget float64) {
	a.mu.Lock()
	on := a.itlOneSidedAtCap
	a.mu.Unlock()
	if on {
		adm.OneSidedAboveKSat, adm.OneSidedTarget, adm.BPrior = true, tpotTarget, a.bPrior()
	}
}

// bPrior is the prior on B the one-sided rule pins (itl.BPrior): the largest
// B learned on any key of the line's tracker, else itl.DefaultBPrior.
func (a *Analyzer) bPrior() float64 {
	bs := a.lines.Baselines()
	learned := make([]float64, 0, len(bs))
	for _, b := range bs {
		learned = append(learned, b)
	}
	return itl.BPrior(learned...)
}
