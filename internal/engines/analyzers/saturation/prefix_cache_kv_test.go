package saturation

import (
	"fmt"
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
// holds the block too, so where the running requests are read, the KV a
// resident request holds is measured, between ILeff + D*OL/2 and
// IL + D*OL/2; where they are not, it is ILeff + D*OL/2 as before.

// hitsOff turns SwitchKVPerRequestHitsResident off.
func hitsOff(a *SaturationAnalyzer) { a.kvHitsResident = false }

// hitRow is a Ready decode replica pod of variant-d, il in / ol out at a hit
// rate of 0.5, its cache capTokens tokens, holding kv of it now across
// running requests.
func hitRow(pod string, il, ol float64, capTokens int64, kv float64, running int) domain.ReplicaMetrics {
	rm := makeReplicaMetrics(pod, "variant-d", 5000, capTokens, 0, il, ol)
	rm.PrefixCacheHitRate = 0.5
	rm.KvUsageInstant, rm.KvUsageInstantKnown = kv, true
	rm.RunningRequests, rm.RunningRequestsKnown = running, true
	return rm
}

// cachingRow is hitRow at 1000 in / 100 out and a cache of 100,000 tokens.
// The shape prices a resident request at 500 + 50 = 550, and with no hit at
// 1000 + 50 = 1050. A reading needs 2 requests (minResidentSequences).
func cachingRow(kv float64, running int) domain.ReplicaMetrics {
	return hitRow("pod-1", 1000, 100, 100_000, kv, running)
}

// hitsMu runs one cycle of rows, decode replicas of variant-d under a fitted
// line and a declared cap of 1024 a rank (the KV, not the cap, binds), and
// returns the first derived-mu line.
func hitsMu(t *testing.T, rows []domain.ReplicaMetrics, edit func(*SaturationAnalyzer)) map[string]any {
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
	in := makeAnalyzerInput(rows, []domain.VariantReplicaState{
		{VariantName: "variant-d", Role: domain.RoleDecode, AcceleratorName: "H200",
			CurrentReplicas: len(rows), GPUsPerReplica: 1},
	})
	_, err := a.Analyze(ctx, in)
	require.NoError(t, err)
	fields := requireLogged(t, logs, "derived-mu")
	require.Equal(t, true, fields["ok"], "the line prices mu: %v", fields)
	return fields
}

// hitMu is hitsMu of one row.
func hitMu(t *testing.T, rm domain.ReplicaMetrics, edit func(*SaturationAnalyzer)) map[string]any {
	t.Helper()
	return hitsMu(t, []domain.ReplicaMetrics{rm}, edit)
}

// Fifty running requests hold 50,000 tokens, 1000 each: the hits were mostly
// on blocks no other running request holds. The derived mu fits 0.68 x
// 100,000 / 1000 = 68 sequences, where the hit rate's discount fitted 123.6
// -- a mu 1.8x too high, and that many too few replicas. Off, as before.
func TestKVHitsResident_TheMeasuredKVPricesTheDerivedMu(t *testing.T) {
	on := hitMu(t, cachingRow(0.5, 50), nil)
	assert.InDelta(t, 1000, on["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1000, on["kvReqFleet"], 1e-9, "the GPS check prices the same request")
	assert.InDelta(t, 68, on["seqs"], 1e-9)
	assert.InDelta(t, 1000, on["residentKVPerSeq"], 1e-9)
	assert.EqualValues(t, 50, on["residentRunning"])
	assert.Equal(t, residentBoundNone, on["residentKVBound"])

	off := hitMu(t, cachingRow(0.5, 50), hitsOff)
	assert.InDelta(t, 550, off["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 550, off["kvReqFleet"], 1e-9)
	assert.InDelta(t, 68_000.0/550, off["seqs"], 1e-9)
	assert.Equal(t, residentBoundOff, off["residentKVBound"])
	assert.Less(t, on["rate"], off["rate"])
}

// The reading only says how much of the discount is real. Below the shape's
// KVreq (20,000 tokens over 50 requests, 400 each: requests part way through
// their prefill) it is the shape's 550, no more sharing than the hit rate
// allows. Above the shape with no hit (90,000 over 50, 1800 each) it is
// 1050. The log names the bound.
func TestKVHitsResident_TheReadingIsBoundedByTheHitRate(t *testing.T) {
	low := hitMu(t, cachingRow(0.2, 50), nil)
	assert.InDelta(t, 550, low["kvReqPerSeq"], 1e-9)
	assert.Equal(t, residentBoundLower, low["residentKVBound"])
	high := hitMu(t, cachingRow(0.9, 50), nil)
	assert.InDelta(t, 1050, high["kvReqPerSeq"], 1e-9)
	assert.Equal(t, residentBoundUpper, high["residentKVBound"])
	// Between the two it is the reading: 30,000 over 40, 750 each.
	assert.InDelta(t, 750, hitMu(t, cachingRow(0.3, 40), nil)["kvReqPerSeq"], 1e-9)

	// Off, the shape's discount, whatever was read.
	assert.InDelta(t, 550, hitMu(t, cachingRow(0.9, 50), hitsOff)["kvReqPerSeq"], 1e-9)
}

// No reading prices as before, at the shape's 550: a missing running-requests
// series, a missing KV series, and a fill below the ITL band are each no
// reading. The switch only moves the price where it has evidence.
func TestKVHitsResident_NoReadingPricesAsBefore(t *testing.T) {
	unknownRunning := cachingRow(0.5, 50)
	unknownRunning.RunningRequestsKnown = false
	unknownKV := cachingRow(0.5, 50)
	unknownKV.KvUsageInstantKnown = false
	for name, rm := range map[string]domain.ReplicaMetrics{
		"running unknown": unknownRunning, "kv unknown": unknownKV,
		"below the band": cachingRow(0.14, 20), // 700 a request, inside the bounds
	} {
		on := hitMu(t, rm, nil)
		assert.InDelta(t, 550, on["kvReqPerSeq"], 1e-9, name)
		assert.Equal(t, residentBoundNoReading, on["residentKVBound"], name)
	}
	// At the band's edge it is a reading: 15,000 over 20, 750.
	assert.InDelta(t, 750, hitMu(t, cachingRow(0.15, 20), nil)["kvReqPerSeq"], 1e-9)
}

// The reviewer's shared-prefix case. 500 prompt tokens are a system prompt
// every request shares and 500 are its own, with 100 out: the hit rate is
// 0.5 and, at the pricing point, the shape's 550 is right, because the shared
// blocks are held once. A replica running 123 requests holds 500 + 123 x 550
// = 68,150 tokens, 554.1 a request. Four replicas running 2 each hold 500 +
// 2 x 550 = 1600, 800 a request: the shared prefix spread over 2. Read at
// that load the KV-bound mu would be 31% low. Their fill, 0.016, is under
// the ITL band, so they are not read.
func TestKVHitsResident_ASharedPrefixIsReadInsideTheBandOnly(t *testing.T) {
	light := func(n int) []domain.ReplicaMetrics {
		out := make([]domain.ReplicaMetrics, 0, n)
		for i := range n {
			out = append(out, hitRow(fmt.Sprintf("light-%d", i), 1000, 100, 100_000, 0.016, 2))
		}
		return out
	}
	heavy := hitRow("heavy", 1000, 100, 100_000, 0.6815, 123)

	f := hitsMu(t, light(4), nil)
	assert.InDelta(t, 550, f["kvReqPerSeq"], 1e-9, "light load alone is no reading: the shape's 550, not 800")
	assert.Equal(t, residentBoundNoReading, f["residentKVBound"])

	f = hitsMu(t, append(light(4), heavy), nil)
	assert.InDelta(t, 68_150.0/123, f["kvReqPerSeq"], 1e-6, "the replica in the band alone: 554.1")
	assert.EqualValues(t, 123, f["residentRunning"])
}

// The fewest requests a reading is taken from follows the shape: the prompt
// carries no spread, so long prompts need few. At 1000 in / 1000 out (hit
// 0.5: 500 + 500 = 1000, and 1500 with no hit) it is 15. Fourteen requests
// holding 1200 each are no reading; fifteen are.
func TestKVHitsResident_TheFewestRequestsFollowTheShape(t *testing.T) {
	assert.Equal(t, 15, minResidentSequences(shape.New(1000, 1000, 0.5)))
	assert.Equal(t, 2, minResidentSequences(shape.New(1000, 100, 0.5)))
	assert.Equal(t, 2, minResidentSequences(shape.New(8000, 500, 0.9)))
	// A spread of output lengths widens the ages a request is caught at.
	assert.Greater(t, minResidentSequences(shape.New(1000, 1000, 0.5).WithOutputDispersion(2)), 15)
	assert.Equal(t, minResidentFloor, minResidentSequences(shape.Shape{}))

	few := hitMu(t, hitRow("pod-1", 1000, 1000, 20_000, 14*1200.0/20_000, 14), nil)
	assert.InDelta(t, 1000, few["kvReqPerSeq"], 1e-9)
	assert.Equal(t, residentBoundNoReading, few["residentKVBound"])
	enough := hitMu(t, hitRow("pod-1", 1000, 1000, 20_000, 15*1200.0/20_000, 15), nil)
	assert.InDelta(t, 1200, enough["kvReqPerSeq"], 1e-6)
}

// A fleet with no hits prices as before, whatever is read: the two bounds
// are one figure.
func TestKVHitsResident_NoHitsPricesAsBefore(t *testing.T) {
	rm := cachingRow(0.9, 50)
	rm.PrefixCacheHitRate = 0
	assert.InDelta(t, 1050, hitMu(t, rm, nil)["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1050, hitMu(t, rm, hitsOff)["kvReqPerSeq"], 1e-9)
}

// While a shape change is outstanding the reading is kept, held to the bounds
// of the shape priced: the short-window shape (2000 in, 100 out, hit 0.5)
// runs from 1050 to 2050, and 80,000 tokens over 50 requests read 1600, so
// mu is priced at 1600. Above the new shape's upper bound (95,000 over 40,
// 2375) it is 2050. Off, at the discount's 1050.
func TestKVHitsResident_AShapeChangeKeepsTheReadingInTheNewBounds(t *testing.T) {
	swap := func(kv float64, running int, edit func(*SaturationAnalyzer)) map[string]any {
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
			rm := hitRow("pod-1", 2000, out5m, 100_000, kv, running)
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
	on := swap(0.8, 50, nil)
	require.Equal(t, true, on["muShortWindow"], "the swap opened the short window")
	assert.InDelta(t, 1600, on["kvReqPerSeq"], 1e-9)
	assert.Equal(t, residentBoundNone, on["residentKVBound"])

	high := swap(0.95, 40, nil)
	require.Equal(t, true, high["muShortWindow"])
	assert.InDelta(t, 2050, high["kvReqPerSeq"], 1e-9)
	assert.Equal(t, residentBoundUpper, high["residentKVBound"])

	off := swap(0.8, 50, hitsOff)
	require.Equal(t, true, off["muShortWindow"])
	assert.InDelta(t, 1050, off["kvReqPerSeq"], 1e-9)
}

// The fleet reading is the tokens in use and the running requests, summed
// over the Ready generating replicas that read both inside the band. A
// prefill replica, a warm-pool bridge, a replica not Ready, one missing
// either reading, one whose fill is not a fraction, one whose capacity is
// unread or the overflow sentinel, one under the band, and one with no
// running request are each left out.
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
	got := fleetResidentKVPerRequest(counted, roles, 0.15)
	assert.InDelta(t, 110_000, got.tokens, 1e-6)
	assert.Equal(t, 70, got.running)

	notReady := row("variant-d", 0.9, 100_000, 10)
	notReady.Ready = false
	bridge := row("variant-d", 0.9, 100_000, 10)
	bridge.FromWarmPool = true
	noRunning := row("variant-d", 0.9, 100_000, 10)
	noRunning.RunningRequestsKnown = false
	noKV := row("variant-d", 0.9, 100_000, 10)
	noKV.KvUsageInstantKnown = false
	leftOut := []domain.ReplicaMetrics{
		row("variant-p", 0.9, 100_000, 10),
		notReady, bridge, noRunning, noKV,
		row("variant-d", math.NaN(), 100_000, 10),
		row("variant-d", 0.9, 0, 10),
		row("variant-d", 0.9, math.MaxInt64, 10),
		row("variant-d", 0.1, 100_000, 10),
		row("variant-d", 0.9, 100_000, 0),
	}
	got = fleetResidentKVPerRequest(append(leftOut, counted...), roles, 0.15)
	assert.InDelta(t, 110_000, got.tokens, 1e-6, "the rows left out move nothing")
	assert.Equal(t, 70, got.running)

	assert.Equal(t, residentReading{}, fleetResidentKVPerRequest(leftOut, roles, 0.15), "no row counted is no reading")
	assert.Zero(t, residentReading{}.perSeq())
}

// withResidentKV moves KVreq alone, keeps the spread, and leaves a shape with
// no KV per request, or no reading, as it is.
func TestKVHitsResident_ResidentKVMovesOnlyKVreq(t *testing.T) {
	s := shape.New(1000, 100, 0.5).WithOutputDispersion(2) // 600, 1100 with no hit
	reading := func(perSeq float64) residentReading { return residentReading{tokens: perSeq * 10, running: 10} }

	got, bound := withResidentKV(s, residentReading{})
	assert.InDelta(t, 600, got.KVreq, 1e-9, "no reading: the shape's 500 + 2 x 100/2")
	assert.Equal(t, residentBoundNoReading, bound)
	got, bound = withResidentKV(s, reading(700))
	assert.InDelta(t, 700, got.KVreq, 1e-9)
	assert.Equal(t, residentBoundNone, bound)
	assert.InDelta(t, 500, got.ILeff, 1e-9, "ILeff stays the discounted prompt")
	assert.InDelta(t, 2, got.OutputDispersion, 1e-12)
	got, _ = withResidentKV(s, reading(5000))
	assert.InDelta(t, 1100, got.KVreq, 1e-9, "1000 + 2 x 100/2")
	got, _ = withResidentKV(s, reading(100))
	assert.InDelta(t, 600, got.KVreq, 1e-9)

	// A literal's dispersion of 0 reads as 1.
	got, _ = withResidentKV(shape.Shape{AvgInputTokens: 1000, AvgOutputTokens: 100, KVreq: 550}, reading(5000))
	assert.InDelta(t, 1050, got.KVreq, 1e-9)
	got, bound = withResidentKV(shape.Shape{}, reading(700))
	assert.Equal(t, shape.Shape{}, got)
	assert.Equal(t, residentBoundNoReading, bound)
}

// The replay hook reaches the switch: off through SetSwitchesForReplay the
// derived mu prices the discount's 550; on again, the measured 1000.
func TestKVHitsResident_TheReplayHookSetsTheSwitch(t *testing.T) {
	set := func(on bool) func(*SaturationAnalyzer) {
		return func(a *SaturationAnalyzer) {
			require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchKVPerRequestHitsResident: on}))
		}
	}
	assert.InDelta(t, 550, hitMu(t, cachingRow(0.5, 50), set(false))["kvReqPerSeq"], 1e-9)
	assert.InDelta(t, 1000, hitMu(t, cachingRow(0.5, 50), set(true))["kvReqPerSeq"], 1e-9)
}
