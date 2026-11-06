package slo

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// dispersionRate warms an analyzer on one aggregated variant, 1000-token
// outputs and no prompt, each row read through spread, and returns the rate a
// replica is priced at, in tokens per second, at the 44 ms target: fill 0.6 on
// the test line.
func dispersionRate(t *testing.T, on bool, spread func(*domain.ReplicaMetrics)) float64 {
	t.Helper()
	a := New(nil, nil)
	require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchKVPerRequestOutputDispersion: on}))
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	agg := domain.VariantReplicaState{VariantName: "agg", Role: domain.RoleBoth,
		AcceleratorName: "H100", GPUsPerReplica: 1, CurrentReplicas: 2}
	var res *domain.AnalyzerResult
	for i := range 5 {
		rows := cycleRows("agg", i)
		for j := range rows {
			spread(&rows[j])
		}
		var err error
		res, err = a.Analyze(context.Background(), domain.AnalyzerInput{ModelID: "m", Namespace: "ns",
			Config: policy(44*time.Millisecond, 0), ArrivalRate: 2,
			VariantStates: []domain.VariantReplicaState{agg}, ReplicaMetrics: rows})
		require.NoError(t, err)
		clock = clock.Add(15 * time.Second)
	}
	require.Len(t, res.VariantCapacities, 1)
	return res.VariantCapacities[0].PerReplicaCapacity
}

// wide is a row whose outputs have D = 2 about their mean of 1000.
func wide(rm *domain.ReplicaMetrics) {
	rm.AvgOutputTokensSquared, rm.AvgOutputTokensSquaredKnown = 2e6, true
}

// wider is D = 4: half of it, the mean of a row beside one that read nothing,
// is still a spread, so a mix counted as a reading shows.
func wider(rm *domain.ReplicaMetrics) {
	rm.AvgOutputTokensSquared, rm.AvgOutputTokensSquaredKnown = 4e6, true
}

// Fill 0.6 of 100000 tokens holds 120 requests of KVreq 500, and 60 at D = 2,
// so the replica's rate halves on and stands as it was off.
func TestKVOutputDispersion_TheSwitch(t *testing.T) {
	assert.InDelta(t, 60/0.044, dispersionRate(t, true, wide), 1e-6)
	assert.InDelta(t, 120/0.044, dispersionRate(t, false, wide), 1e-6)
	assert.True(t, New(nil, nil).kvOutputDispersion, "on from the constructor")
}

// With a row that read no bound there is no reading of the fleet: the mix of
// a read row and an unread one is neither fleet's spread. Nor is a bound that
// is no number.
func TestKVOutputDispersion_NoReadingIsNoSpread(t *testing.T) {
	unread := func(rm *domain.ReplicaMetrics) {
		if rm.PodName != "agg-a" {
			wider(rm)
		}
	}
	assert.InDelta(t, 120/0.044, dispersionRate(t, true, unread), 1e-6)
	notANumber := func(rm *domain.ReplicaMetrics) {
		rm.AvgOutputTokensSquared, rm.AvgOutputTokensSquaredKnown = math.Inf(1), true
	}
	assert.InDelta(t, 120/0.044, dispersionRate(t, true, notANumber), 1e-6)
	knownButZero := func(rm *domain.ReplicaMetrics) {
		wider(rm)
		if rm.PodName == "agg-a" {
			rm.AvgOutputTokensSquared = 0
		}
	}
	assert.InDelta(t, 120/0.044, dispersionRate(t, true, knownButZero), 1e-6)
}

// The spread is read with the shape's weights: 9 requests a second of 100
// tokens beside 1 of 1000 mean 190, E[O^2] 109000, D 3.02. A row the shape
// does not count (a warm pool's, a prefill role's) has no say, reading or not.
func TestKVOutputDispersion_TheShapesWeights(t *testing.T) {
	states := []domain.VariantReplicaState{{VariantName: "dec", Role: domain.RoleDecode}, {VariantName: "pre", Role: domain.RolePrefill}}
	busy := domain.ReplicaMetrics{VariantName: "dec", Ready: true, RequestRate: 9, AvgOutputTokens: 100,
		AvgOutputTokensSquared: 1e4, AvgOutputTokensSquaredKnown: true}
	long := domain.ReplicaMetrics{VariantName: "dec", Ready: true, RequestRate: 1, AvgOutputTokens: 1000,
		AvgOutputTokensSquared: 1e6, AvgOutputTokensSquaredKnown: true}
	pool := domain.ReplicaMetrics{VariantName: "dec", Ready: true, FromWarmPool: true, RequestRate: 1, AvgOutputTokens: 5}
	prefillRow := domain.ReplicaMetrics{VariantName: "pre", Ready: true, RequestRate: 10, AvgOutputTokens: 1}
	f := readFleet(domain.AnalyzerInput{VariantStates: states, ReplicaMetrics: []domain.ReplicaMetrics{busy, long, pool, prefillRow}})
	assert.InDelta(t, 190, f.shape.AvgOutputTokens, 1e-9)
	assert.InDelta(t, 109000.0/(190*190), f.outputDispersion, 1e-9)
	assert.InDelta(t, 3.0194, f.outputDispersion, 1e-4)
}
