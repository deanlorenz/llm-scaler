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
// itself, so a fit of the service time against the effective prompt length
// over low-load minutes gives the fixed part s0 as its intercept. The
// per-token cost rises with the prompt, from attention, so a lone prompt runs
// as T(I) = s0 + b*I + c*I*I with c >= 0. Over short prompts c*I*I is lost in
// the noise and the straight line s0 + b*I fits; over long ones a straight
// line through the curve's chord meets the axis below zero (run 3 of the
// TTFT recordings, 8k to 37k prompts: -52 ms).
//
// So the curve is fitted where the minutes can carry three parameters, and
// the line everywhere else, as P1 shipped:
//
//   - The admitted minutes are grouped into prompt-length clusters
//     (promptClusters), and only clusters of at least
//     MinQuadraticClusterMinutes minutes count.
//   - At least MinQuadraticClusters of them, the longest's median prompt at
//     least MinQuadraticSpread times the shortest's: the quadratic is the
//     least-squares curve through each counted cluster's median prompt and
//     median service time. Otherwise the line (TooFewPromptClusters,
//     NarrowPromptClusters).
//   - A fitted c that is not above zero is not convex: the line
//     (ConcaveFit).
//   - A quadratic whose intercept is not above zero is refused
//     (NonPositiveIntercept), not replaced by the line.
//
// The curve is fitted through the clusters' medians, not the minutes, which
// is a value estimator (analyzer-learner.md, 5) for a named contaminant: a
// minute no state identifies whose service time reads slow (run 3's
// 22:28 minute at 38k, 0.375 s against 0.30 s for the other four at its
// length). The intercept is extrapolated from the shortest cluster to zero,
// and the curve's three parameters through three clusters amplify one such
// minute into it: an untrimmed fit over run 3's fifteen minutes gives 21.5 ms
// with a negative b, the medians 3.7 ms. Each cluster's median tolerates one
// such minute in three. The line keeps P1's untrimmed fit over every minute:
// its intercept is interpolated where prompts are short, not extrapolated
// through a curve.
//
// AvgTTFT - AvgServiceTime holds a constant overhead (tokenization,
// scheduling, the HTTP path) beside the engine wait; with no queue there is
// no wait, and the least of it is added to a.
//
// 1/slope of the line is an upper bound on V_P, not a source: a busy pod's
// steps of B tokens run at B/(s0 + b*B) < 1/b. It is reported for validation
// only, from the line whichever curve a is taken from.
//
// Low load is a state, not a value: the queue empty over the minute, and
// the pod's occupancy (request rate times service time, Little's law) at or
// under LowLoadOccupancy, so a prompt rarely shares a step. The readings are
// excluded by that state. QueueLength has no presence bit until the TTFT
// recorder fields land; the occupancy test holds a row whose queue was not
// read to low load anyway.
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

// MinQuadraticClusters is the fewest prompt-length clusters, each of at least
// MinQuadraticClusterMinutes minutes, the quadratic is fitted through: one
// per parameter.
const MinQuadraticClusters = 3

// MinQuadraticClusterMinutes is the fewest minutes a prompt-length cluster
// holds for the quadratic to count it: its median then tolerates one slow
// minute in three.
const MinQuadraticClusterMinutes = 3

// MinQuadraticSpread is the least ratio of the longest counted cluster's
// median prompt to the shortest's for the quadratic: below it, c*I*I is not
// told apart from b*I.
const MinQuadraticSpread = 3.0

// AModel is the curve a's intercept was taken from.
type AModel string

const (
	// AModelLinear is the straight line s0 + b*I through every low-load
	// minute.
	AModelLinear AModel = "linear"
	// AModelQuadratic is the convex curve s0 + b*I + c*I*I through the
	// prompt-length clusters' medians.
	AModelQuadratic AModel = "quadratic"
)

// LineReason is why a was taken from the line and not the quadratic.
type LineReason string

const (
	// LineOnly is the quadratic switched off (Cycle.ALineOnly).
	LineOnly LineReason = "LineOnly"
	// TooFewPromptClusters is fewer than MinQuadraticClusters clusters of
	// MinQuadraticClusterMinutes minutes.
	TooFewPromptClusters LineReason = "TooFewPromptClusters"
	// NarrowPromptClusters is clusters spanning less than
	// MinQuadraticSpread.
	NarrowPromptClusters LineReason = "NarrowPromptClusters"
	// ConcaveFit is a fitted c not above zero.
	ConcaveFit LineReason = "ConcaveFit"
)

// fixed sets a on est from the key's low-load samples; lineOnly fits the
// line alone, as P1 shipped.
func (est *Estimate) fixed(ks *keyState, disaggregated, lineOnly bool) {
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
	est.AModel, est.ASlope = AModelLinear, slope
	if lineOnly {
		est.ALineReason = LineOnly
	} else {
		q, why := fitQuadratic(xs, ys)
		est.AClusters = q.clusters
		if why != "" {
			est.ALineReason = why
		} else {
			est.AModel, est.ASlope, est.ACurve = AModelQuadratic, q.b, q.c
			intercept = q.a
		}
	}
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

// quadFit is the quadratic s = a + b*I + c*I*I, and how many clusters of
// MinQuadraticClusterMinutes minutes the minutes held.
type quadFit struct {
	a, b, c  float64
	clusters int
}

// fitQuadratic is the convex curve through the prompt-length clusters'
// medians, or why the line stands instead.
func fitQuadratic(xs, ys []float64) (quadFit, LineReason) {
	var mx, my []float64
	for _, cl := range promptClusters(xs, ys) {
		if cl.n >= MinQuadraticClusterMinutes {
			mx, my = append(mx, cl.x), append(my, cl.y)
		}
	}
	q := quadFit{clusters: len(mx)}
	if len(mx) < MinQuadraticClusters {
		return q, TooFewPromptClusters
	}
	if !(mx[len(mx)-1] >= MinQuadraticSpread*mx[0]) {
		return q, NarrowPromptClusters
	}
	q.a, q.b, q.c = quadratic(mx, my)
	if !(q.c > 0) || !isFinite(q.c) || !isFinite(q.b) {
		return q, ConcaveFit
	}
	return q, ""
}

// cluster is one prompt-length cluster: its minutes' median prompt length
// and median service time, and how many minutes it holds.
type cluster struct {
	x, y float64
	n    int
}

// promptClusters groups the points by prompt length, shortest first: sorted
// by length, a point within MaxPromptDrift of the one before it joins its
// cluster, and a point further away starts the next. The steady gate holds a
// minute to MaxPromptDrift of the minutes before it, so one level of a sweep
// is one cluster; a continuum of lengths is one cluster too, and fits the
// line.
func promptClusters(xs, ys []float64) []cluster {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(i, j int) int {
		switch {
		case xs[i] < xs[j]:
			return -1
		case xs[i] > xs[j]:
			return 1
		}
		return 0
	})
	sx, sy := make([]float64, len(idx)), make([]float64, len(idx))
	for k, i := range idx {
		sx[k], sy[k] = xs[i], ys[i]
	}
	var out []cluster
	start := 0
	for k := 1; k <= len(sx); k++ {
		if k < len(sx) && sx[k] <= (1+MaxPromptDrift)*sx[k-1] {
			continue
		}
		// median sorts its slice in place: the cluster's own range, which
		// no later cluster reads.
		out = append(out, cluster{x: median(sx[start:k]), y: median(sy[start:k]), n: k - start})
		start = k
	}
	return out
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

// quadratic is the least-squares curve y = a + b*x + c*x*x through the
// points, by the normal equations; NaN when they hold fewer than three
// distinct x. Prompt lengths reach x^4 of about 1e24 at a million tokens,
// well inside float64, and the solve is exact to 1e-12 there
// (TestQuadratic_TheSolveIsExactAtEveryScale).
func quadratic(xs, ys []float64) (a, b, c float64) {
	// s[k] is the sum of x^k, t[k] of y*x^k.
	var s [5]float64
	var t [3]float64
	for i := range xs {
		p := 1.0
		for k := range s {
			s[k] += p
			if k < len(t) {
				t[k] += p * ys[i]
			}
			p *= xs[i]
		}
	}
	m := [3][3]float64{{s[0], s[1], s[2]}, {s[1], s[2], s[3]}, {s[2], s[3], s[4]}}
	det := det3(m)
	if !(det > 0) || math.IsInf(det, 1) {
		nan := math.NaN()
		return nan, nan, nan
	}
	var sol [3]float64
	for col := range sol {
		mc := m
		for row := range mc {
			mc[row][col] = t[row]
		}
		sol[col] = det3(mc) / det
	}
	return sol[0], sol[1], sol[2]
}

// det3 is the determinant of m.
func det3(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}
