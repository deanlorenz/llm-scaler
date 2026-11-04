package whatif

import (
	"errors"
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
// divided by the window's mean, _sum/_count: an upper edge never under-states
// the quantile, so the ratio is conservative. R_q is the 90th percentile of
// those per-window ratios (nearest rank), and never below 1. A window with no
// observations is skipped.
//
// The ratio comes back typed, ready for TPOTTarget.Ratio, with its flags:
// CoarseTPOTBucket for a q bucket wider than a quarter of its window's mean. A
// refusal comes back as a non-nil *Unanswerable with a zero ratio, which a
// TPOTTarget reads as "no ratio" (NoTPOTHistogram), so ignoring it cannot
// price a percentile. The refusals: TPOTQuantileUnbounded when a window's
// quantile falls in the +Inf bucket, which has no edge to read;
// NoTPOTHistogram when no window holds an observation; MalformedHistogram for
// data that cannot be read; and InvalidInput only for a q outside (0, 1).
func TPOTRatioFromWindows(windows []TPOTWindow, q float64) (TPOTRatio, []Flag, *Unanswerable) {
	fail := func(r Reason, detail string) (TPOTRatio, []Flag, *Unanswerable) {
		u := refuse(r, detail)
		return TPOTRatio{}, nil, &u
	}
	if !(q > 0 && q < 1) {
		return fail(InvalidInput, fmt.Sprintf("percentile %g, want 0 < q < 1", q))
	}
	var ratios []float64
	coarse := false
	for i, w := range windows {
		if !Finite(w.Count) || w.Count < 0 || !Finite(w.Sum) {
			return fail(MalformedHistogram, fmt.Sprintf("window %d: count %g, sum %g", i, w.Count, w.Sum))
		}
		if w.Count == 0 {
			continue
		}
		if !(w.Sum > 0) {
			return fail(MalformedHistogram, fmt.Sprintf("window %d: %g observations summing to %g", i, w.Count, w.Sum))
		}
		mean := w.Sum / w.Count
		lower, upper, empty, err := quantileBucket(w.Buckets, q, w.Count)
		if err != nil {
			var u Unanswerable
			if !errors.As(err, &u) {
				u = refuse(MalformedHistogram, err.Error())
			}
			return fail(u.Reason, fmt.Sprintf("window %d: %s", i, u.Detail))
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
		return fail(NoTPOTHistogram, "no window holds a TPOT observation")
	}
	slices.Sort(ratios)
	rank := int(math.Ceil(ratioPercentile*float64(len(ratios)))) - 1
	var flags []Flag
	if coarse {
		flags = []Flag{CoarseTPOTBucket}
	}
	return TPOTRatio{Value: max(1, ratios[rank])}, flags, nil
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
// empty is true when the buckets hold no observation. The error is an
// Unanswerable: MalformedHistogram for no buckets under a positive count, an
// edge that does not rise from above 0 (a TPOT is positive), or a count that
// is not finite and non-negative; TPOTQuantileUnbounded for a quantile past
// the last finite edge.
func quantileBucket(buckets []HistogramBucket, q, count float64) (lower, upper float64, empty bool, err error) {
	if len(buckets) == 0 {
		return 0, 0, false, refuse(MalformedHistogram, fmt.Sprintf("%g observations and no buckets", count))
	}
	cum := make([]float64, len(buckets))
	prevEdge, running := 0.0, 0.0
	for j, b := range buckets {
		// NaN fails the comparison and is refused with the rest.
		if !(b.UpperEdge > prevEdge) || !Finite(b.Count) || b.Count < 0 {
			return 0, 0, false, refuse(MalformedHistogram, fmt.Sprintf("bucket %d (le %g, count %g) out of order", j, b.UpperEdge, b.Count))
		}
		running = max(running, b.Count)
		cum[j], prevEdge = running, b.UpperEdge
	}
	total := count
	if n := len(buckets); math.IsInf(buckets[n-1].UpperEdge, 1) {
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
				return 0, 0, false, refuse(TPOTQuantileUnbounded, fmt.Sprintf("the quantile is above the last finite edge %g", prevEdge))
			}
			return prevEdge, b.UpperEdge, false, nil
		}
		prevEdge = b.UpperEdge
	}
	return 0, 0, false, refuse(TPOTQuantileUnbounded, fmt.Sprintf("the quantile is above the last edge %g", prevEdge))
}
