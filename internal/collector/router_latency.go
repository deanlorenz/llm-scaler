package collector

import (
	"context"
	"math"
	"strconv"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/prometheus"
)

// CollectRouterLatency collects one model's request latency as the router
// measured it: the TTFT histogram and the streaming TPOT mean, over
// registration.SLORouterWindow. Returns nil (not an error) when the router
// exports nothing for the model or the queries fail; the caller reports that
// as an unjudged SLO rather than a met or a missed one.
func (c *ReplicaMetricsCollector) CollectRouterLatency(
	ctx context.Context,
	modelID, namespace string,
) *domain.RouterLatency {
	logger := ctrl.LoggerFrom(ctx)
	queries := []string{
		registration.QuerySLORouterTTFTBuckets,
		registration.QuerySLORouterTPOTSum,
		registration.QuerySLORouterTPOTCount,
	}
	results, err := c.refreshShared(ctx, queries, map[string]string{source.ParamNamespace: namespace})
	if err != nil {
		// Counted, not only logged: an absent latency series reads the same
		// whether the router is unscraped or Prometheus is down, and the
		// collection-error counter is what tells the two apart.
		reason := prometheus.CategorizePrometheusError(err)
		metrics.IncMetricsCollectionErrors(constants.QueryTypeRouterLatency, reason)
		logger.V(logging.DEBUG).Info("Router latency unavailable",
			"modelID", modelID, "namespace", namespace, "reason", reason, "error", err)
		return nil
	}

	// The Prometheus source reports a failed query on its result, not as an
	// error from the refresh, so each result is checked as the arrival-rate
	// path checks its own.
	for _, q := range queries {
		if r := results[q]; r != nil && r.HasError() {
			reason := prometheus.CategorizePrometheusError(r.Error)
			metrics.IncMetricsCollectionErrors(constants.QueryTypeRouterLatency, reason)
			logger.V(logging.DEBUG).Info("Router latency query failed",
				"modelID", modelID, "namespace", namespace, "query", q, "reason", reason)
		}
	}

	out := &domain.RouterLatency{WindowSeconds: registration.SLORouterWindowSeconds}
	byBound := make(map[float64]float64)
	for _, v := range modelValues(results[registration.QuerySLORouterTTFTBuckets], modelID) {
		le, err := strconv.ParseFloat(v.Labels["le"], 64)
		if err != nil || math.IsNaN(le) {
			continue
		}
		// One series per (model_name, target_model_name, le); a model reached
		// under both labels contributes to the same bucket.
		byBound[le] += v.Value
	}
	for le, rate := range byBound {
		out.TTFTBuckets = append(out.TTFTBuckets, domain.LatencyBucket{UpperBound: le, Rate: rate})
	}
	for _, v := range modelValues(results[registration.QuerySLORouterTPOTSum], modelID) {
		out.TPOTSumRate += v.Value
	}
	for _, v := range modelValues(results[registration.QuerySLORouterTPOTCount], modelID) {
		out.TPOTCountRate += v.Value
	}
	if len(out.TTFTBuckets) == 0 && out.TPOTCountRate == 0 {
		return nil
	}
	return out
}

// modelValues returns the finite, non-negative values of result that belong to
// modelID, resolving each series' model the way the scheduler-queue metrics are
// resolved (eppSeriesModel). A nil or failed result has none.
func modelValues(result *source.MetricResult, modelID string) []source.MetricValue {
	if result == nil || result.HasError() {
		return nil
	}
	var out []source.MetricValue
	for _, v := range result.Values {
		if eppSeriesModel(v.Labels) != modelID {
			continue
		}
		if math.IsNaN(v.Value) || math.IsInf(v.Value, 0) || v.Value < 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}
