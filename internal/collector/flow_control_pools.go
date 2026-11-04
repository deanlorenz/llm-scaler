package collector

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/prometheus"
)

// poolLabel is the label the router's flow-control series name their
// InferencePool by.
const poolLabel = "inference_pool"

// CollectFlowControlPools collects the flow-control layer of each
// InferencePool that serves modelID alone in namespace: the wait histogram,
// the saturation by stage and the outcome rates, over
// registration.FlowControlWindow. Recorded for the TTFT model and read by
// nothing yet.
//
// A pool is joined to the model when the flow-control queue gauge names the
// pool with this model and no other (registration.QueryFlowControlPoolModels).
// A pool shared by several models is joined to none: its series carry no
// model, and a wait or saturation charged to the wrong model is worse than
// none. Returns nil when no pool is joined, which is also what flow control
// switched off, or a failed query, reads as: absent, never zeros.
//
// The result is in pool-name order, the names dropped: a recording holds
// numbers only.
func (c *ReplicaMetricsCollector) CollectFlowControlPools(
	ctx context.Context,
	modelID, namespace string,
) []domain.FlowControlPool {
	logger := ctrl.LoggerFrom(ctx)
	queries := []string{
		registration.QueryFlowControlPoolModels,
		registration.QueryFlowControlWaitBuckets,
		registration.QueryFlowControlPoolSaturation,
		registration.QueryFlowControlOutcomes,
	}
	results, err := c.refreshShared(ctx, queries, map[string]string{source.ParamNamespace: namespace})
	if err != nil {
		reason := prometheus.CategorizePrometheusError(err)
		metrics.IncMetricsCollectionErrors(constants.QueryTypeFlowControlPools, reason)
		logger.V(logging.DEBUG).Info("Flow-control pool series unavailable",
			"modelID", modelID, "namespace", namespace, "reason", reason, "error", err)
		return nil
	}
	for _, q := range queries {
		if r := results[q]; r != nil && r.HasError() {
			reason := prometheus.CategorizePrometheusError(r.Error)
			metrics.IncMetricsCollectionErrors(constants.QueryTypeFlowControlPools, reason)
			logger.V(logging.DEBUG).Info("Flow-control pool query failed",
				"modelID", modelID, "namespace", namespace, "query", q, "reason", reason)
		}
	}

	pools := poolsServing(results[registration.QueryFlowControlPoolModels], modelID)
	if len(pools) == 0 {
		return nil
	}
	byPool := make(map[string]*domain.FlowControlPool, len(pools))
	for _, p := range pools {
		byPool[p] = &domain.FlowControlPool{WindowSeconds: registration.FlowControlWindowSeconds}
	}

	waits := map[string]map[float64]float64{}
	for _, v := range finiteValues(results[registration.QueryFlowControlWaitBuckets]) {
		pool := v.Labels[poolLabel]
		if byPool[pool] == nil {
			continue
		}
		le, err := strconv.ParseFloat(v.Labels["le"], 64)
		if err != nil || math.IsNaN(le) || v.Value < 0 {
			continue
		}
		if waits[pool] == nil {
			waits[pool] = map[float64]float64{}
		}
		waits[pool][le] += v.Value
	}
	for pool, byBound := range waits {
		byPool[pool].WaitBuckets = latencyBuckets(byBound)
	}

	for _, v := range finiteValues(results[registration.QueryFlowControlPoolSaturation]) {
		p := byPool[v.Labels[poolLabel]]
		if p == nil {
			continue
		}
		value, known := &p.Saturation, &p.SaturationKnown
		switch strings.ToLower(v.Labels["stage"]) {
		case "prefill":
			value, known = &p.PrefillSaturation, &p.PrefillSaturationKnown
		case "decode":
			value, known = &p.DecodeSaturation, &p.DecodeSaturationKnown
		}
		if !*known || v.Value > *value {
			*value = v.Value
		}
		*known = true
	}

	for _, v := range finiteValues(results[registration.QueryFlowControlOutcomes]) {
		p := byPool[v.Labels[poolLabel]]
		if p == nil || v.Value < 0 {
			continue
		}
		outcome := strings.ToLower(v.Labels["outcome"])
		switch {
		case strings.HasPrefix(outcome, "dispatch"):
			p.DispatchedRate += v.Value
		case strings.HasPrefix(outcome, "reject"):
			p.RejectedRate += v.Value
		case strings.HasPrefix(outcome, "evict"):
			p.EvictedRate += v.Value
		default:
			p.OtherOutcomeRate += v.Value
		}
		p.OutcomesKnown = true
	}

	out := make([]domain.FlowControlPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, *byPool[p])
	}
	return out
}

// poolsServing is the pools the queue gauge names with modelID and no other
// model, sorted. A series resolves to a model as the scheduler-queue ones do
// (eppSeriesModel); one that resolves to none names no model and joins
// nothing. A failed or missing result serves none.
func poolsServing(result *source.MetricResult, modelID string) []string {
	if result == nil || result.HasError() {
		return nil
	}
	models := map[string]map[string]bool{}
	for _, v := range result.Values {
		pool, model := v.Labels[poolLabel], eppSeriesModel(v.Labels)
		if pool == "" || model == "" {
			continue
		}
		if models[pool] == nil {
			models[pool] = map[string]bool{}
		}
		models[pool][model] = true
	}
	var out []string
	for pool, ms := range models {
		if len(ms) == 1 && ms[modelID] {
			out = append(out, pool)
		}
	}
	sort.Strings(out)
	return out
}

// finiteValues is a result's finite samples; none for a nil or failed one.
func finiteValues(result *source.MetricResult) []source.MetricValue {
	if result == nil || result.HasError() {
		return nil
	}
	out := make([]source.MetricValue, 0, len(result.Values))
	for _, v := range result.Values {
		if v.Finite() {
			out = append(out, v)
		}
	}
	return out
}
