package whatif

import (
	"fmt"
	"math"
	"slices"
)

// HistogramBucket is one cumulative bucket of a Prometheus histogram: Count
// observations at or below UpperEdge (the le label). The last bucket's edge
// may be +Inf.
type HistogramBucket struct {
	UpperEdge float64
	Count     float64
}

// TPOTWindow is one window of a per-request TPOT histogram
// (llm_d_epp_request_streaming_tpot_seconds): its cumulative buckets and its
// _sum and _count over the window.
type TPOTWindow struct {
	Buckets []HistogramBucket
	Sum     float64
	Count   float64
}

// coarseBucketWidth is the width of the q bucket, over the window's mean,
// above which an R_q is set by the bucket grid rather than the traffic.
const coarseBucketWidth = 0.25

// ratioPercentile is the percentile of the per-window ratios R_q is taken at.
const ratioPercentile = 0.90

// TPOTRatioFromWindows learns R_q, the q-th percentile of per-request TPOT over
// its mean (docs/proposals/slo-capacity-planner.md, "Learning R_q").
//
// Per window, the bucket holding the q quantile is read at its UPPER edge and
// divided by the window's mean, _sum/_count: an upper edge never
// under-states the quantile, so the ratio is conservative. R_q is the 90th
// percentile of those per-window ratios (nearest rank), and never below 1. A
// window with no observations is skipped.
//
// It refuses with TPOTQuantileUnbounded when a window's quantile falls in the
// +Inf bucket, which has no edge to read, and with NoTPOTHistogram when no
// window holds an observation; bad data is MalformedHistogram, a refusal like
// the others, and only a q outside (0, 1) is InvalidInput. Answer.Value is R_q; CoarseTPOTBucket flags a
// q bucket wider than a quarter of its window's mean.
func TPOTRatioFromWindows(windows []TPOTWindow, q float64) Result {
	if !(q > 0 && q < 1) {
		return refuse(InvalidInput, fmt.Sprintf("percentile %g, want 0 < q < 1", q))
	}
	var ratios []float64
	coarse := false
	for i, w := range windows {
		if !Finite(w.Count) || w.Count < 0 || !Finite(w.Sum) {
			return refuse(MalformedHistogram, fmt.Sprintf("window %d: count %g, sum %g", i, w.Count, w.Sum))
		}
		if w.Count == 0 {
			continue
		}
		if !(w.Sum > 0) {
			return refuse(MalformedHistogram, fmt.Sprintf("window %d: %g observations summing to %g", i, w.Count, w.Sum))
		}
		mean := w.Sum / w.Count
		lower, upper, empty, u := quantileBucket(w.Buckets, q, w.Count)
		if u != nil {
			u.Detail = fmt.Sprintf("window %d: %s", i, u.Detail)
			return *u
		}
		if empty {
			continue
		}
		ratios = append(ratios, upper/mean)
		if (upper-lower)/mean > coarseBucketWidth {
			coarse = true
		}
	}
	if len(ratios) == 0 {
		return refuse(NoTPOTHistogram, "no window holds a TPOT observation")
	}
	slices.Sort(ratios)
	rank := int(math.Ceil(ratioPercentile*float64(len(ratios)))) - 1
	var flags []Flag
	if coarse {
		flags = []Flag{CoarseTPOTBucket}
	}
	return Answer{Value: max(1, ratios[rank]), Flags: flags}
}

// quantileBucket finds the bucket holding the q quantile and returns its lower
// and upper edges, read the way histogram_quantile reads them:
//
//   - the cumulative counts are made monotone (a running maximum), since
//     rate() over buckets scraped a moment apart leaves small dips that are
//     not an error;
//   - the rank is q times the +Inf bucket's count, the histogram's own total,
//     rather than _count, which is scraped separately and may exceed it; a
//     histogram with no +Inf bucket ranks against count.
//
// empty is true when the buckets hold no observation. Edges must rise from
// above 0 (a TPOT is positive), and counts must be finite and non-negative;
// anything else is MalformedHistogram.
func quantileBucket(buckets []HistogramBucket, q, count float64) (lower, upper float64, empty bool, u *Unanswerable) {
	cum := make([]float64, len(buckets))
	prevEdge, running := 0.0, 0.0
	for j, b := range buckets {
		// NaN fails the comparison and is refused with the rest.
		if !(b.UpperEdge > prevEdge) || !Finite(b.Count) || b.Count < 0 {
			r := refuse(MalformedHistogram, fmt.Sprintf("bucket %d (le %g, count %g) out of order", j, b.UpperEdge, b.Count))
			return 0, 0, false, &r
		}
		running = max(running, b.Count)
		cum[j], prevEdge = running, b.UpperEdge
	}
	total := count
	if n := len(buckets); n > 0 && math.IsInf(buckets[n-1].UpperEdge, 1) {
		total = cum[n-1]
	}
	if !(total > 0) {
		return 0, 0, true, nil
	}
	rank := q * total
	prevEdge = 0
	for j, b := range buckets {
		if cum[j] >= rank {
			if math.IsInf(b.UpperEdge, 1) {
				r := refuse(TPOTQuantileUnbounded, fmt.Sprintf("the quantile is above the last finite edge %g", prevEdge))
				return 0, 0, false, &r
			}
			return prevEdge, b.UpperEdge, false, nil
		}
		prevEdge = b.UpperEdge
	}
	r := refuse(TPOTQuantileUnbounded, fmt.Sprintf("the quantile is above the last edge %g", prevEdge))
	return 0, 0, false, &r
}
