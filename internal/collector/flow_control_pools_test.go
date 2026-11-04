package collector

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// poolSeries is one series of pool with extra labels.
func poolSeries(pool string, value float64, kv ...string) source.MetricValue {
	labels := map[string]string{poolLabel: pool}
	for i := 0; i+1 < len(kv); i += 2 {
		labels[kv[i]] = kv[i+1]
	}
	return source.MetricValue{Labels: labels, Value: value}
}

// collectPools runs CollectFlowControlPools for model-a against results.
func collectPools(t *testing.T, results map[string]*source.MetricResult, err error) ([]domain.FlowControlPool, map[string]string) {
	t.Helper()
	var params map[string]string
	src := &mockMetricsSource{refreshFunc: func(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
		params = spec.Params
		return results, err
	}}
	return NewReplicaMetricsCollector(src, nil, nil, nil, nil).CollectFlowControlPools(context.Background(), "model-a", "ns"), params
}

// The pools joined to a model are those the queue gauge names with that model
// alone; each figure is read per pool, and the result is in pool-name order
// with no names.
func TestCollectFlowControlPools_JoinsAPoolServingTheModelAlone(t *testing.T) {
	results := map[string]*source.MetricResult{
		registration.QueryFlowControlPoolModels: {Values: []source.MetricValue{
			poolSeries("pool-b", 0, seriesTargetModelLabel, "model-a"),
			poolSeries("pool-a", 3, seriesModelLabel, "model-a"),
			// Shared: joined to neither model.
			poolSeries("pool-shared", 0, seriesTargetModelLabel, "model-a"),
			poolSeries("pool-shared", 0, seriesTargetModelLabel, "model-b"),
			// Another model's pool.
			poolSeries("pool-other", 0, seriesTargetModelLabel, "model-b"),
			// A series naming no model joins nothing.
			poolSeries("pool-a", 0),
		}},
		registration.QueryFlowControlWaitBuckets: {Values: []source.MetricValue{
			poolSeries("pool-a", 4, "le", "+Inf"),
			poolSeries("pool-a", 1, "le", "0.1"),
			poolSeries("pool-a", math.NaN(), "le", "1"),
			poolSeries("pool-a", 1, "le", "soon"),
			poolSeries("pool-shared", 99, "le", "+Inf"),
		}},
		registration.QueryFlowControlPoolSaturation: {Values: []source.MetricValue{
			poolSeries("pool-a", 0.4, "stage", "prefill"),
			poolSeries("pool-a", 0.9, "stage", "decode"),
			poolSeries("pool-b", 0.3),
			poolSeries("pool-b", 0.5, "stage", "other"),
			poolSeries("pool-b", math.NaN(), "stage", "decode"),
		}},
		registration.QueryFlowControlOutcomes: {Values: []source.MetricValue{
			poolSeries("pool-a", 5, "outcome", "Dispatched"),
			poolSeries("pool-a", 0.5, "outcome", "RejectedCapacity"),
			poolSeries("pool-a", 0.25, "outcome", "RejectedOther"),
			poolSeries("pool-a", 0.1, "outcome", "EvictedTTL"),
			poolSeries("pool-a", 0.05, "outcome", "Something"),
			poolSeries("pool-shared", 7, "outcome", "Dispatched"),
		}},
	}
	got, params := collectPools(t, results, nil)
	assert.Equal(t, map[string]string{source.ParamNamespace: "ns"}, params)
	require.Len(t, got, 2, "pool-a and pool-b serve model-a alone")

	a, b := got[0], got[1]
	assert.Equal(t, registration.FlowControlWindowSeconds, a.WindowSeconds)
	assert.Equal(t, []domain.LatencyBucket{{UpperBound: 0.1, Rate: 1}, {UpperBound: math.Inf(1), Rate: 4}}, a.WaitBuckets)
	assert.Equal(t, domain.FlowControlPool{
		WindowSeconds:          registration.FlowControlWindowSeconds,
		WaitBuckets:            a.WaitBuckets,
		PrefillSaturation:      0.4,
		PrefillSaturationKnown: true,
		DecodeSaturation:       0.9,
		DecodeSaturationKnown:  true,
		DispatchedRate:         5,
		RejectedRate:           0.75,
		EvictedRate:            0.1,
		OtherOutcomeRate:       0.05,
		OutcomesKnown:          true,
	}, a)
	// pool-b: no wait series and no outcome series are absent, not zeros;
	// a stage-less and an unknown stage take the higher; a NaN stage is not
	// read.
	assert.Equal(t, domain.FlowControlPool{
		WindowSeconds:   registration.FlowControlWindowSeconds,
		Saturation:      0.5,
		SaturationKnown: true,
	}, b)
}

// No pool joined is nil: flow control off (no gauge), a failed query, a
// source error, and a pool shared with another model all read as absent.
func TestCollectFlowControlPools_AbsentIsNil(t *testing.T) {
	shared := &source.MetricResult{Values: []source.MetricValue{
		poolSeries("p", 0, seriesTargetModelLabel, "model-a"),
		poolSeries("p", 0, seriesTargetModelLabel, "model-b"),
	}}
	served := &source.MetricResult{Values: []source.MetricValue{poolSeries("p", 0, seriesTargetModelLabel, "model-a")}}
	for name, tc := range map[string]struct {
		results map[string]*source.MetricResult
		err     error
	}{
		"flow control off":  {map[string]*source.MetricResult{}, nil},
		"shared pool":       {map[string]*source.MetricResult{registration.QueryFlowControlPoolModels: shared}, nil},
		"failed join query": {map[string]*source.MetricResult{registration.QueryFlowControlPoolModels: {Error: errors.New("timeout"), Values: served.Values}}, nil},
		"source error":      {nil, errors.New("prometheus unreachable")},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := collectPools(t, tc.results, tc.err)
			assert.Nil(t, got)
		})
	}

	// Joined, with every per-pool query failed: the pool is there and every
	// figure is absent.
	failed := &source.MetricResult{Error: errors.New("timeout"), Values: []source.MetricValue{
		poolSeries("p", 1, "le", "+Inf", "stage", "decode", "outcome", "Dispatched")}}
	got, _ := collectPools(t, map[string]*source.MetricResult{
		registration.QueryFlowControlPoolModels:     served,
		registration.QueryFlowControlWaitBuckets:    failed,
		registration.QueryFlowControlPoolSaturation: failed,
		registration.QueryFlowControlOutcomes:       failed,
	}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, domain.FlowControlPool{WindowSeconds: registration.FlowControlWindowSeconds}, got[0])
}
