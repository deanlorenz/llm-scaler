package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// tpotWindow is one window of 100 observations at a mean of 10 ms, half of them
// at or below 10 ms, and the p90 in the bucket that ends at edge.
func tpotWindow(edge float64) whatif.TPOTWindow {
	return whatif.TPOTWindow{Sum: 1, Count: 100, Buckets: []whatif.HistogramBucket{
		{UpperEdge: 0.010, Count: 50}, {UpperEdge: edge, Count: 95}, {UpperEdge: 2 * edge, Count: 100}}}
}

// Fine at a 10 ms mean: the q bucket is 2 ms wide. Coarse: it is 10 ms wide.
var (
	fineWindow   = tpotWindow(0.012)
	coarseWindow = tpotWindow(0.020)
)

func windowsState(router, engine []whatif.TPOTWindow) *learnreplay.State {
	return &learnreplay.State{TPOTWindows: router, EngineTPOTWindows: engine}
}

func emptyWindow() whatif.TPOTWindow {
	return whatif.TPOTWindow{Buckets: []whatif.HistogramBucket{{UpperEdge: 0.01}}}
}

func TestEngineTPOT_TheOrder(t *testing.T) {
	cases := []struct {
		name           string
		router, engine []whatif.TPOTWindow
		wantEngine     bool
		wantReason     whatif.Reason
		wantCoarse     bool
	}{
		{name: "a fine router: the router", router: []whatif.TPOTWindow{fineWindow}, engine: []whatif.TPOTWindow{fineWindow}},
		{name: "a coarse router, a fine engine: the engine", router: []whatif.TPOTWindow{coarseWindow},
			engine: []whatif.TPOTWindow{fineWindow}, wantEngine: true},
		{name: "both coarse: the router, which is what the client sees", router: []whatif.TPOTWindow{coarseWindow},
			engine: []whatif.TPOTWindow{coarseWindow}, wantCoarse: true},
		{name: "a coarse router and no engine: the router", router: []whatif.TPOTWindow{coarseWindow}, wantCoarse: true},
		{name: "no router: the engine", engine: []whatif.TPOTWindow{fineWindow}, wantEngine: true},
		{name: "no router, a coarse engine: the engine, flagged", engine: []whatif.TPOTWindow{coarseWindow}, wantEngine: true, wantCoarse: true},
		{name: "an empty router: the engine", router: []whatif.TPOTWindow{emptyWindow()}, engine: []whatif.TPOTWindow{fineWindow}, wantEngine: true},
		{name: "neither: refused", wantReason: whatif.NoTPOTHistogram},
		{name: "an empty router and an empty engine: refused", router: []whatif.TPOTWindow{emptyWindow()},
			engine: []whatif.TPOTWindow{emptyWindow()}, wantReason: whatif.NoTPOTHistogram},
		// The router's data is wrong: that is not the same as absent.
		{name: "a malformed router stands", router: []whatif.TPOTWindow{{Count: 5}}, engine: []whatif.TPOTWindow{fineWindow},
			wantReason: whatif.MalformedHistogram},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := answerFlags{set: map[string]bool{}}
			r, flags, refused := f.tpotRatioFor(windowsState(c.router, c.engine), 0.9)
			if c.wantReason != "" {
				require.NotNil(t, refused)
				assert.Equal(t, c.wantReason, refused.Reason)
				assert.False(t, f.engineTPOT)
				return
			}
			require.Nil(t, refused)
			assert.Positive(t, r.Value)
			assert.Equal(t, c.wantEngine, f.engineTPOT)
			assert.Equal(t, c.wantEngine, slices.Contains(flags, engineTPOTHistogram))
			assert.Equal(t, c.wantCoarse, slices.Contains(flags, whatif.CoarseTPOTBucket))
		})
	}
}

// --assume-tpot-ratio overrides both, as every --assume-* flag does.
func TestEngineTPOT_AnAssumedRatioWins(t *testing.T) {
	f := answerFlags{set: map[string]bool{"assume-tpot-ratio": true}, tpotRatio: 1.3}
	r, _, refused := f.tpotRatioFor(windowsState(nil, []whatif.TPOTWindow{fineWindow}), 0.9)
	require.Nil(t, refused)
	assert.True(t, r.Assumed)
	assert.False(t, f.engineTPOT)
}

// Through the command, on a recording with no router latency: the percentile
// refused NoTPOTHistogram before, and now answers, says from what, and is
// flagged in text and JSON.
func TestEngineTPOT_ThroughTheCommand(t *testing.T) {
	q := []string{"replicas-to-slo", "--recording", goldenDir + "throughput-sweep-restart/recording.jsonl.gz", "--tpot-percentile", "90"}
	text, _ := runCmd(t, q...)
	assert.NotContains(t, text, "NoTPOTHistogram", text)
	assert.Regexp(t, `R_q [0-9.]+ from 314 engine TPOT windows, not the router's`, text)
	assert.Contains(t, text, string(engineTPOTHistogram))

	out, _ := runCmd(t, append(q, "-o", "json")...)
	w := decodeOutput(t, out)
	require.NotEmpty(t, w.Roles)
	tp := w.Roles[0].Inputs.TPOT
	require.NotNil(t, tp, out)
	assert.Equal(t, "engine histogram", tp.RatioSource)
	assert.Equal(t, 314, tp.Windows)
	a := w.Roles[0].Results.PercentileTPOT.Answer
	require.NotNil(t, a, out)
	assert.Contains(t, a.Flags, string(engineTPOTHistogram))
}
