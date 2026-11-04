package whatif

import (
	"math"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// readingsOn is n cycles of two replicas' readings on the line, with noise
// that alternates in sign so a fit is exact on average but each resample
// moves it.
func readingsOn(line itl.Model, n int, noise float64) []itl.Observation {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	out := make([]itl.Observation, 0, 2*n)
	for c := range n {
		at := t0.Add(time.Duration(c) * 15 * time.Second)
		for r, k := range []float64{0.2 + 0.5*float64(c%7)/7, 0.25 + 0.5*float64((c+3)%7)/7} {
			sign := 1.0
			if (c+r)%2 == 1 {
				sign = -1
			}
			out = append(out, itl.Observation{K: k, ITLSec: line.ITLAt(k) * (1 + sign*noise), Timestamp: at})
		}
	}
	return out
}

func TestBootstrapLinesIsReproducibleAndMoves(t *testing.T) {
	line := itl.Model{A: 0.004438, B: 0.001557}
	obs := readingsOn(line, 40, 0.05)
	fit := Refit{Tier: TierOLS, PinnedB: line.B}
	a := BootstrapLines(obs, fit, DefaultResampling())
	b := BootstrapLines(obs, fit, DefaultResampling())
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
	other := BootstrapLines(obs, fit, Resampling{Block: 4, Resamples: 200, Seed: 2})
	if other.Lines[0] == a.Lines[0] && other.Lines[1] == a.Lines[1] {
		t.Errorf("seed 2 drew what seed 1 did")
	}
}

// A resample keeps whole cycles: every replica's reading of a cycle comes
// with the others, so a block of 4 cycles never splits one.
func TestBootstrapKeepsCyclesWhole(t *testing.T) {
	obs := readingsOn(itl.Model{A: 0.004, B: 0.002}, 9, 0)
	cycles := cyclesOf(obs)
	if len(cycles) != 9 {
		t.Fatalf("%d cycles, want 9", len(cycles))
	}
	for i, c := range cycles {
		if len(c) != 2 {
			t.Errorf("cycle %d holds %d readings, want both replicas'", i, len(c))
		}
	}
}

func TestBootstrapHasNothingToResample(t *testing.T) {
	line := itl.Model{A: 0.004, B: 0.002}
	cases := map[string]LineResamples{
		"one cycle":      BootstrapLines(readingsOn(line, 1, 0.05), Refit{Tier: TierOLS, PinnedB: line.B}, DefaultResampling()),
		"a held line":    BootstrapLines(readingsOn(line, 40, 0.05), Refit{Tier: TierHeld, PinnedB: line.B}, DefaultResampling()),
		"a borrowed one": BootstrapLines(readingsOn(line, 40, 0.05), Refit{Tier: TierBorrowed}, DefaultResampling()),
		"no resamples":   BootstrapLines(readingsOn(line, 40, 0.05), Refit{Tier: TierOLS}, Resampling{Block: 4}),
	}
	for name, rs := range cases {
		if rs.Drawn != 0 || len(rs.Lines) != 0 {
			t.Errorf("%s: drew %d, fitted %d; want nothing", name, rs.Drawn, len(rs.Lines))
		}
	}
	a := Answer{Value: 1}
	if got := BandAnswer(a, LineResamples{}, func(itl.Model) (float64, bool) { return 1, true }); got.Band != nil {
		t.Errorf("banded from no resamples: %+v", got.Band)
	}
}

// Readings at one k give no full fit: every resample falls back to pinned-B,
// so the tier is unstable, and the band still comes from the pinned fits.
func TestBootstrapFallsBackToPinnedB(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	obs := make([]itl.Observation, 0, 20)
	for c := range 20 {
		obs = append(obs, itl.Observation{K: 0.4, ITLSec: 0.0035 + 0.0001*float64(c%3), Timestamp: t0.Add(time.Duration(c) * time.Minute)})
	}
	rs := BootstrapLines(obs, Refit{Tier: TierOLS, PinnedB: 0.0015}, DefaultResampling())
	if len(rs.Lines) != rs.Drawn || rs.Drawn != 200 {
		t.Fatalf("drew %d, fitted %d", rs.Drawn, len(rs.Lines))
	}
	for _, s := range rs.Lines {
		if !s.TierChanged || s.Line.B != 0.0015 {
			t.Fatalf("resample %+v: want a pinned-B fit at B 0.0015, flagged a change of tier", s)
		}
	}
	got := BandAnswer(Answer{Value: 0.0035}, rs, func(m itl.Model) (float64, bool) { return m.ITLAt(0.4), true })
	if !got.HasFlag(TierUnstable) || got.Band == nil {
		t.Errorf("answer %+v: want a band flagged TierUnstable", got)
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

// PinnedDefaultB refuses a pinned-B or held line on the default intercept,
// and nothing else: an ols line that happens to land on it is a fit.
func TestPinnedDefaultB(t *testing.T) {
	for _, c := range []struct {
		tier   string
		b      float64
		refuse bool
	}{
		{TierPinnedB, itl.DefaultBaselineSec, true},
		{TierHeld, itl.DefaultBaselineSec, true},
		{TierPinnedB, 0.0015, false},
		{TierOLS, itl.DefaultBaselineSec, false},
		{TierBorrowed, itl.DefaultBaselineSec, false},
	} {
		d := olsSweep()
		d.Tier, d.Line.B = c.tier, c.b
		u, refused := KneeRate(d).(Unanswerable)
		if refused != c.refuse || refused && u.Reason != PinnedDefaultB {
			t.Errorf("%s at B %g: %#v, want refused %v", c.tier, c.b, KneeRate(d), c.refuse)
		}
	}
}
