package slo

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// Issue #467: the slo line priced one-sided at the sequence cap while B is
// unlearned (SwitchITLOneSidedAtCap), through the line's pricing
// (priceVariant), on the engine's rig. The slo line is priced at the fill
// where it meets the TPOT target: a lower fill is more replicas, so a line
// priced under the true one meets the target at a higher fill.

// sloCapTruth is a true line, a fill k_cap a replica at its cap sits at, and
// the fill the true line meets the TPOT target at.
type sloCapTruth struct{ a, b, kCap float64 }

func (l sloCapTruth) itl(k float64) float64 { return l.a*k + l.b }

func (l sloCapTruth) String() string {
	return fmt.Sprintf("A %.2f B %.0f ms k_cap %.2f", l.a, 1000*l.b, l.kCap)
}

// sloTruthFill is the fill each truth meets its target at: inside the band.
const sloTruthFill = 0.5

// target is the TPOT target the true line meets at sloTruthFill.
func (l sloCapTruth) target() float64 { return l.itl(sloTruthFill) }

// sloCapTruths is issue #467's bias grid.
var sloCapTruths = []sloCapTruth{{0.05, 0.010, 0.97}, {0.05, 0.030, 0.97}, {0.01, 0.020, 0.97}, {0.01, 0.030, 0.97}}

// capRig is the settle rig over a store stating a cap of 64 for agg.
func capRig(t *testing.T, switches map[string]bool) *settleRig {
	t.Helper()
	r := newSettleRig(t, switches)
	s := capacity.NewStore()
	s.Update("ns", "m", "agg", capacity.Record{EngineParams: &capacity.EngineParams{MaxNumSeqs: 256, DeclaredBatchCap: 64}}, r.now)
	r.a.store = s
	return r
}

// capRows is three replicas of agg at their cap on truth l, at k_cap - 0.01,
// k_cap and k_cap + 0.01, each ITL scaled by (1 + noise[i]).
func capRows(l sloCapTruth, noise [3]float64) []domain.ReplicaMetrics {
	out := make([]domain.ReplicaMetrics, 0, 3)
	for i, k := range []float64{l.kCap - 0.01, l.kCap, l.kCap + 0.01} {
		r := row("agg", fmt.Sprintf("agg-%d", i), k)
		r.AvgITL = l.itl(k) * (1 + noise[i])
		r.RunningRequests, r.QueueLength, r.PreemptionRateKnown = 64, 5, true
		out = append(out, r)
	}
	return out
}

// pricedAtTarget runs n cycles of rows on r at l's target and returns the
// last cycle's line and the fill it meets the target at (whatif.PriceTPOT,
// the band's default), NaN with no line.
func pricedAtTarget(r *settleRig, l sloCapTruth, n int, rows func(c int) []domain.ReplicaMetrics) (itl.Reading, float64) {
	r.t.Helper()
	var read itl.Reading
	for c := range n {
		read = r.cycleAt(rows(c), l.target())
	}
	if read.Tier == "" {
		return read, math.NaN()
	}
	p := whatif.PriceTPOT(whatif.DefaultBand(), read.Model, l.target())
	if !p.OK || !p.Feasible {
		return read, math.NaN()
	}
	return read, p.Fill
}

// cycleAt is cycle at a TPOT target in seconds.
func (r *settleRig) cycleAt(rows []domain.ReplicaMetrics, target float64) itl.Reading {
	r.t.Helper()
	in := domain.AnalyzerInput{Namespace: "ns", ModelID: "m", Config: policy(time.Duration(target*float64(time.Second)), 0),
		VariantStates: []domain.VariantReplicaState{settleAgg}, ReplicaMetrics: rows}
	r.tr.Observe(utils.GetNamespacedKey("ns", "m"), settlerow.Of(rows, map[string]string{"agg": domain.RoleBoth}), r.now)
	_, _, d := r.a.priceVariant(in, settleAgg, windowKey("ns", "m", settleAgg), readFleet(in), target, 0.85, r.now)
	r.now = r.now.Add(settleCycle)
	return d.line
}

// priorFill is the fill, in the default band, where the prior's line meets
// l's target, on readings without noise: the line with intercept bPrior
// through the least reading (k_cap - 0.01), its slope floored at 0; NaN where
// it meets it nowhere in the band (the analyzer abstains), which is where the
// prior's error over B is larger than the target's headroom over B.
func priorFill(l sloCapTruth, bPrior float64) float64 {
	kr := l.kCap - 0.01
	m := itl.Model{A: max(0, (l.itl(kr)-bPrior)/kr), B: bPrior}
	p := whatif.PriceTPOT(whatif.DefaultBand(), m, l.target())
	if !p.OK || !p.Feasible {
		return math.NaN()
	}
	return p.Fill
}

// sloRow measures one truth on and off, with and without noise, prints its
// row and holds it: off above the truth's fill (the negative control); on
// exactly the prior's line's fill, at or under the truth's while B is at most
// the prior; infeasible exactly where the prior's line is.
func sloRow(t *testing.T, l sloCapTruth, prime func(*settleRig), bPrior float64) (on float64) {
	t.Helper()
	const seeds = 20
	rig := func(switches map[string]bool) *settleRig {
		r := capRig(t, switches)
		if prime != nil {
			prime(r)
		}
		return r
	}
	rowsOf := func(n [3][]float64) func(c int) []domain.ReplicaMetrics {
		return func(c int) []domain.ReplicaMetrics {
			if n[0] == nil {
				return capRows(l, [3]float64{})
			}
			return capRows(l, [3]float64{n[0][c], n[1][c], n[2][c]})
		}
	}
	_, off := pricedAtTarget(rig(map[string]bool{SwitchITLOneSidedAtCap: false}), l, 24, rowsOf([3][]float64{}))
	read, on := pricedAtTarget(rig(nil), l, 24, rowsOf([3][]float64{}))
	lo, hi, infeasible := math.Inf(1), math.Inf(-1), 0
	for seed := uint64(1); seed <= seeds; seed++ {
		rnd := learnertest.Rand(seed)
		n := [3][]float64{learnertest.ARNoise(rnd, 24, 0.02, 0.5), learnertest.ARNoise(rnd, 24, 0.02, 0.5),
			learnertest.ARNoise(rnd, 24, 0.02, 0.5)}
		_, f := pricedAtTarget(rig(nil), l, 24, rowsOf(n))
		if math.IsNaN(f) {
			infeasible++
			continue
		}
		lo, hi = min(lo, f), max(hi, f)
	}
	want := priorFill(l, bPrior)
	t.Logf("| %.0f ms | %.2f | %.0f ms | %.2f | %.4f | %.4f | %.4f | [%.4f, %.4f], infeasible %d of %d | %.2f |",
		1000*bPrior, l.a, 1000*l.b, l.kCap, off, on, want, lo, hi, infeasible, seeds, sloTruthFill)
	require.Greater(t, off, sloTruthFill, "negative control, %s: off did not price above the truth's fill", l)
	require.True(t, read.PinnedAtBPrior, "%s: on, not priced at the prior", l)
	if math.IsNaN(want) {
		require.True(t, math.IsNaN(on), "%s: on priced %.4f where the prior's line meets the target nowhere", l, on)
		return on
	}
	require.InDelta(t, want, on, 1e-9, "%s: on is not the prior's line's fill", l)
	return on
}

// The bias table at the slo line's price, the fill where it meets the TPOT
// target, against the truth's (0.5), at the prior itl.DefaultBPrior (30 ms)
// and at a learned prior (a sibling's B of 20 ms). Off (the negative
// control) the line through the default B meets the target at a higher
// fill than the truth, every point of the grid: too few replicas. On, the
// fill is exactly the prior's line's: at or under the truth's while B is at
// most the prior, so never too few replicas, without noise; the target is met
// inside the band wherever the prior's line meets it, and the analyzer
// abstains (infeasible) only where even that line is past the target at the
// band's bottom. A true B above the prior (40 ms) meets it above the truth's
// fill: too few replicas, by the prior's bound. Noise moves the fill by the
// noise on the least reading: logged, not held.
func TestOneSided_TheBiasTableAtTheTarget(t *testing.T) {
	t.Log("| prior | A | B | k_cap | fill off | fill on | prior's line | on, 2 % noise: [least, most] | truth |")
	feasible := 0
	for _, l := range sloCapTruths {
		on := sloRow(t, l, nil, itl.DefaultBPrior)
		if !math.IsNaN(on) {
			feasible++
			require.LessOrEqual(t, on, sloTruthFill+1e-9, "%s: on priced a higher fill than the truth", l)
		}
	}
	require.Positive(t, feasible, "premise: no point of the grid priced a fill")
	above := sloRow(t, sloCapTruth{0.01, 0.040, 0.97}, nil, itl.DefaultBPrior)
	require.False(t, math.IsNaN(above) || above <= sloTruthFill, "B above the prior: %.4f, want a fill above the truth's", above)
	learned := func(r *settleRig) { r.a.lines.SetBaseline("ns|m|sibling|H100|1", 0.020) }
	for _, l := range []sloCapTruth{{0.05, 0.010, 0.97}, {0.01, 0.020, 0.97}} {
		on := sloRow(t, l, learned, 0.020)
		require.False(t, on > sloTruthFill+1e-9, "learned prior, %s: on priced a higher fill than the truth", l)
	}
}

// The rule lasts while the key has no learned B: one-sided while unlearned;
// off once a full fit in the band learns B; back once the line is forgotten
// (ForgetVariant).
//
// The truth's B (20 ms) is under the prior, a sibling's learned 30 ms, so
// the prior's line is above the truth and a raise after B is learned shows.
func TestOneSided_ALearnedBEndsItAndAForgetBringsItBack(t *testing.T) {
	l := sloCapTruth{0.05, 0.020, 0.97}
	r := capRig(t, nil)
	r.a.lines.SetBaseline("ns|m|sibling|H100|1", 0.030)
	capOnly := func(int) []domain.ReplicaMetrics { return capRows(l, [3]float64{}) }
	read, _ := pricedAtTarget(r, l, 12, capOnly)
	require.False(t, read.BaselineLearned)
	require.True(t, read.PinnedAtBPrior, "unlearned: not priced one-sided")

	pricedAtTarget(r, l, 64, func(c int) []domain.ReplicaMetrics {
		k := 0.25
		if (c/16)%2 == 1 {
			k = 0.65
		}
		b0, b1 := row("agg", "band-0", k), row("agg", "band-1", 0.45)
		b0.AvgITL, b1.AvgITL = l.itl(k), l.itl(0.45)
		return append(capOnly(c), b0, b1)
	})
	read, _ = pricedAtTarget(r, l, 140, capOnly)
	require.True(t, read.BaselineLearned, "premise: the band's full fit learned B")
	require.Positive(t, read.Capped)
	require.False(t, read.PinnedAtBPrior, "learned: still priced one-sided")
	require.InDelta(t, l.itl(0.5), read.Model.ITLAt(0.5), 1e-9, "learned: not the true line")

	require.Positive(t, r.a.ForgetVariant(context.Background(), "ns", "m", "agg"))
	read, _ = pricedAtTarget(r, l, 24, capOnly)
	require.False(t, read.BaselineLearned)
	require.True(t, read.PinnedAtBPrior, "forgotten: the one-sided price did not come back")
}

// A held slope keeps the one-sided price, and ends with the hold: once the
// capped replicas leave, the line holds the last line fitted, which met the
// target at the prior's line's fill (here B is the prior, so the truth's,
// 0.5), until the slope is older than slopeHoldFor (1 h here); then it is
// silent. Off (the negative control), the held line meets the target above
// the truth's fill.
func TestOneSided_TheHeldSlopeKeepsThePriceAndEndsWithTheHold(t *testing.T) {
	l := sloCapTruth{0.01, 0.030, 0.97}
	feasible := func(m itl.Model) (float64, bool) {
		p := whatif.PriceTPOT(whatif.DefaultBand(), m, l.target())
		return p.Fill, p.OK && p.Feasible
	}
	type outcome struct {
		heldFill                       float64
		heldFeasible                   bool
		lastFit, lastHeld, firstSilent time.Duration
	}
	run := func(switches map[string]bool) outcome {
		r := capRig(t, switches)
		r.a.WithSlopeHoldFor(time.Hour)
		t0 := r.now
		for range 20 {
			r.cycleAt(capRows(l, [3]float64{}), l.target())
		}
		idle := []domain.ReplicaMetrics{row("agg", "agg-0", 0.05), row("agg", "agg-1", 0.05), row("agg", "agg-2", 0.05)}
		var o outcome
		for r.now.Before(t0.Add(2 * time.Hour)) {
			at := r.now.Sub(t0)
			read := r.cycleAt(idle, l.target())
			switch {
			case read.Tier != "" && !read.SlopeHeld:
				o.lastFit = at
			case read.Tier != "":
				o.heldFill, o.heldFeasible = feasible(read.Model)
				o.lastHeld = at
			case o.firstSilent == 0 && o.lastHeld > 0:
				o.firstSilent = at
			}
		}
		return o
	}
	o := run(nil)
	t.Logf("last fit %s, held to %s (feasible %v), silent from %s", o.lastFit, o.lastHeld, o.heldFeasible, o.firstSilent)
	require.NotZero(t, o.lastHeld, "premise: the slope was held")
	require.True(t, o.heldFeasible, "premise: the held line meets the target in the band")
	require.InDelta(t, priorFill(l, itl.DefaultBPrior), o.heldFill, 1e-9, "the held line does not keep the prior's fill")
	require.NotZero(t, o.firstSilent, "the hold never ended")
	require.Equal(t, settleCycle, o.firstSilent-o.lastHeld)
	require.True(t, o.lastHeld-o.lastFit <= time.Hour && o.firstSilent-o.lastFit > time.Hour,
		"the hold ended %s after the last fit, want just past 1h", o.firstSilent-o.lastFit)

	off := run(map[string]bool{SwitchITLOneSidedAtCap: false})
	t.Logf("off: held fill %.4f (feasible %v)", off.heldFill, off.heldFeasible)
	require.True(t, off.heldFeasible && off.heldFill > sloTruthFill,
		"negative control: off, the held line did not meet the target above the truth's fill")
}

// The switch is a replay switch the analyzer accepts, and off is the line
// before #467.
func TestOneSided_TheSwitchPicksTheLine(t *testing.T) {
	require.Contains(t, ReplaySwitchNames(), SwitchITLOneSidedAtCap)
	l := sloCapTruth{0.05, 0.030, 0.97}
	capOnly := func(int) []domain.ReplicaMetrics { return capRows(l, [3]float64{}) }
	on, _ := pricedAtTarget(capRig(t, nil), l, 12, capOnly)
	off, _ := pricedAtTarget(capRig(t, map[string]bool{SwitchITLOneSidedAtCap: false}), l, 12, capOnly)
	require.True(t, on.PinnedAtBPrior)
	require.False(t, off.PinnedAtBPrior)
	require.Equal(t, itl.DefaultBaselineSec, off.Model.B)
}
