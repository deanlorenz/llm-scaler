package saturation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// The prefill observation through Analyze (prefill_observe.go): the switch
// on logs one prefill-capacity-observed line per prefill variant per cycle,
// and off logs none.

// prefillObserveRun runs cycles of a P/D model, one decode and one prefill
// replica of engine, the prefill pod queued at a steady computed-token rate,
// and returns the analyzer's lines at perReplicaLogLevel.
func prefillObserveRun(t *testing.T, engine inferenceengine.Engine, observe bool, cycles int) []string {
	t.Helper()
	store := capacity.NewStore()
	store.Update("test-ns", "test-model", "prefill", capacity.Record{EngineParams: &capacity.EngineParams{Engine: engine}}, propStart)
	a := NewSaturationAnalyzer(store)
	require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchPrefillCapacityObserve: observe}))
	clock := propStart
	a.now = func() time.Time { return clock }
	var lines []string
	ctx := ctrl.LoggerInto(context.Background(),
		funcr.New(func(prefix, args string) { lines = append(lines, prefix+" "+args) }, funcr.Options{Verbosity: int(perReplicaLogLevel)}))
	states := []domain.VariantReplicaState{
		{VariantName: "decode", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: "prefill", Role: domain.RolePrefill, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	for range cycles {
		dec := makeReplicaMetrics("decode-0", "decode", 5000, 16000, 0, 4000, 500)
		pre := makeReplicaMetrics("prefill-0", "prefill", 0, 16000, 3, 4000, 1)
		pre.Ready, pre.ReadySince = true, propStart.Add(-time.Hour)
		pre.PrefillComputedTokenRate, pre.PrefillComputedTokenRateKnown = 20000, true
		pre.RequestRate, pre.RequestRateKnown = 5, true
		dec.Ready, dec.ReadySince = true, propStart.Add(-time.Hour)
		_, err := a.Analyze(ctx, makeAnalyzerInput([]domain.ReplicaMetrics{dec, pre}, states))
		require.NoError(t, err)
		clock = clock.Add(time.Minute)
	}
	return lines
}

// observedLines is the prefill-capacity-observed lines of lines.
func observedLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, `"prefill-capacity-observed"`) {
			out = append(out, l)
		}
	}
	return out
}

// The switch on logs one line per prefill variant per cycle, naming the
// prefill variant only; off, none. On, three queued minutes are a peak under
// a queue.
func TestPrefillObserve_TheSwitchLogsTheObservation(t *testing.T) {
	on := observedLines(prefillObserveRun(t, inferenceengine.EngineVLLM, true, 5))
	require.Len(t, on, 5, "one line per cycle for the one prefill variant")
	for _, l := range on {
		assert.Contains(t, l, `"variant"="prefill"`)
	}
	last := on[len(on)-1]
	assert.Contains(t, last, `"vpSource"="peak-under-queue"`)
	assert.Contains(t, last, `"vp"=20000`)
	assert.Contains(t, last, `"fRefused"="OnePod"`)

	off := observedLines(prefillObserveRun(t, inferenceengine.EngineVLLM, false, 5))
	assert.Empty(t, off, "the switch off observes nothing")
}

// An SGLang prefill variant is refused V_P through Analyze: the engine is
// read from the variant's scale target.
func TestPrefillObserve_AnSGLangVariantIsRefusedVP(t *testing.T) {
	on := observedLines(prefillObserveRun(t, inferenceengine.EngineSGLang, true, 5))
	require.Len(t, on, 5)
	last := on[len(on)-1]
	assert.Contains(t, last, `"vpRefused"="SGLangNoComputedTokens"`)
	assert.Contains(t, last, `"engine"="sglang"`)
	assert.Contains(t, last, `"vp"=0`)
}

// The observation writes no state any output reads: the same cycles give the
// same results with the switch on and off. The scenario makes prefill's
// demand depend on both inputs the observation shares with pricing, so an
// observation that touched either would move a result: the back-pressure
// bound (on, learned while decode saturates, ordering prefill once decode
// recovers) and decode's saturation (holding prefill on the cycle decode is
// still saturated). Two mutants of observePrefill fail here: one clearing
// c.decodeSaturated, one deleting the bound's windows.
func TestPrefillObserve_MovesNoResult(t *testing.T) {
	const (
		prefillVariant = "prefill-v"
		prefillKv      = int64(1_149_312)
		prompt         = 6000.0
	)
	decode := func(pod string, tokensInUse int64, queue int) domain.ReplicaMetrics {
		rm := makeReplicaMetrics(pod, decodeVariant, tokensInUse, runKvCapacity, queue, prompt, 1000)
		rm.RequestRate = 4.73
		rm.GenerationTokenRate = rm.RequestRate * rm.AvgOutputTokens
		return rm
	}
	decodeFull := func() []domain.ReplicaMetrics {
		return []domain.ReplicaMetrics{decode("decode-0", 970_475, 36), decode("decode-1", 1_039_474, 81)}
	}
	decodeCoping := func() []domain.ReplicaMetrics {
		return []domain.ReplicaMetrics{decode("decode-0", 300_000, 0), decode("decode-1", 280_000, 0)}
	}
	prefillRow := func(queue int, tokens int64, computed float64) domain.ReplicaMetrics {
		rm := makeReplicaMetrics("prefill-0", prefillVariant, tokens, prefillKv, queue, prompt, 1)
		rm.RequestRate, rm.RequestRateKnown = computed/prompt, true
		rm.PrefillComputedTokenRate, rm.PrefillComputedTokenRateKnown = computed, true
		return rm
	}
	states := []domain.VariantReplicaState{
		{VariantName: decodeVariant, Role: domain.RoleDecode, AcceleratorName: "H200", CurrentReplicas: 2, GPUsPerReplica: 1},
		{VariantName: prefillVariant, Role: domain.RolePrefill, AcceleratorName: "H200", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	results := func(observe, bound bool) []domain.AnalyzerResult {
		a := NewSaturationAnalyzer(capacity.NewStore())
		require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchPrefillCapacityObserve: observe}))
		clock := propStart
		a.now = func() time.Time { return clock }
		var out []domain.AnalyzerResult
		run := func(rows []domain.ReplicaMetrics, lambda float64) {
			in := makeAnalyzerInput(rows, states)
			in.ArrivalRate = lambda
			in.Config.(*config.ScalingPolicy).EnablePrefillThroughputBound = bound
			res, err := a.Analyze(context.Background(), in)
			require.NoError(t, err)
			res.AnalyzedAt = time.Time{}
			out = append(out, *res)
		}
		// Decode saturated, prefill queued behind it: two spaced readings
		// of the bound, each a little lower than the last.
		for i := range 2 {
			clock = clock.Add(ThroughputSampleSpacing)
			run(append(decodeFull(), prefillRow(30, 357_800, 4*prompt*(1-0.01*float64(i)))), runLambda)
		}
		// The load doubles while decode is still saturated, prefill's queue
		// empty: the prefill hold stands on decodeSaturated.
		clock = clock.Add(15 * time.Second)
		run(append(decodeFull(), prefillRow(0, 20_000, 2*runLambda*prompt)), 2*runLambda)
		// Decode recovers: the bound orders prefill before its queue forms.
		clock = clock.Add(DecodeSaturationMemory + time.Second)
		for range 3 {
			clock = clock.Add(15 * time.Second)
			run(append(decodeCoping(), prefillRow(0, 20_000, 2*runLambda*prompt)), 2*runLambda)
		}
		return out
	}
	off := results(false, true)
	// The premises: the bound moves prefill's demand, and the held cycle's is
	// below the first recovered cycle's at the same load.
	require.NotEqual(t, results(false, false), off, "the bound must move a result")
	require.Less(t, off[2].RoleDemand[domain.RolePrefill], off[3].RoleDemand[domain.RolePrefill],
		"decode's saturation must hold prefill")
	assert.Equal(t, off, results(true, true))
}

// observeHarness drives the P/D model of prefillObserveRun one cycle at a
// time, the prefill pod queued, with the engine arguments in the store. The
// settle tracker admits the pod from its third cycle (prefill.SteadyFor).
type observeHarness struct {
	t     *testing.T
	a     *SaturationAnalyzer
	store *capacity.Store
	clock time.Time
	lines []string
	ctx   context.Context
}

// newObserveHarness returns a harness with the observation on and params
// recorded for the prefill variant.
func newObserveHarness(t *testing.T, params *capacity.EngineParams) *observeHarness {
	t.Helper()
	h := &observeHarness{t: t, store: capacity.NewStore(), clock: propStart}
	h.setParams(params)
	h.a = NewSaturationAnalyzer(h.store)
	require.NoError(t, h.a.SetSwitchesForReplay(map[string]bool{SwitchPrefillCapacityObserve: true}))
	h.a.now = func() time.Time { return h.clock }
	h.ctx = ctrl.LoggerInto(context.Background(),
		funcr.New(func(prefix, args string) { h.lines = append(h.lines, prefix+" "+args) },
			funcr.Options{Verbosity: int(perReplicaLogLevel)}))
	return h
}

// setParams records the prefill variant's engine arguments, as the engine
// does from its scale target each cycle.
func (h *observeHarness) setParams(params *capacity.EngineParams) {
	h.store.Update("test-ns", "test-model", "prefill", capacity.Record{EngineParams: params}, h.clock)
}

// cycle runs one cycle, the prefill pod computing rate tokens/s with a
// queue, a minute after the last, and returns its observed line.
func (h *observeHarness) cycle(rate float64) string {
	h.t.Helper()
	states := []domain.VariantReplicaState{
		{VariantName: "decode", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: "prefill", Role: domain.RolePrefill, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	dec := makeReplicaMetrics("decode-0", "decode", 5000, 16000, 0, 4000, 500)
	pre := makeReplicaMetrics("prefill-0", "prefill", 0, 16000, 3, 4000, 1)
	pre.PrefillComputedTokenRate, pre.PrefillComputedTokenRateKnown = rate, true
	pre.RequestRate, pre.RequestRateKnown = rate/4000, true
	h.lines = nil
	_, err := h.a.Analyze(h.ctx, makeAnalyzerInput([]domain.ReplicaMetrics{dec, pre}, states))
	require.NoError(h.t, err)
	h.clock = h.clock.Add(time.Minute)
	obs := observedLines(h.lines)
	require.Len(h.t, obs, 1)
	return obs[0]
}

// A change of engine arguments ends the old engine's peak: a redeploy with a
// lower --max-num-batched-tokens starts its own V_P instead of reading the
// old one's for prefill.Retention.
func TestPrefillObserve_AnArgsChangeEndsTheOldPeak(t *testing.T) {
	h := newObserveHarness(t, &capacity.EngineParams{Engine: inferenceengine.EngineVLLM, MaxNumBatchedTokens: 8192, EffectiveMaxBatchedTokens: 8192})
	var last string
	for range 5 {
		last = h.cycle(20000)
	}
	require.Contains(t, last, `"vp"=20000`)

	h.setParams(&capacity.EngineParams{Engine: inferenceengine.EngineVLLM, MaxNumBatchedTokens: 2048, EffectiveMaxBatchedTokens: 2048})
	first := h.cycle(10000)
	assert.Contains(t, first, `"vpRefused"="NoPrefillCapacity"`, "the new arguments have no peak yet")
	for range 2 {
		last = h.cycle(10000)
	}
	assert.Contains(t, last, `"vp"=10000`, "the new arguments' own peak, not the old one")
}

// llm-d.ai/forget-learned-state ends the observation's state, by variant
// (ForgetVariant) and in full (ForgetLearnedState).
func TestPrefillObserve_AForgetEndsTheState(t *testing.T) {
	params := &capacity.EngineParams{Engine: inferenceengine.EngineVLLM, MaxNumBatchedTokens: 8192, EffectiveMaxBatchedTokens: 8192}
	for _, tc := range []struct {
		name   string
		forget func(h *observeHarness)
	}{
		{"variant", func(h *observeHarness) {
			h.a.ForgetVariant("test-ns", "test-model", "prefill", "t1")
			// The variant's record went with it; the engine reloads it from
			// the scale target on the same cycle, under the same arguments,
			// so the key is the one forgotten.
			h.setParams(params)
		}},
		{"all", func(h *observeHarness) { h.a.ForgetLearnedState() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newObserveHarness(t, params)
			var last string
			for range 5 {
				last = h.cycle(20000)
			}
			require.Contains(t, last, `"vp"=20000`)
			tc.forget(h)
			assert.Contains(t, h.cycle(20000), `"vpRefused"="NoPrefillCapacity"`, "the peak is forgotten")
		})
	}
}
