package throughput

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// dispersionKVreq runs an analyzer through Analyze on the steady fleet (1024
// tokens in, 256 out, no prefix hits), each row read through spread, and
// returns the KV per request its supply was priced at: one replica's supply is
// k_sat x KV_max / KVreq sequences over ITL(k_sat) (computeVariantSupply).
func dispersionKVreq(t *testing.T, on bool, spread func(*domain.ReplicaMetrics)) float64 {
	t.Helper()
	a := NewThroughputAnalyzer()
	require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchKVPerRequestOutputDispersion: on}))
	r := newLineRun(t, a)
	var (
		vc  domain.VariantCapacity
		has bool
	)
	for range 12 {
		rows := steadyFleet()
		for i := range rows {
			spread(&rows[i])
		}
		vc, has = r.step(rows)
	}
	require.True(t, has, "no capacity for v1: the case tests nothing")
	st, ok := a.VariantState("m", "ns", "v1")
	require.True(t, ok)
	itlSat := st.ITLModel.ITLAt(itl.DefaultKSat)
	require.Greater(t, itlSat, 0.0)
	require.Greater(t, vc.PerReplicaCapacity, 0.0)
	return itl.DefaultKSat * 65536 / (vc.PerReplicaCapacity * itlSat)
}

// spreadOf gives a row outputs of dispersion d about its mean.
func spreadOf(d float64) func(*domain.ReplicaMetrics) {
	return func(rm *domain.ReplicaMetrics) {
		rm.AvgOutputTokensSquared = d * rm.AvgOutputTokens * rm.AvgOutputTokens
		rm.AvgOutputTokensSquaredKnown = true
	}
}

// noSpread leaves a row as steadyFleet built it: no histogram read.
func noSpread(*domain.ReplicaMetrics) {}

// saturationKVreq is the KV per request the saturation analyzer prices the
// same fleet at: the fleet's shape with its request-weighted spread
// (saturation's fleetShape: shape.New(...).WithOutputDispersion(D), D =
// shape.Dispersion of the fleet's E[O] and its bound on E[O^2]).
func saturationKVreq(d float64) float64 {
	const il, ol = 1024.0, 256.0
	return shape.New(il, ol, 0).WithOutputDispersion(shape.Dispersion(ol, d*ol*ol)).KVreq
}

// Outputs of D 2 about a mean of 256 hold 1024 + 2 x 256/2 = 1280 tokens a
// resident request, the KV per request saturation prices the same fleet at;
// with the switch off, 1024 + 256/2 = 1152, as before.
func TestKVOutputDispersion_TheSupplyHoldsTheSpread(t *testing.T) {
	require.InDelta(t, 1280, saturationKVreq(2), 1e-9)
	require.InDelta(t, saturationKVreq(2), dispersionKVreq(t, true, spreadOf(2)), 1e-6)
	require.InDelta(t, 1152, dispersionKVreq(t, false, spreadOf(2)), 1e-6, "off: D 1, as before")
	require.True(t, NewThroughputAnalyzer().kvOutputDispersion, "on from the constructor")
}

// No reading of the spread is no spread: no histogram, a bound of 0, NaN or
// +Inf, or one row of three that read nothing (the mix of a read row and an
// unread one is neither fleet's spread). Each prices D 1.
func TestKVOutputDispersion_NoReadingIsNoSpread(t *testing.T) {
	bound := func(v float64) func(*domain.ReplicaMetrics) {
		return func(rm *domain.ReplicaMetrics) {
			rm.AvgOutputTokensSquared, rm.AvgOutputTokensSquaredKnown = v, true
		}
	}
	oneUnread := func(rm *domain.ReplicaMetrics) {
		if rm.PodName != "pod-a" {
			spreadOf(4)(rm)
		}
	}
	unknown := func(rm *domain.ReplicaMetrics) {
		spreadOf(4)(rm)
		rm.AvgOutputTokensSquaredKnown = false
	}
	for name, spread := range map[string]func(*domain.ReplicaMetrics){
		"absent": noSpread, "unknown": unknown, "zero": bound(0), "NaN": bound(math.NaN()),
		"+Inf": bound(math.Inf(1)), "one row unread": oneUnread,
	} {
		require.InDelta(t, 1152, dispersionKVreq(t, true, spread), 1e-6, name)
	}
}

// The spread is read with the shape's weights: 9 requests a second of 100
// tokens beside 1 of 1000 mean 190, E[O^2] 109000, D 3.02, as saturation's
// fleetOutputDispersion and the slo analyzer read them. A row the shape does
// not count (no prompt length) has no say, reading or not.
func TestKVOutputDispersion_TheShapesWeights(t *testing.T) {
	busy := domain.ReplicaMetrics{RequestRate: 9, AvgInputTokens: 500, AvgOutputTokens: 100,
		AvgOutputTokensSquared: 1e4, AvgOutputTokensSquaredKnown: true}
	long := domain.ReplicaMetrics{RequestRate: 1, AvgInputTokens: 500, AvgOutputTokens: 1000,
		AvgOutputTokensSquared: 1e6, AvgOutputTokensSquaredKnown: true}
	noPrompt := domain.ReplicaMetrics{RequestRate: 50, AvgOutputTokens: 5}
	rows := []domain.ReplicaMetrics{busy, long, noPrompt}
	_, ol, _ := averageShapeMetrics(rows)
	require.InDelta(t, 190, ol, 1e-9)
	require.InDelta(t, 109000.0/(190*190), variantOutputDispersion(rows), 1e-9)
}
