package throughput

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The line's learner-write carries its pods and its admission margins
// (analyzer-learner.md 6.1), as the saturation analyzer's ITL line does:
// the replicas whose readings the line was fitted from, lowest mean ITL
// first, at most LearnerWriteMaxPods of them, and the full fit's gate -- the
// steady readings' spread against 0.30 and their count against 10.

// learnerWriteRun runs n cycles of rows at V(3), each line also written in
// the console encoding the controller ships, and returns the learner-write
// lines and that console output.
func learnerWriteRun(t *testing.T, n int, rows []domain.ReplicaMetrics) ([]observer.LoggedEntry, string) {
	t.Helper()
	var buf bytes.Buffer
	enc := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	console := zapcore.NewCore(enc, zapcore.AddSync(&buf), zapcore.Level(int8(-logging.VERBOSE)))
	observed, logs := observer.New(zapcore.Level(int8(-logging.VERBOSE)))
	r := newLineRun(t, NewThroughputAnalyzer())
	r.ctx = logr.NewContext(context.Background(), zapr.NewLogger(zap.New(zapcore.NewTee(console, observed))))
	for range n {
		r.step(rows)
	}
	return logs.FilterMessage("learner-write").All(), buf.String()
}

// On the three-replica fleet the first line is the full fit over the four
// cycles' twelve steady readings: every pod lists its four readings at its
// own fill and ITL, and the gate is passed, by two readings and with the
// spread exactly at its threshold.
func TestLearnerWrite_CarriesPodsAndMargins(t *testing.T) {
	writes, _ := learnerWriteRun(t, 4, settleFleet())
	require.Len(t, writes, 1, "premise: the fourth cycle's twelve readings fit the first line")
	f := writes[0].ContextMap()
	require.Equal(t, itlReasonT1OLS, f["source"], "premise: twelve steady readings spread 0.30, the full fit")

	pods, ok := f["pods"].([]map[string]any)
	require.True(t, ok, "pods is a list, got %T", f["pods"])
	assert.EqualValues(t, 0, f["podsOmitted"])
	require.Len(t, pods, 3)
	for i, k := range []float64{0.30, 0.45, 0.60} {
		assert.Equal(t, "pod-"+string(rune('a'+i)), pods[i]["pod"], "lowest ITL first")
		assert.InDelta(t, lineAt(k), pods[i]["reading"], 1e-12)
		assert.InDelta(t, k, pods[i]["k"], 1e-12)
		assert.EqualValues(t, 4, pods[i]["rows"])
		assert.Equal(t, itl.WriteTimeBase, pods[i]["timeBase"])
	}
	assert.EqualValues(t, 12, f["fitRows"])
	assert.EqualValues(t, 12, f["steadyHeld"])
	assert.EqualValues(t, itl.DefaultMinSamples, f["steadyHeldThreshold"])
	assert.EqualValues(t, 2, f["steadyHeldOverThreshold"])
	assert.InDelta(t, 0.30, f["kSpread"], 1e-12)
	assert.InDelta(t, itl.DefaultMinKSpread, f["kSpreadThreshold"], 1e-12)
	assert.InDelta(t, 0, f["kSpreadOverThreshold"], 1e-12)
}

// A fleet of twenty lists sixteen pods, lowest ITL first, counts four in
// podsOmitted, and the line stays under the CRI line limit.
func TestLearnerWrite_ListsSixteenPodsLowestFirst(t *testing.T) {
	const n = 20
	early := lineStart.Add(-time.Hour)
	fleet := make([]domain.ReplicaMetrics, 0, n)
	for i := range n {
		// Highest fill first, so the order the line lists is its own.
		k := 0.75 - 0.025*float64(i)
		fleet = append(fleet, lineRow(fmt.Sprintf("decode-7f9c6d5b8-%05d", i), k, lineAt(k), early))
	}
	writes, console := learnerWriteRun(t, 1, fleet)
	require.Len(t, writes, 1, "premise: twenty readings fit a line on the first cycle")
	f := writes[0].ContextMap()
	assert.EqualValues(t, n-LearnerWriteMaxPods, f["podsOmitted"])
	pods, ok := f["pods"].([]map[string]any)
	require.True(t, ok, "pods is a list, got %T", f["pods"])
	require.Len(t, pods, LearnerWriteMaxPods)
	readings := make([]float64, 0, len(pods))
	for _, p := range pods {
		readings = append(readings, p["reading"].(float64))
	}
	assert.IsIncreasing(t, readings, "lowest ITL first")
	assert.Equal(t, fmt.Sprintf("decode-7f9c6d5b8-%05d", n-1), pods[0]["pod"], "the lowest fill reads the lowest ITL")
	assert.EqualValues(t, n, f["fitRows"])

	for _, line := range strings.Split(console, "\n") {
		assert.Less(t, len(line), criMaxLogLineBytes, "a line over the CRI limit is split by the runtime")
	}
	assert.Contains(t, console, "learner-write")
}
