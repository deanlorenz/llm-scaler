package learnreplay

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// goldenState is the state at a golden's cycle, 0 for the last.
func goldenState(t *testing.T, fixture string, cycle int) *State {
	t.Helper()
	gz, err := os.ReadFile(filepath.Join(goldenDir, fixture, RecordingName))
	require.NoError(t, err)
	raw, err := gunzip(gz)
	require.NoError(t, err)
	st, err := StateAt(context.Background(), raw, Options{}, StateQuery{Cycle: cycle})
	require.NoError(t, err)
	return st
}

// The figures P2's comparison reads from each run's analyzer log, read here
// through the replay's state: V_P from run 2's busy minutes, a from run 4,
// each run refusing the other. The frozen figures are the TTFT page's
// ("What P1 learns on main"); P1's own log line is the same estimate.
func TestStateAt_ReadsP1Through(t *testing.T) {
	st := goldenState(t, ttftSaturatedFixture, 0)
	require.Len(t, st.Prefill, 1)
	ps := st.Prefill[0]
	require.True(t, ps.EstimateRead, "no estimate stamped with the cycle")
	assert.Equal(t, prefill.SourceBusyMinutes, ps.Estimate.VPSource)
	assert.InDelta(t, 134775, ps.Estimate.VP, 1)
	assert.Zero(t, ps.Estimate.A)
	assert.NotEmpty(t, ps.Estimate.ARefused)
	assert.Equal(t, 1, ps.Pods)
	assert.False(t, st.FlowControlSeries)

	st = goldenState(t, ttftOpenShortFixture, 0)
	require.Len(t, st.Prefill, 1)
	ps = st.Prefill[0]
	require.True(t, ps.EstimateRead)
	assert.InDelta(t, 0.01158, ps.Estimate.A, 0.00001)
	assert.NotEmpty(t, ps.Estimate.VPRefused)
	assert.Equal(t, prefill.AModelQuadratic, ps.Estimate.AModel)
	assert.Positive(t, ps.PromptTokens)
	assert.Positive(t, ps.PromptCycles)
	assert.LessOrEqual(t, ps.PromptCycles, shapeCycles)

	var decode *VariantState
	for i := range st.Variants {
		if domain.CanonicalRole(st.Variants[i].Role) == domain.RoleDecode {
			decode = &st.Variants[i]
		}
	}
	require.NotNil(t, decode, "no decode variant")
	assert.Positive(t, decode.DecodeTTFT.EngineTTFT.Seconds)
	assert.GreaterOrEqual(t, decode.DecodeTTFT.EngineTTFT.PromptTokens, whatif.MinDecodeTTFTPrompt)
	assert.Positive(t, decode.DecodeTTFT.TransferSeconds)
}

// A cycle before P1 learned a figure reads the estimate as it stood then.
func TestStateAt_TheEstimateIsTheCycles(t *testing.T) {
	early := goldenState(t, ttftOpenShortFixture, 3)
	late := goldenState(t, ttftOpenShortFixture, 0)
	require.Len(t, early.Prefill, 1)
	require.True(t, early.Prefill[0].EstimateRead)
	assert.Zero(t, early.Prefill[0].Estimate.A, "a learned by the third cycle")
	assert.Positive(t, late.Prefill[0].Estimate.A)
}

func prefillRow(pod string, computed, requests float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{VariantName: "pre", PodName: pod, Ready: true,
		PrefillComputedTokenRate: computed, PrefillComputedTokenRateKnown: true,
		RequestRate: requests, RequestRateKnown: true}
}

func decodeRow(pod string, ttft, requests float64) domain.ReplicaMetrics {
	return domain.ReplicaMetrics{VariantName: "dec", PodName: pod, Ready: true, AvgTTFT: ttft, AvgTTFTKnown: true,
		RequestRate: requests, RequestRateKnown: true}
}

func pdInput(rows ...domain.ReplicaMetrics) domain.AnalyzerInput {
	return domain.AnalyzerInput{ReplicaMetrics: rows, VariantStates: []domain.VariantReplicaState{
		{VariantName: "pre", Role: domain.RolePrefill}, {VariantName: "dec", Role: domain.RoleDecode}}}
}

// The prompt is computed tokens over requests across the cycles, and a cycle
// with no traffic does not read as a prompt of nothing.
func TestTTFTSamples_Prompt(t *testing.T) {
	s := newTTFTSamples()
	s.note(pdInput(prefillRow("p0", 1000, 1)), 1)
	s.note(pdInput(prefillRow("p0", 9000, 3)), 2)
	s.note(pdInput(prefillRow("p0", 0, 0)), 3)
	tokens, from, to, cycles := promptOf(s.prompts["pre"])
	assert.InDelta(t, 2500, tokens, 1e-9)
	assert.Equal(t, []int{1, 2, 2}, []int{from, to, cycles})

	got, _, _, n := promptOf(nil)
	assert.Zero(t, got)
	assert.Zero(t, n)
}

// A row that is not serving is not read: not Ready, or a warm-pool bridge.
func TestTTFTSamples_ReadsServingRowsOnly(t *testing.T) {
	s := newTTFTSamples()
	notReady := prefillRow("p1", 100000, 1)
	notReady.Ready = false
	bridge := prefillRow("p2", 100000, 1)
	bridge.FromWarmPool = true
	s.note(pdInput(prefillRow("p0", 2000, 1), notReady, bridge), 1)
	tokens, _, _, _ := promptOf(s.prompts["pre"])
	assert.InDelta(t, 2000, tokens, 1e-9)
	require.Len(t, s.shares["pre"], 1)
	assert.Equal(t, 1, s.shares["pre"][0].pods)
}

// f_raw is the busiest pod over the mean, the largest over the window, and
// the window restarts at another pod count.
func TestTTFTSamples_RawShare(t *testing.T) {
	assert.InDelta(t, 2, rawShare([]float64{3, 1, 0.5, 1.5}), 1e-9)
	assert.Zero(t, rawShare([]float64{0, 0}))
	assert.Zero(t, rawShare(nil))

	s := newTTFTSamples()
	s.note(pdInput(prefillRow("p0", 900, 1), prefillRow("p1", 100, 1)), 1) // 1.8, on two pods
	s.note(pdInput(prefillRow("p0", 500, 1)), 2)                           // one pod
	s.note(pdInput(prefillRow("p0", 300, 1), prefillRow("p1", 200, 1)), 3) // 1.2
	s.note(pdInput(prefillRow("p0", 400, 1), prefillRow("p1", 200, 1)), 4) // 1.33
	assert.InDelta(t, 4.0/3, shareAt(s.shares["pre"], 2), 1e-9)
	assert.Zero(t, shareAt(s.shares["pre"], 3))

	for c := 5; c < 5+rawShareCycles; c++ {
		s.note(pdInput(prefillRow("p0", 300, 1), prefillRow("p1", 200, 1)), c)
	}
	assert.Len(t, s.shares["pre"], rawShareCycles)
	assert.InDelta(t, 1.2, shareAt(s.shares["pre"], 2), 1e-9)
}

// The decode engine's TTFT is a pair: the largest cycle's mean, with that
// cycle's prompt, each mean weighted by the rows' request rates.
func TestTTFTSamples_EngineTTFTIsAPair(t *testing.T) {
	s := newTTFTSamples()
	s.note(pdInput(prefillRow("p0", 8000, 1), decodeRow("d0", 0.1, 1), decodeRow("d1", 0.3, 3)), 1) // 0.25 at 8000
	s.note(pdInput(prefillRow("p0", 2000, 1), decodeRow("d0", 0.05, 1)), 2)                         // 0.05 at 2000
	require.Len(t, s.engineTTFT["dec"], 2)
	assert.InDelta(t, 0.25, s.engineTTFT["dec"][0].Seconds, 1e-9)
	assert.InDelta(t, 8000, s.engineTTFT["dec"][0].PromptTokens, 1e-9)

	// No prompt in the cycle: nothing to pair it with.
	s.note(pdInput(prefillRow("p0", 0, 0), decodeRow("d0", 9, 1)), 3)
	assert.Len(t, s.engineTTFT["dec"], 2)

	// Rows with no request rate read equally.
	u := newTTFTSamples()
	d0, d1 := decodeRow("d0", 0.1, 0), decodeRow("d1", 0.3, 0)
	u.note(pdInput(prefillRow("p0", 2000, 1), d0, d1), 1)
	assert.InDelta(t, 0.2, u.engineTTFT["dec"][0].Seconds, 1e-9)

	for c := 0; c < decodeTTFTCycles+2; c++ {
		s.note(pdInput(prefillRow("p0", 2000, 1), decodeRow("d0", 0.01, 1)), 10+c)
	}
	assert.Len(t, s.engineTTFT["dec"], decodeTTFTCycles)
}

// The largest TTFT comes with its own cycle's prompt.
func TestLargestTTFT(t *testing.T) {
	assert.Equal(t, whatif.DecodeTTFT{Seconds: 0.3, PromptTokens: 500}, largestTTFT([]whatif.DecodeTTFT{
		{Seconds: 0.1, PromptTokens: 9000}, {Seconds: 0.3, PromptTokens: 500}, {Seconds: 0.2, PromptTokens: 7000}}))
	assert.Zero(t, largestTTFT(nil))
}

// An estimate stashed at another cycle is not this cycle's.
func TestEstimateAt(t *testing.T) {
	now := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	stash := func(at time.Time, ok bool) func(string, string, string) (prefill.Estimate, time.Time, bool) {
		return func(ns, model, v string) (prefill.Estimate, time.Time, bool) {
			assert.Equal(t, []string{"ns", "m", "v"}, []string{ns, model, v})
			return prefill.Estimate{VP: 7}, at, ok
		}
	}
	est, ok := estimateAt(stash(now, true), "ns", "m", "v", now)
	assert.True(t, ok)
	assert.Equal(t, 7.0, est.VP)
	est, ok = estimateAt(stash(now.Add(-15*time.Second), true), "ns", "m", "v", now)
	assert.False(t, ok)
	assert.Zero(t, est.VP)
	_, ok = estimateAt(stash(now, false), "ns", "m", "v", now)
	assert.False(t, ok)
}

// Each of the router's flow-control series says it is on; a nil router
// latency, or one with no wait histogram, says nothing.
func TestFlowControlSeries(t *testing.T) {
	assert.False(t, flowControlSeries(domain.AnalyzerInput{}))
	assert.False(t, flowControlSeries(domain.AnalyzerInput{RouterLatency: &domain.RouterLatency{}}))
	assert.True(t, flowControlSeries(domain.AnalyzerInput{SchedulerQueue: &domain.SchedulerQueueMetrics{}}))
	assert.True(t, flowControlSeries(domain.AnalyzerInput{FlowControlPools: []domain.FlowControlPool{{}}}))
	assert.True(t, flowControlSeries(domain.AnalyzerInput{RouterLatency: &domain.RouterLatency{
		FlowControlWaitBuckets: []domain.LatencyBucket{{UpperBound: 1, Rate: 1}}}}))
}

// The transfer is the mean over the rows that read one, and an unread row is
// not a zero.
func TestTTFTSamples_Transfer(t *testing.T) {
	s := newTTFTSamples()
	d0 := decodeRow("d0", 0.1, 1)
	d0.AvgKVTransferTime, d0.AvgKVTransferTimeKnown = 0.02, true
	d1 := decodeRow("d1", 0.1, 1)
	s.note(pdInput(prefillRow("p0", 2000, 1), d0, d1), 1)
	require.Len(t, s.transfers["dec"], 1)
	assert.InDelta(t, 0.02, s.transfers["dec"][0], 1e-12)
}
