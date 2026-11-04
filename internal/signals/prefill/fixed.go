package prefill

import (
	"math"
	"slices"
)

// The fixed cost a (slo-capacity-planner-ttft.md, "What is derivable today"
// and "Prefill capacity").
//
// On a P/D prefill pod the sidecar asks for one token, so the service time
// (AvgServiceTime) is the prefill time. At low load a prompt has a step to
// itself and runs as T(I) = s0 + c*I, so a fit of the service time against
// the effective prompt length over low-load minutes gives the fixed part s0
// and the per-token part c. AvgTTFT - AvgServiceTime holds a constant
// overhead (tokenization, scheduling, the HTTP path) beside the engine wait;
// with no queue there is no wait, and the least of it is added to a.
//
// 1/c is an upper bound on V_P, not a source: a busy pod's steps of B tokens
// run at B/(s0 + c*B) < 1/c. It is reported for validation only.
//
// Low load is a state, not a value: the queue empty over the minute, and
// the pod's occupancy (request rate times service time, Little's law) at or
// under LowLoadOccupancy, so a prompt rarely shares a step. The readings are
// excluded by that state and fitted untrimmed (analyzer-learner.md, 5).
// QueueLength has no presence bit until the TTFT recorder fields land; the
// occupancy test holds a row whose queue was not read to low load anyway.
//
// On an aggregated pod the service time includes decode, and a is refused.

// LowLoadOccupancy is the most a pod may be occupied (requests in service on
// average) for its minute to count as low load.
const LowLoadOccupancy = 0.1

// MinFixedSamples is the fewest low-load minutes a is fitted from.
const MinFixedSamples = 5

// MinPromptSpread is the least ratio of the longest to the shortest
// effective prompt the low-load minutes must span for the fit to separate
// the fixed cost from the per-token cost, read between the second shortest
// and the second longest so that each end holds two minutes.
const MinPromptSpread = 1.5

// fixed sets a on est from the key's low-load samples.
func (est *Estimate) fixed(ks *keyState, disaggregated bool) {
	if !disaggregated {
		est.ARefused = AggregatedRefused
		return
	}
	var xs, ys []float64
	overhead, overheadOK := 0.0, false
	for _, name := range ks.podNames() {
		for _, s := range ks.pods[name].samples {
			if !s.lowLoad {
				continue
			}
			xs, ys = append(xs, s.ieff), append(ys, s.svc)
			if s.overheadOK && (!overheadOK || s.overhead < overhead) {
				overhead, overheadOK = s.overhead, true
			}
		}
	}
	est.ASamples = len(xs)
	if len(xs) < MinFixedSamples {
		est.ARefused = TooFewLowLoadMinutes
		return
	}
	sorted := slices.Sorted(slices.Values(xs))
	lo, hi := sorted[0], sorted[len(sorted)-1]
	est.APromptMin, est.APromptMax = lo, hi
	// The spread must hold without the one most extreme minute at either
	// end: at least two minutes at each end of it, so one outlying prompt
	// length cannot set the slope alone.
	if !(sorted[len(sorted)-2] >= MinPromptSpread*sorted[1]) {
		est.ARefused = NoPromptSpread
		return
	}
	intercept, slope := ols(xs, ys)
	if math.IsNaN(slope) {
		// Unreachable past the spread test; kept apart from a fitted slope
		// that is not positive, which is a different finding.
		est.ARefused = NoPromptVariance
		return
	}
	if !(slope > 0) || !isFinite(slope) {
		est.ARefused = NonPositiveSlope
		return
	}
	est.SlopeBound = 1 / slope
	if !(intercept > 0) || !isFinite(intercept) {
		est.ARefused = NonPositiveIntercept
		return
	}
	est.AIntercept = intercept
	est.A = intercept
	if overheadOK {
		est.AOverhead, est.AOverheadKnown = overhead, true
		est.A += overhead
	}
}

// ols is the least-squares line y = intercept + slope*x through the points;
// NaN when x does not vary.
func ols(xs, ys []float64) (intercept, slope float64) {
	n := float64(len(xs))
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx, my = mx/n, my/n
	var sxx, sxy float64
	for i := range xs {
		dx := xs[i] - mx
		sxx += dx * dx
		sxy += dx * (ys[i] - my)
	}
	if !(sxx > 0) {
		return math.NaN(), math.NaN()
	}
	slope = sxy / sxx
	return my - slope*mx, slope
}
