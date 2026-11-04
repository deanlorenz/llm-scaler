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

// Refit is how a resample's line is fitted: by the itl tracker's own choice
// of fit (itl.Refit), which takes the full fit only on steady readings that
// are ready (enough of them, spread enough in k) and otherwise the pinned
// fit, or holds the key's slope; or, for an above-band line, by the
// tracker's above-band fallback (itl.RefitAboveBand).
type Refit struct {
	// Tier is the line's own tier: TierOLS, TierPinnedB or TierAboveBand.
	// Any other has no fit to rerun.
	Tier string
	// Line is the line: a resample on which the tracker would hold the
	// key's slope holds its A.
	Line itl.Model
	// Baseline is the intercept the pinned fit pins: the key's last full
	// fit's, or itl.DefaultBaselineSec.
	Baseline float64
	// MinSettled is the steady gate's MinSettledSamples, 0 for the default.
	MinSettled int
}

// Resample is one bootstrap line: the line fitted from one resample, the
// tier the tracker's choice fitted it at, and whether that differs from the
// line's own tier.
type Resample struct {
	Line        itl.Model
	Tier        string
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

// TierChanges is how many of the drawn resamples landed at another tier
// than the line's, a resample that fitted no line at all included.
func (rs LineResamples) TierChanges() int {
	n := rs.Drawn - len(rs.Lines)
	for _, s := range rs.Lines {
		if s.TierChanged {
			n++
		}
	}
	return n
}

// BootstrapLines refits the line on r.Resamples block-bootstrap resamples
// of the windows it was fitted from: for an above-band line the above-band
// windows, for any other the main and the steady window. The readings are
// grouped into cycles by Timestamp, a cycle keeping its main and its steady
// readings together, so a block is Block whole cycles of every replica's
// readings; a moving block bootstrap draws blocks until the resample holds
// as many cycles as the windows do. The resample's cycles are stamped again
// in the order drawn, one second apart, so the tracker's choice of fit,
// which reads the windows in time order, reads them in that order.
//
// Each resample is fitted as the tracker would fit it (Refit), and the tier
// it lands at is compared with the line's in both directions: ols to
// pinned-B and pinned-B to ols are both changes, and so is a resample on
// which the tracker would hold the slope (priced at the held A) or fit
// nothing at all.
//
// With fewer than two cycles, or a line at a tier with no fit to rerun
// (held or borrowed), there is nothing to resample and Drawn is 0.
// Resampling with Block or Resamples below 1 draws nothing too.
func BootstrapLines(readings itl.KeyReadings, fit Refit, r Resampling) LineResamples {
	all, steady := readings.All, readings.Steady
	switch fit.Tier {
	case TierOLS, TierPinnedB:
	case TierAboveBand:
		all, steady = readings.Above, readings.AboveSteady
	default:
		return LineResamples{}
	}
	if r.Block < 1 || r.Resamples < 1 {
		return LineResamples{}
	}
	cycles := cyclesOf(all, steady)
	n := len(cycles)
	if n < 2 {
		return LineResamples{}
	}
	block := min(r.Block, n)
	rng := rand.New(rand.NewPCG(r.Seed, r.Seed^0x9e3779b97f4a7c15))
	out := LineResamples{Drawn: r.Resamples}
	t0 := cycles[0].at
	for range r.Resamples {
		var sAll, sSteady []itl.Observation
		j := 0
		for got := 0; got < n; got += block {
			start := rng.IntN(n - block + 1)
			for _, c := range cycles[start : start+min(block, n-got)] {
				at := t0.Add(time.Duration(j) * time.Second)
				j++
				for _, o := range c.all {
					o.Timestamp = at
					sAll = append(sAll, o)
				}
				for _, o := range c.steady {
					o.Timestamp = at
					sSteady = append(sSteady, o)
				}
			}
		}
		if s, ok := refit(sAll, sSteady, fit); ok {
			out.Lines = append(out.Lines, s)
		}
	}
	return out
}

// refit fits one resample by the tracker's choice of fit, ok false when it
// fits no line.
func refit(all, steady []itl.Observation, fit Refit) (Resample, bool) {
	var got itl.Refitted
	tier := ""
	if fit.Tier == TierAboveBand {
		got = itl.RefitAboveBand(all, steady, fit.MinSettled)
		if got.Tier != "" {
			tier = TierAboveBand
		}
	} else {
		got = itl.Refit(all, steady, fit.Baseline, fit.MinSettled)
		tier = got.Tier
		if got.Hold {
			got.Model, tier = itl.Model{A: fit.Line.A, B: fit.Baseline}, TierHeld
		}
	}
	if tier == "" {
		return Resample{}, false
	}
	return Resample{Line: got.Model, Tier: tier, TierChanged: tier != fit.Tier}, true
}

// cycle is one cycle's readings: those of the main window and those of the
// steady one, read at the same instant.
type cycle struct {
	at          time.Time
	all, steady []itl.Observation
}

// cyclesOf groups two windows' readings into cycles, by Timestamp, in time
// order.
func cyclesOf(all, steady []itl.Observation) []cycle {
	var out []cycle
	at := map[time.Time]int{}
	add := func(o itl.Observation, isSteady bool) {
		i, ok := at[o.Timestamp]
		if !ok {
			i = len(out)
			at[o.Timestamp] = i
			out = append(out, cycle{at: o.Timestamp})
		}
		if isSteady {
			out[i].steady = append(out[i].steady, o)
		} else {
			out[i].all = append(out[i].all, o)
		}
	}
	for _, o := range all {
		add(o, false)
	}
	for _, o := range steady {
		add(o, true)
	}
	slices.SortStableFunc(out, func(a, b cycle) int { return a.at.Compare(b.at) })
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
	for _, s := range rs.Lines {
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
	if float64(rs.TierChanges()) > tierUnstableShare*float64(rs.Drawn) && !a.HasFlag(TierUnstable) {
		a.Flags = append(slices.Clone(a.Flags), TierUnstable)
	}
	return a
}
