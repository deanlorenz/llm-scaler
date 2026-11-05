package slo

import (
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// P1's figures, read from the saturation analyzer (docs/proposals/slo-tiers.md,
// "Where P1's figures come from").
//
// P1's observer lives in the saturation analyzer, which feeds it decode's
// back-pressure and the back-pressure bound, two inputs this analyzer does
// not have. So this analyzer runs no observer of its own: it reads each
// prefill variant's latest estimate from the saturation analyzer's stash,
// through PrefillEstimates, and never rebuilds the observer's key.
//
// Lock order: this analyzer's mu, then the saturation analyzer's, then the
// observer's. The saturation analyzer never calls back here, and it takes
// the observer's lock only with its own released, so a read from here
// cannot deadlock a saturation cycle.

// PrefillEstimates reads a prefill variant's latest P1 estimate, when it was
// written on the engine's cycle clock, and whether there is one.
// *saturation.SaturationAnalyzer implements it.
type PrefillEstimates interface {
	PrefillEstimate(namespace, modelID, variant string) (prefill.Estimate, time.Time, bool)
}

// WithPrefillEstimates sets where the analyzer reads P1's prefill estimates
// from: the engine and the replay pass their saturation analyzer, which
// reads the same clock. Unset, no prefill variant has an estimate. Call it
// before the analyzer is used.
func (a *Analyzer) WithPrefillEstimates(p PrefillEstimates) *Analyzer {
	a.prefillEstimates = p
	return a
}

// prefillEstimate is the variant's estimate when it was written this cycle:
// stamped at now, the analyzer's own clock, which is the saturation
// analyzer's too. An estimate from another cycle is refused, so one left by
// a cycle in which the observation did not run is never read. The engine
// runs saturation before this analyzer, so the estimate is the one made from
// this cycle's rows.
//
// It takes a.mu, then the saturation analyzer's lock through the handle. In
// S1a a.mu guards nothing the read touches: it is taken to fix the lock
// order now, so the race test holds it. S1b's TTFT state (the imbalance
// windows) is kept under a.mu, and read beside the estimate.
func (a *Analyzer) prefillEstimate(namespace, modelID, variant string, now time.Time) (prefill.Estimate, bool) {
	if a.prefillEstimates == nil {
		return prefill.Estimate{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	est, at, ok := a.prefillEstimates.PrefillEstimate(namespace, modelID, variant)
	if !ok || !at.Equal(now) {
		return prefill.Estimate{}, false
	}
	return est, true
}
