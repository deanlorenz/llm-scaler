package saturation

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// Issue #277: a prefix-cache hit saves KV only when another running request
// holds the block too, so the KV a resident request holds is measured from
// the running requests, between ILeff + D*OL/2 and IL + D*OL/2.

// hitsOff turns SwitchKVPerRequestHitsResident off.
func hitsOff(a *SaturationAnalyzer) { a.kvHitsResident = false }

// cachingRow is a Ready decode replica of 1000 in / 100 out at a hit rate of
// 0.5, its cache 100,000 tokens, holding kv of it now across running
// requests. The shape prices a resident request at 500 + 50 = 550, and with
// no hit at 1000 + 50 = 1050.
func cachingRow(kv float64, running int) domain.ReplicaMetrics {
	rm := makeReplicaMetrics("pod-1", "variant-d", 5000, 100_000, 0, 1000, 100)
	rm.PrefixCacheHitRate = 0.5
	rm.KvUsageInstant, rm.KvUsageInstantKnown = kv, true
	rm.RunningRequests, rm.RunningRequestsKnown = running, true
	return rm
}

// hitsMu runs one cycle of rm, a decode replica of variant-d under a fitted
// line and a declared cap of 1024 a rank (the KV, not the cap, binds), and
// returns the derived-mu line.
func hitsMu(t *testing.T, rm domain.ReplicaMetrics, edit func(*SaturationAnalyzer)) map[string]any {
	t.Helper()
	ctx, logs := observedCtx(t)
	store := capacity.NewStore()
	store.Update("test-ns", "test-model", "variant-d", capacity.Record{
		AcceleratorName: "H200", GpuCount: 1, EngineParams: declaredParams(1024),
	}, time.Now())
	a := NewSaturationAnalyzer(store)
	a.publishLine(a.itlPhysicsKey("test-ns", "test-model", "variant-d", "H200", 1, ""),
		"a fitted line", itl.Model{A: 0.08, B: 0.01}, time.Now())
	if edit != nil {
		edit(a)
	}
	in := makeAnalyzerInput([]domain.ReplicaMetrics{rm}, []domain.VariantReplicaState{
		{VariantName: "variant-d", Role: domain.RoleDecode, AcceleratorName: "H200",
			CurrentReplicas: 1, GPUsPerReplica: 1},
	})
	_, err := a.Analyze(ctx, in)
	require.NoError(t, err)
	fields := requireLogged(t, logs, "derived-mu")
	require.Equal(t, true, fields["ok"], "the line prices mu: %v", fields)
	return fields
}

// Fifty running requests hold 50,000 tokens, 1000 each: the hits were mostly
// on blocks no other running request holds. The derived mu fits 0.68 x
// 100,000 / 1000 = 68 sequences, where the hit rate's discount fitted 123.6
// -- a mu 1.8x too high, and that many too few replicas. Off, as before.
func TestKVHitsResident_TheMeasuredKVPricesTheDerivedMu(t *testing.T) {
	on := hitsMu(t, cachingRow(0.5, 50), nil)
	assert.InDelta(t, 1000, on["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1000, on["kvReqFleet"], 1e-9, "the GPS check prices the same request")
	assert.InDelta(t, 68, on["seqs"], 1e-9)
	assert.InDelta(t, 1000, on["residentKVPerSeq"], 1e-9)
	assert.Equal(t, true, on["residentKVKnown"])

	off := hitsMu(t, cachingRow(0.5, 50), hitsOff)
	assert.InDelta(t, 550, off["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 550, off["kvReqFleet"], 1e-9)
	assert.InDelta(t, 68_000.0/550, off["seqs"], 1e-9)
	assert.Less(t, on["rate"], off["rate"])
}

// The reading only says how much of the discount is real. Below the shape's
// KVreq (20,000 tokens over 50 requests, 400 each: requests part way through
// their prefill) it is the shape's 550, no more sharing than the hit rate
// allows. Above the shape with no hit (90,000 over 50, 1800 each) it is
// 1050.
func TestKVHitsResident_TheReadingIsBoundedByTheHitRate(t *testing.T) {
	assert.InDelta(t, 550, hitsMu(t, cachingRow(0.2, 50), nil)["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1050, hitsMu(t, cachingRow(0.9, 50), nil)["kvReqPerSeq"], 1e-9)
	// Between the two it is the reading: 30,000 over 40, 750 each.
	assert.InDelta(t, 750, hitsMu(t, cachingRow(0.3, 40), nil)["kvReqPerSeq"], 1e-9)

	// Off, the shape's discount, whatever was read.
	assert.InDelta(t, 550, hitsMu(t, cachingRow(0.9, 50), hitsOff)["kvReqPerSeq"], 1e-9)
}

// No reading is no discount: the request is priced at 1050, the hit rate's
// saving taken back, so the derived mu is never above the one before. A
// missing running-requests series, a missing KV series, and fewer than
// minResidentSequences running are each no reading. Off, 550 throughout.
func TestKVHitsResident_NoReadingPricesNoHit(t *testing.T) {
	unknownRunning := cachingRow(0.5, 50)
	unknownRunning.RunningRequestsKnown = false
	unknownKV := cachingRow(0.5, 50)
	unknownKV.KvUsageInstantKnown = false
	few := cachingRow(0.05, minResidentSequences-1)
	for name, rm := range map[string]domain.ReplicaMetrics{
		"running unknown": unknownRunning, "kv unknown": unknownKV, "too few running": few,
	} {
		on := hitsMu(t, rm, nil)
		assert.InDelta(t, 1050, on["kvReqPerSeq"], 1e-9, name)
		assert.Equal(t, false, on["residentKVKnown"], name)
		assert.InDelta(t, 550, hitsMu(t, rm, hitsOff)["kvReqPerSeq"], 1e-9, name+", off")
	}
	// minResidentSequences running is a reading: 5000 tokens over 8, 625.
	assert.InDelta(t, 625, hitsMu(t, cachingRow(0.05, minResidentSequences), nil)["kvReqPerSeq"], 1e-9)
}

// A fleet with no hits prices as before, whatever is read: the two bounds
// are one figure.
func TestKVHitsResident_NoHitsPricesAsBefore(t *testing.T) {
	rm := cachingRow(0.9, 50)
	rm.PrefixCacheHitRate = 0
	assert.InDelta(t, 1050, hitsMu(t, rm, nil)["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1050, hitsMu(t, rm, hitsOff)["kvReqPerSeq"], 1e-9)
}

// While a shape change is outstanding the requests resident belong to both
// shapes, so they are no reading of the one arriving: the short-window shape
// (2000 in, 100 out, hit 0.5) is priced at no discount, 2000 + 50 = 2050,
// though 80,000 tokens over 50 requests read 1600 each. Off, at the
// discount's 1050.
func TestKVHitsResident_AShapeChangeDropsTheReading(t *testing.T) {
	swap := func(edit func(*SaturationAnalyzer)) map[string]any {
		store := capacity.NewStore()
		store.Update("test-ns", "test-model", "variant-d", capacity.Record{
			AcceleratorName: "H200", GpuCount: 1, EngineParams: declaredParams(1024),
		}, time.Now())
		a := NewSaturationAnalyzer(store)
		a.publishLine(a.itlPhysicsKey("test-ns", "test-model", "variant-d", "H200", 1, ""),
			"a fitted line", itl.Model{A: 0.08, B: 0.01}, time.Now())
		if edit != nil {
			edit(a)
		}
		states := []domain.VariantReplicaState{{VariantName: "variant-d", Role: domain.RoleDecode,
			AcceleratorName: "H200", CurrentReplicas: 1, GPUsPerReplica: 1}}
		row := func(out5m, out1m float64) []domain.ReplicaMetrics {
			rm := makeReplicaMetrics("pod-1", "variant-d", 5000, 100_000, 0, 2000, out5m)
			rm.PrefixCacheHitRate = 0.5
			rm.KvUsageInstant, rm.KvUsageInstantKnown = 0.8, true
			rm.RunningRequests, rm.RunningRequestsKnown = 50, true
			rm.AvgOutputTokensRecent, rm.AvgInputTokensRecent = out1m, 2000
			return []domain.ReplicaMetrics{rm}
		}
		ctx1, _ := observedCtx(t)
		_, err := a.Analyze(ctx1, makeAnalyzerInput(row(1000, 1000), states))
		require.NoError(t, err)
		ctx2, logs := observedCtx(t)
		_, err = a.Analyze(ctx2, makeAnalyzerInput(row(550, 100), states))
		require.NoError(t, err)
		return requireLogged(t, logs, "derived-mu")
	}
	on := swap(nil)
	require.Equal(t, true, on["muShortWindow"], "the swap opened the short window")
	assert.Equal(t, false, on["residentKVKnown"])
	assert.InDelta(t, 2050, on["kvReqPerSeq"], 1e-9)

	off := swap(hitsOff)
	require.Equal(t, true, off["muShortWindow"])
	assert.InDelta(t, 1050, off["kvReqPerSeq"], 1e-9)
}

// The fleet reading is the tokens in use over the running requests, summed
// over the Ready generating replicas that read both. A prefill replica, a
// warm-pool bridge, a replica not Ready, one missing either reading, one
// whose capacity is unread or the overflow sentinel, and one with no running
// request are each left out.
func TestKVHitsResident_TheFleetReadingCountsReadyGeneratingReplicas(t *testing.T) {
	roles := map[string]string{"variant-d": domain.RoleDecode, "variant-p": domain.RolePrefill}
	row := func(variant string, kv float64, capTokens int64, running int) domain.ReplicaMetrics {
		rm := makeReplicaMetrics("pod", variant, 0, capTokens, 0, 1000, 100)
		rm.KvUsageInstant, rm.KvUsageInstantKnown = kv, true
		rm.RunningRequests, rm.RunningRequestsKnown = running, true
		return rm
	}
	counted := []domain.ReplicaMetrics{
		row("variant-d", 0.5, 100_000, 50), // 50,000 over 50
		row("variant-d", 0.3, 200_000, 20), // 60,000 over 20
	}
	got, ok := fleetResidentKVPerRequest(counted, roles)
	require.True(t, ok)
	assert.InDelta(t, 110_000.0/70, got, 1e-9)

	notReady := row("variant-d", 0.9, 100_000, 10)
	notReady.Ready = false
	bridge := row("variant-d", 0.9, 100_000, 10)
	bridge.FromWarmPool = true
	noRunning := row("variant-d", 0.9, 100_000, 10)
	noRunning.RunningRequestsKnown = false
	noKV := row("variant-d", 0.9, 100_000, 10)
	noKV.KvUsageInstantKnown = false
	badKV := row("variant-d", math.NaN(), 100_000, 10)
	leftOut := []domain.ReplicaMetrics{
		row("variant-p", 0.9, 100_000, 10),
		notReady, bridge, noRunning, noKV, badKV,
		row("variant-d", 0.9, 0, 10),
		row("variant-d", 0.9, math.MaxInt64, 10),
		row("variant-d", 0.9, 100_000, 0),
	}
	got, ok = fleetResidentKVPerRequest(append(leftOut, counted...), roles)
	require.True(t, ok)
	assert.InDelta(t, 110_000.0/70, got, 1e-9, "the rows left out move nothing")

	_, ok = fleetResidentKVPerRequest(leftOut, roles)
	assert.False(t, ok, "no row counted is no reading")
}

// withResidentKV moves KVreq alone, keeps the spread, and leaves a shape with no
// KV per request as it is.
func TestKVHitsResident_ResidentKVMovesOnlyKVreq(t *testing.T) {
	s := shape.New(1000, 100, 0.5).WithOutputDispersion(2)
	got := withResidentKV(s, 0, false)
	assert.InDelta(t, 1100, got.KVreq, 1e-9, "1000 + 2 x 100/2")
	assert.InDelta(t, 500, got.ILeff, 1e-9, "ILeff stays the discounted prompt")
	assert.InDelta(t, 2, got.OutputDispersion, 1e-12)
	assert.InDelta(t, 700, withResidentKV(s, 700, true).KVreq, 1e-9)
	assert.InDelta(t, 600, withResidentKV(s, 100, true).KVreq, 1e-9, "500 + 2 x 100/2")

	// A literal's dispersion of 0 reads as 1.
	assert.InDelta(t, 1050, withResidentKV(shape.Shape{AvgInputTokens: 1000, AvgOutputTokens: 100, KVreq: 550}, 0, false).KVreq, 1e-9)
	assert.Equal(t, shape.Shape{}, withResidentKV(shape.Shape{}, 700, true))
}

// The replay hook reaches the switch: off through SetSwitchesForReplay the
// derived mu prices the discount's 550; on again, the measured 1000.
func TestKVHitsResident_TheReplayHookSetsTheSwitch(t *testing.T) {
	set := func(on bool) func(*SaturationAnalyzer) {
		return func(a *SaturationAnalyzer) {
			require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchKVPerRequestHitsResident: on}))
		}
	}
	assert.InDelta(t, 550, hitsMu(t, cachingRow(0.5, 50), set(false))["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1000, hitsMu(t, cachingRow(0.5, 50), set(true))["kvReqPerSeq"], 1e-9)
}
