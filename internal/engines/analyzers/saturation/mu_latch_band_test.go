package saturation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The stale latch's band at the measured rate noise (#526,
// SwitchMuLatchReadsSmoothedRatio). The 1.6-2.0 band is narrower than the
// per-pod rate noise measured on E1 (7.5-18.9 %, lag-one 0.1-0.4), so on
// the cycle's own ratio one excursion set and released the latch several
// times. On the smoothed ratio it changes at most once.

// bandNoise is the measured rate noise, low and high, at the measured
// middle lag-one correlation: the AR(1) noise #524's latch cases run.
var bandNoise = []struct {
	name       string
	sigma, phi float64
}{{"sigma 10%", 0.10, 0.3}, {"sigma 15%", 0.15, 0.3}}

// bandPath is a noise-free ratio path: from for hold cycles, a straight
// ramp over ramp cycles, then to for rest cycles.
func bandPath(from, to float64, hold, ramp, rest int) []float64 {
	out := make([]float64, 0, hold+ramp+rest)
	for range hold {
		out = append(out, from)
	}
	for i := 1; i <= ramp; i++ {
		out = append(out, from+(to-from)*float64(i)/float64(ramp))
	}
	for range rest {
		out = append(out, to)
	}
	return out
}

// bandAnalyzer is a new analyzer with the switch set to on.
func bandAnalyzer(t testing.TB, on bool) *SaturationAnalyzer {
	t.Helper()
	a := NewSaturationAnalyzer(capacity.NewStore())
	require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchMuLatchReadsSmoothedRatio: on}))
	return a
}

// checkLatchBand is #524's edges/mu/latch-return-excursion with the switch
// set: a stale measurement starts at 2.5x the derived mu (0.4x on the low
// side), where it yields, and comes back across returnRatio once, on a
// 30 min ramp to 0.75 of it, or a step onto it held for 40 min. The choice
// changes at most once per excursion, and at the end of a ramp it has
// changed.
func checkLatchBand(tb testing.TB, tn config.AnalyzerTuning, sigma, phi float64, on bool) func(uint64) error {
	return func(seed uint64) error {
		r := propRand(seed)
		t := tuning{AnalyzerTuning: tn.WithDefaults()}
		maxR, retR := t.MuChoice.MaxRatio, t.MuChoice.ReturnRatio
		d := whatif.DerivedMu{Rate: 1, OK: true}
		sampled := propStart.Add(-2 * time.Hour)
		for _, side := range []struct {
			name      string
			inv, ramp bool
		}{{"high, ramp", false, true}, {"high, on the return ratio", false, false}, {"low, ramp", true, true}, {"low, on the return ratio", true, false}} {
			path := bandPath(1.25*maxR, 0.75*retR, 8, 120, 40)
			if !side.ramp {
				path = bandPath(1.25*maxR, retR, 8, 0, 160)
			}
			a := bandAnalyzer(tb, on)
			noise := arNoise(r, len(path), sigma, phi)
			noise[0] = 0
			v := make([]bool, len(path))
			for i, x := range path {
				if side.inv {
					x = 1 / x
				}
				now := propStart.Add(time.Duration(i) * propCycle)
				v[i] = a.measuredYieldsToDerived(propMuKey, throughputReading{rate: x * (1 + noise[i]), sampledAt: sampled}, d, 0, t, now)
			}
			if !v[0] {
				return violation(kindNotDetected, "%s: a stale measurement at %.2fx did not yield", side.name, path[0])
			}
			if n := reversals(v); n > 1 {
				return violation(kindFlipped, "%s (maxRatio %.2f, returnRatio %.2f): %d reversals in %d cycles", side.name, maxR, retR, n, len(v))
			}
			if side.ramp && v[len(v)-1] {
				return violation(kindStale, "%s: the ramp ended at %.2fx and the measurement still yields", side.name, path[len(path)-1])
			}
		}
		return nil
	}
}

// On, every seed of #524's latch cases holds at both measured noise levels,
// at the default return ratio and at 1.3. Off, the same cases fail, as #524
// ledgers them (default: 100 of 100 at either level; 1.3: 11 and 97 of
// 100): the switch is what the cases see.
func TestMuLatchBand_OneChangePerExcursionAtTheMeasuredNoise(t *testing.T) {
	ret13 := tuningWith(func(t *config.AnalyzerTuning) { t.MuChoice.ReturnRatio = 1.3 })
	for _, tc := range []struct {
		name string
		tn   config.AnalyzerTuning
		// offFailsAtLeast is the fewest seeds of 100 the switch off fails at
		// each noise level, below what #524 measured.
		offFailsAtLeast [2]int
	}{
		{"returnRatio 1.6", tuningDefault, [2]int{90, 90}},
		{"returnRatio 1.3", ret13, [2]int{1, 80}},
	} {
		for i, ns := range bandNoise {
			on, err := runCase(checkLatchBand(t, tc.tn, ns.sigma, ns.phi, true), arSeeds(cheapSeeds))
			require.NoError(t, err)
			assert.Empty(t, on.Failures, "%s, %s, switch on: %s", tc.name, ns.name, on)
			off, err := runCase(checkLatchBand(t, tc.tn, ns.sigma, ns.phi, false), arSeeds(cheapSeeds))
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(off.Failures), tc.offFailsAtLeast[i], "%s, %s, switch off: %s", tc.name, ns.name, off)
			t.Logf("%s, %s: on %s; off %s", tc.name, ns.name, on, off)
		}
	}
}

// arSeeds is seeds 1..n.
func arSeeds(n int) []uint64 {
	seeds := make([]uint64, n)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	return seeds
}

// stepLatchCycles is how many cycles after the ratio of a stale
// measurement steps from 1x to the ratio to, the latch sets, the step's cycle counted
// as 1; -1 when it never does in 40 cycles.
func stepLatchCycles(t *testing.T, on bool, to float64) int {
	a := bandAnalyzer(t, on)
	tn := tuning{AnalyzerTuning: tuningDefault.WithDefaults()}
	d := whatif.DerivedMu{Rate: 1, OK: true}
	sampled := propStart.Add(-2 * time.Hour)
	for i := range 20 {
		now := propStart.Add(time.Duration(i) * propCycle)
		require.False(t, a.measuredYieldsToDerived(propMuKey, throughputReading{rate: 1, sampledAt: sampled}, d, 0, tn, now))
	}
	for i := 1; i <= 40; i++ {
		now := propStart.Add(time.Duration(19+i) * propCycle)
		if a.measuredYieldsToDerived(propMuKey, throughputReading{rate: to, sampledAt: sampled}, d, 0, tn, now) {
			return i
		}
	}
	return -1
}

// The reaction time the smoothing costs: a stale measurement whose ratio
// steps from 1x to 2.5x sets the latch on the 14th cycle at 15 s (3.25 min;
// MuLatchRatioSmoothing times ln 3 is 3.3 min), and from 1x to 4x on the
// 5th (1.25 min). Off, it set on the step. The latch is not made useless:
// a real stale measurement latches within those times.
func TestMuLatchBand_AStaleStepLatchesWithinTheStatedTime(t *testing.T) {
	for _, tc := range []struct {
		to      float64
		on, off int
	}{{2.5, 14, 1}, {4, 5, 1}} {
		assert.Equal(t, tc.on, stepLatchCycles(t, true, tc.to), "1x -> %.1fx, switch on", tc.to)
		assert.Equal(t, tc.off, stepLatchCycles(t, false, tc.to), "1x -> %.1fx, switch off", tc.to)
	}
}

// A measurement whose ratio was past maxRatio while it was still fresh
// latches on the cycle it goes stale: the smoothed ratio is already there.
func TestMuLatchBand_ARatioSmoothedWhileFreshLatchesWhenStale(t *testing.T) {
	a := bandAnalyzer(t, true)
	tn := tuning{AnalyzerTuning: tuningDefault.WithDefaults()}
	d := whatif.DerivedMu{Rate: 1, OK: true}
	sampled := propStart
	fresh := tn.MuChoice.FreshFor.Duration()
	for i := 0; ; i++ {
		now := propStart.Add(time.Duration(i) * propCycle)
		got := a.measuredYieldsToDerived(propMuKey, throughputReading{rate: 2.5, sampledAt: sampled}, d, 0, tn, now)
		if now.Sub(sampled) < fresh {
			require.False(t, got, "cycle %d: fresh", i)
			continue
		}
		assert.True(t, got, "cycle %d: the first stale cycle", i)
		break
	}
}

// Another replica on the same cycle adds nothing to the smoothed ratio: one
// cycle's readings weigh as one, whatever the replica count.
func TestMuLatchBand_ASecondReadingOnOneCycleAddsNothing(t *testing.T) {
	c := &muChoice{}
	now := propStart
	assert.InDelta(t, 1.0, c.smoothRatio(1.0, now), 1e-12)
	next := now.Add(propCycle)
	first := c.smoothRatio(3.0, next)
	for range 10 {
		assert.InDelta(t, first, c.smoothRatio(3.0, next), 1e-12)
	}
	assert.Less(t, first, 1.2, "one cycle of 15 s moves the ratio by about 8 %% of the step")
}
