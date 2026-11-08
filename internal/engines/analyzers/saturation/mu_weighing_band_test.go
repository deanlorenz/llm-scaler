package saturation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The weighing's band (muChoice.weighs, SwitchMuWeighingBand): a key starts
// weighing when the readers' rate over what the unread replicas count at (the
// ratio), averaged over WeighSetWindow, sits under SiblingMuTolerance (0.80);
// it stops no sooner than WeighClearWindow after, when the ratio averaged over
// that window reaches 0.90, and at once with no unread replica.

// weighAt feeds the latch one reading a cycle from start, n unread replicas,
// h 1, and returns its choices.
func weighAt(c *muChoice, start time.Time, ratios []float64, n int) []bool {
	out := make([]bool, len(ratios))
	for i, x := range ratios {
		out[i] = c.weighs(x, 1, n, SiblingMuTolerance, start.Add(time.Duration(i)*propCycle))
	}
	return out
}

// repeat is v, k times.
func repeat(v float64, k int) []float64 {
	out := make([]float64, k)
	for i := range out {
		out[i] = v
	}
	return out
}

// The band unit: set, hold inside the band and inside the clear window,
// clear, and the cases that skip the windows.
func TestWeighingBand_SetHoldClear(t *testing.T) {
	require.InDelta(t, 0.90, weighClear(SiblingMuTolerance), 1e-12, "clear at 0.90 by default")
	require.Equal(t, 1.0, weighClear(0.97), "the clear never passes 1")
	require.Equal(t, 1.0, weighClear(1), "a tolerance of 1 has no band over it")
	setCycles := int(WeighSetWindow / propCycle)
	clearCycles := int(WeighClearWindow / propCycle)

	// A key's first reading under the tolerance sets it: its window holds
	// that reading alone. Over it, the key stays out.
	require.Equal(t, []bool{true}, weighAt(&muChoice{}, propStart, []float64{0.79}, 3), "first reading under 0.80 sets")
	require.Equal(t, []bool{false}, weighAt(&muChoice{}, propStart, []float64{0.80}, 3), "first reading at 0.80 does not")

	// Set on the window's mean, not a reading: after five minutes at 0.95 a
	// reading at 0.5 does not set, and the readings that follow at 0.5 set
	// once the five-minute mean is under 0.80.
	c := &muChoice{}
	got := weighAt(c, propStart, append(repeat(0.95, setCycles), repeat(0.5, setCycles)...), 3)
	first := -1
	for i, w := range got {
		if w {
			first = i
			break
		}
	}
	// The mean of the last 20 readings is under 0.80 once 7 of them read
	// 0.5: 0.95 - 0.45 x 7 / 20 = 0.7925, and 0.815 at 6.
	require.Equal(t, setCycles+6, first, "set when the window's mean crosses 0.80, not on the first low reading")
	t.Logf("a step from 0.95 to 0.5 set after %s", time.Duration(first-setCycles+1)*propCycle)

	// Held inside the clear window at any ratio, then inside the band, then
	// cleared once the window's mean reaches 0.90.
	c = &muChoice{}
	start := propStart
	require.Equal(t, []bool{true}, weighAt(c, start, []float64{0.5}, 3))
	held := weighAt(c, start.Add(propCycle), repeat(1.0, clearCycles-1), 3)
	for i, w := range held {
		require.True(t, w, "cycle %d inside the clear window: a weighing key holds at 1.0", i+1)
	}
	c = &muChoice{}
	got = weighAt(c, start, append([]float64{0.5}, repeat(0.85, 3*clearCycles)...), 3)
	for i, w := range got {
		require.True(t, w, "cycle %d: a weighing key holds while its mean sits under 0.90", i)
	}
	c = &muChoice{}
	got = weighAt(c, start, append([]float64{0.5}, repeat(0.92, 3*clearCycles)...), 3)
	cleared := -1
	for i, w := range got {
		if !w {
			cleared = i
			break
		}
	}
	require.Equal(t, clearCycles, cleared, "cleared once the clear window has run and its mean is over 0.90")
	require.False(t, c.weighs(0.85, 1, 3, SiblingMuTolerance, start.Add(time.Duration(len(got))*propCycle)),
		"cleared, a reading inside the band does not set it again")

	// No unread replica clears at once, and forgets the ratios.
	c = &muChoice{}
	require.True(t, c.weighs(0.5, 1, 3, SiblingMuTolerance, start))
	require.False(t, c.weighs(0.5, 1, 0, SiblingMuTolerance, start.Add(propCycle)), "no replica left to count clears inside the clear window")
	require.Empty(t, c.weighRatios)
	require.True(t, c.weighs(0.5, 1, 3, SiblingMuTolerance, start.Add(2*propCycle)), "the replicas back, the next reading under 0.80 sets again")
}

// weighNoise is one noise level of the excursion.
type weighNoise struct{ sigma, phi float64 }

// weighNoises are the levels the band is held at: the #524 case's (4 %, phi
// 0.9), and the per-replica rate noise measured on E1, 9 % to 19 % with a
// lag-one correlation of 0.1 to 0.4 (10 % and 15 % at phi 0.3, and at 0.75).
var weighNoises = []weighNoise{{0.04, 0.9}, {0.10, 0.3}, {0.10, 0.75}, {0.15, 0.3}, {0.15, 0.75}}

// weighStart is the cycles from a key's first reading before the changes an
// excursion is held to are counted: the set window and the clear window.
// A key's first set is on its first reading, so at 10 % to 15 % noise a
// first reading in a noise trough sets it, and it clears after the clear
// window; that is the key's start, not the excursion (the first-sample
// class), and is logged, not held.
const weighStart = int((5*time.Minute + 10*time.Minute) / propCycle)

// weighTrail is checkWeighExcursion's rig (property_ar_edges_test.go) at a
// noise level, switches set: one replica queued with a full cache beside
// three unqueued at half their cache serving 0.6, the queued one's ratio
// following path. It returns, per cycle with a sample, the latch (with the
// band on) or the bare tolerance's choice (off: the sample is over the
// readers' mean exactly when it weighed).
func weighTrail(seed uint64, path []float64, nz weighNoise, switches map[string]bool) ([]bool, error) {
	r := propRand(seed)
	const served = 0.6
	h := served / floorTarget
	noise := arNoise(r, len(path), nz.sigma, nz.phi)
	rig := newPropRig()
	if err := rig.a.SetSwitchesForReplay(switches); err != nil {
		return nil, err
	}
	band := rig.a.muWeighingBand
	var out []bool
	for i, x := range path {
		slow := x * h * (1 + noise[i])
		if _, err := rig.analyze(slowPodRows(4, 1, slow, served, 0.5), aggStates(4), slow+3*served, propCycle); err != nil {
			return nil, err
		}
		sample, read, ok := rig.weighSample()
		if !ok {
			if i < 4 {
				continue
			}
			return nil, fmt.Errorf("cycle %d: no sample", i)
		}
		w := sample > read*(1+1e-9)
		if band {
			rig.a.mu.Lock()
			for _, c := range rig.a.throughputMuChoice {
				w = c.weighing
			}
			rig.a.mu.Unlock()
		}
		out = append(out, w)
	}
	return out, nil
}

// The excursion of edges/mu/sibling-tolerance-excursion (0.95 -> 0.65 over
// 30 min) and the same crossing upward (a slow replica recovering, 0.65 ->
// 0.95), at every noise level of weighNoises, 20 seeds each. With the band on
// the source changes at most once in the excursion and ends on the path's
// side; at 4 % that holds from the first cycle. Negative control: the bare
// tolerance (the band off) changes it more than once on most seeds at every
// level.
func TestWeighingBand_OneChangePerExcursion(t *testing.T) {
	for _, nz := range weighNoises {
		for _, ex := range []struct {
			name string
			path []float64
			end  bool
		}{
			{"down 0.95 -> 0.65", arPath(0.95, 0.65, 8, 120, 40), true},
			// 20 min at the end: at 15 % noise the clear window's mean of a
			// ratio at 0.95 can sit under 0.90 for 10 min (seed 10 at phi
			// 0.75 still weighed at 10 min).
			{"up 0.65 -> 0.95", arPath(0.65, 0.95, 8, 120, 80), false},
		} {
			name := fmt.Sprintf("%s, noise %.0f%% phi %.2f", ex.name, 100*nz.sigma, nz.phi)
			from := weighStart
			if nz.sigma <= 0.04 {
				from = 0
			}
			onFail, offFail, startChanges := 0, 0, 0
			for seed := uint64(1); seed <= 20; seed++ {
				on, err := weighTrail(seed, ex.path, nz, nil)
				require.NoError(t, err)
				n, all := reversals(on[from:]), reversals(on)
				startChanges += all - n
				if n > 1 || on[len(on)-1] != ex.end {
					onFail++
					t.Logf("%s, seed %d: %d changes from cycle %d (%d in all), first at %v, ends weighing %v",
						name, seed, n, from, all, flipCycles(on, 4), on[len(on)-1])
				}
				off, err := weighTrail(seed, ex.path, nz, map[string]bool{SwitchMuWeighingBand: false})
				require.NoError(t, err)
				if reversals(off[from:]) > 1 {
					offFail++
				}
			}
			t.Logf("%s: band on fails %d of 20 seeds (%d changes at the keys' starts), off %d of 20", name, onFail, startChanges, offFail)
			require.Zero(t, onFail, "%s: the band changed source more than once in the excursion, or ended on the wrong side", name)
			require.Greater(t, offFail, 10, "%s: negative control: the bare tolerance flaps on most seeds", name)
		}
	}
}

// A slow replica that appears at 20 min into a settled fleet, for 10 min:
// the set window's delay writes samples of the readers alone into the window
// before the key weighs, and the window keeps them. At 5 min the probe stays
// inside its bound at every slowness, at the bare tolerance's peaks at 0.3
// and 0.55; at 0.65 the bare tolerance flapped on the slow replica's
// readings and sealed the fleet over the bound (negative control), and the
// band does not. Measured, band on: peaks 24, 21, 20 at 0.3, 0.55, 0.65; off:
// 24, 21, 24.
func TestWeighingBand_ALateSlowReplicaStaysInsideTheProbesBound(t *testing.T) {
	for _, c := range []struct {
		slow     float64
		peak     int
		offSeals bool
	}{
		{0.3, 24, false},
		{0.55, 21, false},
		{0.65, 21, true},
	} {
		run := func(on bool) probeResult {
			pc := slowReplicaCase(c.slow)
			pc.mu = func(i int, e time.Duration) float64 {
				if i == 0 && e >= 20*time.Minute && e < 30*time.Minute {
					return c.slow
				}
				return 1
			}
			pc.switches = map[string]bool{SwitchMuWeighingBand: on}
			return runProbe(t, pc)
		}
		on, off := run(true), run(false)
		t.Logf("late slow replica at %.2f: band on need %d final %d peak %d %q; off final %d peak %d %q",
			c.slow, on.need, on.final, on.peak, on.verdict(), off.final, off.peak, off.verdict())
		require.Empty(t, on.verdict(), "slow %.2f, band on", c.slow)
		require.LessOrEqual(t, on.peak, c.peak, "slow %.2f, band on", c.slow)
		if c.offSeals {
			require.Contains(t, off.verdict(), "over-ordered", "slow %.2f: negative control, the bare tolerance seals", c.slow)
		}
	}
}
