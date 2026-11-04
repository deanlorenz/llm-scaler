package prefill

import (
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
//  1. Busy minutes: the queue above zero on every sample of the minute, the
//     pod's KV not full and decode not back-pressuring; V_P is their median
//     over the last BusyWindow, from at least MinBusyMinutes. A busy minute
//     needs the least waiting count of the minute (Cycle.QueueMin, from the
//     rows' QueueMin by QueueMins) read and above zero: a pod whose queue
//     min was not read has none. A busy minute reads at or below V_P: at it
//     when the pod ran flat out, under it when a data-parallel rank idled
//     (flagged, DataParallelRanks) or the pod stalled on something the
//     tests above miss (decode back-pressure that its saturation test does
//     not catch, say).
//  2. The peak under a queue: the highest rate sustained over PeakRun
//     minutes, each with a queue on some sample. A lower bound. A peak with
//     no queue is the offered load, not capacity, and is not used.
//  3. The back-pressure bound (Cycle.Bound). A lower bound.
//
// Otherwise NoPrefillCapacity. SGLang always refuses: it publishes no
// computed-token series.
//
// "Cannot over-state" holds up to the noise of rate(). Each sample is a
// one-minute rate, and the peak is the max, over every replica and up to
// Retention (up to 358 overlapping three-minute windows on one replica), of
// each window's least minute. A minimum of three cuts a single high minute, but the max
// over that many runs still keeps the luckiest one: the peak can read above
// V_P by about the spread of a one-minute rate, never by more.

// MinBusyMinutes is the fewest busy minutes V_P is taken from.
const MinBusyMinutes = 5

// BusyWindow is how far back busy minutes are read. V_P depends on the
// prompt lengths a pod is fed (each step pays a fixed cost the prompts
// share), so a busy minute speaks for the load it ran under, and a median
// over Retention would keep a load that has gone for six hours and win over
// the fresher sources below. Thirty minutes is six times MinBusyMinutes:
// room for a pod saturated one minute in six to keep its source, while a
// busy period that ended leaves it within half an hour.
const BusyWindow = 30 * time.Minute

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
	est.VPQueuedMinutes, est.VPLongestQueuedRun = queuedRuns(ks)
	v, n, multiRank := busyMedian(ks, c.Now)
	est.VPBusyMinutes = n
	if n >= MinBusyMinutes {
		est.VP, est.VPSource, est.VPSamples = v, SourceBusyMinutes, n
		if multiRank {
			est.VPLowerBound, est.VPLowerBoundReason = true, DataParallelRanks
		}
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

// busyMedian is the median rate over every replica's busy minutes no older
// than BusyWindow at now, how many there are, and whether any came from a
// pod with more than one data-parallel rank.
func busyMedian(ks *keyState, now time.Time) (float64, int, bool) {
	cutoff := now.Add(-BusyWindow)
	var rates []float64
	multiRank := false
	for _, name := range ks.podNames() {
		for _, s := range ks.pods[name].samples {
			if s.busy && s.rateOK && s.rate > 0 && !s.at.Before(cutoff) {
				rates = append(rates, s.rate)
				multiRank = multiRank || s.multiRank
			}
		}
	}
	if len(rates) == 0 {
		return 0, 0, false
	}
	return median(rates), len(rates), multiRank
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

// queuedRuns is how many samples of the key's replicas held a queue with a
// rate read, and the longest run of them on one replica, by the rule
// peakUnderQueue reads runs by.
func queuedRuns(ks *keyState) (queued, longest int) {
	for _, name := range ks.podNames() {
		run := 0
		var last time.Time
		for _, s := range ks.pods[name].samples {
			if !s.queued || !s.rateOK || !(s.rate > 0) {
				run = 0
				continue
			}
			if run > 0 && s.at.Sub(last) > MaxRunGap {
				run = 0
			}
			run++
			queued++
			last = s.at
			longest = max(longest, run)
		}
	}
	return queued, longest
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
