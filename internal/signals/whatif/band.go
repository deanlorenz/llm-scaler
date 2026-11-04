package whatif

import (
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// Resampling is a block bootstrap's settings (docs/proposals/slo-capacity-planner.md,
// "Uncertainty"): resamples made of Block consecutive cycles at a time, so
// that the overlap of one-minute rates read every 15 s stays inside a block,
// Resamples of them, drawn from Seed so that a band can be reproduced.
type Resampling struct {
	Block     int
	Resamples int
	Seed      uint64
}

// DefaultResampling is the page's: blocks of 4 cycles, 200 resamples, seed 1.
func DefaultResampling() Resampling {
	return Resampling{Block: 4, Resamples: 200, Seed: 1}
}

// The band's edges, as percentiles of the resampled figures.
const (
	bandLo = 0.10
	bandHi = 0.90
)

// tierUnstableShare is the share of resamples above which a change of tier
// flags the band TierUnstable.
const tierUnstableShare = 0.10

// Refit is how a resample's line is fitted: at Tier, TierOLS or TierPinnedB,
// with PinnedB the intercept a pinned-B fit holds. An ols fit that fails
// falls back to pinned-B at PinnedB, as the itl tracker does, and that
// resample has changed tier.
type Refit struct {
	Tier    string
	PinnedB float64
}

// Resample is one bootstrap line: the line fitted from one resample, and
// whether it was fitted at another tier than Refit asked.
type Resample struct {
	Line        itl.Model
	TierChanged bool
}

// LineResamples is a block bootstrap of a line's readings.
type LineResamples struct {
	// Lines is each resample that fitted a line.
	Lines []Resample
	// Drawn is how many resamples were drawn, those that fitted no line
	// included: the denominator of the tier-change share.
	Drawn int
}

// BootstrapLines refits the line on r.Resamples block-bootstrap resamples
// of readings. The readings are grouped into cycles by their Timestamp, so a
// block is Block whole cycles of every replica's readings; a moving block
// bootstrap draws blocks until the resample holds as many cycles as the
// readings do. A resample is fitted as fit says.
//
// With fewer than two cycles, or a Refit at a tier with no fit (held or
// borrowed: no readings of the variant's own), there is nothing to resample
// and Drawn is 0. Resampling with Block or Resamples below 1 draws nothing
// too.
func BootstrapLines(readings []itl.Observation, fit Refit, r Resampling) LineResamples {
	if fit.Tier != TierOLS && fit.Tier != TierPinnedB || r.Block < 1 || r.Resamples < 1 {
		return LineResamples{}
	}
	cycles := cyclesOf(readings)
	n := len(cycles)
	if n < 2 {
		return LineResamples{}
	}
	block := min(r.Block, n)
	rng := rand.New(rand.NewPCG(r.Seed, r.Seed^0x9e3779b97f4a7c15))
	out := LineResamples{Drawn: r.Resamples}
	var sample []itl.Observation
	for range r.Resamples {
		sample = sample[:0]
		for got := 0; got < n; got += block {
			start := rng.IntN(n - block + 1)
			for _, c := range cycles[start : start+min(block, n-got)] {
				sample = append(sample, c...)
			}
		}
		if line, changed, ok := refit(sample, fit); ok {
			out.Lines = append(out.Lines, Resample{Line: line, TierChanged: changed})
		}
	}
	return out
}

// refit fits one resample at fit's tier, falling back from ols to pinned-B.
func refit(obs []itl.Observation, fit Refit) (line itl.Model, changed, ok bool) {
	if fit.Tier == TierOLS {
		if m, ok := itl.Fit(obs); ok {
			return m, false, true
		}
		changed = true
	}
	if !(fit.PinnedB > 0) {
		return itl.Model{}, changed, false
	}
	m, ok := itl.FitPinnedB(obs, fit.PinnedB)
	return m, changed, ok
}

// cyclesOf groups readings into cycles, by Timestamp, in time order.
func cyclesOf(readings []itl.Observation) [][]itl.Observation {
	sorted := slices.Clone(readings)
	slices.SortStableFunc(sorted, func(a, b itl.Observation) int { return a.Timestamp.Compare(b.Timestamp) })
	var out [][]itl.Observation
	var at time.Time
	for i, o := range sorted {
		if i == 0 || !o.Timestamp.Equal(at) {
			out = append(out, nil)
			at = o.Timestamp
		}
		out[len(out)-1] = append(out[len(out)-1], o)
	}
	return out
}

// BandAnswer bands a, an answer priced on one line, from the resamples:
// value reruns the question on a resampled line and returns the figure it
// prices, ok false when that line refuses it. The band is the 10th to the
// 90th percentile of the figures (nearest rank), so it need not be
// symmetric about a.Value; more than a tenth of the drawn resamples changing
// tier flags TierUnstable. With no figure to band, a comes back unchanged.
func BandAnswer(a Answer, rs LineResamples, value func(itl.Model) (float64, bool)) Answer {
	var vals []float64
	changed := 0
	for _, s := range rs.Lines {
		if s.TierChanged {
			changed++
		}
		if v, ok := value(s.Line); ok && Finite(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return a
	}
	slices.Sort(vals)
	at := func(p float64) float64 { return vals[max(0, int(math.Ceil(p*float64(len(vals))))-1)] }
	a.Band = &Interval{Lo: at(bandLo), Hi: at(bandHi)}
	// Drawn resamples that fitted no line changed tier too: they fell
	// through every fit.
	changed += rs.Drawn - len(rs.Lines)
	if float64(changed) > tierUnstableShare*float64(rs.Drawn) && !a.HasFlag(TierUnstable) {
		a.Flags = append(slices.Clone(a.Flags), TierUnstable)
	}
	return a
}
