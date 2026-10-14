package saturation

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The service-rate signal is the analyzer's account of what it priced a
// variant on. These drive the real cycle and read the signal off the result,
// because the signal is only worth publishing if it is the figure the cycle
// actually used -- a helper tested in isolation would pass with nothing
// calling it.

const (
	srNS    = "test-ns" // makeAnalyzerInput's: the store is keyed on it
	srModel = "test-model"
	srAccel = "H100"
)

func srParams() *capacity.EngineParams {
	return &capacity.EngineParams{
		Engine:                    inferenceengine.EngineVLLM,
		GpuMemoryUtilization:      0.9,
		BlockSize:                 16,
		KvCacheDtype:              "auto",
		WeightDtype:               "auto",
		TensorParallelSize:        1,
		MaxNumSeqs:                256,
		EffectiveMaxBatchedTokens: 8192,
	}
}

func srAnalyzer(variants ...string) *SaturationAnalyzer {
	store := capacity.NewStore()
	for _, v := range variants {
		store.Update(srNS, srModel, v, capacity.Record{
			AcceleratorName: srAccel, GpuCount: 1, EngineParams: srParams(),
		})
	}
	return NewSaturationAnalyzer(store)
}

// srReadings gives n decode readings of one variant on tracedModel, spread
// 0.04 apart in k from 0.20. Ten span 0.36 of k, which clears both of the
// window's gates (DefaultMinSamples 10, DefaultMinKSpread 0.30).
func srReadings(variant string, n int) []domain.ReplicaMetrics {
	out := make([]domain.ReplicaMetrics, 0, n)
	for i := 0; i < n; i++ {
		k := 0.20 + 0.04*float64(i)
		rm := makeReplicaMetrics(fmt.Sprintf("%s-%d", variant, i), variant, 5000, 16000, 0, 6000, 1000)
		rm.Ready = true
		rm.KvUsageInstant = k
		rm.AvgITL = tracedModel.ITLAt(k)
		out = append(out, rm)
	}
	return out
}

func srSignal(t *testing.T, res *domain.AnalyzerResult, variant string) domain.ServiceRateSignal {
	t.Helper()
	require.NotNil(t, res)
	for _, vc := range res.VariantCapacities {
		if vc.VariantName == variant {
			require.NotNil(t, vc.ServiceRate,
				"variant %q carries no service-rate signal: nothing attached it", variant)
			return *vc.ServiceRate
		}
	}
	t.Fatalf("no variant capacity for %q", variant)
	return domain.ServiceRateSignal{}
}

// A variant that fits its own line is published on that line, with the count
// of the readings it was fitted from, and a rate derived from it.
func TestServiceRateSignal_OwnLine(t *testing.T) {
	const v = "decode-own"
	a := srAnalyzer(v)
	states := []domain.VariantReplicaState{
		{VariantName: v, Role: domain.RoleDecode, AcceleratorName: srAccel, CurrentReplicas: 10, GPUsPerReplica: 1},
	}
	ctx, _ := observedCtx(t)
	in := makeAnalyzerInput(srReadings(v, 10), states)
	res, err := a.Analyze(ctx, in)
	require.NoError(t, err)

	// The composed figure, not k_sat: 0.8 x 0.85 with this input's policy.
	require.InDelta(t, 0.68, res.PricingK, 1e-9)

	sig := srSignal(t, res, v)
	require.True(t, sig.HasLine, "ten readings over 0.36 of k fit a line")
	assert.InDelta(t, tracedModel.A, sig.LineSlope, 1e-6)
	assert.InDelta(t, tracedModel.B, sig.LineIntercept, 1e-6)
	assert.InDelta(t, tracedModel.ITLAt(res.PricingK), sig.ITLAtPricingK, 1e-6,
		"the line must be evaluated at the pricing k the result reports")
	assert.True(t, sig.ITLWindowed)
	assert.Equal(t, 10, sig.LineSamples)
	assert.Equal(t, domain.RoleDecode, sig.Role)
	assert.Equal(t, domain.ServiceRateSourceDerived, sig.RateSource,
		"a variant with its own line is priced from it")
	assert.Greater(t, sig.Rate, 0.0)
}

// A variant with nothing of its own is published on the line it BORROWED --
// that is the line it was priced on -- while its sample count stays its own
// window's, which is empty. Counting the lender's readings would say the
// variant is close to a fit it has not begun.
func TestServiceRateSignal_BorrowedLine(t *testing.T) {
	const (
		lender  = "variant-lender"
		starved = "variant-starved"
	)
	a := srAnalyzer(lender, starved)
	states := []domain.VariantReplicaState{
		{VariantName: lender, Role: domain.RoleDecode, AcceleratorName: srAccel, CurrentReplicas: 10, GPUsPerReplica: 1},
		{VariantName: starved, Role: domain.RoleDecode, AcceleratorName: srAccel, CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	starvedRow := makeReplicaMetrics("starved-0", starved, 5000, 16000, 0, 6000, 1000)
	starvedRow.Ready = true
	starvedRow.KvUsageInstant = 0.30 // no AvgITL: nothing of its own to fit
	rows := append(srReadings(lender, 10), starvedRow)

	ctx, logs := observedCtx(t)
	// The lender sorts first, so the borrow happens in this same cycle (see
	// fitLines on what the sort order decides).
	res, err := a.Analyze(ctx, makeReusingAnalyzerInput(rows, states))
	require.NoError(t, err)
	borrows := logs.FilterMessage("itl-line-borrowed").All()
	require.Len(t, borrows, 1, "the fixture must borrow, or this proves nothing")
	require.Equal(t, starved, borrows[0].ContextMap()["variant"])

	own := srSignal(t, res, lender)
	sig := srSignal(t, res, starved)
	require.True(t, sig.HasLine, "a borrowed line is the line the variant was priced on")
	assert.Equal(t, own.LineSlope, sig.LineSlope)
	assert.Equal(t, own.LineIntercept, sig.LineIntercept)
	assert.True(t, sig.ITLWindowed)
	assert.Equal(t, 0, sig.LineSamples, "the variant's OWN window, not the lender's")
	assert.Equal(t, 10, own.LineSamples)
}

// A decode variant whose window holds readings but not enough to fit has no
// line, and still publishes how many it holds: that count is the whole
// explanation of the absence.
func TestServiceRateSignal_NoLineButSamples(t *testing.T) {
	const v = "decode-cold"
	a := srAnalyzer(v)
	states := []domain.VariantReplicaState{
		{VariantName: v, Role: domain.RoleDecode, AcceleratorName: srAccel, CurrentReplicas: 4, GPUsPerReplica: 1},
	}
	require.Less(t, 4, itl.DefaultMinSamples, "the fixture must stay below the fit's sample floor")
	ctx, _ := observedCtx(t)
	// Reuse off (the default), so there is no line to borrow either.
	res, err := a.Analyze(ctx, makeAnalyzerInput(srReadings(v, 4), states))
	require.NoError(t, err)

	sig := srSignal(t, res, v)
	assert.False(t, sig.HasLine)
	assert.Zero(t, sig.LineSlope)
	assert.Zero(t, sig.ITLAtPricingK)
	assert.True(t, sig.ITLWindowed, "the sample count is published without a line")
	assert.Equal(t, 4, sig.LineSamples)
	assert.NotEqual(t, domain.ServiceRateSourceDerived, sig.RateSource,
		"no line, so nothing can have been derived from one")
}

// A decode variant with an empty window still publishes zero samples, and a
// prefill variant keeps no window at all and publishes no line series.
func TestServiceRateSignal_EmptyWindowAndPrefill(t *testing.T) {
	const (
		dec = "decode-empty"
		pre = "prefill-a"
	)
	a := srAnalyzer(dec, pre)
	states := []domain.VariantReplicaState{
		{VariantName: dec, Role: domain.RoleDecode, AcceleratorName: srAccel, CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: pre, Role: domain.RolePrefill, AcceleratorName: srAccel, CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	decRow := makeReplicaMetrics("dec-0", dec, 5000, 16000, 0, 6000, 1000)
	decRow.Ready = true
	preRow := makeReplicaMetrics("pre-0", pre, 5000, 16000, 0, 6000, 1000)
	preRow.Ready = true
	ctx, _ := observedCtx(t)
	res, err := a.Analyze(ctx, makeAnalyzerInput([]domain.ReplicaMetrics{decRow, preRow}, states))
	require.NoError(t, err)

	d := srSignal(t, res, dec)
	assert.True(t, d.ITLWindowed, "an empty window is the commonest reason for no line")
	assert.Equal(t, 0, d.LineSamples)
	assert.False(t, d.HasLine)

	p := srSignal(t, res, pre)
	assert.False(t, p.ITLWindowed, "prefill keeps no ITL window")
	assert.False(t, p.HasLine)
	assert.Equal(t, domain.RolePrefill, p.Role)
}

// The rate's source, per case, and what is left out of the median.
func TestVariantServiceRate(t *testing.T) {
	rc := func(variant string, rate float64, mut func(*capacity.ReplicaCapacity)) capacity.ReplicaCapacity {
		r := capacity.ReplicaCapacity{VariantName: variant, SaturatedThroughput: rate}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	derived := func(r *capacity.ReplicaCapacity) { r.SaturatedThroughputDerived = true }
	neighbour := func(r *capacity.ReplicaCapacity) { r.SaturatedThroughputBorrowed = true }
	bridge := func(r *capacity.ReplicaCapacity) { r.FromWarmPool = true }

	cases := []struct {
		name       string
		caps       []capacity.ReplicaCapacity
		wantRate   float64
		wantSource string
	}{
		{"measured", []capacity.ReplicaCapacity{rc("v", 2, nil)}, 2, domain.ServiceRateSourceMeasured},
		{"neighbour", []capacity.ReplicaCapacity{rc("v", 2, neighbour)}, 2, domain.ServiceRateSourceNeighbour},
		{"derived", []capacity.ReplicaCapacity{rc("v", 2, derived)}, 2, domain.ServiceRateSourceDerived},
		// Derived wins over the Borrowed flag: when the derived figure is in
		// use, Borrowed describes the measured window that was NOT used.
		{"derived beside a borrowed window", []capacity.ReplicaCapacity{
			rc("v", 2, func(r *capacity.ReplicaCapacity) { derived(r); neighbour(r) }),
		}, 2, domain.ServiceRateSourceDerived},
		{"a bridge is not the variant's own", []capacity.ReplicaCapacity{
			rc("v", 2, nil), rc("v", 100, bridge),
		}, 2, domain.ServiceRateSourceMeasured},
		{"only a bridge", []capacity.ReplicaCapacity{rc("v", 100, bridge)}, 0, ""},
		{"another variant's replica", []capacity.ReplicaCapacity{rc("w", 2, nil)}, 0, ""},
		{"no rate, NaN, +Inf", []capacity.ReplicaCapacity{
			rc("v", 0, nil), rc("v", math.NaN(), nil), rc("v", math.Inf(1), nil),
		}, 0, ""},
		{"odd count: the middle", []capacity.ReplicaCapacity{
			rc("v", 3, nil), rc("v", 1, neighbour), rc("v", 2, derived),
		}, 2, domain.ServiceRateSourceDerived},
		// Even count: the central pair averaged, the source the lower one's.
		{"even count", []capacity.ReplicaCapacity{
			rc("v", 4, nil), rc("v", 2, neighbour),
		}, 3, domain.ServiceRateSourceNeighbour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, source := variantServiceRate("v", tc.caps)
			assert.Equal(t, tc.wantRate, rate)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}
