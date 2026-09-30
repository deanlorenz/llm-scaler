// Package prefill is the linear time-to-first-token model TTFT(T) = A·T + B
// over the prompt tokens a replica is holding: the observations it is fitted
// from, the fit and its validity rule.
//
// It is the prefill side's counterpart to package itl, which does the same for
// decode with ITL(k) = A·k + B. The two roles are bounded by different things
// -- decode by resident KV, prefill by prompt tokens to compute -- so they get
// different regressors, but the same shape: fit a line from ordinary traffic,
// and price a replica from the line instead of waiting to watch it saturate.
//
// # Why the slope is the capacity
//
// T is the prompt tokens ACCEPTED by the replica and not yet first-tokened:
// the running requests plus the waiting ones, in tokens. A request's first
// token therefore arrives after the work ahead of it has been computed, so
//
//	TTFT = T/V + B
//
// where V is the rate the replica retires prompt tokens and B is the fixed
// per-request cost (scheduling, the first forward pass's constant part, the KV
// handoff to decode). Fitting TTFT against T recovers A = 1/V, so
//
//	V = 1/A  tokens per second
//
// is the replica's prefill token velocity -- the same quantity TokenScale
// (arXiv 2512.03416) profiles offline by raising the request rate until the
// output rate saturates. Fitting it from traffic needs no profiling run, no
// per-(model, accelerator) table, and follows a heterogeneous fleet.
//
// # Why not TTFT on its own
//
// TTFT includes queue wait, so it rises when a fleet is behind and falls when
// it catches up -- the property that makes end-to-end latency unusable for
// sizing. Regressing it on T is what removes that: a fleet that is behind has
// a larger T, and the ratio of the two is a rate, which is what a capacity is.
package prefill

import "math"

// slopeEpsilon is the smallest slope A that counts as meaningfully positive.
// A perfectly flat fit is mathematically A == 0 but OLS rounding leaves noise
// of order 1e-17. A real slope here is the reciprocal of a token velocity: at
// 140,000 tokens/s it is about 7e-6, which is far above that noise and far
// below any slope a degenerate fit produces.
const slopeEpsilon = 1e-12

// maxPlausibleVelocity bounds 1/A from above. A slope small enough to imply a
// faster-than-this replica is a fit through points that barely differ in T,
// not a measurement -- the regressor's spread requirement (window.go) is the
// first defence and this is the backstop. 10^9 prompt tokens per second is
// orders of magnitude beyond any accelerator, so nothing real is rejected.
const maxPlausibleVelocity = 1e9

// Model is the linear time-to-first-token model: TTFT(T) = A·T + B, with T in
// prompt tokens and TTFT in seconds. A is the marginal cost of one more
// prompt token to compute, and its reciprocal is the replica's token velocity.
// B is the fixed per-request cost at zero backlog.
type Model struct {
	A float64
	B float64
}

// IsZero returns true when the model has not been fitted (both coefficients
// are zero).
func (m Model) IsZero() bool {
	return m.A == 0 && m.B == 0
}

// TTFTAt returns the predicted time to first token, in seconds, for a replica
// holding tokens prompt tokens.
func (m Model) TTFTAt(tokens float64) float64 {
	return m.A*tokens + m.B
}

// TokenVelocity returns the prompt tokens per second the fitted line implies a
// replica retires, 1/A. Zero for an unfitted or invalid model, so a caller that
// forgets to check IsZero gets a figure that fails a `> 0` test rather than an
// infinity that sizes a fleet.
func (m Model) TokenVelocity() float64 {
	if !ValidModel(m.A, m.B) {
		return 0
	}
	return 1 / m.A
}

// ValidModel reports whether an (A, B) pair is a usable prefill model.
//
// The rules, and why each one is here:
//
//   - A finite and meaningfully positive. More prompt tokens must cost more
//     time; a flat or inverted slope means the regressor is not explaining the
//     latency, and its reciprocal would be a velocity of infinity or a
//     negative one.
//   - 1/A no larger than maxPlausibleVelocity, so a near-zero slope cannot
//     become an unbounded capacity.
//   - B finite, and not negative beyond rounding. B is a fixed cost and cannot
//     be below zero; OLS drives it there when the top of the range bends away
//     from the line, which is the same failure package itl documents for its
//     own intercept. A small negative is tolerated as fit noise and clamped by
//     the caller's use of TTFTAt, a large one rejects the fit.
func ValidModel(a, b float64) bool {
	if math.IsNaN(a) || math.IsInf(a, 0) {
		return false
	}
	if a <= slopeEpsilon {
		return false
	}
	if 1/a > maxPlausibleVelocity {
		return false
	}
	if math.IsNaN(b) || math.IsInf(b, 0) {
		return false
	}
	// A fixed cost cannot be meaningfully negative. The tolerance is one
	// millisecond: below that it is rounding in the fit, above it the line is
	// not describing the data.
	return b >= -0.001
}

// Fit computes the ordinary-least-squares line through the observations.
// Returns ok=false when there are too few points, when the regressor does not
// vary (a vertical spread of T is what makes the slope identifiable), or when
// the resulting coefficients fail ValidModel.
func Fit(obs []Observation) (Model, bool) {
	n := float64(len(obs))
	if n < 2 {
		return Model{}, false
	}

	var sumT, sumY, sumT2, sumTY float64
	for _, o := range obs {
		sumT += o.Tokens
		sumY += o.TTFTSec
		sumT2 += o.Tokens * o.Tokens
		sumTY += o.Tokens * o.TTFTSec
	}

	// The denominator is n·Σ(T²) − (ΣT)², which is zero exactly when every T
	// is identical. Guarded rather than divided into: a fleet serving one
	// steady shape produces exactly that, and it is the ordinary case, not an
	// error.
	den := n*sumT2 - sumT*sumT
	if den == 0 || math.IsNaN(den) || math.IsInf(den, 0) {
		return Model{}, false
	}

	a := (n*sumTY - sumT*sumY) / den
	b := (sumY - a*sumT) / n
	if !ValidModel(a, b) {
		return Model{}, false
	}
	return Model{A: a, B: b}, true
}
