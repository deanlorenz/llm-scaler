package collector

import (
	"context"
	"errors"
	"math"
	"sort"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
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

// TestCollectRouterLatency_FailuresReadAsAbsent pins that a failed query never
// reads as a latency: an error from the source, and a result carrying its own
// error, both leave the SLO unjudged rather than met or missed.
func TestCollectRouterLatency_FailuresReadAsAbsent(t *testing.T) {
	failing := &mockMetricsSource{
		refreshFunc: func(_ context.Context, _ source.RefreshSpec) (map[string]*source.MetricResult, error) {
			return nil, errors.New("prometheus unreachable")
		},
	}
	assert.Nil(t, NewReplicaMetricsCollector(failing, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns"))

	erroring := &mockMetricsSource{
		refreshFunc: func(_ context.Context, _ source.RefreshSpec) (map[string]*source.MetricResult, error) {
			return map[string]*source.MetricResult{
				registration.QuerySLORouterTTFTBuckets: {Error: errors.New("bad query"), Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "+Inf"}, Value: 1},
				}},
			}, nil
		},
	}
	assert.Nil(t, NewReplicaMetricsCollector(erroring, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns"))
}

// TestCollectRouterLatency_HalfATPOTPairIsNone pins that the TPOT mean is read
// from both halves or not at all. With the _sum query failed and _count back,
// summing what came back published a mean of 0: a met TPOT that neither alert
// could see past.
func TestCollectRouterLatency_HalfATPOTPairIsNone(t *testing.T) {
	ttft := &source.MetricResult{Values: []source.MetricValue{
		{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "+Inf"}, Value: 1},
	}}
	good := func(v float64) *source.MetricResult {
		return &source.MetricResult{Values: []source.MetricValue{
			{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: v},
		}}
	}
	failed := &source.MetricResult{Error: errors.New("timeout")}
	for name, tc := range map[string]struct{ sum, count *source.MetricResult }{
		"sum failed":    {failed, good(1)},
		"count failed":  {good(0.08), failed},
		"sum missing":   {nil, good(1)},
		"sum zero":      {good(0), good(1)},
		"count missing": {good(0.08), nil},
	} {
		t.Run(name, func(t *testing.T) {
			results := map[string]*source.MetricResult{registration.QuerySLORouterTTFTBuckets: ttft}
			if tc.sum != nil {
				results[registration.QuerySLORouterTPOTSum] = tc.sum
			}
			if tc.count != nil {
				results[registration.QuerySLORouterTPOTCount] = tc.count
			}
			src := &mockMetricsSource{refreshFunc: func(context.Context, source.RefreshSpec) (map[string]*source.MetricResult, error) {
				return results, nil
			}}
			got := NewReplicaMetricsCollector(src, nil, nil, nil, nil).CollectRouterLatency(context.Background(), "model-a", "ns")
			require.NotNil(t, got, "TTFT is still read")
			assert.Zero(t, got.TPOTSumRate)
			assert.Zero(t, got.TPOTCountRate, "no TPOT requests means TPOT unjudged, not met")
		})
	}
}

// TestCollectRouterLatency_SkipsUnreadableBoundsAndKeepsTPOTAlone pins that a
// bucket whose bound does not parse is dropped rather than read as zero, and
// that a router exporting TPOT and no TTFT still yields a reading.
func TestCollectRouterLatency_SkipsUnreadableBoundsAndKeepsTPOTAlone(t *testing.T) {
	mockSource := &mockMetricsSource{
		refreshFunc: func(_ context.Context, _ source.RefreshSpec) (map[string]*source.MetricResult, error) {
			return map[string]*source.MetricResult{
				registration.QuerySLORouterTTFTBuckets: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "fast"}, Value: 1},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a", "le": "NaN"}, Value: 1},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: 1},
				}},
				registration.QuerySLORouterTPOTSum: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: 0.08},
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: math.Inf(1)},
				}},
				registration.QuerySLORouterTPOTCount: {Values: []source.MetricValue{
					{Labels: map[string]string{seriesTargetModelLabel: "model-a"}, Value: 1},
				}},
			}, nil
		},
	}
	got := NewReplicaMetricsCollector(mockSource, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns")
	require.NotNil(t, got)
	assert.Empty(t, got.TTFTBuckets)
	assert.Equal(t, 0.08, got.TPOTSumRate)
	assert.Equal(t, 1.0, got.TPOTCountRate)
}

// TestCollectRouterLatency_CountsAFailedQuery pins that a failed query is
// counted, so a Prometheus outage is not read as a router that is not scraped.
// The failure is carried the way the real Prometheus source carries it: on
// each query's result, with Refresh itself returning no error. A version that
// counted only a Refresh error passed a mock that failed Refresh, and counted
// nothing in production.
func TestCollectRouterLatency_CountsAFailedQuery(t *testing.T) {
	registry := promclient.NewRegistry()
	require.NoError(t, metrics.InitMetrics(registry))
	failing := &mockMetricsSource{
		refreshFunc: func(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
			out := map[string]*source.MetricResult{}
			for _, q := range spec.Queries {
				out[q] = &source.MetricResult{QueryName: q, Error: errors.New("prometheus unreachable")}
			}
			return out, nil
		},
	}
	assert.Nil(t, NewReplicaMetricsCollector(failing, nil, nil, nil, nil).
		CollectRouterLatency(context.Background(), "model-a", "ns"))

	mfs, err := registry.Gather()
	require.NoError(t, err)
	var counted float64
	for _, mf := range mfs {
		if mf.GetName() != constants.WVAMetricsCollectionErrorsTotal {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == constants.LabelQueryType && l.GetValue() == constants.QueryTypeRouterLatency {
					counted += m.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Equal(t, 3.0, counted, "one per failed query")
}
