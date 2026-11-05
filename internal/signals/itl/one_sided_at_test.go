package itl

import (
	"math"
	"testing"
	"time"
)

// Issue #467: the one-sided price at the cap where the line is priced, not
// at DefaultKSat: at a fill (Admission.OneSidedAt, the saturation line's
// pricing k) and at a target ITL (Admission.OneSidedTarget, the slo line's
// TPOT target).

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

// oneSidedGrid is issue #467's bias grid.
var oneSidedGrid = []struct{ a, b, kCap float64 }{
	{0.05, 0.010, 0.97}, {0.05, 0.030, 0.97}, {0.01, 0.020, 0.97}, {0.01, 0.030, 0.97},
	{0.05, 0.010, 0.92}, {0.01, 0.030, 0.92},
}

// The saturation line's case: priced at a fill under kSat (0.68, the
// default pricing k). Without the rule, through the default B, the price is
// under the truth by the derived term (B0 - B)(1 - k/kbar); with it, at or
// above the truth on readings without noise, and the line returned passes
// through the default B (A above zero), at the price of the line through
// the least ITL. The rule's choice at 0.68 is the choice at kSat: on
// readings at or past kSat the two lines cross at their mean k.
func TestOneSidedAt_ThePricingKIsAtOrAboveTheTruth(t *testing.T) {
	const kPrice = 0.68
	for _, g := range oneSidedGrid {
		truth := g.a*kPrice + g.b
		off := capWindow(NewTracker(), Admission{MaxNumSeqs: 64}, g.a, g.b, g.kCap, nil)
		on := capWindow(NewTracker(), Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: kPrice}, g.a, g.b, g.kCap, nil)
		atK := on.Model.ITLAt(kPrice)
		errOff, errOn := 100*(off.Model.ITLAt(kPrice)-truth)/truth, 100*(atK-truth)/truth
		// kbar is the mean fill of the readings the pinned fit took: the
		// steady ones, whose mix of the three fills is near even, not even.
		want := 100 * (DefaultBaselineSec - g.b) * (1 - kPrice/g.kCap) / truth
		t.Logf("A %.2f B %.0f ms k_cap %.2f: off %+.2f%% (derived %+.2f%%), on %+.2f%%", g.a, 1000*g.b, g.kCap, errOff, want, errOn)
		if math.Abs(errOff-want) > 0.1 || !(errOff < 0) {
			t.Errorf("negative control: off priced %+.3f%%, want the derived term %+.3f%%, under the truth", errOff, want)
		}
		if !on.PinnedAtLeastITL || atK < truth*(1-1e-12) {
			t.Errorf("on: priced %.5f s against the truth %.5f s (one-sided %v)", atK, truth, on.PinnedAtLeastITL)
		}
		if on.Model.B != DefaultBaselineSec || !(on.Model.A > 0) {
			t.Errorf("on: the line %+v, want one through the default B with A above zero", on.Model)
		}
	}
}

// The slo line's case: priced at the fill where the line meets its TPOT
// target, a lower fill being more replicas. On, the fill is at or under
// the true line's on readings without noise, and the line returned meets
// the target at the fill the line through the least ITL does; off, the
// fill is above the truth's.
func TestOneSidedAt_ATargetPricesAFillAtOrUnderTheTruths(t *testing.T) {
	for _, g := range oneSidedGrid {
		// A target the true line meets at 0.5, inside the band.
		target := g.a*0.5 + g.b
		adm := Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedTarget: target}
		lo := capWindow(NewTracker(), Admission{MaxNumSeqs: 64}, g.a, g.b, g.kCap, nil)
		on := capWindow(NewTracker(), adm, g.a, g.b, g.kCap, nil)
		truthFill, offFill, onFill := 0.5, adm.pricedFill(lo.Model), adm.pricedFill(on.Model)
		t.Logf("A %.2f B %.0f ms k_cap %.2f: fill truth %.4f, off %.4f, on %.4f", g.a, 1000*g.b, g.kCap, truthFill, offFill, onFill)
		if !(offFill > truthFill) {
			t.Errorf("negative control: off priced the fill %.4f, want above the truth's %.4f", offFill, truthFill)
		}
		if !on.PinnedAtLeastITL || onFill > truthFill+1e-12 {
			t.Errorf("on: priced the fill %.4f, above the truth's %.4f (one-sided %v)", onFill, truthFill, on.PinnedAtLeastITL)
		}
		if on.Model.B != DefaultBaselineSec || !(on.Model.A > 0) {
			t.Errorf("on: the line %+v, want one through the default B with A above zero", on.Model)
		}
	}
}

// With the gauge's noise (2 %, a fixed pattern of signs) the price at the
// pricing k is within known truth's 5 % on every case: the margin the pin
// buys is (b - B)(1 - k/kbar), and at k 0.68 it is larger than at kSat.
func TestOneSidedAt_WithNoiseItIsWithinEpsilon(t *testing.T) {
	const kPrice = 0.68
	noise := func(c, i int) float64 { return 0.02 * float64((c*7+i*3)%5-2) / 2 }
	for _, g := range oneSidedGrid {
		truth := g.a*kPrice + g.b
		on := capWindow(NewTracker(), Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: kPrice}, g.a, g.b, g.kCap, noise)
		if p := on.Model.ITLAt(kPrice); p < truth/1.05 {
			t.Errorf("A %.2f B %.0f ms k_cap %.2f: priced %.5f s, under the truth %.5f s past epsilon", g.a, 1000*g.b, g.kCap, p, truth)
		}
	}
}

// The rule compares the two lines where the line is priced (OneSidedAt),
// not at kSat. On readings at the cap alone the choice is the same at every
// fill under kSat, but a window that holds readings in the band too can make
// the fit through the default B cross the line through the least ITL
// between the two. Here a replica in the band at k 0.20, on a steep line,
// lifts the fit's slope until it passes the flat least-ITL line at kSat
// while lying under it at the pricing k: compared at kSat the fit would be
// priced, under the least-ITL line at k 0.68. The price at 0.68 must be the
// higher of the two there.
func TestOneSidedAt_TheLinesAreComparedAtThePricingK(t *testing.T) {
	const kPrice = 0.68
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	crossed := 0
	for _, bandITL := range []float64{0.020, 0.030, 0.040, 0.050, 0.060} {
		run := func(adm Admission) (*Tracker, Reading) {
			tr := NewTracker()
			var r Reading
			for c := range 6 {
				rows := cappedRows(0.97, 0, 0.050, start)
				rows[2].KvCacheUsage, rows[2].KvUsageInstant, rows[2].AvgITL = 0.20, 0.20, bandITL
				r = tr.Observe("k", "v", rows, adm, start.Add(time.Duration(c)*15*time.Second))
			}
			return tr, r
		}
		tr, _ := run(Admission{MaxNumSeqs: 64})
		rs := tr.Readings("k")
		m := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, Admission{}).Model
		lo, ok := atLeastITLLine(rs.All)
		if !ok || m.IsZero() {
			t.Fatalf("band ITL %.3f: premise: no lines (%+v, %v)", bandITL, m, ok)
		}
		if (m.ITLAt(kPrice) > lo.ITLAt(kPrice)) == (m.ITLAt(DefaultKSat) > lo.ITLAt(DefaultKSat)) {
			continue
		}
		crossed++
		_, on := run(Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: kPrice})
		want := max(m.ITLAt(kPrice), lo.ITLAt(kPrice))
		t.Logf("band ITL %.3f: default-B fit %.5f s, least-ITL line %.5f s at k %.2f; priced %.5f s",
			bandITL, m.ITLAt(kPrice), lo.ITLAt(kPrice), kPrice, on.Model.ITLAt(kPrice))
		if math.Abs(on.Model.ITLAt(kPrice)-want) > 1e-12 {
			t.Errorf("band ITL %.3f: priced %.6f s at k %.2f, want the higher line's %.6f s", bandITL, on.Model.ITLAt(kPrice), kPrice, want)
		}
	}
	if crossed == 0 {
		t.Fatal("premise: no window had the two lines cross between the pricing k and kSat")
	}
}

// The held A is the A of the line returned, so a later hold, through the
// default B, prices what the one-sided line did at the pricing k.
func TestOneSidedAt_TheHeldSlopeKeepsThePrice(t *testing.T) {
	const kPrice = 0.68
	tr := NewTracker()
	adm := Admission{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: kPrice}
	on := capWindow(tr, adm, 0.01, 0.030, 0.97, nil)
	held := tr.Slopes()["k"].A
	if math.Abs(held-on.Model.A) > 1e-15 {
		t.Errorf("held A %g, the line's %g", held, on.Model.A)
	}
	if got, want := (Model{A: held, B: DefaultBaselineSec}).ITLAt(kPrice), on.Model.ITLAt(kPrice); math.Abs(got-want) > 1e-15 {
		t.Errorf("held line prices %.6f s at k %.2f, the one-sided line %.6f s", got, kPrice, want)
	}
}

// Refit, given the admission's one-sided fields, prices the line Observe
// priced; without them, the fit through the default B alone. The what-if
// bands refit with it (whatif.Refit.OneSided), or price under the line.
func TestRefit_OneSidedPricesTheLineObserveDid(t *testing.T) {
	for _, adm := range []Admission{
		{MaxNumSeqs: 64, OneSidedAboveKSat: true},
		{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedAt: 0.68},
		{MaxNumSeqs: 64, OneSidedAboveKSat: true, OneSidedTarget: 0.01*0.5 + 0.030},
	} {
		tr := NewTracker()
		obs := capWindow(tr, adm, 0.01, 0.030, 0.97, nil)
		rs := tr.Readings("k")
		got := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, adm)
		if !got.PinnedAtLeastITL || got.Model != obs.Model {
			t.Errorf("%+v: refit %+v (one-sided %v), Observe %+v", adm, got.Model, got.PinnedAtLeastITL, obs.Model)
		}
		plain := Refit(rs.All, rs.Steady, DefaultBaselineSec, 0, Admission{})
		if plain.PinnedAtLeastITL || plain.Model == obs.Model {
			t.Errorf("negative control: refit without the rule gave the one-sided line %+v", plain.Model)
		}
	}
}
