package itl

import (
	"math"
	"testing"
	"time"
)

// Issue #467: the one-sided price at the cap with a stated prior on B
// (Admission.BPrior), judged where the line is priced: at a fill
// (Admission.OneSidedAt, the saturation line's pricing k) and at a target
// ITL (Admission.OneSidedTarget, the slo line's TPOT target).

// capCycles is how many cycles capWindow runs: past DefaultMinSamples
// readings, four of each fill.
const capCycles = 12

// capWindow is capCycles cycles of three replicas at their cap, on line ITL
// = a k + b, at fills cycling over kCap - 0.01, kCap and kCap + 0.01; noise,
// when set, scales each reading by (1 + noise(c, i)).
func capWindow(tr *Tracker, adm Admission, a, b, kCap float64, noise func(c, i int) float64) Reading {
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var r Reading
	for c := range capCycles {
		k := []float64{kCap - 0.01, kCap, kCap + 0.01}[c%3]
		rows := cappedRows(k, a, b, start)
		if noise != nil {
			for i := range rows {
				rows[i].AvgITL *= 1 + noise(c, i)
			}
		}
		r = tr.Observe("k", "v", rows, adm, start.Add(time.Duration(c)*15*time.Second))
	}
	return r
}

// priorGrid is issue #467's bias grid, B at or under DefaultBPrior.
var priorGrid = []struct{ a, b, kCap float64 }{
	{0.05, 0.010, 0.97}, {0.05, 0.030, 0.97}, {0.01, 0.020, 0.97}, {0.01, 0.030, 0.97},
	{0.05, 0.010, 0.92}, {0.01, 0.030, 0.92},
}

// boundAt is the price the prior's line gives at k, worked by hand from the
// least-ITL reading (k_r, ITL_r) the pinned fit read from the key's windows
// (pinnedReadings): BPrior + max(0, ITL_r - BPrior) k / k_r. With the slope
// above zero it is the truth plus (BPrior - B)(1 - k/k_r); floored at zero
// (ITL_r under the prior) it is BPrior itself.
func boundAt(tr *Tracker, k float64) float64 {
	rs := tr.Readings("k")
	read, _ := pinnedReadings(rs.All, rs.Steady)
	best := Observation{ITLSec: math.Inf(1)}
	for _, o := range read {
		if atOrPastKSat(o.K) && o.ITLSec < best.ITLSec {
			best = o
		}
	}
	return DefaultBPrior + max(0, best.ITLSec-DefaultBPrior)*k/best.K
}

// atPrice is an admission priced one-sided at the default pricing k (0.68)
// with the prior DefaultBPrior.
func atPrice() Admission {
	return Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: 0.68, BPrior: DefaultBPrior}
}

// The saturation line's case, at the pricing k (0.68). Off, through the
// default B, the price is under the truth by the derived term (B0 - B)(1 -
// k/kbar). On, with the prior at DefaultBPrior, at or above the truth for
// every B at or under the prior, over by exactly the derived bound
// (boundAt): (BPrior - B)(1 - k/k_r), k_r the least reading's fill, or, where
// that reading's ITL is under the prior and the slope floors at 0, the prior
// itself; the truth exactly where B is the prior. The line returned passes through the default B with A
// above zero.
func TestBPrior_ThePricingKIsAtOrAboveTheTruthAndBounded(t *testing.T) {
	const kPrice = 0.68
	for _, g := range priorGrid {
		truth := g.a*kPrice + g.b
		off := capWindow(NewTracker(), Admission{MaxNumSeqs: 64}, g.a, g.b, g.kCap, nil)
		tr := NewTracker()
		on := capWindow(tr, atPrice(), g.a, g.b, g.kCap, nil)
		atK := on.Model.ITLAt(kPrice)
		errOff, errOn := 100*(off.Model.ITLAt(kPrice)-truth)/truth, 100*(atK-truth)/truth
		// kbar is the mean fill of the readings the pinned fit took: the
		// steady ones, whose mix of the three fills is near even, not even.
		want := 100 * (DefaultBaselineSec - g.b) * (1 - kPrice/g.kCap) / truth
		bound := 100 * (boundAt(tr, kPrice) - truth) / truth
		t.Logf("A %.2f B %.0f ms k_cap %.2f: off %+.2f%% (derived %+.2f%%), on %+.2f%% (bound %+.2f%%)",
			g.a, 1000*g.b, g.kCap, errOff, want, errOn, bound)
		if math.Abs(errOff-want) > 0.1 || !(errOff < 0) {
			t.Errorf("negative control: off priced %+.3f%%, want the derived term %+.3f%%, under the truth", errOff, want)
		}
		if !on.PinnedAtBPrior || on.PinnedAtLeastITL || atK < truth*(1-1e-12) {
			t.Errorf("on: priced %.5f s against the truth %.5f s (at the prior %v)", atK, truth, on.PinnedAtBPrior)
		}
		if math.Abs(errOn-bound) > 1e-9 {
			t.Errorf("on: over by %+.4f%%, want the bound %+.4f%%", errOn, bound)
		}
		if on.Model.B != DefaultBaselineSec || !(on.Model.A > 0) {
			t.Errorf("on: the line %+v, want one through the default B with A above zero", on.Model)
		}
	}
}

// A true B above the prior is under-priced, by at most (B - BPrior)(1 -
// k/k_r): bounded, and less than the default B alone.
func TestBPrior_ATrueBAboveThePriorIsUnderByTheBound(t *testing.T) {
	const kPrice, a, b, kCap = 0.68, 0.01, 0.040, 0.97
	truth := a*kPrice + b
	tr := NewTracker()
	on := capWindow(tr, atPrice(), a, b, kCap, nil)
	off := capWindow(NewTracker(), Admission{MaxNumSeqs: 64}, a, b, kCap, nil)
	errOn := 100 * (on.Model.ITLAt(kPrice) - truth) / truth
	errOff := 100 * (off.Model.ITLAt(kPrice) - truth) / truth
	bound := 100 * (boundAt(tr, kPrice) - truth) / truth
	t.Logf("B 40 ms: on %+.2f%% (bound %+.2f%%), off %+.2f%%", errOn, bound, errOff)
	if math.Abs(errOn-bound) > 1e-9 || !(errOn < 0) || !(errOn > errOff) {
		t.Errorf("on %+.4f%%, want the bound %+.4f%%, under the truth and over the default B's %+.4f%%", errOn, bound, errOff)
	}
}

// With the gauge's noise (2 %, a fixed pattern of signs) the price at the
// pricing k is within known truth's 5 % on every case.
func TestBPrior_WithNoiseItIsWithinEpsilon(t *testing.T) {
	const kPrice = 0.68
	noise := func(c, i int) float64 { return 0.02 * float64((c*7+i*3)%5-2) / 2 }
	for _, g := range priorGrid {
		truth := g.a*kPrice + g.b
		on := capWindow(NewTracker(), atPrice(), g.a, g.b, g.kCap, noise)
		if p := on.Model.ITLAt(kPrice); p < truth/1.05 {
			t.Errorf("A %.2f B %.0f ms k_cap %.2f: priced %.5f s, under the truth %.5f s past epsilon", g.a, 1000*g.b, g.kCap, p, truth)
		}
	}
}

// The slo line's case: priced at the fill where the line meets its TPOT
// target, a lower fill being more replicas. On, the fill is at or under the
// true line's for every B at or under the prior, and inside the band
// wherever the prior's line meets the target there; where the prior's line
// is past the target at the band's bottom (its error over B larger than the
// target's headroom) it abstains. Off, it is above the truth's.
func TestBPrior_ATargetPricesAFillAtOrUnderTheTruths(t *testing.T) {
	for _, g := range priorGrid {
		// A target the true line meets at 0.5, inside the band.
		target := g.a*0.5 + g.b
		adm := Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedTarget: target, BPrior: DefaultBPrior}
		lo := capWindow(NewTracker(), Admission{MaxNumSeqs: 64}, g.a, g.b, g.kCap, nil)
		on := capWindow(NewTracker(), adm, g.a, g.b, g.kCap, nil)
		offFill, onFill := adm.pricedFill(lo.Model), adm.pricedFill(on.Model)
		t.Logf("A %.2f B %.0f ms k_cap %.2f: fill truth 0.5000, off %.4f, on %.4f", g.a, 1000*g.b, g.kCap, offFill, onFill)
		if !(offFill > 0.5) {
			t.Errorf("negative control: off priced the fill %.4f, want above the truth's 0.5", offFill)
		}
		if !on.PinnedAtBPrior || onFill > 0.5+1e-12 {
			t.Errorf("on: priced the fill %.4f, above the truth's 0.5 (at the prior %v)", onFill, on.PinnedAtBPrior)
		}
		// Abstains (the band's bottom) only where the prior's line itself is
		// past the target there: the prior's error is larger than the
		// target's headroom over B.
		bandLo, _ := adm.band()
		tr := NewTracker()
		capWindow(tr, Admission{MaxNumSeqs: 64}, g.a, g.b, g.kCap, nil)
		rs := tr.Readings("k")
		read, _ := pinnedReadings(rs.All, rs.Steady)
		p, _ := priorLine(read, DefaultBPrior)
		if feasible := p.ITLAt(bandLo) < target; feasible != (onFill > bandLo) {
			t.Errorf("on: fill %.4f, but the prior's line is %.5f s at the band's bottom against the target %.5f s",
				onFill, p.ITLAt(bandLo), target)
		}
	}
}

// The held A is the A of the line returned, so a later hold, through the
// default B, prices what the line at the prior did at the pricing k.
func TestBPrior_TheHeldSlopeKeepsThePrice(t *testing.T) {
	const kPrice = 0.68
	tr := NewTracker()
	on := capWindow(tr, atPrice(), 0.05, 0.010, 0.97, nil)
	if !on.PinnedAtBPrior {
		t.Fatal("premise: not priced at the prior")
	}
	held := Model{A: tr.Slopes()["k"].A, B: DefaultBaselineSec}
	if math.Abs(held.ITLAt(kPrice)-on.Model.ITLAt(kPrice)) > 1e-15 {
		t.Errorf("held line %.6f s at k %.2f, the line at the prior %.6f s", held.ITLAt(kPrice), kPrice, on.Model.ITLAt(kPrice))
	}
}

// The rule compares the two lines where the line is priced (OneSidedAt),
// not at kSat. A window with a replica in the band (k 0.75, too close to the
// cap for a full fit) beside the capped ones steepens the fit through the
// default B until it crosses the prior's line between the pricing k and
// kSat; the price at the pricing k must be the higher of the two there.
func TestBPrior_TheLinesAreComparedAtThePricingK(t *testing.T) {
	const kPrice = 0.68
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	crossed := 0
	for _, bandITL := range []float64{0.050, 0.053, 0.056, 0.059, 0.062, 0.065} {
		run := func(adm Admission) (*Tracker, Reading) {
			tr := NewTracker()
			var r Reading
			for c := range 6 {
				rows := cappedRows(0.97, 0, 0.050, start)
				rows[2].KvCacheUsage, rows[2].KvUsageInstant, rows[2].AvgITL = 0.75, 0.75, bandITL
				r = tr.Observe("k", "v", rows, adm, start.Add(time.Duration(c)*15*time.Second))
			}
			return tr, r
		}
		tr, _ := run(Admission{MaxNumSeqs: 64})
		rs := tr.Readings("k")
		m := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, Admission{}).Model
		p, ok := priorLine(rs.All, DefaultBPrior)
		if !ok || m.IsZero() {
			t.Fatalf("band ITL %.3f: premise: no lines (%+v, %v)", bandITL, m, ok)
		}
		if (m.ITLAt(kPrice) > p.ITLAt(kPrice)) == (m.ITLAt(DefaultKSat) > p.ITLAt(DefaultKSat)) {
			continue
		}
		crossed++
		_, on := run(atPrice())
		want := max(m.ITLAt(kPrice), p.ITLAt(kPrice))
		t.Logf("band ITL %.3f: default-B fit %.5f s, prior's line %.5f s at k %.2f; priced %.5f s",
			bandITL, m.ITLAt(kPrice), p.ITLAt(kPrice), kPrice, on.Model.ITLAt(kPrice))
		if math.Abs(on.Model.ITLAt(kPrice)-want) > 1e-12 {
			t.Errorf("band ITL %.3f: priced %.6f s at k %.2f, want the higher line's %.6f s", bandITL, on.Model.ITLAt(kPrice), kPrice, want)
		}
	}
	if crossed == 0 {
		t.Fatal("premise: no window had the two lines cross between the pricing k and kSat")
	}
}

// The prior's line is through the least at-cap reading, slope floored at 0;
// readings under kSat are not read.
func TestBPrior_ThePriorLineIsThroughTheLeastReading(t *testing.T) {
	obs := []Observation{{K: 0.80, ITLSec: 0.010}, {K: 0.95, ITLSec: 0.060}, {K: 0.90, ITLSec: 0.055}}
	m, ok := priorLine(obs, 0.030)
	if !ok || math.Abs(m.A-(0.055-0.030)/0.90) > 1e-15 || m.B != 0.030 {
		t.Errorf("%+v, want B 0.030 through (0.90, 0.055)", m)
	}
	if m, _ := priorLine([]Observation{{K: 0.90, ITLSec: 0.020}}, 0.030); m.A != 0 {
		t.Errorf("a least ITL under the prior: A %g, want 0", m.A)
	}
	if _, ok := priorLine([]Observation{{K: 0.80, ITLSec: 0.05}}, 0.030); ok {
		t.Error("a line from readings under kSat")
	}
}

// BPrior is the largest learned B, else DefaultBPrior.
func TestBPrior_IsTheLargestLearnedB(t *testing.T) {
	if got := BPrior(); got != DefaultBPrior {
		t.Errorf("none learned: %g, want %g", got, DefaultBPrior)
	}
	if got := BPrior(0.004, 0.012, math.NaN(), -1, math.Inf(1)); got != 0.012 {
		t.Errorf("learned: %g, want 0.012", got)
	}
}

// Refit, given the admission's one-sided fields, prices the line Observe
// priced; without them, the fit through the default B alone. The what-if
// bands refit with it (whatif.Refit.OneSided), or price under the line.
func TestRefit_OneSidedPricesTheLineObserveDid(t *testing.T) {
	for _, adm := range []Admission{
		{MaxNumSeqs: 64, OneSidedAboveKSat: true},
		atPrice(),
		{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedTarget: 0.01*0.5 + 0.020, BPrior: DefaultBPrior},
	} {
		tr := NewTracker()
		obs := capWindow(tr, adm, 0.01, 0.020, 0.97, nil)
		rs := tr.Readings("k")
		got := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, adm)
		raised := got.PinnedAtLeastITL || got.PinnedAtBPrior
		if !raised || got.Model != obs.Model || got.PinnedAtBPrior != obs.PinnedAtBPrior {
			t.Errorf("%+v: refit %+v (%v %v), Observe %+v", adm, got.Model, got.PinnedAtLeastITL, got.PinnedAtBPrior, obs.Model)
		}
		plain := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, Admission{})
		if plain.PinnedAtLeastITL || plain.PinnedAtBPrior || plain.Model == obs.Model {
			t.Errorf("negative control: refit without the rule gave the one-sided line %+v", plain.Model)
		}
	}
}
