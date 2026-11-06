package learnreplay

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// lineInput is one cycle of one decode pod at fill k with a prompt of il.
func lineInput(k, il float64) domain.AnalyzerInput {
	return domain.AnalyzerInput{Config: &config.ScalingPolicy{}, ReplicaMetrics: []domain.ReplicaMetrics{{
		PodName: "p", VariantName: "v", Ready: true, KvUsageMean: k, KvUsageMeanKnown: true,
		GenerationTokenRate: 100, GenerationTokenRateKnown: true, AvgInputTokens: il, AvgOutputTokens: 100,
	}}}
}

// noteCycles feeds one reading every 15 s from t0, and returns the samples.
func noteCycles(t0 time.Time, inputs ...domain.AnalyzerInput) *lineSamples {
	l := newLineSamples()
	for i, in := range inputs {
		l.note(in, i+1, t0.Add(time.Duration(i)*15*time.Second))
	}
	return l
}

// repeated is n copies of in.
func repeated(n int, in domain.AnalyzerInput) []domain.AnalyzerInput {
	out := make([]domain.AnalyzerInput, n)
	for i := range out {
		out[i] = in
	}
	return out
}

func offRamp(rows []whatif.LineRow) int {
	n := 0
	for _, r := range rows {
		if !r.Ramp {
			n++
		}
	}
	return n
}

var lineT0 = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

// A pod is off a ramp only once read for the gate's whole time, 2 minutes at
// 15 s cycles: the row at 120 s, the ninth, is the first.
func TestLineSamples_AFreshPodIsOnARamp(t *testing.T) {
	l := noteCycles(lineT0, repeated(9, lineInput(0.5, 1000))...)
	rows := l.rows("v")
	require.Len(t, rows, 9)
	assert.Equal(t, 1, offRamp(rows))
	assert.False(t, rows[0].Ramp, "the row off a ramp is listed first")
	assert.Equal(t, 9, rows[0].Cycle)
}

// A fill past the gate's drift within its time is a ramp, one within it is not.
func TestLineSamples_AMovedFillIsARamp(t *testing.T) {
	still := append(repeated(9, lineInput(0.5, 1000)), lineInput(0.59, 1000))
	assert.Equal(t, 2, offRamp(noteCycles(lineT0, still...).rows("v")))

	moved := append(repeated(9, lineInput(0.5, 1000)), lineInput(0.61, 1000))
	assert.Equal(t, 1, offRamp(noteCycles(lineT0, moved...).rows("v")))
}

// A row's lengths are means over minutes, and lag a change of shape: the KV a
// request holds moving past 20% is a ramp, whatever the fill does.
func TestLineSamples_AMovedShapeIsARamp(t *testing.T) {
	// KV a request: 1050, then 1250, 16% of it off; then 1350, 22% off.
	still := append(repeated(9, lineInput(0.5, 1000)), lineInput(0.5, 1200))
	assert.Equal(t, 2, offRamp(noteCycles(lineT0, still...).rows("v")))

	moved := append(repeated(9, lineInput(0.5, 1000)), lineInput(0.5, 1300))
	assert.Equal(t, 1, offRamp(noteCycles(lineT0, moved...).rows("v")))
}

// A row above the band is no row of the check, and below the GPS floor neither.
func TestLineSamples_RowsOutsideTheCheck(t *testing.T) {
	for _, k := range []float64{0.29, 0.81} {
		l := noteCycles(lineT0, repeated(12, lineInput(k, 1000))...)
		assert.Empty(t, l.rows("v"), "fill %g", k)
	}
	assert.Len(t, noteCycles(lineT0, repeated(12, lineInput(0.79, 1000))...).rows("v"), 12)
}

// A prefill pod makes one token a request: it says nothing of a decode line.
func TestLineSamples_APrefillRowIsNoRow(t *testing.T) {
	in := lineInput(0.5, 1000)
	in.VariantStates = []domain.VariantReplicaState{{VariantName: "v", Role: "prefill"}}
	l := newLineSamples()
	l.note(in, 1, lineT0)
	assert.Empty(t, l.rows("v"))
}

// Each kind of row keeps its latest cycles apart: a recording that ends on a
// ramp still holds the rows it last held still at.
func TestLineSamples_ARampAtTheEndLeavesTheSteadyRows(t *testing.T) {
	inputs := repeated(40, lineInput(0.5, 1000))
	for i := range 30 {
		k := 0.35
		if i%2 == 1 {
			k = 0.7
		}
		inputs = append(inputs, lineInput(k, 1000))
	}
	rows := noteCycles(lineT0, inputs...).rows("v")
	assert.Equal(t, shapeCycles, offRamp(rows))
	assert.Equal(t, shapeCycles*2, len(rows))
	assert.Equal(t, 40, rows[shapeCycles-1].Cycle, "the last row off a ramp")
}

// keepCycles cuts by cycles, not rows: a cycle's pods stay together.
func TestKeepCycles(t *testing.T) {
	rows := []whatif.LineRow{{Cycle: 1}, {Cycle: 1}, {Cycle: 2}, {Cycle: 3}, {Cycle: 3}}
	assert.Equal(t, []whatif.LineRow{{Cycle: 2}, {Cycle: 3}, {Cycle: 3}}, keepCycles(rows, 2))
	assert.Equal(t, rows, keepCycles(rows, 3))
	assert.Empty(t, keepCycles(nil, 2))
}

// The replay carries the rows to the state: an empty list here is a check that
// reads "not run" on every answer.
func TestStateAt_CarriesTheLineRows(t *testing.T) {
	st, err := StateAt(context.Background(), olsSweepRecording(t), Options{}, StateQuery{})
	require.NoError(t, err)
	var rows []whatif.LineRow
	for _, v := range st.Variants {
		rows = append(rows, v.LineRows...)
	}
	require.NotEmpty(t, rows)
	assert.Positive(t, offRamp(rows))
	for _, r := range rows {
		assert.LessOrEqual(t, r.Cycle, st.Cycle)
		assert.Positive(t, r.KVPerRequest)
	}
}
