package collector

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
)

// TestCollectRouterLatency_PicksItsModelAndMergesBuckets pins the router
// latency read: series resolve to a model as the flow-control ones do
// (target_model_name, else model_name), a model reached under both labels
// sums into one bucket, and another model's series, a NaN and a negative rate
// are left out.
func TestCollectRouterLatency_PicksItsModelAndMergesBuckets(t *testing.T) {
	ts := time.Now()
	var gotParams map[string]string
	mockSource := &mockMetricsSource{
		refreshFunc: func(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
			gotParams = spec.Params
			return map[string]*source.MetricResult{
				registration.QuerySLORouterTTFTBuckets: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "1"}, Value: 0.5, Timestamp: ts},
					{Labels: map[string]string{seriesModelLabel: "model-a", "le": "1"}, Value: 0.25, Timestamp: ts},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "+Inf"}, Value: 1, Timestamp: ts},
					{Labels: map[string]string{seriesTargetModelLabel: "model-b", "le": "1"}, Value: 50, Timestamp: ts},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "3"}, Value: math.NaN(), Timestamp: ts},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "5"}, Value: -1, Timestamp: ts},
				}},
				registration.QuerySLORouterTPOTSum: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: 0.08, Timestamp: ts},
					{Labels: map[string]string{seriesTargetModelLabel: "model-b"}, Value: 9, Timestamp: ts},
				}},
				registration.QuerySLORouterTPOTCount: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: 1, Timestamp: ts},
				}},
			}, nil
		},
	}

	got := NewReplicaMetricsCollector(mockSource, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns")

	require.NotNil(t, got)
	assert.Equal(t, map[string]string{source.ParamNamespace: "ns"}, gotParams)
	assert.Equal(t, registration.SLORouterWindowSeconds, got.WindowSeconds)
	sort.Slice(got.TTFTBuckets, func(i, j int) bool { return got.TTFTBuckets[i].UpperBound < got.TTFTBuckets[j].UpperBound })
	require.Len(t, got.TTFTBuckets, 2)
	assert.Equal(t, 1.0, got.TTFTBuckets[0].UpperBound)
	assert.Equal(t, 0.75, got.TTFTBuckets[0].Rate)
	assert.True(t, math.IsInf(got.TTFTBuckets[1].UpperBound, 1))
	assert.Equal(t, 0.08, got.TPOTSumRate)
	assert.Equal(t, 1.0, got.TPOTCountRate)
}

// TestCollectRouterLatency_NilWhenRouterSilent pins that a router exporting
// nothing for the model reads as absent, not as a zero latency.
func TestCollectRouterLatency_NilWhenRouterSilent(t *testing.T) {
	mockSource := &mockMetricsSource{
		refreshFunc: func(_ context.Context, _ source.RefreshSpec) (map[string]*source.MetricResult, error) {
			return map[string]*source.MetricResult{
				registration.QuerySLORouterTTFTBuckets: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-b", "le": "1"}, Value: 1},
				}},
			}, nil
		},
	}
	assert.Nil(t, NewReplicaMetricsCollector(mockSource, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns"))
}
