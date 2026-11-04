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

// The observation writes no state any output reads: the same cycles, with
// the prefill bound on and decode saturating on some, give the same results
// with the switch on and off.
func TestPrefillObserve_MovesNoResult(t *testing.T) {
	results := func(observe bool) []domain.AnalyzerResult {
		store := capacity.NewStore()
		a := NewSaturationAnalyzer(store)
		require.NoError(t, a.SetSwitchesForReplay(map[string]bool{SwitchPrefillCapacityObserve: observe}))
		clock := propStart
		a.now = func() time.Time { return clock }
		states := []domain.VariantReplicaState{
			{VariantName: "decode", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
			{VariantName: "prefill", Role: domain.RolePrefill, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		}
		out := make([]domain.AnalyzerResult, 0, 8)
		for i := range 8 {
			dec := makeReplicaMetrics("decode-0", "decode", 12000, 16000, i%3*4, 4000, 500)
			pre := makeReplicaMetrics("prefill-0", "prefill", 2000, 16000, 6, 4000, 1)
			pre.PrefillComputedTokenRate, pre.PrefillComputedTokenRateKnown = 20000, true
			pre.RequestRate, pre.RequestRateKnown = 5, true
			in := makeAnalyzerInput([]domain.ReplicaMetrics{dec, pre}, states)
			// The bound on, so its stash for the observation is written.
			in.Config.(*config.ScalingPolicy).EnablePrefillThroughputBound = true
			res, err := a.Analyze(context.Background(), in)
			require.NoError(t, err)
			res.AnalyzedAt = time.Time{}
			out = append(out, *res)
			clock = clock.Add(time.Minute)
		}
		return out
	}
	assert.Equal(t, results(false), results(true))
}
