package itl

import (
	"cmp"
	"math"
	"slices"
)

// DefaultBaselineSec is the per-token latency of an unloaded replica: the
// intercept B of ITL(k) = A*k + B when there is not enough spread in k to fit
// one. It is a hardware floor -- the time a single sequence takes per token
// with the cache nearly empty -- so it varies far less between deployments
// than the contention slope A does, which is what makes pinning it and fitting
// only A a reasonable trade when the observations are poor.
//
// The throughput analyzer carries the same number as its own
// DefaultBaselineITLSec. It is stated here rather than imported because that
// package is an analyzer and this is a signal; the two collapse onto this one
// when the throughput analyzer's in-flight work lands.
const DefaultBaselineSec = 0.006

// FitPinnedB fits ITL(k) = A*k + B with B held at the value given: A is the
// interquartile mean of the readings' own slopes (ITL_i - B) / k_i, each
// weighted by k_i -- the mean of the slopes that hold the middle half of the
// weight, a quarter trimmed from either end.
//
// Not least squares, because the readings a pinned fit gets are not noise
// around the line. Below DefaultMinSamples steady readings it takes every
// reading, and a replica's first reading after it starts serving -- a pod
// that joined, or one the load came back to after a minute without tokens --
// carries an ITL many times the line's: on a two-namespace run, every new
// replica's first reading was about 0.46 of k at 435 to 496 ms, slopes of 0.9
// to 1.06 against 0.09 to 0.10 for the readings around them. Least squares
// weighs a reading by its squared error, so three of sixteen moved A from
// 0.101 to 0.170, and the derived mu read 3.1 to 4.2 requests/s against the
// 6.1 to 6.2 each replica served, for the whole of a twenty-minute phase.
// Those three carried 11% of the weight, all in the top quarter, so the trim
// sets them aside, and on readings that are all on one line the mean is that
// line's slope, as least squares is.
//
// Not the weighted median, the least-absolute-deviations A, though it trims
// as well: the median jumps from one group of slopes to the other as their
// weight crosses one half. A stationary fleet whose replicas serve two shapes
// at the same fill (slopes 0.08 and 0.12, half each) had A swing between the
// two every few cycles, 36% of its mean, where least squares held to 2%; and
// with one replica of three at 0.12 the median priced the fleet as if it did
// not exist. The interquartile mean moves with the weight, so it returns the
// blend there, as least squares does.
//
// This is the fit for a fleet whose replicas sit at similar loads. A router
// that balances well gives every replica nearly the same k, and a full
// two-parameter fit over points that share an x is meaningless -- Window.Ready
// exists to refuse exactly that, requiring DefaultMinKSpread before Fit is
// trusted. Pinning B leaves one parameter, which a cluster of points at one k
// can still determine: it answers "how much slower than an idle replica is one
// at this load", which is what the caller needs, without pretending to know
// where the line crosses zero.
//
// ok is false when there are no usable observations or the result is not a
// model any reading can be taken from. A negative slope is rejected: more
// cache in use cannot make a replica faster, and a fit that says so is
// measurement noise, not a model.
func FitPinnedB(obs []Observation, b float64) (Model, bool) {
	if len(obs) == 0 || !(b > 0) {
		return Model{}, false
	}
	slopes := make([]weightedSlope, 0, len(obs))
	var total float64
	for _, o := range obs {
		if !(o.K > 0) || !(o.ITLSec > 0) || math.IsInf(o.K, 0) || math.IsInf(o.ITLSec, 0) {
			continue
		}
		slopes = append(slopes, weightedSlope{a: (o.ITLSec - b) / o.K, w: o.K})
		total += o.K
	}
	if len(slopes) == 0 || !(total > 0) {
		return Model{}, false
	}
	a := interquartileMean(slopes, total)
	if !(a >= 0) {
		return Model{}, false
	}
	m := Model{A: a, B: b}
	if !ValidModel(m.A, m.B) {
		return Model{}, false
	}
	return m, true
}

// weightedSlope is one reading's slope through the pinned intercept, and the
// weight the fit gives it: its k.
type weightedSlope struct {
	a, w float64
}

// interquartileMean is the weighted mean of the slopes over the middle half
// of total: sorted by slope, each slope counts for the part of its weight
// that lies between a quarter and three quarters of total, so a slope
// straddling either cut counts in part. It sorts s.
func interquartileMean(s []weightedSlope, total float64) float64 {
	slices.SortFunc(s, func(x, y weightedSlope) int { return cmp.Compare(x.a, y.a) })
	lo, hi := total/4, 3*total/4
	var cum, sum, mass float64
	for _, x := range s {
		if in := min(cum+x.w, hi) - max(cum, lo); in > 0 {
			sum += in * x.a
			mass += in
		}
		cum += x.w
	}
	return sum / mass
}
