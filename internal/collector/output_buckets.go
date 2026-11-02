package collector

import (
	"context"
	"math"
	"strconv"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// extractOutputBuckets reads the output-length histogram into the instances
// the model-scoped queries already found, one rate per bucket bound, and skips
// the rest, like the other supplementary series (extractSequenceCap).
//
// The query is agnostic, which filterResultsToModel does not narrow, so its
// series are matched to the model here; one carrying no model_name is kept,
// as the instance key already ties it to a seen pod. A series whose bound
// does not parse, or whose rate is not a finite non-negative number, is
// dropped; a histogram missing a bucket is then refused as a whole by
// shape.SquaredMeanLowerBound, never read with a hole in it.
func (c *ReplicaMetricsCollector) extractOutputBuckets(ctx context.Context, modelID, namespace string,
	results map[string]*source.MetricResult, podData map[string]*podMetricData) {
	result := results[registration.QueryOutputTokensBuckets]
	if result == nil || result.HasError() {
		return
	}
	for _, value := range result.Values {
		if m, ok := value.Labels[seriesModelLabel]; ok && m != "" && m != modelID {
			continue
		}
		le, err := strconv.ParseFloat(value.Labels["le"], 64)
		if err != nil || math.IsNaN(le) {
			continue
		}
		if !value.Finite() || !(value.Value >= 0) || math.IsInf(value.Value, 0) {
			continue
		}
		instanceKey, _, _ := c.buildInstanceKey(ctx, namespace, value.Labels)
		if instanceKey == "" || podData[instanceKey] == nil {
			continue
		}
		d := podData[instanceKey]
		if d.outputTokensBuckets == nil {
			d.outputTokensBuckets = make(map[float64]float64)
		}
		// Two series with one bound (a model reached under two label
		// sets) are one bucket.
		d.outputTokensBuckets[le] += value.Value
	}
}

// outputTokensSquared reduces an instance's histogram and its mean output
// length to the lower bound on E[O^2] (shape.SquaredMeanLowerBound), and
// whether it is a reading.
func outputTokensSquared(buckets map[float64]float64, mean float64) (float64, bool) {
	if len(buckets) == 0 {
		return 0, false
	}
	bs := make([]shape.Bucket, 0, len(buckets))
	for le, rate := range buckets {
		bs = append(bs, shape.Bucket{UpperBound: le, Rate: rate})
	}
	return shape.SquaredMeanLowerBound(bs, mean)
}
