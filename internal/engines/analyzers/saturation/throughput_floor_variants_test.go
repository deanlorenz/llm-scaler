package saturation

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/floor"
)

// Issue #495: the throughput floor raised a role and not its variants, so a
// variant kept its occupancy figure and the warm pool's pressure
// (steadystate.variantPressure, SpareFraction = 1 - Utilization) read it as
// sparer than the role it serves. With SwitchThroughputFloorRaisesVariants on,
// each variant of a raised role reads the role's spare, 1 - role demand / role
// supply; off, it reads its own occupancy, as before.

// floorRaisesVariantsOff turns SwitchThroughputFloorRaisesVariants off.
func floorRaisesVariantsOff(a *SaturationAnalyzer) { a.floorRaisesVariants = false }

// spareOf is what the warm pool reads for vc: steadystate.variantPressure's
// SpareFraction.
func spareOf(vc domain.VariantCapacity) float64 { return 1 - vc.Utilization }

// variantNamed is the variant of vcs named name; the test fails without one.
func variantNamed(t *testing.T, vcs []domain.VariantCapacity, name string) domain.VariantCapacity {
	t.Helper()
	for _, vc := range vcs {
		if vc.VariantName == name {
			return vc
		}
	}
	require.FailNow(t, "no variant "+name)
	return domain.VariantCapacity{}
}

// floorRaisedDecodeCycle is the P/D fleet of "the throughput floor, through
// Analyze" (throughput_floor_test.go): decode seen saturated at runMu over
// MinThroughputSamplesToOrder cycles, then six decode replicas at 3 000
// tokens each and one idle prefill at runLambda, where occupancy reads a
// fraction of one replica and the floor raises decode to lambda / mu. It
// returns the last cycle's result, with edit applied to the analyzer first.
func floorRaisedDecodeCycle(t *testing.T, edit func(*SaturationAnalyzer)) *domain.AnalyzerResult {
	t.Helper()
	a := NewSaturationAnalyzer(capacity.NewStore())
	if edit != nil {
		edit(a)
	}
	clock := time.Date(2026, 9, 17, 10, 48, 9, 0, time.UTC)
	a.now = func() time.Time { return clock }
	ctx := context.Background()

	decode := func(pod string, tokensInUse int64, queue int, rate float64) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, decodeVariant, tokensInUse, runKvCapacity, queue, 6000, 1000)
		rm.RequestRate = rate
		rm.GenerationTokenRate = rm.RequestRate * rm.AvgOutputTokens
		rm.Ready = true
		return rm
	}
	prefill := func(pod string, tokensInUse int64) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, "prefill-v", tokensInUse, 1_149_312, 0, 6000, 1)
		rm.RequestRate = runLambda
		rm.GenerationTokenRate = rm.RequestRate * rm.AvgOutputTokens
		rm.Ready = true
		return rm
	}
	states := func(decodeN, prefillN int) []domain.VariantReplicaState {
		return []domain.VariantReplicaState{
			{VariantName: decodeVariant, Role: domain.RoleDecode, AcceleratorName: "H200", CurrentReplicas: decodeN, GPUsPerReplica: 1},
			{VariantName: "prefill-v", Role: domain.RolePrefill, AcceleratorName: "H200", CurrentReplicas: prefillN, GPUsPerReplica: 1},
		}
	}
	for i := floor.MinThroughputSamplesToOrder - 1; i >= 0; i-- {
		in := makeAnalyzerInput(
			[]domain.ReplicaMetrics{decode("decode-0", 1_158_912, 10, runMu+0.01*float64(i)), prefill("prefill-0", 66_183)},
			states(1, 1))
		in.ArrivalRate = runLambda
		_, err := a.Analyze(ctx, in)
		require.NoError(t, err)
		clock = clock.Add(ThroughputSampleSpacing + time.Second)
	}

	rm := make([]domain.ReplicaMetrics, 0, 7)
	for i := range 6 {
		rm = append(rm, decode(string(rune('a'+i)), 3_000, 0, 1.0))
	}
	rm = append(rm, prefill("p", 0))
	in := makeAnalyzerInput(rm, states(6, 1))
	in.ArrivalRate = runLambda
	result, err := a.Analyze(ctx, in)
	require.NoError(t, err)
	return result
}

// One cycle where the floor raises decode: the decode variant reads the
// role's spare, which is what the warm pool's pressure reads; prefill, which
// the floor does not raise, is the same either way.
func TestFloorRaisesVariants_TheDecodeVariantReadsTheRolesSpare(t *testing.T) {
	on := floorRaisedDecodeCycle(t, nil)
	off := floorRaisedDecodeCycle(t, floorRaisesVariantsOff)

	d := variantNamed(t, on.VariantCapacities, decodeVariant)
	supply := float64(d.ReplicaCount) * d.PerReplicaCapacity
	require.Positive(t, supply)
	roleDemand := on.RoleDemand[domain.RoleDecode]
	// The premise: the floor raised decode to lambda / mu replicas' worth.
	require.InDelta(t, runLambda/runMu*d.PerReplicaCapacity, roleDemand, 1)

	assert.InDelta(t, roleDemand, d.TotalDemand, 1e-6, "the variant carries the role's floored demand")
	assert.InDelta(t, 1-roleDemand/supply, spareOf(d), 1e-12,
		"the warm pool reads the variant as spare as its role, 1 - role demand / role supply")

	// The role-level figures are the switch's to leave alone.
	assert.Equal(t, off.RoleDemand, on.RoleDemand, "the role demand does not move with the switch")
	assert.Equal(t, off.TotalDemand, on.TotalDemand, "nor does the model total")

	// A role the floor does not raise is unchanged.
	assert.Equal(t, variantNamed(t, off.VariantCapacities, "prefill-v"),
		variantNamed(t, on.VariantCapacities, "prefill-v"), "prefill is not raised, and reads the same")

	// Off, the variant keeps its occupancy figure, and reads sparer than its
	// role.
	dOff := variantNamed(t, off.VariantCapacities, decodeVariant)
	// Measured: 0.9968 off against 0.8148 on -- the 3 000 tokens six
	// replicas hold, against the 1.11 replicas lambda / mu needs.
	assert.Greater(t, spareOf(dOff), spareOf(d)+0.15, "off, the warm pool reads the variant far sparer")
	assert.InDelta(t, 6*3_000/supply, dOff.Utilization, 0.01, "off, the variant reads its occupancy")
	t.Logf("decode role demand %.0f on supply %.0f: spare %.4f on, %.4f off", roleDemand, supply, spareOf(d), spareOf(dOff))
}

// twoDecodeVariants is a P/D fleet with two decode variants of different
// supply (four replicas and two, the same per-replica capacity) idling at a
// tenth of a replica each, and one prefill variant of two replicas holding
// three replicas' worth -- above its floor, so the floor does not raise it.
// Every replica row carries a saturated throughput of runMu on enough samples
// to order.
func twoDecodeVariants() ([]domain.VariantCapacity, []capacity.ReplicaCapacity, map[string]float64) {
	const k1 = 930_508.0
	variants := []domain.VariantCapacity{
		{VariantName: "p", Role: domain.RolePrefill, PerReplicaCapacity: k1, ReplicaCount: 2,
			TotalDemand: 3 * k1, Utilization: 1.5},
		{VariantName: "d4", Role: domain.RoleDecode, PerReplicaCapacity: k1, ReplicaCount: 4,
			TotalDemand: 0.1 * k1, Utilization: 0.1 / 4},
		{VariantName: "d2", Role: domain.RoleDecode, PerReplicaCapacity: k1, ReplicaCount: 2,
			TotalDemand: 0.1 * k1, Utilization: 0.1 / 2},
	}
	replicas := []capacity.ReplicaCapacity{
		{VariantName: "p", SaturatedThroughput: runMu, SaturatedThroughputSamples: 2},
		{VariantName: "d4", SaturatedThroughput: runMu, SaturatedThroughputSamples: 2},
		{VariantName: "d2", SaturatedThroughput: runMu, SaturatedThroughputSamples: 2},
	}
	roleDemand := map[string]float64{
		domain.RolePrefill: 3 * k1,
		domain.RoleDecode:  0.2 * k1,
	}
	return variants, replicas, roleDemand
}

// floorTwoDecodeVariants runs the floor on twoDecodeVariants at 12 req/s,
// with edit applied to the analyzer first, and returns the variants and role
// demand it leaves.
func floorTwoDecodeVariants(t *testing.T, edit func(*SaturationAnalyzer)) ([]domain.VariantCapacity, map[string]float64) {
	t.Helper()
	a := &SaturationAnalyzer{floorRaisesVariants: true}
	if edit != nil {
		edit(a)
	}
	variants, replicas, roleDemand := twoDecodeVariants()
	total := roleDemand[domain.RolePrefill] + roleDemand[domain.RoleDecode]
	a.applyThroughputFloor(domain.AnalyzerInput{ModelID: "m", Namespace: "n", ArrivalRate: 12},
		&config.ScalingPolicy{}, replicas, variants, total, roleDemand, nil, 0, false, logr.Discard())
	return variants, roleDemand
}

// P/D with two decode variants of different supply: the floor's raise is
// split by supply, so both read the role's spare, and prefill, which the
// floor does not raise, keeps its own figures.
func TestFloorRaisesVariants_TwoDecodeVariantsSplitBySupply(t *testing.T) {
	variants, roleDemand := floorTwoDecodeVariants(t, nil)
	before, _, beforeRoles := twoDecodeVariants()

	want := roleDemand[domain.RoleDecode]
	require.Greater(t, want, beforeRoles[domain.RoleDecode], "the premise: the floor raised decode")
	roleSupply := 6 * before[1].PerReplicaCapacity

	d4, d2 := variantNamed(t, variants, "d4"), variantNamed(t, variants, "d2")
	assert.InDelta(t, want*4/6, d4.TotalDemand, 1e-6, "four of the role's six replicas take four sixths")
	assert.InDelta(t, want*2/6, d2.TotalDemand, 1e-6, "two of six take two sixths")
	assert.InDelta(t, want, d4.TotalDemand+d2.TotalDemand, 1e-6, "the shares are the role's demand")
	for _, vc := range []domain.VariantCapacity{d4, d2} {
		assert.InDelta(t, 1-want/roleSupply, spareOf(vc), 1e-12,
			"%s reads 1 - role demand / role supply", vc.VariantName)
		assert.InDelta(t, vc.TotalDemand/(float64(vc.ReplicaCount)*vc.PerReplicaCapacity), vc.Utilization, 1e-12,
			"%s: Utilization moves with TotalDemand", vc.VariantName)
	}

	// Prefill holds more than its floor: neither it nor its variant moves.
	assert.Equal(t, beforeRoles[domain.RolePrefill], roleDemand[domain.RolePrefill])
	assert.Equal(t, before[0], variantNamed(t, variants, "p"))
}

// The negative control: off, the floor raises the role by the same amount
// and the variants keep their occupancy figures, reading sparer than the
// role.
func TestFloorRaisesVariants_TheSwitchOffLeavesTheVariants(t *testing.T) {
	on, onRoles := floorTwoDecodeVariants(t, nil)
	off, offRoles := floorTwoDecodeVariants(t, floorRaisesVariantsOff)
	before, _, _ := twoDecodeVariants()

	assert.Equal(t, onRoles, offRoles, "the role demand is the same either way")
	assert.Equal(t, before, off, "off, no variant moves")
	for _, name := range []string{"d4", "d2"} {
		vOn, vOff := variantNamed(t, on, name), variantNamed(t, off, name)
		assert.Greater(t, spareOf(vOff), spareOf(vOn), "%s: off, the warm pool reads it sparer", name)
		t.Logf("%s spare: %.4f on, %.4f off", name, spareOf(vOn), spareOf(vOff))
	}
}

// A variant already above its share keeps its own figure: the raise only
// ever raises.
func TestFloorRaisesVariants_AVariantAboveItsShareKeepsIt(t *testing.T) {
	a := &SaturationAnalyzer{floorRaisesVariants: true}
	variants, replicas, roleDemand := twoDecodeVariants()
	// d2 holds 1.9 of its 2 replicas: above any share of a floor near 2.2
	// replicas' worth over six.
	variants[2].TotalDemand = 1.9 * variants[2].PerReplicaCapacity
	variants[2].Utilization = 0.95
	roleDemand[domain.RoleDecode] = variants[1].TotalDemand + variants[2].TotalDemand
	hot := variants[2]
	total := roleDemand[domain.RolePrefill] + roleDemand[domain.RoleDecode]
	a.applyThroughputFloor(domain.AnalyzerInput{ModelID: "m", Namespace: "n", ArrivalRate: 12},
		&config.ScalingPolicy{}, replicas, variants, total, roleDemand, nil, 0, false, logr.Discard())

	want := roleDemand[domain.RoleDecode]
	require.Greater(t, want, 0.1*variants[1].PerReplicaCapacity+hot.TotalDemand, "the premise: the floor raised decode")
	assert.Equal(t, hot, variants[2], "the hot variant is above its share and keeps its figures")
	assert.InDelta(t, want*4/6, variants[1].TotalDemand, 1e-6, "its sibling takes its share")
}
