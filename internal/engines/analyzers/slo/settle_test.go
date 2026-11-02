package slo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// The slo line on the settle tracker (settle.go): its four switches, each
// on and off, through the line's pricing (priceVariant) on the engine's
// rig: the engine's tracker, at a 15 s interval, observed before each
// cycle. Off is the line before learner PR 4a, the negative control the
// switch must fail.

const settleCycle = 15 * time.Second

// settleCycles is how many cycles the default steadyGate.steadyFor spans.
var settleCycles = int(itl.DefaultSteadyFor / settleCycle)

// settleRig is the slo analyzer as the engine runs it, from a controller
// start at t0.
type settleRig struct {
	t   *testing.T
	a   *Analyzer
	tr  *settle.Tracker
	now time.Time
}

func newSettleRig(t *testing.T, switches map[string]bool) *settleRig {
	t.Helper()
	tr := settle.NewTracker()
	tr.SetInterval(settleCycle)
	r := &settleRig{t: t, tr: tr, now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	r.a = New(nil, nil).WithSettleView(tr)
	r.a.now = func() time.Time { return r.now }
	require.NoError(t, r.a.SetSwitchesForReplay(switches))
	return r
}

// cycle observes rows as the engine does, prices agg once, moves the clock
// a cycle on, and returns what the line read.
func (r *settleRig) cycle(rows []domain.ReplicaMetrics) itl.Reading {
	r.t.Helper()
	in := domain.AnalyzerInput{Namespace: "ns", ModelID: "m", Config: policy(44*time.Millisecond, 0),
		VariantStates: []domain.VariantReplicaState{settleAgg}, ReplicaMetrics: rows}
	r.tr.Observe(utils.GetNamespacedKey("ns", "m"), settlerow.Of(rows, map[string]string{"agg": domain.RoleBoth}), r.now)
	_, _, d := r.a.priceVariant(in, settleAgg, windowKey("ns", "m", settleAgg), readFleet(in), 0.044, 0.85, r.now)
	r.now = r.now.Add(settleCycle)
	return d.line
}

var settleAgg = domain.VariantReplicaState{VariantName: "agg", Role: domain.RoleBoth,
	AcceleratorName: "H100", GPUsPerReplica: 1, CurrentReplicas: 3}

// settleFleet is three replicas of agg at distinct, steady fills.
func settleFleet() []domain.ReplicaMetrics {
	return []domain.ReplicaMetrics{row("agg", "agg-a", 0.30), row("agg", "agg-b", 0.45), row("agg", "agg-c", 0.60)}
}

// settlingCycles is the cycles, of n, on which a reading was kept out as
// its replica's settle.
func settlingCycles(r *settleRig, n int, rows func(i int) []domain.ReplicaMetrics) []int {
	var out []int
	for i := range n {
		if r.cycle(rows(i)).Settling > 0 {
			out = append(out, i)
		}
	}
	return out
}

func span(from, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = from + i
	}
	return out
}

// A controller restart is a settle: the line does not refit for steadyFor
// from the tracker's first sight of the fleet. Off, as before, the first
// cycle's replicas are taken as settled.
func TestSettle_AControllerRestartWaitsTheSettle(t *testing.T) {
	fleet := func(int) []domain.ReplicaMetrics { return settleFleet() }
	assert.Equal(t, span(0, settleCycles), settlingCycles(newSettleRig(t, nil), 2*settleCycles, fleet))
	assert.Empty(t, settlingCycles(newSettleRig(t, map[string]bool{SwitchITLWaitsAfterRestart: false}), 2*settleCycles, fleet),
		"switch off: taken as settled")
}

// The analyzer alone, with no engine tracker, has watched the fleet:
// nothing waits on its first cycles.
func TestSettle_TheAnalyzerAloneDoesNotWait(t *testing.T) {
	a := New(nil, nil)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	for range 3 {
		in := domain.AnalyzerInput{Namespace: "ns", ModelID: "m", Config: policy(44*time.Millisecond, 0),
			VariantStates: []domain.VariantReplicaState{settleAgg}, ReplicaMetrics: settleFleet()}
		a.observeOwnSettle(in, now)
		_, _, d := a.priceVariant(in, settleAgg, windowKey("ns", "m", settleAgg), readFleet(in), 0.044, 0.85, now)
		assert.Zero(t, d.line.Settling)
		assert.Equal(t, 3, d.line.Offered, "premise: the rows are readings")
		now = now.Add(settleCycle)
	}
}

// One missing row keeps the replica's settle. Off, as before, the replica
// starts over when it comes back.
func TestSettle_OneMissingRowKeepsTheSettle(t *testing.T) {
	missAt := settleCycles + 2
	rows := func(i int) []domain.ReplicaMetrics {
		f := settleFleet()
		if i == missAt {
			return f[:2]
		}
		return f
	}
	n := missAt + settleCycles + 3
	assert.Equal(t, span(0, settleCycles), settlingCycles(newSettleRig(t, nil), n, rows))
	assert.Equal(t, append(span(0, settleCycles), span(missAt+1, settleCycles)...),
		settlingCycles(newSettleRig(t, map[string]bool{SwitchITLKeepsSettleThroughMissingRow: false}), n, rows),
		"switch off: back after one missed row, it waits again")
}

// A new Ready time under the same Pod name is an engine restart: the
// replica waits steadyFor. Off, the line's own trace missed it.
func TestSettle_ANewReadyTimeUnderTheSameNameStartsTheSettle(t *testing.T) {
	restartAt := settleCycles + 2
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	rows := func(i int) []domain.ReplicaMetrics {
		f := settleFleet()
		for j := range f {
			f[j].ReadySince = t0.Add(-time.Hour)
		}
		if i >= restartAt {
			f[2].ReadySince = t0.Add(time.Duration(restartAt)*settleCycle - 5*time.Second)
		}
		return f
	}
	n := restartAt + settleCycles + 3
	assert.Equal(t, append(span(0, settleCycles), span(restartAt, settleCycles)...),
		settlingCycles(newSettleRig(t, nil), n, rows))
	assert.Equal(t, span(0, settleCycles), settlingCycles(newSettleRig(t, map[string]bool{SwitchITLReadsSettle: false}), n, rows),
		"switch off: the restart is missed")
}

// A Pod with an Absent rank is no reading. Off, its Finite ranks' mean was
// one.
func TestSettle_APodWithAnAbsentRankIsNoReading(t *testing.T) {
	rows := settleFleet()
	rows[2].AvgITLKnown = false
	on := newSettleRig(t, nil).cycle(rows)
	assert.Equal(t, 2, on.Offered)
	assert.Equal(t, 1, on.NoITL)
	off := newSettleRig(t, map[string]bool{SwitchITLReadsFinitePodMean: false}).cycle(rows)
	assert.Equal(t, 3, off.Offered, "switch off: the Absent pod's mean is a reading")
	assert.Zero(t, off.NoITL)
}
