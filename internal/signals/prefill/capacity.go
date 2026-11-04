package prefill

import (
	"math"
	"slices"
	"time"
)

// Prefill capacity, V_P (slo-capacity-planner-ttft.md, "Prefill capacity").
//
// A vLLM prefill pod is a work-conserving server of prompt tokens, so its
// computed-token rate never exceeds its capacity: a rate read while it held
// work is at most V_P. The sources are taken in the page's order, and only
// sources that cannot over-state V_P:
//
//  1. Busy minutes: the queue above zero on every sample of the minute and
//     decode not back-pressuring; V_P is their median, over at least
//     MinBusyMinutes. A busy minute needs the least waiting count of the
//     minute (Cycle.QueueMin), which no collector query reads today.
//  2. The peak under a queue: the highest rate sustained over PeakRun
//     minutes, each with a queue on some sample. A lower bound. A peak with
//     no queue is the offered load, not capacity, and is not used.
//  3. The back-pressure bound (Cycle.Bound). A lower bound.
//
// Otherwise NoPrefillCapacity. SGLang always refuses: it publishes no
// computed-token series.

// MinBusyMinutes is the fewest busy minutes V_P is taken from.
const MinBusyMinutes = 5

// PeakRun is how many queued minutes in a row a peak must be sustained over.
const PeakRun = 3

// MaxRunGap is the widest gap between two minutes that still continues a
// run: one missed minute.
const MaxRunGap = 2 * time.Minute

// MinBoundSamples is the fewest spaced readings the back-pressure bound must
// hold before it is a source, as many as the floor needs before it orders
// from it (floor.MinThroughputSamplesToOrder).
const MinBoundSamples = 2

// capacity sets V_P on est from the key's samples and the cycle's bound.
func (est *Estimate) capacity(ks *keyState, c Cycle, sglang bool) {
	if sglang {
		est.VPRefused = SGLangRefused
		return
	}
	if v, n := busyMedian(ks); n >= MinBusyMinutes {
		est.VP, est.VPSource, est.VPSamples = v, SourceBusyMinutes, n
		return
	}
	if v, ok := peakUnderQueue(ks); ok {
		est.VP, est.VPSource, est.VPLowerBound, est.VPSamples = v, SourcePeakUnderQueue, true, PeakRun
		return
	}
	if b := c.Bound; finitePositive(b.Tokens) && b.Samples >= MinBoundSamples {
		est.VP, est.VPSource, est.VPLowerBound, est.VPSamples = b.Tokens, SourceBackPressureBound, true, b.Samples
		return
	}
	est.VPRefused = NoPrefillCapacity
}

// busyMedian is the median rate over every replica's busy minutes, and how
// many there are.
func busyMedian(ks *keyState) (float64, int) {
	var rates []float64
	for _, name := range ks.podNames() {
		for _, s := range ks.pods[name].samples {
			if s.busy && s.rateOK && s.rate > 0 {
				rates = append(rates, s.rate)
			}
		}
	}
	if len(rates) == 0 {
		return 0, 0
	}
	return median(rates), len(rates)
}

// peakUnderQueue is the highest rate any replica sustained over PeakRun
// queued minutes in a row: the least rate of the run, the highest run.
func peakUnderQueue(ks *keyState) (float64, bool) {
	best, found := 0.0, false
	for _, name := range ks.podNames() {
		run := []float64{}
		var last time.Time
		for _, s := range ks.pods[name].samples {
			if !s.queued || !s.rateOK || !(s.rate > 0) {
				run = run[:0]
				continue
			}
			if len(run) > 0 && s.at.Sub(last) > MaxRunGap {
				run = run[:0]
			}
			run = append(run, s.rate)
			last = s.at
			if len(run) < PeakRun {
				continue
			}
			sustained := slices.Min(run[len(run)-PeakRun:])
			if sustained > best {
				best, found = sustained, true
			}
		}
	}
	return best, found
}

// median is the median of xs, which it sorts; NaN never reaches it.
func median(xs []float64) float64 {
	slices.Sort(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// isFinite reports whether x is neither NaN nor infinite.
func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}
