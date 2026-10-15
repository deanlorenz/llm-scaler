package collector

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
	llmdVariantAutoscalingV1alpha1 "github.com/llm-d/llm-d-workload-variant-autoscaler/internal/variant"
)

// The prefill time and engine queue time, from the query result to
// domain.ReplicaMetrics. They differ in one rule: a zero queue wait is a
// reading (an idle scheduler), a zero prefill time is not.
func TestCollectReplicaMetrics_PrefillAndQueueTime(t *testing.T) {
	podLabels := map[string]string{seriesModelLabel: "test-model", "pod": "pod-abc", "instance": "10.0.0.1:8000"}
	collect := func(t *testing.T, vals map[string]float64) (prefill, queue float64) {
		t.Helper()
		if err := metrics.InitMetrics(prometheus.NewRegistry()); err != nil {
			t.Fatalf("InitMetrics: %v", err)
		}
		scheme := runtime.NewScheme()
		if err := llmdVariantAutoscalingV1alpha1.AddToScheme(scheme); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
		ts := time.Now()
		results := map[string]*source.MetricResult{
			"kv_cache_usage": {Values: []source.MetricValue{{Labels: podLabels, Value: 0.5, Timestamp: ts}}},
		}
		for name, v := range vals {
			results[name] = &source.MetricResult{Values: []source.MetricValue{{Labels: podLabels, Value: v, Timestamp: ts}}}
		}
		c := NewReplicaMetricsCollector(&mockMetricsSource{
			refreshFunc: func(context.Context, source.RefreshSpec) (map[string]*source.MetricResult, error) {
				return results, nil
			},
		}, fake.NewClientBuilder().WithScheme(scheme).Build(), nil, nil, scalerLocator(map[string]string{"pod-abc": "va-1"}))
		// A namespace where the slo analyzer runs: elsewhere these are not fetched.
		c.SetSLOInputNamespaces(map[string]bool{"test-ns": true})
		got, err := c.CollectReplicaMetrics(context.Background(), "test-model", "test-ns",
			map[string]scaletarget.ScaleTargetAccessor{}, map[string]*llmdVariantAutoscalingV1alpha1.VariantAutoscaling{}, nil)
		if err != nil || len(got) != 1 {
			t.Fatalf("CollectReplicaMetrics: %d rows, %v", len(got), err)
		}
		return got[0].AvgPrefillTime, got[0].AvgQueueTime
	}

	if p, q := collect(t, map[string]float64{registration.QueryAvgPrefillTime: 0.8, registration.QueryAvgQueueTime: 0.3}); p != 0.8 || q != 0.3 {
		t.Errorf("each reading on its own field: got (%v, %v), want (0.8, 0.3)", p, q)
	}
	if _, q := collect(t, map[string]float64{registration.QueryAvgPrefillTime: 0.8, registration.QueryAvgQueueTime: 0}); q != 0 {
		t.Errorf("an idle queue reads %v, want 0", q)
	}
	// A zero queue reading is kept and a zero prefill one is not: told apart
	// through a field that starts non-zero, by seeding it with a real reading
	// first is not possible here, so the rule is pinned on the absent side:
	// the prefill reading must not become a 0 that downstream reads as data.
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if p, _ := collect(t, map[string]float64{registration.QueryAvgPrefillTime: bad}); p != 0 {
			t.Errorf("prefill reading %v survived as %v", bad, p)
		}
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, q := collect(t, map[string]float64{registration.QueryAvgQueueTime: bad}); q != 0 {
			t.Errorf("queue reading %v survived as %v", bad, q)
		}
	}
}
