package saturation

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// a's curved fit through Analyze (SwitchPrefillAQuadratic): a prefill pod at
// low load swept over 8k, 30k and 37k prompts, its service time convex in
// the prompt as run 3 of the TTFT recordings measured it.

// lowLoadPrefillCycle runs one cycle of the harness's P/D model with the
// prefill pod at low load on prompts of ieff tokens, and returns its
// observed line.
func (h *observeHarness) lowLoadPrefillCycle(ieff float64) string {
	h.t.Helper()
	states := []domain.VariantReplicaState{
		{VariantName: "decode", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: "prefill", Role: domain.RolePrefill, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	dec := makeReplicaMetrics("decode-0", "decode", 5000, 16000, 0, 4000, 500)
	pre := makeReplicaMetrics("prefill-0", "prefill", 0, 16000, 0, ieff, 1)
	svc := 0.0028 + 1.734e-6*ieff + 1.727e-10*ieff*ieff
	pre.RequestRate, pre.RequestRateKnown = 0.02, true
	pre.PrefillComputedTokenRate, pre.PrefillComputedTokenRateKnown = 0.02*ieff, true
	pre.AvgServiceTime, pre.AvgTTFT = svc, svc+0.0025
	h.lines = nil
	_, err := h.a.Analyze(h.ctx, makeAnalyzerInput([]domain.ReplicaMetrics{dec, pre}, states))
	require.NoError(h.t, err)
	h.clock = h.clock.Add(time.Minute)
	obs := observedLines(h.lines)
	require.Len(h.t, obs, 1)
	return obs[0]
}

// aQuadRun drives a harness through five minutes at each of run 3's prompt
// levels with SwitchPrefillAQuadratic set to on, and returns the last line.
func aQuadRun(t *testing.T, on bool) string {
	t.Helper()
	h := newObserveHarness(t, &capacity.EngineParams{Engine: inferenceengine.EngineVLLM})
	require.NoError(t, h.a.SetSwitchesForReplay(map[string]bool{SwitchPrefillAQuadratic: on}))
	var last string
	for _, l := range []float64{8000, 30000, 37000} {
		for range 5 {
			last = h.lowLoadPrefillCycle(l)
		}
	}
	return last
}

// On, a is the curve's intercept, 2.8 ms, plus the 2.5 ms overhead, and the
// line says so; off, a is the line's, which meets the axis below zero, and
// is refused as P1 shipped.
func TestPrefillAQuad_TheSwitchFitsTheCurve(t *testing.T) {
	on := aQuadRun(t, true)
	assert.Contains(t, on, `"aModel"="quadratic"`)
	assert.Contains(t, on, `"aLineReason"=""`)
	assert.Contains(t, on, `"aClusters"=3`)
	assert.Contains(t, on, `"aRefused"=""`)
	m := regexp.MustCompile(`"aIntercept"=([0-9.e+-]+)`).FindStringSubmatch(on)
	require.Len(t, m, 2, on)
	intercept, err := strconv.ParseFloat(m[1], 64)
	require.NoError(t, err)
	assert.InDelta(t, 0.0028, intercept, 1e-9)

	off := aQuadRun(t, false)
	assert.Contains(t, off, `"aModel"="linear"`)
	assert.Contains(t, off, `"aLineReason"="LineOnly"`)
	assert.Contains(t, off, `"aRefused"="NonPositiveIntercept"`)
	assert.Contains(t, off, `"a"=0 "aIntercept"=0 `)
}
