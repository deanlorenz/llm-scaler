package collector

import (
	"context"
	"math"
	"sort"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// extractTTFTInputs reads the series the TTFT model learns from
// (registration/ttft_inputs.go) into the instances the model-scoped queries
// already found. They are supplementary like the output-length histogram:
// they attach to an instance and never define one, and a failed query leaves
// each one absent. Recorded, and read by no analyzer yet.
func (c *ReplicaMetricsCollector) extractTTFTInputs(ctx context.Context, modelID, namespace string,
	results map[string]*source.MetricResult, podData map[string]*podMetricData) {
	for name, field := range map[string]func(*podMetricData) *map[float64]float64{
		registration.QueryPromptTokensBuckets: func(d *podMetricData) *map[float64]float64 { return &d.promptTokensBuckets },
		registration.QueryTTFTBuckets:         func(d *podMetricData) *map[float64]float64 { return &d.ttftBuckets },
		registration.QueryQueueTimeBuckets:    func(d *podMetricData) *map[float64]float64 { return &d.queueTimeBuckets },
		registration.QueryTPOTBuckets:         func(d *podMetricData) *map[float64]float64 { return &d.tpotBuckets },
	} {
		c.extractBuckets(ctx, modelID, namespace, results[name], podData, field)
	}
	c.extractMean(ctx, modelID, namespace, results[registration.QueryAvgQueueTime], podData,
		func(d *podMetricData) (*float64, *bool) { return &d.avgQueueTime, &d.avgQueueTimeKnown })
	c.extractMean(ctx, modelID, namespace, results[registration.QueryAvgPrefillTime], podData,
		func(d *podMetricData) (*float64, *bool) { return &d.avgPrefillTime, &d.avgPrefillTimeKnown })
	c.extractMean(ctx, modelID, namespace, results[registration.QueryAvgKVTransferTime], podData,
		func(d *podMetricData) (*float64, *bool) { return &d.avgKVTransferTime, &d.avgKVTransferTimeKnown })
}

// extractMean reads one per-instance mean into the value and presence bit
// field returns, for the instances already found. A finite, non-negative
// value is a reading, a zero included (no wait). A NaN is the 0/0 of a window
// with no request, and is not one; nor is an infinity, a negative, another
// model's series or a failed query. Sticky across series under one key, like
// ITL: once a series is read, a later unusable one leaves it.
func (c *ReplicaMetricsCollector) extractMean(ctx context.Context, modelID, namespace string,
	result *source.MetricResult, podData map[string]*podMetricData, field func(*podMetricData) (*float64, *bool)) {
	if result == nil || result.HasError() {
		return
	}
	for _, value := range result.Values {
		if value.Labels[seriesModelLabel] != modelID {
			continue
		}
		if !value.Finite() || !(value.Value >= 0) {
			continue
		}
		instanceKey, _, _ := c.buildInstanceKey(ctx, namespace, value.Labels)
		if instanceKey == "" || podData[instanceKey] == nil {
			continue
		}
		v, known := field(podData[instanceKey])
		*v, *known = value.Value, true
	}
}

// lengthBuckets is a histogram read by extractBuckets as the row carries it,
// sorted by bound; nil stays nil.
func lengthBuckets(byBound map[float64]float64) []domain.LengthBucket {
	if byBound == nil {
		return nil
	}
	out := make([]domain.LengthBucket, 0, len(byBound))
	for le, rate := range byBound {
		out = append(out, domain.LengthBucket{UpperBound: le, Rate: rate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpperBound < out[j].UpperBound })
	return out
}

// latencyBuckets is lengthBuckets for a histogram of seconds.
func latencyBuckets(byBound map[float64]float64) []domain.LatencyBucket {
	if byBound == nil {
		return nil
	}
	out := make([]domain.LatencyBucket, 0, len(byBound))
	for le, rate := range byBound {
		out = append(out, domain.LatencyBucket{UpperBound: le, Rate: rate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpperBound < out[j].UpperBound })
	return out
}

// mergeLengthBuckets is one pod's histogram from its ranks': each bound's
// rate summed, since a request any rank served is one the pod served. Nil
// unless every rank had a histogram: a rank without one would read as a
// rank that served nothing.
func mergeLengthBuckets(group []domain.ReplicaMetrics, field func(domain.ReplicaMetrics) []domain.LengthBucket) []domain.LengthBucket {
	byBound := map[float64]float64{}
	for _, m := range group {
		bs := field(m)
		if bs == nil {
			return nil
		}
		for _, b := range bs {
			byBound[b.UpperBound] += b.Rate
		}
	}
	return lengthBuckets(byBound)
}

// mergeLatencyBuckets is mergeLengthBuckets for a histogram of seconds.
func mergeLatencyBuckets(group []domain.ReplicaMetrics, field func(domain.ReplicaMetrics) []domain.LatencyBucket) []domain.LatencyBucket {
	byBound := map[float64]float64{}
	for _, m := range group {
		bs := field(m)
		if bs == nil {
			return nil
		}
		for _, b := range bs {
			byBound[b.UpperBound] += b.Rate
		}
	}
	return latencyBuckets(byBound)
}

// mergeKnownMean is one pod's mean from its ranks', weighted by request rate
// as service time is (weightedByRequestRate), and known only when every rank
// read one. Unknown, the mean is 0, so the value never carries a reading the
// bit denies.
func mergeKnownMean(group []domain.ReplicaMetrics, field func(domain.ReplicaMetrics) (float64, bool)) (float64, bool) {
	for _, m := range group {
		if _, known := field(m); !known {
			return 0, false
		}
	}
	v := weightedByRequestRate(group, func(m domain.ReplicaMetrics) float64 {
		x, _ := field(m)
		return x
	})
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
