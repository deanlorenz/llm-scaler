package prefill

import (
	"time"
)

// The window's defaults.
const (
	// DefaultWindowMaxSize is the maximum number of (T, TTFT) observations
	// retained. When full, the oldest is evicted on each Add.
	DefaultWindowMaxSize = 20

	// DefaultObservationMaxAge is how long an observation may stay in the
	// window. Nothing prunes implicitly -- the owner calls Prune once per
	// cycle before asking Ready -- so a fit is never built from a load
	// pattern that has since been replaced.
	DefaultObservationMaxAge = 30 * time.Minute

	// DefaultMinSamples is the minimum number of observations before the
	// window is Ready to fit.
	DefaultMinSamples = 8

	// DefaultMinTokenSpread is the minimum (max_T - min_T) required before a
	// fit is attempted, in prompt tokens.
	//
	// A line through points that barely differ in T has an arbitrary slope,
	// and the slope IS the capacity here: its reciprocal sizes the fleet. The
	// bound is expressed in absolute tokens rather than a fraction because a
	// prefill replica's backlog moves in units of whole prompts -- one 30,000
	// token prompt arriving or clearing is a spread of 30,000 -- so a fleet
	// serving any real traffic crosses this quickly, and one serving a
	// perfectly constant backlog genuinely has nothing to fit.
	DefaultMinTokenSpread = 2000.0

	// DefaultMinObservableTokens is the lower bound on T for an accepted
	// observation. Below a prompt or so of backlog the measured TTFT is
	// dominated by the fixed cost B and carries almost no information about
	// the slope, while its relative noise is highest.
	DefaultMinObservableTokens = 500.0
)

// Observation is one (T, TTFT) pair from one replica in one cycle.
type Observation struct {
	// Tokens is the prompt tokens the replica had accepted and not yet
	// first-tokened: running plus waiting requests, in tokens. This is the
	// regressor, and the reason the slope is a reciprocal velocity -- see the
	// package comment.
	Tokens float64

	// TTFTSec is the mean time to first token measured on that replica over
	// the cycle, in seconds.
	TTFTSec float64

	// At is when the observation was taken, for Prune.
	At time.Time
}

// Window is a rolling set of observations for one fit key. It is not safe for
// concurrent use; the owner serialises access, as the ITL window's owner does.
type Window struct {
	obs     []Observation
	maxSize int
}

// NewWindow returns an empty window holding at most maxSize observations.
// A non-positive maxSize takes DefaultWindowMaxSize.
func NewWindow(maxSize int) *Window {
	if maxSize <= 0 {
		maxSize = DefaultWindowMaxSize
	}
	return &Window{maxSize: maxSize}
}

// Add records one observation, rejecting the ones that cannot inform a fit:
// a non-positive or non-finite latency, and a backlog below
// DefaultMinObservableTokens. Returns whether it was kept, so a caller can
// report a replica whose readings are all being discarded rather than leaving
// a window silently empty.
func (w *Window) Add(o Observation) bool {
	if !(o.TTFTSec > 0) || !(o.Tokens >= DefaultMinObservableTokens) {
		return false
	}
	// Both fields are compared with `>`-style tests above, which a NaN fails,
	// so a NaN is already excluded. An infinity passes `> 0`, and is not.
	if isNonFinite(o.TTFTSec) || isNonFinite(o.Tokens) {
		return false
	}
	w.obs = append(w.obs, o)
	if len(w.obs) > w.maxSize {
		w.obs = w.obs[len(w.obs)-w.maxSize:]
	}
	return true
}

// Prune drops observations older than maxAge relative to now. Called once per
// cycle by the owner, before Ready.
func (w *Window) Prune(now time.Time, maxAge time.Duration) {
	if maxAge <= 0 {
		return
	}
	cutoff := now.Add(-maxAge)
	kept := w.obs[:0]
	for _, o := range w.obs {
		if o.At.After(cutoff) {
			kept = append(kept, o)
		}
	}
	w.obs = kept
}

// Len returns how many observations the window holds.
func (w *Window) Len() int { return len(w.obs) }

// Observations returns a copy of the window's contents.
func (w *Window) Observations() []Observation {
	out := make([]Observation, len(w.obs))
	copy(out, w.obs)
	return out
}

// Ready reports whether the window holds enough observations, spread widely
// enough in T, for Fit to mean anything.
func (w *Window) Ready(minSamples int, minSpread float64) bool {
	if minSamples <= 0 {
		minSamples = DefaultMinSamples
	}
	if minSpread <= 0 {
		minSpread = DefaultMinTokenSpread
	}
	if len(w.obs) < minSamples {
		return false
	}
	lo, hi := w.obs[0].Tokens, w.obs[0].Tokens
	for _, o := range w.obs[1:] {
		if o.Tokens < lo {
			lo = o.Tokens
		}
		if o.Tokens > hi {
			hi = o.Tokens
		}
	}
	return hi-lo >= minSpread
}

func isNonFinite(v float64) bool {
	return v != v || v > 1e308 || v < -1e308
}
