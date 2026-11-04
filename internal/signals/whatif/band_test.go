package whatif

import (
	"math"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// readingsOn is n cycles of two replicas' readings on the line, k cycling
// over [lo, hi], with noise that alternates in sign so each resample moves
// the fit. Every reading is steady: the main and the steady window hold the
// same ones.
func readingsOn(line itl.Model, n int, lo, hi, noise float64) itl.KeyReadings {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	out := make([]itl.Observation, 0, 2*n)
	for c := range n {
		at := t0.Add(time.Duration(c) * 15 * time.Second)
		for r, f := range []float64{float64(c%7) / 6, float64((c+3)%7) / 6} {
			k := lo + (hi-lo)*f
			sign := 1.0
			if (c+r)%2 == 1 {
				sign = -1
			}
			out = append(out, itl.Observation{K: k, ITLSec: line.ITLAt(k) * (1 + sign*noise), Timestamp: at})
		}
	}
	return itl.KeyReadings{All: out, Steady: out}
}

var worked = itl.Model{A: 0.004438, B: 0.001557}

func TestDefaultResamplingIsThePages(t *testing.T) {
	if r := DefaultResampling(); r != (Resampling{Block: 4, Resamples: 200, Seed: 1}) {
		t.Errorf("%+v, want blocks of 4 cycles, 200 resamples, seed 1", r)
	}
}

func TestBootstrapLinesIsReproducibleAndMoves(t *testing.T) {
	rs := readingsOn(worked, 40, 0.2, 0.7, 0.05)
	fit := Refit{Tier: TierOLS, Line: worked, Baseline: worked.B}
	a := BootstrapLines(rs, fit, DefaultResampling())
	b := BootstrapLines(rs, fit, DefaultResampling())
	if a.Drawn != 200 || len(a.Lines) != 200 {
		t.Fatalf("drew %d, fitted %d; want 200 of each", a.Drawn, len(a.Lines))
	}
	distinct := map[itl.Model]bool{}
	for i := range a.Lines {
		if a.Lines[i] != b.Lines[i] {
			t.Fatalf("resample %d: %+v then %+v from the same seed", i, a.Lines[i], b.Lines[i])
		}
		distinct[a.Lines[i].Line] = true
	}
	if len(distinct) < 50 {
		t.Errorf("only %d distinct lines in 200 resamples", len(distinct))
	}
	other := BootstrapLines(rs, fit, Resampling{Block: 4, Resamples: 200, Seed: 2})
	if other.Lines[0] == a.Lines[0] && other.Lines[1] == a.Lines[1] {
		t.Errorf("seed 2 drew what seed 1 did")
	}
}

// A resample keeps whole cycles: a cycle's main and steady readings, of
// every replica, come together.
func TestBootstrapKeepsCyclesWhole(t *testing.T) {
	rs := readingsOn(worked, 9, 0.2, 0.7, 0)
	cycles := cyclesOf(rs.All, rs.Steady)
	if len(cycles) != 9 {
		t.Fatalf("%d cycles, want 9", len(cycles))
	}
	for i, c := range cycles {
		if len(c.all) != 2 || len(c.steady) != 2 {
			t.Errorf("cycle %d holds %d and %d readings, want both replicas' in both windows", i, len(c.all), len(c.steady))
		}
	}
}

func TestBootstrapHasNothingToResample(t *testing.T) {
	rs := readingsOn(worked, 40, 0.2, 0.7, 0.05)
	cases := map[string]LineResamples{
		"one cycle":      BootstrapLines(readingsOn(worked, 1, 0.2, 0.7, 0.05), Refit{Tier: TierOLS, Line: worked, Baseline: worked.B}, DefaultResampling()),
		"a held line":    BootstrapLines(rs, Refit{Tier: TierHeld, Line: worked, Baseline: worked.B}, DefaultResampling()),
		"a borrowed one": BootstrapLines(rs, Refit{Tier: TierBorrowed, Line: worked}, DefaultResampling()),
		"no resamples":   BootstrapLines(rs, Refit{Tier: TierOLS, Line: worked}, Resampling{Block: 4}),
	}
	for name, got := range cases {
		if got.Drawn != 0 || len(got.Lines) != 0 {
			t.Errorf("%s: drew %d, fitted %d; want nothing", name, got.Drawn, len(got.Lines))
		}
	}
	if got := BandAnswer(Answer{Value: 1}, LineResamples{}, func(itl.Model) (float64, bool) { return 1, true }); got.Band != nil {
		t.Errorf("banded from no resamples: %+v", got.Band)
	}
}

// The tracker's readiness gates the full fit on a resample, as it gated the
// line: steady readings spread less than itl.DefaultMinKSpread fit pinned-B,
// though itl.Fit alone would take them. An ols line on such readings (one
// fitted when the window was wider) bands as TierUnstable.
func TestBootstrapRerunsTheReadinessGate(t *testing.T) {
	rs := readingsOn(worked, 40, 0.40, 0.65, 0.05)
	if _, ok := itl.Fit(rs.Steady); !ok {
		t.Fatal("the full fit refuses the readings; the case tests nothing")
	}
	got := BootstrapLines(rs, Refit{Tier: TierOLS, Line: worked, Baseline: worked.B}, DefaultResampling())
	if len(got.Lines) != 200 {
		t.Fatalf("fitted %d of %d", len(got.Lines), got.Drawn)
	}
	for _, s := range got.Lines {
		if s.Tier != TierPinnedB || !s.TierChanged || s.Line.B != worked.B {
			t.Fatalf("resample %+v: want a pinned-B fit at the baseline, a change of tier", s)
		}
	}
	a := BandAnswer(Answer{Value: 1}, got, func(m itl.Model) (float64, bool) { return m.ITLAt(0.5), true })
	if !a.HasFlag(TierUnstable) {
		t.Errorf("flags %v, want TierUnstable", a.Flags)
	}
}

// A change of tier counts both ways: a pinned-B line whose readings are
// ready refits ols on the resamples, and that is a change too.
func TestBootstrapCountsAChangeToOLS(t *testing.T) {
	rs := readingsOn(worked, 40, 0.2, 0.7, 0.05)
	got := BootstrapLines(rs, Refit{Tier: TierPinnedB, Line: worked, Baseline: worked.B}, DefaultResampling())
	changed := 0
	for _, s := range got.Lines {
		if s.TierChanged {
			if s.Tier != TierOLS {
				t.Fatalf("resample %+v: a change that is not to ols", s)
			}
			changed++
		}
	}
	if changed < 180 {
		t.Errorf("%d of %d resamples changed to ols, want nearly all", changed, len(got.Lines))
	}
}

// Readings too few to fit, on every resample, are where the tracker holds
// the key's slope: the resample is priced at the held A, at the baseline,
// and has changed tier.
func TestBootstrapHoldsWhereTheTrackerHolds(t *testing.T) {
	rs := readingsOn(worked, 4, 0.2, 0.7, 0.05) // 8 readings, under DefaultMinSamples
	got := BootstrapLines(rs, Refit{Tier: TierPinnedB, Line: worked, Baseline: worked.B}, DefaultResampling())
	if len(got.Lines) != 200 {
		t.Fatalf("fitted %d of %d", len(got.Lines), got.Drawn)
	}
	for _, s := range got.Lines {
		if s.Tier != TierHeld || !s.TierChanged || s.Line != worked {
			t.Fatalf("resample %+v: want the held line, a change of tier", s)
		}
	}
}

// An above-band line resamples the above-band windows, by the fallback's
// own fit, and stays above-band.
func TestBootstrapAboveBandOnItsOwnReadings(t *testing.T) {
	line := itl.Model{A: 0.05, B: 0.01}
	above := readingsOn(line, 20, 0.86, 0.95, 0.02)
	rs := itl.KeyReadings{Above: above.All, AboveSteady: above.Steady}
	got := BootstrapLines(rs, Refit{Tier: TierAboveBand, Line: line}, DefaultResampling())
	if len(got.Lines) != 200 {
		t.Fatalf("fitted %d of %d", len(got.Lines), got.Drawn)
	}
	for _, s := range got.Lines {
		if s.Tier != TierAboveBand || s.TierChanged {
			t.Fatalf("resample %+v: want an above-band fit", s)
		}
	}
	if none := BootstrapLines(itl.KeyReadings{All: above.All, Steady: above.Steady}, Refit{Tier: TierAboveBand, Line: line}, DefaultResampling()); none.Drawn != 0 {
		t.Errorf("an above-band line resampled the band's windows: drew %d", none.Drawn)
	}
}

// The band is the 10th to the 90th percentile by nearest rank, skips a
// line that refuses or prices a non-finite figure, and leaves the answer's
// own flags alone.
func TestBandAnswerPercentiles(t *testing.T) {
	var rs LineResamples
	for i := 1; i <= 100; i++ {
		rs.Lines = append(rs.Lines, Resample{Line: itl.Model{A: float64(i)}})
	}
	rs.Lines = append(rs.Lines, Resample{Line: itl.Model{A: -1}}, Resample{Line: itl.Model{A: -2}})
	rs.Drawn = len(rs.Lines)
	a := Answer{Value: 50, Flags: []Flag{AssumedShape}}
	got := BandAnswer(a, rs, func(m itl.Model) (float64, bool) {
		switch m.A {
		case -1:
			return 0, false
		case -2:
			return math.NaN(), true
		}
		return m.A, true
	})
	if got.Band == nil || got.Band.Lo != 10 || got.Band.Hi != 90 {
		t.Errorf("band %+v, want [10, 90]", got.Band)
	}
	if got.HasFlag(TierUnstable) || len(got.Flags) != 1 || len(a.Flags) != 1 {
		t.Errorf("flags %v (the answer's own: %v), want only AssumedShape", got.Flags, a.Flags)
	}
}

// Above a tenth of the resamples changing tier, and only above it, the band
// is flagged.
func TestTierUnstableThreshold(t *testing.T) {
	for _, c := range []struct {
		changed int
		want    bool
	}{{20, false}, {21, true}} {
		rs := LineResamples{Drawn: 200}
		for i := range 200 {
			rs.Lines = append(rs.Lines, Resample{Line: itl.Model{A: 1}, TierChanged: i < c.changed})
		}
		got := BandAnswer(Answer{Value: 1}, rs, func(itl.Model) (float64, bool) { return 1, true })
		if got.HasFlag(TierUnstable) != c.want {
			t.Errorf("%d of 200 changed: TierUnstable %v, want %v", c.changed, got.HasFlag(TierUnstable), c.want)
		}
	}
}

// The tier reaches every Basis, and the admission chain's too.
func TestTierReachesTheBasis(t *testing.T) {
	d := olsSweep()
	d.Tier = TierOLS
	slo := SLOAtReplicas(d, ReplicasQuery{Headroom: 0.85})
	for name, r := range map[string]Result{"knee": slo.KneeRate, "rate": slo.Rate, "mean": slo.MeanTPOT,
		"question 1": rateFor(d, TPOTTarget{Seconds: 0.004}, 0.85), "fill": FillAtRate(d, 5)} {
		if got := mustAnswer(t, r).Basis.Tier; got != TierOLS {
			t.Errorf("%s: tier %q", name, got)
		}
	}
	adm, ok := DecodeAdmission(d, 5).(Admission)
	if !ok || adm.Basis.Tier != TierOLS {
		t.Errorf("admission %+v: want tier ols", adm)
	}
}

// PinnedDefaultB refuses a line whose slope was fitted against the default
// intercept -- pinned-B, held, or borrowed from a donor that was -- within a
// tolerance, and nothing else: an ols line that lands on it is a fit, and an
// above-band line pins its own least ITL.
func TestPinnedDefaultB(t *testing.T) {
	def := itl.DefaultBaselineSec
	for _, c := range []struct {
		tier   string
		b      float64
		refuse bool
	}{
		{TierPinnedB, def, true},
		{TierHeld, def, true},
		{TierBorrowed, def, true},
		{TierPinnedB, def * (1 + 1e-12), true},
		{TierPinnedB, def * (1 + 1e-6), false},
		{TierPinnedB, 0.0015, false},
		{TierOLS, def, false},
		{TierAboveBand, def, false},
	} {
		d := olsSweep()
		d.Tier, d.Line.B = c.tier, c.b
		u, refused := KneeRate(d).(Unanswerable)
		if refused != c.refuse || refused && u.Reason != PinnedDefaultB {
			t.Errorf("%s at B %.15g: %#v, want refused %v", c.tier, c.b, KneeRate(d), c.refuse)
		}
	}
}
